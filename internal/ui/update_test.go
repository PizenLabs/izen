package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

func TestSGRMouseLeaksExtractsConcatenatedSequences(t *testing.T) {
	input := "\x1b[<64;10;11M[<65;10;11M\x1b[<0;4;2m"
	got := sgrMouseLeaks(input)
	want := []string{"\x1b[<64;10;11M", "[<65;10;11M", "\x1b[<0;4;2m"}

	if len(got) != len(want) {
		t.Fatalf("got %d sequences, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequence %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSelectedViewportTextForwardMultiLine(t *testing.T) {
	lines := []string{
		"alpha first",
		"bravo second",
		"charlie third",
	}
	got := selectedViewportText(lines, mouseSelectionPoint{row: 0, col: 6}, mouseSelectionPoint{row: 2, col: 6})
	want := "first\nbravo second\ncharlie"
	if got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestSelectedViewportTextReverseSelection(t *testing.T) {
	lines := []string{
		"alpha first",
		"bravo second",
		"charlie third",
	}
	got := selectedViewportText(lines, mouseSelectionPoint{row: 2, col: 6}, mouseSelectionPoint{row: 0, col: 6})
	want := "first\nbravo second\ncharlie"
	if got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestSelectedViewportTextStripsANSIAndTrimsTrailingWhitespace(t *testing.T) {
	lines := []string{
		"\x1b[32mhello world   \x1b[0m",
	}
	got := selectedViewportText(lines, mouseSelectionPoint{row: 0, col: 0}, mouseSelectionPoint{row: 0, col: 20})
	want := "hello world"
	if got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
}

func TestViewportPointIncludesScrollOffset(t *testing.T) {
	m := &model{
		vpReady: true,
		vp:      viewport.New(80, 10),
	}
	m.vp.YOffset = 25

	point, ok := m.viewportPoint(4, 4)
	if !ok {
		t.Fatal("expected point inside viewport")
	}
	if point.row != 28 || point.col != 4 {
		t.Fatalf("point = %+v, want row 28 col 4", point)
	}

	if _, ok := m.viewportPoint(4, 11); ok {
		t.Fatal("expected y at viewport height to be outside")
	}
}

func TestParseSGRMouseAcceptsEscPrefixedAndBareForms(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantButton int
		wantCol    int
		wantRow    int
		wantPress  bool
	}{
		{name: "esc press", input: "\x1b[<64;81;12M", wantButton: 64, wantCol: 81, wantRow: 12, wantPress: true},
		{name: "bare release", input: "[<0;7;5m", wantButton: 0, wantCol: 7, wantRow: 5, wantPress: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			button, col, row, press, ok := parseSGRMouse(tt.input)
			if !ok {
				t.Fatalf("parseSGRMouse(%q) failed", tt.input)
			}
			if button != tt.wantButton || col != tt.wantCol || row != tt.wantRow || press != tt.wantPress {
				t.Fatalf("got (%d, %d, %d, %v), want (%d, %d, %d, %v)",
					button, col, row, press, tt.wantButton, tt.wantCol, tt.wantRow, tt.wantPress)
			}
		})
	}
}
