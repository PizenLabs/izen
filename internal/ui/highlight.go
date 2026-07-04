package ui

import (
	"fmt"
	"strings"
)

// ── Professional diff renderer with line numbers ──────────────────────────────
//
// Renders a parsed diff hunk with:
//   - Left gutter: 4-char line number (old | new)
//   - Change type marker: " - " / " + " / "   "
//   - Full-row background tint for added/deleted lines
//   - Syntax highlighting on context lines

// diffLine holds a parsed line from a unified diff hunk.
type diffLine struct {
	kind    byte // '+', '-', ' '
	content string
	oldNum  int // 0 = unknown
	newNum  int
}

// parseDiffHunk parses a unified diff hunk body (after @@ header) into diffLines.
func parseDiffHunk(body string, startOld, startNew int) []diffLine {
	var out []diffLine
	oldN := startOld
	newN := startNew
	for _, raw := range strings.Split(body, "\n") {
		if raw == "" {
			continue
		}
		kind := raw[0]
		content := ""
		if len(raw) > 1 {
			content = raw[1:]
		}
		switch kind {
		case '+':
			out = append(out, diffLine{kind: '+', content: content, oldNum: 0, newNum: newN})
			newN++
		case '-':
			out = append(out, diffLine{kind: '-', content: content, oldNum: oldN, newNum: 0})
			oldN++
		default: // context
			out = append(out, diffLine{kind: ' ', content: content, oldNum: oldN, newNum: newN})
			oldN++
			newN++
		}
	}
	return out
}

// renderDiffLine renders a single diff line with line-number gutter.
// When standalone is true (called from code block renderer), no gutter numbers.
func renderDiffLine(raw string, oldNum, newNum int, standalone bool) string {
	if len(raw) == 0 {
		return ""
	}
	kind := raw[0]
	content := ""
	if len(raw) > 1 {
		content = raw[1:]
	}

	var numGutter string
	if standalone {
		numGutter = hlCodeBg.Render("  │ ")
	}

	switch kind {
	case '+':
		marker := diffAddBgStyle.Render(" + ")
		lineNum := diffLineNumHLSty.Render(fmt.Sprintf("%4d", newNum))
		if standalone {
			return numGutter + diffAddBgStyle.Width(60).Render("+ "+content)
		}
		line := diffAddBgStyle.Render(fmt.Sprintf("     %s + %s", lineNum, content))
		_ = marker
		_ = line
		return diffAddBgStyle.Render(fmt.Sprintf(" %4d + %-60s", newNum, content))
	case '-':
		if standalone {
			return numGutter + diffDelBgStyle.Width(60).Render("- "+content)
		}
		return diffDelBgStyle.Render(fmt.Sprintf(" %4d - %-60s", oldNum, content))
	default:
		if standalone {
			return numGutter + diffCtxStyle.Render("  "+content)
		}
		// Context: show both line numbers, syntax highlight content
		leftNum := diffLineNumSty.Render(fmt.Sprintf("%4d", oldNum))
		rightNum := diffLineNumSty.Render(fmt.Sprintf("%-4d", newNum))
		return leftNum + " " + rightNum + "   " + diffCtxStyle.Render(content)
	}
}

// RenderNumberedDiff renders a full unified diff string with line numbers.
// This is the primary diff renderer for build proposals and /review output.
func RenderNumberedDiff(diffText string, width int) string {
	if diffText == "" {
		return ""
	}
	lines := strings.Split(diffText, "\n")
	var out []string
	var hunkLines []string
	inHunk := false
	oldStart, newStart := 1, 1
	filePath := ""

	flushHunk := func() {
		if len(hunkLines) == 0 {
			return
		}
		parsed := parseDiffHunk(strings.Join(hunkLines, "\n"), oldStart, newStart)
		for _, dl := range parsed {
			out = append(out, renderDiffLine(string(dl.kind)+dl.content, dl.oldNum, dl.newNum, false))
		}
		hunkLines = nil
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			filePath = strings.TrimPrefix(line, "+++ b/")
			fileLabel := lipglossColor(colorAccent).Bold(true).Render("  " + filePath)
			out = append(out, fileLabel)
			out = append(out, lipglossColor(colorSubtle).Render(strings.Repeat("─", min(width-2, 80))))
		case strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"):
			// skip
		case strings.HasPrefix(line, "@@"):
			flushHunk()
			inHunk = true
			// parse @@ -oldStart,oldCount +newStart,newCount @@
			var oS, oC, nS, nC int
			_, _ = fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oS, &oC, &nS, &nC)
			if oS > 0 {
				oldStart = oS
			}
			if nS > 0 {
				newStart = nS
			}
			hunkHeader := diffHunkStyle.Render(line)
			_ = hunkHeader
			out = append(out, lipglossColor(colorDimmed).Render(line))
		default:
			if inHunk {
				hunkLines = append(hunkLines, line)
			}
		}
		_ = filePath
	}
	flushHunk()
	return strings.Join(out, "\n")
}

// RenderInlineDiff is the simpler version used for confirmation previews.
func RenderInlineDiff(diff string) string {
	if diff == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			b.WriteString(diffAddBgStyle.Render(line))
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			b.WriteString(diffDelBgStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(diffHunkStyle.Render(line))
		default:
			b.WriteString(diffCtxStyle.Render(line))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
