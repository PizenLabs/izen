package evidence

import (
	"time"
)

// EvidenceLevel is the L0..L5 graduated evidence classification.
// L0 Execution → L1 Syntax → L2 StaticAnalysis → L3 UnitTests → L4 IntegrationTests → L5 HumanSignoff
type EvidenceLevel uint8

const (
	L0_Execution        EvidenceLevel = 0
	L1_Syntax           EvidenceLevel = 1
	L2_StaticAnalysis   EvidenceLevel = 2
	L3_UnitTests        EvidenceLevel = 3
	L4_IntegrationTests EvidenceLevel = 4
	L5_HumanSignoff     EvidenceLevel = 5
)

// Legacy aliases matching the domain package naming (ARCH:28) so existing
// callers can migrate gradually. Values are identical to the L0..L5 scale.
const (
	LevelNone        = L0_Execution
	LevelArtifact    = L1_Syntax
	LevelStructural  = L2_StaticAnalysis
	LevelDiagnostics = L3_UnitTests
	LevelBuild       = L4_IntegrationTests
	LevelTests       = L5_HumanSignoff
)

func (l EvidenceLevel) String() string {
	switch l {
	case L0_Execution:
		return "L0_EXECUTION"
	case L1_Syntax:
		return "L1_SYNTAX"
	case L2_StaticAnalysis:
		return "L2_STATIC_ANALYSIS"
	case L3_UnitTests:
		return "L3_UNIT_TESTS"
	case L4_IntegrationTests:
		return "L4_INTEGRATION_TESTS"
	case L5_HumanSignoff:
		return "L5_HUMAN_SIGNOFF"
	default:
		return "L0_EXECUTION"
	}
}

// EvidenceVerdict is the per-level result.
type EvidenceVerdict uint8

const (
	VerdictUnknown EvidenceVerdict = iota // not yet evaluated
	VerdictPass
	VerdictFail
	VerdictSkip // verifier absent — explicit SKIP, never implicit PASS (INV:11)
)

func (v EvidenceVerdict) String() string {
	switch v {
	case VerdictPass:
		return "PASS"
	case VerdictFail:
		return "FAIL"
	case VerdictSkip:
		return "SKIP"
	default:
		return "UNKNOWN"
	}
}

// EvidenceState is the terminal classification derived from a vector + policy.
// It reuses the Verdict* naming as mandated by the Phase 5 execution prompt
// so that callers can switch on VerdictPassed / VerdictFailed / VerdictInconclusive.
type EvidenceState uint8

const (
	VerdictPassed       EvidenceState = iota // all required levels PASS contiguously
	VerdictFailed                            // any required level FAIL or monotonic gap
	VerdictInconclusive                      // any required level UNKNOWN/SKIP or missing
)

func (s EvidenceState) String() string {
	switch s {
	case VerdictPassed:
		return "PASSED"
	case VerdictFailed:
		return "FAILED"
	case VerdictInconclusive:
		return "INCONCLUSIVE"
	default:
		return "INCONCLUSIVE"
	}
}

// Aliases for callers that use the EvidenceVerified vocabulary from the
// system-model spec (ARCH:30). They map to the same terminal values.
const (
	EvidenceVerified          = VerdictPassed
	EvidencePartiallyVerified = VerdictInconclusive
	EvidenceUnverified        = VerdictInconclusive
)

// EvidenceVector is the graduated evidence state at time t.
// V_t = (v0, v1, v2, v3, v4, v5) where vi ∈ {PASS, FAIL, SKIP, UNKNOWN}
type EvidenceVector struct {
	Levels        [6]EvidenceVerdict `json:"levels"` // index == EvidenceLevel
	HighestPassed EvidenceLevel      `json:"highest_passed"`
	CollectedAt   time.Time          `json:"collected_at"`
	Duration      time.Duration      `json:"duration"`
}

// ComputeHighestPassed returns the greatest L with Verdict == PASS and all
// lower L also PASS. A gap (e.g., L2 PASS while L1 FAIL) does NOT advance
// beyond the gap — this enforces the monotonic rule.
func (v EvidenceVector) ComputeHighestPassed() EvidenceLevel {
	highest := EvidenceLevel(0)
	// L0 must be PASS to advance at all; if L0 is not PASS highest stays 0
	// but caller can distinguish via Levels[0] check.
	if v.Levels[L0_Execution] != VerdictPass {
		return LevelNone
	}
	highest = L0_Execution
	for lvl := L1_Syntax; lvl <= L5_HumanSignoff; lvl++ {
		if v.Levels[lvl] == VerdictPass {
			// monotonic: every predecessor must be PASS
			ok := true
			for k := L0_Execution; k < lvl; k++ {
				if v.Levels[k] != VerdictPass {
					ok = false
					break
				}
			}
			if ok {
				highest = lvl
			} else {
				break
			}
		} else {
			break
		}
	}
	return highest
}

// DeriveEvidenceState computes EvidenceState from the vector and policy.
// It enforces the monotonic rule: Level N cannot pass if Level N-1 failed
// or is missing (UNKNOWN/SKIP). Returns VerdictPassed, VerdictFailed, or
// VerdictInconclusive.
func DeriveEvidenceState(v EvidenceVector, required EvidenceLevel) EvidenceState {
	if required > L5_HumanSignoff {
		required = L5_HumanSignoff
	}
	// Fast path: L0 required is trivially satisfied if L0 PASS
	if required == L0_Execution {
		switch v.Levels[L0_Execution] {
		case VerdictPass:
			return VerdictPassed
		case VerdictFail:
			return VerdictFailed
		default:
			return VerdictInconclusive
		}
	}

	// Enforce monotonic rule and classify required slice [0..required].
	hasFail := false
	hasMissing := false
	for lvl := L0_Execution; lvl <= required; lvl++ {
		verdict := v.Levels[lvl]
		switch verdict {
		case VerdictFail:
			hasFail = true
		case VerdictPass:
			// Check monotonic gap: predecessor must be PASS
			if lvl > L0_Execution {
				prev := v.Levels[lvl-1]
				if prev != VerdictPass {
					hasFail = true
				}
			}
		case VerdictSkip, VerdictUnknown:
			hasMissing = true
		default:
			hasMissing = true
		}
	}

	if hasFail {
		return VerdictFailed
	}
	if hasMissing {
		return VerdictInconclusive
	}
	return VerdictPassed
}
