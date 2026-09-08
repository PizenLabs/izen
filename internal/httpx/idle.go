// Idle watchdog re-export: the post-TTFT liveness bound lives canonically
// in internal/core/stream (IdleTimeoutReader). This file surfaces the same
// boundaries from the transport layer so provider call sites can reference
// them without importing the stream classifier package, and documents the
// decoupled lifecycle:
//
//	pre-TTFT:  ResponseHeaderTimeout (10s cloud / 15s local) + TTFT request
//	          bound (15s) — enforced by the transport + the caller's TTFT ctx.
//	post-TTFT: inter-token idle timeout (30s, reset on every chunk) under a
//	          generous stream-max ceiling (10m). A slow-but-alive stream never
//	          trips the idle bound; only a stalled socket does.
package httpx

import (
	"io"
	"time"

	"github.com/PizenLabs/izen/internal/core/stream"
)

// Re-exported stream liveness boundaries (single source of truth in
// internal/core/stream; aliases kept here for provider call sites).
const (
	// TTFTTimeout bounds the pre-first-byte phase.
	TTFTTimeout = stream.DefaultTTFTTimeout
	// InterTokenIdleTimeout is the post-TTFT liveness bound, reset on
	// every received chunk.
	InterTokenIdleTimeout = stream.DefaultInterTokenIdleTimeout
	// StreamMaxDuration is the generous absolute ceiling for one stream.
	StreamMaxDuration = stream.DefaultStreamMaxDuration
)

// ErrStreamIdleTimeout is the identifiable stall error (see stream package).
var ErrStreamIdleTimeout = stream.ErrStreamIdleTimeout

// WrapIdle wraps an SSE/streaming body with the inter-token idle watchdog.
// Every Read carrying bytes resets the deadline; a stall past idle
// force-closes the body so the blocked reader unblocks with
// ErrStreamIdleTimeout instead of hanging to the stream-max ceiling.
func WrapIdle(body io.ReadCloser, idle time.Duration) io.ReadCloser {
	return stream.NewIdleTimeoutReader(body, idle)
}
