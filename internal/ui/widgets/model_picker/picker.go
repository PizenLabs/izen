package model_picker

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// picker.go — WHO OWNS THE KEYBOARD, AND WHAT THE ROLES TREE DOES WITH IT.
//
// # THE ROUTING PROBLEM THIS FILE EXISTS TO SOLVE
//
// The Model Registry is three surfaces over one keymap: a fuzzy SEARCH FIELD,
// a left tree of roles, and a right list of models. The hard requirement is that
// the search field keeps every printable rune. So the rule is not "which feature
// wants this key" but "WHICH SURFACE HAS FOCUS, and what is non-printable":
//
//	printable rune      → ALWAYS the search filter, on every surface
//	arrow / Enter / Tab → the focused surface's navigation
//	Alt+<letter>        → a feature, on every surface
//
// A modifier is not a printable rune in any terminal's default mode, which is
// the entire reason every feature on this widget is on Alt: a binding that can
// be reached without a modifier is a binding that eats the user's search text,
// silently, while appearing to do nothing at all.
//
// # WHY Alt+F TARGETS THE LEFT PANE'S ROLE, NOT THE FOCUSED PANE'S ROLE
//
// The chain being edited is the ROLE's, and the role lives in the left tree.
// So the target is read from the tree's highlight, not from which pane the
// keyboard is in. Alt+F while the MODELS pane is focused means "append this
// model to the chain of the role I selected on the left", and that is the only
// reading that matches what the user is looking at: a cursor in a model list and
// a highlighted role three inches to its left. Tying the target to the
// keyboard's pane would make the same keypress edit the plan chain or the
// default chain depending on invisible state.

// moveActiveCursor moves the highlight of whichever pane is focused, and only
// then — a left-pane and a right-pane cursor that move together would mean the
// user could not hold a position in the models list while selecting a role.
func (m Model) moveActiveCursor(delta int) Model {
	switch m.paneFocus {
	case PaneProviders:
		m.moveProviderCursor(delta)
	case PaneRoles:
		m.moveRoleCursor(delta)
	default:
		m.moveCursor(delta)
	}
	return m
}

// ── FALLBACK CHAIN STRUCTURE OPERATIONS ───────────────────────────────────────

// reorderHighlightedFallback moves the highlighted fallback hop up (-1) or down
// (+1) in its role's chain.
//
// It acts ONLY on a highlighted fallback CHILD. A parent row and the Primary
// row are both refused, and refused loudly:
//
//   - Reordering the Primary is meaningless. It is hop 0 by definition; the
//     thing a user pressing Alt+K on a Primary row wants is to promote the
//     first fallback into the primary slot, which is a different operation with
//     a different consequence (it changes what runs first), and guessing at it
//     here would silently do the wrong thing.
//   - Reordering a parent row is a category error. A role has one chain.
//
// The message says which of those the user hit, because a keypress that does
// nothing and says nothing is indistinguishable from a broken key.
func (m Model) reorderHighlightedFallback(delta int) (Model, tea.Cmd) {
	node, ok := m.HighlightedRoleNode()
	if !ok {
		m.status = "No role selected"
		return m, nil
	}
	if node.Kind != RoleNodeFallback {
		m.status = reorderRefusal(node)
		return m, nil
	}
	chain := m.FallbackChain(node.Role)
	from := node.Index
	to := from + delta
	if to < 0 || to >= len(chain) {
		m.status = fmt.Sprintf("%s is already hop 1 of %d in the %s chain", chain[from], from+1, node.Role)
		return m, nil
	}
	chain = append([]string(nil), chain...)
	chain[from], chain[to] = chain[to], chain[from]
	m.setFallbackChain(node.Role, chain)
	m.fallbackRole = node.Role
	m.status = fmt.Sprintf("Moved %s to hop %d of the %s chain — Enter to save, Esc to discard",
		chain[to], to+1, node.Role)
	// The highlight follows the MODEL rather than the index, so the user's eye
	// stays on the thing they moved instead of on whatever slid into the row
	// the cursor used to be in.
	m.focusNode(node)
	return m, nil
}

// removeHighlightedFallback drops the highlighted fallback hop from its role's
// chain.
//
// The survivors keep their relative order, because the order IS the content of
// a chain. Removing [a b c]'s head yields [b c] and never [c a b]: a delete
// that reshuffled would make the remaining priorities a function of the edit
// history rather than of what the user built.
func (m Model) removeHighlightedFallback() (Model, tea.Cmd) {
	node, ok := m.HighlightedRoleNode()
	if !ok {
		m.status = "No role selected"
		return m, nil
	}
	if node.Kind != RoleNodeFallback {
		m.status = removeRefusal(node)
		return m, nil
	}
	chain := m.FallbackChain(node.Role)
	if node.Index < 0 || node.Index >= len(chain) {
		m.status = fmt.Sprintf("Hop %d is no longer in the %s chain", node.Index+1, node.Role)
		return m, nil
	}
	removed := chain[node.Index]
	next := append(append([]string(nil), chain[:node.Index]...), chain[node.Index+1:]...)
	m.setFallbackChain(node.Role, next)
	m.fallbackRole = node.Role
	m.status = fmt.Sprintf("Removed %s (hop %d) from the %s chain — Enter to save, Esc to discard",
		removed, node.Index+1, node.Role)
	// The deleted row is gone, so focusNode falls back to the role's Primary
	// row. Leaving the cursor at its old index would silently highlight the
	// NEXT role's first hop, and a second Alt+D would then delete something the
	// user never selected.
	m.focusNode(node)
	return m, nil
}

// focusRolePrimary moves the tree highlight onto a role's [1] Primary row.
func (m *Model) focusRolePrimary(roleKey string) {
	for i, n := range m.roleTree() {
		if n.Kind == RoleNodePrimary && n.Role == roleKey {
			m.roleCursor = i
			return
		}
	}
	m.clampRoleCursor()
}

// focusNode re-attaches the tree highlight to a NODE rather than to the index
// the node used to occupy.
//
// This is the difference between a tree that follows the user's attention and
// one that silently retargets. A cursor is an integer, and every structural
// edit renumbers the rows: remove the hop directly ABOVE the highlight and
// index N now holds the NEXT role's first hop, so the following Alt+D deletes
// something the user never selected. Because rows carry their own identity, the
// highlight is resolved from that identity instead of from an arithmetic
// relationship that no longer holds.
//
// When the node is genuinely gone — it was the one that was just removed — the
// highlight lands on the role's Primary row: the nearest surviving thing a user
// can act on, and never a DIFFERENT role's row.
func (m *Model) focusNode(node RoleNode) {
	if node.Role == "" {
		m.clampRoleCursor()
		return
	}
	for i, n := range m.roleTree() {
		if n.Role != node.Role || n.Kind != node.Kind {
			continue
		}
		if node.Kind == RoleNodeFallback && !strings.EqualFold(n.Model, node.Model) {
			continue
		}
		m.roleCursor = i
		return
	}
	m.focusRolePrimary(node.Role)
}

// reorderRefusal and removeRefusal name the row the user actually pressed on.
// "Nothing happened" is the answer a user files as a bug report; "Alt+K does
// not apply to a Primary row" is the answer they can act on.
func reorderRefusal(node RoleNode) string {
	if node.Kind == RoleNodePrimary {
		return "Alt+K/Alt+J reorder fallback hops — the Primary row is hop 0 and is not in the chain"
	}
	return "Alt+K/Alt+J reorder a [n] Fallback row — expand the role and select one"
}

func removeRefusal(node RoleNode) string {
	if node.Kind == RoleNodePrimary {
		return "Alt+D removes a [n] Fallback row — it never removes the Primary"
	}
	return "Alt+D removes a [n] Fallback row — expand the role and select one"
}
