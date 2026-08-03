package policy

import "github.com/PizenLabs/izen/internal/domain/workflow"

// TransitionPolicy governs workflow phase transitions. It is the policy-layer
// view of the rules the workflow runtime enforces, so application wiring can
// supply a stricter or more lenient policy without touching domain state.
type TransitionPolicy interface {
	// AllowTransition reports whether moving from one phase to another is
	// permitted. A nil return means the transition is allowed.
	AllowTransition(from, to workflow.Phase) error
}

// DefaultTransitionPolicy adopts the system transition rules as defined by
// workflow.DefaultTransitionRule.
type DefaultTransitionPolicy struct{}

// AllowTransition delegates to the system transition rule.
func (DefaultTransitionPolicy) AllowTransition(from, to workflow.Phase) error {
	return workflow.DefaultTransitionRule(from, to)
}

// Rule adapts the policy to the workflow runtime's TransitionRule signature,
// allowing the policy to be injected as a custom rule at construction time.
func (p DefaultTransitionPolicy) Rule() workflow.TransitionRule {
	return p.AllowTransition
}
