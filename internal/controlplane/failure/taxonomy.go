package failure

import (
	"fmt"

	coreclassifier "github.com/PizenLabs/izen/internal/core/classifier"
)

// FailureClass represents a deterministic category of execution/review failure.
type FailureClass int

const (
	CODE_FAILURE        FailureClass = iota // Syntax, compilation, linting errors
	ENVIRONMENT_FAILURE                     // Missing binaries, network down, config errors
	TEST_FAILURE                            // Assertion failures, spec mismatches
	SCOPE_FAILURE                           // Mutation outside authorized scope or budget
	SECURITY_ISSUE                          // Hardcoded secrets, forbidden patterns
	UNKNOWN                                 // Unclassified or ambiguous
)

func (fc FailureClass) String() string {
	switch fc {
	case CODE_FAILURE:
		return "CODE_FAILURE"
	case ENVIRONMENT_FAILURE:
		return "ENVIRONMENT_FAILURE"
	case TEST_FAILURE:
		return "TEST_FAILURE"
	case SCOPE_FAILURE:
		return "SCOPE_FAILURE"
	case SECURITY_ISSUE:
		return "SECURITY_ISSUE"
	case UNKNOWN:
		return "UNKNOWN"
	default:
		return fmt.Sprintf("FailureClass(%d)", int(fc))
	}
}

// ToCore maps the control-plane FailureClass to the core classifier's
// FailureClass for integration with the workflow state machine.
func (fc FailureClass) ToCore() coreclassifier.FailureClass {
	switch fc {
	case CODE_FAILURE:
		return coreclassifier.FailureCodeClass
	case ENVIRONMENT_FAILURE:
		return coreclassifier.FailureEnvironmentClass
	case TEST_FAILURE:
		return coreclassifier.FailureTestClass
	case SCOPE_FAILURE:
		return coreclassifier.FailureScopeClass
	case SECURITY_ISSUE:
		return coreclassifier.FailureCodeClass // treated as code defect by core
	case UNKNOWN:
		return coreclassifier.FailureUnknownClass
	default:
		return coreclassifier.FailureUnknownClass
	}
}

// RecoveryAction specifies the deterministic action the system must take in
// response to a classified failure.
type RecoveryAction int

const (
	ActionAutoRepair                RecoveryAction = iota // Retry with corrective prompt
	ActionEscalateToHuman                                 // Max auto-repairs exhausted
	ActionReplan                                          // Re-trigger planning cycle
	ActionInvestigateEnv                                  // Diagnostic mode, no mutation
	ActionImmediateRollback                               // Rollback to last checkpoint
	ActionImmediateRollbackAndBlock                       // Rollback + block further mutations
	ActionHaltAndEscalate                                 // Mandatory human stop
)

func (ra RecoveryAction) String() string {
	switch ra {
	case ActionAutoRepair:
		return "ActionAutoRepair"
	case ActionEscalateToHuman:
		return "ActionEscalateToHuman"
	case ActionReplan:
		return "ActionReplan"
	case ActionInvestigateEnv:
		return "ActionInvestigateEnv"
	case ActionImmediateRollback:
		return "ActionImmediateRollback"
	case ActionImmediateRollbackAndBlock:
		return "ActionImmediateRollbackAndBlock"
	case ActionHaltAndEscalate:
		return "ActionHaltAndEscalate"
	default:
		return fmt.Sprintf("RecoveryAction(%d)", int(ra))
	}
}
