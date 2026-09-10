package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// AllModelsEntry: the providers pane pins the synthetic [All models] global
// entry at index 0 (filter ""), and selecting it yields the full catalog.
func TestAllModelsEntry(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	if m.ProviderCursor() != 0 || !m.isAllModelsSelected() {
		t.Fatalf("initial provider cursor = %d, want 0 (All models)", m.ProviderCursor())
	}
	if m.ProviderFilter() != "" {
		t.Fatalf("All models scope must use the empty provider filter, got %q", m.ProviderFilter())
	}
	if n := m.allModelsCount(); n != 3 {
		t.Fatalf("allModelsCount = %d, want 3", n)
	}
	view := m.View()
	if !strings.Contains(view, "All models (3)") {
		t.Errorf("providers pane must render 'All models (3)', got:\n%s", view)
	}
	// All three models visible across providers while All models is focused.
	dbg := m.highlightedProviderLabel()
	if dbg != "All models" {
		t.Errorf("highlightedProviderLabel = %q, want All models", dbg)
	}
	if got := len(m.Models()); got != 3 {
		t.Errorf("All models scope exposes %d models, want 3", got)
	}
}

// TabularColumns: the MODELS pane renders ID | context | price | capability
// flags derived strictly from ModelDescriptor fields — never hardcoded.
func TestTabularModelColumns(t *testing.T) {
	models := []registry.ModelDescriptor{
		{
			ID: "openrouter/anthropic/claude-sonnet-4", Provider: "openrouter", Name: "Claude Sonnet 4",
			ContextWindow: 200000, IsThinking: true,
			InputCostPerM: 3.0, OutputCostPerM: 15.0,
			Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools},
		},
		{
			ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "Gemini Flash",
			ContextWindow: 1000000,
			Capabilities:  []registry.ModelCapability{registry.CapVision},
		},
	}
	m := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(120, 30)
	view := m.View()

	if !strings.Contains(view, "200k") {
		t.Errorf("models pane must render the 200k context column, got:\n%s", view)
	}
	if !strings.Contains(view, "$3.00/$15.00") {
		t.Errorf("models pane must render $3.00/$15.00 pricing column, got:\n%s", view)
	}
	if !strings.Contains(view, "[Thinking]") || !strings.Contains(view, "[Tools]") {
		t.Errorf("models pane must render [Thinking] [Tools] capability badges, got:\n%s", view)
	}
	if !strings.Contains(view, "1M") || !strings.Contains(view, "[Vision]") {
		t.Errorf("models pane must render 1M context and [Vision] badge, got:\n%s", view)
	}
	// Zero-cost models render 'free' (not a blank).
	free := New(seedSnapshot([]registry.ModelDescriptor{{
		ID: "ollama/llama3", Provider: "ollama", ContextWindow: 8000,
	}})).SetPaneFocus(PaneModels).SetSize(120, 30).View()
	if !strings.Contains(free, "free") {
		t.Errorf("zero-cost model must render 'free' price, got:\n%s", free)
	}
}

// RecentSection: a seeded MRU list pins a RECENTLY USED block at the top of
// the models list (scoped to the current provider), and it is suppressed in
// Roles mode.
func TestRecentSection(t *testing.T) {
	recent := []authority.ModelBinding{
		{ProviderID: "openrouter", ModelID: "openrouter/deepseek/deepseek-r1"},
	}
	m := New(seedSnapshot(testModels())).SetRecentBindings(recent)
	if got := len(m.RecentBindings()); got != 1 {
		t.Fatalf("RecentBindings = %d, want 1", got)
	}
	view := m.View()
	if !strings.Contains(view, "RECENTLY USED") {
		t.Errorf("browsing view must pin RECENTLY USED, got:\n%s", view)
	}
	if !strings.Contains(view, "openrouter/deepseek/deepseek-r1") {
		t.Errorf("RECENTLY USED must list the remembered model, got:\n%s", view)
	}
	// Roles mode suppresses the section.
	roles := m.SetShowingRoles(true).View()
	if strings.Contains(roles, "RECENTLY USED") {
		t.Errorf("roles view must not pin RECENTLY USED, got:\n%s", roles)
	}
}

// MRU for unknown models: bindings outside the snapshot are skipped safely.
func TestRecentSectionFiltersUnknownModels(t *testing.T) {
	m := New(seedSnapshot(testModels())).SetRecentBindings([]authority.ModelBinding{
		{ProviderID: "openrouter", ModelID: "ghost/model"},
	})
	view := m.View()
	if strings.Contains(view, "ghost/model") {
		t.Errorf("RECENTLY USED must not render unknown models, got:\n%s", view)
	}
}

// MRURecording: AddRecentModel dedupes by model and keeps newest-first with a
// bounded list of 12.
func TestMRUAddRecentModel(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	for i := 0; i < 5; i++ {
		m = m.AddRecentModel("openrouter/deepseek/deepseek-r1", "openrouter")
	}
	if got := len(m.RecentBindings()); got != 1 {
		t.Fatalf("dedupe failed: %d entries, want 1", got)
	}
	if got := m.RecentBindings()[0].ModelID; got != "openrouter/deepseek/deepseek-r1" {
		t.Fatalf("first entry = %q, want newest model", got)
	}
	m = m.AddRecentModel("google/gemini-2.5-flash", "gemini")
	if got := m.RecentBindings()[0].ModelID; got != "google/gemini-2.5-flash" {
		t.Fatalf("newest must be first, got %q", got)
	}
	if got := len(m.RecentBindings()); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	// Bound at 12 distinct models.
	big := New(seedSnapshot(nil))
	for i := 0; i < 15; i++ {
		big = big.AddRecentModel("m/"+string(rune('a'+i%26)), "p")
	}
	// Collides into 26 buckets, only 12 fit.
	if got := len(big.RecentBindings()); got != 12 {
		t.Fatalf("MRU must cap at 12, got %d", got)
	}
}

// RolesOverride: while showing the roles pane, Enter in the models pane binds
// the highlighted model to the highlighted role override with the active
// reasoning effort.
func TestRolesOverrideEmit(t *testing.T) {
	m := New(seedSnapshot(testModels())).
		SetPaneFocus(PaneModels).
		SetShowingRoles(true).
		SetRoleOverrides(map[string]OverrideBinding{
			RoleOverridePlan: {ModelID: "openrouter/deepseek/deepseek-r1", Provider: "openrouter", Effort: "high"},
		})

	// Roles pane summary surfaces the seeded binding (full model is in the
	// Active line; the pane row itself is hard-truncated by design).
	rolesView := m.SetPaneFocus(PaneRoles).View()
	if !strings.Contains(rolesView, "Plan / Thinking") || !strings.Contains(rolesView, "Commit / Fast") {
		t.Errorf("roles pane must list both override entries, got:\n%s", rolesView)
	}
	if !strings.Contains(rolesView, "→ openrouter/deepseek") {
		t.Errorf("roles pane must summarize the seeded plan binding, got:\n%s", rolesView)
	}
	if !strings.Contains(rolesView, "Role: plan → openrouter/deepseek/deepseek-r1") {
		t.Errorf("active line must render full plan binding, got:\n%s", rolesView)
	}
	if s := m.roleOverrideSummary(RoleOverridePlan); !strings.Contains(s, "deepseek-r1") || !strings.Contains(s, "high") {
		t.Errorf("plan summary = %q, want model + effort", s)
	}

	// Enter in PaneModels emits the override for the highlighted role (plan).
	m = m.SetPaneFocus(PaneModels)
	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter must emit RolePolicyOverrideMsg while roles pane is active")
	}
	msg, ok := cmd().(RolePolicyOverrideMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want RolePolicyOverrideMsg", cmd())
	}
	if msg.Role != RoleOverridePlan || msg.ModelID == "" || msg.Provider == "" {
		t.Fatalf("override = %+v, want plan role + model + provider", msg)
	}
}

// ApiKeyOverlay: opening the overlay pins the whole surface to the secure
// masked input; Esc cancels with ApiKeyInputClosedMsg, Enter submits a
// SaveProviderKeyMsg carrying the (trimmed) key.
func TestApiKeyOverlay(t *testing.T) {
	m := New(seedSnapshot(testModels())).SetProviderCursor(1) // -> openrouter
	opened, cmd := m.openApiKeyInput("openrouter")
	if !opened.ApiKeyInputActive() {
		t.Fatal("overlay must be active after open")
	}
	if msg, ok := cmd().(ApiKeyInputOpenedMsg); !ok || msg.Provider != "openrouter" {
		t.Fatalf("open cmd = %#v, want ApiKeyInputOpenedMsg openrouter", cmd())
	}
	view := opened.View()
	if !strings.Contains(view, "SET API KEY") || !strings.Contains(view, "OPENROUTER") {
		t.Errorf("overlay view must show title + provider, got:\n%s", view)
	}
	if !strings.Contains(view, "Enter save·Esc cancel") && !strings.Contains(view, "Enter save · Esc cancel") {
		t.Errorf("overlay view must show save/cancel hints, got:\n%s", view)
	}

	// Type a secret then submit.
	typed, _ := opened.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-live-123")})
	submitted, cmdSave := typed.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if msg, ok := cmdSave().(SaveProviderKeyMsg); !ok || msg.Provider != "openrouter" || msg.APIKey != "sk-live-123" {
		t.Fatalf("save cmd = %#v, want SaveProviderKeyMsg openrouter/sk-live-123", cmdSave())
	}
	if submitted.ApiKeyInputActive() {
		t.Fatal("overlay must close after submit")
	}

	// Empty submit is a no-op (no message emitted).
	opened2, _ := New(seedSnapshot(nil)).openApiKeyInput("openai")
	_, cmdEmpty := opened2.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmdEmpty != nil {
		t.Fatalf("empty submit must not emit, got %v", cmdEmpty())
	}

	// Esc cancels with ApiKeyInputClosedMsg and closes the overlay.
	opened3, _ := New(seedSnapshot(nil)).openApiKeyInput("gemini")
	closed, cmdClose := opened3.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})
	if closed.ApiKeyInputActive() {
		t.Fatal("Esc must close the overlay")
	}
	if msg, ok := cmdClose().(ApiKeyInputClosedMsg); !ok || msg.Provider != "gemini" {
		t.Fatalf("close cmd = %#v, want ApiKeyInputClosedMsg gemini", cmdClose())
	}

	// The masked value must never leak into the rendered view.
	if strings.Contains(opened.View(), "sk-live-123") {
		t.Error("overlay must mask the API key in the rendered view")
	}
}
