// Package intent defines the Lean User Intent Model: a pure, zero-dependency
// domain model capturing ONLY what the user wants to achieve and under what
// constraints.
//
// The package answers exactly one question: "What does the user want to
// achieve and under what constraints?" It carries no execution plans, patches,
// task lists, risk scores, or directives — those belong to the execution
// timeline, not to the user's intent. It represents User Wants, never System
// Executes.
//
// Dependency rule: the package imports only the Go standard library. It never
// imports modes, pipeline, runtime, or UI packages.
package intent

import (
	"fmt"
	"strings"
	"time"
)

// Goal captures the pure human desire: the raw prompt exactly as given, the
// normalized primary intent, and (optionally) a facet refining it.
type Goal struct {
	RawPrompt string `json:"raw_prompt"`
	Primary   string `json:"primary"`
	Facet     string `json:"facet,omitempty"`
}

// Constraints captures the boundaries the user placed around the goal. They
// describe limits on how the goal may be pursued, never the plan itself.
type Constraints struct {
	ForbiddenPaths []string `json:"forbidden_paths,omitempty"`
	MaxDepth       int      `json:"max_depth,omitempty"`
	ReadonlyOnly   bool     `json:"readonly_only"`
}

// EvidenceRef references external evidence the user pointed at (a file, a
// thread, a URL) that grounds the goal. It is a reference, not embedded
// content.
type EvidenceRef struct {
	Source string `json:"source"`
	URI    string `json:"uri"`
}

// UserIntent is the root aggregate of the lean intent model. It represents the
// user's wants and context only: the goal, the constraints around it, and the
// evidence that grounds it.
type UserIntent struct {
	ID          string        `json:"id"`
	Goal        Goal          `json:"goal"`
	Constraints Constraints   `json:"constraints"`
	Evidence    []EvidenceRef `json:"evidence,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
}

// New creates a UserIntent from a raw user prompt. The raw prompt is trimmed
// and mirrored into both the raw and primary goal fields; callers may refine
// Goal.Primary/Facet via the fluent builders or direct field access.
func New(rawPrompt string) *UserIntent {
	trimmed := strings.TrimSpace(rawPrompt)
	return &UserIntent{
		ID:        fmt.Sprintf("intent-%d", time.Now().UnixNano()),
		Goal:      Goal{RawPrompt: trimmed, Primary: trimmed},
		CreatedAt: time.Now().UTC(),
	}
}

// WithConstraints returns a copy of the intent with the given constraints
// applied. It is a pure builder: it never mutates the receiver.
func (i *UserIntent) WithConstraints(c Constraints) *UserIntent {
	if i == nil {
		return nil
	}
	cp := *i
	cp.Constraints = c
	cp.Evidence = append([]EvidenceRef(nil), i.Evidence...)
	return &cp
}

// WithEvidence appends an evidence reference and returns a copy. It is a pure
// builder: it never mutates the receiver.
func (i *UserIntent) WithEvidence(ev EvidenceRef) *UserIntent {
	if i == nil {
		return nil
	}
	cp := *i
	cp.Evidence = append(append([]EvidenceRef(nil), i.Evidence...), ev)
	return &cp
}
