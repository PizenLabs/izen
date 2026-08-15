package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Shared harness helpers ────────────────────────────────────────────────

// ambiguousHotfixModel drives the exact screenshot scenario through
// handleHotfixCmd — $hot with a non-uniquely-inferable target on a large file —
// and returns the model in StateHotfixAmbiguous.
func ambiguousHotfixModel(t *testing.T, mock *mockProvider) *model {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModel()
	if mock != nil {
		m.provider = mock
	}
	cmd := m.handleHotfixCmd("Remove extra text from @index.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	var m2 *model
	for _, em := range drainCmds(t, cmd) {
		if am, ok := em.(hotfixAmbiguousMsg); ok {
			res, _ := m.Update(am)
			m2 = res.(*model)
		}
	}
	if m2 == nil {
		t.Fatal("ambiguous message never reached the event loop")
	}
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("precondition: state = %v, want StateHotfixAmbiguous", m2.state)
	}
	return m2
}

// blockingProvider blocks Execute until its context is cancelled, proving
// provider-cancellation propagation end to end.
type blockingProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (b *blockingProvider) Name() string { return "blocking" }

func (b *blockingProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	if b.started != nil {
		close(b.started)
	}
	select {
	case <-ctx.Done():
		if b.cancelled != nil {
			close(b.cancelled)
		}
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("provider was never cancelled")
	}
}

func (b *blockingProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// structuralHotfixTask is a deterministic structural-intent FILE_MUTATE task
// (never ambiguous) that exercises the provider-invocation path on a large HTML
// file.
func structuralHotfixTask() *plan.Task {
	return &plan.Task{
		StepNum:     0,
		Type:        "FILE_MUTATE",
		Target:      "index.html",
		Description: "fix the mismatched h2 closing tag in @index.html",
	}
}

// ── A. Ambiguous operation finalization ───────────────────────────────────

// TestRegressionAmbiguousFinalization is the screenshot-scenario regression:
// an ambiguous $hot request renders StateHotfixAmbiguous with zero provider
// calls, no patch, no active operation, no busy state, and the input focused —
// the runtime is fully usable with no worker left waiting for candidate
// selection.
func TestRegressionAmbiguousFinalization(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := ambiguousHotfixModel(t, mock)

	// StateHotfixAmbiguous rendered.
	if m.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous", m.state)
	}
	// Provider call count == 0 — the gate never invoked the model.
	if mock.callCount != 0 {
		t.Fatalf("provider called %d times for an ambiguous request, want 0", mock.callCount)
	}
	// Patch == nil — no patch was ever produced.
	if m.pendingHotfixPatch != nil {
		t.Fatal("pendingHotfixPatch set for an ambiguous request")
	}
	if len(m.pendingProposals) != 0 {
		t.Fatal("no patch proposal may be staged for an ambiguous request")
	}
	// Active operation == nil — AMBIGUOUS is terminal.
	if m.activeOp != nil {
		t.Fatalf("active operation not released after ambiguous: %+v", m.activeOp)
	}
	// Busy == false — no transient execution flag remains.
	if m.isWorkflowBusy() {
		t.Fatal("busy after ambiguous result")
	}
	// Input is available.
	if !m.ti.Focused() {
		t.Fatal("input not available after ambiguous result")
	}
	// Spinner/progress stopped.
	if m.shimmerActive {
		t.Fatal("shimmer still active after ambiguous result")
	}
}

// ── B. Runtime remains usable after ambiguity ─────────────────────────────

// TestRegressionRuntimeUsableAfterAmbiguous is the primary screenshot
// regression test: after the ambiguous hotfix is dismissed, the next command
// executes normally.
func TestRegressionRuntimeUsableAfterAmbiguous(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := ambiguousHotfixModel(t, mock)

	// Dismiss the card via Cancel ([⌥X]).
	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true})
	m2 := res.(*model)
	if m2.state != StateChat {
		t.Fatalf("after cancel: state = %v, want StateChat", m2.state)
	}
	if !m2.ti.Focused() {
		t.Fatal("after cancel: input not focused")
	}

	// Submit a new normal command; it must execute successfully.
	m2.ti.SetValue("!echo hello-from-izen-after-ambiguity")
	m3, _ := m2.submitEnter()
	after := m3.(*model)

	if !strings.Contains(recordsText(after), "hello-from-izen-after-ambiguity") {
		t.Fatalf("subsequent command did not execute — records:\n%s", recordsText(after))
	}
	if after.isWorkflowBusy() {
		t.Fatal("runtime busy after subsequent command")
	}
	if after.activeOp != nil {
		t.Fatalf("stale operation after subsequent command: %+v", after.activeOp)
	}
}

// TestRegressionRuntimeDispatchesHotfixAfterAmbiguity proves a NEW $hot can be
// dispatched immediately after the ambiguity card (without restarting the
// app): a fresh operation begins and provider execution resumes.
func TestRegressionRuntimeDispatchesHotfixAfterAmbiguity(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: "```\nhello\n```", TokenOutput: 10}}}
	m := ambiguousHotfixModel(t, mock)

	// Dismiss the card via Clarify ([⌥C]) — focus returns to the input.
	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
	m2 := res.(*model)
	if m2.state != StateChat || !m2.ti.Focused() {
		t.Fatalf("after clarify: state=%v focused=%v", m2.state, m2.ti.Focused())
	}

	// A new $hot request is accepted and begins a new operation.
	m2.ti.SetValue("$hot add a README file @README.md")
	m3, cmd := m2.submitEnter()
	after := m3.(*model)
	if cmd == nil {
		t.Fatal("new $hot returned nil cmd after ambiguity")
	}
	if after.activeOp == nil {
		t.Fatal("new $hot did not begin a fresh operation after ambiguity")
	}
}

// ── C. Provider cancellation ──────────────────────────────────────────────

// TestRegressionProviderCancellation asserts a Ctrl+C during a blocked provider
// call cancels the provider, drives the operation to CANCELLED, and keeps the
// UI responsive.
func TestRegressionProviderCancellation(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newTestModel()
	m.provider = bp

	m.beginOperation(OpHotfix)
	m.hotfixActive = true

	resCh := make(chan tea.Msg, 1)
	go func() { resCh <- m.proposeHotfixPatch(structuralHotfixTask())() }()

	// Wait until the provider is actually blocked inside Execute.
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}

	// Ctrl+C → graceful cancellation. The event loop must respond immediately.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after cancel", m2.state)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy flags still set after cancel")
	}
	if m2.activeOp != nil {
		t.Fatal("active operation not released after cancel")
	}

	// Provider receives the cancellation.
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed the cancellation")
	}

	// The terminal message from the cancelled worker arrives and cleans up.
	select {
	case msg := <-resCh:
		hp, ok := msg.(hotfixProposalMsg)
		if !ok {
			t.Fatalf("expected hotfixProposalMsg, got %T", msg)
		}
		if hp.Err == nil || !isContextCancelled(hp.Err) {
			t.Fatalf("expected a cancellation error, got %v", hp.Err)
		}
		res2, _ := m2.Update(hp)
		m3 := res2.(*model)
		if m3.activeOp != nil || m3.isWorkflowBusy() {
			t.Fatal("terminal cancelled message did not clean the runtime")
		}
		if !m3.ti.Focused() {
			t.Fatal("input not restored after cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no terminal message after cancellation")
	}
}

// ── D. Subprocess cancellation ────────────────────────────────────────────

// TestRegressionSubprocessCancellation asserts Ctrl+C terminates a running
// child process (context-aware exec.CommandContext) and the runtime survives.
func TestRegressionSubprocessCancellation(t *testing.T) {
	m := newTestModel()
	// `exec sleep` replaces bash with the sleep process so the direct child IS
	// the long-running process — the context-cancellation kill terminates it
	// deterministically (no grandchild inheriting the output pipes).
	cmd := m.streamShellCmd("exec sleep 30")
	if cmd == nil {
		t.Fatal("streamShellCmd returned nil")
	}
	if !m.shellRunning || m.shellCancel == nil {
		t.Fatal("shell not armed")
	}

	// Drain the shell channel in the background. The channel reference is
	// captured BEFORE the interrupt (the interrupt tears it down), so the
	// drain goroutine must bind to the pre-interrupt channel.
	shellCh := m.shellCh
	var exitCh = make(chan shellExitMsg, 1)
	go func() {
		for msg := range shellCh {
			if se, ok := msg.(shellExitMsg); ok {
				select {
				case exitCh <- se:
				default:
				}
				return
			}
		}
	}()

	// Ctrl+C cancels the subprocess.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.shellRunning {
		t.Fatal("shell flag still set after Ctrl+C")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Ctrl+C", m2.state)
	}

	// The child process must terminate promptly (context cancellation).
	select {
	case <-exitCh:
		// child terminated
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess did not terminate after Ctrl+C — tmux kill-pane would be required")
	}

	// Izen is still alive and usable.
	if m2.activeOp != nil || m2.isWorkflowBusy() {
		t.Fatal("runtime busy after subprocess cancellation")
	}
}

// ── E. Double Ctrl+C force-exits with status 130 ──────────────────────────

// TestRegressionDoubleCtrlCHardExit130 asserts the first Ctrl+C performs a
// graceful cancellation and a second Ctrl+C while cancellation is in progress
// forces termination with status 130.
func TestRegressionDoubleCtrlCHardExit130(t *testing.T) {
	saved := hardExitFn
	defer func() { hardExitFn = saved }()

	type hardExit struct{ code int }
	exitStatus := -1
	hardExitFn = func(code int) {
		exitStatus = code
		panic(hardExit{code})
	}

	m := newTestModel()
	m.beginOperation(OpHotfix)

	// First Ctrl+C: graceful cancellation + grace window armed.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)
	if m2.cancelGraceDeadline.IsZero() {
		t.Fatal("grace window not armed after first Ctrl+C")
	}

	// Second Ctrl+C: hard exit 130. Call handleCtrlC directly — Update's
	// global panic recovery would otherwise swallow the forced exit.
	func() {
		defer func() {
			r := recover()
			if _, ok := r.(hardExit); !ok {
				t.Fatalf("second Ctrl+C did not force a hard exit (recover=%v)", r)
			}
		}()
		m2.handleCtrlC()
		t.Fatal("second Ctrl+C returned without hard exit")
	}()
	if exitStatus != 130 {
		t.Fatalf("exit status = %d, want 130", exitStatus)
	}
}

// ── F. Timeout ────────────────────────────────────────────────────────────

// TestRegressionTimeoutCleansUp asserts a provider that exceeds its operation
// deadline reaches TIMEOUT, cleans up, restores the input, and lets a
// subsequent command run.
func TestRegressionTimeoutCleansUp(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	bp := &blockingProvider{started: make(chan struct{})}
	m := newTestModel()
	m.ti.Focus()
	m.provider = bp
	m.hotfixTimeout = 50 * time.Millisecond

	m.beginOperation(OpHotfix)
	m.hotfixActive = true

	// Run the worker synchronously; the 50ms deadline expires.
	msg := m.proposeHotfixPatch(structuralHotfixTask())()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if hp.Err == nil || !isContextDeadline(hp.Err) {
		t.Fatalf("expected deadline-exceeded error, got %v", hp.Err)
	}

	// Feed the terminal message: TIMEOUT → cleanup → input available.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatal("active operation not released after timeout")
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after timeout")
	}
	if !m2.ti.Focused() {
		t.Fatal("input not restored after timeout")
	}

	// Subsequent command works.
	m2.ti.SetValue("!echo after-timeout")
	m3, _ := m2.submitEnter()
	after := m3.(*model)
	if !strings.Contains(recordsText(after), "after-timeout") {
		t.Fatalf("subsequent command after timeout failed:\n%s", recordsText(after))
	}
}

// ── G. Panic isolation ────────────────────────────────────────────────────

// TestRegressionWorkerPanicBecomesFailure asserts a panic at the worker
// boundary is converted into a terminal FAILURE that runs cleanup and leaves
// the TUI usable — the runtime never stays stuck in an inconsistent state.
// The panicProvider type is shared with the investigate lifecycle suite.
func TestRegressionWorkerPanicBecomesFailure(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModel()
	m.provider = panicProvider{}
	m.beginOperation(OpHotfix)

	// The proposeHotfixPatch worker recovers the panic and emits a terminal
	// hotfixProposalMsg carrying the panic as an error.
	msg := m.proposeHotfixPatch(structuralHotfixTask())()
	hp, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected terminal hotfixProposalMsg after panic, got %T", msg)
	}
	if hp.Err == nil || !strings.Contains(hp.Err.Error(), "panic") {
		t.Fatalf("panic not surfaced as terminal error: %v", hp.Err)
	}

	// Terminal failure → cleanup → UI remains usable.
	res, _ := m.Update(hp)
	m2 := res.(*model)
	if m2.activeOp != nil {
		t.Fatal("active operation not released after panic")
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after panic")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after panic", m2.state)
	}

	// TUI still accepts commands.
	m2.ti.SetValue("!echo alive-after-panic")
	m3, _ := m2.submitEnter()
	after := m3.(*model)
	if !strings.Contains(recordsText(after), "alive-after-panic") {
		t.Fatalf("TUI unusable after panic:\n%s", recordsText(after))
	}
}

// hotfixTargetsFor resolves the deterministic HTML anomaly candidates for the
// given target file.
func hotfixTargetsFor(t *testing.T, target string) []hotfix.Target {
	t.Helper()
	cands := hotfix.ResolveHTMLCandidates(largeMismatchedIndexHTML())
	if len(cands) == 0 {
		t.Fatal("fixture must yield deterministic candidates")
	}
	return cands
}

// ── H. No event-loop blocking ─────────────────────────────────────────────

// TestRegressionEventLoopNotBlockedByWorker proves a blocking provider worker
// does NOT block the TUI event loop: cancellation/input still process while the
// worker is running.
func TestRegressionEventLoopNotBlockedByWorker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newTestModel()
	m.provider = bp

	m.beginOperation(OpHotfix)
	m.hotfixActive = true
	go func() { m.proposeHotfixPatch(structuralHotfixTask())() }()

	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}

	// While the worker is blocked, the event loop must still process Ctrl+C.
	done := make(chan struct{})
	go func() {
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event loop blocked by worker — cannot process cancellation")
	}

	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed cancellation")
	}
}

// ── I. Candidate selection starts a new operation ─────────────────────────

// TestRegressionCandidateSelectionStartsNewOperation proves that after an
// ambiguous result no active execution remains, and that selecting a candidate
// starts a NEW operation.
func TestRegressionCandidateSelectionStartsNewOperation(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := newTestModel()
	m.provider = mock

	// Ambiguous request with deterministic HTML candidates.
	cands := hotfixTargetsFor(t, "index.html")
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", cands))
	m2 := res.(*model)
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("precondition: state = %v", m2.state)
	}
	// No active execution remains after the ambiguous result.
	if m2.activeOp != nil {
		t.Fatal("active execution remains after ambiguous result")
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after ambiguous result")
	}

	// Enter candidate inspection (alt+i — a plain 'i' is always text) and
	// explicitly select candidate #1.
	m3, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true})
	m4 := m3.(*model)
	if !m4.hotfixCandidatesMode {
		t.Fatal("precondition: candidate inspection mode not entered")
	}
	sel, _ := m4.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	after := sel.(*model)

	// Selecting a candidate starts a NEW operation (op B).
	if after.activeOp == nil {
		t.Fatal("candidate selection did not start a new operation")
	}
	if after.activeOp.Kind != OpHotfix {
		t.Fatalf("new operation kind = %s, want hotfix", after.activeOp.Kind)
	}
	if !after.activeOp.running() {
		t.Fatalf("new operation not running: %+v", after.activeOp)
	}
	if after.activeOp.ID == "" {
		t.Fatal("new operation has no ID")
	}
}

// ── Watchdog / stuck detection ────────────────────────────────────────────

// TestOperationWatchdogReportsStall asserts the watchdog emits a diagnostic
// warning for a stalled operation without ever killing it.
func TestOperationWatchdogReportsStall(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)
	m.activeOp.LastProgress = time.Now().Add(-opWatchdogStuckAfter - time.Second)

	cmd := m.handleWatchdog(watchdogMsg{now: time.Now()})

	// The watchdog re-arms while the operation is active (it never kills).
	if cmd == nil {
		t.Fatal("watchdog stopped while an operation is active")
	}
	if !strings.Contains(recordsText(m), "watchdog") {
		t.Fatalf("watchdog did not report the stall:\n%s", recordsText(m))
	}
	// The operation survives — the watchdog only reports.
	if m.activeOp == nil {
		t.Fatal("watchdog killed the operation")
	}

	// An idle runtime stops the watchdog loop.
	m.finalizeOperation(OpOutcomeSuccess, nil)
	if m.opWatchdogCmd() != nil {
		t.Fatal("watchdog still armed when idle")
	}
}

// ── Operation lifecycle primitives ────────────────────────────────────────

// TestOperationLifecycleTransitions asserts the canonical state machine
// IDLE → DISPATCHED → RUNNING → terminal → FINALIZING → IDLE and single
// ownership.
func TestOperationLifecycleTransitions(t *testing.T) {
	m := newTestModel()
	if m.activeOp != nil {
		t.Fatal("model starts with an active operation")
	}

	op := m.beginOperation(OpHotfix)
	if op == nil || op.ID == "" {
		t.Fatal("beginOperation returned a malformed operation")
	}
	if op.State != OpStateDispatched {
		t.Fatalf("initial state = %s, want dispatched", op.State)
	}
	if m.activeOp != op {
		t.Fatal("beginOperation did not register the operation")
	}
	op.State = OpStateRunning

	// finalizeOperation is the single terminal cleanup path.
	m.finalizeOperation(OpOutcomeAmbiguous, nil)
	if op.State != OpStateTerminal {
		t.Fatalf("finalize did not reach terminal: %s", op.State)
	}
	if op.Outcome != OpOutcomeAmbiguous {
		t.Fatalf("outcome = %s, want ambiguous", op.Outcome)
	}
	if m.activeOp != nil {
		t.Fatal("finalize did not release active ownership")
	}
	if m.isWorkflowBusy() {
		t.Fatal("finalize did not clear busy flags")
	}
	if m.shimmerActive {
		t.Fatal("finalize did not stop the spinner")
	}

	// A new operation supersedes the previous one.
	op2 := m.beginOperation(OpHotfix)
	if m.activeOp != op2 {
		t.Fatal("new operation did not become the active one")
	}
	m.finalizeOperation(OpOutcomeSuccess, nil)
}

// TestOperationFinalizeCancelsContext proves finalizeOperation releases the
// operation cancellation resources (Section 8: operation cancellation
// resources).
func TestOperationFinalizeCancelsContext(t *testing.T) {
	m := newTestModel()
	op := m.beginOperation(OpHotfix)
	ctx := op.Ctx
	select {
	case <-ctx.Done():
		t.Fatal("context already cancelled at begin")
	default:
	}
	m.finalizeOperation(OpOutcomeCancelled, nil)
	select {
	case <-ctx.Done():
		// cancellation propagated
	case <-time.After(time.Second):
		t.Fatal("finalize did not cancel the operation context")
	}
}

// TestRegressionOSSignalCancelsOperation proves the root OS-signal bridge
// (SIGINT/SIGTERM delivered outside the TTY raw-mode key path) routes through
// the same graceful cancellation protocol as Ctrl+C.
func TestRegressionOSSignalCancelsOperation(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)

	res, _ := m.Update(interruptSignalMsg{signal: os.Interrupt})
	m2 := res.(*model)

	if m2.activeOp != nil {
		t.Fatalf("active operation not cancelled by OS signal: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after OS signal cancellation")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after OS signal", m2.state)
	}
}

// TestRegressionBubbleTeaInterruptMsgCancelsOperation proves Bubble Tea's
// built-in InterruptMsg (its own SIGINT forwarding for non-TTY input) also
// routes through the graceful cancellation protocol.
func TestRegressionBubbleTeaInterruptMsgCancelsOperation(t *testing.T) {
	m := newTestModel()
	m.beginOperation(OpHotfix)

	res, _ := m.Update(tea.InterruptMsg{})
	m2 := res.(*model)

	if m2.activeOp != nil {
		t.Fatalf("active operation not cancelled by InterruptMsg: %+v", m2.activeOp)
	}
	if m2.isWorkflowBusy() {
		t.Fatal("busy after InterruptMsg cancellation")
	}
}

// TestRegressionCtrlCCancelsIdleChatKeepsInput asserts the unified Ctrl+C
// protocol does not hijack the idle-chat behavior: with no operation active,
// Ctrl+C clears the input buffer instead of cancelling or exiting.
func TestRegressionCtrlCCancelsIdleChatKeepsInput(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.ti.SetValue("stale text")

	// The unified protocol declines to handle an idle Ctrl+C (handled=false);
	// the chat handler then clears the input. handleKey is invoked directly so
	// the uninitialized-test-model init guard does not intercept the key.
	if handled, _ := m.handleCtrlC(); handled {
		t.Fatal("idle Ctrl+C must not be treated as a cancellation")
	}
	res, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res.(*model)

	if m2.ti.Value() != "" {
		t.Fatalf("idle Ctrl+C did not clear the input buffer: %q", m2.ti.Value())
	}
	_ = cmd
	if m2.state != StateChat {
		t.Fatalf("idle Ctrl+C changed state to %v", m2.state)
	}
	if m2.cancelGraceDeadline != (time.Time{}) {
		t.Fatal("idle Ctrl+C must not arm the force-exit grace window")
	}
}
