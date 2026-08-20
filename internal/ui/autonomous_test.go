package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── PHASE 6 DRIVER BRIDGE TESTS ────────────────────────────────────────
// These tests drive the production autonomous Driver through the UI's
// structural interface (autonomousDriver). The driver is faked so the tests
// assert the UI contract: initiate, park, authorize-then-approve, reject,
// clarify-select, abort — never the loop's internal behavior (covered by
// internal/runtime/autonomy tests).

// fakeAutonomousDriver implements autonomousDriver with programmable
// Run/Resume/Abort outcomes. parkOnRun makes the initial Run park (nil term) so
// Resume*/Abort can later return the programmed terminal outcome.
type fakeAutonomousDriver struct {
	state         autonomy.RuntimeState
	boundary      *autonomy.HumanBoundary
	term          *autonomy.LoopTermination
	parkOnRun     bool
	runErr        error
	resumeErr     error
	abortReason   string
	abortCount    int
	runCount      int
	resumeApprove int
	resumeReject  int
	resumeClarify int
	lastClarify   string
}

func (f *fakeAutonomousDriver) Run(_ context.Context, _ string) (*autonomy.LoopTermination, error) {
	f.runCount++
	if f.parkOnRun {
		return nil, f.runErr
	}
	return f.term, f.runErr
}

func (f *fakeAutonomousDriver) ResumeApprove(_ context.Context) (*autonomy.LoopTermination, error) {
	f.resumeApprove++
	return f.term, f.resumeErr
}

func (f *fakeAutonomousDriver) ResumeReject(_ context.Context, _ string) (*autonomy.LoopTermination, error) {
	f.resumeReject++
	return f.term, f.resumeErr
}

func (f *fakeAutonomousDriver) ResumeClarify(_ context.Context, target string) (*autonomy.LoopTermination, error) {
	f.resumeClarify++
	f.lastClarify = target
	return f.term, f.resumeErr
}

func (f *fakeAutonomousDriver) Abort(reason string) (*autonomy.LoopTermination, error) {
	f.abortCount++
	f.abortReason = reason
	return f.term, nil
}

func (f *fakeAutonomousDriver) State() autonomy.RuntimeState { return f.state }
func (f *fakeAutonomousDriver) Boundary() *autonomy.HumanBoundary {
	return f.boundary
}
func (f *fakeAutonomousDriver) Termination() *autonomy.LoopTermination { return f.term }

// autonomousTestModel is a chat-ready model wired with the fake driver.
func autonomousTestModel(drv *fakeAutonomousDriver) *model {
	m := readyChatModel(newTestModel())
	m.autonomousDriver = drv
	return m
}

// TestAutonomousRunParksAtApproval proves a driver run that parks at an
// approval boundary renders the boundary card, enters StateAwaitingApproval,
// and keeps the run parked (not terminal) for a human decision.
func TestAutonomousRunParksAtApproval(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "mutation ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
		term: nil,
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("change bar to qux @note.txt")
	if cmd == nil {
		t.Fatal("runAutonomousDriver must return a command")
	}
	msg, ok := cmd().(autonomousRunMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want autonomousRunMsg", cmd())
	}
	next := m.handleAutonomousRun(msg)
	if next != nil {
		t.Fatalf("parked outcome must not return a cmd, got %v", next)
	}
	if drv.runCount != 1 {
		t.Fatalf("driver Run calls = %d, want 1", drv.runCount)
	}
	if m.autonomousActive {
		t.Fatal("a parked run must not stay 'active'")
	}
	if !m.autonomousParked() {
		t.Fatal("model must hold the parked boundary")
	}
	if m.state != StateAwaitingApproval {
		t.Fatalf("state = %s, want awaiting_approval", m.state)
	}
	if m.autonomousBoundary == nil || m.autonomousBoundary.PatchID != "p1" {
		t.Fatalf("boundary = %+v, want p1", m.autonomousBoundary)
	}
	if got := m.renderAutonomousBoundaryBlock(120); !strings.Contains(got, "AUTONOMY APPROVAL") {
		t.Fatalf("boundary block missing approval title: %q", got)
	}
}

// TestAutonomousRunParksAtClarify proves a clarify park renders the candidate
// list and Enter resumes with the selected target.
func TestAutonomousRunParksAtClarify(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:    "ambiguous target",
			Action:    autonomy.HumanBoundaryClarify,
			Resumable: true,
			Options:   []string{"a.txt", "b.txt"},
		},
		term: nil,
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("change @* to something")
	msg := cmd().(autonomousRunMsg)
	m.handleAutonomousRun(msg)
	if m.autonomousBoundary == nil || m.autonomousBoundary.Action != autonomy.HumanBoundaryClarify {
		t.Fatalf("boundary = %+v, want clarify", m.autonomousBoundary)
	}
	// Selection starts at the first candidate.
	m.navigateAutonomousBoundary(1)
	block := m.renderAutonomousBoundaryBlock(120)
	if !strings.Contains(block, "b.txt") || !strings.Contains(block, "AUTONOMY TARGET SELECTION") {
		t.Fatalf("clarify block missing candidates: %q", block)
	}
	cmd = m.resumeAutonomousClarify()
	if cmd == nil {
		t.Fatal("clarify resume must return a command")
	}
	cmd()
	if drv.resumeClarify != 1 || drv.lastClarify != "b.txt" {
		t.Fatalf("resumeClarify = %d (%q), want 1 (b.txt)", drv.resumeClarify, drv.lastClarify)
	}
}

// TestAutonomousResumeApproveAuthorizesExecutor proves approving the parked
// boundary issues a MutationAuthorization over the boundary targets and
// attaches it to the executor BEFORE the driver resumes.
func TestAutonomousResumeApproveAuthorizesExecutor(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeCompleted,
			Reason: "applied",
			Class:  autonomy.FailureRecoverable,
		},
	}
	m := autonomousTestModel(drv)
	// Set workflow to Building state so authorization succeeds.
	m.workflowSM = workflow.NewWorkflowStateMachine()
	_ = m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{})
	_ = m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{
		HasPlan:         true,
		HasCapabilities: true,
	})
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{},
		fakeCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	m.mutationBudget = budget.NewBudget(10, 1000, 100000, 3, 1e15, 10)
	mb := budget.DefaultMicroBudget()
	m.microBudget = &mb
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, &mockProvider{responses: []*ai.Response{}}, nil, "")

	// Park the boundary first.
	cmd := m.runAutonomousDriver("change bar to qux @note.txt")
	msg := cmd().(autonomousRunMsg)
	m.handleAutonomousRun(msg)
	if !m.autonomousParked() {
		t.Fatal("run must be parked before approve")
	}

	cmd = m.resumeAutonomousApprove()
	if cmd == nil {
		t.Fatal("approve must return a command")
	}
	msg2 := cmd().(autonomousRunMsg)
	if msg2.term == nil || msg2.term.State != autonomy.RuntimeCompleted {
		t.Fatalf("resume outcome = %+v, want completed", msg2.term)
	}
	if drv.resumeApprove != 1 {
		t.Fatalf("driver ResumeApprove calls = %d, want 1", drv.resumeApprove)
	}

	// The terminal outcome releases the operation and the boundary.
	m.handleAutonomousRun(msg2)
	if m.autonomousParked() {
		t.Fatal("a completed run must not stay parked")
	}
	if m.autonomousActive {
		t.Fatal("a completed run must not stay active")
	}
	if m.state == StateAwaitingApproval {
		t.Fatal("a completed run must resolve the approval state")
	}
}

// TestAutonomousAbortParkedProvesTerminal proves Ctrl+C / Esc abort the parked
// run through driver.Abort and the model releases the boundary.
func TestAutonomousAbortParkedProvesTerminal(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeAborted,
			Reason: "aborted by operator",
			Class:  autonomy.FailurePermanent,
		},
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("change bar to qux @note.txt")
	msg := cmd().(autonomousRunMsg)
	m.handleAutonomousRun(msg)

	cmd = m.abortAutonomousRun("ctrl-c")
	if cmd == nil {
		t.Fatal("abort must return a command")
	}
	msg2 := cmd().(autonomousRunMsg)
	if msg2.term == nil || msg2.term.State != autonomy.RuntimeAborted {
		t.Fatalf("abort outcome = %+v, want aborted", msg2.term)
	}
	if drv.abortCount != 1 || drv.abortReason != "ctrl-c" {
		t.Fatalf("Abort = %d (%q), want 1 (ctrl-c)", drv.abortCount, drv.abortReason)
	}
	m.handleAutonomousRun(msg2)
	if m.autonomousParked() || m.autonomousActive {
		t.Fatal("aborted run must fully release the boundary")
	}
}

// TestAutonomousDriverKeyBindings proves Alt+A approves, Esc rejects, and
// Ctrl+C aborts a parked approval boundary through the key handler.
func TestAutonomousDriverKeyBindings(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeAborted,
			Reason: "rejected by operator",
			Class:  autonomy.FailurePermanent,
		},
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("change bar to qux @note.txt")
	m.handleAutonomousRun(cmd().(autonomousRunMsg))

	// Esc rejects (through the executor-backed reject path).
	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if kcmd == nil {
		t.Fatal("Esc on a parked approval must return a command")
	}
	kcmd()
	if drv.resumeReject != 1 {
		t.Fatalf("Esc must reject, resumeReject = %d", drv.resumeReject)
	}
	m.handleAutonomousRun(autonomousRunMsg{term: drv.term})
	if m.autonomousParked() {
		t.Fatal("rejected run must not stay parked")
	}

	// Alt+A approves.
	drv2 := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeCompleted,
			Reason: "applied",
			Class:  autonomy.FailureRecoverable,
		},
	}
	m2 := autonomousTestModel(drv2)
	m2.handleAutonomousRun(m2.runAutonomousDriver("change bar to qux @note.txt")().(autonomousRunMsg))
	_, kcmd = m2.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'a'}})
	if kcmd == nil {
		t.Fatal("Alt+A on a parked approval must return a command")
	}
	kcmd()
	if drv2.resumeApprove != 1 {
		t.Fatalf("Alt+A must approve, resumeApprove = %d", drv2.resumeApprove)
	}
}

// TestExecuteAutonomyViaDriverFallsBackToRuntime proves the driver bridge
// falls back to the single-shot runtime executor when the driver is not wired
// (harness), preserving the pre-Phase-6 build path.
func TestExecuteAutonomyViaDriverFallsBackToRuntime(t *testing.T) {
	m := autonomousTestModel(nil)
	m.autonomousDriver = nil
	m.gateway = nil
	m.executor = nil
	cmd := m.executeAutonomyViaDriver(autonomy.Trace{})
	if cmd != nil {
		t.Fatalf("fallback without runtime wiring must return nil cmd, got %v", cmd)
	}
	if !strings.Contains(recordsText(m), "execution runtime not wired") {
		t.Fatalf("fallback must surface the wiring error, got: %s", recordsText(m))
	}
}

// TestAutonomousInformBoundaryRendersNonResumable proves a recovery-exhaustion
// park renders an inform card and is NOT treated as a resumable decision.
func TestAutonomousInformBoundaryRendersNonResumable(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:    "recovery budget exhausted",
			Action:    autonomy.HumanBoundaryInform,
			Resumable: false,
		},
		term: nil,
	}
	m := autonomousTestModel(drv)
	m.handleAutonomousRun(m.runAutonomousDriver("change bar to qux @note.txt")().(autonomousRunMsg))

	if m.autonomousBoundary == nil || m.autonomousBoundary.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("boundary = %+v, want inform", m.autonomousBoundary)
	}
	// Inform boundaries have no resume decisions: approve/reject/clarify keys
	// must be inert.
	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if kcmd != nil {
		t.Fatalf("Enter on an inform boundary must be inert, got %v", kcmd)
	}
	block := m.renderAutonomousBoundaryBlock(120)
	if !strings.Contains(block, "AUTONOMY PAUSED") {
		t.Fatalf("inform block missing title: %q", block)
	}
}

// TestAutonomousDuplicateStartGuard proves the UI refuses to start a second
// driver run while one is parked.
func TestAutonomousDuplicateStartGuard(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
	}
	m := autonomousTestModel(drv)
	m.handleAutonomousRun(m.runAutonomousDriver("first")().(autonomousRunMsg))

	if cmd := m.runAutonomousDriver("second"); cmd != nil {
		t.Fatalf("second run while parked must be refused, got %v", cmd)
	}
	if drv.runCount != 1 {
		t.Fatalf("driver Run calls = %d, want 1 (second start must not reach the driver)", drv.runCount)
	}
	if m.autonomousBoundary == nil || m.autonomousBoundary.PatchID != "p1" {
		t.Fatal("the parked boundary must survive a refused second start")
	}
}
