// fallback_chain_keymap_test.go — THE FALLBACK CHAIN IS CONFIGURABLE, AND ITS
// KEYS DO NOT EAT THE SEARCH BOX.
//
// # WHY THIS FILE IS SHAPED AROUND TWO FAILURES
//
// Configuring a role's fallback chain from the Model Registry can fail in two
// ways that no single-frame screenshot reveals:
//
//   - It can be UNREACHABLE. The obvious way to bind it is a bare `f`, and on
//     this surface a bare `f` is a query mutation — so the feature is
//     implemented, advertised, and impossible to use, because the user cannot
//     type anything containing the letter they are looking for. The symptom is
//     not a broken key; it is a search box that appears to be broken.
//   - It can be WRITTEN AT THE WRONG MOMENT. config.Save rewrites the user's
//     whole file, so a chain that writes on every toggle destroys the old chain
//     on the first of a sequence of keystrokes and cannot be undone by the
//     second. That is a data-loss bug wearing a UX costume.
//
// So every test here states either "the search field still works" or "the staged
// chain is what the user built", and the two are checked against the same state
// transitions rather than in separate files. The write-on-Enter half lives in
// internal/ui, next to the parent that performs the write.
//
// This file is in the WIDGET package because the properties are the widget's:
// it has direct access to the staged chain, the rendered line, and the exact key
// routing, and asserting them from outside would require exporting fields whose
// only purpose would be to be asserted.

package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// fallbackDescriptors turns "provider/model" ids into catalog descriptors. It is
// shared by the picker builder and by the tests that need to reason about the
// catalog itself, so a test never has to restate how a slug becomes a
// descriptor.
func fallbackDescriptors(ids []string) []registry.ModelDescriptor {
	models := make([]registry.ModelDescriptor, 0, len(ids))
	for _, id := range ids {
		provider, model, _ := strings.Cut(id, "/")
		models = append(models, registry.ModelDescriptor{ID: model, Provider: provider, Name: model})
	}
	return models
}

// fallbackPicker builds a picker over `provider/model` ids with the models pane
// focused — the surface every key test here runs against.
func fallbackPicker(t *testing.T, ids ...string) Model {
	t.Helper()
	return New(&registry.ModelSnapshot{Models: fallbackDescriptors(ids)}).SetPaneFocus(PaneModels)
}

// ── The search field survives the feature ────────────────────────────────────

// TestBareRunesStillReachTheSearchFilter is the load-bearing assertion for the
// whole "conflict-free keybinding" requirement, and it is stated as the POSITIVE
// case rather than the negative one: the three runes the feature was tempted to
// claim (f, a, r) must still filter the model list.
//
// Stating it positively matters because a negative test ("pressing f must not
// add a fallback") passes just as happily when the key does nothing at all. This
// one fails if the search box regresses AND if a bare keybinding creeps back in.
func TestBareRunesStillReachTheSearchFilter(t *testing.T) {
	// A catalog chosen so each letter under test discriminates: every id AND
	// every provider is free of the other two letters, so `f`, `a` and `r` each
	// match exactly one model. If any of the three letters were bound to a
	// feature instead, the model that letter names would be unreachable BY
	// SEARCH — which is the actual harm, and it is invisible in a screenshot of
	// a keybinding table.
	catalog := []string{"groq/filter-model", "deepseek/chat-alpha", "nomic/reasoner"}
	descs := fallbackDescriptors(catalog)
	for _, tc := range []struct {
		runes string
		why   string
	}{
		{"f", `a bare ` + "`f`" + ` must filter, or the word "fallback" is untypeable`},
		{"a", "a bare `a` must filter, or chat models are unreachable by search"},
		{"r", "a bare `r` must filter, or reasoning models are unreachable by search"},
		{"fall", `the word "fall" must be typeable`},
	} {
		t.Run("runes_"+tc.runes, func(t *testing.T) {
			// The expectation is computed with the PRODUCTION matcher rather
			// than restated here. Restating it would make this test a second
			// implementation of the filter, and it would then fail (or, worse,
			// pass) for reasons that have nothing to do with key routing.
			want := 0
			for _, d := range descs {
				if registry.MatchesQuery(d, tc.runes) {
					want++
				}
			}
			if want == len(descs) {
				t.Fatalf("the catalog does not discriminate %q; pick different ids", tc.runes)
			}

			m := fallbackPicker(t, catalog...)
			updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.runes)})
			if cmd != nil {
				t.Errorf("typing %q emitted a command; printable runes are search input only", tc.runes)
			}
			// The query must have GREWWN. This is the assertion a bare feature
			// binding fails even when the visible list happens to look the same:
			// the rune was consumed as a keystroke and dropped on the floor.
			if updated.query != tc.runes {
				t.Errorf("typing %q left the query %q — %s", tc.runes, updated.query, tc.why)
			}
			if got := len(updated.filtered); got != want {
				t.Errorf("typing %q left %d models visible, want %d — %s",
					tc.runes, got, want, tc.why)
			}
		})
	}
}

// TestBareRunesNeverMutateTheFallbackChain is the negative half, and it is
// necessary because the positive test above only proves the filter worked — a
// key that both filters AND toggles would pass it.
func TestBareRunesNeverMutateTheFallbackChain(t *testing.T) {
	for _, r := range []string{"f", "F", "a", "A", "r", "R"} {
		m := fallbackPicker(t, "ollama/llama3.2")
		updated, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)})
		if chain := updated.FallbackChain(updated.FallbackRole()); len(chain) != 0 {
			t.Errorf("bare %q mutated the fallback chain: %v", r, chain)
		}
		if updated.FallbackChainDirty() {
			t.Errorf("bare %q staged a fallback edit", r)
		}
	}
}

// TestDetailViewHasNoBareFeatureKeys is the same property on the OTHER model
// surface. The detail view is a model surface too, and a bare `r` there was
// quietly eating a filter query for as long as the bare binding existed.
func TestDetailViewHasNoBareFeatureKeys(t *testing.T) {
	m := fallbackPicker(t, "openai/o1")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m.State() != StateDetail {
		t.Fatal("precondition: the picker should be in StateDetail")
	}
	before, _ := m.CurrentReasoningOption()
	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		t.Error("a bare `r` in the detail view emitted a command")
	}
	if after, _ := updated.CurrentReasoningOption(); after != before {
		t.Errorf("a bare `r` cycled the effort from %q to %q", before, after)
	}
	if chain := updated.FallbackChain(updated.FallbackRole()); len(chain) != 0 {
		t.Errorf("a bare `r` mutated the fallback chain: %v", chain)
	}
}

// ── The modifier keys work ───────────────────────────────────────────────────

// TestFallbackModifierTogglesTheHighlightedModel drives the three accepted
// spellings of Alt+F through the real Update loop and asserts the chain each one
// produces.
//
// The three spellings are the point: a terminal that reports Alt+F as `alt+F` or
// as `ctrl+f` must not be told the feature does not exist, and a binding that
// only accepts the author's spelling is a binding that works on one machine.
func TestFallbackModifierTogglesTheHighlightedModel(t *testing.T) {
	spellings := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"alt+f", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true}},
		{"alt+F", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F"), Alt: true}},
		{"ctrl+f", tea.KeyMsg{Type: tea.KeyCtrlF}},
	}
	for _, s := range spellings {
		t.Run(s.name, func(t *testing.T) {
			m := fallbackPicker(t, "ollama/llama3.2")
			updated, _ := m.UpdateModel(s.msg)
			roleKey := updated.FallbackRole()
			chain := updated.FallbackChain(roleKey)
			if len(chain) != 1 || chain[0] != "ollama/llama3.2" {
				t.Fatalf("chain = %v, want [ollama/llama3.2]", chain)
			}
			if !updated.FallbackChainDirty() {
				t.Error("the edit was not marked unsaved")
			}
			// The status line names the ROLE, because the same keypress edits a
			// different chain depending on the pane and the models alone would
			// not say which.
			if !strings.Contains(updated.status, roleKey) {
				t.Errorf("the status line does not name the role %q: %q", roleKey, updated.status)
			}

			// Pressing it again REMOVES the same model — the toggle is a toggle.
			removed, _ := updated.UpdateModel(s.msg)
			if chain := removed.FallbackChain(roleKey); len(chain) != 0 {
				t.Errorf("a second press left the chain at %v, want empty", chain)
			}
		})
	}
}

// TestFallbackChainPreservesOrderAcrossEdits pins the semantics that make a chain
// a chain rather than a set: the order is the content, and it must survive
// edits that do not concern it.
func TestFallbackChainPreservesOrderAcrossEdits(t *testing.T) {
	m := fallbackPicker(t, "ollama/a", "ollama/b", "ollama/c")
	roleKey := m.FallbackRole()

	// A provider-qualified catalog: entries are appended in highlight order, so
	// pressing Alt+F on each in turn builds the chain in that order. The key is
	// the model's LAST character, so it is also a key that would be a plausible
	// bare binding — which is the point: a modifier makes the two impossible to
	// confuse.
	for i := range 3 {
		m = m.SetCursor(i)
		m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	}
	want := []string{"ollama/a", "ollama/b", "ollama/c"}
	if got := m.FallbackChain(roleKey); !equalRuneSlices(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}

	// Remove the MIDDLE entry: the survivors keep their relative order.
	m = m.SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if got := m.FallbackChain(roleKey); !equalRuneSlices(got, []string{"ollama/a", "ollama/c"}) {
		t.Errorf("after removing the middle entry the chain is %v, want [ollama/a ollama/c]", got)
	}
	// And removing the tail leaves the head alone.
	m = m.SetCursor(2)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if got := m.FallbackChain(roleKey); !equalRuneSlices(got, []string{"ollama/a"}) {
		t.Errorf("removing the tail left %v, want [ollama/a]", got)
	}
}

// TestFallbackChainWritesAreSlugQualified pins the one property that makes a
// staged chain survive the user switching providers between configuring it and
// the turn that consults it. A bare model ID is resolved against whatever
// provider happens to be bound at that moment, and getting that wrong is a 404
// from a different company rather than a visible error.
func TestFallbackChainWritesAreSlugQualified(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	chain := m.FallbackChain(m.FallbackRole())
	if len(chain) != 1 || chain[0] != "ollama/llama3.2" {
		t.Fatalf("chain = %v, want the provider-qualified [ollama/llama3.2]", chain)
	}
}

// ── The role the edit applies to ─────────────────────────────────────────────

// TestFallbackRoleFollowsTheSurface pins which chain Alt+F edits. It matters
// because the runtime consults the chain for the TURN ROLE, so a widget that
// defaulted to any other role would let a user configure a chain that provably
// never fires — the most expensive kind of configuration bug, because it is
// indistinguishable from "the fallback feature is broken".
func TestFallbackRoleFollowsTheSurface(t *testing.T) {
	// Default: the turn role for a non-plan workspace.
	if got := fallbackPicker(t, "ollama/llama3.2").FallbackRole(); got != "default" {
		t.Errorf("FallbackRole in the default workspace = %q, want \"default\"", got)
	}
	// Plan workspace: the plan chain, which is what a plan-mode turn consults.
	m := fallbackPicker(t, "ollama/llama3.2").SetActiveWorkspace("plan")
	if got := m.FallbackRole(); got != "plan" {
		t.Errorf("FallbackRole in the plan workspace = %q, want \"plan\"", got)
	}
	// Roles pane: the highlighted role wins, because the user navigated to it.
	//
	// The index is computed from the TREE rather than hard-coded, because the
	// roles pane is no longer a list of two names: it is a flattened hierarchy
	// of roles and their [1] Primary / [n] Fallback children. A literal 1 here
	// would silently select the plan role's PRIMARY CHILD — which still names
	// the plan role, so the assertion would still be about the plan role while
	// claiming to be about the second one. That is the failure mode a hard-coded
	// index produces here, and it is why the row is located by identity.
	m = fallbackPicker(t, "ollama/llama3.2")
	m.paneFocus = PaneRoles
	m.showingRoles = true
	m = m.SetRoleCursor(roleRowIndex(m, RoleOverrideCommit, RoleNodeParent))
	if got := m.FallbackRole(); got != RoleOverrideCommit {
		t.Errorf("FallbackRole on the second Roles entry = %q, want %q", got, RoleOverrideCommit)
	}
	// And each role's chain is independent: an edit in the plan chain must not
	// appear in the default chain.
	m = fallbackPicker(t, "ollama/llama3.2").SetActiveWorkspace("plan")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if got := m.FallbackChain("plan"); len(got) != 1 {
		t.Fatalf("the plan chain is %v, want one hop", got)
	}
	if got := m.FallbackChain("default"); len(got) != 0 {
		t.Errorf("the plan edit leaked into the default chain: %v", got)
	}
}

// ── Staging, confirming, discarding ───────────────────────────────────────────

// TestEnterConfirmsTheStagedChainAndEmitsIt is the widget half of the write-on-
// confirm contract: Enter emits FallbackChainChangedMsg carrying the COMPLETE
// chain, because a message carrying only a delta could not express "drop hop 2
// of 3" and would leave the parent guessing.
func TestEnterConfirmsTheStagedChainAndEmitsIt(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2", "openai/gpt-4o")
	m.paneFocus = PaneModels
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	m = m.SetCursor(1)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})

	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on a staged chain emitted no command")
	}
	msg, ok := cmd().(FallbackChainChangedMsg)
	if !ok {
		t.Fatalf("Enter emitted %T, want FallbackChainChangedMsg", cmd())
	}
	if msg.Role != m.FallbackRole() {
		t.Errorf("confirmed role = %q, want %q", msg.Role, m.FallbackRole())
	}
	if !equalRuneSlices(msg.Chain, []string{"ollama/llama3.2", "openai/gpt-4o"}) {
		t.Errorf("confirmed chain = %v, want both hops in order", msg.Chain)
	}
	// The confirm takes priority over activation: a staged edit that activated a
	// model instead would close the modal and silently drop the chain.
	if updated.State() == StateDetail {
		t.Error("Enter on a staged chain also opened the detail view; one key, one action")
	}
}

// TestEnterOnAnUnstagedChainStillActivates is the other half, and it is what
// makes the priority safe: the confirm short-circuit is gated on a staged edit,
// so the normal Enter contract is untouched when there is nothing to save.
func TestEnterOnAnUnstagedChainStillActivates(t *testing.T) {
	m := fallbackPicker(t, "openai/o1")
	updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter with nothing staged emitted %T, want nil", cmd())
	}
	if updated.State() != StateDetail {
		t.Errorf("Enter with nothing staged left the picker in %v, want StateDetail", updated.State())
	}
}

// TestApplyFallbackChainConfirmClearsTheDirtyMarker pins the round trip: after a
// successful write the badge must clear, and the parent is the only thing that
// clears it — a widget that cleared it on Enter would claim "saved" for a write
// that never happened.
func TestApplyFallbackChainConfirmClearsTheDirtyMarker(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if !m.FallbackChainDirty() {
		t.Fatal("precondition: the edit should be staged")
	}
	if saved := m.ApplyFallbackChainConfirm(); saved.FallbackChainDirty() {
		t.Error("ApplyFallbackChainConfirm did not clear the unsaved marker")
	}
	// And the chain survives the confirm — clearing the badge must not clear the
	// data.
	saved := m.ApplyFallbackChainConfirm()
	if got := saved.FallbackChain(saved.FallbackRole()); len(got) != 1 {
		t.Errorf("the confirm cleared the chain: %v", got)
	}
}

// TestSetFallbackChainsResetsTheDirtyMarker: a re-seed is the parent saying
// "this is what is on disk", and any staged edit has just been overruled by an
// authoritative value. Leaving the badge set would show a user an "unsaved"
// marker for an edit that no longer exists.
func TestSetFallbackChainsResetsTheDirtyMarker(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if !m.FallbackChainDirty() {
		t.Fatal("precondition: the edit should be staged")
	}
	seeded := m.SetFallbackChains(map[string][]string{"default": {"ollama/other"}})
	if seeded.FallbackChainDirty() {
		t.Error("a re-seed from the parent left the unsaved marker set")
	}
	if got := seeded.FallbackChain("default"); !equalRuneSlices(got, []string{"ollama/other"}) {
		t.Errorf("the re-seed produced %v, want the parent's value", got)
	}
}

// TestFallbackChainAccessorReturnsCopies pins that the widget's chain cannot be
// mutated by a caller — the chain is what gets persisted, and a caller that
// could reach into the widget's slice by accident could write a chain the user
// never staged.
func TestFallbackChainAccessorReturnsCopies(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})

	chain := m.FallbackChain(m.FallbackRole())
	chain[0] = "ollama/tampered"
	if got := m.FallbackChain(m.FallbackRole()); got[0] != "ollama/llama3.2" {
		t.Errorf("mutating the returned slice changed the widget's chain: %v", got)
	}
	all := m.FallbackChains()
	for k := range all {
		all[k] = nil
	}
	if got := m.FallbackChain(m.FallbackRole()); len(got) != 1 {
		t.Errorf("mutating FallbackChains() changed the widget's chain: %v", got)
	}
}

// ── The metadata panel ────────────────────────────────────────────────────────

// TestFallbackChainLineRendersTheActiveChain pins the metadata panel's form,
// because that string is what a user reads to decide whether the configuration
// in front of them is the configuration they have.
func TestFallbackChainLineRendersTheActiveChain(t *testing.T) {
	m := fallbackPicker(t, "ollama/qwen2.5-coder:7b", "ollama/llama3.2")
	m = m.SetFallbackChains(map[string][]string{
		"default": {"ollama/qwen2.5-coder:7b", "ollama/llama3.2"},
	})
	line := ansi.Strip(m.renderFallbackChainLine())
	for _, want := range []string{
		"Fallback Chain:", "default",
		"1. ollama/qwen2.5-coder:7b", "->", "2. ollama/llama3.2",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the chain line omits %q: %s", want, line)
		}
	}
}

// TestFallbackChainLineShowsUnsavedAndEmpty explicitly covers the two states a
// blank rendering cannot express. Both exist for the same reason: this surface is
// where a user checks what their config says, and an absence is the one
// rendering indistinguishable from "the feature is off".
func TestFallbackChainLineShowsUnsavedAndEmpty(t *testing.T) {
	empty := ansi.Strip(fallbackPicker(t, "ollama/llama3.2").renderFallbackChainLine())
	if !strings.Contains(empty, "(none)") {
		t.Errorf("an empty chain rendered as an absence rather than as \"none\": %s", empty)
	}

	m := fallbackPicker(t, "ollama/llama3.2")
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	if line := ansi.Strip(m.renderFallbackChainLine()); !strings.Contains(line, "unsaved") {
		t.Errorf("a staged edit did not render as unsaved: %s", line)
	}
	if line := ansi.Strip(m.ApplyFallbackChainConfirm().renderFallbackChainLine()); strings.Contains(line, "unsaved") {
		t.Errorf("a confirmed edit still renders as unsaved: %s", line)
	}
}

// TestBrowsingViewRendersTheFallbackChainLine: the line is chrome, so it must be
// on screen in the layout and not only in a helper. A line that renders in a
// unit test and not in the view is a line a user never sees.
func TestBrowsingViewRendersTheFallbackChainLine(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2", "openai/gpt-4o").SetSize(100, 30)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l"), Alt: true})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Fallback Chain:") {
		t.Errorf("the browsing view does not render the fallback chain line:\n%s", view)
	}
	if !strings.Contains(view, "Alt+F") {
		t.Errorf("the browsing view does not advertise the Alt+F binding:\n%s", view)
	}
	// The detail view reaches the same editor on the same key.
	detail := fallbackPicker(t, "openai/o1").SetSize(100, 30)
	detail, _ = detail.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if d := ansi.Strip(detail.View()); !strings.Contains(d, "Alt+F") {
		t.Errorf("the detail footer does not advertise Alt+F:\n%s", d)
	}
}

// TestStatusRowSurvivesTheModalHeightBudget is the regression guard for an
// off-by-one that predates the fallback chain and that the fallback chain then
// made fatal.
//
// padFooter hard-clips from the END of the body, so a chrome budget one row too
// generous does not crop the list — it crops the STATUS line, silently, on a
// modal of an entirely normal size. Every status this surface has ever shown was
// therefore invisible, and a user pressing Alt+F appeared to get no feedback at
// all: the chain line updated and nothing said so.
//
// The assertion is a sentinel rather than a specific status, so it fails on the
// BUDGET and not on the wording of whichever message happens to be current.
func TestStatusRowSurvivesTheModalHeightBudget(t *testing.T) {
	for _, size := range [][2]int{{110, 30}, {100, 24}, {80, 20}, {130, 40}, {70, 16}} {
		m := fallbackPicker(t, "ollama/llama3.2", "openai/gpt-4o").SetSize(size[0], size[1])
		m.status = "STATUS-SENTINEL"
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "STATUS-SENTINEL") {
			t.Errorf("modal %dx%d clipped the status line (chrome budget is one row too "+
				"generous; padFooter clips the body from the end):\n%s", size[0], size[1], view)
		}
	}
}

// TestFallbackToggleFeedbackIsVisible closes the loop on the property the whole
// keymap exists to deliver: pressing Alt+F must produce visible feedback, and
// the feedback must say what happened and how to commit it.
func TestFallbackToggleFeedbackIsVisible(t *testing.T) {
	m := fallbackPicker(t, "ollama/llama3.2", "openai/gpt-4o").SetSize(110, 30)
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true})
	view := ansi.Strip(m.View())
	for _, want := range []string{"Added", "ollama/llama3.2", "default", "Enter to save"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view after Alt+F omits %q — the user cannot tell the key did "+
				"anything:\n%s", want, view)
		}
	}
}

func equalRuneSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// roleRowIndex locates a tree row by IDENTITY (role + kind + hop) rather than by
// position, so a test about "the plan role's second fallback" keeps testing the
// plan role's second fallback after the tree gains a level or the chain grows.
func roleRowIndex(m Model, role string, kind RoleNodeKind) int {
	for i, n := range m.RoleTree() {
		if n.Role == role && n.Kind == kind {
			return i
		}
	}
	return -1
}
