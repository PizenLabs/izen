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
	// Calculate content width (accounting for borders and padding)
	contentWidth := r.Width - 4 // 2 for borders, 2 for padding
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Top border - minimal visual noise
	border := strings.Repeat("─", contentWidth)
	if len(border) == 0 {
		border = "─"
	}

	// Header line with expand/collapse indicator
	statusIcon := "•" // pending
	expandIcon := "❯" // collapsed by default

	if v.Expanded {
		expandIcon = "▼" // expanded
	}

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

	// Style for the expand indicator
	expandStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	// Build the header line with status and expand indicators
	headerLine := fmt.Sprintf("%s %s %s", statusStyle.Render(statusIcon), expandStyle.Render(expandIcon), headerText)

	if !v.Expanded {
		// COLLAPSED: Render compact header + action buttons (always visible)
		var lines []string
		lines = append(lines, border)
		lines = append(lines, headerLine)

		// Add scope/risk info inline for compact view
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

		lines = append(lines, "") // Spacing before actions
		lines = append(lines, "[A] Accept    [L] Allow All    [R] Reject")
		lines = append(lines, border)
		return strings.Join(lines, "\n")
	}

	// EXPANDED: Render full diff view
	var lines []string
	lines = append(lines, border)
	lines = append(lines, headerLine)
	lines = append(lines, "") // Empty line for spacing

	// Semantic Summary - max 2 lines above diff
	if v.SemanticSummary != "" {
		summaryLines := wrapText(v.SemanticSummary, contentWidth)
		if len(summaryLines) > 2 {
			summaryLines = summaryLines[:2]
		}
		for _, line := range summaryLines {
			if len(line) > 0 {
				lines = append(lines, "  "+line) // Indent for visual separation
			}
		}
		lines = append(lines, "") // Empty line after summary
	}

	// Diff - the evidence (takes most of the space)
	if v.Diff.Content != "" {
		dr := &DiffRenderer{Width: contentWidth, IsNewFile: v.IsNewFile}
		diffRendered := dr.Render(v.Diff)

		diffLines := strings.Split(diffRendered, "\n")
		for _, line := range diffLines {
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}
		lines = append(lines, "") // Empty line after diff
	}

	// Scope - compact: "Scope Internal/Public"
	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	lines = append(lines, formatCompactField("Scope", scope, contentWidth))

	// Risk - compact: "Risk LOW"
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	lines = append(lines, formatCompactField("Risk", riskLevel, contentWidth))

	// Checkpoint - compact: "Checkpoint cp-18312"
	lines = append(lines, formatCompactField("Checkpoint", "cp-pending", contentWidth))

	lines = append(lines, "") // Empty line before actions

	// Decision Actions - always visible, sticky: "[A] Accept    [L] Allow All    [R] Reject"
	lines = append(lines, "[A] Accept    [L] Allow All    [R] Reject")
	lines = append(lines, "") // Empty line before bottom border

	// Bottom border - minimal visual noise
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
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= maxWidth {
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
		} else {
			// Current line is full, start a new one
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
