package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── TUI EVENT-LOOP DECOUPLING (spinner contract during DAG_EXECUTING) ──────
//
// The approved DAG executes inside a non-blocking tea.Cmd goroutine. The
// update thread only stays alive if spin.Tick commands are batched alongside
// it AND the keep-alive gates count the autonomous run as active work —
// otherwise no message re-enters Update until the terminal msg lands and the
// spinner freezes for the whole transaction.

// TestProposalApproveKeepsSpinnerTicksAliveDuringDAGExecution proves Enter on
// a DECOMPOSITION_PROPOSAL arms the animation loops together with the driver
// goroutine, keeps re-arming them per frame while DAG_EXECUTING runs, survives
// a mid-run shimmer clear, and releases everything on the terminal outcome.
func TestProposalApproveKeepsSpinnerTicksAliveDuringDAGExecution(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:    "staged plan",
			Action:    autonomy.HumanBoundaryDecomposition,
			Resumable: true,
			Targets:   []string{"index.html"},
			Proposal: &planner.ExecutionDAG{
				Objective: "restyle every row", Target: "index.html",
				Kind: planner.SplitBoundedLines, Status: planner.PlanStaged,
				SubTasks: []planner.SubTask{{
					ID: "st-1", Index: 1, Kind: planner.SplitBoundedLines,
					Region: planner.Region{StartLine: 1, EndLine: 4},
				}},
			},
		},
		term: &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "dag complete"},
	}
	m := autonomousTestModel(drv)

	// Park at the proposal boundary.
	cmd := m.runAutonomousDriver("restyle every row @index.html")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)
	if !m.autonomousParked() || m.autonomousBoundary.Action != autonomy.HumanBoundaryDecomposition {
		t.Fatal("run must park at the DECOMPOSITION_PROPOSAL boundary")
	}

	// ENTER authorizes & runs the DAG.
	resume := m.resumeAutonomousProposalApprove()
	if resume == nil {
		t.Fatal("proposal approve must return a command")
	}
	batch, ok := resume().(tea.BatchMsg)
	if !ok {
		t.Fatalf("driver resume must be batched with the tick loops, got %T", resume())
	}
	if len(batch) < 3 {
		t.Fatalf("batch = %d cmds, want >= 3 (driver goroutine + smooth tick + shimmer tick)", len(batch))
	}
	// The FIRST command is the non-blocking driver execution.
	if am, ok := batch[0]().(autonomousRunMsg); !ok || am.term == nil {
		t.Fatalf("first batched cmd must be the driver run, got %v", batch[0])
	}

	// Execution-state markers: the run is active and the shimmer armed, so
	// the spinner cannot be reconciled away mid-flight.
	if !m.autonomousActive {
		t.Fatal("DAG_EXECUTING must mark the run active so the tick loops survive")
	}
	if !m.shimmerActive {
		t.Fatal("loading shimmer must stay armed during DAG execution")
	}

	// Per-frame re-arm: each spinner frame schedules the next tick.
	_, frameCmd := m.Update(shimmerFrameMsg{})
	if frameCmd == nil {
		t.Fatal("shimmerFrameMsg must re-arm the spinner tick during DAG_EXECUTING")
	}

	// Resilience: even if a mid-run stream event cleared the shimmer, the
	// smooth tick loop keeps advancing frames because the run is active.
	m.stopShimmer()
	_, tickCmd := m.Update(smoothStreamTickMsg(time.Now()))
	if tickCmd == nil {
		t.Fatal("smoothStreamTickMsg must keep the render loop alive during DAG_EXECUTING even without shimmer")
	}

	// Terminal outcome releases the operation and every tick loop.
	m.handleAutonomousRun(autonomousRunMsg{term: drv.term})
	if m.autonomousActive {
		t.Fatal("a completed DAG must release the active marker")
	}
	if _, idleCmd := m.Update(tickMsg(time.Now())); idleCmd != nil {
		t.Fatal("tick loop must stop once the DAG terminal outcome landed")
	}
}
