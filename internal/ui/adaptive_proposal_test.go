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

// pressRunes delivers printable text to the model the way a human typing it
// would: one KeyMsg per rune.
func pressRunes(t *testing.T, m *model, text string) {
	t.Helper()
	for _, r := range text {
		if _, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}); kcmd != nil {
			_ = kcmd
		}
	}
}

// freeFormBoundary is a parked adaptive proposal whose question requires the
// human's own words rather than a menu choice.
func freeFormBoundary() *autonomy.HumanBoundary {
	b := adaptiveBoundary()
	b.ProposalQuestions = []autonomy.HumanProposalQuestion{{
		ID:            "approach",
		Prompt:        "How should the migration be phased?",
		Kind:          autonomy.HumanProposalQuestionFreeForm,
		Required:      true,
		AllowFreeForm: true,
	}}
	return b
}

// TestAutonomousAdaptiveProposalFreeFormSubmitsThroughTheDriver is the free-form
// interaction proof: the text the human types is validated, bound to the parked
// proposal identity and revision, and submitted through the SAME authoritative
// Driver surface an option selection uses. The UI never mutates proposal state
// itself.
func TestAutonomousAdaptiveProposalFreeFormSubmitsThroughTheDriver(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  freeFormBoundary(),
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("redesign this project help me")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)
	if !m.autonomousParked() {
		t.Fatal("model must hold the parked adaptive proposal boundary")
	}

	// Empty input is refused: a decision with no answer is not a decision.
	if submit := m.resumeAutonomousAdaptiveProposal(); submit != nil {
		t.Fatal("submitting with no answer at all must be refused")
	}
	if !m.autonomousParked() {
		t.Fatal("a refused submission must leave the proposal parked")
	}
	if block := m.renderAutonomousBoundaryBlock(120); !strings.Contains(block, "i free-form answer") {
		t.Fatalf("the rendered card must promise the free-form key it actually honours:\n%s", block)
	}

	// Typing, then committing, then submitting.
	pressRunes(t, m, "i")
	if m.adaptiveEditing != "approach" {
		t.Fatalf("editing question = %q, want approach", m.adaptiveEditing)
	}
	pressRunes(t, m, "phase by module")
	// A draft that has not been committed is not an answer yet.
	if submit := m.resumeAutonomousAdaptiveProposal(); submit != nil {
		t.Fatal("an uncommitted draft must not be submitted")
	}
	if _, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); kcmd != nil {
		_ = kcmd
	}
	if m.adaptiveEditing != "" {
		t.Fatal("Enter inside the editor must commit the draft and close it")
	}
	if got := m.adaptiveFreeFormFor("approach"); got != "phase by module" {
		t.Fatalf("committed free-form = %q, want the typed text", got)
	}
	// The UI holds no option selection beside a free-form answer: they are two
	// spellings of one decision.
	if m.adaptiveAnswers["approach"] != nil {
		t.Fatalf("the UI kept an option selection beside the free-form answer: %v", m.adaptiveAnswers["approach"])
	}

	// The card shows the committed answer, so the human can see what will be
	// submitted before submitting it.
	if block := m.renderAutonomousBoundaryBlock(120); !strings.Contains(block, "phase by module") {
		t.Fatalf("the card does not show the committed free-form answer:\n%s", block)
	}

	submit := m.resumeAutonomousAdaptiveProposal()
	if submit == nil {
		t.Fatal("submitting a committed free-form answer must route a resume command")
	}
	if batch, ok := submit().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	if drv.resumeAdaptive != 1 {
		t.Fatalf("ResumeAdaptiveProposal calls = %d, want 1 (free-form must traverse the same path)", drv.resumeAdaptive)
	}
	got := drv.lastAdaptiveDecisions
	if len(got) != 1 {
		t.Fatalf("decisions = %+v, want exactly the answered question", got)
	}
	if got[0].QuestionID != "approach" || got[0].FreeForm != "phase by module" {
		t.Fatalf("routed decision = %+v, want the typed free-form text", got[0])
	}
	if got[0].OptionID != "" || len(got[0].OptionIDs) != 0 {
		t.Fatalf("a free-form answer must not also carry an option selection: %+v", got[0])
	}
	if drv.lastAdaptiveFingerprint != freeFormBoundary().ProposalFingerprint {
		t.Fatalf("fingerprint = %q, want the parked proposal's identity", drv.lastAdaptiveFingerprint)
	}
}

// TestAutonomousAdaptiveProposalFreeFormDraftIsDiscarded proves an uncommitted
// draft never reaches the runtime: Escape throws it away, and a question that
// does not accept free-form input never opens an editor at all.
func TestAutonomousAdaptiveProposalFreeFormDraftIsDiscarded(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  freeFormBoundary(),
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("redesign this project help me")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	pressRunes(t, m, "i")
	pressRunes(t, m, "half typed")
	if _, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); kcmd != nil {
		_ = kcmd
	}
	if got := m.adaptiveFreeFormFor("approach"); got != "half typed" {
		t.Fatalf("committed free-form = %q, want the typed text", got)
	}

	// Re-open the editor and type more, then abandon it.
	pressRunes(t, m, "i")
	pressRunes(t, m, "discard me")
	if _, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEscape}); kcmd != nil {
		_ = kcmd
	}
	if m.adaptiveDraft != "" || m.adaptiveEditing != "" {
		t.Fatalf("escape did not discard the draft (editing=%q draft=%q)", m.adaptiveEditing, m.adaptiveDraft)
	}
	if got := m.adaptiveFreeFormFor("approach"); got != "half typed" {
		t.Fatalf("the committed answer changed to %q after a discarded draft", got)
	}

	// A question with no free-form permission refuses the editor outright.
	drv2 := &fakeAutonomousDriver{state: autonomy.RuntimeAwaitingHuman, parkOnRun: true, boundary: adaptiveBoundary()}
	m2 := autonomousTestModel(drv2)
	cmd2 := m2.runAutonomousDriver("redesign this project help me")
	m2.handleAutonomousRun(extractAutonomousRunMsg(t, cmd2()))
	if _, kcmd := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}); kcmd != nil {
		_ = kcmd
	}
	if m2.adaptiveEditing != "" {
		t.Fatal("a single-choice question without free-form permission opened an editor")
	}
	if _, kcmd := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}); kcmd != nil {
		_ = kcmd
	}
	if m2.adaptiveDraft != "" {
		t.Fatal("text was collected for a question that accepts no free-form answer")
	}
}
