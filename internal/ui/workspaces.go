package ui

import (
	"fmt"
	"strings"
)

// The builders below are registered explicitly into a Registry at application
// bootstrap (see program.go). No init()-based self-registration: wiring is
// deterministic and lives in one place.

// ── /ask ───────────────────────────────────────────────────────────────────
// Read-only mode: no handoff capabilities are exposed.
type askView struct{}

func (askView) BuildWorkspace(m *model) Workspace {
	ws := m.assembleScreen(nil)
	ws.Header = "ask · explain, inspect, understand"
	return ws
}

// ── /plan ──────────────────────────────────────────────────────────────────
type planView struct{}

func (planView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if len(m.handoffCtx.PendingTodos) > 0 {
		var todoBlock strings.Builder
		todoBlock.WriteString("Execute the planned changes with these TODOs:\n")
		for _, t := range m.handoffCtx.PendingTodos {
			fmt.Fprintf(&todoBlock, "  - %s\n", t)
		}
		actions = append(actions, Action{
			ID:       "execute-patch",
			Label:    "Execute & Verify Patch",
			Shortcut: "alt+c",
			Command:  "/mode build",
			Query:    todoBlock.String(),
			Enabled:  true,
			Priority: 100,
		})
	} else if len(m.currentResultActions()) > 0 {
		// Fallback: when no plan was staged (zero tasks / error), surface the
		// baseline Action Chips from the current workflow result so the user
		// is never left with a dead viewport and no buttons.
		actions = append(actions, m.currentResultActions()...)
	}
	ws := m.assembleScreen(actions)
	ws.Header = "plan · break down, structure, design"
	return ws
}

// ── /build ─────────────────────────────────────────────────────────────────
type buildView struct{}

func (buildView) BuildWorkspace(m *model) Workspace {
	// Build exposes the post-verification capability (commit / rollback) from
	// the current workflow result.
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "build · implement, refactor, elevate"
	return ws
}

// ── /investigate ───────────────────────────────────────────────────────────
type investigateView struct{}

func (investigateView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if m.handoffCtx.ProposedFix != "" {
		actions = append(actions, Action{
			ID:       "formulate-plan",
			Label:    "Formulate Execution Plan",
			Shortcut: "alt+b",
			Command:  "/mode plan",
			Query:    "Formulate an execution plan for the proposed fix:\n\n" + m.handoffCtx.ProposedFix,
			Enabled:  true,
			Priority: 100,
		})
	}
	ws := m.assembleScreen(actions)
	ws.Header = "investigate · debug, trace, root-cause"
	return ws
}

// ── /review ────────────────────────────────────────────────────────────────
type reviewView struct{}

func (reviewView) BuildWorkspace(m *model) Workspace {
	// Review exposes the failure-investigation capability only from the
	// current workflow result (e.g. a $test that just failed in review) — not
	// a payload carried over from an earlier session.
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "review · analyze, critique, improve"
	return ws
}
