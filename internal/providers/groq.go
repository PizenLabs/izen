package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

type GroqProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewGroqProvider(apiKey, model, baseURL string) *GroqProvider {
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1"
	}
	return &GroqProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
	}
}

func (p *GroqProvider) Name() string {
	return "groq"
}

func (p *GroqProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	body := groqRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("groq: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("groq: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("groq: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("groq: status %d: %s", resp.StatusCode, string(respBody))
	}

	var groqResp groqResponse
	if err := json.NewDecoder(resp.Body).Decode(&groqResp); err != nil {
		return nil, fmt.Errorf("groq: decode: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return nil, fmt.Errorf("groq: no choices")
	}

	content := ""
	if groqResp.Choices[0].Message != nil {
		content = groqResp.Choices[0].Message.Content
	}

	tokenIn := 0
	tokenOut := 0
	if groqResp.Usage != nil {
		tokenIn = groqResp.Usage.PromptTokens
		tokenOut = groqResp.Usage.CompletionTokens
	}

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
	}, nil
}

func (p *GroqProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	body := groqRequest{
		Model:    model,
		Messages: msgs,
		Stream:   true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("groq: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("groq: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("groq: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("groq: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &groqSSEReader{body: resp.Body}
	return &GroqStreamResult{ReadCloser: sr, sr: sr}, nil
}

func (p *GroqProvider) buildMessages(req ai.Request) []groqMessage {
	msgs := make([]groqMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, groqMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, groqMessage{Role: m.Role, Content: content})
	}
	return msgs
}

type groqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqRequest struct {
	Model    string        `json:"model"`
	Messages []groqMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type groqResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []groqChoice `json:"choices"`
	Usage   *groqUsage   `json:"usage,omitempty"`
}

type groqChoice struct {
	Index        int        `json:"index"`
	Message      *groqMsg   `json:"message,omitempty"`
	Delta        *groqDelta `json:"delta,omitempty"`
	FinishReason string     `json:"finish_reason"`
}

type groqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type groqUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type GroqStreamResult struct {
	io.ReadCloser
	sr *groqSSEReader
}

func (r *GroqStreamResult) Usage() (input, output int) {
	if r.sr != nil && r.sr.finalUsage != nil {
		return r.sr.finalUsage.PromptTokens, r.sr.finalUsage.CompletionTokens
	}
	return 0, 0
}

type groqSSEReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *groqUsage
}

func (s *groqSSEReader) Read(p []byte) (int, error) {
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

		var chunk groqResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
		}

		if chunk.Choices[0].Delta != nil && chunk.Choices[0].Delta.Content != "" {
			n := copy(p, chunk.Choices[0].Delta.Content)
			return n, nil
		}

		if chunk.Choices[0].FinishReason != "" {
			continue
		}
	}
}

func (s *groqSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
