package failure

// DefaultMaxAutoRepairAttempts is the maximum number of automatic repair
// attempts before CODE_FAILURE escalates to a human.
const DefaultMaxAutoRepairAttempts = 2

// RecoveryManager implements the bounded recovery loop. It maps each
// FailureClass to a deterministic RecoveryAction based on the failure type
// and the number of previous auto-repair attempts.
type RecoveryManager struct {
	MaxAutoRepairAttempts int
}

// NewRecoveryManager creates a RecoveryManager with the default limit of 2
// auto-repair attempts for CODE_FAILURE.
func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{
		MaxAutoRepairAttempts: DefaultMaxAutoRepairAttempts,
	}
}

// HandleFailure returns the RecoveryAction for the given FailureClass and
// attempt count. The attemptCount is the number of auto-repair attempts that
// have already been made for this failure class in the current cycle.
//
// Policies:
//   - CODE_FAILURE: AutoRepair until MaxAutoRepairAttempts, then EscalateToHuman
//   - TEST_FAILURE: Replan (re-trigger planning cycle)
//   - ENVIRONMENT_FAILURE: InvestigateEnv (diagnostic mode, no mutation)
//   - SCOPE_FAILURE: ImmediateRollback
//   - SECURITY_ISSUE: ImmediateRollbackAndBlock
//   - UNKNOWN: HaltAndEscalate (mandatory human stop)
func (rm *RecoveryManager) HandleFailure(fc FailureClass, attemptCount int) RecoveryAction {
	switch fc {
	case CODE_FAILURE:
		if attemptCount < rm.MaxAutoRepairAttempts {
			return ActionAutoRepair
		}
		return ActionEscalateToHuman

	case TEST_FAILURE:
		return ActionReplan

	case ENVIRONMENT_FAILURE:
		return ActionInvestigateEnv

	case SCOPE_FAILURE:
		return ActionImmediateRollback

	case SECURITY_ISSUE:
		return ActionImmediateRollbackAndBlock

	case UNKNOWN:
		return ActionHaltAndEscalate

	default:
		return ActionHaltAndEscalate
	}
}
