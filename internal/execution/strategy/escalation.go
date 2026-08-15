package strategy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── PHASE 11 — EVIDENCE-DRIVEN ESCALATION ──────────────────────────────────
//
// Escalation is the engine's recorded answer to "why did this execution grow?"
// Every escalation — a strategy change (target ambiguity → human
// clarification), an evidence expansion (single-file → repository scope), or a
// context expansion — is recorded with its previous state, new state, the
// evidence that caused it, the additional context introduced, and the reason
// the previous level was insufficient. There is no silent escalation: a
// decision record that grows without an escalation entry is a bug.

// EscalationRecord is one evidence-driven escalation of an operation.
type EscalationRecord struct {
	// From is the previous state / level ("targeted_mutation",
	// "single-file context").
	From string
	// To is the new state / level ("human_clarification", "repository_scope").
	To string
	// Evidence names the concrete evidence that caused the escalation.
	Evidence string
	// AdditionalContext names the context channels introduced by the
	// escalation ("" when none).
	AdditionalContext string
	// Reason explains why the previous level was insufficient.
	Reason string
	// At is when the escalation was recorded.
	At time.Time
}

// String renders the escalation compactly for $inspect.
func (e EscalationRecord) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s -> %s", e.From, e.To)
	if e.Evidence != "" {
		b.WriteString(" evidence=" + e.Evidence)
	}
	if e.AdditionalContext != "" {
		b.WriteString(" context=" + e.AdditionalContext)
	}
	if e.Reason != "" {
		b.WriteString(" reason=" + e.Reason)
	}
	return b.String()
}

// Recorder accumulates escalation records safely. It is used by recorders
// that build a graph incrementally from real runtime boundaries.
type Recorder struct {
	mu      sync.Mutex
	records []EscalationRecord
	now     func() time.Time
}

// NewRecorder returns an empty escalation recorder.
func NewRecorder() *Recorder { return &Recorder{now: time.Now} }

// Record appends one escalation and returns it.
func (r *Recorder) Record(from, to, evidence, additionalContext, reason string) EscalationRecord {
	now := time.Now
	if r != nil && r.now != nil {
		now = r.now
	}
	rec := EscalationRecord{
		From:              from,
		To:                to,
		Evidence:          evidence,
		AdditionalContext: additionalContext,
		Reason:            reason,
		At:                now(),
	}
	if r != nil {
		r.mu.Lock()
		r.records = append(r.records, rec)
		r.mu.Unlock()
	}
	return rec
}

// Records returns the recorded escalations in order.
func (r *Recorder) Records() []EscalationRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EscalationRecord, len(r.records))
	copy(out, r.records)
	return out
}

// Count returns the number of recorded escalations.
func (r *Recorder) Count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// EscalationsFor derives the escalation records implied by a strategy-profile
// decision and its compiled context envelope. The profile's Escalation flag
// (set by the selector for human clarification / repository work) becomes a
// recorded, evidence-carrying escalation; an expanded context envelope
// contributes a context-escalation record. It is pure: it never mutates the
// profile or envelope.
func EscalationsFor(p ExecutionStrategyProfile, env ContextEnvelope) []EscalationRecord {
	var out []EscalationRecord

	if p.Escalation {
		rec := EscalationRecord{
			From:     "initial",
			To:       "human_clarification",
			Evidence: p.StrategyReason,
			Reason:   p.EscalationReason,
		}
		if rec.Reason == "" {
			rec.Reason = "the human is authoritative over ambiguity, scope and risk"
		}
		if rec.Evidence == "" {
			rec.Evidence = "target resolution is not deterministically sufficient"
		}
		out = append(out, rec)
		return out
	}

	// Evidence-driven strategy expansion is the only other sanctioned
	// escalation: the strategy itself names the expansion reason.
	switch p.Strategy {
	case RepositoryInvestigation:
		out = append(out, EscalationRecord{
			From:              "initial",
			To:                "repository_investigation",
			Evidence:          p.StrategyReason,
			AdditionalContext: "dependency_evidence, repository_constraints",
			Reason:            "the request demands repository evidence before reasoning",
		})
	case MultiFilePlanning:
		out = append(out, EscalationRecord{
			From:              "initial",
			To:                "multi_file_planning",
			Evidence:          p.StrategyReason,
			AdditionalContext: "dependency_evidence, repository_constraints",
			Reason:            "no explicit target set; repository-level reasoning is justified",
		})
	}

	if env.Expanded {
		out = append(out, EscalationRecord{
			From:              "sufficient_context",
			To:                "expanded_context",
			Evidence:          env.ExpansionReason,
			AdditionalContext: contextKindsOf(env),
			Reason:            env.ExpansionReason,
		})
	}
	return out
}

// contextKindsOf returns the sorted context-kind labels of the envelope.
func contextKindsOf(env ContextEnvelope) string {
	if len(env.Items) == 0 {
		return ""
	}
	seen := map[string]bool{}
	for _, it := range env.Items {
		seen[it.Kind.Label()] = true
	}
	labels := make([]string, 0, len(seen))
	for l := range seen {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return strings.Join(labels, ",")
}
