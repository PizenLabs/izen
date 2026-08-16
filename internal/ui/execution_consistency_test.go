package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── PHASE 5 UX/EXECUTION CONSISTENCY — SINGLE EXECUTION TRUTH PIPELINE ───────
//
// Engine Event → Execution State → Presentation Projection → Renderer.
//
// These tests validate the three acceptance scenarios end-to-end through the
// model:
//
//  1. "hi" — a direct conversation: instant answer, NO spinner, NO execution
//     timeline, NO narrative panel, NO repository activity.
//  2. "$prompt inspect index.html" — an execution: the visible surface is the
//     event-derived human narrative ("Reading index.html", "Analyzing"), never
//     a "Thinking..." claim.
//  3. "$prompt remove extra content @index.html" — a mutation: the proposal
//     appears, the user approval gate holds, and NO file is touched until
//     approval.

// ── CASE 1: DIRECT CONVERSATION ──────────────────────────────────────────────

// TestConsistencyCase1_HiAnswerOnlyNoTimeline pins Case 1: a casual greeting
// is a single human action (Input → Intent → Model → Response). It must not
// create an execution timeline, a narrative panel, or any execution vocabulary —
// the human surface is the answer only. The loading dock (spinner + contextual
// tip) stays alive while the answer is generated, but its text is never an
// execution/progress claim.
func TestConsistencyCase1_HiAnswerOnlyNoTimeline(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "Hello there!", TokenOutput: 3, TokenInput: 2}},
	}, nil)
	m.state = StateChat

	cmd := m.runGatedLine("hi")
	if cmd == nil {
		t.Fatal("conversation dispatch returned nil command")
	}
	// No execution narrative projection and no dock text: nothing but the
	// answer (plus the spinner/tip surface) may reach the user.
	if m.execView != nil {
		t.Fatal("conversation must not create an execution narrative projection")
	}
	if dock := m.composeDockText(); dock != "" {
		t.Fatalf("conversation must show no execution text, got %q", dock)
	}
	if panel := m.renderExecutionLayered(); panel != "" {
		t.Fatalf("conversation must not render an execution panel: %q", panel)
	}
	// The loading dock (spinner + tips) must NOT render for a conversation:
	// PROMPT.md Test 1 requires a direct answer only — no spinner, no timeline.
	if dock := m.renderLoadingDock(); dock != "" {
		t.Fatalf("conversation must show no loading dock (no spinner), got %q", dock)
	}

	// Run the executor and project the result: the answer lands in the AI role.
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("got %T, want gatedExecutionMsg", msg)
	}
	if gem.err != nil {
		t.Fatalf("conversation execution failed: %v", gem.err)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	_ = res
	joined := recordsText(m)
	if !strings.Contains(joined, "Hello there!") {
		t.Fatalf("conversation answer missing from the surface: %q", joined)
	}
	// No internal concept, no timeline vocabulary, no fake thinking.
	for _, leak := range []string{
		"[runtime]", "Thinking", "Understanding request", "Reading ", "Analyzing",
		"execution.started", "provider.response", "artifact produced",
	} {
		if strings.Contains(joined, leak) {
			t.Errorf("conversation surface leaked %q: %q", leak, joined)
		}
	}
}

// ── CASE 2: "$prompt inspect index.html" ─────────────────────────────────────

// TestConsistencyCase2_InspectShowsEventDerivedSteps pins Case 2: the visible
// execution surface is the event-derived human narrative — "Reading index.html"
// (the runtime reads the target) and "Analyzing" (the model processes it) —
// and NEVER a fake "Thinking..." state. Every step exists only because a real
// canonical event arrived.
func TestConsistencyCase2_InspectShowsEventDerivedSteps(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m.state = StateChat

	cmd := m.runGatedLine("$prompt inspect index.html")
	if cmd == nil {
		t.Fatal("execution dispatch returned nil command")
	}
	// Before any event: nothing is rendered (no static template seed).
	if panel := m.renderExecutionLayered(); panel != "" {
		t.Fatalf("panel rendered before any runtime event: %q", panel)
	}

	// The runtime drives the canonical event stream (as the executor emits it
	// for a targeted mutation).
	rid := "case2"
	m.handleDomainEvent(events.NewExecutionStarted(rid, "build", "inspect index.html"))
	m.handleDomainEvent(events.NewStrategySelected(rid, "targeted_mutation", true, "resolved target"))
	m.handleDomainEvent(events.NewTargetResolved(rid, "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewContextPrepared(rid, []string{"user_intent", "target_content"}, 42))
	m.handleDomainEvent(events.NewModelInvoked(rid, "mock", 0, 0))
	m.handleDomainEvent(events.NewProviderResponse(rid, "mock", 5, 7))

	panel := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel, "Reading index.html") {
		t.Fatalf("execution surface missing the target-read step: %q", panel)
	}
	if !strings.Contains(panel, "Analyzing") {
		t.Fatalf("execution surface missing the analysis step: %q", panel)
	}
	// A fake thinking state must never appear, and the steps are event-derived.
	if strings.Contains(panel, "Thinking") {
		t.Fatalf("execution surface claims a fake thinking state: %q", panel)
	}
	if strings.Contains(panel, "understanding") {
		t.Fatalf("execution surface claims a fake understanding step: %q", panel)
	}

	// The narrative is a pure function of the events: a partial graph (nothing
	// beyond target resolution) yields only the steps that occurred.
	m2 := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m2.execView = presentation.NewExecutionProjection()
	m2.execView.Begin("case2b")
	m2.executionResolving = true
	m2.execVisibility = presentation.VisibilityNormal
	m2.handleDomainEvent(events.NewExecutionStarted("case2b", "build", "inspect index.html"))
	m2.handleDomainEvent(events.NewTargetResolved("case2b", "index.html", true, "strategy"))
	partial := stripANSITest(m2.renderExecutionLayered())
	if !strings.Contains(partial, "Reading index.html") {
		t.Fatalf("partial execution missing its real step: %q", partial)
	}
	if strings.Contains(partial, "Analyzing") {
		t.Fatalf("partial execution fabricated an analysis step that never occurred: %q", partial)
	}
}

// ── CASE 3: "$prompt remove extra content @index.html" ───────────────────────

// TestConsistencyCase3_MutationOnlyAfterApproval pins Case 3 end-to-end through
// the real RuntimeExecutor: the proposal appears, the approval gate holds, and
// NO file is mutated until the human approves — after approval the mutation is
// applied by the runtime.
func TestConsistencyCase3_MutationOnlyAfterApproval(t *testing.T) {
	orig := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n  <meta charset=\"utf-8\">\n  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n  <title>Demo Page</title>\n  <link rel=\"stylesheet\" href=\"styles.css\">\n</head>\n<body>\n  <header>\n    <h1>Welcome to the Demo</h1>\n  </header>\n  <main>\n    <p>This is the primary content of the demo page.</p>\n    <p>extra</p>\n  </main>\n  <footer>\n    <p>Copyright 2026</p>\n  </footer>\n</body>\n</html>\n"
	block := "<<<<<<< SEARCH\n    <p>This is the primary content of the demo page.</p>\n    <p>extra</p>\n=======\n    <p>This is the primary content of the demo page.</p>\n>>>>>>>"
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     block,
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage: ai.ProviderUsage{
			PromptTokens:     2860,
			CompletionTokens: 2048,
			TotalTokens:      4908,
			Known:            true,
		},
	}}}
	m := gatedDispatchModel(t, mock, map[string]string{"index.html": orig})
	m.state = StateChat

	cmd := m.runGatedLine("$prompt remove extra content @index.html")
	if cmd == nil {
		t.Fatal("execution dispatch returned nil command")
	}

	// ── 1. The runtime runs and stops at the approval gate ─────────────
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("got %T, want gatedExecutionMsg", msg)
	}
	if gem.err != nil {
		t.Fatalf("execution failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatalf("result must stop at the approval gate with a held patch: %+v", gem.res)
	}
	// The authoritative usage account is computed by the runtime.
	if gem.res.Completed.InputTokens != 2860 || gem.res.Completed.OutputTokens != 2048 {
		t.Fatalf("runtime usage account = %d/%d, want provider-reported 2860/2048",
			gem.res.Completed.InputTokens, gem.res.Completed.OutputTokens)
	}
	if gem.res.Completed.Provider != "mock" {
		t.Fatalf("runtime usage account provider = %q, want mock", gem.res.Completed.Provider)
	}

	// ── 2. Project the pending result: the proposal appears ────────────
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval — the approval gate must hold", m2.state)
	}
	if len(m2.pendingProposals) != 1 {
		t.Fatalf("pending proposals = %d, want 1 — the proposal must appear", len(m2.pendingProposals))
	}
	joined := recordsText(m2)
	if !strings.Contains(joined, "Proposed change to index.html") {
		t.Fatalf("proposal notice missing: %q", joined)
	}

	// ── 3. NO mutation may occur before approval ───────────────────────
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != orig {
		t.Fatalf("file mutated BEFORE approval:\n%s", onDisk)
	}

	// ── 4. Approve through the runtime: the mutation applies ───────────
	applyCmd := m2.runExecutorApproveCmd(m2.executorPendingPatchID)
	if applyCmd == nil {
		t.Fatal("approve command is nil")
	}
	applyMsg := applyCmd()
	erm, ok := applyMsg.(executionResultMsg)
	if !ok {
		t.Fatalf("approve produced %T, want executionResultMsg", applyMsg)
	}
	if erm.err != nil {
		t.Fatalf("approve failed: %v", erm.err)
	}
	res2, cmd2 := m2.executionResultUpdate(erm)
	m3 := res2.(*model)
	if m3.state == StateAwaitingApproval {
		t.Fatal("approval gate still held after a successful apply")
	}
	// Drain the dispatched commands (token usage → footer counters).
	for _, m3msg := range drainCmds(t, cmd2) {
		var r3 tea.Model
		r3, _ = m3.Update(m3msg)
		m3 = r3.(*model)
	}
	onDisk, err = os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == orig {
		t.Fatal("file was NOT mutated after approval")
	}
	if !strings.Contains(string(onDisk), "primary content of the demo page") || strings.Contains(string(onDisk), "    <p>extra</p>") {
		t.Fatalf("file content not the approved artifact:\n%s", onDisk)
	}
	// The runtime's authoritative usage account travels to the footer.
	if m3.InputTokens != 2860 || m3.OutputTokens != 2048 {
		t.Fatalf("footer tokens = %d/%d, want provider-reported 2860/2048", m3.InputTokens, m3.OutputTokens)
	}
}
