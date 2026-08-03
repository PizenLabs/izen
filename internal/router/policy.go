package router

// ConfidencePolicy controls when a classification is considered ambiguous and
// requires UI-level disambiguation. The threshold is exposed via runtime
// configuration rather than hardcoded so deployments can tune aggressiveness.
type ConfidencePolicy struct {
	// Threshold is the minimum confidence below which ConfirmationRequirement
	// is set. Must be in [0.0, 1.0]; values outside the range are clamped.
	Threshold float64
}

// DefaultConfidencePolicy returns the default policy used when no runtime
// configuration overrides the threshold.
func DefaultConfidencePolicy() ConfidencePolicy {
	return ConfidencePolicy{Threshold: 0.6}
}

// Apply projects the policy onto a classification result: when the normalized
// confidence falls below the threshold the result is flagged as requiring
// confirmation so the UI can disambiguate instead of making a blind guess.
func (p ConfidencePolicy) Apply(r ClassificationResult) ClassificationResult {
	threshold := p.Threshold
	if threshold < 0.0 {
		threshold = 0.0
	}
	if threshold > 1.0 {
		threshold = 1.0
	}
	if r.Confidence < threshold {
		r.ConfirmationRequirement = true
	}
	return r
}
