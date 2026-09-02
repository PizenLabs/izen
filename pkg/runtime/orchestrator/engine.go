package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/pkg/runtime/authorization"
	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
)

// Orchestrator is the control-plane orchestrator. It owns the deterministic
// control-loop pipeline and the fail-safe rollback guarantee: a cycle either
// commits atomically through the executor or leaves the workspace untouched.
type Orchestrator struct {
	preflight *preflight.PreflightEngine
	validator *executor.ProposalValidator
	executor  *executor.RuntimeExecutor
	gate      *authorization.ApprovalGate
}

// NewOrchestrator returns an Orchestrator wired to the given engines.
func NewOrchestrator(
	pf *preflight.PreflightEngine,
	val *executor.ProposalValidator,
	exec *executor.RuntimeExecutor,
	gate *authorization.ApprovalGate,
) *Orchestrator {
	return &Orchestrator{preflight: pf, validator: val, executor: exec, gate: gate}
}

// RunCycle executes one full control-loop cycle:
//
//  1. Preflight: compile the raw intent into a CompiledRequest.
//  2. Non-deterministic proposal: ask the provider for a ProposedMutation.
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

	// Step 2: Non-deterministic proposal (LLM stage).
	proposal, err := provider.GenerateProposal(ctx, compiled)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: generate proposal: %w", err)
	}
	if proposal == nil {
		return nil, errors.New("orchestrator: proposal provider returned a nil proposal")
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
	}

	// Step 7: Atomic execution or abort.
	switch action {
	case authorization.ActionExecute:
		if err := o.executor.Commit(*proposal, backup); err != nil {
			return nil, fmt.Errorf("orchestrator: commit: %w", err)
		}
		result.Committed = true
		return result, nil
	case authorization.ActionInspect:
		// Inspection was authorized but not execution; the workspace is left
		// untouched.
		return result, nil
	default:
		// ActionCancel (or any rejected decision) aborts without modifying
		// the workspace.
		return result, ErrExecutionRejected
	}
}
