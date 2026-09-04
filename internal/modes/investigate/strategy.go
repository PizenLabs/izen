package investigate

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// InvestigateStrategy is the stateless investigate ModeStrategy.
type InvestigateStrategy struct{}

// NewInvestigateStrategy returns a stateless investigate strategy.
func NewInvestigateStrategy() *InvestigateStrategy { return &InvestigateStrategy{} }

// Evaluate implements modes.ModeStrategy. It is read-only and emits diagnostic proposals.
func (s *InvestigateStrategy) Evaluate(ctx context.Context, scope substrate.ReadScope, input modes.StrategyInput) (substrate.Proposal, error) {
	if scope == nil {
		return substrate.Proposal{}, fmt.Errorf("investigate strategy: nil ReadScope")
	}
	_, _ = scope.Snapshot()
	// Investigate is read-only; it emits an exec proposal for diagnostic command when needed.
	ops := []substrate.Operation{}
	if input.Intent != "" {
		ops = append(ops, substrate.Operation{Type: substrate.OpExecCmd, Args: []string{"echo", input.Intent}})
	}
	return substrate.Proposal{ID: "investigate-" + input.Intent, Intent: input.Intent, Operations: ops}, nil
}

var _ modes.ModeStrategy = (*InvestigateStrategy)(nil)
