// Package components provides native inline TUI components for the izen
// chat stream: the unified diff viewer and the wall-timer metadata suffix.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MaxDiffLines caps line rendering per diff block. Beyond this the viewport
// truncates with a virtualized "(+N more)" marker so a pathological patch can
// never blow the frame budget or flood the viewport.
const MaxDiffLines = 500

var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	diffDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))
	diffCtxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	diffHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	diffLineNoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#45475a"))
	// wallMetaStyle is the faint bottom-right wall-timer metadata:
	// ⟨Wall: X.XXs | Timeout: XXXs⟩. Faint keeps telemetry subordinate.
	wallMetaStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("#6c7086"))
)

// DiffLine is one rendered row of a unified diff.
type DiffLine struct {
	// Kind is '+', '-', ' ' (context) or '@' (hunk header).
	Kind byte
	// OldNo / NewNo are the 1-based line numbers (0 when not applicable).
	OldNo int
	NewNo int
	// Text is the line body without the leading +/- marker.
	Text string
}

// DiffComponent renders file patch chunks as a native inline diff block:
//
//	┌─ Edit: <file_path> (+<added>/-<removed>)
//	  <old> <new> <marker> <text> ...
//	                              ⟨Wall: X.XXs | Timeout: XXXs⟩
//
// Added lines render green (+), removed lines red (-), line numbers aligned
// left in the dim gutter. Rendering caps at MaxDiffLines per block with
// virtualized truncation. Width < 20 degrades to a plain header (never
// panics); broken diff syntax renders as context lines (never panics).
type DiffComponent struct {
	File    string
	Lines   []DiffLine
	Wall    string
	Timeout string
}

// NewDiffComponent builds a component from pre-parsed lines.
func NewDiffComponent(file string, lines []DiffLine) DiffComponent {
	return DiffComponent{File: file, Lines: lines}
}

// ParseUnifiedDiff parses a unified diff body into DiffLines. Hunk headers
// (@@ ...) reset the line counters; unknown/malformed rows degrade to context
// lines so broken diff syntax never panics the TUI. oldStart/newStart seed
// the counters (0 = start at 1).
func ParseUnifiedDiff(file, body string, oldStart, newStart int) DiffComponent {
	oldNo := oldStart
	if oldNo <= 0 {
		oldNo = 1
	}
	newNo := newStart
	if newNo <= 0 {
		newNo = 1
	}
	var lines []DiffLine
	for _, raw := range strings.Split(body, "\n") {
		if strings.HasPrefix(raw, "@@") {
			lines = append(lines, DiffLine{Kind: '@', Text: raw})
			continue
		}
		if raw == "" {
			continue
		}
		switch raw[0] {
		case '+':
			if strings.HasPrefix(raw, "+++") {
				continue
			}
			lines = append(lines, DiffLine{Kind: '+', NewNo: newNo, Text: strings.TrimPrefix(raw[1:], " ")})
			newNo++
		case '-':
			if strings.HasPrefix(raw, "---") {
				continue
			}
			lines = append(lines, DiffLine{Kind: '-', OldNo: oldNo, Text: strings.TrimPrefix(raw[1:], " ")})
			oldNo++
		default:
			text := raw
			if strings.HasPrefix(text, " ") {
				text = text[1:]
			}
			lines = append(lines, DiffLine{Kind: ' ', OldNo: oldNo, NewNo: newNo, Text: text})
			oldNo++
			newNo++
		}
	}
	return DiffComponent{File: file, Lines: lines}
}

// Added returns the added-line count.
func (c DiffComponent) Added() int {
	n := 0
	for _, l := range c.Lines {
		if l.Kind == '+' {
			n++
		}
	}
	return n
}

// Removed returns the removed-line count.
func (c DiffComponent) Removed() int {
	n := 0
	for _, l := range c.Lines {
		if l.Kind == '-' {
			n++
		}
	}
	return n
}

// WithWall attaches pre-formatted wall metadata (see FormatWallMeta).
func (c DiffComponent) WithWall(wall, timeout string) DiffComponent {
	c.Wall = wall
	c.Timeout = timeout
	return c
}

// FormatWallMeta renders ⟨Wall: X.XXs | Timeout: XXXs⟩ in faint style.
// wallSeconds is the measured elapsed time; timeoutSeconds is the bound.
// It is the TUI projection of executor.FormatWallMeta (styling lives here).
func FormatWallMeta(wallSeconds float64, timeoutSeconds int) string {
	if wallSeconds < 0 {
		wallSeconds = 0
	}
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	return wallMetaStyle.Render(fmt.Sprintf("⟨Wall: %.2fs | Timeout: %ds⟩", wallSeconds, timeoutSeconds))
}

// Render renders the diff block at the given terminal width. The output is
// always at least the header line; line bodies truncate to fit and the block
// caps at MaxDiffLines with a virtualized "(+N more)" tail.
func (c DiffComponent) Render(width int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	file := c.File
	if file == "" {
		file = "(unknown file)"
	}
	header := fmt.Sprintf("┌─ Edit: %s (+%d/-%d)", file, c.Added(), c.Removed())
	b.WriteString(diffHeaderStyle.Render(truncateToWidth(header, width)) + "\n")
	n := len(c.Lines)
	shown := n
	truncated := 0
	if shown > MaxDiffLines {
		truncated = shown - MaxDiffLines
		shown = MaxDiffLines
	}
	for i := 0; i < shown; i++ {
		b.WriteString(renderDiffLine(c.Lines[i], width) + "\n")
	}
	if truncated > 0 {
		b.WriteString(diffHunkStyle.Render(fmt.Sprintf("  … (+%d more lines, virtualized)", truncated)) + "\n")
	}
	// Wall-timer metadata pinned bottom-right in faint style.
	meta := ""
	if c.Wall != "" || c.Timeout != "" {
		meta = c.Wall
		if c.Timeout != "" {
			if meta != "" {
				meta += " | " + c.Timeout
			} else {
				meta = c.Timeout
			}
		}
		meta = wallMetaStyle.Render(meta)
	} else {
		meta = ""
	}
	if meta != "" {
		b.WriteString(pinRight(meta, width) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func renderDiffLine(l DiffLine, width int) string {
	switch l.Kind {
	case '+':
		gutter := diffLineNoStyle.Render(fmt.Sprintf("%4d %4d", 0, l.NewNo))
		body := diffAddStyle.Render("+ " + l.Text)
		return truncateToWidth(gutter+" "+body, width)
	case '-':
		gutter := diffLineNoStyle.Render(fmt.Sprintf("%4d %4d", l.OldNo, 0))
		body := diffDelStyle.Render("- " + l.Text)
		return truncateToWidth(gutter+" "+body, width)
	case '@':
		return diffHunkStyle.Render(truncateToWidth("  "+l.Text, width))
	default:
		gutter := diffLineNoStyle.Render(fmt.Sprintf("%4d %4d", l.OldNo, l.NewNo))
		body := diffCtxStyle.Render("  " + l.Text)
		return truncateToWidth(gutter+" "+body, width)
	}
}

func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(s)
	// Rune-based fallback (ANSI-safe enough for short diff rows; the styles
	// above emit one SGR run per row so slicing runes keeps escapes intact
	// for the common case).
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
}

func pinRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return truncateToWidth(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}
