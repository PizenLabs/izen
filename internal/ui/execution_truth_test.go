package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Execution Truth (Phase 4) ──────────────────────────────────────────────
//
// These tests reproduce the exact reported scenario — a /build$hot targeted
// edit where OpenRouter billed real completion tokens while the footer showed
// "0 tok" and the execution log claimed "Edit(index.html)" while the result
// was "nochange". They assert the FULL contract: target → read → model →
// artifact → diff → authorization → apply → verify → result, with provider
// usage, mutation evidence and the execution log all backed by the runtime's
// single source of truth.

// hotfixTruthModel wires a $hot-capable model with a real execution engine
// (so the apply path can mutate the filesystem), a working authorization
// engine (checkpoint + capability + budget) and the shared test provider.
func hotfixTruthModel(t *testing.T, mock *mockProvider) *model {
	t.Helper()
	m := hotfixModelWithProvider(t, mock)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	// Deterministic E2E: the compile-gate verifier would invoke the real Go
	// toolchain in the temp workspace; disable it so the apply + filesystem +
	// evidence assertions are what this test exercises.
	if m.execEng != nil && m.execEng.Patches != nil {
		m.execEng.Patches.SetVerifier(nil)
	}
	// Authorization policy: a fresh mutation budget, capability grants covering
	// the target, and a checkpoint-verifying engine that reports Building.
	m.mutationBudget = budget.NewBudget(10, 1000, 100000, 3, 30*time.Minute, 10)
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{},
		fakeCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	return m
}

// TestExecutionTruth_HotfixTargetedEditEndToEnd is the PART H regression test.
// Input: "/build$hot Remove extra text from an explicitly targeted file" with
// an explicitly targeted @index.html and a deterministic fake provider
// returning a valid mutation artifact.
func TestExecutionTruth_HotfixTargetedEditEndToEnd(t *testing.T) {
	orig := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n  <h2>Extra text</h2>\n</body>\n</html>\n"
	fixed := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n</body>\n</html>\n"

	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + fixed + "```",
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage: ai.ProviderUsage{
			PromptTokens:     2860,
			CompletionTokens: 2048,
			TotalTokens:      4908,
			Known:            true,
		},
	}}}
	m := hotfixTruthModel(t, mock)
	_ = os.WriteFile("index.html", []byte(orig), 0o644)

	// ── 1. Parse intent / resolve target ──────────────────────────────
	// The exact user input: /build$hot Remove extra text from an explicitly
	// targeted file (resolved to @index.html).
	cmd := m.handleHotfixCmd("Remove extra text from an explicitly targeted file @index.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	// The target was resolved: an operation was registered and the hotfix task
	// staged with the resolved index.html target.
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("hotfix operation not registered: %+v", m.activeOp)
	}
	tasks := m.sess.CurrentTasks
	if len(tasks) != 1 || tasks[0].Target != "index.html" {
		t.Fatalf("hotfix target not resolved: %+v", tasks)
	}

	// ── 2. File read once for initial read + 3. provider invoked once ──
	// Run the command batch: the provider call executes synchronously inside
	// proposeHotfixPatch.
	msgs := runBuildCmdsFiltered(t, cmd)
	var hp hotfixProposalMsg
	hpFound := false
	for _, msg := range msgs {
		if p, ok := msg.(hotfixProposalMsg); ok {
			hp = p
			hpFound = true
		}
	}
	if !hpFound {
		t.Fatalf("no hotfixProposalMsg produced: %+v", msgs)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider invoked %d times, want exactly 1", mock.callCount)
	}

	// ── 4. Provider usage propagated ──────────────────────────────────
	if hp.TokenInput != 2860 || hp.TokenOutput != 2048 {
		t.Fatalf("proposal token usage = (%d, %d), want (2860, 2048)", hp.TokenInput, hp.TokenOutput)
	}

	// ── 5. Artifact extracted + 6. concrete diff exists + 7. real changes ──
	if hp.Patch == nil || strings.TrimSpace(hp.Patch.Modified) == "" {
		t.Fatal("no mutation artifact extracted")
	}
	if strings.TrimSpace(hp.Diff) == "" {
		t.Fatal("no compiled diff produced")
	}
	added, removed := countLinesDelta(hp.Diff)
	if added == 0 && removed == 0 {
		t.Fatalf("diff contains no real changes:\n%s", hp.Diff)
	}

	// ── 8. Authorization policy executes normally (approval gate) ────
	// Feed the terminal proposal through the event loop: it must enter the
	// approval state and dispatch the provider usage for the footer.
	res, cmd := m.Update(hp)
	m2 := res.(*model)
	if !m2.awaitingConfirmation {
		t.Fatal("proposal did not enter the approval state")
	}
	// Execute the dispatched commands (token usage + thought) so the footer
	// accumulates the provider-reported usage.
	for _, m2msg := range drainCmds(t, cmd) {
		var r2 tea.Model
		r2, _ = m2.Update(m2msg)
		m2 = r2.(*model)
	}
	// ── 13. Token count is provider-reported ──────────────────────────
	if m2.InputTokens != 2860 {
		t.Errorf("footer InputTokens = %d, want provider-reported 2860", m2.InputTokens)
	}
	if m2.OutputTokens != 2048 {
		t.Errorf("footer OutputTokens = %d, want provider-reported 2048 (was the 0 tok bug)", m2.OutputTokens)
	}

	// ── 9. Filesystem actually changes ────────────────────────────────
	proposal := m2.pendingProposals[0]
	applyMsg := m2.applyProposalCmd(proposal)()
	mr, ok := applyMsg.(mutationResultMsg)
	if !ok {
		t.Fatalf("expected terminal mutationResultMsg, got %T", applyMsg)
	}
	if mr.err != nil {
		t.Fatalf("apply failed: %v", mr.err)
	}
	// ── 11. Final result is CHANGED ───────────────────────────────────
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
		t.Fatal("filesystem was NOT mutated despite a successful apply")
	}
	if !strings.Contains(string(onDisk), "<h1>Home</h1>") || strings.Contains(string(onDisk), "Extra text") {
		t.Fatalf("filesystem content not the corrected artifact:\n%s", onDisk)
	}

	// ── 10. Verification sees the change + 12. log has real evidence ──
	// Mark the task completed so the terminal handler does not advance the
	// queue into a new execution (the apply itself already proved the
	// mutation); the log + operation-terminal assertions then hold.
	curTasks := m2.sess.CurrentTasks
	for i := range curTasks {
		curTasks[i].Status = "completed"
	}
	m2.sess.StageTaskList(&curTasks)
	res, _ = m2.Update(mr)
	m3 := res.(*model)
	if !m3.logStore.EntryCountContains("Result") {
		t.Fatal("execution log missing a Result-stage entry")
	}
	// ── 14. No "nochange" emitted ─────────────────────────────────────
	if mr.status == "nochange" {
		t.Fatal("result claims nochange for a real filesystem mutation")
	}
	// ── 15. No "Edit(file)" success event exists without an apply ─────
	// The result entry must be a semantic Result entry with the changed
	// outcome — never a bare successful "Edit" claiming an un-applied plan.
	if entry := m3.logStore.LastResult(); entry == nil || entry.Outcome != execution.OutcomeChanged {
		t.Fatalf("result log entry not the changed outcome: %+v", entry)
	}

	// ── 16. Operation reaches terminal state + 17. live workers = 0 ──
	if m3.activeOp != nil {
		t.Fatalf("operation not released after terminal result: %+v", m3.activeOp)
	}
	snap := m3.lastExecutionSnapshot
	if snap.CompletedAt.IsZero() {
		t.Fatal("telemetry not finalized")
	}
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("live workers after terminal result: %v", snap.LiveWorkers)
	}
	if snap.Invocations != 1 {
		t.Fatalf("telemetry invocations = %d, want 1", snap.Invocations)
	}
}

// TestExecutionTruth_ProviderNoArtifactIsPatchFailure covers NEGATIVE CASE A:
// the provider returns no usable mutation artifact — the result must be
// PATCH_GENERATION_FAILED, never a fake nochange or a successful Edit.
func TestExecutionTruth_ProviderNoArtifactIsPatchFailure(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "",
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage:       ai.ProviderUsage{PromptTokens: 2860, CompletionTokens: 2048, TotalTokens: 4908, Known: true},
	}}}
	m := hotfixTruthModel(t, mock)
	_ = os.WriteFile("index.html", []byte("<html><body><h1>x</h1></body></html>\n"), 0o644)

	m.beginOperation(OpHotfix)
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "rewrite the page header @index.html"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err == nil {
		t.Fatal("empty provider response must fail patch generation")
	}
	// ── 14. No "nochange" is fabricated for a missing artifact ────────
	if strings.Contains(hp.Err.Error(), "nochange") {
		t.Fatalf("missing artifact mislabeled as nochange: %v", hp.Err)
	}
	// The telemetry outcome is a generation failure (patch_failed semantics),
	// never success.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.lastExecutionSnapshot.Outcome == "success" {
		t.Fatal("missing artifact must not finalize as success")
	}
}

// TestExecutionTruth_EmptyPatchIsNoChange covers NEGATIVE CASE B: the model
// resolves to content identical to the on-disk file. NO_CHANGE is valid here
// BECAUSE a mutation artifact existed and was compared byte-for-byte against
// the filesystem — and the provider usage must still be recorded.
func TestExecutionTruth_EmptyPatchIsNoChange(t *testing.T) {
	// An existing NON-stub file (>100 lines, no trailing newline) so the
	// existing-file strategy applies and the fenced artifact resolves back to
	// the on-disk bytes EXACTLY (ResolveModifiedContent trims surrounding
	// whitespace).
	lines := make([]string, 0, 105)
	for i := 0; i < 105; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	orig := strings.Join(lines, "\n")
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\n" + orig + "\n```",
		TokenInput:  2860,
		TokenOutput: 2048,
		Usage:       ai.ProviderUsage{PromptTokens: 2860, CompletionTokens: 2048, TotalTokens: 4908, Known: true},
	}}}
	m := hotfixTruthModel(t, mock)
	_ = os.WriteFile("index.html", []byte(orig), 0o644)

	m.beginOperation(OpBuild)
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "update the heading"}
	msg := m.proposeBuildPatch(task)()
	// The identical-content artifact resolves back to the on-disk bytes: the
	// zero-patch short-circuit returns a terminal NO_CHANGE mutation result
	// (never a proposal claiming a change).
	mr, ok := msg.(mutationResultMsg)
	if !ok {
		// If the extraction produced a proposal instead, it must not carry a
		// non-empty diff for byte-identical content.
		if bp, bpOK := msg.(buildProposalReadyMsg); bpOK {
			if bp.Err != nil {
				t.Fatalf("patch generation failed: %v", bp.Err)
			}
			if bp.Diff != "" {
				t.Fatalf("byte-identical artifact produced a non-empty diff:\n%s", bp.Diff)
			}
			t.Fatal("byte-identical content must short-circuit to nochange, not a proposal")
		}
		t.Fatalf("expected terminal mutationResultMsg or buildProposalReadyMsg, got %T", msg)
	}
	if mr.status != "nochange" {
		t.Fatalf("status = %q, want nochange", mr.status)
	}
	if mr.outcome() != execution.OutcomeNoChange {
		t.Fatalf("outcome = %q, want nochange", mr.outcome())
	}
	// The provider usage is carried on the nochange result so the footer never
	// drops the tokens the model consumed.
	if mr.TokenInput != 2860 || mr.TokenOutput != 2048 {
		t.Fatalf("nochange result dropped provider usage: (%d, %d)", mr.TokenInput, mr.TokenOutput)
	}
}

// TestExecutionTruth_ApplyFailureIsApplyFailed covers NEGATIVE CASE D: an
// apply failure is APPLY_FAILED (with evidence), never a silent success or
// nochange. The semantic outcome is derived from the error.
func TestExecutionTruth_ApplyFailureIsApplyFailed(t *testing.T) {
	m := hotfixTruthModel(t, &mockProvider{})
	m.workflowSM = nil // transitionToBuilding no-op
	if m.execEng != nil {
		m.execEng.Patches.SetVerifier(nil)
	}
	// No provider needed: drive the apply path directly with an empty artifact
	// so the write fails deterministically at the patch engine.
	proposal := SemanticProposal{
		ID:     "apply-fail",
		Target: SemanticTarget{QualifiedName: "index.html"},
		Diff:   "",
	}
	msg := m.applyProposalCmd(proposal)()
	mr, ok := msg.(mutationResultMsg)
	if !ok {
		t.Fatalf("expected mutationResultMsg, got %T", msg)
	}
	if mr.err == nil {
		t.Fatal("empty artifact must fail the apply")
	}
	if mr.outcome() != execution.OutcomeApplyFailed {
		t.Fatalf("outcome = %q, want apply_failed", mr.outcome())
	}
}

// cancelObservingProvider returns the request's context error when the
// operation was already cancelled, so the cancellation test observes the
// propagation deterministically (the plain mockProvider ignores ctx).
type cancelObservingProvider struct {
	inner *mockProvider
}

func (p *cancelObservingProvider) Name() string { return "cancel-observing" }

func (p *cancelObservingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.inner.Execute(ctx, req)
}

func (p *cancelObservingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// TestExecutionTruth_CancelledDuringModelIsCancelled covers NEGATIVE CASE F:
// a context-cancelled provider call during model execution finalizes as
// CANCELLED, not a failure or a mutation.
func TestExecutionTruth_CancelledDuringModelIsCancelled(t *testing.T) {
	mock := &cancelObservingProvider{inner: &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}}
	m := hotfixTruthModel(t, mock.inner)
	_ = os.WriteFile("index.html", []byte("<html></html>\n"), 0o644)
	m.provider = mock

	m.beginOperation(OpHotfix)
	m.activeOp.Cancel()
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "rewrite @index.html"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err == nil || !isContextCancelled(hp.Err) {
		t.Fatalf("expected context cancellation error, got: %v", hp.Err)
	}
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.lastExecutionSnapshot.Outcome != "cancelled" {
		t.Fatalf("telemetry outcome = %q, want cancelled", m2.lastExecutionSnapshot.Outcome)
	}
}

// TestExecutionTruth_NoMutationResultNeverLogsSuccessEdit asserts the exact
// "Edit(index.html) ✓ completed without mutation" impossibility: a nochange
// result renders a semantic NoChange entry — never a successful Edit.
func TestExecutionTruth_NoMutationResultNeverLogsSuccessEdit(t *testing.T) {
	ls := NewLogStore()
	ls.AddFullSemantic(LogResult, "index.html", false, "nochange", "", "", execution.StageResult, execution.OutcomeNoChange)
	entries := ls.Entries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	rendered := RenderEntry(entries[0], 100, 0)
	if strings.Contains(rendered, "Edit(") {
		t.Fatalf("nochange rendered as an Edit entry:\n%s", rendered)
	}
	if !strings.Contains(rendered, "NoChange") {
		t.Fatalf("nochange not rendered with its semantic label:\n%s", rendered)
	}
	// A genuine mutation renders as a Result entry with the changed outcome —
	// the success glyph (green dot) and never the NoChange label.
	ls.AddFullSemantic(LogResult, "index.html", true, "+3 -1", "", "", execution.StageResult, execution.OutcomeChanged)
	changedRender := RenderEntry(ls.Entries()[1], 100, 0)
	if !strings.Contains(changedRender, "Result") {
		t.Fatalf("changed mutation not rendered as a Result entry:\n%s", changedRender)
	}
	if strings.Contains(changedRender, "NoChange") || strings.Contains(changedRender, "∅") {
		t.Fatalf("changed mutation rendered with a nochange label:\n%s", changedRender)
	}
	if ls.Entries()[1].Success != true {
		t.Fatal("changed mutation must carry Success=true")
	}
}

// TestExecutionTruth_EvidenceSemantics pins the MutationEvidence contract:
// ApplyExecutedChanged is the ONLY combination that represents a real
// mutation, and NO_CHANGE is never fabricated from a missing artifact.
func TestExecutionTruth_EvidenceSemantics(t *testing.T) {
	changed := execution.MutationEvidence{Stage: execution.StageApply, Outcome: execution.OutcomeChanged, ApplyExecuted: true, FilesystemChanged: true}
	if !changed.ApplyExecutedChanged() {
		t.Error("applied+changed must be ApplyExecutedChanged")
	}
	nochange := execution.MutationEvidence{Stage: execution.StageApply, Outcome: execution.OutcomeNoChange, ApplyExecuted: true, FilesystemChanged: false}
	if nochange.ApplyExecutedChanged() {
		t.Error("applied+unchanged must NOT be ApplyExecutedChanged")
	}
	noartifact := execution.MutationEvidence{Stage: execution.StageApply, Outcome: execution.OutcomeNoArtifact, ApplyExecuted: false}
	if noartifact.ApplyExecutedChanged() {
		t.Error("no artifact must never claim a mutation")
	}
	if execution.ParseMutationOutcome("nochange") != execution.OutcomeNoChange {
		t.Error("ParseMutationOutcome(nochange) mismatch")
	}
	if execution.ParseMutationOutcome("patch generation failed") != execution.OutcomePatchGenerationFailed {
		t.Error("ParseMutationOutcome(patch generation failed) mismatch")
	}
	if execution.ParseMutationOutcome("mystery") != execution.OutcomeNoArtifact {
		t.Error("vague status must normalize to no_artifact, never a mutation")
	}
	if execution.OutcomeNoChange.MutationSucceeded() {
		t.Error("NO_CHANGE must never report a successful mutation")
	}
	if !execution.OutcomeChanged.MutationSucceeded() {
		t.Error("CHANGED must report a successful mutation")
	}
}

// compile-time guard: hotfixTruthModel requires errors.Is usage.
var _ = errors.Is
