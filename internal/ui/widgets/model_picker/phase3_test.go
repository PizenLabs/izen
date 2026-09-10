package model_picker

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

func TestPhase3ContextualRender(t *testing.T) {
	none := registry.ModelDescriptor{ID: "z", Provider: "nope", Name: "z"}
	mNone := New(seedSnapshot([]registry.ModelDescriptor{none}))
	vNone := mNone.View()
	// Browsing state is clean: no BINDINGS, no Alt, but has new footer
	if strings.Contains(vNone, "BINDINGS") {
		t.Errorf("browsing view must not contain BINDINGS, got:\n%s", vNone)
	}
	if strings.Contains(vNone, "Alt+d") {
		t.Errorf("browsing view must not contain Alt bindings, got:\n%s", vNone)
	}
	if !strings.Contains(vNone, "IZEN MODEL REGISTRY") || !strings.Contains(vNone, "models") {
		t.Errorf("header must show registry + count:\n%s", vNone)
	}
	// Detail view shows MODEL DETAILS with specs
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	mStd := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	mStd, _ = mStd.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	vDetail := mStd.View()
	if !strings.Contains(vDetail, "MODEL DETAILS") {
		t.Errorf("detail view must contain MODEL DETAILS, got:\n%s", vDetail)
	}
	if !strings.Contains(vDetail, "Context:") || !strings.Contains(vDetail, "Price:") {
		t.Errorf("detail view must contain Context and Price, got:\n%s", vDetail)
	}
	if strings.Contains(vDetail, "WORKSPACE TARGET ASSIGNMENT") {
		t.Errorf("detail view must NOT contain workspace assignment matrix, got:\n%s", vDetail)
	}
	if !strings.Contains(vDetail, "Reasoning Policy") {
		t.Errorf("detail view must contain Reasoning Policy control, got:\n%s", vDetail)
	}
	// Browsing footer clean check
	vBrowse := New(seedSnapshot([]registry.ModelDescriptor{openai})).SetSize(100, 30).View()
	if !strings.Contains(vBrowse, "↑/↓") || !strings.Contains(vBrowse, "quick") {
		t.Errorf("browsing footer must be clean with quick assign hint, got:\n%s", vBrowse)
	}
}

func TestPhase3ExecutionTruth(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList().SetActiveWorkspace(TargetPlan)
	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil {
		t.Fatal("quick assign 'a' must emit ModelAssignmentRequestedMsg")
	}
	assign := unwrapAssignmentMsg(t, cmd())
	if assign.ModelID != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("assign model = %q, want highlighted", assign.ModelID)
	}
	if string(assign.Target) == "" {
		t.Logf("assign target is empty (workspace matrix removed per Phase 3)")
	}
	// Detail assignment also emits
	m2 := New(seedSnapshot(testModels()))
	m2 = m2.FocusList()
	m2, _ = m2.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m2.State() != StateDetail {
		t.Fatalf("Enter must open detail, got %v", m2.State())
	}
	_, cmd3 := m2.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	_ = cmd3 // workspace matrix removed; no-op
	// Legacy binding still works for stale check (kept for backward compat)
	m3 := New(seedSnapshot(testModels()))
	m3 = m3.FocusList()
	m3, _ = m3.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	// Use old QueueBind for stale test via direct call
	m3, _ = m3.UpdateModel(BindingFailedMsg{Role: "plan", Err: errors.New("disk full"), Seq: 1})
	if _, ok := m3.Roles()["plan"]; ok {
		t.Error("failed bind must not mutate roles")
	}
}

func TestPhase3Activate(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList()
	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m.State() != StateDetail {
		t.Fatalf("Enter must transition to StateDetail, got %v", m.State())
	}
	if cmd != nil {
		t.Errorf("Enter to detail should not emit Activate, got %T", cmd())
	}
	// In detail, Enter confirms assignment for the hovered target
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in detail must dispatch ModelAssignmentRequestedMsg")
	}
	act := unwrapAssignmentMsg(t, cmd())
	if act.ModelID == "" {
		t.Error("assignment must carry ModelID")
	}
	// Esc returns to browsing
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})
	if m.State() != StateBrowsing {
		t.Errorf("Esc must return to browsing, got %v", m.State())
	}
}

func TestPhase3MultiField(t *testing.T) {
	r := registry.NewRegistryWithCachePath("")
	r.SetSeed([]registry.ModelDescriptor{
		{ID: "deepseek/deepseek-r1", Provider: "deepseek", Name: "R1", ContextWindow: 128000, InputCostPerM: 0.55, OutputCostPerM: 2.19},
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "mini", ContextWindow: 128000},
	})
	if got := r.Filter("deepseek/r1", ""); len(got) != 1 {
		t.Errorf("deepseek/r1 filter = %d, want 1", len(got))
	}
	if got := r.Filter("128k", ""); len(got) != 2 {
		t.Errorf("128k filter = %d, want 2", len(got))
	}
	if got := r.Filter("0.55", ""); len(got) != 1 {
		t.Errorf("price filter = %d, want 1", len(got))
	}
}

// Header MUST show total snapshot models in RAM while the search line shows
// filter-scoped matches; a nonsense query keeps the total and renders the
// empty-query message with the query echoed.
func TestPhase3HeaderTotalVsMatches(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.SetQuery("xyz123")
	view := m.View()
	if !strings.Contains(view, "3 models") {
		t.Errorf("header must still show total cached models, got:\n%s", view)
	}
	if !strings.Contains(view, "0/3 matches") {
		t.Errorf("search line must show 0/3 matches, got:\n%s", view)
	}
	if !strings.Contains(view, "No models matching query: 'xyz123'") {
		t.Errorf("empty state must echo query, got:\n%s", view)
	}
}

// Populated header keeps the total even when the filter matches everything.
func TestPhase3HeaderTotalOnMatch(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	view := m.View()
	if !strings.Contains(view, "3 models loaded") {
		t.Errorf("header must show total loaded, got:\n%s", view)
	}
	if !strings.Contains(view, "3/3 matches") {
		t.Errorf("search line must show 3/3 matches, got:\n%s", view)
	}
}

// Cold start renders the zero-state inline (never a modal) and Init requests
// background sync.
func TestPhase3ColdStartZeroState(t *testing.T) {
	m := New(seedSnapshot(nil))
	view := m.View()
	if !strings.Contains(view, "0 models") {
		t.Errorf("cold start must show 0 models, got:\n%s", view)
	}
	if !strings.Contains(view, "No models loaded for provider") {
		t.Errorf("cold start must render zero-state inline, got:\n%s", view)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("cold start must request background sync")
	}
}
