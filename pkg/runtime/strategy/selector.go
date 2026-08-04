package strategy

import "github.com/PizenLabs/izen/pkg/runtime/analyzer"

// Built-in strategy names registered by the wire package.
const (
	// StrategyDirect is the DirectGenerationStrategy name.
	StrategyDirect = "direct_generation"
	// StrategyIterative is the IterativeStrategy name.
	StrategyIterative = "iterative"
)

// Default direct-generation scope thresholds. Tasks under both thresholds
// are routed to DirectGenerationStrategy; everything else falls back to
// IterativeStrategy.
const (
	// DefaultDirectTokenBudget is the inclusive maximum estimated token
	// budget for the direct fast path.
	DefaultDirectTokenBudget = 25_000
	// DefaultDirectMaxFanout is the inclusive maximum dependency fanout for
	// the direct fast path.
	DefaultDirectMaxFanout = 4
)

// Selector is the default strategy resolver. It mirrors the default policy:
//
//	token estimate < 25k AND dependency fanout < 4  -> direct_generation
//	otherwise                                       -> iterative
//
// The same facts always produce the same strategy.
func Selector(facts *analyzer.Facts) string {
	if facts.TokenEstimate < DefaultDirectTokenBudget && facts.MaxFanout < DefaultDirectMaxFanout {
		return StrategyDirect
	}
	return StrategyIterative
}
