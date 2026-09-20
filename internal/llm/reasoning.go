package llm

import "strings"

// stripThinkingTags removes reasoning delimiters and the reasoning sentinel
// from LLM payload text. Reasoning models often wrap their answer inside
// <think>...</think> blocks and leave message content empty; the delimiters
// must be removed before the fallback text can be parsed downstream.
func stripThinkingTags(s string) string {
	s = strings.ReplaceAll(s, "\x00RSNG\x00", "")
	for {
		stripped := s
		s = strings.Replace(s, "<think>", "", 1)
		s = strings.Replace(s, "</think>", "", 1)
		s = strings.Replace(s, "<thought>", "", 1)
		s = strings.Replace(s, "</thought>", "", 1)
		if s == stripped {
			break
		}
	}
	return strings.TrimSpace(s)
}

// usableContent returns the first non-blank candidate among the main content
// and the reasoning/thinking fields. It is the reasoning-fallback seam used by
// the OpenAI-compatible completion parsers: a provider that reports the answer
// inside message.reasoning / reasoning_content / thinking (with empty content)
// must still yield usable payload text instead of an "empty response" failure.
//
// Stream Delta Reasoning Invariant (Phase 6.4.5): OpenRouter SSE parsers MUST
// unmarshal delta.reasoning, delta.thinking, and delta.content into a unified
// stream accumulator. This helper is the non-streaming half of that invariant:
// thinking is treated as first-class reasoning progress, never as empty.
func usableContent(content, reasoning, reasoningContent, thinking string) string {
	content = strings.TrimSpace(content)
	if content != "" {
		return content
	}
	for _, c := range []string{reasoning, reasoningContent, thinking} {
		if strings.TrimSpace(c) != "" {
			return stripThinkingTags(c)
		}
	}
	return ""
}

// deltaReasoningText extracts unified reasoning progress from an OpenAI/
// OpenRouter SSE delta: reasoning_content, reasoning, and thinking are all
// treated as active stream progress. Returns "" when the delta carries no
// reasoning text.
func deltaReasoningText(reasoningContent, reasoning, thinking string) string {
	if reasoningContent != "" {
		return reasoningContent
	}
	if reasoning != "" {
		return reasoning
	}
	return thinking
}
