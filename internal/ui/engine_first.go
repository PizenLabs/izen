package ui

// ── Engine-first helpers ────────────────────────────────────────────────────
//
// The engine-first strategy LAYER lives in the runtime now: the unified
// IntentGateway (internal/execution) runs Strategy.Select unconditionally on
// every user action and produces an ExecuteRequest. The UI no longer owns
// strategy selection, model routing, or execution-path decisions — the helpers
// below are the small presentation-side labels and transient-state cleanup the
// renderer still needs.

// hotfixOutputBudget returns the adaptive output budget selected by the
// engine-first router for the current targeted mutation, falling back to the
// legacy fixed bound when no strategy budget is active. It is a strict no-op
// when the strategy did not select a budget.
func (m *model) hotfixOutputBudget() int {
	if m.activeStrategyBudget > 0 {
		return m.activeStrategyBudget
	}
	return 2048
}

// hotfixBrandingLabel returns the status-line label of the bounded mutation
// executor: "HOTFIX" for $hot, "PROMPT" when a $prompt routed through the
// engine-first strategy layer. It is reset to the $hot default by the
// terminal messages so a stale label never leaks across operations.
func (m *model) hotfixBrandingLabel() string {
	if m.hotfixBranding == "PROMPT" {
		return "PROMPT"
	}
	return "HOTFIX"
}

// clearEngineFirstMutationState releases the transient strategy state of a
// targeted mutation at its terminal message: the adaptive output budget and
// the operation branding. The retained ExecutionStrategyProfile in
// m.lastExecutionStrategy survives for $inspect.
func (m *model) clearEngineFirstMutationState() {
	m.activeStrategyBudget = 0
	m.hotfixBranding = ""
}
