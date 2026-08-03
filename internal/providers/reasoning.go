package providers

import "strings"

// stripThinkingTags removes reasoning delimiters and the reasoning sentinel
// from LLM payload text. Mini/reasoning models (e.g. Cohere North Mini via
// OpenRouter) frequently wrap their entire answer inside <think>...</think>
// (or <thought>...</thought>) blocks and leave the message content empty;
// without stripping the delimiters the fallback text would still fail
// downstream parsing. Replacement is applied iteratively so nested or
// repeated marker pairs are all consumed.
func stripThinkingTags(s string) string {
	s = strings.ReplaceAll(s, ReasoningSentinel, "")
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

// firstUsableContent returns the first non-blank candidate among the main
// content and the reasoning/thinking fields. It is the reasoning-fallback seam
// for non-streaming completions: some providers return the entire answer in
// the reasoning field (message.reasoning / message.reasoning_content) with an
// empty content, which would otherwise surface as an "empty response from
// provider" failure. Main content, when present, is returned verbatim; only
// the reasoning fallbacks go through thinking-tag stripping.
func firstUsableContent(content, reasoning, reasoningContent string) string {
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
