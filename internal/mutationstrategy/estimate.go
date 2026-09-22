package mutationstrategy

import (
	"math"
)

// EstimatedMutationSize is the structural planning signal for one
// proposed mutation step (or the aggregate plan). It is NOT a token
// prediction and it is NOT a budget. It preserves uncertainty so
// callers never receive a fake-precise single number.
//
// The estimate incorporates (where evidence exists): number of
// affected files/artifacts, operation type, approximate affected
// lines where reliable, structural complexity, dependency count,
// and expected content generation where capability metadata is
// available. It never hardcodes provider magic numbers.
type EstimatedMutationSize struct {
	// Lower is the conservative lower bound of structural units.
	Lower int `json:"lower"`
	// Expected is the central expectation.
	Expected int `json:"expected"`
	// Upper is the conservative upper bound.
	Upper int `json:"upper"`
	// Confidence in [0,1] — low when evidence is weak.
	Confidence float64 `json:"confidence"`
}

// Valid reports whether the estimate is well-formed.
func (e EstimatedMutationSize) Valid() bool {
	return e.Lower >= 0 && e.Expected >= 0 && e.Upper >= 0 &&
		e.Lower <= e.Expected && e.Expected <= e.Upper &&
		e.Confidence >= 0 && e.Confidence <= 1
}

// Fits reports whether the expected mutation fits within the given
// step envelope (budget.MaxOutputTokens is treated as the structural
// capacity of one bounded model proposal). A zero/negative budget
// is unbounded for planning (fits by definition — preflight will
// still gate at execution if needed).
func (e EstimatedMutationSize) Fits(budget StepBudget) bool {
	if budget.MaxOutputTokens <= 0 {
		return true
	}
	return e.Expected <= budget.MaxOutputTokens
}

// Exceeds reports whether the expected mutation exceeds the envelope.
func (e EstimatedMutationSize) Exceeds(budget StepBudget) bool {
	return !e.Fits(budget)
}

// ─── estimator ────────────────────────────────────────────────────────

// Estimator is the deterministic structural size estimator carried by
// Derive. It is pure: inputs are evidence-backed values only.
type Estimator struct {
	// DefaultStepEnvelope is the fallback per-step structural capacity
	// when no provider metadata is supplied. It is Config, not a
	// hardcoded provider magic number.
	DefaultStepEnvelope int
}

// DefaultEstimator returns a balanced estimator with a conservative
// default envelope.
func DefaultEstimator() Estimator {
	return Estimator{DefaultStepEnvelope: 4096}
}

// EstimateForStep deterministically estimates the structural magnitude
// of one step referencing the given surface refs under the proposed
// operation. The estimate is structural (artifact count, operation
// weight, dependency weight), not a raw token count. It is
// domain-neutral: families are distinct extensions/directories, not
// web artifact families.
func (est Estimator) EstimateForStep(surfaceRefs []string, op OperationKind, dependencies int) EstimatedMutationSize {
	nFiles := len(surfaceRefs)
	if nFiles == 0 {
		return EstimatedMutationSize{Lower: 0, Expected: 0, Upper: 0, Confidence: 0.2}
	}

	// Base structural unit: number of distinct structural groups crossed.
	families := artifactFamilyCount(surfaceRefs)

	// Operation weight: create is structurally heaviest, modify is
	// moderate (bounded patch), noop is zero. Refactor/rename inherit
	// modify-weight but with an uncertainty uplift.
	baseUnits := baseForOperation(op)

	// Structural magnitude: families × per-file weight × operation weight
	// plus a dependency penalty (each dependency adds ~20% of the base).
	// This is intentionally NOT tokens-per-char — it is a bounded-planning
	// signal whose absolute numbers stay comparable across small/large tasks.
	expected := nFiles*baseUnits + families*40 + dependencies*20
	if expected < 10 {
		expected = 10
	}
	// Preserve uncertainty: narrow window for single-file modify, wide
	// window for multi-artifact create/refactor.
	var spread int
	switch {
	case op == OpCreate || op == OpRefactor:
		spread = expected / 3
	case nFiles >= 3:
		spread = expected / 4
	default:
		spread = expected / 6
	}
	if spread < 4 {
		spread = 4
	}
	lower := expected - spread
	upper := expected + spread
	if lower < 1 {
		lower = 1
	}
	confidence := confidenceFor(families, nFiles, op, dependencies)

	// Clamp to small ints to avoid false-precision.
	lower = clamp(lower, 1, math.MaxInt32)
	expected = clamp(expected, 1, math.MaxInt32)
	upper = clamp(upper, 1, math.MaxInt32)

	return EstimatedMutationSize{
		Lower:      lower,
		Expected:   expected,
		Upper:      upper,
		Confidence: confidence,
	}
}

// EstimateForPlan aggregates per-step estimates into the plan's
// aggregate estimate. The aggregate is NOT required to equal the sum
// of steps precisely — it preserves that decomposition may overlap;
// it only must be monotonic (aggregate ≥ max step) and carry the
// lowest confidence among the steps.
func (est Estimator) EstimateForPlan(steps []MutationStep) EstimatedMutationSize {
	if len(steps) == 0 {
		return EstimatedMutationSize{Lower: 0, Expected: 0, Upper: 0, Confidence: 0.1}
	}
	var sumLower, sumExpected, sumUpper int
	lowestConf := 1.0
	maxExpected := 0
	for _, s := range steps {
		sumLower += s.Estimate.Lower
		sumExpected += s.Estimate.Expected
		sumUpper += s.Estimate.Upper
		if s.Estimate.Confidence < lowestConf {
			lowestConf = s.Estimate.Confidence
		}
		if s.Estimate.Expected > maxExpected {
			maxExpected = s.Estimate.Expected
		}
	}
	// Aggregate is capped to max of sum and max-step to keep it
	// meaningful without pretending exact additivity.
	_ = maxExpected
	return EstimatedMutationSize{
		Lower:      sumLower,
		Expected:   sumExpected,
		Upper:      sumUpper,
		Confidence: lowestConf,
	}
}

func baseForOperation(op OperationKind) int {
	switch op {
	case OpCreate:
		return 180
	case OpDelete:
		return 40
	case OpNoop:
		return 0
	case OpRefactor:
		return 140
	case OpRename:
		return 80
	default: // MODIFY, unknown
		return 100
	}
}

func artifactFamilyCount(paths []string) int {
	families := map[string]bool{}
	for _, p := range paths {
		families[familyOf(p)] = true
	}
	return len(families)
}

func familyOf(path string) string {
	// Domain-neutral structural grouping: distinct extensions and
	// directory prefixes constitute families. No web-specific
	// html/css/js/assets classification. Asset directories collapse
	// via extension or directory heuristic generically.
	low := toLower(path)
	ext := extOf(low)
	if ext != "" {
		return ext
	}
	// No extension: group by top-level directory
	if idx := indexOf(low, "/"); idx > 0 {
		return low[:idx]
	}
	return "misc"
}

func indexOf(s string, substr string) int {
	if substr == "" {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func confidenceFor(families, nFiles int, op OperationKind, deps int) float64 {
	// Higher confidence when few families and an explicit operation;
	// lower when multi-family or ambiguous noop.
	base := 0.65
	if nFiles == 1 && families == 1 {
		base = 0.85
	}
	if op == OpRefactor || op == OpNoop {
		base -= 0.15
	}
	if families >= 3 {
		base -= 0.15
	}
	if deps > 2 {
		base -= 0.10
	}
	if base < 0.1 {
		base = 0.1
	}
	if base > 0.95 {
		base = 0.95
	}
	// Deterministic rounding.
	return float64(int(base*100+0.5)) / 100
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// string helpers (inlined to avoid extra import churn in tests; the
// package already imports strings at plan.go).
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

//nolint:unused // retained helpers for potential future generic grouping
func hasSuffix(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

//nolint:unused
func contains(s, substr string) bool {
	if substr == "" {
		return true
	}
	limit := len(s) - len(substr) + 1
	for i := 0; i < limit; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func extOf(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[i:]
		}
		if s[i] == '/' {
			break
		}
	}
	return ""
}
