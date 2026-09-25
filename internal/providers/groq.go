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
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
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
		client:  &http.Client{Transport: StrictTransport(CloudResponseHeaderTimeout)},
	}
}

func (p *GroqProvider) Name() string {
	return "groq"
}

func (p *GroqProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	requestStarted := time.Now()
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("groq: no model assigned to target node (empty ModelBinding.ModelID)")
	}
	prepared, plan, err := PrepareContractRequest("groq", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	body := groqRequest{
		Model:          model,
		Messages:       msgs,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stop:           req.Stop,
		Stream:         false,
		ResponseFormat: req.ResponseFormat,
		Tools:          marshalContractTools(req.Tools),
		ExtraParams:    req.ExtraParams,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("groq: marshal: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("groq: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("groq: do: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, NewProviderError("groq", resp.StatusCode, respBody)
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
	var usage ai.ProviderUsage
	usage.RequestStartedAt = requestStarted
	if groqResp.Usage != nil {
		tokenIn = groqResp.Usage.PromptTokens
		tokenOut = groqResp.Usage.CompletionTokens
		usage = groqResp.Usage.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = groqResp.Choices[0].FinishReason
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}

	response := &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		Usage:       usage,
	}
	StampResponseMetadata(response, "groq", model, plan, groqResp.Choices[0].FinishReason)
	if response.Truncated {
		return response, ai.NewOutputTruncated("groq", "length")
	}
	return response, nil
}

func (p *GroqProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	requestStarted := time.Now()
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("groq: no model assigned to target node (empty ModelBinding.ModelID)")
	}
	prepared, plan, err := PrepareContractRequest("groq", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	body := groqRequest{
		Model:          model,
		Messages:       msgs,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stop:           req.Stop,
		Stream:         true,
		StreamOptions:  &streamOptions{IncludeUsage: true},
		ResponseFormat: req.ResponseFormat,
		Tools:          marshalContractTools(req.Tools),
		ExtraParams:    req.ExtraParams,
	}

	reqCtx, cancel := context.WithCancel(ctx)

	payload, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("groq: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("groq: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Title", "izen")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("groq: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		return nil, NewProviderError("groq", resp.StatusCode, respBody)
	}

	sr := &groqSSEReader{body: resp.Body, cancel: cancel, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(requestStarted)
	// Phase 6.4.4 Optimistic Prompt Token Invariant.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
	return &GroqStreamResult{ReadCloser: sr, sr: sr, metadata: newResponseMetadata("groq", model, plan)}, nil
}

func (p *GroqProvider) buildMessages(req ai.Request) []groqMessage {
	if prepared, _, err := PrepareContractRequest("groq", req); err == nil {
		req = prepared
	}
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
	Model          string             `json:"model"`
	Messages       []groqMessage      `json:"messages"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	Temperature    float64            `json:"temperature,omitempty"`
	Stop           []string           `json:"stop,omitempty"`
	Stream         bool               `json:"stream,omitempty"`
	StreamOptions  *streamOptions     `json:"stream_options,omitempty"`
	ResponseFormat *ai.ResponseFormat `json:"response_format,omitempty"`
	Tools          []json.RawMessage  `json:"tools,omitempty"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r groqRequest) MarshalJSON() ([]byte, error) {
	type alias groqRequest
	return marshalWithExtra(alias(r), r.ExtraParams)
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
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
}

type groqUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ProviderUsage converts the parsed Groq usage into the authoritative
// ai.ProviderUsage contract.
func (u *groqUsage) ProviderUsage() ai.ProviderUsage {
	return openAICompatibleUsage(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

type GroqStreamResult struct {
	io.ReadCloser
	sr       *groqSSEReader
	metadata ai.ResponseMetadata
}

func (r *GroqStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return normalizeUsageMetadata(r.sr.usage.Usage())
	}
	return ai.ProviderUsage{}
}

// FinishReason reports the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...), or "" if none was seen.
func (r *GroqStreamResult) FinishReason() string {
	if r.sr != nil {
		return NormalizeFinishReason(r.sr.finishReason)
	}
	return ""
}

// ResponseMetadata returns the standardized contract/finish-reason wrapper for
// this stream.
func (r *GroqStreamResult) ResponseMetadata() ai.ResponseMetadata {
	if r == nil {
		return ai.ResponseMetadata{}
	}
	return streamResponseMetadata(r.metadata, r.Usage(), r.FinishReason())
}

func (r *GroqStreamResult) TruncationError() error {
	if r == nil {
		return nil
	}
	return streamTruncationError("groq", r.FinishReason())
}

type groqSSEReader struct {
	cancel           context.CancelFunc
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *groqUsage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1).
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

func (s *groqSSEReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (s *groqSSEReader) stopIdle() {
	if s.idleStop != nil {
		stop := s.idleStop
		s.idleStop = nil
		stop()
	}
}

// closeTerminal records a terminal finish_reason and tears the channel
// down immediately so the UI timer stops at once.
func (s *groqSSEReader) closeTerminal(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), reason)
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	s.closed = true
	_ = closeSSERequest(s.cancel, s.body, nil)
}

func (s *groqSSEReader) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.EOF
	}

	if s.reader == nil {
		s.reader = bufio.NewReader(s.body)
	}
	s.armIdle()

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			trimmed := strings.TrimSpace(line)
			if trimmed == "data: [DONE]" {
				s.closed = true
				if s.lifecycle != nil {
					s.lifecycle.MarkClosed()
				}
				s.stopIdle()
				s.usage.markCompleted(time.Now(), s.finishReason)
				_ = closeSSERequest(s.cancel, s.body, nil)
				return 0, io.EOF
			}
			s.usage.markInterrupted()
			if s.lifecycle != nil {
				s.lifecycle.MarkClosed()
			}
			s.stopIdle()
			s.closed = true
			_ = closeSSERequest(s.cancel, s.body, nil)
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
			if s.lifecycle != nil {
				s.lifecycle.MarkClosed()
			}
			s.stopIdle()
			s.usage.markCompleted(time.Now(), s.finishReason)
			_ = closeSSERequest(s.cancel, s.body, nil)
			return 0, io.EOF
		}

		var chunk groqResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
			s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		// Stream Terminal Invariant (Phase 6.4.1): finish_reason != ""
		// closes the output channel immediately — never wait for [DONE].
		if dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason) {
			if chunk.Choices[0].Delta != nil && chunk.Choices[0].Delta.Content != "" {
				content := chunk.Choices[0].Delta.Content
				s.finishReason = chunk.Choices[0].FinishReason
				s.usage.markCompleted(time.Now(), chunk.Choices[0].FinishReason)
				s.usage.recordOutput(len(content))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(content))
				}
				s.stopIdle()
				n := copy(p, content)
				if s.lifecycle != nil {
					s.lifecycle.MarkClosed()
				}
				s.closed = true
				_ = closeSSERequest(s.cancel, s.body, nil)
				return n, nil
			}
			s.closeTerminal(chunk.Choices[0].FinishReason)
			return 0, io.EOF
		}

		if chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content (thinking process) is routed to the
			// reasoning handler only — never emitted into the response
			// stream. Some models report the field as "reasoning" instead of
			// "reasoning_content"; both are routed identically.
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				s.usage.recordReasoning(len(reasoningText))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(reasoningText))
				}
				if s.reasoningHandler != nil {
					if err := s.reasoningHandler(reasoningText); err != nil {
						s.closed = true
						if s.lifecycle != nil {
							s.lifecycle.MarkClosed()
						}
						s.stopIdle()
						return 0, err
					}
				}
				continue
			}
			if delta.Content != "" {
				s.usage.recordOutput(len(delta.Content))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(delta.Content))
				}
				s.stopIdle()
				n := copy(p, delta.Content)
				return n, nil
			}
		}
	}
}

func (s *groqSSEReader) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	return closeSSERequest(s.cancel, s.body, nil)
}
