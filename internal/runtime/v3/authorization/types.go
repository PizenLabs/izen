// Package authorization implements the deterministic approval state machine
// that guards task execution boundaries in the Izen runtime control plane.
//
// Authorization safety relies on an explicit State Machine and an
// InteractionEpoch sequence counter. Temporal hysteresis (MinDelayWindow) is
// strictly a PTY buffer-bleeding UX mitigation; it must never be relied upon
// as a proof of safety. Transitioning to an approval state never implicitly
// authorizes execution from pre-existing or queued key events — every
// authorization is a fresh, explicit Evaluate against the current session.
package authorization

import (
	"errors"
	"time"
)

// Error sentinels returned by the ApprovalGate. Callers should test with
// errors.Is rather than comparing directly.
var (
	// ErrStaleEpoch is returned when an event carries an epoch that does not
	// match the current session, either because the event predates a newer
	// session or because it belongs to a session that never existed.
	ErrStaleEpoch = errors.New("authorization rejected: stale interaction epoch")
	// ErrSessionUnarmed is returned when an event (or arm request) arrives
	// before the current session has been armed.
	ErrSessionUnarmed = errors.New("authorization rejected: session is unarmed")
	// ErrSessionFinalized is returned when the current session has already
	// transitioned to StateAuthorized or StateRejected.
	ErrSessionFinalized = errors.New("authorization rejected: session already finalized")
)

// InteractionEpoch is a strictly monotonically increasing sequence counter.
// It is the authoritative ordering mechanism of the approval state machine:
// only the epoch of the current session is eligible for evaluation.
type InteractionEpoch uint64

// ApprovalState enumerates the lifecycle states of an ApprovalSession.
type ApprovalState int

const (
	// StateUnarmed is the initial state of a freshly created session. No
	// event can be evaluated against an unarmed session.
	StateUnarmed ApprovalState = iota
	// StateArmed marks a session whose activation time has been recorded and
	// which is eligible for event evaluation.
	StateArmed
	// StateAuthorized marks a session that granted execution or inspection.
	StateAuthorized
	// StateRejected marks a session that was cancelled by the user.
	StateRejected
)

// ApprovalAction enumerates the actions a user may express towards a pending
// proposal.
type ApprovalAction int

const (
	// ActionNone is the zero action: an explicit no-op that authorizes
	// nothing and finalizes nothing.
	ActionNone ApprovalAction = iota
	// ActionExecute authorizes the proposal to be executed.
	ActionExecute
	// ActionInspect authorizes inspection-only access to the proposal.
	ActionInspect
	// ActionCancel rejects the proposal.
	ActionCancel
)

// ApprovalSession is the stateful authorization context for a single
// proposal. It is created by ApprovalGate.NewSession and mutated exclusively
// through the gate; external code must treat it as read-only.
type ApprovalSession struct {
	// Epoch is the session's unique sequence number, equal to the gate's
	// counter value at creation time.
	Epoch InteractionEpoch
	// ProposalID identifies the proposal the session guards.
	ProposalID string
	// ActivatedAt records when the session was armed (time.Now() at
	// ArmSession). It is the zero time until the session is armed.
	ActivatedAt time.Time
	// State is the current lifecycle state of the session.
	State ApprovalState
	// DefaultAction is the action applied when the user takes no explicit
	// action (for example an Enter press at the approval prompt).
	DefaultAction ApprovalAction
	// MinDelayWindow is the PTY buffer-bleeding mitigation window. An
	// ActionExecute evaluated before MinDelayWindow has elapsed since
	// ActivatedAt is ignored as buffer noise. This is strictly a UX
	// mitigation and must never be treated as an authorization guarantee.
	// Defaults to DefaultMinDelayWindow.
	MinDelayWindow time.Duration
}

// ApprovalEvent is a single authorization request directed at the current
// session.
type ApprovalEvent struct {
	// Epoch is the session epoch the event targets. Any event whose epoch
	// differs from the current session is rejected as stale.
	Epoch InteractionEpoch
	// Action is the requested approval action.
	Action ApprovalAction
}
