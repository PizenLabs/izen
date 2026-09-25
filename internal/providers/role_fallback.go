package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/PizenLabs/izen/internal/httpx"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
)

// Role fallback eligibility (network-transient failures only).
//
// Izen's only implicit-looking behavior is the EXPLICIT role fallback chain
// configured by the user (config.Roles). This file owns the single predicate
// that decides whether a failed invocation may switch to that chain's
// fallback model.
//
// ELIGIBLE (fall back):
//   - network timeouts / connection failures (i/o timeout, context deadline,
//     connection refused, DNS, TLS handshake, unexpected EOF, stream idle),
//   - HTTP 429 (rate limit),
//   - HTTP 5xx (server errors).
//
// NOT ELIGIBLE (never fall back):
//   - wire-policy / agentic-harness refusals — handled transparently by
//     Dynamic Contract Promotion before dispatch, so a 403 of that class is a
//     promotion regression the user must see, not a reason to change models,
//   - HTTP 400/401/403/404 and every other 4xx (bad request, auth, unknown
//     model, quota-permission): the request itself is wrong, and a different
//     model would not fix it,
//   - cancellation and context cancellation (the user asked to stop),
//   - local contract/serialization failures (deterministic client-side bugs).
type FallbackClass string

const (
	// FallbackClassNone means the error must never trigger a model switch.
	FallbackClassNone FallbackClass = ""
	// FallbackClassTimeout is a network/stream timeout or liveness failure.
	FallbackClassTimeout FallbackClass = "timeout"
	// FallbackClassNetwork is a transport-level connection failure (dial, DNS,
	// reset, TLS, truncated stream) — the request never reached a working
	// endpoint. Same trigger class as a timeout: transient infrastructure.
	FallbackClassNetwork FallbackClass = "network failure"
	// FallbackClassRateLimit is an HTTP 429 rate limit.
	FallbackClassRateLimit FallbackClass = "rate limit (HTTP 429)"
	// FallbackClassServerError is an HTTP 5xx provider failure.
	FallbackClassServerError FallbackClass = "server error"
)

// ClassifyFallbackError classifies err for role-fallback eligibility. The
// returned class is FallbackClassNone when the failure must not switch models.
//
// The classification is deliberately conservative: it inspects the structured
// provider status code first (authoritative), then the transport/context
// error chain (net.Error timeouts, syscall errors, os.ErrDeadlineExceeded).
func ClassifyFallbackError(err error) FallbackClass {
	if err == nil {
		return FallbackClassNone
	}
	// 1. Wire-policy / agentic-harness refusals are promotion territory, never
	//    a fallback trigger — checked before the status code so a 403 can never
	//    leak into another class.
	if errors.Is(err, ErrOpenRouterModelIncompatible) {
		return FallbackClassNone
	}
	// 2. Explicit user/provider cancellation is never retried on another model.
	if errors.Is(err, context.Canceled) {
		return FallbackClassNone
	}
	// 3. Authoritative HTTP status from the structured provider error.
	var pe *httpx.ProviderError
	if errors.As(err, &pe) && pe != nil {
		switch code := pe.StatusCode; {
		case code == 429:
			return FallbackClassRateLimit
		case code >= 500:
			return FallbackClassServerError
		default:
			return FallbackClassNone
		}
	}
	// 4. Transport / timeout surface.
	switch {
	case isTimeoutError(err):
		return FallbackClassTimeout
	case isTransportError(err):
		return FallbackClassNetwork
	}
	return FallbackClassNone
}

// IsFallbackEligible reports whether err may switch to the role's configured
// fallback model. It is the boolean form of ClassifyFallbackError.
func IsFallbackEligible(err error) bool {
	return ClassifyFallbackError(err) != FallbackClassNone
}

// FallbackReason renders the human-readable cause used in the trace event:
// "Primary model failed (<reason>). Switched to <fallback_model>."
func FallbackReason(err error) string {
	class := ClassifyFallbackError(err)
	if class == FallbackClassNone {
		return "unknown"
	}
	if detail := transportDetail(err); detail != "" {
		return string(class) + " (" + detail + ")"
	}
	return string(class)
}

// transportDetail extracts a short, non-sensitive transport cause (e.g.
// "connection refused", "context deadline exceeded") for the trace event.
func transportDetail(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "deadline exceeded"
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context deadline exceeded",
		"i/o timeout",
		"connection refused",
		"connection reset by peer",
		"no such host",
		"tls handshake",
		"unexpected eof",
		"stream idle",
		"eof",
	} {
		if strings.Contains(msg, marker) {
			return marker
		}
	}
	return ""
}

// isTimeoutError reports whether the error chain is a genuine timeout: a
// context/os deadline, a net.Error reporting Timeout(), or the stream liveness
// watchdog.
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if errors.Is(err, corestream.ErrStreamIdleTimeout) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var sysErr interface{ Timeout() bool }
	if errors.As(err, &sysErr) && sysErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "i/o timeout")
}

// isTransportError reports whether the error chain is a connection-level
// failure: the request never completed a round trip to a working endpoint
// (dial/DNS/reset/TLS) or the response was truncated.
func isTransportError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused",
		"connection reset by peer",
		"no such host",
		"tls handshake",
		"unexpected eof",
		"eof",
		"broken pipe",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// FormatFallbackEvent renders the single trace-view line emitted when a role
// chain switches models:
//
//	[fallback] Primary model failed (<reason>). Switched to <chainModel>.
//
// chainModel is the RESOLVED target of the user's configured chain, never a
// model this package invented: the chain itself is the only source.
func FormatFallbackEvent(chainModel, reason string) string {
	return fmt.Sprintf("[fallback] Primary model failed (%s). Switched to %s.", reason, chainModel)
}
