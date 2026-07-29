package failure

import (
	"testing"
)

func TestHandleFailure_CodeFailure_AutoRepair(t *testing.T) {
	rm := NewRecoveryManager()

	tests := []struct {
		attempts int
		want     RecoveryAction
		desc     string
	}{
		{0, ActionAutoRepair, "first failure — auto repair"},
		{1, ActionAutoRepair, "second failure — auto repair"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := rm.HandleFailure(CODE_FAILURE, tc.attempts); got != tc.want {
				t.Errorf("HandleFailure(CODE_FAILURE, %d) = %s, want %s", tc.attempts, got, tc.want)
			}
		})
	}
}

func TestHandleFailure_CodeFailure_EscalateAfterMax(t *testing.T) {
	rm := NewRecoveryManager()

	tests := []struct {
		attempts int
		want     RecoveryAction
		desc     string
	}{
		{2, ActionEscalateToHuman, "third failure — escalate"},
		{3, ActionEscalateToHuman, "fourth failure — still escalate"},
		{10, ActionEscalateToHuman, "many failures — escalate"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := rm.HandleFailure(CODE_FAILURE, tc.attempts); got != tc.want {
				t.Errorf("HandleFailure(CODE_FAILURE, %d) = %s, want %s", tc.attempts, got, tc.want)
			}
		})
	}
}

func TestHandleFailure_CodeFailure_CustomMax(t *testing.T) {
	rm := &RecoveryManager{MaxAutoRepairAttempts: 1}

	if got := rm.HandleFailure(CODE_FAILURE, 0); got != ActionAutoRepair {
		t.Errorf("HandleFailure(CODE_FAILURE, 0) with Max=1 = %s, want ActionAutoRepair", got)
	}
	if got := rm.HandleFailure(CODE_FAILURE, 1); got != ActionEscalateToHuman {
		t.Errorf("HandleFailure(CODE_FAILURE, 1) with Max=1 = %s, want ActionEscalateToHuman", got)
	}
}

func TestHandleFailure_TestFailure(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(TEST_FAILURE, 0); got != ActionReplan {
		t.Errorf("HandleFailure(TEST_FAILURE) = %s, want ActionReplan", got)
	}
	// attemptCount is irrelevant for TEST_FAILURE
	if got := rm.HandleFailure(TEST_FAILURE, 5); got != ActionReplan {
		t.Errorf("HandleFailure(TEST_FAILURE, 5) = %s, want ActionReplan", got)
	}
}

func TestHandleFailure_EnvironmentFailure(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(ENVIRONMENT_FAILURE, 0); got != ActionInvestigateEnv {
		t.Errorf("HandleFailure(ENVIRONMENT_FAILURE) = %s, want ActionInvestigateEnv", got)
	}
}

func TestHandleFailure_ScopeFailure(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(SCOPE_FAILURE, 0); got != ActionImmediateRollback {
		t.Errorf("HandleFailure(SCOPE_FAILURE) = %s, want ActionImmediateRollback", got)
	}
}

func TestHandleFailure_SecurityIssue(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(SECURITY_ISSUE, 0); got != ActionImmediateRollbackAndBlock {
		t.Errorf("HandleFailure(SECURITY_ISSUE) = %s, want ActionImmediateRollbackAndBlock", got)
	}
}

func TestHandleFailure_Unknown(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(UNKNOWN, 0); got != ActionHaltAndEscalate {
		t.Errorf("HandleFailure(UNKNOWN) = %s, want ActionHaltAndEscalate", got)
	}
}

func TestHandleFailure_InvalidClass(t *testing.T) {
	rm := NewRecoveryManager()
	if got := rm.HandleFailure(FailureClass(99), 0); got != ActionHaltAndEscalate {
		t.Errorf("HandleFailure(invalid) = %s, want ActionHaltAndEscalate", got)
	}
}

func TestNewRecoveryManager_Defaults(t *testing.T) {
	rm := NewRecoveryManager()
	if rm.MaxAutoRepairAttempts != DefaultMaxAutoRepairAttempts {
		t.Errorf("MaxAutoRepairAttempts = %d, want %d", rm.MaxAutoRepairAttempts, DefaultMaxAutoRepairAttempts)
	}
}
