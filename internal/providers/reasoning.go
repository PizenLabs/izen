package providers

import (
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// stripThinkingTags removes reasoning delimiters and the reasoning sentinel
// from LLM payload text. Mini/reasoning models (e.g. Cohere North Mini via
// OpenRouter) frequently wrap their entire answer inside <think>...</think>
// (or <thought>...</thought>) blocks and leave the message content empty;
// without stripping the delimiters the fallback text would still fail
// downstream parsing. Replacement is applied iteratively so nested or
// repeated marker pairs are all consumed. The text between the markers is
// preserved — this is the reasoning-fallback seam that salvages models whose
// only output lives inside the thinking block.
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
// provider" failure.
//
// Reasoning-token hygiene: when the main content carries REAL code alongside
// <think>/<thought> blocks, the thinking blocks are stripped so reasoning
// tokens can never leak into the parseable output or inflate the visible code
// volume. When the main content is entirely a thinking block the reasoning
// fields are preferred; only when they too are empty does the thinking text
// itself become the answer (it is the model's sole output).
func firstUsableContent(content, reasoning, reasoningContent string) string {
	if visible := ai.SanitizeResponse(content); visible != "" {
		return visible
	}
	for _, c := range []string{reasoning, reasoningContent} {
		if strings.TrimSpace(c) != "" {
			return stripThinkingTags(c)
		}
	}
	return ai.VisibleCompletion(content)
}
