// roles_tree_test.go — THE ROLES PANE IS A TREE, AND A KEY PRESS ACTS ON THE ROW
// THE USER IS LOOKING AT.
//
// # WHY A TREE NEEDS THIS MANY ASSERTIONS
//
// A flat list of role names can say "this role exists". It cannot say which
// model is primary, where in the priority order a fallback sits, or — the
// failure that costs real data — WHICH HOP A KEY PRESS IS ABOUT TO DELETE. "
// Remove the highlighted fallback" is well-defined on a list of hops and
// undefined on a list of names.
//
// So the properties asserted here are structural, not cosmetic: what rows
// exist, in what order, what each row's identity is, and where the highlight
// lands after a mutation renumbers everything below it. The last one is the
// subtle one and gets its own test.

package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// treePicker builds a picker whose ROLES tree is up and focused, with the given
// per-role chains already staged.
func treePicker(t *testing.T, chains map[string][]string) Model {
	t.Helper()
	m := fallbackPicker(t, "ollama/a", "ollama/b", "ollama/c").SetSize(120, 32)
	m = m.SetShowingRoles(true).SetPaneFocus(PaneRoles)
	m = m.SetRoleOverrides(map[string]OverrideBinding{
		RoleOverridePlan: {ModelID: "openrouter/deepseek-r1", Provider: "openrouter"},
	})
	if len(chains) > 0 {
		m = m.SetFallbackChains(chains)
	}
	return m
}

// selectFallback parks the tree highlight on a role's n-th fallback hop
// (0-based), by identity rather than by index.
func selectFallback(t *testing.T, m Model, role string, hop int) Model {
	t.Helper()
	idx := -1
	for i, n := range m.RoleTree() {
		if n.Kind == RoleNodeFallback && n.Role == role && n.Index == hop {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("no %s fallback hop %d in the tree: %+v", role, hop, m.RoleTree())
	}
	return m.SetRoleCursor(idx)
}

// ── What the tree contains ────────────────────────────────────────────────────

// TestRolesTreeShowsPrimaryAndNumberedFallbacks is the DoD assertion: an
// EXPANDED role renders a [1] Primary row and one numbered row per hop.
//
// It is stated positively, on the rendered pane, because the alternative —
// asserting on RoleTree()'s contents — passes just as happily when the renderer
// draws something else entirely. A feature that exists in the model and not on
// screen is a feature nobody has.
func TestRolesTreeShowsPrimaryAndNumberedFallbacks(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "openai/gpt-4o"}})
	view := ansi.Strip(m.renderRolesPane())
	for _, want := range []string{
		"Plan / Thinking",
		"[1] Primary",
		"[2] Fallback", "ollama/a",
		"[3] Fallback", "openai/gpt-4o",
		// Both role parents are present, expanded (▼), so the tree is the
		// default shape rather than something the user has to switch on.
		"▼ Plan / Thinking", "▼ Commit / Fast",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the roles tree omits %q:\n%s", want, view)
		}
	}
	// The primary is the role's declared binding, not whatever the models
	// cursor happens to be on: a tree that rendered the highlighted model as a
	// role's primary would show a configuration nobody wrote.
	if !strings.Contains(view, "deepseek-r1") {
		t.Errorf("the [1] Primary row does not carry the role's binding:\n%s", view)
	}
}

// TestRolesTreeIsExpandedByDefault pins that the roles render EXPANDED with no
// entry in the collapse map. A collapsed-by-default tree renders as the flat
// list it replaced, and the user discovers the structure by pressing a key
// nothing on screen advertises.
func TestRolesTreeIsExpandedByDefault(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
	for _, ro := range roleOverrideEntries {
		if !m.roleExpanded(ro.Key) {
			t.Errorf("role %q is collapsed on a fresh picker", ro.Key)
		}
	}
	// A parent row's Expanded flag is what the ▶/▼ marker is derived from, so
	// assert the flag too: a correct tree and a wrong marker is a tree whose
	// marker lies about the tree.
	node, ok := m.HighlightedRoleNode()
	if !ok || node.Kind != RoleNodeParent || !node.Expanded {
		t.Errorf("row 0 = %+v, want an expanded parent", node)
	}
}

// TestRolesTreeCollapseHidesChildrenAndPersists pins the fold: Left collapses,
// Right re-expands, and a collapse survives a structural edit so deleting a hop
// out of a chain the user had folded does not unfold it under them.
func TestRolesTreeCollapseHidesChildrenAndPersists(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "ollama/b"}})
	before := len(m.RoleTree())

	m.collapseRoleNode()
	if len(m.RoleTree()) != before-3 { // parent + primary + two hops
		t.Errorf("collapsing hid %d rows, want 3", before-len(m.RoleTree()))
	}
	if view := ansi.Strip(m.renderRolesPane()); strings.Contains(view, "[2] Fallback") {
		t.Errorf("a collapsed role still renders its children:\n%s", view)
	}
	if view := ansi.Strip(m.renderRolesPane()); !strings.Contains(view, "▶ Plan / Thinking") {
		t.Errorf("a collapsed role does not render the collapsed marker:\n%s", view)
	}

	// A structural edit while collapsed must not re-expand it: re-seed the
	// chain from the parent (which is what a successful write does) and confirm
	// the fold survives.
	m = m.SetFallbackChains(map[string][]string{
		RoleOverridePlan:   {"ollama/a"},
		RoleOverrideCommit: {"ollama/c"},
	})
	if m.roleExpanded(RoleOverridePlan) {
		t.Error("a re-seed re-expanded a role the user had collapsed")
	}
	if view := ansi.Strip(m.renderRolesPane()); strings.Contains(view, "ollama/a") {
		t.Errorf("a re-seed unhid a collapsed role's children:\n%s", view)
	}

	m.expandRoleNode()
	if !m.roleExpanded(RoleOverridePlan) {
		t.Error("Right did not re-expand the role")
	}
	if len(m.RoleTree()) != before {
		t.Errorf("re-expanding restored %d rows, want %d", len(m.RoleTree()), before)
	}
}

// TestRolesTreeLeftFromChildWalksToParent pins the two-outcome Left: on a child
// it means "go to my parent", on a parent it means "collapse me". Both are the
// standard tree idiom, and implementing only one leaves a user with no way back
// up from a deeply nested chain.
func TestRolesTreeLeftFromChildWalksToParent(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "ollama/b"}})
	m = selectFallback(t, m, RoleOverridePlan, 1)
	m.collapseRoleNode()
	node, _ := m.HighlightedRoleNode()
	if node.Kind != RoleNodeParent || node.Role != RoleOverridePlan {
		t.Fatalf("Left from a child landed on %+v, want the plan parent row", node)
	}
	// And it did NOT collapse on the way: ↓↑ should not silently fold a role
	// the user just opened.
	if !m.roleExpanded(RoleOverridePlan) {
		t.Error("walking up to the parent also collapsed it")
	}
}

// ── What the highlight means ──────────────────────────────────────────────────

// TestHighlightedRoleResolvesTheOwningRoleOfEveryRowKind pins that the role a
// cross-pane key press acts on is the role of the highlighted ROW, whatever kind
// of row that is.
//
// It is stated for all three kinds because the failure is invisible from any one
// of them: a HighlightedRole that only looks at parent rows would look correct
// in every test that happens to leave the cursor on a parent, and would edit the
// wrong role the moment the user parked the cursor on a fallback — which is
// exactly where they park it when they want to delete that fallback.
func TestHighlightedRoleResolvesTheOwningRoleOfEveryRowKind(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "ollama/b"}})
	want := map[RoleNodeKind]bool{
		RoleNodeParent:   true,
		RoleNodePrimary:  true,
		RoleNodeFallback: true,
	}
	seen := make(map[RoleNodeKind]bool)
	for i, node := range m.RoleTree() {
		if !want[node.Kind] {
			continue
		}
		got := m.SetRoleCursor(i).HighlightedRole()
		if got != node.Role {
			t.Errorf("row %d (%s of %q) resolved to role %q", i, node.Kind, node.Role, got)
		}
		seen[node.Kind] = true
	}
	for kind := range want {
		if !seen[kind] {
			t.Errorf("the fixture never produced a %s row, so nothing was asserted about it", kind)
		}
	}
}

// TestRolesTreeFocusIndicatorDistinguishesParentFromChild pins the DoD's
// "clearly highlight whether focus is on the Parent Role Node or a specific
// Child Fallback Node".
//
// It is asserted on the PLAIN TEXT, not the styled render, because the
// distinguishing marks have to survive a terminal with no colour support. A
// design that only distinguishes rows by background colour is unusable in
// exactly the terminals that need it most.
func TestRolesTreeFocusIndicatorDistinguishesParentFromChild(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
	child := selectFallback(t, m, RoleOverridePlan, 0)
	childView := ansi.Strip(child.renderRolesPane())
	// The focused CHILD carries the cursor and its own label.
	if !strings.Contains(childView, "▸ [2] Fallback") {
		t.Errorf("the focused fallback row has no cursor + label:\n%s", childView)
	}
	// The focused PARENT is on a different row, and the child it used to be is
	// no longer marked — a cursor on two rows at once is two possible answers
	// to "what will Alt+D delete".
	parentView := ansi.Strip(m.renderRolesPane())
	if !strings.Contains(parentView, "▸ ▼ Plan / Thinking") {
		t.Errorf("the focused parent row has no cursor + marker:\n%s", parentView)
	}
	if strings.Contains(parentView, "▸ [2] Fallback") {
		t.Errorf("the child row is still marked focused while the parent is:\n%s", parentView)
	}
}

// TestRolesTreeCursorDoesNotShiftRowContents pins that focusing a row does not
// move its text. A cursor that occupies the indent on a focused row and not on
// an unfocused one makes every model slug change column as the highlight moves,
// which is a list users misread.
func TestRolesTreeCursorDoesNotShiftRowContents(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
	unfocused := stripTreeRow(t, ansi.Strip(m.renderRolesPane()), "ollama/a")
	focused := stripTreeRow(t, ansi.Strip(selectFallback(t, m, RoleOverridePlan, 0).renderRolesPane()), "ollama/a")
	if unfocused != focused {
		t.Errorf("the model slug moved when the row was focused:\n unfocused %q\n   focused %q", unfocused, focused)
	}
}

// TestRolesTreeWindowFollowsTheHighlight pins that a tree taller than its pane
// still shows the cursor.
//
// Without the extra slack in visibleRoleWindow, ↓ past the last visible row
// scrolls the highlight clean off the bottom: a pane where nothing is
// highlighted reads as "the key is broken", and the user has no way to tell
// which row they are on.
func TestRolesTreeWindowFollowsTheHighlight(t *testing.T) {
	m := fallbackPicker(t, "ollama/a", "ollama/b").SetSize(120, 16)
	m = m.SetShowingRoles(true).SetPaneFocus(PaneRoles)
	m = m.SetFallbackChains(map[string][]string{RoleOverridePlan: {
		"ollama/a", "ollama/b", "ollama/c", "openai/gpt-4o", "groq/llama", "openai/o1",
	}})
	total := len(m.RoleTree())
	budget := m.paneHeight() - 2
	if total <= budget {
		t.Fatalf("precondition: the tree (%d rows) must overflow the pane budget (%d)", total, budget)
	}
	for _, idx := range []int{0, total - 1, total / 2} {
		start, end := m.SetRoleCursor(idx).visibleRoleWindow(total, budget)
		if idx < start || idx >= end {
			t.Errorf("cursor %d is outside the visible window [%d,%d)", idx, start, end)
		}
	}
}

// TestRolesTreePaneHeightMatchesTheModelsPane pins the geometry invariant the
// whole dual-pane layout rests on: both panes are EXACTLY paneH lines, because
// they are joined horizontally and a pane one row taller drags the modal's
// horizontal rule out of alignment for the rest of the session.
func TestRolesTreePaneHeightMatchesTheModelsPane(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {100, 24}, {80, 20}, {70, 16}} {
		m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "ollama/b"}})
		m = m.SetSize(size[0], size[1])
		roles := strings.Split(strings.TrimRight(m.renderRolesPane(), "\n"), "\n")
		models := strings.Split(strings.TrimRight(m.renderModelsPane(), "\n"), "\n")
		if len(roles) != m.paneHeight() || len(models) != m.paneHeight() {
			t.Errorf("modal %dx%d: roles pane has %d lines, models pane %d, want %d each",
				size[0], size[1], len(roles), len(models), m.paneHeight())
		}
		// And the two panes plus the separator must tile the SAME width in both
		// left-pane modes. That is the invariant a leftPaneWidth() mismatch
		// between the two renderers breaks, and it is what leaves a gap (or an
		// overlap) between the panes.
		rolesTotal := lipgloss.Width(m.renderRolesPane()) + 1 + lipgloss.Width(m.renderModelsPane())
		plain := m.SetShowingRoles(false)
		plainTotal := lipgloss.Width(plain.renderProvidersPane()) + 1 + lipgloss.Width(plain.renderModelsPane())
		if rolesTotal != plainTotal {
			t.Errorf("modal %dx%d: the panes tile to %d cells with the roles tree and %d without",
				size[0], size[1], rolesTotal, plainTotal)
		}
		if got := lipgloss.Width(m.renderRolesPane()); got != rolesPaneWidth {
			t.Errorf("modal %dx%d: the roles pane is %d cells, want %d",
				size[0], size[1], got, rolesPaneWidth)
		}
	}
}

// ── Search input is uncontested ───────────────────────────────────────────────

// TestRolesTreeTypingAlwaysReachesTheSearchFilter is the load-bearing assertion
// for the whole tree pane, and it is deliberately paranoid: every letter of
// every feature label, typed while the ROLES tree has focus.
//
// The tree added Alt+K, Alt+J, Alt+D and Alt+E, and each of those is a plausible
// bare key on a different design (K for "keep", D for delete, E for edit). Each
// one bound bare would make the model it names unsearchable — silently, because
// the list just stops changing. A table of keybindings does not reveal this;
// only driving real key messages through Update does.
//
// j and k are deliberately NOT in this list, and the omission is the contract
// rather than a gap in the test: the tree navigates with j/k by design (vim-style
// motion on the surface the user is looking at), and those two keys are handed
// to the search field the moment search focus is engaged. See
// TestRolesTreeJKNavigatesThenYieldsToSearch. What must never happen is a bare
// FEATURE key, and none of these letters is one.
func TestRolesTreeTypingAlwaysReachesTheSearchFilter(t *testing.T) {
	for _, runes := range []string{
		// One letter at a time, which is how a terminal delivers typing.
		"e", "d", "a", "r", "f", "p", "s", "v", "i", "x", "l", "t", "n", "o",
		// And the words the feature labels are made of, minus the j/k navigation.
		"ed", "delete", "params", "fallback", "reorder", "primary", "chain",
	} {
		m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
		updated, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(runes)})
		if cmd != nil {
			t.Errorf("typing %q on the roles tree emitted a command; printable runes are search input only", runes)
		}
		if updated.Query() != runes {
			t.Errorf("typing %q left the query %q — the tree took a bare key", runes, updated.Query())
		}
		// And no feature was triggered by the act of typing it.
		if updated.FallbackChainDirty() {
			t.Errorf("typing %q staged a chain edit", runes)
		}
		if updated.RoleParamsDirty() {
			t.Errorf("typing %q staged a parameter edit", runes)
		}
		if updated.RoleConfigActive() {
			t.Errorf("typing %q opened the parameter editor", runes)
		}
	}
}

// TestRolesTreeJKNavigatesThenYieldsToSearch pins the ONE bare-key contract this
// widget has, in the order that makes it usable.
//
// j/k walk the tree — roles AND their children, which is the whole point of a
// tree — and the search field takes them back the instant it is engaged. Both
// halves are asserted, because either alone is a bug: navigation that never
// yields makes the word "j" untypeable, and yielding always makes j/k
// unusable.
func TestRolesTreeJKNavigatesThenYieldsToSearch(t *testing.T) {
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a", "ollama/b"}})
	start := m.RoleTreeCursor()

	// Navigation: ↓ j walks two rows, and the second row is the role's FIRST
	// FALLBACK — the proof that the walk goes through children rather than
	// through role names.
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	afterDown, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if afterDown.RoleTreeCursor() != start+2 {
		t.Errorf("↓ then j moved to row %d, want %d", afterDown.RoleTreeCursor(), start+2)
	}
	node, _ := afterDown.HighlightedRoleNode()
	if node.Kind != RoleNodeFallback || node.Index != 0 {
		t.Errorf("↓↓ landed on %+v, want the role's first fallback hop", node)
	}
	afterK, _ := afterDown.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if afterK.RoleTreeCursor() != start+1 {
		t.Errorf("k did not walk back one row: %d", afterK.RoleTreeCursor())
	}
	// Navigation never touches the query.
	if got := afterK.Query(); got != "" {
		t.Errorf("j/k navigation changed the query to %q", got)
	}

	// Yield: with search focus engaged, the same two runes are the query.
	focused := m.FocusSearch().SetPaneFocus(PaneRoles)
	typed, cmd := focused.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if cmd != nil {
		t.Error("typing j with search focused emitted a command")
	}
	if typed.Query() != "j" {
		t.Errorf("search-focused j left the query %q, want \"j\"", typed.Query())
	}
	typed, _ = typed.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if typed.Query() != "jk" {
		t.Errorf("search-focused jk left the query %q, want \"jk\"", typed.Query())
	}
}

// TestRolesTreeModifiersNeverMutateTheSearchQuery is the other half, and it is
// necessary because the test above only proves the runes were not consumed by a
// feature: a key that both filters AND acts would still pass it.
func TestRolesTreeModifiersNeverMutateTheSearchQuery(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("e"), Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("k"), Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("j"), Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("d"), Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true},
		{Type: tea.KeyUp, Alt: true},
		{Type: tea.KeyDown, Alt: true},
	} {
		m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
		updated, _ := m.UpdateModel(key)
		if got := updated.Query(); got != "" {
			t.Errorf("%s changed the search query to %q", key.String(), got)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func altRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

// stripTreeRow returns the row of a rendered pane that contains needle, with its
// leading cursor/indent whitespace removed, so two rows can be compared for
// content alignment.
func stripTreeRow(t *testing.T, pane, needle string) string {
	t.Helper()
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimLeft(line, " ▸▼▶")
		}
	}
	t.Fatalf("no row containing %q in:\n%s", needle, pane)
	return ""
}
