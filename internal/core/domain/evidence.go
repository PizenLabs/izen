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

// computeHighestPassed returns the greatest L with contiguous PASS from L0 upward.
func computeHighestPassed(v EvidenceVector) EvidenceLevel {
	if v.Levels[LevelNone] != VerdictPass && LevelNone == 0 {
		// LevelNone is L0 — if L0 not PASS, nothing passed (unless LevelNone is sentinel 0 with UNKNOWN)
		// Treat LevelNone PASS as L0 PASS; if L0 is not PASS, highest is LevelNone (0) but not > LevelNone.
		// Check all levels contiguously starting at 0.
		hasPass := false
		for lvl := LevelNone; lvl <= LevelTests; lvl++ {
			if v.Levels[lvl] != VerdictPass {
				break
			}
			hasPass = true
			if lvl > 0 && v.Levels[lvl-1] != VerdictPass {
				break
			}
		}
		if !hasPass && v.Levels[0] != VerdictPass {
			return LevelNone
		}
	}
	// Contiguous PASS check
	if v.Levels[0] != VerdictPass {
		return LevelNone
	}
	highest := LevelNone
	for lvl := LevelNone; lvl <= LevelTests; lvl++ {
		if v.Levels[lvl] == VerdictPass {
			ok := true
			for k := LevelNone; k < lvl; k++ {
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
// It enforces the monotonic rule: Level N cannot pass if Level N-1 failed or is missing.
func DeriveEvidenceState(v EvidenceVector, required EvidenceLevel) EvidenceState {
	// Recompute HighestPassed with monotonic enforcement if Levels are populated,
	// otherwise fall back to stored HighestPassed for backward compatibility.
	highest := v.HighestPassed
	// If Levels contain any explicit verdicts, recompute contiguously.
	hasExplicit := false
	for i := 0; i < len(v.Levels); i++ {
		if v.Levels[i] != VerdictUnknown {
			hasExplicit = true
			break
		}
	}
	if hasExplicit {
		highest = computeHighestPassed(v)
		// Monotonic violation detection: if any level up to required is FAIL,
		// the vector is considered failed even if HighestPassed looks high.
		for lvl := LevelNone; lvl <= required && int(lvl) < len(v.Levels); lvl++ {
			if v.Levels[lvl] == VerdictFail {
				// FAIL at or below required overrides PASS
				// If fail is below required, highest should reflect gap.
				if lvl <= required {
					// Derive should not be VERIFIED if any required level failed
					// We keep highest as computed (will be below fail)
				}
			}
			// Monotonic gap: N PASS but N-1 not PASS => treat as not truly passed
			if lvl > LevelNone && v.Levels[lvl] == VerdictPass && v.Levels[lvl-1] != VerdictPass {
				// Gap makes the PASS invalid — cap highest below gap
				if highest >= lvl {
					highest = lvl - 1
				}
			}
		}
	}
	if highest >= required && required > LevelNone {
		return EvidenceVerified
	}
	if highest > LevelNone {
		return EvidencePartiallyVerified
	}
	return EvidenceUnverified
}
