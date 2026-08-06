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
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

type OpenAIProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai"
}

type openaiMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type openaiRequest struct {
	Model         string          `json:"model"`
	Messages      []openaiMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   float64         `json:"temperature,omitempty"`
	Stop          []string        `json:"stop,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
}

type openaiResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiChoice struct {
	Index        int            `json:"index"`
	Message      *openaiMessage `json:"message,omitempty"`
	Delta        *openaiDelta   `json:"delta,omitempty"`
	FinishReason string         `json:"finish_reason"`
}

type openaiDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (p *OpenAIProvider) buildMessages(req ai.Request) []openaiMessage {
	msgs := make([]openaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, openaiMessage{Role: m.Role, Content: content})
	}
	return msgs
}

func (p *OpenAIProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	body := openaiRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.Stop,
		Stream:      false,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	var openaiResp openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
	}

	content := ""
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		content = firstUsableContent(msg.Content, msg.Reasoning, msg.ReasoningContent)
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
	}, nil
}

func (p *OpenAIProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	body := openaiRequest{
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
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &openaiSSEReader{body: resp.Body, reasoningHandler: req.ReasoningHandler}
	return &OpenAIStreamResult{ReadCloser: sr, sr: sr}, nil
}

type OpenAIStreamResult struct {
	io.ReadCloser
	sr *openaiSSEReader
}

func (r *OpenAIStreamResult) Usage() (input, output int) {
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return 0, 0
}

// FinishReason reports the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...), or "" if none was seen.
func (r *OpenAIStreamResult) FinishReason() string {
	if r.sr != nil {
		return r.sr.finishReason
	}
	return ""
}

type openaiSSEReader struct {
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *openaiUsage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker
}

func (s *openaiSSEReader) Read(p []byte) (int, error) {
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
			s.closed = true
			return 0, io.EOF
		}

		var chunk openaiResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
			s.usage.recordUsage(chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content (thinking process) is routed to the reasoning
			// handler only — it is never emitted into the response stream.
			// Some models report the field as "reasoning" instead of
			// "reasoning_content"; both are routed identically.
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				s.usage.recordOutput(len(reasoningText))
				if s.reasoningHandler != nil {
					if err := s.reasoningHandler(reasoningText); err != nil {
						s.closed = true
						return 0, err
					}
				}
				continue
			}
			if delta.Content != "" {
				s.usage.recordOutput(len(delta.Content))
				n := copy(p, delta.Content)
				return n, nil
			}
		}

		if chunk.Choices[0].FinishReason != "" {
			s.finishReason = chunk.Choices[0].FinishReason
			continue
		}
	}
}

func (s *openaiSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
