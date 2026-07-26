package workflow

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/core/classifier"
)

func TestNewMachine_InitialState(t *testing.T) {
	m := NewWorkflowStateMachine()
	if m.State() != StateIdle {
		t.Errorf("initial state = %s, want %s", m.State(), StateIdle)
	}
	if !StateIdle.Valid() {
		t.Error("StateIdle should be valid")
	}
}

func TestState_Valid(t *testing.T) {
	states := []WorkflowState{StateIdle, StateInvestigating, StatePlanning, StateBuilding, StateReviewing, StateRepairing, StateVerified, StateFailed}
	for _, s := range states {
		if !s.Valid() {
			t.Errorf("WorkflowState(%d).Valid() = false, want true", int(s))
		}
	}
	if WorkflowState(-1).Valid() {
		t.Error("WorkflowState(-1).Valid() = true, want false")
	}
	if WorkflowState(100).Valid() {
		t.Error("WorkflowState(100).Valid() = true, want false")
	}
}

func TestState_IsTerminal(t *testing.T) {
	if !StateVerified.IsTerminal() {
		t.Error("StateVerified.IsTerminal() = false, want true")
	}
	if !StateFailed.IsTerminal() {
		t.Error("StateFailed.IsTerminal() = false, want true")
	}
	if StateIdle.IsTerminal() {
		t.Error("StateIdle.IsTerminal() = true, want false")
	}
	if StateBuilding.IsTerminal() {
		t.Error("StateBuilding.IsTerminal() = true, want false")
	}
}

func TestState_String(t *testing.T) {
	tests := []struct {
		s    WorkflowState
		want string
	}{
		{StateIdle, "idle"},
		{StateInvestigating, "investigating"},
		{StatePlanning, "planning"},
		{StateBuilding, "building"},
		{StateReviewing, "reviewing"},
		{StateRepairing, "repairing"},
		{StateVerified, "verified"},
		{StateFailed, "failed"},
		{WorkflowState(99), "WorkflowState(99)"},
	}
	for _, tc := range tests {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("WorkflowState(%d).String() = %q, want %q", int(tc.s), got, tc.want)
		}
	}
}

func TestEvent_String(t *testing.T) {
	tests := []struct {
		e    WorkflowEvent
		want string
	}{
		{EventInvestigate, "investigate"},
		{EventPlan, "plan"},
		{EventBuild, "build"},
		{EventReview, "review"},
		{EventFailureIdentified, "failure-identified"},
		{EventVerificationPassed, "verification-passed"},
		{EventReset, "reset"},
		{WorkflowEvent(99), "WorkflowEvent(99)"},
	}
	for _, tc := range tests {
		if got := tc.e.String(); got != tc.want {
			t.Errorf("WorkflowEvent(%d).String() = %q, want %q", int(tc.e), got, tc.want)
		}
	}
}

func TestError_Error(t *testing.T) {
	te := &TransitionError{From: StateIdle, Event: EventBuild, Msg: "test error"}
	if te.Error() == "" {
		t.Fatal("TransitionError.Error() returned empty string")
	}
	ge := &GuardError{From: StateIdle, Event: EventBuild, Msg: "guard error"}
	if ge.Error() == "" {
		t.Fatal("GuardError.Error() returned empty string")
	}
}

func TestTransitions_Idle(t *testing.T) {
	m := NewWorkflowStateMachine()

	tests := []struct {
		name  string
		event WorkflowEvent
		want  WorkflowState
		err   bool
	}{
		{"investigate", EventInvestigate, StateInvestigating, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m = NewWorkflowStateMachine()
			err := m.SendEvent(tc.event, TransitionContext{})
			if tc.err {
				if err == nil {
					t.Errorf("SendEvent(%s) expected error", tc.event)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendEvent(%s): %v", tc.event, err)
			}
			if m.State() != tc.want {
				t.Errorf("state after %s = %s, want %s", tc.event, m.State(), tc.want)
			}
		})
	}
}

func TestTransitions_Idle_Plan(t *testing.T) {
	m := NewWorkflowStateMachine()
	if err := m.SendEvent(EventPlan, TransitionContext{}); err != nil {
		t.Fatalf("SendEvent(Plan): %v", err)
	}
	if m.State() != StatePlanning {
		t.Errorf("state = %s, want %s", m.State(), StatePlanning)
	}
}

func TestTransitions_Idle_Reset(t *testing.T) {
	m := NewWorkflowStateMachine()
	if err := m.SendEvent(EventReset, TransitionContext{}); err != nil {
		t.Fatalf("SendEvent(Reset): %v", err)
	}
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Idle_Invalid(t *testing.T) {
	m := NewWorkflowStateMachine()
	invalidEvents := []WorkflowEvent{EventBuild, EventReview, EventFailureIdentified, EventVerificationPassed}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Idle should be rejected", e)
			continue
		}
		var transErr *TransitionError
		if !errors.As(err, &transErr) {
			t.Errorf("SendEvent(%s) error type = %T, want *TransitionError", e, err)
		}
		if m.State() != StateIdle {
			t.Errorf("state after failed %s = %s, want %s", e, m.State(), StateIdle)
		}
	}
}

func TestTransitions_Investigating(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventInvestigate, TransitionContext{})

	if err := m.SendEvent(EventPlan, TransitionContext{}); err != nil {
		t.Fatalf("SendEvent(Plan) from Investigating: %v", err)
	}
	if m.State() != StatePlanning {
		t.Errorf("state = %s, want %s", m.State(), StatePlanning)
	}
}

func TestTransitions_Investigating_Invalid(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventInvestigate, TransitionContext{})

	invalidEvents := []WorkflowEvent{EventBuild, EventReview, EventFailureIdentified, EventVerificationPassed, EventInvestigate}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Investigating should be rejected", e)
			continue
		}
		var transErr *TransitionError
		if !errors.As(err, &transErr) {
			t.Errorf("SendEvent(%s) error type = %T, want *TransitionError", e, err)
		}
	}
}

func TestTransitions_Investigating_Reset(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventInvestigate, TransitionContext{})
	m.MustTransition(EventReset, TransitionContext{})
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Planning_Build_Guard(t *testing.T) {
	tests := []struct {
		name string
		ctx  TransitionContext
		want WorkflowState
		err  bool
	}{
		{"no plan no caps", TransitionContext{}, StatePlanning, true},
		{"has plan no caps", TransitionContext{HasPlan: true}, StatePlanning, true},
		{"no plan has caps", TransitionContext{HasCapabilities: true}, StatePlanning, true},
		{"has plan and caps", TransitionContext{HasPlan: true, HasCapabilities: true}, StateBuilding, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewWorkflowStateMachine()
			m.MustTransition(EventPlan, TransitionContext{})
			err := m.SendEvent(EventBuild, tc.ctx)
			if tc.err {
				if err == nil {
					t.Fatal("expected GuardError")
				}
				var guardErr *GuardError
				if !errors.As(err, &guardErr) {
					t.Errorf("error type = %T, want *GuardError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendEvent(Build): %v", err)
			}
			if m.State() != tc.want {
				t.Errorf("state = %s, want %s", m.State(), tc.want)
			}
		})
	}
}

func TestTransitions_Planning_Invalid(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventPlan, TransitionContext{})

	invalidEvents := []WorkflowEvent{EventInvestigate, EventPlan, EventReview, EventFailureIdentified, EventVerificationPassed}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Planning should be rejected", e)
		}
	}
}

func TestTransitions_Planning_Reset(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventReset, TransitionContext{})
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Building_Review(t *testing.T) {
	m := buildMachine(t)
	m.MustTransition(EventReview, TransitionContext{})
	if m.State() != StateReviewing {
		t.Errorf("state = %s, want %s", m.State(), StateReviewing)
	}
}

func TestTransitions_Building_Invalid(t *testing.T) {
	m := buildMachine(t)
	invalidEvents := []WorkflowEvent{EventInvestigate, EventPlan, EventBuild, EventVerificationPassed}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Building should be rejected", e)
		}
	}
}

func TestTransitions_Building_Reset(t *testing.T) {
	m := buildMachine(t)
	m.MustTransition(EventReset, TransitionContext{})
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Reviewing_Verified(t *testing.T) {
	m := reviewMachine(t)
	m.MustTransition(EventVerificationPassed, TransitionContext{})
	if m.State() != StateVerified {
		t.Errorf("state = %s, want %s", m.State(), StateVerified)
	}
}

func TestTransitions_Reviewing_Invalid(t *testing.T) {
	m := reviewMachine(t)
	invalidEvents := []WorkflowEvent{EventInvestigate, EventPlan, EventBuild, EventReview}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Reviewing should be rejected", e)
		}
	}
}

func TestTransitions_Reviewing_Reset(t *testing.T) {
	m := reviewMachine(t)
	m.MustTransition(EventReset, TransitionContext{})
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Repairing_Build_Guard(t *testing.T) {
	tests := []struct {
		name string
		ctx  TransitionContext
		err  bool
	}{
		{"no capabilities", TransitionContext{}, true},
		{"with capabilities", TransitionContext{HasCapabilities: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := repairMachine(t)
			err := m.SendEvent(EventBuild, tc.ctx)
			if tc.err {
				if err == nil {
					t.Fatal("expected GuardError")
				}
				return
			}
			if err != nil {
				t.Fatalf("SendEvent(Build) from Repairing: %v", err)
			}
			if m.State() != StateBuilding {
				t.Errorf("state = %s, want %s", m.State(), StateBuilding)
			}
		})
	}
}

func TestTransitions_Repairing_Invalid(t *testing.T) {
	m := repairMachine(t)
	invalidEvents := []WorkflowEvent{EventInvestigate, EventPlan, EventReview, EventVerificationPassed}
	for _, e := range invalidEvents {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Repairing should be rejected", e)
		}
	}
}

func TestTransitions_Repairing_Reset(t *testing.T) {
	m := repairMachine(t)
	m.MustTransition(EventReset, TransitionContext{})
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Verified_OnlyReset(t *testing.T) {
	m := verifiedMachine(t)
	if err := m.SendEvent(EventReset, TransitionContext{}); err != nil {
		t.Fatalf("SendEvent(Reset) from Verified: %v", err)
	}
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Verified_Invalid(t *testing.T) {
	m := verifiedMachine(t)
	events := []WorkflowEvent{EventInvestigate, EventPlan, EventBuild, EventReview, EventFailureIdentified, EventVerificationPassed}
	for _, e := range events {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Verified should be rejected", e)
		}
	}
}

func TestTransitions_Failed_OnlyReset(t *testing.T) {
	m := buildMachine(t)
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureUnknownClass,
	})
	if m.State() != StateFailed {
		t.Fatalf("state = %s, want %s", m.State(), StateFailed)
	}
	if err := m.SendEvent(EventReset, TransitionContext{}); err != nil {
		t.Fatalf("SendEvent(Reset) from Failed: %v", err)
	}
	if m.State() != StateIdle {
		t.Errorf("state after reset = %s, want %s", m.State(), StateIdle)
	}
}

func TestTransitions_Failed_Invalid(t *testing.T) {
	m := buildMachine(t)
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureUnknownClass,
	})
	events := []WorkflowEvent{EventInvestigate, EventPlan, EventBuild, EventReview, EventFailureIdentified, EventVerificationPassed}
	for _, e := range events {
		err := m.SendEvent(e, TransitionContext{})
		if err == nil {
			t.Errorf("SendEvent(%s) from Failed should be rejected", e)
		}
	}
}

func TestFailureRouting_FromBuilding(t *testing.T) {
	for _, tc := range failureTestCases() {
		t.Run("building_"+tc.name, func(t *testing.T) {
			m := buildMachine(t)
			err := m.SendEvent(EventFailureIdentified, TransitionContext{FailureClass: tc.class})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("SendEvent(FailureIdentified): %v", err)
			}
			if m.State() != tc.want {
				t.Errorf("state = %s, want %s", m.State(), tc.want)
			}
		})
	}
}

func TestFailureRouting_FromReviewing(t *testing.T) {
	for _, tc := range failureTestCases() {
		t.Run("reviewing_"+tc.name, func(t *testing.T) {
			m := reviewMachine(t)
			err := m.SendEvent(EventFailureIdentified, TransitionContext{FailureClass: tc.class})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("SendEvent(FailureIdentified): %v", err)
			}
			if m.State() != tc.want {
				t.Errorf("state = %s, want %s", m.State(), tc.want)
			}
		})
	}
}

func TestFailureRouting_FromRepairing(t *testing.T) {
	repairClasses := []struct {
		name  string
		class classifier.FailureClass
		want  WorkflowState
	}{
		{"code", classifier.FailureCodeClass, StateRepairing},
		{"environment", classifier.FailureEnvironmentClass, StateInvestigating},
		{"test", classifier.FailureTestClass, StatePlanning},
		{"scope", classifier.FailureScopeClass, StatePlanning},
		{"unknown", classifier.FailureUnknownClass, StateFailed},
	}
	for _, tc := range repairClasses {
		t.Run("repairing_"+tc.name, func(t *testing.T) {
			m := repairMachine(t)
			err := m.SendEvent(EventFailureIdentified, TransitionContext{FailureClass: tc.class})
			if err != nil {
				t.Fatalf("SendEvent(FailureIdentified) from Repairing: %v", err)
			}
			if m.State() != tc.want {
				t.Errorf("state = %s, want %s", m.State(), tc.want)
			}
		})
	}
}

func TestMustTransition_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on invalid transition")
		}
	}()
	m := NewWorkflowStateMachine()
	m.MustTransition(EventBuild, TransitionContext{})
}

func TestCheckpointCoordinator_NoRef(t *testing.T) {
	mgr := &mockCheckpointManager{}
	cc := NewCheckpointCoordinator(mgr)
	if cc.HasRef() {
		t.Error("HasRef() = true before any checkpoint")
	}
	if err := cc.Rollback(); err == nil {
		t.Error("Rollback() without ref: expected error")
	}
}

func TestCheckpointCoordinator_CreateAndRollback(t *testing.T) {
	mgr := &mockCheckpointManager{}
	cc := NewCheckpointCoordinator(mgr)
	if err := cc.CreateBeforeBuild(); err != nil {
		t.Fatalf("CreateBeforeBuild(): %v", err)
	}
	if !cc.HasRef() {
		t.Error("HasRef() = false after CreateBeforeBuild")
	}
	if err := cc.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
}

func TestCheckpointCoordinator_CreateFails(t *testing.T) {
	mgr := &mockCheckpointManager{createErr: errors.New("disk full")}
	cc := NewCheckpointCoordinator(mgr)
	if err := cc.CreateBeforeBuild(); err == nil {
		t.Fatal("CreateBeforeBuild(): expected error")
	}
	if cc.HasRef() {
		t.Error("HasRef() = true after failed create")
	}
}

func TestMachine_WithCheckpoint_BuildTrigger(t *testing.T) {
	mgr := &mockCheckpointManager{}
	cc := NewCheckpointCoordinator(mgr)
	m := NewWorkflowStateMachine().WithCheckpointCoordinator(cc)
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	if !mgr.created {
		t.Error("checkpoint should have been created before build")
	}
	if m.State() != StateBuilding {
		t.Errorf("state = %s, want %s", m.State(), StateBuilding)
	}
}

func TestMachine_WithCheckpoint_RepairTrigger(t *testing.T) {
	mgr := &mockCheckpointManager{}
	cc := NewCheckpointCoordinator(mgr)
	m := NewWorkflowStateMachine().WithCheckpointCoordinator(cc)
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})

	mgr.created = false
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureCodeClass,
	})
	if !mgr.created {
		t.Error("checkpoint should have been created before entering repairing")
	}
	if m.State() != StateRepairing {
		t.Errorf("state = %s, want %s", m.State(), StateRepairing)
	}
}

func TestMachine_ScopeFailureTriggersRollback(t *testing.T) {
	mgr := &mockCheckpointManager{}
	cc := NewCheckpointCoordinator(mgr)
	m := NewWorkflowStateMachine().WithCheckpointCoordinator(cc)
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	mgr.created = false
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureScopeClass,
	})
	if !mgr.rolledBack {
		t.Error("rollback should have been triggered on scope failure")
	}
	if m.State() != StatePlanning {
		t.Errorf("state = %s, want %s", m.State(), StatePlanning)
	}
}

func TestMachine_CheckpointCreationFailure(t *testing.T) {
	mgr := &mockCheckpointManager{createErr: errors.New("disk full")}
	cc := NewCheckpointCoordinator(mgr)
	m := NewWorkflowStateMachine().WithCheckpointCoordinator(cc)
	m.MustTransition(EventPlan, TransitionContext{})

	err := m.SendEvent(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	if err == nil {
		t.Fatal("expected error from checkpoint failure")
	}
	if m.State() != StatePlanning {
		t.Errorf("state should remain planning after checkpoint failure, got %s", m.State())
	}
}

func TestMachine_WithoutCoordinator(t *testing.T) {
	m := NewWorkflowStateMachine()
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	if m.State() != StateBuilding {
		t.Errorf("state = %s, want %s", m.State(), StateBuilding)
	}
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureScopeClass,
	})
	if m.State() != StatePlanning {
		t.Errorf("state after scope failure without coordinator = %s, want %s", m.State(), StatePlanning)
	}
}

func TestMachine_FullLifecycle(t *testing.T) {
	m := NewWorkflowStateMachine()

	assertState := func(want WorkflowState) {
		t.Helper()
		if m.State() != want {
			t.Errorf("state = %s, want %s", m.State(), want)
		}
	}

	sendOk := func(event WorkflowEvent, ctx TransitionContext) {
		t.Helper()
		if err := m.SendEvent(event, ctx); err != nil {
			t.Fatalf("SendEvent(%s): %v", event, err)
		}
	}

	assertState(StateIdle)
	sendOk(EventPlan, TransitionContext{})
	assertState(StatePlanning)
	sendOk(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	assertState(StateBuilding)

	sendOk(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureCodeClass,
	})
	assertState(StateRepairing)

	sendOk(EventBuild, TransitionContext{HasCapabilities: true})
	assertState(StateBuilding)

	sendOk(EventReview, TransitionContext{})
	assertState(StateReviewing)

	sendOk(EventVerificationPassed, TransitionContext{})
	assertState(StateVerified)

	sendOk(EventReset, TransitionContext{})
	assertState(StateIdle)
}

func TestMachine_InvalidState(t *testing.T) {
	m := &WorkflowStateMachine{current: WorkflowState(99)}
	err := m.SendEvent(EventReset, TransitionContext{})
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
	var transErr *TransitionError
	if !errors.As(err, &transErr) {
		t.Errorf("error type = %T, want *TransitionError", err)
	}
}

func failureTestCases() []struct {
	name    string
	class   classifier.FailureClass
	want    WorkflowState
	wantErr bool
} {
	return []struct {
		name    string
		class   classifier.FailureClass
		want    WorkflowState
		wantErr bool
	}{
		{"code", classifier.FailureCodeClass, StateRepairing, false},
		{"environment", classifier.FailureEnvironmentClass, StateInvestigating, false},
		{"test", classifier.FailureTestClass, StatePlanning, false},
		{"scope", classifier.FailureScopeClass, StatePlanning, false},
		{"unknown", classifier.FailureUnknownClass, StateFailed, false},
	}
}

func buildMachine(t *testing.T) *WorkflowStateMachine {
	t.Helper()
	m := NewWorkflowStateMachine()
	m.MustTransition(EventPlan, TransitionContext{})
	m.MustTransition(EventBuild, TransitionContext{HasPlan: true, HasCapabilities: true})
	return m
}

func reviewMachine(t *testing.T) *WorkflowStateMachine {
	t.Helper()
	m := buildMachine(t)
	m.MustTransition(EventReview, TransitionContext{})
	return m
}

func repairMachine(t *testing.T) *WorkflowStateMachine {
	t.Helper()
	m := buildMachine(t)
	m.MustTransition(EventFailureIdentified, TransitionContext{
		FailureClass: classifier.FailureCodeClass,
	})
	return m
}

func verifiedMachine(t *testing.T) *WorkflowStateMachine {
	t.Helper()
	m := reviewMachine(t)
	m.MustTransition(EventVerificationPassed, TransitionContext{})
	return m
}

type mockCheckpointManager struct {
	createErr   error
	rollbackErr error
	created     bool
	rolledBack  bool
}

func (m *mockCheckpointManager) CreateCheckpoint() (CheckpointRef, error) {
	m.created = true
	return "test-ref", m.createErr
}

func (m *mockCheckpointManager) RollbackToCheckpoint(ref CheckpointRef) error {
	m.rolledBack = true
	return m.rollbackErr
}

func (m *mockCheckpointManager) HasCheckpoint() bool {
	return m.created
}

func (m *mockCheckpointManager) LatestCheckpoint() (CheckpointRef, error) {
	return "test-ref", m.createErr
}
