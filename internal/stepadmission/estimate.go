package stepadmission

import (
	"math"
	"strings"
)

// StepSizeEstimate is the domain-neutral decision/response complexity
// estimate for ONE candidate step. It describes the expected model output
// requirement and operational complexity of the CURRENT step, not the total
// task complexity.
//
// It is intentionally distinct from mutationstrategy.EstimatedMutationSize:
//
//	EstimatedMutationSize == structural mutation size (how much workspace change)
//	StepSizeEstimate      == model output requirement for this bounded decision
//
// Example distinction (spec §9):
//
//	"Inspect authentication middleware" may involve many files but require
//	small response → small StepSizeEstimate, large EstimatedMutationSize
//	"Generate complete implementation" may involve small target but large
//	output → large StepSizeEstimate, small EstimatedMutationSize
//
// Therefore the admission model keeps them separate.
type StepSizeEstimate struct {
	Lower      int     `json:"lower"`
	Expected   int     `json:"expected"`
	Upper      int     `json:"upper"`
	Confidence float64 `json:"confidence"`
}

// Valid reports whether the estimate is well-formed.
func (e StepSizeEstimate) Valid() bool {
	return e.Lower >= 0 && e.Expected >= 0 && e.Upper >= 0 &&
		e.Lower <= e.Expected && e.Expected <= e.Upper &&
		e.Confidence >= 0 && e.Confidence <= 1
}

// Fits reports whether the expected requirement fits within the given
// step budget (MaxOutputTokens). Zero budget means unbounded (fits by
// definition — execution preflight still gates later if needed).
func (e StepSizeEstimate) Fits(budget int) bool {
	if budget <= 0 {
		return true
	}
	return e.Expected <= budget
}

// EstimateForCandidate deterministically estimates the decision/response
// complexity of the candidate. Dimensions considered (domain-neutral):
//   - files / artifacts involved (len(Targets))
//   - distinct structural groups (extension/directory families)
//   - evidence references (len(EvidenceRefs))
//   - dependency count (len(DependsOn))
//   - step kind weight (MUTATE heavier than INVESTIGATE)
//
// Only these justified dimensions are included; no fake-precision
// token-per-character model. Uncertainty is preserved via Lower/Upper
// and Confidence rather than a single exact token number.
func EstimateForCandidate(c CandidateStep) StepSizeEstimate {
	nFiles := len(c.Targets)
	if nFiles == 0 {
		// No targets: minimal reasoning step
		return StepSizeEstimate{Lower: 8, Expected: 16, Upper: 24, Confidence: 0.3}
	}
	families := countFamilies(c.Targets)
	evidenceCount := len(c.EvidenceRefs)
	deps := len(c.DependsOn)
	kindWeight := weightForKind(c.Kind)

	base := nFiles*kindWeight + families*24 + evidenceCount*12 + deps*18
	if base < 16 {
		base = 16
	}
	// Spread preserves uncertainty: wider for heavy kinds and many families
	var spread int
	switch {
	case isHeavyKind(c.Kind):
		spread = base / 3
	case nFiles >= 4 || families >= 3:
		spread = base / 4
	default:
		spread = base / 6
	}
	if spread < 6 {
		spread = 6
	}
	lower := base - spread
	upper := base + spread
	if lower < 8 {
		lower = 8
	}
	confidence := confidenceForEstimate(families, nFiles, c.Kind, evidenceCount, deps)

	lower = clampInt(lower, 1, math.MaxInt32)
	base = clampInt(base, 1, math.MaxInt32)
	upper = clampInt(upper, 1, math.MaxInt32)

	return StepSizeEstimate{
		Lower:      lower,
		Expected:   base,
		Upper:      upper,
		Confidence: confidence,
	}
}

// EstimateFromMutationSize converts an existing EstimatedMutationSize into a
// StepSizeEstimate for admission. The conversion is explicit so callers can
// see that the two concepts are distinct but reuse the mutation estimator
// when handling MUTATE steps (spec §16). The mapping is conservative: it
// treats the structural units as output-complexity hints but does NOT equate
// them.
func EstimateFromMutationSize(lower, expected, upper int, confidence float64) StepSizeEstimate {
	// Derive output requirement as roughly the mutation expectation plus a
	// small overhead for reasoning/context framing. The overhead is fixed
	// and documented (spec says do not fake precision).
	const framingOverhead = 24
	exp := expected + framingOverhead
	low := lower + framingOverhead/2
	up := upper + framingOverhead
	if low < 1 {
		low = 1
	}
	if exp < 1 {
		exp = 1
	}
	if up < exp {
		up = exp + 8
	}
	if low > exp {
		low = exp - 4
		if low < 1 {
			low = 1
		}
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return StepSizeEstimate{
		Lower:      low,
		Expected:   exp,
		Upper:      up,
		Confidence: confidence,
	}
}

func weightForKind(kind string) int {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "MUTATE":
		return 110
	case "VERIFY":
		return 70
	case "INVESTIGATE":
		return 55
	case "ANALYZE":
		return 65
	case "EXPERIMENT":
		return 75
	case "OBSERVE":
		return 45
	default:
		return 60
	}
}

func isHeavyKind(kind string) bool {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "MUTATE", "EXPERIMENT":
		return true
	default:
		return false
	}
}

func countFamilies(paths []string) int {
	seen := map[string]bool{}
	for _, p := range paths {
		seen[familyOf(p)] = true
	}
	return len(seen)
}

func familyOf(path string) string {
	low := toLower(path)
	ext := extOf(low)
	if ext != "" {
		return ext
	}
	if idx := indexOf(low, "/"); idx > 0 {
		return low[:idx]
	}
	return "misc"
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

func indexOf(s, substr string) int {
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

func confidenceForEstimate(families, nFiles int, kind string, evidence, deps int) float64 {
	base := 0.70
	if nFiles == 1 && families == 1 {
		base = 0.86
	}
	if isHeavyKind(kind) {
		base -= 0.12
	}
	if families >= 3 {
		base -= 0.12
	}
	if evidence == 0 {
		base -= 0.12
	}
	if deps > 2 {
		base -= 0.08
	}
	if base < 0.12 {
		base = 0.12
	}
	if base > 0.95 {
		base = 0.95
	}
	return float64(int(base*100+0.5)) / 100
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
