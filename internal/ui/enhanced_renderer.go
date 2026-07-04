package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Enhanced Mutation Renderer aligned with REVIEW_LAYOUT.md design principles
type EnhancedMutationRenderer struct {
	Width int
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

	// Header text — no expand/collapse toggle (moved to footer)
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

	// Footer line: expand/collapse toggle + action keybindings — always visible, anchored at bottom
	expandIcon := "❯"
	if v.Expanded {
		expandIcon = "▼"
	}
	footerLine := fmt.Sprintf("%s  [A] Accept  [L] Allow All  [R] Reject",
		expandStyle.Render(expandIcon))

	if !v.Expanded {
		// COLLAPSED: compact header + metadata + sticky footer
		lines := make([]string, 0, 7)
		lines = append(lines, border)
		lines = append(lines, headerText)

		scope := "Internal"
		if v.Impact.HasAPIChanges {
			scope = "Public"
		}
		riskLevel := v.Risk.Level
		if riskLevel == "" {
			riskLevel = "UNKNOWN"
		}
		metadata := fmt.Sprintf("Scope %s | Risk %s", scope, riskLevel)
		lines = append(lines, expandStyle.Render("  "+metadata))

		lines = append(lines, "") // spacing
		lines = append(lines, footerLine)
		lines = append(lines, "") // spacing before border
		lines = append(lines, border)
		return strings.Join(lines, "\n")
	}

	// EXPANDED: full diff view + sticky footer
	var lines []string
	lines = append(lines, border)
	lines = append(lines, headerText)
	lines = append(lines, "")

	// Semantic Summary
	if v.SemanticSummary != "" {
		summaryLines := wrapText(v.SemanticSummary, contentWidth)
		if len(summaryLines) > 2 {
			summaryLines = summaryLines[:2]
		}
		for _, line := range summaryLines {
			if len(line) > 0 {
				lines = append(lines, "  "+line)
			}
		}
		lines = append(lines, "")
	}

	// Diff content
	if v.Diff.Content != "" {
		dr := &DiffRenderer{Width: contentWidth, IsNewFile: v.IsNewFile}
		diffRendered := dr.Render(v.Diff)
		diffLines := strings.Split(diffRendered, "\n")
		for _, line := range diffLines {
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}
		lines = append(lines, "")
	}

	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	lines = append(lines, formatCompactField("Scope", scope, contentWidth))
	lines = append(lines, formatCompactField("Risk", riskLevel, contentWidth))
	lines = append(lines, "")

	// Sticky footer with toggle + actions
	lines = append(lines, footerLine)
	lines = append(lines, "") // spacing before bottom border

	lines = append(lines, border)
	return strings.Join(lines, "\n")
}

// formatCompactField creates a compact field: "Label Value" (no extra padding)
func formatCompactField(label, value string, maxWidth int) string {
	// Truncate if too long
	if len(label)+1+len(value) > maxWidth {
		// Prioritize the value, truncate label if needed
		if len(value) > maxWidth-3 { // Leave space for label and space
			value = value[:maxWidth-3]
		}
		availableForLabel := maxWidth - len(value) - 1
		if availableForLabel > 0 && len(label) > availableForLabel {
			label = label[:availableForLabel]
		}
	}
	return label + " " + value
}

// wrapText wraps text to fit within maxWidth, respecting word boundaries
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
