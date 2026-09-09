// Package agent implements the Phase 3 role-aware LLM client dispatcher
// and step-by-step agent execution loop.
package agent

import (
	"strings"
)

// NormalizeModelID splits a model id into its provider prefix and bare id.
//
// Fully qualified ids ("groq/llama-3.3-70b-versatile") yield
// ("groq", "llama-3.3-70b-versatile"). Bare ids
// ("llama-3.3-70b-versatile") yield ("", "llama-3.3-70b-versatile").
// Multi-segment ids ("openrouter/anthropic/claude") treat the first segment
// as the provider and preserve the remainder as the bare id
// ("openrouter", "anthropic/claude"). Surrounding whitespace is trimmed;
// an empty input yields ("", "").
func NormalizeModelID(id string) (provider string, bareID string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ""
	}
	if idx := strings.Index(id, "/"); idx >= 0 {
		provider = strings.TrimSpace(id[:idx])
		bareID = strings.TrimSpace(id[idx+1:])
		if bareID == "" {
			return "", id
		}
		return provider, bareID
	}
	return "", id
}
