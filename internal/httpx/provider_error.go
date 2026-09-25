package httpx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderError is the structured, unadulterated provider error forwarded
// from the API server. RawMessage is extracted verbatim from the provider's
// JSON error response (error.message) and never replaced with generic text.
type ProviderError struct {
	StatusCode int
	Code       string
	Type       string
	RawMessage string
	Provider   string
	RawBody    string
}

// Error returns the transparent TUI banner form:
// ✗ [<Provider> <StatusCode>] <RawMessage>
// (e.g. ✗ [OpenRouter 400] Model ... requires higher max_tokens budget)
func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	prov := e.Provider
	if prov == "" {
		prov = "Provider Error"
	} else {
		prov = title(prov)
	}
	msg := strings.TrimSpace(e.RawMessage)
	if msg == "" {
		msg = strings.TrimSpace(e.RawBody)
	}
	if msg == "" {
		msg = "unknown provider error"
	}
	return fmt.Sprintf("✗ [%s %d] %s", prov, e.StatusCode, msg)
}

// Unwrap-compatible accessor for errors.As.
func (e *ProviderError) As(target any) bool { return false }

func title(s string) string {
	if s == "" {
		return s
	}
	// Capitalize first letter only (openrouter -> Openrouter, then map known).
	lower := strings.ToLower(s)
	switch lower {
	case "openrouter":
		return "OpenRouter"
	case "openai":
		return "OpenAI"
	case "anthropic", "claude":
		return "Anthropic"
	case "ollama":
		return "Ollama"
	case "gemini":
		return "Gemini"
	case "groq":
		return "Groq"
	default:
		return strings.ToUpper(s[:1]) + s[1:]
	}
}

// agenticHarnessMarker is the provider-side eligibility refusal OpenRouter
// returns when a model restricted to agentic harnesses is invoked through
// an ordinary API-key client (HTTP 403).
const agenticHarnessMarker = "agentic harness"

// IsModelCompatibility reports whether the provider error is a
// model/execution-path compatibility refusal rather than a generic failure:
// HTTP 403 carrying an agentic-harness eligibility message. Callers use it
// to surface "unavailable for Izen's current execution path" instead of a
// generic streaming failure. The raw message is never altered.
func (e *ProviderError) IsModelCompatibility() bool {
	if e == nil {
		return false
	}
	if e.StatusCode != 403 {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(e.RawMessage))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(e.RawBody))
	}
	return strings.Contains(msg, agenticHarnessMarker)
}

// FormatProviderError renders the TUI status banner for any error, preferring
// the structured ProviderError form when available.
func FormatProviderError(provider string, statusCode int, rawMessage string) string {
	return (&ProviderError{Provider: provider, StatusCode: statusCode, RawMessage: rawMessage}).Error()
}

// ParseProviderError extracts a structured ProviderError from an HTTP error
// body. It probes OpenAI-compatible (error.message/code/type), OpenRouter
// (error.message/code int), Ollama ({error: string}), and Anthropic
// ({error: {message, type}}) shapes, preserving the raw message verbatim.
func ParseProviderError(provider string, statusCode int, body []byte) *ProviderError {
	pe := &ProviderError{Provider: provider, StatusCode: statusCode, RawBody: strings.TrimSpace(string(body))}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		pe.RawMessage = fmt.Sprintf("HTTP %d", statusCode)
		return pe
	}
	// Shape 1: {"error": {"message": ..., "code": ..., "type": ...}}
	var obj struct {
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &obj); err == nil && obj.Error != nil {
		pe.RawMessage = strings.TrimSpace(obj.Error.Message)
		pe.Type = obj.Error.Type
		switch c := obj.Error.Code.(type) {
		case string:
			pe.Code = c
		case float64:
			if c != 0 {
				pe.Code = fmt.Sprintf("%.0f", c)
			}
		}
		if pe.RawMessage == "" {
			pe.RawMessage = pe.Type
		}
		if pe.RawMessage != "" {
			return pe
		}
	}
	// Shape 2: {"error": "string"} (Ollama)
	var flat struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &flat); err == nil && flat.Error != "" {
		pe.RawMessage = strings.TrimSpace(flat.Error)
		return pe
	}
	// Shape 3: {"message": "..."} fallback
	var msgOnly struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &msgOnly); err == nil && msgOnly.Message != "" {
		pe.RawMessage = strings.TrimSpace(msgOnly.Message)
		return pe
	}
	// Raw passthrough: never replace with generic text.
	pe.RawMessage = trimmed
	return pe
}
