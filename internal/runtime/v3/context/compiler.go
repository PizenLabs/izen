package context

import (
	"fmt"
	"sort"
)

// ContextCompiler is a stateless compiler that selects context units under a
// strict token budget, ranked by relevance and filtered by depth availability.
type ContextCompiler struct{}

// NewCompiler returns a ready-to-use ContextCompiler.
func NewCompiler() *ContextCompiler {
	return &ContextCompiler{}
}

// DetermineDepth maps an intent confidence score to an expansion depth:
// confidence > 0.85 yields DepthDeep, 0.60 <= confidence <= 0.85 yields
// DepthConservative, and any lower confidence yields DepthMinimal.
func (c *ContextCompiler) DetermineDepth(confidence float64) ExpansionDepth {
	switch {
	case confidence > 0.85:
		return DepthDeep
	case confidence >= 0.60:
		return DepthConservative
	default:
		return DepthMinimal
	}
}

// Compile evaluates the intent depth, filters candidate units by depth
// availability, and runs a ranked knapsack packing that selects the highest
// relevance units without exceeding tokenBudget. Missing secondary units
// degrade the result gracefully instead of failing closed.
func (c *ContextCompiler) Compile(intent IntentSpec, candidateUnits []ContextUnit, tokenBudget int) (*CompiledContext, error) {
	if tokenBudget < 0 {
		return nil, fmt.Errorf("context: token budget must be non-negative, got %d", tokenBudget)
	}

	depth := c.DetermineDepth(intent.Confidence)

	eligible := make([]ContextUnit, 0, len(candidateUnits))
	for _, unit := range candidateUnits {
		if unit.TokenCost < 0 {
			continue // Defensive: skip malformed units.
		}
		if kindAccessible(unit.Kind, depth) {
			eligible = append(eligible, unit)
		}
	}

	// Ranked packing: relevance descending, then token cost ascending.
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Relevance != eligible[j].Relevance {
			return eligible[i].Relevance > eligible[j].Relevance
		}
		return eligible[i].TokenCost < eligible[j].TokenCost
	})

	selected := make([]ContextUnit, 0, len(eligible))
	used := 0
	for _, unit := range eligible {
		if used+unit.TokenCost <= tokenBudget {
			selected = append(selected, unit)
			used += unit.TokenCost
		}
	}

	return &CompiledContext{
		Units:       selected,
		TotalTokens: used,
		Budget:      tokenBudget,
		Depth:       depth,
		Intent:      intent.ActionDescription,
	}, nil
}

// kindAccessible reports whether a context kind is available at the given
// expansion depth. DepthMinimal exposes only target state, DepthConservative
// adds manifests, and DepthDeep exposes the full context set.
func kindAccessible(kind ContextKind, depth ExpansionDepth) bool {
	switch depth {
	case DepthMinimal:
		return kind == KindTargetState
	case DepthConservative:
		return kind == KindTargetState || kind == KindManifest
	case DepthDeep:
		return true
	default:
		return false
	}
}
