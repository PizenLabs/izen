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
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
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
		client: &http.Client{Transport: StrictTransport(CloudResponseHeaderTimeout)},
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
	Stream        bool            `json:"stream,omitempty"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
	// ReasoningEffort is the native OpenAI qualitative reasoning control
	// (low / medium / high / xhigh). It is injected from the dynamically
	// resolved effort directive; empty omits the field.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r openaiRequest) MarshalJSON() ([]byte, error) {
	type alias openaiRequest
	return marshalWithExtra(alias(r), r.ExtraParams)
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

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamOptions is an alias for backward compatibility within the package.
type streamOptions = StreamOptions

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ProviderUsage converts the parsed OpenAI usage into the authoritative
// ai.ProviderUsage contract.
func (u *openaiUsage) ProviderUsage() ai.ProviderUsage {
	return openAICompatibleUsage(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
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
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("openai: no model assigned to target node (empty ModelBinding.ModelID)")
	}

	msgs := p.buildMessages(req)

	body := openaiRequest{
		Model:           model,
		Messages:        msgs,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		Stop:            req.Stop,
		Stream:          false,
		ReasoningEffort: req.Reasoning.LevelOrDefault(),
		ExtraParams:     req.ExtraParams,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, NewProviderError("openai", resp.StatusCode, respBody)
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
	var usage ai.ProviderUsage
	usage.RequestStartedAt = time.Now()
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
		usage = openAICompatibleUsage(openaiResp.Usage.PromptTokens, openaiResp.Usage.CompletionTokens, openaiResp.Usage.TotalTokens)
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = openaiResp.Choices[0].FinishReason
	// Task 1: fail fast on truncated payload before envelope parsing, but
	// PRESERVE the canonical partial buffer. Universal Stream Outcome:
	// length -> PARTIAL across all tiers; never clear/swallow the buffer.
	if openaiResp.Choices[0].FinishReason == "length" {
		if usage.FirstTokenAt.IsZero() {
			usage.FirstTokenAt = usage.CompletedAt
		}
		return &ai.Response{
			Content:     content,
			TokenInput:  tokenIn,
			TokenOutput: tokenOut,
			Usage:       usage,
		}, fmt.Errorf("%w: finish_reason=length", ai.ErrPayloadTruncated)
	}
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

func (p *OpenAIProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("openai: no model assigned to target node (empty ModelBinding.ModelID)")
	}

	msgs := p.buildMessages(req)

	body := openaiRequest{
		Model:           model,
		Messages:        msgs,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		Stop:            req.Stop,
		Stream:          true,
		StreamOptions:   &streamOptions{IncludeUsage: true},
		ReasoningEffort: req.Reasoning.LevelOrDefault(),
		ExtraParams:     req.ExtraParams,
	}

	reqCtx, cancel := context.WithCancel(ctx)

	payload, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Title", "izen")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openai: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, NewProviderError("openai", resp.StatusCode, respBody)
	}

	sr := &openaiSSEReader{body: resp.Body, cancel: cancel, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(time.Now())
	// Phase 6.4.4 Optimistic Prompt Token Invariant: commit the estimated
	// prompt count BEFORE entering the SSE chunk read loop.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
	return &OpenAIStreamResult{ReadCloser: sr, sr: sr}, nil
}

type OpenAIStreamResult struct {
	io.ReadCloser
	sr *openaiSSEReader
}

func (r *OpenAIStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return ai.ProviderUsage{}
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
	cancel           context.CancelFunc
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *openaiUsage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1):
	// finish_reason != "" closes the channel immediately, and a zero-token
	// idle watchdog force-cancels the stream context past the deadline.
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

// armIdle starts the zero-token idle watchdog exactly once per stream.
func (s *openaiSSEReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

// stopIdle disarms the watchdog; idempotent.
func (s *openaiSSEReader) stopIdle() {
	if s.idleStop != nil {
		stop := s.idleStop
		s.idleStop = nil
		stop()
	}
}

// drainTrailingUsage implements the Phase 6.4.2 Bounded Usage Drain
// (Telemetry Accuracy Invariant): after a terminal finish_reason, allow up
// to dprovider.UsageDrainTimeout (50ms) for a trailing usage-only SSE event
// (choices: []) so billed tokens land in the usage tracker before teardown.
// Single-threaded polling over s.reader.Buffered() — no concurrent reads.
func (s *openaiSSEReader) drainTrailingUsage() {
	if s.reader == nil {
		return
	}
	deadline := time.Now().Add(dprovider.UsageDrainTimeout)
	for {
		if n := s.reader.Buffered(); n > 0 {
			// Only consume a COMPLETE buffered line (see openrouter
			// drainTrailingUsage): a partial line must keep polling
			// until the deadline rather than blocking past it.
			peeked, err := s.reader.Peek(n)
			if err != nil {
				return
			}
			if !bytes.Contains(peeked, []byte{'\n'}) {
				if !time.Now().Before(deadline) {
					return
				}
				time.Sleep(2 * time.Millisecond)
				continue
			}
			line, err := s.reader.ReadString('\n')
			if err != nil {
				return
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
				return
			}
			var chunk openaiResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return
			}
			if chunk.Usage != nil {
				s.finalUsage = chunk.Usage
				s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
			}
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// closeTerminal records a terminal finish_reason and tears the channel
// down immediately: cancel context, drain body, mark closed, complete
// usage. Callers return io.EOF right after (or flush pending first).
func (s *openaiSSEReader) closeTerminal(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), reason)
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	if s.cancel != nil {
		s.cancel()
	}
	_, _ = io.Copy(io.Discard, s.body)
	s.closed = true
}

func (s *openaiSSEReader) Read(p []byte) (int, error) {
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
			if s.cancel != nil {
				s.cancel()
			}
			_, _ = io.Copy(io.Discard, s.body)
			s.closed = true
			s.usage.markCompleted(time.Now(), s.finishReason)
			return 0, io.EOF
		}

		var chunk openaiResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
			s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
		}

		// Telemetry Accuracy Invariant (Phase 6.4.2): usage-only chunks
		// (len(choices) == 0 with usage) carry the authoritative billing
		// counts and MUST be captured before teardown.
		if dprovider.IsUsageOnlyChunk(len(chunk.Choices), chunk.Usage != nil) {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		// Stream Terminal Invariant (Phase 6.4.1): finish_reason != ""
		// closes the output channel immediately — never wait for [DONE].
		// Phase 6.4.2 Bounded Usage Drain: allow up to 50ms for a
		// trailing usage-only chunk before tearing down.
		if dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason) {
			// A terminal chunk that also carries content emits it now and
			// terminates on the next Read; a content-free terminal chunk
			// terminates immediately so the UI timer stops at once.
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
				// Channel is terminal: bounded usage drain first so a
				// trailing usage-only chunk is captured, then tear down
				// transport now; the next Read observes s.closed and
				// returns EOF.
				s.drainTrailingUsage()
				if s.lifecycle != nil {
					s.lifecycle.MarkClosed()
				}
				if s.cancel != nil {
					s.cancel()
				}
				_, _ = io.Copy(io.Discard, s.body)
				s.closed = true
				// If the caller's buffer was too small the tail is dropped
				// here only for this coalesced edge; providers emit the
				// terminal reason on its own chunk in practice.
				return n, nil
			}
			s.drainTrailingUsage()
			s.closeTerminal(chunk.Choices[0].FinishReason)
			return 0, io.EOF
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

func (s *openaiSSEReader) Close() error {
	s.closed = true
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	if s.cancel != nil {
		s.cancel()
	}
	_, _ = io.Copy(io.Discard, s.body)
	return s.body.Close()
}
