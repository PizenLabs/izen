// Package router implements the Hybrid Intent Gateway: a deterministic fast
// path that runs before any semantic classification, delegating to a
// provider-agnostic IntentClassifier for everything else.
//
// The gateway depends ONLY on the abstract IntentClassifier interface. It
// never imports a concrete LLM provider, SDK, or HTTP client. Confidence is a
// normalized 0.0–1.0 score; when it falls below the configured threshold the
// result's ConfirmationRequirement is set so the UI can disambiguate instead
// of guessing.
package router

import "context"

// Intent is the canonical execution phase a prompt is projected onto.
type Intent string

const (
	IntentAsk         Intent = "ask"
	IntentInvestigate Intent = "investigate"
	IntentPlan        Intent = "plan"
	IntentBuild       Intent = "build"
	IntentReview      Intent = "review"
	IntentUnknown     Intent = ""
)

// ClassificationResult is the provider-agnostic outcome of a classification.
// It carries the projected execution phase, a normalized confidence in
// [0.0, 1.0], the detected locale, the justification, and whether ambiguity
// requires UI-level confirmation.
type ClassificationResult struct {
	Intent     Intent
	Confidence float64
	Language   string
	// Explanation is the human-readable justification for the classification.
	Explanation string
	// ConfirmationRequirement indicates the ambiguity threshold was met and a
	// user confirmation must be requested before the intent is acted on.
	ConfirmationRequirement bool
}

// IntentClassifier is the abstract contract the Hybrid Intent Gateway depends
// on. It is strictly language-agnostic: it projects the prompt's underlying
// intent into the canonical execution phase via semantic understanding, not
// language-specific keyword dictionaries or regex filters. Implementations
// MUST accept a context and respond promptly to cancellation.
type IntentClassifier interface {
	Classify(ctx context.Context, input string) (ClassificationResult, error)
}

// ClassifyFunc is a function adapter for IntentClassifier. It allows a plain
// function to satisfy the interface without importing any provider.
type ClassifyFunc func(ctx context.Context, input string) (ClassificationResult, error)

// Classify adapts a ClassifyFunc into an IntentClassifier.
func (f ClassifyFunc) Classify(ctx context.Context, input string) (ClassificationResult, error) {
	return f(ctx, input)
}
