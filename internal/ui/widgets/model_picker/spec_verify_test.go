package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
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

// FocusScope state machine: UNIFIED ENGINE – Tab wall eliminated.
// Search is ALWAYS active; Tab is no-op, Down moves cursor (global nav),
// Alt+d/p/s/v/a bind roles, ←/→ cycle reasoning, typing always filters.
func TestFocusScopeStateMachine(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	if m.Focus() != FocusList {
		t.Fatalf("initial focus = %v, want FocusList", m.Focus())
	}
	// Unified: Tab is no-op (search always active) – focus must not toggle.
	mTab, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyTab})
	if mTab.Focus() != m.Focus() {
		t.Errorf("Tab must be no-op in unified engine, got %v want %v", mTab.Focus(), m.Focus())
	}
	mShift, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyShiftTab})
	if mShift.Focus() != m.Focus() {
		t.Errorf("Shift+Tab must be no-op in unified engine")
	}

	// Alt bindings must work regardless of focus.
	for _, tc := range []struct{ key, role string }{
		{"alt+d", "default"}, {"alt+p", "plan"}, {"alt+s", "smol"}, {"alt+v", "vision"}, {"alt+a", "adviser"},
	} {
		mm := m
		var cmd tea.Cmd
		// Send Alt+key as Runes with Alt flag
		r := tc.key[4:] // after "alt+"
		_, cmd = mm.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r), Alt: true})
		if cmd == nil {
			t.Fatalf("alt %q must emit binding cmd", tc.key)
		}
		if bind := cmd().(modelapp.BindModelToRoleCommand); string(bind.Role) != tc.role {
			t.Errorf("alt %q role = %q, want %q", tc.key, string(bind.Role), tc.role)
		}
	}
	// Plain d/p/s/v/a must NOT bind in unified engine when focus is Search – they type.
	mSearch := m.FocusSearch()
	for _, key := range []string{"d", "p", "s", "v", "a"} {
		mm, cmd := mSearch.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Errorf("plain %q in search focus must not emit bind (unified search), got cmd", key)
		}
		_ = mm
	}

	// ←/→ cycle reasoning without touching the cursor or query.
	before := m.Cursor()
	m2, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	if cmd != nil {
		t.Error("→ must emit no command")
	}
	if m2.Cursor() != before {
		t.Errorf("→ changed cursor %d→%d", before, m2.Cursor())
	}
	if o, ok := m2.CurrentReasoningOption(); !ok || o != "low" {
		t.Errorf("after → reasoning option = %q,%v, want low,true", o, ok)
	}

	// Down moves cursor globally (no focus toggle)
	mDown, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	if mDown.Cursor() != 1 {
		t.Errorf("down must move cursor in unified engine, got %d want 1", mDown.Cursor())
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

// Header focus indicator renders the active scope: Focus: [SEARCH] (Tab to
// List) in search focus and Focus: [LIST] (Tab to Search) in list focus.
// Default is [LIST] per UX spec.
func TestHeaderFocusIndicator(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	view := m.View()
	if !strings.Contains(view, "[LIST]") || !strings.Contains(view, "Tab to Search") {
		t.Errorf("default list-focus header must show Focus: [LIST] (Tab to Search), got:\n%s", view)
	}
	m = m.FocusSearch()
	view2 := m.View()
	if !strings.Contains(view2, "[SEARCH]") || !strings.Contains(view2, "Tab to List") {
		t.Errorf("search-focus header must show Focus: [SEARCH] (Tab to List), got:\n%s", view2)
	}
	if strings.Contains(view2, "[LIST]") {
		t.Errorf("search-focus header must not show [LIST]:\n%s", view2)
	}
}

// Selected-row single-line contract: the active row renders the word "Tools"
// on the SAME line as the cursor with zero embedded newlines, zero wrapped
// fragment below the row, and no frame-breaking blank line. Lipgloss width
// wrap must never split the long capabilities cell onto line 2.
func TestSelectedRowSingleLineNoWrap(t *testing.T) {
	models := []registry.ModelDescriptor{
		{
			ID:            "meta/muse-glimmer-30b:batch",
			Provider:      "meta",
			Name:          "Muse Glimmer 30B batch",
			ContextWindow: 1_000_000,
			InputCostPerM: 0.25, OutputCostPerM: 0.75,
			Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools, registry.CapVision},
		},
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "GPT-4o mini", ContextWindow: 128000},
	}
	m := New(seedSnapshot(models)).SetSize(100, 30)
	m = m.MoveCursor(0)
	view := m.View()

	lines := strings.Split(strings.ReplaceAll(view, "\r", ""), "\n")
	activeIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, ">") && strings.Contains(ln, "muse") {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		t.Fatalf("active muse row not found:\n%s", view)
	}
	active := lines[activeIdx]

	// The word Tools must stay on the SELECTED row line itself.
	if !strings.Contains(active, "Tools") {
		t.Errorf("Tools must remain on the selected row line:\n%q", active)
	}
	if strings.Contains(active, "\n") {
		t.Errorf("selected row must contain zero embedded newlines: %q", active)
	}

	// Zero wrap fragments: any other line carrying "Tools" must be a real
	// model row (contains a "/" model ID + provider pill) or chrome, never a
	// bare continuation of the caps cell spilled onto line 2.
	for i, ln := range lines {
		if i == activeIdx || !strings.Contains(ln, "Tools") {
			continue
		}
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		isRow := strings.Contains(ln, "/") && strings.Contains(ln, "[")
		isChrome := strings.HasPrefix(trimmed, "REASONING") ||
			strings.HasPrefix(trimmed, "BINDINGS") ||
			strings.HasPrefix(trimmed, "IZEN") ||
			strings.Contains(trimmed, "Enter") ||
			strings.Contains(trimmed, "Focus:")
		if !isRow && !isChrome {
			t.Errorf("wrap fragment on line %d (selected-rows's caps spilled to line 2): %q\nfull view:\n%s", i+1, ln, view)
		}
	}
}
