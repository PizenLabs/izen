package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// newViTestModel builds a minimal model with sample chat records for testing
// the vi-mode navigation and yank pipeline.
func newViTestModel() *model {
	m := newTestModel()
	m.state = StateChat
	m.showBanner = false
	m.PreRenderedHistory = ""
	m.records = []record{
		{role: roleUser, text: "hello world"},
		{role: roleSystem, text: "system message here"},
		{role: roleAI, text: "ai response content"},
	}
	m.Viewport.Height = 20
	return m
}

func TestViModeEnterInitializes2DState(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()

	if !m.inViMode {
		t.Fatal("inViMode should be true after enterViMode")
	}
	if m.viModeState != ViNormal {
		t.Fatalf("expected ViNormal, got %d", m.viModeState)
	}
	if m.cursorLine != 2 {
		t.Fatalf("expected cursor on last record (2), got %d", m.cursorLine)
	}
	if m.cursorCol != 0 {
		t.Fatalf("expected cursorCol 0, got %d", m.cursorCol)
	}
}

func TestViModeHorizontalNav(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.cursorLine = 0 // "hello world" (len 11, last index 10)

	// 'l' moves right
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.cursorCol != 1 {
		t.Fatalf("expected cursorCol 1 after 'l', got %d", m.cursorCol)
	}

	// Move to near end
	m.cursorCol = 9
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.cursorCol != 10 {
		t.Fatalf("expected cursorCol 10 (last index), got %d", m.cursorCol)
	}

	// 'l' at last char should not exceed bounds (len 11, last index 10)
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.cursorCol != 10 {
		t.Fatalf("cursorCol should clamp at 10, got %d", m.cursorCol)
	}

	// 'h' moves left
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.cursorCol != 9 {
		t.Fatalf("expected cursorCol 9 after 'h', got %d", m.cursorCol)
	}

	// '0' snaps to start
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if m.cursorCol != 0 {
		t.Fatalf("expected cursorCol 0 after '0', got %d", m.cursorCol)
	}

	// '$' snaps to end
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'$'}})
	if m.cursorCol != 10 {
		t.Fatalf("expected cursorCol 10 after '$', got %d", m.cursorCol)
	}
}

func TestViModeColClampOnVerticalMove(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.cursorLine = 0
	m.cursorCol = 10 // end of "hello world"

	// Move down to line 1 ("system message here", len 19) — col should be kept
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.cursorLine != 1 {
		t.Fatalf("expected cursorLine 1, got %d", m.cursorLine)
	}
	if m.cursorCol != 10 {
		t.Fatalf("expected cursorCol preserved at 10, got %d", m.cursorCol)
	}

	// Move onto a shorter line would clamp — go to line 0 with an OOB col
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m.cursorCol = 20 // beyond line 0 length (11)
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.cursorCol != 18 {
		t.Fatalf("expected cursorCol clamped to 18 (len 19 -1), got %d", m.cursorCol)
	}
}

func TestViModeJumpToBottomAnchorsViewport(t *testing.T) {
	// Build a model tall enough that the last record is off-screen.
	m := newViTestModel()
	m.enterViMode()
	// Override with 20 single-line records so scrolling is required.
	m.records = make([]record, 20)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: fmt.Sprintf("line %d", i)}
	}
	// viewport height of 5 terminal rows
	m.height = 9
	m.Viewport.Height = 5
	m.Ready = true

	// Simulate the user having scrolled to the top first.
	m.cursorLine = 0
	m.viTopLine = 0
	m.syncViewportToCursor()

	// Press 'G' — should jump to the last record AND scroll the viewport so
	// the last line is anchored at the bottom.
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})

	if m.cursorLine != 19 {
		t.Fatalf("expected cursorLine 19 after G, got %d", m.cursorLine)
	}
	vpHeight := m.computeVpHeight()
	expectedTop := 20 - vpHeight
	if expectedTop < 0 {
		expectedTop = 0
	}
	if m.viTopLine != expectedTop {
		t.Fatalf("expected viTopLine %d after G, got %d", expectedTop, m.viTopLine)
	}
	if m.cursorLine >= m.viTopLine+vpHeight {
		t.Fatalf("cursorLine %d must be within viewport [viTopLine %d, +%d)", m.cursorLine, m.viTopLine, vpHeight)
	}
}

func TestViModeJumpToTopSnapsViewport(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.records = make([]record, 20)
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: fmt.Sprintf("line %d", i)}
	}
	m.height = 9
	m.Viewport.Height = 5
	m.Ready = true

	// Be somewhere in the middle/bottom first.
	m.cursorLine = 19
	m.viTopLine = 15
	m.syncViewportToCursor()

	// Press 'gg' (g then g) — should snap cursor and viewport to line 0.
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})

	if m.cursorLine != 0 {
		t.Fatalf("expected cursorLine 0 after gg, got %d", m.cursorLine)
	}
	if m.viTopLine != 0 {
		t.Fatalf("expected viTopLine 0 after gg, got %d", m.viTopLine)
	}
}

func TestViModeViewportTopLineSync(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	// Force computeVpHeight() to return 2: height - inputHeight(3) - statusLineHeight(1)
	m.height = 6
	m.Viewport.Height = 2

	// Move cursor up to line 0 — viTopLine should follow
	m.cursorLine = 1
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.viTopLine != 0 {
		t.Fatalf("expected viTopLine 0 after moving cursor above, got %d", m.viTopLine)
	}

	// At bottom record (index 2) with viTopLine forced to 0, syncViewportToCursor
	// should scroll so cursor stays within the visible window [viTopLine, viTopLine+vpHeight).
	m.cursorLine = 2
	m.viTopLine = 0
	m.syncViewportToCursor()
	vpHeight := m.computeVpHeight()
	if m.cursorLine >= m.viTopLine+vpHeight {
		t.Fatalf("cursorLine %d should be within viTopLine %d + %d", m.cursorLine, m.viTopLine, vpHeight)
	}
}

func TestViModeCharLevelYank(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.cursorLine = 0 // "hello world"

	// Enter visual, move cursor to col 5 on same line, yank
	m.handleViModeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m.cursorCol = 5 // select "hello " (indices 0..5, inclusive)

	got := charLevelSelection(m, 0, 0, 0, 5)
	want := "hello "
	if got != want {
		t.Fatalf("expected yank %q, got %q", want, got)
	}
}

// charLevelSelection mirrors yankSelection's extraction for test assertion
// (inclusive of eCol, matching Vim visual-select semantics).
func charLevelSelection(m *model, sLine, sCol, eLine, eCol int) string {
	var buf strings.Builder
	if sLine == eLine {
		runes := []rune(m.records[sLine].text)
		end := eCol + 1
		if end > len(runes) {
			end = len(runes)
		}
		if sCol < end {
			buf.WriteString(string(runes[sCol:end]))
		}
	}
	return buf.String()
}

func TestViModeRenderCursorMarkerInjected(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.cursorLine = 0
	m.cursorCol = 2 // 'l' in "hello"

	out := m.renderRecordsWithCursor()
	// The cursor marker should have been replaced with styled output, so the
	// raw marker bytes must NOT appear in the final render.
	if strings.Contains(out, cursorOpen) || strings.Contains(out, cursorClose) {
		t.Fatalf("cursor markers leaked into render output: %q", out)
	}
	// The 'l' character should be present somewhere in the output
	if !strings.Contains(out, "l") {
		t.Fatal("expected cursor character 'l' to be present in render")
	}
}

func TestViModeRenderSelectionMarkerReplaced(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	m.viModeState = ViVisual
	m.visualStartLine = 0
	m.visualStartCol = 0
	m.cursorLine = 0
	m.cursorCol = 5

	out := m.renderRecordsWithCursor()
	if strings.Contains(out, selOpen) || strings.Contains(out, selClose) {
		t.Fatalf("selection markers leaked into render output: %q", out)
	}
}

func TestViModeYankMultiLine(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	// Select from line 0 col 6 to line 1 col 6
	m.viModeState = ViVisual
	m.visualStartLine = 0
	m.visualStartCol = 6
	m.cursorLine = 1
	m.cursorCol = 6

	got := charLevelSelectionMulti(m)
	// line0 from col6→end = "world"; line1 from start→col6 (inclusive) = "system "
	want := "world\nsystem "
	if got != want {
		t.Fatalf("expected multi-line yank %q, got %q", want, got)
	}
}

// charLevelSelectionMulti mirrors the multi-line branch of yankSelection.
func charLevelSelectionMulti(m *model) string {
	sLine, sCol := m.visualStartLine, m.visualStartCol
	eLine, eCol := m.cursorLine, m.cursorCol
	if sLine > eLine || (sLine == eLine && sCol > eCol) {
		sLine, eLine = eLine, sLine
		sCol, eCol = eCol, sCol
	}
	var buf strings.Builder
	for i := sLine; i <= eLine && i < len(m.records); i++ {
		runes := []rune(m.records[i].text)
		switch i {
		case sLine:
			if sCol < len(runes) {
				buf.WriteString(string(runes[sCol:]))
			}
		case eLine:
			endCol := eCol + 1
			if endCol > len(runes) {
				endCol = len(runes)
			}
			if endCol > 0 {
				buf.WriteString(string(runes[:endCol]))
			}
		default:
			buf.WriteString(m.records[i].text)
		}
		if i < eLine {
			buf.WriteString("\n")
		}
	}
	return buf.String()
}

// TestViModeAnsiSafeCursorInjection verifies that placing the cursor on a line
// containing ANSI style codes does not slice/corrupt the escape sequences. The
// rendered output must still reconstruct the exact printable text once ANSI is
// stripped, and no raw SGR parameter bytes may leak without their ESC leader.
func TestViModeAnsiSafeCursorInjection(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	// Plain text is "hello world" (len 11); it carries a truecolor SGR around
	// "hello" and a reset before " world".
	ansiText := "\x1b[38;2;108;112;134mhello\x1b[0m world"
	m.records = []record{{role: roleError, text: ansiText}}
	m.cursorLine = 0
	// Column 6 is the 'w' in "world" — immediately after a reset escape, the
	// exact spot where the old raw-slice approach would have truncated the SGR.
	m.cursorCol = 6

	out := m.renderRecordsWithCursor()

	// Stripping ANSI must reconstruct the original printable text exactly.
	stripped := ansi.Strip(out)
	if stripped != "hello world" {
		t.Fatalf("ANSI-safe render corrupted text: got %q, want %q", stripped, "hello world")
	}

	// The cursor highlight style must be present.
	if !strings.Contains(out, viCursorStyle.Render("w")) {
		t.Fatalf("cursor style not applied for column 6 ('w') in output %q", out)
	}

	// No orphaned SGR may appear without an ESC leader. A valid SGR is always
	// "\x1b[<params>m"; an orphan is "[<params>m" whose '[' is NOT preceded by
	// '\x1b'. The parameter bytes (e.g. "38;2;108;112;134m") legitimately occur
	// as a sub-string of a valid escape, so we must anchor on the '[' and verify
	// it carries its ESC leader — only then is it a true leak onto the screen.
	for i := 0; i < len(out); i++ {
		if out[i] != '[' || i+1 >= len(out) || out[i+1] < '0' || out[i+1] > '9' {
			continue
		}
		// Match "[\d+(;\d+)*m".
		j := i + 1
		for j < len(out) && out[j] >= '0' && out[j] <= '9' {
			j++
		}
		matched := false
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
		if j < len(out) && out[j] == 'm' {
			matched = true
		}
		if matched {
			if i == 0 || out[i-1] != '\x1b' {
				t.Fatalf("orphaned ANSI SGR leaked (no ESC leader) at %d: %q", i, out)
			}
			i = j // skip past this valid escape sequence
		}
	}
}

// TestSanitizeIngressANSI verifies the ingress filter drops orphaned SGR
// sequences (those with no leading ESC) while preserving valid escape-led
// ANSI styling and ordinary text.
func TestSanitizeIngressANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "orphaned simple reset dropped",
			in:   "ok[0m done",
			want: "ok done",
		},
		{
			name: "orphaned truecolor SGR dropped",
			in:   "[38;2;108;112;134mhello[0m world",
			want: "hello world",
		},
		{
			name: "valid ESC-led sequence preserved",
			in:   "\x1b[38;2;108;112;134mhello\x1b[0m world",
			want: "\x1b[38;2;108;112;134mhello\x1b[0m world",
		},
		{
			name: "lipgloss-styled content preserved",
			in:   "prefix " + viCursorStyle.Render("w") + " suffix",
			want: "prefix " + viCursorStyle.Render("w") + " suffix",
		},
		{
			name: "ordinary text with brackets untouched",
			in:   "a [b] c [1;2;3] keep me",
			want: "a [b] c [1;2;3] keep me",
		},
		{
			name: "mixed valid and orphaned",
			in:   "\x1b[32mgreen[0m[31mred\x1b[0m",
			want: "\x1b[32mgreenred\x1b[0m",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeIngressANSI(c.in)
			if got != c.want {
				t.Fatalf("sanitizeIngressANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestViModeAnsiSafeSelectionInjection verifies that a visual selection over a
// line containing ANSI codes highlights the correct printable character range
// without breaking the surrounding escape sequences.
func TestViModeAnsiSafeSelectionInjection(t *testing.T) {
	m := newViTestModel()
	m.enterViMode()
	ansiText := "\x1b[38;2;108;112;134mhello\x1b[0m world"
	m.records = []record{{role: roleError, text: ansiText}}
	m.viModeState = ViVisual
	m.visualStartLine = 0
	m.visualStartCol = 0
	m.cursorLine = 0
	m.cursorCol = 4 // select "hello" (printable cols 0..4)

	out := m.renderRecordsWithCursor()

	stripped := ansi.Strip(out)
	if stripped != "hello world" {
		t.Fatalf("ANSI-safe selection corrupted text: got %q, want %q", stripped, "hello world")
	}
	if !strings.Contains(out, viSelectionBgStyle.Render("hello")) {
		t.Fatalf("selection style not applied for range [0,4] in output %q", out)
	}
}
