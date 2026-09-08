package authorization

import (
	"errors"
	"testing"
	"time"
)

// newArmedSession creates a gate with one armed session at the given epoch,
// returning the gate and session. delayWindow is applied to the session so
// tests can deterministically control the PTY buffer-bleeding mitigation.
func newArmedSession(t *testing.T, delayWindow time.Duration) (*ApprovalGate, *ApprovalSession) {
	t.Helper()
	gate := NewGate()
	s := gate.NewSession("proposal-1", ActionExecute)
	s.MinDelayWindow = delayWindow
	if err := gate.ArmSession(s.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}
	return gate, s
}

// eval wraps Evaluate and fails the test on an unexpected error.
func eval(t *testing.T, gate *ApprovalGate, evt ApprovalEvent) (ApprovalAction, error) {
	t.Helper()
	return gate.Evaluate(evt)
}

// assertErr verifies err is (or wraps) want.
func assertErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("expected error %v, got %v", want, err)
	}
}

func TestStaleEpochRejection(t *testing.T) {
	gate := NewGate()
	first := gate.NewSession("proposal-1", ActionExecute)
	second := gate.NewSession("proposal-2", ActionExecute)
	if second.Epoch <= first.Epoch {
		t.Fatalf("expected epoch to strictly increase, got %d then %d", first.Epoch, second.Epoch)
	}
	if err := gate.ArmSession(second.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}

	tests := []struct {
		name  string
		event ApprovalEvent
	}{
		{"event from superseded session", ApprovalEvent{Epoch: first.Epoch, Action: ActionExecute}},
		{"event from unarmed epoch zero", ApprovalEvent{Epoch: 0, Action: ActionExecute}},
		{"event from future epoch", ApprovalEvent{Epoch: second.Epoch + 1, Action: ActionExecute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := eval(t, gate, tt.event)
			assertErr(t, err, ErrStaleEpoch)
			if action != ActionNone {
				t.Fatalf("expected ActionNone on stale epoch, got %v", action)
			}
			if second.State != StateArmed {
				t.Fatalf("session must remain armed, got %v", second.State)
			}
		})
	}
}

func TestUnarmedGateRejection(t *testing.T) {
	gate := NewGate()
	s := gate.NewSession("proposal-1", ActionExecute)

	tests := []struct {
		name  string
		event ApprovalEvent
	}{
		{"execute on unarmed session", ApprovalEvent{Epoch: s.Epoch, Action: ActionExecute}},
		{"inspect on unarmed session", ApprovalEvent{Epoch: s.Epoch, Action: ActionInspect}},
		{"cancel on unarmed session", ApprovalEvent{Epoch: s.Epoch, Action: ActionCancel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := eval(t, gate, tt.event)
			assertErr(t, err, ErrSessionUnarmed)
			if action != ActionNone {
				t.Fatalf("expected ActionNone on unarmed session, got %v", action)
			}
			if s.State != StateUnarmed {
				t.Fatalf("session must remain unarmed, got %v", s.State)
			}
		})
	}
}

func TestEvaluateWithoutSession(t *testing.T) {
	gate := NewGate()
	action, err := gate.Evaluate(ApprovalEvent{Epoch: 1, Action: ActionExecute})
	assertErr(t, err, ErrSessionUnarmed)
	if action != ActionNone {
		t.Fatalf("expected ActionNone without a session, got %v", action)
	}
}

func TestArmSessionWithoutSession(t *testing.T) {
	gate := NewGate()
	assertErr(t, gate.ArmSession(1), ErrSessionUnarmed)
}

func TestArmSessionStaleEpoch(t *testing.T) {
	gate := NewGate()
	gate.NewSession("proposal-1", ActionExecute)
	assertErr(t, gate.ArmSession(99), ErrStaleEpoch)
}

func TestPTYBufferBleedingMitigation(t *testing.T) {
	gate := NewGate()
	s := gate.NewSession("proposal-1", ActionExecute)
	if err := gate.ArmSession(s.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}

	// An ActionExecute within the (default 150ms) window is ignored as
	// buffer noise: no authorization, session stays armed.
	action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionExecute})
	if err != nil {
		t.Fatalf("buffer noise must be ignored, not rejected: %v", err)
	}
	if action != ActionNone {
		t.Fatalf("expected ActionNone for buffer noise, got %v", action)
	}
	if s.State != StateArmed {
		t.Fatalf("session must remain armed after ignored noise, got %v", s.State)
	}

	// The session remains fully usable: zeroing the window and re-evaluating
	// the same event authorizes it.
	s.MinDelayWindow = 0
	action, err = eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionExecute})
	if err != nil {
		t.Fatalf("Evaluate after window bypass: %v", err)
	}
	if action != ActionExecute {
		t.Fatalf("expected ActionExecute, got %v", action)
	}
	if s.State != StateAuthorized {
		t.Fatalf("expected StateAuthorized, got %v", s.State)
	}
}

func TestPTYWindowAllowsNonExecuteActions(t *testing.T) {
	gate := NewGate()
	s := gate.NewSession("proposal-1", ActionExecute)
	if err := gate.ArmSession(s.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}

	// Only ActionExecute is subject to the buffer-bleeding mitigation;
	// inspect and cancel resolve immediately.
	action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionInspect})
	if err != nil {
		t.Fatalf("Evaluate inspect during window: %v", err)
	}
	if action != ActionInspect {
		t.Fatalf("expected ActionInspect, got %v", action)
	}
	if s.State != StateAuthorized {
		t.Fatalf("expected StateAuthorized, got %v", s.State)
	}
}

func TestPTYWindowExpires(t *testing.T) {
	gate, s := newArmedSession(t, 5*time.Millisecond)

	deadline := s.ActivatedAt.Add(s.MinDelayWindow)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionExecute})
	if err != nil {
		t.Fatalf("Evaluate after window expiry: %v", err)
	}
	if action != ActionExecute {
		t.Fatalf("expected ActionExecute after window expiry, got %v", action)
	}
	if s.State != StateAuthorized {
		t.Fatalf("expected StateAuthorized, got %v", s.State)
	}
}

func TestSuccessfulAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		action ApprovalAction
	}{
		{"execute", ActionExecute},
		{"inspect", ActionInspect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate, s := newArmedSession(t, 0)
			action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: tt.action})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if action != tt.action {
				t.Fatalf("expected %v, got %v", tt.action, action)
			}
			if s.State != StateAuthorized {
				t.Fatalf("expected StateAuthorized, got %v", s.State)
			}
			if s.ActivatedAt.IsZero() {
				t.Fatal("expected ActivatedAt to be set after arming")
			}
		})
	}
}

func TestCancelRejection(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionCancel})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if action != ActionCancel {
		t.Fatalf("expected ActionCancel, got %v", action)
	}
	if s.State != StateRejected {
		t.Fatalf("expected StateRejected, got %v", s.State)
	}
}

func TestActionNoneIsNoOp(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	action, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionNone})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if action != ActionNone {
		t.Fatalf("expected ActionNone, got %v", action)
	}
	if s.State != StateArmed {
		t.Fatalf("session must remain armed after ActionNone, got %v", s.State)
	}
}

func TestEvaluateAfterFinalization(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	if _, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionExecute}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if s.State != StateAuthorized {
		t.Fatalf("expected StateAuthorized, got %v", s.State)
	}

	for _, action := range []ApprovalAction{ActionExecute, ActionInspect, ActionCancel} {
		t.Run("action_"+action.name(), func(t *testing.T) {
			got, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: action})
			assertErr(t, err, ErrSessionFinalized)
			if got != ActionNone {
				t.Fatalf("expected ActionNone on finalized session, got %v", got)
			}
		})
	}
}

func TestArmSessionAfterFinalization(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	if _, err := eval(t, gate, ApprovalEvent{Epoch: s.Epoch, Action: ActionCancel}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if s.State != StateRejected {
		t.Fatalf("expected StateRejected, got %v", s.State)
	}
	assertErr(t, gate.ArmSession(s.Epoch), ErrSessionFinalized)
}

func TestArmSessionIdempotent(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	firstActivated := s.ActivatedAt
	if err := gate.ArmSession(s.Epoch); err != nil {
		t.Fatalf("re-arm of armed session must succeed, got %v", err)
	}
	if s.State != StateArmed {
		t.Fatalf("expected StateArmed, got %v", s.State)
	}
	if !s.ActivatedAt.Equal(firstActivated) {
		t.Fatal("idempotent re-arm must not refresh ActivatedAt")
	}
}

func TestNewSessionInvalidatesPriorEvents(t *testing.T) {
	gate := NewGate()
	first := gate.NewSession("proposal-1", ActionExecute)
	if err := gate.ArmSession(first.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}
	if _, err := eval(t, gate, ApprovalEvent{Epoch: first.Epoch, Action: ActionExecute}); err != nil {
		t.Fatalf("first session should authorize: %v", err)
	}

	// A newer session supersedes the finalized one.
	second := gate.NewSession("proposal-2", ActionExecute)
	if err := gate.ArmSession(second.Epoch); err != nil {
		t.Fatalf("ArmSession: %v", err)
	}
	if got := gate.CurrentSession(); got != second {
		t.Fatalf("expected current session to be the newest, got %+v", got)
	}
	// Events against the old session epoch are now stale.
	_, err := gate.Evaluate(ApprovalEvent{Epoch: first.Epoch, Action: ActionExecute})
	assertErr(t, err, ErrStaleEpoch)
}

func TestWithMinDelayWindowOption(t *testing.T) {
	gate := NewGate(WithMinDelayWindow(2 * time.Second))
	s := gate.NewSession("proposal-1", ActionExecute)
	if s.MinDelayWindow != 2*time.Second {
		t.Fatalf("expected 2s window, got %v", s.MinDelayWindow)
	}

	// Negative durations are ignored and leave the default in place.
	gate2 := NewGate(WithMinDelayWindow(-time.Second))
	if gate2.defaultDelayWindow != DefaultMinDelayWindow {
		t.Fatalf("expected default window %v, got %v", DefaultMinDelayWindow, gate2.defaultDelayWindow)
	}
}

func TestDefaultMinDelayWindowApplied(t *testing.T) {
	gate := NewGate()
	s := gate.NewSession("proposal-1", ActionExecute)
	if s.MinDelayWindow != DefaultMinDelayWindow {
		t.Fatalf("expected default window %v, got %v", DefaultMinDelayWindow, s.MinDelayWindow)
	}
}

func TestCurrentSessionNil(t *testing.T) {
	gate := NewGate()
	if got := gate.CurrentSession(); got != nil {
		t.Fatalf("expected nil session, got %+v", got)
	}
}

func TestProposalIDPreserved(t *testing.T) {
	gate := NewGate()
	s := gate.NewSession("proposal-xyz", ActionInspect)
	if s.ProposalID != "proposal-xyz" {
		t.Fatalf("expected proposal id %q, got %q", "proposal-xyz", s.ProposalID)
	}
	if s.DefaultAction != ActionInspect {
		t.Fatalf("expected DefaultAction %v, got %v", ActionInspect, s.DefaultAction)
	}
	if s.State != StateUnarmed {
		t.Fatalf("new session must start unarmed, got %v", s.State)
	}
}

func TestEvaluateConcurrentSafety(t *testing.T) {
	gate, s := newArmedSession(t, 0)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_, _ = gate.Evaluate(ApprovalEvent{Epoch: s.Epoch, Action: ActionInspect})
			}
		}()
	}
	for range 8 {
		<-done
	}
}

// name returns a stable label used to build subtest names for actions.
func (a ApprovalAction) name() string {
	switch a {
	case ActionExecute:
		return "execute"
	case ActionInspect:
		return "inspect"
	case ActionCancel:
		return "cancel"
	default:
		return "none"
	}
}
