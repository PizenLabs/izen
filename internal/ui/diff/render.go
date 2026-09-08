package diff

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Color theme for the diff viewer (spec palette).
const (
	diffAddBg = "#1e3a29"
	diffAddFg = "#a6e3a1"
	diffDelBg = "#3a1e1e"
	diffDelFg = "#f38ba8"
	diffCtxNo = "#45475a"
	diffTxtFg = "#cdd6f4"
	diffHunk  = "#89dceb"
)

var (
	styleFileBadge = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#11111b")).
			Background(lipgloss.Color("#cba6f7")).
			Padding(0, 1)
	styleFileBadgeNew = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#11111b")).
				Background(lipgloss.Color("#a6e3a1")).
				Padding(0, 1)
	styleFileBadgeDel = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#11111b")).
				Background(lipgloss.Color("#f38ba8")).
				Padding(0, 1)
	styleFilePath = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb"))

	styleHunkHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(diffHunk)).Faint(true)

	styleAddLine = lipgloss.NewStyle().
			Foreground(lipgloss.Color(diffAddFg)).
			Background(lipgloss.Color(diffAddBg))
	styleDelLine = lipgloss.NewStyle().
			Foreground(lipgloss.Color(diffDelFg)).
			Background(lipgloss.Color(diffDelBg))
	styleCtxLine = lipgloss.NewStyle().Foreground(lipgloss.Color(diffTxtFg))

	styleOldNo = lipgloss.NewStyle().Foreground(lipgloss.Color(diffCtxNo))
	styleNewNo = lipgloss.NewStyle().Foreground(lipgloss.Color(diffCtxNo))
	styleAddNo = lipgloss.NewStyle().Foreground(lipgloss.Color(diffAddFg)).Bold(true)
	styleDelNo = lipgloss.NewStyle().Foreground(lipgloss.Color(diffDelFg)).Bold(true)

	styleCollapseBar = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Faint(true)
)

// gutterWidth is the fixed layout cost: "  12 │    │ " style columns.
// Format: <OldNo:4> │ <NewNo:4> │ <Prefix:1> <Content>
const gutterWidth = 4 + 3 + 4 + 3 + 2

// contentWidth returns the usable width for diff content given the terminal width.
func contentWidth(termWidth int) int {
	w := termWidth - gutterWidth
	if w < 10 {
		w = 10
	}
	return w
}

func formatNo(n int) string {
	if n <= 0 {
		return "    "
	}
	return fmt.Sprintf("%4d", n)
}

func truncateCells(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	// ASCII fast path: byte length equals cell width, no table lookup.
	if len(s) <= maxW && isASCII(s) {
		return s
	}
	if runewidth.StringWidth(s) <= maxW {
		return s
	}
	return runewidth.Truncate(s, maxW, "…")
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// colorProbe detects once whether Lipgloss emits ANSI in this environment.
// In pipes/tests the color profile degrades to Ascii and every style Render
// is an identity function — the fast path below then skips all per-line
// Lipgloss overhead and produces byte-identical output via plain concat.
// On a real TTY the styled path is used.
var (
	colorProbeOnce sync.Once
	colorProbeOn   bool
)

func colorOn() bool {
	colorProbeOnce.Do(func() {
		colorProbeOn = strings.Contains(styleAddLine.Render("x"), "\x1b")
	})
	return colorProbeOn
}

// renderLineFast builds the dual-column row without Lipgloss. Output is
// identical to RenderLine whenever color is disabled (see colorOn).
func renderLineFast(l DiffLine, termWidth int) string {
	cw := contentWidth(termWidth)
	text := truncateCells(l.Text, cw)
	var prefix string
	switch l.Type {
	case LineAdded:
		prefix = "+"
	case LineDeleted:
		prefix = "-"
	default:
		prefix = " "
	}
	return formatNo(l.OldNo) + " │ " + formatNo(l.NewNo) + " │ " + prefix + " " + text
}

// RenderFileHeader renders the file badge box, e.g. "[MODIFIED] src/main.go".
func RenderFileHeader(f FileDiff, width int) string {
	badge := styleFileBadge.Render(f.StatusLabel())
	switch {
	case f.IsNew:
		badge = styleFileBadgeNew.Render(f.StatusLabel())
	case f.IsDel:
		badge = styleFileBadgeDel.Render(f.StatusLabel())
	}
	path := styleFilePath.Render(truncateCells(f.DisplayPath(), max(width-gutterWidth, 10)))
	return badge + " " + path
}

// RenderHunkHeader renders a dim cyan hunk header line, width-truncated.
func RenderHunkHeader(header string, width int) string {
	return styleHunkHeader.Render(truncateCells(header, max(width-2, 10)))
}

// RenderLine renders one diff line with dual-column gutters. Each returned
// string is a single terminal row; styling is applied per line so ANSI
// backgrounds never bleed across line breaks.
func RenderLine(l DiffLine, termWidth int) string {
	if !colorOn() {
		return renderLineFast(l, termWidth)
	}
	cw := contentWidth(termWidth)
	text := truncateCells(l.Text, cw)
	var prefix string
	switch l.Type {
	case LineAdded:
		prefix = "+"
		gutter := styleOldNo.Render("    ") + " │ " + styleAddNo.Render(formatNo(l.NewNo)) + " │ "
		return gutter + styleAddLine.Render(prefix+" "+text)
	case LineDeleted:
		prefix = "-"
		gutter := styleDelNo.Render(formatNo(l.OldNo)) + " │ " + styleNewNo.Render("    ") + " │ "
		return gutter + styleDelLine.Render(prefix+" "+text)
	default:
		prefix = " "
		gutter := styleOldNo.Render(formatNo(l.OldNo)) + " │ " + styleNewNo.Render(formatNo(l.NewNo)) + " │ "
		return gutter + styleCtxLine.Render(prefix+" "+text)
	}
}

// RenderCollapseBar renders the "N unchanged lines collapsed" summary row.
func RenderCollapseBar(count int, _ int) string {
	msg := fmt.Sprintf("···  │  ···  │   ... %d unchanged lines collapsed (press 'c' to expand) ...", count)
	return styleCollapseBar.Render(msg)
}

// RenderPlainLine renders a line without Lipgloss styling (for tests/perf).
func RenderPlainLine(l DiffLine) string {
	var prefix string
	switch l.Type {
	case LineAdded:
		prefix = "+"
	case LineDeleted:
		prefix = "-"
	default:
		prefix = " "
	}
	oldS, newS := "", ""
	if l.OldNo > 0 {
		oldS = fmt.Sprintf("%4d", l.OldNo)
	} else {
		oldS = "    "
	}
	if l.NewNo > 0 {
		newS = fmt.Sprintf("%4d", l.NewNo)
	} else {
		newS = "    "
	}
	return fmt.Sprintf("%s │ %s │ %s %s", oldS, newS, prefix, l.Text)
}
