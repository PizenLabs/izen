package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── ACCEPTANCE SUITE (task §14) ─────────────────────────────────────────
// These tests drive the EXACT acceptance inputs end-to-end through the TUI
// entry point (handleInput) with the autonomy decision runtime wired, and
// assert the terminal outcomes contract (§11):
//
//   - direct_response  — conversation answers directly, no timeline/proposal
//   - ask_user         — capability escalation / proposal gate, never silent
//   - auto_continue    — granted boundary executes without re-asking
//   - failed_with_diagnosis — target not found surfaces a diagnosis, never a
//     raw parser error

// redundantFixture is a small HTML file with deterministic redundant content:
// an orphan text node and a duplicated section.
const redundantFixture = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Site</title>
</head>
<body>
<section class="content"><p>Keep this meaningful section.</p></section>
stray orphan text outside any container
<section class="content"><p>Keep this meaningful section.</p></section>
</body>
</html>
`

func writeIndexFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(redundantFixture), 0o644); err != nil {
		t.Fatal(err)
	}
}

// autonomyAcceptanceModel is a chat-ready, autonomy-wired model with the mock
// provider, the RuntimeExecutor and the IntentGateway, positioned in /ask. The
// executor+gateway mirror the production composition (compose.Wire) so the
// exercised mutation path is the RuntimeExecutor, never a UI-owned provider.
func autonomyAcceptanceModel() *model {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.provider = &mockProvider{responses: []*ai.Response{{Content: "ok", TokenOutput: 5}}}
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, m.provider, nil, "")
	return m
}

// grantMutation authorizes the full BUILD capability vector for the scope so a
// mutation request auto-continues instead of gating on a proposal.
func (m *model) grantMutation() {
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
}

func noExecutionEnginesStarted(m *model) bool {
	return !m.investigateRunning && !m.reviewRunning && !m.agentRunning
}

// CASE A — conversation.
// "what is Go?" is answered as ASK / direct response: no execution timeline,
// no mutation, no proposal, no grant.
func TestAcceptanceCaseAConversationDirectResponse(t *testing.T) {
	m := autonomyAcceptanceModel()

	cmd := m.handleInput("what is Go?")
	if cmd == nil {
		t.Fatal("conversation must dispatch a chat response")
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("workspace = /%s, want /ask (conversation stays in ask)", got)
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("conversation must not render an autonomy proposal")
	}
	if len(m.pendingAutonomyTargets) > 0 {
		t.Fatal("conversation must not stage a target selector")
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("conversation must not issue any capability grant")
	}
	if !noExecutionEnginesStarted(m) {
		t.Fatal("conversation must not start any execution engine")
	}
	if !strings.Contains(recordsText(m), "AUTONOMY DECISION") {
		t.Error("conversation still observes the autonomy decision")
	}
}

// CASE B — explicit read-only /ask.
// "/ask inspect @index.html" is read-only: it may analyze the file but must
// never mutate or escalate.
func TestAcceptanceCaseBAskInspectReadOnly(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()

	cmd := m.handleInput("/ask inspect @index.html")
	if cmd == nil {
		t.Fatal("/ask inspect must dispatch a read-only analysis")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("/ask inspect must not escalate to a mutation proposal")
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("/ask inspect must not grant anything")
	}
	if strings.Contains(recordsText(m), "[HOTFIX]") || strings.Contains(recordsText(m), "mutat") {
		t.Error("/ask inspect must never mutate or dispatch a hotfix")
	}
}

// CASE B' — /ask mutation request.
// "/ask remove redundant content from @index.html" must NOT silently execute
// or answer as chat: the autonomy runtime returns a capability escalation
// proposal (ask_user). The user authorizes the boundary once.
func TestAcceptanceCaseBAskMutationEscalates(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()

	cmd := m.handleInput("/ask remove redundant content from @index.html")
	if cmd != nil {
		t.Fatalf("/ask mutation must await a proposal gate, got cmd %T", cmd)
	}
	if m.pendingAutonomyProposal == nil {
		t.Fatal("/ask mutation must stage a capability escalation proposal")
	}
	if !m.pendingAutonomyProposal.Missing.Has(autonomy.CapMutate) {
		t.Fatalf("escalation must request the mutate capability, got %v", m.pendingAutonomyProposal.Missing)
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("escalation must not grant until Execute")
	}
	// The proposal must not expose internal grant vocabulary.
	view := m.renderAutonomyProposalBlock(100)
	if strings.Contains(view, "/grant") || strings.Contains(view, "bitmask") {
		t.Error("proposal must never expose internal authorization vocabulary")
	}
	// The user never types a second command: Execute escalates and continues.
	if cmd := m.executeAutonomyProposal(); cmd == nil {
		t.Fatal("Execute on the /ask escalation must continue execution")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("post-escalation workspace = /%s, want /build", got)
	}
}

// CASE C — $prompt read-only.
// "$prompt inspect @index.html" is owned by the autonomy runtime: workspace =
// INVESTIGATE, read-only, no /ask handoff, no manual workspace switch.
func TestAcceptanceCaseCPromptInspectReadOnly(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()

	cmd := m.handleInput("$prompt inspect @index.html")
	if cmd == nil {
		t.Fatal("$prompt inspect must dispatch the investigate engine")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("workspace = /%s, want /investigate", got)
	}
	if !m.investigateRunning {
		t.Error("investigate engine must start after $prompt routing")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("read-only investigation must not render a proposal")
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("read-only investigation must not grant anything")
	}
	if strings.Contains(recordsText(m), "transitioning to /ask") {
		t.Error("$prompt must not fall back to the legacy /ask handoff")
	}
}

// CASE D — $prompt mutation.
// "$prompt read @index.html and remove redundant content" resolves intent =
// modification, workspace = BUILD, target = index.html. Without authorization
// it renders ONE autonomy proposal; Execute authorizes internally and the
// mutation continues on the RuntimeExecutor — no /grant, no re-submitted
// prompt, no manual /build, and the deterministic evidence ledger reaches the
// model before any reasoning.
func TestAcceptanceCaseDPromptMutationProposalAndEvidence(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")

	cmd := m.handleInput("$prompt read @index.html and remove redundant content")
	if cmd != nil {
		t.Fatalf("pre-grant mutation must await the proposal, got cmd %T", cmd)
	}
	prop := m.pendingAutonomyProposal
	if prop == nil {
		t.Fatal("expected a pending autonomy proposal")
	}
	if prop.Intent != autonomy.IntentModification {
		t.Errorf("intent = %s, want modification", prop.Intent)
	}
	if prop.Workspace != autonomy.WorkspaceBuild {
		t.Errorf("workspace = %s, want build", prop.Workspace)
	}
	if prop.Target != "index.html" {
		t.Errorf("target = %q, want index.html", prop.Target)
	}

	// ONE authorization: Execute grants internally, revalidates and continues.
	executeCmd := m.executeAutonomyProposal()
	if executeCmd == nil {
		t.Fatal("Execute must continue execution")
	}
	if m.pendingAutonomyProposal != nil {
		t.Error("Execute must consume the proposal (no repeated authorization)")
	}
	if !m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Error("authority must hold after the internal grant")
	}

	// The authorized mutation routes through the RuntimeExecutor and stops at
	// its approval gate with a held patch — never a legacy staged plan.
	msg := executeCmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg after authorization, got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("authorized mutation must hold a patch at the executor approval gate")
	}
	if len(gem.res.Targets) == 0 || gem.res.Targets[0] != "index.html" {
		t.Errorf("execution target = %v, want index.html", gem.res.Targets)
	}
	// The deterministic evidence ledger (context + redundancy) reaches the
	// provider request as the authoritative evidence contract (§9/§10).
	if len(mock.requests) == 0 {
		t.Fatal("provider must have received the execution request")
	}
	userContent := ""
	for _, m := range mock.requests[0].Messages {
		if m.Role == "user" {
			userContent += m.Content
		}
	}
	if !strings.Contains(userContent, "Context Evidence Ledger") {
		t.Errorf("mutation prompt missing context ledger: %q", userContent)
	}
	if !strings.Contains(userContent, "Redundant content findings") {
		t.Errorf("mutation prompt missing redundancy ledger: %q", userContent)
	}
}

// CASE E — /build$hot execution request.
// "/build$hot check @index.html and remove redundant content" is a direct
// autonomy execution request. The target resolves deterministically (never a
// "target ambiguous" dead end when redundancy evidence exists) and execution
// continues on the RuntimeExecutor — no second /build.
func TestAcceptanceCaseEBuildHotExecution(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")

	cmd := m.handleInput("/build$hot check @index.html and remove redundant content")
	if cmd != nil {
		t.Fatalf("pre-grant hotfix must await the proposal, got cmd %T", cmd)
	}
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected the autonomy proposal for the hotfix execution request")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("workspace = /%s, want /build", got)
	}

	// Execute authorizes internally; the hotfix continues on the RuntimeExecutor
	// with the SAME objective — no second /build, no ambiguity dead end.
	executeCmd := m.executeAutonomyProposal()
	if executeCmd == nil {
		t.Fatal("authorized hotfix must continue execution")
	}
	msg := executeCmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("authorized hotfix must route through the executor (gatedExecutionMsg), got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("hotfix must stop at the executor approval gate with a held patch")
	}
	if len(gem.res.Targets) == 0 || gem.res.Targets[0] != "index.html" {
		t.Errorf("hotfix target = %v, want index.html", gem.res.Targets)
	}
	logged := recordsText(m)
	if strings.Contains(logged, "Target is ambiguous") {
		t.Errorf("redundancy-resolved hotfix must not dead-end on ambiguity:\n%s", logged)
	}
}

// CASE F — malformed/redundant HTML.
// "$prompt inspect and remove redundant content from @index.html" must
// classify as MODIFICATION (the mutation verb owns the intent), the file is
// read, structural + redundancy evidence is produced, and the model receives
// the evidence — the pipeline never collapses to "html: empty document".
func TestAcceptanceCaseFMalformedHTMLEvidence(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	m.grantMutation()

	cmd := m.handleInput("$prompt inspect and remove redundant content from @index.html")
	if cmd == nil {
		t.Fatal("post-grant mutation must dispatch the build")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("granted mutation must not render a proposal")
	}

	// The autonomy runtime owns the intent: modification, never investigation.
	res := m.autonomy.Classify("inspect and remove redundant content from @index.html")
	if res.Intent != autonomy.IntentModification {
		t.Fatalf("autonomy intent = %s, want modification (mutation verb dominates inspection)", res.Intent)
	}

	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg, got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("granted mutation must hold a patch at the executor approval gate")
	}
	// The deterministic evidence reaches the provider request — the model never
	// re-discovers the redundant content from raw text.
	if len(mock.requests) == 0 {
		t.Fatal("provider must have received the execution request")
	}
	userContent := ""
	for _, m := range mock.requests[0].Messages {
		if m.Role == "user" {
			userContent += m.Content
		}
	}
	if !strings.Contains(userContent, "Redundant content findings") {
		t.Errorf("mutation prompt missing redundancy ledger:\n%s", userContent)
	}
	if !strings.Contains(userContent, "orphan") {
		t.Errorf("mutation prompt missing orphan-content finding:\n%s", userContent)
	}
	if strings.Contains(userContent, "empty document") {
		t.Errorf("evidence must never claim an empty document for a readable file:\n%s", userContent)
	}
}

// CASE G — approved autonomous loop.
// After ONE user authorization, the runtime may investigate → plan → build →
// verify (and diagnose → loop) inside the approved boundary without asking
// again. A second mutation objective in the same scope auto-continues.
func TestAcceptanceCaseGApprovedLoopNoReauthorization(t *testing.T) {
	writeIndexFixture(t)
	m := autonomyAcceptanceModel()
	mock := &mockProvider{responses: []*ai.Response{
		{Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section.</p>\n=======\n<p>Kept.</p>\n>>>>>>>", Usage: ai.ProviderUsage{Known: true}},
		{Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section.</p>\n=======\n<p>Kept.</p>\n>>>>>>>", Usage: ai.ProviderUsage{Known: true}},
	}}
	m.provider = mock
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	// Governance surface for a real approval: AuthorizationEngine + budget +
	// capabilities + a trivial verifier (the compile gate would invoke the real
	// toolchain).
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{},
		fakeCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	m.mutationBudget = budget.NewBudget(10, 1000, 100000, 3, 30*time.Minute, 10)
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	trivial := execution.NewVerifier(".")
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(trivial)

	// First request: one proposal, one Execute.
	firstCmd := m.handleInput("$prompt read @index.html and remove redundant content")
	if firstCmd != nil || m.pendingAutonomyProposal == nil {
		t.Fatal("first mutation must gate on a proposal")
	}
	firstExec := m.executeAutonomyProposal()
	if firstExec == nil {
		t.Fatal("Execute must continue the first mutation")
	}
	if !m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Fatal("grant must cover the approved boundary after one authorization")
	}
	// Drive the first execution to its approval gate, approve it, and complete
	// it so the agent-active flag clears before the second objective.
	firstMsg := firstExec()
	gem, ok := firstMsg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("first mutation must route through the executor, got %T", firstMsg)
	}
	if gem.err != nil {
		t.Fatalf("first executor failed: %v", gem.err)
	}
	res, _ := m.Update(gem)
	m = res.(*model)
	if m.executorPendingPatchID == "" {
		t.Fatal("first execution must stage a patch at the approval gate")
	}
	approveMsg := m.runExecutorApproveCmd(m.executorPendingPatchID)()
	mr, ok := approveMsg.(executionResultMsg)
	if !ok {
		t.Fatalf("expected executionResultMsg from approve, got %T", approveMsg)
	}
	if mr.err != nil {
		t.Fatalf("first approve failed: %v", mr.err)
	}
	res, _ = m.Update(mr)
	m = res.(*model)

	// Second objective inside the same boundary: auto-continue, no re-ask.
	secondCmd := m.handleInput("$prompt remove the stray orphan text from @index.html")
	if secondCmd == nil {
		t.Fatal("approved boundary must auto-continue without a repeated proposal")
	}
	if m.pendingAutonomyProposal != nil {
		t.Error("approved boundary must not re-ask for authorization")
	}
}

// §11 — a mutation request whose target exists nowhere is a terminal
// diagnosis (failed_with_diagnosis), never a silent new-file creation and
// never a raw parser error.
func TestAcceptanceTargetNotFoundDiagnosis(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// No index.html anywhere in the workspace.
	if err := os.WriteFile("README.md", []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyAcceptanceModel()
	m.grantMutation()

	cmd := m.handleInput("$prompt remove redundant content from @index.html")
	if cmd != nil {
		t.Fatalf("removal of a non-existent target must stop, got cmd %T", cmd)
	}
	logged := recordsText(m)
	if !strings.Contains(logged, "target not found") {
		t.Errorf("missing target must surface a diagnosis:\n%s", logged)
	}
	for _, want := range []string{"what Izen attempted", "what evidence it found", "why the current strategy failed", "next"} {
		if !strings.Contains(logged, want) {
			t.Errorf("diagnosis missing %q:\n%s", want, logged)
		}
	}
}

// §8 — target ambiguity is a decision: several workspace files match a target
// that has no exact root path → a small candidate selector pauses, Enter
// continues on the selected file without restarting the command. (An explicit
// @path that exists at the workspace root is authoritative and resolves without
// a selector — the canonical strategy resolver decides.)
func TestAcceptanceAmbiguousTargetSelector(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("templates", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("src", 0o755); err != nil {
		t.Fatal(err)
	}
	// No ./index.html exists: only two same-named files deeper in the tree, so
	// the canonical resolver surfaces a genuine ambiguity.
	if err := os.WriteFile("templates/index.html", []byte("<html><body><p>template</p></body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("src/index.html", []byte("<html><body><p>src</p></body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyAcceptanceModel()
	m.grantMutation()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>template</p>\n=======\n<p>kept</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")

	cmd := m.handleInput("$prompt remove redundant content from @index.html")
	if cmd != nil {
		t.Fatalf("ambiguous target must pause with a selector, got cmd %T", cmd)
	}
	if len(m.pendingAutonomyTargets) != 2 {
		t.Fatalf("expected 2 candidate targets, got %v", m.pendingAutonomyTargets)
	}
	view := m.renderAutonomyTargetSelectorBlock(100)
	if !strings.Contains(view, "AUTONOMY TARGET SELECTION") {
		t.Errorf("selector block missing title:\n%s", view)
	}

	// Enter selects the highlighted candidate and resumes on the RuntimeExecutor.
	res, selCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := res.(*model)
	if selCmd == nil {
		t.Fatal("Enter on a target candidate must continue execution")
	}
	if len(m2.pendingAutonomyTargets) != 0 {
		t.Fatal("selection must consume the selector")
	}
	msg := selCmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg after target selection, got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if len(gem.res.Targets) == 0 || gem.res.Targets[0] == "index.html" {
		t.Errorf("selected execution target = %v, want a resolved tree path (templates/… or src/…)", gem.res.Targets)
	}
}
