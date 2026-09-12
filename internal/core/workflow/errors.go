package workflow

import "errors"

// Sentinel errors for workflow state machine transition rejections.
//
// All transition rejections MUST wrap one of these sentinels so callers can
// identify the failure class with errors.Is. Raw string matching
// (strings.Contains) on error messages is strictly forbidden.
var (
	// ErrInvalidTransition is returned when a transition violates the
	// state/event table or a guard rejects it.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrBackwardTransitionDisallowed is returned when a transition attempts
	// to move to a previous phase outside the sanctioned re-plan edges.
	ErrBackwardTransitionDisallowed = errors.New("moving to a previous phase is not permitted")
	// ErrEventNotAllowed is returned when an event is not allowed in the
	// current state.
	ErrEventNotAllowed = errors.New("event not allowed in current state")
)
