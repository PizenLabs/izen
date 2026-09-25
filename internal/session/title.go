package session

import "strings"

// MaxTitleRunes is the deterministic upper bound for a heuristic session title
// derived directly from the first user prompt (Stage 1 of the two-stage titling
// pipeline). The bound keeps titles single-line and layout-safe.
const MaxTitleRunes = 40

// SanitizeTitle derives a deterministic, human-readable session title from a
// raw user prompt. It:
//
//   - collapses every run of whitespace (including newlines/tabs) into a
//     single space,
//   - drops control characters that would corrupt a single-line label,
//   - trims surrounding whitespace, and
//   - truncates the result to at most MaxTitleRunes runes, appending an
//     ellipsis when truncation occurred.
//
// It is pure and allocation-light: the same prompt always yields the same
// title, so a title set on Turn 1 is reproducible across restarts.
func SanitizeTitle(prompt string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, prompt)

	// Collapse runs of spaces.
	var b strings.Builder
	b.Grow(len(cleaned))
	prevSpace := false
	for _, r := range cleaned {
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
			b.WriteRune(r)
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	title := strings.TrimSpace(b.String())
	if title == "" {
		return ""
	}

	runes := []rune(title)
	if len(runes) <= MaxTitleRunes {
		return title
	}
	// Reserve one rune for the ellipsis so the total stays <= MaxTitleRunes.
	return strings.TrimSpace(string(runes[:MaxTitleRunes-1])) + "…"
}

// UserTurns returns the number of accepted human turns in a history slice. It
// is the turn counter surfaced by the Session Manager detail preview.
func UserTurns(history []Message) int {
	n := 0
	for _, m := range history {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// LastUserPrompt returns the most recent user message content, or "" when the
// history holds no human turn. The returned string is sanitized for single-line
// preview rendering.
func LastUserPrompt(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return SanitizeTitle(history[i].Content)
		}
	}
	return ""
}

// EstimatedHistoryTokens returns a deterministic chars/4 token estimate for a
// raw-history slice. It intentionally mirrors the compaction engine's estimator
// (role + separator + content) without importing the compaction package, so the
// dependency direction stays session -> compaction-free. It is an estimate, not
// a provider billing count.
func EstimatedHistoryTokens(history []Message) int {
	total := 0
	for _, m := range history {
		chars := len([]rune(m.Role)) + 2 + len([]rune(m.Content))
		if chars <= 0 {
			continue
		}
		total += (chars + 3) / 4
	}
	return total
}
