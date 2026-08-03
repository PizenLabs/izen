package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LLMClassifyFunc is the provider-agnostic seam through which
// PromptIntentClassifier reaches an LLM. It returns the raw completion text for
// the given system prompt and user input. The composition root adapts an
// ai.Provider (or any HTTP client) to this function, so the router package
// itself never imports a concrete provider.
type LLMClassifyFunc func(ctx context.Context, systemPrompt, userInput string) (string, error)

// PromptIntentClassifier is a provider-agnostic, language-agnostic semantic
// classifier. It instructs the underlying model to classify intent by semantic
// understanding — projecting any natural-language input onto the canonical
// execution phase — and parses the structured response. It contains no
// language-specific keyword dictionaries or regex filters.
type PromptIntentClassifier struct {
	llm LLMClassifyFunc
}

// NewPromptIntentClassifier wraps an LLM classify function into a semantic
// IntentClassifier.
func NewPromptIntentClassifier(llm LLMClassifyFunc) *PromptIntentClassifier {
	return &PromptIntentClassifier{llm: llm}
}

// intentSystemPrompt instructs the model to classify intent purely by semantic
// understanding in any language, returning a strict JSON envelope.
const intentSystemPrompt = `You are an intent classifier for a coding assistant. Given a user request in ANY natural language, project its underlying intent onto exactly one canonical execution phase and return strict JSON only:

{"intent":"ask|investigate|plan|build|review","confidence":0.0..1.0,"language":"<detected locale>","explanation":"<one-line justification>"}

Semantic guidance:
- ask: request for explanation, inspection, or read-only understanding.
- investigate: debugging, bug hunting, crash/failure root-cause analysis.
- plan: architecture, design, frontend UI/layout/styling, refactor design, multi-file analysis before changes.
- build: concrete implementation/execution of a defined change.
- review: auditing existing changes for risk/regression.

Base the classification on meaning, not surface keywords. Respond with the JSON object only.`

// Classify performs semantic classification via the injected LLM function and
// parses the structured response. The result's language is taken from the
// model response when present, otherwise recovered via deterministic script
// detection. It is cancellable through ctx.
func (c *PromptIntentClassifier) Classify(ctx context.Context, input string) (ClassificationResult, error) {
	if c == nil || c.llm == nil {
		return ClassificationResult{Intent: IntentUnknown}, ErrNoClassifier
	}
	text, err := c.llm(ctx, intentSystemPrompt, input)
	if err != nil {
		return ClassificationResult{Intent: IntentUnknown}, fmt.Errorf("router: classifier call failed: %w", err)
	}
	res, err := parseClassification(text)
	if err != nil {
		return ClassificationResult{Intent: IntentUnknown}, fmt.Errorf("router: parse classification: %w", err)
	}
	if res.Language == "" {
		res.Language = detectLanguage(input)
	}
	return res, nil
}

// rawClassification mirrors the model's JSON envelope.
type rawClassification struct {
	Intent      string  `json:"intent"`
	Confidence  float64 `json:"confidence"`
	Language    string  `json:"language"`
	Explanation string  `json:"explanation"`
}

// parseClassification parses a model response into a ClassificationResult. It
// tolerates a leading code fence and trailing prose, and normalizes the intent
// string onto the canonical Intent enum. Confidence is clamped to [0.0, 1.0].
func parseClassification(text string) (ClassificationResult, error) {
	cleaned := strings.TrimSpace(text)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	if idx := strings.Index(cleaned, "{"); idx >= 0 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx >= 0 {
		cleaned = cleaned[:idx+1]
	}

	var raw rawClassification
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return ClassificationResult{}, err
	}

	intent := Intent(raw.Intent)
	switch intent {
	case IntentAsk, IntentInvestigate, IntentPlan, IntentBuild, IntentReview:
	default:
		intent = IntentUnknown
	}

	confidence := raw.Confidence
	if confidence < 0.0 {
		confidence = 0.0
	}
	if confidence > 1.0 {
		confidence = 1.0
	}

	return ClassificationResult{
		Intent:      intent,
		Confidence:  confidence,
		Language:    raw.Language,
		Explanation: raw.Explanation,
	}, nil
}
