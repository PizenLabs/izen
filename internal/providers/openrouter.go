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
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ErrOpenRouterAuth is returned when OpenRouter authentication fails (HTTP 401
// or missing API key). The UI layer detects this sentinel error via errors.Is
// and displays a clear actionable banner instead of a raw HTTP status message.
var ErrOpenRouterAuth = errors.New("openrouter: authorization failed (HTTP 401): invalid or missing OPENROUTER_API_KEY — check your environment variables or run: export OPENROUTER_API_KEY=<your_key>")

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
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := openrouterRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.Stop,
		Stream:      false,
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

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openrouter: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen")
	httpReq.Header.Set("X-Title", "izen")
	httpReq.Header.Set("X-Description", "AI amplifies human judgment. Humans remain in control.")
	httpReq.Header.Set("X-Categories", "cli-agent")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: do: %w", err)
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
		content = openaiResp.Choices[0].Message.Content
		if openaiResp.Choices[0].FinishReason == "tool_calls" && len(openaiResp.Choices[0].Message.ToolCalls) > 0 {
			for _, tc := range openaiResp.Choices[0].Message.ToolCalls {
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
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
	}

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		ToolCalls:   toolCalls,
	}, nil
}

func (p *OpenRouterProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := openrouterRequest{
		Model:         model,
		Messages:      msgs,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		Stop:          req.Stop,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openrouter: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen")
	httpReq.Header.Set("X-Title", "izen")
	httpReq.Header.Set("X-Description", "AI amplifies human judgment. Humans remain in control.")
	httpReq.Header.Set("X-Categories", "cli-agent")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: do: %w", err)
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
	return msgs
}

type openrouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openrouterRequest struct {
	Model         string              `json:"model"`
	Messages      []openrouterMessage `json:"messages"`
	MaxTokens     int                 `json:"max_tokens,omitempty"`
	Temperature   float64             `json:"temperature,omitempty"`
	Stop          []string            `json:"stop,omitempty"`
	Stream        bool                `json:"stream"`
	StreamOptions *streamOptions      `json:"stream_options,omitempty"`
	Tools         []json.RawMessage   `json:"tools,omitempty"`
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
	Role      string               `json:"role"`
	Content   string               `json:"content"`
	ToolCalls []openrouterToolCall `json:"tool_calls,omitempty"`
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
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenRouterStreamResult struct {
	io.ReadCloser
	sr *openrouterSSEReader
}

func (r *OpenRouterStreamResult) Usage() (input, output int) {
	if r.sr != nil && r.sr.finalUsage != nil {
		return r.sr.finalUsage.PromptTokens, r.sr.finalUsage.CompletionTokens
	}
	return 0, 0
}

type openrouterSSEReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *openrouterUsage
}

func (s *openrouterSSEReader) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.EOF
	}

	if s.reader == nil {
		s.reader = bufio.NewReader(s.body)
	}

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
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
			s.closed = true
			return 0, io.EOF
		}

		var chunk openrouterResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content: emit wrapped in sentinel so the UI can
			// separate it from the message content buffer.
			if delta.ReasoningContent != "" {
				reasoning := ReasoningSentinel + delta.ReasoningContent + ReasoningSentinel
				n := copy(p, reasoning)
				return n, nil
			}
			if delta.Content != "" {
				n := copy(p, delta.Content)
				return n, nil
			}
			if len(delta.ToolCalls) > 0 {
				for _, tc := range delta.ToolCalls {
					tcData, err := json.Marshal(tc)
					if err != nil {
						continue
					}
					toolCallChunk := ToolCallSentinel + string(tcData) + ToolCallSentinel
					n := copy(p, toolCallChunk)
					if n < len(toolCallChunk) {
						return n, nil
					}
				}
			}
		}

		if chunk.Choices[0].FinishReason != "" {
			continue
		}
	}
}

func (s *openrouterSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
