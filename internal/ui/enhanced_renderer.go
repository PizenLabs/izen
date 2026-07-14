package ui

import (
	"strings"
)

// Enhanced Mutation Renderer aligned with REVIEW_LAYOUT.md design principles
type EnhancedMutationRenderer struct {
	Width        int
	ScrollOffset int
}

func (r *EnhancedMutationRenderer) Render(v MutationCardViewModel) string {
	contentWidth := r.Width
	if contentWidth < 20 {
		contentWidth = 20
	}

	toggleLabel := dimmedStyle.Render("[▼ Expand]")
	if v.Expanded {
		toggleLabel = dimmedStyle.Render("[▲ Collapse]")
	}
	actionLine := renderHotkeyPromptWithToggle(contentWidth)

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
			headerText += ": " + symbolName
		} else {
			headerText += ": " + v.Target.Name
		}
	} else {
		headerText += ": Unknown"
	}
	headerLine := boldTextStyle.Render(Icon.Edit+" "+headerText) + " " + toggleLabel

	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	metadataLine := dimmedStyle.Render("Scope: " + scope + " · Risk: " + riskLevel)

	lines := make([]string, 0, 20)
	lines = append(lines, "")
	lines = append(lines, "  "+headerLine)
	lines = append(lines, "  "+metadataLine)
	lines = append(lines, "")

	// Bounded diff content — scrollable via proposalDiffOffset
	if v.Expanded {
		if v.Diff.Content != "" {
			dr := &DiffRenderer{Width: contentWidth - 4, IsNewFile: v.IsNewFile}
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
					lines = append(lines, "  "+line)
				}
			}

			if end < total || start > 0 {
				scrollHint := "    " + dimmedStyle.Render("(scroll ↑↓)")
				lines = append(lines, scrollHint)
			}
			lines = append(lines, "")
		}
	}

	// Action keys
	lines = append(lines, "  "+actionLine)
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
