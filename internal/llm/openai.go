package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/httpx"
)

type OpenAIClient struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	bus     *events.Bus
}

func NewOpenAIClient(apiKey, model, baseURL string) *OpenAIClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1/chat/completions"
	}
	return &OpenAIClient{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Transport: httpx.StrictTransport(httpx.CloudResponseHeaderTimeout)},
	}
}

// WithEventBus wires an optional event bus. When set, an interrupted stream
// (context deadline / cancellation) publishes a StreamUsage envelope with the
// partial token usage before the timeout error is returned, so tokens billed by
// the provider are never silently zeroed in telemetry.
func (c *OpenAIClient) WithEventBus(bus *events.Bus) *OpenAIClient {
	c.bus = bus
	return c
}

// publishStreamUsage emits the partial token usage of an interrupted stream as
// a StreamUsage envelope. It never blocks (the bus is non-blocking) and never
// mutates any state.
func (c *OpenAIClient) publishStreamUsage(model string, input, output int, interrupted bool, reason string) {
	if c.bus == nil {
		return
	}
	env := events.NewEnvelope(events.DomainKindTelemetry, "llm.stream", events.StreamUsagePayload{
		Model:        model,
		InputTokens:  input,
		OutputTokens: output,
		Interrupted:  interrupted,
		Reason:       reason,
	})
	c.bus.PublishEnvelope(env)
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// defaultOpenAIMaxTokens is the output limit applied when a request carries
// no explicit MaxTokens. 4096 keeps long code-generation answers clear of
// the completion ceiling (finish_reason "length") instead of relying on
// provider defaults (often ~1500-2048 tokens).
const defaultOpenAIMaxTokens = 4096

// maxOpenAIMaxTokens is the hard ceiling for an explicit MaxTokens budget:
// larger requests are clamped, never sent unconstrained.
const maxOpenAIMaxTokens = 8192

// clampOpenAIMaxTokens enforces the output-limit contract: an unset budget
// defaults to 4096 (long code-generation answers clear the completion
// ceiling instead of relying on provider defaults), an explicit budget is
// preserved verbatim up to the 8192 hard cap.
func clampOpenAIMaxTokens(n int) int {
	if n <= 0 {
		return defaultOpenAIMaxTokens
	}
	if n > maxOpenAIMaxTokens {
		return maxOpenAIMaxTokens
	}
	return n
}

// streamOptions is an alias for backward compatibility.
type streamOptions = StreamOptions

type openAIReq struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Stream        bool            `json:"stream,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   float64         `json:"temperature,omitempty"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
}

type openAIResp struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int               `json:"index"`
	Message      *openAIMsgContent `json:"message,omitempty"`
	Delta        *openAIDelta      `json:"delta,omitempty"`
	FinishReason string            `json:"finish_reason"`
}

type openAIMsgContent struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Thinking carries OpenRouter's native delta.thinking / message.thinking
	// field (Phase 6.4.5 Stream Delta Reasoning Invariant). It is treated as
	// first-class reasoning progress alongside reasoning/reasoning_content.
	Thinking string `json:"thinking,omitempty"`
}

type openAIDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	// Thinking carries OpenRouter's native delta.thinking field (Phase 6.4.5
	// Stream Delta Reasoning Invariant). Non-empty thinking tokens count as
	// active stream progress and feed the unified stream accumulator.
	Thinking string `json:"thinking,omitempty"`
}

// unifiedDeltaReasoning unifies the three OpenRouter SSE reasoning spellings
// (reasoning_content, reasoning, thinking) into a single progress signal.
// It delegates field ordering to deltaReasoningText so streaming and
// non-streaming paths converge on the same fallback.
func unifiedDeltaReasoning(d *openAIDelta) string {
	if d == nil {
		return ""
	}
	return deltaReasoningText(d.ReasoningContent, d.Reasoning, d.Thinking)
}

type openAIUsage struct {
	PromptTokens     int                  `json:"prompt_tokens"`
	CompletionTokens int                  `json:"completion_tokens"`
	TotalTokens      int                  `json:"total_tokens"`
	Cost             float64              `json:"cost,omitempty"`
	PromptDetails    *openAIPromptDetails `json:"prompt_tokens_details,omitempty"`
}

type openAIPromptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func (c *OpenAIClient) Name() string {
	if strings.Contains(c.baseURL, "openrouter") {
		return "openrouter"
	}
	return "openai"
}

func (c *OpenAIClient) buildMessages(req PromptRequest) []openAIMessage {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage(m))
	}
	return msgs
}

func (c *OpenAIClient) resolveEndpoint() string {
	if strings.HasSuffix(c.baseURL, "/chat/completions") {
		return c.baseURL
	}
	return c.baseURL + "/chat/completions"
}

func (c *OpenAIClient) resolveModel(override string) string {
	return override
}

func (c *OpenAIClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	body := openAIReq{
		Model:       c.resolveModel(req.Model),
		Messages:    c.buildMessages(req),
		Stream:      false,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	// Default output limit: never send unconstrained max_tokens (0 or null).
	body.MaxTokens = clampOpenAIMaxTokens(body.MaxTokens)

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: marshal: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.resolveEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if strings.Contains(c.baseURL, "openrouter") {
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-Title", "izen")
		httpReq.Header.Set("X-OpenRouter-Categories", "agent-runtime")
		httpReq.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	var openaiResp openAIResp
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return LLMResponse{}, fmt.Errorf("openai: decode: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return LLMResponse{}, fmt.Errorf("openai: no choices")
	}

	content := ""
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		content = usableContent(msg.Content, msg.Reasoning, msg.ReasoningContent, msg.Thinking)
	}
	// Task 1: truncated payload handling — intercept BEFORE envelope parsing,
	// but PRESERVE the canonical partial buffer. Universal Stream Outcome:
	// length -> PARTIAL across all tiers; never clear/swallow the buffer.
	if openaiResp.Choices[0].FinishReason == "length" {
		tokenIn, tokenOut, cacheRead := 0, 0, 0
		if openaiResp.Usage != nil {
			tokenIn = openaiResp.Usage.PromptTokens
			tokenOut = openaiResp.Usage.CompletionTokens
			if openaiResp.Usage.PromptDetails != nil {
				cacheRead = openaiResp.Usage.PromptDetails.CachedTokens
			}
		}
		return LLMResponse{
			Content:         content,
			TokenInput:      tokenIn,
			TokenOutput:     tokenOut,
			CacheReadTokens: cacheRead,
			FinishReason:    "length",
			Truncated:       true,
		}, fmt.Errorf("%w: finish_reason=length", ErrPayloadTruncated)
	}
	content = SanitizeOutput(content)

	tokenIn, tokenOut, cacheRead := 0, 0, 0
	var cost float64
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
		cost = openaiResp.Usage.Cost
		if openaiResp.Usage.PromptDetails != nil {
			cacheRead = openaiResp.Usage.PromptDetails.CachedTokens
		}
	}

	llmResp := LLMResponse{
		Content:         content,
		TokenInput:      tokenIn,
		TokenOutput:     tokenOut,
		CacheReadTokens: cacheRead,
		FinishReason:    "stop",
		Truncated:       false,
	}

	if strings.Contains(c.baseURL, "openrouter") {
		modelID := c.resolveModel(req.Model)
		usage := CalculateCost(modelID, UsageReport{
			InputTokens:  tokenIn,
			OutputTokens: tokenOut,
		})
		llmResp.TotalCostUSD = usage.TotalCostUSD
		if cost > 0 {
			llmResp.TotalCostUSD = cost
		}
		llmResp.TotalCostUSD = EnforceFreeModelOverride(modelID, llmResp.TotalCostUSD)
	}

	if c.bus != nil {
		c.bus.Publish(events.NewProviderUsageUpdate("", c.resolveModel(req.Model), tokenIn, tokenOut, 0))
	}
	return llmResp, nil
}

func (c *OpenAIClient) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	body := openAIReq{
		Model:         c.resolveModel(req.Model),
		Messages:      c.buildMessages(req),
		Stream:        true,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	// Default output limit: never send unconstrained max_tokens (0 or null).
	body.MaxTokens = clampOpenAIMaxTokens(body.MaxTokens)

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: marshal: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.resolveEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if strings.Contains(c.baseURL, "openrouter") {
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-Title", "izen")
		httpReq.Header.Set("X-OpenRouter-Categories", "agent-runtime")
		httpReq.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}
	// Ensure the underlying TCP connection is closed immediately when the
	// parent context is cancelled (context timeout / user abort).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			cancel()
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		case <-done:
		}
	}()

	var full strings.Builder
	var reasoning strings.Builder
	tokenIn, tokenOut, cacheRead := 0, 0, 0
	var cost float64
	// outputChars accumulates streamed output characters so partial token usage
	// can be estimated when the request is interrupted (context deadline)
	// before the provider delivers its final usage chunk.
	outputChars := 0
	truncated := false
	reader := newOpenAIStreamReader(resp.Body)
	reader.cancel = cancel
	// Phase 6.4.5 unified stream accumulator: delta.reasoning, delta.thinking,
	// and delta.content all feed one transport-agnostic live counter so
	// reasoning-only streams count as progress (no false idle timeout) and
	// partial telemetry survives interruption. Authoritative usage chunks
	// always override the character fallback via SetAuthoritative.
	accum := &dprovider.StreamAccumulator{}
	promptChars := 0
	for _, m := range body.Messages {
		promptChars += len(m.Content)
	}
	accum.SetPromptEstimate(dprovider.EstimatePromptTokens(promptChars))

	// resolveUsage returns the authoritative token counts when a usage chunk
	// arrived, otherwise the unified accumulator snapshot (prompt estimate +
	// content-chars/4 fallback). The accumulator is total: partial streams
	// snapshot partial tokens even without a terminal usage frame.
	resolveUsage := func() (int, int) {
		if tokenIn > 0 || tokenOut > 0 {
			return tokenIn, tokenOut
		}
		if p, c, known, _ := accum.Snapshot(); known {
			return p, c
		}
		if outputChars > 0 {
			return tokenIn, outputChars / 4
		}
		return tokenIn, tokenOut
	}

	for {
		chunk, err := reader.ReadChunk()
		if errors.Is(err, io.EOF) {
			// Clean SSE termination: data: [DONE] consumed, drain remaining
			// buffer to io.EOF and signal transport to close immediately.
			cancel()
			_, _ = io.Copy(io.Discard, resp.Body)
			break
		}
		if err != nil {
			// "Explicit Over Implicit": report the partial usage before
			// returning the timeout/cancel error. Consumed tokens must never
			// silently vanish from telemetry. A natural EOF is handled above.
			if ctxErr := ctx.Err(); ctxErr != nil {
				estIn, estOut := resolveUsage()
				c.publishStreamUsage(c.resolveModel(req.Model), estIn, estOut, true, ctxErr.Error())
				cancel()
				return LLMResponse{TokenInput: estIn, TokenOutput: estOut}, fmt.Errorf("openai: stream: %w", err)
			}
			cancel()
			return LLMResponse{}, fmt.Errorf("openai: stream: %w", err)
		}

		if chunk.Usage != nil {
			tokenIn = chunk.Usage.PromptTokens
			tokenOut = chunk.Usage.CompletionTokens
			cost = chunk.Usage.Cost
			if chunk.Usage.PromptDetails != nil {
				cacheRead = chunk.Usage.PromptDetails.CachedTokens
			}
			// Unified accumulator: authoritative usage always wins over
			// estimates, but the live estimate is retained for partial
			// streams that never deliver a terminal usage frame.
			accum.SetAuthoritative(tokenIn, tokenOut)
		}

		// Task 1: intercept finish_reason == "length" BEFORE any envelope parsing.
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason == "length" {
			truncated = true
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Stream Delta Reasoning Invariant (Phase 6.4.5): reasoning,
			// thinking, and reasoning_content are unified into one progress
			// signal. Non-empty reasoning tokens count as active stream
			// progress (idle watchdog reset + accumulator) so reasoning
			// models never trigger a false 15s timeout or 'empty response'
			// error. Reasoning is routed to the reasoning pipeline only —
			// never appended to visible content — and retained so a
			// reasoning-only stream falls back to thinking text.
			reasoningText := unifiedDeltaReasoning(delta)
			if reasoningText != "" {
				outputChars += len(reasoningText)
				reader.noteTokens(len(reasoningText))
				accum.AddReasoning(len(reasoningText))
				reasoning.WriteString(reasoningText)
				if req.ReasoningHandler != nil {
					if err := req.ReasoningHandler(reasoningText); err != nil {
						cancel()
						return LLMResponse{}, err
					}
				}
			}
			if delta.Content != "" {
				outputChars += len(delta.Content)
				reader.noteTokens(len(delta.Content))
				accum.AddContent(len(delta.Content))
				full.WriteString(delta.Content)
				if handler != nil {
					if err := handler(delta.Content); err != nil {
						cancel()
						return LLMResponse{}, err
					}
				}
			}
		}
	}

	// Fail fast on truncated payload — do NOT attempt envelope parsing.
	// Universal Stream Outcome: preserve the canonical partial buffer
	// verbatim (no synthetic mutation); callers map length -> PARTIAL.
	if truncated {
		cancel()
		return LLMResponse{
			Content:         full.String(),
			TokenInput:      tokenIn,
			TokenOutput:     tokenOut,
			CacheReadTokens: cacheRead,
			FinishReason:    "length",
			Truncated:       true,
		}, fmt.Errorf("%w: finish_reason=length", ErrPayloadTruncated)
	}
	content := full.String()
	if strings.TrimSpace(content) == "" {
		// Reasoning fallback: the model emitted only thinking content.
		content = stripThinkingTags(reasoning.String())
	}

	llmResp := LLMResponse{
		Content:         SanitizeOutput(content),
		TokenInput:      tokenIn,
		TokenOutput:     tokenOut,
		CacheReadTokens: cacheRead,
		FinishReason:    "stop",
		Truncated:       false,
	}

	if strings.Contains(c.baseURL, "openrouter") {
		modelID := c.resolveModel(req.Model)
		usage := CalculateCost(modelID, UsageReport{
			InputTokens:  tokenIn,
			OutputTokens: tokenOut,
		})
		llmResp.TotalCostUSD = usage.TotalCostUSD
		if cost > 0 {
			llmResp.TotalCostUSD = cost
		}
		llmResp.TotalCostUSD = EnforceFreeModelOverride(modelID, llmResp.TotalCostUSD)
	}
	// Explicit cancel signals the transport to send TCP FIN immediately;
	// deferred drain ensures connection reuse and prevents OpenRouter's
	// 5-minute idle timeout.
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	return llmResp, nil
}

type openAIStreamReader struct {
	body   io.ReadCloser
	reader *sseReader

	cancel context.CancelFunc
	// terminal marks that a finish_reason chunk was observed: the next
	// ReadChunk terminates without waiting for [DONE].
	terminal bool
	// lifecycle tracks token emission for the zero-token idle watchdog.
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

type openAIChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}
