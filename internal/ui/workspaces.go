package ui

// The builders below are registered explicitly into a Registry at application
// bootstrap (see program.go). No init()-based self-registration: wiring is
// deterministic and lives in one place.

// ── /ask ───────────────────────────────────────────────────────────────────
// Read-only mode: no handoff capabilities are exposed.
type askView struct{}

func (askView) BuildWorkspace(m *model) Workspace {
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "ask · explain, inspect, understand"
	return ws
}

// ── /plan ──────────────────────────────────────────────────────────────────
type planView struct{}

func (planView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if len(m.handoffCtx.PendingTodos) > 0 {
		actions = append(actions, Action{
			ID:       "approve-plan",
			Label:    "✓ Approve & Run /build",
			Shortcut: "alt+p",
			Command:  "/build",
			Enabled:  true,
			Priority: 100,
		})
		actions = append(actions, Action{
			ID:       "reject-plan",
			Label:    "✗ Reject & Back",
			Shortcut: "alt+r",
			Command:  "/ask",
			Enabled:  true,
			Priority: 90,
		})
		actions = append(actions, Action{
			ID:       "execute-patch",
			Label:    "> Execute & Verify Patch",
			Shortcut: "alt+c",
			Command:  "/build",
			Enabled:  true,
			Priority: 80,
		})
	} else if len(m.currentResultActions()) > 0 {
		actions = append(actions, m.currentResultActions()...)
	}
	ws := m.assembleScreen(actions)
	ws.Header = "plan · architecture, migrations, refactors — strategic blueprint"
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
			Command:  "/plan",
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
