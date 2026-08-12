package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/modes"
)

// newReviewBusyModel builds a model mid-review: the async agentStartMsg has
// been processed (agentRunning/reviewRunning set) and a PhaseChanged domain
// event has been projected onto the derived UI state, exactly the sequence
// that previously left the viewport stuck in StateProcessing — the "Processing
// file mutations... Please wait." deadlock — even after the terminal
// reviewResultMsg was delivered.
func newReviewBusyModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel()
	m.resolveApprovalState()
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.pipelineRunning = false
	m.shellRunning = false

	// agentStartMsg processed.
	m.agentRunning = true
	m.reviewRunning = true

	// PhaseChanged → review arrives while the run is busy: the derived state
	// freezes into StateProcessing.
	res, _ := m.Update(domainEventMsg{ev: events.NewPhaseChanged("idle", "reviewing")})
	m2 := res.(*model)
	if m2.state != StateProcessing {
		t.Fatalf("precondition: state=%v, want StateProcessing (stuck spinner setup)", m2.state)
	}
	return m2
}

// TestReviewCleanTreeResultReleasesStuckProcessing asserts the terminal
// reviewResultMsg (the exact message emitted for a clean working tree —
// "no changes to review") releases the stuck StateProcessing and stops the
// spinner: every transient flag is cleared and the next smooth-stream tick no
// longer re-dispatches itself, so the event loop can never spin forever.
func TestReviewCleanTreeResultReleasesStuckProcessing(t *testing.T) {
	m := newReviewBusyModel(t)
	if m.state != StateProcessing {
		t.Fatalf("precondition: state=%v, want StateProcessing (stuck spinner setup)", m.state)
	}

	res, _ := m.Update(reviewResultMsg{
		records: []record{{role: roleSystem, text: "no changes to review — working tree is clean"}},
	})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after clean-tree reviewResultMsg (spinner must not persist)", m2.state)
	}
	if m2.reviewRunning || m2.agentRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("transient flags still set after reviewResultMsg: review=%v agent=%v stream=%v pipeline=%v",
			m2.reviewRunning, m2.agentRunning, m2.streaming, m2.pipelineRunning)
	}

	// The unified tick loop must now halt: no active work, no re-dispatched cmd.
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick still re-dispatches a cmd after review completion — spinner loop never stops")
	}
}

// TestReviewCleanTreeResultReleasesStuckProcessingWithError asserts the error
// branch of reviewResultMsg performs the same state release (an engine error
// must never leave the spinner up either).
func TestReviewCleanTreeResultReleasesStuckProcessingWithError(t *testing.T) {
	m := newReviewBusyModel(t)

	res, _ := m.Update(reviewResultMsg{err: errors.New("review engine error")})
	m2 := res.(*model)

	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after reviewResultMsg error", m2.state)
	}
	if m2.reviewRunning || m2.agentRunning {
		t.Errorf("processing flags still set after review error: review=%v agent=%v", m2.reviewRunning, m2.agentRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick loop still alive after review error — spinner never stops")
	}
}

// TestReviewEscCancelsActivePipelineContext asserts Esc during an active
// review pipeline (reviewRunning set, view frozen in StateProcessing) routes
// through the emergency interrupt: it cancels the registered background
// context, clears every transient flag, returns focus to chat and dispatches a
// CancelCmd — without killing the app.
func TestReviewEscCancelsActivePipelineContext(t *testing.T) {
	m := newReviewBusyModel(t)

	// Simulate an already-registered review watchdog so the interrupt can
	// actually cancel the in-flight pipeline context.
	cancelled := false
	var cancelFunc = func() { cancelled = true }
	m.backgroundCancels = append(m.backgroundCancels, cancelFunc)

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(*model)

	if !cancelled {
		t.Error("Esc did not cancel the registered review pipeline context")
	}
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after Esc cancel", m2.state)
	}
	if m2.reviewRunning || m2.agentRunning || m2.streaming {
		t.Errorf("processing flags still set after Esc cancel: review=%v agent=%v stream=%v",
			m2.reviewRunning, m2.agentRunning, m2.streaming)
	}
	if cmd == nil {
		t.Fatal("Esc must return a command (CancelCmd) so the runtime observes the cancellation")
	}
	// The tick loop must not be re-spun by the cancelled state.
	_, cmd2 := m2.Update(smoothStreamTickMsg{})
	if cmd2 != nil {
		t.Fatal("tick loop still alive after Esc cancellation — spinner never stops")
	}
}

// TestReviewRunningSetSynchronously asserts runReviewCmd flips reviewRunning
// synchronously on the event-loop thread so the spinner renders immediately
// and Esc/Ctrl+C can interrupt before the async pipeline even starts.
func TestReviewRunningSetSynchronously(t *testing.T) {
	m := newTestModel()
	m.resolveApprovalState()
	m.reviewRunning = false
	m.agentRunning = false

	cmd := m.runReviewCmd("")
	if cmd == nil {
		t.Fatal("runReviewCmd returned nil cmd")
	}
	if !m.reviewRunning {
		t.Error("reviewRunning = false after runReviewCmd returned; must be set synchronously")
	}
	if m.lastActionTime.IsZero() {
		t.Error("lastActionTime not stamped by runReviewCmd")
	}
}

// TestReviewCleanTreeInputFastPathSkipsSpinner drives the real /review input
// dispatch on a clean git repo and asserts the fast-path short-circuit: the
// clean-tree message is surfaced, every processing flag is reset, the derived
// state returns to chat, and NO command is returned — so the async pipeline
// (and its "Processing file mutations..." spinner) never even starts.
func TestReviewCleanTreeInputFastPathSkipsSpinner(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	t.Chdir(dir)

	m := newTestModel()
	m.resolveApprovalState()
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.pipelineRunning = false
	m.shellRunning = false
	m.backgroundCancels = nil

	cmd := m.handleInput("/review")
	if cmd != nil {
		t.Fatalf("handleInput(/review) on a clean tree returned a cmd (%T) — a pipeline/spinner was started", cmd)
	}
	if m.reviewRunning || m.agentRunning || m.streaming {
		t.Errorf("processing flags set on clean-tree fast-path: review=%v agent=%v stream=%v",
			m.reviewRunning, m.agentRunning, m.streaming)
	}
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat after clean-tree fast-path", m.state)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "no changes to review") {
			found = true
			break
		}
	}
	if !found {
		t.Error("clean-tree fast-path did not surface the 'no changes to review' message")
	}
}

// initGitRepo creates a committed (clean) git repository at root so the review
// fast-path probe reports a pristine working tree.
func initGitRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=izen test", "GIT_AUTHOR_EMAIL=test@izen.dev",
			"GIT_COMMITTER_NAME=izen test", "GIT_COMMITTER_EMAIL=test@izen.dev",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "base.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write base.go: %v", err)
	}
	run("add", "base.go")
	run("commit", "-m", "baseline")
}

// TestReviewModeEntryNotBlocked ensures the fast-path leaves the app fully
// interactive: the resolver is in /review, no spinner owns the loop, and a
// subsequent input line is not gated by a phantom processing state.
func TestReviewModeEntryNotBlocked(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	t.Chdir(dir)

	m := newTestModel()
	m.resolveApprovalState()
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.state = StateChat
	m.reviewRunning = false
	m.agentRunning = false

	m.handleInput("/review")

	if got := m.resolver.Current(); got != modes.ModeReview {
		t.Errorf("resolver mode = %v, want ModeReview", got)
	}
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat (input must not be gated)", m.state)
	}
}
