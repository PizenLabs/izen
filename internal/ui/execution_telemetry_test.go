package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/modes"
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
// $hot performs exactly ONE provider invocation, owned by the RuntimeExecutor
// (recorded in the runtime proof), and the UI's operation telemetry records
// ZERO invocations — the UI owns no provider calls on the migrated path.
func TestExecutionTelemetry_HotfixSingleProviderInvocation(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```\nMIT License\n\nCopyright (c) 2026\n```",
		TokenInput:  12,
		TokenOutput: 50,
	}}}
	m := gatedDispatchModel(t, mock, nil)
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot add a note file @note.md")
	if cmd == nil {
		t.Fatal("handleInput returned nil for $hot")
	}
	if mock.callCount != 0 {
		t.Fatalf("provider invoked %d times before the execution ran, want 0", mock.callCount)
	}

	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("gate err: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("nil gate result")
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times for one $hot, want exactly 1", mock.callCount)
	}
	// The runtime proof carries the single provider invocation.
	if len(gem.res.Proof.ModelInvocations) != 1 {
		t.Fatalf("proof model invocations = %d, want 1", len(gem.res.Proof.ModelInvocations))
	}
	if gem.res.Proof.ModelInvocations[0].TokenOutput != 50 {
		t.Fatalf("proof token output = %d, want 50 (provider usage is authoritative)", gem.res.Proof.ModelInvocations[0].TokenOutput)
	}

	// Terminal proposal → OpHotfix operation begins at the approval gate.
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.activeOp == nil || m2.activeOp.Kind != OpHotfix {
		t.Fatalf("expected an OpHotfix operation at the proposal terminal, got %+v", m2.activeOp)
	}
	// The UI's operation telemetry records ZERO invocations — the UI made none.
	if snap := m2.activeOp.Telemetry.Snapshot(); snap.Invocations != 0 {
		t.Fatalf("UI telemetry invocations = %d, want 0 (the runtime owns all invocations)", snap.Invocations)
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
	m := gatedDispatchModel(t, mock, nil)
	m.resolver.Set(modes.ModeBuild)

	// Produce telemetry to inspect: a successful $hot through the runtime.
	cmd := m.handleInput("$hot add a note file @note.md")
	if cmd == nil {
		t.Fatal("handleInput returned nil for $hot")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("execution failed: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("nil gate result")
	}
	m.executionResultUpdate(executionResultMsg{res: gem.res})

	// $inspect routes through the parser + handleReviewDollar without error.
	icmd := m.handleInput("$inspect")
	if icmd == nil {
		t.Fatal("handleInput returned nil for $inspect")
	}
	res := icmd()
	if res != nil {
		t.Fatalf("inspect command produced a stray message: %T", res)
	}
	// The detailed timeline is rendered directly from the retained record.
	rendered := renderInspectTimeline(m.lastExecutionSnapshot, "")
	if !strings.Contains(rendered, "Execution:") {
		t.Fatalf("inspect timeline missing execution metadata:\n%s", rendered)
	}
	// The runtime-owned proof carries the single authoritative invocation.
	if proof := renderExecutionProof(m.lastExecutionProof); !strings.Contains(proof, "provider-invocations=1") {
		t.Fatalf("inspect proof missing the authoritative invocation count:\n%s", proof)
	}
}

// TestExecutionTelemetry_WorkersReleasedAfterRealBuild drives the RuntimeExecutor
// build-execution path through the real worker machinery and asserts the worker
// tracker returns to zero live workers once the operation finalizes — the "no
// worker survives operation finalization" guarantee end to end (test #12). On
// the executor path the terminal that finalizes the operation is a terminal
// execution outcome (a failed execution here); a held patch instead pauses at
// the approval gate under a fresh operation, so the finalization guarantee is
// exercised through the failure terminal.
func TestExecutionTelemetry_WorkersReleasedAfterRealBuild(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: ""}}}
	tasks := []plan.Task{{
		StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle",
		Description: "rewrite index.html",
	}}
	m := buildRunModel(t, mock, tasks, smallHTML)

	// The dispatch seam runs the executor synchronously under the operation; an
	// empty model artifact is a deterministic execution failure.
	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	var gem *gatedExecutionMsg
	for _, msg := range msgs {
		if g, ok := msg.(gatedExecutionMsg); ok {
			gem = &g
		}
	}
	if gem == nil || gem.err == nil {
		t.Fatalf("expected a terminal execution failure, got %+v", gem)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times, want exactly 1", mock.callCount)
	}
	res, _ := m.Update(*gem)
	m2 := res.(*model)
	if m2.lastExecutionTelemetry == nil {
		t.Fatal("no telemetry retained after the build execution terminal")
	}
	if !m2.lastExecutionTelemetry.Finalized() {
		t.Fatal("telemetry not finalized after the build execution terminal")
	}
	snap := m2.lastExecutionSnapshot
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("live workers after build finalization: %v", snap.LiveWorkers)
	}
	if snap.Outcome != "failure" {
		t.Fatalf("telemetry outcome = %q, want failure", snap.Outcome)
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
	m := gatedDispatchModel(t, mock, nil)
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot add a note file @note.md")
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("execution failed: %v", gem.err)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
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
	m := gatedDispatchModel(t, mock, nil)
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot add a note file @note.md")
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("execution failed: %v", gem.err)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)

	rendered := renderInspectTimeline(m2.lastExecutionSnapshot, "")
	for _, forbidden := range []string{"thinking", "reasoning", "thought", "chain-of-thought"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("inspect view leaks %q:\n%s", forbidden, rendered)
		}
	}
	// The authoritative invocation count lives in the runtime-owned proof.
	if proof := renderExecutionProof(m2.lastExecutionProof); !strings.Contains(proof, "provider-invocations=1") {
		t.Fatalf("inspect proof missing the authoritative invocation count:\n%s", proof)
	}
}

// TestExecutionTelemetry_WorkersReleasedAfterRealBuild drives the RuntimeExecutor
