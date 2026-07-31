package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// ── Sanitization ────────────────────────────────────────────────────────────

func TestNormalizeLineEndings(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"CRLF", "a\r\nb\r\nc", "a\nb\nc"},
		{"lone CR", "a\rb\nc", "a\nb\nc"},
		{"mixed", "a\r\nb\rc\nd\n", "a\nb\nc\nd\n"},
		{"no CR", "a\nb\nc", "a\nb\nc"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeLineEndings(c.in); got != c.want {
				t.Errorf("normalizeLineEndings(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExpandTabs(t *testing.T) {
	if got := expandTabs("a\tb"); got != "a    b" {
		t.Errorf("expandTabs = %q, want 4-space tab", got)
	}
	// Leading tabs become indentation, not empty.
	if got := expandTabs("\tindent"); got != "    indent" {
		t.Errorf("expandTabs leading = %q", got)
	}
	// No tabs — unchanged.
	if got := expandTabs("plain"); got != "plain" {
		t.Errorf("expandTabs plain = %q", got)
	}
}

func TestSanitizeText(t *testing.T) {
	// CRLF + literal escapes + real tab all cleaned in one pass.
	in := "line1\r\nline2\\nvalue\there"
	got := sanitizeText(in)
	if strings.Contains(got, "\r") {
		t.Errorf("sanitizeText left CR: %q", got)
	}
	if strings.Contains(got, "\\n") || !strings.Contains(got, "\n") {
		t.Errorf("sanitizeText did not decode literal \\n: %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Errorf("sanitizeText left a tab: %q", got)
	}
	if !strings.Contains(got, "value    here") {
		t.Errorf("sanitizeText did not expand tab: %q", got)
	}
}

// ── ANSI-safe truncation ────────────────────────────────────────────────────

func TestTruncateANSIPreservesStyleAndText(t *testing.T) {
	styled := "\x1b[38;2;255;0;0m" + strings.Repeat("x", 40) + "\x1b[0m"
	got := truncateANSI(styled, 20)

	stripped := ansi.Strip(got)
	if lipgloss.Width(stripped) > 20 {
		t.Errorf("truncated width %d exceeds 20", lipgloss.Width(stripped))
	}
	if !strings.HasSuffix(stripped, "...") {
		t.Errorf("truncated output missing ellipsis: %q", stripped)
	}
	// The SGR open sequence must survive unbroken (no mid-sequence cut).
	if !strings.HasPrefix(got, "\x1b[38;2;255;0;0m") {
		t.Errorf("SGR sequence truncated: %q", got)
	}
	// No orphaned SGR may leak without its ESC leader.
	if orphanSGRLeaked(got) {
		t.Errorf("orphaned SGR leaked: %q", got)
	}
}

func TestTruncateANSIDoesNotSplitWideRunes(t *testing.T) {
	wide := strings.Repeat("界", 10) // 2 cells each → 20 cells
	got := truncateANSI(wide, 8)
	if lipgloss.Width(got) > 8 {
		t.Errorf("wide truncation exceeded 8 cells: %q (%d)", got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("wide truncation missing ellipsis: %q", got)
	}
	// Every remaining rune must be a complete 2-cell glyph.
	for _, r := range strings.TrimSuffix(got, "...") {
		if runewidth.RuneWidth(r) != 2 {
			t.Errorf("truncation split a wide rune: %q", got)
		}
	}
}

func TestTruncateANSIWithinBudgetUnchanged(t *testing.T) {
	short := "\x1b[31mhi\x1b[0m"
	if got := truncateANSI(short, 20); got != short {
		t.Errorf("short styled input altered: %q", got)
	}
}

// ── Cell-accurate wrapping ──────────────────────────────────────────────────

func TestWrapPlainLineCellAccuracy(t *testing.T) {
	line := strings.Repeat("a", 40) + " " + strings.Repeat("b", 40)
	for _, wl := range wrapPlainLine(line, 15) {
		if lipgloss.Width(wl) > 15 {
			t.Errorf("wrapped line exceeds 15 cells: %q (%d)", wl, lipgloss.Width(wl))
		}
	}
}

func TestWrapPlainLineWideGlyphs(t *testing.T) {
	// 10 double-width glyphs in a 6-cell window must chunk at cell boundaries
	// (3 glyphs per chunk), never splitting a rune or overflowing.
	wrapped := wrapPlainLine(strings.Repeat("界", 10), 6)
	if len(wrapped) < 3 {
		t.Fatalf("expected multiple chunks, got %d", len(wrapped))
	}
	for _, wl := range wrapped {
		if lipgloss.Width(wl) > 6 {
			t.Errorf("wide chunk exceeds 6 cells: %q (%d)", wl, lipgloss.Width(wl))
		}
	}
}

func TestWrapPlainLineANSIKeepsAllWords(t *testing.T) {
	styled := ansiText + "alpha" + ansiReset + " " + ansiText + "beta" + ansiReset + " " + "gamma"
	wrapped := wrapPlainLine(styled, 6)
	stripped := strings.Join(wrapped, " ")
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("wrapped styled text lost %q: %v", want, stripped)
		}
	}
	for _, wl := range wrapped {
		if lipgloss.Width(wl) > 6 {
			t.Errorf("styled wrapped line exceeds 6 cells: %q (%d)", wl, lipgloss.Width(wl))
		}
		if orphanSGRLeaked(wl) {
			t.Errorf("styled wrap leaked orphan SGR: %q", wl)
		}
	}
}

func TestWrapTextPreservesNewlines(t *testing.T) {
	got := wrapText("line one\nline two is longer than the limit for sure", 12)
	if len(got) < 2 {
		t.Fatalf("expected multiple lines, got %v", got)
	}
	// The explicit \n between line one and line two must be preserved.
	if got[0] != "line one" {
		t.Errorf("first line = %q, want \"line one\"", got[0])
	}
}

func TestWrapStringDelegates(t *testing.T) {
	got := wrapString("one two three", 7)
	if len(got) < 2 {
		t.Fatalf("wrapString did not wrap: %v", got)
	}
	for _, wl := range got {
		if lipgloss.Width(wl) > 7 {
			t.Errorf("wrapString line exceeds 7 cells: %q", wl)
		}
	}
}

func TestChunkWordCellAligned(t *testing.T) {
	// Wide glyphs chunk at 2-cell boundaries.
	pieces := chunkWord(strings.Repeat("界", 10), 6)
	for _, p := range pieces {
		if lipgloss.Width(p) > 6 {
			t.Errorf("chunk exceeds 6 cells: %q (%d)", p, lipgloss.Width(p))
		}
	}
	// ANSI sequences never split mid-way.
	styled := "\x1b[32m" + strings.Repeat("a", 30) + "\x1b[0m"
	for _, p := range chunkWord(styled, 8) {
		if orphanSGRLeaked(p) {
			t.Errorf("chunk leaked orphan SGR: %q", p)
		}
	}
}

func TestWrapIndentedLineWideGlyphChunk(t *testing.T) {
	// 2-cell glyphs inside a tight 6-cell window must stay inside the bound.
	for _, wl := range wrapIndentedLine(strings.Repeat("界", 12), 6) {
		if lipgloss.Width(wl) > 6 {
			t.Errorf("indented wide chunk exceeds 6 cells: %q (%d)", wl, lipgloss.Width(wl))
		}
	}
}

// ── Marker-aware double-wrap guard ─────────────────────────────────────────

func TestMarkdownLinePrefixWidth(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"- bullet text", 2},
		{"* alt bullet", 2},
		{"- [ ] unchecked", 2},
		{"- [x] checked", 2},
		{"> quote", 2},
		{"### heading", 2},
		{"1. ordered", 3},
		{"10. ordered", 4},
		{"plain text", 0},
		{"---", 0},
	}
	for _, c := range cases {
		if got := markdownLinePrefixWidth(c.line); got != c.want {
			t.Errorf("markdownLinePrefixWidth(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

func TestDeterministicPipelineListNoOverflow(t *testing.T) {
	width := 40
	// A long bullet item must wrap so the styled first line (marker + content)
	// never exceeds the wrap boundary, and no word is lost.
	long := "- " + strings.Repeat("word ", 30)
	rendered := RenderDeterministicPipeline(long, width, false)

	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if lipgloss.Width(line) > width {
			t.Errorf("styled line exceeds %d cells: %q (%d)", width, line, lipgloss.Width(line))
		}
	}
	stripped := ansi.Strip(rendered)
	if !strings.Contains(stripped, "word") || !strings.HasPrefix(strings.TrimSpace(stripped), "•") {
		t.Errorf("bullet content lost or marker missing: %q", stripped)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// orphanSGRLeaked reports whether out contains an SGR sequence "[\d+m" whose
// '[' is NOT preceded by an ESC leader.
func orphanSGRLeaked(out string) bool {
	for i := 0; i < len(out); i++ {
		if out[i] != '[' || i+1 >= len(out) || out[i+1] < '0' || out[i+1] > '9' {
			continue
		}
		j := i + 1
		for j < len(out) && out[j] >= '0' && out[j] <= '9' {
			j++
		}
		for j < len(out) && out[j] == ';' {
			k := j + 1
			if k < len(out) && out[k] >= '0' && out[k] <= '9' {
				j = k + 1
				for j < len(out) && out[j] >= '0' && out[j] <= '9' {
					j++
				}
			} else {
				break
			}
		}
		if j < len(out) && out[j] == 'm' && (i == 0 || out[i-1] != '\x1b') {
			return true
		}
	}
	return false
}
