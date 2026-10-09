package proposal

import (
	"fmt"
	"strings"
)

// Interaction is the strategy IZEN selects for a given objective. The mission's
// whole point is that this is a CHOICE, not a fixed ceremony: a task with no
// material choices must not be asked anything.
type Interaction string

const (
	// InteractionDirect executes without a proposal. It is correct for a clear,
	// sufficiently bounded objective whose target and approach are determined by
	// the objective and reliable evidence.
	InteractionDirect Interaction = "direct"
	// InteractionInvestigate inspects the workspace first and then presents a
	// recommendation, alternatives and only the questions that materially affect
	// execution.
	InteractionInvestigate Interaction = "investigate"
	// InteractionClarify must ask before executing because the evidence does not
	// determine the target or the implementation.
	InteractionClarify Interaction = "clarify"
)

// Valid reports whether the interaction is a member of the closed vocabulary.
func (i Interaction) Valid() bool {
	switch i {
	case InteractionDirect, InteractionInvestigate, InteractionClarify:
		return true
	default:
		return false
	}
}

// String returns the canonical interaction label.
func (i Interaction) String() string { return string(i) }

// Signal is the deterministic input to interaction selection. Every field is a
// fact the runtime already owns; none of them is a model opinion.
type Signal struct {
	// Objective is the human objective, verbatim.
	Objective string
	// Mutating reports whether the objective requires a workspace change.
	Mutating bool
	// HasExplicitTarget reports that the objective states (or the runtime
	// already bound) a target set.
	HasExplicitTarget bool
	// DerivedTargets is the unique evidence-bound target set, when one was
	// derived.
	DerivedTargets []string
	// Ambiguity reports that several observed files satisfy the objective and
	// none is proven to be the intended one.
	Ambiguity bool
	// Unresolved reports that nothing could be derived at all.
	Unresolved bool
	// ProjectLevel reports the gateway classified the objective as a broad
	// project-level transformation.
	ProjectLevel bool
	// Consequential counts the decisions whose different answers lead to
	// materially different implementations. It is zero for a task with no such
	// choice.
	Consequential int
	// Risk is the classified risk of the objective.
	Risk RiskSeverity
	// AutonomyPermitted reports that the applicable authorization policy allows
	// the runtime to execute this objective without a fresh human proposal.
	AutonomyPermitted bool
}

// Selection is the deterministic interaction verdict with its justification.
type Selection struct {
	Interaction Interaction
	// RequireProposal reports that a proposal must be formed before execution,
	// even when the interaction is Direct (investigate always forms one).
	RequireProposal bool
	// RequireAnswer reports that at least one question must be answered before
	// execution may be admitted.
	RequireAnswer bool
	// Reasons are the deterministic, ordered justifications.
	Reasons []string
}

// SelectInteraction chooses the interaction for an objective. It is a pure
// function: identical signals always yield an identical selection.
//
// Precedence (each case is a fact, not a preference):
//
//  1. an irreducible ambiguity or an unresolved scope    → CLARIFY
//     (only a human can determine the intended target)
//  2. material consequential choices                     → INVESTIGATE
//     (the human must choose, but the runtime investigates first and asks only
//     about the choices that matter)
//  3. a broad project-level objective                    → INVESTIGATE
//     (discover and recommend; do not ask the user to name a filename)
//  4. a clear, bounded objective                         → DIRECT
//     (no planning ceremony for a straightforward task)
//
// A read-only objective that is not ambiguous is always DIRECT: there is
// nothing consequential about answering a question.
func SelectInteraction(s Signal) Selection {
	switch {
	case !s.Mutating && !s.Ambiguity && !s.Unresolved:
		return Selection{
			Interaction: InteractionDirect,
			Reasons:     []string{"the objective is read-only and determinate: no execution choice to make"},
		}
	case s.Ambiguity:
		return Selection{
			Interaction:     InteractionClarify,
			RequireProposal: true,
			RequireAnswer:   true,
			Reasons: []string{
				"workspace evidence offers several candidates and none is proven to be the intended target",
				"only a human decision can determine the target",
			},
		}
	case s.Unresolved && !s.ProjectLevel:
		return Selection{
			Interaction:     InteractionClarify,
			RequireProposal: true,
			RequireAnswer:   true,
			Reasons: []string{
				"the objective's target is unresolved and no observed evidence determines it",
				"a human must state the intended target",
			},
		}
	case s.Consequential > 0:
		return Selection{
			Interaction:     InteractionInvestigate,
			RequireProposal: true,
			RequireAnswer:   true,
			Reasons: []string{
				fmt.Sprintf("%d consequential decision(s) lead to materially different implementations", s.Consequential),
				"the runtime investigates the workspace and asks only about those decisions",
			},
		}
	case s.ProjectLevel:
		return Selection{
			Interaction:     InteractionInvestigate,
			RequireProposal: true,
			Reasons: []string{
				"the objective is a broad project-level transformation",
				"the runtime derives a multi-file scope from evidence rather than asking for a filename",
			},
		}
	case s.HasExplicitTarget && s.AutonomyPermitted:
		return Selection{
			Interaction: InteractionDirect,
			Reasons:     []string{"a target is stated and the authorization policy permits autonomous execution"},
		}
	default:
		return Selection{
			Interaction:     InteractionDirect,
			RequireProposal: false,
			Reasons:         []string{"the objective is sufficiently clear and bounded"},
		}
	}
}

// ConsequentialQuestions counts the questions whose answer changes the
// implementation. A question with no `Affects` and no alternatives is a
// formality and is not counted. This is the contract's own definition of
// "material", so interaction selection and question authoring cannot drift.
func ConsequentialQuestions(qs []Question) int {
	n := 0
	for _, q := range qs {
		if !q.Required {
			continue
		}
		if len(q.Options) > 1 || q.AllowFreeForm || len(q.Affects) > 0 || len(q.DependsOn) > 0 {
			n++
		}
	}
	return n
}

// Unanswered returns the required questions that have no recorded human
// decision, in declaration order.
func Unanswered(c *Contract) []Question {
	if c == nil {
		return nil
	}
	var out []Question
	for _, q := range c.Questions {
		if !q.Required {
			continue
		}
		if d := c.DecisionFor(q.ID); questionAnswered(q, d) {
			continue
		}
		out = append(out, q)
	}
	return out
}

// Explain renders a selection compactly for logs and tests.
func (s Selection) Explain() string {
	if len(s.Reasons) == 0 {
		return string(s.Interaction)
	}
	return string(s.Interaction) + ": " + strings.Join(s.Reasons, "; ")
}
