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
	// Browsing state is clean: no BINDINGS, no Alt bindings in footer
	if strings.Contains(vNone, "BINDINGS") {
		t.Errorf("browsing view must not contain BINDINGS, got:\n%s", vNone)
	}
	if strings.Contains(vNone, "Alt+d") {
		t.Errorf("browsing view must not contain Alt bindings, got:\n%s", vNone)
	}
	if !strings.Contains(vNone, "IZEN MODEL REGISTRY") || !strings.Contains(vNone, "models") {
		t.Errorf("header must show registry + count:\n%s", vNone)
	}
	// Dual-pane layout shows PROVIDERS and MODELS panes
	if !strings.Contains(vNone, "PROVIDERS") || !strings.Contains(vNone, "MODELS") {
		t.Errorf("browsing view must show PROVIDERS and MODELS panes:\n%s", vNone)
	}
	// Footer shows new 3-pane keybindings
	if !strings.Contains(vNone, "Tab") || !strings.Contains(vNone, "API key") {
		t.Errorf("browsing footer must show Tab and API key hints, got:\n%s", vNone)
	}
	// Active line shows provider/model
	if !strings.Contains(vNone, "Active:") {
		t.Errorf("browsing view must show Active line, got:\n%s", vNone)
	}
	// Detail view shows MODEL DETAILS with specs
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	mStd := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	mStd, _ = mStd.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
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
}

func TestPhase3ExecutionTruth(t *testing.T) {
	// Enter in PaneModels emits assignment directly
	m := New(seedSnapshot(testModels()))
	m = m.SetPaneFocus(PaneModels)
	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in PaneModels must emit ModelAssignmentRequestedMsg")
	}
	assign := unwrapAssignmentMsg(t, cmd())
	if assign.ModelID != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("assign model = %q, want highlighted", assign.ModelID)
	}
	// Binding failure still recorded correctly
	m3 := New(seedSnapshot(testModels()))
	m3 = m3.SetPaneFocus(PaneModels)
	m3, _ = m3.UpdateModel(BindingFailedMsg{Role: "plan", Err: errors.New("disk full"), Seq: 1})
	if _, ok := m3.Roles()["plan"]; ok {
		t.Error("failed bind must not mutate roles")
	}
}

func TestPhase3Activate(t *testing.T) {
	// Enter in PaneModels commits directly (no intermediate detail step)
	m := New(seedSnapshot(testModels()))
	m = m.SetPaneFocus(PaneModels)
	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in PaneModels must dispatch ModelAssignmentRequestedMsg")
	}
	act := unwrapAssignmentMsg(t, cmd())
	if act.ModelID == "" {
		t.Error("assignment must carry ModelID")
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

// Header MUST show total snapshot models. A nonsense query filters the
// models pane but the header count remains.
func TestPhase3HeaderTotalVsMatches(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.SetQuery("xyz123")
	view := m.View()
	if !strings.Contains(view, "3 models") {
		t.Errorf("header must still show total cached models, got:\n%s", view)
	}
	// Models pane shows empty when no matches
	if !strings.Contains(view, "(no models)") {
		t.Errorf("empty models pane must show placeholder, got:\n%s", view)
	}
}

// Populated header keeps the total even when the filter matches everything.
func TestPhase3HeaderTotalOnMatch(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	view := m.View()
	if !strings.Contains(view, "3 models loaded") {
		t.Errorf("header must show total loaded, got:\n%s", view)
	}
}

// Cold start renders empty panes and Init requests background sync.
func TestPhase3ColdStartZeroState(t *testing.T) {
	m := New(seedSnapshot(nil))
	view := m.View()
	if !strings.Contains(view, "0 models") {
		t.Errorf("cold start must show 0 models, got:\n%s", view)
	}
	// Empty models pane shows placeholder
	if !strings.Contains(view, "(no models)") {
		t.Errorf("cold start must render empty models pane, got:\n%s", view)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("cold start must request background sync")
	}
}
