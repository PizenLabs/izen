// Package compaction is the Session Compaction authority (SESSION.md §13).
//
// Architectural separation: this subsystem is responsible ONLY for preserving
// single-session continuity. It reads a session's raw history stream and
// produces generational CompactContexts. It NEVER owns project knowledge and
// NEVER compiles prompt context — those belong to the Knowledge Lifecycle and
// Context Compiler subsystems.
//
// Phase 2 replaces fixed turn limits with ADAPTIVE, event-driven checkpoints:
// the policy reacts to turn volume, raw-event volume, and summary token
// growth, so when (or whether) a session is compacted is decided by observed
// state, not a hardcoded window.
package compaction

import (
	"github.com/PizenLabs/izen/internal/session"
)

// Policy drives adaptive compaction checkpointing. Every threshold is a
// configurable knob — there is deliberately NO fixed turn limit invariant.
type Policy struct {
	// TurnThreshold is the number of NEW user turns since the last sealed
	// checkpoint that force a new generation.
	TurnThreshold int
	// EventThreshold is the number of raw history events (turns of any role)
	// since the last sealed checkpoint that force a new generation. It is the
	// event-driven fallback for long assistant-only stretches.
	EventThreshold int
	// TokenGrowthFactor triggers a new generation when the turns accumulated
	// since the last checkpoint would grow the summary beyond factor × its
	// previous size. Values < 1 disable token-growth adaptation.
	TokenGrowthFactor float64
	// MinGapTurns is the minimum number of new turns required to seal a
	// checkpoint, preventing thrashing on short bursts.
	MinGapTurns int
	// RecentWindow is how many recent turns a generation carries verbatim
	// (SESSION.md §12.3 — the "recent context" tier).
	RecentWindow int
}

// DefaultPolicy returns the adaptive production defaults. All values are
// policy knobs, not architectural invariants.
func DefaultPolicy() Policy {
	return Policy{
		TurnThreshold:     10,
		EventThreshold:    20,
		TokenGrowthFactor: 1.5,
		MinGapTurns:       2,
		RecentWindow:      6,
	}
}

// tokenGrowthFloor is the minimum appended-token volume required before
// token-growth adaptation may fire. It prevents a small 1.5× factor from
// thrashing on tiny summaries; it is a token-accounting constant, not a fixed
// turn invariant.
const tokenGrowthFloor = 100

// ShouldCheckpoint decides whether the accumulated history since the last
// sealed checkpoint warrants a new generation. It implements the adaptive
// rule set: any single threshold crossing is sufficient.
//
//   - The EVENT threshold fires unconditionally — an assistant-heavy run with
//     few user turns still warrants a checkpoint (the raw-event volume IS the
//     gap signal).
//   - Token-growth fires unconditionally too: a single huge prompt that would
//     double the summary warrants folding immediately, and its 100-token
//     floor is its own anti-thrash guard.
//   - The TURN threshold alone is gated by MinGapTurns so a short turn burst
//     cannot thrash generations.
func (p Policy) ShouldCheckpoint(turnsSince, eventsSince, appendedTokens, baseSummaryTokens int) bool {
	if p.EventThreshold > 0 && eventsSince >= p.EventThreshold {
		return true
	}
	if p.TokenGrowthFactor >= 1 && baseSummaryTokens > 0 {
		if appendedTokens >= tokenGrowthFloor && float64(appendedTokens) > p.TokenGrowthFactor*float64(baseSummaryTokens) {
			return true
		}
	}
	if turnsSince < p.MinGapTurns {
		return false
	}
	if p.TurnThreshold > 0 && turnsSince >= p.TurnThreshold {
		return true
	}
	return false
}

// countUserTurns returns the number of user-role turns in a message slice.
func countUserTurns(msgs []session.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" {
			n++
		}
	}
	return n
}
