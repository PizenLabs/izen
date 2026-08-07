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
// ambiguous. It returns nil when no conflict exists.
func (a *AmbiguityDetector) Questions(_ *Resolution, _ WorkspaceState, conflict Conflict) []ir.ClarificationQuestion {
	if !conflict.Present {
		return nil
	}
	detected := strings.Join(conflict.Detected, ", ")
	if detected == "" {
		detected = "unknown"
	}
	return []ir.ClarificationQuestion{{
		Question: fmt.Sprintf(
			"Your request targets a %s, but this workspace is currently a %s workspace. How should I proceed?",
			conflict.Requested, detected,
		),
		Options: []string{
			fmt.Sprintf("Replace the existing %s workspace entirely", detected),
			fmt.Sprintf("Build the %s alongside the existing workspace", conflict.Requested),
			"Merge selectively and keep both",
		},
		Reason: conflict.Reason,
	}}
}
