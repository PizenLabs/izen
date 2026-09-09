package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Typing "free north" (with a real KeySpace press) must keep the space in the
// query and filter with AND logic: only models matching BOTH tokens survive.
func TestSpaceKeyBuildsMultiTokenQuery(t *testing.T) {
	models := []registry.ModelDescriptor{
		{ID: "openrouter/free-hybrid-north", Provider: "openrouter", Name: "Free North Hybrid"},
		{ID: "openrouter/free-only", Provider: "openrouter", Name: "Free Only"},
		{ID: "nvidia/north-star", Provider: "nvidia", Name: "North Star"},
	}
	m := New(seedSnapshot(models))

	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("free")})
	_ = cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeySpace})
	_ = cmd
	if m.Query() != "free " {
		t.Fatalf("query after space = %q, want %q", m.Query(), "free ")
	}
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("north")})
	_ = cmd
	if m.Query() != "free north" {
		t.Fatalf("query = %q, want %q", m.Query(), "free north")
	}
	got := m.Filtered()
	if len(got) != 1 || got[0].ID != "openrouter/free-hybrid-north" {
		ids := make([]string, 0, len(got))
		for _, d := range got {
			ids = append(ids, d.ID)
		}
		t.Fatalf("filtered = %v, want only the both-tokens hybrid", ids)
	}
}

// Space in list focus must be a no-op: no query mutation, no bind command,
// no selection side effect.
func TestSpaceKeyNoOpInListFocus(t *testing.T) {
	m := New(seedSnapshot(testModels())).FocusList()
	before := m.Query()
	m, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Error("space in list focus must not emit a command")
	}
	if m.Query() != before {
		t.Errorf("query = %q, want unchanged %q", m.Query(), before)
	}
	if m.Done() {
		t.Error("space must never trigger selection")
	}
}

// Dynamic context units: >=1M renders M (whole or one-decimal), >=1k renders
// k, smaller renders raw, non-positive renders the unknown marker.
func TestFormatContextWindowUnits(t *testing.T) {
	cases := map[int]string{
		1000000: "1M",
		2000000: "2M",
		1500000: "1.5M",
		128000:  "128k",
		256000:  "256k",
		2000:    "2k",
		500:     "500",
		0:       "-",
		-10:     "-",
	}
	for in, want := range cases {
		if got := formatContextWindow(in); got != want {
			t.Errorf("formatContextWindow(%d) = %q, want %q", in, got, want)
		}
	}
	// The table cell keeps the em-dash unknown marker but shares M/k math.
	if got := formatContext(1000000); got != "1M" {
		t.Errorf("formatContext(1000000) = %q, want 1M", got)
	}
	if got := formatContext(2000000); got != "2M" {
		t.Errorf("formatContext(2000000) = %q, want 2M", got)
	}
	if got := formatContext(128000); got != "128k" {
		t.Errorf("formatContext(128000) = %q, want 128k", got)
	}
	if got := formatContext(0); got != "—" {
		t.Errorf("formatContext(0) = %q, want —", got)
	}
}

// Cycling right off "default" must produce a concrete selection; cycling back
// left to "default" must return to nil (omitted downstream).
func TestDefaultCycleRoundTrip(t *testing.T) {
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	m := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	if sel := m.CurrentReasoningSelection(); sel != nil {
		t.Fatalf("initial selection = %+v, want nil (default)", sel)
	}
	m = m.CycleReasoning(1)
	sel := m.CurrentReasoningSelection()
	if sel == nil || sel.Option != "low" {
		t.Fatalf("after right, selection = %+v, want low", sel)
	}
	m = m.CycleReasoning(-1)
	if sel := m.CurrentReasoningSelection(); sel != nil {
		t.Fatalf("after left back to default, selection = %+v, want nil", sel)
	}
	if bar := m.RenderReasoningBar(); !strings.Contains(bar, "default") {
		t.Errorf("bar = %q, want default tier rendered", bar)
	}
}
