package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/audit"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/provider/detector"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// LLMResponse is the agent-facing LLM result.
type LLMResponse = llm.LLMResponse

// ProviderError classifies a provider transport/API failure by HTTP status.
type ProviderError struct {
	StatusCode int
	Message    string
}

// Error implements error.
func (e *ProviderError) Error() string {
	if e == nil {
		return "agent: nil provider error"
	}
	return fmt.Sprintf("agent: provider error %d: %s", e.StatusCode, e.Message)
}

// IsRetryable reports whether err is a transient provider failure (429 or
// 5xx) that the caller may retry or recover from a checkpoint.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var perr *ProviderError
	if as := asProviderError(err); as != nil {
		perr = as
	} else {
		// Fallback: match status codes embedded in generic errors.
		msg := err.Error()
		for _, code := range []string{"429", "500", "502", "503", "504"} {
			if strings.Contains(msg, code) {
				return true
			}
		}
		return false
	}
	return perr.StatusCode == http.StatusTooManyRequests ||
		(perr.StatusCode >= 500 && perr.StatusCode <= 599)
}

func asProviderError(err error) *ProviderError {
	var perr *ProviderError
	if errors.As(err, &perr) {
		return perr
	}
	return nil
}

// ClientFactory builds an LLM provider client for the given provider
// credentials.
type ClientFactory func(provider, apiKey, baseURL string) (llm.LLMProvider, error)

// Dispatcher routes role-tagged prompts to the model bound to that role.
type Dispatcher struct {
	WorkDir       string
	SessionID     string
	Cfg           *config.CascadeConfig
	Reg           *registry.Registry
	Providers     []detector.ProviderConfig
	Factory       ClientFactory
	SystemPrompts map[string]string
	TokenLimits   map[string]int
}

// defaultSystemPrompts are the fallback system prompts per role.
func defaultSystemPrompt(roleName string) string {
	switch roleName {
	case role.RolePlan:
		return "You are the planning specialist. Produce a concise, ordered implementation plan."
	case role.RoleSmol:
		return "You are the summarizer. Reply with a single short git commit message line."
	case role.RoleVision:
		return "You are the vision specialist. Describe visual content precisely."
	case role.RoleAdviser:
		return "You are the review adviser. Give security and correctness guidance."
	default:
		return "You are the execution specialist. Produce precise technical output."
	}
}

// DispatchForRole resolves the target model via role.ResolveRoleModel,
// selects credentials via the detector/cascade provider list, builds the
// provider client, logs the dispatch to .izen/audit/events.ndjson, and
// executes the call.
func (d *Dispatcher) DispatchForRole(ctx context.Context, roleName, prompt string) (*LLMResponse, error) {
	if d == nil {
		return nil, fmt.Errorf("agent: nil dispatcher")
	}
	if ctx == nil {
		return nil, fmt.Errorf("agent: nil context")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("agent: empty prompt")
	}
	// ResolveRoleModel validates the role name (unknown roles error).
	desc, err := role.ResolveRoleModel(roleName, d.Cfg, d.Reg)
	if err != nil {
		return nil, fmt.Errorf("agent: resolve role %q: %w", roleName, err)
	}
	provider, bare := NormalizeModelID(desc.ID)
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(desc.Provider))
	} else {
		provider = strings.ToLower(provider)
	}
	modelID := desc.ID
	if bare != "" && provider != "" {
		// Providers expect the bare model id (OpenRouter-style full ids are
		// handled by keeping the full id when the provider prefix is the
		// routing vendor itself).
		if provider == "openrouter" {
			modelID = desc.ID
		} else {
			modelID = bare
		}
	}

	cred, ok := lookupCredential(d.Providers, provider, desc.Provider)
	if !ok {
		return nil, fmt.Errorf("agent: no credentials for provider %q", provider)
	}

	factory := d.Factory
	if factory == nil {
		factory = DefaultClientFactory
	}
	client, err := factory(cred.Name, cred.APIKey, cred.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("agent: build client for %q: %w", cred.Name, err)
	}

	// Dispatch audit event (best-effort: never fail the call on audit I/O).
	if d.WorkDir != "" {
		alog := audit.NewLogger(d.WorkDir)
		_ = alog.LogEvent(d.SessionID, audit.EventRoleDispatched, map[string]any{
			"role": roleName, "model": desc.ID, "provider": cred.Name,
		})
	}

	system := defaultSystemPrompt(roleName)
	if d.SystemPrompts != nil {
		if s, ok := d.SystemPrompts[roleName]; ok && strings.TrimSpace(s) != "" {
			system = s
		}
	}
	maxTokens := 2048
	if d.TokenLimits != nil {
		if n, ok := d.TokenLimits[roleName]; ok && n > 0 {
			maxTokens = n
		}
	}

	req := llm.PromptRequest{
		Model:       modelID,
		System:      system,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:   maxTokens,
		Temperature: 0.2,
	}
	resp, err := client.GenerateResponse(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("agent: role %q model %q: %w", roleName, desc.ID, err)
	}
	if d.WorkDir != "" {
		alog := audit.NewLogger(d.WorkDir)
		_ = alog.LogEvent(d.SessionID, audit.EventLLMRequest, map[string]any{
			"role": roleName, "model": desc.ID,
			"input_tokens": resp.TokenInput, "output_tokens": resp.TokenOutput,
		})
	}
	out := resp
	return &out, nil
}

// lookupCredential finds credentials by provider name (case-insensitive),
// falling back to the descriptor provider.
func lookupCredential(provs []detector.ProviderConfig, names ...string) (detector.ProviderConfig, bool) {
	for _, want := range names {
		want = strings.ToLower(strings.TrimSpace(want))
		if want == "" {
			continue
		}
		for _, p := range provs {
			if strings.ToLower(strings.TrimSpace(p.Name)) == want {
				return p, true
			}
		}
	}
	return detector.ProviderConfig{}, false
}

// DefaultClientFactory builds an HTTP LLM client: Anthropic uses its native
// messages API; every other known provider uses an OpenAI-compatible
// chat/completions gateway (OpenAI, Groq, OpenRouter, Gemini's
// .../v1beta/openai endpoint, Ollama).
func DefaultClientFactory(provider, apiKey, baseURL string) (llm.LLMProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("agent: missing api key for provider %q", provider)
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = detector.BaseURLFor(strings.ToLower(provider))
	}
	if baseURL == "" {
		return nil, fmt.Errorf("agent: unknown base URL for provider %q", provider)
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}
	if strings.EqualFold(provider, "anthropic") {
		return &anthropicClient{apiKey: apiKey, baseURL: baseURL, client: httpClient}, nil
	}
	return &openAICompatClient{provider: provider, apiKey: apiKey, baseURL: baseURL, client: httpClient}, nil
}

// ── OpenAI-compatible client ─────────────────────────────────────────────

type openAICompatClient struct {
	provider string
	apiKey   string
	baseURL  string
	client   *http.Client
}

func (c *openAICompatClient) Name() string { return c.provider }

func (c *openAICompatClient) GenerateResponse(ctx context.Context, req llm.PromptRequest) (llm.LLMResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       req.Model,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": firstUserContent(req)},
		},
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.LLMResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return llm.LLMResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return llm.LLMResponse{}, &ProviderError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.LLMResponse{}, fmt.Errorf("agent: provider %s status %d: %s", c.provider, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return llm.LLMResponse{}, fmt.Errorf("agent: decode provider response: %w", err)
	}
	out := llm.LLMResponse{}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
	}
	if parsed.Usage != nil {
		out.TokenInput = parsed.Usage.PromptTokens
		out.TokenOutput = parsed.Usage.CompletionTokens
	}
	return out, nil
}

func (c *openAICompatClient) StreamResponse(ctx context.Context, req llm.PromptRequest, _ llm.StreamHandler) (llm.LLMResponse, error) {
	return c.GenerateResponse(ctx, req)
}

// ── Anthropic native client ──────────────────────────────────────────────

type anthropicClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func (c *anthropicClient) Name() string { return "anthropic" }

func (c *anthropicClient) GenerateResponse(ctx context.Context, req llm.PromptRequest) (llm.LLMResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	body, _ := json.Marshal(map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"system":     req.System,
		"messages": []map[string]string{
			{"role": "user", "content": firstUserContent(req)},
		},
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return llm.LLMResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return llm.LLMResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return llm.LLMResponse{}, &ProviderError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.LLMResponse{}, fmt.Errorf("agent: anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return llm.LLMResponse{}, fmt.Errorf("agent: decode anthropic response: %w", err)
	}
	var sb strings.Builder
	for _, b := range parsed.Content {
		sb.WriteString(b.Text)
	}
	out := llm.LLMResponse{Content: sb.String()}
	if parsed.Usage != nil {
		out.TokenInput = parsed.Usage.InputTokens
		out.TokenOutput = parsed.Usage.OutputTokens
	}
	return out, nil
}

func (c *anthropicClient) StreamResponse(ctx context.Context, req llm.PromptRequest, _ llm.StreamHandler) (llm.LLMResponse, error) {
	return c.GenerateResponse(ctx, req)
}

func firstUserContent(req llm.PromptRequest) string {
	for _, m := range req.Messages {
		if m.Role == "user" && m.Content != "" {
			return m.Content
		}
	}
	if len(req.Messages) > 0 {
		return req.Messages[0].Content
	}
	return ""
}
