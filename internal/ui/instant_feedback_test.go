package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// askTestModel builds a /ask chat model with a wired execution engine so the
// full Enter → handleInput → unified gateway path can be exercised.
func askTestModel() *model {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.ti.Focus()
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.workspaceRoot = "."
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, nil, nil, "")
	trivial := execution.NewVerifier(".")
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(trivial)
	return m
}

// TestEnterDispatchesInstantShimmer guards the t=0ms contract: pressing Enter
// must set shimmerActive and return a command batch that schedules the
// animation ticks SYNCHRONOUSLY — before any async context prep resolves —
// so the loading dock animates immediately instead of freezing for the
// workspace scan.
func TestEnterDispatchesInstantShimmer(t *testing.T) {
	m := askTestModel()
	m.ti.SetValue("fix the bug in main.go")
	m.ti.CursorEnd()

	if m.shimmerActive {
		t.Fatal("test model must start with the shimmer inactive")
	}

	nm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := nm.(*model)

	if !m2.shimmerActive {
		t.Fatal("Enter did not activate the shimmer at t=0ms")
	}
	if m2.shimmerText != "Resolving execution..." {
		t.Fatalf("shimmer text = %q, want %q", m2.shimmerText, "Resolving execution...")
	}
	if cmd == nil {
		t.Fatal("Enter returned a nil command — no shimmer/smooth ticks scheduled")
	}
	// The prompt must be committed to the chat history synchronously.
	if len(m2.records) == 0 {
		t.Fatal("Enter did not push the user record before returning")
	}
}

// TestPrepareAskStreamCmdRunsAsync guards the async /ask context prep: the
// returned command assembles content on a background goroutine and delivers it
// as an askStreamPreparedMsg, never blocking the submit path. Without a
// planner the content round-trips unchanged and governance is marked so
// streamCmd skips its own file-read fallback.
func TestPrepareAskStreamCmdRunsAsync(t *testing.T) {
	m := askTestModel()
	cmd := m.prepareAskStreamCmd("hello world")
	if cmd == nil {
		t.Fatal("prepareAskStreamCmd returned nil")
	}

	msg := cmd()
	prepared, ok := msg.(askStreamPreparedMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want askStreamPreparedMsg", msg)
	}
	if prepared.content != "hello world" {
		t.Fatalf("content = %q, want unchanged %q", prepared.content, "hello world")
	}
	if !prepared.governed {
		t.Fatal("prep must mark the turn governed so streamCmd skips re-injection")
	}
}

// TestAskStreamPreparedMsgAppliesGovernance guards the async prep handler: the
// governance flag is applied on the event loop and then consumed by streamCmd
// (which degrades gracefully with no provider wired) so it never leaks into the
// next turn's fallback logic.
func TestAskStreamPreparedMsgAppliesGovernance(t *testing.T) {
	m := askTestModel()
	m.provider = nil

	nm, _ := m.Update(askStreamPreparedMsg{
		content:  "assembled context",
		governed: true,
	})
	m2 := nm.(*model)

	// streamCmd captured and cleared the per-turn governance flag — it must
	// never leak into a subsequent turn.
	if m2.askContextGoverned {
		t.Fatal("governance flag leaked after streamCmd consumed it")
	}
}

// TestThinkingBufferCollapsedExpandHint guards the faint (Ctrl+O to expand)
// hint on the collapsed inline reasoning line.
func TestThinkingBufferCollapsedExpandHint(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("analyze the failure mode")

	out := tb.Render(80, true, SpinnerSnowflake())
	if !strings.Contains(out, "[Ctrl+O to expand]") {
		t.Fatalf("collapsed streaming line missing expand hint: %q", out)
	}
}

// TestDockNoThinkingClaim guards execution-truthful progress: while the loading
// dock is live and the runtime is waiting on the provider, the dock MUST show a
// truthful provider state ("Model ● waiting"), never a "Thinking..." claim and
// never a reasoning expand hint on the progress line.
func TestDockNoThinkingClaim(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Waiting for model...", "analyze")
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("analyzing code structure")
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	got := m.composeDockTextWithFlake("✻")
	if strings.Contains(got, "Thinking") {
		t.Fatalf("dock claims the model is thinking: %q", got)
	}
	if strings.Contains(got, "[Ctrl+O to expand]") {
		t.Fatalf("dock progress line carries a reasoning expand hint: %q", got)
	}
	if !strings.Contains(got, "waiting") {
		t.Fatalf("dock does not render the truthful waiting state: %q", got)
	}
}

// TestThinkingBufferScrollExpanded guards in-box scrolling of the expanded
// reasoning box: j/k scroll moves the window, scrolling up suppresses
// auto-scroll, and reaching the tail resumes it.
func TestThinkingBufferScrollExpanded(t *testing.T) {
	tb := NewThinkingBuffer()
	for i := 0; i < 50; i++ {
		tb.Append("line\n")
	}
	tb.SetExpanded(true)

	// First Render caches the total line count used by HasOverflow.
	_ = tb.Render(80, true, SpinnerSnowflake())
	if !tb.HasOverflow() {
		t.Fatal("50 lines must overflow the default maxLines window")
	}

	tb.ScrollUp(3)
	if !tb.ScrolledUp() {
		t.Fatal("ScrollUp must latch scrolledUp (suppress auto-scroll)")
	}

	// Render while scrolled up must not yank back to the tail.
	out := tb.Render(80, true, SpinnerSnowflake())
	if !strings.Contains(out, "j/k scroll") {
		t.Fatalf("scrolled box missing scroll affordance footer: %q", out)
	}

	tb.ScrollDown(1000)
	if tb.ScrolledUp() {
		t.Fatal("scrolling past the tail must resume auto-scroll")
	}

	tb.ResetScroll()
	if tb.ScrolledUp() || tb.HasOverflow() == false {
		t.Fatal("ResetScroll must clear scroll state while overflow persists")
	}
}

// TestThinkingBufferScrollNoOverflowNotScrollable guards that a short
// reasoning block is NOT scrollable (scroll keys fall through to the viewport).
func TestThinkingBufferScrollNoOverflowNotScrollable(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("short reasoning")
	tb.SetExpanded(true)

	_ = tb.Render(80, true, SpinnerSnowflake())
	if tb.HasOverflow() {
		t.Fatal("short reasoning must not overflow")
	}

	tb.ScrollUp(1)
	if tb.ScrolledUp() {
		t.Fatal("non-overflowing box must not latch scroll state")
	}
}
