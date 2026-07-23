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

type OllamaClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOllamaClient(baseURL, apiKey, model string) *OllamaClient {
	return &OllamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{},
	}
}

func (c *OllamaClient) Name() string {
	return "ollama"
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
	if override != "" {
		return override
	}
	return c.model
}

func (c *OllamaClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
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
	defer func() { _ = resp.Body.Close() }()

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
		text = openaiResp.Choices[0].Message.Content
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

	return LLMResponse{
		Content:      text,
		TokenInput:   tokenIn,
		TokenOutput:  tokenOut,
		TotalCostUSD: 0,
	}, nil
}

func (c *OllamaClient) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
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

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return LLMResponse{}, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	tokenIn, tokenOut := 0, 0
	reader := newOpenAIStreamReader(resp.Body)

	for {
		chunk, err := reader.ReadChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = resp.Body.Close()
			return LLMResponse{}, fmt.Errorf("ollama: stream: %w", err)
		}

		if chunk.Usage != nil {
			tokenIn = chunk.Usage.PromptTokens
			tokenOut = chunk.Usage.CompletionTokens
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

	if tokenIn == 0 && tokenOut == 0 {
		promptLen := 0
		for _, m := range req.Messages {
			promptLen += len(m.Content)
		}
		tokenIn = promptLen / 4
		tokenOut = full.Len() / 4
	}

	return LLMResponse{
		Content:      SanitizeOutput(full.String()),
		TokenInput:   tokenIn,
		TokenOutput:  tokenOut,
		TotalCostUSD: 0,
	}, nil
}
