package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── DECOMPOSITION_PROPOSAL (PLAN_STAGED) DECISION CARD ──────────────────────
//
// When the autonomy loop parks at a HumanBoundaryDecomposition boundary, the
// TUI must render an interactive proposal card — strategy kind, every staged
// sub-task with its line-range window, and the explicit keybindings — and
// map Enter/Esc onto the driver's proposal resume surface. A parked proposal
// must never leave the UI unresponsive.

// stagedProposalBoundary builds a parked DECOMPOSITION_PROPOSAL boundary
// carrying a validated 3-sub-task ExecutionDAG.
func stagedProposalBoundary() *autonomy.HumanBoundary {
	dag := planner.NewExecutionDAG("restyle every row @index.html", "index.html",
		planner.SplitBoundedLines, "digest", 2048)
	subtasks := []planner.SubTask{
		{ID: "st-1", Index: 1, Kind: planner.SplitBoundedLines, Target: "index.html",
			Description: "section [lines 1–4]", Region: planner.Region{StartLine: 1, EndLine: 4}, EstimatedTokens: 120},
		{ID: "st-2", Index: 2, Kind: planner.SplitBoundedLines, Target: "index.html",
			Description: "section [lines 5–37]", Region: planner.Region{StartLine: 5, EndLine: 37}, EstimatedTokens: 900},
		{ID: "st-3", Index: 3, Kind: planner.SplitBoundedLines, Target: "index.html",
			Description: "section [lines 38–64]", Region: planner.Region{StartLine: 38, EndLine: 64}, EstimatedTokens: 780},
	}
	for _, st := range subtasks {
		if err := dag.AddTask(st); err != nil {
			panic(err)
		}
	}
	return &autonomy.HumanBoundary{
		Reason:    dag.ProposalSummary(),
		Targets:   []string{"index.html"},
		Proposal:  dag,
		Action:    autonomy.HumanBoundaryDecomposition,
		Resumable: true,
	}
}

// TestDecompositionProposalRendersInteractiveCard proves a run that parks at
// a DECOMPOSITION_PROPOSAL boundary renders the framed yellow decision card
// with the strategy kind, sub-task line-range breakdowns and action prompts.
func TestDecompositionProposalRendersInteractiveCard(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  stagedProposalBoundary(),
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("restyle every row @index.html")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	if !m.autonomousParked() {
		t.Fatal("the staged proposal must park the run")
	}
	if m.state != StateAwaitingApproval {
		t.Fatalf("state = %s, want awaiting_approval while the proposal is pending", m.state)
	}

	block := m.renderAutonomousBoundaryBlock(120)
	for _, want := range []string{
		"DECOMPOSITION PROPOSAL",
		"SEARCH_REPLACE_BOUNDED_LINES", // strategy kind
		"st-1", "st-2", "st-3",
		"lines 1–4", "lines 5–37", "lines 38–64",
		"[Enter] Authorize & Run DAG",
		"[Esc] Cancel",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("proposal card missing %q:\n%s", want, block)
		}
	}
	// The card is framed by the dedicated double-border proposal box.
	if !strings.Contains(block, "╔") {
		t.Errorf("proposal card must render inside its framed box:\n%s", block)
	}
}

// TestDecompositionProposalViewProjectsCard proves the full View() output
// carries the interactive card while parked at the proposal gate.
func TestDecompositionProposalViewProjectsCard(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  stagedProposalBoundary(),
	}
	m := initializedChatModel(t)
	m.autonomousDriver = drv
	m.width = 120
	cmd := m.runAutonomousDriver("restyle every row @index.html")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)
	m.refreshViewportContent()

	view := m.View()
	for _, want := range []string{
		"DECOMPOSITION PROPOSAL",
		"st-2",
		"Authorize & Run DAG",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q while parked at the proposal gate", want)
		}
	}
}

// TestDecompositionProposalKeyBindings proves Enter maps onto ApproveDAG
// execution (ResumeApproveProposal) and Esc onto plan cancellation
// (ResumeRejectProposal) for a parked DECOMPOSITION_PROPOSAL boundary.
func TestDecompositionProposalKeyBindings(t *testing.T) {
	newPark := func(term *autonomy.LoopTermination) (*model, *fakeAutonomousDriver) {
		drv := &fakeAutonomousDriver{
			state:     autonomy.RuntimeAwaitingHuman,
			parkOnRun: true,
			boundary:  stagedProposalBoundary(),
			term:      term,
		}
		m := autonomousTestModel(drv)
		cmd := m.runAutonomousDriver("restyle every row @index.html")
		msg := extractAutonomousRunMsg(t, cmd())
		m.handleAutonomousRun(msg)
		return m, drv
	}

	completed := &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "dag completed"}
	aborted := &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "plan rejected"}

	// Enter authorizes & runs the DAG.
	m, drv := newPark(completed)
	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if kcmd == nil {
		t.Fatal("Enter on a staged proposal must return a command")
	}
	kcmd()
	if drv.resumeApproveProposal != 1 {
		t.Fatalf("Enter must approve the DAG, ResumeApproveProposal calls = %d", drv.resumeApproveProposal)
	}
	m.handleAutonomousRun(autonomousRunMsg{term: drv.term})
	if m.autonomousParked() {
		t.Fatal("a completed DAG must release the parked boundary")
	}

	// Esc cancels the whole plan.
	m2, drv2 := newPark(aborted)
	_, kcmd2 := m2.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if kcmd2 == nil {
		t.Fatal("Esc on a staged proposal must return a command")
	}
	kcmd2()
	if drv2.resumeRejectProposal != 1 {
		t.Fatalf("Esc must cancel the DAG, ResumeRejectProposal calls = %d", drv2.resumeRejectProposal)
	}
	m2.handleAutonomousRun(autonomousRunMsg{term: drv2.term})
	if m2.autonomousParked() {
		t.Fatal("a cancelled plan must release the parked boundary")
	}
}

// TestDecompositionProposalResumeRequiresProposal proves both resume helpers
// are inert unless a decomposition boundary is actually parked.
func TestDecompositionProposalResumeRequiresProposal(t *testing.T) {
	m := autonomousTestModel(&fakeAutonomousDriver{state: autonomy.RuntimeIdle})
	if cmd := m.resumeAutonomousProposalApprove(); cmd != nil {
		t.Fatalf("approve without a parked proposal must be inert, got %v", cmd)
	}
	if cmd := m.resumeAutonomousProposalReject("x"); cmd != nil {
		t.Fatalf("reject without a parked proposal must be inert, got %v", cmd)
	}
}
