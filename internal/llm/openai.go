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
)

type OpenAIClient struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
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
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
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

	content := ""
	if openaiResp.Choices[0].Message != nil {
		content = openaiResp.Choices[0].Message.Content
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

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("openai: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	tokenIn, tokenOut, cacheRead := 0, 0, 0
	var cost float64
	reader := newOpenAIStreamReader(resp.Body)

	for {
		chunk, err := reader.ReadChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
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

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil && chunk.Choices[0].Delta.Content != "" {
			content := chunk.Choices[0].Delta.Content
			full.WriteString(content)
			if handler != nil {
				if err := handler(content); err != nil {
					_ = resp.Body.Close()
					return LLMResponse{}, err
				}
			}
		}
	}

	llmResp := LLMResponse{
		Content:         SanitizeOutput(full.String()),
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
