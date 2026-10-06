package autonomy

// ── R5 — NON-PROGRESS AND LOOP CONTROL: runtime-owned contract, proven ───────
//
// These tests pin the RUNTIME side of the progress contract traced in
// docs/report/R5_NON_PROGRESS_REPORT.md:
//
//	continuation eligibility  → the objective's own progress
//	                            (execution.DecideContinuation) + the recovery
//	                            matrix (DecideRecoveryWith)
//	stop / termination        → execution.ReduceProgress (evidence exhaustion)
//	                            + the RuntimeLoop bounds
//	stall detection           → the FailureLedger (identical failure under an
//	                            unchanged evidence epoch)
//
// The five R5 cases are exercised here at the control-plane seam and through
// the real Driver. The pure semantic proof of P0–P6 lives in
// internal/execution/r5_progress_test.go.

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

// ── shared fixtures ─────────────────────────────────────────────────────────

// r5ProgressRecorder collects the continuation decisions a real run emits.
type r5ProgressRecorder struct {
	mu      sync.Mutex
	evals   []events.ContinuationDecisionPayload
	selects []events.ContinuationDecisionPayload
}

func r5Record(bus *events.Bus) *r5ProgressRecorder {
	r := &r5ProgressRecorder{}
	bus.Subscribe(events.EventContinuationEvaluated, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ContinuationDecisionPayload); ok {
			r.mu.Lock()
			r.evals = append(r.evals, p)
			r.mu.Unlock()
		}
	})
	bus.Subscribe(events.EventContinuationSelected, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ContinuationDecisionPayload); ok {
			r.mu.Lock()
			r.selects = append(r.selects, p)
			r.mu.Unlock()
		}
	})
	return r
}

func (r *r5ProgressRecorder) snapshot() (evals, selects []events.ContinuationDecisionPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.ContinuationDecisionPayload(nil), r.evals...),
		append([]events.ContinuationDecisionPayload(nil), r.selects...)
}

// waitForDecisions lets the asynchronous bus deliver everything the run emitted.
func waitForDecisions(r *r5ProgressRecorder, min int) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		evals, _ := r.snapshot()
		if len(evals) >= min {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// r5ExecuteCycle runs one full observe→decide→execute→verify→interpret cycle on
// a RuntimeLoop, mirroring the internal/autonomy test helper.
func r5ExecuteCycle(t *testing.T, ctx context.Context, l *autonomy.RuntimeLoop, obs autonomy.Observation, d autonomy.LoopDecision) autonomy.RuntimeState {
	t.Helper()
	switch l.State() {
	case autonomy.RuntimeObserving:
		if got := l.Observe(obs); got != autonomy.RuntimeDeciding {
			t.Fatalf("observe = %s, want deciding", got)
		}
	case autonomy.RuntimeInterpreting, autonomy.RuntimeRecovering:
	default:
		t.Fatalf("unexpected loop state %s", l.State())
	}
	if got, err := l.Step(ctx, d); err != nil || got != autonomy.RuntimeExecuting {
		t.Fatalf("decide = %s, err=%v; want executing", got, err)
	}
	l.ConsumeExecution(obs)
	l.ConsumeVerification(obs)
	return l.State()
}

// r5PartialRouteDriver builds the same partial lifecycle the objective
// lifecycle tests use, but WITHOUT the human gate that stand-down the
// continuation router. It exists so the router's own transition can be tested
// in isolation.
func r5PartialRouteDriver(t *testing.T, req1, req2 string) *Driver {
	t.Helper()
	lc := partiallySatisfyingDriver(t, "extra.txt", req1, req2)
	d := lc.driver
	// A fresh objective lifecycle clears the human gate; re-derive the widened
	// contract and re-fold the real mutation evidence, exactly as the helper
	// does, so the state is genuinely PARTIALLY_SATISFIED and not gated.
	d.resetObjectiveContract()
	d.objective.proposals = scriptedProposalLedger(req1, req2)
	d.objective.requirementsDerived = true
	d.objectiveState()
	d.bindStepEvidence()
	return d
}

// ── CASE A — genuine progress keeps continuation allowed ────────────────────

func TestR5_CaseA_GenuineProgressIsContinuable(t *testing.T) {
	const req1 = "change bar to qux @note.txt"
	const req2 = "change bar to qux @extra.txt"

	d := r5PartialRouteDriver(t, req1, req2)

	// A real durable mutation landed on one of two declared targets. The
	// objective is genuinely PARTIALLY_SATISFIED — the one state that is
	// positive proof the approach works and simply has not gone far enough.
	if got := d.ObjectiveProgress(); got != execution.ProgressPartiallySatisfied {
		t.Fatalf("progress = %s, want PARTIALLY_SATISFIED", got)
	}
	if !d.ObjectiveContinuation().Continue() {
		t.Fatalf("continuation = %s, want CONTINUE/REPLAN", d.ObjectiveContinuation().Decision)
	}

	// The runtime-owned router must re-open the loop. This is the actual
	// continue/stop decision, not merely a projection.
	decision := autonomy.LoopDecision{Action: autonomy.LoopUnsubstantiate, Reason: "a refused completion claim"}
	d.routeObjectiveContinuation(&decision)
	if decision.Action != autonomy.LoopContinue && decision.Action != autonomy.LoopRepair {
		t.Fatalf("real progress produced %s, want CONTINUE/REPAIR", decision.Action)
	}
	if !strings.Contains(decision.Reason, "PARTIALLY_SATISFIED") {
		t.Fatalf("continuation reason does not name the progress state: %q", decision.Reason)
	}
	// The refusal was not silently converted into a completion.
	if decision.Action == autonomy.LoopComplete {
		t.Fatal("the router fabricated a completion")
	}
}

// TestR5_CaseA_EndToEndMutationReachesProven is the positive end-to-end
// control: a real two-target objective, a real provider, a real approval, and
// the objective reaches PROVEN — so the negative cases below are refusals, not
// a contract that can never be met.
func TestR5_CaseA_EndToEndMutationReachesProven(t *testing.T) {
	root, mock, adapter, bus := testHarness(t, []*ai.Response{
		completedArtifact(sampleReplace),
		completedArtifact(sampleReplace),
	})
	writeTarget(t, root, "extra.txt", sampleOriginal)
	rec := r5Record(bus)
	d := NewDriver(adapter, bus,
		WithRequirementPass(scriptedRequirements(
			"change bar to qux @note.txt",
			"change bar to qux @extra.txt",
		)),
		WithLoopBounds(lifecycleBounds()),
	)
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt @extra.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if d.ObjectiveProgress() != execution.ProgressProven {
		t.Fatalf("progress = %s, want PROVEN", d.ObjectiveProgress())
	}
	if mock.calls() == 0 {
		t.Fatal("the run completed with zero provider calls")
	}

	// The forensic record retains the progress classification and the attempt
	// transitions. This is what answers "what changed between attempt N and
	// N+1" without replaying the contract.
	evals, _ := rec.snapshot()
	if len(evals) == 0 {
		t.Fatal("no continuation.evaluated records were published")
	}
	var sawProven bool
	for _, e := range evals {
		if e.Progress == "" {
			t.Fatalf("continuation record %d carries no progress classification", e.Step)
		}
		if e.Progress == string(execution.ProgressProven) {
			sawProven = true
		}
	}
	if !sawProven {
		t.Fatalf("no continuation record reported PROVEN progress: %+v", evals)
	}
}

// ── CASE B — same output, same state, must stop ─────────────────────────────

func TestR5_CaseB_RepeatedIdenticalFailureStopsSemantically(t *testing.T) {
	ledger := newFailureLedger()
	// Two identical deterministic failures under the SAME evidence epoch: a
	// request whose answer cannot change.
	o := autonomy.Observation{
		RequestID: "run-1",
		Target:    "style.css",
		Outcome:   autonomy.OutcomeFailed,
		Diagnostic: "TARGET_NOT_FOUND [style.css]: style.css does not exist; the resolved " +
			"scope is [index.html,script.js,styles.css] and is unchanged",
	}
	in := RecoveryInput{
		Observation: o,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       []string{"index.html", "script.js", "styles.css"},
	}

	first := DecideRecoveryWith(in)
	if first.Action == recoveryUnsubstantiate {
		t.Fatalf("the FIRST occurrence stopped immediately (%s); a fresh failure earns one informed replan", first.Reason)
	}
	second := DecideRecoveryWith(in)
	if second.Action != recoveryUnsubstantiate {
		t.Fatalf("the SECOND identical occurrence = %s, want UNSUBSTANTIATED", second.Action)
	}
	if second.Failure.Class != execution.FailureNonProgressing {
		t.Fatalf("repeat class = %s, want NON_PROGRESSING_EXECUTION", second.Failure.Class)
	}
	if !strings.Contains(second.Reason, "NON_PROGRESSING_EXECUTION") {
		t.Fatalf("the stop is not attributed to non-progress: %s", second.Reason)
	}
}

func TestR5_CaseB_NoProgressIsBoundedByTheRuntimeLoop(t *testing.T) {
	ctx := context.Background()
	// The default fail-safe: three consecutive identical execution-bound
	// decisions abort the loop permanently.
	l := autonomy.NewRuntimeLoop(autonomy.DefaultLoopBounds())
	l.Start("objective")
	obs := autonomy.Observation{Outcome: autonomy.OutcomeArtifactProduced}

	// Two identical continues are consumed normally...
	r5ExecuteCycle(t, ctx, l, obs, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	r5ExecuteCycle(t, ctx, l, obs, autonomy.LoopDecision{Action: autonomy.LoopContinue})

	// ...and the third is refused before it can execute. The run cannot
	// continue indefinitely on identical decisions.
	state, err := l.Step(ctx, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if state != autonomy.RuntimeAborted {
		t.Fatalf("state = %s, want aborted", state)
	}
	term := l.Termination()
	if term == nil || !strings.Contains(term.Reason, "identical decisions") {
		t.Fatalf("termination = %+v, want the identical-decision bound", term)
	}
}

// ── CASE C — repeated no-op capability ──────────────────────────────────────
//
// Two independent owners answer this:
//
//	the objective contract → a no-op satisfies no completion condition, so it
//	                         can never prove a mutation objective
//	the progress detector  → repeated identical no-op snapshots are NO_PROGRESS
//
// The end-to-end runtime proof that a change-request DAG which applies zero
// bytes never completes already exists as
// TestPhase12_ModificationNoOpDAGNeverClaimsCompletion; it is referenced here
// rather than duplicated.

func TestR5_CaseC_NoOpCapabilitySatisfiesNoCondition(t *testing.T) {
	const target = "main.go"
	contract := execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: "r5-c-noop",
		Request:     "make main.go print hello",
		Kind:        execution.TaskPatch,
		Scope:       []string{target},
	})
	// A capability executed successfully, exit 0, and the workspace is
	// byte-identical: no artifact, no mutation, no observation.
	noop := execution.ObjectiveEvidence{
		Provider:         execution.ProviderDone,
		FinishReason:     "stop",
		Artifact:         execution.ArtifactNone,
		Mutation:         execution.FilesystemNone,
		MutatedFiles:     0,
		ResponseProduced: true,
		Conditions:       contract.Conditions,
	}
	in := execution.ProgressInput{
		Contract: contract, Conditions: contract.Conditions,
		Attempted: true, AttemptsUsed: 1, MaxAttempts: 3, MaxRecoveryCycles: 2,
	}
	if got := execution.ReduceProgressWith(in, noop); got == execution.ProgressProven {
		t.Fatal("a no-op capability reported PROVEN")
	}
	cont := execution.DecideContinuation(in, noop)
	if cont.Decision == execution.Complete {
		t.Fatal("a no-op capability completed the objective")
	}
	// Nothing holds, so this is not the partial-progress path either.
	if cont.Progress == execution.ProgressPartiallySatisfied {
		t.Fatal("a no-op capability reported PARTIALLY_SATISFIED")
	}
}

// ── CASE D — repeated mutation attempt ──────────────────────────────────────

func TestR5_CaseD_RepeatedMutationRequestCannotLoop(t *testing.T) {
	ledger := newFailureLedger()
	// The model requests the SAME patch twice. The first attempt is applied;
	// the second anchors on content that no longer exists (the file already
	// changed), so it is an invariant ANCHOR_NOT_FOUND failure.
	//
	// The runtime's answer has THREE layers, all proven here:
	//   1. the repeat is RECORDED and named (NON_PROGRESSING_EXECUTION);
	//   2. the anchor class's own attempt limit bounds the retry; and
	//   3. the disposition is terminal — never an unending identical re-issue.
	base := autonomy.Observation{
		RequestID: "run-1",
		Target:    "index.html",
		Outcome:   autonomy.OutcomeArtifactRejected,
		Diagnostic: "executor: hallucinated anchor — zero match: index.html: " +
			"SEARCH matches zero regions (match count 0)",
		Objective: execution.ObjectiveEvidence{
			Provider: execution.ProviderDone,
			Artifact: execution.ArtifactProduced,
		},
	}
	in := RecoveryInput{
		Observation: base,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       []string{"index.html"},
	}
	first := DecideRecoveryWith(in)
	if first.Failure.Class != execution.FailureAnchorNotFound {
		t.Fatalf("first class = %s, want ANCHOR_NOT_FOUND", first.Failure.Class)
	}
	if first.Action == recoveryFail || first.Action == recoveryUnsubstantiate {
		t.Fatalf("the FIRST anchor miss stopped immediately (%s); it earns one informed re-prompt", first.Reason)
	}

	// The second real dispatch carries AttemptNum=1 (the loop increments the
	// attempt counter between dispatches). The identical request under the same
	// evidence is now both a recorded repeat AND past the anchor attempt limit.
	second := base
	second.AttemptNum = 1
	in.Observation = second
	repeat := DecideRecoveryWith(in)
	if repeat.Failure.Count != 2 {
		t.Fatalf("repeat count = %d, want 2", repeat.Failure.Count)
	}
	if !strings.Contains(repeat.Reason, string(execution.FailureNonProgressing)) {
		t.Fatalf("the repeat is not attributed to non-progress: %s", repeat.Reason)
	}
	if repeat.Action != recoveryFail && repeat.Action != recoveryUnsubstantiate && repeat.Action != recoveryWaitForAuthorization {
		t.Fatalf("the repeated mutation was re-authorized as %s — it can loop", repeat.Action)
	}
}

// ── CASE E — progress followed by stall ─────────────────────────────────────

// TestR5_CaseE_ProgressThenStallIsBounded is the decisive case. It drives real
// evidence forward (which the ledger records as new evidence) and then
// demonstrates the two independent stop owners:
//
//	the ledger       → a failure repeated under UNCHANGED evidence stops
//	the runtime loop → identical execution-bound decisions are bounded
//
// The important truth it records: the ledger only clears a repeat when the
// EVIDENCE EPOCH moved, so progress earns exactly one legitimate re-attempt and
// a stall cannot keep earning more.
func TestR5_CaseE_ProgressThenStallIsBounded(t *testing.T) {
	ledger := newFailureLedger()
	o := autonomy.Observation{
		RequestID:  "run-1",
		Target:     "style.css",
		Outcome:    autonomy.OutcomeFailed,
		Diagnostic: "TARGET_NOT_FOUND [style.css]: missing",
	}
	in := RecoveryInput{
		Observation: o,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       []string{"styles.css"},
	}

	// attempt 1: fresh failure → informed replan (evidence-bearing).
	in.Bounds.MaxRecoveryCycles = 6
	first := DecideRecoveryWith(in)
	if first.Action == recoveryUnsubstantiate {
		t.Fatalf("attempt 1 stopped immediately: %s", first.Reason)
	}

	// A durable mutation lands elsewhere: the runtime observed new evidence, so
	// the epoch opens. This is REAL progress, and it legitimately clears the
	// repeat memory.
	ledger.AdvanceEvidence()
	afterProgress := DecideRecoveryWith(in)
	if afterProgress.Failure.Class == execution.FailureNonProgressing {
		t.Fatalf("new evidence did not clear the non-progress verdict: %s", afterProgress.Reason)
	}
	if afterProgress.Action == recoveryUnsubstantiate {
		t.Fatalf("a repetition after real progress was stopped as non-progress: %s", afterProgress.Reason)
	}

	// The next attempt changes nothing. The SAME failure under the SAME epoch
	// is now non-progressing, and the run terminates rather than looping.
	stalled := DecideRecoveryWith(in)
	if stalled.Failure.Class != execution.FailureNonProgressing {
		t.Fatalf("stall class = %s, want NON_PROGRESSING_EXECUTION", stalled.Failure.Class)
	}
	if stalled.Action != recoveryUnsubstantiate {
		t.Fatalf("a stalled repetition = %s, want UNSUBSTANTIATED", stalled.Action)
	}
}

// TestR5_CaseE_ActionCeilingIsNotProgressAware records the precise boundary of
// the existing contract. The RuntimeLoop's identical-decision ceiling bounds a
// stall, but it is a function of the ACTION, not of the objective's state: the
// same ceiling aborts a sequence regardless of whether each cycle advanced.
// This is a safety ceiling, not a semantic progress detector, and R5 reports it
// as such.
func TestR5_CaseE_ActionCeilingIsNotProgressAware(t *testing.T) {
	ctx := context.Background()
	// A generous semantic budget: the ONLY thing that can stop these loops is
	// the identical-decision ceiling.
	bounds := autonomy.LoopBounds{MaxAttempts: 100, MaxRecoveryCycles: 100, MaxExecutionSteps: 100, MaxIdenticalDecisions: 2, MaxTotalTokens: 100_000_000}

	// Sequence 1: a "stalled" run — every observation is identical.
	stalled := autonomy.NewRuntimeLoop(bounds)
	stalled.Start("stall")
	stallObs := autonomy.Observation{Outcome: autonomy.OutcomeArtifactProduced}
	r5ExecuteCycle(t, ctx, stalled, stallObs, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	r5ExecuteCycle(t, ctx, stalled, stallObs, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	stallState, _ := stalled.Step(ctx, autonomy.LoopDecision{Action: autonomy.LoopContinue})

	// Sequence 2: a "progressing" run — every observation is different
	// (mutation count and outcome advance). The loop never sees that: it sees
	// two identical CONTINUE actions and aborts exactly the same way.
	progressing := autonomy.NewRuntimeLoop(bounds)
	progressing.Start("progress")
	r5ExecuteCycle(t, ctx, progressing, autonomy.Observation{Outcome: autonomy.OutcomeCreated, TokenUsage: 1}, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	r5ExecuteCycle(t, ctx, progressing, autonomy.Observation{Outcome: autonomy.OutcomeChanged, TokenUsage: 2}, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	progressState, _ := progressing.Step(ctx, autonomy.LoopDecision{Action: autonomy.LoopContinue})

	if stallState != autonomy.RuntimeAborted || progressState != autonomy.RuntimeAborted {
		t.Fatalf("stall=%s progress=%s, want both aborted by the action ceiling", stallState, progressState)
	}
	// The ceiling names an ACTION loop, not a state stall. That is exactly the
	// distinction R5 reports: the ceiling bounds both, and therefore curtails a
	// legitimately progressing multi-step objective as well as a stalled one.
	if !strings.Contains(stalled.Termination().Reason, "identical decisions") {
		t.Fatalf("stall termination = %+v", stalled.Termination())
	}
	if !strings.Contains(progressing.Termination().Reason, "identical decisions") {
		t.Fatalf("progress termination = %+v", progressing.Termination())
	}
}

// ── the forensic contract ───────────────────────────────────────────────────

// TestR5_ForensicsRecordProgressAndTransitions proves the emitted continuation
// record carries the progress classification and per-attempt transition flags,
// so a reader can answer "what changed between attempt N and N+1?" from the
// event stream alone.
func TestR5_ForensicsRecordProgressAndTransitions(t *testing.T) {
	root, _, adapter, bus := testHarness(t, []*ai.Response{completedArtifact(sampleReplace)})
	rec := r5Record(bus)
	d := NewDriver(adapter, bus,
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()),
	)
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	_ = root
	waitForDecisions(rec, 2)

	evals, selects := rec.snapshot()
	if len(evals) == 0 || len(selects) == 0 {
		t.Fatalf("continuation evidence missing: %d evaluated, %d selected", len(evals), len(selects))
	}
	for i, e := range evals {
		t.Logf("R5 forensic decision #%d: progress=%s prev=%s outcome=%s proposed=%s selected=%s authorities=%v new_evidence=%t new_artifact=%t mutation_applied=%t verification_advanced=%t objective_advanced=%t",
			i+1, e.Progress, e.PreviousProgress, e.Outcome, e.ProposedAction, e.SelectedAction,
			e.Authorities, e.NewEvidence, e.NewArtifact, e.MutationApplied, e.VerificationAdvanced, e.ObjectiveAdvanced)
	}
	// Every evaluated record names a progress classification from the closed
	// vocabulary — not an empty string, not a model claim.
	known := map[string]bool{}
	for _, p := range []execution.ObjectiveProgress{
		execution.ProgressDiscovered, execution.ProgressUnderstood,
		execution.ProgressRequirementsDerived, execution.ProgressReady,
		execution.ProgressInProgress, execution.ProgressPartiallySatisfied,
		execution.ProgressRequiresContinuation, execution.ProgressBlocked,
		execution.ProgressProven, execution.ProgressFailed,
		execution.ProgressUnsubstantiated, execution.ProgressRequiresAuthorization,
	} {
		known[string(p)] = true
	}
	for i, e := range evals {
		if !known[e.Progress] {
			t.Fatalf("evaluated[%d].Progress = %q, not a progress classification", i, e.Progress)
		}
	}
	// At least one record must show the attempt that actually changed the
	// workspace: a previously-unsatisfied condition advanced.
	var sawAdvance bool
	for _, e := range evals {
		if e.MutationApplied || e.ObjectiveAdvanced || e.NewEvidence {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Fatalf("no continuation record reported an authoritative advance: %+v", evals)
	}
}
