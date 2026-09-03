// Package tui — decision_surface.go explicitly binds DecisionSurface Option IDs
// to Autonomy Proposal Actions. It is the TUI-to-Autonomy intent mapping
// contract: every option's ID is the canonical ProposalIntent string the driver
// validates via the zero-call barrier before any preflight or provider call.
//
// Contract:
//
//	Option [1] Fall back to full-file  → ProposalFullFileFallback ("full_file_fallback")
//	Option [2] Re-prompt full text     → ProposalRepromptFullText ("reprompt_full_text")
//	Option Inject line-offset           → ProposalInjectLineOffset ("inject_line_offset")
//
// Hard-block options (Task 2) — appended by EnsureHardBlockOptions on
// every awaiting_human parking so the UI never deadlocks:
//
//	Abort Run & Return to Idle         → ProposalAbortRun ("abort_run")
//	Force Bounded Patch                 → ProposalForceBoundedPatch ("force_bounded_patch")
//	Switch Model                        → ProposalSwitchModel ("switch_model")
package tui

import "fmt"

// decisionSurfaceOptionIDs is the authoritative mapping from display index to
// canonical intent IDs. It is the single source for the adapter's Option.Key
// values and the driver's ParseProposalIntent normalization.
var decisionSurfaceOptionIDs = map[string]ProposalIntent{
	"1":                       ProposalFullFileFallback,
	"2":                       ProposalRepromptFullText,
	"full_file_fallback":      ProposalFullFileFallback,
	"reprompt_full_text":      ProposalRepromptFullText,
	"inject_line_offset":      ProposalInjectLineOffset,
	"abort_run":               ProposalAbortRun,
	"force_bounded_patch":     ProposalForceBoundedPatch,
	"switch_model":            ProposalSwitchModel,
	"IntentFullFileFallback":  ProposalFullFileFallback,
	"IntentRepromptFullText":  ProposalRepromptFullText,
	"IntentInjectLineOffset":  ProposalInjectLineOffset,
	"IntentAbortRun":          ProposalAbortRun,
	"IntentForceBoundedPatch": ProposalForceBoundedPatch,
	"IntentSwitchModel":       ProposalSwitchModel,
}

// resolveDecisionSurfaceIntent normalizes a raw TUI selection (index string
// "1"/"2", raw ID, or canonical intent) into the closed vocabulary the driver
// validates. It delegates to ParseProposalIntent which is the single
// normalization authority. It also handles empty Action fallback to index string
// as required by TASK 1: when opt.Action is empty, the caller should pass the
// index string ("1","2") so the intent resolves to the canonical fallback.
func resolveDecisionSurfaceIntent(raw string) ProposalIntent {
	if raw == "" {
		return ProposalIntent("")
	}
	return ParseProposalIntent(raw)
}

// resolveDecisionSurfaceIntentWithFallback handles the TASK 1 empty-Action case
// explicitly: when the selected option's Action/ID is empty, it falls back to
// the 1-based index string so the surface never emits an empty payload.
func resolveDecisionSurfaceIntentWithFallback(raw string, selectedIndex int) ProposalIntent {
	intentStr := raw
	if intentStr == "" {
		intentStr = fmt.Sprintf("%d", selectedIndex+1)
	}
	return ParseProposalIntent(intentStr)
}

// decisionSurfaceOptionIDFor returns the canonical ID string for an intent.
// It is used by renderers to ensure the published DecisionSurface carries the
// exact IDs the driver expects.
func decisionSurfaceOptionIDFor(intent ProposalIntent) string {
	return string(intent)
}

// Ensure the explicit bindings are referenced so the mapping is not dead code.
// This also provides a compile-time check that the constants exist.
var (
	_ = ProposalFullFileFallback
	_ = ProposalRepromptFullText
	_ = ProposalInjectLineOffset
	_ = ProposalAbortRun
	_ = ProposalForceBoundedPatch
	_ = ProposalSwitchModel
	_ = decisionSurfaceOptionIDs
	_ = resolveDecisionSurfaceIntent
	_ = resolveDecisionSurfaceIntentWithFallback
	_ = decisionSurfaceOptionIDFor
)
