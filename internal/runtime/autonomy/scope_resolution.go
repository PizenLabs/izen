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
// The three states are formalised here so the transition is a typed fact rather
// than an inference a reader has to make from two differently-worded log lines.
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
	// ScopeResolved: the gateway accepted the target set; the run may act on it.
	ScopeResolved ScopeResolutionState = "RESOLVED"
	// ScopeRefused: discovery observed candidates and the gateway declined to
	// treat them as a mutation scope. The run stays read-only and says so.
	ScopeRefused ScopeResolutionState = "REFUSED"
)

// String returns the canonical scope-resolution label.
func (s ScopeResolutionState) String() string { return string(s) }

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
	// Reason is the deterministic justification, verbatim from the authority
	// that produced the transition.
	Reason string
}

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
		"[scope] %s -> targets=[%s]%s%s",
		res.State, strings.Join(res.Targets, ","),
		kindSuffix(res.Kinds), reasonSuffix(res.Reason))))
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
