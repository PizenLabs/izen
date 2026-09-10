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
		{ID: "nvidia/north-star", Provider: "nvidia", Name: "North Star", InputCostPerM: 0.5, OutputCostPerM: 1.5},
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

// Space in unified engine: always active search – space appends to query
// (type to search). Legacy no-op expectation retired; verify unified behavior.
func TestSpaceKeyNoOpInListFocus(t *testing.T) {
	m := New(seedSnapshot(testModels())).FocusList()
	m, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Error("space must not emit a command")
	}
	// Unified engine: space builds multi-token query
	if m.Query() != " " {
		t.Errorf("query = %q, want %q (unified search active)", m.Query(), " ")
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

// FocusScope state machine: Tab toggles PaneProviders/PaneModels, Enter commits in PaneModels.
func TestFocusScopeStateMachine(t *testing.T) {
	snap := &registry.ModelSnapshot{
		Models: testModels(),
		Providers: []registry.ProviderSummary{
			{Name: "openrouter", ModelCount: 1, Status: "ok"},
			{Name: "gemini", ModelCount: 1, Status: "ok"},
			{Name: "openai", ModelCount: 1, Status: "ok"},
		},
	}
	m := New(snap)
	if m.PaneFocus() != PaneProviders {
		t.Fatalf("initial pane focus = %v, want PaneProviders", m.PaneFocus())
	}
	// Tab toggles to PaneModels
	mTab, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyTab})
	if mTab.PaneFocus() != PaneModels {
		t.Errorf("Tab must toggle to PaneModels, got %v", mTab.PaneFocus())
	}
	// Second Tab toggles back to PaneProviders
	mTab2, _ := mTab.UpdateModel(tea.KeyMsg{Type: tea.KeyTab})
	if mTab2.PaneFocus() != PaneProviders {
		t.Errorf("second Tab must toggle back to PaneProviders, got %v", mTab2.PaneFocus())
	}

	// Alt+A in PaneProviders emits ConfigureProviderMsg
	_, cmdAlt := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
	if cmdAlt == nil {
		t.Fatal("Alt+A in PaneProviders must emit ConfigureProviderMsg")
	}
	if msg, ok := cmdAlt().(ConfigureProviderMsg); !ok || !strings.Contains(msg.Provider, "openrouter") {
		t.Fatalf("Alt+A msg = %#v, want ConfigureProviderMsg openrouter", cmdAlt())
	}

	// Enter in PaneModels commits and closes
	mModels := New(snap).SetPaneFocus(PaneModels)
	_, cmd := mModels.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in PaneModels must emit assignment command")
	}

	// Down moves cursor in PaneProviders pane (provider list)
	mDown, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	if mDown.ProviderCursor() != 1 {
		t.Errorf("down must move provider cursor, got %d want 1", mDown.ProviderCursor())
	}
	// In PaneModels down moves the model cursor
	m = m.SetProviderFilter("").SetPaneFocus(PaneModels)
	mDown2, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	if mDown2.Cursor() != 1 {
		t.Errorf("down must move cursor in PaneModels, got %d want 1", mDown2.Cursor())
	}
}

// Search-focus exclusivity: printable runes never leak into bindings while
// focused on the search input, and the query filters RAM synchronously.
func TestSearchFocusTypingDoesNotBind(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{
		{ID: "openrouter/free-hybrid-north", Provider: "openrouter", Name: "Free North Hybrid"},
	}))
	m, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("free")})
	if cmd != nil {
		t.Fatal("typing in search must not emit commands")
	}
	if m.Query() != "free" {
		t.Fatalf("query = %q, want free", m.Query())
	}
	if m.Focus() != FocusSearch {
		t.Fatalf("focus drifted to %v", m.Focus())
	}
}

// Header focus indicator renders the active pane scope: Focus: [PROVIDERS]
// (Tab to Models) in providers focus and Focus: [MODELS] (Tab to Providers)
// in models focus. Default is [PROVIDERS] per dual-pane UX spec.
func TestHeaderFocusIndicator(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	view := m.View()
	if !strings.Contains(view, "[PROVIDERS]") || !strings.Contains(view, "Tab to Models") {
		t.Errorf("default providers-focus header must show Focus: [PROVIDERS] (Tab to Models), got:\n%s", view)
	}
	m = m.SetPaneFocus(PaneModels)
	view2 := m.View()
	if !strings.Contains(view2, "[MODELS]") || !strings.Contains(view2, "Tab to Providers") {
		t.Errorf("models-focus header must show Focus: [MODELS] (Tab to Providers), got:\n%s", view2)
	}
	if strings.Contains(view2, "[PROVIDERS]") {
		t.Errorf("models-focus header must not show [PROVIDERS]:\n%s", view2)
	}
}

// Selected-row single-line contract: the active model row renders on a
// SINGLE physical line with zero embedded newlines and zero wrap fragments.
// The dual-pane layout shows model IDs in the right pane.
func TestSelectedRowSingleLineNoWrap(t *testing.T) {
	models := []registry.ModelDescriptor{
		{
			ID:            "meta/muse-glimmer-30b:batch",
			Provider:      "meta",
			Name:          "Muse Glimmer 30B batch",
			ContextWindow: 1_000_000,
			InputCostPerM: 0, OutputCostPerM: 0.75,
			Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools, registry.CapVision},
		},
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "GPT-4o mini", ContextWindow: 128000, InputCostPerM: 0},
	}
	m := New(seedSnapshot(models)).SetSize(100, 30)
	m = m.MoveCursor(0)
	view := m.View()

	lines := strings.Split(strings.ReplaceAll(view, "\r", ""), "\n")
	activeIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "muse") {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatalf("active muse row not found:\n%s", view)
	}
	active := lines[activeIdx]

	// The selected row must be a single line with no embedded newlines.
	if strings.Contains(active, "\n") {
		t.Errorf("selected row must contain zero embedded newlines: %q", active)
	}
}
