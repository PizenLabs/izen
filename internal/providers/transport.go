package providers

import (
	"net/http"
	"time"

	"github.com/PizenLabs/izen/internal/httpx"
)

// ── Transport socket hardening (TTFT hang prevention) ───────────────────────
// The shared implementation lives in internal/httpx (importable from both
// internal/providers and internal/llm without a cycle); the names below are
// thin aliases so provider call sites read plainly.

// CloudResponseHeaderTimeout bounds the wait for the first response byte
// from hosted endpoints (strictly inside the 15s TTFT request context).
const CloudResponseHeaderTimeout = httpx.CloudResponseHeaderTimeout

// LocalResponseHeaderTimeout is the looser bound for local models (ollama):
// a cold model load can legitimately take >10s before the first byte.
const LocalResponseHeaderTimeout = httpx.LocalResponseHeaderTimeout

// StrictTransport returns an http.Transport with explicit timeout boundaries
// on every socket phase (dial 5s, TLS handshake 5s, response headers,
// expect-continue 1s). headerTimeout <= 0 falls back to the cloud default.
func StrictTransport(headerTimeout time.Duration) *http.Transport {
	return httpx.StrictTransport(headerTimeout)
}

// TTFTPhaseDetail classifies a TTFT failure by the socket phase that stalled
// (DNS, TCP dial, TLS handshake, or HTTP response headers). It returns ""
// when the error carries no phase-identifiable signature.
func TTFTPhaseDetail(err error) string {
	return httpx.PhaseDetail(err)
}
