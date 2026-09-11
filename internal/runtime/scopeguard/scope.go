package scopeguard

import (
	"fmt"
	"strings"
)

// ScopeVerdict is the deterministic outcome of the ScopeGuard check.
type ScopeVerdict string

const (
	// ScopePass means every mutating target is inside AuthorizedTargetScope.
	ScopePass ScopeVerdict = "PASS"
	// ScopeRejected means at least one mutating target escapes the scope.
	// The proposal MUST NOT reach tool dispatch or LLM evaluation.
	ScopeRejected ScopeVerdict = "SCOPE_VIOLATION_REJECTED"
)

// ScopeResult carries the verdict plus the offending targets.
type ScopeResult struct {
	Verdict ScopeVerdict `json:"verdict"`
	// Violations are the normalized mutating targets outside scope.
	Violations []string `json:"violations,omitempty"`
	// Reason is a bounded human-readable justification.
	Reason string `json:"reason"`
}

// ScopeLedger is the minimal ledger sink for SCOPE_VIOLATION_REJECTED
// lineage. Implementations must append one audit event per rejection.
type ScopeLedger interface {
	RecordCustomEvent(taskID string, eventType string, payload map[string]any) error
}

// ScopeGuard is the first line of defense: it intercepts all worker
// proposals before tool dispatch or execution authorization and compares
// proposed file modifications against AuthorizedTargetScope.
//
// Hard enforcement rule: a mutating proposal (write, patch, edit,
// delete) to a file outside scope is REJECTED at the guard boundary —
// never passed to the LLM for semantic evaluation — and recorded as
// SCOPE_VIOLATION_REJECTED in ledger.ndjson.
type ScopeGuard struct {
	scope []string
}

// NewScopeGuard binds a guard to the task's AuthorizedTargetScope.
// The scope is copied and normalized; later caller mutation cannot
// widen the guard.
func NewScopeGuard(authorizedScope []string) *ScopeGuard {
	return &ScopeGuard{scope: normalizeScope(authorizedScope)}
}

// Scope returns a copy of the authorized scope.
func (g *ScopeGuard) Scope() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.scope...)
}

// Check applies the hard enforcement rule. Read/test proposals always
// pass (context expansion never grants write authority — Invariant 4 —
// so reads are audited but never rejected here); mutating proposals
// pass only when every target is inside scope. A nil guard rejects all
// mutating proposals (fail-closed).
func (g *ScopeGuard) Check(p Proposal) ScopeResult {
	if err := p.Validate(); err != nil {
		return ScopeResult{Verdict: ScopeRejected, Reason: "invalid proposal: " + err.Error()}
	}
	if !p.Op.Mutating() {
		return ScopeResult{Verdict: ScopePass, Reason: fmt.Sprintf("%s proposal carries no mutation authority", p.Op)}
	}
	if g == nil || len(g.scope) == 0 {
		return ScopeResult{
			Verdict:    ScopeRejected,
			Violations: append([]string(nil), p.TargetFiles...),
			Reason:     "no authorized target scope: all mutations rejected",
		}
	}
	var violations []string
	for _, t := range p.TargetFiles {
		if !withinScope(g.scope, t) {
			violations = append(violations, normalizeTarget(t))
		}
	}
	if len(violations) > 0 {
		return ScopeResult{
			Verdict:    ScopeRejected,
			Violations: violations,
			Reason:     "mutating target outside AuthorizedTargetScope: " + strings.Join(violations, ", "),
		}
	}
	return ScopeResult{Verdict: ScopePass, Reason: "all mutating targets within AuthorizedTargetScope"}
}

// Enforce runs Check and, on rejection, records a
// SCOPE_VIOLATION_REJECTED audit event before returning. The ledger sink
// may be nil (event omitted, verdict unchanged). It returns true when
// the proposal may continue to the StructuralGuard tier.
func (g *ScopeGuard) Enforce(p Proposal, ledger ScopeLedger) ScopeResult {
	res := g.Check(p)
	if res.Verdict == ScopeRejected && ledger != nil {
		_ = ledger.RecordCustomEvent(p.TaskID, "SCOPE_VIOLATION_REJECTED", map[string]any{
			"proposalId": p.ID,
			"op":         string(p.Op),
			"targets":    append([]string(nil), p.TargetFiles...),
			"violations": append([]string(nil), res.Violations...),
			"reason":     res.Reason,
		})
	}
	return res
}
