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
// inside message.reasoning / reasoning_content (with empty content) must still
// yield usable payload text instead of an "empty response" failure.
func usableContent(content, reasoning, reasoningContent string) string {
	content = strings.TrimSpace(content)
	if content != "" {
		return content
	}
	for _, c := range []string{reasoning, reasoningContent} {
		if strings.TrimSpace(c) != "" {
			return stripThinkingTags(c)
		}
	}
	return ""
}
