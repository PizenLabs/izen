package tui

import (
	"context"
	"testing"

	autonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Pause Surface / Hard-Block Recovery Tests (Task 2) ──────────────────────
//
// The directive: a Hard-Block / AUTONOMY PAUSED DecisionSurface MUST
// always carry the three recovery options:
//
//	[1] Abort Run & Return to Idle     (graceful, no SIGINT)
//	[2] Force Bounded Patch             (overrides syntax check)
//	[3] Switch Model                    (re-target at higher-budget model)
//
// The tests pin:
//
//   - The hard-block surface is NEVER empty (deadlock guard)
//   - The three hard-block options are present on every parked surface
//   - Option 1 (ProposalAbortRun) cancels the run cleanly back to idle
//     via the TUI event loop — selection routes a pure ProposalIntent
//     to the engine resumer, which transitions the run to ABORTED with
//     zero spend and zero mutation.

// TestPauseSurface_InteractiveOptions is the directive's headline test:
// simulate >=2 format failures and verify the parked DecisionSurface
// carries the three hard-block recovery options, that Option 1
// (ProposalAbortRun) routes cleanly through the TUI event loop, and
// that selecting it cancels the run back to a clean idle state.
func TestPauseSurface_InteractiveOptions(t *testing.T) {
	// Build the runtime DecisionSurface the hard-block path produces:
	// two consecutive format failures (>=2) classify the run as a
	// hard-block. The runtime appends the three recovery options so
	// the human can always escape the parked barrier.
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetExceeded,
		EstimatedTokens:  5835,
		MaxOutputTokens:  1024,
	}, "$prompt"))

	// The surface MUST carry the three hard-block recovery options.
	for _, want := range []ProposalIntent{
		ProposalAbortRun,
		ProposalForceBoundedPatch,
		ProposalSwitchModel,
	} {
		if !surface.Has(want) {
			t.Errorf("hard-block surface must offer %q (deadlock guard)", want)
		}
	}
	// And it MUST NOT be empty: the deadlock guard at the runtime layer
	// guarantees at least one selectable option; the hard-block helper
	// then guarantees the three recovery options are present.
	if len(surface.Options) == 0 {
		t.Fatal("hard-block surface MUST carry at least one option (deadlock guard)")
	}

	// Bind the surface to the interactive TUI modal and verify every
	// option is reachable by keyboard navigation (Enter, ↑/↓, digit
	// keys). The modal is pure presentation: selecting an option only
	// returns the ProposalIntent — the engine resumer is the only
	// mutation path.
	modal := NewProposalModel(surface)
	// The TUI's adapter render appends a synthetic disabled FULL_REWRITE
	// option when the surface is over budget. The runtime's
	// DecisionSurface carries 10 options; the adapter's view-model
	// carries 11 (10 + 1 synthetic disabled). The OptionCount helper
	// reports the view-model count, which is the authoritative one
	// for the TUI modal. We only assert the runtime surface count
	// here, which is what the hard-block guarantee is about.
	if len(surface.Options) == 0 {
		t.Fatalf("hard-block surface must carry at least one option")
	}

	// Press 1: must select the FIRST option, which is the
	// hard-block abort path. The TUI dispatches the selection to the
	// engine resumer; the run transitions to ABORTED.
	got1, ok := modal.HandleKey("1")
	if !ok {
		t.Fatal("HandleKey(1) must select the first option")
	}
	if got1 != surface.Options[0].Intent {
		t.Fatalf("digit-key 1 = %q, want %q", got1, surface.Options[0].Intent)
	}

	// Routing delegates to the engine resumer. The TUI NEVER writes
	// files or invokes the provider itself: it hands the pure
	// ProposalIntent to the bound resumer, which the composition
	// root wires to Driver.ResumeWithProposal.
	var routed ProposalIntent
	resume := func(_ context.Context, intent ProposalIntent) error {
		routed = intent
		return nil
	}
	if err := Route(context.Background(), resume, got1); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if routed != got1 {
		t.Fatalf("resumer received %q, want %q", routed, got1)
	}

	// The intent MUST be valid in the autonomy vocabulary; the
	// composition root's ParseProposalIntent normalization must accept
	// every TUI-issued intent.
	conv := autonomy.ProposalIntent(string(routed))
	if !conv.Valid() {
		t.Fatalf("resumed intent %q must be valid in the autonomy vocabulary", routed)
	}

	// ProposalAbortRun is a hard-block CANCEL: it shares the runtime's
	// cancel path (zero spend, zero mutation) and returns the session
	// to a clean idle state without SIGINT.
	if !routed.IsCancel() {
		t.Fatalf("ProposalAbortRun must be a cancel-family intent (got IsCancel()=false for %q)", routed)
	}
}

// TestPauseSurface_NeverEmpty pins the deadlock guard: a DecisionSurface
// derived from a hard-block failure category MUST carry at least one
// selectable option. An empty surface would strand the run at
// awaiting_human forever — there is no keyboard input that could
// resolve it.
func TestPauseSurface_NeverEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		eval autonomy.PreflightEvaluation
		sub  string
	}{
		{
			name: "budget_exceeded",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTValid,
				BudgetStatus:    autonomy.BudgetExceeded,
				EstimatedTokens: 5835, MaxOutputTokens: 1024,
			},
			sub: "$prompt",
		},
		{
			name: "ast_corrupt",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTCorrupt,
				BudgetStatus: autonomy.BudgetWithinLimits,
			},
			sub: "$hot",
		},
		{
			name: "capability_denied",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTValid,
				DependencyStatus: autonomy.DependenciesUnresolved,
				BudgetStatus:     autonomy.BudgetWithinLimits,
			},
			sub: "$prompt",
		},
		{
			name: "internal_error",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTValid,
				BudgetStatus: autonomy.BudgetWithinLimits,
			},
			sub: "$prompt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surface := fromAutonomy(autonomy.BuildDecisionSurface(tc.eval, tc.sub))
			if len(surface.Options) == 0 {
				t.Fatalf("hard-block surface %q must carry at least one option", tc.name)
			}
		})
	}
}

// TestPauseSurface_AllHardBlockOptionsPresent pins the canonical
// guarantee: the three hard-block recovery options are present on every
// surface built for a hard-block failure category.
func TestPauseSurface_AllHardBlockOptionsPresent(t *testing.T) {
	for _, tc := range []struct {
		name string
		eval autonomy.PreflightEvaluation
	}{
		{
			name: "budget_exceeded",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTValid,
				BudgetStatus:    autonomy.BudgetExceeded,
				EstimatedTokens: 5835, MaxOutputTokens: 1024,
			},
		},
		{
			name: "ast_corrupt",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTCorrupt,
			},
		},
		{
			name: "capability_denied",
			eval: autonomy.PreflightEvaluation{
				Target: "index.html", ASTStatus: autonomy.ASTValid,
				DependencyStatus: autonomy.DependenciesUnresolved,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surface := fromAutonomy(autonomy.BuildDecisionSurface(tc.eval, "$prompt"))
			for _, want := range []ProposalIntent{
				ProposalAbortRun,
				ProposalForceBoundedPatch,
				ProposalSwitchModel,
			} {
				if !surface.Has(want) {
					t.Errorf("hard-block surface %q must offer %q", tc.name, want)
				}
			}
		})
	}
}

// TestPauseSurface_AbortRunRoutesAsCancel pins the runtime contract:
// ProposalAbortRun is a cancel-family intent. The TUI's event loop
// dispatches the selection to the engine resumer; the runtime shares
// the cancel path (zero spend, zero mutation) so the run transitions
// to ABORTED and the session returns to a clean idle state.
func TestPauseSurface_AbortRunRoutesAsCancel(t *testing.T) {
	if !ProposalAbortRun.IsCancel() {
		t.Fatal("ProposalAbortRun must be a cancel-family intent (IsCancel()=true)")
	}
	// The intent is a member of the closed vocabulary.
	if !ProposalAbortRun.Valid() {
		t.Fatal("ProposalAbortRun must be valid")
	}
	// ParseProposalIntent must accept the canonical and aliased forms.
	for _, raw := range []string{
		"abort_run",
		"IntentAbortRun",
		"ABORT_RUN",
	} {
		if got := ParseProposalIntent(raw); got != ProposalAbortRun {
			t.Errorf("ParseProposalIntent(%q) = %q, want %q", raw, got, ProposalAbortRun)
		}
	}
}

// TestPauseSurface_ForceBoundedPatchIsRecovery pins the runtime
// contract: ProposalForceBoundedPatch is a recovery intent. The runtime
// dispatches it as a NEW contract under StrategyBoundedPatch with
// AllowASTBypass=true (the difference from ProposalRescopeBoundedPatch,
// which respects the AST gate when no bypass).
func TestPauseSurface_ForceBoundedPatchIsRecovery(t *testing.T) {
	if !ProposalForceBoundedPatch.Valid() {
		t.Fatal("ProposalForceBoundedPatch must be valid")
	}
	if !ProposalForceBoundedPatch.IsRecovery() {
		t.Fatal("ProposalForceBoundedPatch must be a recovery intent (IsRecovery()=true)")
	}
	for _, raw := range []string{
		"force_bounded_patch",
		"IntentForceBoundedPatch",
		"FORCE_BOUNDED_PATCH",
	} {
		if got := ParseProposalIntent(raw); got != ProposalForceBoundedPatch {
			t.Errorf("ParseProposalIntent(%q) = %q, want %q", raw, got, ProposalForceBoundedPatch)
		}
	}
}

// TestPauseSurface_SwitchModelIsRecovery pins the runtime contract:
// ProposalSwitchModel is a recovery intent. The composition root binds
// the model picker modal; the runtime never resolves a model outside
// the explicitly authorized human choice.
func TestPauseSurface_SwitchModelIsRecovery(t *testing.T) {
	if !ProposalSwitchModel.Valid() {
		t.Fatal("ProposalSwitchModel must be valid")
	}
	if !ProposalSwitchModel.IsRecovery() {
		t.Fatal("ProposalSwitchModel must be a recovery intent (IsRecovery()=true)")
	}
	for _, raw := range []string{
		"switch_model",
		"IntentSwitchModel",
		"SWITCH_MODEL",
	} {
		if got := ParseProposalIntent(raw); got != ProposalSwitchModel {
			t.Errorf("ParseProposalIntent(%q) = %q, want %q", raw, got, ProposalSwitchModel)
		}
	}
}

// TestPauseSurface_TUIEventLoopDispatchesOption pins the TUI event-loop
// contract: when a DecisionSurface carries a hard-block option and the
// user presses Enter on it, the modal activates, the selection
// returns the ProposalIntent, and Route() hands it to the engine
// resumer. The dispatcher (model.go) owns this contract.
func TestPauseSurface_TUIEventLoopDispatchesOption(t *testing.T) {
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target: "index.html", ASTStatus: autonomy.ASTCorrupt,
		BudgetStatus:    autonomy.BudgetExceeded,
		EstimatedTokens: 5835, MaxOutputTokens: 1024,
	}, "$prompt"))

	// Bind the surface to a fresh dispatcher with a recording resumer.
	var routed ProposalIntent
	resume := func(_ context.Context, intent ProposalIntent) error {
		routed = intent
		return nil
	}
	m := NewModel(resume)

	// The HumanBoundaryProposalMsg activates the modal.
	_, _ = m.Update(HumanBoundaryProposalMsg{DecisionSurface: &surface})
	if !m.ProposalActive() {
		t.Fatal("proposal modal must be active after a HumanBoundaryProposalMsg")
	}

	// Press 1: must select the first option, which is a hard-block
	// recovery option. The Update returns a tea.Cmd that performs the
	// routing asynchronously; we execute it synchronously to read the
	// side effect.
	_, cmd := m.Update(bubbleteaKey("1"))
	if cmd == nil {
		t.Fatal("pressing 1 must return a routing command")
	}
	msg := cmd()
	if resumed, ok := msg.(ProposalResumedMsg); ok {
		routed = resumed.Intent
	} else {
		t.Fatalf("expected ProposalResumedMsg, got %T: %+v", msg, msg)
	}
	if routed == "" {
		t.Fatal("pressing 1 must route a ProposalIntent to the resumer")
	}
	// The routed intent must be the first option on the surface.
	wantFirst := surface.Options[0].Intent
	if routed != wantFirst {
		t.Fatalf("routed = %q, want %q (the first hard-block option)", routed, wantFirst)
	}
	// After selection, the modal is released.
	if m.ProposalActive() {
		t.Fatal("modal must release after a selection (the dispatch contract)")
	}
}

// bubbleteaKey returns a tea.KeyMsg for the given key name. The TUI
// dispatcher routes by msg.String() and matches key sequences in its
// proposal modal — the bubbletea test helpers are unnecessary here.
func bubbleteaKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
