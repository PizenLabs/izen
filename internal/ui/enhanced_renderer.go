package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Enhanced Mutation Renderer aligned with REVIEW_LAYOUT.md design principles
type EnhancedMutationRenderer struct {
	Width        int
	ScrollOffset int
}

func (r *EnhancedMutationRenderer) Render(v MutationCardViewModel) string {
	contentWidth := r.Width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	border := strings.Repeat("─", contentWidth)
	if len(border) == 0 {
		border = "─"
	}

	expandStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	toggleLabel := "[▼ Expand]"
	if v.Expanded {
		toggleLabel = "[▲ Collapse]"
	}
	actionLine := "  [A] Accept  [L] Allow All  [R] Reject  [P] Toggle"

	// Header with inline toggle: "Edit • filename [▼ Expand]"
	headerText := "Edit"
	if v.Target.Name != "" {
		symbolName := v.Target.Name
		if dotIdx := strings.LastIndex(symbolName, "."); dotIdx >= 0 {
			symbolName = symbolName[dotIdx+1:]
		}
		if slashIdx := strings.LastIndex(symbolName, "/"); slashIdx >= 0 {
			symbolName = symbolName[slashIdx+1:]
		}
		if symbolName != "" {
			headerText += " • " + symbolName
		} else {
			headerText += " • " + v.Target.Name
		}
	} else {
		headerText += " • Unknown"
	}
	headerLine := headerText + " " + toggleLabel

	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	metadataLine := expandStyle.Render("  Scope " + scope + " | Risk " + riskLevel)

	if !v.Expanded {
		// COLLAPSED: header + metadata + action keys
		lines := make([]string, 0, 6)
		lines = append(lines, border)
		lines = append(lines, headerLine)
		lines = append(lines, metadataLine)
		lines = append(lines, "")
		lines = append(lines, actionLine)
		lines = append(lines, "")
		lines = append(lines, border)
		return strings.Join(lines, "\n")
	}

	// EXPANDED: header + metadata + bounded diff + action keys
	lines := make([]string, 0, 20)
	lines = append(lines, border)
	lines = append(lines, headerLine)
	lines = append(lines, metadataLine)
	lines = append(lines, "")

	// Bounded diff content — scrollable via proposalDiffOffset
	if v.Diff.Content != "" {
		dr := &DiffRenderer{Width: contentWidth, IsNewFile: v.IsNewFile}
		diffRendered := dr.Render(v.Diff)
		diffLines := strings.Split(diffRendered, "\n")

		total := len(diffLines)
		start := r.ScrollOffset
		if start >= total {
			start = 0
		}
		end := start + maxProposalDiffHeight
		if end > total {
			end = total
		}

		for _, line := range diffLines[start:end] {
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}

		if end == total && start == 0 && len(diffLines) > 0 {
			// all lines fit — no indicator
		} else if end < total || start > 0 {
			scrollHint := "  " + expandStyle.Render("(scroll ↑↓)")
			lines = append(lines, scrollHint)
		}
		lines = append(lines, "")
	}

	// Action keys
	lines = append(lines, actionLine)
	lines = append(lines, "")
	lines = append(lines, border)
	return strings.Join(lines, "\n")
}
// formatCompactField creates a compact field: "Label Value" (no extra padding)
func formatCompactField(label, value string, maxWidth int) string {
	if len(label)+1+len(value) > maxWidth {
		if len(value) > maxWidth-3 {
			value = value[:maxWidth-3]
		}
		availableForLabel := maxWidth - len(value) - 1
		if availableForLabel > 0 && len(label) > availableForLabel {
			label = label[:availableForLabel]
		}
	}
	return label + " " + value
}

// wrapText wraps text to fit within maxWidth, respecting word boundaries.
// Words longer than maxWidth are split across lines to prevent terminal wrapping.
func wrapText(text string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		for len(word) > maxWidth {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
			lines = append(lines, word[:maxWidth])
			word = word[maxWidth:]
		}
		if len(word) == 0 {
			continue
		}
		switch {
		case currentLine.Len() == 0:
			currentLine.WriteString(word)
		case currentLine.Len()+1+len(word) <= maxWidth:
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
		default:
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}
