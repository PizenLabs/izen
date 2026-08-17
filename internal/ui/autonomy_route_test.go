package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

func autonomyTestModel() *model {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.provider = &mockProvider{responses: []*ai.Response{{Content: "ok", TokenOutput: 5}}}
	return m
}

// TestAutonomyValidationCase1ConversationDirectResponse pins Case 1: "hi" is a
// conversation that is answered directly — no workspace switch, no autonomous
// loop, no timeline, no grant, no proposal.
func TestAutonomyValidationCase1ConversationDirectResponse(t *testing.T) {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeBuild)

	cmd := m.runAutonomyRoutedCmd("hi")
	// A greeting is answered locally by interceptLocalIntent (nil cmd) or via
	// the chat stream — either way the workspace must NOT switch and no
	// execution engine may start.
	if m.investigateRunning || m.reviewRunning || m.agentRunning {
		t.Fatal("conversation must not start any execution engine")
	}
	// The workspace must NOT switch away from the current mode for a greeting.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("workspace = /%s, want /build (no switch for conversation)", got)
	}
	// No proposal and no grant for conversation.
	if m.pendingAutonomyProposal != nil {
		t.Fatal("conversation must not render an autonomy proposal")
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("conversation must not issue any capability grant")
	}
	_ = cmd
}

// TestAutonomyValidationCase2InspectRoutesToInvestigate pins Case 2:
// "$prompt inspect @index.html" selects the INVESTIGATE workspace and executes
// the evidence pipeline (no mutation capability).
func TestAutonomyValidationCase2InspectRoutesToInvestigate(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.runAutonomyRoutedCmd("inspect @index.html")
	if cmd == nil {
		t.Fatal("inspect objective must dispatch the investigate engine")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("workspace = /%s, want /investigate", got)
	}
	if !m.investigateRunning {
		t.Error("investigate engine must have started")
	}
	// Read-only intent never asks for a proposal.
	if m.pendingAutonomyProposal != nil {
		t.Fatal("read-only investigation must not render an autonomy proposal")
	}
}

// TestAutonomyValidationCase3MutationProposalThenExecutes pins Case 3:
// "$prompt read @index.html and remove extra contents" renders an autonomy
// PROPOSAL (ask_user) — never a /grant hint. Executing the proposal issues the
// capability grant internally, re-runs the decision and executes in BUILD
// without a repeated approval.
func TestAutonomyValidationCase3MutationProposalThenExecutes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte("<html><body><main><p>keep</p></main>stray text</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyTestModel()

	cmd := m.runAutonomyRoutedCmd("read @index.html and remove extra contents")
	if cmd != nil {
		t.Fatal("pre-grant mutation must return nil cmd and await the proposal")
	}
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected a pending autonomy proposal before mutation executes")
	}
	if !m.pendingAutonomyProposal.Missing.Has(autonomy.CapMutate) {
		t.Fatalf("pending proposal must require mutate, got %v", m.pendingAutonomyProposal.Missing)
	}

	// The proposal must be rendered — and it must NOT instruct "Approve with
	// /grant" (requirement 1: grant is internal).
	if !strings.Contains(recordsText(m), "AUTONOMY PROPOSAL") {
		t.Error("expected the proposal to be rendered")
	}
	if strings.Contains(recordsText(m), "/grant") {
		t.Error("proposal must never instruct the user to type /grant")
	}

	// The proposal surface must render with the decision facts and actions.
	view := m.renderAutonomyProposalBlock(100)
	for _, want := range []string{"modification", "build", "Execute", "Inspect", "Cancel", "Rollback"} {
		if !strings.Contains(view, want) {
			t.Errorf("proposal missing %q:\n%s", want, view)
		}
	}

	// Select Execute (the highlighted action on a fresh proposal) and activate:
	// the runtime grants internally, revalidates the decision and continues —
	// no re-submitted prompt, no command parser, no second approval.
	executeCmd := m.executeAutonomyProposal()
	if executeCmd == nil {
		t.Fatal("Execute must continue the pending autonomy decision")
	}
	if m.pendingAutonomyProposal != nil {
		t.Error("pending proposal must be consumed by Execute")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("post-execute workspace = /%s, want /build", got)
	}
	if !m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Error("authority must hold after the internal grant")
	}
}

// TestAutonomyProposalKeyboardNavigation pins the keyboard-driven proposal
// interaction: ↑/↓ navigate the action list, Enter activates the highlighted
// action, Esc cancels. No /grant command exists anywhere in the surface.
func TestAutonomyProposalKeyboardNavigation(t *testing.T) {
	m := autonomyTestModel()

	m.runAutonomyRoutedCmd("read @index.html and remove extra contents")
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected a pending proposal")
	}

	// ↓ moves from Execute (index 0) to Inspect (index 1); Enter toggles the
	// read-only inspect detail.
	res, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	after := res.(*model)
	if after.autonomyProposalSelect != 1 {
		t.Fatalf("selection = %d, want 1 after ↓", after.autonomyProposalSelect)
	}
	if cmd != nil {
		t.Fatalf("navigation must not execute anything, got cmd")
	}
	res, _ = after.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	after = res.(*model)
	if !after.autonomyProposalInspect {
		t.Fatal("Enter on Inspect must toggle the read-only detail view")
	}
	if after.pendingAutonomyProposal == nil {
		t.Fatal("Inspect must not consume the proposal")
	}
	view := after.renderAutonomyProposalBlock(100)
	if !strings.Contains(view, "Decision detail") {
		t.Errorf("inspect view missing decision detail:\n%s", view)
	}

	// Esc cancels the proposal: no grant, no execution.
	res, _ = after.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	after = res.(*model)
	if after.pendingAutonomyProposal != nil {
		t.Fatal("Esc must cancel the proposal")
	}
	if after.autonomy.Grants().Count() != 0 {
		t.Fatal("cancel must not issue any capability grant")
	}
}

// TestAutonomyGrantNoRepeatedApproval pins the "no repeated approval for the
// granted scope" guarantee: after one proposal Execute, the same objective
// auto-continues.
func TestAutonomyGrantNoRepeatedApproval(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte("<html><body><main><p>keep</p></main>stray text</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyTestModel()

	// First request: ask_user (mutation not granted).
	m.runAutonomyRoutedCmd("remove unused content from @index.html")
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected pending proposal on first mutation request")
	}
	m.executeAutonomyProposal()

	// Second request with the same objective: now auto-continues directly.
	m.pendingAutonomyProposal = nil
	m.resolver.Set(modes.ModeAsk)
	cmd := m.runAutonomyRoutedCmd("remove unused content from @index.html")
	if cmd == nil {
		t.Fatal("post-grant mutation must execute without a repeated proposal")
	}
	if m.pendingAutonomyProposal != nil {
		t.Error("granted scope must not re-ask for approval")
	}
}

// TestAutonomyFreeFormConversationStaysAsk pins the free-form boundary: bare
// conversation input never enters a workspace or execution loop.
func TestAutonomyFreeFormConversationStaysAsk(t *testing.T) {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeAsk)

	cmd := m.handleInput("explain what a goroutine is")
	if cmd == nil {
		t.Fatal("conversation must dispatch a chat response")
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("workspace = /%s, want /ask for conversation", got)
	}
}

// TestAutonomyPromptDirectiveRoutesThroughRuntime pins that the $prompt
// directive flows through the autonomy runtime rather than the legacy /ask
// handoff when the decision runtime is wired.
func TestAutonomyPromptDirectiveRoutesThroughRuntime(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.routePromptDirective("inspect @index.html")
	if cmd == nil {
		t.Fatal("$prompt inspect must dispatch")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("$prompt inspect → workspace /%s, want /investigate", got)
	}
	if !m.investigateRunning {
		t.Error("investigate engine must start after $prompt routing")
	}
}

// TestAutonomyPromptDirectiveMutationGates pins that $prompt mutation requests
// render the autonomy proposal before execution.
func TestAutonomyPromptDirectiveMutationGates(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.routePromptDirective("read @index.html and remove extra contents")
	if cmd != nil {
		t.Fatal("pre-grant $prompt mutation must await the proposal")
	}
	if m.pendingAutonomyProposal == nil || !m.pendingAutonomyProposal.Missing.Has(autonomy.CapMutate) {
		t.Fatal("$prompt mutation must stage a mutation proposal")
	}
}

// TestAutonomyOwnsIntentAfterBoundary pins requirements 2/3: once an input
// enters the autonomy boundary, the autonomy engine owns intent, capability,
// workspace and execution routing. "$prompt Please read this file and remove
// any redundant content for me @index.html" must yield intent=modification and
// workspace=build — it must NEVER become a legacy submit_prompt → intent=ask
// classification after autonomy already owned the request.
func TestAutonomyOwnsIntentAfterBoundary(t *testing.T) {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeAsk)

	cmd := m.routePromptDirective("Please read this file and remove any redundant content for me @index.html")
	if cmd != nil {
		t.Fatal("pre-grant mutation must await the proposal")
	}
	prop := m.pendingAutonomyProposal
	if prop == nil {
		t.Fatal("expected a pending proposal")
	}
	if prop.Intent != autonomy.IntentModification {
		t.Errorf("autonomy intent = %s, want modification", prop.Intent)
	}
	if prop.Target != "index.html" {
		t.Errorf("autonomy target = %q, want index.html", prop.Target)
	}
	if prop.Workspace != autonomy.WorkspaceBuild {
		t.Errorf("autonomy workspace = %s, want build", prop.Workspace)
	}

	// The legacy mode-first classifier must never re-classify a request the
	// autonomy engine already owns: no /ask handoff, no router classification.
	logged := recordsText(m)
	if strings.Contains(logged, "Refining prompt through ask handoff") {
		t.Error("legacy /ask refinement must not run after autonomy owns the request")
	}
	if strings.Contains(logged, "[intent] classified: /ask") {
		t.Error("legacy mode-first classifier must not run after autonomy owns the request")
	}
}

// TestAutonomyConfirmationGateNoLoop pins that a non-capability ask_user gate
// (risk acknowledgement / target confirmation) resolves on Execute without
// re-entering the same gate — the runtime executes the decided workspace
// directly and never loops back to a duplicate proposal.
func TestAutonomyConfirmationGateNoLoop(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte("<html><body><main><p>keep</p></main>stray text</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyTestModel()
	// Wire a high-risk classifier so the controller raises a risk-acknowledgement
	// gate AFTER the mutation capability is granted.
	m.autonomy = autonomy.NewEngine(
		autonomy.WithScope("repository"),
		autonomy.WithRiskFunc(func(target string) autonomy.MutationRiskInput {
			return autonomy.MutationRiskInput{Level: autonomy.RiskHigh}
		}),
	)

	// Gate 1: capability authorization.
	cmd := m.runAutonomyRoutedCmd("remove redundant content from @index.html")
	if cmd != nil {
		t.Fatal("pre-grant mutation must await the proposal")
	}
	if m.pendingAutonomyProposal == nil || len(m.pendingAutonomyProposal.Missing) == 0 {
		t.Fatal("gate 1 must be the capability authorization")
	}

	// Execute → grant → revalidate → a risk-acknowledgement gate surfaces.
	cmd = m.executeAutonomyProposal()
	if cmd != nil {
		t.Fatal("risk gate must await a second proposal (no direct execution yet)")
	}
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected the risk-acknowledgement gate")
	}
	if len(m.pendingAutonomyProposal.Missing) != 0 {
		t.Fatalf("risk gate must not request capabilities, got %v", m.pendingAutonomyProposal.Missing)
	}

	// Execute on the confirmation gate acknowledges and executes directly.
	cmd = m.executeAutonomyProposal()
	if cmd == nil {
		t.Fatal("acknowledged risk gate must continue execution")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("confirmation gate must not re-propose")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("post-acknowledgement workspace = /%s, want /build", got)
	}
}

// TestAutonomyContextEvidenceLedger pins requirement §6/§8: before the build
// engine asks the model, the runtime compiles structural evidence for the
// resolved target and hands the model a Context Evidence Ledger.
func TestAutonomyContextEvidenceLedger(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	indexHTML := "<html><body><main><p>keep</p></main>stray text</body></html>\n"
	if err := os.WriteFile("index.html", []byte(indexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyTestModel()
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	m.resolver.Set(modes.ModeAsk)

	cmd := m.runAutonomyRoutedCmd("read @index.html and remove extra contents")
	if cmd == nil {
		t.Fatal("post-grant mutation must execute")
	}
	// The runtime must have compiled the target's structural evidence: orphan
	// text (stray text) is a deterministic finding the model never has to
	// rediscover.
	msg := cmd()
	prm, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("expected planResultMsg, got %T", msg)
	}
	if len(prm.Tasks) == 0 {
		t.Fatal("expected staged build tasks")
	}
	if !strings.Contains(prm.Tasks[0].Evidence, "Context Evidence Ledger") {
		t.Errorf("task evidence missing ledger, got: %q", prm.Tasks[0].Evidence)
	}
	if !strings.Contains(prm.Tasks[0].Evidence, "html.orphan_text") {
		t.Errorf("task evidence missing orphan-text finding, got: %q", prm.Tasks[0].Evidence)
	}
	if prm.Tasks[0].Target != "index.html" {
		t.Errorf("build target = %q, want index.html", prm.Tasks[0].Target)
	}
}
