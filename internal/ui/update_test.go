package ui

import "testing"

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
