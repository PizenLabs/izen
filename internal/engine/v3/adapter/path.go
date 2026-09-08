package adapter

import (
	"path/filepath"
	"strings"
)

// slug converts a user-facing name into the filesystem-safe kebab-case slug
// used in paths and URLs. This is the ONLY path-shaping helper adapters use,
// keeping layout knowledge confined to the adapter layer.
func slug(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	var b strings.Builder
	prevDash := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "index"
	}
	return s
}

// pascal converts a name into PascalCase (e.g. "user profile" → "UserProfile").
func pascal(name string) string {
	words := splitWords(name)
	var b strings.Builder
	for _, w := range words {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// snake converts a name into snake_case (e.g. "Create Users Table" →
// "create_users_table").
func snake(name string) string {
	words := splitWords(name)
	return strings.Join(words, "_")
}

// splitWords splits a name on whitespace, dashes, dots, underscores and
// camelCase boundaries, lowercasing each word.
func splitWords(name string) []string {
	name = strings.TrimSpace(name)
	var raw []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			raw = append(raw, cur.String())
			cur.Reset()
		}
	}
	for i, r := range name {
		switch {
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '/':
			flush()
		case i > 0 && r >= 'A' && r <= 'Z' && !isUpper(rune(name[i-1])):
			flush()
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()

	words := make([]string, 0, len(raw))
	for _, w := range raw {
		if w = strings.ToLower(w); w != "" {
			words = append(words, w)
		}
	}
	return words
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// joinPath joins path segments with "/" regardless of host separator, so
// artifact paths are stable on every platform.
func joinPath(parts ...string) string {
	return filepath.ToSlash(filepath.Join(parts...))
}
