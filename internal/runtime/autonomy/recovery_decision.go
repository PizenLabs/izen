package autonomy

// ── EVIDENCE-DRIVEN RECOVERY ────────────────────────────────────────────────
//
// This file is the Control Plane's answer to "what may happen next?", and it is
// deliberately NOT a function of (Outcome, bounds).
//
// THE SIGNATURE THAT WAS WRONG. Recovery used to be `DecideRecovery(o, b)` —
// one observation and the loop bounds. Everything the runtime had learned about
// the OBJECTIVE was absent from the decision: which requirements are already
// discharged, which conditions still hold, what the last attempt actually
// failed at, and whether the identical failure had already happened. The matrix
// could therefore not tell "try again" from "this strategy has no new evidence",
// because the difference lived in state it was never given.
//
// THE INPUTS NOW. A RecoveryInput carries, from five independent owners:
//
//	ObjectiveProgress   the objective lifecycle's own state (objective_lifecycle.go)
//	Evidence            the runtime's accumulated observation + capability evidence
//	Failure             the STRUCTURED classification of this attempt
//	Remaining           the unmet conditions and undischarged requirements
//	Bounds              the runtime-owned termination bounds
//
// No owner supplies another's field, and nothing here re-derives a fact another
// component owns. `DecideRecovery` is retained as the BOUNDS-ONLY compatibility
// entry point so every existing caller and test keeps its exact behaviour; the
// driver calls DecideRecoveryWith because only the driver can assemble the
// objective half.

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// RecoveryInput is the complete, evidence-bearing input to one recovery
// decision. Every field is owned by a different component; none is a guess.
type RecoveryInput struct {
	// Observation is the bounded observation of the attempt that failed.
	Observation autonomy.Observation
	// Bounds are the runtime-owned termination bounds.
	Bounds autonomy.LoopBounds
	// Failure is the structured classification of that observation. When empty it
	// is derived, so a caller that has not classified yet still gets the typed
	// answer rather than the legacy generic one.
	Failure execution.ExecutionFailure
	// Ledger is the objective's failure memory. Nil disables repeat detection,
	// which is correct for a single-shot caller and honest about it.
	Ledger *FailureLedger
	// Progress is the objective lifecycle's current state. The zero value means
	// "not yet adjudicated", which the decision treats as "nothing proven" — the
	// fail-closed reading.
	Progress execution.ObjectiveProgress
	// Continuation is the objective lifecycle's own next-action verdict. It is the
	// authority on whether another step is ADMISSIBLE at all.
	Continuation execution.Continuation
	// ObjectiveID is the identity every continuation must retain.
	ObjectiveID string
	// Scope is the AUTHORITATIVE resolved target set, verbatim. Recovery may
	// re-read it but never rewrite it.
	Scope []string
}

// recoveryAction is the closed vocabulary of what recovery may propose. It is a
// strict subset of the loop's actions, and the mapping from it to loop actions is
// total and visible in one switch.
type recoveryAction string

const (
	// recoveryContinue: another computation step is justified on the SAME
	// contract with the SAME approach.
	recoveryContinue recoveryAction = "CONTINUE"
	// recoveryReplan: the approach is unsupported by the evidence; a new plan is
	// required. The objective identity and contract are RETAINED.
	recoveryReplan recoveryAction = "REPLAN"
	// recoveryWaitForAuthorization: a human owns the decision.
	recoveryWaitForAuthorization recoveryAction = "WAIT_FOR_AUTHORIZATION"
	// recoveryFail: the evidence positively contradicts the objective.
	recoveryFail recoveryAction = "FAIL"
	// recoveryUnsubstantiate: the run stopped without proof. The honest terminal
	// truth: neither success nor a fabricated failure verdict.
	recoveryUnsubstantiate recoveryAction = "UNSUBSTANTIATED"
)

// String renders the canonical action label.
func (a recoveryAction) String() string { return string(a) }

// RecoveryDecision is the typed recovery verdict plus the evidence behind it.
type RecoveryDecision struct {
	// Action is what the Control Plane decided.
	Action recoveryAction
	// Reason is the deterministic justification, naming the class and the
	// evidence that produced it.
	Reason string
	// Failure is the classification the decision was made from.
	Failure execution.ExecutionFailure
	// ObjectiveID is the identity that MUST survive this decision. A replan keeps
	// it; only a fresh Run may mint a new one.
	ObjectiveID string
	// RetainsIdentity reports whether the next attempt keeps the same objective.
	// It is always true here — it exists so a trace can state the invariant
	// rather than leaving it implied.
	RetainsIdentity bool
}

// loopDecision projects the recovery verdict onto the loop's action vocabulary.
//
// The mapping is total, and every branch names WHY. Notably FAIL and
// UNSUBSTANTIATED are distinct: "the evidence says this cannot work" and "we
// could not prove it" are different truths and the loop must be able to report
// each one honestly.
func (d RecoveryDecision) loopDecision() autonomy.LoopDecision {
	switch d.Action {
	case recoveryContinue:
		return autonomy.LoopDecision{Action: autonomy.LoopContinue, Reason: d.Reason}
	case recoveryReplan:
		return autonomy.LoopDecision{Action: autonomy.LoopRepair, Reason: d.Reason}
	case recoveryWaitForAuthorization:
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman, Reason: d.Reason}
	case recoveryFail:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort, Reason: d.Reason}
	case recoveryUnsubstantiate:
		return autonomy.LoopDecision{Action: autonomy.LoopUnsubstantiate, Reason: d.Reason}
	default:
		// Fail-closed: an unrecognised recovery verdict may never authorise
		// another attempt. It terminates as unsubstantiated, which is a truthful
		// statement about the evidence rather than a claim about the model.
		return autonomy.LoopDecision{Action: autonomy.LoopUnsubstantiate,
			Reason: "recovery produced no admissible action for this failure; the objective is unsubstantiated"}
	}
}

// DecideRecoveryWith is the objective-aware recovery decision.
//
// Its precedence is the whole design:
//
//	1  the objective lifecycle says COMPLETE         → the authority already
//	    decided; recovery is not consulted
//	2  the objective lifecycle says WAIT             → a human gate owns it
//	3  the failure repeats with no new evidence      → REPLAN or UNSUBSTANTIATE
//	    (never another identical attempt)
//	4  the failure's own policy forbids a retry      → REPLAN / WAIT / UNSUBSTANTIATE
//	5  otherwise                                     → the legacy bounded matrix,
//	    which is now reached only for genuinely retryable failures
//
// Step 3 is the fix for the repeated-request defect: it fires before any
// subtype-specific branch, so no failure can reach a "try again" path twice with
// the same evidence behind it.
func DecideRecoveryWith(in RecoveryInput) RecoveryDecision {
	f := in.Failure
	if f.Class == execution.FailureNone {
		f = ClassifyObservation(in.Observation)
	}
	out := RecoveryDecision{
		Failure:         f,
		ObjectiveID:     in.ObjectiveID,
		RetainsIdentity: true,
	}

	// 1/2 — The objective lifecycle is authoritative when it has adjudicated.
	switch in.Continuation.Decision {
	case execution.Complete:
		out.Action = recoveryContinue
		out.Reason = "objective lifecycle reports PROVEN; recovery is not consulted"
		return out
	case execution.WaitForAuthorization:
		out.Action = recoveryWaitForAuthorization
		out.Reason = "objective lifecycle reports REQUIRES_AUTHORIZATION: a human gate owns the decision"
		return out
	}

	// 3 — NON-PROGRESSING EXECUTION. The runtime already has this exact failure,
	// under this exact evidence. This is the branch the previous matrix
	// structurally could not take: it had counters but no record of WHAT failed.
	//
	// WHAT HAPPENS NEXT DEPENDS ON THE CLASS'S OWN POLICY:
	//
	//	RetryForbidden → terminate HERE. The failure is deterministic (a target
	//	                that does not exist, a provider refusal); re-issuing it
	//	                cannot change the answer, and no class breaker exists to
	//	                eventually say so.
	//	otherwise      → FALL THROUGH to the matrix, which owns each class's
	//	                established bound (the I3 one-transition latch, the
	//	                contract-recovery breaker, the anchor attempt limit).
	//	                The ledger does NOT impose a uniform count of its own:
	//	                overriding a per-class breaker with a generic "seen twice"
	//	                rule would silently change what those breakers mean.
	//	                What the ledger adds is the TYPE — the repeat is recorded,
	//	                counted, and named in the reason, so the decision is
	//	                attributable rather than looking like a fresh failure.
	count, repeated := in.Ledger.Observe(f)
	f.Count = count
	out.Failure = f
	//
	// The `RetryForbidden` guard is expressed as a condition rather than a
	// one-armed switch: there is exactly one disposition here, and a switch with
	// a single case invites a second arm that was never reasoned about.
	if repeated && f.Class.RetryPolicy() == execution.RetryForbidden {
		out.Failure.Class = execution.FailureNonProgressing
		out.Action = recoveryUnsubstantiate
		out.Reason = fmt.Sprintf(
			"NON_PROGRESSING_EXECUTION: %s was observed %d time(s) under unchanged evidence "+
				"(policy=%s); another identical attempt cannot change the answer — "+
				"the objective is unsubstantiated",
			f.String(), count, f.Class.RetryPolicy())
		return out
	}

	// 4 — The failure's own policy. A class that forbids retrying is never
	// handed to the legacy matrix, which would otherwise see an ordinary
	// OutcomeFailed and reach a "bounded transport re-execution" branch.
	//
	// NOTE ON DISPOSITIONS. `RetryForbidden` means "another identical attempt is
	// not admissible". It does NOT mean "abort": the correct TERMINAL disposition
	// still differs per class, and each one below preserves the disposition the
	// matrix already established. Parking a mutation-gate failure at a human
	// boundary is a different decision from aborting a stale run, and conflating
	// them would be a regression in its own right.
	if f.Class.RetryPolicy() == execution.RetryForbidden {
		// Each disposition below preserves the one the matrix already
		// established for that class. They are deliberately not uniform.
		switch f.Class {
		case execution.FailureTargetNotFound, execution.FailureTargetIdentityMismatch:
			// The model asked for a target that is not in the resolved scope.
			// Nothing is substituted; the next planner invocation is told the
			// scope verbatim and asked to work within it.
			out.Action = recoveryReplan
			out.Reason = fmt.Sprintf(
				"%s: requested target %q is not in the authoritative scope [%s]; "+
					"no target was substituted — replanning against the resolved scope",
				f.Class, f.Target, strings.Join(in.Scope, ","))
			return out
		case execution.FailureStaleCandidate:
			// The workspace moved between binding and applying. The existing
			// matrix aborts: a stale attempt never re-executes over new ground.
			out.Action = recoveryFail
			out.Reason = f.Class.String() + ": " + f.Evidence + " — aborting a stale run rather than re-executing over moved ground"
			return out
		case execution.FailureMutationFailure:
			// An apply/verify gate already ran and failed. The ground was rolled
			// back; the existing matrix PARKS here, because a human deciding
			// whether to re-approve is a different authority from the loop
			// deciding to give up.
			out.Action = recoveryWaitForAuthorization
			out.Reason = f.Class.String() + ": " + f.Evidence + " — mutation gate failed; no automatic retry over rolled-back ground"
			return out
		case execution.FailureProviderRefusal:
			out.Action = recoveryFail
			out.Reason = "PROVIDER_REFUSAL: the provider refused generation; the same request will be refused again"
			return out
		case execution.FailurePreflightInfeasible:
			// Boundary-2 refused BEFORE any provider request. Only an explicit
			// human re-scope continues (invariant I5).
			out.Action = recoveryWaitForAuthorization
			out.Reason = "PREFLIGHT_INFEASIBLE: " + f.Evidence + " — explicit re-scope required (intent unchanged)"
			return out
		default:
			out.Action = recoveryUnsubstantiate
			out.Reason = f.Class.String() + ": " + f.Evidence
			return out
		}
	}

	// 5 — A failure whose class permits another attempt. The matrix owns the
	// detailed policy and each class's established bound; it is reached now only
	// for genuinely retryable failures, which is what keeps its transport-retry
	// branch honest.
	legacy := DecideRecovery(in.Observation, in.Bounds)
	out.Action = loopActionToRecovery(legacy.Action)
	out.Reason = legacy.Reason
	if repeated {
		// Name the repeat explicitly. The matrix's own reason explains WHAT it
		// decided; this states that the decision was informed by the runtime
		// having already seen this exact failure under this exact evidence.
		out.Reason = fmt.Sprintf("NON_PROGRESSING_EXECUTION: %s (seen %d time(s) under unchanged evidence) — %s",
			f.String(), count, out.Reason)
		return out
	}
	if f.Evidence != "" && !strings.Contains(out.Reason, f.Evidence) {
		out.Reason = fmt.Sprintf("%s [%s: %s]", legacy.Reason, f.Class, f.Evidence)
	}
	return out
}

// loopActionToRecovery projects a loop action back into the recovery vocabulary so
// the decision has ONE representation. An unknown loop action fails closed.
func loopActionToRecovery(a autonomy.LoopAction) recoveryAction {
	switch a {
	case autonomy.LoopContinue, autonomy.LoopRetry:
		return recoveryContinue
	case autonomy.LoopRepair:
		return recoveryReplan
	case autonomy.LoopAskHuman:
		return recoveryWaitForAuthorization
	case autonomy.LoopAbort:
		return recoveryFail
	case autonomy.LoopUnsubstantiate, autonomy.LoopComplete:
		return recoveryUnsubstantiate
	default:
		return recoveryUnsubstantiate
	}
}

// RecoveryReason renders the recovery verdict for a terminal reason string. It
// keeps the class, the evidence and the identity together so a human reading a
// park or an abort can see which failure caused it.
func RecoveryReason(d RecoveryDecision) string {
	var sb strings.Builder
	sb.WriteString(d.Action.String())
	if d.Failure.Class != execution.FailureNone {
		sb.WriteString(": ")
		sb.WriteString(string(d.Failure.Class))
	}
	if d.Failure.Target != "" {
		sb.WriteString(" target=")
		sb.WriteString(d.Failure.Target)
	}
	if d.ObjectiveID != "" {
		sb.WriteString(" objective=")
		sb.WriteString(d.ObjectiveID)
	}
	if d.Reason != "" {
		sb.WriteString(" — ")
		sb.WriteString(d.Reason)
	}
	return sb.String()
}
