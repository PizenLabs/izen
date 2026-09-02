package capability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Adapter inspects a provider's live capability surface and translates it into
// ModelCapabilities records. Implementations are expected to be safe for
// concurrent use.
type Adapter interface {
	// Inspect returns the provider's current model capabilities. An empty
	// result with a nil error means the provider reported no models.
	Inspect(ctx context.Context) ([]ModelCapabilities, error)
}

// openRouterEndpoint is the OpenRouter models catalog endpoint.
const openRouterEndpoint = "https://openrouter.ai/api/v1/models"

// OpenRouterAdapter inspects the OpenRouter model catalog. Capabilities are
// parsed from the provider-advertised fields: reasoning is derived from
// supported_parameters, the context window from context_length, and the
// maximum output budget from top_provider.max_completion_tokens.
type OpenRouterAdapter struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// NewOpenRouterAdapter returns an adapter hitting the live OpenRouter catalog.
func NewOpenRouterAdapter(apiKey string) *OpenRouterAdapter {
	return &OpenRouterAdapter{
		apiKey:   apiKey,
		endpoint: openRouterEndpoint,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// NewOpenRouterAdapterWithEndpoint returns an adapter hitting a custom catalog
// endpoint with the given client. It is the seam tests and self-hosted
// deployments use to avoid real network access.
func NewOpenRouterAdapterWithEndpoint(apiKey, endpoint string, client *http.Client) *OpenRouterAdapter {
	if endpoint == "" {
		endpoint = openRouterEndpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &OpenRouterAdapter{apiKey: apiKey, endpoint: endpoint, client: client}
}

// openRouterResponse is the OpenRouter /models response envelope.
type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// openRouterModel is one entry of the OpenRouter model catalog.
type openRouterModel struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name,omitempty"`
	ContextLength       int      `json:"context_length,omitempty"`
	SupportedParameters []string `json:"supported_parameters,omitempty"`
	TopProvider         *struct {
		ContextLength       int  `json:"context_length,omitempty"`
		MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	} `json:"top_provider,omitempty"`
}

// Inspect implements Adapter.
func (a *OpenRouterAdapter) Inspect(ctx context.Context) ([]ModelCapabilities, error) {
	if a == nil || a.endpoint == "" {
		return nil, errAdapterNil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
	req.Header.Set("X-OpenRouter-Title", "izen")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result openRouterResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	models := make([]ModelCapabilities, 0, len(result.Data))
	for _, m := range result.Data {
		reasoning := HasReasoningParameter(m.SupportedParameters)
		if !reasoning {
			reasoning = SupportsEffortWithProvider("openrouter", m.ID)
		}
		ctxWin := m.ContextLength
		if ctxWin == 0 && m.TopProvider != nil && m.TopProvider.ContextLength != 0 {
			ctxWin = m.TopProvider.ContextLength
		}
		maxOut := 0
		if m.TopProvider != nil && m.TopProvider.MaxCompletionTokens != nil {
			maxOut = *m.TopProvider.MaxCompletionTokens
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, ModelCapabilities{
			Provider:          "openrouter",
			ModelID:           m.ID,
			Name:              name,
			SupportsReasoning: reasoning,
			ContextWindow:     ctxWin,
			MaxOutputTokens:   maxOut,
		}.Normalize())
	}
	return models, nil
}

// defaultOllamaBaseURL is the local Ollama server address.
const defaultOllamaBaseURL = "http://localhost:11434"

// OllamaAdapter inspects the local Ollama server's installed model list. Ollama
// does not advertise reasoning parameters, so reasoning support and effort
// levels are derived from model-family heuristics (deepseek-r1, qwen3-r1, ...).
type OllamaAdapter struct {
	baseURL string
	client  *http.Client
}

// NewOllamaAdapter returns an adapter hitting the default local Ollama server.
func NewOllamaAdapter() *OllamaAdapter {
	return &OllamaAdapter{
		baseURL: defaultOllamaBaseURL,
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

// NewOllamaAdapterWithBaseURL returns an adapter hitting a custom Ollama base
// URL with the given client. Tests and remote Ollama hosts use this seam.
func NewOllamaAdapterWithBaseURL(baseURL string, client *http.Client) *OllamaAdapter {
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	return &OllamaAdapter{baseURL: baseURL, client: client}
}

// ollamaTagsResponse is the /api/tags response envelope.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// Inspect implements Adapter.
func (a *OllamaAdapter) Inspect(ctx context.Context) ([]ModelCapabilities, error) {
	if a == nil || a.baseURL == "" {
		return nil, errAdapterNil
	}
	endpoint := strings.TrimSuffix(a.baseURL, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result ollamaTagsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	models := make([]ModelCapabilities, 0, len(result.Models))
	for _, m := range result.Models {
		reasoning := SupportsEffortWithProvider("ollama", m.Name)
		models = append(models, ModelCapabilities{
			Provider:          "ollama",
			ModelID:           m.Name,
			Name:              m.Name,
			SupportsReasoning: reasoning,
		}.Normalize())
	}
	return models, nil
}
