package router

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/events"
)

// ErrNoClassifier is returned when the Router is configured without an
// IntentClassifier and a prompt falls through the deterministic fast path.
var ErrNoClassifier = fmt.Errorf("router: no semantic classifier configured")

// Router is the Hybrid Intent Gateway. It runs the deterministic fast path
// FIRST — never invoking semantic classification — and only falls back to the
// injected IntentClassifier when no deterministic signal matches. It depends
// solely on the abstract IntentClassifier interface and the event bus; it has
// no knowledge of any concrete LLM provider.
//
// Router is safe for concurrent use: its configuration is immutable after
// construction and the event bus is itself thread-safe.
type Router struct {
	classifier IntentClassifier
	policy     ConfidencePolicy
	bus        *events.Bus
}

// NewRouter builds a Hybrid Intent Gateway around the given semantic
// classifier and confidence policy. A nil policy falls back to the default.
func NewRouter(classifier IntentClassifier, policy *ConfidencePolicy) *Router {
	p := DefaultConfidencePolicy()
	if policy != nil {
		p = *policy
	}
	return &Router{
		classifier: classifier,
		policy:     p,
	}
}

// WithEventBus wires the event bus so classification milestones are published
// (IntentClassified, ApprovalRequested). Nil disables emission. It returns the
// Router for chaining.
func (r *Router) WithEventBus(bus *events.Bus) *Router {
	if r != nil {
		r.bus = bus
	}
	return r
}

// Route classifies a raw prompt into a canonical execution phase. It runs the
// deterministic fast path first; when the fast path matches, the classifier is
// NEVER invoked and the result carries full confidence. Otherwise the prompt is
// delegated to the semantic classifier and the confidence policy is applied.
//
// When the classification is ambiguous (ConfirmationRequirement), an
// ApprovalRequested event is published so the UI can disambiguate instead of
// acting on a blind guess.
func (r *Router) Route(ctx context.Context, input string) (ClassificationResult, error) {
	if r == nil {
		return ClassificationResult{}, ErrNoClassifier
	}

	if fp, ok := fastPath(input); ok {
		res := ClassificationResult{
			Intent:      fp.intent,
			Confidence:  fp.confidence,
			Explanation: fp.explanation,
			Language:    detectLanguage(input),
		}
		r.emitClassified(res, input)
		return res, nil
	}

	if r.classifier == nil {
		return ClassificationResult{}, ErrNoClassifier
	}

	res, err := r.classifier.Classify(ctx, input)
	if err != nil {
		return ClassificationResult{}, fmt.Errorf("router: semantic classification failed: %w", err)
	}
	res = r.policy.Apply(res)
	r.emitClassified(res, input)

	if res.ConfirmationRequirement {
		r.emitApproval(res, input)
	}

	return res, nil
}

// emitClassified publishes the IntentClassified event when a bus is wired.
func (r *Router) emitClassified(res ClassificationResult, input string) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(events.NewIntentClassified(
		string(res.Intent),
		input,
		res.Confidence,
		res.Language,
		res.Explanation,
		res.ConfirmationRequirement,
	))
}

// emitApproval publishes an ApprovalRequested event for ambiguous
// classifications so the UI can present a disambiguation prompt.
func (r *Router) emitApproval(res ClassificationResult, input string) {
	if r.bus == nil {
		return
	}
	reason := fmt.Sprintf("ambiguous intent (confidence %.2f below threshold): %s",
		res.Confidence, input)
	r.bus.Publish(events.NewApprovalRequested("", reason, ""))
}

// detectLanguage performs lightweight, deterministic locale detection based on
// Unicode script blocks. It is intentionally keyword-free and language-agnostic:
// the detected script names a writing system, never a specific language. Returns
// "latin" for Latin-script input, "cjk" for CJK, "cyrillic" for Cyrillic, and
// "unknown" otherwise. This function is allocation-free on the fast path.
func detectLanguage(input string) string {
	if input == "" {
		return "unknown"
	}
	sawLatin := false
	for _, r := range input {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
			r >= 0x3040 && r <= 0x30FF, // Hiragana + Katakana
			r >= 0xAC00 && r <= 0xD7AF: // Hangul
			return "cjk"
		case r >= 0x0400 && r <= 0x04FF, // Cyrillic
			r >= 0x0500 && r <= 0x052F:
			return "cyrillic"
		case r >= 0x0041 && r <= 0x007A, // Basic Latin letters
			r >= 0x00C0 && r <= 0x024F: // Latin-1 + Extended
			sawLatin = true
		}
	}
	if sawLatin {
		return "latin"
	}
	return "unknown"
}
