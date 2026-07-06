package ui

import (
	"fmt"
	"strconv"
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
// The new implementation produces a symmetrical single-line-number gutter
// with full-width colored background tracks for additions and deletions.
type DiffRenderer struct {
	Width     int
	IsNewFile bool // When true, render single-column line numbers (new file creation)
}

// padToWidth pads s to exactly n visual cells with trailing spaces.
func padToWidth(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func (r *DiffRenderer) Render(v DiffCardViewModel) string {
	if v.Content == "" {
		return ""
	}

	lines := strings.Split(v.Content, "\n")
	var renderedLines []string

	contentWidth := r.Width
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Hunk line tracking
	newLineNum := 1

	for _, line := range lines {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			// Parse hunk header for new-file starting line number
			parts := strings.Split(line, "@@")
			if len(parts) >= 2 {
				header := strings.TrimSpace(parts[1])
				fields := strings.Fields(header)
				if len(fields) >= 2 {
					newRange := strings.TrimPrefix(fields[1], "+")
					newParts := strings.Split(newRange, ",")
					if len(newParts) >= 1 {
						if start, err := strconv.Atoi(newParts[0]); err == nil {
							newLineNum = start
						}
					}
				}
			}

			// Render hunk header as metadata line
			hunk := diffHunkStyle.Render(padToWidth(line, contentWidth))
			renderedLines = append(renderedLines, hunk)

			if len(parts) >= 3 {
				sym := strings.TrimSpace(parts[2])
				if sym != "" {
					symLine := diffHunkStyle.Render(padToWidth("  ─── "+sym+" ───", contentWidth))
					renderedLines = append(renderedLines, symLine)
				}
			}
			continue
		}

		if r.IsNewFile {
			switch {
			case strings.HasPrefix(line, "+"):
				clean := strings.TrimPrefix(line, "+")
				gutter := diffLineNumHLSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				body := "+ " + clean
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + semanticAddStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)
			case strings.HasPrefix(line, "-"):
				continue
			default:
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + semanticNormalStyle.Render(padToWidth("  "+line, bodyW))
				renderedLines = append(renderedLines, row)
			}
		} else {
			// STANDARD DIFF — symmetrical single-line-number gutter
			switch {
			case strings.HasPrefix(line, "-"):
				clean := strings.TrimPrefix(line, "-")
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				body := "- " + clean
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffDelBgStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)

			case strings.HasPrefix(line, "+"):
				clean := strings.TrimPrefix(line, "+")
				gutter := diffLineNumHLSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				body := "+ " + clean
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffAddBgStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)

			default:
				// Context line
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffCtxStyle.Render(padToWidth("  "+line, bodyW))
				renderedLines = append(renderedLines, row)
			}
		}
	}
	return strings.Join(renderedLines, "\n")
}

// MutationRenderer is the orchestrator that composes sub-renderers to output the final Mutation Card.
type MutationRenderer struct {
	Width        int
	ScrollOffset int
}

func (r *MutationRenderer) Render(v MutationCardViewModel) string {
	contentWidth := r.Width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	border := strings.Repeat("─", contentWidth)
	if len(border) == 0 {
		border = "─"
	}

	toggleLabel := "[▼ Expand]"
	if v.Expanded {
		toggleLabel = "[▲ Collapse]"
	}
	actionLine := renderHotkeyPromptWithToggle()

	// Header with inline toggle: "Edit • filename [▼ Expand]"
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
	headerLine := header + " " + toggleLabel

	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	metadataLine := dimmedStyle.Render("  Scope " + scope + " | Risk " + riskLevel)

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

	// Bounded diff content — scrollable via ScrollOffset
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

		if end < total || start > 0 {
			scrollHint := "  " + dimmedStyle.Render("(scroll ↑↓)")
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

// SemanticRenderer legacy wrapper to maintain compatibility while migrating.
type SemanticRenderer struct {
	Width        int
	ScrollOffset int
}

func NewSemanticRenderer(width int) *SemanticRenderer {
	return &SemanticRenderer{Width: width}
}

func (r *SemanticRenderer) RenderMutationCard(m SemanticMutation) string {
	vm := ToMutationCardViewModel(m)
	mr := &MutationRenderer{Width: r.Width, ScrollOffset: r.ScrollOffset}
	return mr.Render(vm)
}
