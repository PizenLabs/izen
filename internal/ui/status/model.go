package status

import "strings"

// CompressModelSlug normalizes a raw model id into a compact footer-safe slug:
//
//   - strips suffix tags: :free, :latest, :default
//   - compresses provider prefixes: openrouter/ -> or/,
//     github-copilot/ -> copilot/, strips ollama/
//   - middle-truncates with … when the result exceeds 20 cells
//     (e.g. or/cohere/nor…i-code)
//
// The function is rune-aware and never panics on empty input. It is a pure
// projection helper for the telemetry footer (Slot 1).
func CompressModelSlug(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Strip suffix tags (only trailing, case-sensitive to match provider ids).
	for _, suffix := range []string{":free", ":latest", ":default"} {
		if strings.HasSuffix(s, suffix) {
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	// Compress provider prefixes.
	switch {
	case strings.HasPrefix(s, "openrouter/"):
		s = "or/" + strings.TrimPrefix(s, "openrouter/")
	case strings.HasPrefix(s, "github-copilot/"):
		s = "copilot/" + strings.TrimPrefix(s, "github-copilot/")
	case strings.HasPrefix(s, "ollama/"):
		s = strings.TrimPrefix(s, "ollama/")
	}
	// Middle truncation to 20 cells.
	const maxLen = 20
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	// Keep head 12 + … + tail 7 = 20 cells.
	const head = 12
	tail := maxLen - 1 - head
	if tail < 1 {
		tail = 1
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
