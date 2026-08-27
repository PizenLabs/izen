package scope

import (
	"testing"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// flatHTMLScan builds a shallow, low-coupling HTML topology: a handful of
// root-level nodes with no active reference edges (repetitive boilerplate).
func flatHTMLScan(lines int) *planner.LeaScanReport {
	nodes := make([]planner.DOMNode, 0, 6)
	for i := 0; i < 6; i++ {
		nodes = append(nodes, planner.DOMNode{
			Tag:       "div",
			StartLine: i*lines/6 + 1,
			EndLine:   (i+1)*lines/6 + 1,
			Parent:    -1,
			Depth:     0,
		})
	}
	return &planner.LeaScanReport{
		Format:     "html",
		Nodes:      nodes,
		References: nil,
		TotalLines: lines,
	}
}

// nestedJSXScan builds a deeply nested JSX topology with 15+ active reference
// edges (class/component dependencies), emulating a symbol-dense component tree.
func nestedJSXScan(lines int) *planner.LeaScanReport {
	nodes := make([]planner.DOMNode, 0, 24)
	depth := 0
	for i := 0; i < 24; i++ {
		if i%4 == 0 {
			depth++
		}
		nodes = append(nodes, planner.DOMNode{
			Tag:       "Component",
			StartLine: i*lines/24 + 1,
			EndLine:   (i+1)*lines/24 + 1,
			Parent:    -1,
			Depth:     depth,
		})
	}
	refs := make([]planner.ActiveReference, 0, 16)
	for i := 0; i < 16; i++ {
		refs = append(refs, planner.ActiveReference{Name: "dep", Kind: "class", UsedAt: []int{i + 1}})
	}
	return &planner.LeaScanReport{
		Format:     "jsx",
		Nodes:      nodes,
		References: refs,
		TotalLines: lines,
	}
}

// TestScope_FlatLargeDocument_ResolvesSinglePass verifies that a 35KB flat,
// repetitive HTML document resolves to SinglePass despite its byte size:
// structural complexity (not bytes) drives the decision.
func TestScope_FlatLargeDocument_ResolvesSinglePass(t *testing.T) {
	const kb = 1024
	const bytes = 35 * kb
	estTokens := bytes / 4 // ~4 chars/token heuristic used across the pipeline
	in := ScopeInput{
		Scan:            flatHTMLScan(900),
		EstimatedTokens: estTokens,
		MaxOutputTokens: 4096,
		TotalLines:      900,
	}
	dec := New(DefaultPolicy()).Evaluate(in)
	if dec.Strategy != StrategySinglePass {
		t.Fatalf("flat 35KB HTML: want %s, got %s (score=%.3f reason=%q)",
			StrategySinglePass, dec.Strategy, dec.Score, dec.Reason)
	}
	if dec.Metrics.ASTDepth != 1 {
		t.Errorf("flat doc ASTDepth = %d, want 1", dec.Metrics.ASTDepth)
	}
	if dec.Metrics.DependencyFanOut != 0 {
		t.Errorf("flat doc DependencyFanOut = %d, want 0", dec.Metrics.DependencyFanOut)
	}
}

// TestScope_NestedSmallDocument_ResolvesDAG verifies that an 8KB densely nested
// JSX/HTML document with 15+ class dependencies resolves to DAG decomposition
// even though it is far smaller in bytes than the flat document above.
func TestScope_NestedSmallDocument_ResolvesDAG(t *testing.T) {
	const kb = 1024
	const bytes = 8 * kb
	estTokens := bytes / 4
	in := ScopeInput{
		Scan:            nestedJSXScan(130),
		EstimatedTokens: estTokens,
		MaxOutputTokens: 4096,
		TotalLines:      130,
	}
	dec := New(DefaultPolicy()).Evaluate(in)
	if dec.Strategy != StrategyDAG {
		t.Fatalf("nested 8KB JSX: want %s, got %s (score=%.3f reason=%q)",
			StrategyDAG, dec.Strategy, dec.Score, dec.Reason)
	}
	if dec.Metrics.DependencyFanOut < 15 {
		t.Errorf("nested doc DependencyFanOut = %d, want >= 15", dec.Metrics.DependencyFanOut)
	}
	if dec.Metrics.ASTDepth < 5 {
		t.Errorf("nested doc ASTDepth = %d, want >= 5", dec.Metrics.ASTDepth)
	}
}

// TestScope_PolicyOverride_AltersSelection confirms that a custom threshold
// (a configurable policy input, not a fixed constant) cleanly flips the
// strategy for a borderline document without touching the engine formula.
func TestScope_PolicyOverride_AltersSelection(t *testing.T) {
	// Borderline document: moderately nested, moderate coupling.
	nodes := make([]planner.DOMNode, 0, 10)
	for i := 0; i < 10; i++ {
		nodes = append(nodes, planner.DOMNode{Tag: "section", StartLine: i + 1, EndLine: i + 2, Parent: -1, Depth: i / 3})
	}
	refs := make([]planner.ActiveReference, 0, 7)
	for i := 0; i < 7; i++ {
		refs = append(refs, planner.ActiveReference{Name: "c", Kind: "class", UsedAt: []int{i + 1}})
	}
	scan := &planner.LeaScanReport{Format: "html", Nodes: nodes, References: refs, TotalLines: 40}
	in := ScopeInput{Scan: scan, EstimatedTokens: 2000, MaxOutputTokens: 4096, TotalLines: 40}

	base := DefaultPolicy()
	loThreshold := base
	loThreshold.Threshold = 0.0 // everything is DAG
	hiThreshold := base
	hiThreshold.Threshold = 100.0 // everything is SinglePass

	dagDecision := New(loThreshold).Evaluate(in)
	if dagDecision.Strategy != StrategyDAG {
		t.Fatalf("threshold=0.0: want %s, got %s (score=%.3f)", StrategyDAG, dagDecision.Strategy, dagDecision.Score)
	}
	singleDecision := New(hiThreshold).Evaluate(in)
	if singleDecision.Strategy != StrategySinglePass {
		t.Fatalf("threshold=100.0: want %s, got %s (score=%.3f)", StrategySinglePass, singleDecision.Strategy, singleDecision.Score)
	}
	// Same document, same metrics — only the policy threshold changed.
	if dagDecision.Score != singleDecision.Score {
		t.Errorf("score drift across policy override: %.3f vs %.3f", dagDecision.Score, singleDecision.Score)
	}
}
