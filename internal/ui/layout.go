package ui

import "strings"

// Layout partitions the terminal into three strictly separated regions:
//
//	Fixed Header  — locked at top, never scrolls
//	Scrollable    — content region (viewport)
//	Fixed Footer  — locked at bottom, never scrolls
//
// The viewport height is computed as totalHeight minus the rendered height of
// both fixed regions. The renderer never stores or owns layout state; it
// projects the current dimensions on every call.
type Layout struct {
	TotalWidth  int
	TotalHeight int
}

// Partitioned holds the three rendered region strings.
type Partitioned struct {
	Header      string
	Content     string
	Footer      string
	HeaderLines int
	FooterLines int
}

// Partition splits the given header/footer strings into the three regions,
// clamping the content region height so the full screen is always covered.
// header and footer are rendered by their respective components; content is
// the pre-rendered workspace string (or viewport.View()).
func (l *Layout) Partition(header, content, footer string) Partitioned {
	hLines := countLines(header)
	fLines := countLines(footer)

	return Partitioned{
		Header:      header,
		Content:     content,
		Footer:      footer,
		HeaderLines: hLines,
		FooterLines: fLines,
	}
}

// Assemble joins the three regions into a single terminal frame, left-aligned,
// with no gaps between regions.
func Assemble(p Partitioned) string {
	var b strings.Builder
	if p.Header != "" {
		b.WriteString(p.Header)
		b.WriteByte('\n')
	}
	if p.Content != "" {
		b.WriteString(p.Content)
	}
	if p.Footer != "" {
		b.WriteString("\n")
		b.WriteString(p.Footer)
	}
	return b.String()
}

// Partition is a convenience function that joins three regions — header,
// content, footer — into a single terminal frame using Assemble. It creates
// a zero-size Layout (height not used because content is pre-sized).
func Partition(content, header, footer string) string {
	return Assemble(Partitioned{
		Header:      header,
		Content:     content,
		Footer:      footer,
		HeaderLines: countLines(header),
		FooterLines: countLines(footer),
	})
}

// countLines returns the number of newline-separated lines in s, minimum 1
// when s is non-empty, 0 when empty.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	if s == "\n" {
		return 1
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		return n + 1
	}
	return n
}
