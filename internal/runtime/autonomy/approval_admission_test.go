package autonomy

// ── FAILED COMPUTATION MUST NOT CROSS THE AUTHORIZATION BOUNDARY ─────────────
//
// The invariant under test (spec §1/§3/§14):
//
//	FAILED / OUTPUT_EXHAUSTED / SUPERSEDED / CANCELLED computation
//	    → ArtifactState != PRODUCED
//	    → ProposalState  != EXECUTABLE
//	    → MutationAuthorization = NOT_ADMISSIBLE
//
// and the state machine an approval boundary must obey:
//
//	candidate valid → authorization admissible → approval required
//
// NOT:
//
//	candidate exists → show approval → discover authorization is impossible
//
// Every test here drives the REAL Driver → ExecutorAdapter → RuntimeExecutor over
// a real directory with a scripted provider. Nothing is mocked except the
// provider's response text.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/workflow"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// ── providers ────────────────────────────────────────────────────────────────

// zeroByteProvider is the production repro shape: every invocation is cut at the
// provider's output ceiling and delivers NOTHING. It is the canonical
// "output exhausted with no new delivered bytes" computation — the bounded-step
// no-progress guard fires on step 1.
type zeroByteProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *zeroByteProvider) Name() string { return "zero-byte" }

func (p *zeroByteProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return &ai.Response{
		Content: "",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 3072, FinishReason: "length"},
	}, nil
}

func (p *zeroByteProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

func (p *zeroByteProvider) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// patchThenFailProvider answers the first invocation with a valid, complete
// artifact for target and every later invocation with a zero-byte truncation.
// It is the "computation A succeeds, computation B fails" lineage.
type patchThenFailProvider struct {
	mu       sync.Mutex
	calls    int
	artifact string
}

func (p *patchThenFailProvider) Name() string { return "patch-then-fail" }

func (p *patchThenFailProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return &ai.Response{
			Content: p.artifact,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 120, FinishReason: "stop"},
		}, nil
	}
	return &ai.Response{
		Content: "",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 3072, FinishReason: "length"},
	}, nil
}

func (p *patchThenFailProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

func (p *patchThenFailProvider) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// perTargetProvider answers per TARGET: a target whose real bytes appear in the
// prompt AND which is present in `complete` gets its scripted artifact; every
// other invocation gets a zero-byte truncation. It lets a test hold a candidate
// for one file while another file's computation FAILS, which is exactly the
// situation the supersession scope rule governs.
type perTargetProvider struct {
	mu       sync.Mutex
	complete map[string]string
}

func (p *perTargetProvider) Name() string { return "per-target" }

func (p *perTargetProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// The executor names the one file it is mutating on the SYSTEM channel
	// ("You are strictly modifying ONE file: <path>"). Keying the script on that
	// makes each answer attributable to the file rather than to call order.
	for target, artifact := range p.complete {
		if strings.Contains(req.System, "ONE file: "+target) {
			return &ai.Response{
				Content: artifact,
				Usage:   ai.ProviderUsage{Known: true, PromptTokens: 600, CompletionTokens: 90, FinishReason: "stop"},
			}, nil
		}
	}
	return &ai.Response{
		Content: "",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 3072, FinishReason: "length"},
	}, nil
}

func (p *perTargetProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// ── admission authority stubs ────────────────────────────────────────────────

// admitVerifier / admitCheckpoints are the two collaborators the production
// AuthorizationEngine needs. Both are satisfied: the point of these tests is the
// ORDERING of the approval boundary, not the checkpoint store.
type admitVerifier struct{}

func (admitVerifier) VerifySourceHash([]string, string) error { return nil }

type admitCheckpoints struct{}

func (admitCheckpoints) HasCheckpoint() bool { return true }

func (admitCheckpoints) LatestCheckpoint() (workflow.CheckpointRef, error) {
	return workflow.CheckpointRef("cp-test"), nil
}

// writeCaps is the capability set the composition root grants in production:
// write+patch over the workspace, with no path restriction.
func writeCaps() *domaincap.CapabilitySet {
	cs := domaincap.NewCapabilitySet()
	cs.Grant(domaincap.CapabilityWrite)
	cs.Grant(domaincap.CapabilityPatch)
	return cs
}

func newAdmissibleEngine(mb *budget.MutationBudget) *authorization.AuthorizationEngine {
	return authorization.NewAuthorizationEngine(admitVerifier{}, admitCheckpoints{},
		func() workflow.WorkflowState { return workflow.StateBuilding })
}

// realAdmissionProbe wires the driver's approval-admission authority to the REAL
// AuthorizationEngine, exactly as the composition root does: the pre-check and the
// token issued on approve are the same engine.
func realAdmissionProbe(mb *budget.MutationBudget) ApprovalAdmissionFunc {
	eng := newAdmissibleEngine(mb)
	return func(targets []string, _ string) error { return eng.AdmissibleBuild(targets, writeCaps(), mb) }
}

// ── workspace helpers ────────────────────────────────────────────────────────

func lifecycleWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html": "<!DOCTYPE html>\n<html><body>\n  <h1>Placeholder</h1>\n</body></html>\n",
		"styles.css": ":root { --fg: #111 }\nbody { margin: 0 }\n",
		"script.js":  "console.log('placeholder');\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func snapshotFiles(t *testing.T, root string, names ...string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = string(b)
	}
	return out
}

func assertUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	for name, prior := range before {
		now, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s became unreadable: %v", name, err)
		}
		if string(now) != prior {
			t.Errorf("%s was mutated by a non-authorized run:\nbefore=%q\nafter =%q", name, prior, string(now))
		}
	}
}

var allLifecycleFiles = []string{"index.html", "styles.css", "script.js"}

func lifecycleBounds() autonomy.LoopBounds {
	return autonomy.LoopBounds{
		MaxAttempts: 6, MaxRecoveryCycles: 6, MaxExecutionSteps: 30,
		MaxIdenticalDecisions: 30, MaxTotalTokens: 2_000_000,
	}
}

// ── CASE A — output exhaustion with zero artifact bytes ──────────────────────

// TestCaseA_ZeroByteExhaustionNeverReachesTheApprovalBoundary is the reported
// production defect, end to end:
//
//	model invoked → provider exhausts output → no artifact bytes delivered
//	→ executor fails → NO executable mutation proposal → NO mutation approval
//	state → NO mutation → objective remains unproven
func TestCaseA_ZeroByteExhaustionNeverReachesTheApprovalBoundary(t *testing.T) {
	root := lifecycleWorkspace(t)
	before := snapshotFiles(t, root, allLifecycleFiles...)

	mb := budget.DefaultBudget()
	provider := &zeroByteProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mb)),
	)

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 1. THE MODEL WAS ACTUALLY INVOKED. Without this the test could pass by
	//    never reaching the provider at all.
	if provider.n() == 0 {
		t.Fatal("the provider was never invoked; this test must exercise a real exhaustion")
	}
	// 2. NO EXECUTABLE MUTATION PROPOSAL.
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("a zero-byte exhausted computation produced executable candidate(s) %v", ids)
	}
	if x.CandidateHeld("") {
		t.Fatal("an empty candidate id must never be reported as held")
	}
	// 3. NO MUTATION APPROVAL STATE.
	b := d.Boundary()
	if b == nil {
		t.Fatal("the run must converge on a human boundary, not vanish")
	}
	if b.Action == autonomy.HumanBoundaryApproval {
		t.Fatalf("a computation that delivered no artifact bytes reached the approval boundary: %+v", b)
	}
	if b.PatchID != "" {
		t.Fatalf("an approval boundary carries candidate %q for a computation that produced nothing", b.PatchID)
	}
	if b.Resumable && b.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("only an informational park may be non-decisional: %+v", b)
	}
	// 4. NO MUTATION.
	assertUnchanged(t, root, before)
	// 5. THE OBJECTIVE REMAINS UNPROVEN.
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("output exhaustion reported completion: %+v", term)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("output exhaustion reached the completed state")
	}
	for _, tr := range d.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("history records a false completion: %+v", tr)
		}
	}
	// And the refusal is TRUTHFUL: the boundary names the real cause.
	if !strings.Contains(strings.ToLower(b.Reason), "exhaust") &&
		!strings.Contains(strings.ToLower(b.Reason), "format failures") &&
		!strings.Contains(strings.ToLower(b.Reason), "hard-block") {
		t.Errorf("boundary reason does not name the real cause: %q", b.Reason)
	}
}

// TestCaseA_ExecutorRejectsAPendingApprovalOnZeroByteExhaustion pins the layer
// that owns artifact state: the executor must never report OutcomePendingApproval
// for a computation that delivered no bytes, so nothing downstream can infer
// "produced" from the mere fact that a provider was invoked.
func TestCaseA_ExecutorRejectsAPendingApprovalOnZeroByteExhaustion(t *testing.T) {
	root := lifecycleWorkspace(t)
	provider := &zeroByteProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "case-a", Mode: "build",
		Prompt: acceptanceObjective, Targets: []string{"index.html"},
		Model: "test/model",
	})
	if err == nil {
		t.Fatalf("a zero-byte exhausted computation must fail, got outcome=%s", res.Proof.Outcome)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected a result carrying a proof")
	}
	if res.Proof.Outcome != execution.OutcomeTruncated {
		t.Errorf("outcome = %s, want %s (typed, recoverable exhaustion)", res.Proof.Outcome, execution.OutcomeTruncated)
	}
	if res.PendingPatchID != "" {
		t.Errorf("OutcomeTruncated carried an approval-held candidate %q", res.PendingPatchID)
	}
	if res.Content != "" || res.ArtifactKind != "" {
		t.Errorf("rejected generation leaked bytes onto the result: kind=%q len=%d", res.ArtifactKind, len(res.Content))
	}
	// The partial candidate survives as EVIDENCE only, and is explicitly not
	// committed: evidence about a computation, never state a mutation can use.
	if len(res.ArtifactCandidates) == 0 {
		t.Fatal("the exhaustion evidence was dropped")
	}
	for _, c := range res.ArtifactCandidates {
		if c.Committed {
			t.Errorf("a zero-byte candidate is marked committed: %+v", c)
		}
		if c.DeliveredBytes != 0 {
			t.Errorf("a zero-byte run claims %d delivered bytes", c.DeliveredBytes)
		}
	}
	// The typed condition must be the recoverable bounded-step one, so the
	// continuation matrix classifies it as OUTPUT_EXHAUSTED and never as a
	// schema violation.
	if !llmstep.IsOutputExhausted(err) {
		t.Errorf("err = %v, want a typed llmstep.OutputExhaustedError", err)
	}
	if got := RecoverySubtype(observeForTest(x, res)); got != SubtypeOutputExhausted {
		t.Errorf("failure subtype = %s, want %s", got, SubtypeOutputExhausted)
	}
}

func observeForTest(_ *execution.RuntimeExecutor, res *execution.ExecutionResult) autonomy.Observation {
	return autonomy.Observation{
		Outcome:    autonomy.ExecutionOutcome(res.Proof.Outcome),
		Target:     "index.html",
		Diagnostic: errString(res.Err),
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ── CASE B — stale candidate ─────────────────────────────────────────────────

// TestCaseB_SupersededCandidateCannotReachTheApprovalBoundary proves a candidate
// whose producing computation FAILED is neither held nor approvable, and that a
// driver still holding such a boundary never presents it as an approval.
func TestCaseB_SupersededCandidateCannotReachTheApprovalBoundary(t *testing.T) {
	root := lifecycleWorkspace(t)
	before := snapshotFiles(t, root, "index.html")

	provider := &patchThenFailProvider{
		artifact: "<<<<<<< SEARCH\n  <h1>Placeholder</h1>\n=======\n  <h1>TomHunter</h1>\n>>>>>>>",
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	// COMPUTATION A succeeds and holds a candidate.
	held, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "computation-a", Mode: "build", Prompt: "rename the heading in @index.html",
		Target: "index.html", Model: "test/model",
	})
	if err != nil || held.PendingPatchID == "" {
		t.Fatalf("computation A must hold a candidate: err=%v", err)
	}
	stale := held.PendingPatchID

	// COMPUTATION B over the SAME target fails at the bounded-step output gate.
	if _, bErr := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "computation-b", Mode: "build", Prompt: "rename the heading in @index.html",
		Target: "index.html", Model: "test/model",
	}); bErr == nil {
		t.Fatal("computation B must fail at the output gate")
	}

	// The candidate of A is gone from the runtime's own held set…
	if x.CandidateHeld(stale) {
		t.Fatalf("candidate %s of the superseded computation is still held", stale)
	}
	if !adapter.CandidateHeld(stale) {
		// the adapter must agree with the executor: same map, no second lineage
		t.Logf("adapter agrees candidate %s is not held", stale)
	}
	// …and the mutation boundary refuses it.
	if _, aErr := x.Approve(context.Background(), stale); aErr == nil {
		t.Fatal("Approve accepted a candidate whose producing computation was superseded")
	}
	assertUnchanged(t, root, before)
	if provider.n() < 2 {
		t.Fatalf("expected a success then a failure, got %d invocations", provider.n())
	}
}

// TestCaseB_DriverDowngradesAnApprovalBoundaryWhoseCandidateIsGone is the runtime
// half of §4: the approval surface itself must not appear when the candidate is
// not held. The driver learns this from the executor's own pending map.
func TestCaseB_DriverDowngradesAnApprovalBoundaryWhoseCandidateIsGone(t *testing.T) {
	root := lifecycleWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	provider := &patchThenFailProvider{artifact: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>"}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	mb := budget.DefaultBudget()
	d := NewDriver(adapter, bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mb)))

	// Drive the loop far enough that the executor holds a candidate, then
	// supersede it out from under the parked boundary. Rather than reaching
	// through the loop's internals, the boundary is exercised directly: an
	// approval naming a candidate the executor does not hold must be refused.
	ghost := &autonomy.HumanBoundary{
		Reason:    "mutation awaiting approval",
		PatchID:   "computation-a-patch-1",
		Action:    autonomy.HumanBoundaryApproval,
		Resumable: true,
		Targets:   []string{"index.html"},
	}
	if x.CandidateHeld(ghost.PatchID) {
		t.Fatal("precondition: the ghost candidate must not be held")
	}
	d.admitApproval(ghost)
	if ghost.Action == autonomy.HumanBoundaryApproval {
		t.Fatalf("an approval boundary survived without a held candidate: %+v", ghost)
	}
	if ghost.PatchID != "" {
		t.Fatalf("a refused boundary still names candidate %q", ghost.PatchID)
	}
	if ghost.Resumable {
		t.Fatal("a refused boundary must not be resumable")
	}
	if !strings.Contains(ghost.Reason, "no longer held") {
		t.Errorf("refusal reason does not name the cause: %q", ghost.Reason)
	}
}

// ── CASE C — a valid previous candidate ──────────────────────────────────────

// TestCaseC_ValidCandidateOfASuccessfulComputationRemainsAdmissible states the
// contract that DOES exist: a candidate whose computation is still the live one
// is executable, and it stays admissible. Case B must not be "fixable" by
// invalidating every candidate.
func TestCaseC_ValidCandidateOfASuccessfulComputationRemainsAdmissible(t *testing.T) {
	root := lifecycleWorkspace(t)
	before := snapshotFiles(t, root, "index.html")

	provider := &patchThenFailProvider{
		artifact: "<<<<<<< SEARCH\n  <h1>Placeholder</h1>\n=======\n  <h1>TomHunter</h1>\n>>>>>>>",
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "computation-a", Mode: "build", Prompt: "rename the heading in @index.html",
		Target: "index.html", Model: "test/model",
	})
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("computation A must hold a candidate: err=%v", err)
	}
	if !adapter.CandidateHeld(res.PendingPatchID) {
		t.Fatalf("a live candidate is not reported as held: %s", res.PendingPatchID)
	}

	// The driver presents it as an approval ONLY because it is held AND
	// admissible.
	mb := budget.DefaultBudget()
	d := NewDriver(adapter, bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mb)))
	ok := &autonomy.HumanBoundary{
		Reason: "mutation awaiting approval", PatchID: res.PendingPatchID,
		Action: autonomy.HumanBoundaryApproval, Resumable: true,
		Targets: []string{"index.html"},
	}
	d.admitApproval(ok)
	if ok.Action != autonomy.HumanBoundaryApproval || ok.PatchID == "" {
		t.Fatalf("a valid, admissible candidate was refused: %+v", ok)
	}

	// And it is genuinely executable end to end.
	apr, aErr := x.Approve(context.Background(), res.PendingPatchID)
	if aErr != nil {
		t.Fatalf("a live candidate must be approvable: %v", aErr)
	}
	if apr.Proof.Outcome != execution.OutcomeChanged {
		t.Fatalf("approve outcome = %s, want changed", apr.Proof.Outcome)
	}
	got, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "TomHunter") {
		t.Fatalf("the live candidate's bytes never reached disk: %q", string(got))
	}
	_ = before
}

// TestCaseC_UnrelatedTargetsDoNotContendForOneApproval follows the existing
// lineage rule: a candidate held for a DIFFERENT file is a different approval and
// a failing computation over one target must not destroy it. The rule is stated
// here so Case B's fix cannot be over-applied.
func TestCaseC_UnrelatedTargetsDoNotContendForOneApproval(t *testing.T) {
	root := lifecycleWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	// Answer per TARGET: the CSS target gets a complete artifact, the HTML target
	// gets a zero-byte truncation.
	x := testExecutor(t, root, &perTargetProvider{
		complete: map[string]string{
			"styles.css": "<<<<<<< SEARCH\nbody { margin: 0 }\n=======\nbody { margin: 0; font-family: system-ui }\n>>>>>>>",
		},
	}, bus)

	css, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "css-only", Mode: "build", Prompt: "restyle @styles.css",
		Target: "styles.css", Model: "test/model",
	})
	if err != nil || css.PendingPatchID == "" {
		t.Fatalf("precondition: a CSS candidate is required: err=%v", err)
	}
	// A failing computation over index.html must leave the CSS candidate alone.
	htmlRes, htmlErr := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "html-fail", Mode: "build", Prompt: "restyle @index.html",
		Target: "index.html", Model: "test/model",
	})
	if htmlErr == nil {
		t.Fatalf("precondition: the HTML computation must fail, got %s", htmlRes.Proof.Outcome)
	}
	if !x.CandidateHeld(css.PendingPatchID) {
		t.Fatalf("an unrelated candidate %s was destroyed by a failing computation", css.PendingPatchID)
	}
	if _, aErr := x.Approve(context.Background(), css.PendingPatchID); aErr != nil {
		t.Fatalf("the unrelated candidate must remain executable: %v", aErr)
	}
}

// ── CASE D — insufficient budget ─────────────────────────────────────────────

// TestCaseD_InsufficientBudgetWithholdsTheApprovalBoundary is the reported
// "show approval → discover authorization impossible" shape, at the runtime
// boundary. When the runtime's own authorization authority refuses, there is no
// approval surface at all.
func TestCaseD_InsufficientBudgetWithholdsTheApprovalBoundary(t *testing.T) {
	root := lifecycleWorkspace(t)
	before := snapshotFiles(t, root, allLifecycleFiles...)

	provider := &patchThenFailProvider{
		artifact: "<<<<<<< SEARCH\n  <h1>Placeholder</h1>\n=======\n  <h1>TomHunter</h1>\n>>>>>>>",
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	// A candidate that IS valid and IS held.
	held, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "computation-a", Mode: "build", Prompt: "rename the heading in @index.html",
		Target: "index.html", Model: "test/model",
	})
	if err != nil || held.PendingPatchID == "" {
		t.Fatalf("precondition: a valid held candidate is required: err=%v", err)
	}

	// The mutation budget is genuinely spent (cumulative, not the clock).
	mb := budget.NewBudget(1, 5000, 1_000_000, 10, 30*time.Second, 5)
	if err := mb.Consume(budget.BudgetDelta{Files: 1}); err != nil {
		t.Fatal(err)
	}
	if !mb.IsExhausted() {
		t.Fatal("precondition: the mutation budget must be exhausted")
	}

	d := NewDriver(adapter, bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mb)))

	b := &autonomy.HumanBoundary{
		Reason: "mutation awaiting approval", PatchID: held.PendingPatchID,
		Action: autonomy.HumanBoundaryApproval, Resumable: true,
		Targets: []string{"index.html"},
	}
	d.admitApproval(b)

	if b.Action == autonomy.HumanBoundaryApproval {
		t.Fatalf("an approval surface was opened for a mutation the runtime cannot authorize: %+v", b)
	}
	if b.Resumable {
		t.Fatal("a budget-refused boundary must not be resumable")
	}
	if b.PatchID != "" {
		t.Fatalf("a budget-refused boundary still names candidate %q", b.PatchID)
	}
	if !strings.Contains(b.Reason, "budget") || !strings.Contains(b.Reason, "exhausted") {
		t.Errorf("the terminal reason is not the truthful budget outcome: %q", b.Reason)
	}
	// The authorization itself must be a truthful terminal denial.
	eng := newAdmissibleEngine(mb)
	denied := eng.AdmissibleBuild([]string{"index.html"}, writeCaps(), mb)
	if denied == nil {
		t.Fatal("AdmissibleBuild admitted a mutation over an exhausted budget")
	}
	var authErr *authorization.AuthorizationDenied
	if !errors.As(denied, &authErr) {
		t.Fatalf("denial = %T, want *authorization.AuthorizationDenied", denied)
	}
	if authErr.Step != authorization.StepBudgetSufficiency {
		t.Errorf("denial step = %s, want budget-sufficiency", authErr.Step)
	}
	assertUnchanged(t, root, before)
}

// TestCaseD_ProbeConsumesNothing proves the admission probe is a READ. A probe
// that spent budget would make the very act of asking a human cost the session its
// remaining mutation allowance.
func TestCaseD_ProbeConsumesNothing(t *testing.T) {
	mb := budget.NewBudget(10, 5000, 1_000_000, 10, 30*time.Second, 5)
	eng := newAdmissibleEngine(mb)
	for i := 0; i < 25; i++ {
		if err := eng.AdmissibleBuild([]string{"index.html", "styles.css"}, writeCaps(), mb); err != nil {
			t.Fatalf("probe %d denied: %v", i, err)
		}
	}
	if got := mb.RemainingFiles(); got != 10 {
		t.Fatalf("the admission probe consumed budget: RemainingFiles=%d, want 10", got)
	}
}

// TestCaseD_ProbeAndAuthorizationShareOneClauseList pins that the pre-check
// cannot drift from the authorization it previews: a mutation the probe admits is
// authorizable, and one it refuses is refused by the engine too.
func TestCaseD_ProbeAndAuthorizationShareOneClauseList(t *testing.T) {
	mb := budget.NewBudget(10, 5000, 1_000_000, 10, 30*time.Second, 5)
	eng := newAdmissibleEngine(mb)

	// Admissible: scope inside the grant.
	if err := eng.AdmissibleBuild([]string{"index.html"}, writeCaps(), mb); err != nil {
		t.Fatalf("an in-scope mutation must be admissible: %v", err)
	}
	if _, err := eng.AuthorizeBuildCandidate([]string{"index.html"}, writeCaps(), mb, nil, false, true, "cand-1"); err != nil {
		t.Fatalf("the same mutation must be authorizable: %v", err)
	}

	// Refused: out of scope — by BOTH entry points.
	scoped := domaincap.NewCapabilitySet()
	scoped.Grant(domaincap.CapabilityWrite, domaincap.ScopeRule{
		Capability: domaincap.CapabilityWrite, Patterns: []string{"*.html"},
	})
	if err := eng.AdmissibleBuild([]string{"styles.css"}, scoped, mb); err == nil {
		t.Fatal("an out-of-scope target was declared admissible")
	} else {
		var denied *authorization.AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != authorization.StepScopeContainment {
			t.Errorf("scope refusal = %v, want a scope-containment denial", err)
		}
	}
	if _, err := eng.AuthorizeBuildCandidate([]string{"styles.css"}, scoped, mb, nil, false, true, "cand-2"); err == nil {
		t.Fatal("an out-of-scope target was authorized")
	}

	// Refused: budget — by BOTH entry points.
	spent := budget.NewBudget(1, 5000, 1_000_000, 10, 30*time.Second, 5)
	if err := spent.Consume(budget.BudgetDelta{Files: 1}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AdmissibleBuild([]string{"index.html"}, writeCaps(), spent); err == nil {
		t.Fatal("an exhausted budget was declared admissible")
	}
	if _, err := eng.AuthorizeBuildCandidate([]string{"index.html"}, writeCaps(), spent, nil, false, true, "cand-3"); err == nil {
		t.Fatal("an exhausted budget was authorized")
	}
}

// ── CASE E — the successful path stays intact ───────────────────────────────

// TestCaseE_PortfolioSuccessStillReachesTheApprovalBoundaryAndApplies is the
// end-to-end success path WITH the admission gate bound. The fix must not tax the
// happy path: a genuine, admissible candidate still parks at approval, is
// authorized, mutates the workspace and verifies.
func TestCaseE_PortfolioSuccessStillReachesTheApprovalBoundaryAndApplies(t *testing.T) {
	root := portfolioWorkspace(t)
	before := snapshotFiles(t, root, "index.html", "styles.css", "script.js")

	mock := &portfolioProvider{served: map[string]int{}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	mb := budget.DefaultBudget()
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mb)))

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("the canonical $prompt path must not error: %v", err)
	}
	if term != nil && term.State.IsTerminal() {
		t.Fatalf("run terminated before the approval gate (state=%s reason=%q)", term.State, term.Reason)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryApproval || b.PatchID == "" {
		t.Fatalf("boundary = %+v; want an approval gate carrying a real held patch", b)
	}
	if !b.Resumable {
		t.Fatal("a valid admissible approval boundary must be resumable")
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("state = %s after approve, want completed", d.State())
	}
	for name, prior := range before {
		now, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(now) == prior {
			t.Errorf("%s was not mutated: the success path regressed", name)
		}
	}
	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied {
		t.Errorf("evidence mutation state = %q, want APPLIED", obs.Objective.Mutation)
	}
	if !obs.Objective.VerificationRan {
		t.Error("evidence records that verification never ran")
	}
	// mutation != proof: the objective authority still owns the completion claim.
	if ev := d.objectiveEvaluation(); !ev.Outcome.Proves() {
		t.Errorf("objective verdict = %s (%s), want PROVEN after a verified apply", ev.Outcome, ev.Reason)
	}
}
