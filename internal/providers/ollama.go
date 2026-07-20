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

	"github.com/PizenLabs/izen/internal/ai"
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
		client:  &http.Client{},
	}
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	Format    string          `json:"format,omitempty"` // "json" for structured output
	MaxTokens *int            `json:"max_tokens,omitempty"`
	Options   *struct {
		NumPredict int `json:"num_predict"`
	} `json:"options,omitempty"`
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
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	maxTokens := 4096
	body := ollamaRequest{
		Model:     model,
		Messages:  msgs,
		Stream:    false,
		MaxTokens: &maxTokens,
		Options: &struct {
			NumPredict int `json:"num_predict"`
		}{NumPredict: 4096},
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
		body.Format = "json"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
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
	if ollamaResp.Usage != nil {
		tokenIn = ollamaResp.Usage.PromptTokens
		tokenOut = ollamaResp.Usage.CompletionTokens
	}
	if tokenIn == 0 && tokenOut == 0 {
		promptLen := 0
		for _, m := range req.Messages {
			promptLen += len(m.Content)
		}
		tokenIn = promptLen / 4
		tokenOut = len(content) / 4
	}

	return &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
	}, nil
}

func (p *OllamaProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	msgs := p.buildMessages(req)

	maxTokens := 4096
	body := ollamaRequest{
		Model:     model,
		Messages:  msgs,
		Stream:    true,
		MaxTokens: &maxTokens,
		Options: &struct {
			NumPredict int `json:"num_predict"`
		}{NumPredict: 4096},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
	}

	sr := &sseReader{body: resp.Body}
	return &StreamResult{ReadCloser: sr, sr: sr}, nil
}

type StreamResult struct {
	io.ReadCloser
	sr *sseReader
}

func (r *StreamResult) Usage() (input, output int) {
	if r.sr != nil && r.sr.finalUsage != nil {
		return r.sr.finalUsage.PromptTokens, r.sr.finalUsage.CompletionTokens
	}
	return 0, 0
}

type sseReader struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	closed     bool
	finalUsage *usage
}

func (s *sseReader) Usage() (input, output int) {
	if s.finalUsage != nil {
		return s.finalUsage.PromptTokens, s.finalUsage.CompletionTokens
	}
	return 0, 0
}

func (s *sseReader) Read(p []byte) (int, error) {
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

		var chunk ollamaResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
		}

		if chunk.Choices[0].Delta != nil && chunk.Choices[0].Delta.Content != "" {
			n := copy(p, chunk.Choices[0].Delta.Content)
			return n, nil
		}

		if chunk.Choices[0].FinishReason != "" {
			continue
		}
	}
}

func (s *sseReader) Close() error {
	s.closed = true
	return s.body.Close()
}

// ── Local SLM Bridge ──────────────────────────────────────────────────────────

// DiagnoseSystemPrompt enforces strict single-line output from the local SLM so
// the distilled diagnosis stays under 100 tokens with no markdown or fluff.
const DiagnoseSystemPrompt = `You are a root cause analysis engine. Analyze the given error log and respond with a SINGLE concise sentence identifying the root cause. Do not exceed 100 tokens. Do not use markdown, bullet points, or conversational text. Output ONLY the one-sentence diagnosis.`

type ollamaGenerateRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	System  string `json:"system,omitempty"`
	Stream  bool   `json:"stream"`
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama generate: status %d: %s", resp.StatusCode, string(respBody))
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
