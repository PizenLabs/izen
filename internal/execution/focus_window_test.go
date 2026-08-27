package execution

import (
	"fmt"
	"strings"
	"testing"
)

// ── region-focused bounded-patch windows ────────────────────────────────────

func TestFocusSlice_ClampsAndOffsets(t *testing.T) {
	src := "l1\nl2\nl3\nl4\nl5\n" // 5 lines + trailing newline
	cases := []struct {
		name        string
		start, end  int
		wantContent string
		wantOffset  int
	}{
		{"exact middle", 2, 4, "l2\nl3\nl4", 1},
		{"single line", 3, 3, "l3", 2},
		{"end clamped to file keeps exact tail bytes", 4, 99, "l4\nl5\n", 3},
		{"unset focus (start<1) signals whole-file fallback", 0, 0, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, offset := focusSlice(src, tc.start, tc.end)
			if got != tc.wantContent {
				t.Fatalf("content = %q, want %q", got, tc.wantContent)
			}
			if offset != tc.wantOffset {
				t.Fatalf("offset = %d, want %d", offset, tc.wantOffset)
			}
		})
	}
}

func TestFocusSlice_DegenerateInputsFallBackToWholeFile(t *testing.T) {
	for name, tc := range map[string]struct {
		src        string
		start, end int
	}{
		"empty source":      {"   ", 1, 5},
		"inverted range":    {"a\nb\n", 4, 2},
		"start beyond file": {"a\nb\n", 9, 12},
	} {
		t.Run(name, func(t *testing.T) {
			got, offset := focusSlice(tc.src, tc.start, tc.end)
			if got != "" || offset != 0 {
				t.Fatalf("focusSlice(%q,%d,%d) = (%q,%d), want empty fallback",
					tc.src, tc.start, tc.end, got, offset)
			}
		})
	}
}

// A region-focused selection must only ever rotate WITHIN the assigned
// interval: no attempt index may surface another unit's lines.
func TestRegionFocusedWindowRotationStaysInsideAssignedRegion(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "filler line %03d content for window rotation test xxxxxxxxxx\n", i)
	}
	original := b.String()

	const focusStart, focusEnd = 50, 80
	focusSource, offset := focusSlice(original, focusStart, focusEnd)
	if focusSource == "" {
		t.Fatal("focus slice unexpectedly empty")
	}
	for attempt := 1; attempt <= 6; attempt++ {
		w := selectBoundedPatchWindowScaled(focusSource, attempt, maxBoundedPatchContextBytes)
		absStart, absEnd := w.startLine+offset, w.endLine+offset
		if absStart < focusStart || absEnd > focusEnd {
			t.Fatalf("attempt %d window lines %d-%d escapes assigned region %d-%d",
				attempt, absStart, absEnd, focusStart, focusEnd)
		}
	}
}
