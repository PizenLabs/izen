package benchmark

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/providers"
)

// ── Live model backends ─────────────────────────────────────────────────────
//
// A live sweep wires each benchmarked model to its real endpoint. The
// canonical roster is OpenRouter-served; credentials come from the
// environment so CI runs stay offline unless explicitly opted in:
//
//	OPENROUTER_API_KEY   required for live mode
//	OPENROUTER_BASE_URL  optional override (self-hosted gateways)

const (
	openRouterEnvKey     = "OPENROUTER_API_KEY"
	openRouterEnvBaseURL = "OPENROUTER_BASE_URL"
	defaultOpenRouterURL = "https://openrouter.ai/api/v1"
	requestTimeout       = 120 * time.Second
)

// ProviderResponder adapts an ai.Provider onto the Responder contract.
type ProviderResponder struct {
	provider ai.Provider
	model    Model
}

// NewProviderResponder wraps any configured provider backend.
func NewProviderResponder(p ai.Provider, m Model) *ProviderResponder {
	return &ProviderResponder{provider: p, model: m}
}

// Respond performs one synchronous completion with authoritative usage.
func (r *ProviderResponder) Respond(ctx context.Context, req Request) (Response, error) {
	if req.MaxTokens <= 0 {
		req.MaxTokens = defaultBenchMaxOutputTokens
	}
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := r.provider.Execute(callCtx, ai.Request{
		Model:     r.model.ID,
		System:    req.System,
		MaxTokens: req.MaxTokens,
		Messages: []ai.Message{
			{Role: "user", Content: req.Prompt},
		},
	})
	if err != nil {
		return Response{}, fmt.Errorf("provider %s: %w", r.model.ID, err)
	}
	return Response{
		Content:      resp.Content,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

// LiveRespondersFromEnv builds responders for every registered model the
// environment can serve. It returns nil (with a reason) when live mode is
// not configured, so callers degrade to offline sweeps deterministically.
func LiveRespondersFromEnv(models []Model) ([]RegisteredResponder, string) {
	apiKey := os.Getenv(openRouterEnvKey)
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Sprintf("live mode disabled: %s not set", openRouterEnvKey)
	}
	baseURL := os.Getenv(openRouterEnvBaseURL)
	if baseURL == "" {
		baseURL = defaultOpenRouterURL
	}
	var out []RegisteredResponder
	for _, m := range models {
		switch m.Provider {
		case "openrouter":
			p := providers.NewOpenRouterProvider(apiKey, m.ID, baseURL)
			out = append(out, RegisteredResponder{Model: m, Responder: NewProviderResponder(p, m)})
		default:
			return nil, fmt.Sprintf("no live transport for provider %q (model %s)", m.Provider, m.ID)
		}
	}
	return out, ""
}

// RegisteredResponder pairs one model with its wired backend.
type RegisteredResponder struct {
	Model     Model
	Responder Responder
}
