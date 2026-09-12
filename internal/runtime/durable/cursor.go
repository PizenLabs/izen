package durable

import (
	"fmt"
	"strings"
)

// Reconcile applies the deterministic side-effect rule:
//
//	Missing Response != Missing Side Effect.
//
// Compare the current worktree digest against the cursor digests:
//   - current == post -> ALREADY_COMMITTED (advance, DO NOT RE-EXECUTE)
//   - current == pre  -> SAFE_RETRY (never committed, retry allowed)
//   - otherwise       -> CONFLICT (third-party mutation / partial write)
func Reconcile(c ExecutionCursor, currentDigest string) ReconcileDecision {
	switch currentDigest {
	case c.PostconditionDigest:
		// Note: when pre == post (no-op operation) both match; the
		// postcondition wins so a completed no-op is never re-executed.
		return DecisionAlreadyCommitted
	case c.PreconditionDigest:
		return DecisionSafeRetry
	default:
		return DecisionConflict
	}
}

// IdempotentRunner wraps a side-effecting capability so it executes at
// most once per OperationID when the worktree already reflects the
// postcondition. digestOf computes the current tree digest; effect runs
// the real side effect and returns the post-execution digest observed by
// the caller (or "" to have the runner recompute it).
type IdempotentRunner struct {
	Store *TaskStore
}

// Run executes operationID exactly once under cursor semantics:
//
//  1. If the current digest already equals postcondition -> record the
//     commit (if not already committed) and return (false, nil):
//     the effect is NOT invoked twice.
//  2. If the current digest equals precondition -> invoke effect once,
//     then commit and return (true, nil).
//  3. Otherwise -> record TARGET_CONFLICT and return an error without
//     invoking the effect.
//
// digestOf and effect are the injectable seams tests use to simulate a
// network drop mid-execution (effect runs, response lost, retry must
// observe the postcondition and skip).
func (r *IdempotentRunner) Run(
	taskID, stepID, operationID, pre, post string,
	digestOf func() (string, error),
	effect func() error,
) (executed bool, err error) {
	if r == nil || r.Store == nil {
		return false, fmt.Errorf("durable: nil IdempotentRunner store")
	}
	if strings.TrimSpace(operationID) == "" {
		return false, fmt.Errorf("durable: empty operation id")
	}
	if digestOf == nil {
		return false, fmt.Errorf("durable: nil digest func")
	}
	current, err := digestOf()
	if err != nil {
		return false, err
	}
	// Fast path: already committed -> never invoke the effect twice.
	if current == post {
		st, ok := r.Store.State(taskID)
		needsCommit := true
		if ok && st.Cursor != nil && st.Cursor.OperationID == operationID &&
			st.Cursor.Status == CursorCommitted {
			needsCommit = false
		}
		if needsCommit {
			if derr := r.Store.DispatchCursor(ExecutionCursor{
				TaskID: taskID, StepID: stepID, OperationID: operationID,
				PreconditionDigest: pre, PostconditionDigest: post,
			}); derr != nil {
				return false, derr
			}
			if cerr := r.Store.CommitExecution(taskID, operationID); cerr != nil {
				return false, cerr
			}
		}
		return false, nil
	}
	decision := Reconcile(ExecutionCursor{
		PreconditionDigest: pre, PostconditionDigest: post,
	}, current)
	switch decision {
	case DecisionConflict:
		_ = r.Store.RecordConflict(taskID, operationID,
			"pre-execution digest matches neither precondition nor postcondition")
		return false, fmt.Errorf("durable: target conflict for operation %q", operationID)
	case DecisionSafeRetry:
		// Fall through to execute exactly once below.
	default:
		// DecisionAlreadyCommitted is unreachable here (handled by the
		// fast path) but kept for exhaustiveness.
		return false, nil
	}
	if err := r.Store.DispatchCursor(ExecutionCursor{
		TaskID: taskID, StepID: stepID, OperationID: operationID,
		PreconditionDigest: pre, PostconditionDigest: post,
	}); err != nil {
		return false, err
	}
	if effect != nil {
		if err := effect(); err != nil {
			return true, err
		}
	}
	if err := r.Store.CommitExecution(taskID, operationID); err != nil {
		return true, err
	}
	return true, nil
}
