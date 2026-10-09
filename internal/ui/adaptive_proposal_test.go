package ui

// ── ADAPTIVE PROPOSAL UI PROJECTION ─────────────────────────────────────────
//
// The UI is a PROJECTION of the runtime boundary: it renders the adaptive
// proposal the driver parked and routes the human's decisions back through
// Driver.ResumeAdaptiveProposal. It owns no proposal state and executes nothing.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
)

func adaptiveBoundary() *autonomy.HumanBoundary {
	return &autonomy.HumanBoundary{
		Reason:              "adaptive proposal: investigate — awaiting 1 decision",
		Action:              autonomy.HumanBoundaryAdaptiveProposal,
		Resumable:           true,
		ProposalID:          "p1",
		ProposalRevision:    1,
		ProposalFingerprint: "fp-1",
		ProposalObjective:   "redesign this project",
		ProposalSummary:     "Evidence-bound change — apply the objective",
		ProposalStrategies: []autonomy.HumanProposalStrategy{{
			ID: "evidence-bound", Title: "Evidence-bound change",
			Summary: "apply the objective to the observed artifacts", Recommended: true,
			Tradeoffs: []autonomy.HumanProposalTradeoff{{Description: "scope", Benefit: "coverage", Cost: "time"}},
			Risks:     []autonomy.HumanProposalRisk{{Description: "wide change", Severity: "medium"}},
		}},
		ProposalQuestions: []autonomy.HumanProposalQuestion{{
			ID: "scope", Prompt: "How wide should the change be?",
			Kind: autonomy.HumanProposalQuestionSingle, Required: true,
			Options: []autonomy.HumanProposalOption{
				{ID: "evidence-bound", Label: "All observed artifacts"},
				{ID: "minimal", Label: "Smallest change only"},
			},
			Recommended: "evidence-bound",
		}},
	}
}

func TestAutonomousAdaptiveProposalRendersAndSubmits(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  adaptiveBoundary(),
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("redesign this project help me")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	if !m.autonomousParked() {
		t.Fatal("model must hold the parked adaptive proposal boundary")
	}
	block := m.renderAutonomousBoundaryBlock(120)
	if block == "" {
		t.Fatal("a parked adaptive proposal must render a non-empty card")
	}
	for _, want := range []string{"ADAPTIVE PROPOSAL", "redesign this project", "How wide", "All observed artifacts", "trade-off"} {
		if !strings.Contains(block, want) {
			t.Fatalf("adaptive block missing %q:\n%s", want, block)
		}
	}

	// Select the second option (minimal) and submit. The decision must route
	// through the authoritative runtime surface with the selected option id.
	m.selectAdaptiveOption(1)
	submit := m.resumeAutonomousAdaptiveProposal()
	if submit == nil {
		t.Fatal("submitting decisions must route a resume command")
	}
	if batch, ok := submit().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	if drv.resumeAdaptive != 1 {
		t.Fatalf("ResumeAdaptiveProposal calls = %d, want 1", drv.resumeAdaptive)
	}
	if len(drv.lastAdaptiveDecisions) != 1 {
		t.Fatalf("decisions = %+v, want exactly the answered question", drv.lastAdaptiveDecisions)
	}
	if got := drv.lastAdaptiveDecisions[0]; got.QuestionID != "scope" || got.OptionID != "minimal" {
		t.Fatalf("routed decision = %+v, want scope=minimal", got)
	}
}

func TestAutonomousAdaptiveProposalEscapeAborts(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  adaptiveBoundary(),
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("redesign this project help me")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if kcmd == nil {
		t.Fatal("Esc on an adaptive proposal must route an abort")
	}
	if batch, ok := kcmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	if drv.abortCount != 1 {
		t.Fatalf("abort calls = %d, want 1 (Esc must not silently drop the run)", drv.abortCount)
	}
}

func TestAutonomousAdaptiveProposalDigitSelects(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  adaptiveBoundary(),
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("redesign this project help me")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	// Pressing "1" selects the first option; Enter submits it.
	if _, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}); kcmd != nil {
		_ = kcmd
	}
	if got := m.adaptiveAnswers["scope"]; len(got) != 1 || got[0] != "evidence-bound" {
		t.Fatalf("digit selection = %v, want the first option", got)
	}
}
