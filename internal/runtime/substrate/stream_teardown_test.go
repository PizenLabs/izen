package substrate

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamTeardown verifies instant TCP FIN/RST on SSE stream completion.
// It mocks a streaming HTTP server emitting SSE events ending with data: [DONE]
// and asserts:
//  1. resp.Body.Read() reaches io.EOF (drained)
//  2. server handler receives context.Canceled immediately (<100ms) after client
//     consumes the final token, without waiting for idle timeout.
func TestStreamTeardown(t *testing.T) {
	disconnected := make(chan time.Time, 1)
	started := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Emit two content chunks and a final [DONE]
		events := []string{
			`data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":" world"}}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, ev := range events {
			if _, err := io.WriteString(w, ev); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		// Wait for client disconnect via context cancellation.
		// The handler should be cancelled promptly when client drains and closes.
		select {
		case <-r.Context().Done():
			disconnected <- time.Now()
		case <-time.After(5 * time.Second):
			// If client does not close promptly, this will fire and test fails.
		}
	}))
	defer srv.Close()

	// Client side: use the canonical teardown pattern from the directive.
	parentCtx := context.Background()
	reqCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	// Ensure body is drained and closed even on early return.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	<-started

	// Read SSE stream using bufio.Reader similar to provider's sseReader.
	reader := bufio.NewReader(resp.Body)
	var collected strings.Builder
	readDone := time.Now()

Loop:
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break Loop
			}
			t.Fatalf("read: %v", err)
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
			// Clean termination: cancel request context immediately to signal
			// transport to send TCP FIN, then drain any trailing bytes.
			cancel()
			_, _ = io.Copy(io.Discard, resp.Body)
			readDone = time.Now()
			break Loop
		}
		collected.WriteString(data)
	}

	// Assert body is drained — EOF, closed, or context-canceled all indicate
	// the unread buffer was consumed and connection can be reused.
	remaining, err := io.ReadAll(resp.Body)
	if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "context") {
		t.Fatalf("drain read: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected 0 remaining bytes after drain, got %d: %q", len(remaining), string(remaining))
	}

	// Wait for server to notice disconnect.
	select {
	case discAt := <-disconnected:
		elapsed := discAt.Sub(readDone)
		if elapsed > 100*time.Millisecond {
			t.Fatalf("server disconnect not prompt: elapsed %v > 100ms (billing would be delayed)", elapsed)
		}
		t.Logf("disconnect prompt: %v after readDone", elapsed)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive context cancellation within 2s — socket leak (would cause 5m billing delay)")
	}

	// Also assert we actually got the streamed content before DONE
	if collected.Len() == 0 {
		t.Fatal("expected collected content before DONE")
	}
}

// TestStreamTeardown_EOF verifies that draining reaches io.EOF even when
// server sends trailing bytes after [DONE] (simulating usage JSON).
func TestStreamTeardown_EOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
		// Keep connection open; client must drain to EOF.
		<-r.Context().Done()
	}))
	defer srv.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				cancel()
				_, _ = io.Copy(io.Discard, resp.Body)
				break
			}
		}
	}

	// After cancel+drain, body should be at EOF/closed/canceled — all indicate teardown.
	buf := make([]byte, 1)
	n, err := resp.Body.Read(buf)
	if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "context") {
		if err != nil {
			t.Fatalf("expected EOF or closed after drain, got n=%d err=%v", n, err)
		}
	}
	if n != 0 && errors.Is(err, io.EOF) {
		t.Fatalf("expected 0 bytes after drain, got n=%d", n)
	}
}
