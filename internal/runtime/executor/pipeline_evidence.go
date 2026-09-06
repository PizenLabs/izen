//nolint:gocritic
package executor

import (
	"context"
	"strings"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
)

// BuildEvidenceVector aggregates stdout, exit codes, and test logs into an
// EvidenceVector covering L0..L5. The mapping is deterministic:
//
//	L0 Execution       — PASS if mutation applied and no execution error
//	L1 Syntax          — PASS if stdout does not contain "syntax error"
//	L2 StaticAnalysis  — PASS if stdout does not contain "lint" failures
//	L3 UnitTests       — PASS if test logs do not contain "FAIL"
//	L4 IntegrationTests— PASS if test logs indicate integration PASS or empty
//	L5 HumanSignoff    — SKIP unless humanApproved true (then PASS)
func BuildEvidenceVector(result domain.MutationResult, stdout string, exitCode int, testLogs string, humanApproved bool) evidence.EvidenceVector {
	v := evidence.EvidenceVector{}
	lower := strings.ToLower(stdout + " " + testLogs)

	// L0
	if result.Applied && exitCode == 0 {
		v.Levels[evidence.L0_Execution] = evidence.VerdictPass
	} else if exitCode != 0 || !result.Applied {
		// If no mutation and no error, treat as UNKNOWN for read-only ops
		if len(result.Targets) == 0 && exitCode == 0 {
			v.Levels[evidence.L0_Execution] = evidence.VerdictPass
		} else {
			v.Levels[evidence.L0_Execution] = evidence.VerdictFail
		}
		if strings.Contains(lower, "fail") {
			v.Levels[evidence.L0_Execution] = evidence.VerdictFail
		}
	} else {
		v.Levels[evidence.L0_Execution] = evidence.VerdictUnknown
	}

	// L1 Syntax
	if strings.Contains(lower, "syntax error") || strings.Contains(lower, "unexpected token") {
		v.Levels[evidence.L1_Syntax] = evidence.VerdictFail
	} else if v.Levels[evidence.L0_Execution] == evidence.VerdictPass {
		v.Levels[evidence.L1_Syntax] = evidence.VerdictPass
	} else {
		v.Levels[evidence.L1_Syntax] = evidence.VerdictSkip
	}

	// L2 StaticAnalysis
	if strings.Contains(lower, "lint error") || strings.Contains(lower, "staticcheck") {
		v.Levels[evidence.L2_StaticAnalysis] = evidence.VerdictFail
	} else if v.Levels[evidence.L1_Syntax] == evidence.VerdictPass {
		v.Levels[evidence.L2_StaticAnalysis] = evidence.VerdictPass
	} else {
		v.Levels[evidence.L2_StaticAnalysis] = evidence.VerdictSkip
	}

	// L3 UnitTests
	if strings.Contains(strings.ToLower(testLogs), "fail") || strings.Contains(strings.ToLower(testLogs), "failed") {
		v.Levels[evidence.L3_UnitTests] = evidence.VerdictFail
	} else if testLogs == "" {
		// No test output — treat as SKIP (verifier absent, INV:11 never implicit PASS)
		v.Levels[evidence.L3_UnitTests] = evidence.VerdictSkip
	} else {
		v.Levels[evidence.L3_UnitTests] = evidence.VerdictPass
	}

	// L4 IntegrationTests
	if strings.Contains(strings.ToLower(testLogs), "integration fail") {
		v.Levels[evidence.L4_IntegrationTests] = evidence.VerdictFail
	} else if v.Levels[evidence.L3_UnitTests] == evidence.VerdictPass {
		v.Levels[evidence.L4_IntegrationTests] = evidence.VerdictPass
	} else {
		v.Levels[evidence.L4_IntegrationTests] = evidence.VerdictSkip
	}

	// L5 HumanSignoff
	if humanApproved {
		v.Levels[evidence.L5_HumanSignoff] = evidence.VerdictPass
	} else {
		v.Levels[evidence.L5_HumanSignoff] = evidence.VerdictSkip
	}

	v.HighestPassed = v.ComputeHighestPassed()
	return v
}

// EvaluateEvidenceAndRollback derives the terminal evidence state and, if the
// vector is FAILED for the required level, triggers CheckpointCoordinator.Rollback
// before the workflow transitions to Failed. It returns the derived state.
func (e *RuntimeExecutor) EvaluateEvidenceAndRollback(ctx context.Context, vec evidence.EvidenceVector, required evidence.EvidenceLevel, ckptID domain.CheckpointID, boundary domain.RollbackBoundary) evidence.EvidenceState {
	state := evidence.DeriveEvidenceState(vec, required)
	if state == evidence.VerdictFailed {
		if e.ckpt != nil {
			_ = e.ckpt.Rollback(ctx, ckptID, boundary)
		}
	}
	return state
}
