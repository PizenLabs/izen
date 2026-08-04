package planner

import "strings"

// SourceType identifies a context source category the planner may query.
// Each intent maps to a strict percentage allocation across these sources
// (see allocationFor), and each source has a fixed truncation priority.
type SourceType string

const (
	// SourceLog is Phase 1 Tee tool output (`.logs/`): panics, test failures,
	// compiler output and execution traces.
	SourceLog SourceType = "log"
	// SourceCallTree is a Lea TraceCallChain reconstruction of a symbol's
	// callers/callees.
	SourceCallTree SourceType = "call_tree"
	// SourceGraph is structural symbol data resolved from the Lea graph
	// (definitions, interfaces, call trees, package structure).
	SourceGraph SourceType = "graph"
	// SourceArch is the Lea GetArchitectureSummary + FindRoutes overview.
	SourceArch SourceType = "architecture"
	// SourceFile is a focused file snippet (raw source region).
	SourceFile SourceType = "file"
)

// String returns the stable serialized form of the source type.
func (s SourceType) String() string { return string(s) }

// DefaultMaxContextTokens caps the total assembled context window. The value
// is derived from the ~4 chars/token heuristic: 4000 tokens ≈ 16KB of text,
// which fits comfortably inside every model window while leaving room for the
// system prompt, conversation history and the completion budget.
const DefaultMaxContextTokens = 4000

// Allocation describes how one intent splits the token budget between context
// sources. Percent values sum to 100. Priority lists the same sources in
// ascending truncation order: the earliest entry is kept first when the
// budget is exceeded.
type Allocation struct {
	Intent   Intent
	Percent  map[SourceType]int
	Priority []SourceType
}

// Budget is the computed token budget for a single planning run. Total is the
// global cap; BySource is the per-source share derived from the intent's
// percentage allocation.
type Budget struct {
	Total    int
	BySource map[SourceType]int
}

// Source returns the per-source token share for the given source.
func (b Budget) Source(src SourceType) int {
	if b.BySource == nil {
		return 0
	}
	return b.BySource[src]
}

// allocationFor returns the fixed allocation table for an intent. EXPLANATION
// and GENERAL share the balanced split; BUG_FIX mirrors the reference
// 50% log / 30% call tree / 20% file snippet split.
func allocationFor(intent Intent) Allocation {
	switch intent {
	case IntentBugFix:
		return Allocation{
			Intent: intent,
			Percent: map[SourceType]int{
				SourceLog:      50,
				SourceCallTree: 30,
				SourceFile:     20,
			},
			Priority: []SourceType{SourceLog, SourceCallTree, SourceFile},
		}
	case IntentArchitecture:
		return Allocation{
			Intent: intent,
			Percent: map[SourceType]int{
				SourceArch:  60,
				SourceGraph: 40,
			},
			Priority: []SourceType{SourceArch, SourceGraph},
		}
	case IntentRefactor:
		return Allocation{
			Intent: intent,
			Percent: map[SourceType]int{
				SourceGraph:    40,
				SourceCallTree: 30,
				SourceFile:     30,
			},
			Priority: []SourceType{SourceGraph, SourceCallTree, SourceFile},
		}
	default: // EXPLANATION, GENERAL
		return Allocation{
			Intent: intent,
			Percent: map[SourceType]int{
				SourceGraph: 50,
				SourceFile:  50,
			},
			Priority: []SourceType{SourceGraph, SourceFile},
		}
	}
}

// Allocate computes the per-source token budget for the given intent and
// total cap. A non-positive cap falls back to DefaultMaxContextTokens.
func Allocate(intent Intent, totalTokens int) Budget {
	if totalTokens <= 0 {
		totalTokens = DefaultMaxContextTokens
	}
	alloc := allocationFor(intent)
	bySource := make(map[SourceType]int, len(alloc.Percent))
	for src, pct := range alloc.Percent {
		bySource[src] = totalTokens * pct / 100
	}
	return Budget{Total: totalTokens, BySource: bySource}
}

// Chunk is one unit of retrieved context. Priority is the source's truncation
// rank (ascending = kept first); Score is the relevance signal used to order
// chunks within the same priority. Tokens is the estimated token weight.
type Chunk struct {
	Source   SourceType
	Content  string
	Score    float64
	Priority int
	Tokens   int
}

// ContextPlan is the outcome of one planning run: the classified intent, the
// computed budget, the ranked and budget-fitted chunks, and the telemetry
// describing how the budget was enforced.
type ContextPlan struct {
	Intent     Intent
	Budget     Budget
	Chunks     []Chunk
	TokenTotal int
	Truncated  bool
	Dropped    int
}

// headerFor maps a source type to its rendered section header.
func headerFor(src SourceType) string {
	switch src {
	case SourceLog:
		return "TOOL LOG"
	case SourceCallTree:
		return "CALL CHAIN"
	case SourceGraph:
		return "SYMBOL DEFINITIONS"
	case SourceArch:
		return "ARCHITECTURE"
	case SourceFile:
		return "FILE SNIPPET"
	default:
		return string(src)
	}
}

// Assemble renders the plan's chunks into a prompt-ready context block. The
// total rendered token weight is guaranteed ≤ Budget.Total by construction.
func (p *ContextPlan) Assemble() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	for i, c := range p.Chunks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("### " + headerFor(c.Source) + "\n")
		b.WriteString(c.Content)
	}
	return b.String()
}

// DebugInfo is an on-demand diagnostic snapshot of one context-governance run:
// the allocated token budget versus the chunks that were retrieved, kept, and
// dropped by budget enforcement. RetrievedChunks counts every chunk gathered
// before the budget enforcer ran (FittedChunks + DroppedChunks).
type DebugInfo struct {
	Intent          Intent
	AllocatedTokens int
	RetrievedChunks int
	FittedChunks    int
	DroppedChunks   int
	UsedTokens      int
}

// Debug materializes the governance snapshot of a completed planning run. It
// is pure — no state is tracked during planning beyond the plan fields the
// budget enforcer already produces.
func (p *ContextPlan) Debug() DebugInfo {
	if p == nil {
		return DebugInfo{}
	}
	di := DebugInfo{
		Intent:          p.Intent,
		AllocatedTokens: p.Budget.Total,
		FittedChunks:    len(p.Chunks),
		DroppedChunks:   p.Dropped,
		UsedTokens:      p.TokenTotal,
	}
	di.RetrievedChunks = di.FittedChunks + di.DroppedChunks
	return di
}
