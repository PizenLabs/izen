package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

func seedSnapshot(models []registry.ModelDescriptor) *registry.ModelSnapshot {
	return &registry.ModelSnapshot{Models: append([]registry.ModelDescriptor(nil), models...)}
}

func testModels() []registry.ModelDescriptor {
	return []registry.ModelDescriptor{
		{ID: "openrouter/deepseek/deepseek-r1", Provider: "openrouter", Name: "DeepSeek R1", ContextWindow: 64000},
		{ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "Gemini Flash", ContextWindow: 1000000},
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "GPT-4o mini", ContextWindow: 128000},
	}
}

// Init must be pure (zero I/O) and construction must populate instantly
// from the snapshot. Populated pickers return nil; cold-start (empty)
// pickers emit SyncRequestedMsg so the parent pulls provider APIs in the
// background without blocking the TUI.
func TestInitPureNoIO(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	if cmd := m.Init(); cmd != nil {
		t.Error("Init must return nil (pure view, no background I/O)")
	}
	if len(m.Models()) != 3 {
		t.Errorf("construction must populate instantly, got %d", len(m.Models()))
	}
	if m.Loading() {
		t.Error("pure view must not start in loading state")
	}
	if cmd := New(nil).Init(); cmd == nil {
		t.Fatal("Init on empty snapshot must request background sync")
	} else if _, ok := cmd().(modelapp.SyncRequestedMsg); !ok {
		t.Errorf("empty Init cmd = %T, want SyncRequestedMsg", cmd())
	}
	view := New(seedSnapshot(testModels())).View()
	if strings.Contains(view, "Fetching models") {
		t.Error("view must never render a blocking Fetching modal")
	}
}

// Init on an empty snapshot must auto-trigger background sync.
func TestInitColdStartRequestsSync(t *testing.T) {
	m := New(seedSnapshot(nil))
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("cold-start Init must return a sync-request command")
	}
	if _, ok := cmd().(modelapp.SyncRequestedMsg); !ok {
		t.Fatalf("cold-start cmd = %T, want SyncRequestedMsg", cmd())
	}
	// Populated picker stays quiet.
	if cmd := New(seedSnapshot(testModels())).Init(); cmd != nil {
		t.Error("populated Init must return nil")
	}
}

// SnapshotMsg must swap the pointer and rebuild the derived view.
func TestSnapshotPointerReplacement(t *testing.T) {
	m := New(seedSnapshot(nil))
	if len(m.Models()) != 0 {
		t.Fatalf("empty snapshot, got %d", len(m.Models()))
	}
	m, _ = m.UpdateModel(SnapshotMsg{Snap: seedSnapshot(testModels())})
	if len(m.Models()) != 3 {
		t.Errorf("after SnapshotMsg, models = %d, want 3", len(m.Models()))
	}
}

// Typing in search focus must filter the snapshot RAM slice synchronously.
func TestSearchInputFiltersRAM(t *testing.T) {
	m := New(seedSnapshot(testModels()))

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("deepseek")})
	if len(m.Filtered()) != 1 {
		t.Fatalf("filtered %d, want 1 after typing deepseek", len(m.Filtered()))
	}
	if got := m.Highlighted().ID; got != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("highlight = %q, want deepseek-r1", got)
	}

	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.Filtered()) == 0 {
		t.Error("backspace must keep matches")
	}
}

// Pressing p in list focus must emit a BindModelToRoleCommand (no file I/O,
// no direct persistence, no badge mutation until confirmation).
func TestRoleBindingHotkeyEmitsCommand(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList()

	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if cmd == nil {
		t.Fatal("role hotkey must return a tea.Cmd emitting BindModelToRoleCommand")
	}
	msg := cmd()
	bind, ok := msg.(modelapp.BindModelToRoleCommand)
	if !ok {
		t.Fatalf("cmd msg = %T, want BindModelToRoleCommand", msg)
	}
	if string(bind.Role) != "plan" {
		t.Errorf("role = %q, want plan", string(bind.Role))
	}
	if bind.ModelID != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("model = %q, want highlighted deepseek-r1", bind.ModelID)
	}
	// Pure view: badges stay empty until the app layer confirms.
	if _, ok := m.Roles()["plan"]; ok {
		t.Error("picker must not mutate badges on keypress; wait for BindingSucceededMsg")
	}
	if !strings.Contains(m.Status(), "queued bind") {
		t.Errorf("status = %q, want queued bind", m.Status())
	}

	// Confirmation path updates badges.
	m, _ = m.UpdateModel(BindingSucceededMsg{Role: "plan", ModelID: bind.ModelID})
	if got := m.Roles()["plan"]; got != bind.ModelID {
		t.Errorf("after confirm, roles[plan] = %q", got)
	}
	if view := m.View(); !strings.Contains(view, "[PLAN]") {
		t.Errorf("view must render [PLAN] badge after confirmation:\n%s", view)
	}
}

// d/s/v/a hotkeys bind their roles via command emission.
func TestAllRoleHotkeysEmit(t *testing.T) {
	cases := map[string]string{"d": "default", "s": "smol", "v": "vision", "a": "adviser"}
	for key, wantRole := range cases {
		m := New(seedSnapshot(testModels()))
		m = m.FocusList().MoveCursor(2) // gpt-4o-mini
		var cmd tea.Cmd
		var updated Model
		updated, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		_ = updated
		if cmd == nil {
			t.Fatalf("key %q must emit a command", key)
		}
		bind := cmd().(modelapp.BindModelToRoleCommand)
		if string(bind.Role) != wantRole {
			t.Errorf("key %q role = %q, want %q", key, string(bind.Role), wantRole)
		}
		if bind.ModelID != "openai/gpt-4o-mini" {
			t.Errorf("key %q model = %q", key, bind.ModelID)
		}
	}
}

// Typing "p" in search focus must filter, never emit a bind command.
func TestSearchFocusDoesNotBind(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	if !m.SearchFocused() {
		t.Fatal("picker must start search-focused")
	}
	var cmd tea.Cmd
	m, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if cmd != nil {
		t.Error("typing p in search focus must not emit a bind command")
	}
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

// Reasoning fidelity: openai renders standard trio, gemini toggle trio,
// openrouter extended quintet; cycling stays within the permitted set.
func TestReasoningFidelity(t *testing.T) {
	openai := registry.ModelDescriptor{ID: "openai/o1", Provider: "openai", Name: "o1"}
	m := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	if opts := ReasoningOptionsFor(openai); len(opts) != 3 || opts[0] != "low" {
		t.Errorf("openai options = %v, want [low medium high]", opts)
	}
	if bar := m.RenderReasoningBar(); !strings.Contains(bar, "low") || !strings.Contains(bar, "high") {
		t.Errorf("standard bar = %q, want low/medium/high", bar)
	}
	if strings.Contains(m.RenderReasoningBar(), "xhigh") {
		t.Errorf("standard bar must not contain xhigh: %q", m.RenderReasoningBar())
	}

	extended := registry.ModelDescriptor{ID: "x", Provider: "openrouter", Name: "x"}
	mx := New(seedSnapshot([]registry.ModelDescriptor{extended}))
	if opts := ReasoningOptionsFor(extended); len(opts) != 5 {
		t.Errorf("extended options = %v, want 5", opts)
	}
	if bar := mx.RenderReasoningBar(); !strings.Contains(bar, "xhigh") || !strings.Contains(bar, "max") {
		t.Errorf("extended bar = %q, want xhigh/max", bar)
	}

	toggle := registry.ModelDescriptor{ID: "y", Provider: "gemini", Name: "y"}
	mt := New(seedSnapshot([]registry.ModelDescriptor{toggle}))
	if bar := mt.View(); !strings.Contains(bar, "off") || !strings.Contains(bar, "auto") {
		t.Errorf("toggle view = %q, want off/auto/on", bar)
	}

	fixed := registry.ModelDescriptor{ID: "deepseek-r1", Provider: "deepseek", Name: "R1"}
	mf := New(seedSnapshot([]registry.ModelDescriptor{fixed}))
	if bar := mf.RenderReasoningBar(); !strings.Contains(bar, "Fixed") || !strings.Contains(bar, "Locked") {
		t.Errorf("fixed bar = %q, want Fixed/Locked", bar)
	}
	if _, ok := mf.CurrentReasoningOption(); ok {
		t.Error("fixed mode must carry no caller-selected option")
	}

	unknown := registry.ModelDescriptor{ID: "z", Provider: "nope", Name: "z"}
	mu := New(seedSnapshot([]registry.ModelDescriptor{unknown}))
	if bar := mu.RenderReasoningBar(); !strings.Contains(bar, "N/A") {
		t.Errorf("none bar = %q, want N/A", bar)
	}

	// Cycling clamps within the standard trio (never reaches xhigh).
	mc := New(seedSnapshot([]registry.ModelDescriptor{openai}))
	for i := 0; i < 10; i++ {
		mc = mc.CycleReasoning(1)
	}
	if opt, _ := mc.CurrentReasoningOption(); opt != "high" {
		t.Errorf("clamped option = %q, want high", opt)
	}
}

// Scope toggle flips local/global and flows into the emitted command.
func TestScopeToggleFlowsIntoCommand(t *testing.T) {
	m := New(seedSnapshot(testModels()))
	m = m.FocusList()
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if !m.IsGlobal() {
		t.Fatal("g must toggle scope to global")
	}
	var cmd tea.Cmd
	var updated Model
	updated, cmd = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	_ = updated
	if cmd == nil {
		t.Fatal("bind must emit a command")
	}
	if bind := cmd().(modelapp.BindModelToRoleCommand); !bind.IsGlobal {
		t.Error("command must carry IsGlobal=true after toggle")
	}
}
