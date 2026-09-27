package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ui/components"
)

// ── DECOUPLED SPINNER TICKER ────────────────────────────────────────────────
//
// The braille indicator has exactly one writer: the 100ms SpinnerTickMsg
// ticker. These tests pin that ownership in both directions — the animation
// ticker advances the frame, and NO frame loop does — because the failure mode
// being guarded against is a second writer creeping back in at a second rate,
// which is invisible in any single frame and only shows up as a stutter.

// TestSpinnerTickIsTheOnlyFrameWriter is the regression guard for the frozen
// spinner AND for the stutter: /plan synthesis dispatches only the
// smoothStreamTickCmd loop (no token stream, no legacy tickMsg loop), so the
// indicator must animate there too. The property that gives it to us is not
// "the smooth loop increments" — it is "the smooth loop ARMS the dedicated
// ticker, and the ticker increments".
func TestSpinnerTickIsTheOnlyFrameWriter(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.agentRunning = true
	m.agentLabel = "synthesizing plan"
	m.planPending = true
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}

	nm, cmd := m.Update(smoothStreamTickMsg(time.Now()))
	m2 := nm.(*model)

	if m2.spinnerFrame != 0 {
		t.Errorf("the smooth frame loop advanced the spinner (%d): the frame loop is a RENDER "+
			"of the animation, not a driver of it", m2.spinnerFrame)
	}
	if !m2.spinnerTickArmed {
		t.Error("the smooth frame loop did not arm the decoupled animation ticker")
	}
	// The arming is single-flight: a second call on the next tick must not
	// dispatch a SECOND live ticker, or N live loops would produce N timers.
	nm2, cmd2 := m2.Update(smoothStreamTickMsg(time.Now()))
	if cmd2 != nil && !m2.spinnerTickArmed {
		t.Error("a second frame loop call re-armed the already-live ticker")
	}
	if nm2.(*model).spinnerTickArmed != true {
		t.Error("the single-flight flag was cleared by a frame loop")
	}
	_ = cmd
	_ = cmd2

	// And the ticker itself is what moves the glyph.
	nm3, cmd3 := m2.Update(spinnerTickMsg(time.Now()))
	m3 := nm3.(*model)
	if m3.spinnerFrame == 0 {
		t.Fatal("the 100ms animation ticker did not advance the spinner — spinner is frozen")
	}
	if !m3.spinnerTickArmed {
		t.Error("the animation ticker did not re-arm itself during active work")
	}
	_ = cmd3
}

// TestSpinnerFrameIsNotDrivenByAnyFrameLoop is the ownership assertion stated
// directly, driven through every message that used to write the counter. It is
// the test that fails if a future animation loop is added and helpfully
// increments the spinner "so it animates".
func TestSpinnerFrameIsNotDrivenByAnyFrameLoop(t *testing.T) {
	frames := []struct {
		name string
		msg  tea.Msg
	}{
		{"smoothStreamTickMsg", smoothStreamTickMsg(time.Now())},
		{"tickMsg", tickMsg(time.Now())},
		{"executingHeaderTickMsg", executingHeaderTickMsg{}},
		{"shimmerFrameMsg", shimmerFrameMsg{}},
	}
	for _, f := range frames {
		t.Run(f.name, func(t *testing.T) {
			m := newTestModel()
			m.state = StateChat
			m.streaming = true
			m.agentRunning = true
			m.reviewRunning = true
			m.pipelineRunning = true
			m.planPending = true
			m.shellRunning = true
			m.shimmerActive = true
			m.spinnerFrame = 0

			nm, _ := m.Update(f.msg)
			if got := nm.(*model).spinnerFrame; got != 0 {
				t.Errorf("%s advanced the spinner to %d; the 100ms SpinnerTickMsg is the "+
					"only writer and the frame loops only render it", f.name, got)
			}
		})
	}
}

// TestSpinnerTickIntervalIsOneHundredMilliseconds pins the animation rate
// independently of the render rate. The braille cycle has ten glyphs, so 100ms is
// a full 1.0s rotation; anything faster is the frame loop leaking into the
// animation again.
func TestSpinnerTickIntervalIsOneHundredMilliseconds(t *testing.T) {
	if SpinnerInterval != 100*time.Millisecond {
		t.Errorf("SpinnerInterval = %v, want 100ms", SpinnerInterval)
	}
	if components.SpinnerTickInterval != SpinnerInterval {
		t.Errorf("the workspace and the execution-bounded spinner disagree on the cadence: "+
			"%v vs %v", SpinnerInterval, components.SpinnerTickInterval)
	}
}

// TestSpinnerDoesNotAdvanceWhenIdle ensures the ticker never animates the
// spinner with no owning producer (no leaked animation on an idle prompt), and
// that it disarms rather than re-arming forever.
func TestSpinnerDoesNotAdvanceWhenIdle(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.pipelineRunning = false
	m.shellRunning = false
	m.planPending = false
	m.autonomousActive = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}

	nm, cmd := m.Update(spinnerTickMsg(time.Now()))
	m2 := nm.(*model)

	if m2.spinnerFrame != 0 {
		t.Fatalf("spinner advanced while idle (no owning producer): frame=%d", m2.spinnerFrame)
	}
	if cmd != nil {
		t.Error("the animation ticker re-armed itself with no owning producer")
	}
	if m2.spinnerTickArmed {
		t.Error("the animation ticker stayed armed with no owning producer")
	}
}

// TestSpinnerTickIsArmedOnceAcrossManyFrameLoops is the single-flight assertion:
// a stream dispatches the smooth tick, the shimmer tick AND the header tick, and
// all three arm the animation ticker. Exactly one live timer may result.
func TestSpinnerTickIsArmedOnceAcrossManyFrameLoops(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.disarmSpinnerTick()

	first := m.ensureSpinnerTick()
	if first == nil {
		t.Fatal("the first arming produced no command")
	}
	if !m.spinnerTickArmed {
		t.Error("the first arming did not mark the ticker live")
	}
	for range 5 {
		if again := m.ensureSpinnerTick(); again != nil {
			t.Fatal("a second arming dispatched a SECOND live ticker")
		}
	}
	// The tick clears the flag, so the NEXT producer re-arms cleanly.
	m.spinnerTickArmed = false
	if next := m.ensureSpinnerTick(); next == nil {
		t.Error("after a tick the ticker could not be re-armed")
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
