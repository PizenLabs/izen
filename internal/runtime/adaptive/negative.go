package adaptive

import (
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
)

// NegativeKnowledgeStatus is the lifecycle of a rejected hypothesis.
type NegativeKnowledgeStatus string

const (
	// StatusActiveNegativeKnowledge constrains every handoff whose scope
	// overlaps the entry's TargetScope.
	StatusActiveNegativeKnowledge NegativeKnowledgeStatus = "ACTIVE"
	// StatusStaleNegativeKnowledge no longer constrains: a RE_PLAN or
	// structural scope shift invalidated its preconditions.
	StatusStaleNegativeKnowledge NegativeKnowledgeStatus = "STALE"
)

// NegativeKnowledge is one hypothesis disproven with concrete evidence
// (failed build, failing unit test, static-analysis error).
type NegativeKnowledge struct {
	ID           string
	Hypothesis   string
	WhyRejected  string
	EvidenceRefs []string
	TargetScope  []string
	Status       NegativeKnowledgeStatus
}

// Valid reports whether n carries the minimum fields for persistence:
// a hypothesis plus at least one concrete evidence reference. Bare model
// assertion without evidence is never negative knowledge.
func (n NegativeKnowledge) Valid() error {
	if strings.TrimSpace(n.Hypothesis) == "" {
		return fmt.Errorf("adaptive: empty negative-knowledge hypothesis")
	}
	if len(n.EvidenceRefs) == 0 {
		return fmt.Errorf("adaptive: negative knowledge requires evidence refs")
	}
	return nil
}

// NegativeLedger is the in-memory index over the durable
// NEGATIVE_KNOWLEDGE_* events. Durable TaskStore remains the source of
// truth; this ledger is the ergonomic query/validation layer.
type NegativeLedger struct {
	mu      sync.Mutex
	entries map[string][]NegativeKnowledge
	nextID  int
}

// NewNegativeLedger returns an empty ledger.
func NewNegativeLedger() *NegativeLedger {
	return &NegativeLedger{entries: make(map[string][]NegativeKnowledge)}
}

// Record persists one disproven hypothesis as ACTIVE, both in-memory and
// (when store != nil) in ledger.ndjson via NEGATIVE_KNOWLEDGE_RECORDED.
// A failing verification MUST call this: the acceptance suite asserts the
// ledger event exists.
func (l *NegativeLedger) Record(taskID string, store *durable.TaskStore, n NegativeKnowledge) (NegativeKnowledge, error) {
	if strings.TrimSpace(taskID) == "" {
		return NegativeKnowledge{}, fmt.Errorf("adaptive: empty task id")
	}
	if err := n.Valid(); err != nil {
		return NegativeKnowledge{}, err
	}
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string][]NegativeKnowledge)
	}
	if strings.TrimSpace(n.ID) == "" {
		l.nextID++
		n.ID = fmt.Sprintf("NK-%04d", l.nextID)
	}
	n.Status = StatusActiveNegativeKnowledge
	n.EvidenceRefs = append([]string(nil), n.EvidenceRefs...)
	n.TargetScope = append([]string(nil), n.TargetScope...)
	l.entries[taskID] = append(l.entries[taskID], n)
	l.mu.Unlock()

	if store != nil {
		if err := store.RecordNegativeKnowledge(taskID, durable.NegativeKnowledgeRecord{
			ID:           n.ID,
			Hypothesis:   n.Hypothesis,
			WhyRejected:  n.WhyRejected,
			EvidenceRefs: append([]string(nil), n.EvidenceRefs...),
			TargetScope:  append([]string(nil), n.TargetScope...),
			Status:       durable.NegativeStatusActive,
		}); err != nil {
			return NegativeKnowledge{}, err
		}
	}
	return n, nil
}

// ActiveForScope returns ACTIVE entries whose TargetScope overlaps scope.
// Entries with an empty TargetScope are scope-universal. Empty scope
// matches all ACTIVE entries.
func (l *NegativeLedger) ActiveForScope(taskID string, scope []string) []NegativeKnowledge {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []NegativeKnowledge
	for _, n := range l.entries[taskID] {
		if n.Status != StatusActiveNegativeKnowledge {
			continue
		}
		if len(scope) > 0 && len(n.TargetScope) > 0 && !scopesOverlap(n.TargetScope, scope) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// All returns every entry (active + stale) for a task.
func (l *NegativeLedger) All(taskID string) []NegativeKnowledge {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]NegativeKnowledge(nil), l.entries[taskID]...)
}

// MarkStaleOnReplan transitions to STALE every ACTIVE entry whose
// TargetScope no longer overlaps newScope (scope-universal entries are
// retained), both in-memory and in the ledger via
// NEGATIVE_KNOWLEDGE_STALED. STALE entries are withheld from active
// constraints so they cannot block valid execution under the new scope.
// An empty newScope transitions nothing. Returns the count transitioned.
func (l *NegativeLedger) MarkStaleOnReplan(taskID string, store *durable.TaskStore, newScope []string, reason string) (int, error) {
	if len(newScope) == 0 {
		return 0, nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "RE_PLAN scope shift invalidated preconditions"
	}
	l.mu.Lock()
	var ids []string
	for i, n := range l.entries[taskID] {
		if n.Status != StatusActiveNegativeKnowledge || len(n.TargetScope) == 0 {
			continue
		}
		if !scopesOverlap(n.TargetScope, newScope) {
			l.entries[taskID][i].Status = StatusStaleNegativeKnowledge
			ids = append(ids, n.ID)
		}
	}
	l.mu.Unlock()
	for _, id := range ids {
		if store != nil {
			if err := store.MarkNegativeKnowledgeStale(taskID, id, reason); err != nil {
				return len(ids), err
			}
		}
	}
	return len(ids), nil
}

// ValidateProposal rejects a worker proposal that attempts a known failed
// approach within scope (Negative Knowledge Inviolability). It delegates
// to ephemeral.ValidateProposal over the ACTIVE in-scope constraints.
func (l *NegativeLedger) ValidateProposal(taskID, proposal string, scope []string) error {
	active := l.ActiveForScope(taskID, scope)
	constraints := make([]ephemeral.NegativeConstraint, 0, len(active))
	for _, n := range active {
		constraints = append(constraints, ToEphemeralConstraint(n))
	}
	return ephemeral.ValidateProposal(proposal, constraints)
}

// ToEphemeralConstraint converts one entry to its handoff projection.
func ToEphemeralConstraint(n NegativeKnowledge) ephemeral.NegativeConstraint {
	return ephemeral.NegativeConstraint{
		Hypothesis:   n.Hypothesis,
		WhyRejected:  n.WhyRejected,
		EvidenceRefs: append([]string(nil), n.EvidenceRefs...),
		TargetScope:  append([]string(nil), n.TargetScope...),
	}
}

// ToEphemeralConstraints projects ACTIVE in-scope entries for a
// ResumeContract.
func (l *NegativeLedger) ToEphemeralConstraints(taskID string, scope []string) []ephemeral.NegativeConstraint {
	active := l.ActiveForScope(taskID, scope)
	out := make([]ephemeral.NegativeConstraint, 0, len(active))
	for _, n := range active {
		out = append(out, ToEphemeralConstraint(n))
	}
	return out
}

// AttachToContract sets the contract's NegativeConstraints to the ACTIVE
// in-scope entries so the replacement worker receives them under
// [IMMUTABLE NEGATIVE CONSTRAINTS].
func (l *NegativeLedger) AttachToContract(taskID string, c *ephemeral.ResumeContract) {
	if c == nil {
		return
	}
	scope := c.Capsule.ActiveScope
	c.NegativeConstraints = l.ToEphemeralConstraints(taskID, scope)
}

// RenderImmutableConstraints serializes entries into the ResumeContract
// system-prompt block:
//
//	[IMMUTABLE NEGATIVE CONSTRAINTS]
//	DO NOT ATTEMPT THE FOLLOWING REJECTED APPROACHES:
//	- Approach: "..." / Reason: "..." / Evidence: [...]
//
// CANONICAL API: this is the canonical entry point for constraint-block
// rendering. The block bytes are produced by the single implementation in
// durable.RenderImmutableConstraintsBlock (ephemeral.RenderResumePrompt
// delegates to the same renderer), so the format cannot drift between
// packages. Only ACTIVE entries are rendered; STALE entries are withheld.
func RenderImmutableConstraints(entries []NegativeKnowledge) string {
	constraints := make([]durable.ImmutableConstraint, 0, len(entries))
	for _, n := range entries {
		if n.Status != StatusActiveNegativeKnowledge {
			continue
		}
		constraints = append(constraints, durable.ImmutableConstraint{
			Hypothesis:   n.Hypothesis,
			WhyRejected:  n.WhyRejected,
			EvidenceRefs: append([]string(nil), n.EvidenceRefs...),
		})
	}
	return durable.RenderImmutableConstraintsBlock(constraints)
}

func scopesOverlap(a, b []string) bool {
	for _, x := range a {
		nx := strings.TrimSpace(x)
		if nx == "" {
			continue
		}
		for _, y := range b {
			ny := strings.TrimSpace(y)
			if ny == "" {
				continue
			}
			if nx == ny || strings.HasPrefix(nx, ny) || strings.HasPrefix(ny, nx) {
				return true
			}
		}
	}
	return false
}
