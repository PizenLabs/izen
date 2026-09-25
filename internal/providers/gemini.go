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

type GeminiProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewGeminiProvider(apiKey, model string) *GeminiProvider {
	return &GeminiProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Transport: StrictTransport(CloudResponseHeaderTimeout)},
	}
}

func (p *GeminiProvider) Name() string {
	return "gemini"
}

type geminiMessage struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought"`
}

type geminiRequest struct {
	Contents          []geminiMessage          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	Stream            bool                     `json:"stream"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r geminiRequest) MarshalJSON() ([]byte, error) {
	type alias geminiRequest
	return marshalWithExtra(alias(r), r.ExtraParams)
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens  int             `json:"maxOutputTokens,omitempty"`
	Temperature      float64         `json:"temperature,omitempty"`
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

func geminiTools(tools []ai.ToolDefinition) []geminiTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  append(json.RawMessage(nil), tool.Function.Parameters...),
		})
	}
	return []geminiTool{{FunctionDeclarations: declarations}}
}

type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
	// FinishReason is the terminal generation finish reason ("STOP",
	// "MAX_TOKENS", "SAFETY", ...) reported on the final candidate chunk.
	FinishReason string `json:"finishReason"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// ProviderUsage converts the parsed Gemini usage metadata into the
// authoritative ai.ProviderUsage contract.
func (u *geminiUsageMetadata) ProviderUsage() ai.ProviderUsage {
	out := ai.ProviderUsage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: u.CandidatesTokenCount,
		TotalTokens:      u.TotalTokenCount,
		Known:            true,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens
	}
	return out
}

type geminiStreamResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
}

// finishReasonLabel normalizes a Gemini terminal finish reason onto the
// canonical label set ("stop", "length", "tool_calls", ...). "MAX_TOKENS"
// (completion ceiling hit) maps to "length" so consumers can uniformly detect
// truncation.
func finishReasonLabel(candidates []geminiCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	switch candidates[0].FinishReason {
	case "MAX_TOKENS":
		return "length"
	case "STOP":
		return "stop"
	default:
		return strings.ToLower(candidates[0].FinishReason)
	}
}

func (p *GeminiProvider) buildMessages(req ai.Request) []geminiMessage {
	if prepared, _, err := PrepareContractRequest("gemini", req); err == nil {
		req = prepared
	}
	msgs := make([]geminiMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		} else if m.Role == "system" {
			continue
		}
		msgs = append(msgs, geminiMessage{
			Role:  role,
			Parts: []geminiPart{{Text: content}},
		})
	}
	return msgs
}

func (p *GeminiProvider) apiURL(model string, stream bool) string {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	return fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:%s?key=%s", model, action, p.apiKey)
}

func (p *GeminiProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	requestStarted := time.Now()
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	prepared, plan, err := PrepareContractRequest("gemini", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := geminiRequest{
		Contents: msgs,
		Stream:   false,
		Tools:    geminiTools(req.Tools),
		GenerationConfig: &geminiGenerationConfig{
			MaxOutputTokens: maxTokens,
			Temperature:     req.Temperature,
			ResponseMimeType: func() string {
				if plan.NativeSchema {
					return "application/json"
				}
				return ""
			}(),
			ResponseSchema: plan.NativeJSONSchema(),
		},
		ExtraParams: req.ExtraParams,
	}

	if req.System != "" {
		body.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.apiURL(model, false), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: do request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, NewProviderError("gemini", resp.StatusCode, respBody)
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}

	content := ""
	if len(geminiResp.Candidates) > 0 {
		for _, part := range geminiResp.Candidates[0].Content.Parts {
			content += part.Text
		}
	}

	tokenIn := 0
	tokenOut := 0
	var usage ai.ProviderUsage
	usage.RequestStartedAt = requestStarted
	if geminiResp.UsageMetadata != nil {
		tokenIn = geminiResp.UsageMetadata.PromptTokenCount
		tokenOut = geminiResp.UsageMetadata.CandidatesTokenCount
		usage = geminiResp.UsageMetadata.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = finishReasonLabel(geminiResp.Candidates)
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}

	finishReason := finishReasonLabel(geminiResp.Candidates)
	response := &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		Usage:       usage,
	}
	StampResponseMetadata(response, "gemini", model, plan, finishReason)
	if response.Truncated {
		return response, ai.NewOutputTruncated("gemini", "length")
	}
	return response, nil
}

func (p *GeminiProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	requestStarted := time.Now()
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	prepared, plan, err := PrepareContractRequest("gemini", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := geminiRequest{
		Contents: msgs,
		Stream:   true,
		Tools:    geminiTools(req.Tools),
		GenerationConfig: &geminiGenerationConfig{
			MaxOutputTokens: maxTokens,
			Temperature:     req.Temperature,
			ResponseMimeType: func() string {
				if plan.NativeSchema {
					return "application/json"
				}
				return ""
			}(),
			ResponseSchema: plan.NativeJSONSchema(),
		},
		ExtraParams: req.ExtraParams,
	}

	if req.System != "" {
		body.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	reqCtx, cancel := context.WithCancel(ctx)

	payload, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.apiURL(model, true), bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gemini: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		return nil, NewProviderError("gemini", resp.StatusCode, respBody)
	}

	sr := &geminiSSEReader{body: resp.Body, cancel: cancel, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(requestStarted)
	// Phase 6.4.4 Optimistic Prompt Token Invariant.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
	return &GeminiStreamResult{ReadCloser: sr, sr: sr, metadata: newResponseMetadata("gemini", model, plan)}, nil
}

type GeminiStreamResult struct {
	io.ReadCloser
	sr       *geminiSSEReader
	metadata ai.ResponseMetadata
}

func (r *GeminiStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return normalizeUsageMetadata(r.sr.usage.Usage())
	}
	return ai.ProviderUsage{}
}

// FinishReason reports the terminal finish reason observed on the stream.
// The Gemini finishReason "MAX_TOKENS" (completion ceiling hit) is normalized
// to "length" so consumers can uniformly detect truncation; otherwise the raw
// finishReason ("STOP", "SAFETY", ...) is returned.
func (r *GeminiStreamResult) FinishReason() string {
	if r.sr != nil {
		return NormalizeFinishReason(r.sr.finishReason)
	}
	return ""
}

// ResponseMetadata returns the standardized contract/finish-reason wrapper for
// this stream.
func (r *GeminiStreamResult) ResponseMetadata() ai.ResponseMetadata {
	if r == nil {
		return ai.ResponseMetadata{}
	}
	return streamResponseMetadata(r.metadata, r.Usage(), r.FinishReason())
}

func (r *GeminiStreamResult) TruncationError() error {
	if r == nil {
		return nil
	}
	return streamTruncationError("gemini", r.FinishReason())
}

type geminiSSEReader struct {
	cancel           context.CancelFunc
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *geminiUsageMetadata
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1).
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
	// terminalSeen marks that a finishReason chunk was observed: the next
	// Read terminates even if the transport never delivers a closer.
	terminalSeen bool
}

func (s *geminiSSEReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (s *geminiSSEReader) stopIdle() {
	if s.idleStop != nil {
		stop := s.idleStop
		s.idleStop = nil
		stop()
	}
}

// closeTerminal records a terminal finish reason and tears the channel
// down immediately so the UI timer stops at once.
func (s *geminiSSEReader) closeTerminal(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), finishReasonLabel([]geminiCandidate{{FinishReason: reason}}))
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	s.closed = true
	_ = closeSSERequest(s.cancel, s.body, nil)
}

func (s *geminiSSEReader) Read(p []byte) (int, error) {
	if s.closed {
		return 0, io.EOF
	}
	if s.terminalSeen {
		s.closeTerminal(s.finishReason)
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
				s.usage.markCompleted(time.Now(), finishReasonLabel(nil))
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
			// [DONE] is a semantic terminal sentinel for SSE-compatible
			// gateways. Close immediately; never wait for a server-side EOF.
			s.closeTerminal(s.finishReason)
			return 0, io.EOF
		}

		var event geminiStreamResponse
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.UsageMetadata != nil {
			s.finalUsage = event.UsageMetadata
			s.usage.recordUsageFull(event.UsageMetadata.ProviderUsage())
		}

		if len(event.Candidates) > 0 {
			// Stream Terminal Invariant (Phase 6.4.1): a parsed
			// finishReason closes the channel — emit any content in this
			// chunk now, then terminate on the next Read without waiting
			// for a further transport frame.
			if dprovider.ShouldCloseOnFinishReason(event.Candidates[0].FinishReason) {
				s.finishReason = event.Candidates[0].FinishReason
				s.usage.markCompleted(time.Now(), finishReasonLabel(event.Candidates))
				s.terminalSeen = true
			}
			for _, part := range event.Candidates[0].Content.Parts {
				// Thought parts carry reasoning content and must be routed
				// to the reasoning handler — they must never appear in the
				// visible response.
				if part.Thought {
					s.usage.recordReasoning(len(part.Text))
					if s.lifecycle != nil {
						s.lifecycle.NoteTokens(len(part.Text))
					}
					if s.reasoningHandler != nil {
						if err := s.reasoningHandler(part.Text); err != nil {
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
				if part.Text != "" {
					s.usage.recordOutput(len(part.Text))
					if s.lifecycle != nil {
						s.lifecycle.NoteTokens(len(part.Text))
					}
					s.stopIdle()
					n := copy(p, part.Text)
					return n, nil
				}
			}
			// Content-free terminal chunk (finishReason only): terminate
			// immediately so the UI timer stops at once.
			if s.terminalSeen {
				s.closeTerminal(s.finishReason)
				return 0, io.EOF
			}
		}

		if len(event.Candidates) > 0 && event.Candidates[0].Content.Role == "" {
			s.closed = true
			s.usage.markCompleted(time.Now(), finishReasonLabel(event.Candidates))
			_ = closeSSERequest(s.cancel, s.body, nil)
			return 0, io.EOF
		}
	}
}

func (s *geminiSSEReader) Close() error {
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
