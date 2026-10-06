package kernelbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// This file is the read half of the seam.
//
// A read is a smaller operation than a write and a harder one to get honest,
// because the obvious implementation is "call os.ReadFile and return the bytes".
// That produces content nobody can account for: no event says a read happened, no
// evidence records what was read, and an unreadable file comes back as the same
// empty string as an empty one. Both failure modes end with the runtime
// overwriting a file it never successfully looked at.
//
// So a read here is a real OBSERVE execution over the file.read capability, under
// an explicit grant naming exactly the destinations, with an independent verifier
// that re-reads each destination and requires the evidence to agree with what is
// on disk.
//
// # What the evidence does and does not carry
//
// The kernel's evidence vocabulary records OBSERVATIONS — a kind, a target, a
// byte count — and not payloads. That is a deliberate property of the kernel, and
// this slice does not change it. It has one consequence a reader must be told
// about plainly:
//
//	Verification compares what the two independent reads DISAGREE about —
//	existence, and byte count — and not the full byte sequences.
//
// Two reads of the same path that return the same length but different bytes
// would pass. That requires the file to change between the capability's read and
// the verifier's re-read, with the replacement happening to be exactly the same
// length. It is a real TOCTOU window, not a closed one. Closing it entirely needs
// content-bearing evidence in the kernel, which is a kernel change and therefore
// out of scope for this slice; docs/architecture/STRANGLER_MIGRATION.md records
// it as an open item rather than pretending the check is byte-exact.
//
// The bytes this seam returns are the VERIFIER's re-read, not the capability's.
// The seam returns the reading it is willing to vouch for, and cross-checks it
// against the capability's recorded byte count before doing so.

// A read shares MaxObservedTargets with an existence observation on purpose: both
// are asked over the same destination-set shape, so they carry the same bound and
// the same refusal rather than two limits that drift apart.

// MaxReadBytes bounds one file's content per read.
//
// It exists because the destination of a read here is named by a model, and a
// model can name a multi-gigabyte file. The bound is declared on the capability
// rather than checked here, so exceeding it is refused in the kernel's own
// vocabulary: the capability records a truncated byte count, the verifier's
// independent re-read measures the full file, the two disagree, and the execution
// does not reach PROVEN.
//
// That is the important part. A bound that silently returned a prefix would hand
// the caller a diff computed from a truncated baseline and report the read as
// truthful. Failing closed is what makes a bound safe to have.
const MaxReadBytes = 8 << 20

// readGrantID names the authorization every read runs under. It is a constant
// because read authority in this deployment is fixed by policy — one filesystem
// capability over a named workspace — and exposing it as a parameter would let a
// caller widen it.
const readGrantID = "kernelbridge.read"

// Read is the terminal truth of one read execution over a destination set.
//
// The three fields that answer a caller's question are kept apart on purpose:
//
//   - Found is true only for a destination the kernel observed present and that
//     adjudication accepted;
//   - Absent is true only for a destination the kernel observed NOT to exist;
//   - Bytes is the size the verifier measured on disk.
//
// A destination that was neither found nor absent was not established, and
// Content returns "" for it. That third state is the point: it is the difference
// between "the file is empty" and "the runtime could not tell".
type Read struct {
	Observation

	// bytesOnDisk maps each canonical destination onto the length an independent
	// verifier measured. It is populated only for destinations the verifier
	// actually re-read, so its absence is itself a fact: the check did not run
	// for that destination.
	bytesOnDisk map[string]int

	// content maps each canonical destination onto the bytes the verifier read.
	// Content is served from here rather than from the capability, so what a
	// caller receives is the reading that was independently re-checked.
	content map[string]string
}

// Proven reports whether adjudication reached PROVEN. It is the only thing that
// authorizes a content claim.
//
// It is a method rather than a field for the same reason it is on Observation: a
// field would be a second source of truth a hand-built literal could contradict.
func (r Read) Proven() bool { return r.Observation.Proven() }

// Found reports whether target was proven present by a PROVEN execution.
func (r Read) Found(target string) bool {
	p, ok := r.Presence[canonical(target)]
	return ok && r.Proven() && p.Observed && p.Present
}

// Absent reports whether target was proven NOT to exist by a PROVEN execution.
//
// This is a real observed fact about the filesystem, never a failure disguised as
// success and never an empty read. A caller that treats absence as content would
// overwrite a file it never successfully opened.
func (r Read) Absent(target string) bool {
	p, ok := r.Presence[canonical(target)]
	return ok && r.Proven() && p.Observed && !p.Present
}

// Content returns the bytes an independent verifier read for target.
//
// It returns "" when the destination was not established as present, or when no
// verification ran over it. The two cases are distinguishable through Found, and
// the distinction matters: a caller that renders "" must be able to say whether
// the file is empty or unreadable.
func (r Read) Content(target string) string {
	if !r.Found(target) {
		return ""
	}
	return r.content[canonical(target)]
}

// BytesOnDisk returns the length the verifier measured for target, and whether it
// measured one at all. A destination no verifier looked at returns (0, false) —
// again distinguishable from a genuinely empty file, which returns (0, true).
func (r Read) BytesOnDisk(target string) (int, bool) {
	if !r.Found(target) {
		return 0, false
	}
	n, ok := r.bytesOnDisk[canonical(target)]
	return n, ok
}

// ReadFiles runs one read execution and returns its terminal truth.
//
// The contract is OBSERVE with verification required, so mutation is forbidden by
// construction: a read that wrote to the workspace would be refused at admission
// rather than discovered afterwards.
func ReadFiles(ctx context.Context, root string, targets []string) Read {
	read := Read{
		bytesOnDisk: map[string]int{},
		content:     map[string]string{},
	}
	if len(targets) == 0 {
		read.Observation = refused(kernel.OutcomeUnsubstantiated, kernel.FailureInvalidSpec,
			"read requires at least one target; an empty target set proves nothing")
		return read
	}

	canonicalTargets := canonicalise(targets)
	if len(canonicalTargets) == 0 {
		read.Observation = refused(kernel.OutcomeUnsubstantiated, kernel.FailureInvalidSpec,
			"no read target named a file")
		return read
	}
	if len(canonicalTargets) > MaxObservedTargets {
		read.Observation = refused(kernel.OutcomeBudgetExhausted, kernel.FailureBudgetExhausted,
			fmt.Sprintf("read target set has %d entries, above the declared bound of %d",
				len(canonicalTargets), MaxObservedTargets))
		return read
	}

	executionID := readExecutionIDFor(root, canonicalTargets)

	caps, err := filesystem.New(root, filesystem.WithMaxReadBytes(MaxReadBytes))
	if err != nil {
		read.Observation = refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability surface unavailable: %v", err))
		return read
	}
	registry, err := caps.Registry()
	if err != nil {
		read.Observation = refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability registry unavailable: %v", err))
		return read
	}

	// The verifier records what it measured, and the seam returns that. It is not
	// the capability: replaying file.read would only re-run the implementation
	// whose report is being judged.
	measured := &readMeasurement{root: caps.Root(), bytes: map[string]int{}, content: map[string]string{}}

	engine, err := kernel.NewEngine(registry, kernel.WithVerifier(kernel.VerifierFunc(measured.verify)))
	if err != nil {
		read.Observation = refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("kernel engine unavailable: %v", err))
		return read
	}
	grant, err := kernel.NewGrant(readGrantID, []kernel.CapabilityID{kernel.FileRead}, canonicalTargets)
	if err != nil {
		read.Observation = refusedFor(executionID, kernel.OutcomeRequiresAuthorization, kernel.FailureAuthorization,
			fmt.Sprintf("read grant could not be built: %v", err))
		return read
	}

	spec := readSpec(executionID, canonicalTargets, caps.Root())
	if err := engine.Open(spec, grant); err != nil {
		read.Observation = refusedFor(executionID, outcomeForOpenError(err), classForOpenError(err),
			fmt.Sprintf("kernel admission refused: %v", err))
		return read
	}

	result := engine.Run(ctx)
	read.Observation = collect(result, engine.Log().Events(), executionID, canonicalTargets)
	read.bytesOnDisk = measured.bytes
	read.content = measured.content
	return read
}

// ── The spec ────────────────────────────────────────────────────────────────

// readSpec builds the immutable description of one read execution.
func readSpec(executionID string, targets []string, root string) kernel.Spec {
	program := make(kernel.Program, 0, len(targets))
	for i, target := range targets {
		program = append(program, kernel.Step{
			ID:         fmt.Sprintf("read-%d", i+1),
			Capability: kernel.FileRead,
			Target:     target,
			Args:       map[string]string{},
			Note:       "read workspace content",
		})
	}
	return kernel.Spec{
		ExecutionID: executionID,
		Objective:   fmt.Sprintf("read %d workspace target(s) under %s", len(targets), root),
		Contract: kernel.Contract{
			Kind:                 kernel.ContractObserve,
			Targets:              targets,
			RequiresVerification: true,
		},
		Program: program,
		Budget: kernel.Budget{
			// One step per destination, and each capability invoked at most once
			// per destination. Both bounds follow mechanically from the program
			// shape rather than from policy, which is why they belong on the spec:
			// they make an accidentally widened program fail loudly instead of
			// running.
			MaxSteps:              MaxObservedTargets,
			MaxStepsPerCapability: len(targets),
		},
	}
}

// ── Verification ─────────────────────────────────────────────────────────────

// readMeasurement is the verification seam for a read execution, and the record
// of what it independently established.
//
// The asymmetry with a write is worth stating. A write's obligation is "these
// bytes are there", so its verifier compares content and failing is the only safe
// answer. A read's obligation is "the report is truthful", so presence and absence
// are both passes, and only DISAGREEMENT between the log and the filesystem is a
// failure. A read that correctly reports a missing file has done its job.
//
// It is deliberately NOT synchronized. The engine calls Verify synchronously
// during Run, so the maps below are written and then read across a total order
// with no concurrency between them; a mutex here would imply a second goroutine
// that does not exist.
type readMeasurement struct {
	root    string
	bytes   map[string]int
	content map[string]string
}

func (m *readMeasurement) verify(ctx context.Context, req kernel.VerificationRequest) (kernel.Verdict, error) {
	if req.Contract.Kind != kernel.ContractObserve || len(req.Targets) == 0 {
		return kernel.VerdictNotApplicable, nil
	}
	recorded := recordedReads(req.Evidence)
	for _, target := range req.Targets {
		if err := ctx.Err(); err != nil {
			return kernel.VerdictUnknown, err
		}
		observed, ok := recorded[canonical(target)]
		if !ok {
			// The contract declared an observation obligation for this destination
			// and the log records none. Refusing is the only truthful verdict:
			// there is nothing to agree with.
			return kernel.VerdictFail, nil
		}

		data, fact, cause := reRead(m.root, target)
		switch fact {
		case diskAbsent:
			// Absence must be true on BOTH sides. A log saying "absent" about a
			// file that is now there describes an observation that does not
			// describe the filesystem, and returning PASS would hide exactly the
			// disagreement a verifier exists to catch.
			if !observed.absent {
				return kernel.VerdictFail, nil
			}
			continue

		case diskUnreadable:
			// There is no evidence about this destination either way, so there is
			// nothing to agree with. The cause is surfaced so the execution stops
			// with the real reason instead of settling on a verdict nobody checked.
			return kernel.VerdictFail, cause
		}

		// diskReadable.
		if observed.absent {
			return kernel.VerdictFail, nil
		}
		// It records what it found so the seam can serve it, and compares the
		// length against the length the capability recorded. See the file header
		// for why the comparison stops at the length.
		if observed.bytes != len(data) {
			return kernel.VerdictFail, nil
		}
		key := canonical(target)
		m.bytes[key] = len(data)
		m.content[key] = string(data)
	}
	return kernel.VerdictPass, nil
}

// recordedRead is what the log says happened to one destination.
type recordedRead struct {
	// absent reports that the destination was looked for and was not there.
	absent bool
	// bytes is the length the capability recorded for a destination it read.
	bytes int
	// found reports that the destination was read.
	found bool
}

// recordedReads indexes the observation evidence by destination.
//
// A destination observed more than once has a current truth, so the LAST record
// wins. Presence is recorded for file.read as well as file.exists because reading
// a file is an observation that it was there, and the kernel's own per-target
// observation clause already accepts FILE_READ as satisfying it.
func recordedReads(evidence []kernel.Evidence) map[string]recordedRead {
	out := make(map[string]recordedRead, len(evidence))
	for _, e := range evidence {
		if e.Target == "" {
			continue
		}
		switch e.Capability {
		case kernel.FileRead, kernel.FileExists:
		default:
			continue
		}
		key := canonical(e.Target)
		switch e.Kind {
		case kernel.EvidenceFileRead:
			out[key] = recordedRead{bytes: e.Bytes, found: true}
		case kernel.EvidenceFilePresent:
			out[key] = recordedRead{found: true}
		case kernel.EvidenceFileAbsent:
			out[key] = recordedRead{absent: true}
		}
	}
	return out
}

// ── Naming ───────────────────────────────────────────────────────────────────

// readExecutionIDFor derives a deterministic execution name from the request.
func readExecutionIDFor(root string, targets []string) string {
	h := sha256.New()
	h.Write([]byte("read"))
	h.Write([]byte{0})
	h.Write([]byte(root))
	for _, t := range targets {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return "read-" + hex.EncodeToString(h.Sum(nil))[:16]
}
