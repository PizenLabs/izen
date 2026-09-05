package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/llm"
)

// ErrOpenRouterAuth is returned when OpenRouter authentication fails (HTTP 401
// or missing API key). The UI layer detects this sentinel error via errors.Is
// and displays a clear actionable banner instead of a raw HTTP status message.
var ErrOpenRouterAuth = errors.New("openrouter: authorization failed (HTTP 401): invalid or missing OPENROUTER_API_KEY — check your environment variables or run: export OPENROUTER_API_KEY=<your_key>")

// DefaultOpenRouterModel is the safe fallback model ID used when a request
// carries no model resolvable to OpenRouter's vendor/model schema.
const DefaultOpenRouterModel = "anthropic/claude-3.5-sonnet"

// openRouterMaxRateLimitRetries bounds how many times a request answered with
// HTTP 429 (Too Many Requests / rate limit) is retried before the error is
// surfaced to the caller. Each retry waits longer (exponential backoff), so the
// total backoff window across all retries stays small while giving a
// rate-limited free-tier model room to recover.
const openRouterMaxRateLimitRetries = 3

// openRouterRateLimitBackoffBase is the base delay unit for exponential backoff
// on HTTP 429 responses: retries wait 1s, 2s, 4s. It is a package-level variable
// (not a const) so tests can shrink it and keep the suite fast.
var openRouterRateLimitBackoffBase = time.Second

// openRouterModelIDRe matches OpenRouter's vendor/model-id schema: a non-empty
// vendor component, a single "/", and a non-empty model component. Vendors
// carry hyphens and digits (meta-llama, gpt-4o) and models may carry
// ":free"-style variants. An ID without a vendor prefix (e.g. Ollama's
// "qwen2.5-coder:7b") is rejected by the API with HTTP 400 "not a valid model
// ID" and must be mapped before dispatch.
var openRouterModelIDRe = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// SanitizeModelForOpenRouter maps a model ID onto a valid OpenRouter model ID.
// Local/Ollama IDs (e.g. "qwen2.5-coder:7b") carry no vendor prefix and are
// rejected by OpenRouter with status 400; they are remapped to the provider's
// default model. Returns "" only when neither the requested nor the fallback
// model is valid for OpenRouter.
func SanitizeModelForOpenRouter(model, fallback string) string {
	for _, candidate := range []string{model, fallback} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && openRouterModelIDRe.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

// resolveModel returns the effective model ID for a request, remapping any
// local/non-OpenRouter ID onto the provider default so the API never rejects
// the payload with HTTP 400. Returns an error when no valid ID is available.
func (p *OpenRouterProvider) resolveModel(reqModel string) (string, error) {
	model := p.model
	if reqModel != "" {
		model = reqModel
	}
	if model = SanitizeModelForOpenRouter(model, p.model); model == "" {
		return "", fmt.Errorf("openrouter: no valid model ID configured (got %q)", p.model)
	}
	return model, nil
}

// ReasoningSentinel is a zero-width marker embedded in the stream output to
// distinguish reasoning content from message content. The UI layer detects
// these markers and routes reasoning into a separate collapsible buffer.
const ReasoningSentinel = "\x00RSNG\x00"

// ToolCallSentinel is a zero-width marker embedded in the stream output to
// distinguish tool call delta JSON from message content. The UI layer detects
// these markers and routes them into the ToolCallBuffer for live code preview.
const ToolCallSentinel = "\x00TCLL\x00"

type OpenRouterProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenRouterProvider(apiKey, model, baseURL string) *OpenRouterProvider {
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return &OpenRouterProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

func (p *OpenRouterProvider) Name() string {
	return "openrouter"
}

// resolveAPIKey returns the effective API key for a request. It checks the
// OPENROUTER_API_KEY environment variable first (picking up runtime .env
// changes), then falls back to the compile-time key from the provider config.
func (p *OpenRouterProvider) resolveAPIKey() string {
	if envKey := os.Getenv("OPENROUTER_API_KEY"); envKey != "" {
		return envKey
	}
	return p.apiKey
}

func (p *OpenRouterProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, false)

	resp, stats, err := p.doChatRequest(ctx, key, body, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: server returned 401: %s", ErrOpenRouterAuth, strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openrouter: status %d: %s", resp.StatusCode, string(respBody))
	}

	var openaiResp openrouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("openrouter: decode: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: no choices")
	}

	content := ""
	var toolCalls []ai.ToolCall
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		content = firstUsableContent(msg.Content, msg.Reasoning, msg.ReasoningContent)
		if openaiResp.Choices[0].FinishReason == "tool_calls" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				toolCalls = append(toolCalls, ai.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: ai.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
	}

	tokenIn := 0
	tokenOut := 0
	var usage ai.ProviderUsage
	usage.RequestStartedAt = time.Now()
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
		usage = openaiResp.Usage.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = openaiResp.Choices[0].FinishReason
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}
	usage.HTTPAttempts = stats.attempts
	usage.RateLimitedRetries = stats.rateLimitedRetries

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		ToolCalls:   toolCalls,
		Usage:       usage,
	}, nil
}

func (p *OpenRouterProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, true)

	resp, stats, err := p.doChatRequest(ctx, key, body, true)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: server returned 401: %s", ErrOpenRouterAuth, strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openrouter: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &openrouterSSEReader{body: resp.Body}
	sr.usage.markRequestStarted(time.Now())
	sr.usage.recordTransport(stats.attempts, stats.rateLimitedRetries)
	return &OpenRouterStreamResult{ReadCloser: sr, sr: sr}, nil
}

func (p *OpenRouterProvider) buildMessages(req ai.Request) []openrouterMessage {
	msgs := make([]openrouterMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openrouterMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, openrouterMessage{Role: m.Role, Content: content})
	}
	return cleanMessages(msgs)
}

// cleanMessages validates and sanitizes a message sequence before it is
// sent to the OpenRouter API. It prevents HTTP 400 responses caused by
// empty content, consecutive same-role messages, or structural violations.
// Rules applied in order:
//  1. Drop messages with empty content (after sanitization).
//  2. Merge consecutive messages with the same role by joining with "\n".
//  3. Strip leading assistant messages (no response without a prior user prompt).
//  4. Ensure the final message is not a system message (remove trailing system messages).
//  5. Sliding window: keep at most the last 30 messages to prevent unbounded
//     token growth across long sessions.
func cleanMessages(msgs []openrouterMessage) []openrouterMessage {
	// Step 1: drop empty content.
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.Content != "" {
			filtered = append(filtered, m)
		}
	}
	msgs = filtered

	// Step 2: merge consecutive same-role messages.
	merged := make([]openrouterMessage, 0, len(msgs))
	for i, m := range msgs {
		if i > 0 && m.Role == msgs[i-1].Role && m.Role != "system" {
			last := &merged[len(merged)-1]
			last.Content += "\n" + m.Content
			continue
		}
		merged = append(merged, m)
	}
	msgs = merged

	// Step 3: strip leading assistant messages.
	for len(msgs) > 0 && msgs[0].Role == "assistant" {
		msgs = msgs[1:]
	}

	// Step 4: strip trailing system messages, but preserve at least one
	// message (the head system prompt at index 0) so the IZEN identity and
	// user name context is never dropped.
	for len(msgs) > 1 && msgs[len(msgs)-1].Role == "system" {
		msgs = msgs[:len(msgs)-1]
	}

	// Step 5: sliding window truncation.
	// Keep system + user + assistant messages bounded so the payload never
	// explodes across long sessions. Preserve system message at index 0.
	const maxMessages = 30
	if len(msgs) > maxMessages {
		head := 1
		tail := msgs[head:]
		if len(tail) > maxMessages {
			tail = tail[len(tail)-maxMessages:]
		}
		msgs = append(msgs[:head], tail...)
	}

	return msgs
}

type openrouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openrouterRequest struct {
	Model               string              `json:"model"`
	Messages            []openrouterMessage `json:"messages"`
	MaxTokens           int                 `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens,omitempty"`
	Temperature         float64             `json:"temperature,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
	Stream              bool                `json:"stream"`
	StreamOptions       *streamOptions      `json:"stream_options,omitempty"`
	Tools               []json.RawMessage   `json:"tools,omitempty"`
	// Reasoning carries OpenRouter's provider-agnostic reasoning control. It
	// is injected from the dynamically resolved effort directive; a nil value
	// omits the field entirely.
	Reasoning *openrouterReasoning `json:"reasoning,omitempty"`
}

// openrouterReasoning is OpenRouter's reasoning control payload: an optional
// qualitative effort (low/medium/high), an optional max_tokens reasoning cap,
// and an optional on/off switch. OpenRouter relays whichever field is set to
// the underlying provider's native mechanism.
type openrouterReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// reasoningFor builds the OpenRouter reasoning payload from the resolved
// effort directive. The qualitative effort maps to reasoning.effort; the CoT
// cap and budget map to reasoning.max_tokens. Disabled maps to
// reasoning.enabled=false — the only control reliably honored by models whose
// gateways ignore the CoT cap (they otherwise spend the whole output budget in
// the hidden reasoning channel). A nil request reasoning config yields nil
// (field omitted, the pre-existing behavior).
func reasoningFor(req ai.Request) *openrouterReasoning {
	if req.Reasoning == nil {
		return nil
	}
	if req.Reasoning.Disabled {
		enabled := false
		return &openrouterReasoning{Enabled: &enabled}
	}
	r := &openrouterReasoning{Effort: req.Reasoning.Level}
	switch {
	case req.Reasoning.CoTLimit > 0:
		r.MaxTokens = req.Reasoning.CoTLimit
	case req.Reasoning.BudgetTokens > 0:
		r.MaxTokens = req.Reasoning.BudgetTokens
	}
	if r.Effort == "" && r.MaxTokens == 0 {
		return nil
	}
	return r
}

// openRouterModelSupportsReasoning reports whether the target model accepts
// OpenRouter's reasoning control. It delegates to the strict explicit
// whitelist in llm.ModelSupportsEffortWithProvider — only verified
// reasoning families (openai/o1*, openai/o3*, anthropic/claude-3-7-sonnet*,
// deepseek/deepseek-r1*) return true. All other models (qwen2.5,
// aion-labs/aion-3.0, gemma, etc.) return false so the reasoning payload is
// never injected and the gateway never rejects with HTTP 400.
func openRouterModelSupportsReasoning(model string) bool {
	return llm.ModelSupportsEffortWithProvider("openrouter", model)
}

// buildRequest assembles the OpenRouter chat-completion payload for a request.
// The reasoning control is injected only when the target model supports the
// reasoning schema (see openRouterModelSupportsReasoning), so a non-reasoning
// model never receives a payload the gateway rejects with HTTP 400.
// It also enforces provider-specific token contracts via TokenManager:
// OpenAI => reasoning_effort + max_completion_tokens, Anthropic => max_tokens = budget+4096.
func (p *OpenRouterProvider) buildRequest(model string, msgs []openrouterMessage, req ai.Request, stream bool) openrouterRequest {
	body := openrouterRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.Stop,
		Stream:      stream,
		Reasoning:   reasoningFor(req),
	}
	// Default max output tokens for code generation tasks if not explicitly
	// limited by the provider or user request.
	if body.MaxTokens == 0 {
		body.MaxTokens = 4096
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if !openRouterModelSupportsReasoning(model) {
		body.Reasoning = nil
	} else if body.Reasoning != nil {
		// Enforce token contracts via TokenManager
		tm := llm.NewTokenManager()
		effort := body.Reasoning.Effort
		// For openrouter vendor-prefixed IDs, infer effective provider
		payload := tm.BuildPayload("openrouter", model, effort)
		lowerModel := strings.ToLower(model)
		switch {
		case strings.HasPrefix(lowerModel, "openai/o1") || strings.HasPrefix(lowerModel, "openai/o3"):
			// OpenAI: reasoning_effort + max_completion_tokens
			if payload.ReasoningEffort != "" {
				body.Reasoning.Effort = payload.ReasoningEffort
			}
			body.Reasoning.MaxTokens = 0
			if payload.MaxCompletionTokens > 0 {
				body.MaxCompletionTokens = payload.MaxCompletionTokens
				// Also keep MaxTokens for compatibility; some gateways map it
				if body.MaxTokens == 0 || body.MaxTokens < payload.MaxCompletionTokens {
					body.MaxTokens = payload.MaxCompletionTokens
				}
			}
		case strings.HasPrefix(lowerModel, "anthropic/claude-3-7"):
			// Anthropic: thinking.budget_tokens + max_tokens = budget+4096
			body.Reasoning.Effort = ""
			if payload.ThinkingBudget > 0 {
				body.Reasoning.MaxTokens = payload.ThinkingBudget
			}
			if payload.MaxTokens > 0 {
				body.MaxTokens = payload.MaxTokens
				body.MaxCompletionTokens = 0
			}
		default:
			// Generic: apply thinking budget if present
			if payload.ThinkingBudget > 0 {
				body.Reasoning.MaxTokens = payload.ThinkingBudget
			}
			if payload.MaxTokens > 0 && body.MaxTokens == 0 {
				body.MaxTokens = payload.MaxTokens
			}
		}
		// Ensure Anthropic max_tokens > budget_tokens
		if strings.HasPrefix(lowerModel, "anthropic/") && body.Reasoning != nil && body.Reasoning.MaxTokens > 0 {
			if body.MaxTokens <= body.Reasoning.MaxTokens {
				body.MaxTokens = body.Reasoning.MaxTokens + 4096
			}
		}
	}
	if len(req.Tools) > 0 {
		rawTools := make([]json.RawMessage, 0, len(req.Tools))
		for _, t := range req.Tools {
			data, err := json.Marshal(t)
			if err == nil {
				rawTools = append(rawTools, data)
			}
		}
		body.Tools = rawTools
	}
	return body
}

// chatRequestStats carries the transport forensics of one logical invocation
// (Phase 7 P5): attempts is the total number of HTTP round-trips (1 + every
// retry), rateLimitedRetries how many of those were 429 rate-limit retries.
type chatRequestStats struct {
	attempts           int
	rateLimitedRetries int
}

// doChatRequest POSTs a chat-completion payload to the OpenRouter gateway and
// returns the HTTP response (the caller owns the body) plus the transport
// forensics of the call. When the payload
// carried a reasoning control and the gateway rejects it with HTTP 400, the
// reasoning field is stripped and the request is retried exactly once: some
// OpenRouter models do not accept the reasoning schema, and OpenRouter bills
// tokens at the gateway before the stream fails. Stripping reasoning lets the
// turn complete without reasoning rather than failing the whole request.
//
// HTTP 429 (Too Many Requests) responses are handled gracefully with a retry
// loop before the error is thrown: the Retry-After header is honored when
// present, otherwise the request is retried with exponential backoff
// (1s -> 2s -> 4s) up to openRouterMaxRateLimitRetries times. The wait between
// retries is interruptible by the request context so a cancelled or
// deadline-exceeded context surfaces promptly instead of sleeping out the full
// backoff window. Both the non-streaming Execute path and the streaming
// ExecuteStream path route through this function, so rate-limited free-tier
// builds recover instead of aborting on the first 429. Every retry is a
// transport attempt of the SAME logical invocation — recovered 429s never
// double the invocation count, and their responses carry no billed tokens.
func (p *OpenRouterProvider) doChatRequest(ctx context.Context, key string, body openrouterRequest, stream bool) (*http.Response, chatRequestStats, error) {
	var stats chatRequestStats
	attempt := func(b openrouterRequest) (*http.Response, error) {
		stats.attempts++
		payload, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("openrouter: marshal: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("openrouter: new request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key)
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-OpenRouter-Categories", "agent-runtime")
		httpReq.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")
		return p.client.Do(httpReq)
	}

	resp, err := attempt(body)
	if err != nil {
		return nil, stats, fmt.Errorf("openrouter: do: %w", err)
	}
	if resp.StatusCode == http.StatusBadRequest && body.Reasoning != nil {
		// A non-reasoning model rejected the reasoning schema. Discard the
		// rejected response, strip the reasoning payload and retry once.
		_ = resp.Body.Close()
		body.Reasoning = nil
		resp, err = attempt(body)
		if err != nil {
			return nil, stats, fmt.Errorf("openrouter: do: %w", err)
		}
	}

	// Rate-limited (429): retry with backoff instead of aborting the turn.
	for retries := 0; resp.StatusCode == http.StatusTooManyRequests && retries < openRouterMaxRateLimitRetries; retries++ {
		delay, ok := retryAfterDelay(resp)
		if !ok {
			delay = openRouterRateLimitBackoff(retries)
		}
		_ = resp.Body.Close()
		if !waitRateLimitRetry(ctx, delay) {
			return nil, stats, fmt.Errorf("openrouter: rate limited (429): retry aborted: %w", ctx.Err())
		}
		resp, err = attempt(body)
		if err != nil {
			return nil, stats, fmt.Errorf("openrouter: do: %w", err)
		}
		stats.rateLimitedRetries++
	}
	return resp, stats, nil
}

// retryAfterDelay parses the HTTP Retry-After header from a response into the
// delay to wait before retrying. Retry-After carries either a number of seconds
// or an HTTP-date; both forms are honored. ok=false when the header is absent
// or unparsable, so callers fall back to exponential backoff.
func retryAfterDelay(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// openRouterRateLimitBackoff returns the exponential backoff delay for the
// zero-based retry index: 1s for retry 0, 2s for retry 1, 4s for retry 2.
func openRouterRateLimitBackoff(retry int) time.Duration {
	if retry < 0 {
		retry = 0
	}
	if retry > 10 {
		retry = 10
	}
	return time.Duration(1<<retry) * openRouterRateLimitBackoffBase
}

// waitRateLimitRetry blocks for delay, aborting early when ctx is cancelled.
// It reports false when the context was cancelled before the delay elapsed, so
// the caller can surface the cancellation instead of sleeping out the backoff.
func waitRateLimitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type openrouterResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openrouterChoice `json:"choices"`
	Usage   *openrouterUsage   `json:"usage,omitempty"`
}

type openrouterChoice struct {
	Index        int              `json:"index"`
	Message      *openrouterMsg   `json:"message,omitempty"`
	Delta        *openrouterDelta `json:"delta,omitempty"`
	FinishReason string           `json:"finish_reason"`
}

type openrouterMsg struct {
	Role             string               `json:"role"`
	Content          string               `json:"content"`
	Reasoning        string               `json:"reasoning,omitempty"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []openrouterToolCall `json:"tool_calls,omitempty"`
}

type openrouterToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openrouterToolCallFunc `json:"function"`
}

type openrouterToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openrouterDelta struct {
	Role             string                `json:"role,omitempty"`
	Content          string                `json:"content,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ToolCalls        []openrouterToolDelta `json:"tool_calls,omitempty"`
}

type openrouterToolDelta struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type openrouterUsage struct {
	PromptTokens     int                       `json:"prompt_tokens"`
	CompletionTokens int                       `json:"completion_tokens"`
	TotalTokens      int                       `json:"total_tokens"`
	PromptDetails    *openrouterUsageDetails   `json:"prompt_tokens_details,omitempty"`
	CompletionDetail *openrouterCompletionInfo `json:"completion_tokens_details,omitempty"`
}

// openrouterUsageDetails carries the input-token split OpenRouter exposes
// through the OpenAI-compatible prompt_tokens_details object.
type openrouterUsageDetails struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// openrouterCompletionInfo carries the output-token split for reasoning.
type openrouterCompletionInfo struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ProviderUsage converts the parsed OpenRouter usage object into the
// authoritative ai.ProviderUsage contract. Known is always true for a parsed
// object: OpenRouter reports a usage object on every non-streaming response.
func (u *openrouterUsage) ProviderUsage() ai.ProviderUsage {
	out := ai.ProviderUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		Known:            true,
	}
	if u.PromptDetails != nil {
		out.CachedTokens = u.PromptDetails.CachedTokens
		if out.ReasoningTokens == 0 {
			out.ReasoningTokens = u.PromptDetails.ReasoningTokens
		}
	}
	if u.CompletionDetail != nil {
		out.ReasoningTokens += u.CompletionDetail.ReasoningTokens
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens + out.ReasoningTokens
	}
	return out
}

type OpenRouterStreamResult struct {
	io.ReadCloser
	sr *openrouterSSEReader
}

func (r *OpenRouterStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return ai.ProviderUsage{}
}

// FinishReason returns the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...). It reports "" when the stream ended
// before any finish_reason chunk was seen. Consumers use it to distinguish a
// natural completion ("stop") from a response truncated by the completion
// ceiling ("length").
func (r *OpenRouterStreamResult) FinishReason() string {
	if r.sr != nil {
		return r.sr.finishReason
	}
	return ""
}

// thinkTagSplitter is a stateful inline <think>...</think> extractor for
// OpenRouter models that return their thinking blocks inside delta.content
// instead of a dedicated reasoning field. It rewrites contiguous thinking
// segments into ReasoningSentinel-wrapped runs so EVERY downstream consumer —
// including ones that never ran the stream classifier — receives reasoning on
// the sentinel channel and visible content untouched. Partial markers that
// straddle a chunk boundary ("<thi", "ink>") are held back in a residue buffer
// until enough bytes arrive, so a marker is consumed exactly once regardless
// of how the gateway fragments the SSE deltas.
type thinkTagSplitter struct {
	inThink bool
	residue []byte
}

func (t *thinkTagSplitter) write(chunk []byte) []byte {
	if len(chunk) == 0 {
		return nil
	}
	data := chunk
	if len(t.residue) > 0 {
		data = append(append([]byte(nil), t.residue...), chunk...)
		t.residue = nil
	}
	var out []byte
	for len(data) > 0 {
		if t.inThink {
			idx := bytes.Index(data, []byte("</think>"))
			if idx < 0 {
				// Still inside the thinking block: emit what is provably not a
				// partial closing marker, hold back the ambiguous tail.
				keep := longestPartialSuffix(data, "</think>")
				emit := len(data) - keep
				out = append(out, data[:emit]...)
				t.residue = append(t.residue[:0], data[emit:]...)
				return out
			}
			out = append(out, data[:idx]...)
			out = append(out, ReasoningSentinel...)
			t.inThink = false
			data = data[idx+len("</think>"):]
			continue
		}
		idx := bytes.Index(data, []byte("<think>"))
		if idx < 0 {
			keep := longestPartialSuffix(data, "<think>")
			emit := len(data) - keep
			out = append(out, data[:emit]...)
			t.residue = append(t.residue[:0], data[emit:]...)
			return out
		}
		out = append(out, data[:idx]...)
		out = append(out, ReasoningSentinel...)
		t.inThink = true
		data = data[idx+len("<think>"):]
	}
	return out
}

// takeResidue flushes bytes held back for a possibly-partial marker when the
// stream terminates: no more bytes can complete the marker, so the tail is
// delivered verbatim (classified by the state at hold time).
func (t *thinkTagSplitter) takeResidue() []byte {
	tail := t.residue
	t.residue = nil
	if t.inThink && len(tail) > 0 {
		wrapped := make([]byte, 0, len(ReasoningSentinel)+len(tail)+len(ReasoningSentinel))
		wrapped = append(wrapped, ReasoningSentinel...)
		wrapped = append(wrapped, tail...)
		wrapped = append(wrapped, ReasoningSentinel...)
		return wrapped
	}
	return tail
}

// longestPartialSuffix returns the length of the longest proper suffix of data
// that is a prefix of marker — the number of trailing bytes that MIGHT be the
// beginning of marker arriving in the next chunk.
func longestPartialSuffix(data []byte, marker string) int {
	max := len(marker) - 1
	if len(data) < max {
		max = len(data)
	}
	for k := max; k > 0; k-- {
		if bytes.HasSuffix(data, []byte(marker[:k])) {
			return k
		}
	}
	return 0
}

type openrouterSSEReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *openrouterUsage

	// think splits inline <think>…</think> blocks out of delta.content into
	// sentinel-wrapped reasoning runs (see thinkTagSplitter).
	think thinkTagSplitter

	// usage tracks cumulative token accounting: the authoritative provider
	// usage when a usage chunk arrives, plus a character-count estimate when
	// the stream is interrupted (context deadline) before that chunk.
	usage streamUsageTracker

	// finishReason records the terminal finish_reason chunk observed on the
	// stream ("stop", "length", "tool_calls", ...). It is surfaced to callers
	// via OpenRouterStreamResult.FinishReason() so consumers can detect
	// responses truncated by the completion ceiling (finish_reason: "length")
	// instead of assuming the response ended naturally.
	finishReason string

	// pending holds bytes produced by a parsed SSE event that did not fit
	// into the caller's buffer on a previous Read() call. Read() must never
	// silently drop bytes just because len(p) was smaller than one logical
	// unit (a sentinel-wrapped reasoning/content/tool-call chunk) — doing so
	// previously truncated large reasoning bursts and tool-call argument
	// JSON mid-stream, dropping the closing sentinel along with the tail of
	// the data. That desynced the sentinel parser downstream (an opened-but-
	// never-closed \x00RSNG\x00 block), which caused partially-streamed
	// reasoning text to leak into the visible answer instead of staying in
	// the Thinking Panel. Buffering the remainder here and draining it on
	// the next Read() call restores normal io.Reader semantics regardless
	// of the caller's buffer size.
	pending []byte
}

func (s *openrouterSSEReader) Read(p []byte) (int, error) {
	// Drain any bytes left over from a previous parsed event before doing
	// any new work. This is what makes Read() safe to call with any buffer
	// size without losing data.
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}

	if s.closed {
		return 0, io.EOF
	}

	if s.reader == nil {
		s.reader = bufio.NewReader(s.body)
	}

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.usage.markInterrupted()
			}
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			if tail := s.think.takeResidue(); len(tail) > 0 {
				s.pending = append(s.pending, tail...)
			}
			s.closed = true
			s.usage.markCompleted(time.Now(), s.finishReason)
			if len(s.pending) > 0 {
				n := copy(p, s.pending)
				s.pending = s.pending[n:]
				return n, nil
			}
			return 0, io.EOF
		}

		var chunk openrouterResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
			s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Choices[0].FinishReason != "" {
			s.finishReason = chunk.Choices[0].FinishReason
			s.usage.markCompleted(time.Now(), chunk.Choices[0].FinishReason)
			continue
		}

		if chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content: emit wrapped in sentinel so the UI can
			// separate it from the message content buffer. Any bytes that
			// don't fit in p are held in s.pending and drained on the next
			// Read() call — never dropped, and never split without keeping
			// the remainder for delivery (see s.pending doc comment).
			// Some models/gateways report reasoning under "reasoning" instead
			// of "reasoning_content"; both are routed identically. Inline
			// <think>…</think> blocks inside delta.content are extracted into
			// the same sentinel channel by s.think.
			var out []byte
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				s.usage.recordReasoning(len(reasoningText))
				out = append(out, ReasoningSentinel...)
				out = append(out, reasoningText...)
				out = append(out, ReasoningSentinel...)
			}
			if delta.Content != "" {
				s.usage.recordOutput(len(delta.Content))
				out = append(out, s.think.write([]byte(delta.Content))...)
			}
			if len(out) > 0 {
				n := copy(p, out)
				if n < len(out) {
					s.pending = out[n:]
				}
				return n, nil
			}
			if len(delta.ToolCalls) > 0 {
				// Concatenate all tool-call deltas for this event into one
				// pending buffer rather than returning after the first
				// truncated chunk and silently discarding the rest — a
				// dropped tail here corrupts the tool-call JSON and causes
				// the downstream json.Unmarshal in the consumer to fail
				// silently, losing the whole tool call (and, with it, any
				// file mutation it carried).
				var all []byte
				for _, tc := range delta.ToolCalls {
					tcData, err := json.Marshal(tc)
					if err != nil {
						continue
					}
					all = append(all, ToolCallSentinel...)
					all = append(all, tcData...)
					all = append(all, ToolCallSentinel...)
				}
				if len(all) > 0 {
					s.usage.recordOutput(len(all))
					n := copy(p, all)
					if n < len(all) {
						s.pending = all[n:]
					}
					return n, nil
				}
			}
		}

		if chunk.Choices[0].FinishReason != "" {
			s.finishReason = chunk.Choices[0].FinishReason
			continue
		}
	}
}

func (s *openrouterSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
