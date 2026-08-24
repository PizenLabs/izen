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

// largeRedundantIndexHTML is a >100-line HTML fixture with real redundant
// content: an orphan text node and a duplicated section.
func largeRedundantIndexHTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Site</title>\n</head>\n<body>\n")
	for i := 0; i < 110; i++ {
		b.WriteString("<section class=\"content\"><p>Keep this meaningful section number " + numStr(i) + ".</p></section>\n")
	}
	b.WriteString("stray orphan text outside any container\n")
	b.WriteString("<section class=\"content\"><p>Keep this meaningful section number 5.</p></section>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func numStr(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}

// TestBuildHotAutonomyExecution pins requirement 4/9/D: "/build$hot check
// @index.html and remove redundant content" is an EXECUTION REQUEST. It enters
// the autonomy runtime (never the legacy mode-first classifier), resolves the
// target from @index.html, classifies intent as modification, selects the BUILD
// workspace, and — when mutation authorization is missing — renders ONE
// autonomy proposal. Executing the proposal issues the internal grant and
// continues on the RuntimeExecutor WITHOUT a second /build command.
func TestBuildHotAutonomyExecution(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeRedundantIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section number 1.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("/build$hot check @index.html and remove redundant content")
	if cmd != nil {
		t.Fatalf("pre-grant hotfix must await the proposal, got cmd %T", cmd)
	}

	// The workspace resolved to BUILD and the target resolved to index.html.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("workspace = /%s, want /build", got)
	}
	if m.pendingAutonomyProposal == nil {
		t.Fatal("expected an autonomy proposal (mutation not granted)")
	}
	prop := m.pendingAutonomyProposal
	if prop.Intent != autonomy.IntentModification {
		t.Errorf("intent = %s, want modification", prop.Intent)
	}
	if prop.Workspace != autonomy.WorkspaceBuild {
		t.Errorf("workspace = %s, want build", prop.Workspace)
	}
	if prop.Target != "index.html" {
		t.Errorf("target = %q, want index.html", prop.Target)
	}
	if !prop.Missing.Has(autonomy.CapMutate) {
		t.Errorf("proposal must request the mutate capability, got %v", prop.Missing)
	}

	// The proposal must be actionable (Execute/Inspect/Cancel) and must NEVER
	// instruct the user to type /grant.
	view := m.renderAutonomyProposalBlock(100)
	for _, want := range []string{"Execute", "Inspect", "Cancel", "build", "index.html", "modification"} {
		if !strings.Contains(view, want) {
			t.Errorf("proposal missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(recordsText(m), "/grant") {
		t.Error("proposal must not instruct the user to type /grant")
	}

	// Enter on the highlighted action (Execute) authorizes internally and
	// continues execution — no re-submitted prompt, no second /build.
	res, executeCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := res.(*model)
	if m2.pendingAutonomyProposal != nil {
		t.Fatal("Execute must consume the proposal")
	}
	if executeCmd == nil {
		t.Fatal("Execute must continue execution")
	}
	if got := m2.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("post-execute workspace = /%s, want /build", got)
	}
	if !m2.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Error("authority must hold after the internal grant")
	}

	// The hotfix continues on the RuntimeExecutor with the SAME objective — no
	// second /build — and stops at the executor approval gate.
	msg := executeCmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("hotfix must route through the executor (gatedExecutionMsg), got %T", msg)
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
	// Requirement 5/4: ambiguity is a decision, never a dead end — the
	// "Target is ambiguous. No model call was made." collapse must not occur.
	if strings.Contains(recordsText(m2), "Target is ambiguous") {
		t.Errorf("redundancy-resolved hotfix must not dead-end on ambiguity:\n%s", recordsText(m2))
	}
}

// TestBuildHotAutonomyAutoContinue pins that a /build$hot execution request
// with mutation already authorized auto-continues directly into the
// RuntimeExecutor — no proposal, no second /build (requirement 4/6).
func TestBuildHotAutonomyAutoContinue(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeRedundantIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section number 1.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("/build$hot check @index.html and remove redundant content")
	if cmd == nil {
		t.Fatal("authorized hotfix must dispatch immediately (no proposal)")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("authorized mutation must not render a proposal")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("workspace = /%s, want /build", got)
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("authorized hotfix must stop at the executor approval gate")
	}
}

// TestAutonomyModificationProposalRedundancyEvidence pins requirement 8: for a
// redundancy-removal request the deterministic redundancy ledger is compiled as
// target evidence BEFORE any model reasoning.
func TestAutonomyModificationProposalRedundancyEvidence(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeRedundantIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>Keep this meaningful section number 1.</p>\n=======\n<p>Kept.</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m.provider = mock
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$hot check @index.html and remove redundant content")
	if cmd == nil {
		t.Fatal("authorized redundancy hotfix must dispatch")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("authorized mutation must not render a proposal")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("authorized hotfix must stop at the executor approval gate")
	}
	// The runtime owns the deterministic redundancy classification; the UI
	// never re-classifies the request after the executor resolved it. The
	// deterministic evidence ledger crosses into the model as the
	// authoritative evidence contract.
	evidenceInPrompt := false
	for _, r := range mock.requests {
		for _, m := range r.Messages {
			if strings.Contains(m.Content, "Context Evidence Ledger") {
				evidenceInPrompt = true
			}
		}
	}
	if !evidenceInPrompt {
		t.Error("mutation prompt must carry the deterministic redundancy evidence ledger")
	}
}
