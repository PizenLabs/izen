package ui

// ── THE APPROVAL BOUNDARY REFLECTS AUTHORITATIVE RUNTIME STATE ───────────────
//
// The reported production defect, as the operator saw it:
//
//	execution failed in executor.bounded-step: output exhausted
//	with no new delivered bytes
//	continuation would not advance the artifact
//
//	AUTONOMY APPROVAL — mutation awaiting authorization:
//	index.html, script.js, styles.css
//
//	[press Approve]  authorization: budget-sufficiency:
//	                 mutation budget already exhausted      ← repeatedly
//
// Two runtime facts make that trace impossible:
//
//  1. an approval surface may only exist for a candidate the execution authority
//     still HOLDS (the driver enforces this — see internal/runtime/autonomy), and
//  2. it may only exist for a mutation the runtime's own AuthorizationEngine
//     admits (budget, scope, capability, checkpoint, policy).
//
// These tests pin the projection half: the UI renders whatever the runtime says,
// refuses to arm the pending-approval state for a non-decisional park, and
// CONVERGES on a refused authorization instead of offering the same impossible
// approval again.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/capability"
)

// gateWorkspace is the single fixture every gate test uses: one small text file
// and the SEARCH/REPLACE artifact a model working from its real bytes produces.
const (
	gateTarget      = "note.txt"
	gateOriginal    = "bar\n"
	gateReplacement = "qux\n"
)

// newGateModel builds a model parked at an approval boundary whose candidate IS
// genuinely held by the executor, wired to a real AuthorizationEngine. mutBudget
// is the session mutation budget the approve path is judged against.
//
// The candidate is real on purpose: a fabricated PatchID would exercise the
// refusal path (which the UI now takes) instead of the approval path.
func newGateModel(t *testing.T, mutBudget *budget.MutationBudget) (*model, *fakeAutonomousDriver, string, string) {
	t.Helper()
	root := t.TempDir()
	targetPath := filepath.Join(root, gateTarget)
	if err := os.WriteFile(targetPath, []byte("foo\n"+gateOriginal+"baz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := readyChatModel(newTestModel())
	patchID := holdRealCandidate(t, m, root, gateTarget, gateOriginal, gateReplacement)

	m.autonomousDriver = &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   patchID,
			Reason:    "mutation ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{gateTarget},
		},
		// The terminal a successful approve converges to. parkOnRun keeps Run
		// parked, so this only answers Resume*.
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeCompleted,
			Reason: "applied",
		},
	}
	armGateGovernance(m, mutBudget)
	return m, m.autonomousDriver.(*fakeAutonomousDriver), patchID, root
}

// armGateGovernance puts the model into Building with a real authorization
// engine, capability grant and mutation budget — the exact state the approve
// path is supposed to be judged in.
func armGateGovernance(m *model, mutBudget *budget.MutationBudget) {
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
	m.mutationBudget = mutBudget
	micro := budget.DefaultMicroBudget()
	m.microBudget = &micro
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
}

// parkAtApproval runs the fake driver and projects its park, asserting that the
// approval card is on screen.
func parkAtApproval(t *testing.T, m *model) {
	t.Helper()
	msg := extractAutonomousRunMsg(t, m.runAutonomousDriver("update @"+gateTarget)())
	m.handleAutonomousRun(msg)
	if !m.autonomousParked() {
		t.Fatal("precondition: the run must be parked at the approval gate")
	}
	b := m.autonomousBoundary
	if b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("precondition: expected an approval boundary, got %+v", b)
	}
}

func spentMutationBudget() *budget.MutationBudget {
	mb := budget.NewBudget(1, 1000, 100000, 3, 30*time.Minute, 10)
	if err := mb.Consume(budget.BudgetDelta{Files: 1}); err != nil {
		panic(err)
	}
	if !mb.IsExhausted() {
		panic("precondition: the mutation budget must be exhausted")
	}
	return mb
}

func viewportOf(m *model) string { return recordsText(m) }

// TestApprovalGate_RefusedAuthorizationConvergesInsteadOfRepeating is the direct
// regression for "authorization then fails repeatedly". With the mutation budget
// genuinely spent, pressing Alt+A must state the terminal refusal, close the
// gate, and leave nothing behind that would invite the same impossible approval
// again.
func TestApprovalGate_RefusedAuthorizationConvergesInsteadOfRepeating(t *testing.T) {
	m, drv, _, root := newGateModel(t, spentMutationBudget())
	parkAtApproval(t, m)

	if cmd := m.resumeAutonomousApprove(); cmd != nil {
		t.Error("a refused authorization must not dispatch a driver resume")
	}
	if m.autonomousParked() {
		t.Fatal("a refused authorization left the approval gate parked: the same impossible approval can be requested again")
	}
	if m.autonomousBoundary != nil {
		t.Fatalf("a refused authorization kept the boundary: %+v", m.autonomousBoundary)
	}
	if m.state == StateAwaitingApproval {
		t.Error("a refused authorization left the model awaiting approval")
	}
	if drv.resumeApprove != 0 {
		t.Fatalf("driver ResumeApprove calls = %d, want 0", drv.resumeApprove)
	}

	text := viewportOf(m)
	if !strings.Contains(text, "budget") || !strings.Contains(text, "exhausted") {
		t.Errorf("the surfaced reason is not the truthful budget outcome:\n%s", text)
	}

	got, err := os.ReadFile(filepath.Join(root, gateTarget))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), gateReplacement) {
		t.Fatalf("a refused authorization mutated the workspace: %q", string(got))
	}

	before := viewportOf(m)
	if cmd := m.resumeAutonomousApprove(); cmd != nil {
		t.Error("a second press on a closed gate must not dispatch anything")
	}
	if after := viewportOf(m); after != before {
		t.Errorf("a second press re-opened the impossible approval:\n%s", after)
	}
}

// TestApprovalGate_AnAgedSessionCanStillAuthorizeMutation is the regression for
// the budget semantics that produced the refusal above: MaxExecutionTime is a
// per-operation bound, so a session that has simply been OPEN longer than it must
// not be permanently unauthorized.
func TestApprovalGate_AnAgedSessionCanStillAuthorizeMutation(t *testing.T) {
	mb := budget.NewBudget(10, 1000, 100000, 3, 30*time.Second, 10)
	mb.BeginOperation()
	time.Sleep(2 * time.Millisecond)
	mb.BeginOperation()

	m, drv, _, _ := newGateModel(t, mb)
	parkAtApproval(t, m)

	cmd := m.resumeAutonomousApprove()
	if cmd == nil {
		t.Fatalf("an unspent, aged budget must still authorize; viewport:\n%s", viewportOf(m))
	}
	msg := extractAutonomousRunMsg(t, cmd())
	if msg.term == nil || msg.term.State != autonomy.RuntimeCompleted {
		t.Fatalf("resume outcome = %+v, want completed", msg.term)
	}
	if drv.resumeApprove != 1 {
		t.Fatalf("driver ResumeApprove calls = %d, want 1", drv.resumeApprove)
	}
}

// TestApprovalGate_AnUnheldCandidateNeverReachesApproval proves the UI refuses a
// boundary whose candidate the execution authority no longer holds — the exact
// shape of "a failed computation is still visible to authorization".
func TestApprovalGate_AnUnheldCandidateNeverReachesApproval(t *testing.T) {
	m, drv, patchID, _ := newGateModel(t, budget.NewBudget(10, 1000, 100000, 3, 30*time.Minute, 10))
	parkAtApproval(t, m)

	// The candidate is superseded / drained between the park and the human answer.
	if _, err := m.executor.Reject(context.Background(), patchID, "superseded"); err != nil {
		t.Fatalf("draining the held candidate: %v", err)
	}

	if cmd := m.resumeAutonomousApprove(); cmd != nil {
		t.Error("approving a candidate the executor no longer holds must not dispatch anything")
	}
	if drv.resumeApprove != 0 {
		t.Fatalf("driver ResumeApprove calls = %d, want 0", drv.resumeApprove)
	}
	if m.autonomousParked() {
		t.Fatal("a stale candidate left the approval gate parked")
	}
	if text := viewportOf(m); !strings.Contains(text, "no longer held") {
		t.Errorf("the refusal does not name the real cause:\n%s", text)
	}
}

// TestApprovalGate_InformParkDoesNotArmThePendingApprovalState pins the smaller
// truth defect this report exposed: an informational pause is not a permission
// request. Freezing the workflow as "awaiting authorization" for a park with no
// resume decision is a lie about state, and it is part of why the operator was
// looking at an approval surface the runtime had already closed.
func TestApprovalGate_InformParkDoesNotArmThePendingApprovalState(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason:    "hard-block: bounded recovery exhausted — explicit re-scope required",
			Action:    autonomy.HumanBoundaryInform,
			Resumable: false,
			Targets:   []string{gateTarget},
		},
	}
	m := autonomousTestModel(drv)
	m.workflowSM = workflow.NewWorkflowStateMachine()
	msg := extractAutonomousRunMsg(t, m.runAutonomousDriver("update @"+gateTarget)())
	m.handleAutonomousRun(msg)

	if m.state == StateAwaitingApproval {
		t.Error("an inform park armed the pending-approval workflow state")
	}
	if m.workflowSM.PendingApproval() {
		t.Error("an inform park marked the workflow as awaiting authorization")
	}
}

// TestApprovalGate_RendersNoAutonomyApprovalForANonApprovalBoundary is the direct
// rendering assertion: an admission-refused boundary must never render the
// AUTONOMY APPROVAL card, in either the log form or the modal block form.
func TestApprovalGate_RendersNoAutonomyApprovalForANonApprovalBoundary(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			Reason: "mutation is not admissible: authorization: budget-sufficiency: mutation budget already exhausted",
			Action: autonomy.HumanBoundaryInform, Resumable: false,
			Targets: []string{"index.html", "script.js", "styles.css"},
		},
	}
	m := autonomousTestModel(drv)
	msg := extractAutonomousRunMsg(t, m.runAutonomousDriver("redesign the portfolio")())
	m.handleAutonomousRun(msg)

	text := viewportOf(m)
	if strings.Contains(text, "AUTONOMY APPROVAL") {
		t.Errorf("an admission-refused boundary rendered the AUTONOMY APPROVAL card:\n%s", text)
	}
	if strings.Contains(text, "mutation awaiting authorization") {
		t.Errorf("an admission-refused boundary rendered 'mutation awaiting authorization':\n%s", text)
	}
	if block := m.renderAutonomousBoundaryBlock(120); strings.Contains(block, "AUTONOMY APPROVAL") {
		t.Errorf("the approval modal was rendered for a refused boundary:\n%s", block)
	}
	if !strings.Contains(text, "not admissible") {
		t.Errorf("the terminal refusal reason is not visible:\n%s", text)
	}
}

// TestExecutorApprovalGate_RefusedAuthorizationDrainsTheHeldCandidate is the
// direct-executor half of the boundary fix: a proposal whose authorization the
// runtime refuses is RELEASED (rejected through the executor, so the ledger
// records the true outcome) instead of lingering as an approvable candidate.
func TestExecutorApprovalGate_RefusedAuthorizationDrainsTheHeldCandidate(t *testing.T) {
	m, _, patchID, root := newGateModel(t, spentMutationBudget())
	m.executorPendingPatchID = patchID
	m.executorPendingTargets = []string{gateTarget}
	m.enterApprovalState()

	msg := m.runExecutorApproveCmd(patchID)()
	if _, ok := msg.(executionResultMsg); !ok {
		t.Fatalf("a refused authorization must surface as an executionResultMsg error, got %T", msg)
	}
	if m.executor.CandidateHeld(patchID) {
		t.Fatal("a candidate whose authorization was refused is still held and approvable")
	}
	if m.executorPendingPatchID != "" {
		t.Errorf("the UI still points at candidate %q", m.executorPendingPatchID)
	}
	if m.state == StateAwaitingApproval {
		t.Error("a refused authorization left the model awaiting approval")
	}
	got, err := os.ReadFile(filepath.Join(root, gateTarget))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), gateReplacement) {
		t.Fatalf("a refused authorization mutated the workspace: %q", string(got))
	}
}
