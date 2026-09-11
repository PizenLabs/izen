package adaptive

import (
	"fmt"
	"sync"
)

// ContextTier is one rung of the progressive 5-tier context ladder.
// Workers initialize at the lowest sufficient tier (L0) and expand only
// on deterministic EvidencePressure signals.
type ContextTier int

const (
	// TierL0 is Minimum Viable AST Scope (~0.5k-1k tokens).
	TierL0 ContextTier = iota
	// TierL1 is Target Scope File/Module (~2k-4k tokens).
	TierL1
	// TierL2 is Structural Context (~4k-8k tokens).
	TierL2
	// TierL3 is Evidence & Execution Context (~8k-15k tokens).
	TierL3
	// TierL4 is Deep Package Scope (~20k-30k tokens).
	TierL4
)

// MaxTier is the top of the ladder.
const MaxTier = TierL4

// String returns the stable ladder label (L0..L4).
func (t ContextTier) String() string {
	switch t {
	case TierL0:
		return "L0"
	case TierL1:
		return "L1"
	case TierL2:
		return "L2"
	case TierL3:
		return "L3"
	case TierL4:
		return "L4"
	default:
		return "L?"
	}
}

// Valid reports whether t is a rung on the ladder.
func (t ContextTier) Valid() bool { return t >= TierL0 && t <= TierL4 }

// TokenRange returns the documented approximate token window for a tier.
func (t ContextTier) TokenRange() (min, max int) {
	switch t {
	case TierL0:
		return 500, 1000
	case TierL1:
		return 2000, 4000
	case TierL2:
		return 4000, 8000
	case TierL3:
		return 8000, 15000
	case TierL4:
		return 20000, 30000
	default:
		return 0, 0
	}
}

// Description returns the human-readable scope of a tier.
func (t ContextTier) Description() string {
	switch t {
	case TierL0:
		return "Minimum Viable AST Scope: symbol definition + direct file imports + directly referenced interface signatures"
	case TierL1:
		return "Target Scope File/Module: full file contents within the explicit ActiveTargetScope"
	case TierL2:
		return "Structural Context: AST slices, symbol definitions, caller/callee graphs, dependent test interfaces"
	case TierL3:
		return "Evidence & Execution Context: recent test outputs, execution cursor diffs, verification logs"
	case TierL4:
		return "Deep Package Scope: extended repository slices and distant module interfaces"
	default:
		return "unknown tier"
	}
}

// TokenBudget returns the upper bound of the tier's token window: the
// budget a worker at that tier may consume.
func (t ContextTier) TokenBudget() int {
	_, max := t.TokenRange()
	return max
}

// ContextPlanner tracks the per-task ladder rung. The zero value is NOT
// ready to use; construct with NewContextPlanner.
type ContextPlanner struct {
	mu    sync.Mutex
	tiers map[string]ContextTier
}

// NewContextPlanner returns a planner with every task at L0.
func NewContextPlanner() *ContextPlanner {
	return &ContextPlanner{tiers: make(map[string]ContextTier)}
}

// Tier returns the current rung for a task (default L0).
func (p *ContextPlanner) Tier(taskID string) ContextTier {
	if p == nil {
		return TierL0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tiers[taskID]
}

// Reset returns a task to L0 (e.g. fresh task creation).
func (p *ContextPlanner) Reset(taskID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tiers, taskID)
}

// SyncFromStore aligns the in-memory rung with the durable tier persisted
// in ledger.ndjson (used after replay / recovery).
func (p *ContextPlanner) SyncFromStore(taskID string, tier int) {
	if p == nil {
		return
	}
	t := ContextTier(tier)
	if !t.Valid() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tiers[taskID] = t
}

// Advance moves one rung up the ladder (Ln → Ln+1). It returns the new
// tier and false when already at L4 (no further expansion possible).
func (p *ContextPlanner) Advance(taskID string) (ContextTier, bool) {
	if p == nil {
		return TierL0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.tiers[taskID]
	if cur >= MaxTier {
		return MaxTier, false
	}
	cur++
	p.tiers[taskID] = cur
	return cur, true
}

// RequestExpansion is the ONLY gate for tier growth. It advances the
// ladder if and only if dec.Expand is true — i.e. a deterministic
// EvidencePressure signal fired. A confidence-only request
// (dec.Expand == false, e.g. `confidence: 0.2` with no signal) is
// rejected: the tier is returned unchanged with expanded=false.
//
// Callers MUST record the pressure event in ledger.ndjson (via the store)
// before or with this call; RequestExpansion enforces the decision-level
// invariant while the ledger enforces the audit-level invariant.
func (p *ContextPlanner) RequestExpansion(taskID string, dec PressureDecision) (tier ContextTier, expanded bool, reason string) {
	if !dec.Expand {
		return p.Tier(taskID), false,
			fmt.Sprintf("expansion rejected: %s (self-reported confidence never expands context)", dec.Reason)
	}
	next, ok := p.Advance(taskID)
	if !ok {
		return MaxTier, false, "already at maximum tier L4; no further expansion"
	}
	return next, true, "expanded on evidence pressure: " + string(dec.Signal)
}
