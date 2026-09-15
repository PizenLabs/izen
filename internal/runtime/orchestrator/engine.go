package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/domain"
	coreauth "github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/llm"
	providercap "github.com/PizenLabs/izen/internal/provider"
	"github.com/PizenLabs/izen/internal/runtime/authorization"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
)

// isTruncationError reports whether err signals finish_reason="length"
// across ALL provider tiers (llm, ai, or string dialect). Truncation maps to
// the universal PARTIAL outcome, never a terminal execution error, and the
// canonical partial buffers are preserved (never cleared/swallowed).
func isTruncationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrPayloadTruncated) || errors.Is(err, ai.ErrPayloadTruncated) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "finish_reason=length") ||
		strings.Contains(msg, "finish_reason\"=\"length\"") ||
		strings.Contains(msg, "errpayloadtruncated")
}

// partialResult builds the universal PARTIAL outcome for a truncated stream:
// Verdict=EvidenceState.PARTIAL, Status="PARTIAL" (provider.StreamPartial),
// Committed=false, no terminal error.
func partialResult() *ExecutionResult {
	return &ExecutionResult{
		Verdict: evidence.PARTIAL,
		Status:  string(providercap.StreamPartial),
	}
}

// Orchestrator is the control-plane orchestrator. It owns the deterministic
// control-loop pipeline and the fail-safe rollback guarantee: a cycle either
// commits atomically through the executor or leaves the workspace untouched.
type Orchestrator struct {
	preflight *preflight.PreflightEngine
	validator *executor.ProposalValidator
	executor  *executor.FileExecutor
	gate      *authorization.ApprovalGate
	// audit is the synchronous audit durability seam bound into the
	// Truthful State Transition evaluation. Nil disables audit gating
	// (legacy/harness mode). Wired via WithAuditFlusher.
	audit AuditFlusher
}

// NewOrchestrator returns an Orchestrator wired to the given engines.
func NewOrchestrator(
	pf *preflight.PreflightEngine,
	val *executor.ProposalValidator,
	exec *executor.FileExecutor,
	gate *authorization.ApprovalGate,
) *Orchestrator {
	return &Orchestrator{preflight: pf, validator: val, executor: exec, gate: gate}
}

// RunCycle executes one full control-loop cycle:
//
//  1. Preflight: compile the raw intent into a CompiledRequest.
//     1b. Fast-path static gate (synchronous, deterministic, bounded O(N) on
//     target depth): lexical target safety always runs; when
//     cfg.FastPathAuth is set the full static gate (capabilities → targets →
//     references → preflight) runs and drops unauthorized requests BEFORE any
//     model invocation. No network call is made on fast-path denial.
//  2. Non-deterministic proposal: ask the provider for a ProposedMutation.
//     The returned proposal is strictly UNTRUSTED.
//     2b. Execution-boundary sanitization: any authority override directive
//     smuggled in proposal bytes drops with ErrCapabilityDenied.
//  3. Deterministic validation: reject unsafe proposals before any approval
//     session or snapshot exists.
//  4. Approval session creation: open a fresh session and capture the
//     pre-mutation snapshot.
//  5. UI projection & arming: render the diff, then arm the session. The
//     state-machine arming invariant is enforced here: no authorization event
//     is evaluated before the diff has been rendered and the session
//     explicitly armed.
//  6. Authorization evaluation: wait for the user's ApprovalEvent and
//     evaluate it against the armed session.
//  7. Atomic execution or abort: ActionExecute commits atomically;
//     ActionCancel (or any non-execute decision) aborts without touching the
//     workspace.
//
// Reasoning-vs-authority separation is preserved: model output never grants
// authority. Downstream diagnostic/repair tasks may still invoke reasoning;
// security is enforced at the execution boundary (RuntimeExecutor +
// fast-path gate), never by stripping model capabilities.
func (o *Orchestrator) RunCycle(ctx context.Context, req preflight.PreflightRequest, provider ProposalProvider, ui UIProjectionBridge, cfg OrchestratorConfig) (*ExecutionResult, error) {
	if o == nil {
		return nil, errors.New("orchestrator: nil Orchestrator")
	}
	if o.preflight == nil {
		return nil, errors.New("orchestrator: no preflight engine wired")
	}
	if o.validator == nil {
		return nil, errors.New("orchestrator: no proposal validator wired")
	}
	if o.executor == nil {
		return nil, errors.New("orchestrator: no runtime executor wired")
	}
	if o.gate == nil {
		return nil, errors.New("orchestrator: no approval gate wired")
	}
	if provider == nil {
		return nil, errors.New("orchestrator: no proposal provider")
	}
	if ui == nil {
		return nil, errors.New("orchestrator: no UI projection bridge")
	}

	// Step 1: Preflight (deterministic).
	if req.TokenBudget <= 0 {
		req.TokenBudget = cfg.TokenBudget
	}
	compiled, err := o.preflight.Execute(req)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: preflight: %w", err)
	}

	// Step 1a: Zero Side-Effect for Ask. When static auth evidence declares
	// intent=ask, the cycle short-circuits to a purely read-only response:
	// no provider invocation, no patch staging, no checkpoint/snapshot, no
	// diff preview, no preflight barrier wait, no AST verification loop.
	// Provider/model mismatch still fails fast BEFORE the short-circuit so
	// misconfiguration never silently degrades to a timeout loop.
	if cfg.FastPathAuth != nil && cfg.FastPathAuth.Objective.Intent.Kind == domain.IntentAsk {
		if err := executor.ValidateProviderModel(cfg.FastPathAuth.Provider, cfg.FastPathAuth.Model); err != nil {
			return nil, err
		}
		// Read-only code evidence in ASK stream output is PERMITTED; disk
		// mutation stays denied (Committed=false, no executor call).
		return &ExecutionResult{
			ProposalID: "",
			Target:     "",
			Action:     authorization.ActionNone,
			Committed:  false,
			Verdict:    evidence.VerdictPassed,
			Status:     string(providercap.StreamComplete),
		}, nil
	}

	// Step 1b: Synchronous deterministic fast-path gate BEFORE any model
	// invocation. Lexical target safety always runs (bounded O(N) on target
	// depth); the full static gate runs when cfg.FastPathAuth is set.
	// Denial here prevents any LLM network call, saving tokens and latency.
	if fastErr := o.fastPathBeforeModel(ctx, req, compiled, cfg); fastErr != nil {
		return nil, fastErr
	}

	// Step 2: Non-deterministic proposal (LLM stage). The proposal is strictly
	// untrusted and carries zero authority.
	//
	// Universal Stream Outcome Invariant: finish_reason="length" maps to
	// PARTIAL (EvidenceState.PARTIAL) across ALL provider tiers. It returns
	// a PARTIAL status with the preserved partial buffers, never a terminal
	// execution error and never a cleared/swallowed buffer.
	proposal, err := provider.GenerateProposal(ctx, compiled)
	if err != nil {
		if isTruncationError(err) {
			return partialResult(), nil
		}
		return nil, fmt.Errorf("orchestrator: generate proposal: %w", err)
	}
	if proposal == nil {
		return nil, errors.New("orchestrator: proposal provider returned a nil proposal")
	}

	// Step 2b: Execution-boundary sanitization. Authority override directives
	// smuggled in model output are dropped unconditionally with
	// ErrCapabilityDenied — model bytes never grant authority.
	if sanErr := executor.SanitizeUntrustedPayload(proposal.RawPatch); sanErr != nil {
		return nil, fmt.Errorf("%w: %w: %s", ErrProposalValidationFailed, coreauth.ErrCapabilityDenied, sanErr.Error())
	}
	if proposal.TargetRef != nil {
		if sanErr := executor.SanitizeUntrustedPayload(proposal.TargetRef.Canonical); sanErr != nil {
			return nil, fmt.Errorf("%w: %w: %s", ErrProposalValidationFailed, coreauth.ErrCapabilityDenied, sanErr.Error())
		}
		if sanErr := executor.SanitizeUntrustedPayload(proposal.TargetRef.Raw); sanErr != nil {
			return nil, fmt.Errorf("%w: %w: %s", ErrProposalValidationFailed, coreauth.ErrCapabilityDenied, sanErr.Error())
		}
	}

	// Step 3: Deterministic validation. Failure halts the cycle before any
	// approval session or snapshot exists, preserving the fail-safe rollback
	// guarantee.
	valRes, err := o.validator.Validate(*proposal)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: validate proposal: %w", err)
	}
	if !valRes.Valid {
		return nil, fmt.Errorf("%w: %s", ErrProposalValidationFailed, valRes.ErrorReason)
	}

	// Step 4: Approval session creation and snapshot backup.
	session := o.gate.NewSession(proposal.ProposalID, authorization.ActionExecute)
	backup, err := o.executor.PrepareSnapshot(proposal.TargetRef.Canonical)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: prepare snapshot: %w", err)
	}

	// Step 5: UI projection and gate arming. Arming strictly follows
	// rendering; only after arming may authorization events be evaluated.
	if err := ui.RenderProposal(valRes.Evidence, cfg.ViewportConfig); err != nil {
		return nil, fmt.Errorf("orchestrator: render proposal: %w", err)
	}
	if err := o.gate.ArmSession(session.Epoch); err != nil {
		return nil, fmt.Errorf("orchestrator: arm session: %w", err)
	}
	ui.OnSessionArmed(session.Epoch)

	// Step 6: Authorization evaluation. An explicit ActionNone (the gate's
	// PTY buffer-bleeding mitigation) leaves the session armed and keeps the
	// cycle waiting for an explicit decision.
	var action authorization.ApprovalAction
	for {
		evt, err := ui.WaitForApproval(ctx)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: wait for approval: %w", err)
		}
		action, err = o.gate.Evaluate(evt)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: evaluate approval: %w", err)
		}
		if action != authorization.ActionNone {
			break
		}
	}

	result := &ExecutionResult{
		ProposalID: proposal.ProposalID,
		Target:     proposal.TargetRef.Canonical,
		Action:     action,
		Evidence:   valRes.Evidence,
		Status:     string(providercap.StreamComplete),
	}

	// Step 7: Atomic execution or abort.
	switch action {
	case authorization.ActionExecute:
		if err := o.executor.Commit(*proposal, backup); err != nil {
			return nil, fmt.Errorf("orchestrator: commit: %w", err)
		}
		result.Committed = true
		// Step 8: Truthful State Transition finalization — the blocking,
		// synchronous audit flush. A flush failure MUST NOT roll back the
		// disk mutation (Mutation Non-Rollback Isolation) but it MUST
		// invalidate success: Completed=false, Verdict=Failed with
		// ErrAuditPersistenceFailed. The error is propagated, never
		// swallowed.
		if auditErr := o.finalizeAudit(result, true); auditErr != nil {
			return result, auditErr
		}
		return result, nil
	case authorization.ActionInspect:
		// Inspection was authorized but not execution; the workspace is left
		// untouched. Session finalization still flushes audit synchronously;
		// a flush failure compromises the terminal even without a mutation.
		if auditErr := o.finalizeAudit(result, false); auditErr != nil {
			return result, auditErr
		}
		return result, nil
	default:
		// ActionCancel (or any rejected decision) aborts without modifying
		// the workspace. The terminal still finalizes audit durability; a
		// flush failure is joined with the rejection so errors.Is finds
		// ErrAuditPersistenceFailed while the rejection stays observable.
		if auditErr := o.finalizeAudit(result, false); auditErr != nil {
			return result, errors.Join(ErrExecutionRejected, auditErr)
		}
		return result, ErrExecutionRejected
	}
}
