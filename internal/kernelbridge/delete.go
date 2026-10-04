package kernelbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// This file is the deleting half of the mutating seam.
//
// Observe asks whether a target is there, Read returns its bytes, Apply places
// content, and Delete removes a target. Delete is a separate direction rather
// than "write empty bytes" because the two produce different filesystem facts:
// after a write of "" the target is a present, empty file; after a delete the
// target is gone. A contract that names one of those must not be satisfied by
// the other, and collapsing them would let a caller report a removal that left
// an empty file behind.
//
// The same three properties that hold for Apply hold here:
//
//  1. The removal happens through the filesystem capability, under a grant that
//     names exactly the requested destinations. A destination the caller did not
//     name cannot be removed, and it cannot be removed twice.
//  2. What happened is evidence, not a return value. A removal is proven by
//     FILE_DELETED evidence and a fresh FILE_ABSENT observation, never by the
//     fact that os.Remove returned nil.
//  3. Removing a target that is already gone is a NO-OP, not a mutation: the
//     log records FILE_ABSENT but no FILE_DELETED, so a DELETE contract that
//     demands a durable change is not satisfied by having found nothing to do.

// deleteGrantID names the authorization every deletion runs under. It is a
// constant for the same reason the mutation grant is: deletion authority here is
// fixed by policy, and exposing it as a parameter would let a caller widen it.
const deleteGrantID = "kernelbridge.delete"

// Delete runs one DELETE execution and returns its terminal truth.
//
// The program invokes exactly one file.delete step per named destination, under
// one explicit grant naming exactly those destinations. Nothing here decides
// whether a destination SHOULD be removed: that decision belongs to the Control
// Plane that authorized the request, and this seam only asks the kernel to carry
// it out and report what actually happened.
func Delete(ctx context.Context, root string, targets []string) Applied {
	canonicalTargets := canonicalise(targets)
	if len(canonicalTargets) == 0 {
		return refusedApply(kernel.FailureInvalidSpec,
			"deletion requires at least one target; an empty target set removes nothing and proves nothing")
	}
	if len(canonicalTargets) > MaxAppliedTargets {
		return refusedApply(kernel.FailureBudgetExhausted,
			fmt.Sprintf("delete target set has %d entries, above the declared bound of %d",
				len(canonicalTargets), MaxAppliedTargets))
	}

	executionID := deleteExecutionIDFor(root, canonicalTargets)

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

	engine, err := kernel.NewEngine(registry, kernel.WithVerifier(absenceVerifier(caps.Root())))
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureCapabilityUnavailable,
			fmt.Sprintf("kernel engine unavailable: %v", err))
	}

	grant, err := kernel.NewGrant(deleteGrantID, []kernel.CapabilityID{kernel.FileDelete}, canonicalTargets)
	if err != nil {
		return refusedApplyFor(executionID, kernel.FailureAuthorization, err.Error())
	}

	spec := deletionSpec(executionID, canonicalTargets, caps.Root())
	if err := engine.Open(spec, grant); err != nil {
		return refusedApplyFor(executionID, kernel.ClassOf(err),
			fmt.Sprintf("kernel admission refused: %v", err))
	}

	result := engine.Run(ctx)
	return Applied{
		Observation: collect(result, engine.Log().Events(), executionID, canonicalTargets),
		Contract:    spec.Contract.Kind,
	}
}

// deletionSpec builds the immutable description of one DELETE execution.
//
// Each destination gets exactly one step. Unlike a write, a delete does not need
// a preceding observation to be interpretable: the capability's verdict already
// distinguishes "was there and removed" (PASS) from "was already gone" (NO_OP),
// and the evidence kinds FILE_DELETED and FILE_ABSENT carry that distinction
// forward. Adding an observation step would record the same fact twice without
// changing the verdict.
func deletionSpec(executionID string, targets []string, root string) kernel.Spec {
	program := make(kernel.Program, 0, len(targets))
	for i, target := range targets {
		program = append(program, kernel.Step{
			ID:         fmt.Sprintf("delete-%d", i+1),
			Capability: kernel.FileDelete,
			Target:     target,
			Args:       map[string]string{},
			Note:       "remove workspace target",
		})
	}
	return kernel.Spec{
		ExecutionID: executionID,
		Objective: fmt.Sprintf("remove %d workspace target(s) under %s",
			len(targets), root),
		Contract: kernel.Contract{
			Kind:                 kernel.ContractDelete,
			Targets:              targets,
			RequiresVerification: true,
		},
		Program: program,
		Budget: kernel.Budget{
			MaxSteps:              len(targets),
			MaxStepsPerCapability: len(targets),
		},
	}
}

// ── Verification ─────────────────────────────────────────────────────────────

// absenceVerifier is the verification seam for a DELETE execution.
//
// A delete's obligation is "the target is not there now", and the only honest
// way to check that is to look again, through code that did not perform the
// removal. Presence and absence must agree with the log on both sides: a log
// that says "absent" about a file that is now present describes an observation
// that does not describe the filesystem, and passing it would hide exactly the
// disagreement a verifier exists to catch.
func absenceVerifier(root string) kernel.Verifier {
	return kernel.VerifierFunc(func(ctx context.Context, req kernel.VerificationRequest) (kernel.Verdict, error) {
		if req.Contract.Kind != kernel.ContractDelete || len(req.Targets) == 0 {
			return kernel.VerdictNotApplicable, nil
		}
		recorded := recordedAbsence(req.Evidence)
		for _, target := range req.Targets {
			if err := ctx.Err(); err != nil {
				return kernel.VerdictUnknown, err
			}
			if !recorded[canonical(target)] {
				// The contract declared an absence obligation for this target and
				// the log records no observation for it. Refusing is the only
				// truthful verdict: there is nothing to agree with.
				return kernel.VerdictFail, nil
			}
			_, fact, cause := reRead(root, target)
			switch fact {
			case diskAbsent:
				continue
			case diskReadable:
				// It is still there. The delete did not take effect, whatever the
				// capability reported.
				return kernel.VerdictFail, nil
			default:
				// There is no answer about this destination either way. The cause
				// is surfaced so "the workspace could not be read" stays
				// distinguishable from "the target is still there" — both fail,
				// and an operator needs to know which happened.
				return kernel.VerdictFail, cause
			}
		}
		return kernel.VerdictPass, nil
	})
}

// recordedAbsence indexes the absence observations by destination. A target
// observed more than once has a current truth, so the LAST record wins.
func recordedAbsence(evidence []kernel.Evidence) map[string]bool {
	out := make(map[string]bool, len(evidence))
	for _, e := range evidence {
		if e.Target == "" {
			continue
		}
		switch e.Capability {
		case kernel.FileDelete, kernel.FileExists:
		default:
			continue
		}
		if e.Kind == kernel.EvidenceFileAbsent {
			out[canonical(e.Target)] = true
		}
	}
	return out
}

// ── Naming ───────────────────────────────────────────────────────────────────

// deleteExecutionIDFor derives a deterministic execution name from the request.
func deleteExecutionIDFor(root string, targets []string) string {
	h := sha256.New()
	h.Write([]byte("delete"))
	h.Write([]byte{0})
	h.Write([]byte(root))
	for _, t := range targets {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return "delete-" + hex.EncodeToString(h.Sum(nil))[:16]
}
