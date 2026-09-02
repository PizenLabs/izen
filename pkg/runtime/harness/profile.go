package harness

// PromptStrategy selects the output contract the engine will request from the
// model, driven by observed behavioral telemetry.
type PromptStrategy int

const (
	// StrategyStrictUnifiedDiff asks for a strict unified diff; best when the
	// model is reliable at structured output.
	StrategyStrictUnifiedDiff PromptStrategy = iota
	// StrategySimpleBlocks asks for simple fenced artifact blocks; more
	// forgiving for less structured models.
	StrategySimpleBlocks
	// StrategyFullRewrite asks for a full-file rewrite; the most conservative
	// fallback when structured output reliability is low.
	StrategyFullRewrite
)

// String returns a stable label for a PromptStrategy.
func (s PromptStrategy) String() string {
	switch s {
	case StrategyStrictUnifiedDiff:
		return "strict_unified_diff"
	case StrategySimpleBlocks:
		return "simple_blocks"
	case StrategyFullRewrite:
		return "full_rewrite"
	default:
		return "unknown"
	}
}

// ModelBehaviorProfile tracks observed telemetry about how a model produces
// output, and dynamically selects a PromptStrategy that maximizes the chance
// of a clean, low-inference extraction.
type ModelBehaviorProfile struct {
	// executionCount is the number of mutation attempts observed.
	executionCount int
	// structuredHits is the number of clean structured parses.
	structuredHits int
	// patchApplySuccesses is the number of patch applications that succeeded.
	patchApplySuccesses int
	// truncations is the number of truncated outputs observed.
	truncations int
	// syntaxErrors is the number of syntax-corrupting patches observed.
	syntaxErrors int
}

// NewModelBehaviorProfile returns an empty telemetry profile.
func NewModelBehaviorProfile() *ModelBehaviorProfile {
	return &ModelBehaviorProfile{}
}

// StructuredOutputReliability returns the fraction of attempts that produced a
// clean structured parse, in [0, 1]. Returns 0 when nothing has been observed.
func (p *ModelBehaviorProfile) StructuredOutputReliability() float64 {
	if p.executionCount == 0 {
		return 0
	}
	return float64(p.structuredHits) / float64(p.executionCount)
}

// PatchApplySuccessRate returns the fraction of patches that applied cleanly.
func (p *ModelBehaviorProfile) PatchApplySuccessRate() float64 {
	if p.executionCount == 0 {
		return 0
	}
	return float64(p.patchApplySuccesses) / float64(p.executionCount)
}

// TruncationFrequency returns the fraction of attempts that were truncated.
func (p *ModelBehaviorProfile) TruncationFrequency() float64 {
	if p.executionCount == 0 {
		return 0
	}
	return float64(p.truncations) / float64(p.executionCount)
}

// SyntaxErrorRate returns the fraction of attempts that introduced syntax
// errors.
func (p *ModelBehaviorProfile) SyntaxErrorRate() float64 {
	if p.executionCount == 0 {
		return 0
	}
	return float64(p.syntaxErrors) / float64(p.executionCount)
}

// RecordOutcome ingests a single observed attempt. Exactly one of the numeric
// flags should be set per call.
func (p *ModelBehaviorProfile) RecordOutcome(structured, patchApplied, truncated, syntaxErr bool) {
	p.executionCount++
	if structured {
		p.structuredHits++
	}
	if patchApplied {
		p.patchApplySuccesses++
	}
	if truncated {
		p.truncations++
	}
	if syntaxErr {
		p.syntaxErrors++
	}
}

// ResolveStrategy dynamically selects a PromptStrategy from observed telemetry.
// With fewer than 20 observations it falls back to the conservative
// StrategySimpleBlocks to avoid over-fitting to sparse data.
func (p *ModelBehaviorProfile) ResolveStrategy() PromptStrategy {
	if p.executionCount < 20 {
		return StrategySimpleBlocks
	}

	reliability := p.StructuredOutputReliability()
	truncation := p.TruncationFrequency()
	syntaxErr := p.SyntaxErrorRate()

	// High structured reliability and low failure modes warrant the strict
	// contract, which yields Tier 1 exact parses.
	if reliability >= 0.9 && syntaxErr < 0.1 && truncation < 0.1 {
		return StrategyStrictUnifiedDiff
	}
	// Heavy truncation or syntax corruption degrades into a full rewrite, which
	// is the most conservative and hardest to get wrong.
	if truncation >= 0.3 || syntaxErr >= 0.3 {
		return StrategyFullRewrite
	}
	return StrategySimpleBlocks
}
