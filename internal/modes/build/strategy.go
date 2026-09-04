package build

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// BuildStrategy is the stateless build ModeStrategy.
type BuildStrategy struct{}

// NewBuildStrategy returns a stateless build strategy.
func NewBuildStrategy() *BuildStrategy { return &BuildStrategy{} }

// Evaluate implements modes.ModeStrategy. It compiles intent into a proposal
// via ReadScope only.
func (s *BuildStrategy) Evaluate(ctx context.Context, scope substrate.ReadScope, input modes.StrategyInput) (substrate.Proposal, error) {
	if scope == nil {
		return substrate.Proposal{}, fmt.Errorf("build strategy: nil ReadScope")
	}
	_, _ = scope.Snapshot()
	ops := []substrate.Operation{}
	for _, t := range input.Targets {
		// Content would be derived from scope reads; placeholder for compilation.
		data, _ := scope.ReadFile(t)
		ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: t, Content: data})
	}
	if len(ops) == 0 && input.Intent != "" {
		ops = append(ops, substrate.Operation{Type: substrate.OpFileWrite, Target: "build.out", Content: []byte(input.Intent)})
	}
	return substrate.Proposal{ID: "build-" + input.Intent, Intent: input.Intent, Operations: ops}, nil
}

var _ modes.ModeStrategy = (*BuildStrategy)(nil)
