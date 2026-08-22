package ui

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Build-lifecycle harness ──────────────────────────────────────────────────
//
// The suite below drives the RuntimeExecutor build-execution path
// (handleBuildRun → runRuntimeTaskRequest → RuntimeExecutor.Execute) — the
// pipeline that replaced the legacy per-task proposeBuildPatch loop. The
// executor owns the provider invocation, patch creation and the approval gate;
// the UI owns input collection, the proposal dock and the runtime result
// projection (gatedExecutionMsg → executionResultUpdate). Ctrl+C cancels the
// operation context the provider call inherits, so the UI can never remain
// stuck on a processing state until a 5-minute timeout.
//
// buildRunModel wires a minimal-but-real model in the Building phase with a
// staged FILE_MUTATE task, a writable target file, an IntentGateway and a
// RuntimeExecutor bound to the test provider, so handleBuildRun exercises its
// full dispatch seam (transition → executor submission → approval gate).

func buildRunModel(t *testing.T, provider ai.Provider, tasks []plan.Task, fileContent string) *model {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if fileContent != "" {
		if err := os.WriteFile("index.html", []byte(fileContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestModel()
	m.state = StateChat
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.provider = provider
	m.caps = capability.NewCapabilitySet()
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.activityTree = NewActivityTree()
	// Wire the runtime boundary (production cutover): the FILE_MUTATE task
	// routes through the RuntimeExecutor, which owns provider invocation,
	// patch creation, the approval gate, apply and verification.
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, provider, nil, "")
	m.sess.StageTaskList(&tasks)
	driveIntoBuildPhase(t, m)
	m.ti.Focus()
	return m
}

// runBuildCmdsFiltered executes a tea.Cmd (expanding nested BatchMsg groups)
// and returns the terminal messages, dropping animation/tick messages so no
// timer goroutine leaks into the test process. It is used for the fast,
// non-blocking lifecycle cases.
func runBuildCmdsFiltered(t *testing.T, c tea.Cmd) []tea.Msg {
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
		if msg == nil {
			continue
		}
		switch msg.(type) {
		case smoothStreamTickMsg, tickMsg, shimmerFrameMsg,
			watchdogMsg, spinnerTickMsg, proTipTickMsg:
			continue
		}
		out = append(out, msg)
	}
	return out
}

// runBuildCmdsFilteredBackground is the async variant used by the cancellation
// tests: the worker goroutine blocks inside the provider call until the
// operation context is cancelled.
func runBuildCmdsFilteredBackground(c tea.Cmd) <-chan tea.Msg {
	ch := make(chan tea.Msg, 16)
	go func() {
		defer close(ch)
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
			if msg == nil {
				continue
			}
			switch msg.(type) {
			case smoothStreamTickMsg, tickMsg, shimmerFrameMsg,
				watchdogMsg, spinnerTickMsg, proTipTickMsg:
				continue
			}
			ch <- msg
		}
	}()
	return ch
}

// goroutineDelta asserts that no worker goroutine remains blocked after a
// cancellation, using a generous tolerance (the test process runs other
// timer/runtime goroutines concurrently).
func goroutineDelta(t *testing.T, baseline int, what string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	now := runtime.NumGoroutine()
	if now > baseline+3 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutine leak after %s: %d goroutines now vs %d baseline\n%s", what, now, baseline, buf[:n])
	}
}

// smallHTML is a stub file (< 100 lines) so StrategyForOriginal selects the
// whole-file overwrite protocol on the FIRST provider attempt.
const smallHTML = "<!DOCTYPE html>\n<html>\n<head>\n  <title>Home</title>\n</head>\n<body>\n  <h1>Hello</h1>\n</body>\n</html>\n"

// ── 1. Normal execution completes ───────────────────────────────────────────

func TestBuildLifecycleNormalExecutionCompletes(t *testing.T) {
	// A valid SEARCH/REPLACE patch response yields a held patch: the executor
	// stops at its approval gate with PendingPatchID — the normal-execution
	// terminal on the RuntimeExecutor path.
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle", Description: "rewrite index.html"}}
	m := buildRunModel(t, mock, tasks, smallHTML)

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))

	// The executor dispatch (runRuntimeExecuteCmd) registers a single OpHotfix
	// operation BEFORE the provider call — the single-ownership + cancellation
	// slot on the executor path (the operation kind is OpHotfix, NOT OpBuild).
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("execution operation not registered at dispatch: %+v", m.activeOp)
	}
	if m.isWorkflowBusy() == false {
		t.Fatal("expected the busy/processing flags armed during execution")
	}
	if m.state != StateProcessing {
		t.Fatalf("during execution: state = %v, want StateProcessing", m.state)
	}

	// The provider is invoked SYNCHRONOUSLY inside the dispatched cmd; the
	// terminal message is the executor's gated result — never a legacy
	// buildProposalReadyMsg.
	var gem *gatedExecutionMsg
	for _, msg := range msgs {
		if g, ok := msg.(gatedExecutionMsg); ok {
			gem = &g
		}
	}
	if gem == nil {
		t.Fatalf("no gatedExecutionMsg reached the event loop (msgs=%d)", len(msgs))
	}
	if gem.err != nil {
		t.Fatalf("execution failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatalf("executor must hold a patch at the approval gate, got %+v", gem.res)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times, want exactly 1", mock.callCount)
	}

	// Terminal message → the proposal dock is staged for RuntimeExecutor.Approve
	// with a fresh OpHotfix operation holding the approval gate.
	res, _ := m.Update(*gem)
	m2 := res.(*model)
	if m2.executorPendingPatchID == "" {
		t.Fatal("approval-held patch not staged for approval")
	}
	if m2.pendingHotfixPatch == nil {
		t.Fatal("proposal patch not staged for authorization")
	}
	if len(m2.pendingProposals) == 0 {
		t.Fatal("no proposal staged for human approval")
	}
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval", m2.state)
	}
	if m2.activeOp == nil || m2.activeOp.Kind != OpHotfix {
		t.Fatalf("approval gate must hold an active OpHotfix operation: %+v", m2.activeOp)
	}
}

// ── 2. Execution failure returns control to the UI ──────────────────────────

func TestBuildLifecycleFailureReturnsControlToUI(t *testing.T) {
	// An empty model artifact is a deterministic execution failure on the
	// RuntimeExecutor path: the runtime produces no usable mutation artifact
	// and returns a terminal error — the executor performs a single provider
	// invocation (no legacy retry budget on this path).
	mock := &mockProvider{responses: []*ai.Response{{Content: ""}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, smallHTML)

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	var gem *gatedExecutionMsg
	for _, msg := range msgs {
		if g, ok := msg.(gatedExecutionMsg); ok {
			gem = &g
		}
	}
	if gem == nil {
		t.Fatal("no terminal gatedExecutionMsg reached the event loop")
	}
	if gem.err == nil {
		t.Fatal("expected a terminal execution failure")
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times, want exactly 1", mock.callCount)
	}

	res, _ := m.Update(*gem)
	m2 := res.(*model)
	// GUARANTEED LIFECYCLE: failure returns control to the interactive UI.
	if m2.activeOp != nil {
		t.Fatalf("execution operation not finalized on failure: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy flags still set after failure")
	}
	if m2.shimmerActive {
		t.Fatal("spinner still active after failure")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat (interactive)", m2.state)
	}
	if m2.executorPendingPatchID != "" {
		t.Fatal("a failed execution must not leave a held patch staged for approval")
	}
	// The UI accepts a new command.
	m2.ti.SetValue("!echo alive-after-failure")
	m3, _ := m2.submitEnter()
	if !strings.Contains(recordsText(m3.(*model)), "alive-after-failure") {
		t.Fatal("TUI not usable after build failure")
	}
}

// ── 3. Ctrl+C cancels a running provider operation ─────────────────────────

func TestBuildLifecycleCtrlCCancelsProviderCall(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)

	baseline := runtime.NumGoroutine()
	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))

	// Wait until the provider is actually blocked inside Execute.
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("no cancellable execution operation while the provider runs: %+v", m.activeOp)
	}

	// Ctrl+C → graceful cancellation of the CURRENT execution.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatalf("active operation not released by Ctrl+C: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy flags still set after Ctrl+C")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Ctrl+C", m2.state)
	}

	// The provider observes the cancellation (cancellation propagation).
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed the Ctrl+C cancellation")
	}

	// The worker goroutine releases with a terminal message.
	var terminal tea.Msg
	select {
	case terminal = <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker goroutine still blocked after cancellation")
	}
	res2, _ := m2.Update(terminal)
	m3 := res2.(*model)
	if m3.activeOp != nil || m3.isWorkflowBusy() {
		t.Fatal("terminal cancelled message did not clean the runtime")
	}
	if m3.state != StateChat {
		t.Fatalf("state after terminal cancelled msg = %v, want StateChat", m3.state)
	}
	if m3.shimmerActive {
		t.Fatal("spinner still active after cancellation")
	}
	// No goroutine remains blocked on the provider.
	goroutineDelta(t, baseline, "Ctrl+C provider cancellation")
}

// ── 5 + 6. Ctrl+C releases every worker goroutine; none remain blocked ─────

func TestBuildLifecycleCancelReleasesAllWorkers(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"},
	}
	m := buildRunModel(t, bp, tasks, smallHTML)

	baseline := runtime.NumGoroutine()
	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed the cancellation")
	}
	// The single provider worker returns its terminal message → it is released.
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("provider worker goroutine never released after cancellation")
	}
	goroutineDelta(t, baseline, "Ctrl+C worker release")
}

// ── 7. No command is dispatched twice ──────────────────────────────────────

func TestBuildLifecycleNoDuplicateDispatch(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "```\nhello izen\n```", TokenOutput: 50}}}
	m := gatedDispatchModel(t, mock, nil)
	m.resolver.Set(modes.ModeBuild)

	// A single $hot user action dispatches exactly ONE execution request
	// through the unified gateway and, after the runtime's model invocation,
	// exactly ONE provider call (owned by the executor, never by the UI).
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
		t.Fatalf("provider called %d times for one command, want exactly 1", mock.callCount)
	}

	// Terminal proposal → approval gate, exactly one lifecycle.
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval", m2.state)
	}
}

// ── 8 + 9. No execution remains stuck in "Processing file mutations" ───────

func TestBuildLifecycleProcessingStateTerminates(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// A valid SEARCH/REPLACE patch holds at the executor approval gate: the
		// terminal presentation is the proposal dock (StateAwaitingApproval),
		// never the processing state.
		mock := &mockProvider{responses: []*ai.Response{{
			Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
			Usage:   ai.ProviderUsage{Known: true},
		}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, smallHTML)
		msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
		var gem *gatedExecutionMsg
		for _, msg := range msgs {
			if g, ok := msg.(gatedExecutionMsg); ok {
				gem = &g
			}
		}
		if gem == nil || gem.err != nil {
			t.Fatalf("expected a successful execution, got %+v", gem)
		}
		res, _ := m.Update(*gem)
		m2 := res.(*model)
		if m2.state == StateProcessing {
			t.Fatalf("stuck in Processing after success: state=%v", m2.state)
		}
		if m2.state != StateAwaitingApproval {
			t.Fatalf("state = %v, want StateAwaitingApproval (approval gate)", m2.state)
		}
		if m2.executorPendingPatchID == "" {
			t.Fatal("approval-held patch not staged after success")
		}
	})

	t.Run("failure", func(t *testing.T) {
		mock := &mockProvider{responses: []*ai.Response{{Content: ""}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, smallHTML)
		msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
		var gem *gatedExecutionMsg
		for _, msg := range msgs {
			if g, ok := msg.(gatedExecutionMsg); ok {
				gem = &g
			}
		}
		if gem == nil || gem.err == nil {
			t.Fatalf("expected a terminal failure, got %+v", gem)
		}
		res, _ := m.Update(*gem)
		m2 := res.(*model)
		if m2.state == StateProcessing || m2.isWorkflowBusy() || m2.shimmerActive {
			t.Fatalf("stuck in Processing after failure: state=%v busy=%v shimmer=%v", m2.state, m2.isWorkflowBusy(), m2.shimmerActive)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, bp, tasks, smallHTML)
		msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
		select {
		case <-bp.started:
		case <-time.After(3 * time.Second):
			t.Fatal("provider never started")
		}
		res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		m2 := res.(*model)
		select {
		case <-bp.cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("provider never cancelled")
		}
		select {
		case terminal := <-msgs:
			updated, _ := m2.Update(terminal)
			m2 = updated.(*model)
		case <-time.After(3 * time.Second):
			t.Fatal("worker never released")
		}
		if m2.state == StateProcessing || m2.isWorkflowBusy() || m2.shimmerActive {
			t.Fatalf("stuck in Processing after cancellation: state=%v busy=%v shimmer=%v", m2.state, m2.isWorkflowBusy(), m2.shimmerActive)
		}
	})

	t.Run("no-change held patch", func(t *testing.T) {
		// The RuntimeExecutor is the sole execution authority: EVERY valid
		// artifact stops at its approval gate — including a verbatim echo of
		// the original file (no content change). The legacy mutationResultMsg
		// zero-patch short-circuit does not exist on this path. The echoed
		// content must equal strings.TrimSpace(orig) exactly (no trailing
		// newline), since ResolveModifiedContent trims the model output.
		var lines []string
		for i := 0; i < 220; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		large := strings.Join(lines, "\n")
		mock := &mockProvider{responses: []*ai.Response{{Content: large}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, large)
		msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
		var gem *gatedExecutionMsg
		for _, msg := range msgs {
			if g, ok := msg.(gatedExecutionMsg); ok {
				gem = &g
			}
		}
		if gem == nil || gem.err != nil {
			t.Fatalf("expected a held patch at the executor approval gate, got %+v", gem)
		}
		if gem.res == nil || gem.res.PendingPatchID == "" {
			t.Fatalf("the executor must hold the no-change artifact at the gate, got %+v", gem.res)
		}
		res, _ := m.Update(*gem)
		m2 := res.(*model)
		if m2.state == StateProcessing {
			t.Fatalf("stuck in Processing after the no-change terminal: state=%v", m2.state)
		}
		if m2.state != StateAwaitingApproval {
			t.Fatalf("state = %v, want StateAwaitingApproval", m2.state)
		}
		if m2.executorPendingPatchID == "" {
			t.Fatal("approval-held patch not staged after the no-change terminal")
		}
	})
}

// ── 10 + 11. Spinner/progress terminates on success/failure/cancellation ──
// Covered exhaustively by TestBuildLifecycleNormalExecutionCompletes (approval
// gate terminal), TestBuildLifecycleFailureReturnsControlToUI and the
// cancellation sub-test in TestBuildLifecycleProcessingStateTerminates above
// (each asserts the terminal presentation — !busy, !shimmerActive and state !=
// StateProcessing after the terminal message; a held patch presents as the
// approval gate, never as a stuck processing state).

// ── 12. A cancelled execution allows the next command to be submitted ──────

func TestBuildLifecycleCancelledExecutionAllowsNextCommand(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)
	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never cancelled")
	}
	// Drain the worker's terminal message so no goroutine lingers.
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}

	// The next command must execute normally.
	m.ti.SetValue("!echo after-cancel-build")
	m2, _ := m.submitEnter()
	if !strings.Contains(recordsText(m2.(*model)), "after-cancel-build") {
		t.Fatal("cancelled execution blocked the next command")
	}
	if m2.(*model).isWorkflowBusy() {
		t.Fatal("runtime busy after submitting the next command")
	}
}

// ── 13. Existing hotfix authorization/budget behavior remains unchanged ────

func TestBuildLifecycleHotfixBehaviorUnaffected(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "<<<<<<< SEARCH\n# README\n=======\n# Fixed README\n>>>>>>>", TokenOutput: 50}}}
	m := gatedDispatchModel(t, mock, map[string]string{
		"README.md": "# README\nbody\n",
	})
	m.resolver.Set(modes.ModeBuild)

	// $hot dispatches through the unified gateway → RuntimeExecutor and reaches
	// the same approval gate with a staged proposal.
	cmd := m.handleInput("$hot update the header in @README.md")
	if cmd == nil {
		t.Fatal("handleInput returned nil for $hot")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("gate err: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("nil gate result")
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval (unchanged hotfix gate)", m2.state)
	}
	if m2.pendingHotfixTask == nil || m2.pendingHotfixPatch == nil {
		t.Fatal("hotfix proposal not staged for authorization")
	}
}

// ── 14. Ambiguity stays explicit on the executor path ─────────────────────
// An unresolvable mutation target stops before any provider invocation and is
// surfaced explicitly — covered by TestRuntimeCutoverFlagOnAmbiguousTargetStaysExplicit
// (runtime_cutover_test.go) on the executor path.
