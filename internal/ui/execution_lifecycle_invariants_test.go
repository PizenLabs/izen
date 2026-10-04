package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	runtimeAutonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
	runtimeorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
	"github.com/PizenLabs/izen/internal/session"
)

// ── EXECUTION-LIFECYCLE INVARIANTS ──────────────────────────────────────────
//
// The reported production failure, exactly:
//
//	$prompt Create one file: index.html
//	→ artifact candidate produced
//	→ plan step completed
//	→ MUTATION REVIEW (mutation awaiting authorization)
//	→ [Enter] approve
//	→ authorization refused:
//	    workflow-state: expected Building or Repairing for build execution, got idle
//
// Every test below is one numbered invariant from the report, expressed as an
// executable assertion against the PRODUCTION-SHAPED wiring: a real
// WorkflowStateMachine, a real PhaseManager bound to it, a real
// AuthorizationEngine that reads the machine, and a real RuntimeExecutor holding
// a real candidate.
//
// The shape matters. The original defect was invisible to every existing test
// because none of them bound an orchestrator to the state machine the
// authorization engine reads — so the two could disagree without anything
// noticing.

// lifecycleFixture is the production shape: SM + orchestrator bound to it +
// authorization engine reading it + a real held candidate.
type lifecycleFixture struct {
	m         *model
	root      string
	target    string
	patchID   string
	preview   execution.CandidatePreview
	digest    string
	authEng   *authorization.AuthorizationEngine
	mutBudget *budget.MutationBudget
	caps      *capability.CapabilitySet
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	root := t.TempDir()
	const target = "index.html"
	const original = "<html>\n<body>\n  <h1>old</h1>\n</body>\n</html>\n"
	const replacement = "<html>\n<body>\n  <h1>new</h1>\n</body>\n</html>\n"
	if err := os.WriteFile(filepath.Join(root, target), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	m := readyChatModel(newTestModel())
	// ── PRODUCTION SHAPE: the orchestrator OWNS the state machine, and the
	// authorization engine READS that same machine. Every authority in the loop
	// therefore agrees by construction.
	sm := workflow.NewWorkflowStateMachine()
	m.workflowSM = sm
	m.orch = runtimeorch.New(sm, nil)

	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps

	mb := budget.NewBudget(10, 1000, 100000, 3, 60*1e9, 10)
	m.mutationBudget = mb
	micro := budget.DefaultMicroBudget()
	m.microBudget = &micro

	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{}, fakeCheckpointChecker{},
		func() workflow.WorkflowState { return sm.State() },
	)

	// A GENUINE held candidate: a fabricated identity would exercise the refusal
	// path instead of the approval path this file tests.
	patchID := holdRealCandidate(t, m, root, target, original, replacement)
	preview, ok := m.executor.CandidatePreview(patchID)
	if !ok {
		t.Fatal("the freshly produced candidate is not previewable; the mutation review would be a lie")
	}

	// A driver is attached from the start: a parked run is a RUNTIME fact, so a
	// harness that parks without one would be asserting a state the production
	// shape never produces.
	m.autonomousDriver = &fakeAutonomousDriver{
		runID:     "run-2",
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
	}

	return &lifecycleFixture{
		m:         m,
		root:      root,
		target:    target,
		patchID:   patchID,
		preview:   preview,
		digest:    preview.Digest(),
		authEng:   m.authEngine,
		mutBudget: mb,
		caps:      caps,
	}
}

// intoBuildPhase puts the fixture in the executable position, exactly as
// executeAutonomyWorkspace → setMode(ModeBuild) does before a run starts.
func (f *lifecycleFixture) intoBuildPhase(t *testing.T) {
	t.Helper()
	f.m.modeChangeAuthorized = true
	f.m.setMode(modes.ModeBuild)
	if got := f.m.workflowSM.State(); !got.Executable() {
		t.Fatalf("setup: workflow state = %v, want an executable position", got)
	}
	// The guard on the build edge needs plan evidence, and in production that
	// evidence is the synthetic direct-mutation micro-plan the autonomy route
	// binds before switching workspace (see authorizeAutonomousApproval).
	if f.m.orch != nil && !f.m.orch.HasAuthorizedPlan() {
		if err := f.m.orch.EnsureSyntheticMicroPlan(
			[]string{f.target}, "modification", "",
			[]string{"read", "analyze", "propose", "mutate", "verify"},
		); err != nil {
			t.Fatalf("setup: binding the synthetic plan: %v", err)
		}
		f.m.modeChangeAuthorized = true
		f.m.setMode(modes.ModeBuild)
		if got := f.m.workflowSM.State(); !got.Executable() {
			t.Fatalf("setup: workflow state = %v after plan binding, want an executable position", got)
		}
	}
}

// parkAtMutationReview is the reported sequence up to the human decision: the
// candidate is produced and the run parks at its mutation review.
func (f *lifecycleFixture) parkAtMutationReview(t *testing.T) *autonomy.HumanBoundary {
	t.Helper()
	f.m.autonomousBoundary = &autonomy.HumanBoundary{
		PatchID:               f.patchID,
		CandidateID:           f.patchID,
		CandidateDigest:       f.digest,
		CandidateOperation:    f.preview.Operation,
		Targets:               []string{f.target},
		CandidateTargets:      []string{f.target},
		CandidateDiff:         f.preview.Diff,
		CandidateAddedLines:   f.preview.AddedLines,
		CandidateRemovedLines: f.preview.RemovedLines,
		CandidateEvidence:     runtimeAutonomy.MutationReviewEvidence(f.preview),
		Action:                autonomy.HumanBoundaryApproval,
		Resumable:             true,
		Reason:                "mutation awaiting authorization",
	}
	// The RUNTIME owns the boundary. Production reads the driver's copy (that is
	// what `Driver.Boundary()` returns), so the fixture mirrors that ownership
	// rather than inventing a UI-only boundary that no runtime agrees with.
	if drv, ok := f.m.autonomousDriver.(*fakeAutonomousDriver); ok {
		drv.boundary = f.m.autonomousBoundary
		drv.state = autonomy.RuntimeAwaitingHuman
	}
	f.m.enterApprovalState()
	b := f.m.autonomousBoundary
	if b == nil {
		t.Fatal("setup: the run did not park")
	}
	return b
}

// ── INVARIANT 1 ─────────────────────────────────────────────────────────────
// A run awaiting human mutation authorization cannot be `idle`.
//
// The park is an explicit LIFECYCLE POSITION, not a boolean a UI remembers. It is
// non-terminal: the run still owes a mutation, a verification and an objective
// verdict.
func TestInvariant1_AwaitingMutationAuthorizationIsNeverIdle(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)

	st := f.m.workflowSM.State()
	if st == workflow.StateIdle {
		t.Fatal("a run parked at a mutation review reported itself idle")
	}
	if !st.Parked() {
		t.Fatalf("the parked lifecycle position is %v, want awaiting_authorization", st)
	}
	if st.IsTerminal() {
		t.Fatalf("the parked position %v reported itself terminal", st)
	}
	if got := st.String(); got != "awaiting_authorization" {
		t.Fatalf("parked lifecycle label = %q, want awaiting_authorization", got)
	}
	if f.m.workflowSM.PendingApproval() != true {
		t.Error("the parked position did not arm the pending-approval gate")
	}
	// The orchestrator projection must agree that a run is parked rather than
	// silently report a live build phase over an idle machine.
	if f.m.orch != nil && f.m.orch.ParkedAtAuthorization() != st.Parked() {
		t.Errorf("orchestrator parked(%v) disagrees with the lifecycle(%v)",
			f.m.orch.ParkedAtAuthorization(), st.Parked())
	}
}

// ── INVARIANT 2 ─────────────────────────────────────────────────────────────
// A plan step completing cannot terminate an execution run if a candidate is
// awaiting authorization.
//
// "Plan step completed" and "provider completed" are STAGE facts. The historical
// bug treated them as completion: a stage event or a provider return unwound the
// workflow to idle, and the next mutation authorization was refused. This pins
// that neither the stage ledger nor the provider's return touches the lifecycle.
func TestInvariant2_StepCompletionCannotTerminateARunAwaitingAuthorization(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)
	before := f.m.workflowSM.State()

	// A plan step completes. It is a fact about ONE stage of the objective.
	f.m.handleDomainEvent(events.NewStepCompleted(1, 1, "stop"))
	if got := f.m.workflowSM.State(); got != before {
		t.Fatalf("a plan step completing moved the lifecycle %v -> %v; a stage is not the execution", before, got)
	}
	if !f.m.workflowSM.Parked() {
		t.Fatal("a plan step completing released the parked run")
	}

	// A provider returns. Also not the execution.
	f.m.handleDomainEvent(events.NewExecutionFinished("exec-1", false, string(execution.OutcomePendingApproval)))
	if got := f.m.workflowSM.State(); got != before {
		t.Fatalf("a provider return moved the lifecycle %v -> %v; a provider is untrusted", before, got)
	}

	// And the run is still resumable with its candidate intact.
	if !f.m.executor.CandidateHeld(f.patchID) {
		t.Fatal("a stage completion released the held candidate")
	}
}

// ── INVARIANT 3 ─────────────────────────────────────────────────────────────
// Approving a candidate resumes the SAME run.
//
// This is the direct regression for the reported failure. The resume goes through
// the lifecycle's own resume edge; it is not a reset, not a re-plan and not a new
// run.
func TestInvariant3_ApprovingResumesTheSameRun(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)

	parkedState := f.m.workflowSM.State()
	before, err := os.ReadFile(filepath.Join(f.root, f.target))
	if err != nil {
		t.Fatal(err)
	}

	drv := &fakeAutonomousDriver{
		runID:     "run-2",
		state:     autonomy.RuntimeExecuting,
		boundary:  f.m.autonomousBoundary,
		parkOnRun: true,
		term:      &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "applied"},
	}
	f.m.autonomousDriver = drv

	// Resume. This is the exact production seam that used to fail.
	cmd := f.m.resumeAutonomousApprove()
	if cmd == nil {
		t.Fatalf("approving the reviewed candidate dispatched nothing:\n%s", viewportOf(f.m))
	}
	msg := extractAutonomousRunMsg(t, cmd())
	if msg.err != nil {
		t.Fatalf("approval refused: %v\n%s", msg.err, viewportOf(f.m))
	}
	if drv.resumeApprove != 1 {
		t.Fatalf("driver ResumeApprove calls = %d, want 1 (one resume, no second execution)", drv.resumeApprove)
	}
	if drv.runCount != 0 {
		t.Fatalf("approval started a SECOND execution run (Run calls = %d)", drv.runCount)
	}
	// The lifecycle left the parked position through the resume edge rather than
	// through a reset.
	if f.m.workflowSM.State() == parkedState {
		t.Errorf("the lifecycle is still %v after an authorized resume", parkedState)
	}

	// The candidate the human reviewed is the one the runtime authorized.
	auth := f.m.executor.AttachedAuthorization()
	if auth == nil {
		t.Fatal("no authorization was attached for the approved candidate")
	}
	if auth.CandidateID != f.patchID {
		t.Errorf("authorization bound to candidate %q, want the reviewed %q", auth.CandidateID, f.patchID)
	}
	if auth.CandidateDigest != f.digest {
		t.Errorf("authorization digest = %q, want the reviewed %q", auth.CandidateDigest, f.digest)
	}

	f.m.handleAutonomousRun(msg)

	// Finally: the SAME candidate, under THAT authorization, really applies.
	// The fake driver cannot mutate, so the executor is driven directly — which
	// is exactly the seam the resume authorizes, and therefore the only honest
	// place to prove the reviewed change is the change that lands.
	res, err := f.m.executor.Approve(context.Background(), f.patchID)
	if err != nil {
		t.Fatalf("applying the authorized candidate: %v", err)
	}
	if res.Proof == nil || res.Proof.Outcome != execution.OutcomeChanged {
		t.Fatalf("authorized apply outcome = %+v, want changed", res.Proof)
	}
	after, err := os.ReadFile(filepath.Join(f.root, f.target))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Fatal("the approved candidate was never applied")
	}
	if !strings.Contains(string(after), "new") {
		t.Fatalf("the applied content is not the reviewed change:\n%s", after)
	}
}

// ── INVARIANT 4 ─────────────────────────────────────────────────────────────
// Approval cannot apply a candidate different from the one reviewed.
//
// Identity alone is not enough: it answers "is this the same computation?", not
// "is this the same CHANGE?". The authorization carries the content digest the
// review was rendered from, so a candidate replaced under a stable identity
// invalidates the authorization and forces a new review.
func TestInvariant4_ApprovalCannotApplyACandidateDifferentFromTheReviewed(t *testing.T) {
	t.Run("identity mismatch", func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.intoBuildPhase(t)
		b := f.parkAtMutationReview(t)
		b.CandidateID = "a-different-candidate"

		if cmd := f.m.resumeAutonomousApprove(); cmd != nil {
			t.Fatal("an authorization for a different candidate dispatched an apply")
		}
		before, err := os.ReadFile(filepath.Join(f.root, f.target))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(before), "new") {
			t.Fatal("a mismatched authorization mutated the workspace")
		}
		text := viewportOf(f.m)
		if !strings.Contains(text, "authorization invalidated") {
			t.Errorf("the refusal does not state that the authorization was invalidated:\n%s", text)
		}
		if !strings.Contains(text, "no longer the candidate that was reviewed") {
			t.Errorf("the refusal does not name the real cause (identity divergence):\n%s", text)
		}
		if !strings.Contains(text, "new mutation review is required") {
			t.Errorf("the refusal does not state the remedy:\n%s", text)
		}
	})

	t.Run("same identity, changed content", func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.intoBuildPhase(t)
		b := f.parkAtMutationReview(t)
		// The candidate's identity is unchanged, but the review was rendered from
		// different bytes. This is the substitution a PatchID-only binding misses.
		b.CandidateDigest = "0000000000000000000000000000000000000000000000000000000000000000"

		if cmd := f.m.resumeAutonomousApprove(); cmd != nil {
			t.Fatal("an authorization for changed content dispatched an apply")
		}
		if !f.m.executor.CandidateHeld(f.patchID) {
			t.Fatal("a refused stale authorization consumed the candidate; a refusal is not an approval")
		}
		text := viewportOf(f.m)
		if !strings.Contains(text, "changed since it was reviewed") {
			t.Errorf("the refusal does not name the stale candidate:\n%s", text)
		}
		if !strings.Contains(text, "new mutation review") {
			t.Errorf("the refusal does not state the remedy:\n%s", text)
		}
		before, err := os.ReadFile(filepath.Join(f.root, f.target))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(before), "new") {
			t.Fatal("a stale authorization mutated the workspace")
		}
	})
}

// ── INVARIANT 5 ─────────────────────────────────────────────────────────────
// `/new` resets conversation state without silently destroying a parked execution.
//
// Conversation and execution are separate entities. `/new` owns the conversation;
// it does not own the run — and it must SAY SO rather than leaving the operator to
// discover a live run through a late refusal.
func TestInvariant5_NewResetsConversationWithoutDestroyingAParkedExecution(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	prevSess := f.m.sess

	// A REAL session manager on disk: `/new` is a durable conversation boundary,
	// so the test uses the durable path rather than a stub.
	sm := session.NewManager(t.TempDir(),
		session.WithLockConfig(session.LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}))
	if err := sm.Open(context.Background()); err != nil {
		t.Fatalf("open session manager: %v", err)
	}
	t.Cleanup(func() { _ = sm.Close() })
	sm.Session().Objective = "parked execution"
	f.m.sessionManager = sm
	f.m.sess = sm.Session()
	// The park must survive the transient reset that /new performs, so the test
	// arms it AFTER the harness state it depends on is established.
	f.parkAtMutationReview(t)
	parkedState := f.m.workflowSM.State()

	// Real spend before the boundary, so the conversation-scope reset is observable.
	f.m.commitTokenUsage(4321, 876)

	f.m.runNewSessionCmd()

	// The CONVERSATION is new.
	if f.m.sess == prevSess {
		t.Error("/new did not create a new conversation session")
	}
	if f.m.InputTokens != 0 || f.m.OutputTokens != 0 {
		t.Errorf("conversation token metrics survived /new: in=%d out=%d", f.m.InputTokens, f.m.OutputTokens)
	}
	// The EXECUTION is untouched.
	if got := f.m.workflowSM.State(); got != parkedState {
		t.Errorf("/new moved the execution lifecycle %v -> %v; a conversation boundary is not an execution transition", parkedState, got)
	}
	if !f.m.workflowSM.Parked() {
		t.Error("/new silently terminated a parked execution")
	}
	if !f.m.executor.CandidateHeld(f.patchID) {
		t.Error("/new silently discarded the held mutation candidate")
	}
	if !f.m.workflowSM.PendingApproval() {
		t.Error("/new released the parked run's human boundary")
	}
	// And the operator is told, from authoritative state.
	if !strings.Contains(viewportOf(f.m), "remains parked") {
		t.Errorf("/new started a new conversation without reporting the parked execution:\n%s", viewportOf(f.m))
	}
}

// ── INVARIANT 6 ─────────────────────────────────────────────────────────────
// A new execution cannot start while another run is active or parked unless the
// existing run is explicitly resumed or aborted.
//
// The check is at ADMISSION: before intent parsing, before the autonomy decision
// is rendered, before the workflow phase moves to build. A late refusal — after a
// full decision trace and a BUILDING header — is the reported defect.
func TestInvariant6_NewExecutionIsRefusedAtAdmissionWhileARunIsParked(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)
	f.m.autonomousDriver = &fakeAutonomousDriver{
		runID:     "run-2",
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary:  f.m.autonomousBoundary,
	}

	parkedState := f.m.workflowSM.State()
	phaseBefore := f.m.orch.Current()

	if cmd := f.m.routePromptDirective("Create one file: index.html"); cmd != nil {
		t.Fatal("a refused admission dispatched work")
	}
	text := viewportOf(f.m)

	// Nothing was started and nothing moved.
	if got := f.m.workflowSM.State(); got != parkedState {
		t.Errorf("a refused admission moved the lifecycle %v -> %v", parkedState, got)
	}
	if got := f.m.orch.Current(); got != phaseBefore {
		t.Errorf("a refused admission moved the phase %v -> %v; admission must run BEFORE the mode switch", phaseBefore, got)
	}
	// The refusal is truthful and actionable, not a raw error.
	for _, want := range []string{"EXECUTION BLOCKED", "run-2", "awaiting mutation authorization", "No new execution started"} {
		if !strings.Contains(text, want) {
			t.Errorf("the admission refusal does not state %q:\n%s", want, text)
		}
	}
	// And no autonomy trace implied work began.
	if strings.Contains(text, "AUTONOMY DECISION") {
		t.Errorf("a refused admission still rendered an autonomy decision trace:\n%s", text)
	}
	// Zero provider calls: nothing was dispatched.
	if n := f.m.executor.CandidateHeld(f.patchID); !n {
		t.Error("the parked candidate was disturbed by a refused admission")
	}
}

// ── INVARIANT 7 ─────────────────────────────────────────────────────────────
// Process restart preserves a parked execution's semantic state.
//
// A restart must not resolve a live run into idle/completed/failed unless evidence
// proved that transition before shutdown. The state machine is reconstructed from
// evidence, so a run parked at the human boundary restores to exactly that.
func TestInvariant7_RestartRestoresAParkedExecutionToItsExactSemanticState(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)
	wantState := f.m.workflowSM.State()

	// ── RESTART: a fresh process builds fresh authorities. The candidate record is
	// reconstructed from the durable execution evidence, exactly as recovery does.
	sm := workflow.NewWorkflowStateMachine()
	restored := readyChatModel(newTestModel())
	restored.workflowSM = sm
	restored.orch = runtimeorch.New(sm, nil)
	restored.mutationBudget = f.mutBudget
	restored.caps = f.caps
	restored.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{}, fakeCheckpointChecker{},
		func() workflow.WorkflowState { return sm.State() },
	)

	// The evidence a restart reads: this run parked at a human boundary.
	restored.recoverParkedExecution(parkedRunEvidence{
		RunID:       "run-2",
		Targets:     []string{f.target},
		CandidateID: f.patchID,
		Digest:      f.digest,
		Boundary:    autonomy.HumanBoundaryApproval,
		Resumable:   true,
	})

	if got := sm.State(); got != wantState {
		t.Fatalf("restart restored %v, want the parked position %v", got, wantState)
	}
	if !restored.workflowParked() {
		t.Fatal("the restored run does not report itself parked")
	}
	if sm.State().IsTerminal() {
		t.Fatal("the restored run reports itself terminal")
	}
	// The boundary is restored, so the human can still answer the same question.
	if !restored.workflowSM.PendingApproval() {
		t.Error("the restored run lost its human boundary")
	}
	// And the restored state is truthfully reported.
	if label := restored.workflowLifecycleLabel(); label == workflow.StateIdle.String() {
		t.Errorf("a restored parked run reported lifecycle %q", label)
	}
}

// ── INVARIANT 8 ─────────────────────────────────────────────────────────────
// A candidate does not imply a mutation.
func TestInvariant8_ACandidateDoesNotImplyAMutation(t *testing.T) {
	f := newLifecycleFixture(t)
	f.intoBuildPhase(t)
	f.parkAtMutationReview(t)

	// A validated candidate is exactly that: an artifact the runtime HOLDS.
	before, err := os.ReadFile(filepath.Join(f.root, f.target))
	if err != nil {
		t.Fatal(err)
	}
	if !f.m.executor.CandidateHeld(f.patchID) {
		t.Fatal("the candidate is not held; the premise of this invariant does not hold")
	}
	if strings.Contains(string(before), "new") {
		t.Fatal("producing a candidate mutated the workspace")
	}
	// Artifact produced, mutation NOT applied: the review says so in its own words.
	block := f.m.renderAutonomousBoundaryBlock(120)
	if !strings.Contains(block, "MUTATION REVIEW") {
		t.Errorf("the parked boundary is not a mutation review:\n%s", block)
	}
	if !strings.Contains(block, "Mutation has NOT occurred") {
		t.Errorf("the review does not state that nothing was applied:\n%s", block)
	}
	// The evidence checklist marks the mutation row UNSATISFIED — never as a
	// green check for work that has not happened.
	sawUnsatisfiedMutation := false
	for _, e := range f.m.autonomousBoundary.CandidateEvidence {
		if e.Label == "mutation applied" && !e.Satisfied {
			sawUnsatisfiedMutation = true
		}
		if e.Label == "mutation applied" && e.Satisfied {
			t.Fatal("the evidence checklist claims the mutation was applied")
		}
	}
	if !sawUnsatisfiedMutation {
		t.Error("the mutation review carries no explicit unsatisfied 'mutation applied' row")
	}
}

// ── INVARIANT 12 ────────────────────────────────────────────────────────────
// A missing target never becomes an inferred target.
//
// The runtime resolves targets from evidence (resolved scope → target exists /
// does not exist → typed target result). No prompt keyword, filename or artifact
// type may invent one.
func TestInvariant12_AMissingTargetNeverBecomesAnInferredTarget(t *testing.T) {
	root := t.TempDir()
	provider := &mockProvider{responses: []*ai.Response{{
		Content: "<html><body><h1>new</h1></body></html>\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, bus, "")
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(), ExpiresAt: time.Now().Add(time.Hour),
	})

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		Mode: "build", Prompt: "update @does-not-exist.html", Target: "does-not-exist.html",
	})
	if err == nil && res != nil && res.Proof != nil {
		if res.Proof.Outcome == execution.OutcomePendingApproval && len(res.Targets) > 0 {
			for _, target := range res.Targets {
				if target != "does-not-exist.html" {
					t.Errorf("the runtime inferred target %q for a missing file", target)
				}
			}
		}
	}
	// The workspace must be untouched: a missing target is not permission to
	// create something the runtime guessed.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == "does-not-exist.html" {
			t.Fatal("the runtime created the missing target it inferred")
		}
	}
}

// ── INVARIANT 13 ────────────────────────────────────────────────────────────
// Known provider usage must never render as zero.
func TestInvariant13_KnownProviderUsageNeverRendersAsZero(t *testing.T) {
	m := readyChatModel(newTestModel())
	// A provider that reports real usage.
	cmd := m.tokenUsageCmdKnown(1234, 567, true)
	if cmd == nil {
		t.Fatal("known usage dispatched no update")
	}
	msg, ok := cmd().(UsageUpdateMsg)
	if !ok {
		t.Fatalf("usage update = %T, want UsageUpdateMsg", cmd())
	}
	if msg.PromptTokens != 1234 || msg.CompletionTokens != 567 || !msg.Known {
		t.Fatalf("usage update = %+v, want 1234/567 known", msg)
	}

	// Unavailable usage must be UNAVAILABLE, never a fabricated zero.
	if m.tokenUsageCmdKnown(0, 0, false) != nil {
		t.Error("unknown usage was dispatched as if it were a real count")
	}

	// And a REAL zero is preserved, because it is a fact.
	realZero := m.tokenUsageCmdKnown(0, 0, true)
	if realZero == nil {
		t.Fatal("a provider-reported zero was suppressed; zero is a real value")
	}
	z, ok := realZero().(UsageUpdateMsg)
	if !ok || !z.Known {
		t.Errorf("a real zero was reported as unknown: %+v", z)
	}
}
