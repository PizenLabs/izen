package scope

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Elastic Scope Scoring Engine ─────────────────────────────────────────────
//
// Decomposition strategy (SinglePass bounded patch vs. DAG decomposition) is a
// function of STRUCTURAL COMPLEXITY, not byte count. A 35KB flat document may
// resolve to a single-pass patch while an 8KB densely nested AST with high
// symbol fan-out triggers a DAG decomposition. File size is only one input
// heuristic feeding the TokenToMaxOutputRatio term; it is never an
// architecture invariant.
//
// Every weight and the selection threshold are POLICY INPUTS (configurable via
// Planner Options / Engine Policy), never fixed constants burned into the
// architecture.

// StrategyType is the decomposition contract the engine selects.
type StrategyType string

const (
	// StrategySinglePass selects a single bounded-patch pass over the target.
	StrategySinglePass StrategyType = "SinglePass"
	// StrategyDAG selects topological DAG decomposition into individually
	// preflight-feasible sub-tasks.
	StrategyDAG StrategyType = "DAG"
)

// StructuralMetrics are the complexity signals the engine scores. None of them
// is a raw byte count; size enters only via TokenToMaxOutputRatio.
type StructuralMetrics struct {
	// ASTDepth is the maximum nesting depth of the parsed topology
	// (number of levels, so a flat document with only root nodes is 1).
	ASTDepth int
	// SymbolDensity is nodes-per-line of the scanned document: how densely
	// symbols/element nodes pack the source.
	SymbolDensity float64
	// DependencyFanOut is the count of active id/class/reference edges — the
	// inter-region coupling the decomposition must respect.
	DependencyFanOut int
	// TokenToMaxOutputRatio is EstimatedTokens / MaxOutputTokens: how close
	// the target's regeneration estimate sits to the output ceiling.
	TokenToMaxOutputRatio float64
}

// ScopeDecision is the engine's verdict for one target.
type ScopeDecision struct {
	Strategy      StrategyType
	Score         float64
	Reason        string
	EstimatedCost int
	Metrics       StructuralMetrics
}

// Policy carries the configurable scoring coefficients and selection
// threshold. There are NO hard-coded magic numbers elsewhere in the engine:
// callers supply the policy (DefaultPolicy is provided for convenience).
type Policy struct {
	// ASTDepthWeight scales the nesting-depth signal.
	ASTDepthWeight float64
	// SymbolDensityWeight scales the nodes-per-line signal.
	SymbolDensityWeight float64
	// DependencyFanOutWeight scales the coupling/reference signal.
	DependencyFanOutWeight float64
	// TokenRatioWeight scales the size-vs-budget signal.
	TokenRatioWeight float64
	// Threshold is the aggregate score above which DAG decomposition is
	// selected; at or below it, single-pass is chosen.
	Threshold float64
}

// DefaultPolicy returns a balanced starting policy. Structural signals dominate
// (so a flat giant document stays SinglePass while a small but deeply nested
// document goes DAG), with the size/budget ratio as a secondary tie-breaker.
func DefaultPolicy() Policy {
	return Policy{
		ASTDepthWeight:         0.5,
		SymbolDensityWeight:    2.0,
		DependencyFanOutWeight: 0.3,
		TokenRatioWeight:       0.4,
		Threshold:              4.0,
	}
}

// ScopeInput is the engine's read-only view of one target's structural
// discovery. It is intentionally a scope-owned struct (NOT the preflight
// snapshot) so the engine imports only the planner's scan types and never
// forces an import cycle with the preflight store.
type ScopeInput struct {
	// Scan is the read-only AST/DOM analysis (nil for unscannable formats).
	Scan *planner.LeaScanReport
	// EstimatedTokens is the tokenizer/budget estimate for the target scope.
	EstimatedTokens int
	// MaxOutputTokens is the scope budget ceiling (max_output-derived).
	MaxOutputTokens int
	// TotalLines is the file line count (falls back to scan.TotalLines).
	TotalLines int
}

// Engine scores structural complexity and selects a decomposition strategy.
type Engine struct {
	policy Policy
	logFn  func(string, ...interface{})
}

// Option configures an Engine.
type Option func(*Engine)

// WithLogFn wires an activity sink for the [scope] decision line.
func WithLogFn(fn func(string, ...interface{})) Option {
	return func(e *Engine) {
		if fn != nil {
			e.logFn = fn
		}
	}
}

// New constructs an Engine from a policy and options.
func New(policy Policy, opts ...Option) *Engine {
	e := &Engine{
		policy: policy,
		logFn:  func(string, ...interface{}) {},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// MetricsFromScan derives the structural complexity signals from a Lea scan
// report plus the token estimate and output ceiling. It is the single place
// that maps discovery data onto the scored metrics, keeping byte size as only
// one contributor (TokenToMaxOutputRatio).
func MetricsFromScan(scan *planner.LeaScanReport, estimatedTokens, maxOutputTokens int) StructuralMetrics {
	m := StructuralMetrics{}
	if scan != nil {
		m.ASTDepth = maxNodeDepth(scan.Nodes) + 1
		m.DependencyFanOut = len(scan.References)
		lines := scan.TotalLines
		if lines <= 0 {
			lines = len(scan.Nodes)
		}
		if lines > 0 {
			m.SymbolDensity = float64(len(scan.Nodes)) / float64(lines)
		}
	}
	denom := maxOutputTokens
	if denom <= 0 {
		denom = 1
	}
	if estimatedTokens > 0 {
		m.TokenToMaxOutputRatio = float64(estimatedTokens) / float64(denom)
	}
	return m
}

// log writes to the engine's activity sink.
func (e *Engine) log(format string, args ...interface{}) {
	if e != nil && e.logFn != nil {
		e.logFn(format, args...)
	}
}

// Evaluate scores the supplied structural input and returns the strategy
// decision. It is the single entry point the preflight planner hooks call:
// the file size is read ONLY through TokenToMaxOutputRatio, never treated as an
// architecture invariant.
func (e *Engine) Evaluate(in ScopeInput) ScopeDecision {
	m := MetricsFromScan(in.Scan, in.EstimatedTokens, in.MaxOutputTokens)
	return e.Decide(m, in.EstimatedTokens)
}

// Decide applies the policy to already-derived metrics and returns the
// decision. EstimatedCost is the generation estimate (tokens) the winning
// strategy would face, carried for TUI transparency.
func (e *Engine) Decide(m StructuralMetrics, estimatedCost int) ScopeDecision {
	p := e.policy
	score := p.ASTDepthWeight*float64(m.ASTDepth) +
		p.SymbolDensityWeight*m.SymbolDensity +
		p.DependencyFanOutWeight*float64(m.DependencyFanOut) +
		p.TokenRatioWeight*m.TokenToMaxOutputRatio

	d := ScopeDecision{
		Score:         score,
		EstimatedCost: estimatedCost,
		Metrics:       m,
	}
	if score >= p.Threshold {
		d.Strategy = StrategyDAG
		d.Reason = dagReason(m)
	} else {
		d.Strategy = StrategySinglePass
		d.Reason = singlePassReason(m)
	}
	e.log("[scope] strategy=%s score=%.2f reason=%q", d.Strategy, d.Score, d.Reason)
	return d
}

// ShouldDecompose reports whether the decision selects DAG decomposition
// (true) versus a single-pass bounded patch (false).
func (d ScopeDecision) ShouldDecompose() bool {
	return d.Strategy == StrategyDAG
}

func singlePassReason(m StructuralMetrics) string {
	return fmt.Sprintf("flat AST (depth=%d), low dependency fan-out (%d) — single-pass bounded patch",
		m.ASTDepth, m.DependencyFanOut)
}

func dagReason(m StructuralMetrics) string {
	return fmt.Sprintf("deep AST (depth=%d) + high dependency fan-out (%d) — DAG decomposition",
		m.ASTDepth, m.DependencyFanOut)
}

// maxNodeDepth returns the largest Depth among nodes (root depth is 0).
// Returns -1 when there are no nodes so callers can add 1 for a 0-based floor.
func maxNodeDepth(nodes []planner.DOMNode) int {
	max := -1
	for _, n := range nodes {
		if n.Depth > max {
			max = n.Depth
		}
	}
	return max
}
