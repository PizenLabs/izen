package components

import (
	"strings"
	"testing"
)

func TestDiffComponentRender(t *testing.T) {
	c := ParseUnifiedDiff("main.go", "+added\n-removed\n context", 1, 1)
	out := c.Render(80)
	if !strings.Contains(out, "┌─ Edit: main.go (+1/-1)") {
		t.Errorf("header missing:\n%s", out)
	}
	if !strings.Contains(out, "+") || !strings.Contains(out, "-") {
		t.Errorf("diff markers missing:\n%s", out)
	}
	// Wall metadata pinned bottom-right.
	w := c.WithWall("⟨Wall: 1.23s⟩", "⟨Timeout: 30s⟩").Render(80)
	if !strings.Contains(w, "Wall") {
		t.Errorf("wall meta missing:\n%s", w)
	}
	// Broken syntax never panics.
	broken := ParseUnifiedDiff("", "@@ broken\n+++x\n---y\n???", 0, 0)
	if out := broken.Render(10); out == "" {
		t.Errorf("broken diff must still render")
	}
	// 500-line cap with virtualization.
	var big []DiffLine
	for i := 0; i < 600; i++ {
		big = append(big, DiffLine{Kind: ' ', OldNo: i, NewNo: i, Text: "ctx"})
	}
	capped := NewDiffComponent("big.go", big).Render(80)
	if !strings.Contains(capped, "virtualized") {
		t.Errorf("cap marker missing")
	}
	if got := FormatWallMeta(1.234, 30); !strings.Contains(got, "Wall: 1.23s") {
		t.Errorf("FormatWallMeta = %q", got)
	}
}
