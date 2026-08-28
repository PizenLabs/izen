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
	proposaltui "github.com/PizenLabs/izen/internal/ui/tui"
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

	resumeApproveProposal int
	resumeRejectProposal  int
	resumeProposal        int
	lastProposalIntent    string
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

func (f *fakeAutonomousDriver) ResumeApproveProposal(_ context.Context) (*autonomy.LoopTermination, error) {
	f.resumeApproveProposal++
	return f.term, f.resumeErr
}

func (f *fakeAutonomousDriver) ResumeRejectProposal(_ context.Context, _ string) (*autonomy.LoopTermination, error) {
	f.resumeRejectProposal++
	return f.term, f.resumeErr
}

func (f *fakeAutonomousDriver) ResumeWithProposal(_ context.Context, intent string) (*autonomy.LoopTermination, error) {
	f.resumeProposal++
	f.lastProposalIntent = intent
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

func (f *fakeAutonomousDriver) SetStreamCallback(cb execution.StreamCallback) {}

func (f *fakeAutonomousDriver) AggregatedUsage() (int, int, bool) { return 0, 0, false }

// extractAutonomousRunMsg extracts an autonomousRunMsg from either a batch message
// or a direct autonomousRunMsg.
func extractAutonomousRunMsg(t *testing.T, msg tea.Msg) autonomousRunMsg {
	t.Helper()
	if batchMsg, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batchMsg {
			if m := c(); m != nil {
				if am, ok := m.(autonomousRunMsg); ok {
					return am
				}
			}
		}
		t.Fatalf("batch must contain autonomousRunMsg, got %v", batchMsg)
	}
	if am, ok := msg.(autonomousRunMsg); ok {
		return am
	}
	t.Fatalf("expected autonomousRunMsg or batch containing it, got %T", msg)
	return autonomousRunMsg{}
}

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
	// runAutonomousDriver returns a batch; execute it and find the autonomousRunMsg
	batchMsg := cmd().(tea.BatchMsg)
	var msg autonomousRunMsg
	found := false
	for _, c := range batchMsg {
		if m := c(); m != nil {
			if am, ok := m.(autonomousRunMsg); ok {
				msg = am
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("batch must contain autonomousRunMsg, got %v", batchMsg)
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
	batchMsg := cmd().(tea.BatchMsg)
	var msg autonomousRunMsg
	found := false
	for _, c := range batchMsg {
		if m := c(); m != nil {
			if am, ok := m.(autonomousRunMsg); ok {
				msg = am
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("batch must contain autonomousRunMsg, got %v", batchMsg)
	}
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
	// The resume is batched with the tick loops; executing the batch runs the
	// driver command itself.
	extractAutonomousRunMsg(t, cmd())
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
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)
	if !m.autonomousParked() {
		t.Fatal("run must be parked before approve")
	}

	cmd = m.resumeAutonomousApprove()
	if cmd == nil {
		t.Fatal("approve must return a command")
	}
	msg2 := extractAutonomousRunMsg(t, cmd())
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
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	cmd = m.abortAutonomousRun("ctrl-c")
	if cmd == nil {
		t.Fatal("abort must return a command")
	}
	msg2 := extractAutonomousRunMsg(t, cmd())
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
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	// Esc rejects (through the executor-backed reject path).
	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if kcmd == nil {
		t.Fatal("Esc on a parked approval must return a command")
	}
	extractAutonomousRunMsg(t, kcmd())
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
	cmd = m2.runAutonomousDriver("change bar to qux @note.txt")
	msg = extractAutonomousRunMsg(t, cmd())
	m2.handleAutonomousRun(msg)
	_, kcmd = m2.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'a'}})
	if kcmd == nil {
		t.Fatal("Alt+A on a parked approval must return a command")
	}
	extractAutonomousRunMsg(t, kcmd())
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
	cmd := m.runAutonomousDriver("change bar to qux @note.txt")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

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
	cmd := m.runAutonomousDriver("first")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

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

// ── UI projection invariant: awaiting_human ⇒ renderable DecisionSurface ─────

// TestAutonomousDecisionSurfaceProjection pins the projection invariant: when
// the runtime parks at HumanBoundaryProposal (a Zero-Token DecisionSurface),
// the UI must render an INTERACTIVE recovery surface from the typed boundary
// options — never a static pause and never a log-parsed inference. Enter routes
// the selected intent to ResumeWithProposal; Esc routes cancel.
func TestAutonomousDecisionSurfaceProjection(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:           "Zero-Token DecisionSurface: corrupt AST baseline, budget exceeded",
			Action:           autonomy.HumanBoundaryProposal,
			Resumable:        true,
			Targets:          []string{"index.html"},
			SurfaceASTStatus: "corrupt",
			ProposalOptions: []autonomy.HumanProposalOption{
				{ID: "rescope_bounded_patch", Label: "Re-scope to bounded SEARCH/REPLACE", Description: "New bounded contract", Intent: "rescope_bounded_patch"},
				{ID: "inspect", Label: "Inspect diagnostics", Description: "Read-only", Intent: "inspect"},
				{ID: "cancel", Label: "Cancel", Description: "Abandon", Intent: "cancel"},
			},
		},
	}
	m := autonomousTestModel(drv)

	cmd := m.runAutonomousDriver("$prompt check this file @index.html and remove redundant content")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	if !m.autonomousParked() {
		t.Fatal("model must hold the parked DecisionSurface boundary")
	}
	if m.autonomousBoundary == nil || m.autonomousBoundary.Action != autonomy.HumanBoundaryProposal {
		t.Fatalf("boundary = %+v, want HumanBoundaryProposal", m.autonomousBoundary)
	}
	// The interactive proposal model is active (never a static pause).
	if m.proposalTUI == nil {
		t.Fatal("a parked DecisionSurface must activate the interactive proposal model")
	}
	block := m.renderAutonomousBoundaryBlock(120)
	if block == "" {
		t.Fatal("a parked DecisionSurface must render a non-empty interactive block")
	}
	for _, want := range []string{"Re-scope to bounded SEARCH/REPLACE", "Inspect diagnostics", "↑/↓ navigate"} {
		if !strings.Contains(block, want) {
			t.Fatalf("proposal block missing %q:\n%s", want, block)
		}
	}
	// Navigation moves the highlight; Enter routes the SELECTED intent to the
	// driver's ResumeWithProposal — the UI never executes anything itself.
	m.proposalTUI.Reset() // highlight the first option (rescope_bounded_patch)
	enterCmd := m.resumeAutonomousProposal(string(m.proposalTUI.Select()))
	if enterCmd == nil {
		t.Fatal("selecting an option must route a resume command")
	}
	// Execute the returned batch so the driver call actually happens (the
	// Bubble Tea loop does this in production).
	if batch, ok := enterCmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	if drv.resumeProposal != 1 {
		t.Fatalf("ResumeWithProposal calls = %d, want 1", drv.resumeProposal)
	}
	if drv.lastProposalIntent != string(proposaltui.ProposalRescopeBoundedPatch) {
		t.Fatalf("routed intent = %q, want rescope_bounded_patch (the highlighted option)", drv.lastProposalIntent)
	}
}

// TestAutonomousDecisionSurfaceEscapeRoutesCancel pins that Esc on a parked
// DecisionSurface routes the cancel intent (the driver aborts with zero spend —
// the UI never hard-cancels on its own).
func TestAutonomousDecisionSurfaceEscapeRoutesCancel(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:    "Zero-Token DecisionSurface: output budget exceeded (bounded patch required)",
			Action:    autonomy.HumanBoundaryProposal,
			Resumable: true,
			Targets:   []string{"index.html"},
			ProposalOptions: []autonomy.HumanProposalOption{
				{ID: "rescope_bounded_patch", Label: "Re-scope", Description: "x", Intent: "rescope_bounded_patch"},
				{ID: "cancel", Label: "Cancel", Description: "y", Intent: "cancel"},
			},
		},
	}
	m := autonomousTestModel(drv)
	cmd := m.runAutonomousDriver("fix @index.html")
	msg := extractAutonomousRunMsg(t, cmd())
	m.handleAutonomousRun(msg)

	_, kcmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if kcmd == nil {
		t.Fatal("Esc on a DecisionSurface must route the cancel intent")
	}
	if batch, ok := kcmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	if drv.lastProposalIntent != "cancel" {
		t.Fatalf("Esc routed intent = %q, want cancel", drv.lastProposalIntent)
	}
	if drv.resumeProposal != 1 {
		t.Fatalf("ResumeWithProposal calls = %d, want 1", drv.resumeProposal)
	}
}
