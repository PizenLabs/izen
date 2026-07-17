package ui

import (
	"strings"
	"testing"
	"time"
)

// TestSmoothTickAdvancesSpinnerDuringSynthesis is the regression guard for the
// frozen-spinner bug: /plan synthesis dispatches only the smoothStreamTickCmd
// loop (no token stream, no tickMsg loop), so if that handler does not advance
// m.spinnerFrame the braille indicator is physically stuck on frame 0 for the
// whole synthesis. This proves a smoothStreamTickMsg advances the frame while a
// background op owns the flags.
func TestSmoothTickAdvancesSpinnerDuringSynthesis(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.agentRunning = true
	m.agentLabel = "synthesizing plan"
	m.planPending = true
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{} // zero → first tick advances immediately

	newModel, _ := m.Update(smoothStreamTickMsg(time.Now()))
	m2 := newModel.(*model)

	if m2.spinnerFrame == 0 {
		t.Fatal("spinnerFrame did not advance on smoothStreamTickMsg during synthesis — spinner is frozen")
	}
}

// TestSmoothTickThrottlesSpinner verifies the ~100ms throttle: two ticks fired
// back-to-back (well under 100ms apart) advance the frame only once, so the
// 20ms token pacing does not spin the braille animation absurdly fast.
func TestSmoothTickThrottlesSpinner(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.agentRunning = true
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}

	nm, _ := m.Update(smoothStreamTickMsg(time.Now()))
	m1 := nm.(*model)
	frameAfterFirst := m1.spinnerFrame

	nm2, _ := m1.Update(smoothStreamTickMsg(time.Now())) // immediate second tick
	m2 := nm2.(*model)

	if m2.spinnerFrame != frameAfterFirst {
		t.Fatalf("spinner advanced twice within throttle window: %d -> %d",
			frameAfterFirst, m2.spinnerFrame)
	}
}

// TestSmoothTickDoesNotAdvanceWhenIdle ensures we never animate the spinner when
// there is no background work (no leaked animation on an idle prompt).
func TestSmoothTickDoesNotAdvanceWhenIdle(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.pipelineRunning = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}

	nm, _ := m.Update(smoothStreamTickMsg(time.Now()))
	m2 := nm.(*model)

	if m2.spinnerFrame != 0 {
		t.Fatalf("spinner advanced while idle (no owning producer): frame=%d", m2.spinnerFrame)
	}
}

// TestPlanSlowNoticeFiresWhilePending verifies the soft-timeout notice surfaces
// a viewport record (not a raw print) when synthesis is still pending and the
// probe's start time matches the current synthesis.
func TestPlanSlowNoticeFiresWhilePending(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	started := time.Now()
	m.planPending = true
	m.planStartedAt = started
	before := len(m.records)

	nm, _ := m.Update(planSlowNoticeMsg{startedAt: started})
	m2 := nm.(*model)

	if len(m2.records) <= before {
		t.Fatal("expected a viewport notice record when plan synthesis is slow")
	}
	found := false
	for _, r := range m2.records {
		if strings.Contains(r.text, "[timeout]") && strings.Contains(r.text, "unresponsive") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("slow-notice record did not contain the expected [timeout]/unresponsive message")
	}
}

// TestPlanSlowNoticeIgnoredWhenResolved ensures a stale probe (synthesis already
// finished, or a different run) does NOT emit a spurious warning.
func TestPlanSlowNoticeIgnoredWhenResolved(t *testing.T) {
	m := newTestModel()
	m.state = StateChat

	// Case A: synthesis already resolved (planPending == false).
	m.planPending = false
	m.planStartedAt = time.Now()
	before := len(m.records)
	nm, _ := m.Update(planSlowNoticeMsg{startedAt: m.planStartedAt})
	if got := len(nm.(*model).records); got != before {
		t.Fatalf("stale slow-notice emitted a record after synthesis resolved: %d -> %d", before, got)
	}

	// Case B: a NEW synthesis is running but the probe is from an OLD one.
	m.planPending = true
	m.planStartedAt = time.Now()
	before = len(m.records)
	nm2, _ := m.Update(planSlowNoticeMsg{startedAt: time.Now().Add(-time.Hour)})
	if got := len(nm2.(*model).records); got != before {
		t.Fatalf("stale probe from a prior synthesis emitted a record: %d -> %d", before, got)
	}
}
