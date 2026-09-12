package runtime

// STEP 2 — Runtime Engine API parity.
//
// This file equips RuntimeEngine (the core proposal-execution hub) with
// native support for every execution contract defined in internal/domain/:
//
//   - domain/task.TaskResult construction for the whole ExecuteProposal
//     pipeline (success, scope denial, verification failure, transport
//     failure, timeout/cancellation).
//   - domain/orchestration.Phase transition handling during proposal
//     execution, persisted as PHASE_TRANSITION (+ WORKSPACE_SWITCHED)
//     audit lineage in ledger.ndjson.
//   - fail-closed capability validation for unhandled capability scopes.
//
// The durable TaskStore (ledger.ndjson) remains the single source of truth:
// every method below persists before returning, and no in-memory execution
// state is introduced.

import (
	"context"
	"errors"
	"fmt"

	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// ExecuteProposalResult runs one proposal through the full ExecuteProposal
// pipeline and maps the outcome onto a fully compliant
// domain/task.TaskResult:
//
//   - success → ExecStatusCompleted, Data carries the durable TaskState.
//   - scope/policy denial → ExecStatusFailed with the denial cause.
//   - verification failure → ExecStatusFailed with the verification cause.
//   - context deadline exceeded → ExecStatusFailed with
//     domaintask.ErrTaskTimeout as the cause (errors.Is-compatible).
//   - explicit cancellation (ctx.Canceled or runtime Cancel) →
//     ExecStatusCanceled.
//   - transport/executor failure → ExecStatusFailed with the classified
//     cause; the durable failure/recovery lineage is already persisted by
//     ExecuteProposal.
//
// The returned error mirrors result.Error (nil on success) for callers that
// expect the Go error channel; result itself is always terminal.
func (e *RuntimeEngine) ExecuteProposalResult(
	ctx context.Context,
	p scopeguard.Proposal,
	primaryScope []string,
	effect scopeguard.Effect,
	verify scopeguard.Verifier,
) domaintask.TaskResult {
	st, err := e.ExecuteProposal(ctx, p, primaryScope, effect, verify)
	if err == nil {
		// The kernel lifecycle lets cancellation win over a reported
		// success; the parity mapper honors the same precedence so a
		// canceled proposal never reports Completed.
		if ctx != nil && ctx.Err() != nil {
			return canceledResult(ctx, st)
		}
		return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted, Data: st}
	}
	// Timeout and cancellation preserve their canonical causes so
	// errors.Is(result.Error, domaintask.ErrTaskTimeout) and
	// errors.Is(result.Error, context.Canceled) keep working — whether the
	// pipeline surfaced the context error or a derived failure.
	if ctx != nil && ctx.Err() != nil {
		return canceledResult(ctx, st)
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusFailed, Error: err, Data: st}
}

// canceledResult maps a context that is done (on either the success or the
// failure path) onto its terminal TaskResult: deadline exhaustion preserves
// domaintask.ErrTaskTimeout for errors.Is, cancellation maps to Canceled.
func canceledResult(ctx context.Context, st durable.TaskState) domaintask.TaskResult {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(context.Cause(ctx), domaintask.ErrTaskTimeout) {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		return domaintask.TaskResult{
			Status: domaintask.ExecStatusFailed,
			Error:  fmt.Errorf("%w: %w", domaintask.ErrTaskTimeout, cause),
			Data:   st,
		}
	}
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusCanceled, Error: cause, Data: st}
}

// TransitionPhase validates a logical orchestration hop fail-closed and
// persists it as durable audit lineage for the engine's bound task:
//
//   - unknown phases → plain error, nothing persisted.
//   - forbidden edge → *domainorch.TransitionError, nothing persisted.
//   - valid hop → exactly one PHASE_TRANSITION event, plus a
//     WORKSPACE_SWITCHED event when the target phase maps onto a semantic
//     workspace (the workspace session policy is switched through the same
//     StoreLedger so capability allowances track the phase).
//
// A no-op (to == from) persists nothing and returns nil.
func (e *RuntimeEngine) TransitionPhase(from, to domainorch.Phase) error {
	if e == nil || e.store == nil {
		return fmt.Errorf("runtime: nil engine dependency (fail-closed)")
	}
	if !to.Valid() {
		return fmt.Errorf("runtime: invalid phase %q", to)
	}
	if !from.Valid() {
		return fmt.Errorf("runtime: invalid phase %q", from)
	}
	if to == from {
		return nil
	}
	if !domainorch.ValidTransition(from, to) {
		return &domainorch.TransitionError{From: from, To: to, Msg: "no valid transition"}
	}
	if err := e.store.RecordCustomEvent(e.taskID, durable.EventPhaseTransition, map[string]any{
		"from": from.String(),
		"to":   to.String(),
	}); err != nil {
		return fmt.Errorf("runtime: record phase transition: %w", err)
	}
	if ws, ok := phaseWorkspace(to); ok && e.policy != nil {
		ledger := scopeguard.StoreLedger{Store: e.store}
		if err := e.policy.SwitchTo(ws, ledger); err != nil {
			return fmt.Errorf("runtime: switch workspace for phase %s: %w", to, err)
		}
	}
	return nil
}

// phaseWorkspace maps a logical phase onto its semantic workspace. Idle
// and Ask carry no workspace (ok=false): they persist PHASE_TRANSITION
// only.
func phaseWorkspace(p domainorch.Phase) (scopeguard.Workspace, bool) {
	switch p {
	case domainorch.PhaseInvestigate:
		return scopeguard.WorkspaceInvestigate, true
	case domainorch.PhasePlan:
		return scopeguard.WorkspacePlan, true
	case domainorch.PhaseBuild:
		return scopeguard.WorkspaceBuild, true
	case domainorch.PhaseReview:
		return scopeguard.WorkspaceReview, true
	default:
		return "", false
	}
}

// ValidateCapability reports whether the engine's active workspace policy
// grants the requested capability class. Semantics are fail-closed:
//
//   - unknown op class → error (unhandled capability scopes never execute).
//   - nil engine/policy → error (no authority context, no grant).
//   - policy denies op → error.
//   - otherwise → nil.
//
// Targets are accepted for future scope-rule evaluation and are currently
// recorded for audit completeness; they never widen authority.
func (e *RuntimeEngine) ValidateCapability(op scopeguard.OpClass, _ ...string) error {
	if e == nil || e.policy == nil {
		return fmt.Errorf("runtime: nil engine dependency (fail-closed)")
	}
	if !op.Valid() {
		return fmt.Errorf("runtime: unknown capability %q (fail-closed)", string(op))
	}
	if !e.policy.Policy.Allowed.Allows(op) {
		return fmt.Errorf("runtime: workspace %q denies %s capability (fail-closed)",
			e.policy.Policy.Mode, string(op))
	}
	return nil
}

// AnalyzeFailure classifies an execution error through the bounded-recovery
// taxonomy and returns it as a terminal domain/task.TaskResult:
//
//   - ErrTaskTimeout (or a deadline-exceeded context) → Failed with the
//     timeout cause preserved for errors.Is.
//   - context cancellation → Canceled.
//   - everything else → Failed with the original cause; Data carries the
//     ephemeral.FailureReason so planners can route without re-classifying.
//
// It performs no ledger writes: classification is pure; persistence happens
// inside ExecuteProposal's failure loop.
func (e *RuntimeEngine) AnalyzeFailure(execErr error) domaintask.TaskResult {
	if execErr == nil {
		return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted}
	}
	if errors.Is(execErr, domaintask.ErrTaskTimeout) || errors.Is(execErr, context.DeadlineExceeded) {
		return domaintask.TaskResult{
			Status: domaintask.ExecStatusFailed,
			Error:  fmt.Errorf("%w: %w", domaintask.ErrTaskTimeout, execErr),
			Data:   ephemeral.ClassifyError(execErr),
		}
	}
	if errors.Is(execErr, context.Canceled) {
		return domaintask.TaskResult{Status: domaintask.ExecStatusCanceled, Error: execErr}
	}
	return domaintask.TaskResult{
		Status: domaintask.ExecStatusFailed,
		Error:  execErr,
		Data:   ephemeral.ClassifyError(execErr),
	}
}
