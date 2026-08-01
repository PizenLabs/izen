package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// OpenCodeProvider talks to the opencode HTTP API (https://opencode.ai/zen/v1),
// which exposes an OpenAI-compatible /chat/completions endpoint. The endpoint
// routes to a catalog of models (DeepSeek, Grok, MiniMax, GLM, Kimi, GPT, ...)
// through a single OPENCODE_API_KEY.
type OpenCodeProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenCodeProvider(apiKey, model, baseURL string) *OpenCodeProvider {
	if baseURL == "" {
		baseURL = "https://opencode.ai/zen/v1"
	}
	return &OpenCodeProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

func (p *OpenCodeProvider) Name() string {
	return "opencode"
}

// resolveAPIKey returns the effective API key for a request. It checks the
// OPENCODE_API_KEY environment variable first (picking up runtime .env
// changes), then falls back to the compile-time key from the provider config.
func (p *OpenCodeProvider) resolveAPIKey() string {
	if envKey := os.Getenv("OPENCODE_API_KEY"); envKey != "" {
		return envKey
	}
	return p.apiKey
}

func (p *OpenCodeProvider) buildMessages(req ai.Request) []opencodeMessage {
	msgs := make([]opencodeMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, opencodeMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, opencodeMessage{Role: m.Role, Content: content})
	}
	return msgs
}

func (p *OpenCodeProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("opencode: api key is empty — set OPENCODE_API_KEY or configure api_key in provider config")
	}

	msgs := p.buildMessages(req)

	body := opencodeRequest{
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
		return nil, fmt.Errorf("opencode: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("opencode: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opencode: status %d: %s", resp.StatusCode, string(respBody))
	}

	var ocResp opencodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ocResp); err != nil {
		return nil, fmt.Errorf("opencode: decode: %w", err)
	}

	if len(ocResp.Choices) == 0 {
		return nil, fmt.Errorf("opencode: no choices")
	}

	content := ""
	var toolCalls []ai.ToolCall
	if ocResp.Choices[0].Message != nil {
		content = ocResp.Choices[0].Message.Content
		if ocResp.Choices[0].FinishReason == "tool_calls" && len(ocResp.Choices[0].Message.ToolCalls) > 0 {
			for _, tc := range ocResp.Choices[0].Message.ToolCalls {
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
	if ocResp.Usage != nil {
		tokenIn = ocResp.Usage.PromptTokens
		tokenOut = ocResp.Usage.CompletionTokens
	}

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		ToolCalls:   toolCalls,
	}, nil
}

func (p *OpenCodeProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("opencode: api key is empty — set OPENCODE_API_KEY or configure api_key in provider config")
	}

	msgs := p.buildMessages(req)

	body := opencodeRequest{
		Model:         model,
		Messages:      msgs,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		Stop:          req.Stop,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
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
		return nil, fmt.Errorf("opencode: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("opencode: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opencode: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &opencodeSSEReader{body: resp.Body, reasoningHandler: req.ReasoningHandler}
	return &OpenCodeStreamResult{ReadCloser: sr, sr: sr}, nil
}

type opencodeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type opencodeRequest struct {
	Model         string            `json:"model"`
	Messages      []opencodeMessage `json:"messages"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   float64           `json:"temperature,omitempty"`
	Stop          []string          `json:"stop,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions *streamOptions    `json:"stream_options,omitempty"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
}

type opencodeResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []opencodeChoice `json:"choices"`
	Usage   *opencodeUsage   `json:"usage,omitempty"`
}

type opencodeChoice struct {
	Index        int            `json:"index"`
	Message      *opencodeMsg   `json:"message,omitempty"`
	Delta        *opencodeDelta `json:"delta,omitempty"`
	FinishReason string         `json:"finish_reason"`
}

type opencodeMsg struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []opencodeToolCall `json:"tool_calls,omitempty"`
}

type opencodeToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function opencodeToolCallFunc `json:"function"`
}

type opencodeToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type opencodeDelta struct {
	Role             string              `json:"role,omitempty"`
	Content          string              `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []opencodeToolDelta `json:"tool_calls,omitempty"`
}

type opencodeToolDelta struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type opencodeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenCodeStreamResult struct {
	io.ReadCloser
	sr *opencodeSSEReader
}

func (r *OpenCodeStreamResult) Usage() (input, output int) {
	if r.sr != nil && r.sr.finalUsage != nil {
		return r.sr.finalUsage.PromptTokens, r.sr.finalUsage.CompletionTokens
	}
	return 0, 0
}

// FinishReason reports the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...), or "" if none was seen.
func (r *OpenCodeStreamResult) FinishReason() string {
	if r.sr != nil {
		return r.sr.finishReason
	}
	return ""
}

type opencodeSSEReader struct {
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *opencodeUsage
	finishReason     string
	reasoningHandler func(string) error

	// pending holds bytes produced by a parsed SSE event that did not fit
	// into the caller's buffer on a previous Read() call. Read() must never
	// silently drop bytes just because len(p) was smaller than one logical
	// unit (a reasoning burst or a tool-call argument JSON chunk) — dropping
	// the tail corrupts the tool-call JSON and the sentinel stream. Buffering
	// the remainder here and draining it on the next Read() call restores
	// normal io.Reader semantics regardless of the caller's buffer size.
	pending []byte
}

func (s *opencodeSSEReader) Read(p []byte) (int, error) {
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

		var chunk opencodeResponse
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
			// Reasoning content (thinking process) is routed to the reasoning
			// handler only — never emitted into the response stream.
			if delta.ReasoningContent != "" {
				if s.reasoningHandler != nil {
					if err := s.reasoningHandler(delta.ReasoningContent); err != nil {
						s.closed = true
						return 0, err
					}
				}
				continue
			}
			if delta.Content != "" {
				n := copy(p, delta.Content)
				if n < len(delta.Content) {
					s.pending = []byte(delta.Content)[n:]
				}
				return n, nil
			}
			if len(delta.ToolCalls) > 0 {
				// Concatenate all tool-call deltas for this event into one
				// buffer so the downstream consumer never sees a truncated
				// tool-call JSON blob.
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

func (s *opencodeSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
