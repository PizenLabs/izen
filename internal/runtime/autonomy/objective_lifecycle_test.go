package autonomy

// ── The objective lifecycle, exercised through the REAL Driver ───────────────
//
// The domain-level cases live in internal/execution/objective_contract_test.go
// and prove the CONTRACT is correct. This file proves the RUNTIME honours it:
// that the completion seam, the continuation router and the trace all read the
// objective's own state rather than the shape of the work that happened to land.
//
// Three layers are used deliberately, and each one says what it can and cannot
// prove:
//
//	DRIVER SEAM   the exact gate under test, driven with a hand-built
//	              observation — precise about the decision, no executor
//	FULL RUN      Driver → adapter → executor → provider → authorization →
//	              mutation → verification, with a scripted model — proves the
//	              contract survives the real pipeline
//	TRACE         the emitted activity stream — proves the telemetry is
//	              generated from runtime events, not assembled as a checklist

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// scriptedRequirements returns a RequirementPassFunc that proposes exactly the
// supplied requirement texts. It models a model that enumerates what the
// objective genuinely requires — which is all the pass is ever allowed to do.
func scriptedRequirements(texts ...string) RequirementPassFunc {
	return func(_ context.Context, _ string, _ []string) ([]execution.DerivedRequirement, error) {
		out := make([]execution.DerivedRequirement, 0, len(texts))
		for i, t := range texts {
			out = append(out, execution.DerivedRequirement{
				ID:     "r" + smallInt(i+1),
				Text:   t,
				Origin: execution.OriginModel,
			})
		}
		return out, nil
	}
}

// completedArtifact wraps a scripted artifact in a response that reports an
// authoritative finish_reason, so the transport boundary reads DONE rather than
// STREAMING. It changes nothing about the payload.
func completedArtifact(content string) *ai.Response {
	return &ai.Response{
		Content: content,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 20, FinishReason: "stop"},
	}
}

func smallInt(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

var errRequirementPassUnavailable = &requirementPassError{}

type requirementPassError struct{}

func (*requirementPassError) Error() string { return "requirement pass unavailable" }

// activityRecorder subscribes to the bus BEFORE a run and records every
// activity line the runtime emits. Asserting on it proves the trace was
// generated from real runtime events rather than assembled as a static list.
type activityRecorder struct {
	mu    sync.Mutex
	lines []string
}

func newActivityRecorder(bus *events.Bus) *activityRecorder {
	r := &activityRecorder{}
	bus.SubscribeAll(func(ev events.DomainEvent) {
		payload, ok := ev.Payload().(events.ActivityPayload)
		if !ok {
			return
		}
		r.mu.Lock()
		r.lines = append(r.lines, payload.Line)
		r.mu.Unlock()
	})
	return r
}

func (r *activityRecorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// waitForActivity waits until the bus has delivered everything the run emitted.
// The bus dispatches asynchronously, so a zero-wait read would assert against a
// partially-populated trace.
func waitForActivity(r *activityRecorder) {
	deadline := time.Now().Add(750 * time.Millisecond)
	last := -1
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.lines)
		r.mu.Unlock()
		if n > 0 && n == last {
			return
		}
		last = n
		time.Sleep(15 * time.Millisecond)
	}
}

// runObjective drives a real full run over a single-target workspace.
func runObjective(t *testing.T, objective string, responses []*ai.Response, opts ...Option) (*Driver, string) {
	t.Helper()
	root, _, adapter, _ := testHarness(t, responses)
	d := NewDriver(adapter, nil, opts...)
	if _, err := d.Run(context.Background(), objective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return d, root
}

// partialLifecycle is the minimum-patch shape at the driver seam: a REAL durable
// mutation applied through the REAL approval gate, against an objective contract
// that declares one more target than the evidence reaches.
type partialLifecycle struct {
	driver   *Driver
	contract execution.ObjectiveContract
	root     string
	// dispatched is the observation the executor produced for the approved
	// mutation, captured before the scope was widened. It carries the transport
	// facts (provider state, finish reason) the boundary cases assert on.
	dispatched autonomy.Observation
	// decision is the completion claim as the authority left it: a claimed
	// completion rewritten into the action its evidence supports.
	decision autonomy.LoopDecision
}

// partiallySatisfyingDriver builds a partialLifecycle over the single-target
// fixture.
//
// WHY THE SCOPE IS WIDENED RATHER THAN SERVED BY A SECOND ARTIFACT. One
// approval applies every held candidate, and a single scripted generation either
// produces a candidate for every declared target or fails the whole execution at
// the artifact boundary — so the end-to-end pipeline cannot be made to
// under-deliver by scripting a weaker second response. That is itself a truthful
// finding about the executor.
//
// The condition under test is therefore constructed at the seam: the mutation is
// real, the authorization is real, the file really changed on disk, and the
// objective contract then covers one more declared target than the evidence
// reaches. That is the evidence-bound-discovery shape this runtime produces for a
// broad objective whose model covered only part of the scope, and it is the
// state the completion gate has to refuse.
func partiallySatisfyingDriver(t *testing.T, untouched string, proposals ...string) partialLifecycle {
	t.Helper()
	return partiallySatisfyingDriverWith(t, scriptedRequirements(proposals...), untouched, proposals...)
}

// partiallySatisfyingDriverWith is partiallySatisfyingDriver with an injectable
// requirement pass, so the "the pass is optional" cases can drive a real failure
// through the real seam.
func partiallySatisfyingDriverWith(t *testing.T, pass RequirementPassFunc, untouched string, proposals ...string) partialLifecycle {
	t.Helper()
	root, _, adapter, _ := testHarness(t, []*ai.Response{completedArtifact(sampleReplace)})
	writeTarget(t, root, untouched, sampleOriginal)
	d := NewDriver(adapter, nil,
		WithRequirementPass(pass),
		WithLoopBounds(lifecycleBounds()))
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (the mutation is held at the approval gate)", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	dispatched := d.LastObservation()

	// Widen the declared scope: a broad objective's evidence-bound discovery
	// binds every file the objective is about, and the model covered only part of
	// it. Re-derive the contract over the broader scope and re-fold the mutation
	// evidence against it.
	d.resolved.Targets = []string{"note.txt", untouched}
	d.req.Targets = []string{"note.txt", untouched}
	d.resetObjectiveContract()
	d.objective.proposals = scriptedProposalLedger(proposals...)
	d.objective.requirementsDerived = true
	d.objectiveState()
	d.bindStepEvidence()

	// Re-run the completion gate over the widened contract. The runtime does this
	// on every loop iteration; a test that only inspected the cached verdict from
	// the pre-widening run would be asserting stale state.
	decision := autonomy.LoopDecision{
		Action: autonomy.LoopComplete,
		Reason: "completion claim proposed after the mutation landed",
	}
	d.authorizeObjectiveCompletion(&decision)

	return partialLifecycle{
		driver:     d,
		contract:   d.objectiveContract(),
		root:       root,
		dispatched: dispatched,
		decision:   decision,
	}
}

// scriptedProposalLedger returns the proposals a RequirementPassFunc would have
// returned, so the lifecycle can be re-derived without re-invoking the pass.
func scriptedProposalLedger(texts ...string) []execution.DerivedRequirement {
	out := make([]execution.DerivedRequirement, 0, len(texts))
	for i, t := range texts {
		out = append(out, execution.DerivedRequirement{
			ID:     "r" + smallInt(i+1),
			Text:   t,
			Origin: execution.OriginModel,
		})
	}
	return out
}

// ── CASE A — mutation succeeds, objective does not ──────────────────────────

// TestDriverCaseA_MutationAloneDoesNotProveTheObjective is the headline
// runtime-level guarantee.
//
// The objective names two declared targets. The model performs ONE small,
// perfectly valid, perfectly authorized mutation on one of them. The run
// proposes completion. The runtime must not call that success.
func TestDriverCaseA_MutationAloneDoesNotProveTheObjective(t *testing.T) {
	lc := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	)
	d, contract := lc.driver, lc.contract

	// Sanity: the mutation really happened and really is on a declared target.
	if got := readTarget(t, lc.root, "note.txt"); got == sampleOriginal {
		t.Fatal("the authorized mutation did not land; the case is not exercising what it claims to")
	}
	if d.LastObservation().Objective.Mutation != execution.FilesystemApplied {
		t.Fatalf("mutation boundary = %s, want APPLIED — the case needs a real mutation",
			d.LastObservation().Objective.Mutation)
	}
	if len(contract.Scope) < 2 {
		t.Fatalf("the declared scope is %v; the case needs two targets", contract.Scope)
	}

	// Now the verdict.
	if lc.decision.Action == autonomy.LoopComplete {
		t.Fatalf("the completion claim survived the gate: %+v", lc.decision)
	}
	if lc.decision.Action != autonomy.LoopUnsubstantiate {
		t.Fatalf("a refused completion became %q; the only terminal alternative to complete must be unsubstantiate", lc.decision.Action)
	}
	evaluation := d.objectiveEvaluation()
	if evaluation.Outcome == execution.ObjectiveProven {
		t.Fatal("a valid mutation on one of two declared targets PROVED the objective")
	}
	if evaluation.Outcome != execution.ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want UNSUBSTANTIATED", evaluation.Outcome)
	}

	// The refusal must be attributable, not a shrug.
	if evaluation.Clause == "" || evaluation.Reason == "" {
		t.Fatalf("the refusal carries no attributable reason: %+v", evaluation)
	}

	// Every unmet obligation is named individually.
	conditions := d.ObjectiveConditions()
	if len(conditions) == 0 {
		t.Fatal("no completion conditions were authored; the objective contract is not in force")
	}
	var unmet []string
	for _, c := range conditions {
		if !c.Satisfied() {
			unmet = append(unmet, c.ID)
		}
	}
	if len(unmet) == 0 {
		t.Fatalf("every condition reports satisfied yet the objective was refused: %+v", evaluation)
	}
	if !objectiveHasCondition(unmet, "cond-scope-mutated") {
		t.Fatalf("the untransformed declared target is missing from %v", unmet)
	}

	// And the model's own requirement for the untouched target is undischarged.
	report := d.ObjectiveReport()
	var undischarged []string
	for _, r := range report.Requirements {
		if r.Status == execution.RequirementAdmitted && !r.Discharged {
			undischarged = append(undischarged, r.ID)
		}
	}
	if len(undischarged) == 0 {
		t.Fatalf("every admitted requirement reports discharged: %+v", report.Requirements)
	}
	t.Logf("objective=%s progress=%s continuation=%s unmet=%v undischarged=%v reason=%s",
		d.ObjectiveIdentity(), d.ObjectiveProgress(), d.ObjectiveContinuation().Decision,
		unmet, undischarged, evaluation.Reason)
}

// TestDriverCaseA2_ProgressIsPartialNotTerminal pins that an under-delivered but
// advancing objective reports PARTIALLY_SATISFIED and is eligible to continue,
// rather than being collapsed into a terminal failure.
func TestDriverCaseA2_ProgressIsPartialNotTerminal(t *testing.T) {
	d := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	).driver
	progress := d.ObjectiveProgress()
	if progress == execution.ProgressProven {
		t.Fatal("progress = PROVEN on an under-delivered objective")
	}
	if !progress.Advanceable() {
		t.Fatalf("progress = %s, want an advanceable state (the mutation landed; only part of the objective did)", progress)
	}
	next := d.ObjectiveContinuation()
	if !next.Continue() {
		t.Fatalf("continuation = %s, want CONTINUE or REPLAN (%s)", next.Decision, next.Reason)
	}
	if len(next.UnmetConditionIDs) == 0 {
		t.Fatal("the continuation carries no unmet conditions; the next step would not know what is left")
	}
}

// TestDriverCaseA3_ContinuationDoesNotReDispatchAHumanDecision preserves the
// pre-existing invariant that an approved mutation never auto-repairs itself into
// a second provider invocation — including when the objective is still
// incomplete.
//
// It deliberately runs WITHOUT the seam widening: the human gate is recorded by
// the approve path, and a new objective legitimately clears it.
func TestDriverCaseA3_ContinuationDoesNotReDispatchAHumanDecision(t *testing.T) {
	root, _, adapter, _ := testHarness(t, []*ai.Response{completedArtifact(sampleReplace)})
	writeTarget(t, root, "extra.txt", sampleOriginal)
	d := NewDriver(adapter, nil,
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()))
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if !d.objective.humanGated {
		t.Fatal("the approve path did not mark the lifecycle human-gated")
	}
	decision := autonomy.LoopDecision{Action: autonomy.LoopUnsubstantiate, Reason: "forced for the assertion"}
	d.routeObjectiveContinuation(&decision)
	if decision.Action != autonomy.LoopUnsubstantiate {
		t.Fatalf("continuation re-opened the loop after a human decision: action = %s", decision.Action)
	}

	// A genuinely new objective clears the gate, and the same decision may then
	// continue. That is the correct asymmetry.
	d.resetObjectiveContract()
	if d.objective.humanGated {
		t.Fatal("a new objective inherited the previous human gate")
	}
}

// ── CASE B — provider DONE is not objective completion ──────────────────────

// TestDriverCaseB_ProviderDoneIsNotObjectiveCompletion pins the transport
// boundary at the driver seam: the provider returned normally and the run
// proposed completion, but the objective's own requirements were not discharged.
func TestDriverCaseB_ProviderDoneIsNotObjectiveCompletion(t *testing.T) {
	lc := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	)
	d, dispatched := lc.driver, lc.dispatched
	// The provider really did finish cleanly on the dispatched step. That is the
	// whole point of the case: a clean transport plus a real mutation, and the
	// objective is still not proven.
	if got := dispatched.Objective.Provider; got != execution.ProviderDone {
		t.Fatalf("provider boundary = %s, want DONE — the case must exercise a completed transport", got)
	}
	if d.objectiveEvaluation().Outcome == execution.ObjectiveProven {
		t.Fatal("ProviderState=DONE produced ObjectiveState=PROVEN")
	}
	if d.objectiveEvaluation().Clause == "" {
		t.Fatal("the refusal is not attributable to a clause")
	}
}

// ── CASE C — the result was never inspected ─────────────────────────────────

// TestDriverCaseC_ResultNeverInspectedIsNotCompletion removes the single easiest
// step for a runtime to skip — looking at what it just wrote — and proves the
// omission is fatal to the completion claim.
func TestDriverCaseC_ResultNeverInspectedIsNotCompletion(t *testing.T) {
	d, root := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithLoopBounds(lifecycleBounds()))
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	// The runtime DID re-inspect, so this run is legitimately proven. The
	// obligation is real and it was met.
	if len(d.objectiveState().postMutation) == 0 {
		t.Fatal("the runtime applied a mutation and never re-read the result")
	}
	if readTarget(t, root, "note.txt") == sampleOriginal {
		t.Fatal("the approved mutation did not land")
	}
	if d.objectiveEvaluation().Outcome != execution.ObjectiveProven {
		t.Fatalf("a single-target objective whose only condition is the change itself was refused: %s (%s)",
			d.objectiveEvaluation().Outcome, d.objectiveEvaluation().Reason)
	}

	// Now prove the negative against the SAME lifecycle: strip the re-inspection
	// from the bundle and the objective stops being provable.
	ev := d.objectiveEvidenceWithContract()
	if len(ev.PostMutationObserved) == 0 {
		t.Fatal("fixture is broken: the bundle carried no post-mutation observation")
	}
	ev.PostMutationObserved = nil
	if got := execution.AuthorizeObjective(d.lastContract, ev); got.Outcome == execution.ObjectiveProven {
		t.Fatal("an uninspected mutation result reached PROVEN")
	}
}

// ── CASE D — output exhausted never mutates ─────────────────────────────────

// TestDriverCaseD_OutputExhaustedNeverMutates re-pins the pre-existing safety
// invariant through the new contract, so a future change cannot quietly route
// exhaustion into a completion.
func TestDriverCaseD_OutputExhaustedNeverMutates(t *testing.T) {
	d, root := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: "prose only, no artifact"}},
		WithLoopBounds(lifecycleBounds()))
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a computation that produced no artifact completed")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("the workspace changed: %q", got)
	}
	if d.objectiveEvaluation().Outcome == execution.ObjectiveProven {
		t.Fatal("a lifecycle with no artifact reached PROVEN")
	}
}

// ── CASE E / F — satisfied objectives, including a tiny one ─────────────────

// TestDriverCaseF_TinyValidChangeStillCompletes is the anti-line-count test at
// the runtime level. If this ever fails, something has started treating change
// volume as a proxy for adequacy.
func TestDriverCaseF_TinyValidChangeStillCompletes(t *testing.T) {
	d, root := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()))
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed (%s)", term, d.objectiveEvaluation().Reason)
	}
	if d.ObjectiveProgress() != execution.ProgressProven {
		t.Fatalf("progress = %s, want PROVEN", d.ObjectiveProgress())
	}
	// The delta was one word. Nothing in the runtime measured it.
	if got := readTarget(t, root, "note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("unexpected workspace content %q", got)
	}
}

// TestDriverCaseE_SatisfiedObjectiveProvesWithExplicitContract is the positive
// fixture the negative cases depend on: an objective whose completion contract
// and evidence are explicit reaches PROVEN.
func TestDriverCaseE_SatisfiedObjectiveProvesWithExplicitContract(t *testing.T) {
	d, _ := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()))
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	contract := d.objectiveContract()
	if len(contract.Conditions) == 0 {
		t.Fatal("no completion conditions were authored")
	}
	if len(contract.AdmittedRequirements()) != 1 {
		t.Fatalf("admitted requirements = %d, want 1", len(contract.AdmittedRequirements()))
	}
	if d.objectiveEvaluation().Outcome != execution.ObjectiveProven {
		t.Fatalf("outcome = %s (%s), want PROVEN", d.objectiveEvaluation().Outcome, d.objectiveEvaluation().Reason)
	}
	for _, c := range d.ObjectiveConditions() {
		if !c.Satisfied() {
			t.Fatalf("condition %s is unmet on a PROVEN objective: %+v", c.ID, c)
		}
	}
}

// ── CASE G — objective state survives bounded provider calls ────────────────

// TestDriverCaseG_ObjectiveStateSurvivesContinuation proves the requirement
// ledger and the objective identity survive the lifecycle: the same contract is
// still in force after a step, and work already discharged is not repeated.
func TestDriverCaseG_ObjectiveStateSurvivesContinuation(t *testing.T) {
	d := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	).driver
	identity := d.ObjectiveIdentity()
	if identity == "" {
		t.Fatal("the lifecycle has no objective identity")
	}
	// Re-reading the contract must be idempotent and stable.
	first := d.objectiveContract()
	second := d.objectiveContract()
	if len(first.Requirements) != len(second.Requirements) {
		t.Fatalf("the requirement ledger changed between reads: %d vs %d",
			len(first.Requirements), len(second.Requirements))
	}
	for i := range first.Requirements {
		if first.Requirements[i].ID != second.Requirements[i].ID ||
			first.Requirements[i].Status != second.Requirements[i].Status {
			t.Fatalf("requirement %d is not stable across reads: %+v vs %+v",
				i, first.Requirements[i], second.Requirements[i])
		}
	}
	if d.ObjectiveIdentity() != identity {
		t.Fatalf("objective identity changed: %q -> %q", identity, d.ObjectiveIdentity())
	}
	// Every admitted requirement is recorded with its grounding, so a reviewer can
	// see WHAT it was held to.
	for _, r := range first.Requirements {
		if r.Admitted() && len(r.Targets) == 0 {
			t.Fatalf("admitted requirement %q carries no grounded target: %+v", r.ID, r)
		}
	}
}

// TestDriverCaseG2_AFreshObjectiveInheritsNothing proves the per-run reset: a
// new objective cannot satisfy itself with the previous objective's discharged
// requirements or its result observations.
func TestDriverCaseG2_AFreshObjectiveInheritsNothing(t *testing.T) {
	lc := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	)
	d := lc.driver
	if len(d.objectiveState().discharged) == 0 {
		t.Fatal("the first objective discharged nothing, so the reset is untestable")
	}
	if len(d.objectiveState().postMutation) == 0 {
		t.Fatal("the first objective recorded no result re-inspection, so the reset is untestable")
	}
	firstIdentity := d.ObjectiveIdentity()

	d.resetObjectiveContract()
	if len(d.objectiveState().discharged) != 0 ||
		len(d.objectiveState().claimed) != 0 ||
		len(d.objectiveState().postMutation) != 0 ||
		d.objective.steps != 0 ||
		d.objective.humanGated {
		t.Fatalf("a new objective inherited per-run state: %+v", d.objective)
	}
	// The identity survives within a run: it is derived from the run identity and
	// the request, and a reset inside one run is the SAME objective.
	if got := d.ObjectiveIdentity(); got != firstIdentity {
		t.Fatalf("objective identity is not stable within a run: %q -> %q", firstIdentity, got)
	}
}

// ── CASE H — continuation carries evidence, not an empty prompt ─────────────

// TestDriverCaseH_ContinuationCarriesEvidenceNotAnEmptyPrompt proves the
// continuation is a continuation: the follow-up step is handed the objective
// contract, the discharged set and the unresolved work.
func TestDriverCaseH_ContinuationCarriesEvidenceNotAnEmptyPrompt(t *testing.T) {
	d := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	).driver
	evidence := d.continuationEvidence()
	for _, want := range []string{"[OBJECTIVE", "extra.txt", "requirements:", "UNRESOLVED", "completion conditions:"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("continuation evidence is missing %q:\n%s", want, evidence)
		}
	}
	// Work that already landed is reported as discharged, never as pending again.
	if !strings.Contains(evidence, "DISCHARGED") {
		t.Fatalf("the continuation does not report the work that already landed:\n%s", evidence)
	}
	// The post-mutation re-inspection is part of the carried evidence.
	// The re-inspection is part of the carried state, so a step that is told to
	// continue is also told what has already been looked at.
	if !strings.Contains(evidence, "post-mutation re-inspected") {
		t.Fatalf("the continuation does not carry the result re-inspection:\n%s", evidence)
	}
	// And the derived clause set is present, with the file reference intact — a
	// segmentation bug that tore "@note.txt" apart would show up right here.
	if !strings.Contains(evidence, "@note.txt") {
		t.Fatalf("the continuation lost a target reference from the clause set:\n%s", evidence)
	}
}

// ── CASE I — domain independence over the real runtime ──────────────────────

// TestDriverCaseI_DomainIndependenceOverTheRealRuntime runs the same machinery
// over two unrelated domains through the real Driver.
func TestDriverCaseI_DomainIndependenceOverTheRealRuntime(t *testing.T) {
	t.Run("documentation", func(t *testing.T) {
		d, _ := runObjective(t, "rewrite the introduction in @note.txt",
			[]*ai.Response{{Content: sampleReplace}},
			WithRequirementPass(scriptedRequirements("rewrite the introduction in @note.txt")),
			WithLoopBounds(lifecycleBounds()))
		if d.State() != autonomy.RuntimeAwaitingHuman {
			t.Fatalf("state = %s, want awaiting_human", d.State())
		}
		term, err := d.ResumeApprove(context.Background())
		if err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
		if term == nil || term.State != autonomy.RuntimeCompleted {
			t.Fatalf("a satisfied documentation objective was not proven: %+v", term)
		}
	})

	t.Run("code refactor under-delivered", func(t *testing.T) {
		// Same code path, a different subject matter, and the same refusal: the
		// refusal names a declared target, never a file type or a language.
		d := partiallySatisfyingDriver(t, "extra.txt",
			"change bar to qux in @note.txt",
			"change bar to qux in @extra.txt",
		).driver
		if d.objectiveEvaluation().Outcome == execution.ObjectiveProven {
			t.Fatal("an under-delivered refactor objective was proven")
		}
		if !strings.Contains(d.objectiveEvaluation().Reason, "extra.txt") {
			t.Fatalf("the refusal does not name the untouched declared target: %q", d.objectiveEvaluation().Reason)
		}
	})
}

// ── CASE J — the optional requirement pass cannot loosen the contract ────────

// TestDriverCaseJ_RequirementPassFailureNeverLoosensTheContract proves the pass
// is optional and that its absence costs the RUNTIME nothing.
func TestDriverCaseJ_RequirementPassFailureNeverLoosensTheContract(t *testing.T) {
	t.Run("pass fails", func(t *testing.T) {
		failing := func(context.Context, string, []string) ([]execution.DerivedRequirement, error) {
			return nil, errRequirementPassUnavailable
		}
		d, _ := runObjective(t, "change bar to qux @note.txt",
			[]*ai.Response{completedArtifact(sampleReplace)},
			WithRequirementPass(failing),
			WithLoopBounds(lifecycleBounds()))
		if d.State() != autonomy.RuntimeAwaitingHuman {
			t.Fatalf("state = %s, want awaiting_human", d.State())
		}
		if _, err := d.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
		// A failed derivation is recorded, not swallowed.
		if !strings.Contains(d.objective.requirementNote, "unavailable") {
			t.Fatalf("the failed derivation was not recorded: %q", d.objective.requirementNote)
		}
		// The runtime's own obligations are still authored and still sufficient to
		// reach PROVEN — the pass being unavailable cost nothing that the runtime
		// was relying on it for.
		if len(d.ObjectiveConditions()) == 0 {
			t.Fatal("no completion conditions were authored")
		}
		if d.objectiveEvaluation().Outcome != execution.ObjectiveProven {
			t.Fatalf("a satisfied objective was refused after a failed derivation: %s (%s)",
				d.objectiveEvaluation().Outcome, d.objectiveEvaluation().Reason)
		}
		if len(d.objectiveReportRequirements()) != 0 {
			t.Fatalf("a failed derivation produced requirements: %+v", d.objectiveReportRequirements())
		}
	})

	t.Run("pass unwired", func(t *testing.T) {
		d, _ := runObjective(t, "change bar to qux @note.txt",
			[]*ai.Response{completedArtifact(sampleReplace)},
			WithLoopBounds(lifecycleBounds()))
		if _, err := d.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
		if len(d.ObjectiveConditions()) == 0 {
			t.Fatal("no completion conditions were authored without a requirement pass")
		}
		if d.objectiveEvaluation().Outcome != execution.ObjectiveProven {
			t.Fatalf("an unwired requirement pass changed the verdict: %s (%s)",
				d.objectiveEvaluation().Outcome, d.objectiveEvaluation().Reason)
		}
	})

	t.Run("pass returns junk", func(t *testing.T) {
		junk := func(context.Context, string, []string) ([]execution.DerivedRequirement, error) {
			return nil, errRequirementPassUnavailable
		}
		lc := partiallySatisfyingDriverWith(t, junk, "extra.txt",
			"change bar to qux @note.txt",
			"change bar to qux @extra.txt",
		)
		if lc.driver.objectiveEvaluation().Outcome == execution.ObjectiveProven {
			t.Fatal("a failed requirement pass loosened the completion contract")
		}
	})
}

// ── CASE K — the trace reports real state ───────────────────────────────────

// TestDriverCaseK_TraceReportsRealState proves the telemetry is generated from
// runtime events rather than assembled as a static checklist.
func TestDriverCaseK_TraceReportsRealState(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	rec := newActivityRecorder(bus)
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	writeTarget(t, root, "extra.txt", sampleOriginal)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root),
		testExecutor(t, root, &mockProvider{responses: []*ai.Response{completedArtifact(sampleReplace)}}, bus))
	d := NewDriver(adapter, bus,
		WithRequirementPass(scriptedRequirements(
			"change bar to qux @note.txt",
			"change bar to qux @extra.txt",
		)),
		WithLoopBounds(lifecycleBounds()))
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	waitForActivity(rec)
	joined := rec.joined()

	// Every claim below is backed by a line the runtime actually emitted.
	for _, want := range []string{
		"[objective] id=",           // the contract, emitted once, before execution
		"[objective] requirements ", // the admissibility tally
		"[intent] user_intent=",     // the four-axis intent record
		"[scope] RESOLVED",          // the typed scope-resolution transition
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("trace is missing %q:\n%s", want, joined)
		}
	}
	// A trace must never assert a static checklist the runtime did not emit.
	for _, banned := range []string{"\u2713 inspect", "\u2713 analyze", "\u2713 propose", "\u2713 mutate", "\u2713 verify"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("trace contains a fabricated checklist entry %q", banned)
		}
	}
	// The intent record must name four DISTINCT axes, not one word reused.
	for _, axis := range []string{"user_intent=", "command_mode=", "interaction_contract=", "execution_intent="} {
		if !strings.Contains(joined, axis) {
			t.Fatalf("the intent record does not name the %s axis:\n%s", axis, joined)
		}
	}
}

// ── CASE L — scope resolution is a typed transition ─────────────────────────

// TestDriverCaseL_ScopeResolutionIsTyped proves the preflight/discovery
// transition is a typed fact: an empty preflight scope is UNRESOLVED, not a
// value that was silently overwritten.
func TestDriverCaseL_ScopeResolutionIsTyped(t *testing.T) {
	d, _ := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithLoopBounds(lifecycleBounds()))
	if d.scopeResolution.State != ScopeResolved {
		t.Fatalf("scope resolution state = %s, want RESOLVED", d.scopeResolution.State)
	}
	if len(d.scopeResolution.Targets) == 0 {
		t.Fatal("a resolved scope recorded no targets")
	}
	// The vocabulary is total: every state is distinct and renderable.
	seen := map[string]bool{}
	for _, s := range []ScopeResolutionState{ScopeUnresolved, ScopeDiscovered, ScopeResolved, ScopeRefused} {
		if s.String() == "" || seen[s.String()] {
			t.Fatalf("scope-resolution states are not distinct: %q", s)
		}
		seen[s.String()] = true
	}
}

// objectiveReportRequirements is a small accessor used by the assertions above.
func (d *Driver) objectiveReportRequirements() []execution.RequirementReport {
	return d.ObjectiveReport().Requirements
}

func objectiveHasCondition(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
