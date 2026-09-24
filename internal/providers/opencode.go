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
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
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
		client:  &http.Client{Transport: StrictTransport(CloudResponseHeaderTimeout)},
	}
}

func (p *OpenCodeProvider) Name() string {
	return "opencode"
}

// resolveAPIKey returns the effective API key for a request. Strict
// precedence: the explicitly configured key (saved in ~/.izen/config.yml and
// injected at cold-boot construction time) always wins over the shell
// environment variable. Env is only a fallback when no configured key exists.
func (p *OpenCodeProvider) resolveAPIKey() string {
	if key := strings.TrimSpace(p.apiKey); key != "" {
		return key
	}
	if envKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); envKey != "" {
		return envKey
	}
	return ""
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
		ExtraParams: req.ExtraParams,
	}

	// INVARIANT 1: casual minimal prompts must never carry tools.
	if !isCasualSystemPrompt(req.System) && len(req.Tools) > 0 {
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

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("opencode: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode: do: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, NewProviderError("opencode", resp.StatusCode, respBody)
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
	var usage ai.ProviderUsage
	usage.RequestStartedAt = time.Now()
	if ocResp.Usage != nil {
		tokenIn = ocResp.Usage.PromptTokens
		tokenOut = ocResp.Usage.CompletionTokens
		usage = ocResp.Usage.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	if len(ocResp.Choices) > 0 {
		usage.FinishReason = ocResp.Choices[0].FinishReason
	}
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}

	response := &ai.Response{
		Content:      content,
		TokenInput:   tokenIn,
		TokenOutput:  tokenOut,
		ToolCalls:    toolCalls,
		FinishReason: ocResp.Choices[0].FinishReason,
		Truncated:    isOutputLength(ocResp.Choices[0].FinishReason),
		Usage:        usage,
	}
	if isOutputLength(ocResp.Choices[0].FinishReason) {
		return response, ai.NewOutputTruncated("opencode", ocResp.Choices[0].FinishReason)
	}
	return response, nil
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
		ExtraParams:   req.ExtraParams,
	}

	// INVARIANT 1: casual minimal prompts must never carry tools.
	if !isCasualSystemPrompt(req.System) && len(req.Tools) > 0 {
		rawTools := make([]json.RawMessage, 0, len(req.Tools))
		for _, t := range req.Tools {
			data, err := json.Marshal(t)
			if err == nil {
				rawTools = append(rawTools, data)
			}
		}
		body.Tools = rawTools
	}

	reqCtx, cancel := context.WithCancel(ctx)

	payload, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Title", "izen")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: do: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		return nil, NewProviderError("opencode", resp.StatusCode, respBody)
	}

	sr := &opencodeSSEReader{body: resp.Body, cancel: cancel, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(time.Now())
	// Phase 6.4.4 Optimistic Prompt Token Invariant.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
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
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions *streamOptions    `json:"stream_options,omitempty"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r opencodeRequest) MarshalJSON() ([]byte, error) {
	type alias opencodeRequest
	return marshalWithExtra(alias(r), r.ExtraParams)
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
	Reasoning        string              `json:"reasoning,omitempty"`
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

// ProviderUsage converts the parsed opencode usage into the authoritative
// ai.ProviderUsage contract.
func (u *opencodeUsage) ProviderUsage() ai.ProviderUsage {
	return openAICompatibleUsage(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

type OpenCodeStreamResult struct {
	io.ReadCloser
	sr *opencodeSSEReader
}

func (r *OpenCodeStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return ai.ProviderUsage{}
}

// FinishReason reports the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...), or "" if none was seen.
func (r *OpenCodeStreamResult) FinishReason() string {
	if r.sr != nil {
		return r.sr.finishReason
	}
	return ""
}

func (r *OpenCodeStreamResult) TruncationError() error {
	if r == nil {
		return nil
	}
	return streamTruncationError("opencode", r.FinishReason())
}

type opencodeSSEReader struct {
	cancel           context.CancelFunc
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *opencodeUsage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker

	// pending holds bytes produced by a parsed SSE event that did not fit
	// into the caller's buffer on a previous Read() call. Read() must never
	// silently drop bytes just because len(p) was smaller than one logical
	// unit (a reasoning burst or a tool-call argument JSON chunk) — dropping
	// the tail corrupts the tool-call JSON and the sentinel stream. Buffering
	// the remainder here and draining it on the next Read() call restores
	// normal io.Reader semantics regardless of the caller's buffer size.
	pending []byte

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1).
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

func (s *opencodeSSEReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (s *opencodeSSEReader) stopIdle() {
	if s.idleStop != nil {
		stop := s.idleStop
		s.idleStop = nil
		stop()
	}
}

// closeTerminal records a terminal finish_reason and tears the channel
// down immediately so the UI timer stops at once.
func (s *opencodeSSEReader) closeTerminal(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), reason)
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	s.closed = true
	_ = closeSSERequest(s.cancel, s.body, nil)
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

		var chunk opencodeResponse
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
				if n < len(content) {
					s.pending = []byte(content)[n:]
				}
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
			// Reasoning content (thinking process) is routed to the reasoning
			// handler only — never emitted into the response stream. Some
			// models report the field as "reasoning" instead of
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
					s.usage.recordOutput(len(all))
					n := copy(p, all)
					if n < len(all) {
						s.pending = all[n:]
					}
					return n, nil
				}
			}
		}

		if dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason) {
			s.closeTerminal(chunk.Choices[0].FinishReason)
			return 0, io.EOF
		}
	}
}

func (s *opencodeSSEReader) Close() error {
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
