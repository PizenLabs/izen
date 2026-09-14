package components

import (
	"strings"
	"testing"
)

func testAskOptions() []AskOption {
	return []AskOption{
		{ID: "a", Title: "Alpha", Description: "first", Recommended: true},
		{ID: "b", Title: "Beta", Description: "second"},
		{ID: "c", Title: "Gamma"},
	}
}

func TestAskComponentKeybinds(t *testing.T) {
	// j/k + Up/Down navigation.
	a := NewAskComponent("Pick?", false, testAskOptions())
	a.HandleKey("j")
	if a.Cursor != 1 {
		t.Fatalf("j: cursor = %d, want 1", a.Cursor)
	}
	a.HandleKey("k")
	if a.Cursor != 0 {
		t.Fatalf("k: cursor = %d, want 0", a.Cursor)
	}
	a.HandleKey("down")
	if a.Cursor != 1 {
		t.Fatalf("down: cursor = %d, want 1", a.Cursor)
	}
	a.HandleKey("up")
	if a.Cursor != 0 {
		t.Fatalf("up: cursor = %d, want 0", a.Cursor)
	}

	// Enter submits single-select.
	b := NewAskComponent("Pick?", false, testAskOptions())
	b.Cursor = 2
	if resolved := b.HandleKey("enter"); !resolved || !b.Done {
		t.Fatalf("enter must resolve single-select")
	}
	if len(b.Submitted) != 1 || b.Submitted[0] != "c" {
		t.Fatalf("submitted = %v, want [c]", b.Submitted)
	}

	// Space toggles only in multi-select.
	c := NewAskComponent("Pick?", true, testAskOptions())
	c.HandleKey("space")
	c.HandleKey("j")
	c.HandleKey("space")
	c.HandleKey("enter")
	if len(c.Submitted) != 2 || c.Submitted[0] != "a" || c.Submitted[1] != "b" {
		t.Fatalf("multi submitted = %v, want [a b]", c.Submitted)
	}
	single := NewAskComponent("Pick?", false, testAskOptions())
	single.HandleKey("space")
	if len(single.Selected) != 0 {
		t.Fatalf("space must not toggle in single-select")
	}

	// Esc cancels.
	d := NewAskComponent("Pick?", false, testAskOptions())
	if resolved := d.HandleKey("esc"); !resolved || !d.Cancelled {
		t.Fatalf("esc must cancel")
	}
	if resp := d.Response(); !resp.Cancelled {
		t.Fatalf("cancelled response must flag Cancelled")
	}
}

func TestAskComponentRecommendedBadge(t *testing.T) {
	a := NewAskComponent("Pick?", false, testAskOptions())
	out := a.Render(80)
	if !strings.Contains(out, "(Recommended)") {
		t.Errorf("render missing (Recommended) badge:\n%s", out)
	}
	// Empty options never panic.
	empty := NewAskComponent("Pick?", false, nil)
	if out := empty.Render(10); out == "" {
		t.Errorf("empty render must still return a card")
	}
	empty.HandleKey("enter")
	if !empty.Done {
		t.Errorf("enter on empty must resolve")
	}
}
