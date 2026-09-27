// fallback_chain_ops_test.go — THE ROLES TREE IS A LIST YOU CAN EDIT IN PLACE.
//
// # WHY REORDER AND DELETE LIVE IN THE TREE AND NOT SOMEWHERE ELSE
//
// A chain is an ORDERED queue of "and then what?", so the two things a user
// actually need are to change the order and to remove one element. Both are
// trivial on a list of rows and impossible on a list of role NAMES — which is
// the whole argument for the tree: the rows exist so these operations have
// something to name.
//
// The assertions below are about IDENTITY, not about indexes: a reorder test
// that says "row 2 moved to row 1" passes just as happily when the row that
// moved is a different model than the one the user picked. So each assertion
// names the model.

package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// threeHopPlan is the fixture every test here uses: a role with a primary and
// three ordered fallbacks, so a reorder and a delete both have room to be wrong
// in a distinguishable way.
func threeHopPlan(t *testing.T) Model {
	t.Helper()
	return treePicker(t, map[string][]string{
		RoleOverridePlan: {"ollama/a", "openai/gpt-4o", "groq/llama"},
	})
}

// planChain is the plan role's chain, defensively copied.
func planChain(m Model) []string {
	return m.FallbackChain(RoleOverridePlan)
}

// TestAltKAndAltJReorderTheHighlightedFallback is the DoD assertion for
// reordering, driven through the real key handler rather than through a helper,
// because the binding is what has to work.
func TestAltKAndAltJReorderTheHighlightedFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		up   bool
		from int
		want []string
	}{
		{"alt+j moves hop 0 down", false, 0, []string{"openai/gpt-4o", "ollama/a", "groq/llama"}},
		{"alt+j moves hop 1 down", false, 1, []string{"ollama/a", "groq/llama", "openai/gpt-4o"}},
		{"alt+k moves hop 2 up", true, 2, []string{"ollama/a", "groq/llama", "openai/gpt-4o"}},
		{"alt+k moves hop 1 up", true, 1, []string{"openai/gpt-4o", "ollama/a", "groq/llama"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := selectFallback(t, threeHopPlan(t), RoleOverridePlan, tc.from)
			var key tea.KeyMsg
			if tc.up {
				key = altRune('k')
			} else {
				key = altRune('j')
			}
			updated, _ := m.UpdateModel(key)
			if got := planChain(updated); !equalRuneSlices(got, tc.want) {
				t.Errorf("chain = %v, want %v", got, tc.want)
			}
			// Reordering is a STAGED edit like any other: the file is written
			// on the confirming Enter, never by the arrow key itself.
			if !updated.FallbackChainDirty() {
				t.Error("the reorder was not marked unsaved")
			}
			if !strings.Contains(updated.status, "Enter to save") {
				t.Errorf("the status does not say how to commit: %q", updated.status)
			}
		})
	}
}

// TestReorderKeepsTheHighlightOnTheMovedModel pins that the highlight follows
// the MODEL across the swap. An index-based highlight would stay on the same
// row, which after a swap is a DIFFERENT model — so the user moves a hop and
// then, without noticing, deletes the hop that took its place.
func TestReorderKeepsTheHighlightOnTheMovedModel(t *testing.T) {
	m := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 0)
	before, _ := m.HighlightedRoleNode()
	updated, _ := m.UpdateModel(altRune('j'))
	after, ok := updated.HighlightedRoleNode()
	if !ok {
		t.Fatal("no highlighted row after the reorder")
	}
	if after.Model != before.Model {
		t.Errorf("the highlight is on %q, want the moved model %q", after.Model, before.Model)
	}
	if after.Index == before.Index {
		t.Error("the moved model did not change position")
	}
}

// TestReorderAtTheEndsIsRefusedLoudly pins the boundary. Moving the first hop
// "up" is not a no-op with no feedback: a user holding Alt+K expects to know
// they have reached the top, and silence is indistinguishable from a broken key.
func TestReorderAtTheEndsIsRefusedLoudly(t *testing.T) {
	top := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 0)
	updated, cmd := top.UpdateModel(altRune('k'))
	if cmd != nil {
		t.Error("a refused reorder emitted a command")
	}
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("a refused reorder mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "already hop 1") {
		t.Errorf("the refusal was not explained: %q", updated.status)
	}

	bottom := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 2)
	updated, _ = bottom.UpdateModel(altRune('j'))
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("a refused reorder mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "already hop 1 of 3") {
		t.Errorf("the refusal did not say which hop: %q", updated.status)
	}
}

// TestReorderRefusesParentAndPrimaryRows pins the two rows a reorder must never
// touch, and that it SAYS so.
//
// Reordering the Primary is meaningless — it is hop 0 by definition — and
// guessing at "promote the first fallback into the primary slot" here would
// change what runs first, which is a different operation with a different
// consequence and a different confirmation.
func TestReorderRefusesParentAndPrimaryRows(t *testing.T) {
	parent := threeHopPlan(t)
	updated, _ := parent.UpdateModel(altRune('j'))
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("reordering a parent row mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "Fallback row") {
		t.Errorf("the refusal on a parent row was not explained: %q", updated.status)
	}

	primary := selectFallbackTestRow(t, threeHopPlan(t), RoleOverridePlan, RoleNodePrimary)
	updated, _ = primary.UpdateModel(altRune('j'))
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("reordering the Primary row mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "Primary row is hop 0") {
		t.Errorf("the refusal on the Primary row was not explained: %q", updated.status)
	}
}

// TestAltDAndDeleteRemoveTheHighlightedFallback is the DoD assertion for
// deletion, run through both accepted spellings.
func TestAltDAndDeleteRemoveTheHighlightedFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"alt+d", altRune('d')},
		{"alt+D", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D"), Alt: true}},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 1)
			updated, _ := m.UpdateModel(tc.msg)
			want := []string{"ollama/a", "groq/llama"}
			if got := planChain(updated); !equalRuneSlices(got, want) {
				t.Errorf("chain = %v, want %v (the survivors keep their order)", got, want)
			}
			if !updated.FallbackChainDirty() {
				t.Error("the removal was not marked unsaved")
			}
			if !strings.Contains(updated.status, "openai/gpt-4o") {
				t.Errorf("the status does not name the removed model: %q", updated.status)
			}
		})
	}
}

// TestDeleteThenDeleteAgainCannotReachAnotherRole is the assertion that makes
// deletion SAFE rather than merely possible.
//
// Removing a hop renumbers every row below it, so an index-based highlight lands
// on the next role's first hop. A user who presses Alt+D twice — once to remove
// what they meant, once more to remove what slid into place — would delete a
// model from a role they never selected. The highlight must land on the deleted
// role's own Primary row instead.
func TestDeleteThenDeleteAgainCannotReachAnotherRole(t *testing.T) {
	m := treePicker(t, map[string][]string{
		RoleOverridePlan:   {"ollama/a", "openai/gpt-4o", "groq/llama"},
		RoleOverrideCommit: {"openai/gpt-4o-mini"},
	})
	m = selectFallback(t, m, RoleOverridePlan, 0)
	m, _ = m.UpdateModel(altRune('d'))
	node, ok := m.HighlightedRoleNode()
	if !ok {
		t.Fatal("no highlighted row after the removal")
	}
	if node.Role != RoleOverridePlan {
		t.Fatalf("the highlight moved to role %q — a second Alt+D would edit the wrong role", node.Role)
	}
	// A second Alt+D on the Primary row is refused, so the worst case is a
	// refusal, never a cross-role deletion.
	before := m.FallbackChain(RoleOverrideCommit)
	m, _ = m.UpdateModel(altRune('d'))
	if got := m.FallbackChain(RoleOverrideCommit); !equalRuneSlices(got, before) {
		t.Errorf("a second Alt+D reached another role: %v", got)
	}
	if !strings.Contains(m.status, "never removes the Primary") {
		t.Errorf("the second Alt+D was not refused with a reason: %q", m.status)
	}
}

// TestDeleteRefusesParentAndPrimaryRows pins the two rows Alt+D must never
// remove. Deleting the primary would silently move a role's first execution
// from a configured model to whatever happened to be next.
func TestDeleteRefusesParentAndPrimaryRows(t *testing.T) {
	parent := threeHopPlan(t)
	updated, _ := parent.UpdateModel(altRune('d'))
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("Alt+D on a parent row mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "Fallback row") {
		t.Errorf("the refusal on a parent row was not explained: %q", updated.status)
	}

	primary := selectFallbackTestRow(t, threeHopPlan(t), RoleOverridePlan, RoleNodePrimary)
	updated, _ = primary.UpdateModel(altRune('d'))
	if got := planChain(updated); !equalRuneSlices(got, []string{"ollama/a", "openai/gpt-4o", "groq/llama"}) {
		t.Errorf("Alt+D on the Primary row mutated the chain: %v", got)
	}
	if !strings.Contains(updated.status, "never removes the Primary") {
		t.Errorf("the refusal on the Primary row was not explained: %q", updated.status)
	}
}

// TestChainOpsAreScopedToTheRolesPane pins that the new keys did not leak onto
// the models pane. Alt+J on the models list is not a reorder — there is no chain
// in view — and a key that quietly reorders an invisible chain is a way to
// corrupt a configuration the user cannot see.
func TestChainOpsAreScopedToTheRolesPane(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		altRune('k'), altRune('j'), altRune('d'),
		{Type: tea.KeyRunes, Runes: []rune("K"), Alt: true},
		{Type: tea.KeyUp, Alt: true}, {Type: tea.KeyDown, Alt: true},
		{Type: tea.KeyDelete},
	} {
		m := fallbackPicker(t, "ollama/a", "ollama/b", "ollama/c").
			SetFallbackChains(map[string][]string{RoleOverridePlan: {"ollama/a"}})
		updated, _ := m.UpdateModel(key)
		if got := updated.FallbackChain(RoleOverridePlan); !equalRuneSlices(got, []string{"ollama/a"}) {
			t.Errorf("%s on the models pane changed the chain: %v", key.String(), got)
		}
	}
}

// TestAltFFromTheModelsPaneAppendsToTheRoleSelectedOnTheLeft is the DoD
// assertion that the cross-pane add works, and that it targets the LEFT pane's
// role rather than the focused pane's role.
//
// It is the case a user hits constantly: pick a role on the left, Tab to the
// models, press Alt+F on a model. Keying the target off the keyboard's pane
// sends the edit to the workspace role instead — a chain that does not visibly
// change while a different one does, which is the most expensive kind of
// configuration bug because it is indistinguishable from "the feature is
// broken".
func TestAltFFromTheModelsPaneAppendsToTheRoleSelectedOnTheLeft(t *testing.T) {
	m := treePicker(t, nil)
	// The user selects the SECOND role on the left...
	m = m.SetRoleCursor(roleRowIndex(m, RoleOverrideCommit, RoleNodeParent))
	if got := m.FallbackRole(); got != RoleOverrideCommit {
		t.Fatalf("precondition: the highlighted role is %q, want commit", got)
	}
	// ...then crosses to the models pane, which moves the keyboard OFF the left
	// pane. Enter is the crossing key, not Tab: Tab is the three-way pane cycle
	// (Providers → Models → Roles → Providers), so from the ROLES tree it wraps
	// back to Providers and takes the roles view down with it. Enter means "I
	// chose this role, now pick the model", which is exactly the intent, and it
	// leaves the roles tree up so the user can still see the chain grow.
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if m.PaneFocus() != PaneModels {
		t.Fatalf("precondition: Enter should have moved focus to the models pane, got %v", m.PaneFocus())
	}
	if !m.ShowingRoles() {
		t.Fatal("precondition: crossing to the models pane must not tear the roles view down")
	}
	// ...and presses Alt+F. The commit role's chain must grow.
	m, _ = m.UpdateModel(altRune('f'))
	if got := m.FallbackChain(RoleOverrideCommit); len(got) != 1 || got[0] != "ollama/a" {
		t.Errorf("commit chain = %v, want [ollama/a]", got)
	}
	if got := m.FallbackChain(RoleOverridePlan); len(got) != 0 {
		t.Errorf("the add leaked into the plan chain: %v", got)
	}
	// And the left pane shows it IMMEDIATELY, without a confirm.
	view := ansi.Strip(m.renderRolesPane())
	if !strings.Contains(view, "[2] Fallback") || !strings.Contains(view, "ollama/a") {
		t.Errorf("the ROLES tree did not update in real time:\n%s", view)
	}
}

// TestEnterOnTheModelsPaneSetsThePrimaryOfTheLeftRole pins the other
// cross-pane operation: Enter with the roles view up binds the highlighted model
// as that role's PRIMARY, and the role comes from the highlighted TREE row —
// including a fallback child, because that is where a user's cursor ends up.
func TestEnterOnTheModelsPaneSetsThePrimaryOfTheLeftRole(t *testing.T) {
	for _, row := range []struct {
		kind RoleNodeKind
		hop  int
	}{
		{RoleNodeParent, 0},
		{RoleNodePrimary, 0},
		{RoleNodeFallback, 1},
	} {
		m := treePicker(t, map[string][]string{
			RoleOverridePlan:   {"ollama/a", "openai/gpt-4o"},
			RoleOverrideCommit: {"ollama/b", "openai/gpt-4o-mini"},
		})
		m = m.SetRoleCursor(fallbackRowIndex(t, m, RoleOverrideCommit, row.kind, row.hop))
		m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
		_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("Enter from a %s row emitted no command", row.kind)
		}
		msg, ok := cmd().(RolePolicyOverrideMsg)
		if !ok {
			t.Fatalf("Enter from a %s row emitted %T, want RolePolicyOverrideMsg", row.kind, cmd())
		}
		if msg.Role != RoleOverrideCommit {
			t.Errorf("Enter from a %s row bound the %q role, want commit", row.kind, msg.Role)
		}
		// The catalog's descriptors carry the BARE model id (the provider is a
		// separate field), so the bound id is "a", not the "ollama/a" slug.
		if msg.ModelID != "a" {
			t.Errorf("Enter bound %q, want the highlighted model", msg.ModelID)
		}
		if msg.Provider != "ollama" {
			t.Errorf("Enter bound provider %q, want ollama", msg.Provider)
		}
	}
}

// TestReorderThenDeleteThenConfirmEmitsTheFinalChain pins the round trip: the
// widget STAGES, and the confirming Enter carries the complete post-edit chain
// rather than a delta. A delta could not express "drop hop 2 of 3" and would
// leave the parent guessing.
func TestReorderThenDeleteThenConfirmEmitsTheFinalChain(t *testing.T) {
	m := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 0)
	m, _ = m.UpdateModel(altRune('j')) // [a gpt llama] → [gpt a llama]
	m = selectFallback(t, m, RoleOverridePlan, 1)
	m, _ = m.UpdateModel(altRune('d')) // drop "a"  → [gpt llama]
	if got := planChain(m); !equalRuneSlices(got, []string{"openai/gpt-4o", "groq/llama"}) {
		t.Fatalf("staged chain = %v", got)
	}
	_, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on a staged chain emitted no command")
	}
	msg, ok := cmd().(FallbackChainChangedMsg)
	if !ok {
		t.Fatalf("Enter emitted %T, want FallbackChainChangedMsg", cmd())
	}
	if msg.Role != RoleOverridePlan {
		t.Errorf("confirmed role = %q, want plan", msg.Role)
	}
	if !equalRuneSlices(msg.Chain, []string{"openai/gpt-4o", "groq/llama"}) {
		t.Errorf("confirmed chain = %v, want the final staged chain", msg.Chain)
	}
}

// TestTreeEditsAppearOnScreen is the DoD's "immediately updates the ROLES tree
// structure in real time", asserted through View rather than through the pane
// helper: a row that renders in a unit test and not in the view is a row a user
// never sees.
func TestTreeEditsAppearOnScreen(t *testing.T) {
	m := selectFallback(t, threeHopPlan(t), RoleOverridePlan, 0)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "[2] Fallback") || !strings.Contains(view, "ollama/a") {
		t.Fatalf("the view does not show the staged chain:\n%s", view)
	}
	// After a reorder the view's own numbering must move with it: [2] is now
	// the model that used to be hop 2.
	m, _ = m.UpdateModel(altRune('j'))
	view = ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	var plan []string
	for _, line := range lines {
		if strings.Contains(line, "Fallback") && !strings.Contains(line, "Fallback Chain") {
			plan = append(plan, strings.TrimSpace(line))
		}
	}
	if len(plan) < 2 {
		t.Fatalf("the view lost the fallback rows:\n%s", view)
	}
	if !strings.Contains(plan[0], "openai/gpt-4o") {
		t.Errorf("the first fallback row is %q, want the reordered model", plan[0])
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// fallbackRowIndex locates a fallback hop by identity, or a non-fallback row of
// the given kind for the role.
func fallbackRowIndex(t *testing.T, m Model, role string, kind RoleNodeKind, hop int) int {
	t.Helper()
	for i, n := range m.RoleTree() {
		if n.Role != role || n.Kind != kind {
			continue
		}
		if kind == RoleNodeFallback && n.Index != hop {
			continue
		}
		return i
	}
	t.Fatalf("no %s row for %q (hop %d) in %+v", kind, role, hop, m.RoleTree())
	return -1
}

// selectFallbackTestRow parks the cursor on a non-fallback row kind.
func selectFallbackTestRow(t *testing.T, m Model, role string, kind RoleNodeKind) Model {
	t.Helper()
	return m.SetRoleCursor(fallbackRowIndex(t, m, role, kind, 0))
}
