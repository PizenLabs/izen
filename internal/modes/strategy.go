package modes

import (
	"context"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// StrategyInput is the immutable intent input for a mode evaluation.
// Modes are stateless transformers: they read via ReadScope and emit a
// immutable Proposal without holding I/O handles or capabilities.
type StrategyInput struct {
	Intent  string
	Prompt  string
	Targets []string
	Mode    Mode
	// Meta carries optional mode-specific hints (e.g. task filter).
	Meta map[string]string
}

// ModeStrategy is the unified stateless evaluation contract for all modes.
// Implementations MUST ONLY interact with the system via substrate.ReadScope
// and return a substrate.Proposal. No direct I/O or capability handles are
// permitted.
type ModeStrategy interface {
	Evaluate(ctx context.Context, scope substrate.ReadScope, input StrategyInput) (substrate.Proposal, error)
}
