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

