package plan

import "time"

// TaskStatus is the lifecycle state of a single plan step.
type TaskStatus string

const (
	StatusPending TaskStatus = "PENDING"
	StatusRunning TaskStatus = "RUNNING"
	StatusSuccess TaskStatus = "SUCCESS"
	StatusFailed  TaskStatus = "FAILED"
)

// PlanStep is one node of the hierarchical execution plan.
type PlanStep struct {
	ID          string
	Title       string
	Status      TaskStatus
	SubSteps    []PlanStep
	ElapsedTime time.Duration
	Error       string
}

// ExecutionPlan is the agent's step-by-step resolution strategy.
type ExecutionPlan struct {
	Goal  string
	Steps []PlanStep
}

// Counts walks the plan (including nested sub-steps) and returns the number
// of completed (SUCCESS), running (RUNNING), failed (FAILED) and total steps.
func (p *ExecutionPlan) Counts() (completed, running, failed, total int) {
	if p == nil {
		return 0, 0, 0, 0
	}
	var walk func(steps []PlanStep)
	walk = func(steps []PlanStep) {
		for _, s := range steps {
			total++
			switch s.Status {
			case StatusSuccess:
				completed++
			case StatusRunning:
				running++
			case StatusFailed:
				failed++
			}
			if len(s.SubSteps) > 0 {
				walk(s.SubSteps)
			}
		}
	}
	walk(p.Steps)
	return completed, running, failed, total
}

// TotalElapsed sums ElapsedTime across all steps (including sub-steps).
func (p *ExecutionPlan) TotalElapsed() time.Duration {
	if p == nil {
		return 0
	}
	var total time.Duration
	var walk func(steps []PlanStep)
	walk = func(steps []PlanStep) {
		for _, s := range steps {
			total += s.ElapsedTime
			if len(s.SubSteps) > 0 {
				walk(s.SubSteps)
			}
		}
	}
	walk(p.Steps)
	return total
}

// UpdateStep sets the status/elapsed/error of the step with the given ID.
// It returns true when a step matched. Empty status leaves status unchanged;
// negative elapsed leaves elapsed unchanged.
func (p *ExecutionPlan) UpdateStep(id string, status TaskStatus, elapsed time.Duration, errMsg string) bool {
	if p == nil {
		return false
	}
	return updateStepInSlice(p.Steps, id, status, elapsed, errMsg)
}

func updateStepInSlice(steps []PlanStep, id string, status TaskStatus, elapsed time.Duration, errMsg string) bool {
	for i := range steps {
		if steps[i].ID == id {
			if status != "" {
				steps[i].Status = status
			}
			if elapsed >= 0 {
				steps[i].ElapsedTime = elapsed
			}
			if errMsg != "" {
				steps[i].Error = errMsg
			}
			return true
		}
		if updateStepInSlice(steps[i].SubSteps, id, status, elapsed, errMsg) {
			return true
		}
	}
	return false
}

// IsTerminal reports whether the status is a terminal state.
func (s TaskStatus) IsTerminal() bool {
	return s == StatusSuccess || s == StatusFailed
}
