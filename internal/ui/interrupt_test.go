package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newInterruptExecutingModel builds an executing model (streaming under
// StateProcessing) suitable for driving the double-tap Esc protocol through
// the real Update key pipeline. The interrupt hatch runs before init-stage
// routing, so no on-disk workspace is required.
func newInterruptExecutingModel() *model {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.agentRunning = true
	m.spinnerFrame = 1
	m.streamStartTime = time.Now().Add(-10 * time.Second)
	m.executionStartedAt = m.streamStartTime
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 128)
	return m
}

// TestSingleEscKey_ArmsInterruptWithoutCanceling pins the safe first tap: it
// sets armed=true, leaves execution running, and flips the right footer hint
// from the subtle "Esc stop" to the warning "Press Esc again!".
func TestSingleEscKey_ArmsInterruptWithoutCanceling(t *testing.T) {
	m := newInterruptExecutingModel()

	before := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(before, "Esc stop") {
		t.Fatalf("precondition: idle executing hint missing %q:\n%q", "Esc stop", before)
	}
	if strings.Contains(before, "Press Esc again!") {
		t.Fatalf("precondition: warn hint leaked before arming:\n%q", before)
	}

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(*model)

	if !m2.interruptState.armed {
		t.Fatal("first Esc must set interruptState.armed = true")
	}
	if cmd == nil {
		t.Fatal("first Esc must return the single disarm tick command")
	}
	// No cancellation on the first tap.
	if m2.state != StateProcessing {
		t.Errorf("state = %v, want StateProcessing after first Esc (arm only)", m2.state)
	}
	if !m2.streaming || !m2.agentRunning {
		t.Error("first Esc must not clear execution flags")
	}

	after := stripANSIFooter(m2.renderFixedFooter(100, nil))
	if !strings.Contains(after, "Press Esc again!") {
		t.Errorf("armed footer missing warn hint %q:\n%q", "Press Esc again!", after)
	}
	if strings.Contains(after, "Ctrl+C") || strings.Contains(after, "^C") {
		t.Errorf("footer must never render Ctrl+C:\n%q", after)
	}
}

// TestDoubleEscKey_TriggersCancellation pins the second tap: within the 1.5s
// window it dispatches MsgCancelStream, and handling that message cancels the
// active stream back to StateChat.
func TestDoubleEscKey_TriggersCancellation(t *testing.T) {
	m := newInterruptExecutingModel()

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mArmed := res.(*model)
	if !mArmed.interruptState.armed {
		t.Fatal("precondition: first Esc did not arm")
	}

	res2, cancelCmd := mArmed.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res2.(*model)
	if cancelCmd == nil {
		t.Fatal("second Esc must return a command dispatching MsgCancelStream")
	}
	msgs := drainCmds(t, cancelCmd)
	found := false
	for _, msg := range msgs {
		if _, ok := msg.(MsgCancelStream); ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("second Esc must dispatch MsgCancelStream, got %v", msgs)
	}
	if m2.interruptState.armed {
		t.Error("second Esc must disarm before dispatching cancel")
	}

	res3, cmd := m2.Update(MsgCancelStream{})
	m3 := res3.(*model)
	if m3.state != StateChat {
		t.Errorf("state = %v, want StateChat after double-Esc", m3.state)
	}
	if m3.streaming || m3.agentRunning {
		t.Errorf("execution flags still set after double-Esc: streaming=%v agent=%v",
			m3.streaming, m3.agentRunning)
	}
	if cmd == nil {
		t.Fatal("MsgCancelStream must return a command (interrupt record)")
	}
}

// TestEscKey_TimeoutAutoDisarms pins the race-free reset chain: a reset tick
// bearing the current sequenceID disarms, while a stale tick from a previous
// press is ignored.
func TestEscKey_TimeoutAutoDisarms(t *testing.T) {
	m := newInterruptExecutingModel()

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mArmed := res.(*model)
	seq := mArmed.interruptState.sequenceID
	if !mArmed.interruptState.armed {
		t.Fatal("precondition: first Esc did not arm")
	}

	// Stale tick (previous press) must be ignored.
	resStale, _ := mArmed.Update(MsgResetInterruptState{SequenceID: seq + 999})
	if !resStale.(*model).interruptState.armed {
		t.Error("stale MsgResetInterruptState must not disarm a newer window")
	}

	// Matching tick disarms after the 1.5s window.
	res2, _ := mArmed.Update(MsgResetInterruptState{SequenceID: seq})
	m2 := res2.(*model)
	if m2.interruptState.armed {
		t.Error("matching MsgResetInterruptState must disarm the window")
	}
	if m2.state != StateProcessing || !m2.streaming {
		t.Error("timeout disarm must not cancel execution — only clear the hint")
	}
	footer := stripANSIFooter(m2.renderFixedFooter(100, nil))
	if !strings.Contains(footer, "Esc stop") {
		t.Errorf("disarmed footer must restore %q:\n%q", "Esc stop", footer)
	}
	if strings.Contains(footer, "Press Esc again!") {
		t.Errorf("disarmed footer must drop the warn hint:\n%q", footer)
	}
}

// TestCtrlC_HardCancelsImmediately pins the silent fallback: Ctrl+C disarms
// any pending Esc window and cancels at once, ignoring the double-tap
// requirement, without ever rendering itself.
func TestCtrlC_HardCancelsImmediately(t *testing.T) {
	m := newInterruptExecutingModel()

	// Arm first to prove Ctrl+C overrides the window.
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mArmed := res.(*model)
	if !mArmed.interruptState.armed {
		t.Fatal("precondition: first Esc did not arm")
	}

	res2, cmd := mArmed.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := res2.(*model)
	if m2.interruptState.armed {
		t.Error("Ctrl+C must disarm the Esc window")
	}
	if m2.state != StateChat {
		t.Errorf("state = %v, want StateChat after Ctrl+C hard-cancel", m2.state)
	}
	if m2.streaming || m2.agentRunning {
		t.Errorf("execution flags still set after Ctrl+C: streaming=%v agent=%v",
			m2.streaming, m2.agentRunning)
	}
	if cmd == nil {
		t.Fatal("Ctrl+C must return a command (interrupt record)")
	}
}

// TestDoubleEscModalIsolation pins modal input isolation: with an overlay
// modal active, Esc must not arm the stream-cancellation flow.
func TestDoubleEscModalIsolation(t *testing.T) {
	m := newInterruptExecutingModel()
	m.showHelpOverlay = true

	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(*model)
	if m2.interruptState.armed {
		t.Error("Esc with a modal active must not arm the interrupt window")
	}
	if !m2.streaming {
		t.Error("Esc with a modal active must not cancel streaming")
	}
}

// TestExecutingFooterEscHints pins the dynamic footer swap: subtle "Esc stop"
// idle, high-visibility "Press Esc again!" while armed, and zero Ctrl+C text
// in either state.
func TestExecutingFooterEscHints(t *testing.T) {
	m := newInterruptExecutingModel()

	idle := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(idle, "Esc stop") {
		t.Errorf("executing footer missing idle hint %q:\n%q", "Esc stop", idle)
	}
	if strings.Contains(idle, "Press Esc again!") {
		t.Errorf("executing footer leaked armed hint while idle:\n%q", idle)
	}

	m.interruptState.armed = true
	m.interruptState.armedAt = time.Now()
	armed := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(armed, "Press Esc again!") {
		t.Errorf("armed footer missing warn hint %q:\n%q", "Press Esc again!", armed)
	}

	for _, footer := range []string{idle, armed} {
		if strings.Contains(footer, "^C") || strings.Contains(footer, "Ctrl+C") {
			t.Errorf("footer must never render Ctrl+C:\n%q", footer)
		}
		// Core telemetry slots survive alongside the hint.
		for _, want := range []string{"qwen2.5-coder:7b", "↑", "↓", "tok/s"} {
			if !strings.Contains(footer, want) {
				t.Errorf("executing footer missing %q:\n%q", want, footer)
			}
		}
	}
}
