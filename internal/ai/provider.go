package ai

import (
	"context"
	"io"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	Type string `json:"type"` // "json_object" or "text"
}

type Request struct {
	Model          string           `json:"model"`
	Messages       []Message        `json:"messages"`
	Stream         bool             `json:"stream"`
	System         string           `json:"-"` // Explicit system prompt (top-level for Anthropic, prepended for OpenAI-compatible)
	MaxTokens      int              `json:"-"` // 0 = use provider default
	Stop           []string         `json:"-"` // Optional stop sequences (e.g. [">>>>>>>"])
	Temperature    float64          `json:"-"` // 0 = use provider default
	ResponseFormat *ResponseFormat  `json:"response_format,omitempty"`
	Tools          []ToolDefinition `json:"-"` // Native LLM function calling tool definitions
	// Reasoning carries the resolved reasoning control (effort level, thinking
	// budget, CoT cap) produced by the decision engine. Providers translate it
	// into their native API payload (reasoning_effort / thinking.budget_tokens /
	// reasoning.{effort,max_tokens}). When nil or zero, no reasoning payload is
	// injected and the provider behaves exactly as before.
	Reasoning *ReasoningConfig `json:"-"`
	// ReasoningHandler receives reasoning/thinking content as it streams in,
	// separated from the main response. Providers call it with verbatim chunks
	// (the same text that reasoning_content / thinking_delta / thought frames
	// carry) so consumers can publish a reasoning stream without ever mixing it
	// into the response pipeline. When nil, reasoning content is silently
	// discarded by the SSE readers.
	ReasoningHandler func(chunk string) error
}

type Response struct {
	Content     string     `json:"content"`
	TokenInput  int        `json:"token_input"`
	TokenOutput int        `json:"token_output"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"` // Native LLM function calls from tool_calls finish_reason
}

type Provider interface {
	Name() string
	Execute(ctx context.Context, req Request) (*Response, error)
	ExecuteStream(ctx context.Context, req Request) (io.ReadCloser, error)
}

// FinishReasonProvider is implemented by stream results that can report the
// provider's terminal finish_reason (e.g. "stop", "length", "tool_calls").
// Consumers use it to detect responses truncated by the API's completion
// ceiling (finish_reason == "length") rather than finished naturally ("stop").
type FinishReasonProvider interface {
	FinishReason() string
}

type ProviderConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

type Manager struct {
	providers map[string]Provider
	defaultP  string
}

func NewManager() *Manager {
	return &Manager{
		providers: make(map[string]Provider),
	}
}

func (m *Manager) Register(name string, p Provider) {
	m.providers[name] = p
}

func (m *Manager) Get(name string) (Provider, bool) {
	p, ok := m.providers[name]
	return p, ok
}

func (m *Manager) SetDefault(name string) {
	m.defaultP = name
}

func (m *Manager) Default() (Provider, bool) {
	return m.Get(m.defaultP)
}

func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.providers))
	for n := range m.providers {
		names = append(names, n)
	}
	return names
}

type StreamForwarder struct {
	closer  io.ReadCloser
	onChunk func(string)
	onDone  func(string)
	buf     strings.Builder
}

func NewStreamForwarder(closer io.ReadCloser, onChunk func(string), onDone func(string)) *StreamForwarder {
	return &StreamForwarder{
		closer:  closer,
		onChunk: onChunk,
		onDone:  onDone,
	}
}

func (sf *StreamForwarder) Read(p []byte) (int, error) {
	n, err := sf.closer.Read(p)
	if n > 0 {
		chunk := string(p[:n])
		sf.buf.WriteString(chunk)
		if sf.onChunk != nil {
			sf.onChunk(chunk)
		}
	}
	if err == io.EOF {
		if sf.onDone != nil {
			sf.onDone(sf.buf.String())
		}
	}
	return n, err
}

func (sf *StreamForwarder) Close() error {
	return sf.closer.Close()
}
