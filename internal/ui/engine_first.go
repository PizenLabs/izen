package ui

// ── Engine-first helpers ────────────────────────────────────────────────────
//
// The engine-first strategy LAYER lives in the runtime now: the unified
// IntentGateway (internal/execution) runs Strategy.Select unconditionally on
// every user action and produces an ExecuteRequest. The UI no longer owns
// strategy selection, model routing, or execution-path decisions — the helpers
// below are the small presentation-side labels and transient-state cleanup the
// renderer still needs.

// clearEngineFirstMutationState releases the transient strategy state of a
// targeted mutation at its terminal message: the adaptive output budget and
// the operation branding. The retained ExecutionStrategyProfile in
// m.lastExecutionStrategy survives for $inspect.
func (m *model) clearEngineFirstMutationState() {
	m.activeStrategyBudget = 0
	m.hotfixBranding = ""
}
