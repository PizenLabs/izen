package artifact

import "fmt"

// LifecycleState represents the current state of an artifact in its lifecycle.
type LifecycleState string

const (
	StateDraft            LifecycleState = "DRAFT"
	StateValidated        LifecycleState = "VALIDATED"
	StateAwaitingApproval LifecycleState = "AWAITING_APPROVAL"
	StateAuthorized       LifecycleState = "AUTHORIZED"
	StateConsumed         LifecycleState = "CONSUMED"
	StateArchived         LifecycleState = "ARCHIVED"
	StateStale            LifecycleState = "STALE"
	StateInvalidated      LifecycleState = "INVALIDATED"
	StateRejected         LifecycleState = "REJECTED"
)

// Valid reports whether the state is one of the defined lifecycle states.
func (s LifecycleState) Valid() bool {
	switch s {
	case StateDraft, StateValidated, StateAwaitingApproval,
		StateAuthorized, StateConsumed, StateArchived,
		StateStale, StateInvalidated, StateRejected:
		return true
	}
	return false
}

// IsTerminal reports whether the state is a terminal (absorbing) state.
func (s LifecycleState) IsTerminal() bool {
	switch s {
	case StateStale, StateInvalidated, StateRejected:
		return true
	}
	return false
}

// ─── LifecycleTransitionValidator ────────────────────────────────────────────

// LifecycleTransitionValidator enforces valid state transitions for artifacts.
// Direct arbitrary lifecycle assignments are forbidden; all state changes must
// go through this validator.
type LifecycleTransitionValidator struct {
	allowlist map[LifecycleState][]LifecycleState
}

// NewLifecycleTransitionValidator returns a validator with the strict transition
// rules defined by the architecture specification.
func NewLifecycleTransitionValidator() *LifecycleTransitionValidator {
	return &LifecycleTransitionValidator{
		allowlist: map[LifecycleState][]LifecycleState{
			StateDraft:            {StateValidated, StateRejected},
			StateValidated:        {StateAwaitingApproval, StateAuthorized, StateInvalidated},
			StateAwaitingApproval: {StateAuthorized, StateRejected},
			StateAuthorized:       {StateConsumed, StateStale, StateInvalidated},
			StateConsumed:         {StateArchived},
			StateStale:            {StateValidated, StateInvalidated},
		},
	}
}

// IsValidTransition checks whether moving from `from` to `to` is allowed.
// Any state can transition to INVALIDATED or STALE. States without an explicit
// allowlist entry are terminal and cannot transition further (except to
// INVALIDATED/STALE).
func (v *LifecycleTransitionValidator) IsValidTransition(from, to LifecycleState) bool {
	if from == to {
		return false
	}
	if !from.Valid() || !to.Valid() {
		return false
	}
	// Any state can transition to INVALIDATED or STALE.
	if to == StateInvalidated || to == StateStale {
		return true
	}
	allowed, ok := v.allowlist[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

// MustTransition panics if the transition is invalid.
func (v *LifecycleTransitionValidator) MustTransition(from, to LifecycleState) {
	if !v.IsValidTransition(from, to) {
		panic(fmt.Sprintf("artifact: invalid lifecycle transition %s -> %s", from, to))
	}
}
