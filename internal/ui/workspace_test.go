package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSplitVis(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		visLen int
		wantL  string
		wantR  string
	}{
		{
			name:   "zero length",
			input:  "hello",
			visLen: 0,
			wantL:  "",
			wantR:  "hello",
		},
		{
			name:   "split at middle",
			input:  "hello",
			visLen: 3,
			wantL:  "hel",
			wantR:  "lo",
		},
		{
			name:   "split at end",
			input:  "abc",
			visLen: 3,
			wantL:  "abc",
			wantR:  "",
		},
		{
			name:   "split beyond visible length pads left",
			input:  "hi",
			visLen: 5,
			wantL:  "hi   ",
			wantR:  "",
		},
		{
			name:   "empty string",
			input:  "",
			visLen: 3,
			wantL:  "   ",
			wantR:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotL, gotR := splitVis(tt.input, tt.visLen)
			if gotL != tt.wantL {
				t.Errorf("splitVis(%q, %d) left = %q, want %q", tt.input, tt.visLen, gotL, tt.wantL)
			}
			if gotR != tt.wantR {
				t.Errorf("splitVis(%q, %d) right = %q, want %q", tt.input, tt.visLen, gotR, tt.wantR)
			}
		})
	}
}

func TestSplitVisANSISafe(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Render("hello")
	left, right := splitVis(styled, 3)
	if !strings.Contains(left, "hel") {
		t.Errorf("expected left to contain 'hel', got %q", left)
	}
	if !strings.Contains(right, "lo") {
		t.Errorf("expected right to contain 'lo', got %q", right)
	}
}

func TestOverlayOnBasic(t *testing.T) {
	bg := "line1\nline2\nline3\nline4\nline5"
	fg := "XX"
	w, h := 5, 5

	result := overlayOn(bg, fg, w, h)
	lines := strings.Split(result, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// fg "XX" should be centered at row=2 (index), col=1
	// line0: "line1" -> unchanged
	if lines[0] != "line1" {
		t.Errorf("line[0] = %q, want %q", lines[0], "line1")
	}
	// line2: "line3" -> "lXXe3" (XX replaces positions 1-2)
	if !strings.Contains(lines[2], "XX") {
		t.Errorf("line[2] = %q, expected to contain 'XX'", lines[2])
	}
}

func TestOverlayOnLarger(t *testing.T) {
	bg := "abcd\nefgh\nijkl\nmnop\nqrst"
	fg := "--\n--"
	w, h := 4, 5

	result := overlayOn(bg, fg, w, h)
	lines := strings.Split(result, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// fg is 2x2, centered at row=1 (index), col=1.
	// Background is padded to width 4, so each bg line is "abcd"/"efgh"/etc.
	// sx = (4-2)/2 = 1, sy = (5-2)/2 = 1.
	// line1: left="e", fl="--", right="h"  => "e\033[0m--\033[0mh"
	// line2: left="i", fl="--", right="l"  => "i\033[0m--\033[0ml"
	if !strings.Contains(lines[1], "--") {
		t.Errorf("line[1] = %q, expected to contain '--'", lines[1])
	}
	if !strings.Contains(lines[2], "--") {
		t.Errorf("line[2] = %q, expected to contain '--'", lines[2])
	}
}

func TestOverlayOnANSIPreservation(t *testing.T) {
	bg := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4")).Render("hello world")
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true).Render("!!")
	w, h := 20, 3
	result := overlayOn(bg, fg, w, h)

	// Both ANSI strings should be present in result
	if !strings.Contains(result, "hello") {
		t.Errorf("result should contain background text 'hello'")
	}
	if !strings.Contains(result, "!!") {
		t.Errorf("result should contain foreground text '!!'")
	}
	if !strings.Contains(result, "world") {
		t.Errorf("result should contain background text 'world'")
	}
}

func TestOverlayOnEmptyBackground(t *testing.T) {
	fg := "modal"
	w, h := 10, 3
	result := overlayOn("", fg, w, h)
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	// fg centered: sy = (3-1)/2 = 1, sx = (10-5)/2 = 2
	if !strings.Contains(lines[1], "modal") {
		t.Errorf("line[1] = %q, expected to contain 'modal'", lines[1])
	}
}
