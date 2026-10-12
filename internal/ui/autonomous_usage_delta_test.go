package ui

// ── Autonomous token accounting ─────────────────────────────────────────────
//
// The autonomous driver exposes a CUMULATIVE per-run aggregate that it does not
// reset when the run parks and resumes. The footer commits provider usage into
// the session baseline, so committing the raw cumulative total at every boundary
// (the park AND the terminal) adds the same provider calls to the baseline more
// than once. That is the observed ~2× discrepancy between the footer and the
// provider's real billing.
//
// These tests pin the corrected rule: every provider call is folded into the
// session exactly once — each boundary commits only the DELTA since the last
// commit for the same run, a new run starts its own baseline, and an UNKNOWN
// account still commits nothing.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestAutonomousUsageIsCommittedOncePerRun proves the park and the terminal of
// one run commit the provider's spend exactly once, not once per boundary.
func TestAutonomousUsageIsCommittedOncePerRun(t *testing.T) {
	drv := &fakeAutonomousDriver{
		runID: "run-1", state: autonomy.RuntimeAwaitingHuman,
		aggInput: 9122, aggOutput: 624, aggKnown: true,
	}
	m := autonomousTestModel(drv)

	// First boundary: the run parks after a billed invocation.
	cmd := m.handleAutonomousRun(autonomousRunMsg{})
	if cmd == nil {
		t.Fatal("the parked boundary produced no usage command")
	}
	msg, ok := cmd().(UsageUpdateMsg)
	if !ok {
		t.Fatalf("parked boundary command = %T, want UsageUpdateMsg", cmd())
	}
	if msg.PromptTokens != 9122 || msg.CompletionTokens != 624 {
		t.Fatalf("parked usage = %d/%d, want the driver's 9122/624", msg.PromptTokens, msg.CompletionTokens)
	}
	m.Update(msg)

	// The terminal boundary of the SAME run: the driver's aggregate is
	// cumulative and unchanged, so nothing new may be committed. Committing the
	// cumulative total again is the ~2× footer discrepancy.
	term := &autonomy.LoopTermination{State: autonomy.RuntimeUnsubstantiated, Reason: "objective was not proven"}
	cmd = m.handleAutonomousRun(autonomousRunMsg{term: term})
	if cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	if m.InputTokens != 9122 || m.OutputTokens != 624 {
		t.Fatalf("session usage = %d/%d, want the provider's 9122/624 committed exactly once",
			m.InputTokens, m.OutputTokens)
	}
}

func TestAutonomousUsageCommitsNewSpendAcrossResume(t *testing.T) {
	drv := &fakeAutonomousDriver{
		runID: "run-1", state: autonomy.RuntimeAwaitingHuman,
		aggInput: 1000, aggOutput: 100, aggKnown: true,
	}
	m := autonomousTestModel(drv)
	if cmd := m.handleAutonomousRun(autonomousRunMsg{}); cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	// A resume billed one more invocation: the aggregate grows, and only the
	// delta is committed.
	drv.aggInput, drv.aggOutput = 1500, 160
	if cmd := m.handleAutonomousRun(autonomousRunMsg{}); cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	if m.InputTokens != 1500 || m.OutputTokens != 160 {
		t.Fatalf("session usage = %d/%d, want the run's cumulative 1500/160",
			m.InputTokens, m.OutputTokens)
	}
}

func TestAutonomousUsageResetsForANewRun(t *testing.T) {
	drv := &fakeAutonomousDriver{runID: "run-1", aggInput: 1000, aggOutput: 100, aggKnown: true}
	m := autonomousTestModel(drv)
	if cmd := m.handleAutonomousRun(autonomousRunMsg{}); cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	// A new run starts its aggregate from zero; the previous run's baseline must
	// not be subtracted from it.
	drv.runID = "run-2"
	drv.aggInput, drv.aggOutput = 700, 70
	if cmd := m.handleAutonomousRun(autonomousRunMsg{}); cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	if m.InputTokens != 1700 || m.OutputTokens != 170 {
		t.Fatalf("session usage = %d/%d, want 1000+700 / 100+70", m.InputTokens, m.OutputTokens)
	}
}

func TestAutonomousUsageUnknownCommitsNothing(t *testing.T) {
	drv := &fakeAutonomousDriver{runID: "run-1", aggInput: 500, aggOutput: 50, aggKnown: false}
	m := autonomousTestModel(drv)
	beforeIn, beforeOut := m.InputTokens, m.OutputTokens
	if cmd := m.handleAutonomousRun(autonomousRunMsg{}); cmd != nil {
		if u, ok := cmd().(UsageUpdateMsg); ok {
			m.Update(u)
		}
	}
	if m.InputTokens != beforeIn || m.OutputTokens != beforeOut {
		t.Fatalf("an unknown account changed the session counters to %d/%d", m.InputTokens, m.OutputTokens)
	}
}
