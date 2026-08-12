package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/orchestrator"
	"github.com/PizenLabs/izen/internal/patch"
)

// hotfixIndexHTML is the deterministic fixture for the $hot → changeset DoD
// integration test. The anchor line "<h3>Old Delta</h3>" is the unique
// high-similarity match for the cloud-model snippet "<h3>Project Delta</h3>".
const hotfixIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Delta</title>
</head>
<body>
  <h3>Old Delta</h3>
  <p>Stable content</p>
</body>
</html>
`

// drainCmds executes a tea.Cmd and returns every terminal message it yields,
// recursively expanding nested tea.BatchMsg groups. This mirrors how the
// Bubble Tea runtime dispatches multi-message commands.
func drainCmds(t *testing.T, c tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	var stack []tea.Cmd
	if c != nil {
		stack = append(stack, c)
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		msg := cur()
		if batch, ok := msg.(tea.BatchMsg); ok {
			stack = append(stack, batch...)
			continue
		}
		out = append(out, msg)
	}
	return out
}

// TestHotfixCleanStateTransitionFromIdle is DoD Test 3: a $hot urgent fix that
// skips the plan phase starts with the orchestrator at PhaseIdle. Executing the
// hotfix must drive the workflow cleanly into StateBuilding — it must NEVER
// surface "invalid transition idle -> build" (orchestrator.TransitionError) and
// therefore never trigger automated rollback after a valid patch apply.
func TestHotfixCleanStateTransitionFromIdle(t *testing.T) {
	rt := runtime.New(
		artifact.NewStore(t.TempDir()),
		capability.NewCapabilitySet(),
		budget.NewBudget(10, 1000, 100000, 3, 30*time.Second, 10),
	)
	m := newTestModel()
	m.workflowSM = workflow.NewWorkflowStateMachine()
	m.orch = orchestrator.New(m.workflowSM, rt)
	m.caps = capability.NewCapabilitySet()

	hotfixTask := plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}
	m.sess.CurrentTasks = []plan.Task{hotfixTask}

	// Precondition: the orchestrator and SM are both at Idle (the $hot path).
	if got := m.orch.CurrentWorkflowState(); got != workflow.StateIdle {
		t.Fatalf("precondition: orchestrator state = %s, want StateIdle", got)
	}

	// Direct transition check: from Idle the build transition must succeed via
	// the Force fallback and land the SM in StateBuilding.
	if err := m.transitionToBuilding(); err != nil {
		t.Fatalf("transitionToBuilding from Idle returned error (invalid transition idle -> build?): %v", err)
	}
	if got := m.orch.CurrentWorkflowState(); got != workflow.StateBuilding {
		t.Fatalf("orchestrator state after transition = %s, want StateBuilding", got)
	}
	if got := m.workflowSM.State(); got != workflow.StateBuilding {
		t.Fatalf("SM state after transition = %s, want StateBuilding", got)
	}

	// Reset for the full applyHotfixPatch flow (execution engine intentionally
	// absent so the ONLY possible error after the state transition is the
	// engine guard — never a workflow transition error).
	m.workflowSM = workflow.NewWorkflowStateMachine()
	m.orch = orchestrator.New(m.workflowSM, rt)
	m.sess.CurrentTasks = []plan.Task{hotfixTask}

	msg := m.applyHotfixPatch(&hotfixTask, &execution.Patch{File: "index.html", Modified: "--- a/index.html\n+++ b/index.html\n"})()
	result, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected buildResultMsg, got %T", msg)
	}
	if result.err != nil {
		if strings.Contains(result.err.Error(), "transition") || strings.Contains(result.err.Error(), "idle -> build") {
			t.Fatalf("hotfix apply surfaced an illegal state transition error: %v", result.err)
		}
		// Any other error is fine here: the execution engine is intentionally
		// nil, so "engine not configured" is the expected post-transition error.
	}
	if got := m.orch.CurrentWorkflowState(); got != workflow.StateBuilding {
		t.Fatalf("orchestrator state after hotfix apply = %s, want StateBuilding (workspace must stay clean)", got)
	}
}

// TestHotfixTinyResponseTriggersNonStreamingFallback is the DoD resilience test
// (fallback branch): a 1-token SSE response (a dropped/truncated connection,
// e.g. cohere/north-mini-code:free) must NOT be passed to the changeset
// pipeline as a patch. The system retries ONCE with a standard non-streaming
// request and, when the retry returns a real code block, compiles and proposes
// a valid patch — never surfacing "ambiguous change representation".
func TestHotfixTinyResponseTriggersNonStreamingFallback(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	if err := os.WriteFile(indexPath, []byte(hotfixIndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			// 1-token truncated SSE response (the "Thought for 2m59s (1 tokens)").
			{Content: "Sure", TokenOutput: 1},
			// The non-streaming fallback returns the real code block.
			{Content: "Here is the corrected block:\n```html\n<h3>Project Delta</h3>\n```\n", TokenOutput: 60},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "update the header title in the landing page",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// The non-streaming fallback must have been triggered (two provider calls).
	if mock.callCount != 2 {
		t.Fatalf("provider calls = %d, want 2 (initial + non-streaming fallback)", mock.callCount)
	}
	if result.Err != nil {
		t.Fatalf("hotfix failed after fallback: %v", result.Err)
	}
	if result.Patch == nil || !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled unified diff patch after the fallback, got: %+v", result.Patch)
	}
}

// TestHotfixTinyResponseExplicitEmptyAbort is the DoD resilience test (explicit
// abort branch): when BOTH the initial and the non-streaming fallback return a
// 1-token response, the hotfix aborts with the explicit "Provider returned
// empty response" message — it must NEVER surface the misleading "ambiguous
// change representation" that an empty changeset extraction would produce.
func TestHotfixTinyResponseExplicitEmptyAbort(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	if err := os.WriteFile(indexPath, []byte(hotfixIndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			{Content: "Sure", TokenOutput: 1},
			{Content: "No", TokenOutput: 1},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "produce a summary of the landing page state",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if mock.callCount != 2 {
		t.Fatalf("provider calls = %d, want 2 (initial + non-streaming fallback)", mock.callCount)
	}
	if result.Err == nil {
		t.Fatal("expected an explicit empty-response error after both attempts failed")
	}
	if !strings.Contains(result.Err.Error(), "Provider returned empty response (1 token)") {
		t.Errorf("error missing explicit connectivity message: %v", result.Err)
	}
	if strings.Contains(result.Err.Error(), "ambiguous") {
		t.Fatalf("empty response surfaced as ambiguous change representation: %v", result.Err)
	}
}

// TestStreamTransparencyThoughtBufferRetains100Percent is DoD Test 1: every
// raw chunk dispatched via ThoughtBufferUpdatedMsg — reasoning AND content —
// must be retained 100% in the active ThinkingBuffer and accessible through the
// Ctrl+O thought drawer. No model output may be discarded or silently swallowed.
func TestStreamTransparencyThoughtBufferRetains100Percent(t *testing.T) {
	m := newTestModel()

	// Simulate the live stream: a cloud model emits reasoning chunks and
	// content chunks that the provider dispatches as ThoughtBufferUpdatedMsg.
	stream := []ThoughtBufferUpdatedMsg{
		{Content: "analyzing the html structure"},
		{Content: "\n"},
		{Content: "```html\n<h3>Project Delta</h3>\n"},
		{Content: "```\n"},
	}
	want := ""
	for _, tb := range stream {
		want += tb.Content
	}

	var cur tea.Model = m
	for _, tb := range stream {
		res, cmd := cur.Update(tb)
		if cmd != nil {
			t.Fatalf("ThoughtBufferUpdatedMsg handler returned an unexpected cmd")
		}
		cur = res
	}
	// Mark the thought block complete (stream end).
	res, cmd := cur.Update(ThoughtBufferUpdatedMsg{Done: true})
	if cmd != nil {
		t.Fatal("Done ThoughtBufferUpdatedMsg returned an unexpected cmd")
	}
	m2 := res.(*model)

	if m2.thinkingBuffer == nil {
		t.Fatal("ThinkingBuffer was never created")
	}
	if got := m2.thinkingBuffer.String(); got != want {
		t.Fatalf("ThinkingBuffer retained %q, want 100%% of stream %q", got, want)
	}
	if !m2.thinkingBuffer.Complete() {
		t.Fatal("thought block not marked complete at stream end")
	}

	// Ctrl+O expands the thought drawer and the raw stream renders.
	m2.toggleThoughtBlock()
	if !m2.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not expand the thought block")
	}
	rendered := m2.thinkingBuffer.Render(m2.width, false, "")
	if !strings.Contains(rendered, "Project Delta") || !strings.Contains(rendered, "analyzing the html structure") {
		t.Fatalf("Ctrl+O thought render missing the raw stream:\n%s", rendered)
	}
}

// TestHotfixThoughtBufferCtrlO is DoD Test 1: a $hot execution against a cloud
// provider populates the thought/trace buffers with the raw LLM output, and
// pressing Ctrl+O toggles the thought drawer so the raw output renders — even
// though the hotfix patch call is non-streaming (single-shot response).
func TestHotfixThoughtBufferCtrlO(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	if err := os.WriteFile(indexPath, []byte(hotfixIndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			{
				Content:     "Here is the corrected block:\n```html\n<h3>Project Delta</h3>\n```\n",
				TokenInput:  2873,
				TokenOutput: 107,
			},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "update the header title in the landing page",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Err != nil {
		t.Fatalf("hotfix patch generation failed: %v", result.Err)
	}
	if result.RawOutput == "" {
		t.Fatal("hotfixProposalMsg did not carry the raw LLM output (RawOutput empty)")
	}

	// Deliver the proposal through the event loop: the handler dispatches a
	// ThoughtBufferUpdatedMsg (marking the thought block complete) which
	// populates the ThinkingBuffer with the raw LLM output, and writes the
	// raw output trace into traceBuffer immediately.
	res, cmd := m.Update(result)
	m2 := res.(*model)
	for _, em := range drainCmds(t, cmd) {
		if tb, ok := em.(ThoughtBufferUpdatedMsg); ok {
			res, cmd = m2.Update(tb)
			m2 = res.(*model)
			if cmd != nil {
				t.Fatal("ThoughtBufferUpdatedMsg handler returned an unexpected cmd")
			}
		}
	}
	if m2.thinkingBuffer == nil || m2.thinkingBuffer.Len() == 0 {
		t.Fatalf("thought buffer not populated after $hot (Len=%d)", func() int {
			if m2.thinkingBuffer == nil {
				return -1
			}
			return m2.thinkingBuffer.Len()
		}())
	}
	if m2.traceBuffer.Len() == 0 {
		t.Fatal("output-trace buffer not populated after $hot")
	}

	// Ctrl+O toggles the thought drawer and the raw output renders.
	res2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m3 := res2.(*model)
	if m3.thinkingBuffer == nil || !m3.thinkingBuffer.Expanded() {
		t.Fatal("Ctrl+O did not expand the thought block")
	}
	rendered := m3.thinkingBuffer.Render(m3.width, false, "")
	if !strings.Contains(rendered, "Project Delta") {
		t.Fatalf("Ctrl+O thought render missing the raw LLM output:\n%s", rendered)
	}
	// The raw stream is also inspectable via the output-trace viewport.
	if !strings.Contains(m3.traceBuffer.String(), "Project Delta") {
		t.Fatalf("output-trace buffer missing the raw LLM output:\n%s", m3.traceBuffer.String())
	}

	// A second Ctrl+O collapses the thought drawer.
	res3, _ := m3.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m4 := res3.(*model)
	if m4.thinkingBuffer == nil || m4.thinkingBuffer.Expanded() {
		t.Fatal("second Ctrl+O did not collapse the thought block")
	}
}

// TestHotfixChangesetMarkdownCodeBlockAppliesToIndexHTML is the DoD integration
// test for the $hot → changeset wireup: a cloud model (Cohere / Gemma 4) that
// emits a RAW markdown code block — no unified diff, no SEARCH/REPLACE markers
// — must be routed through the changeset pipeline, compiled into an
// authoritative unified diff, and applied to index.html after approval.
func TestHotfixChangesetMarkdownCodeBlockAppliesToIndexHTML(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	if err := os.WriteFile(indexPath, []byte(hotfixIndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			{
				Content: "Here is the corrected block:\n```html\n<h3>Project Delta</h3>\n```\n",
			},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "update the header title in the landing page",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Err != nil {
		t.Fatalf("changeset pipeline failed to compile the hotfix patch: %v", result.Err)
	}
	if result.Patch == nil {
		t.Fatal("expected a non-nil patch from the changeset pipeline")
	}
	if result.Patch.Modified == "" || !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected the authoritative unified diff (with @@ hunk) as the patch payload, got:\n%s", result.Patch.Modified)
	}
	if !strings.Contains(result.Diff, "Project Delta") {
		t.Errorf("approval diff missing the corrected header:\n%s", result.Diff)
	}

	// Apply the compiled diff through the application's authoritative patch
	// engine and assert the on-disk file reflects the change.
	eng := patch.NewEngine()
	applyResult, err := eng.Apply(dir, patch.Request{
		File:          "index.html",
		Original:      hotfixIndexHTML,
		Raw:           result.Patch.Modified,
		TaskObjective: task.Description,
		FileType:      ".html",
	})
	if err != nil {
		t.Fatalf("apply compiled diff to index.html: %v\n%s", err, result.Patch.Modified)
	}
	if !applyResult.Applied {
		t.Fatalf("compiled diff was not applied: %+v", applyResult)
	}

	onDisk, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "<h3>Project Delta</h3>") {
		t.Errorf("index.html missing the corrected header:\n%s", onDisk)
	}
	if strings.Contains(string(onDisk), "<h3>Old Delta</h3>") {
		t.Errorf("index.html still contains the old header:\n%s", onDisk)
	}
}

// TestHotfixFailedExecutionEmitsTokenUsageMsg is the DoD token-telemetry test:
// a failed $hot execution (the cloud model returned conversational text the
// changeset pipeline could not map → ErrAmbiguousChange → local fallback also
// failed) MUST still emit a TokenUsageMsg carrying the provider-reported counts,
// and the status bar token counter must increment (> 0) — it must never stay
// stuck at "0 tok (0%)".
func TestHotfixFailedExecutionEmitsTokenUsageMsg(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	if err := os.WriteFile(indexPath, []byte(hotfixIndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			{
				// Conversational output the changeset pipeline cannot map:
				// no diff headers, no code fences, no matchable anchor.
				Content:     "I have analyzed the file but cannot make changes.",
				TokenInput:  2873,
				TokenOutput: 107,
			},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "produce a summary of the landing page state",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Err == nil {
		t.Fatal("expected the changeset pipeline to pause on unmappable output")
	}
	if result.TokenInput != 2873 || result.TokenOutput != 107 {
		t.Fatalf("provider token usage lost on the failed hotfix: got %d/%d, want 2873/107",
			result.TokenInput, result.TokenOutput)
	}

	// Deliver the failed hotfix proposal through the event loop. The handler
	// MUST dispatch a TokenUsageMsg on the failure path (not swallow usage).
	res, cmd := m.Update(result)
	m2 := res.(*model)
	msgs := drainCmds(t, cmd)

	var tokenMsg TokenUsageMsg
	found := false
	for _, em := range msgs {
		if tm, ok := em.(TokenUsageMsg); ok {
			tokenMsg = tm
			found = true
		}
	}
	if !found {
		t.Fatalf("TokenUsageMsg was not dispatched after the failed $hot execution (messages: %d)", len(msgs))
	}
	if tokenMsg.PromptTokens+tokenMsg.CompletionTokens <= 0 {
		t.Fatalf("TokenUsageMsg carried zero tokens: %+v", tokenMsg)
	}
	if tokenMsg.PromptTokens != 2873 || tokenMsg.CompletionTokens != 107 {
		t.Errorf("TokenUsageMsg counts = %d/%d, want 2873/107", tokenMsg.PromptTokens, tokenMsg.CompletionTokens)
	}

	// Process the TokenUsageMsg: session counters accumulate and the status
	// bar footer (TotalTokens) reflects the consumed tokens immediately.
	_, cmd2 := m2.Update(tokenMsg)
	if cmd2 != nil {
		t.Fatalf("TokenUsageMsg handler returned an unexpected cmd")
	}
	if m2.TotalTokens <= 0 {
		t.Fatalf("status bar token counter did not increment: TotalTokens=%d", m2.TotalTokens)
	}
	if m2.InputTokens != 2873 || m2.OutputTokens != 107 {
		t.Errorf("session token counters = %d/%d, want 2873/107", m2.InputTokens, m2.OutputTokens)
	}
}
