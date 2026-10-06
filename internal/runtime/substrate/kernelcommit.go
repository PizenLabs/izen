package substrate

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// This file is Path A's crossing point into the Runtime Kernel.
//
// Path A is the workspace mutation route `izen run` takes:
//
//	cmd/izen/runtime.go: runRuntimeCommand
//	  → app.Pipeline.Run
//	  → app.proposalFromTx            (Core: staging → proposal)
//	  → ConcreteSubstrate.Execute     (Core: transaction, rollback, evidence)
//	  → commitWrite / commitDelete    ← this file
//	  → kernelbridge.Apply / Delete
//	  → runtime/kernel                (admission, grant, dispatch, verify, adjudicate)
//	  → runtime/capabilities/filesystem
//
// Core keeps everything it owned before the migration and keeps it here: the
// transaction id, the pre-mutation snapshot, the rollback that undoes a partial
// batch, the mandatory pre-commit AST verification, the ExecutionProof and the
// evidence store. What moved is exactly one thing — the final filesystem effect
// of a committed operation. It used to be `osFilePort.Write` / `osFilePort.Remove`
// behind a FilePort, with no grant, no event, no evidence and no verification
// behind it. It is now a kernel execution under an explicit grant, re-read from
// disk by a verifier that did not write it, and adjudicated into an outcome this
// file projects back into Core evidence.
//
// Two rules govern what may be added here.
//
// The first is that this file may not become a second control plane. It composes
// a request, asks, and reports what came back. It does not choose a destination,
// does not decide a target may be written, does not retry, and does not widen a
// grant. Where a decision is genuinely Core's — whether the destination already
// existed, and therefore which obligation set the write is judged against — it is
// taken from the snapshot Core already took for rollback, never from a guess made
// here.
//
// The second is that a kernel outcome is not a Core outcome. `kernelbridge.Apply`
// reaching PROVEN proves that one filesystem effect happened and was verified.
// It does not prove the objective, and it does not prove the proposal committed:
// only the enclosing transaction, which rolls back and marks on any failure, may
// say that. Every helper below therefore returns the kernel's evidence to the
// caller rather than a boolean that would collapse the two claims.

// commitWrite places one FILE_WRITE payload on disk through the Runtime Kernel.
//
// It is the only place Path A writes a file it was asked to commit. The legacy
// `osFilePort.Write` call it replaced reported success from the syscall's return
// alone: no grant, no event, no evidence, and no re-read, so a destination whose
// bytes never landed was indistinguishable from one that landed correctly.
//
// The failure this preserves and sharpens:
//
//   - a target outside the workspace, or one a symlink resolves outside it, is
//     refused by Core's use-time confinement before the kernel is asked at all,
//     with the same ErrWorkspaceEscape sentinel as before;
//   - bytes that reach disk but are then changed by a concurrent writer are
//     caught by the kernel's independent re-read, and the write is reported as
//     unproven, which fails the operation and taints the transaction so the
//     surrounding rollback runs. The legacy syscall could not see that race at
//     all;
//   - a write that did not happen leaves no write evidence, so the operation
//     fails on the absence of evidence rather than on an optimistic read of a
//     return code.
func (s *ConcreteSubstrate) commitWrite(ctx context.Context, target string, content []byte) (MutationEvidence, error) {
	rel, err := s.confinedTarget(target)
	if err != nil {
		return MutationEvidence{Op: OpFileWrite, Target: target}, err
	}

	applied := kernelbridge.Apply(ctx, s.root, []kernelbridge.Write{{
		Target:   rel,
		Content:  string(content),
		Contract: writeContract,
	}})

	evidence := mutationEvidenceFor(OpFileWrite, rel, applied)
	if applied.Landed(rel) {
		return evidence, nil
	}
	return evidence, kernelRefused("write", rel, applied)
}

// writeContract is the obligation set every Path A write is declared under.
//
// FILE_WRITE replaces a destination's whole content and does not distinguish
// "this file is new" from "this file existed", so declaring CREATE would assert
// a creation the proposal never claimed — and for an overwrite that assertion
// is false. PATCH is the honest declaration for a whole-content replace: it
// demands durable write evidence for the destination and nothing more.
//
// PATCH applied to a destination that did not exist under-claims, and that is
// the safe direction: an obligation the runtime could not meet is reported, an
// obligation it invented is not. Applied.Created still reports the truth about
// creation, derived from the kernel's own pre-write observation rather than from
// anything Core asserted.
const writeContract = kernelbridge.ContractPatch

// commitDelete removes one FILE_DELETE target through the Runtime Kernel.
//
// The distinction it preserves is the one Path A has always made and a bare
// `os.Remove` made invisibly. Removing a target that was not there is not a
// failure — it is the desired end state — but it is also not a removal, and
// reporting it as one would let a proposal claim to have deleted a file it never
// touched. The kernel distinguishes them exactly: a durable removal records
// FILE_DELETED, an already-absent destination records FILE_ABSENT and nothing
// else, and the DELETE contract is satisfied only by the former.
//
// So both outcomes are accepted, and which one happened is carried in the
// returned evidence rather than flattened into one success bit. A target the
// kernel refused to touch at all — a directory, a target outside the workspace,
// one it was never granted — is neither, and fails.
func (s *ConcreteSubstrate) commitDelete(ctx context.Context, target string) (MutationEvidence, error) {
	rel, err := s.confinedTarget(target)
	if err != nil {
		return MutationEvidence{Op: OpFileDelete, Target: target}, err
	}

	applied := kernelbridge.Delete(ctx, s.root, []string{rel})

	evidence := mutationEvidenceFor(OpFileDelete, rel, applied)
	switch {
	case applied.Deleted(rel):
		return evidence, nil
	case applied.Vanished(rel):
		// Already absent: the operation's end state holds, nothing was removed,
		// and the evidence says so. This is the evidence-backed form of the
		// `os.IsNotExist` tolerance the legacy port had.
		return evidence, nil
	default:
		return evidence, kernelRefused("delete", rel, applied)
	}
}

// confinedTarget maps an execution target onto the workspace-relative spelling
// the kernel's grant and evidence log agree on, after enforcing Core's use-time
// confinement.
//
// Both halves matter and neither replaces the other. substrateRel is lexical: it
// rejects a target that climbs out of the root before anything is opened. The
// use-time verify that follows is a real check: a symlink swapped in between
// authorization and application, which resolves outside the root, fails closed
// with ErrWorkspaceEscape before the kernel is asked to touch anything. The
// kernel then re-checks confinement independently, from its own root handle, so
// the guarantee does not rest on this call alone.
func (s *ConcreteSubstrate) confinedTarget(target string) (string, error) {
	rel, err := substrateRel(s.root, target)
	if err != nil {
		return "", err
	}
	if s.delegate != nil {
		if err := s.delegate.verifyUse(rel); err != nil {
			return "", err
		}
	}
	return rel, nil
}

// kernelRefused builds the error a caller sees when the kernel did not prove the
// effect it was asked for.
//
// It reports the whole verdict rather than a bare failure, because "the runtime
// refused to run" and "the runtime ran and could not prove it" are different
// facts and a caller that cannot tell them apart will retry a refusal as though
// it were a flake.
func kernelRefused(op, target string, applied kernelbridge.Applied) error {
	return fmt.Errorf(
		"kernel did not prove the %s of %s: outcome=%s class=%s verify=%s reason=%s execution=%s",
		op, target, applied.Outcome, applied.Class, applied.Verify, applied.Reason, applied.ExecutionID)
}

// mutationEvidenceFor projects one kernel execution into Core's evidence record.
//
// Every field comes from the Applied the bridge returned, and the two landing
// questions are asked through the bridge's own accessors rather than by reading
// raw evidence here. That keeps "did this land" one question with one answer —
// one that already accounts for verification and adjudication — so this
// projection cannot report a landing the kernel declined to report, and cannot
// report a non-landing as proven.
//
// The target recorded is the one Core asked for, not one recovered from the
// result. A refusal that never reached admission still names the destination that
// was refused, which is the fact an operator needs; a result carrying no target
// set would name nothing at all.
func mutationEvidenceFor(op OperationType, target string, applied kernelbridge.Applied) MutationEvidence {
	m := MutationEvidence{
		Op:          op,
		Target:      target,
		ExecutionID: applied.ExecutionID,
		Contract:    applied.Contract.String(),
		Outcome:     applied.Outcome.String(),
		Class:       applied.Class.String(),
		Verify:      applied.Verify.String(),
		Reason:      applied.Reason,
	}
	if target != "" {
		m.Landed = applied.Landed(target)
		m.Deleted = applied.Deleted(target)
		m.Vanished = applied.Vanished(target)
	}
	return m
}
