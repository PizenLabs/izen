package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
)

type sseReader struct {
	body      io.ReadCloser
	reader    *bufio.Reader
	closed    bool
	closeOnce sync.Once
}

func newSSEReader(body io.ReadCloser) *sseReader {
	return &sseReader{
		body:   body,
		reader: bufio.NewReader(body),
	}
}

func (r *sseReader) ReadEvent() (string, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return "", err
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
			r.closed = true
			// ZERO-DEFER: drain and close synchronously the instant [DONE]
			// is parsed, so the HTTP session is torn down before any
			// outer pipeline join. Keep-Alive pooling preserved via
			// Discard+Close returning the TCP conn to the idle pool.
			r.closeOnce.Do(func() {
				_, _ = io.Copy(io.Discard, r.body)
				_ = r.body.Close()
			})
			return "", io.EOF
		}

		return data, nil
	}
}

func (r *sseReader) Close() error {
	r.closed = true
	var err error
	r.closeOnce.Do(func() {
		_, _ = io.Copy(io.Discard, r.body)
		err = r.body.Close()
	})
	return err
}

func newOpenAIStreamReader(body io.ReadCloser) *openAIStreamReader {
	return &openAIStreamReader{
		body:      body,
		reader:    newSSEReader(body),
		lifecycle: dprovider.NewStreamLifecycle(),
	}
}

// armIdle starts the zero-token idle watchdog exactly once per stream: if
// no tokens are emitted within dprovider.StreamIdleTimeout while waiting
// on the terminal frame, the stream context is force-cancelled so the UI
// timer stops instead of drifting at 0.0 tok/s.
func (r *openAIStreamReader) armIdle() {
	r.startOnce.Do(func() {
		lc := r.lifecycle
		if lc == nil {
			lc = dprovider.NewStreamLifecycle()
			r.lifecycle = lc
		}
		r.idleStop = dprovider.ArmIdleDeadline(r.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (r *openAIStreamReader) stopIdle() {
	if r.idleStop != nil {
		stop := r.idleStop
		r.idleStop = nil
		stop()
	}
}

func (r *openAIStreamReader) noteTokens(n int) {
	if r.lifecycle != nil {
		r.lifecycle.NoteTokens(n)
	}
	r.stopIdle()
}

func (r *openAIStreamReader) ReadChunk() (openAIChunk, error) {
	r.armIdle()
	// Stream Terminal Invariant (Phase 6.4.1): a parsed finish_reason
	// closes the channel — the next ReadChunk after a terminal chunk
	// returns EOF immediately without waiting for [DONE].
	// Phase 6.4.2 Bounded Usage Drain (Telemetry Accuracy Invariant): before
	// tearing down, allow up to dprovider.UsageDrainTimeout (50ms) for a
	// trailing usage-only chunk (choices: [] with usage) so billed tokens
	// are captured by the caller instead of dropped at teardown.
	if r.terminal {
		if drained, ok := r.drainTrailingUsage(); ok {
			return drained, nil
		}
		if r.lifecycle != nil {
			r.lifecycle.MarkClosed()
		}
		r.stopIdle()
		if r.cancel != nil {
			r.cancel()
		}
		_, _ = io.Copy(io.Discard, r.body)
		return openAIChunk{}, io.EOF
	}
	for {
		data, err := r.reader.ReadEvent()
		if err != nil {
			return openAIChunk{}, err
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		// Telemetry Accuracy Invariant: usage-only chunks are returned to
		// the caller (which records chunk.Usage) — never swallowed here.
		if len(chunk.Choices) > 0 && dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason) {
			r.terminal = true
		}
		return chunk, nil
	}
}

// drainTrailingUsage polls for one trailing SSE event for up to
// dprovider.UsageDrainTimeout after a terminal finish_reason. It reports
// the event when it carries a usage object (choices may be empty); any
// other outcome (timeout, [DONE], EOF, parse failure, usage-free event)
// reports ok=false and the caller tears the channel down. Single-threaded:
// ReadChunk is the stream's only reader, so polling the buffered reader
// never races. Only ONE drained event is ever returned — terminal stays
// set, so the following ReadChunk still terminates.
func (r *openAIStreamReader) drainTrailingUsage() (drained openAIChunk, ok bool) {
	if r.reader == nil || r.reader.reader == nil {
		return openAIChunk{}, false
	}
	buf := r.reader.reader
	deadline := time.Now().Add(dprovider.UsageDrainTimeout)
	for {
		if n := buf.Buffered(); n > 0 {
			// Only consume when a COMPLETE line is buffered: ReadString
			// blocks for '\n', so a partial line with no more data
			// arriving would hang the drain past its bound. Peek is
			// non-blocking for already-buffered bytes. Blank SSE
			// separators are skipped — the usage event follows them.
			// (ReadEvent is deliberately NOT used here: it blocks past
			// a lone buffered blank line waiting for the next event.)
			peeked, err := buf.Peek(n)
			if err != nil {
				return openAIChunk{}, false
			}
			if !bytes.Contains(peeked, []byte{'\n'}) {
				if !time.Now().Before(deadline) {
					return openAIChunk{}, false
				}
				time.Sleep(2 * time.Millisecond)
				continue
			}
			line, err := buf.ReadString('\n')
			if err != nil {
				return openAIChunk{}, false
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
				return openAIChunk{}, false
			}
			var chunk openAIChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return openAIChunk{}, false
			}
			if chunk.Usage == nil {
				return openAIChunk{}, false
			}
			return chunk, true
		}
		if !time.Now().Before(deadline) {
			return openAIChunk{}, false
		}
		time.Sleep(2 * time.Millisecond)
	}
}
