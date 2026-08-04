package engine

import (
	"errors"
	"testing"
)

func TestStateMachineTransitions(t *testing.T) {
	sm := NewStateMachine()
	if sm.Current() != StateIdle {
		t.Fatalf("initial state = %s, want idle", sm.Current())
	}
	if !sm.Can(StateReceived) {
		t.Fatal("idle should allow received")
	}
	if err := sm.Transition(StateReceived); err != nil {
		t.Fatal(err)
	}
	if sm.Current() != StateReceived {
		t.Errorf("state = %s, want received", sm.Current())
	}
	if err := sm.Transition(StateAnalyzed); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StatePlanned); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StatePolicyOK); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StateExecuting); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StateValidating); err != nil {
		t.Fatal(err)
	}
	if err := sm.Transition(StateDone); err != nil {
		t.Fatal(err)
	}
	if sm.Current() != StateDone {
		t.Errorf("state = %s, want done", sm.Current())
	}
	if len(sm.History()) != 7 {
		t.Errorf("history = %d transitions, want 7", len(sm.History()))
	}
}

func TestStateMachineInvalidTransition(t *testing.T) {
	sm := NewStateMachine()
	// Skipping straight to done is not allowed.
	if err := sm.Transition(StateDone); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("err = %v, want ErrInvalidTransition", err)
	}
	if sm.Current() != StateIdle {
		t.Errorf("invalid transition must not move state, got %s", sm.Current())
	}
	// A terminal state has no outgoing transitions.
	sm2 := NewStateMachine()
	_ = sm2.Transition(StateReceived)
	_ = sm2.Transition(StateAnalyzed)
	_ = sm2.Transition(StatePlanned)
	_ = sm2.Transition(StatePolicyOK)
	_ = sm2.Transition(StateExecuting)
	_ = sm2.Transition(StateValidating)
	_ = sm2.Transition(StateDone)
	if err := sm2.Transition(StateReceived); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("terminal state must reject transitions, got %v", err)
	}
}

func TestStateMachineRecoveryPath(t *testing.T) {
	sm := NewStateMachine()
	// Walk to executing, then route a failure through recovering.
	_ = sm.Transition(StateReceived)
	_ = sm.Transition(StateAnalyzed)
	_ = sm.Transition(StatePlanned)
	_ = sm.Transition(StatePolicyOK)
	_ = sm.Transition(StateExecuting)
	if !sm.Can(StateRecovering) {
		t.Fatal("executing must allow recovering")
	}
	_ = sm.Transition(StateRecovering)
	if !sm.Can(StateRecovered) || !sm.Can(StateFailed) {
		t.Fatal("recovering must allow recovered and failed")
	}
	_ = sm.Transition(StateRecovered)
	if sm.Current() != StateRecovered {
		t.Errorf("state = %s, want recovered", sm.Current())
	}
}

func TestStateMachineReset(t *testing.T) {
	sm := NewStateMachine()
	_ = sm.Transition(StateReceived)
	sm.Reset()
	if sm.Current() != StateIdle || len(sm.History()) != 0 {
		t.Error("reset should return to idle with empty history")
	}
}
