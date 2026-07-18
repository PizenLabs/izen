package ui

// Action is a capability exposed by the current workflow. It is pure data:
// the renderer projects it without needing to know anything about modes,
// engine internals, or handoffs. Actions are produced by a mode's
// BuildWorkspace (see workspace.go), never by the renderer.
//
// Every field required for rendering and activation lives here, so the
// renderer stays a dumb projection.
type Action struct {
	ID       string // stable identifier, e.g. "investigate-root-cause"
	Label    string // human-readable, e.g. "Investigate Root Cause"
	Shortcut string // activation hotkey, e.g. "alt+a" (alt+ only, see MODIFIER SAFETY)
	Command  string // command executed on activation, e.g. "/mode investigate"
	Query    string // optional seed content passed with the command
	Enabled  bool   // whether the action can currently be activated
	Priority int    // ordering weight; higher renders first / more prominent
}

// Result is the outcome of a workflow step. Per the capability architecture,
// every workflow result exposes the actions it makes available; the engine
// never pushes UI flags. The renderer only ever sees the actions a result
// exposes. This is the single bridge:
//
//	Engine → Workflow Result → Capabilities → Renderer
type Result struct {
	Actions []Action
}

// failureResult builds the result exposed when a failure is the current
// subject of the view: a single capability to investigate its root cause.
func failureResult(payload string) *Result {
	return &Result{Actions: []Action{{
		ID:       "investigate-root-cause",
		Label:    "Investigate Root Cause",
		Shortcut: "alt+a",
		Command:  "/mode investigate",
		Query:    "Investigate root cause of the following failure:\n\n" + payload,
		Enabled:  true,
		Priority: 100,
	}}}
}

// buildVerifyResult builds the result exposed after a post-build verification:
// commit on success, rollback on failure.
func buildVerifyResult(passed bool) *Result {
	if passed {
		return &Result{Actions: []Action{{
			ID:       "commit-safe-baseline",
			Label:    "Commit Safe Baseline",
			Shortcut: "alt+d",
			Command:  "/commit",
			Enabled:  true,
			Priority: 100,
		}}}
	}
	return &Result{Actions: []Action{{
		ID:       "rollback-workspace",
		Label:    "Rollback Workspace",
		Shortcut: "alt+r",
		Command:  "/undo",
		Enabled:  true,
		Priority: 100,
	}}}
}

// investigateResultActions builds the persistent navigation controls exposed
// when an /investigate run completes. Both chips are always present so the user
// is never stranded on a dead viewport: "📋 Plan Solution" submits /plan against
// the structured diagnostic payload passed from /investigate, and
// "🔄 Re-investigate" re-runs /investigate. Per the mode-boundary law, /plan is
// a pure deterministic translation of the diagnostic data — it performs no
// semantic scanning of its own.
func investigateResultActions() *Result {
	return &Result{Actions: []Action{
		{
			ID:       "plan-solution",
			Label:    "📋 Plan Solution",
			Shortcut: "alt+p",
			Command:  "/mode plan",
			Enabled:  true,
			Priority: 100,
		},
		{
			ID:       "re-investigate",
			Label:    "🔄 Re-investigate",
			Shortcut: "alt+i",
			Command:  "/mode investigate",
			Enabled:  true,
			Priority: 90,
		},
	}}
}

// currentResultActions returns the capabilities exposed by the current workflow
// result. Returns nil when no result is active for the current view.
func (m *model) currentResultActions() []Action {
	if m.currentResult == nil {
		return nil
	}
	return m.currentResult.Actions
}
