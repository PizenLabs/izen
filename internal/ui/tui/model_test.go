package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	autonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// TestTUI_RendersProposalModalOnDecisionSurfaceEvent pins the deadlock fix:
// a HumanBoundaryProposal event carrying a DecisionSurface payload must route
// the active TUI view to the interactive ProposalModel modal — it must NEVER
// degrade to the static pause card. The output frame must render the
// interactive proposal menu (every option, the strategy title, and the
// keybinding hint).
func TestTUI_RendersProposalModalOnDecisionSurfaceEvent(t *testing.T) {
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetWithinLimits,
	}, "$prompt"))

	model := NewModel(nil)
	if model.ProposalActive() {
		t.Fatal("a fresh dispatcher must not render the proposal modal")
	}

	// Feed the HumanBoundaryProposal event with the DecisionSurface payload.
	updated, cmd := model.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})
	if updated == nil {
		t.Fatal("Update must return the dispatcher")
	}
	if cmd != nil {
		t.Fatal("the proposal modal is a pure value object — activating it schedules no background command")
	}
	if updated.activeModal != ModalProposal {
		t.Fatalf("activeModal = %v, want ModalProposal (static pause fallback is forbidden)", updated.activeModal)
	}
	if updated.proposalModel == nil {
		t.Fatal("proposalModel must be instantiated on a DecisionSurface event")
	}
	if !updated.ProposalActive() {
		t.Fatal("ProposalActive() must report the interactive modal once a DecisionSurface event arrives")
	}

	// The output frame must be the interactive proposal menu — not a static
	// "Ctrl+C to dismiss" pause card.
	out := updated.View()
	if out == "" {
		t.Fatal("View must render a non-empty frame while the proposal modal is active")
	}
	for _, want := range []string{"PROPOSAL STRATEGY", "Enter select", "1-9 quick select", "Esc cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output frame must include interactive hint %q", want)
		}
	}
	for _, opt := range surface.Options {
		if !strings.Contains(out, opt.Label) {
			t.Fatalf("output frame must include proposal option %q", opt.Label)
		}
	}
	if !strings.Contains(out, surface.Target) {
		t.Fatalf("output frame must include target %q", surface.Target)
	}
	// The interactive menu must never carry the static pause copy.
	if strings.Contains(out, "Ctrl+C to dismiss") {
		t.Fatal("output frame must not degrade to the static pause state")
	}
}

// TestTUI_ProposalModalHijacksViewportKeys pins the keyboard focus-hijack
// invariant: while the proposal modal is active, keypresses such as 'j' / 'k'
// (a viewport scroll binding) MUST be consumed by the proposal modal — they
// are never forwarded to any other handler (e.g. the workspace viewport). The
// modal stays active and the key produces no routing command, proving the
// dispatcher never leaks focus to the underlying view. A leaked key could
// scroll the viewport and desync the modal from the decision the user is
// answering; hijacking guarantees every keypress resolves the awaiting_human
// barrier (or navigates the modal) instead.
func TestTUI_ProposalModalHijacksViewportKeys(t *testing.T) {
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetWithinLimits,
	}, "$prompt"))

	var routed []ProposalIntent
	resume := func(_ context.Context, intent ProposalIntent) error {
		routed = append(routed, intent)
		return nil
	}
	model := NewModel(resume)
	model.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})
	if !model.ProposalActive() {
		t.Fatal("precondition: proposal modal must be active")
	}

	// Feed the viewport-scroll keys ('j' / 'k') while the modal owns the
	// keyboard. They must NOT be forwarded to a viewport scroll handler — the
	// dispatcher routes them only to the proposal modal.
	for _, key := range []rune{'j', 'k'} {
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		if updated == nil {
			t.Fatalf("Update must return the dispatcher for key %q", key)
		}
		if !updated.ProposalActive() {
			t.Fatalf("key %q must be consumed by the proposal modal (focus must not leak)", key)
		}
		if cmd != nil {
			t.Fatalf("key %q is a viewport-scroll binding and must NOT route a proposal intent (got a command)", key)
		}
	}

	// The modal never routed anything and no viewport scroll happened: the
	// underlying handler was never reached.
	if len(routed) != 0 {
		t.Fatalf("viewport-scroll keys routed %v, want none (focus leaked)", routed)
	}

	// The modal is still interactive and resolvable: Enter still selects, which
	// proves keyboard focus remained on the proposal throughout.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter must still route a selection after j/k hijack")
	}
	_ = updated
	if resumed := cmd().(ProposalResumedMsg); resumed.Intent != surface.Options[0].Intent {
		t.Fatalf("Enter routed intent=%q, want %q", resumed.Intent, surface.Options[0].Intent)
	}
}

// TestTUI_EmptyDecisionSurfaceKeepsModalInactive asserts a HumanBoundary
// proposal event WITHOUT a surface payload never manufactures a fake modal —
// the dispatcher stays on the workspace view (no deadlock, no fake decision).
func TestTUI_EmptyDecisionSurfaceKeepsModalInactive(t *testing.T) {
	model := NewModel(nil)
	updated, _ := model.Update(HumanBoundaryProposalMsg{DecisionSurface: nil})
	if updated.activeModal != ModalNone {
		t.Fatalf("activeModal = %v, want ModalNone for a nil DecisionSurface", updated.activeModal)
	}
	if updated.ProposalActive() {
		t.Fatal("a nil DecisionSurface must not activate the proposal modal")
	}
	if updated.View() != "" {
		t.Fatal("View must render nothing when no modal is active")
	}
}

// TestTUI_KeysRouteProposalIntentToEngine pins the keybinding contract: while
// the proposal modal is active, digit keys (1-5), Enter and Esc are consumed by
// the modal and the selected ProposalIntent is routed to the engine resumer
// (Driver.ResumeWithProposal) — never executed locally.
func TestTUI_KeysRouteProposalIntentToEngine(t *testing.T) {
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetWithinLimits,
	}, "$prompt"))

	var routed []ProposalIntent
	resume := func(_ context.Context, intent ProposalIntent) error {
		routed = append(routed, intent)
		return nil
	}
	model := NewModel(resume)
	model.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})

	// Digit key: option 1 routes the first option's intent to the engine.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if updated == nil {
		t.Fatal("Update must return the dispatcher")
	}
	if updated.ProposalActive() {
		t.Fatal("a selection must dismiss the proposal modal")
	}
	if cmd == nil {
		t.Fatal("a selection must route to the engine resumer")
	}
	msg := cmd()
	resumed, ok := msg.(ProposalResumedMsg)
	if !ok {
		t.Fatalf("routing command returned %T, want ProposalResumedMsg", msg)
	}
	if resumed.Err != nil {
		t.Fatalf("routing failed: %v", resumed.Err)
	}
	if len(routed) != 1 || routed[0] != surface.Options[0].Intent {
		t.Fatalf("resumer received %v, want %q", routed, surface.Options[0].Intent)
	}

	// Esc cancels: the modal routes ProposalCancel (never a local hard-cancel).
	model.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc must route the cancel intent")
	}
	if resumed := cmd().(ProposalResumedMsg); resumed.Err != nil || resumed.Intent != ProposalCancel {
		t.Fatalf("Esc routed intent=%q err=%v, want cancel", resumed.Intent, resumed.Err)
	}

	// Navigation keys (↑/↓) keep the modal active without routing.
	model.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !updated.ProposalActive() {
		t.Fatal("↑ navigation must keep the proposal modal active")
	}
	if cmd != nil {
		t.Fatal("navigation must not route a proposal intent")
	}
	if len(routed) != 2 {
		t.Fatalf("routed intents = %v, want exactly 2 (digit + esc)", routed)
	}
}
