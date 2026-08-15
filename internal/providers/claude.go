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
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

type ClaudeProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewClaudeProvider(apiKey, model string) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

func (p *ClaudeProvider) Name() string {
	return "anthropic"
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// claudeThinking is the Anthropic extended-thinking control. When injected
// with type "enabled" and a positive budget, the model performs extended
// reasoning up to budget_tokens. It is injected from the dynamically resolved
// effort directive; a zero budget omits it entirely (thinking disabled).
type claudeThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type claudeRequest struct {
	Model         string          `json:"model"`
	Messages      []claudeMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   float64         `json:"temperature,omitempty"`
	Stream        bool            `json:"stream"`
	System        string          `json:"system,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Thinking      *claudeThinking `json:"thinking,omitempty"`
}

type claudeResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Content    []claudeContent `json:"content"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      *claudeUsage    `json:"usage"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ProviderUsage converts the parsed Claude usage into the authoritative
// ai.ProviderUsage contract. Claude reports a usage object on every message,
// so Known is always true.
func (u *claudeUsage) ProviderUsage() ai.ProviderUsage {
	out := ai.ProviderUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		Known:            true,
	}
	return out
}

// claudeStopReason normalizes an Anthropic stop_reason onto the canonical
// label set ("stop", "length", "tool_calls", ...).
func claudeStopReason(raw string) string {
	switch raw {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "":
		return ""
	default:
		return strings.ToLower(raw)
	}
}

type claudeStreamEvent struct {
	Type  string       `json:"type"`
	Delta *claudeDelta `json:"delta,omitempty"`
	Usage *claudeUsage `json:"usage,omitempty"`
}

type claudeDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Thinking carries the reasoning process text for thinking_delta events
	// (the Anthropic native equivalent of reasoning_content).
	Thinking string `json:"thinking"`
	// StopReason carries the terminal stop reason for message_delta events
	// ("end_turn", "max_tokens", "tool_use", ...).
	StopReason string `json:"stop_reason"`
}

func (p *ClaudeProvider) buildMessages(req ai.Request) []claudeMessage {
	msgs := make([]claudeMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, claudeMessage{Role: m.Role, Content: content})
	}
	return msgs
}

// thinkingFor builds the Anthropic extended-thinking control from the resolved
// effort directive. A nil request reasoning config or a non-positive budget
// yields nil (thinking disabled, the pre-existing behavior).
func thinkingFor(req ai.Request) *claudeThinking {
	if req.Reasoning == nil || req.Reasoning.BudgetTokens <= 0 {
		return nil
	}
	return &claudeThinking{Type: "enabled", BudgetTokens: req.Reasoning.BudgetTokens}
}

func (p *ClaudeProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := claudeRequest{
		Model:         model,
		Messages:      msgs,
		MaxTokens:     maxTokens,
		Temperature:   req.Temperature,
		Stream:        false,
		System:        req.System,
		StopSequences: req.Stop,
		Thinking:      thinkingFor(req),
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("claude: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude: status %d: %s", resp.StatusCode, string(respBody))
	}

	var claudeResp claudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return nil, fmt.Errorf("claude: decode response: %w", err)
	}

	content := ""
	for _, c := range claudeResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	tokenIn := 0
	tokenOut := 0
	var usage ai.ProviderUsage
	usage.RequestStartedAt = time.Now()
	if claudeResp.Usage != nil {
		tokenIn = claudeResp.Usage.InputTokens
		tokenOut = claudeResp.Usage.OutputTokens
		usage = claudeResp.Usage.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = claudeStopReason(claudeResp.StopReason)
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		Usage:       usage,
	}, nil
}

func (p *ClaudeProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := claudeRequest{
		Model:         model,
		Messages:      msgs,
		MaxTokens:     maxTokens,
		Temperature:   req.Temperature,
		Stream:        true,
		System:        req.System,
		StopSequences: req.Stop,
		Thinking:      thinkingFor(req),
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("claude: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &claudeSSEReader{body: resp.Body, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(time.Now())
	return &ClaudeStreamResult{ReadCloser: sr, sr: sr}, nil
}

type ClaudeStreamResult struct {
	io.ReadCloser
	sr *claudeSSEReader
}

func (r *ClaudeStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return ai.ProviderUsage{}
}

// FinishReason reports the terminal stop reason observed on the stream. The
// Anthropic stop_reason "max_tokens" (completion ceiling hit) is normalized to
// "length" so consumers can uniformly detect truncation; otherwise the raw
// stop_reason ("end_turn", "tool_use", ...) is returned.
func (r *ClaudeStreamResult) FinishReason() string {
	if r.sr != nil {
		switch r.sr.finishReason {
		case "max_tokens":
			return "length"
		default:
			return r.sr.finishReason
		}
	}
	return ""
}

type claudeSSEReader struct {
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *claudeUsage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker
}

func (s *claudeSSEReader) Read(p []byte) (int, error) {
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

		var event claudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Usage != nil {
				s.finalUsage = &claudeUsage{
					InputTokens: event.Usage.InputTokens,
				}
				s.usage.recordInputTokens(event.Usage.InputTokens)
			}
		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			// Reasoning process (thinking_delta) is routed to the reasoning
			// handler only — never emitted into the response stream.
			if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
				s.usage.recordOutput(len(event.Delta.Thinking))
				if s.reasoningHandler != nil {
					if err := s.reasoningHandler(event.Delta.Thinking); err != nil {
						s.closed = true
						return 0, err
					}
				}
				continue
			}
			if event.Delta.Text != "" {
				s.usage.recordOutput(len(event.Delta.Text))
				n := copy(p, event.Delta.Text)
				return n, nil
			}
		case "message_delta":
			if event.Usage != nil {
				if s.finalUsage == nil {
					s.finalUsage = &claudeUsage{}
				}
				s.finalUsage.OutputTokens = event.Usage.OutputTokens
				s.usage.recordOutputTokens(event.Usage.OutputTokens)
			}
			if event.Delta != nil && event.Delta.StopReason != "" {
				s.finishReason = event.Delta.StopReason
				s.usage.markCompleted(time.Now(), claudeStopReason(event.Delta.StopReason))
			}
		case "message_stop":
			s.closed = true
			s.usage.markCompleted(time.Now(), claudeStopReason(s.finishReason))
			return 0, io.EOF
		}
	}
}

func (s *claudeSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
