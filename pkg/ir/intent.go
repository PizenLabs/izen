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

// Well-known QuestionOption identifiers understood by the planner. The
// workspace-conflict options carry a semantic meaning the pipeline maps onto
// IntentIR.PreserveWorkspace; OptionTypeYourOwn lets the user supply a
// free-form answer instead of picking a fixed branch.
const (
	// OptionReplaceWorkspace discards the existing workspace content and
	// builds the requested target from scratch.
	OptionReplaceWorkspace = "replace_workspace"
	// OptionBuildAlongside keeps the existing workspace and adds the
	// requested target next to it.
	OptionBuildAlongside = "build_alongside"
	// OptionMergeSelective keeps the relevant parts of both workspaces.
	OptionMergeSelective = "merge_selective"
	// OptionTypeYourOwn switches the UI into free-form text entry mode.
	OptionTypeYourOwn = "type_your_own"
)

// QuestionOption is one mutually-exclusive execution branch the user can pick
// from. Label is the short choice headline; Description is the one-line
// consequence card rendered underneath it by the UI.
type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default"`
}

// NewCustomAnswerOption builds the free-form "type your own answer" branch
// that the interactive UI treats specially: confirming it opens a text input
// instead of resolving the question.
func NewCustomAnswerOption() QuestionOption {
	return QuestionOption{
		ID:          OptionTypeYourOwn,
		Label:       "Type your own answer",
		Description: "Enter a custom instruction instead of picking a preset branch",
	}
}

// IsCustomAnswerOption reports whether opt is the free-form text-entry branch.
func IsCustomAnswerOption(opt QuestionOption) bool {
	return opt.ID == OptionTypeYourOwn
}

// ClarificationQuestion captures a high-impact ambiguity the compiler
// surfaced so the UI can ask the user before planning proceeds. A
// ClarificationQuestion never describes a preference; it always names an
// execution branch whose outcome changes the plan materially.
type ClarificationQuestion struct {
	// ID uniquely identifies the question within an IntentIR.
	ID string `json:"id"`
	// Header is the short UI badge shown above the prompt (e.g. "Workspace
	// Conflict Detected").
	Header string `json:"header"`
	// QuestionText is the user-facing prompt.
	QuestionText string `json:"question_text"`
	// Options are the mutually-exclusive execution branches the user can
	// pick between. Empty when free-form input is the only sensible answer.
	Options []QuestionOption `json:"options"`
	// SelectedOption is the option ID chosen by the user. Populated by the
	// pipeline after the clarification UI resolves the question.
	SelectedOption string `json:"selected_option"`
	// CustomAnswer carries the free-form answer when SelectedOption is
	// OptionTypeYourOwn.
	CustomAnswer string `json:"custom_answer,omitempty"`
	// Reason is the machine-readable trigger that raised the question.
	Reason string `json:"reason,omitempty"`
}

// DefaultOptionID returns the ID of the option the UI should pre-highlight:
// the first IsDefault option, or the first option when none is marked.
func (q ClarificationQuestion) DefaultOptionID() string {
	for _, o := range q.Options {
		if o.IsDefault {
			return o.ID
		}
	}
	if len(q.Options) > 0 {
		return q.Options[0].ID
	}
	return ""
}

// IsAnswered reports whether the question already carries a user selection.
func (q ClarificationQuestion) IsAnswered() bool {
	return q.SelectedOption != "" || q.CustomAnswer != ""
}

// ClarificationAnswer is one user's reply to one ClarificationQuestion. It
// either names a preset option or, for OptionTypeYourOwn, carries the typed
// CustomAnswer.
type ClarificationAnswer struct {
	QuestionID   string `json:"question_id"`
	OptionID     string `json:"option_id"`
	CustomAnswer string `json:"custom_answer,omitempty"`
}

// ClarificationResponse is the full set of answers the clarification UI
// returns to the pipeline for one ambiguous intent. It unblocks the pipeline's
// synchronous response channel.
type ClarificationResponse struct {
	Answers []ClarificationAnswer `json:"answers"`
}

// DefaultAnswers resolves every question to its default option. It is the
// non-interactive fallback the pipeline uses when no TUI clarifier is wired,
// so a headless run can never hang on an unanswered question.
func DefaultAnswers(questions []ClarificationQuestion) []ClarificationAnswer {
	answers := make([]ClarificationAnswer, 0, len(questions))
	for _, q := range questions {
		answers = append(answers, ClarificationAnswer{
			QuestionID: q.ID,
			OptionID:   q.DefaultOptionID(),
		})
	}
	return answers
}

// ApplyAnswers copies questions and folds every answer back onto the matching
// question by ID, setting SelectedOption and CustomAnswer. Unanswered
// questions are preserved unchanged.
func ApplyAnswers(questions []ClarificationQuestion, answers []ClarificationAnswer) []ClarificationQuestion {
	out := make([]ClarificationQuestion, len(questions))
	copy(out, questions)
	for _, a := range answers {
		for i := range out {
			if out[i].ID != a.QuestionID {
				continue
			}
			out[i].SelectedOption = a.OptionID
			out[i].CustomAnswer = a.CustomAnswer
		}
	}
	return out
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
