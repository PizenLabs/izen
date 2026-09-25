package providers

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// g8UnexpectedEOFBody emits one complete SSE line and then fails the transport
// with io.ErrUnexpectedEOF. It models a proxy/socket cut after a partial
// provider response; no deadline or retry is needed to release the reader.
type g8UnexpectedEOFBody struct {
	data   []byte
	sent   bool
	closed chan struct{}
	once   sync.Once
}

func (b *g8UnexpectedEOFBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (b *g8UnexpectedEOFBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func g8ReadAllWithTimeout(t *testing.T, r io.Reader, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(r)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("SSE reader did not release after transport termination (waited %v)", timeout)
		return nil
	}
}

func g8ReaderFactory(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
	return &openrouterSSEReader{body: body, cancel: cancel}
}

func TestG8SSEUnexpectedEOFReleasesContextAndBody(t *testing.T) {
	// Keep the provider matrix explicit: every adapter owns an SSE reader and
	// must apply the same terminal cleanup rule.
	factories := []struct {
		name string
		make func(io.ReadCloser, context.CancelFunc) io.Reader
	}{
		{
			name: "openrouter",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &openrouterSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "openai",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &openaiSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "groq",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &groqSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "ollama",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &sseReader{body: body, cancel: cancel}
			},
		},
		{
			name: "ninerouter",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &ninerouterSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "opencode",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &opencodeSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "gemini",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &geminiSSEReader{body: body, cancel: cancel}
			},
		},
		{
			name: "claude",
			make: func(body io.ReadCloser, cancel context.CancelFunc) io.Reader {
				return &claudeSSEReader{body: body, cancel: cancel}
			},
		},
	}

	for _, tc := range factories {
		t.Run(tc.name, func(t *testing.T) {
			body := &g8UnexpectedEOFBody{
				data:   []byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"),
				closed: make(chan struct{}),
			}
			cancelled := make(chan struct{})
			var cancelOnce sync.Once
			cancel := func() { cancelOnce.Do(func() { close(cancelled) }) }
			reader := tc.make(body, cancel)

			err := g8ReadAllWithTimeout(t, reader, time.Second)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("read error = %v, want io.ErrUnexpectedEOF", err)
			}
			select {
			case <-cancelled:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("SSE reader did not cancel its request context")
			}
			select {
			case <-body.closed:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("SSE reader did not close its response body")
			}
		})
	}
}

func TestG8SSEDoneReleasesContextAndBody(t *testing.T) {
	// blockingAfterDataBody keeps the server-side transport open after the
	// semantic terminal sentinel. A correct reader must not drain it.
	body := &blockingAfterDataBody{
		data:   []byte("data: [DONE]\n\n"),
		closed: make(chan struct{}),
	}
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	cancel := func() { cancelOnce.Do(func() { close(cancelled) }) }
	reader := g8ReaderFactory(body, cancel)

	if err := g8ReadAllWithTimeout(t, reader, time.Second); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v, want clean EOF", err)
	}
	select {
	case <-cancelled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("[DONE] did not cancel the request context")
	}
	select {
	case <-body.closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("[DONE] did not close the response body")
	}
}

func TestG8FinishReasonLengthAlwaysUsesCanonicalTruncationError(t *testing.T) {
	for _, provider := range []string{"openrouter", "openai", "groq", "ollama", "gemini", "claude", "opencode", "ninerouter"} {
		t.Run(provider, func(t *testing.T) {
			err := streamTruncationError(provider, "length")
			if !errors.Is(err, ErrOutputTruncated) {
				t.Fatalf("streamTruncationError = %v, want ErrOutputTruncated", err)
			}
		})
	}
}
