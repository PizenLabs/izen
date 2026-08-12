package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestForensicCtrlCSwallowedInAmbiguousState proves the demonstrated liveness
// defect: after a $hot request returns HOTFIX_AMBIGUOUS the Ctrl+C key is
// swallowed by the handleKey default branch, the input stays blurred, and no
// interrupt command is produced. This is why the TUI appears dead and the user
// reaches for tmux kill-pane.
func TestForensicCtrlCSwallowedInAmbiguousState(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	amb := res.(*model)
	if amb.state != StateHotfixAmbiguous {
		t.Fatalf("precondition: state = %v, want StateHotfixAmbiguous", amb.state)
	}

	resModel, cmd := amb.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	after := resModel.(*model)

	t.Logf("state after Ctrl+C  = %v", after.state)
	t.Logf("cmd   after Ctrl+C  = %v", cmd)
	t.Logf("ti    focused       = %v", after.ti.Focused())
	t.Logf("pendingHotfixAmbiguous = %v", after.pendingHotfixAmbiguous != nil)
	t.Logf("busy flags: agent=%v stream=%v hotfix=%v",
		after.agentRunning, after.streaming, after.hotfixActive)

	if after.pendingHotfixAmbiguous == nil {
		t.Logf("PROGRESS: ambiguity card dismissed by Ctrl+C")
	} else {
		t.Errorf("FAIL: ambiguity card still up after Ctrl+C")
	}
	if after.ti.Focused() {
		t.Logf("PROGRESS: input is focused after Ctrl+C")
	} else {
		t.Errorf("FAIL: input still blurred after Ctrl+C — cannot type the next command")
	}
	if after.state == StateChat {
		t.Logf("PROGRESS: state returned to chat")
	} else {
		t.Errorf("FAIL: state = %v after Ctrl+C — still trapped in the ambiguity card", after.state)
	}
	if cmd != nil {
		t.Logf("PROGRESS: Ctrl+C returned a runtime cancel command")
	} else {
		t.Logf("INFO: no runtime wired in harness — cancel cmd nil, graceful dismissal still applied")
	}
}

// TestForensicTypingBlockedInAmbiguousState proves the second half of the
// liveness defect: while StateHotfixAmbiguous renders, keystrokes intended for
// the input are hard-intercepted and dropped — a normal command can never be
// typed without first pressing the hidden c/i/x card keys.
func TestForensicTypingBlockedInAmbiguousState(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	amb := res.(*model)

	// User tries to type a brand-new command.
	resModel, cmd := amb.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'$'}})
	after := resModel.(*model)
	_ = cmd

	if after.ti.Focused() {
		t.Logf("PROGRESS: input accepts keystrokes while the card is up")
	} else {
		t.Errorf("FAIL: '$' keystroke dropped — input blurred, command cannot be composed")
	}
	got := after.ti.Value()
	if strings.Contains(got, "$") {
		t.Logf("PROGRESS: '$' reached the input buffer: %q", got)
	} else {
		t.Errorf("FAIL: input buffer = %q — '$' swallowed by the ambiguous card", got)
	}
}

// TestForensicBusyStateAfterAmbiguity proves the current model leaves the
// transient busy flags cleared but the view locked in a modal with input
// blurred — the runtime cannot answer "is an operation active".
func TestForensicBusyStateAfterAmbiguity(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	amb := res.(*model)

	t.Logf("busy flags after ambiguity: agent=%v stream=%v hotfix=%v",
		amb.agentRunning, amb.streaming, amb.hotfixActive)
	t.Logf("ti focused after ambiguity: %v", amb.ti.Focused())
	if amb.ti.Focused() {
		t.Logf("INFO: input is focused after ambiguity (already usable)")
	} else {
		t.Errorf("FAIL: input blurred after ambiguity — no way to type the next command without pressing a card key")
	}
}
