package kernelbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// This file is the mutating half of the seam.
//
// Observe asks the kernel a question about the workspace and hands back
// adjudicated truth. Apply asks it to change the workspace and hands back
// adjudicated truth about the change. The asymmetry is deliberate: a mutation is
// the operation the kernel exists to make checkable, so nothing here performs
// one directly, nothing here decides whether one may happen, and nothing here
// decides whether it worked.
//
// Three properties hold for every mutation that leaves this package:
//
//  1. The bytes reach disk through the filesystem capability, under a grant that
//     names exactly the requested destinations. A destination the caller did not
//     name cannot be written, and one it did name cannot be written twice.
//  2. What happened is evidence, not a return value. The seam never reports a
//     write as done because a syscall returned nil; it reports what the log
//     proves.
//  3. The bytes are re-read afterwards by code that did not write them, and a
//     destination whose on-disk content differs from the request is a failed
//     execution rather than a successful one.

// MaxAppliedTargets bounds one MUTATE execution's destination set.
//
// The bound is declared to the kernel as the spec's step budget, so an oversized
// request is refused in the kernel's own vocabulary rather than in a bridge-local
// error type. It is refused rather than truncated: silently dropping a
// destination would produce an execution that looks complete while a write the
// user approved never happened, which is the exact failure this package exists to
// make impossible.
const MaxAppliedTargets = 64

// mutateGrantID names the authorization every mutation runs under.
//
// Mutation authority in this deployment is fixed by policy, not by argument: the
// seam may place exactly the bytes it was handed, at exactly the destinations it
// was handed, inside one workspace, and nothing else. Exposing the grant as a
// parameter would let a caller widen the one thing a grant exists to bound.
const mutateGrantID = "kernelbridge.mutate"

// ContractCreate and ContractPatch re-export the kernel's two mutation contracts.
//
// They exist so a legacy caller can declare which obligation set its write is
// judged against WITHOUT importing the kernel. The architecture lock forbids that
// import — precisely so the bridge stays the single door — and a lock that a
// sanctioned caller cannot obey is a lock that gets deleted. The values are the
// kernel's own; nothing is renamed, and no obligation is added or dropped.
const (
	ContractCreate = kernel.ContractCreate
	ContractPatch  = kernel.ContractPatch
)

// Write is one requested workspace mutation.
//
// Contract is the caller's declaration of which obligation set this write is
// judged against, and it must be ContractCreate or ContractPatch. The seam does
// not choose it: choosing the contract is the Control Plane's decision, and a
// bridge that picked the contract for its caller would be a second, undeclared
// control plane.
//
// A batch that mixes new and existing destinations must declare ContractPatch.
// CREATE's obligations are a strict superset of PATCH's — it additionally
// demands an observation that the target now exists — so declaring CREATE for a
// batch that also rewrites an existing file would assert the creation of a file
// that was already there. PATCH applied to a new file under-claims; that is the
// safe direction, because an unmet obligation is reported and an invented one is
// not.
type Write struct {
	// Target is the workspace-relative destination. It is canonicalised by the
	// seam, and a destination that does not canonicalise names no file and is
	// refused.
	Target string
	// Content is the exact content the destination must end up holding. It is
	// placed whole: file.write replaces content, it does not patch it.
	Content string
	// Contract is the caller's obligation declaration.
	Contract kernel.ContractKind
}

// Applied is the terminal truth of one MUTATE execution.
//
// It carries the whole chain that produced it — events, evidence, the
// authoritative state and the adjudicated outcome — for the same reason Observe
// does: a caller that reports "written" to a human has to be able to say what
// proved it, and a test has to be able to check that the chain exists rather than
// just the verdict.
//
// There is deliberately no Succeeded, Verified, Applied or Completed field. Each
// of those would be a second source of truth that a caller — or a hand-built
// literal in a test — could set inconsistently with the state it claims to
// describe, and the inconsistency would surface as a confidently wrong answer
// rather than as a compile error. Every answer below is derived from the
// adjudication.
type Applied struct {
	Observation

	// Contract is the obligation set the verdict was judged against.
	Contract kernel.ContractKind
}

// Written returns the destinations the evidence log proves were durably
// written, in sorted order.
//
// It reports what the log proves and nothing more. A write that reached disk and
// then failed verification is still listed, because bytes on disk are a fact and
// the outcome is a separate claim about the contract. A caller that wants "this
// is finished" must require both Written and Proven; the seam does not collapse
// the two for it.
func (a Applied) Written() []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range a.Evidence {
		if e.Kind != kernel.EvidenceFileWritten || e.Target == "" || seen[e.Target] {
			continue
		}
		seen[e.Target] = true
		out = append(out, e.Target)
	}
	sort.Strings(out)
	return out
}

// Removed returns the destinations the evidence log proves were durably
// deleted, in sorted order.
//
// It is the mutating counterpart of Written for the DELETE direction. Like
// Written it reports what the log proves and nothing more: a destination whose
// bytes reached disk and then failed verification is not listed here, because a
// delete produced no FILE_DELETED evidence in that case.
func (a Applied) Removed() []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range a.Evidence {
		if e.Kind != kernel.EvidenceFileDeleted || e.Target == "" || seen[e.Target] {
			continue
		}
		seen[e.Target] = true
		out = append(out, e.Target)
	}
	sort.Strings(out)
	return out
}

// Deleted reports whether the evidence proves target was removed AND
// adjudication accepted the execution as a whole.
//
// It is the DELETE-direction twin of Landed: evidence that a destination was
// removed beside an unproven outcome describes a deletion the runtime cannot
// vouch for, and this seam will not present that as a completed removal.
func (a Applied) Deleted(target string) bool {
	want := canonical(target)
	if want == "" || !a.Proven() {
		return false
	}
	for _, t := range a.Removed() {
		if t == want {
			return true
		}
	}
	return false
}

// Wrote reports whether the evidence log carries durable write evidence for
// target, whether or not adjudication accepted the execution as a whole.
//
// It is the weaker twin of Landed and it exists for a specific caller need: a
// concurrent writer can replace a destination between the capability's write and
// the verifier's independent re-read, producing write evidence beside an unproven
// outcome. Core still has to treat that as an executed mutation — the workspace
// changed and the enclosing transaction must roll it back and be marked tainted —
// and Landed deliberately returns false in that case. Wrote answers "did bytes
// reach disk", which is the fact the transaction needs; Landed answers "is this a
// completed write", which is the fact a human needs. Collapsing the two would
// either taint a refusal that never wrote, or hide a write that did.
func (a Applied) Wrote(target string) bool {
	want := canonical(target)
	if want == "" {
		return false
	}
	for _, t := range a.Written() {
		if t == want {
			return true
		}
	}
	return false
}

// Created reports whether this execution created target.
//
// Both halves are read out of the evidence log, and their order matters: the
// destination must have been proven ABSENT by an observation recorded BEFORE the
// write, and the log must carry write evidence for it. Neither half can be
// supplied by the caller, so "created" cannot be asserted for a file the runtime
// merely overwrote — which is the case a buffer-time guess gets wrong whenever a
// file happens to be empty.
func (a Applied) Created(target string) bool {
	want := canonical(target)
	if want == "" {
		return false
	}
	wroteAt := -1
	for i, e := range a.Evidence {
		if e.Kind == kernel.EvidenceFileWritten && canonical(e.Target) == want {
			wroteAt = i
			break
		}
	}
	if wroteAt < 0 {
		return false
	}
	for _, e := range a.Evidence[:wroteAt] {
		if e.Kind == kernel.EvidenceFileAbsent &&
			e.Capability == kernel.FileExists &&
			canonical(e.Target) == want {
			return true
		}
	}
	return false
}

// Landed reports whether the evidence proves target's bytes reached disk AND
// adjudication accepted the execution as a whole.
//
// The two conditions are not interchangeable. Write evidence alongside an
// unproven outcome describes a workspace the runtime cannot vouch for, and this
// seam will not present that as a completed write: a caller that renders it as one
// is asserting a completion nobody adjudicated.
func (a Applied) Landed(target string) bool {
	want := canonical(target)
	if want == "" || !a.Proven() {
		return false
	}
	for _, t := range a.Written() {
		if t == want {
			return true
		}
	}
	return false
}

// Apply runs one MUTATE execution and returns its terminal truth.
//
// The execution observes each destination, then writes it, under one explicit
// grant naming exactly those destinations. Nothing here is simulated: the
// filesystem capability performs the real atomic write, and the verification seam
// re-reads the bytes from disk through code that did not write them.
//
// A destination whose content on disk differs from the request is a FAILED
// execution, not a warning. A mutation whose only witness is the code that
// performed it has not been verified, and reporting it as done is the precise lie
// this seam exists to make unrepresentable.
func Apply(ctx context.Context, root string, writes []Write) Applied {
	if len(writes) == 0 {
		return refusedApply(kernel.FailureInvalidSpec,
			"mutation requires at least one destination; an empty write set changes nothing and proves nothing")
	}

	prepared, err := prepareWrites(writes)
	if err != nil {
		var block kernel.Block
		if !errors.As(err, &block) || !block.Class.Valid() {
			return refusedApply(kernel.FailureInvalidSpec, err.Error())
		}
		return refusedApply(block.Class, block.Reason)
	}

	targets := make([]string, 0, len(prepared))
	expected := make(map[string]string, len(prepared))
	for _, w := range prepared {
		targets = append(targets, w.Target)
		expected[w.Target] = w.Content
	}
	executionID := mutateExecutionIDFor(root, prepared[0].Contract, targets)

	caps, err := filesystem.New(root)
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability surface unavailable: %v", err))
	}
	registry, err := caps.Registry()
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("workspace capability registry unavailable: %v", err))
	}

	// The verifier is constructed over the request's own content. It is
	// deliberately not the capability: replaying file.write would only re-run the
	// implementation whose report is being judged.
	engine, err := kernel.NewEngine(registry, kernel.WithVerifier(contentVerifier(caps.Root(), expected)))
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("kernel engine unavailable: %v", err))
	}

	grant, err := kernel.NewGrant(mutateGrantID,
		[]kernel.CapabilityID{kernel.FileExists, kernel.FileWrite}, targets)
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureAuthorization, err.Error())
	}

	spec := mutationSpec(executionID, prepared, caps.Root())
	// Admission refusals are results, not Go errors, for the same reason they are
	// in Observe: a caller must be able to tell "the runtime refused to run" from
	// "the runtime could not answer", and a Go error would flatten both into one
	// failure bit.
	if err := engine.Open(spec, grant); err != nil {
		return refusedApplyFor(executionID, kernel.ClassOf(err),
			fmt.Sprintf("kernel admission refused: %v", err))
	}

	result := engine.Run(ctx)
	return Applied{
		Observation: collect(result, engine.Log().Events(), executionID, targets),
		Contract:    spec.Contract.Kind,
	}
}

// ── The request ──────────────────────────────────────────────────────────────

// prepareWrites canonicalises the requested destinations and refuses a request
// the kernel could not execute as written.
//
// Every refusal here is a decision about WHAT to write, and this package does not
// make those decisions — it reports that it cannot:
//
//   - a destination that does not canonicalise names no file;
//   - a repeated destination would be written twice while only one of the two
//     writes reaches the evidence log, leaving the record ambiguous about which
//     write it describes;
//   - a batch whose calls disagree about the contract would require choosing one
//     obligation set on the caller's behalf, and there is no honest way to guess
//     which of two declared obligations was meant.
//
// A destination that escapes the workspace is NOT refused here. It is passed to
// the kernel so the refusal is recorded in the kernel's own vocabulary —
// REQUIRES_AUTHORIZATION, attributed to the step and the capability — rather than
// in a bridge-local error a caller could mistake for a missing file.
func prepareWrites(writes []Write) ([]Write, error) {
	out := make([]Write, 0, len(writes))
	seen := make(map[string]bool, len(writes))
	var contract kernel.ContractKind

	for i, w := range writes {
		target := canonical(w.Target)
		if target == "" {
			return nil, kernel.Block{
				Class:  kernel.FailureInvalidSpec,
				Reason: fmt.Sprintf("write %d names no destination", i+1),
			}
		}
		if seen[target] {
			return nil, kernel.Block{
				Class:  kernel.FailureInvalidSpec,
				Reason: fmt.Sprintf("destination %q appears more than once in one write set, so the evidence log could not say which write a record describes", target),
			}
		}
		seen[target] = true

		switch w.Contract {
		case kernel.ContractCreate, kernel.ContractPatch:
		default:
			return nil, kernel.Block{
				Class: kernel.FailureInvalidSpec,
				Reason: fmt.Sprintf("write to %q declares contract %q; a mutation must declare %s or %s",
					target, w.Contract, kernel.ContractCreate, kernel.ContractPatch),
			}
		}
		if contract == "" {
			contract = w.Contract
		} else if contract != w.Contract {
			return nil, kernel.Block{
				Class: kernel.FailureInvalidSpec,
				Reason: fmt.Sprintf("write set mixes %s and %s obligations; one execution carries one contract",
					contract, w.Contract),
			}
		}
		out = append(out, Write{Target: target, Content: w.Content, Contract: w.Contract})
	}

	if len(out) > MaxAppliedTargets {
		return nil, kernel.Block{
			Class: kernel.FailureBudgetExhausted,
			Reason: fmt.Sprintf("write set has %d destinations, above the declared bound of %d",
				len(out), MaxAppliedTargets),
		}
	}
	return out, nil
}

// mutationSpec builds the immutable description of one MUTATE execution.
//
// Each destination gets two steps in a fixed order: the existence observation
// first, then the write. The order is the program's only control flow and it is
// load-bearing. An observation taken after the write could not distinguish "this
// file was created" from "this file was overwritten", which is exactly the
// question a caller has to answer when it reports what changed. Recording the
// observation first makes creation a derivable fact rather than a guess carried
// over from before the execution.
//
// The contract requires verification, so what reached disk is re-read and
// compared by code that did not write it.
func mutationSpec(executionID string, writes []Write, root string) kernel.Spec {
	program := make(kernel.Program, 0, 2*len(writes))
	targets := make([]string, 0, len(writes))
	for i, w := range writes {
		targets = append(targets, w.Target)
		program = append(program,
			kernel.Step{
				ID:         fmt.Sprintf("pre-%d", i+1),
				Capability: kernel.FileExists,
				Target:     w.Target,
				Args:       map[string]string{},
				Note:       "observe destination presence before the write",
			},
			kernel.Step{
				ID:         fmt.Sprintf("write-%d", i+1),
				Capability: kernel.FileWrite,
				Target:     w.Target,
				Args:       map[string]string{"content": w.Content},
				Note:       "write destination content",
			})
	}
	return kernel.Spec{
		ExecutionID: executionID,
		Objective: fmt.Sprintf("write %d workspace destination(s) under %s",
			len(writes), root),
		Contract: kernel.Contract{
			Kind:                 writes[0].Contract,
			Targets:              targets,
			RequiresVerification: true,
		},
		Program: program,
		Budget: kernel.Budget{
			// Two steps per destination, and each capability is invoked at most
			// once per destination. Both bounds are mechanical consequences of the
			// program shape rather than policy, which is why they belong on the
			// spec: they make an accidentally widened program fail loudly instead
			// of running.
			MaxSteps:              2 * len(writes),
			MaxStepsPerCapability: len(writes),
		},
	}
}

// ── Verification ─────────────────────────────────────────────────────────────

// contentVerifier is the verification seam for a MUTATE execution.
//
// It answers one question per destination: do the bytes on disk right now equal
// the bytes the execution was asked to place there? Both halves are required. A
// destination with write evidence and wrong content is a FAIL, not a pass — the
// whole point of a mutation is what it left behind, and "the write syscall
// returned nil" is not an answer to that.
//
// The re-read deliberately does NOT go through the filesystem capability.
// Replaying file.write would only re-run the implementation whose report is being
// judged, which is an echo rather than a check.
func contentVerifier(root string, want map[string]string) kernel.Verifier {
	return kernel.VerifierFunc(func(ctx context.Context, req kernel.VerificationRequest) (kernel.Verdict, error) {
		if !req.Contract.Kind.RequiresMutation() || len(req.Targets) == 0 {
			return kernel.VerdictNotApplicable, nil
		}
		for _, target := range req.Targets {
			if err := ctx.Err(); err != nil {
				return kernel.VerdictUnknown, err
			}
			// The obligation is about the filesystem, not about a step having run.
			// A destination with no durable write record is refused before the disk
			// is consulted, because there is nothing to agree with.
			if !wroteTarget(req.Evidence, target) {
				return kernel.VerdictFail, nil
			}
			expected, ok := want[canonical(target)]
			if !ok {
				// The execution declared an obligation for a destination the request
				// never described. Refusing is the only truthful verdict.
				return kernel.VerdictFail, nil
			}

			onDisk, fact, cause := reRead(root, target)
			switch fact {
			case diskReadable:
				if string(onDisk) != expected {
					return kernel.VerdictFail, nil
				}
			case diskAbsent:
				// The destination is not there any more. A FAIL, never a skip: a
				// skipped check here would be indistinguishable from a pass, and
				// this one is the case a truncated or rolled-back write produces.
				return kernel.VerdictFail, nil
			default:
				// The verifier could not obtain an answer at all. The cause is
				// surfaced so "the workspace could not be read" stays
				// distinguishable from "the content is wrong" — both fail, and an
				// operator needs to know which one happened.
				return kernel.VerdictFail, cause
			}
		}
		return kernel.VerdictPass, nil
	})
}

// wroteTarget reports whether the log carries durable write evidence for the
// exact destination.
func wroteTarget(evidence []kernel.Evidence, target string) bool {
	want := canonical(target)
	for _, e := range evidence {
		if e.Kind == kernel.EvidenceFileWritten && canonical(e.Target) == want {
			return true
		}
	}
	return false
}

// ── Refusals and naming ──────────────────────────────────────────────────────

// refusedApply builds the terminal truth for a request that never reached a
// capability.
//
// The outcome is derived from the kernel's own class taxonomy rather than chosen
// here, so "a human must decide" and "the runtime broke" stay distinguishable at
// the call site.
func refusedApply(class kernel.FailureClass, reason string) Applied {
	return refusedApplyFor("", class, reason)
}

func refusedApplyFor(executionID string, class kernel.FailureClass, reason string) Applied {
	if !class.Valid() {
		class = kernel.FailureInvalidSpec
	}
	block := kernel.Block{Class: class, Reason: reason}
	return Applied{
		Observation: refusedFor(executionID, outcomeForOpenError(block), classForOpenError(block), reason),
	}
}

// mutateExecutionIDFor derives a deterministic execution name from the mutation
// request.
//
// Determinism keeps a log line correlatable with the request that produced it
// without the seam keeping any state, exactly as it does for an observation. The
// contract is part of the name because the same destinations under different
// obligations are two different claims, and a log that conflated them could not
// be reconstructed.
func mutateExecutionIDFor(root string, contract kernel.ContractKind, targets []string) string {
	h := sha256.New()
	h.Write([]byte("mutate"))
	h.Write([]byte{0})
	h.Write([]byte(root))
	h.Write([]byte{0})
	h.Write([]byte(contract))
	for _, t := range targets {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return "mutate-" + hex.EncodeToString(h.Sum(nil))[:16]
}
