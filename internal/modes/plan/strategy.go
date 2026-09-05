package plan

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// PlanStrategy is the stateless plan mode Strategy. It evaluates intent via
// ReadScope and emits an immutable Proposal. It holds no I/O handles.
type PlanStrategy struct{}

// NewPlanStrategy returns a stateless plan strategy.
func NewPlanStrategy() *PlanStrategy { return &PlanStrategy{} }

// Evaluate implements modes.ModeStrategy.
func (s *PlanStrategy) Evaluate(ctx context.Context, scope substrate.ReadScope, input modes.StrategyInput) (substrate.Proposal, error) {
	if scope == nil {
		return substrate.Proposal{}, fmt.Errorf("plan strategy: nil ReadScope")
	}
	// Read via scope only; no direct file access.
	_, _ = scope.Snapshot()
	ops := []substrate.Operation{}
	if len(input.Targets) > 0 {
		for _, t := range input.Targets {
			ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: t, Content: []byte(input.Prompt)})
		}
	} else if input.Intent != "" {
		ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: ".izen/plans/current.md", Content: []byte(input.Intent)})
	}
	return substrate.Proposal{ID: "plan-" + input.Intent, Intent: input.Intent, Operations: ops}, nil
}

// Ensure interface compliance.
var _ modes.ModeStrategy = (*PlanStrategy)(nil)
