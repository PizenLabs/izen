package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type AnthropicClient struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewAnthropicClient(apiKey, model string) *AnthropicClient {
	return &AnthropicClient{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com/v1/messages",
		client:  &http.Client{},
	}
}

type anthropicContent struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Cache string `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicReq struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
	System      []anthropicContent `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicResp struct {
	ID      string             `json:"id"`
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
	Model   string             `json:"model"`
	Usage   *anthropicUsage    `json:"usage"`
}

type anthropicUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CacheCreateTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens   int `json:"cache_read_input_tokens"`
}

type anthropicStreamEvent struct {
	Type  string          `json:"type"`
	Delta *anthropicDelta `json:"delta,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c *AnthropicClient) Name() string {
	return "anthropic"
}

func (c *AnthropicClient) buildSystemContent(req PromptRequest) []anthropicContent {
	if req.System == "" {
		return nil
	}
	system := []anthropicContent{{Type: "text", Text: req.System}}
	if req.CacheSystem {
		system[0].Cache = "ephemeral"
	}
	return system
}

func (c *AnthropicClient) buildMessages(req PromptRequest) []anthropicMessage {
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		content := anthropicContent{Type: "text", Text: m.Content}
		for _, ci := range req.CacheMessages {
			if ci == i {
				content.Cache = "ephemeral"
				break
			}
		}
		msgs = append(msgs, anthropicMessage{
			Role:    m.Role,
			Content: []anthropicContent{content},
		})
	}
	return msgs
}

func (c *AnthropicClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := anthropicReq{
		Model:       c.resolveModel(req.Model),
		MaxTokens:   maxTokens,
		Stream:      false,
		System:      c.buildSystemContent(req),
		Messages:    c.buildMessages(req),
		Temperature: req.Temperature,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if req.CacheSystem {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(respBody))
	}

	var claudeResp anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: decode: %w", err)
	}

	content := ""
	for _, c := range claudeResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	content = SanitizeOutput(content)

	tokenIn, tokenOut, cacheWrite, cacheRead := 0, 0, 0, 0
	if claudeResp.Usage != nil {
		tokenIn = claudeResp.Usage.InputTokens
		tokenOut = claudeResp.Usage.OutputTokens
		cacheWrite = claudeResp.Usage.CacheCreateTokens
		cacheRead = claudeResp.Usage.CacheReadTokens
	}

	return LLMResponse{
		Content:          content,
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, nil
}

func (c *AnthropicClient) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := anthropicReq{
		Model:       c.resolveModel(req.Model),
		MaxTokens:   maxTokens,
		Stream:      true,
		System:      c.buildSystemContent(req),
		Messages:    c.buildMessages(req),
		Temperature: req.Temperature,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")
	if req.CacheSystem {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("anthropic: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(respBody))
	}

	tokenIn, tokenOut, cacheWrite, cacheRead := 0, 0, 0, 0
	var full strings.Builder
	reader := newAnthropicStreamReader(resp.Body)

	for {
		event, err := reader.ReadEvent()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = resp.Body.Close()
			return LLMResponse{}, fmt.Errorf("anthropic: stream read: %w", err)
		}

		switch event.Type {
		case "message_start":
			if event.Usage != nil {
				tokenIn = event.Usage.InputTokens
				cacheWrite = event.Usage.CacheCreateTokens
				cacheRead = event.Usage.CacheReadTokens
			}
		case "content_block_delta":
			if event.Delta != nil && event.Delta.Text != "" {
				full.WriteString(event.Delta.Text)
				if handler != nil {
					if err := handler(event.Delta.Text); err != nil {
						_ = resp.Body.Close()
						return LLMResponse{}, err
					}
				}
			}
		case "message_delta":
			if event.Usage != nil {
				tokenOut = event.Usage.OutputTokens
			}
		case "message_stop":
			_ = resp.Body.Close()
			return LLMResponse{
				Content:          SanitizeOutput(full.String()),
				TokenInput:       tokenIn,
				TokenOutput:      tokenOut,
				CacheWriteTokens: cacheWrite,
				CacheReadTokens:  cacheRead,
			}, nil
		}
	}

	return LLMResponse{
		Content:          SanitizeOutput(full.String()),
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, nil
}

func (c *AnthropicClient) resolveModel(override string) string {
	if override != "" {
		return override
	}
	return c.model
}

type anthropicStreamReader struct {
	body   io.ReadCloser
	reader *bufio.Reader
}

func newAnthropicStreamReader(body io.ReadCloser) *anthropicStreamReader {
	return &anthropicStreamReader{
		body:   body,
		reader: bufio.NewReader(body),
	}
}

func (r *anthropicStreamReader) ReadEvent() (anthropicStreamEvent, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return anthropicStreamEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		return event, nil
	}
}
