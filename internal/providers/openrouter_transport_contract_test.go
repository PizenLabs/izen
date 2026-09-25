package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// blockingAfterDataBody models the failure that caused the original
// deadlock: it delivers a complete terminal SSE event and then keeps the
// transport open until Close is called. Any io.Copy-style drain blocks here.
type blockingAfterDataBody struct {
	data   []byte
	sent   bool
	closed chan struct{}
	once   sync.Once
}

func (b *blockingAfterDataBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	<-b.closed
	return 0, io.EOF
}

func (b *blockingAfterDataBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestOpenRouterSSEReaderTerminatesWithoutTransportDrain(t *testing.T) {
	body := &blockingAfterDataBody{
		data:   []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
		closed: make(chan struct{}),
	}
	sr := &openrouterSSEReader{body: body}
	result := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if n, err := readWithTimeout(t, result, make([]byte, 128), time.Second); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("terminal read = %d, %v; want EOF", n, err)
	}
	select {
	case <-body.closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("SSE body was not closed at finish_reason")
	}
}

func TestOpenRouterSSEReaderHandlesDoneWithoutTransportDrain(t *testing.T) {
	body := &blockingAfterDataBody{
		data:   []byte("data: [DONE]\n\n"),
		closed: make(chan struct{}),
	}
	sr := &openrouterSSEReader{body: body}
	result := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if n, err := readWithTimeout(t, result, make([]byte, 128), time.Second); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("DONE read = %d, %v; want EOF", n, err)
	}
	select {
	case <-body.closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("SSE body was not closed at [DONE]")
	}
}

func TestClaudeAndGeminiReadersHandleDoneWithoutTransportDrain(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		body := &blockingAfterDataBody{data: []byte("data: [DONE]\n\n"), closed: make(chan struct{})}
		sr := &claudeSSEReader{body: body}
		result := &ClaudeStreamResult{ReadCloser: sr, sr: sr}
		if n, err := readWithTimeout(t, result, make([]byte, 128), time.Second); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("DONE read = %d, %v; want EOF", n, err)
		}
		select {
		case <-body.closed:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Claude SSE body was not closed at [DONE]")
		}
	})

	t.Run("gemini", func(t *testing.T) {
		body := &blockingAfterDataBody{data: []byte("data: [DONE]\n\n"), closed: make(chan struct{})}
		sr := &geminiSSEReader{body: body}
		result := &GeminiStreamResult{ReadCloser: sr, sr: sr}
		if n, err := readWithTimeout(t, result, make([]byte, 128), time.Second); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("DONE read = %d, %v; want EOF", n, err)
		}
		select {
		case <-body.closed:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Gemini SSE body was not closed at [DONE]")
		}
	})
}

func TestOpenRouterExecuteReturnsTypedTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"partial output"},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":8,"total_tokens":12}}`)
	}))
	defer server.Close()

	p := NewOpenRouterProvider("test-key", "openai/gpt-4o", server.URL)
	resp, err := p.Execute(context.Background(), ai.Request{Model: "openai/gpt-4o"})
	if resp == nil {
		t.Fatal("truncated response must be retained for usage telemetry")
	}
	if !errors.Is(err, ai.ErrOutputTruncated) {
		t.Fatalf("error = %v, want ai.ErrOutputTruncated", err)
	}
	if !resp.Truncated || resp.FinishReason != "length" {
		t.Fatalf("response metadata = truncated:%v finish:%q", resp.Truncated, resp.FinishReason)
	}
}

func TestOpenRouterStreamResultReportsTypedTruncation(t *testing.T) {
	sr := &openrouterSSEReader{
		body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")),
	}
	result := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}
	_, _ = io.ReadAll(result)
	if err := result.TruncationError(); !errors.Is(err, ai.ErrOutputTruncated) {
		t.Fatalf("TruncationError = %v, want ai.ErrOutputTruncated", err)
	}
}
