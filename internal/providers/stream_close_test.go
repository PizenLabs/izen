package providers

import (
	"errors"
	"io"
	"testing"
	"time"
)

// readWithTimeout performs one Read with a hard timeout: a reader that
// waits for a terminal frame ([DONE]/message_stop) that never arrives
// would block forever and fail the test instead of hanging the suite.
func readWithTimeout(t *testing.T, r io.Reader, buf []byte, timeout time.Duration) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Read(buf)
		done <- result{n, err}
	}()
	select {
	case res := <-done:
		return res.n, res.err
	case <-time.After(timeout):
		t.Fatalf("stream did not terminate promptly on finish_reason (waited %v)", timeout)
		return 0, nil
	}
}

// TestStreamClosesOnFinishReasonWithoutDone pins the Stream Terminal
// Invariant (Phase 6.4.1): every SSE reader MUST close its output channel
// immediately upon parsing finish_reason != "", even when the transport
// never delivers [DONE]. The pipe stays open (no EOF from below); only an
// explicit channel closure lets the UI timer stop at 0.0 tok/s drift.
func TestStreamClosesOnFinishReasonWithoutDone(t *testing.T) {
	const timeout = 3 * time.Second
	// emulationCancel mirrors production transport teardown: in live
	// streams cancel aborts the HTTP transport, which unblocks the
	// reader's drain. Closing the pipe's write end emulates that abort
	// without delivering a [DONE] frame or an underlying EOF first.
	emulationCancel := func(pw *io.PipeWriter) func() {
		return func() { _ = pw.Close() }
	}
	openAICompat := func(name string, mk func(body io.ReadCloser, cancel func()) io.ReadCloser) {
		t.Run(name, func(t *testing.T) {
			pr, pw := io.Pipe()
			r := mk(pr, emulationCancel(pw))
			// io.Pipe is unbuffered: writes must run concurrently with
			// reads or the second Write blocks forever.
			go func() {
				_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
				_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
				// NOTE: no [DONE], pipe left open — the reader must still end.
			}()
			buf := make([]byte, 256)
			n, err := readWithTimeout(t, r, buf, timeout)
			if err != nil || string(buf[:n]) != "hi" {
				t.Fatalf("content = %q, %v", buf[:n], err)
			}
			n, err = readWithTimeout(t, r, buf, timeout)
			if n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("terminal read = %d, %v; want 0, EOF", n, err)
			}
			_ = r.Close()
			_ = pw.Close()
		})
	}

	openAICompat("openai", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &openaiSSEReader{body: body, cancel: cancel}
		return &OpenAIStreamResult{ReadCloser: sr, sr: sr}
	})
	openAICompat("groq", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &groqSSEReader{body: body, cancel: cancel}
		return &GroqStreamResult{ReadCloser: sr, sr: sr}
	})
	openAICompat("ollama", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &sseReader{body: body, cancel: cancel}
		return &StreamResult{ReadCloser: sr, sr: sr}
	})
	openAICompat("ninerouter", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &ninerouterSSEReader{body: body, cancel: cancel}
		return &NineRouterStreamResult{ReadCloser: sr, sr: sr}
	})
	openAICompat("opencode", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &opencodeSSEReader{body: body, cancel: cancel}
		return &OpenCodeStreamResult{ReadCloser: sr, sr: sr}
	})
	openAICompat("openrouter", func(body io.ReadCloser, cancel func()) io.ReadCloser {
		sr := &openrouterSSEReader{body: body, cancel: cancel}
		return &OpenRouterStreamResult{ReadCloser: sr, sr: sr}
	})

	t.Run("gemini", func(t *testing.T) {
		pr, pw := io.Pipe()
		sr := &geminiSSEReader{body: pr, cancel: emulationCancel(pw)}
		r := &GeminiStreamResult{ReadCloser: sr, sr: sr}
		go func() {
			_, _ = pw.Write([]byte("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"done\"}]}}]}\n\n"))
		}()
		buf := make([]byte, 256)
		n, err := readWithTimeout(t, r, buf, timeout)
		if err != nil || string(buf[:n]) != "done" {
			t.Fatalf("content = %q, %v", buf[:n], err)
		}
		n, err = readWithTimeout(t, r, buf, timeout)
		if n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("terminal read = %d, %v; want 0, EOF", n, err)
		}
		_ = r.Close()
		_ = pw.Close()
	})

	t.Run("claude", func(t *testing.T) {
		pr, pw := io.Pipe()
		sr := &claudeSSEReader{body: pr, cancel: emulationCancel(pw)}
		r := &ClaudeStreamResult{ReadCloser: sr, sr: sr}
		go func() {
			_, _ = pw.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
			_, _ = pw.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
			// NOTE: no message_stop, pipe left open — stop_reason ends it.
		}()
		buf := make([]byte, 256)
		n, err := readWithTimeout(t, r, buf, timeout)
		if err != nil || string(buf[:n]) != "ok" {
			t.Fatalf("content = %q, %v", buf[:n], err)
		}
		n, err = readWithTimeout(t, r, buf, timeout)
		if n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("terminal read = %d, %v; want 0, EOF", n, err)
		}
		if got := r.FinishReason(); got != "end_turn" {
			t.Fatalf("FinishReason() = %q, want end_turn", got)
		}
		_ = r.Close()
		_ = pw.Close()
	})
}
