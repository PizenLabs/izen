package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestShellChunkAndExitLifecycle drives the streaming-shell message pipeline
// directly: a chunk appends to the running exec entry while the shimmer stays
// active, and the terminal exit event completes the entry AND tears down the
// dock cleanly (no stuck loading line).
func TestShellChunkAndExitLifecycle(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.activityTree = NewActivityTree()
	m.activityTree.AppendOrUpdateExec("echo hello-world", -1, 0, "")
	m.startShimmer("Executing command...", "execute")

	// Chunk arrives → output accumulates, shimmer still active (running).
	nm, _ := m.Update(shellChunkMsg{text: "hello-world\n"})
	m = nm.(*model)
	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 exec entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].CommandExec.Output, "hello-world") {
		t.Errorf("chunk not appended to exec output: %q", entries[0].CommandExec.Output)
	}
	if !m.shimmerActive {
		t.Error("shimmer must stay active while the shell is running")
	}

	// Exit event → shimmer stops, exec entry completes with exit status.
	nm2, _ := m.Update(shellExitMsg{cmd: "echo hello-world", exitCode: 0, elapsed: 10 * time.Millisecond})
	m = nm2.(*model)
	if m.shimmerActive {
		t.Error("shimmer must stop cleanly on shell exit")
	}
	if m.shellRunning {
		t.Error("shellRunning must clear on shell exit")
	}
	if m.shellCh != nil {
		t.Error("shellCh must be torn down on shell exit")
	}
	entries = m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 exec entry after exit, got %d", len(entries))
	}
	if entries[0].CommandExec.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", entries[0].CommandExec.ExitCode)
	}
}

// TestStreamShellCmdRealProcess launches the real streaming pipeline for a
// fast command and verifies the output lands in the activity tree and the
// shell state is cleared on completion.
func TestStreamShellCmdRealProcess(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.activityTree = NewActivityTree()

	readCmd := m.streamShellCmd("printf 'alpha\nbeta\n'")
	if !m.shellRunning {
		t.Fatal("streamShellCmd must set shellRunning")
	}
	if m.shellCh == nil {
		t.Fatal("streamShellCmd must create the shell channel")
	}

	var exit *shellExitMsg
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readCmd == nil {
			t.Fatal("readShellCh returned nil prematurely")
		}
		msg := readCmd()
		if msg == nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		nm, _ := m.Update(msg)
		m = nm.(*model)
		if v, ok := msg.(shellExitMsg); ok {
			exit = &v
			break
		}
		readCmd = m.readShellCh()
	}
	if exit == nil {
		t.Fatalf("shellExitMsg never arrived; tree=%v", m.activityTree.Entries())
	}
	if m.shellRunning {
		t.Error("shellRunning not cleared after exit")
	}
	if m.shimmerActive {
		t.Error("shimmer still active after exit")
	}
	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 exec entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].CommandExec.Output, "alpha") ||
		!strings.Contains(entries[0].CommandExec.Output, "beta") {
		t.Errorf("streamed output missing lines: %q", entries[0].CommandExec.Output)
	}
}

// TestShimmerTickKeepsDispatchingDuringShell guards the spinner-tick contract:
// while a shell command is running, shimmerTickCmd must keep returning a
// non-nil tick; once the shell exits and the shimmer is stopped, it returns nil
// so the tick loop self-terminates (no leaked goroutine, no stuck dock).
func TestShimmerTickKeepsDispatchingDuringShell(t *testing.T) {
	m := newTestModel()
	m.shellRunning = true
	m.startShimmer("Executing command...", "execute")

	if cmd := m.shimmerTickCmd(); cmd == nil {
		t.Fatal("shimmerTickCmd must schedule a frame while a shell is running")
	}

	// Simulate the terminal event clearing the shell + shimmer.
	m.shellRunning = false
	m.stopShimmer()
	if cmd := m.shimmerTickCmd(); cmd != nil {
		t.Fatal("shimmerTickCmd must self-terminate after the shell exits")
	}
}

// TestCtrlOExpandsShellOutputDuringRun drives the full keypress path: Ctrl+O
// while a shell exec entry is active toggles the activity-tree expansion so the
// streamed output appears in the viewport.
func TestCtrlOExpandsShellOutputDuringRun(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.activityTree = NewActivityTree()
	m.activityTree.AppendOrUpdateExec("bash npm run build", -1, 0, "")
	m.activityTree.AppendExecOutput("> compiling\n")

	// Collapsed: viewport hides the streamed output.
	m.refreshViewportContent()
	collapsed := m.Viewport.View()
	if strings.Contains(collapsed, "> compiling") {
		t.Fatalf("collapsed view leaked shell output:\n%s", collapsed)
	}

	// Ctrl+O expands the shell output inline in the viewport.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.refreshViewportContent()
	expanded := m.Viewport.View()
	if !strings.Contains(expanded, "> compiling") {
		t.Fatalf("Ctrl+O did not expand the shell output in the viewport:\n%s", expanded)
	}
	if !m.activityTree.Expanded() {
		t.Fatal("activity tree expansion flag not set by Ctrl+O")
	}

	// Second Ctrl+O collapses it again.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.refreshViewportContent()
	collapsed2 := m.Viewport.View()
	if strings.Contains(collapsed2, "> compiling") {
		t.Fatalf("second Ctrl+O did not collapse the shell output:\n%s", collapsed2)
	}
}
