package compiler

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/ir"
)

// AmbiguityDetector decides whether a resolved intent carries multiple valid
// high-impact execution branches. It is the final policy stage of the
// compiler: given the resolution and the workspace conflict, it sets
// DecisionAmbiguity and produces the ClarificationQuestions the UI should
// surface before planning.
type AmbiguityDetector struct{}

// NewAmbiguityDetector builds an AmbiguityDetector with the default policy.
func NewAmbiguityDetector() *AmbiguityDetector {
	return &AmbiguityDetector{}
}

// Process reports whether the intent is ambiguous. A conflict between the
// requested target and the existing workspace always makes the intent
// ambiguous, because the plan branch (replace vs. build-alongside vs.
// merge) materially changes the outcome.
func (a *AmbiguityDetector) Process(_ *Resolution, _ WorkspaceState, conflict Conflict) bool {
	return conflict.Present
}

// Questions returns the ClarificationQuestions to ask when the intent is
// ambiguous. It returns nil when no conflict exists. Every option is a
// structured ir.QuestionOption carrying a semantic ID the pipeline maps onto
// IntentIR.PreserveWorkspace, plus a free-form "type your own answer" branch.
func (a *AmbiguityDetector) Questions(_ *Resolution, _ WorkspaceState, conflict Conflict) []ir.ClarificationQuestion {
	if !conflict.Present {
		return nil
	}
	detected := strings.Join(conflict.Detected, ", ")
	if detected == "" {
		detected = "unknown"
	}
	requested := conflict.Requested
	if requested == "" {
		requested = "the requested target"
	}
	return []ir.ClarificationQuestion{{
		ID:     "workspace-conflict",
		Header: "Workspace Conflict Detected",
		QuestionText: fmt.Sprintf(
			"Your request targets a %s, but this workspace is currently a %s workspace. How should I proceed?",
			conflict.Requested, detected,
		),
		Options: []ir.QuestionOption{
			{
				ID:          ir.OptionReplaceWorkspace,
				Label:       fmt.Sprintf("Completely replace workspace with %s", requested),
				Description: fmt.Sprintf("Discards the existing %s files and builds a clean %s", detected, requested),
			},
			{
				ID:          ir.OptionBuildAlongside,
				Label:       fmt.Sprintf("Build %s alongside the existing workspace", requested),
				Description: fmt.Sprintf("Keeps the current %s files and adds the new %s next to them", detected, requested),
			},
			{
				ID:          ir.OptionMergeSelective,
				Label:       "Merge selectively and keep both",
				Description: fmt.Sprintf("Keeps the relevant %s parts and folds the %s in where it fits", detected, requested),
				IsDefault:   true,
			},
			ir.NewCustomAnswerOption(),
		},
		Reason: conflict.Reason,
	}}
}
