// Package scheduler implements the demoted StepScheduler experiment
// (Phase 8 M1 — canonical runtime convergence).
//
// DEMOTION NOTICE: StepScheduler is NOT the production orchestration
// authority. The canonical scheduler / orchestration owner is
// runtime/autonomy.Driver (bounded loop; decomposition via
// execution/planner; execution via execution.RuntimeExecutor). This
// package has zero production importers and must never gain one: it is
// retained as an architecture experiment holding tested pure logic
// (EffectiveBudget, Schedule partitioning, AcceptStep validation) plus
// the Phase 6.2 experiment suite. Pinned by
// TestExperimentSchedulerHasNoProductionImporters.
//
// The old Invariant 2 below ("step scope strictly owned by StepScheduler
// prior to authorization") is SUPERSEDED: step selection, admission, and
// recovery entry are owned by the Driver and the RuntimeExecutor pipeline.
// The AcceptStep validator remains available as a pure helper for tests.
//
// Invariant 2 (Scheduler Ownership, HISTORICAL): step scope and step boundaries are
// strictly owned by StepScheduler prior to authorization. The Executor MUST
// NOT alter, truncate, or mutate step targets — it accepts exact
// ExecutionStep inputs via AcceptStep validation.
package scheduler

import (
	"fmt"
	"reflect"

	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// StepType classifies the bounded work of one ExecutionStep.
type StepType string

const (
	// StepTypeMutation performs workspace mutations. It MUST run under the
	// ZERO PROSE / RAW TOOL CALL ONLY output protocol.
	StepTypeMutation StepType = "mutation"
	// StepTypeRead performs read-only observation.
	StepTypeRead StepType = "read"
	// StepTypeVerify performs evidence verification (build/test/lint).
	StepTypeVerify StepType = "verify"
)

// StepOutcome is the terminal outcome of one bounded step.
type StepOutcome string

const (
	StepOutcomePending  StepOutcome = "pending"
	StepOutcomeComplete StepOutcome = "complete"
	// StepOutcomePartial marks finish_reason=length truncation: the staged
	// proposal was discarded and the scheduler must issue a continuation.
	StepOutcomePartial StepOutcome = "partial"
	StepOutcomeFailed  StepOutcome = "failed"
)

// ExecutionStep is one bounded unit of work owned by the StepScheduler.
// Targets is immutable after Schedule returns: the executor accepts it
// exactly and never mutates it (Invariant 2).
type ExecutionStep struct {
	ID                    string
	Type                  StepType
	Targets               []string
	EstimatedMutationSize int
	StepBudget            int
	Sequence              int
	TotalSteps            int
	Strategy              StepStrategy
	Operation             string
	StateFingerprint      string
	BlockedReason         string
}

// TaskSpec is the durable task the scheduler decomposes. Task scope
// (Targets) is durable across steps (Invariant 1); only the ephemeral
// StepBudget varies per step.
type TaskSpec struct {
	Objective string
	// Targets is the durable declared/dynamic scope ($hot / $prompt).
	Targets []string
	// EstimatedSizes carries the per-target estimated mutation size in
	// tokens. Missing entries fall back to uniform division of
	// TotalEstimatedSize.
	EstimatedSizes map[string]int
	// TotalEstimatedSize is the whole-task mutation estimate in tokens.
	TotalEstimatedSize int
	// TaskRemainingBudget is the task-level remaining output budget.
	TaskRemainingBudget int
	// RequestedStepBudget is the desired per-step complexity budget.
	RequestedStepBudget int
	// ReasoningMargin is the caller-owned opaque-reasoning reserve
	// (Invariant 3: never hardcoded by the engine).
	ReasoningMargin int
	// Provider advertises the model completion ceiling.
	Provider *dprovider.ProviderMetadata
	// Type classifies every emitted step (default mutation).
	Type              StepType
	Operations        map[string]string
	DiskSizes         map[string]int64
	StateFingerprints map[string]string
	History           map[string][]StepOutcome
	State             *durable.TaskState
	StepIndex         int
	TargetASTs        map[string]string
	LatestEvidence    []EvidenceItem
}

// StepScheduler decomposes durable tasks into bounded ExecutionSteps.
type StepScheduler struct{}

// NewStepScheduler builds a StepScheduler.
func NewStepScheduler() *StepScheduler { return &StepScheduler{} }

// EffectiveBudget derives the ephemeral per-step budget for the task.
func (s *StepScheduler) EffectiveBudget(spec TaskSpec) int {
	req := spec.RequestedStepBudget
	if req <= 0 {
		req = spec.TotalEstimatedSize
	}
	limit := 0
	if spec.Provider != nil {
		limit = spec.Provider.ResolvedOutputLimit().Value
	}
	return dprovider.EffectiveStepBudget(req, limit, spec.TaskRemainingBudget, spec.ReasoningMargin)
}

// Schedule partitions the durable target scope into sequential bounded
// steps. Each step's EstimatedMutationSize fits within the effective step
// budget unless a SINGLE target alone exceeds it — then it still owns its
// own step (the executor yields StepOutcomePartial and the scheduler
// continues; the task scope is never narrowed to fit the model).
func (s *StepScheduler) Schedule(spec TaskSpec) []ExecutionStep {
	targets := append([]string(nil), spec.Targets...)
	if len(targets) == 0 {
		return nil
	}
	budget := s.EffectiveBudget(spec)
	if budget <= 0 {
		budget = dprovider.DefaultRequestedStepBudget
	}
	sizes := make(map[string]int, len(targets))
	for _, t := range targets {
		if v, ok := spec.EstimatedSizes[t]; ok && v > 0 {
			sizes[t] = v
			continue
		}
		if spec.TotalEstimatedSize > 0 {
			sizes[t] = (spec.TotalEstimatedSize + len(targets) - 1) / len(targets)
		} else {
			sizes[t] = budget
		}
	}
	typ := spec.Type
	if typ == "" {
		typ = StepTypeMutation
	}
	var steps []ExecutionStep
	var cur []string
	curSize := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		cp := append([]string(nil), cur...)
		steps = append(steps, ExecutionStep{
			Type:                  typ,
			Targets:               cp,
			EstimatedMutationSize: curSize,
			StepBudget:            budget,
		})
		cur = nil
		curSize = 0
	}
	for _, t := range targets {
		sz := sizes[t]
		if len(spec.Operations) > 0 {
			flush()
			steps = append(steps, strategyStep(spec, t, typ, budget, sz))
			continue
		}
		if len(cur) > 0 && curSize+sz > budget {
			flush()
		}
		cur = append(cur, t)
		curSize += sz
		// A single oversized target still occupies exactly one step; the
		// remainder is handled by Partial continuation, never by dropping
		// scope.
		if curSize >= budget {
			flush()
		}
	}
	flush()
	for i := range steps {
		steps[i].ID = fmt.Sprintf("step-%d-of-%d", i+1, len(steps))
		steps[i].Sequence = i + 1
		steps[i].TotalSteps = len(steps)
	}
	return steps
}

// AcceptStep validates that the executor received the scheduler-owned step
// EXACTLY: same target count, order, and contents. Any alteration,
// truncation, or mutation of the target slice is rejected (Invariant 2).
func AcceptStep(scheduled, received ExecutionStep) error {
	if len(received.Targets) != len(scheduled.Targets) {
		return fmt.Errorf("scheduler: executor altered step %q target length: scheduled %d, received %d",
			scheduled.ID, len(scheduled.Targets), len(received.Targets))
	}
	for i := range scheduled.Targets {
		if received.Targets[i] != scheduled.Targets[i] {
			return fmt.Errorf("scheduler: executor mutated step %q target[%d]: scheduled %q, received %q",
				scheduled.ID, i, scheduled.Targets[i], received.Targets[i])
		}
	}
	if !reflect.DeepEqual(scheduled, received) {
		return fmt.Errorf("scheduler: executor altered step %q fields", scheduled.ID)
	}
	return nil
}
