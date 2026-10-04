// Package kernelbridge is the single seam through which the legacy execution
// tree asks the Runtime Kernel a question about the workspace.
//
// # Why this package exists
//
// Izen currently runs three separate execution stacks (see
// docs/architecture/STRANGLER_MIGRATION.md). None of them is the kernel. Each
// one answers its own workspace questions with whatever syscall was nearest,
// which means a target can be declared "present" by a bare `os.Stat` with no
// event, no state, no evidence and no verification behind it.
//
// Strangling those stacks is only possible if there is exactly one place a
// legacy caller can reach the kernel. That place is this package. Everything
// else is a bypass, and test/architecture/kernel_lock_test.go fails the build
// when one appears.
//
// # What this package must never become
//
// It is a migration seam, not a layer. It holds no execution state between calls,
// no planning, no provider, no recovery, no retry and no mutation policy. It
// composes a Spec, a Grant and a verifier, and returns whatever the kernel
// adjudicated. If it ever needs to know what to DO rather than only how to ask —
// if it starts choosing destinations, narrowing or widening a grant, or deciding
// that a second attempt is warranted — it has become a second runtime, and the
// strangler has stopped strangling.
//
// # The rule this package enforces
//
//	question in, evidence-backed verdict out
//
// A caller may not learn whether a workspace target exists except by receiving a
// terminal Outcome that adjudication derived from a capability's evidence, and
// may not place bytes on disk except by receiving an Applied whose destinations
// were written under an explicit grant and re-read afterwards by code that did
// not write them. There is deliberately no cheaper API here — no bool-returning
// helper, no "convenience" existence predicate, no error-returning write
// shorthand — because a convenience API is exactly how a kernel gets bypassed one
// release later.
//
// The three directions are separate files with separate entry points:
// Observe asks a question, Apply performs a mutation, Read returns bytes.
// Each one owns its own contract and its own verifier, and none of them can be
// used in place of another.
//
// # Failure is a first-class answer
//
// When the outcome is not PROVEN, the answer is UNKNOWN. It is never "absent",
// and it is never "written".
//
// Collapsing "I could not prove it" into "it does not exist" is the precise
// defect the kernel exists to remove, so the bridge refuses to perform that
// collapse on a caller's behalf: Observation.Exists reports false for an
// unproven target, and Observed distinguishes "proven absent" from "not proven"
// so a caller can tell the two apart and refuse to proceed on the latter.
// Applied.Landed makes the identical distinction on the mutating side: write
// evidence beside an unproven outcome is a workspace the runtime cannot vouch
// for, and it is not reported as a completed write.
package kernelbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// MaxObservedTargets bounds one OBSERVE execution's target set.
//
// The bound is declared to the kernel as the spec's step budget rather than
// enforced only here, so an oversized request is refused in the kernel's own
// vocabulary (BUDGET_EXHAUSTED) instead of in a bridge-local error type. A
// target set larger than this is refused rather than truncated: silently
// dropping targets would produce an observation that looks complete and is not.
const MaxObservedTargets = 256

// grantID names the authorization every observation runs under. It is a
// constant rather than a parameter because observation authority in this
// deployment is fixed by policy — read one filesystem capability over a named
// workspace — and exposing it as a parameter would let a caller widen it.
const grantID = "kernelbridge.observe"

// Presence is what one execution proved about one target.
//
// The three fields are deliberately distinct. Observed without Present means the
// kernel looked and found nothing. Present is only ever true when Observed is
// true and the kernel's adjudication reached PROVEN.
type Presence struct {
	// Target is the canonical workspace-relative path the observation is about.
	Target string
	// Present reports that the target was observed to exist.
	Present bool
	// Observed reports that the kernel recorded an observation for this target
	// at all. It is false when the execution stopped before observing it.
	Observed bool
}

// Observation is the terminal truth of one OBSERVE execution over a target set.
//
// It carries the whole chain that produced it — events, evidence, the
// authoritative state, and the outcome — so a caller can render the reasoning
// rather than a bare boolean, and a test can prove the chain exists.
type Observation struct {
	// ExecutionID names the kernel execution, deterministically derived from the
	// question being asked so the same question always has the same name.
	ExecutionID string
	// Outcome is the kernel's terminal verdict.
	Outcome kernel.Outcome
	// Class is the failure class behind a non-PROVEN outcome, or "" for PROVEN.
	// It is the kernel's own taxonomy, never a bridge-local one.
	Class kernel.FailureClass
	// Reason is the kernel's one-line deterministic explanation.
	Reason string
	// Revision is the state revision at which the verdict was recorded.
	Revision uint64
	// Verify is the verification axis: PASSED, FAILED, NOT_RUN or
	// NOT_APPLICABLE. A caller that requires a real check reads this rather than
	// inferring it from Proven.
	Verify kernel.VerifyAxis
	// Targets is the canonical target set the execution declared.
	Targets []string
	// Presence maps each canonical target onto what was proved about it.
	Presence map[string]Presence
	// Evidence is the complete observation record.
	Evidence []kernel.Evidence
	// Events is the durable event log, in sequence order.
	Events []kernel.Event
	// State is the authoritative state the verdict was derived from.
	//
	// It is the ZERO value when the request was refused before admission, because
	// no execution exists to describe. Its axes then carry no meaning at all — not
	// "NONE", which would be a claim — so a consumer must decide from Proven() and
	// the evidence, never from an axis on a refusal. Verify above is set explicitly
	// even then, because callers read it directly.
	State kernel.State
}

// Proven reports whether adjudication reached PROVEN. It is the only thing that
// authorizes a completion claim.
//
// It is a method rather than a field on purpose. A field would be a second
// source of truth that a caller — or a hand-built literal in a test — could set
// inconsistently with Outcome, and the inconsistency would show up as a
// confidently wrong answer rather than as a compile error. Deriving it from the
// outcome makes disagreement impossible to express.
func (o Observation) Proven() bool { return o.Outcome.Proves() }

// Exists reports whether target was proven present by a PROVEN execution.
//
// It returns false — never a guess — when the target was not observed, when the
// execution did not reach PROVEN, or when target is not in the observed set.
func (o Observation) Exists(target string) bool {
	p, ok := o.Presence[canonical(target)]
	return ok && o.Proven() && p.Observed && p.Present
}

// Absent reports whether target was proven NOT to exist by a PROVEN execution.
//
// This is the answer a caller needs in order to distinguish "the workspace does
// not contain it" from "nobody proved anything", and the two must never be
// rendered the same way.
func (o Observation) Absent(target string) bool {
	p, ok := o.Presence[canonical(target)]
	return ok && o.Proven() && p.Observed && !p.Present
}

// ProvenTargets returns the canonical targets proven present, in sorted order.
func (o Observation) ProvenTargets() []string {
	var out []string
	for target, p := range o.Presence {
		if o.Proven() && p.Observed && p.Present {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

// Observed reports the workspace fact for exactly one target.
//
// A single-target question is by far the common case, and giving it its own
// entry point keeps callers from having to index a map and reason about a
// missing key to answer "does this one file exist".
func Observe(ctx context.Context, root string, targets []string) Observation {
	canonicalTargets := canonicalise(targets)
	if len(canonicalTargets) == 0 {
		return refused(kernel.OutcomeUnsubstantiated, kernel.FailureInvalidSpec,
			"observation requires at least one target; an empty target set proves nothing")
	}
	if len(canonicalTargets) > MaxObservedTargets {
		return refused(kernel.OutcomeBudgetExhausted, kernel.FailureBudgetExhausted,
			fmt.Sprintf("observation target set has %d entries, above the declared bound of %d",
				len(canonicalTargets), MaxObservedTargets))
	}

	executionID := executionIDFor(root, canonicalTargets)

	caps, err := filesystem.New(root)
	if err != nil {
		return refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability surface unavailable: %v", err))
	}
	registry, err := caps.Registry()
	if err != nil {
		return refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability registry unavailable: %v", err))
	}

	engine, err := kernel.NewEngine(registry, kernel.WithVerifier(agreementVerifier(caps.Root())))
	if err != nil {
		return refusedFor(executionID, kernel.OutcomeFailed, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("kernel engine unavailable: %v", err))
	}

	grant, err := kernel.NewGrant(grantID, []kernel.CapabilityID{kernel.FileExists}, canonicalTargets)
	if err != nil {
		return refusedFor(executionID, kernel.OutcomeRequiresAuthorization, kernel.FailureAuthorization,
			fmt.Sprintf("observation grant could not be built: %v", err))
	}

	spec := observationSpec(executionID, canonicalTargets, caps.Root())
	// Admission failures are results, not Go errors: a caller must be able to
	// tell "the workspace does not contain this target" from "the runtime refused
	// to run at all", and a Go error would flatten both into one failure bit.
	if err := engine.Open(spec, grant); err != nil {
		return refusedFor(executionID, outcomeForOpenError(err), classForOpenError(err),
			fmt.Sprintf("kernel admission refused: %v", err))
	}
	result := engine.Run(ctx)
	// The event log is read from the engine rather than from the Result: the
	// Result is the verdict, and the log is the only complete record of how the
	// verdict was reached. A caller that wants to show its reasoning needs both.
	return collect(result, engine.Log().Events(), executionID, canonicalTargets)
}

// ObserveOne is Observe for exactly one target.
func ObserveOne(ctx context.Context, root, target string) (Presence, Observation) {
	obs := Observe(ctx, root, []string{target})
	return obs.Presence[canonical(target)], obs
}

// ── The spec ────────────────────────────────────────────────────────────────

// observationSpec builds the immutable execution description.
//
// The contract is OBSERVE and it demands verification. It does NOT demand a
// workspace-wide observation, because file.exists does not enumerate the
// workspace and the OBSERVE contract's own per-target clause already requires
// that each declared target be the subject of a recorded observation. Demanding
// the workspace-wide evidence kind as well would make the contract
// unsatisfiable by this program, and satisfying it by adding an enumeration step
// would be inventing a fact this slice does not need.
func observationSpec(executionID string, targets []string, root string) kernel.Spec {
	program := make(kernel.Program, 0, len(targets))
	for i, target := range targets {
		program = append(program, kernel.Step{
			ID:         fmt.Sprintf("exists-%d", i+1),
			Capability: kernel.FileExists,
			Target:     target,
			Args:       map[string]string{},
			Note:       "observe workspace presence",
		})
	}
	return kernel.Spec{
		ExecutionID: executionID,
		Objective: fmt.Sprintf("observe whether %d workspace target(s) exist under %s",
			len(targets), root),
		Contract: kernel.Contract{
			Kind:                 kernel.ContractObserve,
			Targets:              targets,
			RequiresVerification: true,
		},
		Program: program,
		Budget:  kernel.Budget{MaxSteps: MaxObservedTargets},
	}
}

// ── Verification ────────────────────────────────────────────────────────────

// agreementVerifier is the verification seam for an OBSERVE execution.
//
// It answers one question: does the recorded evidence still agree with the
// filesystem? An OBSERVE contract's obligation is a truthful report, not a
// particular answer, so a target that is genuinely absent is a PASS and a target
// that is genuinely present is a PASS. What is not a pass is evidence that
// disagrees with reality — which is the failure that would make a completion
// claim a lie.
//
// The re-check deliberately does NOT reuse the filesystem capability. Replaying
// the capability would only re-read the same implementation's own report; this
// derives the fact again, through different code, which is what makes it a
// check rather than an echo.
func agreementVerifier(root string) kernel.Verifier {
	return kernel.VerifierFunc(func(ctx context.Context, req kernel.VerificationRequest) (kernel.Verdict, error) {
		if req.Contract.Kind != kernel.ContractObserve || len(req.Targets) == 0 {
			return kernel.VerdictNotApplicable, nil
		}
		recorded := recordedPresence(req.Evidence)
		for _, target := range req.Targets {
			observed, ok := recorded[target]
			if !ok {
				// The execution declared an observation obligation for this target
				// and produced no observation for it. Refusing is the only truthful
				// verdict: there is nothing to agree with.
				return kernel.VerdictFail, nil
			}
			present, err := statOnce(ctx, root, target)
			if err != nil {
				return kernel.VerdictFail, err
			}
			if present != observed {
				return kernel.VerdictFail, nil
			}
		}
		return kernel.VerdictPass, nil
	})
}

// recordedPresence reads the presence verdict out of an evidence record.
//
// Presence and absence are both observations, and the LAST one wins. A target
// observed twice — before and after some other action — has a current truth, and
// using the first would let a stale observation decide the verdict.
func recordedPresence(evidence []kernel.Evidence) map[string]bool {
	out := make(map[string]bool, len(evidence))
	for _, e := range evidence {
		if e.Capability != kernel.FileExists || e.Target == "" {
			continue
		}
		switch e.Kind {
		case kernel.EvidenceFilePresent:
			out[e.Target] = true
		case kernel.EvidenceFileAbsent:
			out[e.Target] = false
		}
	}
	return out
}

// statOnce derives one workspace fact independently of the capability.
//
// It performs its own confinement check for the same reason agreementVerifier
// avoids the capability: an independent derivation has to be independent all the
// way down, or it is not a check at all.
func statOnce(ctx context.Context, root, target string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	abs, err := confined(root, target)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// confined maps a canonical workspace-relative target onto an absolute path and
// refuses anything that escapes the root.
func confined(root, target string) (string, error) {
	rel := canonical(target)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." || path.IsAbs(rel) {
		return "", kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves outside the workspace root", target),
		}
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	back, err := filepath.Rel(root, abs)
	if err != nil {
		return "", kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves outside the workspace root", target),
		}
	}
	back = filepath.ToSlash(back)
	if back == ".." || strings.HasPrefix(back, "../") {
		return "", kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves outside the workspace root", target),
		}
	}
	return abs, nil
}

// diskFact is the three-way answer an independent re-read of one destination can
// give.
//
// The three outcomes are named because the difference between them is the whole
// point of checking. "It is not there" and "I could not read it" are different
// facts about the filesystem, and only one of them can ever be a pass. A helper
// returning ([]byte, error) cannot express that without forcing every caller to
// either propagate an unreadable destination as an engine failure or throw the
// error away — and throwing it away is the move this package exists to stop
// making.
type diskFact int

const (
	// diskReadable: the destination exists and its bytes were read.
	diskReadable diskFact = iota
	// diskAbsent: the destination is not there. A real observed fact.
	diskAbsent
	// diskUnreadable: the destination could not be read for any reason other than
	// absence — a permission error, a directory, an I/O failure.
	diskUnreadable
)

// reRead derives one workspace fact about a destination independently of the
// filesystem capability.
//
// It deliberately does not reuse the capability, and it performs its own
// confinement check for the same reason: an independent derivation has to be
// independent all the way down, or it is not a check at all.
//
// It takes no context. Both verifiers check cancellation themselves before
// consulting it, so a withdrawn execution stops with CANCELLED rather than
// settling on a verdict derived from a half-finished read.
//
// cause is non-nil for every outcome other than a clean answer, including a
// destination that is simply not there. "Permission denied" is the difference
// between a runtime that cannot verify and a workspace nobody may read, and
// losing that string makes two unrelated failures look like one bug.
func reRead(root, target string) (data []byte, fact diskFact, cause error) {
	abs, err := confined(root, target)
	if err != nil {
		return nil, diskUnreadable, err
	}
	content, err := os.ReadFile(abs)
	switch {
	case err == nil:
		return content, diskReadable, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, diskAbsent, err
	default:
		return nil, diskUnreadable, err
	}
}

// ── Result assembly ─────────────────────────────────────────────────────────

// collect projects a kernel Result into an Observation, reading presence from the
// evidence log rather than from any intermediate the caller could have
// influenced.
//
// Presence is projected from both file.exists and file.write evidence, because a
// mutation re-observes its destination after committing and that second
// observation is exactly what a CREATE contract's existence obligation is
// satisfied by. Last record wins: a destination observed before and after a write
// has a current truth, and using the first would let a stale observation decide.
func collect(result kernel.Result, events []kernel.Event, executionID string, targets []string) Observation {
	obs := Observation{
		ExecutionID: executionID,
		Outcome:     result.Outcome,
		Class:       result.Class,
		Reason:      result.Reason,
		Revision:    result.Revision,
		Verify:      result.State.Verify,
		Targets:     append([]string(nil), targets...),
		Presence:    make(map[string]Presence, len(targets)),
		Evidence:    append([]kernel.Evidence(nil), result.Evidence...),
		Events:      append([]kernel.Event(nil), events...),
		State:       result.State,
	}
	for _, target := range targets {
		obs.Presence[target] = Presence{Target: target}
	}
	for _, e := range obs.Evidence {
		if e.Target == "" || !presenceCapable(e.Capability) {
			continue
		}
		if _, declared := obs.Presence[e.Target]; !declared {
			continue
		}
		switch e.Kind {
		case kernel.EvidenceFilePresent, kernel.EvidenceFileRead:
			// FILE_READ counts as presence for the same reason it counts for the
			// kernel's own existence obligations: the capability opened and read
			// the target, which is a stronger observation than a stat.
			obs.Presence[e.Target] = Presence{Target: e.Target, Present: true, Observed: true}
		case kernel.EvidenceFileAbsent:
			obs.Presence[e.Target] = Presence{Target: e.Target, Present: false, Observed: true}
		}
	}
	return obs
}

// presenceCapable reports whether a capability's evidence is a real observation
// of one specific target, and therefore belongs in the presence projection.
//
// file.read counts, and so does file.write. Reading a file is an observation that
// it was there, and the kernel's own CREATE and PATCH obligations already treat
// FILE_READ as satisfying an existence requirement — a projection that disagreed
// with adjudication would report "unknown" for a target the contract accepted.
//
// file.search is deliberately excluded. Its FILE_READ evidence records a match
// count over content it loaded, not a reading of the target as a file, so letting
// it populate presence would let a search answer a question it was not asked.
func presenceCapable(id kernel.CapabilityID) bool {
	switch id {
	case kernel.FileRead, kernel.FileExists, kernel.FileWrite:
		return true
	default:
		return false
	}
}

// refused builds an Observation for a request that never reached the kernel.
//
// It is still a closed-vocabulary Outcome rather than an error return, because a
// caller needs to distinguish "the workspace does not contain it" from "the
// runtime could not answer", and a Go error would collapse that distinction back
// into the single bit this package exists to replace.
func refused(outcome kernel.Outcome, class kernel.FailureClass, reason string) Observation {
	return refusedFor("", outcome, class, reason)
}

func refusedFor(executionID string, outcome kernel.Outcome, class kernel.FailureClass, reason string) Observation {
	return Observation{
		ExecutionID: executionID,
		Outcome:     outcome,
		Class:       class,
		Reason:      reason,
		Verify:      kernel.VerifyNotRun,
		Presence:    map[string]Presence{},
	}
}

// classForOpenError extracts the kernel's own failure class from an admission
// refusal, so the bridge never has to invent a taxonomy of its own.
func classForOpenError(err error) kernel.FailureClass {
	var block kernel.Block
	if errors.As(err, &block) && block.Class.Valid() {
		return block.Class
	}
	return kernel.FailureInvalidSpec
}

// outcomeForOpenError maps a refusal class onto the outcome that class implies.
//
// The mapping mirrors the kernel's own, and deliberately so: an authorization
// refusal must not be reported to a caller as a plain failure, or the caller
// cannot tell "a human must decide" from "the runtime broke".
func outcomeForOpenError(err error) kernel.Outcome {
	switch classForOpenError(err) {
	case kernel.FailureAuthorization:
		return kernel.OutcomeRequiresAuthorization
	case kernel.FailureBudgetExhausted:
		return kernel.OutcomeBudgetExhausted
	case kernel.FailureCancelled:
		return kernel.OutcomeCancelled
	case kernel.FailureInterrupted:
		return kernel.OutcomeInterrupted
	case kernel.FailureUnsubstantiated:
		return kernel.OutcomeUnsubstantiated
	default:
		return kernel.OutcomeFailed
	}
}

// ── Naming and canonicalisation ─────────────────────────────────────────────

// canonical reduces a caller-supplied target to the one spelling the kernel, the
// contract and the evidence log all agree on.
//
// It exists because evidence for "a//b" and for "a/b" would otherwise be two
// incomparable records of the same file, and a contract clause keyed on one
// spelling could not be satisfied by evidence carrying the other.
func canonical(target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return ""
	}
	slashed := filepath.ToSlash(trimmed)
	slashed = strings.TrimPrefix(slashed, "./")
	if slashed == "" {
		return ""
	}
	cleaned := path.Clean(slashed)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// canonicalise applies canonical to a target set, dropping empties and duplicates
// while preserving the caller's order.
//
// Order is preserved rather than sorted because step order is the program's only
// control flow and a caller reading the event log expects to see the targets in
// the order it asked about them.
func canonicalise(targets []string) []string {
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		c := canonical(t)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// executionIDFor derives a deterministic execution name from the question.
//
// Determinism is not cosmetic here: it means the same question asked twice
// produces the same execution id, so a log line can be correlated with the
// resolution that caused it without the bridge keeping any state.
func executionIDFor(root string, targets []string) string {
	h := sha256.New()
	h.Write([]byte(root))
	for _, t := range targets {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return "observe-" + hex.EncodeToString(h.Sum(nil))[:16]
}
