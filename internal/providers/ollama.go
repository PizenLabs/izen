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

type OllamaProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOllamaProvider(baseURL, apiKey, model string) *OllamaProvider {
	return &OllamaProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		// Local bound: cold model loads can legitimately exceed 10s TTFT.
		client: &http.Client{Transport: StrictTransport(LocalResponseHeaderTimeout)},
	}
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

// ErrNamespacedModelID is the deterministic error returned when a
// vendor-prefixed (OpenRouter-style vendor/model) ID reaches the local
// Ollama driver. Such IDs (e.g. thinkingmachines/..., nvidia/...) can never
// execute locally; rejecting them BEFORE any network call upholds the
// Provider Routing Isolation Invariant — the dispatcher must route model
// strings directly to their registered driver without speculative trial
// calls to secondary (local Ollama) endpoints.
var ErrNamespacedModelID = errors.New("ollama: provider/model mismatch: vendor-prefixed model ID does not belong to the local driver")

// rejectNamespacedModel fails fast when model carries a vendor namespace
// (vendor/model). Local Ollama IDs never contain a slash; anything with one
// is a cross-provider ID that must never be trial-executed locally.
func rejectNamespacedModel(model string) error {
	m := strings.TrimSpace(model)
	for i := 0; i < len(m); i++ {
		if m[i] == '/' && i > 0 && i+1 < len(m) {
			return fmt.Errorf("%w: model %q must be routed to its namespaced provider, not ollama", ErrNamespacedModelID, model)
		}
	}
	return nil
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaRequest struct {
	Model         string          `json:"model"`
	Messages      []ollamaMessage `json:"messages"`
	Stream        bool            `json:"stream,omitempty"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
	Format        string          `json:"format,omitempty"` // "json" for structured output
	// FormatSchema carries Ollama's native JSON Schema object. It is kept out
	// of the default tags because Format remains a backwards-compatible string
	// for callers that explicitly request generic JSON mode.
	FormatSchema json.RawMessage `json:"-"`
	MaxTokens    *int            `json:"max_tokens,omitempty"`
	Options      *struct {
		NumPredict  int     `json:"num_predict"`
		Temperature float64 `json:"temperature,omitempty"`
	} `json:"options,omitempty"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r ollamaRequest) MarshalJSON() ([]byte, error) {
	type alias ollamaRequest
	raw, err := marshalWithExtra(alias(r), r.ExtraParams)
	if err != nil || len(r.FormatSchema) == 0 {
		return raw, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["format"] = json.RawMessage(compactJSON(r.FormatSchema))
	return json.Marshal(fields)
}

type ollamaResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

type choice struct {
	Index        int              `json:"index"`
	Message      *responseMessage `json:"message,omitempty"`
	Delta        *delta           `json:"delta,omitempty"`
	FinishReason string           `json:"finish_reason"`
}

type responseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type delta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ProviderUsage converts the parsed Ollama usage into the authoritative
// ai.ProviderUsage contract.
func (u *usage) ProviderUsage() ai.ProviderUsage {
	return openAICompatibleUsage(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

// sanitizeContent strips ANSI escape sequences and TUI UI artifact patterns
// that may have leaked into message content from viewport/rendering buffers.
func sanitizeContent(s string) string {
	// Strip ANSI escape sequences
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i++; i < len(s); i++ {
				if s[i] >= '@' && s[i] <= '~' {
					break
				}
			}
			continue
		}
		b.WriteByte(s[i])
	}
	clean := b.String()

	// Strip lines that are purely UI chrome (status bars, prompt prefixes, etc.)
	lines := strings.Split(clean, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			kept = append(kept, line)
			continue
		}
		// Skip status bar line: ● modelname · N tkn
		if strings.HasPrefix(trimmed, "●") && strings.Contains(trimmed, "·") && (strings.Contains(trimmed, "tkn") || strings.Contains(trimmed, "tok")) {
			continue
		}
		// Skip prompt prefix lines: ❯ ask ⟩ or similar
		if strings.HasPrefix(trimmed, "❯") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func (p *OllamaProvider) buildMessages(req ai.Request) []ollamaMessage {
	if prepared, _, err := PrepareContractRequest("ollama", req); err == nil {
		req = prepared
	}
	msgs := make([]ollamaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		msgs = append(msgs, ollamaMessage{Role: m.Role, Content: content})
	}
	return msgs
}

func (p *OllamaProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	requestStarted := time.Now()
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("ollama: no model assigned to target node (empty ModelBinding.ModelID)")
	}
	// Provider Routing Isolation: never trial-execute a namespaced
	// (vendor/model) ID against the local endpoint.
	if err := rejectNamespacedModel(model); err != nil {
		return nil, err
	}
	prepared, plan, err := PrepareContractRequest("ollama", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := ollamaRequest{
		Model:        model,
		Messages:     msgs,
		Stream:       false,
		MaxTokens:    &maxTokens,
		FormatSchema: plan.NativeJSONSchema(),
		ExtraParams:  req.ExtraParams,
		Options: &struct {
			NumPredict  int     `json:"num_predict"`
			Temperature float64 `json:"temperature,omitempty"`
		}{NumPredict: maxTokens, Temperature: req.Temperature},
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
		body.Format = "json"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, NewProviderError("ollama", resp.StatusCode, respBody)
	}

	var ollamaResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	if len(ollamaResp.Choices) == 0 {
		return nil, fmt.Errorf("ollama: no choices in response")
	}

	content := ""
	if ollamaResp.Choices[0].Message != nil {
		content = ollamaResp.Choices[0].Message.Content
	}

	tokenIn := 0
	tokenOut := 0
	var usage ai.ProviderUsage
	usage.RequestStartedAt = requestStarted
	if ollamaResp.Usage != nil {
		tokenIn = ollamaResp.Usage.PromptTokens
		tokenOut = ollamaResp.Usage.CompletionTokens
		usage = ollamaResp.Usage.ProviderUsage()
	}
	if !usage.Known {
		// Local Ollama models that do not report usage metadata: report a
		// character-count estimate explicitly marked as estimated so the
		// footer can render "≈N tok" instead of a fabricated authoritative
		// count — never a silent 0.
		promptLen := 0
		for _, m := range req.Messages {
			promptLen += len(m.Content)
		}
		tokenIn = promptLen / 4
		tokenOut = len(content) / 4
		usage.Known = true
		usage.Estimated = true
		usage.PromptTokens = tokenIn
		usage.CompletionTokens = tokenOut
		usage.TotalTokens = tokenIn + tokenOut
	}
	usage.CompletedAt = time.Now()
	if len(ollamaResp.Choices) > 0 {
		usage.FinishReason = ollamaResp.Choices[0].FinishReason
	}
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}

	response := &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		Usage:       usage,
	}
	StampResponseMetadata(response, "ollama", model, plan, ollamaResp.Choices[0].FinishReason)
	if response.Truncated {
		return response, ai.NewOutputTruncated("ollama", "length")
	}
	return response, nil
}

func (p *OllamaProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	requestStarted := time.Now()
	model := req.Model
	if model == "" {
		return nil, fmt.Errorf("ollama: no model assigned to target node (empty ModelBinding.ModelID)")
	}
	// Provider Routing Isolation: never trial-execute a namespaced
	// (vendor/model) ID against the local endpoint.
	if err := rejectNamespacedModel(model); err != nil {
		return nil, err
	}
	prepared, plan, err := PrepareContractRequest("ollama", req)
	if err != nil {
		return nil, err
	}
	req = prepared

	msgs := p.buildMessages(req)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := ollamaRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		MaxTokens:     &maxTokens,
		FormatSchema:  plan.NativeJSONSchema(),
		ExtraParams:   req.ExtraParams,
		Options: &struct {
			NumPredict  int     `json:"num_predict"`
			Temperature float64 `json:"temperature,omitempty"`
		}{NumPredict: maxTokens, Temperature: req.Temperature},
	}

	reqCtx, cancel := context.WithCancel(ctx)

	payload, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Title", "izen")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		return nil, NewProviderError("ollama", resp.StatusCode, respBody)
	}

	sr := &sseReader{body: resp.Body, cancel: cancel, reasoningHandler: req.ReasoningHandler}
	sr.usage.markRequestStarted(requestStarted)
	// Phase 6.4.4 Optimistic Prompt Token Invariant.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
	return &StreamResult{ReadCloser: sr, sr: sr, metadata: newResponseMetadata("ollama", model, plan)}, nil
}

type StreamResult struct {
	io.ReadCloser
	sr       *sseReader
	metadata ai.ResponseMetadata
}

func (r *StreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return normalizeUsageMetadata(r.sr.usage.Usage())
	}
	return ai.ProviderUsage{}
}

// FinishReason reports the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...), or "" if none was seen.
func (r *StreamResult) FinishReason() string {
	if r.sr != nil {
		return NormalizeFinishReason(r.sr.finishReason)
	}
	return ""
}

// ResponseMetadata returns the standardized contract/finish-reason wrapper for
// this stream.
func (r *StreamResult) ResponseMetadata() ai.ResponseMetadata {
	if r == nil {
		return ai.ResponseMetadata{}
	}
	return streamResponseMetadata(r.metadata, r.Usage(), r.FinishReason())
}

func (r *StreamResult) TruncationError() error {
	if r == nil {
		return nil
	}
	return streamTruncationError("ollama", r.FinishReason())
}

type sseReader struct {
	cancel           context.CancelFunc
	body             io.ReadCloser
	reader           *bufio.Reader
	closed           bool
	finalUsage       *usage
	finishReason     string
	reasoningHandler func(string) error

	// usage tracks cumulative token accounting (see streamUsageTracker).
	usage streamUsageTracker

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1).
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

func (s *sseReader) Usage() ai.ProviderUsage {
	return s.usage.Usage()
}

func (s *sseReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (s *sseReader) stopIdle() {
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
func (s *sseReader) drainTrailingUsage() {
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
			var chunk ollamaResponse
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
// down immediately so the UI timer stops at once.
func (s *sseReader) closeTerminal(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), reason)
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	s.closed = true
	_ = closeSSERequest(s.cancel, s.body, nil)
}

func (s *sseReader) Read(p []byte) (int, error) {
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

		var chunk ollamaResponse
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
				s.drainTrailingUsage()
				if s.lifecycle != nil {
					s.lifecycle.MarkClosed()
				}
				s.closed = true
				_ = closeSSERequest(s.cancel, s.body, nil)
				return n, nil
			}
			s.drainTrailingUsage()
			s.closeTerminal(chunk.Choices[0].FinishReason)
			return 0, io.EOF
		}

		if chunk.Choices[0].Delta != nil {
			d := chunk.Choices[0].Delta
			// Reasoning content (thinking process) is routed to the reasoning
			// handler only — never emitted into the response stream. Some
			// models report the field as "reasoning" instead of
			// "reasoning_content"; both are routed identically.
			reasoningText := d.ReasoningContent
			if reasoningText == "" {
				reasoningText = d.Reasoning
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
			if d.Content != "" {
				s.usage.recordOutput(len(d.Content))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(d.Content))
				}
				s.stopIdle()
				n := copy(p, d.Content)
				return n, nil
			}
		}
	}
}

func (s *sseReader) Close() error {
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

// ── Local SLM Bridge ──────────────────────────────────────────────────────────

// DiagnoseSystemPrompt enforces strict single-line output from the local SLM so
// the distilled diagnosis stays under 100 tokens with no markdown or fluff.
const DiagnoseSystemPrompt = `You are a root cause analysis engine. Analyze the given error log and respond with a SINGLE concise sentence identifying the root cause. Do not exceed 100 tokens. Do not use markdown, bullet points, or conversational text. Output ONLY the one-sentence diagnosis.`

type ollamaGenerateRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	System  string `json:"system,omitempty"`
	Stream  bool   `json:"stream,omitempty"`
	Options *struct {
		NumPredict int `json:"num_predict"`
	} `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Context  []int  `json:"context,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Generate calls Ollama's native /api/generate endpoint with streaming disabled.
// Returns the generated text or an error. Thread-safe via the underlying HTTP client.
func (p *OllamaProvider) Generate(ctx context.Context, system, prompt string) (string, error) {
	body := ollamaGenerateRequest{
		Model:  p.model,
		Prompt: prompt,
		System: system,
		Stream: false,
		Options: &struct {
			NumPredict int `json:"num_predict"`
		}{NumPredict: 100},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("ollama generate: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("ollama generate: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama generate: connection failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", NewProviderError("ollama", resp.StatusCode, respBody)
	}

	var genResp ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return "", fmt.Errorf("ollama generate: decode response: %w", err)
	}

	if genResp.Error != "" {
		return "", fmt.Errorf("ollama generate: model error: %s", genResp.Error)
	}

	return genResp.Response, nil
}
