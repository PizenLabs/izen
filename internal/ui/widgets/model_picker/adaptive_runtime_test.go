package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

func agenticModel() registry.ModelDescriptor {
	return registry.ModelDescriptor{
		ID:            "thinkingmachines/inkling:free",
		Provider:      "openrouter",
		Name:          "Inkling (free)",
		ContextWindow: 256000,
		Capabilities:  []registry.ModelCapability{registry.CapTools},
	}
}

func standardModel() registry.ModelDescriptor {
	return registry.ModelDescriptor{
		ID:            "openai/gpt-4o",
		Provider:      "openai",
		Name:          "GPT-4o",
		ContextWindow: 128000,
	}
}

// TestAgenticBadgeRendered: an agentic-harness model renders the [Agentic]
// badge in the capability column and its Auto-Promote runtime path in the
// bottom preview, without being hidden or disabled.
func TestAgenticBadgeRendered(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{agenticModel(), standardModel()})).
		SetPaneFocus(PaneModels).SetSize(120, 30)
	view := m.View()

	if !strings.Contains(view, "[Agentic]") {
		t.Fatalf("agentic model must render the [Agentic] badge, got:\n%s", view)
	}
	if !strings.Contains(view, "Runtime Path:") {
		t.Fatalf("preview must render a Runtime Path line, got:\n%s", view)
	}
	if !strings.Contains(view, "Auto-Promote (Binds ReadOnly Tools for /ask)") {
		t.Fatalf("agentic model must show the auto-promote runtime path, got:\n%s", view)
	}
	// Both discovered models are present — nothing hidden.
	if !strings.Contains(view, "thinkingmachines/inkling:free") || !strings.Contains(view, "openai/gpt-4o") {
		t.Fatalf("all discovered models must be listed, got:\n%s", view)
	}
}

// TestStandardRuntimePath: a normal model shows the standard adaptive path and
// no [Agentic] badge.
func TestStandardRuntimePath(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{agenticModel(), standardModel()})).
		SetPaneFocus(PaneModels).SetSize(120, 30).
		MoveCursor(1) // -> openai/gpt-4o
	view := m.View()

	if !strings.Contains(view, "DirectCompletion / AgenticLoop (Standard)") {
		t.Fatalf("standard model must show the standard runtime path, got:\n%s", view)
	}
	// The table still renders the agentic badge for the other row.
	if !strings.Contains(view, "[Agentic]") {
		t.Fatalf("agentic row badge must remain visible while standard row is highlighted, got:\n%s", view)
	}
}

// TestEnterActivatesAgenticModel: the 2-step activation succeeds for an
// agentic-harness model with no local rejection.
func TestEnterActivatesAgenticModel(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{agenticModel()})).SetPaneFocus(PaneModels)

	opened, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("list Enter must open details, got %T", cmd())
	}
	if opened.State() != StateDetail {
		t.Fatalf("list Enter must move to StateDetail, got %v", opened.State())
	}
	_, confirm := opened.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if confirm == nil {
		t.Fatal("detail Enter must emit an assignment command for an agentic model")
	}
	assign := unwrapAssignmentMsg(t, confirm())
	if assign.ModelID != "thinkingmachines/inkling:free" || assign.Provider != "openrouter" {
		t.Fatalf("assignment = %+v, want the agentic model", assign)
	}
}

// TestAltIInspectorShowsContractMapping: Alt+i opens the inspector with the raw
// provider caps and the IZEN dynamic contract mapping.
func TestAltIInspectorShowsContractMapping(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{agenticModel()}))
	opened, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i"), Alt: true})
	// The card may soft-wrap long values at narrow widths and the border glyphs
	// sit between wrapped fragments; strip the frame, then collapse whitespace
	// so the mapping is asserted by content, not by physical line breaks.
	framed := strings.NewReplacer(
		"│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ",
	).Replace(opened.View())
	view := strings.Join(strings.Fields(framed), " ")

	for _, want := range []string{
		"MODEL DETAILS",
		"IZEN CONTRACT MAPPING",
		"Raw Provider Caps",
		"Wire Policy",
		"Agentic harness required",
		"Auto-Promote (Binds ReadOnly Tools for /ask)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("inspector must contain %q, got:\n%s", want, view)
		}
	}
}

// TestAdaptiveTableAlignment pins the 4-column alignment contract: every model
// row occupies the same visible width across terminal viewport sizes, even
// with the [Agentic] badge present.
func TestAdaptiveTableAlignment(t *testing.T) {
	models := []registry.ModelDescriptor{agenticModel(), standardModel(), {
		ID:            "google/gemini-2.5-flash",
		Provider:      "gemini",
		Name:          "Gemini Flash",
		ContextWindow: 1000000,
		Capabilities:  []registry.ModelCapability{registry.CapVision, registry.CapThinking},
	}}
	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {140, 40}} {
		m := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(size.w, size.h)
		view := m.View()
		if !strings.Contains(view, "Runtime Path:") {
			t.Fatalf("size %dx%d: runtime path preview line dropped, got:\n%s", size.w, size.h, view)
		}
		lines := strings.Split(view, "\n")
		widths := map[int]bool{}
		rows := 0
		for _, ln := range lines {
			// Only the dual-pane table rows carry the vertical separator. Every
			// such line is left-pane(24) + separator(1) + right-pane and must
			// share one visible width for the columns to stay aligned.
			if !strings.Contains(ln, "│") {
				continue
			}
			widths[lipgloss.Width(ln)] = true
			rows++
		}
		if rows == 0 {
			t.Fatalf("size %dx%d: no dual-pane rows found", size.w, size.h)
		}
		if len(widths) != 1 {
			t.Fatalf("size %dx%d: table rows misaligned, widths=%v", size.w, size.h, widths)
		}
	}
}
