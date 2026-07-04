package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SymbolRenderer renders a presentation-ready symbol card.
type SymbolRenderer struct {
	Width int
}

func (r *SymbolRenderer) Render(v SymbolCardViewModel) string {
	var b strings.Builder
	kind := v.Kind
	if kind == "" {
		kind = "Symbol"
	}
	fmt.Fprintf(&b, "  Type:   %s\n", kind)
	fmt.Fprintf(&b, "  Symbol: %s\n", v.Name)
	if v.Module != "" {
		fmt.Fprintf(&b, "  Module: %s\n", v.Module)
	}
	if v.Language != "" {
		fmt.Fprintf(&b, "  Lang:   %s\n", v.Language)
	}
	return b.String()
}

// RiskRenderer renders a presentation-ready risk card.
type RiskRenderer struct {
	Width int
}

func (r *RiskRenderer) Render(v RiskCardViewModel) string {
	level := v.Level
	if level == "" {
		level = "UNKNOWN"
	}
	reason := v.Reason
	if reason == "" {
		reason = "No computed risks detected"
	}
	return fmt.Sprintf("  Level:  %s\n  Reason: %s\n", level, reason)
}

// ImpactRenderer renders a presentation-ready impact card.
type ImpactRenderer struct {
	Width int
}

func (r *ImpactRenderer) Render(v ImpactCardViewModel) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Direct:   %d file(s) modified\n", v.DirectCount)
	fmt.Fprintf(&b, "  Indirect: %d downstream caller(s) affected\n", v.IndirectCount)
	if v.RiskScore > 0 {
		fmt.Fprintf(&b, "  Score:    %d/100\n", v.RiskScore)
	}
	if v.HasAPIChanges {
		b.WriteString("  Scope:    PUBLIC API (Breaking Risk)\n")
	} else {
		b.WriteString("  Scope:    Internal implementation\n")
	}
	return b.String()
}

// DiffRenderer renders standard semantic/syntax-highlighted diff regions.
type DiffRenderer struct {
	Width     int
	IsNewFile bool // When true, render single-column line numbers (new file creation)
}

func (r *DiffRenderer) Render(v DiffCardViewModel) string {
	if v.Content == "" {
		return ""
	}

	lines := strings.Split(v.Content, "\n")
	var renderedLines []string

	styleDeletion := lipgloss.NewStyle().Background(lipgloss.Color("#3a1e24")).Foreground(lipgloss.Color("#f1707a"))
	styleAddition := lipgloss.NewStyle().Background(lipgloss.Color("#18302b")).Foreground(lipgloss.Color("#6cd0a1"))
	styleNormalText := lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))

	contentWidth := r.Width - 14
	if contentWidth < 20 {
		contentWidth = 20
	}

	wrapStringToWidth := func(text string, maxW int) []string {
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

	leftLineNum := 1
	rightLineNum := 1

	// Use Surface2 color (#585b70) for line number gutter — subtle and clean
	lineNumStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	for _, line := range lines {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			parts := strings.Split(line, "@@")
			symbolHeader := ""
			if len(parts) >= 3 {
				symbolHeader = strings.TrimSpace(parts[2])
			}

			if symbolHeader != "" {
				// Inject clear AST-aware symbol header representation
				renderedLines = append(renderedLines, fmt.Sprintf("  ─── Change inside Symbol: %s ───", symbolHeader))
			}

			wrappedLines := wrapStringToWidth(line, contentWidth)
			for _, wl := range wrappedLines {
				gutterStr := diffHunkStyle.Render("  ---  --- │ ")
				textStr := diffHunkStyle.Render(wl)
				renderedLines = append(renderedLines, gutterStr+textStr)
			}
			continue
		}

		if r.IsNewFile {
			// NEW FILE: Single-column line numbers (right-aligned, fixed padding)
			switch {
			case strings.HasPrefix(line, "+"):
				cleanLine := strings.TrimPrefix(line, "+")
				wrappedLines := wrapStringToWidth(cleanLine, contentWidth)

				for i, wl := range wrappedLines {
					var gutterStr string
					if i == 0 {
						gutterStr = lineNumStyle.Render(fmt.Sprintf("%2d │ ", rightLineNum))
						rightLineNum++
					} else {
						gutterStr = lineNumStyle.Render("   │ ")
					}
					textStr := styleAddition.Width(contentWidth).Render(wl)
					renderedLines = append(renderedLines, gutterStr+textStr)
				}
			case strings.HasPrefix(line, "-"):
				// Skip deletions in new file mode (shouldn't happen, but safe)
				continue
			default:
				// Context lines (rare in new files, but handle gracefully)
				wrappedLines := wrapStringToWidth(line, contentWidth)
				for i, wl := range wrappedLines {
					var gutterStr string
					if i == 0 {
						gutterStr = lineNumStyle.Render(fmt.Sprintf("%2d │ ", rightLineNum))
						rightLineNum++
					} else {
						gutterStr = lineNumStyle.Render("   │ ")
					}
					textStr := styleNormalText.Width(contentWidth).Render(wl)
					renderedLines = append(renderedLines, gutterStr+textStr)
				}
			}
		} else {
			// STANDARD DIFF: Dual-column line numbers (old | new)
			switch {
			case strings.HasPrefix(line, "-"):
				cleanLine := strings.TrimPrefix(line, "-")
				wrappedLines := wrapStringToWidth(cleanLine, contentWidth)

				for i, wl := range wrappedLines {
					var gutterStr string
					if i == 0 {
						gutterStr = diffLineNumSty.Render(fmt.Sprintf("  %3d      │ - ", leftLineNum))
						leftLineNum++
					} else {
						gutterStr = diffLineNumSty.Render("           │   ")
					}
					textStr := styleDeletion.Width(contentWidth).Render(wl)
					renderedLines = append(renderedLines, gutterStr+textStr)
				}
			case strings.HasPrefix(line, "+"):
				cleanLine := strings.TrimPrefix(line, "+")
				wrappedLines := wrapStringToWidth(cleanLine, contentWidth)

				for i, wl := range wrappedLines {
					var gutterStr string
					if i == 0 {
						gutterStr = diffLineNumHLSty.Render(fmt.Sprintf("       %3d │ + ", rightLineNum))
						rightLineNum++
					} else {
						gutterStr = diffLineNumHLSty.Render("           │   ")
					}
					textStr := styleAddition.Width(contentWidth).Render(wl)
					renderedLines = append(renderedLines, gutterStr+textStr)
				}
			default:
				wrappedLines := wrapStringToWidth(line, contentWidth)

				for i, wl := range wrappedLines {
					var gutterStr string
					if i == 0 {
						gutterStr = diffLineNumSty.Render(fmt.Sprintf("  %3d  %3d │   ", leftLineNum, rightLineNum))
						leftLineNum++
						rightLineNum++
					} else {
						gutterStr = diffLineNumSty.Render("           │   ")
					}
					textStr := styleNormalText.Width(contentWidth).Render(wl)
					renderedLines = append(renderedLines, gutterStr+textStr)
				}
			}
		}
	}
	return strings.Join(renderedLines, "\n")
}

// MutationRenderer is the orchestrator that composes sub-renderers to output the final Mutation Card.
type MutationRenderer struct {
	Width int
}

func (r *MutationRenderer) Render(v MutationCardViewModel) string {
	var lines []string

	contentWidth := r.Width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	expandStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	border := strings.Repeat("─", contentWidth)
	if len(border) == 0 {
		border = "─"
	}

	header := "Edit"
	if v.Target.Name != "" {
		symbolName := v.Target.Name
		if dotIdx := strings.LastIndex(symbolName, "."); dotIdx >= 0 {
			symbolName = symbolName[dotIdx+1:]
		}
		if slashIdx := strings.LastIndex(symbolName, "/"); slashIdx >= 0 {
			symbolName = symbolName[slashIdx+1:]
		}
		if symbolName != "" {
			header += " • " + symbolName
		} else {
			header += " • " + v.Target.Name
		}
	} else {
		header += " • Unknown"
	}

	// Footer with expand/collapse toggle + actions
	expandIcon := "❯"
	if v.Expanded {
		expandIcon = "▼"
	}
	footerLine := fmt.Sprintf("%s  [A] Accept  [L] Allow All  [R] Reject",
		expandStyle.Render(expandIcon))

	lines = append(lines, border)
	lines = append(lines, header)

	if !v.Expanded {
		scope := "Internal"
		if v.Impact.HasAPIChanges {
			scope = "Public"
		}
		riskLevel := v.Risk.Level
		if riskLevel == "" {
			riskLevel = "UNKNOWN"
		}
		lines = append(lines, expandStyle.Render("  Scope "+scope+" | Risk "+riskLevel))
		lines = append(lines, "")
		lines = append(lines, footerLine)
		lines = append(lines, "")
		lines = append(lines, border)
		return strings.Join(lines, "\n")
	}

	// EXPANDED
	lines = append(lines, "")

	if v.SemanticSummary != "" {
		wrappedSummary := wrapText(v.SemanticSummary, contentWidth)
		if len(wrappedSummary) > 2 {
			wrappedSummary = wrappedSummary[:2]
		}
		for _, line := range wrappedSummary {
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}
		lines = append(lines, "")
	}

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
	lines = append(lines, "")

	lines = append(lines, border)
	return strings.Join(lines, "\n")
}

// SemanticRenderer legacy wrapper to maintain compatibility while migrating.
type SemanticRenderer struct {
	Width int
}

func NewSemanticRenderer(width int) *SemanticRenderer {
	return &SemanticRenderer{Width: width}
}

func (r *SemanticRenderer) RenderMutationCard(m SemanticMutation) string {
	vm := ToMutationCardViewModel(m)
	mr := &MutationRenderer{Width: r.Width}
	return mr.Render(vm)
}
