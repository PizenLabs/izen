package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── PHASE 8 — CONTEXT ECONOMY & EXECUTION PROOF ─────────────────────────────
//
// For the exact reported scenario — "$hot Remove extra text from @index.html"
// against ONE explicit small-but-real file — Izen must prove:
//
//   - it read the file once,
//   - it sent a bounded context (one file context, no unrelated repository
//     content, no conversation history, no contradictory tool schemas),
//   - it invoked the provider exactly once,
//   - the provider's reported usage is preserved verbatim,
//   - the artifact/diff/apply/filesystem/verify facts derive from real
//     runtime evidence (never from a generated proposal).

// smallRealIndexHTML is a small (< 100 lines) HTML file with REAL content — the
// exact class the Phase 8 contract fix distinguishes from a stub.
const smallRealIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Home</title>
</head>
<body>
  <h1>Home</h1>
  <h2>Extra text</h2>
  <p>Stable body content</p>
</body>
</html>
`

func writeSmallRealIndex(t *testing.T) string {
	t.Helper()
	return writeHotfixFixture(t, "index.html", smallRealIndexHTML)
}

// TestContextEconomy_SingleFileSingleContext is the duplication-detection
// regression (Section 5): one explicit file → exactly ONE authoritative file
// context, no unrelated repository content, no conversation history, no
// contradictory tool schemas.
func TestContextEconomy_SingleFileSingleContext(t *testing.T) {
	indexPath := writeSmallRealIndex(t)

	// The model (wrongly) re-emits the complete corrected file — the exact
	// observed scenario. The changeset pipeline accepts the bounded whole-file
	// block for a small target; what this test proves is the CONTEXT Izen sent.
	fixed := strings.Replace(smallRealIndexHTML, "  <h2>Extra text</h2>\n", "", 1)
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + fixed + "```",
		TokenInput:  2522,
		TokenOutput: 2048,
	}}}
	m := hotfixModelWithProvider(t, mock)

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Remove extra text from @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider invocations = %d, want exactly 1", mock.callCount)
	}
	if len(mock.requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(mock.requests))
	}

	// ── CONTEXT-OWNERSHIP ACCOUNT ────────────────────────────────────
	env := hp.Envelope
	if env.Target != indexPath {
		t.Errorf("envelope target = %q, want %q", env.Target, indexPath)
	}
	if env.FileContextCount() != 1 {
		t.Errorf("file context count = %d, want exactly 1 (one explicit file → one file context)", env.FileContextCount())
	}
	if env.HasUnrelatedRepositoryContext() {
		t.Error("single-file hotfix carried unrelated repository/workspace context")
	}
	if env.HasConversationHistory() {
		t.Error("single-file hotfix replayed conversation history")
	}
	if env.ToolContext != "" {
		t.Errorf("single-file hotfix shipped contradictory native tool schemas: %q", env.ToolContext)
	}
	if env.FileContext != "" && !strings.Contains(mock.requests[0].Messages[0].Content, env.FileContext) {
		t.Error("envelope file context does not match the actual request payload")
	}

	// ── THE ACTUAL REQUEST ───────────────────────────────────────────
	req := mock.requests[0]
	if len(req.Messages) != 1 {
		t.Errorf("request carries %d messages, want exactly 1 (no history)", len(req.Messages))
	}
	if len(req.Tools) != 0 {
		t.Errorf("request ships %d tool schemas — the code-block contract consumes none", len(req.Tools))
	}
	user := req.Messages[0].Content
	// The distinctive body marker must appear exactly once: the file content is
	// embedded once, never duplicated.
	if n := strings.Count(user, "<h1>Home</h1>"); n != 1 {
		t.Errorf("target file content appears %d times in the request, want exactly 1 (no duplication)", n)
	}
	if !strings.Contains(user, "Do NOT output the entire file") {
		t.Errorf("bounded snippet contract missing from the request:\n%s", user)
	}
}

// TestContextEconomy_ProviderUsagePreservedAndProof asserts the execution-proof
// contract (Section 7/12): provider usage is preserved verbatim from the
// provider response, invocations == 1, and the proof derives apply/filesystem/
// verify facts only from the real apply result.
func TestContextEconomy_ProviderUsagePreservedAndProof(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	orig := smallRealIndexHTML
	if err := os.WriteFile("index.html", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	fixed := strings.Replace(orig, "  <h2>Extra text</h2>\n", "", 1)
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + fixed + "```",
		TokenInput:  2522,
		TokenOutput: 2048,
		Usage: ai.ProviderUsage{
			PromptTokens:     2522,
			CompletionTokens: 2048,
			TotalTokens:      4570,
			Known:            true,
		},
	}}}
	m := hotfixTruthModel(t, mock)

	cmd := m.handleHotfixCmd("Remove extra text from @index.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}

	// ── GENERATION: exactly one invocation, usage preserved ──────────
	var hp hotfixProposalMsg
	found := false
	for _, msg := range runBuildCmdsFiltered(t, cmd) {
		if p, ok := msg.(hotfixProposalMsg); ok {
			hp = p
			found = true
		}
	}
	if !found {
		t.Fatal("no hotfixProposalMsg produced by the generation command")
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider invocations = %d, want exactly 1", mock.callCount)
	}
	if hp.TokenInput != 2522 || hp.TokenOutput != 2048 {
		t.Fatalf("proposal usage = (%d, %d), want provider-reported (2522, 2048)", hp.TokenInput, hp.TokenOutput)
	}

	// ── PROPOSAL → APPROVAL ──────────────────────────────────────────
	res, upCmd := m.Update(hp)
	m2 := res.(*model)
	for _, m2msg := range drainCmds(t, upCmd) {
		r2, _ := m2.Update(m2msg)
		m2 = r2.(*model)
	}
	if !m2.awaitingConfirmation {
		t.Fatal("proposal did not enter the approval state")
	}
	// Generation proof recorded at the proposal handler (invocations + usage
	// + artifact/diff facts, all from real evidence).
	if m2.lastExecutionProof.OperationID == "" {
		t.Fatal("generation proof not recorded")
	}
	if m2.lastExecutionProof.ProviderInvocations != 1 {
		t.Errorf("proof invocations = %d, want 1", m2.lastExecutionProof.ProviderInvocations)
	}
	if m2.lastExecutionProof.InputUsage != 2522 || m2.lastExecutionProof.OutputUsage != 2048 {
		t.Errorf("proof usage = (%d, %d), want provider-reported (2522, 2048)",
			m2.lastExecutionProof.InputUsage, m2.lastExecutionProof.OutputUsage)
	}
	if !m2.lastExecutionProof.ArtifactPresent || !m2.lastExecutionProof.DiffPresent {
		t.Errorf("proof artifact/diff facts wrong: %+v", m2.lastExecutionProof)
	}

	// ── APPLY: filesystem changes, verification passes, proof complete ─
	proposal := m2.pendingProposals[0]
	applyMsg := m2.applyProposalCmd(proposal)()
	mr, ok := applyMsg.(mutationResultMsg)
	if !ok {
		t.Fatalf("expected mutationResultMsg, got %T", applyMsg)
	}
	if mr.err != nil {
		t.Fatalf("apply failed: %v", mr.err)
	}
	outcome := mr.outcome()
	if !outcome.MutationSucceeded() {
		t.Fatalf("result outcome = %q, want changed", outcome)
	}
	if mr.evidence == nil || !mr.evidence.ApplyExecutedChanged() {
		t.Fatalf("evidence does not prove an executed filesystem mutation: %+v", mr.evidence)
	}
	onDisk, rerr := os.ReadFile("index.html")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(onDisk) == orig {
		t.Fatal("filesystem unchanged after a successful apply")
	}
}

// TestContextEconomy_InspectShowsContextOwnership asserts $inspect exposes the
// context-ownership account and the execution proof (Sections 21/22): the
// exact invocation count and the context classes are visible, never inferred.
func TestContextEconomy_InspectShowsContextOwnership(t *testing.T) {
	indexPath := writeSmallRealIndex(t)

	fixed := strings.Replace(smallRealIndexHTML, "  <h2>Extra text</h2>\n", "", 1)
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + fixed + "```",
		TokenInput:  2522,
		TokenOutput: 2048,
	}}}
	m := hotfixModelWithProvider(t, mock)

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Remove extra text from @index.html",
	}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}

	// Store the envelope + generation proof exactly as the terminal handler
	// would (invocations from the real provider count, usage from the response).
	m.lastPromptEnvelope = hp.Envelope
	if hp.TokenInput > 0 {
		m.lastPromptEnvelope.TotalInputTokens = hp.TokenInput
	}
	m.lastExecutionProof = ExecutionProof{
		OperationID:         m.lastPromptEnvelope.OperationID,
		Target:              indexPath,
		ProviderInvocations: mock.callCount,
		InputUsage:          hp.TokenInput,
		OutputUsage:         hp.TokenOutput,
		ArtifactPresent:     hp.Patch != nil,
		DiffPresent:         hp.Diff != "",
	}

	var rendered strings.Builder
	m.pushInspectRecordsForTest(&rendered)
	out := rendered.String()
	if !strings.Contains(out, "context:") {
		t.Errorf("$inspect missing the context-ownership section:\n%s", out)
	}
	if !strings.Contains(out, "file-context=") {
		t.Errorf("$inspect missing the file-context class:\n%s", out)
	}
	if !strings.Contains(out, "provider-invocations=1") {
		t.Errorf("$inspect missing the exact invocation count:\n%s", out)
	}
	if !strings.Contains(out, "proof:") {
		t.Errorf("$inspect missing the execution-proof section:\n%s", out)
	}
	if !strings.Contains(out, "history-context=none") {
		t.Errorf("$inspect must show history-context=none for a single-file hotfix:\n%s", out)
	}
}

// pushInspectRecordsForTest renders the envelope + proof sections exactly as
// runInspectCmd does, capturing the pushed lines for assertions.
func (m *model) pushInspectRecordsForTest(b *strings.Builder) {
	if m.lastPromptEnvelope.OperationID != "" || m.lastPromptEnvelope.Target != "" {
		b.WriteString(renderPromptEnvelope(m.lastPromptEnvelope))
		b.WriteString("\n")
	}
	if m.lastExecutionProof.OperationID != "" || m.lastExecutionProof.Target != "" {
		b.WriteString(renderExecutionProof(m.lastExecutionProof))
		b.WriteString("\n")
	}
}
