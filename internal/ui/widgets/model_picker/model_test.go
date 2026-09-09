package model_picker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

func seedTestRegistry(t *testing.T, models []registry.ModelDescriptor) *registry.Registry {
	t.Helper()
	r := registry.NewRegistryWithCachePath("")
	r.SetSeed(models)
	return r
}

func testModels() []registry.ModelDescriptor {
	return []registry.ModelDescriptor{
		{ID: "openrouter/deepseek/deepseek-r1", Provider: "openrouter", Name: "DeepSeek R1", ContextWindow: 64000},
		{ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "Gemini Flash", ContextWindow: 1000000},
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "GPT-4o mini", ContextWindow: 128000},
	}
}

// Init must dispatch a tea.Cmd that loads without blocking the caller, and a
// nil registry must yield a nil command.
func TestInitAsyncLoad(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models.json")
	r := registry.NewRegistryWithCachePath(cachePath)
	r.SetSeed(testModels())
	// Flush seed to disk via a second registry sharing the path: use Sync
	// path indirectly by writing the envelope the loader accepts.
	payload, _ := json.Marshal(map[string]any{"version": 1, "models": testModels()})
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, payload, 0644); err != nil {
		t.Fatal(err)
	}

	m := New(r, t.TempDir())
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init with registry must return a non-nil tea.Cmd")
	}
	msg := cmd() // executes synchronously in test; in production it runs off the UI thread
	loaded, ok := msg.(ModelsLoadedMsg)
	if !ok {
		t.Fatalf("Init cmd msg = %T, want ModelsLoadedMsg", msg)
	}
	if len(loaded.Models) != 3 {
		t.Errorf("loaded %d models, want 3", len(loaded.Models))
	}

	if cmd := New(nil, "").Init(); cmd != nil {
		t.Error("Init with nil registry must return nil")
	}
}

// Typing in search focus must filter via the RAM registry with instant
// responsiveness (no I/O, synchronous Update).
func TestSearchInputFiltersRAM(t *testing.T) {
	r := seedTestRegistry(t, testModels())
	m := New(r, t.TempDir())
	// Simulate loaded state.
	m, _ = m.UpdateModel(ModelsLoadedMsg{Models: r.Snapshot()})

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("deepseek")})
	if len(m.Filtered()) != 1 {
		t.Fatalf("filtered %d, want 1 after typing deepseek", len(m.Filtered()))
	}
	if got := m.Highlighted().ID; got != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("highlight = %q, want deepseek-r1", got)
	}

	// Backspace shrinks the query and restores matches.
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.Filtered()) == 0 {
		t.Error("backspace must keep matches")
	}
}

// Pressing p in list focus must persist .izen/config.json and update badges.
func TestRoleBindingHotkeyPersists(t *testing.T) {
	r := seedTestRegistry(t, testModels())
	work := t.TempDir()
	m := New(r, work)
	m, _ = m.UpdateModel(ModelsLoadedMsg{Models: r.Snapshot()})
	m = m.FocusList()

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})

	data, err := os.ReadFile(filepath.Join(work, ".izen", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	var roles map[string]string
	if err := json.Unmarshal(root["roles"], &roles); err != nil {
		t.Fatalf("parse roles: %v", err)
	}
	if roles["plan"] != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("roles[plan] = %q, want highlighted deepseek-r1", roles["plan"])
	}
	if got := m.Roles()["plan"]; got != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("in-memory roles[plan] = %q", got)
	}
	view := m.View()
	if !strings.Contains(view, "[PLAN]") {
		t.Errorf("view must render [PLAN] badge after binding:\n%s", view)
	}
}

// d/s hotkeys bind default/smol respectively.
func TestDefaultSmolHotkeys(t *testing.T) {
	r := seedTestRegistry(t, testModels())
	work := t.TempDir()
	m := New(r, work)
	m, _ = m.UpdateModel(ModelsLoadedMsg{Models: r.Snapshot()})
	m = m.FocusList().MoveCursor(2) // gpt-4o-mini

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	roles := m.Roles()
	if roles["default"] != "openai/gpt-4o-mini" {
		t.Errorf("roles[default] = %q", roles["default"])
	}
	if roles["smol"] != "openai/gpt-4o-mini" {
		t.Errorf("roles[smol] = %q", roles["smol"])
	}
}

// Typing "p" in search focus must filter, never rebind.
func TestSearchFocusDoesNotBind(t *testing.T) {
	r := seedTestRegistry(t, testModels())
	m := New(r, t.TempDir())
	m, _ = m.UpdateModel(ModelsLoadedMsg{Models: r.Snapshot()})
	// Default focus is search.
	if !m.SearchFocused() {
		t.Fatal("picker must start search-focused")
	}
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if _, ok := m.Roles()["plan"]; ok {
		t.Error("typing p in search focus must not bind the plan role")
	}
	if m.Query() != "p" {
		t.Errorf("query = %q, want p", m.Query())
	}
}

func TestBadgesRender(t *testing.T) {
	d := registry.ModelDescriptor{ID: "openrouter/deepseek/deepseek-r1", Provider: "openrouter", Name: "R1"}
	badges := BadgesFor(d, map[string]string{"default": "openrouter/deepseek/deepseek-r1"})
	joined := strings.Join(badges, " ")
	for _, want := range []string{"[DEFAULT]", "[THINKING]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("badges = %v, want %s", badges, want)
		}
	}
	v := registry.ModelDescriptor{ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "Flash"}
	if got := strings.Join(BadgesFor(v, nil), " "); !strings.Contains(got, "[VISION]") {
		t.Errorf("flash badges = %v, want [VISION]", got)
	}
}

func TestSaveRoleBindingPreservesKeys(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"roles":{"default":"m1"},"checkpoint_retention":7}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveRoleBinding(work, "plan", "m2"); err != nil {
		t.Fatalf("SaveRoleBinding: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	var root map[string]json.RawMessage
	_ = json.Unmarshal(data, &root)
	var roles map[string]string
	_ = json.Unmarshal(root["roles"], &roles)
	if roles["plan"] != "m2" || roles["default"] != "m1" {
		t.Errorf("roles = %v, want preserved default + new plan", roles)
	}
	if !strings.Contains(string(data), "checkpoint_retention") {
		t.Error("SaveRoleBinding must preserve unrelated keys")
	}
}
