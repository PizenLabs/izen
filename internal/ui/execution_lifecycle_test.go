package ui

import (
	"errors"
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
// The suite below drives the LEGACY per-task build execution path
// (handleBuildRun → proposeBuildPatch) — the exact pipeline whose provider
// calls previously ran on context.Background(), immune to Ctrl+C, leaving the
// UI stuck on the derived "Processing file mutations... Please wait." state
// until the 5-minute buildGenerationTimeout expired per attempt.
//
// buildRunModel wires a minimal-but-real model in the Building phase with a
// staged FILE_MUTATE task and a writable target file, so handleBuildRun
// exercises its full dispatch seam (transition → beginOperation → batch).

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
	mock := &mockProvider{responses: []*ai.Response{{Content: "<!DOCTYPE html>\n<html><body><h1>New</h1></body></html>\n"}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle", Description: "rewrite index.html"}}
	m := buildRunModel(t, mock, tasks, smallHTML)

	cmd := m.handleBuildRun(0)

	// The provider-invoking dispatch seam registers a single build operation
	// BEFORE any provider call — the single-ownership + cancellation slot.
	if m.activeOp == nil || m.activeOp.Kind != OpBuild {
		t.Fatalf("build operation not registered at dispatch: %+v", m.activeOp)
	}
	if m.isWorkflowBusy() == false {
		t.Fatal("expected the busy/processing flags armed during patch generation")
	}
	if m.state != StateProcessing {
		t.Fatalf("during generation: state = %v, want StateProcessing", m.state)
	}

	msgs := runBuildCmdsFiltered(t, cmd)
	var proposal *buildProposalReadyMsg
	for _, msg := range msgs {
		if p, ok := msg.(buildProposalReadyMsg); ok {
			proposal = &p
		}
	}
	if proposal == nil {
		t.Fatalf("no buildProposalReadyMsg reached the event loop (msgs=%d)", len(msgs))
	}
	if proposal.Err != nil {
		t.Fatalf("patch generation failed: %v", proposal.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider called %d times, want exactly 1", mock.callCount)
	}

	// Terminal message → operation finalized, spinner stopped, approval gate.
	res, _ := m.Update(*proposal)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatalf("build operation not finalized after proposal: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy flags still set after successful generation")
	}
	if m2.shimmerActive {
		t.Fatal("spinner still active after successful generation")
	}
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval", m2.state)
	}
	if len(m2.pendingProposals) == 0 {
		t.Fatal("no proposal staged for human approval")
	}
}

// ── 2. Execution failure returns control to the UI ──────────────────────────

func TestBuildLifecycleFailureReturnsControlToUI(t *testing.T) {
	// A large file + a tiny ambiguous snippet on every attempt drives
	// proposeBuildPatch through its full retry budget to a terminal failure.
	var large string
	for i := 0; i < 220; i++ {
		large += fmt.Sprintf("line %d\n", i)
	}
	tiny := "func main() {}\n"
	mock := &mockProvider{responses: []*ai.Response{
		{Content: tiny}, {Content: tiny}, {Content: tiny},
	}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, large)

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	var proposal *buildProposalReadyMsg
	for _, msg := range msgs {
		if p, ok := msg.(buildProposalReadyMsg); ok {
			proposal = &p
		}
	}
	if proposal == nil {
		t.Fatal("no terminal buildProposalReadyMsg reached the event loop")
	}
	if proposal.Err == nil {
		t.Fatal("expected a terminal failure after exhausting retries")
	}
	if mock.callCount != 3 {
		t.Fatalf("provider called %d times, want 3 (1 initial + 2 retries)", mock.callCount)
	}

	res, _ := m.Update(*proposal)
	m2 := res.(*model)
	// GUARANTEED LIFECYCLE: failure returns control to the interactive UI.
	if m2.activeOp != nil {
		t.Fatalf("build operation not finalized on failure: %+v", m2.activeOp)
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
	// The failed task is frozen for inspection (existing behavior).
	for _, task := range m2.sess.CurrentTasks {
		if task.Status != "failed" {
			t.Errorf("task %d status = %q, want failed", task.StepNum, task.Status)
		}
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
	if m.activeOp == nil || m.activeOp.Kind != OpBuild {
		t.Fatalf("no cancellable build operation while provider runs: %+v", m.activeOp)
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

// ── 4. Ctrl+C cancels a running file mutation ──────────────────────────────

func TestBuildLifecycleCtrlCCancelsRunningFileMutation(t *testing.T) {
	m := newTestModel()
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)

	// Drive the apply seam: Alt+A (applySingleProposal) registers an OpBuild
	// whose context is the cancellation parent of applyPatchWithDeadline.
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true})
	m2 := res.(*model)
	if cmd == nil {
		t.Fatal("Alt+A did not dispatch the apply command")
	}
	if m2.activeOp == nil || m2.activeOp.Kind != OpBuild {
		t.Fatalf("apply did not register a cancellable build operation: %+v", m2.activeOp)
	}

	// Ctrl+C during the mutation cancels the apply and returns to chat.
	res2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m3 := res2.(*model)
	if m3.activeOp != nil {
		t.Fatal("apply operation not released by Ctrl+C")
	}
	if m3.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Ctrl+C during mutation", m3.state)
	}
	if m3.isWorkflowBusy() {
		t.Fatal("busy after Ctrl+C during mutation")
	}

	// The apply context is derived from the operation: an already-cancelled
	// operation makes applyPatchWithDeadline abort deterministically.
	m4 := newTestModel()
	m4.execEng = execution.NewEngine(".", m4.cfg, m4.sess)
	m4.beginOperation(OpBuild)
	m4.activeOp.Cancel()
	if err := m4.applyPatchWithDeadline(&execution.Patch{File: "x", Modified: "y"}); !errors.Is(err, execution.ErrPatchApplyTimeout) {
		t.Fatalf("apply after cancellation = %v, want ErrPatchApplyTimeout", err)
	}
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

	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg, got %T", msg)
	}
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
		mock := &mockProvider{responses: []*ai.Response{{Content: "<h1>New</h1>\n"}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, smallHTML)
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
		if m2.state == StateProcessing || m2.isWorkflowBusy() || m2.shimmerActive {
			t.Fatalf("stuck in Processing after success: state=%v busy=%v shimmer=%v", m2.state, m2.isWorkflowBusy(), m2.shimmerActive)
		}
	})

	t.Run("failure", func(t *testing.T) {
		var large string
		for i := 0; i < 220; i++ {
			large += fmt.Sprintf("line %d\n", i)
		}
		mock := &mockProvider{responses: []*ai.Response{{Content: "x"}, {Content: "x"}, {Content: "x"}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, large)
		msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
		var proposal *buildProposalReadyMsg
		for _, msg := range msgs {
			if p, ok := msg.(buildProposalReadyMsg); ok {
				proposal = &p
			}
		}
		if proposal == nil || proposal.Err == nil {
			t.Fatalf("expected a terminal failure, got %+v", proposal)
		}
		res, _ := m.Update(*proposal)
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

	t.Run("zero-patch short-circuit", func(t *testing.T) {
		// For an EXISTING large file, when the model echoes the file back
		// verbatim ResolveModifiedContent resolves to the original and
		// proposeBuildPatch returns mutationResultMsg (nochange) DIRECTLY,
		// skipping buildProposalReadyMsg. The OpBuild begun at dispatch MUST be
		// finalized by the mutationResultMsg handler. The echoed content must
		// equal strings.TrimSpace(orig) exactly (no trailing newline), since
		// ResolveModifiedContent trims the model output.
		var lines []string
		for i := 0; i < 220; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		large := strings.Join(lines, "\n")
		mock := &mockProvider{responses: []*ai.Response{{Content: large}}}
		tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
		m := buildRunModel(t, mock, tasks, large)
		msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
		var mr *mutationResultMsg
		for _, msg := range msgs {
			if x, ok := msg.(mutationResultMsg); ok {
				mr = &x
			}
		}
		if mr == nil {
			t.Fatalf("expected zero-patch mutationResultMsg, got msgs=%d", len(msgs))
		}
		res, _ := m.Update(*mr)
		m2 := res.(*model)
		if m2.activeOp != nil {
			t.Fatalf("build operation not finalized by zero-patch short-circuit: %+v", m2.activeOp)
		}
		if m2.isWorkflowBusy() || m2.shimmerActive {
			t.Fatalf("stuck busy after zero-patch short-circuit: busy=%v shimmer=%v", m2.isWorkflowBusy(), m2.shimmerActive)
		}
		if m2.state != StateChat {
			t.Fatalf("state = %v, want StateChat after zero-patch completion", m2.state)
		}
	})
}

// ── 10 + 11. Spinner/progress terminates on success/failure/cancellation ──
// Covered exhaustively by TestBuildLifecycleNormalExecutionCompletes,
// TestBuildLifecycleFailureReturnsControlToUI and the cancellation sub-test in
// TestBuildLifecycleProcessingStateTerminates above (each asserts !busy,
// !shimmerActive and state != StateProcessing after the terminal message).

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
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg, got %T", msg)
	}
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

// ── 14. Existing ambiguity behavior remains unchanged ──────────────────────

func TestBuildLifecycleAmbiguityBehaviorUnaffected(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := ambiguousHotfixModel(t, mock)

	if m.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous (unchanged ambiguity gate)", m.state)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider called %d times for an ambiguous request, want 0", mock.callCount)
	}
	if m.activeOp != nil {
		t.Fatalf("active operation after ambiguous result: %+v", m.activeOp)
	}
	if m.isWorkflowBusy() {
		t.Fatal("busy after ambiguous result")
	}
}
