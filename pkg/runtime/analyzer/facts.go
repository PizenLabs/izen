// Package analyzer extracts lightweight, deterministic facts about a
// workspace request: the parsed intent, source file inventory, token
// estimates, dependency fanout and the AST scopes touched by the target
// files. It is the read-only observation stage of the Izen v1 runtime: it
// never executes tooling and never mutates workspace state.
package analyzer

import "time"

// Intent classifies the user request with deterministic keyword scoring.
// No LLM and no statistical model is involved.
type Intent string

const (
	// IntentUnknown is the fallback when no intent markers are matched and
	// the input still references code.
	IntentUnknown Intent = "unknown"
	// IntentBugFix matches requests describing defects.
	IntentBugFix Intent = "bug_fix"
	// IntentRefactor matches requests describing restructuring.
	IntentRefactor Intent = "refactor"
	// IntentFeature matches requests describing new capability.
	IntentFeature Intent = "feature"
	// IntentQuestion matches requests asking for an explanation.
	IntentQuestion Intent = "question"
	// IntentChat matches conversational, non-coding prompts: greetings,
	// small talk, identity and memory questions. It never touches files, AST
	// symbols or explicit code operations.
	IntentChat Intent = "chat"
)

// knownIntents is the canonical intent set used to validate intent names.
var knownIntents = map[Intent]struct{}{
	IntentUnknown:  {},
	IntentBugFix:   {},
	IntentRefactor: {},
	IntentFeature:  {},
	IntentQuestion: {},
	IntentChat:     {},
}

// IsKnown reports whether the intent is one of the canonical values.
func (i Intent) IsKnown() bool {
	_, ok := knownIntents[i]
	return ok
}

// Scope is a single top-level AST declaration touched by an analyzed file.
type Scope struct {
	Path      string
	Kind      string
	Name      string
	LineStart int
	LineEnd   int
}

// Facts is the immutable result of one analysis pass. Every slice is sorted
// and deduplicated so a given workspace always produces identical facts.
type Facts struct {
	Root             string
	Input            string
	Intent           Intent
	IntentConfidence float64
	IntentReason     string
	TargetFiles      []string
	Files            int
	TokenEstimate    int
	DependencyFanout map[string][]string
	MaxFanout        int
	ModifiedScopes   []Scope
	GeneratedAt      time.Time
	Duration         time.Duration
}
