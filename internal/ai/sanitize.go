// Package ai defines the provider-agnostic client contract of Izen's LLM
// layer (see provider.go). sanitize.go adds the client-wrapper-level
// completion sanitization that guarantees reasoning/thinking tokens never
// consume the parseable completion budget: reasoning blocks are stripped from
// the response before any parser sees it, so the rendered code volume matches
// the provider-reported completion tokens instead of being silently dwarfed
// by hidden chain-of-thought text.
package ai

import (
	"strings"

	"github.com/PizenLabs/izen/internal/core/stream"
)

// SanitizeResponse strips every reasoning/thinking block from a raw LLM
// completion — <think>…</think>, <thought>…</thought> and the provider
// reasoning sentinel — returning only the final visible content. It is the
// non-streaming counterpart of the stream Splitter and is safe to run over
// already-assembled responses.
func SanitizeResponse(raw string) string {
	content, _ := stream.ReasonBlock(raw)
	return strings.TrimSpace(content)
}

// VisibleCompletion returns the content that should reach parsers and the UI
// for a raw completion:
//
//   - When the response carries real content alongside reasoning, the
//     reasoning blocks are stripped and only the content is returned (the
//     common case: thinking tokens must never leak into code parsing).
//   - When the ENTIRE response is a reasoning block (some models emit their
//     whole answer inside <think>/<thought> or reasoning_content and leave the
//     content field empty), the thinking text itself is returned so the answer
//     survives — it is the only source of the response.
func VisibleCompletion(raw string) string {
	content, reasoning := stream.ReasonBlock(raw)
	if strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(reasoning)
}

// CompletionStats describes the token composition of a raw completion so
// callers can prove that reasoning is not silently eating the completion
// budget. Lengths are in runes (a cheap, dependency-free token proxy).
type CompletionStats struct {
	// ContentLen is the visible content length after stripping reasoning.
	ContentLen int
	// ReasoningLen is the stripped reasoning length.
	ReasoningLen int
}

// CompletionStatsOf splits a raw completion into its content/reasoning lengths.
func CompletionStatsOf(raw string) CompletionStats {
	content, reasoning := stream.ReasonBlock(raw)
	return CompletionStats{
		ContentLen:   len([]rune(strings.TrimSpace(content))),
		ReasoningLen: len([]rune(strings.TrimSpace(reasoning))),
	}
}
