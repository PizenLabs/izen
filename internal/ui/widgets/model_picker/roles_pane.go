package model_picker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// roles_pane.go — THE ROLES PANE IS A TREE, BECAUSE A ROLE IS A TREE.
//
// # WHY A FLAT LIST WAS THE WRONG SHAPE
//
// A role is not one thing. It is a primary model and an ORDERED chain of
// fallbacks, and the order is load-bearing: chain [a b c] means "try a, then b,
// then c", which is a different configuration from [c b a]. A flat list of role
// names can show that a role exists; it cannot show which model is primary,
// where in the priority order any given fallback sits, or — the failure that
// actually cost users their time — WHICH HOP A KEYPRESS IS ABOUT TO DELETE. "
// Remove the highlighted fallback" is a well-defined operation on a list and an
// undefined one on a name.
//
// So the pane is flattened for INPUT and kept as a hierarchy for DISPLAY: every
// role contributes a parent row, and an expanded role contributes its [1]
// Primary child followed by one numbered child per fallback hop. Navigation
// walks that flat sequence, which is what a terminal list actually is, and the
// row knows its parent, which is what makes delete and reorder unambiguous.

// RoleNodeKind classifies one row of the flattened ROLES tree.
type RoleNodeKind int

const (
	// RoleNodeParent is a role row: the thing that is expanded or collapsed.
	RoleNodeParent RoleNodeKind = iota
	// RoleNodePrimary is a role's [1] Primary child row.
	RoleNodePrimary
	// RoleNodeFallback is one numbered fallback hop of a role's chain.
	RoleNodeFallback
)

// String names a row kind. It exists because a row kind is printed in status
// messages and test failures, and an integer in a user's status line ("Alt+D
// does not apply to row kind 1") is a worse message than a word.
func (k RoleNodeKind) String() string {
	switch k {
	case RoleNodeParent:
		return "parent"
	case RoleNodePrimary:
		return "primary"
	case RoleNodeFallback:
		return "fallback"
	default:
		return "unknown"
	}
}

// RoleNode is one row of the flattened tree. It carries enough context to
// answer every question a keypress has to ask ("which role?", "which hop?")
// without consulting any other state, so navigation, reordering, deletion and
// rendering cannot drift apart.
type RoleNode struct {
	// Kind is the row's classification.
	Kind RoleNodeKind
	// Role is the role key this row belongs to, on EVERY row kind. A child
	// node's role is its parent's role, which is what makes "delete the
	// highlighted fallback" name exactly one role.
	Role string
	// Label is the role's display label ("Plan / Thinking").
	Label string
	// Model is the row's model, "" for a parent row.
	Model string
	// Index is the fallback hop position (0-based) for RoleNodeFallback, and
	// -1 otherwise. It is the index the reorder and delete operations use, so
	// the number the user SEES ([3] Fallback) is the number the code uses.
	Index int
	// Expanded reports whether a parent row's children are visible.
	Expanded bool
}

// roleNodeFallback is the sentinel Index for every non-fallback row, chosen so a
// caller that forgets to set Index gets a value that is out of range for every
// real chain rather than a plausible-looking 0 that would delete hop 1.
const roleNodeFallback = -1

// roleOverrideEntries is the closed set of top-level role policy overrides. It
// is the root level of the tree; the children are derived from the staged state.
var roleOverrideEntries = []struct{ Key, Label string }{
	{RoleOverridePlan, "Plan / Thinking"},
	{RoleOverrideCommit, "Commit / Fast"},
}

// subPrefixW reserves the leading indent for roles-pane child rows.
const subPrefixW = 2

// roleTree flattens the ROLES hierarchy into the row sequence that navigation
// walks and rendering draws.
//
// The order is the only order that can be: parents in configuration order, each
// immediately followed by its own children. A depth-first pre-order walk is what
// makes ↓ from a parent land on its first child and ↓ from the last child of one
// role land on the next role's parent, with no special cases in the key handler.
func (m Model) roleTree() []RoleNode {
	nodes := make([]RoleNode, 0, len(roleOverrideEntries)*4)
	for _, ro := range roleOverrideEntries {
		chain := m.FallbackChain(ro.Key)
		expanded := m.roleExpanded(ro.Key)
		nodes = append(nodes, RoleNode{
			Kind:     RoleNodeParent,
			Role:     ro.Key,
			Label:    ro.Label,
			Index:    roleNodeFallback,
			Expanded: expanded,
		})
		if !expanded {
			continue
		}
		nodes = append(nodes, RoleNode{
			Kind:  RoleNodePrimary,
			Role:  ro.Key,
			Label: ro.Label,
			Model: m.rolePrimary(ro.Key),
			Index: roleNodeFallback,
		})
		for i, entry := range chain {
			nodes = append(nodes, RoleNode{
				Kind:  RoleNodeFallback,
				Role:  ro.Key,
				Label: ro.Label,
				Model: entry,
				Index: i,
			})
		}
	}
	return nodes
}

// rolePrimary resolves a role's declared primary model for the tree.
//
// It reads the role POLICY override (the binding the Enter key writes, and the
// one authority.ModelPolicy.Thinking/Fast are driven from) and falls back to the
// role badge map. It never falls back to the currently highlighted model: a tree
// that rendered whatever the cursor happened to be on as a role's primary would
// show a configuration the user has not written.
func (m Model) rolePrimary(key string) string {
	if ob, ok := m.policyOverrides[key]; ok && ob.ModelID != "" {
		return ob.ModelID
	}
	return strings.TrimSpace(m.roles[key])
}

// RoleTree returns the flattened ROLES rows. It is exported for tests and for
// a parent that needs to name the highlighted row without re-deriving the walk.
func (m Model) RoleTree() []RoleNode {
	return m.roleTree()
}

// RoleTreeCursor returns the index of the highlighted row within RoleTree.
func (m Model) RoleTreeCursor() int { return m.roleCursor }

// SetRoleTreeCursor jumps to a row index, clamped to the tree's bounds.
//
// Clamping is a no-op on an empty tree (a picker with no roles still has to
// render, and a cursor of 0 into a zero-length slice is the only non-negative
// answer).
func (m Model) SetRoleTreeCursor(i int) Model {
	nodes := m.roleTree()
	if len(nodes) == 0 {
		m.roleCursor = 0
		return m
	}
	if i < 0 {
		i = 0
	}
	if i >= len(nodes) {
		i = len(nodes) - 1
	}
	m.roleCursor = i
	return m
}

// RoleCursor returns the highlight index within the ROLES tree. It is the
// spec-named alias of RoleTreeCursor.
func (m Model) RoleCursor() int { return m.roleCursor }

// SetRoleCursor jumps to a ROLES tree row index, clamped to bounds.
func (m Model) SetRoleCursor(i int) Model { return m.SetRoleTreeCursor(i) }

// moveRoleCursor shifts the ROLES tree highlight, clamped to bounds.
func (m *Model) moveRoleCursor(delta int) {
	m.roleCursor += delta
	m.clampRoleCursor()
}

// clampRoleCursor pulls the highlight back inside the tree after a structural
// change. A cursor is an INDEX, and an index that survives the thing it indexes
// is how a TUI ends up highlighting a different role than the one the user is
// looking at: delete hop 3 of a 3-hop chain and row 3 is now the NEXT role's
// parent, which is a silent retarget, not a visible bug.
func (m *Model) clampRoleCursor() {
	nodes := m.roleTree()
	if len(nodes) == 0 {
		m.roleCursor = 0
		return
	}
	if m.roleCursor < 0 {
		m.roleCursor = 0
	}
	if m.roleCursor >= len(nodes) {
		m.roleCursor = len(nodes) - 1
	}
}

// HighlightedRoleNode returns the highlighted tree row, and whether one exists.
func (m Model) HighlightedRoleNode() (RoleNode, bool) {
	nodes := m.roleTree()
	if m.roleCursor < 0 || m.roleCursor >= len(nodes) {
		return RoleNode{}, false
	}
	return nodes[m.roleCursor], true
}

// HighlightedRole returns the role key of the highlighted tree ROW.
//
// Every row knows its role, so a cursor parked on a fallback child resolves to
// that fallback's role rather than to something derived from focus state. That
// is what makes the cross-pane operations (Alt+F appends to "the active Role
// selected on the left") mean one specific role no matter which of the tree's
// rows the user stopped on.
func (m Model) HighlightedRole() string {
	if node, ok := m.HighlightedRoleNode(); ok && node.Role != "" {
		return node.Role
	}
	return RoleOverridePlan
}

// roleNodeSelectedRole reports the role a ROLES-pane keypress applies to. It is
// HighlightedRole guarded by "the roles pane is actually up", so a key handled
// on the models surface can never resolve to a role the user cannot see
// highlighted on the left.
func (m Model) roleNodeSelectedRole() (string, bool) {
	if !m.showingRoles {
		return "", false
	}
	if node, ok := m.HighlightedRoleNode(); ok && node.Role != "" {
		return node.Role, true
	}
	return "", false
}

// roleExpanded reports whether a role's children are visible. Roles are
// EXPANDED by default, and that default is the whole reason the tree can be
// used at a glance: a collapsed-by-default tree renders as the flat list it
// replaced, and the user discovers the structure by pressing a key nothing on
// screen advertises.
func (m Model) roleExpanded(key string) bool {
	return !m.roleCollapsed[key]
}

// expandRoleNode expands the highlighted role (no-op on a child row, and
// harmless on an already-expanded parent). It backs the Right arrow.
func (m *Model) expandRoleNode() {
	node, found := m.HighlightedRoleNode()
	if !found || node.Kind != RoleNodeParent {
		return
	}
	if m.roleCollapsed == nil {
		m.roleCollapsed = make(map[string]bool)
	}
	delete(m.roleCollapsed, node.Role)
	m.clampRoleCursor()
}

// collapseRoleNode collapses the highlighted role, or moves the highlight from a
// child row to its parent when the row is already the parent. It backs the Left
// arrow, and the two-outcome behavior is the standard tree idiom: Left on a
// child means "go to my parent", and Left on a parent means "collapse me".
func (m *Model) collapseRoleNode() {
	node, found := m.HighlightedRoleNode()
	if !found {
		return
	}
	if node.Kind == RoleNodeParent {
		if m.roleCollapsed == nil {
			m.roleCollapsed = make(map[string]bool)
		}
		m.roleCollapsed[node.Role] = true
		m.clampRoleCursor()
		return
	}
	// Child row: walk to the parent, and collapse it only when the highlight
	// was already the FIRST child, so ↓↓↑↑ does not surprise the user by
	// folding a role they just opened.
	for i, n := range m.roleTree() {
		if n.Kind == RoleNodeParent && n.Role == node.Role {
			m.roleCursor = i
			return
		}
	}
}

// ── RENDERING ─────────────────────────────────────────────────────────────────

// renderRolesPane draws the ROLES tree into the left pane.
//
// The output is EXACTLY paneH lines, like the providers pane beside it, because
// the two are joined horizontally and a pane one row taller drags the modal's
// horizontal rule out of alignment for the rest of the session. That is why the
// tree is windowed rather than truncated: when the visible roles and their hops
// exceed the pane, the highlight is followed and the overflow is simply below
// the fold.
func (m Model) renderRolesPane() string {
	paneW := rolesPaneWidth
	paneH := m.paneHeight()

	lines := make([]string, 0, paneH)
	lines = append(lines, mutedStyle.Render("ROLES"))
	lines = append(lines, "")

	budget := paneH - 2
	if budget < 0 {
		budget = 0
	}

	nodes := m.roleTree()
	start, end := m.visibleRoleWindow(len(nodes), budget)
	for i := start; i < end && len(lines) < paneH; i++ {
		lines = append(lines, m.renderRoleNode(nodes[i], i == m.roleCursor && m.paneFocus == PaneRoles, paneW))
	}

	for len(lines) < paneH {
		lines = append(lines, strings.Repeat(" ", paneW))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// visibleRoleWindow returns the slice of tree rows to draw for a pane budget,
// keeping the highlight inside it.
//
// The window is one row larger than the budget when the tree overflows, so that
// a user pressing ↓ past the last visible row sees the row they just moved to
// rather than a highlight that has walked off the bottom of the pane. Without
// the slack, ↓↓↓ past the fold produces a pane where nothing is highlighted at
// all, which reads as "the key is broken".
func (m Model) visibleRoleWindow(total, budget int) (start, end int) {
	if total <= 0 || budget <= 0 {
		return 0, 0
	}
	if total <= budget {
		return 0, total
	}
	start = m.roleCursor - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > total {
		start = total - budget
	}
	if start < 0 {
		start = 0
	}
	return start, min(start+budget, total)
}

// renderRoleNode draws one tree row.
//
// The four registers are deliberately distinct, because the question this pane
// exists to answer is "which row is the keypress about to act on", and a parent
// that looked like a child would make Alt+D ambiguous at exactly the moment it
// does damage:
//
//	parent  mauve, bold, ▶/▼            — the role, a container
//	child   teal, indented, [n] marker  — a model, an element
//	focused Surface1 background, bold, ▸ — and the label says which kind
//
// The focused label differs too ("Primary" vs "Fallback"), so the focus is
// legible without colour at all — a terminal with no colour support, or a user
// who cannot use the mauve/teal distinction, still gets a distinct row.
//
// The ▸ cursor occupies the SAME two columns the child indent does, so
// highlighting a row moves nothing. A cursor that shifts a row's contents two
// cells is a cursor under which a model slug changes position as the highlight
// moves, and a list whose text slides under the highlight is a list users
// misread.
func (m Model) renderRoleNode(node RoleNode, focused bool, paneW int) string {
	var cell string
	switch node.Kind {
	case RoleNodeParent:
		marker := "▶"
		if node.Expanded {
			marker = "▼"
		}
		cell = fmt.Sprintf("%s %s", marker, node.Label)
		if focused {
			cell = roleCursorPrefix + cell
		}
	case RoleNodePrimary:
		cell = strings.Repeat(" ", subPrefixW) + roleNodePrefix(1, "Primary") + fallbackNodeModel(node)
		if focused {
			cell = roleCursorPrefix + cell[subPrefixW:]
		}
	case RoleNodeFallback:
		cell = strings.Repeat(" ", subPrefixW) + roleNodePrefix(node.Index+2, "Fallback") + fallbackNodeModel(node)
		if focused {
			cell = roleCursorPrefix + cell[subPrefixW:]
		}
	}

	raw := runewidth.Truncate(cell, paneW, "…")
	raw = padRightExact(raw, paneW)
	if focused {
		if node.Kind == RoleNodeParent {
			return roleParentFocusStyle.Render(raw)
		}
		return roleChildFocusStyle.Render(raw)
	}
	if node.Kind == RoleNodeParent {
		return roleParentStyle.Render(raw)
	}
	return roleChildStyle.Render(raw)
}

// roleCursorPrefix is the two-column focus marker. Its width equals subPrefixW
// on purpose — see renderRoleNode.
const roleCursorPrefix = "▸ "

// roleNodePrefix renders the "[n] Label " gutter of a child row.
//
// # WHY THE NUMBERING STARTS AT 2
//
// A child row's number is its POSITION IN THE PRIORITY ORDER, and the primary
// is priority 1. So hop 0 of the chain renders as "[2] Fallback": the fallback
// that takes over first, coming immediately after the primary that fails.
//
// Numbering the chain 1..n instead would put a "[1] Fallback" directly beneath
// a "[1] Primary" on adjacent rows, which reads as a duplicate entry and as an
// off-by-one — and it would make the numbers in this pane disagree with the
// numbers in the chain line and in the config. One numbering everywhere: 1 is
// the primary, 2 is the first fallback, and Alt+J is how the user changes which
// of those a row is.
func roleNodePrefix(priority int, label string) string {
	return fmt.Sprintf("[%d] %s ", priority, label)
}

// fallbackNodeModel renders a child row's model, or the explicit unbound marker
// when there is none. An empty cell is the one rendering indistinguishable from
// "this pane is broken", so the absence is spelled out.
func fallbackNodeModel(node RoleNode) string {
	if strings.TrimSpace(node.Model) == "" {
		return "—"
	}
	return node.Model
}

// roleOverrideSummary renders the bound model + reasoning effort for a role
// override key, or the unbound marker when none is set.
//
// It is the ONE-LINE form of the same fact the tree's Primary row shows, kept
// for the active-metadata line and for callers that want the role's binding
// without walking the tree.
func (m Model) roleOverrideSummary(key string) string {
	if ob, ok := m.policyOverrides[key]; ok && ob.ModelID != "" {
		s := "→ " + ob.ModelID
		if ob.Effort != "" && ob.Effort != DefaultReasoningOption {
			s += " · " + ob.Effort
		}
		return s
	}
	if model := strings.TrimSpace(m.roles[key]); model != "" {
		return "→ " + model
	}
	return "→ —"
}
