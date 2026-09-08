// Package stream — idle-timeout reader.
//
// Post-TTFT streaming must never be governed by the fixed total request
// deadline (the historical 15s context that expired mid-generation). Once
// the first byte arrives the stream is alive and only an inter-token idle
// gap proves it stalled. IdleTimeoutReader enforces exactly that: every
// successful Read carrying bytes resets the idle deadline; if no chunk
// arrives within the idle window the underlying body is force-closed so a
// blocked Read unblocks with an identifiable error instead of hanging until
// the generous stream-max context fires.
package stream

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ErrStreamIdleTimeout is returned (wrapped) when no stream chunk arrives
// within the configured inter-token idle window.
var ErrStreamIdleTimeout = errors.New("stream idle timeout: no chunk received within inter-token window")

// DefaultInterTokenIdleTimeout is the post-TTFT liveness bound: a stream
// that produces at least one chunk every 30s is alive no matter how long
// the total generation runs.
const DefaultInterTokenIdleTimeout = 30 * time.Second

// DefaultStreamMaxDuration is the generous absolute ceiling for one active
// stream. It exists only to bound a pathological never-ending stream; real
// stalls fail fast via the idle timeout long before this fires.
const DefaultStreamMaxDuration = 10 * time.Minute

// DefaultTTFTTimeout bounds the pre-first-byte phase (connection +
// response headers + queueing). The transport ResponseHeaderTimeout fires
// first (10s cloud / 15s local); this is the outer request-level backstop.
const DefaultTTFTTimeout = 15 * time.Second

// IdleTimeoutReader wraps an SSE/streaming body and aborts it when the
// inter-token gap exceeds idle. Every Read returning n > 0 resets the
// deadline, so a slow-but-continuous model (e.g. 45s of steady tokens)
// never trips it — only a genuinely stalled connection does.
//
// The zero value is not usable; construct with NewIdleTimeoutReader.
// It implements io.ReadCloser: Close stops the watchdog and closes the
// underlying body.
type IdleTimeoutReader struct {
	src  io.ReadCloser
	idle time.Duration

	mu     sync.Mutex
	timer  *time.Timer
	closed bool
	fired  bool
}

// NewIdleTimeoutReader wraps src with an idle watchdog. idle <= 0 falls
// back to DefaultInterTokenIdleTimeout.
func NewIdleTimeoutReader(src io.ReadCloser, idle time.Duration) *IdleTimeoutReader {
	if idle <= 0 {
		idle = DefaultInterTokenIdleTimeout
	}
	r := &IdleTimeoutReader{src: src, idle: idle}
	r.mu.Lock()
	r.timer = time.AfterFunc(idle, r.onIdle)
	r.mu.Unlock()
	return r
}

// Idle returns the configured inter-token window.
func (r *IdleTimeoutReader) Idle() time.Duration { return r.idle }

// IdleFired reports whether the watchdog has fired (the stream stalled).
func (r *IdleTimeoutReader) IdleFired() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fired
}

// reset restarts the idle deadline. Called on every chunk.
func (r *IdleTimeoutReader) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.fired {
		return
	}
	if r.timer == nil {
		r.timer = time.AfterFunc(r.idle, r.onIdle)
		return
	}
	r.timer.Reset(r.idle)
}

// stop disarms the watchdog (terminal EOF/close path).
func (r *IdleTimeoutReader) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
	}
}

// onIdle runs on the timer goroutine: mark the stall and force-close the
// underlying body so any goroutine blocked in Read unblocks promptly.
// Closing a network body is safe to invoke once; subsequent Reads surface
// the idle error below.
func (r *IdleTimeoutReader) onIdle() {
	r.mu.Lock()
	if r.closed || r.fired {
		r.mu.Unlock()
		return
	}
	r.fired = true
	src := r.src
	r.mu.Unlock()
	// Force-close outside the lock: Close may block briefly on I/O teardown.
	_ = src.Close()
}

// Read delegates to the underlying body. A successful chunk (n > 0) resets
// the idle deadline. After the watchdog fires the body is closed, so the
// blocked Read returns here with an error that we translate to the
// identifiable idle-timeout sentinel (callers can errors.Is for it).
func (r *IdleTimeoutReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		r.reset()
		return n, err
	}
	r.mu.Lock()
	fired := r.fired
	r.mu.Unlock()
	if fired {
		if err == nil || err == io.EOF {
			return 0, ErrStreamIdleTimeout
		}
		return 0, errors.Join(ErrStreamIdleTimeout, err)
	}
	if err == io.EOF {
		r.stop()
	}
	return n, err
}

// Close disarms the watchdog and closes the underlying body. Idempotent.
func (r *IdleTimeoutReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.timer != nil {
		r.timer.Stop()
	}
	src := r.src
	r.mu.Unlock()
	return src.Close()
}
