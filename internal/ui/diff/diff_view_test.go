package diff

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// buildGapDiff creates a hunk with `gap` unchanged lines between two changes.
func buildGapDiff(gap int) string {
	var sb strings.Builder
	sb.WriteString("diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n")
	fmt.Fprintf(&sb, "@@ -1,%d +1,%d @@\n", gap+2, gap+2)
	sb.WriteString("-old-first\n")
	for i := 0; i < gap; i++ {
		fmt.Fprintf(&sb, " ctx-%d\n", i)
	}
	sb.WriteString("+new-last\n")
	return sb.String()
}

func TestContextCollapsing(t *testing.T) {
	files, err := ParseUnifiedDiff(buildGapDiff(50))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(files, "test", 100, 30, DefaultCollapseThreshold)
	if m.CollapsedCount() != 1 {
		t.Fatalf("expected 1 collapse bar, got %d", len(files[0].Hunks[0].Lines))
	}
	// 50 unchanged lines must fold into a single summary row:
	// visible = file header + hunk header + del + summary + add = 5 rows.
	if got := m.VisibleLineCount(); got != 5 {
		t.Errorf("visible rows = %d, want 5", got)
	}
	content := m.RenderedContent()
	if !strings.Contains(content, "50 unchanged lines collapsed") {
		t.Errorf("missing collapse bar in:\n%s", content)
	}
	// Toggle expands back to full content (50 + 2 change lines).
	m.ToggleHunk(0, 0)
	if m.CollapsedCount() != 0 {
		t.Errorf("after expand, collapsed bars = %d, want 0", m.CollapsedCount())
	}
	if got := m.VisibleLineCount(); got != 2+52 {
		t.Errorf("expanded rows = %d, want 54", got)
	}
}

func TestToggleAll(t *testing.T) {
	files, err := ParseUnifiedDiff(buildGapDiff(20))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(files, "t", 100, 30, 6)
	m.ToggleAll() // collapse -> expand (started collapsed)
	if m.CollapsedCount() != 0 {
		t.Errorf("ToggleAll expand failed")
	}
	m.ToggleAll() // expand -> collapse
	if m.CollapsedCount() != 1 {
		t.Errorf("ToggleAll collapse failed")
	}
}

func TestRenderPerformance(t *testing.T) {
	// 2,000-line diff must render in < 15ms (no TUI frame drops).
	var sb strings.Builder
	sb.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,2000 +1,2000 @@\n")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&sb, " ctx line number %d with some content to render\n", i)
		fmt.Fprintf(&sb, "-removed line %d\n", i)
	}
	files, err := ParseUnifiedDiff(sb.String())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range files {
		for _, h := range f.Hunks {
			total += len(h.Lines)
		}
	}
	if total < 2000 {
		t.Fatalf("fixture has %d lines, want >= 2000", total)
	}
	m := NewModel(files, "perf", 120, 40, 1<<30) // no folding: measure raw line render
	start := time.Now()
	for _, f := range files {
		for _, h := range f.Hunks {
			var b strings.Builder
			for _, l := range h.Lines {
				b.WriteString(RenderLine(l, 120))
				b.WriteByte('\n')
			}
			_ = b.String()
		}
	}
	_ = m
	if elapsed := time.Since(start); elapsed > 15*time.Millisecond {
		t.Errorf("rendering %d lines took %v, want < 15ms", total, elapsed)
	}
}

func TestToggleHunkOutOfRange(t *testing.T) {
	files, err := ParseUnifiedDiff(buildGapDiff(10))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(files, "t", 100, 30, 6)
	before := m.VisibleLineCount()
	m.ToggleHunk(9, 0)
	m.ToggleHunk(0, 9)
	if m.VisibleLineCount() != before {
		t.Error("out-of-range toggle must be a no-op")
	}
}
