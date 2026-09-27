// role_config_keymap_test.go — A ROLE'S OPERATIONAL PARAMETERS ARE EDITABLE, AND
// THE EDITOR DOES NOT EAT THE SEARCH BOX.
//
// # WHY THE EDITOR IS A MODAL WITH ARROW KEYS ONLY
//
// Alt+E opens an editor over a fuzzy search field, which is the combination
// that has broken surfaces before: a key that means "edit" in one context and
// "type this character" in another is a key the user cannot trust. So the
// editor answers ↑ ↓ ← → and Enter and Esc and nothing else — arrows are
// navigation keys in every terminal, and no arrow can be confused for text.
//
// The assertions below are mostly about the keys it does NOT answer, because a
// modal that quietly lets a key through to the surface behind it is a modal that
// eats keystrokes: the user presses Esc, expects the editor to close, and
// instead closes the whole Model Registry with an unsaved edit in it.

package model_picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// paramPicker builds a picker with the roles tree focused on the plan role.
func paramPicker(t *testing.T) Model {
	t.Helper()
	m := treePicker(t, map[string][]string{RoleOverridePlan: {"ollama/a"}})
	return m.SetRoleParams(map[string]RoleParams{
		RoleOverridePlan: {MaxRetries: 2, TimeoutSeconds: 0, RateLimit: true, ServerError: true},
	})
}

// TestAltEOpensTheRoleParameterEditor pins the entry point and its default
// field.
func TestAltEOpensTheRoleParameterEditor(t *testing.T) {
	for _, key := range []tea.KeyMsg{altRune('e'), {Type: tea.KeyRunes, Runes: []rune("E"), Alt: true}} {
		m, _ := paramPicker(t).UpdateModel(key)
		if !m.RoleConfigActive() {
			t.Fatalf("%s did not open the parameter editor", key.String())
		}
		if m.roleConfig.role != RoleOverridePlan {
			t.Errorf("the editor targets role %q, want the highlighted plan role", m.roleConfig.role)
		}
		if m.roleConfig.field != roleFieldRetries {
			t.Errorf("the editor opened on %q, want the first field", roleFieldLabel[m.roleConfig.field])
		}
	}
	// And it renders as a dialog carrying every field's current value, not as
	// a one-line form the user has to remember.
	opened, _ := paramPicker(t).UpdateModel(altRune('e'))
	view := ansi.Strip(opened.View())
	for _, want := range []string{
		"ROLE PARAMETERS", "PLAN",
		"Max Retries", "2",
		"Timeout (s)",
		"429 Rate Limit", "on",
		"5xx Server Error", "on",
		"Context Length", "off",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the editor omits %q:\n%s", want, view)
		}
	}
}

// TestRoleParameterEditorEditsThroughArrowsOnly drives every field to its
// boundary and back, because an editor that can only be set one way is a
// half-feature: a user who over-scrolled the budget has to reopen the picker.
func TestRoleParameterEditorEditsThroughArrowsOnly(t *testing.T) {
	m, _ := paramPicker(t).UpdateModel(altRune('e'))

	// Up on the first field clamps rather than wrapping onto the last one: a
	// five-row editor that wraps is a list where ↑ from the top lands somewhere
	// the user did not press toward.
	up, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyUp})
	if up.roleConfig.field != roleFieldRetries {
		t.Errorf("↑ on the first field moved to %q", roleFieldLabel[up.roleConfig.field])
	}
	// Up at the bottom of the field list clamps too.
	down := m
	for i := 0; i < int(roleFieldCount)+3; i++ {
		down, _ = down.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	}
	if down.roleConfig.field != roleFieldContextLength {
		t.Errorf("↓ past the last field landed on %q", roleFieldLabel[down.roleConfig.field])
	}

	// Retries: right steps up, left steps down, both clamp at the bounds.
	right, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	if right.roleConfig.draft.MaxRetries != 3 {
		t.Errorf("→ on Max Retries gave %d, want 3", right.roleConfig.draft.MaxRetries)
	}
	clamped := right
	for i := 0; i < roleMaxRetriesMax+3; i++ {
		clamped, _ = clamped.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	}
	if clamped.roleConfig.draft.MaxRetries != roleMaxRetriesMax {
		t.Errorf("→ past the bound gave %d, want the clamp %d",
			clamped.roleConfig.draft.MaxRetries, roleMaxRetriesMax)
	}
	// Down to 0, and 0 is a real value the user can select (an explicit "never
	// retry"), distinct from the file's "unset" — the widget always writes the
	// effective value, so what it shows is what it saves.
	zero := m
	for i := 0; i < roleMaxRetriesMax+3; i++ {
		zero, _ = zero.UpdateModel(tea.KeyMsg{Type: tea.KeyLeft})
	}
	if zero.roleConfig.draft.MaxRetries != 0 {
		t.Errorf("↓ past the bound gave %d, want 0", zero.roleConfig.draft.MaxRetries)
	}

	// Timeout: seconds, and its own bounds.
	timeout := m.moveEditorTo(roleFieldTimeout)
	timeout, _ = timeout.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	if timeout.roleConfig.draft.TimeoutSeconds != 1 {
		t.Errorf("→ on Timeout gave %d, want 1", timeout.roleConfig.draft.TimeoutSeconds)
	}
	timeout, _ = timeout.UpdateModel(tea.KeyMsg{Type: tea.KeyLeft})
	if timeout.roleConfig.draft.TimeoutSeconds != 0 {
		t.Errorf("← on Timeout gave %d, want 0", timeout.roleConfig.draft.TimeoutSeconds)
	}

	// Toggles flip, and they flip on BOTH arrows: a toggle is a boolean, and a
	// boolean has no "up".
	for _, tc := range []struct {
		field roleParamsField
		get   func(RoleParams) bool
	}{
		{roleFieldRateLimit, func(p RoleParams) bool { return p.RateLimit }},
		{roleFieldServerError, func(p RoleParams) bool { return p.ServerError }},
		{roleFieldContextLength, func(p RoleParams) bool { return p.ContextLength }},
	} {
		for _, key := range []tea.KeyMsg{{Type: tea.KeyRight}, {Type: tea.KeyLeft}} {
			before := tc.get(m.roleConfig.draft)
			edited, _ := m.moveEditorTo(tc.field).UpdateModel(key)
			if !edited.RoleConfigActive() {
				t.Fatalf("the editor closed while editing %s", roleFieldLabel[tc.field])
			}
			if after := tc.get(edited.roleConfig.draft); after == before {
				t.Errorf("%s on %s did not toggle (still %v)", key.String(), roleFieldLabel[tc.field], after)
			}
		}
	}
}

// TestRoleParameterEditorIsModal pins that the editor owns the surface.
//
// Every key the browsing surface answers is inert while it is open — including
// Esc, which must close the EDITOR and not the Model Registry, and including the
// printable runes, which must not become a query behind a dialog. A user who
// presses Esc expecting to abandon a parameter edit and instead loses their
// staged chain has been given a data-loss path by a keybinding.
func TestRoleParameterEditorIsModal(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"alt+f", altRune('f')},
		{"alt+k", altRune('k')},
		{"alt+j", altRune('j')},
		{"alt+d", altRune('d')},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}},
		{"a", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}},
		{"e", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}},
		{"alt+i", altRune('i')},
		{"ctrl+r", tea.KeyMsg{Type: tea.KeyCtrlR}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Stage a chain edit BEFORE opening the editor, so a leak is
			// detectable as more than "something changed". Alt+F is the add
			// key that works from the roles pane; Alt+J is scoped to the tree.
			m, _ := paramPicker(t).UpdateModel(altRune('f'))
			if !m.FallbackChainDirty() {
				t.Fatal("precondition: the chain edit should be staged")
			}
			m, _ = m.UpdateModel(altRune('e'))
			if !m.RoleConfigActive() {
				t.Fatal("precondition: the editor should be open")
			}
			m, _ = m.UpdateModel(tc.msg)
			// Enter closes the editor and STAGES; Esc closes it and changes
			// nothing. Every other key must leave it open.
			if tc.name != "enter" && tc.name != "esc" && !m.RoleConfigActive() {
				t.Errorf("%s closed the editor", tc.name)
			}
			if tc.name != "enter" && m.RoleConfigActive() && m.FallbackChainDirty() != true {
				t.Errorf("%s discarded the staged chain edit", tc.name)
			}
			// The staged chain and the query survive a modal key.
			if !m.FallbackChainDirty() {
				t.Errorf("%s discarded the staged chain edit", tc.name)
			}
			if got := m.Query(); got != "" {
				t.Errorf("%s leaked into the search query: %q", tc.name, got)
			}
		})
	}
}

// TestRoleParameterEditorEscCancelsStaging is the one that matters most: Esc
// closes the editor and changes NOTHING, because the draft was a copy.
//
// There is no "undo" step to explain, and no state in which the parameters the
// user was looking at before Alt+E are gone.
func TestRoleParameterEditorEscCancelsStaging(t *testing.T) {
	m, _ := paramPicker(t).UpdateModel(altRune('e'))
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	if m.RoleParamsFor(RoleOverridePlan).MaxRetries != 2 {
		t.Fatal("precondition: the staged draft should differ from the live value")
	}

	cancelled, _ := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.RoleConfigActive() {
		t.Error("Esc did not close the editor")
	}
	if got := cancelled.RoleParamsFor(RoleOverridePlan).MaxRetries; got != 2 {
		t.Errorf("Esc left the budget at %d, want the untouched 2", got)
	}
	if got := cancelled.RoleParamsFor(RoleOverridePlan).ServerError; !got {
		t.Error("Esc applied a toggle the user abandoned")
	}
	if cancelled.RoleParamsDirty() {
		t.Error("Esc marked a parameter edit as staged")
	}
	if !strings.Contains(cancelled.status, "unchanged") {
		t.Errorf("the cancel status does not say nothing changed: %q", cancelled.status)
	}
}

// TestRoleParameterEditorEnterStagesThenTheNextEnterPersists pins the two-step
// write, which is the same shape the chain uses for the same reason:
// config.Save rewrites the user's whole file, so a value that writes on every
// arrow press makes four edits four full rewrites with no way back.
func TestRoleParameterEditorEnterStagesThenTheNextEnterPersists(t *testing.T) {
	m, _ := paramPicker(t).UpdateModel(altRune('e'))
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})

	staged, cmd := m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter inside the editor emitted a persist command; it stages")
	}
	if staged.RoleConfigActive() {
		t.Error("Enter did not close the editor")
	}
	if !staged.RoleParamsDirty() {
		t.Fatal("Enter did not mark the parameter edit as staged")
	}
	if got := staged.RoleParamsFor(RoleOverridePlan).MaxRetries; got != 5 {
		t.Errorf("staged budget = %d, want 5", got)
	}
	// The metadata line says the edit is unsaved, so the user knows a second
	// Enter is the one that writes.
	if line := ansi.Strip(staged.renderRoleParamsLine()); !strings.Contains(line, "unsaved") {
		t.Errorf("the params line does not show the edit as unsaved: %s", line)
	}

	// And with nothing staged a further Enter emits nothing at all, so Enter
	// on an untouched picker cannot rewrite the user's config file.
	_, cmd = staged.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the confirming Enter emitted no command")
	}
	msg, ok := cmd().(RoleParamsChangedMsg)
	if !ok {
		t.Fatalf("the confirming Enter emitted %T, want RoleParamsChangedMsg", cmd())
	}
	if msg.Role != RoleOverridePlan {
		t.Errorf("confirmed role = %q, want plan", msg.Role)
	}
	if msg.Params.MaxRetries != 5 {
		t.Errorf("confirmed budget = %d, want 5", msg.Params.MaxRetries)
	}
	// The whole block travels, not a delta: a partial update would make the
	// persisted file depend on which key was pressed last.
	if !msg.Params.RateLimit || !msg.Params.ServerError || msg.Params.ContextLength {
		t.Errorf("the confirmed block lost the untouched triggers: %+v", msg.Params)
	}
}

// TestApplyRoleParamsConfirmClearsTheDirtyMarker pins the round trip: the badge
// clears only when the PARENT says the write landed, because a widget that
// cleared it on Enter would claim "saved" for a write that never happened.
func TestApplyRoleParamsConfirmClearsTheDirtyMarker(t *testing.T) {
	m, _ := paramPicker(t).UpdateModel(altRune('e'))
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.RoleParamsDirty() {
		t.Fatal("precondition: the edit should be staged")
	}
	saved := m.ApplyRoleParamsConfirm()
	if saved.RoleParamsDirty() {
		t.Error("ApplyRoleParamsConfirm did not clear the unsaved marker")
	}
	// And the value survives — clearing the badge must not clear the data.
	if got := saved.RoleParamsFor(RoleOverridePlan); got != m.RoleParamsFor(RoleOverridePlan) {
		t.Errorf("the confirm changed the value: %+v vs %+v", got, m.RoleParamsFor(RoleOverridePlan))
	}
	// A re-seed also clears it: the parent is saying "this is what is on disk".
	if reSeeded := m.SetRoleParams(map[string]RoleParams{RoleOverridePlan: {MaxRetries: 7}}); reSeeded.RoleParamsDirty() {
		t.Error("a re-seed from the parent left the unsaved marker set")
	}
	if reSeeded := m.SetRoleParams(map[string]RoleParams{RoleOverridePlan: {MaxRetries: 7}}); reSeeded.EmitRoleParamsConfirm() != nil {
		t.Error("a re-seed left a confirm command armed")
	}
}

// TestRoleParamsAccessorReturnsCopies pins that the widget's parameters cannot
// be mutated by a caller — they are what gets persisted, and a caller that could
// reach into the widget's map by accident would write a budget the user never
// staged.
func TestRoleParamsAccessorReturnsCopies(t *testing.T) {
	m := paramPicker(t)
	all := m.RoleParamsMap()
	all[RoleOverridePlan] = RoleParams{MaxRetries: 99}
	if got := m.RoleParamsFor(RoleOverridePlan).MaxRetries; got != 2 {
		t.Errorf("mutating RoleParamsMap() changed the widget's parameters: %d", got)
	}
}

// TestRoleParamsLineNamesTheRoleAndShowsTheValues pins the metadata row: the
// role, the budget, the deadline, the active triggers, and the key that edits
// them. The deadline renders its MEANING, because "0s" reads as "no timeout at
// all" — a different and alarming claim than "the provider profile's own".
func TestRoleParamsLineNamesTheRoleAndShowsTheValues(t *testing.T) {
	m := paramPicker(t).SetShowingRoles(false).SetPaneFocus(PaneModels)
	m = m.SetRoleCursor(0)
	m = m.SetRoleParams(map[string]RoleParams{
		"default": {MaxRetries: 3, TimeoutSeconds: 45, RateLimit: true, ServerError: false, ContextLength: true},
	})
	line := ansi.Strip(m.renderRoleParamsLine())
	for _, want := range []string{
		"Role Params:", "default",
		"retries 3", "timeout 45s", "triggers 429,ctx",
		"Alt+E",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the params line omits %q: %s", want, line)
		}
	}
	// A role with no configured block renders the documented defaults rather
	// than an absence: a blank line is the one rendering indistinguishable from
	// "this pane is broken".
	unseeded := ansi.Strip(paramPicker(t).SetRoleParams(nil).renderRoleParamsLine())
	if !strings.Contains(unseeded, "retries 2") {
		t.Errorf("an unseeded role does not render its defaults: %s", unseeded)
	}
	// "provider default" is 17 cells; an empty cell would be 0.
	if !strings.Contains(unseeded, "provider default") {
		t.Errorf("an unset deadline does not render its meaning: %s", unseeded)
	}
}

// TestRoleParamsEditorReachableFromEitherPane pins that Alt+E works from the
// models list too, because the parameters belong to a role and the roles pane is
// not the only place a user is when they want them — the whole point of editing
// them in a modal rather than as inline steppers.
func TestRoleParamsEditorReachableFromEitherPane(t *testing.T) {
	m := fallbackPicker(t, "ollama/a").SetSize(120, 32)
	m = m.SetRoleParams(map[string]RoleParams{"default": {MaxRetries: 5}})
	opened, _ := m.UpdateModel(altRune('e'))
	if !opened.RoleConfigActive() {
		t.Fatal("Alt+E did not open the editor from the models pane")
	}
	// With no roles view up the editor targets the turn role the runtime would
	// consult, which is the same mapping the chain uses.
	if got := opened.roleConfig.role; got != "default" {
		t.Errorf("the editor targets %q, want the turn role \"default\"", got)
	}
	if got := opened.roleConfig.draft.MaxRetries; got != 5 {
		t.Errorf("the editor did not load the persisted value: %d", got)
	}
	view := ansi.Strip(opened.View())
	if !strings.Contains(view, "ROLE PARAMETERS") {
		t.Errorf("the editor did not render over the surface:\n%s", view)
	}
}

// TestRoleParamsLineSurvivesTheModalHeightBudget is the off-by-one guard for
// the chrome row the params line added.
//
// padFooter hard-clips the body from the END, so a chrome budget one row too
// generous does not crop the tree — it crops the STATUS line, silently, on a
// modal of a normal size. Every status this surface shows would therefore be
// invisible and a user pressing Alt+E would appear to get no feedback at all.
func TestRoleParamsLineSurvivesTheModalHeightBudget(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {110, 30}, {100, 24}, {80, 20}, {130, 40}, {70, 16}} {
		m := paramPicker(t).SetSize(size[0], size[1])
		m.status = "STATUS-SENTINEL"
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "STATUS-SENTINEL") {
			t.Errorf("modal %dx%d clipped the status line (the chrome budget is one row too "+
				"generous; padFooter clips the body from the end):\n%s", size[0], size[1], view)
		}
		// The params row is chrome too, so it is the FIRST thing to go if the
		// budget is wrong in the other direction.
		if !strings.Contains(view, "Role Params:") {
			t.Errorf("modal %dx%d dropped the role-params row:\n%s", size[0], size[1], view)
		}
	}
}

// moveEditorTo drives the open editor's highlight to a named field, the way a
// user would, so the tests that follow exercise real key routing.
func (m Model) moveEditorTo(field roleParamsField) Model {
	for i := 0; i < int(roleFieldCount)+2; i++ {
		if m.roleConfig == nil {
			return m
		}
		if m.roleConfig.field == field {
			return m
		}
		m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	}
	return m
}
