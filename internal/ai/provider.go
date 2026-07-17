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
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	System         string          `json:"-"` // Explicit system prompt (top-level for Anthropic, prepended for OpenAI-compatible)
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type Response struct {
	Content     string `json:"content"`
	TokenInput  int    `json:"token_input"`
	TokenOutput int    `json:"token_output"`
}

type Provider interface {
	Name() string
	Execute(ctx context.Context, req Request) (*Response, error)
	ExecuteStream(ctx context.Context, req Request) (io.ReadCloser, error)
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
