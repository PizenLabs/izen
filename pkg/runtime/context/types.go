// Package context provides an intent-aware context compiler. Context is a
// compiled resource bounded by a strict token budget, not a transcript dump:
// the compiler runs a knapsack packing over candidate units ranked by
// relevance and renders the result as structured XML provenance.
package context

import "fmt"

// ContextKind classifies the origin of a context unit.
type ContextKind int

const (
	// KindTargetState is the desired state of the target file.
	KindTargetState ContextKind = iota
	// KindManifest is build or dependency manifest context (e.g. go.mod).
	KindManifest
	// KindTopology is repository or service topology context.
	KindTopology
	// KindSourceSnippet is a source code excerpt.
	KindSourceSnippet
)

// String returns the stable XML-safe label for a ContextKind.
func (k ContextKind) String() string {
	switch k {
	case KindTargetState:
		return "target"
	case KindManifest:
		return "manifest"
	case KindTopology:
		return "topology"
	case KindSourceSnippet:
		return "source"
	default:
		return "unknown"
	}
}

// ContextUnit is a single context item carrying its provenance metadata.
type ContextUnit struct {
	ID        string      `json:"id"`
	Kind      ContextKind `json:"kind"`
	Source    string      `json:"source"`
	Content   string      `json:"content"`
	TokenCost int         `json:"token_cost"`
	Relevance float64     `json:"relevance"` // Priority score between 0.0 and 1.0
}

// ExpansionDepth controls how many context kinds are available to the
// compiler based on the intent confidence.
type ExpansionDepth int

const (
	// DepthMinimal exposes only target state (Confidence < 0.60).
	DepthMinimal ExpansionDepth = iota
	// DepthConservative exposes target state and manifests (0.60-0.85).
	DepthConservative
	// DepthDeep exposes the full context set (Confidence > 0.85).
	DepthDeep
)

// String returns the stable XML-safe label for an ExpansionDepth.
func (d ExpansionDepth) String() string {
	switch d {
	case DepthMinimal:
		return "minimal"
	case DepthConservative:
		return "conservative"
	case DepthDeep:
		return "deep"
	default:
		return "unknown"
	}
}

// IntentSpec describes the parsed user intent that drives context selection.
type IntentSpec struct {
	ActionDescription string
	Confidence        float64
	TargetFile        string
}

// CompiledContext is the result of the knapsack packing: the selected units,
// the token accounting, the effective expansion depth, and the intent label
// used for XML provenance rendering.
type CompiledContext struct {
	Units       []ContextUnit
	TotalTokens int
	Budget      int
	Depth       ExpansionDepth
	Intent      string
}

// String returns a compact human-readable summary of the compiled context.
func (cc *CompiledContext) String() string {
	if cc == nil {
		return "context: <nil>"
	}
	return fmt.Sprintf("context: intent=%q depth=%s units=%d tokens=%d/%d",
		cc.Intent, cc.Depth.String(), len(cc.Units), cc.TotalTokens, cc.Budget)
}
