package ui

import (
	"os"
	"path/filepath"
	"strings"
)

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// stripLogTokens removes internal bookkeeping patterns from a log preview
// string so they never leak into the TUI viewport. Patterns stripped:
//   - ISO date prefixes: [2026-07-14... or 2026-07-14...
//   - context=<hex-id>  metadata tags
//   - file=<path>, patch=<path> remnants
//   - Other raw key=value tokens that don't add user-facing value.
func stripLogTokens(s string) string {
	var tokens []string
	for _, tok := range strings.Fields(s) {
		// Drop bare ISO dates and [ISO date] brackets
		if len(tok) >= 11 && tok[0] == '[' && (tok[1] == '2' || tok[1] == '1') && tok[5] == '-' && tok[8] == '-' {
			continue
		}
		if len(tok) >= 10 && (tok[0] == '2' || tok[0] == '1') && tok[4] == '-' && tok[7] == '-' {
			continue
		}
		// Drop context=<id> (lowercase only — JSON keys)
		if strings.HasPrefix(tok, "context=") {
			continue
		}
		// Drop file=<path> remnants
		if strings.HasPrefix(tok, "file=") {
			continue
		}
		// Drop patch=<path> remnants
		if strings.HasPrefix(tok, "patch=") {
			continue
		}
		// Drop mode= and role= (already displayed in the structured label)
		if strings.HasPrefix(tok, "mode=") || strings.HasPrefix(tok, "role=") {
			continue
		}
		tokens = append(tokens, tok)
	}
	return strings.Join(tokens, " ")
}

func (m *model) expandFileRefs(line string) string {
	fields := strings.Fields(line)
	changed := false
	for i, field := range fields {
		if strings.HasPrefix(field, "@") {
			ref := filepath.Clean(field[1:])
			if ref == "" || ref == "." {
				continue
			}
			if _, err := os.Stat(ref); err == nil {
				fields[i] = ref
				changed = true
				continue
			}
			matches, err := filepath.Glob(ref)
			if err == nil && len(matches) > 0 {
				fields[i] = matches[0]
				changed = true
				continue
			}
			if _, err := os.Stat(field[1:]); err == nil {
				fields[i] = field[1:]
				changed = true
				continue
			}
			m.push(roleSystem, infoStyle.Render("warn: @"+field[1:]+" not found — sending as literal"))
			fields[i] = field[1:]
			changed = true
		}
	}
	if changed {
		return strings.Join(fields, " ")
	}
	return line
}
