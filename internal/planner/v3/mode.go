package planner

// Mode is a planning execution mode. It is the planner-layer contract that
// replaces ad-hoc boolean flags: a run is planned either as a one-shot
// greenfield generation or as an interactive brownfield repair graph.
type Mode string

// Supported planning modes.
const (
	// ModeGreenfield plans a one-shot batch workspace write in which every
	// generated artifact is a Direct Full-File Overwrite staged through the
	// transaction. The Search/Replace diff parser is disabled for the run, so
	// obsolete workspace code can never anchor the model or trigger
	// patch-thrashing repair loops.
	ModeGreenfield Mode = "greenfield"
	// ModeBrownfield plans an interactive edit-and-verify graph over an
	// existing workspace.
	ModeBrownfield Mode = "brownfield"
)

// ExecutionModeForPolicy decides the strict planning mode from the two rewrite
// signals the runtime derives from an intent:
//
//   - rewritePolicy is true when the compiled ContextPolicy is PolicyRewrite
//     (a total replacement/redesign: the current workspace content is
//     obsolete and User Intent is the absolute source of truth).
//   - preserveWorkspace mirrors ir.IntentIR.PreserveWorkspace; nil when no
//     compiled intent is available.
//
// A rewrite policy OR a non-preserving intent ALWAYS forces Greenfield
// Generation Mode. Keeping the brownfield Search/Replace path would contradict
// the user's explicit replacement: diffs would be applied against obsolete
// files, obsolete code would re-enter the model context and small models would
// regenerate the old implementation instead of the requested target. Full-file
// overwrites are the only correct execution mode for these signals.
func ExecutionModeForPolicy(rewritePolicy bool, preserveWorkspace *bool) Mode {
	if rewritePolicy {
		return ModeGreenfield
	}
	if preserveWorkspace != nil && !*preserveWorkspace {
		return ModeGreenfield
	}
	return ModeBrownfield
}
