package domain

import "time"

// EvidenceLevel is the derived classification over capabilities (ARCH:28).
type EvidenceLevel uint8

const (
	LevelNone        EvidenceLevel = 0 // L0 — no verifier
	LevelArtifact    EvidenceLevel = 1 // L1 — hash + existence (VerifyAll)
	LevelStructural  EvidenceLevel = 2 // L2 — AST / symbol graph / formatter
	LevelDiagnostics EvidenceLevel = 3 // L3 — parser / type diagnostics / linter
	LevelBuild       EvidenceLevel = 4 // L4 — compiler / type checker (go build, tsc)
	LevelTests       EvidenceLevel = 5 // L5 — test runner / runtime validation
)

func (l EvidenceLevel) String() string {
	switch l {
	case LevelNone:
		return "L0_NONE"
	case LevelArtifact:
		return "L1_ARTIFACT"
	case LevelStructural:
		return "L2_STRUCTURAL"
	case LevelDiagnostics:
		return "L3_DIAGNOSTICS"
	case LevelBuild:
		return "L4_BUILD"
	case LevelTests:
		return "L5_TESTS"
	default:
		return "L0_NONE"
	}
}

// EvidenceVector is the graduated evidence state at time t.
type EvidenceVector struct {
	Levels        [6]EvidenceVerdict `json:"levels"` // index == EvidenceLevel
	HighestPassed EvidenceLevel      `json:"highest_passed"`
	CollectedAt   time.Time          `json:"collected_at"`
	Duration      time.Duration      `json:"duration"`
}

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

// EvidenceState is the terminal evidence classification (ARCH:30).
type EvidenceState uint8

const (
	EvidenceUnverified        EvidenceState = iota // no verifier or all SKIP
	EvidencePartiallyVerified                       // some PASS, not all required
	EvidenceVerified                                // HighestPassed >= RequiredLevel
)

func (s EvidenceState) String() string {
	switch s {
	case EvidenceVerified:
		return "VERIFIED"
	case EvidencePartiallyVerified:
		return "PARTIALLY_VERIFIED"
	default:
		return "UNVERIFIED"
	}
}

// DeriveEvidenceState computes EvidenceState from the vector and policy.
func DeriveEvidenceState(v EvidenceVector, required EvidenceLevel) EvidenceState {
	if v.HighestPassed >= required && required > LevelNone {
		return EvidenceVerified
	}
	if v.HighestPassed > LevelNone {
		return EvidencePartiallyVerified
	}
	return EvidenceUnverified
}
