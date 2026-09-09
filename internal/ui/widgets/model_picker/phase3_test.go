package model_picker

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

func TestPhase3ContextualRender(t *testing.T) {
	none := registry.ModelDescriptor{ID: "z", Provider: "nope", Name: "z"}
	mNone := New(seedSnapshot([]registry.ModelDescriptor{none}))
	vNone := mNone.View()
	if !strings.Contains(vNone, "REASONING") {
		t.Errorf("none view must contain REASONING, got:\n%s", vNone)
	}
	if strings.Contains(vNone, "low") {
		t.Errorf("none view must not contain reasoning options, got:\n%s", vNone)
	}
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	mStd := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	vStd := mStd.View()
	if !strings.Contains(vStd, "low") || !strings.Contains(vStd, "high") {
		t.Errorf("standard view must contain low/high, got:\n%s", vStd)
	}
	// Content collapses: none shows minimal dash, standard shows options.
	if !strings.Contains(vNone, "—") {
		t.Errorf("none view must collapse to minimal dash, got:\n%s", vNone)
	}
	if !strings.Contains(vStd, "IZEN MODEL REGISTRY") || !strings.Contains(vStd, "models") {
		t.Errorf("header must show registry + count:\n%s", vStd)
	}
	if !strings.Contains(vStd, "BINDINGS") {
		t.Errorf("view must contain BINDINGS line:\n%s", vStd)
	}
	if !strings.Contains(vStd, "Enter use") {
		t.Errorf("footer must contain Enter use:\n%s", vStd)
	}
}

func TestPhase3ExecutionTruth(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList()
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if _, ok := m.Roles()["plan"]; ok {
		t.Fatal("must not optimistically mutate on BIND")
	}
	if !strings.Contains(m.Status(), "saving...") {
		t.Fatalf("status must show saving..., got %q", m.Status())
	}
	seq := m.LastSeq()
	if seq == 0 {
		t.Fatal("Seq must be stamped on BIND")
	}
	// Failure reverts without corrupting local state.
	m, _ = m.UpdateModel(BindingFailedMsg{Role: "plan", Err: errors.New("disk full"), Seq: seq})
	if _, ok := m.Roles()["plan"]; ok {
		t.Error("failed bind must not mutate roles")
	}
	if !strings.Contains(m.View(), "✕") && !strings.Contains(m.Status(), "✕") {
		t.Errorf("failure must render ✕, status=%q", m.Status())
	}
	// Success confirms with check.
	m2 := New(seedSnapshot(testModels()))
	m2 = m2.FocusList()
	m2, cmd := m2.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	bind := cmd().(modelapp.BindModelToRoleCommand)
	if bind.Seq == 0 {
		t.Error("emitted command must carry Seq")
	}
	m2, _ = m2.UpdateModel(modelapp.BindingResultMsg{Role: "plan", ModelID: bind.ModelID, Seq: bind.Seq})
	if got := m2.Roles()["plan"]; got != bind.ModelID {
		t.Errorf("success must commit roles, got %q", got)
	}
	if !strings.Contains(m2.Status(), "✓") {
		t.Errorf("success must render ✓, got %q", m2.Status())
	}
	// Stale confirmation ignored (same role, older Seq).
	m3 := New(seedSnapshot(testModels()))
	m3 = m3.FocusList()
	m3, _ = m3.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	firstSeq := m3.LastSeq()
	m3, _ = m3.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	secondSeq := m3.LastSeq()
	if secondSeq <= firstSeq {
		t.Fatal("second bind must bump Seq")
	}
	m3, _ = m3.UpdateModel(modelapp.BindingResultMsg{Role: "plan", ModelID: "stale-model", Seq: firstSeq})
	if got := m3.Roles()["plan"]; got == "stale-model" {
		t.Error("stale Seq must be ignored")
	}
}

func TestPhase3Activate(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList()
	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Done() {
		t.Error("Enter must mark done")
	}
	if cmd == nil {
		t.Fatal("Enter must dispatch ActivateModelCommand")
	}
	act, ok := cmd().(modelapp.ActivateModelCommand)
	if !ok {
		t.Fatalf("Enter cmd = %T, want ActivateModelCommand", cmd())
	}
	if act.ModelID == "" {
		t.Error("activate must carry ModelID")
	}
	if m.ActivatedModelID() != act.ModelID {
		t.Errorf("activated = %q, want %q", m.ActivatedModelID(), act.ModelID)
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
	if !strings.Contains(view, "no models loaded") {
		t.Errorf("cold start must render zero-state inline, got:\n%s", view)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("cold start must request background sync")
	}
}
