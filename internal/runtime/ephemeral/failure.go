package ephemeral

import (
	"net"
	"strings"
)

// FailureReason is the canonical taxonomy for upstream LLM provider errors.
// Interceptors MUST classify every provider error into this closed set
// before passing it to the runtime.
type FailureReason string

const (
	FailureTokenLimit          FailureReason = "TOKEN_LIMIT"
	FailureNetworkInterruption FailureReason = "NETWORK_INTERRUPTION"
	FailureProviderUnavailable FailureReason = "PROVIDER_UNAVAILABLE"
	FailureRateLimited         FailureReason = "RATE_LIMITED"
	FailureUserCancelled       FailureReason = "USER_CANCELLED"
	FailureSchemaValidation    FailureReason = "SCHEMA_FAILURE"
	FailureTargetConflict      FailureReason = "TARGET_CONFLICT"
	FailureSemanticDrift       FailureReason = "SEMANTIC_DRIFT"
	FailureVerificationFailure FailureReason = "VERIFICATION_FAILURE"
	// FailureUnknown is the closed-taxonomy fallback when no rule matches.
	// It maps to failover-safe handling (treated as provider-side).
	FailureUnknown FailureReason = "UNKNOWN"
)

// validReasons is the closed taxonomy set.
var validReasons = map[FailureReason]struct{}{
	FailureTokenLimit:          {},
	FailureNetworkInterruption: {},
	FailureProviderUnavailable: {},
	FailureRateLimited:         {},
	FailureUserCancelled:       {},
	FailureSchemaValidation:    {},
	FailureTargetConflict:      {},
	FailureSemanticDrift:       {},
	FailureVerificationFailure: {},
	FailureUnknown:             {},
}

// Valid reports whether r is a member of the closed taxonomy.
func (r FailureReason) Valid() bool {
	_, ok := validReasons[r]
	return ok
}

// ProviderError describes one upstream LLM failure as observed by an
// interceptor. It is provider-agnostic: adapters for OpenAI, Anthropic,
// Ollama and OpenRouter all lower their native errors into this shape
// before classification.
type ProviderError struct {
	// Err is the raw upstream error (may be nil when only FinishReason
	// or HTTPStatus is available, e.g. a truncated stream frame).
	Err error
	// FinishReason is the upstream finish_reason (e.g. "length",
	// "stop", "tool_calls", "error").
	FinishReason string
	// HTTPStatus is the upstream HTTP status code (0 when not applicable,
	// e.g. local Ollama transports or pure stream truncation).
	HTTPStatus int
	// Provider is the originating adapter name (openai, anthropic,
	// ollama, openrouter). Used only for provider-specific message
	// dialects; classification rules are provider-independent.
	Provider string
	// OperationID optionally binds the failure to a durable cursor.
	OperationID string
}

// FailureClassifier maps ProviderError values onto the canonical taxonomy.
// The zero value is ready to use; all methods are pure and goroutine-safe.
type FailureClassifier struct{}

// ClassifyError maps a raw execution/verification error onto the canonical
// taxonomy by lowering it into a ProviderError and applying the same
// deterministic rule chain as Classify. A nil error yields FailureUnknown;
// callers must only invoke it on failure paths.
func ClassifyError(err error) FailureReason {
	return FailureClassifier{}.Classify(ProviderError{Err: err})
}

// Classify applies the deterministic rule chain in priority order:
//
//  1. finish_reason == "length" (or max-output-token truncation) -> TOKEN_LIMIT.
//  2. user cancellation signals -> USER_CANCELLED.
//  3. HTTP 429 -> RATE_LIMITED; HTTP 5xx -> PROVIDER_UNAVAILABLE.
//  4. Transport-level drops / resets / DNS / timeouts -> NETWORK_INTERRUPTION.
//  5. Schema/validation dialects -> SCHEMA_FAILURE.
//  6. Conflict dialects (412, 409, precondition/target) -> TARGET_CONFLICT.
//  7. Drift/verification dialects -> SEMANTIC_DRIFT / VERIFICATION_FAILURE.
//  8. Otherwise -> UNKNOWN (failover-safe fallback).
func (FailureClassifier) Classify(pe ProviderError) FailureReason {
	if r := classifyFinishReason(pe.FinishReason); r != "" {
		return r
	}
	msg := ""
	if pe.Err != nil {
		msg = pe.Err.Error()
	}
	lower := strings.ToLower(msg)

	// User cancellation wins over transport noise: a cancelled context
	// wrapped in a net error is still a cancellation.
	if isCancellation(pe.Err, lower) {
		return FailureUserCancelled
	}
	if r := classifyHTTPStatus(pe.HTTPStatus); r != "" {
		// An explicit provider-unavailable status beats a generic
		// transport message; a bare connection reset with no status
		// stays a network interruption.
		return r
	}
	if r := classifyTransport(pe.Err, lower); r != "" {
		return r
	}
	if r := classifyMessage(lower); r != "" {
		return r
	}
	return FailureUnknown
}

// classifyFinishReason handles rule 1. Upstream finish_reason == "length"
// or any max-output-token truncation dialect MUST evaluate to TOKEN_LIMIT.
func classifyFinishReason(finish string) FailureReason {
	switch strings.ToLower(strings.TrimSpace(finish)) {
	case "length", "max_tokens", "max-output-tokens", "output_truncated":
		return FailureTokenLimit
	default:
		return ""
	}
}

// classifyHTTPStatus handles rule 3. Transport-level HTTP mapping:
// 429 -> RATE_LIMITED, 5xx -> PROVIDER_UNAVAILABLE,
// 408/409/412 conflict dialects -> TARGET_CONFLICT.
func classifyHTTPStatus(status int) FailureReason {
	switch {
	case status == 429:
		return FailureRateLimited
	case status >= 500 && status <= 599:
		return FailureProviderUnavailable
	case status == 409 || status == 412:
		return FailureTargetConflict
	default:
		return ""
	}
}

// isCancellation detects user-driven cancellation across providers.
func isCancellation(err error, lower string) bool {
	if err != nil {
		msg := strings.ToLower(err.Error())
		_ = msg
		// context.Canceled unwraps through joined errors.
		if isContextCanceled(err) {
			return true
		}
	}
	for _, s := range []string{
		"user_cancelled", "user cancelled", "user canceled",
		"cancelled by user", "canceled by user", "request aborted by user",
		"client closed request", "operation was canceled",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// classifyTransport handles rule 4: connection reset, dropped streams,
// DNS failures, timeouts (non-cancellation), and refused connections.
func classifyTransport(err error, lower string) FailureReason {
	if err != nil {
		var netErr net.Error
		if asNetError(err, &netErr) && netErr.Timeout() && !isContextDeadline(err) {
			return FailureNetworkInterruption
		}
		var dnsErr *net.DNSError
		if asDNSError(err, &dnsErr) {
			return FailureNetworkInterruption
		}
		var opErr *net.OpError
		if asOpError(err, &opErr) {
			return FailureNetworkInterruption
		}
	}
	if lower == "" {
		return ""
	}
	for _, s := range []string{
		"connection reset", "connection refused", "connection aborted",
		"broken pipe", "unexpected eof", "eof", "socket hang up",
		"network unreachable", "no such host", "dns", "timeout", "timed out",
		"connection dropped", "stream terminated", "transport", "http/2",
		"502", "503", "504", "bad gateway", "service unavailable",
		"gateway timeout", "internal server error",
	} {
		if strings.Contains(lower, s) {
			// 5xx-shaped messages without an explicit status are
			// provider-side outages.
			if s == "502" || s == "503" || s == "504" ||
				s == "bad gateway" || s == "service unavailable" ||
				s == "gateway timeout" || s == "internal server error" {
				return FailureProviderUnavailable
			}
			return FailureNetworkInterruption
		}
	}
	return ""
}

// classifyMessage handles rules 5-7: schema, conflict, drift and
// verification dialects across OpenAI / Anthropic / Ollama / OpenRouter.
func classifyMessage(lower string) FailureReason {
	if lower == "" {
		return ""
	}
	for _, s := range []string{
		"max_tokens", "max output", "output limit", "token limit",
		"context length", "context_length", "context window",
		"too many tokens", "tokens exceed", "exceeds the maximum",
		"model output exceeded", "errpayloadtruncated",
	} {
		if strings.Contains(lower, s) {
			return FailureTokenLimit
		}
	}
	for _, s := range []string{
		"rate limit", "rate_limit", "ratelimit", "too many requests",
		"quota exceeded", "overloaded", "capacity",
	} {
		if strings.Contains(lower, s) {
			return FailureRateLimited
		}
	}
	for _, s := range []string{
		"schema", "json validation", "failed validation", "invalid json",
		"response_format", "structured output", "tool call validation",
		"function call validation", " zod", "does not match schema",
		"additionalproperties", "required field", "missing required",
	} {
		if strings.Contains(lower, s) {
			return FailureSchemaValidation
		}
	}
	for _, s := range []string{
		"target_conflict", "target conflict", "precondition failed",
		"preconditionfailed", "digest mismatch", "tree modified",
		"externally modified", "concurrent modification", "stale cursor",
	} {
		if strings.Contains(lower, s) {
			return FailureTargetConflict
		}
	}
	for _, s := range []string{
		"semantic drift", "semantic_drift", "plan hypothesis invalid",
		"intent mismatch", "objective drift",
	} {
		if strings.Contains(lower, s) {
			return FailureSemanticDrift
		}
	}
	for _, s := range []string{
		"verification failed", "verification_failed", "verification invalid",
		"postcondition", "check failed",
	} {
		if strings.Contains(lower, s) {
			return FailureVerificationFailure
		}
	}
	return ""
}
