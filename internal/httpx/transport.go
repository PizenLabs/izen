// Package httpx holds the shared HTTP transport hardening used by every
// provider client (internal/providers and internal/llm).
//
// A bare &http.Client{} has NO dial, TLS-handshake, or response-header
// timeouts: a dead endpoint hangs the TCP/HTTP socket until the outer
// context deadline fires, stalling the turn on blocked OS-level I/O.
// Every provider MUST build its client on StrictTransport so each socket
// phase has an explicit boundary and fails fast with a phase-identifiable
// error instead of hanging:
//
//	DialContext (5s)           — fast fail on dead/unroutable endpoints
//	TLSHandshakeTimeout (5s)   — fast fail on stalled TLS handshakes
//	ResponseHeaderTimeout      — max wait for the first response byte
//	                             (10s cloud / 15s local; local model loads
//	                             can legitimately exceed 10s TTFT)
//	ExpectContinueTimeout (1s) — bound on 100-continue negotiation
package httpx

import (
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	transportDialTimeout           = 5 * time.Second
	transportKeepAlive             = 30 * time.Second
	transportTLSHandshakeTimeout   = 5 * time.Second
	transportExpectContinueTimeout = 1 * time.Second

	// CloudResponseHeaderTimeout bounds the wait for the first response
	// byte from hosted endpoints. It sits strictly INSIDE the 15s TTFT
	// request context so a hung socket fails deterministically at the
	// transport layer (with a phase-identifiable "timeout awaiting
	// response headers" error) instead of racing the context deadline.
	CloudResponseHeaderTimeout = 10 * time.Second
	// LocalResponseHeaderTimeout is the looser bound for local models
	// (ollama): a cold model load can legitimately take >10s before the
	// first byte, so the transport must not pre-empt it.
	LocalResponseHeaderTimeout = 15 * time.Second
)

// StrictTransport returns an http.Transport with explicit timeout boundaries
// on every socket phase. headerTimeout <= 0 falls back to the cloud default.
func StrictTransport(headerTimeout time.Duration) *http.Transport {
	if headerTimeout <= 0 {
		headerTimeout = CloudResponseHeaderTimeout
	}
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   transportDialTimeout,
			KeepAlive: transportKeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   transportTLSHandshakeTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: transportExpectContinueTimeout,
		IdleConnTimeout:       90 * time.Second,
	}
}

// PhaseDetail classifies a TTFT failure by the socket phase that stalled
// (DNS, TCP dial, TLS handshake, or HTTP response headers) so the error log
// names the phase instead of showing a bare deadline. It returns "" when the
// error carries no phase-identifiable signature.
func PhaseDetail(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "timeout awaiting response headers"):
		return "No response headers received from endpoint"
	case strings.Contains(s, "TLS handshake timeout"):
		return "TLS handshake with endpoint stalled"
	case strings.Contains(s, "no such host"):
		return "DNS resolution failed for endpoint"
	case strings.Contains(s, "connection refused"):
		return "TCP connection refused by endpoint"
	case strings.Contains(s, "dial tcp") && strings.Contains(s, "i/o timeout"):
		return "TCP connection to endpoint timed out"
	case strings.Contains(s, "Client.Timeout exceeded"):
		return "No response headers received from endpoint"
	default:
		return ""
	}
}
