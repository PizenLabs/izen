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
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ErrOpenRouterAuth is returned when OpenRouter authentication fails (HTTP 401
// or missing API key). The UI layer detects this sentinel error via errors.Is
// and displays a clear actionable banner instead of a raw HTTP status message.
var ErrOpenRouterAuth = errors.New("openrouter: authorization failed (HTTP 401): invalid or missing OPENROUTER_API_KEY — check your environment variables or run: export OPENROUTER_API_KEY=<your_key>")

// DefaultOpenRouterModel is the safe fallback model ID used when a request
// carries no model resolvable to OpenRouter's vendor/model schema.
const DefaultOpenRouterModel = "anthropic/claude-3.5-sonnet"

// openRouterModelIDRe matches OpenRouter's vendor/model-id schema: a non-empty
// vendor component, a single "/", and a non-empty model component. Vendors
// carry hyphens and digits (meta-llama, gpt-4o) and models may carry
// ":free"-style variants. An ID without a vendor prefix (e.g. Ollama's
// "qwen2.5-coder:7b") is rejected by the API with HTTP 400 "not a valid model
// ID" and must be mapped before dispatch.
var openRouterModelIDRe = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// SanitizeModelForOpenRouter maps a model ID onto a valid OpenRouter model ID.
// Local/Ollama IDs (e.g. "qwen2.5-coder:7b") carry no vendor prefix and are
// rejected by OpenRouter with status 400; they are remapped to the provider's
// default model. Returns "" only when neither the requested nor the fallback
// model is valid for OpenRouter.
func SanitizeModelForOpenRouter(model, fallback string) string {
	for _, candidate := range []string{model, fallback} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && openRouterModelIDRe.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

// resolveModel returns the effective model ID for a request, remapping any
// local/non-OpenRouter ID onto the provider default so the API never rejects
// the payload with HTTP 400. Returns an error when no valid ID is available.
func (p *OpenRouterProvider) resolveModel(reqModel string) (string, error) {
	model := p.model
	if reqModel != "" {
		model = reqModel
	}
	if model = SanitizeModelForOpenRouter(model, p.model); model == "" {
		return "", fmt.Errorf("openrouter: no valid model ID configured (got %q)", p.model)
	}
	return model, nil
}

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
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, false)

	resp, err := p.doChatRequest(ctx, key, body, false)
	if err != nil {
		return nil, err
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
		msg := openaiResp.Choices[0].Message
		content = firstUsableContent(msg.Content, msg.Reasoning, msg.ReasoningContent)
		if openaiResp.Choices[0].FinishReason == "tool_calls" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
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
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, true)

	resp, err := p.doChatRequest(ctx, key, body, true)
	if err != nil {
		return nil, err
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
	return cleanMessages(msgs)
}

// cleanMessages validates and sanitizes a message sequence before it is
// sent to the OpenRouter API. It prevents HTTP 400 responses caused by
// empty content, consecutive same-role messages, or structural violations.
// Rules applied in order:
//  1. Drop messages with empty content (after sanitization).
//  2. Merge consecutive messages with the same role by joining with "\n".
//  3. Strip leading assistant messages (no response without a prior user prompt).
//  4. Ensure the final message is not a system message (remove trailing system messages).
//  5. Sliding window: keep at most the last 30 messages to prevent unbounded
//     token growth across long sessions.
func cleanMessages(msgs []openrouterMessage) []openrouterMessage {
	// Step 1: drop empty content.
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.Content != "" {
			filtered = append(filtered, m)
		}
	}
	msgs = filtered

	// Step 2: merge consecutive same-role messages.
	merged := make([]openrouterMessage, 0, len(msgs))
	for i, m := range msgs {
		if i > 0 && m.Role == msgs[i-1].Role && m.Role != "system" {
			last := &merged[len(merged)-1]
			last.Content += "\n" + m.Content
			continue
		}
		merged = append(merged, m)
	}
	msgs = merged

	// Step 3: strip leading assistant messages.
	for len(msgs) > 0 && msgs[0].Role == "assistant" {
		msgs = msgs[1:]
	}

	// Step 4: strip trailing system messages, but preserve at least one
	// message (the head system prompt at index 0) so the IZEN identity and
	// user name context is never dropped.
	for len(msgs) > 1 && msgs[len(msgs)-1].Role == "system" {
		msgs = msgs[:len(msgs)-1]
	}

	// Step 5: sliding window truncation.
	// Keep system + user + assistant messages bounded so the payload never
	// explodes across long sessions. Preserve system message at index 0.
	const maxMessages = 30
	if len(msgs) > maxMessages {
		head := 1
		tail := msgs[head:]
		if len(tail) > maxMessages {
			tail = tail[len(tail)-maxMessages:]
		}
		msgs = append(msgs[:head], tail...)
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
	// Reasoning carries OpenRouter's provider-agnostic reasoning control. It
	// is injected from the dynamically resolved effort directive; a nil value
	// omits the field entirely.
	Reasoning *openrouterReasoning `json:"reasoning,omitempty"`
}

// openrouterReasoning is OpenRouter's reasoning control payload: an optional
// qualitative effort (low/medium/high) and an optional max_tokens reasoning
// cap. OpenRouter relays whichever field is set to the underlying provider's
// native mechanism.
type openrouterReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// reasoningFor builds the OpenRouter reasoning payload from the resolved
// effort directive. The qualitative effort maps to reasoning.effort; the CoT
// cap and budget map to reasoning.max_tokens. A nil request reasoning config
// yields nil (field omitted, the pre-existing behavior).
func reasoningFor(req ai.Request) *openrouterReasoning {
	if req.Reasoning == nil {
		return nil
	}
	r := &openrouterReasoning{Effort: req.Reasoning.Level}
	switch {
	case req.Reasoning.CoTLimit > 0:
		r.MaxTokens = req.Reasoning.CoTLimit
	case req.Reasoning.BudgetTokens > 0:
		r.MaxTokens = req.Reasoning.BudgetTokens
	}
	if r.Effort == "" && r.MaxTokens == 0 {
		return nil
	}
	return r
}

// openRouterNonReasoningModels lists model-family markers known to reject
// OpenRouter's provider-agnostic reasoning payload. Injecting the reasoning
// object into these models makes the gateway reject the entire request with
// HTTP 400 (tokens are billed at the gateway before the stream dies). Add a
// family here when it is observed to reject the reasoning schema so the
// payload is sanitized up front instead of relying on the retry in
// doChatRequest.
var openRouterNonReasoningModels = []string{
	"gemma",
}

// openRouterModelSupportsReasoning reports whether the target model accepts
// OpenRouter's reasoning control. Reasoning is only injected for models known
// to expose a native reasoning mechanism; unknown models are treated as
// reasoning-capable and fall back to the HTTP 400 retry in doChatRequest when
// the gateway disagrees, so a false positive never fails the request.
func openRouterModelSupportsReasoning(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	for _, marker := range openRouterNonReasoningModels {
		if strings.Contains(name, marker) {
			return false
		}
	}
	return true
}

// buildRequest assembles the OpenRouter chat-completion payload for a request.
// The reasoning control is injected only when the target model supports the
// reasoning schema (see openRouterModelSupportsReasoning), so a non-reasoning
// model never receives a payload the gateway rejects with HTTP 400.
func (p *OpenRouterProvider) buildRequest(model string, msgs []openrouterMessage, req ai.Request, stream bool) openrouterRequest {
	body := openrouterRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.Stop,
		Stream:      stream,
		Reasoning:   reasoningFor(req),
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if !openRouterModelSupportsReasoning(model) {
		body.Reasoning = nil
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
	return body
}

// doChatRequest POSTs a chat-completion payload to the OpenRouter gateway and
// returns the HTTP response (the caller owns the body). When the payload
// carried a reasoning control and the gateway rejects it with HTTP 400, the
// reasoning field is stripped and the request is retried exactly once: some
// OpenRouter models do not accept the reasoning schema, and OpenRouter bills
// tokens at the gateway before the stream fails. Stripping reasoning lets the
// turn complete without reasoning rather than failing the whole request.
func (p *OpenRouterProvider) doChatRequest(ctx context.Context, key string, body openrouterRequest, stream bool) (*http.Response, error) {
	attempt := func(b openrouterRequest) (*http.Response, error) {
		payload, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("openrouter: marshal: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("openrouter: new request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key)
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-OpenRouter-Categories", "cli-agent")
		httpReq.Header.Set("X-Description", "AI amplifies human judgment. Humans remain in control.")
		return p.client.Do(httpReq)
	}

	resp, err := attempt(body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: do: %w", err)
	}
	if resp.StatusCode == http.StatusBadRequest && body.Reasoning != nil {
		// A non-reasoning model rejected the reasoning schema. Discard the
		// rejected response, strip the reasoning payload and retry once.
		_ = resp.Body.Close()
		body.Reasoning = nil
		resp, err = attempt(body)
		if err != nil {
			return nil, fmt.Errorf("openrouter: do: %w", err)
		}
	}
	return resp, nil
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
	Role             string               `json:"role"`
	Content          string               `json:"content"`
	Reasoning        string               `json:"reasoning,omitempty"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []openrouterToolCall `json:"tool_calls,omitempty"`
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
	Reasoning        string                `json:"reasoning,omitempty"`
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
	if r.sr != nil {
		return r.sr.usage.Usage()
	}
	return 0, 0
}

// FinishReason returns the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...). It reports "" when the stream ended
// before any finish_reason chunk was seen. Consumers use it to distinguish a
// natural completion ("stop") from a response truncated by the completion
// ceiling ("length").
func (r *OpenRouterStreamResult) FinishReason() string {
	if r.sr != nil {
		return r.sr.finishReason
	}
	return ""
}

type openrouterSSEReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *openrouterUsage

	// usage tracks cumulative token accounting: the authoritative provider
	// usage when a usage chunk arrives, plus a character-count estimate when
	// the stream is interrupted (context deadline) before that chunk.
	usage streamUsageTracker

	// finishReason records the terminal finish_reason chunk observed on the
	// stream ("stop", "length", "tool_calls", ...). It is surfaced to callers
	// via OpenRouterStreamResult.FinishReason() so consumers can detect
	// responses truncated by the completion ceiling (finish_reason: "length")
	// instead of assuming the response ended naturally.
	finishReason string

	// pending holds bytes produced by a parsed SSE event that did not fit
	// into the caller's buffer on a previous Read() call. Read() must never
	// silently drop bytes just because len(p) was smaller than one logical
	// unit (a sentinel-wrapped reasoning/content/tool-call chunk) — doing so
	// previously truncated large reasoning bursts and tool-call argument
	// JSON mid-stream, dropping the closing sentinel along with the tail of
	// the data. That desynced the sentinel parser downstream (an opened-but-
	// never-closed \x00RSNG\x00 block), which caused partially-streamed
	// reasoning text to leak into the visible answer instead of staying in
	// the Thinking Panel. Buffering the remainder here and draining it on
	// the next Read() call restores normal io.Reader semantics regardless
	// of the caller's buffer size.
	pending []byte
}

func (s *openrouterSSEReader) Read(p []byte) (int, error) {
	// Drain any bytes left over from a previous parsed event before doing
	// any new work. This is what makes Read() safe to call with any buffer
	// size without losing data.
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

		var chunk openrouterResponse
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
			// Reasoning content: emit wrapped in sentinel so the UI can
			// separate it from the message content buffer. Any bytes that
			// don't fit in p are held in s.pending and drained on the next
			// Read() call — never dropped, and never split without keeping
			// the remainder for delivery (see s.pending doc comment).
			// Some models/gateways report reasoning under "reasoning" instead
			// of "reasoning_content"; both are routed identically.
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				s.usage.recordOutput(len(reasoningText))
				reasoning := []byte(ReasoningSentinel + reasoningText + ReasoningSentinel)
				n := copy(p, reasoning)
				if n < len(reasoning) {
					s.pending = reasoning[n:]
				}
				return n, nil
			}
			if delta.Content != "" {
				s.usage.recordOutput(len(delta.Content))
				content := []byte(delta.Content)
				n := copy(p, content)
				if n < len(content) {
					s.pending = content[n:]
				}
				return n, nil
			}
			if len(delta.ToolCalls) > 0 {
				// Concatenate all tool-call deltas for this event into one
				// pending buffer rather than returning after the first
				// truncated chunk and silently discarding the rest — a
				// dropped tail here corrupts the tool-call JSON and causes
				// the downstream json.Unmarshal in the consumer to fail
				// silently, losing the whole tool call (and, with it, any
				// file mutation it carried).
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

		if chunk.Choices[0].FinishReason != "" {
			s.finishReason = chunk.Choices[0].FinishReason
			continue
		}
	}
}

func (s *openrouterSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
