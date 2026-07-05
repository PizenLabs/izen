package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// wrapText word-wraps a single block of text to a maximum line width.
func wrapText(text string, maxW int) []string {
	if len(text) == 0 {
		return []string{""}
	}
	var chunks []string
	words := strings.Fields(text)
	if len(words) == 0 {
		runes := []rune(text)
		for i := 0; i < len(runes); i += maxW {
			end := i + maxW
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}
		return chunks
	}
	var currentLine strings.Builder
	for _, word := range words {
		if currentLine.Len()+1+len(word) > maxW {
			chunks = append(chunks, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		} else {
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
			}
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		chunks = append(chunks, currentLine.String())
	}
	return chunks
}

// formatCompactField renders a labeled field with dimmed styling.
func formatCompactField(label, value string, _ int) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDimmed)).
		Render("  " + label + ": " + value)
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
