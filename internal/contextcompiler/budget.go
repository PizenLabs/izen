// Package contextcompiler is the Context Compilation authority (SESSION.md
// §18-19).
//
// Architectural separation: this runtime engine is responsible ONLY for
// deciding what context is injected into the LLM prompt under a strict Token
// Budget. It consumes Snapshot + Project Knowledge + Recent Turns + Workflow
// State + Artifacts and emits a budget-fitted CompiledContext. It never owns
// compaction (session continuity) and never owns knowledge promotion (durable
// cross-session knowledge).
package contextcompiler

// Source identifies one context source the compiler may admit. The priority
// order follows SESSION.md §19 (relevance over chronology).
type Source string

const (
	// SourceUserRequest is the current user request — always admitted first.
	SourceUserRequest Source = "user_request"
	// SourceWorkflow is the active workflow state.
	SourceWorkflow Source = "workflow_state"
	// SourceRecentTurns is the recent conversation.
	SourceRecentTurns Source = "recent_turns"
	// SourceSessionCompact is the session's compact context.
	SourceSessionCompact Source = "session_compact"
	// SourceArtifacts is the relevant artifact surface.
	SourceArtifacts Source = "artifacts"
	// SourceProjectKnowledge is relevant project knowledge chunks
	// (independently addressed, never a monolithic summary).
	SourceProjectKnowledge Source = "project_knowledge"
)

// priorityOrder is the admission order (SESSION.md §19): user request first,
// project knowledge last — the first to be dropped when the budget is tight.
var priorityOrder = []Source{
	SourceUserRequest,
	SourceWorkflow,
	SourceRecentTurns,
	SourceSessionCompact,
	SourceArtifacts,
	SourceProjectKnowledge,
}

// defaultShares is the dynamic per-source token allocation (percent, sums to
// 100). It is a policy table, not an architectural invariant — every share is
// overridable via WithShares.
var defaultShares = map[Source]int{
	SourceUserRequest:      20,
	SourceWorkflow:         10,
	SourceRecentTurns:      25,
	SourceSessionCompact:   20,
	SourceArtifacts:        10,
	SourceProjectKnowledge: 15,
}

// DefaultMaxTokens caps a compiled context window. The value derives from the
// ~4 chars/token heuristic and leaves room for the system prompt and the
// completion budget.
const DefaultMaxTokens = 4000

// Budget is the dynamic token budget for one compilation. Total is the global
// cap; BySource is the per-source share derived from the allocation table.
type Budget struct {
	Total    int
	BySource map[Source]int
}

// Source returns the per-source token share.
func (b Budget) Source(src Source) int {
	if b.BySource == nil {
		return 0
	}
	return b.BySource[src]
}

// Allocate derives per-source shares from a total cap and an allocation table.
// A non-positive cap falls back to DefaultMaxTokens; missing shares default to
// 0. Percentages that do not sum to 100 are normalized.
func Allocate(total int, shares map[Source]int) Budget {
	if total <= 0 {
		total = DefaultMaxTokens
	}
	sum := 0
	for _, pct := range shares {
		sum += pct
	}
	if sum <= 0 {
		shares = defaultShares
		sum = 100
	}
	by := make(map[Source]int, len(shares))
	for src, pct := range shares {
		by[src] = total * pct / sum
	}
	return Budget{Total: total, BySource: by}
}

// EstimateTokens is the coarse ~4 chars/token accounting heuristic. It is the
// compiler's single token oracle, used for budgeting, fitting and telemetry.
func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len([]rune(s)) + 3) / 4
}
