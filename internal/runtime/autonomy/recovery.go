package autonomy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── Zero-Trust Recovery Matrix (Invariants I1–I5) ──────────────────────────
//
// This file REPLACES the legacy trial-and-error recovery model (generic
// failure class → "retry_with_evidence" re-scope loop). Retryability is now a
// function of the canonical FAILURE SUBTYPE, never of a generic execution
// error (I4):
//
//	Subtype              Retry policy
//	──────────────────   ──────────────────────────────────────────────────
//	OutputExhausted      ONE typed contract transition FULL_REWRITE →
//	                     BOUNDED_PATCH (I3: atomic context rebuild + new
//	                     causal contract + fresh workspace version). A second
//	                     exhaustion STRICTLY HALTS (I1) — no relabeled retry.
//	SchemaViolation      Same single typed transition when the attempt is
//	                     still on the full-artifact protocol; afterwards one
//	                     bounded re-issue per remaining recovery cycle
//	                     (rotated window = material change).
//	TransportError       Bounded transport re-execution preserving contract
//	                     identity (same prompt/evidence/strategy ⇒ same
//	                     ContractID, AttemptID increments at admission).
//	ProviderRefusal      Halt — ask the human. Refusal is never retried.
//	PreflightInfeasible  Halt BEFORE any provider request (I5) — explicit
//	                     human re-scope required; intent is never altered.
//	WorkspaceDrift       Abort — the mutation geometry moved between attempts.
//	MutationFailure      Halt — an apply/verify gate already ran; auto-retry
//	                     over rolled-back ground is prohibited.

// FailureSubtype is the I4 retryability key derived from a bounded
// observation. It is deterministic: identical observations always classify
// identically.
type FailureSubtype string

const (
	// SubtypeOutputExhausted: finish_reason=length (Boundary 3 circuit break).
	SubtypeOutputExhausted FailureSubtype = "output_exhausted"
	// SubtypeSchemaViolation: artifact failed hunk-schema/syntax validation.
	SubtypeSchemaViolation FailureSubtype = "schema_violation"
	// SubtypeTransportError: invocation-level transport failure.
	SubtypeTransportError FailureSubtype = "transport_error"
	// SubtypeProviderRefusal: provider refused or filtered generation.
	SubtypeProviderRefusal FailureSubtype = "provider_refusal"
	// SubtypePreflightInfeasible: Boundary-2 budget refusal (zero requests).
	SubtypePreflightInfeasible FailureSubtype = "preflight_infeasible"
	// SubtypeWorkspaceDrift: workspace version changed between attempts.
	SubtypeWorkspaceDrift FailureSubtype = "workspace_drift"
	// SubtypeMutationFailure: apply/verify gates ran and failed.
	SubtypeMutationFailure FailureSubtype = "mutation_failure"
	// SubtypeNoOpObjectiveUnresolved: a NO_CHANGES_REQUIRED claim was
	// contradicted by deterministic structural analysis — escalation trigger.
	SubtypeNoOpObjectiveUnresolved FailureSubtype = "no_op_objective_unresolved"
)

// ErrRecoveryHalted is returned by typedRepair when the zero-trust matrix
// forbids any further continuation. The driver converges to its terminal /
// human boundary instead of re-scoping.
var ErrRecoveryHalted = errors.New("recovery halted by the zero-trust matrix")

// RecoverySubtype maps an observation onto its I4 failure subtype. Success and
// human-gate outcomes are not failures; they classify to "" and never reach
// the recovery path.
func RecoverySubtype(o autonomy.Observation) FailureSubtype {
	switch o.Outcome {
	case autonomy.OutcomeTruncated:
		return SubtypeOutputExhausted
	case autonomy.OutcomeArtifactRetryableRejected:
		return SubtypeSchemaViolation
	case autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed, autonomy.OutcomePatchFailed:
		if o.FinishReason != "" {
			switch execution.NormalizeFinishReason(o.FinishReason) {
			case execution.CanonicalOutputExhausted:
				return SubtypeOutputExhausted
			case execution.CanonicalProviderRefusal:
				return SubtypeProviderRefusal
			}
		}
		return SubtypeTransportError
	case autonomy.OutcomeArtifactRejected:
		return SubtypeSchemaViolation
	case autonomy.OutcomeApplyFailed, autonomy.OutcomeVerifyFailed, autonomy.OutcomeSkipped:
		return SubtypeMutationFailure
	case autonomy.OutcomePreflightInfeasible:
		return SubtypePreflightInfeasible
	case autonomy.OutcomeWorkspaceDrift:
		return SubtypeWorkspaceDrift
	case autonomy.OutcomeNoOpObjectiveUnresolved:
		return SubtypeNoOpObjectiveUnresolved
	default:
		return ""
	}
}

// transitionAvailable reports whether the I3 typed transition
// (FULL_REWRITE → BOUNDED_PATCH) is still available for this lineage. The
// observation's own RecoveryStrategy is the latch: once bounded_patch is set,
// the transition has been consumed.
func transitionAvailable(o autonomy.Observation) bool {
	return o.RecoveryStrategy != autonomy.StrategyBoundedPatch
}

// DecideRecovery is the runtime-owned decision of the zero-trust recovery
// matrix. It returns the loop decision for a FAILED observation:
//
//	I1  OUTPUT_EXHAUSTED with the transition consumed → AskHuman (strict halt)
//	I3  OUTPUT_EXHAUSTED with the transition available → Repair (typed)
//	I4  TransportError → bounded Retry, everything else → human or abort
//	I5  PreflightInfeasible → AskHuman (explicit re-scope; never silent)
//	B5  WorkspaceDrift → Abort
func DecideRecovery(o autonomy.Observation, b autonomy.LoopBounds) autonomy.LoopDecision {
	// HARD-BLOCK: FormatFailureCount >=2 or Ambiguous == true → park at DecisionSurface awaiting_human
	// Do NOT issue a re-scoped [bounded_patch] retry. Immediately park.
	if o.AttemptNum >= 2 || o.RecoveryCycle >= 2 {
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "hard-block: format failures >=2 — park at DecisionSurface awaiting_human, no bounded_patch retry"}
	}
	if o.ClarificationRequired || strings.Contains(strings.ToLower(o.Diagnostic), "ambiguous") {
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "hard-block: ambiguous — park at DecisionSurface awaiting_human, no bounded_patch retry"}
	}
	sub := RecoverySubtype(o)
	attemptsLeft := b.MaxAttempts <= 0 || o.AttemptNum < b.MaxAttempts

	switch sub {
	case SubtypeOutputExhausted:
		if !transitionAvailable(o) {
			return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
				Reason: "invariant I1: output exhausted twice — strict halt, manual re-scope required"}
		}
		return autonomy.LoopDecision{Action: autonomy.LoopRepair,
			Reason: "typed transition FULL_REWRITE -> BOUNDED_PATCH after output exhaustion"}
	case SubtypeSchemaViolation:
		if !transitionAvailable(o) {
			cyclesLeft := b.MaxRecoveryCycles <= 0 || o.RecoveryCycle < b.MaxRecoveryCycles
			if !cyclesLeft {
				return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
					Reason: "bounded-patch schema violations exhausted the recovery cycles — ask human"}
			}
			return autonomy.LoopDecision{Action: autonomy.LoopRepair,
				Reason: "one bounded patch re-issue with a rotated context window"}
		}
		return autonomy.LoopDecision{Action: autonomy.LoopRepair,
			Reason: "typed transition FULL_REWRITE -> BOUNDED_PATCH after schema violation"}
	case SubtypeTransportError:
		cyclesLeft := b.MaxRecoveryCycles <= 0 || o.RecoveryCycle < b.MaxRecoveryCycles
		if !cyclesLeft {
			return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
				Reason: "transport failures exhausted the recovery cycles — ask human"}
		}
		if attemptsLeft {
			return autonomy.LoopDecision{Action: autonomy.LoopRepair,
				Reason: "bounded transport re-execution under the SAME contract identity"}
		}
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "transport failures exhausted the attempts — ask human"}
	case SubtypeProviderRefusal:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort,
			Reason: "provider refused generation — not retryable"}
	case SubtypePreflightInfeasible:
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "invariant I5: preflight infeasible — explicit re-scope required (intent unchanged)"}
	case SubtypeWorkspaceDrift:
		return autonomy.LoopDecision{Action: autonomy.LoopAbort,
			Reason: "workspace version changed between attempts — aborting stale run"}
	case SubtypeNoOpObjectiveUnresolved:
		cyclesLeft := b.MaxRecoveryCycles <= 0 || o.RecoveryCycle < b.MaxRecoveryCycles
		if !cyclesLeft {
			return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
				Reason: "no-op claim still conflicts with structural evidence after re-hydration — ask human"}
		}
		return autonomy.LoopDecision{Action: autonomy.LoopRepair,
			Reason: "NO-OP escalation: re-hydrated judgment over a broader boundary window"}
	case SubtypeMutationFailure:
		return autonomy.LoopDecision{Action: autonomy.LoopAskHuman,
			Reason: "mutation gate failed — no automatic retry over rolled-back ground"}
	default:
		return autonomy.RecoverFailure(o, autonomy.ClassifyOutcome(o.Outcome), b)
	}
}

// typedRepair re-scopes a failed request for exactly ONE materially-changed,
// subtype-driven continuation. It is the replacement of the legacy
// trial-and-error defaultRepair: every repair it emits carries an explicit
// recovery strategy, causal contract lineage, diagnostic-only evidence
// (Recovery Isolation, I2), and the UNCHANGED workspace version (B5). It
// refuses to fabricate a continuation when the matrix says halt.
//
// Material-change guarantees:
//   - OutputExhausted / SchemaViolation transitions switch the artifact
//     protocol to bounded_patch AND append the failure's DiagnosticSignal as
//     evidence — a new contract identity with ParentContractID back-pointer.
//   - SchemaViolation re-issues (already bounded) rotate the context window
//     via the incremented attempt number.
//   - TransportError changes NOTHING material: identical prompt/evidence/
//     strategy so the executor's admission resolves the SAME ContractID and
//     deterministically increments AttemptID.
func typedRepair(o autonomy.Observation, req autonomy.LoopRequest) (autonomy.LoopRequest, error) {
	// HARD-BLOCK: FormatFailureCount >=2 or Ambiguous → no bounded_patch retry
	if o.AttemptNum >= 2 || o.RecoveryCycle >= 2 {
		return req, fmt.Errorf("%w: hard-block format failures >=2 for %s — park at DecisionSurface awaiting_human", ErrRecoveryHalted, o.Target)
	}
	if o.ClarificationRequired || strings.Contains(strings.ToLower(o.Diagnostic), "ambiguous") {
		return req, fmt.Errorf("%w: hard-block ambiguous for %s — park at DecisionSurface awaiting_human", ErrRecoveryHalted, o.Target)
	}
	sub := RecoverySubtype(o)
	target := o.Target
	if target == "" {
		target = firstTarget(req.Targets)
		if target == "" {
			target = req.Target
		}
	}
	attempt := o.AttemptNum + 1
	if attempt < 1 {
		attempt = req.RecoveryAttempt + 1
		if attempt < 1 {
			attempt = 1
		}
	}

	next := req
	next.RequestID = req.RequestID
	next.FinishReason = o.FinishReason
	// BOUNDARY 5: the workspace version is carried through UNCHANGED — if the
	// workspace moves between attempts the adapter aborts before executing.
	// The repair NEVER refreshes it silently.
	next.WorkspaceDigest = req.WorkspaceDigest

	switch sub {
	case SubtypeOutputExhausted:
		if !transitionAvailable(o) {
			return req, fmt.Errorf("%w: output exhausted twice for %s (attempt %d)",
				ErrRecoveryHalted, target, o.AttemptNum)
		}
		next.RecoveryStrategy = autonomy.StrategyBoundedPatch
		next.RecoveryAttempt = attempt
		next.RecoveryReason = fmt.Sprintf(
			"output_budget_exhausted: truncated target %s budget=%d finish_reason=%s",
			target, o.MaxOutputTokens, o.FinishReason)
		next.Evidence = joinEvidence(req.Evidence, fmt.Sprintf(
			"[DIAGNOSTIC subtype=OUTPUT_EXHAUSTED boundary=B3-output-gate target=%s budget=%d finish_reason=%s] "+
				"Partial generation discarded at the output gate; produce a bounded SEARCH/REPLACE patch instead.",
			target, o.MaxOutputTokens, o.FinishReason))
		// CAUSAL RECOVERY: continue the FAILED contract's lineage. The
		// executor resolves the new (materially different) identity into a
		// causally linked contract with the parent back-pointer (I3).
		if o.ContractID != "" {
			next.ParentContractID = o.ContractID
		}
		return next, nil

	case SubtypeSchemaViolation:
		if transitionAvailable(o) {
			next.RecoveryStrategy = autonomy.StrategyBoundedPatch
			next.ParentContractID = o.ContractID
		}
		next.RecoveryAttempt = attempt
		next.RecoveryReason = fmt.Sprintf(
			"schema_violation: artifact rejected for %s (attempt %d) — rotated bounded window",
			target, o.AttemptNum)
		// AST-AWARE RETRY CONTEXT: a structural audit rejection (unterminated
		// <script>, unbalanced HTML) is injected as the exact [CONTRACT FAILURE]
		// Line <N>: <ParseError> directive — never the rejected raw code — so
		// the successor attempt fixes the real defect at its precise line.
		audit := execution.StructuralAuditDirective(o.Diagnostic)
		next.Evidence = joinEvidence(req.Evidence, fmt.Sprintf(
			"[DIAGNOSTIC subtype=SCHEMA_VIOLATION boundary=B4-artifact-gate target=%s] %s "+
				"Produce exactly one anchored SEARCH/REPLACE block.",
			target, audit))
		if next.ParentContractID == "" && o.ContractID != "" && next.RecoveryStrategy == autonomy.StrategyBoundedPatch {
			next.ParentContractID = o.ContractID
		}
		return next, nil

	case SubtypeTransportError:
		// PURE RETRY: nothing material changes — the executor derives the
		// same contract identity and increments the attempt counter there.
		next.RecoveryAttempt = attempt
		next.RecoveryReason = fmt.Sprintf("transport retry for %s (attempt %d)", target, attempt)
		return next, nil

	case SubtypeNoOpObjectiveUnresolved:
		// NO-OP ESCALATION: materially different input — a BROADER boundary
		// window plus elevated structural evidence — so the re-hydrated
		// judgment sees what the contradicted claim denied.
		next.NoOpEscalation = true
		next.ParentContractID = o.ContractID
		next.RecoveryAttempt = attempt
		next.RecoveryReason = fmt.Sprintf(
			"noop_escalation: prior NO_CHANGES_REQUIRED claim for %s conflicted with structural evidence", target)
		next.Evidence = joinEvidence(req.Evidence, fmt.Sprintf(
			"[DIAGNOSTIC subtype=NO_OP_OBJECTIVE_UNRESOLVED boundary=B4-noop-semantics target=%s] "+
				"The previous claim was CONTRADICTED by structural analysis; re-judge over the widened window.",
			target))
		return next, nil

	default:
		return req, fmt.Errorf("%w: subtype %q is not automatically recoverable", ErrRecoveryHalted, sub)
	}
}

// joinEvidence appends a bounded advisory line to the evidence ledger. Only
// DiagnosticSignal-class metadata ever enters evidence — rejected artifact
// bytes are structurally excluded by construction (the caller cannot pass
// them: the signature accepts strings assembled from signals alone).
func joinEvidence(existing, signal string) string {
	if existing == "" {
		return signal
	}
	return existing + "\n" + signal
}
