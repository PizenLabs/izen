// ── Main Narrative / Trace Overlay boundary (Phase 14) ───────────────────────
//
// The Main Narrative buffer carries USER-FACING EXECUTION EVIDENCE only. It is
// what a person reads to answer "what did you actually do, and what changed?".
// Infrastructure telemetry is not that: a loop transition, a context-compilation
// summary, a barrier wait and a preflight decision are all facts about the
// runtime talking to itself.
//
// Those facts are still generated at the source — deleting them would destroy
// the forensic record. They are FILTERED HERE, at the TUI subscriber boundary,
// and routed EXCLUSIVELY to the Trace Overlay (Alt+T). The boundary moves
// information; it never discards it.
//
// Why a hard guard rather than per-call-site discipline: every future event
// handler would otherwise have to remember the rule, and the first one that
// forgets puts "[loop] interpreting → completed" back in front of a user at
// the exact moment they are deciding whether to trust the result. The guard
// below is the single choke point, so that regression is structurally
// impossible rather than merely discouraged.
package ui

import "strings"

// InfrastructureTelemetryPrefixes are the canonical infrastructure tags. A
// record whose FIRST token is one of them describes the runtime, not the work.
//
// PHASE 15 completed the set. The Phase 14 list covered the four families the
// loop itself emitted, and two tags the runtime publishes through the SAME
// activity channel were left out — which is precisely the failure mode this
// classifier exists to prevent:
//
//	[grant]   a capability authorization. A reader who sees "read+mutate granted"
//	          in the narrative is reading the authorization layer, not the work.
//	[intent]  an intent parse or an intent revision. The canonical intent is an
//	          internal authority state; surfacing it mid-run tells the reader the
//	          system is still deciding WHAT it was asked to do, which is the worst
//	          possible moment for that fact.
var InfrastructureTelemetryPrefixes = []string{
	"[loop]",      // autonomous loop transitions
	"[context]",   // context-compilation summaries
	"[barrier]",   // synchronization barrier waits
	"[grant]",     // session capability authorizations
	"[preflight]", // zero-token preflight decisions
	"[index]",     // workspace index / AST mapping phases
	"[intent]",    // intent classification and intent-revision transactions
	"[objective]", // objective-authority verdicts
}

// IsInfrastructureTelemetry reports whether a rendered line is infrastructure
// telemetry that belongs in the Trace Overlay rather than the Main Narrative.
//
// The test is deliberately narrow — a leading tag only, case-insensitive, after
// trimming. It never matches on substrings, because a user-visible sentence can
// legitimately mention "[preflight]" as subject matter; only a record that
// OPENS with the tag is a machine line.
func IsInfrastructureTelemetry(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	// A multi-line record is classified by its first line: a record that opens
	// with a machine tag is a machine record end to end.
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range InfrastructureTelemetryPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
	}
	return false
}

// IsUserFacingEvidence is the positive spelling of the boundary: it reports
// whether a line is admissible to the Main Narrative. It is exactly the
// negation of IsInfrastructureTelemetry, and it exists so call sites read as a
// statement about what the user sees rather than as a list of tags to avoid.
func IsUserFacingEvidence(text string) bool {
	return strings.TrimSpace(text) != "" && !IsInfrastructureTelemetry(text)
}
