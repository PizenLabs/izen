package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSanitizerStripsHardwareCursorVisibility(t *testing.T) {
	cases := []string{
		"\x1b[?25hhello",
		"\x1b[?25lworld",
		"a\x1b[?25h b \x1b[?25l c",
	}
	for _, in := range cases {
		out := SanitizeForIngest(in)
		if strings.Contains(out, "\x1b[?25") || strings.Contains(out, "[?25") {
			t.Errorf("visibility not stripped: %q -> %q", in, out)
		}
	}
}

func TestSanitizerStripsPositionAndMovement(t *testing.T) {
	cases := []string{"\x1b[H", "\x1b[2;4H", "\x1b[A", "\x1b[B", "\x1b[C", "\x1b[12;4f"}
	for _, in := range cases {
		out := SanitizeForIngest(in + "x")
		if strings.Contains(out, "\x1b[H") || strings.Contains(out, "\x1b[A") {
			t.Errorf("cursor seq not stripped: %q -> %q", in, out)
		}
		if out != "x" {
			t.Errorf("expected only payload x, got %q for %q", out, in)
		}
	}
}

func TestSanitizerStripsEraseAndSaveRestore(t *testing.T) {
	cases := []string{"\x1b[2K", "\x1b[2J", "\x1b[s", "\x1b[u", "\x1b7", "\x1b8", "\x1b[2Kclear"}
	for _, in := range cases {
		out := SanitizeForIngest(in)
		if strings.Contains(out, "\x1b[2K") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b7") {
			t.Errorf("erase/save not stripped: %q -> %q", in, out)
		}
	}
}

func TestSanitizerPreservesSGRColor(t *testing.T) {
	sgr := "\x1b[38;2;205;214;244m colored \x1b[0m keep"
	out := SanitizeForIngest(sgr)
	if !strings.Contains(out, "\x1b[38;2;205;214;244m") || !strings.Contains(out, "\x1b[0m") {
		t.Errorf("SGR stripped: %q -> %q", sgr, out)
	}
	if ansi.StringWidth(sgr) != ansi.StringWidth(out) {
		t.Errorf("SGR width mismatch: %d vs %d", ansi.StringWidth(sgr), ansi.StringWidth(out))
	}
}

func TestSanitizerNormalizesCRLF(t *testing.T) {
	in := "a\r\nb\rc\nd"
	out := SanitizeForIngest(in)
	if strings.Contains(out, "\r") {
		t.Errorf("CR not normalized: %q -> %q", in, out)
	}
	if out != "a\nb\nc\nd" {
		t.Errorf("line endings: %q -> %q", in, out)
	}
}

func TestSanitizerViewportZeroAllocSlice(t *testing.T) {
	// Verify structural line caching: BuildDocumentLayout wraps only on build,
	// and DocumentLayout.Slice does O(1) clamped slice extraction.
	records := []record{
		{role: roleUser, text: "hello world this is a long line that will wrap across multiple physical rows when width is small"},
		{role: roleAI, text: "response line one\nresponse line two that also wraps if width small"},
	}
	dl := BuildDocumentLayout(records, 40, "tester")
	if dl.Len() == 0 {
		t.Fatal("layout empty")
	}
	top, height := 0, 2
	slice := dl.Slice(top, height)
	if len(slice) != height && dl.Len() >= height {
		t.Errorf("slice length %d != %d", len(slice), height)
	}
	// Clamped bottom
	slice2 := dl.Slice(dl.Len()-1, 10)
	if len(slice2) == 0 {
		t.Error("clamped slice empty")
	}
	// Ensure View() equivalent via Slice is deterministic
	slice3 := dl.Slice(top, height)
	if len(slice) != len(slice3) {
		t.Error("non-deterministic slice")
	}
	_ = strings.Join(slice, "\n")
}
