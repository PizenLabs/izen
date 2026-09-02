package modelpicker

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/pkg/provider/capability"
)

// testModels returns a mixed catalog: standard chat models (zero effort
// options) and reasoning models (auto/low/medium/high/xhigh).
func testModels() []capability.ModelCapabilities {
	return []capability.ModelCapabilities{
		{Provider: "openai", ModelID: "gpt-4o", Name: "GPT-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
		{Provider: "openai", ModelID: "o3-mini", Name: "O3 mini", SupportsReasoning: true, ContextWindow: 200000, MaxOutputTokens: 100000},
		{Provider: "deepseek", ModelID: "deepseek-r1", Name: "DeepSeek R1", SupportsReasoning: true, ContextWindow: 128000, MaxOutputTokens: 65536},
	}
}

func TestDynamicEffortPickerBinding(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels())

	// Cursor starts on gpt-4o: a standard chat model must expose ZERO effort
	// options.
	if got := m.EffortOptions(); got != nil {
		t.Fatalf("chat model effort options = %v, want nil (0 options)", got)
	}
	if m.HasEffortOptions() {
		t.Fatal("chat model must not show an effort selector")
	}

	// Move to o3-mini: a reasoning model must expose the extended effort set.
	m = m.MoveCursor(1)
	o3 := m.Highlighted()
	if o3 == nil || o3.ModelID != "o3-mini" {
		t.Fatalf("highlighted = %v, want o3-mini", o3)
	}
	want := []capability.EffortLevel{capability.EffortAuto, capability.EffortLow, capability.EffortMedium, capability.EffortHigh, capability.EffortXHigh}
	if got := m.EffortOptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning model effort options = %v, want %v", got, want)
	}
	if !m.HasEffortOptions() {
		t.Fatal("reasoning model must show an effort selector")
	}

	// deepseek-r1 also exposes the extended set.
	m = m.MoveCursor(1)
	if got := m.EffortOptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deepseek-r1 effort options = %v, want %v", got, want)
	}

	// Move back to a chat model: the effort selector must collapse again.
	m = m.MoveCursor(-1).MoveCursor(-1)
	if got := m.EffortOptions(); got != nil {
		t.Fatalf("chat model effort options after returning = %v, want nil", got)
	}
	if m.HasEffortOptions() {
		t.Fatal("chat model must not show an effort selector after navigation")
	}
}

func TestModelPickerNavigation(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels())
	if m.Highlighted().ModelID != "gpt-4o" {
		t.Fatalf("initial highlight = %q, want gpt-4o", m.Highlighted().ModelID)
	}

	t.Run("clamps at top", func(t *testing.T) {
		m = m.MoveCursor(-5)
		if m.Highlighted().ModelID != "gpt-4o" {
			t.Fatalf("clamped highlight = %q, want gpt-4o", m.Highlighted().ModelID)
		}
	})

	t.Run("clamps at bottom", func(t *testing.T) {
		m = m.MoveCursor(100)
		if m.Highlighted().ModelID != "deepseek-r1" {
			t.Fatalf("clamped highlight = %q, want deepseek-r1", m.Highlighted().ModelID)
		}
	})

	t.Run("set cursor clamps", func(t *testing.T) {
		m = m.SetCursor(-3)
		if m.Highlighted().ModelID != "gpt-4o" {
			t.Fatalf("set cursor clamp low = %q", m.Highlighted().ModelID)
		}
		m = m.SetCursor(99)
		if m.Highlighted().ModelID != "deepseek-r1" {
			t.Fatalf("set cursor clamp high = %q", m.Highlighted().ModelID)
		}
	})
}

func TestModelPickerEmptyState(t *testing.T) {
	t.Parallel()
	m := New()
	if m.Highlighted() != nil {
		t.Error("empty picker must have nil highlight")
	}
	if m.EffortOptions() != nil {
		t.Error("empty picker must have nil effort options")
	}
	if m.ThinkingBudget() != 0 || m.TotalMaxTokens() != 0 {
		t.Error("empty picker must report zero budgets")
	}
	if _, _, ok := m.Select(); ok {
		t.Error("empty picker must not resolve")
	}
	if m.Done() {
		t.Error("empty picker must not be done")
	}
	m = m.MoveCursor(1).SetCursor(5).SetEffort(capability.EffortHigh).SetEffortIndex(9)
	if m.Highlighted() != nil {
		t.Error("navigation on empty picker must be a no-op")
	}
	// Tea message on empty picker must not panic.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Error("empty picker update should produce no command")
	}
}

func TestModelPickerEffortSelection(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels()).MoveCursor(1) // o3-mini

	t.Run("default effort is first option", func(t *testing.T) {
		if m.CurrentEffort() != capability.EffortAuto {
			t.Fatalf("default effort = %q, want auto", m.CurrentEffort())
		}
	})

	t.Run("move effort wraps into range", func(t *testing.T) {
		m = m.SetEffort(capability.EffortHigh)
		if m.EffortIndex() != 3 {
			t.Fatalf("effort index = %d, want 3", m.EffortIndex())
		}
		m = m.MoveEffort(1)
		if m.CurrentEffort() != capability.EffortXHigh {
			t.Fatalf("effort = %q, want xhigh", m.CurrentEffort())
		}
		// Past the end clamps to xhigh (index 4).
		m = m.MoveEffort(10)
		if m.CurrentEffort() != capability.EffortXHigh {
			t.Fatalf("effort = %q, want clamped xhigh", m.CurrentEffort())
		}
		// Back below the start clamps to auto.
		m = m.MoveEffort(-20)
		if m.CurrentEffort() != capability.EffortAuto {
			t.Fatalf("effort = %q, want clamped auto", m.CurrentEffort())
		}
	})

	t.Run("set effort snaps unknown to current", func(t *testing.T) {
		m = m.SetEffort(capability.EffortXHigh).SetEffort(capability.EffortLevel("turbo"))
		if m.CurrentEffort() != capability.EffortXHigh {
			t.Fatalf("unknown effort must not change selection, got %q", m.CurrentEffort())
		}
	})

	t.Run("effort index clamps to option count", func(t *testing.T) {
		m = m.SetEffortIndex(99)
		if m.EffortIndex() != 4 {
			t.Fatalf("effort index = %d, want 4", m.EffortIndex())
		}
		m = m.SetEffortIndex(-2)
		if m.EffortIndex() != 0 {
			t.Fatalf("effort index = %d, want 0", m.EffortIndex())
		}
	})

	t.Run("chat model ignores effort mutation", func(t *testing.T) {
		chat := New().SetModels(testModels()) // gpt-4o, no efforts
		chat = chat.SetEffort(capability.EffortHigh).SetEffortIndex(7).MoveEffort(3)
		if chat.EffortIndex() != 0 {
			t.Fatalf("chat effort index = %d, want 0", chat.EffortIndex())
		}
	})
}

func TestModelPickerBudgetDisplay(t *testing.T) {
	t.Parallel()

	t.Run("reasoning model budgets track effort", func(t *testing.T) {
		m := New().SetModels(testModels()).MoveCursor(1) // o3-mini, maxOut 100000
		if m.CurrentEffort() != capability.EffortAuto {
			t.Fatalf("effort = %q, want auto", m.CurrentEffort())
		}
		if m.ThinkingBudget() != 0 {
			t.Errorf("auto thinking budget = %d, want 0", m.ThinkingBudget())
		}
		if m.TotalMaxTokens() != 100000 {
			t.Errorf("auto total max = %d, want 100000", m.TotalMaxTokens())
		}
		m = m.SetEffort(capability.EffortMedium)
		if want := 16000; m.ThinkingBudget() != want { // capped by medium ceiling
			t.Errorf("medium thinking budget = %d, want %d", m.ThinkingBudget(), want)
		}
	})

	t.Run("chat model reports advertised max only", func(t *testing.T) {
		m := New().SetModels(testModels()) // gpt-4o, maxOut 16384
		if m.ThinkingBudget() != 0 {
			t.Errorf("chat thinking budget = %d, want 0", m.ThinkingBudget())
		}
		if m.TotalMaxTokens() != 16384 {
			t.Errorf("chat total max = %d, want 16384", m.TotalMaxTokens())
		}
	})
}

func TestModelPickerSelect(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels()).MoveCursor(2) // deepseek-r1
	m = m.SetEffort(capability.EffortHigh)
	m, sel, ok := m.Select()
	if !ok {
		t.Fatal("Select must resolve")
	}
	if sel.Model.ModelID != "deepseek-r1" {
		t.Errorf("selected model = %q, want deepseek-r1", sel.Model.ModelID)
	}
	if sel.Effort != capability.EffortHigh {
		t.Errorf("selected effort = %q, want high", sel.Effort)
	}
	if !m.Done() {
		t.Error("picker must be done after Select")
	}
	if res := m.Result(); res == nil || res.Model.ModelID != "deepseek-r1" {
		t.Errorf("Result = %+v, want deepseek-r1 selection", res)
	}

	// A done picker ignores further updates.
	afterTea, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Error("done picker must produce no command")
	}
	after := afterTea.(Model)
	if after.Highlighted().ModelID != "deepseek-r1" {
		t.Error("done picker must freeze its highlight")
	}
}

func TestModelPickerFilter(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels())
	m = m.SetFilter("o3")
	if len(m.Models()) != 3 {
		t.Fatalf("Models() must keep the full list, got %d", len(m.Models()))
	}
	if m.Highlighted().ModelID != "o3-mini" {
		t.Fatalf("filtered highlight = %q, want o3-mini", m.Highlighted().ModelID)
	}

	t.Run("multi token AND", func(t *testing.T) {
		m = m.SetFilter("deepseek r1")
		if m.Highlighted().ModelID != "deepseek-r1" {
			t.Fatalf("multi-token filter = %q", m.Highlighted().ModelID)
		}
	})

	t.Run("no match leaves empty filtered list", func(t *testing.T) {
		m = m.SetFilter("zzz")
		if m.Highlighted() != nil {
			t.Fatal("no-match filter must yield nil highlight")
		}
	})

	t.Run("empty query restores full list", func(t *testing.T) {
		m = m.SetFilter("")
		if m.Highlighted().ModelID != "gpt-4o" {
			t.Fatalf("empty filter highlight = %q, want gpt-4o", m.Highlighted().ModelID)
		}
	})
}

func TestTokenize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"   ", nil},
		{"deepseek r1", []string{"deepseek", "r1"}},
		{"DeepSeek DEEPSEEK r1", []string{"deepseek", "r1"}},
	}
	for _, tt := range tests {
		if got := tokenize(tt.query); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestMatchAll(t *testing.T) {
	t.Parallel()
	m := capability.ModelCapabilities{Provider: "openai", ModelID: "gpt-4o", Name: "GPT-4o"}
	if !matchAll(m, []string{"openai", "gpt-4o"}) {
		t.Error("must match provider + id tokens")
	}
	if matchAll(m, []string{"gpt-4o", "claude"}) {
		t.Error("must reject unmatched tokens")
	}
}

func TestTeaUpdates(t *testing.T) {
	t.Parallel()

	m := New().SetModels(testModels())

	t.Run("window size message", func(t *testing.T) {
		got, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		if cmd != nil {
			t.Error("window size must not produce a command")
		}
		m2 := got.(Model)
		if m2.Width != 100 || m2.Height != 30 {
			t.Errorf("size = %dx%d", m2.Width, m2.Height)
		}
	})

	t.Run("keyboard navigation and effort", func(t *testing.T) {
		m = m.MoveCursor(1)
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = got.(Model)
		if m.CurrentEffort() != capability.EffortLow {
			t.Fatalf("right arrow effort = %q, want low", m.CurrentEffort())
		}
		got, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = got.(Model)
		if m.Highlighted().ModelID != "gpt-4o" {
			t.Fatalf("up arrow highlight = %q, want gpt-4o", m.Highlighted().ModelID)
		}
		got, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m = got.(Model)
		if m.Highlighted().ModelID != "gpt-4o" {
			t.Fatalf("left arrow must be a no-op on chat model: %q", m.Highlighted().ModelID)
		}
	})

	t.Run("enter selects", func(t *testing.T) {
		m = m.MoveCursor(1)
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = got.(Model)
		if !m.Done() {
			t.Fatal("enter must resolve the picker")
		}
		if m.Result() == nil {
			t.Fatal("enter must record a selection")
		}
	})

	t.Run("runes type into filter", func(t *testing.T) {
		m = New().SetModels(testModels())
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r1")})
		m = got.(Model)
		if m.Highlighted().ModelID != "deepseek-r1" {
			t.Fatalf("typed filter highlight = %q, want deepseek-r1", m.Highlighted().ModelID)
		}
	})

	t.Run("backspace narrows filter", func(t *testing.T) {
		m = New().SetModels(testModels()).SetFilter("r1")
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = got.(Model)
		if m.filter != "r" {
			t.Fatalf("filter after backspace = %q, want r", m.filter)
		}
	})
}

func TestModelPickerView(t *testing.T) {
	t.Parallel()

	t.Run("empty models", func(t *testing.T) {
		view := New().View()
		if !strings.Contains(view, "no models loaded") {
			t.Errorf("empty view = %q", view)
		}
	})

	t.Run("chat model hides effort selector", func(t *testing.T) {
		view := New().SetModels(testModels()).View()
		if strings.Contains(view, "Effort:") {
			t.Errorf("chat model view must hide effort selector:\n%s", view)
		}
		if !strings.Contains(view, "gpt-4o") {
			t.Errorf("view must list gpt-4o:\n%s", view)
		}
	})

	t.Run("reasoning model shows effort + budgets", func(t *testing.T) {
		m := New().SetModels(testModels()).MoveCursor(1).SetEffort(capability.EffortMedium)
		view := m.View()
		for _, want := range []string{"Effort:", "auto", "low", "medium", "high", "xhigh", "Thinking Budget:", "Total Max:", "o3-mini"} {
			if !strings.Contains(view, want) {
				t.Errorf("reasoning view missing %q:\n%s", want, view)
			}
		}
	})

	t.Run("no match filter", func(t *testing.T) {
		view := New().SetModels(testModels()).SetFilter("zzz").View()
		if !strings.Contains(view, "no models match the filter") {
			t.Errorf("no-match view = %q", view)
		}
	})
}

func TestContextBadge(t *testing.T) {
	t.Parallel()
	if got := ctxBadge(capability.ModelCapabilities{ContextWindow: 128000}); got != "128k" {
		t.Errorf("ctx badge = %q, want 128k", got)
	}
	if got := ctxBadge(capability.ModelCapabilities{MaxOutputTokens: 65536}); got != "out 65k" {
		t.Errorf("fallback badge = %q, want out 65k", got)
	}
	if got := ctxBadge(capability.ModelCapabilities{}); got != "—" {
		t.Errorf("empty badge = %q, want em dash", got)
	}
}

func TestInitNilCommand(t *testing.T) {
	t.Parallel()
	if cmd := New().Init(); cmd != nil {
		t.Error("Init must return nil command")
	}
}
