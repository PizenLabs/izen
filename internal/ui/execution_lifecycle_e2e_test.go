package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	runtimeorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
	"github.com/PizenLabs/izen/internal/session"
)

// e2eHarness is the production-shaped model plus the scripted provider behind it.
//
// The provider reference lives HERE rather than on the production model: counting
// invocations is a test observation, and the count must be taken at the provider
// itself — the only seam a path which builds a request and then discards it
// cannot fool.
type e2eHarness struct {
	*model
	provider *countingProvider
}

// providerCalls reports how many invocations the scripted provider has served.
func (h *e2eHarness) providerCalls() int {
	if h == nil || h.provider == nil {
		return 0
	}
	return h.provider.calls
}

// newE2ESessionManager opens a real, durable session manager for the /new tests.
func newE2ESessionManager(t *testing.T) *session.Manager {
	t.Helper()
	sm := session.NewManager(t.TempDir(),
		session.WithLockConfig(session.LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}))
	if err := sm.Open(context.Background()); err != nil {
		t.Fatalf("open session manager: %v", err)
	}
	t.Cleanup(func() { _ = sm.Close() })
	return sm
}

// holdFullFileCandidate drives the real executor over a scripted provider that
// emits the complete file content, and returns the held candidate's identity and
// the provider behind it. It is the minimal-create shape: the target does not
// exist, so the operation is a genuine CREATE rather than an assumed one.
func holdFullFileCandidate(t *testing.T, m *model, root, target, content string) (string, *countingProvider) {
	t.Helper()
	return holdFullFileCandidateAs(t, m, root, target, content, "")
}

// holdFullFileCandidateAs is holdFullFileCandidate with an explicit request
// identity. A caller uses it when it must control whether a successor candidate
// REUSES a prior identity (the stale-authorization case) or gets a fresh one.
func holdFullFileCandidateAs(t *testing.T, m *model, root, target, content, requestID string) (string, *countingProvider) {
	t.Helper()
	if requestID == "" {
		requestID = fmt.Sprintf("e2e-%s-%d", target, e2eRequestSeq.Add(1))
	}
	// The provider emits the artifact in the executor's OWN protocol — a fenced
	// block — because that is the contract the ingestion path parses. A bare body
	// would be (correctly) refused as prose.
	answer := func() *ai.Response {
		return &ai.Response{
			Content: "```html\n" + content + "```",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 1234, CompletionTokens: 567, FinishReason: "stop"},
		}
	}
	provider := &countingProvider{responses: []*ai.Response{answer(), answer()}}
	m.executor = execution.NewRuntimeExecutor(root, m.cfg, provider, nil, "")
	// Verification is bound to a deterministic always-passing step so the apply
	// assertions exercise the MUTATION boundary rather than a compile gate that
	// would invoke the real toolchain.
	v := execution.NewVerifier(root)
	v.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(v)
	res, err := m.executor.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: requestID, Mode: "build",
		Prompt: "Create one file: " + target, Target: target,
	})
	if err != nil {
		t.Fatalf("producing the candidate: %v", err)
	}
	if res == nil || res.PendingPatchID == "" {
		t.Fatalf("no candidate was held; outcome=%v", res)
	}
	if !m.executor.CandidateHeld(res.PendingPatchID) {
		t.Fatalf("candidate %s is not held", res.PendingPatchID)
	}
	return res.PendingPatchID, provider
}

// e2eRequestSeq names each dispatch distinctly, so a superseding computation
// produces a genuinely different candidate identity. Reusing one request id would
// make C2 carry C1's identity — which is exactly the substitution the stale-
// authorization test needs to be able to distinguish.
var e2eRequestSeq atomic.Uint64

// countingProvider is mockProvider with an observable invocation count, so a test
// can assert that a refused or resumed path issued NO provider call.
type countingProvider struct {
	responses []*ai.Response
	calls     int
}

func (p *countingProvider) Name() string { return "counting" }

func (p *countingProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	if p.calls >= len(p.responses) {
		return &ai.Response{Content: p.responses[len(p.responses)-1].Content,
			Usage: ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"}}, nil
	}
	resp := p.responses[p.calls]
	p.calls++
	return resp, nil
}

// ExecuteStream mirrors the non-streaming answer through the streaming seam, so
// the executor observes the SAME artifact and the SAME usage on either path. A
// stream that returned empty bytes would make the test fail on ingestion rather
// than on the lifecycle it exists to cover.
func (p *countingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	if p.calls >= len(p.responses) {
		return nil, io.ErrClosedPipe
	}
	resp := p.responses[p.calls]
	p.calls++
	return &mockStream{
		content: []byte(resp.Content),
		usage:   resp.Usage,
	}, nil
}

// ── THE REPORTED END-TO-END SCENARIOS ────────────────────────────────────────
//
// Each test below is one of the report's Test A–G, driven against the production
// shape: real state machine, real orchestrator, real authorization engine, real
// executor holding a real candidate. Only the provider is scripted.
//
// The scenario names are the report's. Keeping them makes the traceability from
// "this was reported broken" to "this is asserted" mechanical.

// e2eWorkspace is the minimal-create workspace: index.html does NOT exist yet, so
// the mutation's operation is a CREATE and its pre-state is genuinely observed
// rather than assumed.
func e2eWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

const (
	e2eTarget  = "index.html"
	e2eContent = "<!DOCTYPE html>\n<html><body><h1>Hello</h1></body></html>\n"
)

// newE2EModel builds the production-shaped model for a minimal-create run.
func newE2EModel(t *testing.T, root string) (*e2eHarness, string) {
	t.Helper()
	m := readyChatModel(newTestModel())
	sm := workflow.NewWorkflowStateMachine()
	m.workflowSM = sm
	m.workspaceRoot = root
	m.orch = runtimeorch.New(sm, nil)

	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	m.mutationBudget = budget.NewBudget(10, 1000, 100000, 3, 60*1e9, 10)
	micro := budget.DefaultMicroBudget()
	m.microBudget = &micro
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{}, fakeCheckpointChecker{},
		func() workflow.WorkflowState { return sm.State() },
	)

	// The provider emits the exact artifact the executor's protocol expects.
	patchID, provider := holdFullFileCandidate(t, m, root, e2eTarget, e2eContent)
	m.autonomousDriver = &fakeAutonomousDriver{runID: "run-1", state: autonomy.RuntimeAwaitingHuman, parkOnRun: true}
	return &e2eHarness{model: m, provider: provider}, patchID
}

// e2ePark parks the run at its mutation review using the runtime's own facts.
func e2ePark(t *testing.T, m *model, patchID string) *autonomy.HumanBoundary {
	t.Helper()
	preview, ok := m.executor.CandidatePreview(patchID)
	if !ok {
		t.Fatal("the created candidate is not held by the executor")
	}
	// A CREATE contract: the target did not exist before this mutation.
	if preview.Operation != "CREATE" {
		t.Errorf("operation = %q, want CREATE for a target that did not exist", preview.Operation)
	}
	if !strings.Contains(preview.OperationEvidence, "did not exist") {
		t.Errorf("operation evidence does not rest on the observed pre-state: %q", preview.OperationEvidence)
	}
	b := &autonomy.HumanBoundary{
		PatchID:                    patchID,
		CandidateID:                patchID,
		CandidateDigest:            preview.Digest(),
		CandidateOperation:         preview.Operation,
		CandidateOperationEvidence: preview.OperationEvidence,
		Targets:                    []string{e2eTarget},
		CandidateTargets:           []string{e2eTarget},
		CandidateDiff:              preview.Diff,
		CandidateAddedLines:        preview.AddedLines,
		CandidateRemovedLines:      preview.RemovedLines,
		Action:                     autonomy.HumanBoundaryApproval,
		Resumable:                  true,
		Reason:                     "mutation awaiting authorization",
	}
	if drv, ok := m.autonomousDriver.(*fakeAutonomousDriver); ok {
		drv.boundary = b
		drv.state = autonomy.RuntimeAwaitingHuman
	}
	m.autonomousBoundary = b
	m.enterApprovalState()
	return b
}

// ── TEST A — MINIMAL CREATE, END TO END ─────────────────────────────────────
//
//	execution authorization → execution → artifact candidate → mutation review
//	→ human approval → mutation applied → verification → objective proven
//
// The reported failure was a workflow-state refusal at the approval step. This
// asserts the whole sequence with no refusal anywhere.
func TestE2E_A_MinimalCreateAppliesTheReviewedCandidate(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model

	// 1. EXECUTION AUTHORIZATION: enter the build phase under a declared plan.
	m.modeChangeAuthorized = true
	m.setMode(modes.ModeBuild)
	if err := m.orch.EnsureSyntheticMicroPlan([]string{e2eTarget}, "modification", "",
		[]string{"read", "analyze", "propose", "mutate", "verify"}); err != nil {
		t.Fatalf("bind plan: %v", err)
	}
	m.modeChangeAuthorized = true
	m.setMode(modes.ModeBuild)
	if st := m.workflowSM.State(); !st.Executable() {
		t.Fatalf("execution authorization did not reach an executable position: %v", st)
	}

	// 2. MUTATION REVIEW: the run parks holding the candidate.
	b := e2ePark(t, m, patchID)
	if st := m.workflowSM.State(); !st.Parked() {
		t.Fatalf("the run parked at %v, want awaiting_authorization", st)
	}

	// The review shows the CONCRETE change, not just a target name.
	review := m.renderAutonomousBoundaryBlock(140)
	for _, want := range []string{"MUTATION REVIEW", e2eTarget, "CREATE", "Proposed change", "Mutation has NOT occurred"} {
		if !strings.Contains(review, want) {
			t.Errorf("the mutation review does not show %q:\n%s", want, review)
		}
	}

	// 3. HUMAN APPROVAL resumes the same run — no workflow-state refusal.
	if drv, ok := m.autonomousDriver.(*fakeAutonomousDriver); ok {
		drv.term = &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "applied"}
	}
	cmd := m.resumeAutonomousApprove()
	if cmd == nil {
		t.Fatalf("approval refused with a workflow-state error:\n%s", viewportOf(m))
	}

	// 4. MUTATION APPLIED, then VERIFIED, through the executor under that
	//    authorization. This is the only seam that writes, so it is the only
	//    honest place to assert the mutation landed.
	res, err := m.executor.Approve(context.Background(), patchID)
	if err != nil {
		t.Fatalf("applying the approved mutation: %v", err)
	}
	if res.Proof == nil {
		t.Fatal("the applied mutation carried no proof")
	}
	// The mutation landed on a file that did not exist before. The outcome
	// vocabulary reports that as `changed` (the bytes differ from the prior absent
	// state); the CREATE classification lives in the review, derived from the
	// observed pre-state, and is asserted above.
	if res.Proof.Outcome != execution.OutcomeChanged && res.Proof.Outcome != execution.OutcomeCreated {
		t.Fatalf("mutation outcome = %s, want the applied change for a new file", res.Proof.Outcome)
	}
	// Verification is its own stage with its own evidence.
	if !res.Verification.Passed {
		t.Errorf("verification did not pass after a clean create: %+v", res.Verification)
	}

	// 5. The file exists with exactly the reviewed content.
	onDisk, err := os.ReadFile(filepath.Join(root, e2eTarget))
	if err != nil {
		t.Fatalf("the authorized mutation did not create the target: %v", err)
	}
	if string(onDisk) != e2eContent {
		t.Errorf("created content = %q, want %q", string(onDisk), e2eContent)
	}

	// 6. NO WORKFLOW-STATE REFUSAL appeared anywhere in the transcript.
	text := viewportOf(m)
	for _, forbidden := range []string{"expected Building or Repairing", "got idle"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the reported workflow-state refusal reappeared:\n%s", text)
		}
	}
	// And the authorization was bound to the candidate that was reviewed.
	auth := m.executor.AttachedAuthorization()
	if auth == nil || auth.CandidateID != b.CandidateID || auth.CandidateDigest != b.CandidateDigest {
		t.Errorf("the applied mutation was not the reviewed candidate: %+v", auth)
	}
}

// ── TEST B — REJECT AT THE MUTATION REVIEW ──────────────────────────────────
// The candidate stays unapplied, the run terminates as a human rejection, and the
// workspace is byte-identical.
func TestE2E_B_RejectAtMutationReviewLeavesNoTrace(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	e2ePark(t, m, patchID)

	before, err := os.ReadFile(filepath.Join(root, e2eTarget))
	if err == nil {
		t.Fatalf("precondition: the target must not exist before a rejected create: %q", string(before))
	}

	drv := m.autonomousDriver.(*fakeAutonomousDriver)
	drv.term = &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "rejected by operator"}
	// The real driver resolves a rejection by RELEASING the held candidate through
	// the executor. The fake mirrors that so "the candidate was released" is a
	// fact about the seam rather than an unverified assumption.
	drv.rejectPatchID = patchID
	drv.rejectCandidate = func(ctx context.Context, id, reason string) {
		if _, err := m.executor.Reject(ctx, id, reason); err != nil {
			t.Errorf("releasing the rejected candidate: %v", err)
		}
	}

	// The rejection crosses the DRIVER bridge, which is the owner of the human
	// decision — the UI never rejects a candidate on its own.
	cmd := m.resumeAutonomousReject("rejected by operator")
	if cmd == nil {
		t.Fatalf("rejecting the reviewed candidate dispatched nothing:\n%s", viewportOf(m))
	}
	// The terminal rejection is projected through the normal outcome path: the
	// run is over, so the lifecycle must leave its parked position there — not stay
	// parked waiting for a decision that was already answered.
	m.handleAutonomousRun(extractAutonomousRunMsg(t, cmd()))
	if drv.resumeReject != 1 {
		t.Fatalf("driver ResumeReject calls = %d, want 1", drv.resumeReject)
	}

	// The candidate is RELEASED, so it can never be applied later.
	if m.executor.CandidateHeld(patchID) {
		t.Error("a rejected candidate is still held and approvable")
	}
	// NO FILE MODIFICATION.
	if _, err := os.Stat(filepath.Join(root, e2eTarget)); err == nil {
		t.Fatal("a rejected mutation created the file")
	}
	// The run terminated, so it is no longer parked.
	if m.autonomousParked() {
		t.Error("a rejected run is still parked")
	}
	if m.workflowSM.Parked() {
		t.Error("a rejected run is still in the parked lifecycle position")
	}
}

// ── TEST C — RESUME A PARKED RUN ────────────────────────────────────────────
//
//	Park at AWAITING_MUTATION_AUTHORIZATION → resume
//	→ same RunID, same CandidateID, same candidate, same target
//	→ no regeneration
//
// "No regeneration" is the substantive part: a resume must not spend a provider
// call, because the candidate a human approved is the one being applied.
func TestE2E_C_ResumingAParkedRunChangesNoIdentityAndSpendsNothing(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	e2ePark(t, m, patchID)

	drv := m.autonomousDriver.(*fakeAutonomousDriver)
	drv.term = &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "applied"}

	// Identity BEFORE the resume.
	beforeRunID := drv.RunID()
	beforeCandidate := m.executor.PendingPatchIDs()
	beforeProviderCalls := h.providerCalls()

	if cmd := m.resumeAutonomousApprove(); cmd == nil {
		t.Fatalf("resume dispatched nothing:\n%s", viewportOf(m))
	}

	// Identity AFTER: unchanged.
	if drv.RunID() != beforeRunID {
		t.Errorf("run identity changed across the resume: %q → %q", beforeRunID, drv.RunID())
	}
	if got := m.executor.PendingPatchIDs(); len(got) != len(beforeCandidate) {
		t.Errorf("the candidate set changed across the resume: %v → %v", beforeCandidate, got)
	}
	if h.providerCalls() != beforeProviderCalls {
		t.Errorf("the resume issued %d new provider call(s); a resume must apply the reviewed candidate, not regenerate",
			h.providerCalls()-beforeProviderCalls)
	}
	if drv.runCount != 0 {
		t.Errorf("the resume started a NEW execution run (%d Run calls)", drv.runCount)
	}

	// And it really applies the same candidate.
	if _, err := m.executor.Approve(context.Background(), patchID); err != nil {
		t.Fatalf("the resumed run could not apply its own candidate: %v", err)
	}
}

// ── TEST D — /new WITH A PARKED RUN ─────────────────────────────────────────
//
//	/new → new conversation session
//	     → the old execution is still explicitly visible as parked
//	     → a subsequent build prompt is refused AT ADMISSION
//	     → no provider call
func TestE2E_D_NewSessionKeepsTheParkedRunVisibleAndBlocksNewBuilds(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	e2ePark(t, m, patchID)

	drv := m.autonomousDriver.(*fakeAutonomousDriver)
	parkedRunID := drv.RunID()

	sm := newE2ESessionManager(t)
	m.sessionManager = sm
	m.sess = sm.Session()
	// Re-park: the session switch resets transient interaction state, so the park
	// is re-established through the runtime exactly as production does.
	e2ePark(t, m, patchID)

	providerCallsBefore := h.providerCalls()

	m.runNewSessionCmd()

	// 1. The old execution is EXPLICITLY visible as parked.
	text := viewportOf(m)
	for _, want := range []string{"remains parked", parkedRunID, "awaiting mutation authorization", e2eTarget} {
		if !strings.Contains(text, want) {
			t.Errorf("/new did not report %q:\n%s", want, text)
		}
	}
	if !m.executor.CandidateHeld(patchID) {
		t.Fatal("/new silently discarded the held candidate")
	}

	// 2. A subsequent build prompt is refused AT ADMISSION — before any work.
	if cmd := m.routePromptDirective("Create one file: index.html"); cmd != nil {
		t.Fatal("a blocked execution dispatched work")
	}
	if got := h.providerCalls(); got != providerCallsBefore {
		t.Errorf("the blocked execution issued %d provider call(s); admission must run before dispatch",
			got-providerCallsBefore)
	}
	text = viewportOf(m)
	if !strings.Contains(text, "EXECUTION BLOCKED") {
		t.Errorf("the second build was not refused as an execution conflict:\n%s", text)
	}
	if !strings.Contains(text, "No new execution started") {
		t.Errorf("the refusal does not state that nothing started:\n%s", text)
	}
}

// ── TEST E — PROCESS RESTART ─────────────────────────────────────────────────
//
// Persist AWAITING_MUTATION_AUTHORIZATION, restart, and assert the run and its
// boundary come back with their EXACT semantics — never as idle/completed/failed.
func TestE2E_ProcessRestartRestoresTheParkedRunExactly(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	e2ePark(t, m, patchID)

	// ── PERSIST: the durable record is written at the park.
	durable, ok := m.currentParkedRun()
	if !ok {
		t.Fatal("the parked run produced no durable record to persist")
	}
	record, err := json.Marshal(durable)
	if err != nil {
		t.Fatalf("persisting the parked run: %v", err)
	}
	store := filepath.Join(t.TempDir(), "parked-run.json")
	if err := os.WriteFile(store, record, 0o600); err != nil {
		t.Fatal(err)
	}

	// ── RESTART: a brand-new process with brand-new authorities.
	sm := workflow.NewWorkflowStateMachine()
	restored := readyChatModel(newTestModel())
	restored.workflowSM = sm
	restored.workspaceRoot = root
	restored.orch = runtimeorch.New(sm, nil)
	restored.caps = m.caps
	restored.mutationBudget = m.mutationBudget
	restored.microBudget = m.microBudget
	restored.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{}, fakeCheckpointChecker{},
		func() workflow.WorkflowState { return sm.State() },
	)
	restored.autonomousDriver = &fakeAutonomousDriver{runID: "run-1", state: autonomy.RuntimeAwaitingHuman}

	// ── RECOVER: read the durable record back.
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	var recovered ParkedRun
	if err := json.Unmarshal(raw, &recovered); err != nil {
		t.Fatalf("the persisted parked run did not round-trip: %v", err)
	}
	restored.recoverParkedExecution(recovered.ToParkedRunEvidence())

	// Same run, same candidate, same target, same boundary.
	if got := sm.State(); !got.Parked() {
		t.Fatalf("restart restored %v, want the parked position", got)
	}
	if sm.State() == workflow.StateIdle {
		t.Fatal("restart resolved a parked run into idle — the reported defect")
	}
	if sm.State().IsTerminal() {
		t.Fatal("restart resolved a parked run into a terminal state")
	}
	if !restored.workflowSM.PendingApproval() {
		t.Error("restart lost the human boundary")
	}
	b := restored.autonomousBoundary
	if b == nil {
		t.Fatal("restart produced no boundary to answer")
	}
	if b.CandidateID != patchID {
		t.Errorf("restart restored candidate %q, want %q", b.CandidateID, patchID)
	}
	if b.CandidateDigest != digestOfCurrentBoundary(m) {
		t.Error("restart lost the candidate digest the review was rendered from")
	}
	if len(b.Targets) != 1 || b.Targets[0] != e2eTarget {
		t.Errorf("restart restored targets %v, want [%s]", b.Targets, e2eTarget)
	}
	if b.Action != autonomy.HumanBoundaryApproval {
		t.Errorf("restart restored boundary %q, want an approval boundary", b.Action)
	}

	// And the restored run is truthfully REPORTED as parked, not as idle.
	if label := restored.workflowLifecycleLabel(); label == workflow.StateIdle.String() {
		t.Errorf("the restored run reports lifecycle %q", label)
	}
	// The restored run is reported from the RUNTIME's boundary — the authority the
	// whole model reads — not from a flag the recovery path happened to set.
	restoredDrv, ok := restored.autonomousDriver.(*fakeAutonomousDriver)
	if !ok {
		t.Fatal("the restored model has no driver")
	}
	restoredDrv.boundary = restored.autonomousBoundary
	truth := restored.Truth()
	if !truth.Parked {
		t.Error("the restored run does not report itself parked")
	}
	if truth.CandidateID != patchID {
		t.Errorf("execution truth names candidate %q, want %q", truth.CandidateID, patchID)
	}
	if truth.PendingBoundary != "awaiting mutation authorization" {
		t.Errorf("execution truth names boundary %q, want the mutation-authorization wait", truth.PendingBoundary)
	}
	if truth.MutationApplied {
		t.Error("execution truth claims a mutation was applied for a run that applied none")
	}
}

// ── TEST F — STALE AUTHORIZATION ─────────────────────────────────────────────
//
// Two distinct substitutions, two distinct refusals. Conflating them is what made
// the previous message ("candidate changed") arrive for a candidate that had in
// fact been withdrawn entirely.
//
//	F-identity  C1 was invalidated   → the candidate is gone
//	F-content   C1's identity lives on but its bytes were replaced → stale digest
//
// Neither may mutate anything, and both must demand a new mutation review.
func TestE2E_StaleAuthorizationCannotApplyAChangedCandidate(t *testing.T) {
	t.Run("identity withdrawn", func(t *testing.T) {
		root := e2eWorkspace(t)
		h, patchID := newE2EModel(t, root)
		m := h.model

		b := e2ePark(t, m, patchID)
		if b.CandidateDigest == "" {
			t.Fatal("the review carried no candidate digest to bind the authorization to")
		}

		// C1 is withdrawn by a newer computation and C2 supersedes it.
		if _, err := m.executor.Reject(context.Background(), patchID, "superseded by a newer computation"); err != nil {
			t.Fatalf("withdrawing C1: %v", err)
		}
		c2, _ := holdFullFileCandidate(t, m, root, e2eTarget,
			"<!DOCTYPE html>\n<html><body><h1>Different</h1></body></html>\n")
		if c2 == patchID {
			t.Fatal("precondition: the superseding candidate must be a distinct identity")
		}
		if !m.executor.CandidateHeld(c2) {
			t.Fatal("the superseding candidate is not held")
		}

		m.autonomousBoundary = b
		m.autonomousDriver.(*fakeAutonomousDriver).boundary = b
		if cmd := m.resumeAutonomousApprove(); cmd != nil {
			t.Fatal("an authorization for a withdrawn candidate dispatched an apply")
		}

		text := viewportOf(m)
		if !strings.Contains(text, "no longer held") {
			t.Errorf("the refusal does not name the withdrawn candidate:\n%s", text)
		}
		assertNoE2EMutation(t, root)
		// C2 is untouched and still requires its own review.
		if !m.executor.CandidateHeld(c2) {
			t.Error("the superseding candidate was consumed by a refused authorization")
		}
	})

	t.Run("identity retained, content replaced", func(t *testing.T) {
		root := e2eWorkspace(t)
		h, patchID := newE2EModel(t, root)
		m := h.model

		// The review is rendered from C1.
		b := e2ePark(t, m, patchID)
		reviewed := b.CandidateDigest

		// The SAME candidate identity comes to hold DIFFERENT bytes — the
		// substitution a PatchID-only binding cannot see.
		if _, err := m.executor.Reject(context.Background(), patchID, "recomputed under the same identity"); err != nil {
			t.Fatalf("withdrawing C1: %v", err)
		}
		replacement := "<!DOCTYPE html>\n<html><body><h1>Different</h1></body></html>\n"
		reused, _ := holdFullFileCandidateAs(t, m, root, e2eTarget, replacement,
			strings.TrimSuffix(patchID, "-patch-1"))
		if !m.executor.CandidateHeld(reused) {
			t.Fatal("the recomputed candidate is not held")
		}
		preview, ok := m.executor.CandidatePreview(reused)
		if !ok {
			t.Fatal("the recomputed candidate is not previewable")
		}
		if preview.Digest() == reviewed {
			t.Fatal("precondition: the replacement must differ from what was reviewed")
		}

		// The human answers the review of C1 — but the executor now holds different
		// content under that identity.
		m.autonomousBoundary = b
		m.autonomousDriver.(*fakeAutonomousDriver).boundary = b
		if cmd := m.resumeAutonomousApprove(); cmd != nil {
			t.Fatal("a stale authorization dispatched an apply")
		}
		text := viewportOf(m)
		if !strings.Contains(text, "changed since it was reviewed") {
			t.Errorf("the refusal does not name the changed candidate:\n%s", text)
		}
		if !strings.Contains(text, "new mutation review is required") {
			t.Errorf("the refusal does not state the remedy:\n%s", text)
		}
		assertNoE2EMutation(t, root)
	})
}

// assertNoE2EMutation fails if any target was created or changed — the single
// assertion every refusal path must satisfy.
func assertNoE2EMutation(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, e2eTarget)); err == nil {
		t.Fatal("a refused authorization mutated the workspace")
	}
}

// ── TEST G — PROVIDER TOKEN TELEMETRY ───────────────────────────────────────
//
// Known usage must render as its real value; absent usage must render as
// UNAVAILABLE, never as a fabricated zero.
//
// The reported symptom was real OpenRouter calls consuming thousands of tokens
// while the footer read "↑0 · ↓0". The failure is not in the renderer — it is that
// a real usage account never reached the model's counters. This asserts the whole
// chain: provider → executor aggregate → terminal result → usage message → session
// counters → footer.
func TestE2E_ProviderUsageIsReportedTruthfully(t *testing.T) {
	root := e2eWorkspace(t)
	h, _ := newE2EModel(t, root)
	m := h.model

	// 1. A provider that reports real usage.
	probe, err := m.executor.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "telemetry-known", Mode: "build",
		Prompt: "Create one file: telemetry.txt", Target: "telemetry.txt",
	})
	if err != nil {
		t.Fatalf("usage-probe execution: %v", err)
	}
	if !probe.Completed.Known {
		t.Fatal("the executor did not report known provider usage for a provider that reported it")
	}
	if probe.Completed.InputTokens != 1234 || probe.Completed.OutputTokens != 567 {
		t.Fatalf("the executor aggregated usage as %d/%d, want the provider's 1234/567",
			probe.Completed.InputTokens, probe.Completed.OutputTokens)
	}

	// 2. The usage must reach the session counters through the production path.
	if before := m.InputTokens + m.OutputTokens; before != 0 {
		t.Fatalf("precondition: the session counters must start empty, got %d", before)
	}
	m.Update(projectedUsageMsg(probe))
	if m.InputTokens != 1234 {
		t.Errorf("session input tokens = %d, want the provider's 1234", m.InputTokens)
	}
	if m.OutputTokens != 567 {
		t.Errorf("session output tokens = %d, want the provider's 567", m.OutputTokens)
	}
	if !m.usageKnown {
		t.Error("known provider usage did not mark the session usage-known")
	}

	// 3. The rendered footer shows the real counts, never zeros.
	m.syncUIState()
	footer := renderRecordsForTest(m)
	if strings.Contains(footer, "↑0") || strings.Contains(footer, "↓0") {
		t.Errorf("the footer rendered a zero for known non-zero usage:\n%s", footer)
	}

	// 4. A provider that reports NOTHING is UNKNOWN, not zero.
	beforeIn, beforeOut := m.InputTokens, m.OutputTokens
	m.executor = execution.NewRuntimeExecutor(root, m.cfg, silentProvider{}, nil, "")
	// The SAME mutation-shaped request as the known-usage probe, so the two halves of
	// this test differ in exactly one fact — whether the provider reported usage.
	// Anything else would vary the request shape and test the wrong seam.
	absent, err := m.executor.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "telemetry-absent", Mode: "build",
		Prompt: "Create one file: telemetry-absent.txt", Target: "telemetry-absent.txt",
	})
	if err != nil {
		t.Fatalf("silent-provider execution: %v", err)
	}
	if absent.Completed.Known {
		t.Fatalf("a provider that reported no usage produced a known account: %+v", absent.Completed)
	}
	// An unknown account must be discarded, never folded in as a real zero.
	if cmd := m.tokenUsageCmdKnown(absent.Completed.InputTokens, absent.Completed.OutputTokens, absent.Completed.Known); cmd != nil {
		t.Error("unknown usage was dispatched as if it were a real count")
	}
	if m.InputTokens != beforeIn || m.OutputTokens != beforeOut {
		t.Errorf("an unknown usage report changed the counters to %d/%d", m.InputTokens, m.OutputTokens)
	}

	// 5. A provider-reported ZERO is preserved, because zero is a fact.
	zero := m.tokenUsageCmdKnown(0, 0, true)
	if zero == nil {
		t.Fatal("a provider-reported zero was suppressed; zero is a real value")
	}
	if msg := zero().(UsageUpdateMsg); !msg.Known {
		t.Error("a real zero was reported as unknown")
	}
}

// TestE2E_ParkedRunReportsItsSpend is the second half of the telemetry contract,
// on the path the first half does not cover: a run that STOPS.
//
// Test G proves the chain from a terminal execution result. A parked run never
// produces one — it produces a human boundary instead — and the park is precisely
// where a bill is easiest to drop. A usage account committed only on termination
// reports nothing here, and the footer then renders the absence of a measurement
// as a measured `↑0 · ↓0`.
func TestE2E_ParkedRunReportsItsSpend(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model

	// The run has already billed the provider and is now parked at its review.
	drv := &fakeAutonomousDriver{
		runID: "run-1", state: autonomy.RuntimeAwaitingHuman,
		parkOnRun: true, aggInput: 2181, aggOutput: 5883, aggKnown: true,
	}
	m.autonomousDriver = drv
	e2ePark(t, m, patchID)

	if before := m.InputTokens + m.OutputTokens; before != 0 {
		t.Fatalf("precondition: the session counters must start empty, got %d", before)
	}

	// The production handler receives the parked result and must still commit
	// the account it was given.
	cmd := m.handleAutonomousRun(extractAutonomousRunMsg(t, autonomousRunMsg{}))
	if cmd == nil {
		t.Fatal("a parked run with a known bill produced no command; the spend was dropped")
	}
	msg, ok := cmd().(UsageUpdateMsg)
	if !ok {
		t.Fatalf("the parked run's command is %T, want UsageUpdateMsg", cmd())
	}
	if !msg.Known {
		t.Fatal("a run that billed the provider reported its spend as unknown")
	}
	if msg.PromptTokens != 2181 || msg.CompletionTokens != 5883 {
		t.Errorf("parked-run usage = ↑%d · ↓%d, want the driver's ↑2181 · ↓5883",
			msg.PromptTokens, msg.CompletionTokens)
	}

	// And the footer shows the real counts.
	m.Update(msg)
	m.syncUIState()
	if footer := renderRecordsForTest(m); strings.Contains(footer, "↑0") || strings.Contains(footer, "↓0") {
		t.Errorf("a parked run that spent tokens rendered zeros in the footer:\n%s", footer)
	}

	// A parked run whose provider reported NOTHING commits nothing. The
	// distinction that matters: a missing measurement must not become a zero.
	drv.aggInput, drv.aggOutput, drv.aggKnown = 0, 0, false
	inBefore, outBefore := m.InputTokens, m.OutputTokens
	if cmd := m.handleAutonomousRun(extractAutonomousRunMsg(t, autonomousRunMsg{})); cmd != nil {
		if _, isUsage := cmd().(UsageUpdateMsg); isUsage {
			t.Error("an unreported account was dispatched as a measurement")
		}
	}
	if m.InputTokens != inBefore || m.OutputTokens != outBefore {
		t.Errorf("an unreported account changed the counters to %d/%d", m.InputTokens, m.OutputTokens)
	}
}

// silentProvider reports NO usage at all — the "usage unavailable" case. It exists
// so a fabricated zero cannot be mistaken for a real measurement.
type silentProvider struct{}

func (silentProvider) Name() string { return "silent" }

func (silentProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	return &ai.Response{Content: "This workspace contains a single HTML entry point.", FinishReason: "stop"}, nil
}

func (silentProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, io.ErrClosedPipe
}

// projectedUsageMsg is the message the production projection emits from a terminal
// execution result. Using it (rather than a hand-built literal) keeps the test on
// the real contract rather than beside it.
func projectedUsageMsg(res *execution.ExecutionResult) tea.Msg {
	return UsageUpdateMsg{
		PromptTokens:     res.Completed.InputTokens,
		CompletionTokens: res.Completed.OutputTokens,
		Known:            res.Completed.Known,
	}
}

// holdFullFileCandidateAs is holdFullFileCandidate with an explicit request
// identity. A caller uses it when it must control whether a successor candidate
// REUSES a prior identity (the stale-authorization case) or gets a fresh one.

// digestOfCurrentBoundary is the digest a review was rendered from. The restart
// test asserts the recovered record carries exactly this value — that is what
// makes a restored authorization safe rather than merely plausible.
func digestOfCurrentBoundary(m *model) string {
	if m == nil || m.autonomousBoundary == nil {
		return ""
	}
	return m.autonomousBoundary.CandidateDigest
}

// ── THE OPERATOR CAN ALWAYS ANSWER THE TRUTH QUESTIONS ──────────────────────
//
// The report's closing requirement: the user must be able to determine, at any
// moment, what conversation they are in, what execution run exists, what state it
// is in, what candidate it holds, what they are authorizing, whether anything has
// been mutated, and what remains — and the runtime must answer from AUTHORITATIVE
// state rather than from heuristics, cached UI labels or optimistic assumptions.
func TestE2E_ExecutionTruthAnswersEveryQuestionFromAuthoritativeState(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	sm := newE2ESessionManager(t)
	m.sessionManager = sm
	m.sess = sm.Session()
	e2ePark(t, m, patchID)

	m.runSessionCmd("/session execution")
	text := viewportOf(m)
	for _, want := range []string{
		"EXECUTION TRUTH",
		"Conversation:", // which conversation
		"Run:",          // which run
		"Lifecycle:",    // what state it is in
		"Boundary:",     // what it is waiting for
		"Candidate:",    // what candidate exists
		"Mutated:",      // has anything been mutated
		"Remaining:",    // what remains to be done
		"run-1",         // the driver's own run identity
		patchID,         // the executor's own candidate identity
		e2eTarget,       // the authoritative scope
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the execution truth does not answer %q:\n%s", want, text)
		}
	}
	// The mutation answer must be the truthful one: nothing has been applied.
	if !strings.Contains(text, "no — nothing has been written") {
		t.Errorf("the execution truth overclaims a mutation:\n%s", text)
	}
	if !strings.Contains(text, "awaiting mutation authorization") {
		t.Errorf("the execution truth does not name the pending decision:\n%s", text)
	}
	if !strings.Contains(text, "human authorization") {
		t.Errorf("the execution truth does not say what remains:\n%s", text)
	}
	// And rendering it changed nothing.
	if !m.executor.CandidateHeld(patchID) {
		t.Fatal("rendering the truth disturbed the held candidate")
	}
	if !m.workflowSM.Parked() {
		t.Fatal("rendering the truth released the parked run")
	}
}

// TestE2E_ExecutionTruthStaysTrueAfterTheMutationLands is the second half of the
// truth-surface contract.
//
// A surface that always answers "no mutation" is not conservative, it is wrong:
// the operator asks it precisely when they suspect something changed, and a
// permanent "nothing has been written" about a shipped change is the most
// dangerous answer this panel can give. The mutation line must therefore be read
// from the executor's own post-write record — and the lifecycle must stop
// reading `building` once the run is over.
func TestE2E_ExecutionTruthStaysTrueAfterTheMutationLands(t *testing.T) {
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	sm := newE2ESessionManager(t)
	m.sessionManager = sm
	m.sess = sm.Session()

	e2ePark(t, m, patchID)

	// 1. While parked, nothing has been written — the truthful answer is "no".
	if truth := m.Truth(); truth.MutationApplied {
		t.Fatalf("a parked run claims a mutation was applied: %v", truth.AppliedTargets)
	}

	// 2. Authorize and apply the SAME candidate through the production approval
	//    seam. No regeneration, no second run.
	drv := m.autonomousDriver.(*fakeAutonomousDriver)
	drv.boundary = nil
	drv.term = &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "applied"}
	drv.state = autonomy.RuntimeExecuting
	if cmd := m.resumeAutonomousApprove(); cmd == nil {
		t.Fatalf("approval refused:\n%s", viewportOf(m))
	}
	if _, err := m.executor.Approve(context.Background(), patchID); err != nil {
		t.Fatalf("applying the authorized candidate: %v", err)
	}

	// 3. The truth surface now says yes, and names the target it wrote.
	truth := m.Truth()
	if !truth.MutationApplied {
		t.Fatalf("the mutation landed but the execution truth still reports nothing was written:\n%s", viewportOf(m))
	}
	if len(truth.AppliedTargets) != 1 || truth.AppliedTargets[0] != e2eTarget {
		t.Errorf("applied targets = %v, want exactly [%s]", truth.AppliedTargets, e2eTarget)
	}
	m.runSessionCmd("/session execution")
	text := viewportOf(m)
	if !strings.Contains(text, "yes — written to: "+e2eTarget) {
		t.Errorf("the execution truth does not report the applied target:\n%s", text)
	}
	if strings.Contains(text, "no — nothing has been written") {
		t.Errorf("the execution truth contradicts itself — it also reports no mutation:\n%s", text)
	}
	// It must not overclaim verification either: the record proves bytes on
	// disk, not a passing verifier.
	if strings.Contains(text, "verified") {
		t.Errorf("the execution truth claims verification it does not hold:\n%s", text)
	}

	// 4. A PROVEN completion ends the run: the lifecycle stops reading as though
	// something were still executing.
	m.unwindTerminalExecution()
	after := m.Truth()
	if after.ExecutionState == workflow.StateBuilding.String() {
		t.Errorf("a finished run still reads lifecycle=%q\n%s", after.ExecutionState, viewportOf(m))
	}
	// And the mutation record survives the unwind — an ended run did not unwrite
	// the file.
	if !after.MutationApplied {
		t.Error("the unwind erased the record of an applied mutation")
	}
}
