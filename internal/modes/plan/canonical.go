package plan

import (
	"context"

	stdtask "github.com/PizenLabs/izen/internal/domain/task"
)

// ToCanonicalPlan wraps a canonical task slice into a *task.Plan. Since
// plan.Task is an alias of task.Task, every []Task produced by this package —
// by the LLM JSON plan synthesis or by the MicrokernelPlanner bridge — is
// already a []task.Task; ToCanonicalPlan adds the fast-track and summary
// framing so both pipelines produce the canonical task.Plan model.
func ToCanonicalPlan(tasks []Task, isFastTrack bool, summary string) *stdtask.Plan {
	return stdtask.NewPlan(tasks, isFastTrack, summary)
}

// ProcessFromLedgerCanonical runs the LLM JSON plan synthesis and returns the
// staged plan in the canonical task.Plan model. It is the canonical-equivalent
// entry point alongside ProcessFromLedger.
func (e *Engine) ProcessFromLedgerCanonical(ctx context.Context, ledgerContent, problem, modelName string) (*stdtask.Plan, error) {
	tasks, err := e.processFromLedger(ctx, ledgerContent, problem, modelName, false)
	if err != nil {
		return nil, err
	}
	return ToCanonicalPlan(tasks, false, ""), nil
}
