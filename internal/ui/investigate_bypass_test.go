package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
)

// TestSetModeClearsStaleWidgets verifies that a mode handoff resets the overlay
// widgets owned by the previous mode (build approval dock, Effort selector,
// pending proposal cards) so they never bleed into the incoming mode's view.
func TestSetModeClearsStaleWidgets(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.state = StateAwaitingApproval
	m.currentEffort = EffortHigh
	m.pendingBuildApproval = true

	m.setMode(modes.ModePlan)

	if m.state != StateChat {
		t.Errorf("m.state = %v, want StateChat after mode transition", m.state)
	}
	if len(m.pendingProposals) != 0 {
		t.Errorf("pendingProposals = %d items, want 0 (stale proposal cards must be cleared)", len(m.pendingProposals))
	}
	if m.currentEffort != EffortAuto {
		t.Errorf("m.currentEffort = %v, want EffortAuto after mode transition", m.currentEffort)
	}
	if m.pendingBuildApproval {
		t.Error("pendingBuildApproval must be false after mode transition")
	}
}

// TestIntentBasedInvestigateBypass_FrontendUI verifies that when the model is
// in ModeInvestigate and the user input classifies as FRONTEND_UI, the handler
// routes to ModePlan instead of running the investigate engine. setMode returns
// nil for synchronous mode switches — the mode change has already been applied
// and handoff buffers are serialized to the session ledger by setMode.
func TestIntentBasedInvestigateBypass_FrontendUI(t *testing.T) {
	m := newTestModel()
	m.resolver = modes.NewResolver()
	m.resolver.Set(modes.ModeInvestigate)
	m.handoffLedgerContent = "ledger content"
	m.handoffCtx.LastFailurePayload = "failure"

	content := "rewrite a personal profile website with a new layout and color scheme"
	_ = m.handleMessageContent(content) // nil cmd is expected for sync mode switches
	if m.resolver.Current() != modes.ModePlan {
		t.Errorf("resolver.Current() = %v, want ModePlan (FRONTEND_UI should route to /plan)", m.resolver.Current())
	}
}

// TestIntentBasedInvestigateBypass_Mutation verifies that when the model is in
// ModeInvestigate and the user input has code mutation intent, the handler
// routes to ModeBuild with synthesized pending TODOs.
func TestIntentBasedInvestigateBypass_Mutation(t *testing.T) {
	m := newTestModel()
	m.resolver = modes.NewResolver()
	m.resolver.Set(modes.ModeInvestigate)
	m.handoffLedgerContent = "ledger content"
	m.handoffCtx.LastFailurePayload = "failure"

	content := "add a new function to calculate fibonacci numbers in internal/math/fib.go"
	_ = m.handleMessageContent(content)
	if m.resolver.Current() != modes.ModeBuild {
		t.Errorf("resolver.Current() = %v, want ModeBuild (mutation intent should route to /build)", m.resolver.Current())
	}
	if len(m.handoffCtx.PendingTodos) == 0 {
		t.Error("handoffCtx.PendingTodos is empty, expected synthesized TODOs from mutation intent")
	}
}

// TestIntentBasedInvestigateBypass_Debugging verifies that a genuine debugging
// intent does NOT trigger the bypass and continues to /investigate as normal.
// The content includes a diagnostic signal ("why is") to suppress mutation detection.
func TestIntentBasedInvestigateBypass_Debugging(t *testing.T) {
	m := newTestModel()
	m.resolver = modes.NewResolver()
	m.resolver.Set(modes.ModeInvestigate)
	m.handoffLedgerContent = "ledger content"
	m.handoffCtx.LastFailurePayload = "failure"
	invocationsBefore := m.investigateInvocationCount

	content := "cmd/api/main.go:7: undefined: Router — why is this build failing after adding a new route"
	cmd := m.handleMessageContent(content)
	if cmd == nil {
		t.Fatal("handleMessageContent returned nil for debugging intent, expected runInvestigateCmd")
	}
	if m.resolver.Current() != modes.ModeInvestigate {
		t.Errorf("resolver.Current() = %v, want ModeInvestigate (debugging intent should not bypass)", m.resolver.Current())
	}
	if m.investigateInvocationCount != invocationsBefore+1 {
		t.Errorf("investigateInvocationCount = %d, want %d (should have incremented for investigate run)", m.investigateInvocationCount, invocationsBefore+1)
	}
}
