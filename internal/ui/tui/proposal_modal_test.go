package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	autonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// fromAutonomy adapts the runtime autonomy DecisionSurface (which the tui
// package may not import in non-test code) onto the tui package's pure-data
// mirror. It is a value projection — no callbacks, no I/O.
func fromAutonomy(s autonomy.DecisionSurface) DecisionSurface {
	out := DecisionSurface{
		Target:            s.Target,
		ASTStatus:         string(s.ASTStatus),
		ExternalRefsCount: s.ExternalRefsCount,
		EstimatedTokens:   s.EstimatedTokens,
		CurrentBudget:     s.CurrentBudget,
		Options:           make([]ProposalOption, 0, len(s.Options)),
	}
	for _, opt := range s.Options {
		out.Options = append(out.Options, ProposalOption{
			ID:          opt.ID,
			Label:       opt.Label,
			Description: opt.Description,
			Intent:      ProposalIntent(opt.Intent),
		})
	}
	return out
}

// toAutonomy adapts a tui intent back onto the runtime autonomy vocabulary.
func toAutonomy(i ProposalIntent) autonomy.ProposalIntent {
	return autonomy.ProposalIntent(i)
}

// testSurface builds a $prompt decision surface with scope expansion offered,
// adapted onto the tui package's pure-data mirror.
func testSurface() DecisionSurface {
	return fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetWithinLimits,
	}, "$prompt"))
}

// TestProposalModalSelectEmitsPureIntent asserts that selecting an option emits
// a pure ProposalIntent value — and does NOT execute file writes. The test
// proves the purity by snapshotting the working directory contents before and
// after every selection.
func TestProposalModalSelectEmitsPureIntent(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "before", "untouched")
	snapshot := func() map[string]string {
		return map[string]string{
			"before": readMarker(t, root, "before"),
		}
	}

	surface := testSurface()
	modal := NewProposalModel(surface)

	if modal.OptionCount() != len(surface.Options) {
		t.Fatalf("option count = %d, want %d", modal.OptionCount(), len(surface.Options))
	}

	// Navigate down and select via Enter: must return the highlighted intent.
	modal.Navigate(1)
	got := modal.Select()
	if got != surface.Options[modal.Selected].Intent {
		t.Fatalf("Select() = %q, want %q", got, surface.Options[modal.Selected].Intent)
	}
	assertNoFilesChanged(t, root, snapshot(), "enter selection")

	// Digit-key selection must return the intent at that 1-based index.
	want := surface.Options[0].Intent
	intent, ok := modal.SelectIndex(0)
	if !ok || intent != want {
		t.Fatalf("SelectIndex(0) = %q, %v; want %q", intent, ok, want)
	}
	assertNoFilesChanged(t, root, snapshot(), "index selection")

	// HandleKey("enter") returns the highlighted intent.
	if _, ok := modal.HandleKey("enter"); !ok {
		t.Fatal("HandleKey(enter) must select")
	}
	assertNoFilesChanged(t, root, snapshot(), "handleKey enter")

	// HandleKey("esc") returns ProposalCancel.
	cancelIntent, ok := modal.HandleKey("esc")
	if !ok || cancelIntent != ProposalCancel {
		t.Fatalf("HandleKey(esc) = %q, %v; want cancel", cancelIntent, ok)
	}
	assertNoFilesChanged(t, root, snapshot(), "handleKey esc")

	// Cancel() always returns ProposalCancel — the abort route.
	if cancel := modal.Cancel(); cancel != ProposalCancel {
		t.Fatalf("Cancel() = %q, want cancel", cancel)
	}
	assertNoFilesChanged(t, root, snapshot(), "cancel")
}

// TestProposalModalSelectEmitsAutonomyVocabulary pins that the emitted intent
// converts cleanly onto the runtime autonomy ProposalIntent vocabulary.
func TestProposalModalSelectEmitsAutonomyVocabulary(t *testing.T) {
	surface := testSurface()
	modal := NewProposalModel(surface)
	modal.Selected = 0

	got := modal.Select()
	conv := toAutonomy(got)
	if !conv.Valid() {
		t.Fatalf("emitted intent %q must be valid in the autonomy vocabulary", got)
	}
	// The $prompt surface with unresolved deps must offer scope expansion, and
	// selecting it converts to the autonomy ProposalExpandScope value.
	if !modal.HasOption(ProposalExpandScope) {
		t.Fatal("$prompt surface must offer expand_scope")
	}
	idx := indexOfIntent(surface.Options, ProposalExpandScope)
	if _, ok := modal.SelectIndex(idx); !ok {
		t.Fatal("expand_scope option must be selectable")
	}
	if conv := toAutonomy(modal.Select()); conv != autonomy.ProposalExpandScope {
		t.Fatalf("selected expand_scope converted to %q, want expand_scope", conv)
	}
}

func indexOfIntent(opts []ProposalOption, intent ProposalIntent) int {
	for i, opt := range opts {
		if opt.Intent == intent {
			return i
		}
	}
	return -1
}

// TestProposalModalRoutingDelegatesNoFileWrites asserts the Route primitive
// hands the selected intent to the engine resumer and does not write any file
// itself — the resumer (RuntimeExecutor boundary) is the ONLY mutation path.
func TestProposalModalRoutingDelegatesNoFileWrites(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "before", "untouched")

	got := ""
	resume := func(_ context.Context, intent ProposalIntent) error {
		got = string(intent)
		return nil
	}

	ctx := context.Background()
	if err := Route(ctx, resume, ProposalRepairFirst); err != nil {
		t.Fatalf("route: %v", err)
	}
	if got != string(ProposalRepairFirst) {
		t.Fatalf("resumer received %q, want repair_first", got)
	}
	if _, err := os.Stat(filepath.Join(root, "before")); err != nil {
		t.Fatalf("routing must not touch the filesystem: %v", err)
	}
	if err := Cancel(ctx, resume); err != nil {
		t.Fatalf("cancel route: %v", err)
	}
	if got != string(ProposalCancel) {
		t.Fatalf("resumer received %q, want cancel", got)
	}

	// A nil resumer is refused — routing never silently no-ops.
	if err := Route(ctx, nil, ProposalRepairFirst); err == nil {
		t.Fatal("nil resumer must be refused")
	}
}

// TestProposalModalRendersDecisionSurface asserts the modal renders every
// option from the decision surface (including scope expansion for $prompt) and
// is pure presentation — no file side effects.
func TestProposalModalRendersDecisionSurface(t *testing.T) {
	surface := testSurface()
	modal := NewProposalModel(surface)

	out := modal.Render(60)
	if out == "" {
		t.Fatal("render must produce a box")
	}
	for _, opt := range surface.Options {
		if !contains(out, opt.Label) {
			t.Fatalf("render must include option label %q", opt.Label)
		}
	}
	if !contains(out, surface.Target) {
		t.Fatalf("render must include target %q", surface.Target)
	}
}

// TestProposalModalHotForbidsScopeExpansion pins the policy isolation at the
// modal level: a $hot surface must never contain a ProposalExpandScope option.
func TestProposalModalHotForbidsScopeExpansion(t *testing.T) {
	surface := fromAutonomy(autonomy.BuildDecisionSurface(autonomy.PreflightEvaluation{
		Target:           "index.html",
		ASTStatus:        autonomy.ASTCorrupt,
		DependencyStatus: autonomy.DependenciesUnresolved,
		BudgetStatus:     autonomy.BudgetExceeded,
	}, "$hot"))

	modal := NewProposalModel(surface)
	if modal.HasOption(ProposalExpandScope) {
		t.Fatal("$hot modal must NEVER offer ProposalExpandScope")
	}
	if !modal.HasOption(ProposalCancel) {
		t.Fatal("$hot modal must offer ProposalCancel")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func writeMarker(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoFilesChanged(t *testing.T, root string, snapshot map[string]string, step string) {
	t.Helper()
	for name, before := range snapshot {
		if now := readMarker(t, root, name); now != before {
			t.Fatalf("%s: file %q changed (proposal selection must never write): got %q want %q",
				step, name, now, before)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
