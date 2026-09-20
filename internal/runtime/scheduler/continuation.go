package scheduler

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/executor"
)

// ErrRecoveryHalted aborts the scheduler continuation loop when the
// Zero-Delta Recovery Limit Invariant (Phase 6.4.1) trips: consecutive
// OUTPUT_CEILING recovery attempts produced zero workspace mutations.
// Callers MUST NOT retry the same step on this error; it signals a bounded
// halt, not a transient failure. (Distinct from the autonomy package's
// zero-trust matrix sentinel of the same name, which governs a different
// recovery layer.)
var ErrRecoveryHalted = errors.New("scheduler: recovery halted after consecutive zero-delta recoveries")

// zeroDeltaHaltThreshold is the ConsecutiveZeroDeltas count at which the
// continuation loop aborts. At most maxConsecutiveZeroDeltas (1) bounded
// recovery turn is allowed: the first zero-delta OUTPUT_CEILING partial
// continues, the second halts.
const zeroDeltaHaltThreshold = 2

// maxConsecutiveZeroDeltas documents the recovery budget: maximum
// consecutive zero-delta recoveries allowed is 1.
const maxConsecutiveZeroDeltas = zeroDeltaHaltThreshold - 1

// PostStepEvaluation enforces the Zero-Delta Recovery Limit Invariant after
// one bounded step completes:
//
//   - If the step outcome is StepOutcomePartial with the OUTPUT_CEILING
//     reason and zero workspace mutations (patches == 0), the attempt made
//     no progress: ConsecutiveZeroDeltas is incremented, and when it reaches
//     zeroDeltaHaltThreshold the continuation loop MUST abort immediately
//     with ErrRecoveryHalted.
//   - On any successful nonzero workspace commit (patches > 0),
//     ConsecutiveZeroDeltas resets to 0.
//   - All other outcomes leave the counter untouched.
//
// It mutates state.RecoveryContext in place and is safe to call with a nil
// state (no-op returning nil).
func PostStepEvaluation(state *durable.TaskState, outcome StepOutcome, reason string, patches int) error {
	if state == nil {
		return nil
	}
	if outcome == StepOutcomePartial && reason == OutputCeilingReason && patches == 0 {
		state.RecoveryContext.ConsecutiveZeroDeltas++
		if state.RecoveryContext.ConsecutiveZeroDeltas >= zeroDeltaHaltThreshold {
			return fmt.Errorf("%w: %d consecutive OUTPUT_CEILING recoveries with zero workspace mutations (max %d allowed)",
				ErrRecoveryHalted, state.RecoveryContext.ConsecutiveZeroDeltas, maxConsecutiveZeroDeltas)
		}
		return nil
	}
	if patches > 0 {
		state.RecoveryContext.ConsecutiveZeroDeltas = 0
	}
	return nil
}

type StreamResult struct {
	FinishReason   string
	Truncated      bool
	ObservedTokens int
	StreamBytes    int
}

type StepWorker func(context.Context, ExecutionStep, ContextSlice, *executor.ProposalStagingBuffer) (StreamResult, error)

type StepResult struct {
	Step              ExecutionStep
	Outcome           StepOutcome
	Reason            string
	Patches           int
	NeedsContinuation bool
}

func (s *StepScheduler) RunNext(ctx context.Context, spec TaskSpec, state *durable.TaskState, worker StepWorker, runtimeEvidence evidence.EvidenceState, sink executor.WorkspaceSink) (StepResult, error) {
	result := StepResult{Outcome: StepOutcomeFailed}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if state == nil || worker == nil || sink == nil {
		return result, fmt.Errorf("scheduler: state, worker, and authorized sink are required")
	}
	spec.State = state
	steps := s.Schedule(spec)
	index := spec.StepIndex
	if index < 0 || index >= len(steps) {
		return result, fmt.Errorf("scheduler: step index %d outside scheduled scope", index)
	}
	step := steps[index]
	result.Step = step
	if step.BlockedReason != "" {
		return result, &NoProgressError{
			Target: step.Targets[0], Strategy: StepStrategy(state.RecoveryContext.Strategy),
			StateFingerprint: step.StateFingerprint, Reason: step.BlockedReason,
		}
	}
	if step.Type != StepTypeMutation || step.Strategy == "" || len(step.Targets) != 1 {
		return result, fmt.Errorf("scheduler: continuation requires one explicit mutation target")
	}
	target := step.Targets[0]
	// TargetASTs are scheduler-owned workspace snapshots, never worker claims.
	sources := make(map[string]string, len(spec.Targets))
	for _, scopedTarget := range spec.Targets {
		sources[scopedTarget] = spec.TargetASTs[scopedTarget]
	}
	baseline, err := executor.NewSymbolBaseline(sources)
	if err != nil {
		return result, err
	}
	recovery := durable.RecoveryContext{}
	if state.RecoveryContext.Target == target {
		recovery = state.RecoveryContext
	}
	recovery.StepID = step.ID
	recovery.Target = target
	recovery.Strategy = string(step.Strategy)
	recovery.StateFingerprint = step.StateFingerprint
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	buffer := executor.NewProposalStagingBuffer(step.ID).
		WithTokenGuard(executor.NewStreamTokenGuard(step.StepBudget, cancel)).
		WithRecoveryContext(state, spec.Provider, recovery).
		WithPayloadLimit(step.StepBudget).
		WithSymbolBaseline(target, baseline)
	defer buffer.Discard()
	slice := NewContextPlanner(MaxEvidencePerSlice).Assemble(spec.Objective, TaskStateSnapshot{
		Objective: spec.Objective, RemainingBudget: spec.TaskRemainingBudget,
		StateFingerprint: step.StateFingerprint,
	}, step, spec.TargetASTs[target], spec.LatestEvidence)
	slice.AvailableSymbols = baseline.Context()
	if recovery.Reason == string(executor.RedundantSymbolReason) {
		slice.RecoveryInstructions = fmt.Sprintf("REDUNDANT_SYMBOL: Reuse existing symbols %v from %s. Do not add equivalent private helpers. Replan a bounded patch against the unchanged baseline.", recovery.ReuseSymbols, recovery.ReuseTarget)
	}
	slice.InputTokens = estimateSliceTokens(slice)
	received := step
	received.Targets = append([]string(nil), step.Targets...)
	stream, workerErr := worker(stepCtx, received, slice, buffer)
	if err := AcceptStep(step, received); err != nil {
		return result, err
	}
	stagedBytes := buffer.Len()
	buffer.WithObservedTokens(stream.ObservedTokens)
	var disposition executor.StagingDisposition
	switch {
	case ctx.Err() != nil:
		disposition = buffer.FinalizeErr(ctx.Err())
		workerErr = ctx.Err()
	case workerErr != nil:
		disposition = buffer.FinalizeErr(workerErr)
	default:
		disposition = buffer.Finalize(stream.FinishReason, stream.Truncated)
	}
	patches, outcome, err := executor.CommitGate(disposition, runtimeEvidence, sink)
	result.Outcome = StepOutcome(outcome)
	if result.Reason == "" {
		result.Reason = string(disposition.Reason)
	}
	result.Patches = patches
	result.NeedsContinuation = disposition.NeedsContinuation
	if err != nil {
		return result, err
	}
	// Zero-Delta Recovery Limit Invariant (Phase 6.4.1): a truncated
	// response with zero file mutations consumes one recovery turn; the
	// second consecutive zero-delta OUTPUT_CEILING halts the loop.
	if haltErr := PostStepEvaluation(state, result.Outcome, result.Reason, result.Patches); haltErr != nil {
		result.NeedsContinuation = false
		return result, haltErr
	}
	if outcome == executor.StepOutcomeComplete && patches > 0 && recovery.Reason == string(executor.RedundantSymbolReason) {
		state.RecoveryContext = durable.RecoveryContext{}
	}
	if outcome == executor.StepOutcomeComplete && patches > 0 && step.Strategy == SKELETON_CREATE {
		recovery.Phase = durable.BaselineEstablished
		recovery.LastCleanByteOffset = stagedBytes
		recovery.LastCleanLine = countLines(disposition.Proposal)
		state.RecoveryContext = recovery
		result.NeedsContinuation = true
	}
	if workerErr != nil && outcome != executor.StepOutcomePartial {
		return result, workerErr
	}
	return result, nil
}

func countLines(proposal string) int {
	lines := 0
	for i := 0; i < len(proposal); i++ {
		if proposal[i] == '\n' {
			lines++
		}
	}
	return lines
}
