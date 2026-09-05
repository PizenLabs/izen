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

	"github.com/PizenLabs/izen/internal/events"
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
		client:  &http.Client{},
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

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIReq struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Stream        bool            `json:"stream"`
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
}

type openAIDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
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
	if override != "" {
		return override
	}
	return c.model
}

func (c *OpenAIClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	body := openAIReq{
		Model:       c.resolveModel(req.Model),
		Messages:    c.buildMessages(req),
		Stream:      false,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = 4096
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if strings.Contains(c.baseURL, "openrouter") {
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-OpenRouter-Categories", "agent-runtime")
		httpReq.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	// Task 1: truncated payload handling — intercept BEFORE envelope parsing.
	if openaiResp.Choices[0].FinishReason == "length" {
		return LLMResponse{}, fmt.Errorf("%w: finish_reason=length", ErrPayloadTruncated)
	}

	content := ""
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		content = usableContent(msg.Content, msg.Reasoning, msg.ReasoningContent)
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
	if body.MaxTokens <= 0 {
		body.MaxTokens = 4096
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveEndpoint(), bytes.NewReader(payload))
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
		httpReq.Header.Set("X-OpenRouter-Categories", "agent-runtime")
		httpReq.Header.Set("X-OpenRouter-Description", "AI amplifies human judgment. Humans remain in control.")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return LLMResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}
	// Task 2: strict cancellation — force-close SSE body when context is cancelled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
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

	// resolveUsage returns the authoritative token counts when a usage chunk
	// arrived, otherwise a character-count estimate of the output tokens.
	resolveUsage := func() (int, int) {
		if tokenIn > 0 || tokenOut > 0 {
			return tokenIn, tokenOut
		}
		if outputChars > 0 {
			return tokenIn, outputChars / 4
		}
		return tokenIn, tokenOut
	}

	for {
		chunk, err := reader.ReadChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// "Explicit Over Implicit": report the partial usage before
			// returning the timeout/cancel error. Consumed tokens must never
			// silently vanish from telemetry. A natural EOF is handled above.
			if ctxErr := ctx.Err(); ctxErr != nil {
				estIn, estOut := resolveUsage()
				c.publishStreamUsage(c.resolveModel(req.Model), estIn, estOut, true, ctxErr.Error())
				_ = resp.Body.Close()
				return LLMResponse{TokenInput: estIn, TokenOutput: estOut}, fmt.Errorf("openai: stream: %w", err)
			}
			_ = resp.Body.Close()
			return LLMResponse{}, fmt.Errorf("openai: stream: %w", err)
		}

		if chunk.Usage != nil {
			tokenIn = chunk.Usage.PromptTokens
			tokenOut = chunk.Usage.CompletionTokens
			cost = chunk.Usage.Cost
			if chunk.Usage.PromptDetails != nil {
				cacheRead = chunk.Usage.PromptDetails.CachedTokens
			}
		}

		// Task 1: intercept finish_reason == "length" BEFORE any envelope parsing.
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason == "length" {
			truncated = true
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content (thinking process) is routed to the reasoning
			// pipeline only — it is never appended to the visible response. It
			// is also retained so a reasoning-only stream (empty content) can
			// fall back to the thinking text instead of yielding an empty
			// response.
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				outputChars += len(reasoningText)
				reasoning.WriteString(reasoningText)
				if req.ReasoningHandler != nil {
					if err := req.ReasoningHandler(reasoningText); err != nil {
						_ = resp.Body.Close()
						return LLMResponse{}, err
					}
				}
			}
			if delta.Content != "" {
				outputChars += len(delta.Content)
				full.WriteString(delta.Content)
				if handler != nil {
					if err := handler(delta.Content); err != nil {
						_ = resp.Body.Close()
						return LLMResponse{}, err
					}
				}
			}
		}
	}

	// Fail fast on truncated payload — do NOT attempt envelope parsing.
	if truncated {
		_ = resp.Body.Close()
		return LLMResponse{TokenInput: tokenIn, TokenOutput: tokenOut}, fmt.Errorf("%w: finish_reason=length", ErrPayloadTruncated)
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
	_ = resp.Body.Close()
	return llmResp, nil
}

type openAIStreamReader struct {
	body   io.ReadCloser
	reader *sseReader
}

type openAIChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}
