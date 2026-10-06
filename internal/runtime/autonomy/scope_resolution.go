package autonomy

// ── Scope resolution: UNRESOLVED is not a verdict ───────────────────────────
//
// §15 AUDIT. The trace of a broad objective reads:
//
//	preflight targets=[]
//	snapshot target="" tokens=0
//	… later …
//	scope: index.html, script.js, styles.css
//
// which reads like an empty target state being treated as authoritative and then
// silently overwritten. It is not, and the audit conclusion is that the existing
// BEHAVIOUR is correct:
//
//	unresolved          the gateway classified a targetless objective; there is
//	                    no target to snapshot and NOTHING is inferred from the
//	                    absence
//	    ↓ evidence-bound discovery
//	discovered          the runtime OBSERVED workspace files that satisfy the
//	                    artifact kinds the objective itself declared
//	    ↓ authoritative resolution
//	resolved            the strategy gateway re-resolved over the OBSERVED
//	                    files and selected a mutation contract; those targets
//	                    are now authoritative
//
// The four states are formalised here so the transition is a typed fact rather
// than an inference a reader has to make from two differently-worded log lines.
//
// AMBIGUOUS EXISTS BECAUSE "NO TARGET" AND "SEVERAL TARGETS, NONE PROVEN" ARE
// DIFFERENT FACTS. Both leave the scope unresolved, but they ask the human
// different questions, and collapsing them is exactly how a non-empty candidate
// list came to be read as a resolved scope:
//
//	ambiguous evidence → non-empty candidates → ScopeResolved → mutation
//
// A non-empty candidate list is a QUESTION, not a decision. `ambiguous` carries
// the candidates as evidence and is terminal for authority purposes: nothing it
// holds may become a mutation scope until a human narrows it.
//
// WHAT IS NOT CHANGED. Target resolution stays evidence-bound. No target is ever
// invented, and an unresolved scope is never widened to make an objective
// executable — the derivation only ever proposes, and only the gateway's
// re-resolution over existing files can promote a proposal to authority.

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/events"
)

// ScopeResolutionState is the lifecycle position of an objective's target set.
type ScopeResolutionState string

const (
	// ScopeUnresolved: no target is bound and none was observed. This is an
	// ABSENCE OF A VERDICT, never a verdict of "no targets needed" — the two
	// are distinguishable precisely because the state records that the runtime
	// looked.
	ScopeUnresolved ScopeResolutionState = "UNRESOLVED"
	// ScopeDiscovered: candidates were observed in the workspace but the
	// strategy gateway has not yet accepted them as this run's authority.
	ScopeDiscovered ScopeResolutionState = "DISCOVERED"
	// ScopeAmbiguous: SEVERAL observed files satisfy the artifact kinds the
	// objective DECLARED, and the evidence does not establish which one(s) the
	// objective is about. The candidates are carried as evidence and NOTHING
	// here is a scope — a non-empty candidate list is a question, not a
	// resolution, and it must never cross into mutation authority.
	ScopeAmbiguous ScopeResolutionState = "AMBIGUOUS"
	// ScopeResolved: the gateway accepted the target set; the run may act on it.
	ScopeResolved ScopeResolutionState = "RESOLVED"
	// ScopeRefused: discovery observed candidates and the gateway declined to
	// treat them as a mutation scope. The run stays read-only and says so.
	ScopeRefused ScopeResolutionState = "REFUSED"
)

// AllScopeResolutionStates returns the closed vocabulary. It exists so a test can
// assert the lifecycle positions are exactly these values and no call site
// invented its own label.
func AllScopeResolutionStates() []ScopeResolutionState {
	return []ScopeResolutionState{
		ScopeUnresolved,
		ScopeDiscovered,
		ScopeAmbiguous,
		ScopeResolved,
		ScopeRefused,
	}
}

// String returns the canonical scope-resolution label.
func (s ScopeResolutionState) String() string { return string(s) }

// AuthorizesMutation reports whether the recorded scope may become a mutation
// scope. Exactly one state says yes. Every other position — including one holding
// a non-empty candidate set — is a question, not authority.
func (s ScopeResolutionState) AuthorizesMutation() bool { return s == ScopeResolved }

// ScopeResolution is the durable record of how one lifecycle's target set came to
// be. It is per-run evidence, not a log line: a run that reports "no target"
// without recording that it looked is indistinguishable from one that never
// looked at all.
type ScopeResolution struct {
	// State is the lifecycle position.
	State ScopeResolutionState
	// Targets are the authoritative (RESOLVED) or proposed (DISCOVERED) targets.
	Targets []string
	// Kinds are the artifact kinds the objective declared, when derivation ran.
	Kinds []string
	// Candidates are observed files that satisfy the objective's DECLARED
	// artifact kinds but are NOT proven to be what it is about. They are recorded
	// only for a non-authoritative position (AMBIGUOUS), so a reader can see the
	// exact question the human was asked; they are never a scope.
	Candidates []string
	// Reason is the deterministic justification, verbatim from the authority
	// that produced the transition.
	Reason string
}

// AuthorizesMutation reports whether this record may become a mutation scope.
// Exactly one state says yes. A record holding candidates does not.
func (r ScopeResolution) AuthorizesMutation() bool { return r.State.AuthorizesMutation() }

// noteScopeTransition records a scope-resolution transition and publishes it as
// structured telemetry. Publishing the TRANSITION (not just the result) is what
// makes `targets=[]` legible: a reader sees the state change that produced the
// targets instead of inferring it from two unrelated lines.
func (d *Driver) noteScopeTransition(res ScopeResolution) {
	if d == nil {
		return
	}
	d.scopeResolution = res
	if d.bus == nil {
		return
	}
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[scope] %s -> targets=[%s]%s%s%s",
		res.State, strings.Join(res.Targets, ","),
		candidateSuffix(res.Candidates), kindSuffix(res.Kinds), reasonSuffix(res.Reason))))
}

// candidateSuffix renders the disambiguation candidate set. It is deliberately a
// DIFFERENT label from targets: publishing "candidates" on a line that also says
// AMBIGUOUS is what stops a reader from reading the list as a bound scope.
func candidateSuffix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	return " candidates=[" + strings.Join(candidates, ",") + "]"
}

func kindSuffix(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	return " kinds=[" + strings.Join(kinds, ",") + "]"
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " — " + reason
}
