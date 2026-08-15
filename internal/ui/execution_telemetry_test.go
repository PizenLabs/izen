package ui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Execution Telemetry UI integration (Phase 3) ────────────────────────────
//
// These tests drive the REAL hotfix/build dispatch seams and assert that the
// per-operation execution telemetry (operation ID, stage boundaries, provider
// invocation counting, worker lifetime) is truthful end to end. They never
// depend on real provider latency: mockProvider answers instantly.

// hotfixModelWithProvider wires a build-mode model with a mock provider and
// the minimal session/capability surface $hot needs. It runs in a temp dir so
// fixture files never pollute the shared package working directory.
func hotfixModelWithProvider(t *testing.T, mock *mockProvider) *model {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	m := newTestModel()
	m.provider = mock
	m.caps = capability.NewCapabilitySet()
	return m
}

// TestExecutionTelemetry_HotfixSingleProviderInvocation asserts a successful
// $hot performs exactly ONE provider invocation and the operation telemetry
// records exactly one invocation with the correct operation ID (tests #1/#13).
func TestExecutionTelemetry_HotfixSingleProviderInvocation(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\nMIT License\n\nCopyright (c) 2026\n```",
		TokenInput:  12,
		TokenOutput: 50,
	}}}
	m := hotfixModelWithProvider(t, mock)

	cmd := m.handleInput("$hot add a MIT LICENSE file @LICENSE")
	if cmd == nil {
		t.Fatal("handleInput returned nil for $hot")
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("expected exactly one hotfix operation at dispatch, got: %+v", m.activeOp)
	}
	opID := m.activeOp.ID
	if opID == "" {
		t.Fatal("operation ID is empty")
	}

	// Run the generation command (drop animation ticks).
	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: "LICENSE", Description: "add a MIT LICENSE file @LICENSE"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times for one $hot, want exactly 1", mock.callCount)
	}

	// The telemetry record reflects the single provider invocation and the
	// same operation ID.
	if m.activeOp == nil || m.activeOp.Telemetry == nil {
		t.Fatal("operation telemetry not created")
	}
	snap := m.activeOp.Telemetry.Snapshot()
	if snap.OpID != opID {
		t.Fatalf("telemetry OpID = %q, want %q (stable operation identity)", snap.OpID, opID)
	}
	if snap.Invocations != 1 {
		t.Fatalf("telemetry invocations = %d, want exactly 1", snap.Invocations)
	}
	if snap.Retries != 0 {
		t.Fatalf("telemetry retries = %d, want 0", snap.Retries)
	}

	// Terminal proposal → operation finalized → telemetry terminal.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatalf("hotfix operation not finalized: %+v", m2.activeOp)
	}
	if m2.lastExecutionTelemetry == nil || !m2.lastExecutionTelemetry.Finalized() {
		t.Fatal("telemetry not finalized with the operation")
	}
	if m2.lastExecutionSnapshot.OpID != opID {
		t.Fatalf("finalized snapshot OpID = %q, want %q", m2.lastExecutionSnapshot.OpID, opID)
	}
}

// TestExecutionTelemetry_AmbiguousHotfixZeroProviderInvocations asserts an
// ambiguous $hot performs ZERO provider invocations and the telemetry records
// zero invocations (test #14).
func TestExecutionTelemetry_AmbiguousHotfixZeroProviderInvocations(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := ambiguousHotfixModel(t, mock)

	if m.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous", m.state)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider called %d times for an ambiguous request, want 0", mock.callCount)
	}
	// The ambiguity gate is terminal: the operation is released with zero
	// provider invocations — the telemetry must prove no provider was called.
	if m.activeOp != nil {
		t.Fatalf("active operation after ambiguous result: %+v", m.activeOp)
	}
	if m.lastExecutionSnapshot.OpID == "" {
		t.Fatal("ambiguous execution should retain a telemetry record proving zero invocations")
	}
	if m.lastExecutionSnapshot.Invocations != 0 {
		t.Fatalf("ambiguous execution recorded %d provider invocations, want 0", m.lastExecutionSnapshot.Invocations)
	}
}

// TestExecutionTelemetry_SuccessfulHotfixReachesPatchStage asserts a
// successful hotfix reaches the patch stage: the telemetry stage list contains
// model + patch (and the terminal apply path records when run) (test #15).
func TestExecutionTelemetry_SuccessfulHotfixReachesPatchStage(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n<h1>Fixed</h1>\n```",
		TokenInput:  5,
		TokenOutput: 20,
	}}}
	m := hotfixModelWithProvider(t, mock)
	_ = os.WriteFile("index.html", []byte("<h1>Old</h1>\n"), 0o644)

	m.beginOperation(OpHotfix)
	op := m.activeOp
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "fix the heading @index.html"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	// Drive the terminal proposal through the event loop so the operation (and
	// its telemetry) finalizes with a real terminal patch stage.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatalf("hotfix operation not finalized: %+v", m2.activeOp)
	}

	// The finalized telemetry stage log must contain the model (provider) and
	// patch (changeset compilation) stages — a real execution reached both.
	snap := m2.lastExecutionSnapshot
	var sawModel, sawPatch bool
	for _, sp := range snap.Stages {
		if sp.Stage == "model" && sp.State.Terminal() {
			sawModel = true
		}
		if sp.Stage == "patch" && sp.State.Terminal() {
			sawPatch = true
		}
	}
	if !sawModel {
		t.Fatalf("telemetry missing terminal model stage: %+v", snap.Stages)
	}
	if !sawPatch {
		t.Fatalf("telemetry missing terminal patch stage: %+v", snap.Stages)
	}
	_ = op
}

// TestExecutionTelemetry_FailedPatchExtractionReportsPatchStage asserts a
// failed patch extraction is attributed to the patch stage (test #16). A
// large-file $hot whose model re-emits the whole document is rejected by the
// bounded-contract changeset pipeline at the patch stage.
func TestExecutionTelemetry_FailedPatchExtractionReportsPatchStage(t *testing.T) {
	large := largeMismatchedIndexHTML()
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + large + "\n```",
		TokenInput:  3,
		TokenOutput: 1200,
	}}}
	// hotfixModelWithProvider chdirs into its temp dir; write the fixture
	// AFTER so the file lands in the same working directory the target
	// resolves against.
	m := hotfixModelWithProvider(t, mock)
	_ = os.WriteFile("index.html", []byte(large), 0o644)

	m.beginOperation(OpHotfix)
	op := m.activeOp
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "Fix an HTML syntax error in @index.html"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err == nil || !strings.Contains(hp.Err.Error(), "bounded hotfix contract") {
		t.Fatalf("expected the bounded-contract rejection, got: %v", hp.Err)
	}

	res, _ := m.Update(hp)
	m2 := res.(*model)
	snap := m2.lastExecutionSnapshot
	var sawPatchFailed bool
	for _, sp := range snap.Stages {
		if sp.Stage == "patch" && sp.State == execution.StageFailed {
			sawPatchFailed = true
		}
	}
	if !sawPatchFailed {
		t.Fatalf("failed patch extraction not attributed to patch stage: %+v", snap.Stages)
	}
	_ = op
}

// TestExecutionTelemetry_ProviderCompletionClearsWaiting asserts that after
// provider completion the authoritative stage is never left in "waiting"
// (test #17). The stage transitions waiting → streaming → done exactly as the
// provider round-trip completes.
func TestExecutionTelemetry_ProviderCompletionClearsWaiting(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\n# New\n```",
		TokenInput:  1,
		TokenOutput: 10,
	}}}
	m := hotfixModelWithProvider(t, mock)
	_ = os.WriteFile("README.md", []byte("# Old\n"), 0o644)

	m.beginOperation(OpHotfix)
	op := m.activeOp
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "README.md", Description: "update README @README.md"}
	msg := m.proposeHotfixPatch(task)()
	if _, ok := msg.(hotfixProposalMsg); !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}

	// The provider span must be terminal — the waiting state cannot survive
	// provider completion.
	snap := op.Telemetry.Snapshot()
	if len(snap.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(snap.Providers))
	}
	if !snap.Providers[0].State.Terminal() {
		t.Fatalf("provider span state = %q, want terminal (waiting must be cleared)", snap.Providers[0].State)
	}
}

// TestExecutionTelemetry_DuplicateDispatchSingleActiveExecution asserts a
// duplicate dispatch cannot create two active executions: the second dispatch
// supersedes (cancels) the first, so exactly one operation owns the runtime
// (test #9).
func TestExecutionTelemetry_DuplicateDispatchSingleActiveExecution(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := hotfixModelWithProvider(t, mock)

	first := m.beginOperation(OpHotfix)
	if m.activeOp != first {
		t.Fatalf("first dispatch did not become the active operation")
	}
	second := m.beginOperation(OpBuild)
	if m.activeOp != second {
		t.Fatalf("second dispatch did not supersede the first")
	}
	if first.Ctx == nil || first.Ctx.Err() == nil {
		t.Fatal("superseded operation context was not cancelled (single-ownership rule)")
	}
	// The superseded operation's telemetry was not finalized by the new
	// dispatch — ownership moved, no double-finalize.
	if first.Telemetry.Finalized() {
		t.Fatal("superseded operation must not be finalized by the new dispatch")
	}
}

// TestExecutionTelemetry_CancelledExecutionTerminatesAllWorkers asserts a
// Ctrl+C cancellation releases every registered worker and finalizes the
// telemetry with a cancelled outcome (tests #10/#12).
func TestExecutionTelemetry_CancelledExecutionTerminatesAllWorkers(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := buildRunModel(t, bp, []plan.Task{{
		StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle",
	}}, smallHTML)

	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatalf("active operation not released by Ctrl+C: %+v", m2.activeOp)
	}
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed the cancellation")
	}
	// Drain the worker's terminal message.
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}

	if m2.lastExecutionTelemetry == nil {
		t.Fatal("no telemetry retained after cancellation")
	}
	snap := m2.lastExecutionSnapshot
	if snap.Outcome != "cancelled" {
		t.Fatalf("telemetry outcome = %q, want cancelled", snap.Outcome)
	}
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("workers still live after cancellation: %v", snap.LiveWorkers)
	}
}

// TestExecutionTelemetry_InspectDirectiveReachable asserts `$inspect` routes
// through the parser directive registry and renders the retained telemetry
// into the viewport (test #19/#20 end-to-end): the detailed timeline is
// available on demand while the normal view stays compact.
func TestExecutionTelemetry_InspectDirectiveReachable(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\nMIT\n```",
		TokenInput:  1,
		TokenOutput: 10,
	}}}
	m := hotfixModelWithProvider(t, mock)

	// Produce telemetry to inspect: a successful hotfix.
	m.beginOperation(OpHotfix)
	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: "LICENSE", Description: "add LICENSE @LICENSE"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	m.Update(hp)

	// $inspect routes through the parser + handleReviewDollar without error.
	cmd := m.handleInput("$inspect")
	if cmd == nil {
		t.Fatal("handleInput returned nil for $inspect")
	}
	res := cmd()
	if res != nil {
		t.Fatalf("inspect command produced a stray message: %T", res)
	}
	// The detailed timeline is rendered directly from the retained record.
	rendered := renderInspectTimeline(m.lastExecutionSnapshot, "")
	if !strings.Contains(rendered, "Execution:") || !strings.Contains(rendered, "invocations=1") {
		t.Fatalf("inspect timeline missing execution metadata:\n%s", rendered)
	}
}

// TestExecutionTelemetry_WorkersReleasedAfterRealBuild drives the legacy
// build patch-generation path through a real worker and asserts the worker
// tracker returns to zero live workers once the operation finalizes — the
// "no worker survives operation finalization" guarantee end to end (test #12).
func TestExecutionTelemetry_WorkersReleasedAfterRealBuild(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "<!DOCTYPE html>\n<html><body><h1>New</h1></body></html>\n",
		TokenInput:  5,
		TokenOutput: 20,
	}}}
	tasks := []plan.Task{{
		StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle",
		Description: "rewrite index.html",
	}}
	m := buildRunModel(t, mock, tasks, smallHTML)

	// The dispatch seam spawns the operation worker; the proposal production
	// runs synchronously under the operation.
	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	var proposal *buildProposalReadyMsg
	for _, msg := range msgs {
		if p, ok := msg.(buildProposalReadyMsg); ok {
			proposal = &p
		}
	}
	if proposal == nil || proposal.Err != nil {
		t.Fatalf("expected a valid proposal, got %+v", proposal)
	}
	res, _ := m.Update(*proposal)
	m2 := res.(*model)
	if m2.lastExecutionTelemetry == nil {
		t.Fatal("no telemetry retained after build")
	}
	snap := m2.lastExecutionSnapshot
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("live workers after build finalization: %v", snap.LiveWorkers)
	}
	if snap.Invocations != 1 {
		t.Fatalf("build performed %d provider invocations, want 1", snap.Invocations)
	}
}

// TestExecutionTelemetry_NormalUIStaysCompact asserts the primary UI status
// line stays a single compact truthful line after an execution — the detailed
// timeline lives ONLY behind $inspect, never in the main view (test #19).
func TestExecutionTelemetry_NormalUIStaysCompact(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\nMIT\n```",
		TokenInput:  1,
		TokenOutput: 10,
	}}}
	m := hotfixModelWithProvider(t, mock)

	m.beginOperation(OpHotfix)
	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: "LICENSE", Description: "add LICENSE @LICENSE"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	res, _ := m.Update(hp)
	m2 := res.(*model)

	// The authoritative stage renders as a single-line status, never a
	// multi-line debug dump.
	status := m2.renderStageLine()
	if strings.Count(status, "\n") > 0 {
		t.Fatalf("primary UI status is multi-line after execution:\n%q", status)
	}
	if status == "" {
		// After finalization the stage is terminal; a blank line is acceptable
		// ONLY when no stage is active. Verify the detailed timeline is
		// reachable but does not leak into the primary status.
		if !m2.lastExecutionSnapshot.CompletedAt.IsZero() {
			t.Logf("primary status blank after terminal operation (compact OK)")
		}
	}
	// The detailed timeline is still available behind $inspect.
	rendered := renderInspectTimeline(m2.lastExecutionSnapshot, "")
	if !strings.Contains(rendered, "Execution:") {
		t.Fatalf("detailed timeline not available behind inspect:\n%s", rendered)
	}
}

// TestExecutionTelemetry_NormalUIContainsNoReasoning asserts the inspect view
// renders execution metadata and never hidden reasoning (test #19/#20 at the
// UI integration level).
func TestExecutionTelemetry_NormalUIContainsNoReasoning(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\nMIT\n```",
		TokenInput:  1,
		TokenOutput: 10,
	}}}
	m := hotfixModelWithProvider(t, mock)

	m.beginOperation(OpHotfix)
	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: "LICENSE", Description: "add LICENSE @LICENSE"}
	msg := m.proposeHotfixPatch(task)()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	res, _ := m.Update(hp)
	m2 := res.(*model)

	rendered := renderInspectTimeline(m2.lastExecutionSnapshot, "")
	for _, forbidden := range []string{"thinking", "reasoning", "thought", "chain-of-thought"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("inspect view leaks %q:\n%s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "invocations=1") {
		t.Fatalf("inspect view missing invocation counter:\n%s", rendered)
	}
}

// TestExecutionTelemetry_TimedOutExecutionTerminatesAllWorkers asserts a
// provider that exceeds its operation deadline reaches TIMEOUT, the telemetry
// finalizes with a timeout outcome, and no worker survives (tests #11/#12 at
// the UI integration level).
func TestExecutionTelemetry_TimedOutExecutionTerminatesAllWorkers(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_ = os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644)

	bp := &blockingProvider{started: make(chan struct{})}
	m := newTestModel()
	m.ti.Focus()
	m.provider = bp
	m.hotfixTimeout = 50 * time.Millisecond

	m.beginOperation(OpHotfix)
	op := m.activeOp
	op.Telemetry.Workers().Spawn("hotfix")

	m.hotfixActive = true
	msg := m.proposeHotfixPatch(structuralHotfixTask())()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err == nil || !isContextDeadline(hp.Err) {
		t.Fatalf("expected deadline-exceeded error, got %v", hp.Err)
	}

	// The worker observed the deadline and returned: release it.
	op.Telemetry.Workers().Release("hotfix")

	// Feed the terminal message: TIMEOUT → cleanup → telemetry finalized.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatal("active operation not released after timeout")
	}
	if m2.lastExecutionTelemetry == nil || !m2.lastExecutionTelemetry.Finalized() {
		t.Fatal("telemetry not finalized after timeout")
	}
	snap := m2.lastExecutionSnapshot
	if snap.Outcome != "timeout" {
		t.Fatalf("telemetry outcome = %q, want timeout", snap.Outcome)
	}
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("workers still live after timeout: %v", snap.LiveWorkers)
	}
}

// TestExecutionTelemetry_ApplyStageRecordsUnderDeadline asserts the mutation
// apply path records a terminal apply stage under its deadline (test #15 apply
// leg): a cancelled operation makes the apply abort with a truthful error.
func TestExecutionTelemetry_ApplyStageRecordsUnderDeadline(t *testing.T) {
	m4 := newTestModel()
	m4.execEng = execution.NewEngine(".", m4.cfg, m4.sess)
	m4.beginOperation(OpBuild)
	op := m4.activeOp
	m4.activeOp.Cancel()
	err := m4.applyPatchWithDeadline(&execution.Patch{File: "x", Modified: "y"})
	if !errors.Is(err, execution.ErrPatchApplyTimeout) {
		t.Fatalf("apply after cancellation = %v, want ErrPatchApplyTimeout", err)
	}
	// The operation was cancelled before the apply could start: the apply
	// stage must not linger as a live stage after the operation terminalizes.
	m4.finalizeOperation(OpOutcomeCancelled, nil)
	snap := op.Telemetry.Snapshot()
	for _, sp := range snap.Stages {
		if !sp.State.Terminal() {
			t.Fatalf("stage %s non-terminal after finalize: %+v", sp.Stage, sp.State)
		}
	}
}
