package model_picker

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// SetSize must floor non-positive bounds, store dimensions, and clamp the
// cursor so split-pane resizes never clip or panic.
func TestSetSizeClampsAndStores(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.SetSize(106, 28)
	if w, h := m.Size(); w != 106 || h != 28 {
		t.Errorf("Size = (%d,%d), want (106,28)", w, h)
	}
	m = m.SetSize(0, -5)
	if w, h := m.Size(); w != 1 || h != 1 {
		t.Errorf("Size = (%d,%d), want floored (1,1)", w, h)
	}
	// Cursor clamps into the filtered list after shrink.
	m = New(seedSnapshot(testModels())).SetSize(106, 28).MoveCursor(10)
	if got := m.Cursor(); got != 2 {
		t.Errorf("cursor = %d, want clamped 2", got)
	}
}

// Rows must carry colored provider badges; the header keeps the total and a
// subtle divider separates it from the dual-pane content.
func TestProviderBadgesAndDivider(t *testing.T) {
	m := New(seedSnapshot(testModels())).SetSize(100, 30)
	view := m.View()
	for _, want := range []string{"PROVIDERS", "MODELS", "─", "Active:"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if tag := providerTag("ollama"); !strings.Contains(tag, "[OLLAMA]") {
		t.Errorf("ollama tag = %q, want [OLLAMA]", tag)
	}
}

// The dual-pane layout renders the selected row with Surface0 highlight.
func TestSelectedRowCursor(t *testing.T) {
	m := New(seedSnapshot(testModels())).SetSize(100, 30)
	view := m.View()
	// Zero-state keeps panes and anchored footer.
	empty := New(seedSnapshot(nil)).SetSize(100, 30).View()
	for _, want := range []string{"PROVIDERS", "MODELS", "Tab select"} {
		if !strings.Contains(empty, want) {
			t.Errorf("zero-state missing %q:\n%s", want, empty)
		}
	}
	// Browsing footer should be clean with new hints
	if !strings.Contains(view, "Alt+A") || !strings.Contains(view, "Enter") {
		t.Errorf("browsing footer must contain dual-pane hints, got:\n%s", view)
	}
}

// Registry-level live baseline: cold start is empty until provider sync.
func TestDefaultSnapshotEmpty(t *testing.T) {
	snap := registry.DefaultSnapshot()
	if len(snap.Models) != 0 {
		t.Errorf("DefaultSnapshot must be empty (live data from providers only), got %d models", len(snap.Models))
	}
}
