package authorization

import (
	"sync"
	"time"
)

// DefaultMinDelayWindow is the default PTY buffer-bleeding mitigation window
// applied to sessions created by a gate that was not configured otherwise.
const DefaultMinDelayWindow = 150 * time.Millisecond

// Option configures an ApprovalGate at construction time.
type Option func(*ApprovalGate)

// WithMinDelayWindow overrides the delay window applied to newly created
// sessions. Negative durations are ignored and leave the default in place.
func WithMinDelayWindow(d time.Duration) Option {
	return func(g *ApprovalGate) {
		if d >= 0 {
			g.defaultDelayWindow = d
		}
	}
}

// ApprovalGate is the approval state machine for the runtime control plane.
//
// The gate owns a strictly monotonically increasing InteractionEpoch counter.
// NewSession advances the counter and makes the new session authoritative;
// every prior epoch is thereby invalidated (events carrying a stale epoch are
// rejected with ErrStaleEpoch). Arming the session records its activation
// time, and Evaluate is the only path that transitions a session to
// StateAuthorized or StateRejected.
//
// The gate is safe for concurrent use; sessions returned by the gate must be
// treated as read-only outside of gate methods.
type ApprovalGate struct {
	mu                 sync.Mutex
	epoch              InteractionEpoch
	current            *ApprovalSession
	defaultDelayWindow time.Duration
}

// NewGate initializes a gate with Epoch = 0 and the default delay window.
func NewGate(opts ...Option) *ApprovalGate {
	g := &ApprovalGate{defaultDelayWindow: DefaultMinDelayWindow}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// CurrentSession returns the authoritative session (the most recently created
// one), or nil if no session has been created yet. The returned session must
// be treated as read-only.
func (g *ApprovalGate) CurrentSession() *ApprovalSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// NewSession increments the internal epoch counter (N -> N+1) and returns a
// new session with Epoch = N+1 and State = StateUnarmed. The new session
// becomes authoritative, invalidating any prior events: subsequent Evaluate
// calls carrying an epoch <= N are rejected with ErrStaleEpoch.
func (g *ApprovalGate) NewSession(proposalID string, defaultAction ApprovalAction) *ApprovalSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.epoch++
	s := &ApprovalSession{
		Epoch:          g.epoch,
		ProposalID:     proposalID,
		State:          StateUnarmed,
		DefaultAction:  defaultAction,
		MinDelayWindow: g.defaultDelayWindow,
	}
	g.current = s
	return s
}

// ArmSession validates that epoch matches the current session and transitions
// it from StateUnarmed to StateArmed, recording ActivatedAt = time.Now().
//
// It returns ErrStaleEpoch when the epoch does not match the current session,
// ErrSessionUnarmed when no session has been created, and
// ErrSessionFinalized when the session has already been authorized or
// rejected. Arming an already-armed session is a no-op.
func (g *ApprovalGate) ArmSession(epoch InteractionEpoch) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == nil {
		return ErrSessionUnarmed
	}
	if epoch != g.current.Epoch {
		return ErrStaleEpoch
	}
	switch g.current.State {
	case StateAuthorized, StateRejected:
		return ErrSessionFinalized
	case StateArmed:
		return nil
	default:
		g.current.State = StateArmed
		g.current.ActivatedAt = time.Now()
		return nil
	}
}

// Evaluate resolves an ApprovalEvent against the current session. The checks
// are applied in strict order:
//
//  1. Stale epoch: evt.Epoch must equal the current session epoch, otherwise
//     ErrStaleEpoch is returned.
//  2. Finalized: a session already in StateAuthorized or StateRejected yields
//     ErrSessionFinalized.
//  3. Unarmed: a session not yet in StateArmed yields ErrSessionUnarmed.
//  4. PTY buffer bleeding mitigation: an ActionExecute evaluated before
//     MinDelayWindow has elapsed since ActivatedAt is ignored as buffer
//     noise. The session remains StateArmed and (ActionNone, nil) is
//     returned — no authorization is granted.
//
// A valid ActionExecute or ActionInspect transitions the session to
// StateAuthorized; a valid ActionCancel transitions it to StateRejected. In
// both cases the executed action is returned. ActionNone is a no-op: it
// authorizes nothing and leaves the session StateArmed.
func (g *ApprovalGate) Evaluate(evt ApprovalEvent) (ApprovalAction, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == nil {
		return ActionNone, ErrSessionUnarmed
	}
	if evt.Epoch != g.current.Epoch {
		return ActionNone, ErrStaleEpoch
	}
	switch g.current.State {
	case StateAuthorized, StateRejected:
		return ActionNone, ErrSessionFinalized
	case StateUnarmed:
		return ActionNone, ErrSessionUnarmed
	}
	if evt.Action == ActionExecute && time.Since(g.current.ActivatedAt) < g.current.MinDelayWindow {
		return ActionNone, nil
	}
	switch evt.Action {
	case ActionExecute, ActionInspect:
		g.current.State = StateAuthorized
	case ActionCancel:
		g.current.State = StateRejected
	default:
		return ActionNone, nil
	}
	return evt.Action, nil
}
