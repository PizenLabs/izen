// Package ir defines the canonical Intermediate Representations of the Izen
// Agent Runtime V3. ir.Artifact is the evidence-based file representation
// emitted by extractors; ir.IntentIR is the strongly-typed translation of a
// natural language prompt produced by the Intent Compiler.
//
// Like the rest of the package, intent.go is deliberately dependency-free:
// it carries no AI/LLM concepts and performs no I/O. IntentIR is a plain
// value passed from the compiler to the planner so small/free LLMs receive a
// zero-ambiguity contract instead of raw prompt text.
package ir

import (
	"fmt"
	"strings"
)

// Category discriminates the high-level user intent compiled from a natural
// language prompt. It is the mutually-exclusive primary classification that
// drives planning.
type Category string

const (
	// CategoryCreate generates a brand-new target (greenfield) or adds a new
	// target alongside existing workspace content.
	CategoryCreate Category = "create"
	// CategoryRedesign re-plans an existing target's look, structure or
	// content while preserving its purpose (e.g. "lam lai website",
	// "redesign my portfolio").
	CategoryRedesign Category = "redesign"
	// CategoryRefactor restructures existing code without changing external
	// behaviour.
	CategoryRefactor Category = "refactor"
	// CategoryFixBug repairs a defect in existing workspace content.
	CategoryFixBug Category = "fix_bug"
)

// allCategories preserves declaration order for Valid.
var allCategories = []Category{CategoryCreate, CategoryRedesign, CategoryRefactor, CategoryFixBug}

// Valid reports whether c is one of the defined categories.
func (c Category) Valid() bool {
	for _, x := range allCategories {
		if c == x {
			return true
		}
	}
	return false
}

// String returns the machine-readable category label.
func (c Category) String() string { return string(c) }

// PreservesWorkspace reports whether the category keeps existing workspace
// files in place. It is false for redesign/rewrite categories, which replace
// existing content rather than extending it.
func (c Category) PreservesWorkspace() bool {
	return c != CategoryRedesign
}

// ClarificationQuestion captures a high-impact ambiguity the compiler
// surfaced so the UI can ask the user before planning proceeds. A
// ClarificationQuestion never describes a preference; it always names an
// execution branch whose outcome changes the plan materially.
type ClarificationQuestion struct {
	// Question is the user-facing prompt.
	Question string
	// Options are the mutually-exclusive execution branches the user can
	// pick between. Empty when free-form input is the only sensible answer.
	Options []string
	// Reason is the machine-readable trigger that raised the question.
	Reason string
}

// IntentIR is the strongly-typed, zero-ambiguity translation of one natural
// language prompt. It decouples natural language interpretation from
// planning: the planner consumes only this structure, never raw prompt text,
// which prevents small/free LLMs from anchoring on obsolete examples such as
// a "To-Do App" template.
type IntentIR struct {
	// Category is the primary classification of the request.
	Category Category
	// TargetType names the concrete target (e.g. "portfolio", "rest_api",
	// "todo_app").
	TargetType string
	// Entities carries extracted metadata keyed by role (e.g. "author" ->
	// "Alex Josie").
	Entities map[string]string
	// Technologies is the ordered, de-duplicated stack the target is built
	// on (e.g. ["html", "css", "js"]).
	Technologies []string
	// PreserveWorkspace is false when the category rewrites existing
	// workspace content (redesign) rather than adding to it.
	PreserveWorkspace bool
	// DecisionAmbiguity is true when multiple valid high-impact execution
	// branches exist (e.g. a portfolio requested over an existing To-Do App
	// workspace).
	DecisionAmbiguity bool
	// ClarificationQuestions holds the questions the UI should ask before
	// planning when DecisionAmbiguity is true.
	ClarificationQuestions []ClarificationQuestion
}

// Validate reports whether the intent is well-formed: the category must be a
// defined constant and, when present, the target type must be non-empty.
func (i IntentIR) Validate() error {
	if !i.Category.Valid() {
		return fmt.Errorf("ir: invalid category %q", i.Category)
	}
	if i.TargetType == "" {
		return fmt.Errorf("ir: empty target type")
	}
	return nil
}

// String renders a compact, stable, human-readable label of the intent.
func (i IntentIR) String() string {
	var b strings.Builder
	b.WriteString(string(i.Category))
	b.WriteString(":")
	b.WriteString(i.TargetType)
	if len(i.Technologies) > 0 {
		b.WriteString(" [")
		b.WriteString(strings.Join(i.Technologies, ","))
		b.WriteString("]")
	}
	if len(i.Entities) > 0 {
		keys := make([]string, 0, len(i.Entities))
		for k := range i.Entities {
			keys = append(keys, k)
		}
		// Stable output for a deterministic String().
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
				keys[j], keys[j-1] = keys[j-1], keys[j]
			}
		}
		for _, k := range keys {
			b.WriteString(" ")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(i.Entities[k])
		}
	}
	if i.DecisionAmbiguity {
		b.WriteString(" !ambig")
	}
	return b.String()
}
