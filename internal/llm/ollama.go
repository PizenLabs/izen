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
	"github.com/PizenLabs/izen/internal/httpx"
)

type OllamaClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
	bus     *events.Bus
}

func NewOllamaClient(baseURL, apiKey, model string) *OllamaClient {
	return &OllamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		// Local bound: cold model loads can legitimately exceed 10s TTFT.
		client: &http.Client{Transport: httpx.StrictTransport(httpx.LocalResponseHeaderTimeout)},
	}
}

func (c *OllamaClient) WithEventBus(bus *events.Bus) *OllamaClient {
	c.bus = bus
	return c
}

func (c *OllamaClient) Name() string {
	return "ollama"
}

// ErrOllamaNamespacedModel is returned when a vendor-prefixed
// (OpenRouter-style vendor/model) ID reaches the local Ollama client.
// Provider Routing Isolation Invariant: such IDs must route directly to
// their namespaced driver — never trial-executed against local endpoints.
var ErrOllamaNamespacedModel = errors.New("ollama: provider/model mismatch: vendor-prefixed model ID does not belong to the local driver")

// rejectOllamaNamespacedModel fails fast on vendor-namespaced model IDs
// (e.g. thinkingmachines/..., nvidia/...) before any local HTTP call.
func rejectOllamaNamespacedModel(model string) error {
	m := strings.TrimSpace(model)
	for i := 0; i < len(m); i++ {
		if m[i] == '/' && i > 0 && i+1 < len(m) {
			return fmt.Errorf("%w: model %q must be routed to its namespaced provider, not ollama", ErrOllamaNamespacedModel, model)
		}
	}
	return nil
}

func (c *OllamaClient) buildMessages(req PromptRequest) []openAIMessage {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage(m))
	}
	return msgs
}

func (c *OllamaClient) resolveModel(override string) string {
	return override
}

func (c *OllamaClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	// Provider Routing Isolation: never trial-execute a namespaced ID locally.
	if err := rejectOllamaNamespacedModel(c.resolveModel(req.Model)); err != nil {
		return LLMResponse{}, err
	}
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
		return LLMResponse{}, fmt.Errorf("ollama: marshal: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
	}

	var openaiResp openAIResp
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: decode: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return LLMResponse{}, fmt.Errorf("ollama: no choices")
	}

	text := ""
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		// Reasoning Content Fallback Invariant (Phase 6.4.2) + Stream Delta
		// Reasoning Invariant (Phase 6.4.5): thinking-heavy models may emit
		// the answer in reasoning/thinking fields with empty content —
		// synthesize from reasoning instead of returning empty.
		text = usableContent(msg.Content, msg.Reasoning, msg.ReasoningContent, msg.Thinking)
	}
	text = SanitizeOutput(text)

	tokenIn, tokenOut := 0, 0
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
	}
	if tokenIn == 0 && tokenOut == 0 {
		promptLen := 0
		for _, m := range req.Messages {
			promptLen += len(m.Content)
		}
		tokenIn = promptLen / 4
		tokenOut = len(text) / 4
	}

	llmResp := LLMResponse{
		Content:      text,
		TokenInput:   tokenIn,
		TokenOutput:  tokenOut,
		TotalCostUSD: 0,
	}
	if c.bus != nil {
		c.bus.Publish(events.NewProviderUsageUpdate("", c.resolveModel(req.Model), tokenIn, tokenOut, 0))
	}
	return llmResp, nil
}

func (c *OllamaClient) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	// Provider Routing Isolation: never trial-execute a namespaced ID locally.
	if err := rejectOllamaNamespacedModel(c.resolveModel(req.Model)); err != nil {
		return LLMResponse{}, err
	}
	body := openAIReq{
		Model:       c.resolveModel(req.Model),
		Messages:    c.buildMessages(req),
		Stream:      true,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = 4096
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: marshal: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("ollama: do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	var reasoning strings.Builder
	tokenIn, tokenOut := 0, 0
	reader := newOpenAIStreamReader(resp.Body)

	for {
		chunk, err := reader.ReadChunk()
		if errors.Is(err, io.EOF) {
			cancel()
			_, _ = io.Copy(io.Discard, resp.Body)
			break
		}
		if err != nil {
			cancel()
			return LLMResponse{}, fmt.Errorf("ollama: stream: %w", err)
		}

		if chunk.Usage != nil {
			tokenIn = chunk.Usage.PromptTokens
			tokenOut = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning Content Fallback Invariant (Phase 6.4.2) + Stream
			// Delta Reasoning Invariant (Phase 6.4.5): retain thinking text
			// (all three reasoning spellings) so a reasoning-only stream
			// still yields a payload below and counts as stream progress.
			reasoningText := unifiedDeltaReasoning(delta)
			if reasoningText != "" {
				// Stream progress: reasoning/thinking tokens reset the idle
				// watchdog so thinking-heavy streams never false-trigger it.
				reader.noteTokens(len(reasoningText))
				reasoning.WriteString(reasoningText)
				if req.ReasoningHandler != nil {
					if err := req.ReasoningHandler(reasoningText); err != nil {
						cancel()
						return LLMResponse{}, err
					}
				}
			}
			if delta.Content != "" {
				reader.noteTokens(len(delta.Content))
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

	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	if tokenIn == 0 && tokenOut == 0 {
		promptLen := 0
		for _, m := range req.Messages {
			promptLen += len(m.Content)
		}
		tokenIn = promptLen / 4
		tokenOut = full.Len() / 4
	}

	content := full.String()
	if strings.TrimSpace(content) == "" {
		// Reasoning fallback: the model emitted only thinking content.
		content = stripThinkingTags(reasoning.String())
	}

	return LLMResponse{
		Content:      SanitizeOutput(content),
		TokenInput:   tokenIn,
		TokenOutput:  tokenOut,
		TotalCostUSD: 0,
	}, nil
}
