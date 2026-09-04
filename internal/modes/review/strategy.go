package review

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// ReviewStrategy is the stateless review ModeStrategy (read-only).
type ReviewStrategy struct{}

// NewReviewStrategy returns a stateless review strategy.
func NewReviewStrategy() *ReviewStrategy { return &ReviewStrategy{} }

// Evaluate implements modes.ModeStrategy. Review never mutates; it emits empty or read-only proposals.
func (s *ReviewStrategy) Evaluate(ctx context.Context, scope substrate.ReadScope, input modes.StrategyInput) (substrate.Proposal, error) {
	if scope == nil {
		return substrate.Proposal{}, fmt.Errorf("review strategy: nil ReadScope")
	}
	_, _ = scope.Snapshot()
	// Review is read-only: no file writes. Proposal is empty intent capture.
	return substrate.Proposal{ID: "review-" + input.Intent, Intent: input.Intent, Operations: []substrate.Operation{}}, nil
}

var _ modes.ModeStrategy = (*ReviewStrategy)(nil)
