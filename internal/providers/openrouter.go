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
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ErrOpenRouterAuth is returned when OpenRouter authentication fails (HTTP 401
// or missing API key). The UI layer detects this sentinel error via errors.Is
// and displays a clear actionable banner instead of a raw HTTP status message.
var ErrOpenRouterAuth = errors.New("openrouter: authorization failed (HTTP 401): invalid or missing OPENROUTER_API_KEY — check your environment variables or run: export OPENROUTER_API_KEY=<your_key>")

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
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := openrouterRequest{
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
		return nil, fmt.Errorf("openrouter: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openrouter: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen")
	httpReq.Header.Set("X-Title", "izen")
	httpReq.Header.Set("X-Description", "AI amplifies human judgment. Humans remain in control.")
	httpReq.Header.Set("X-Categories", "cli-agent")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: do: %w", err)
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
		content = openaiResp.Choices[0].Message.Content
		if openaiResp.Choices[0].FinishReason == "tool_calls" && len(openaiResp.Choices[0].Message.ToolCalls) > 0 {
			for _, tc := range openaiResp.Choices[0].Message.ToolCalls {
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
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	msgs := p.buildMessages(req)

	body := openrouterRequest{
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
		return nil, fmt.Errorf("openrouter: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openrouter: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen")
	httpReq.Header.Set("X-Title", "izen")
	httpReq.Header.Set("X-Description", "AI amplifies human judgment. Humans remain in control.")
	httpReq.Header.Set("X-Categories", "cli-agent")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: do: %w", err)
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

	// Step 4: strip trailing system messages.
	for len(msgs) > 0 && msgs[len(msgs)-1].Role == "system" {
		msgs = msgs[:len(msgs)-1]
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
	Role      string               `json:"role"`
	Content   string               `json:"content"`
	ToolCalls []openrouterToolCall `json:"tool_calls,omitempty"`
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
	if r.sr != nil && r.sr.finalUsage != nil {
		return r.sr.finalUsage.PromptTokens, r.sr.finalUsage.CompletionTokens
	}
	return 0, 0
}

type openrouterSSEReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *openrouterUsage

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
			if delta.ReasoningContent != "" {
				reasoning := []byte(ReasoningSentinel + delta.ReasoningContent + ReasoningSentinel)
				n := copy(p, reasoning)
				if n < len(reasoning) {
					s.pending = reasoning[n:]
				}
				return n, nil
			}
			if delta.Content != "" {
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
					n := copy(p, all)
					if n < len(all) {
						s.pending = all[n:]
					}
					return n, nil
				}
			}
		}

		if chunk.Choices[0].FinishReason != "" {
			continue
		}
	}
}

func (s *openrouterSSEReader) Close() error {
	s.closed = true
	return s.body.Close()
}
