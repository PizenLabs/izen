package autonomy

// ── R5.1 — CLOSE THE STALL-AFTER-PROGRESS BOUNDARY ──────────────────────────
//
// R5 proved that `routeObjectiveContinuation` re-opened the loop on the STATIC
// `PARTIALLY_SATISFIED` projection without comparing attempt N to attempt N−1.
// Because that projection is monotone (once one completion condition holds it
// keeps holding), a partial objective could re-open on the SAME authoritative
// state until the action-level `MaxIdenticalDecisions` ceiling stopped it.
//
// These tests pin the narrow correction: continuation after a partial attempt is
// admissible only when the objective's AUTHORITATIVE progress fingerprint
// advanced since the previous continuation evaluation. A repeated partial state
// keeps the existing typed non-success (`UNSUBSTANTIATED`); it is never
// converted into a completion, and the safety ceiling is untouched.
//
// The progress comparison lives in `authoritativeProgressFingerprint` and is a
// function of runtime-owned facts only: the recomputed condition set (status +
// verification state), the failure ledger's evidence epoch, and the durable
// re-inspection / requirement-discharge ledgers. No model text, token count,
// timestamp or attempt id participates.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// r5_1RefusedCompletion is the decision shape the completion authority leaves
// behind when it refuses a claim: the only shape the router is allowed to
// re-open.
func r5_1RefusedCompletion() autonomy.LoopDecision {
	return autonomy.LoopDecision{
		Action: autonomy.LoopUnsubstantiate,
		Reason: "objective UNPROVEN (UNSUBSTANTIATED): refused completion claim",
	}
}

// ── CASE A — progress then NEW progress continues ───────────────────────────

// TestR5_1_CaseA_ProgressThenProgressContinues proves the router distinguishes a
// genuinely advancing partial objective from a static one. Attempt N reaches
// PARTIALLY_SATISFIED; attempt N+1 advances the AUTHORITATIVE state (a durable
// delta on the other declared target discharges the remaining requirement) while
// the projection label stays PARTIALLY_SATISFIED. The router must re-open.
func TestR5_1_CaseA_ProgressThenProgressContinues(t *testing.T) {
	const req1 = "change bar to qux @note.txt"
	const req2 = "change bar to qux @extra.txt"

	d := r5PartialRouteDriver(t, req1, req2)

	// Attempt N: the first partial result has no predecessor, so it is always a
	// delta. It must be eligible to continue.
	first := r5_1RefusedCompletion()
	d.routeObjectiveContinuation(&first)
	if first.Action != autonomy.LoopContinue && first.Action != autonomy.LoopRepair {
		t.Fatalf("first partial attempt = %s, want CONTINUE/REPAIR", first.Action)
	}

	// Attempt N+1: a durable delta lands on the other declared target. That
	// discharges the remaining requirement and opens a new evidence epoch — an
	// authoritative advance — even though the projection stays
	// PARTIALLY_SATISFIED.
	d.obs.Objective.ObservedDeltaTargets = []string{"extra.txt"}
	d.obs.Objective.Mutation = execution.FilesystemApplied
	d.obs.Objective.MutatedFiles = 1
	d.bindStepEvidence()

	if got := d.ObjectiveProgress(); got != execution.ProgressPartiallySatisfied {
		t.Fatalf("progress after the second advance = %s, want PARTIALLY_SATISFIED", got)
	}

	second := r5_1RefusedCompletion()
	d.routeObjectiveContinuation(&second)
	if second.Action != autonomy.LoopContinue && second.Action != autonomy.LoopRepair {
		t.Fatalf("genuine progress was not continuable: %s (%s)", second.Action, second.Reason)
	}
	// Observability: the authoritative advance is recorded as a progress delta.
	if !d.forensics.progressDelta {
		t.Fatal("genuine progress was recorded with progress_delta=false")
	}
}

// ── CASE B — repeated partial state does not re-open ────────────────────────

// TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen is the exact defect. Two
// consecutive continuation evaluations reach the SAME authoritative partial
// state. The projection remains PARTIALLY_SATISFIED, but there is no progress
// delta, so the router must leave the existing typed non-success in place.
func TestR5_1_CaseB_IdenticalPartialStateDoesNotReopen(t *testing.T) {
	const req1 = "change bar to qux @note.txt"
	const req2 = "change bar to qux @extra.txt"

	d := r5PartialRouteDriver(t, req1, req2)

	// Attempt N: partial → continue (records the baseline fingerprint).
	first := r5_1RefusedCompletion()
	d.routeObjectiveContinuation(&first)
	if first.Action != autonomy.LoopContinue && first.Action != autonomy.LoopRepair {
		t.Fatalf("first partial attempt = %s, want CONTINUE/REPAIR", first.Action)
	}

	// Attempt N+1: nothing authoritative changed. The projection is still
	// PARTIALLY_SATISFIED because the first condition is monotone.
	if got := d.ObjectiveProgress(); got != execution.ProgressPartiallySatisfied {
		t.Fatalf("progress = %s, want PARTIALLY_SATISFIED (the projection must stay partial)", got)
	}
	if _, advanced := d.progressFingerprintAndDelta(); advanced {
		t.Fatal("the authoritative progress fingerprint advanced without any state change")
	}

	second := r5_1RefusedCompletion()
	d.routeObjectiveContinuation(&second)

	if second.Action != autonomy.LoopUnsubstantiate {
		t.Fatalf("identical partial state was re-opened as %s; it must stay UNSUBSTANTIATED", second.Action)
	}
	if second.Action == autonomy.LoopComplete {
		t.Fatal("the stall was silently converted into a completion")
	}
	if !strings.Contains(second.Reason, "progress delta") {
		t.Fatalf("the stop is not attributed to the missing progress delta: %q", second.Reason)
	}
	// Observability: the stall is recorded with progress_delta=false, so the
	// event stream shows WHY the router declined to re-open.
	if d.forensics.progressDelta {
		t.Fatal("the stall was recorded with progress_delta=true")
	}
}

// TestR5_1_ForensicsCarryProgressDelta proves the composite is emitted on the
// existing continuation event, not merely held in driver state: a real run's
// records carry the field and at least one authoritative advance reports it.
func TestR5_1_ForensicsCarryProgressDelta(t *testing.T) {
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

	evals, _ := rec.snapshot()
	if len(evals) == 0 {
		t.Fatal("no continuation records were published")
	}
	var sawDelta bool
	for _, e := range evals {
		if e.ProgressDelta {
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatalf("no continuation record carried progress_delta: %+v", evals)
	}
}

// TestR5_1_CaseB_RepeatedStallIsBoundedByTheExistingCeiling proves the new
// semantic check COMPLEMENTS the action ceiling rather than replacing it: even
// if a caller bypassed the router, the RuntimeLoop's identical-decision bound
// still stops the sequence. The ceiling is untouched.
func TestR5_1_CaseB_RepeatedStallIsBoundedByTheExistingCeiling(t *testing.T) {
	ctx := context.Background()
	l := autonomy.NewRuntimeLoop(autonomy.DefaultLoopBounds())
	l.Start("objective")
	obs := autonomy.Observation{Outcome: autonomy.OutcomeArtifactProduced}

	r5ExecuteCycle(t, ctx, l, obs, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	r5ExecuteCycle(t, ctx, l, obs, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	state, err := l.Step(ctx, autonomy.LoopDecision{Action: autonomy.LoopContinue})
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if state != autonomy.RuntimeAborted {
		t.Fatalf("state = %s, want aborted by the identical-decision ceiling", state)
	}
	if term := l.Termination(); term == nil || !strings.Contains(term.Reason, "identical decisions") {
		t.Fatalf("termination = %+v, want the identical-decision bound", term)
	}
}

// ── CASE C — PROVEN is unchanged ────────────────────────────────────────────

// TestR5_1_CaseC_ProvenObjectiveIsUntouched proves the correction cannot affect
// completion: a PROVEN objective is never PARTIALLY_SATISFIED, so the router
// leaves the decision exactly as the authority set it.
func TestR5_1_CaseC_ProvenObjectiveIsUntouched(t *testing.T) {
	d, _ := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{completedArtifact(sampleReplace)},
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()))
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if got := d.ObjectiveProgress(); got != execution.ProgressProven {
		t.Fatalf("progress = %s, want PROVEN", got)
	}
	decision := r5_1RefusedCompletion()
	d.routeObjectiveContinuation(&decision)
	if decision.Action != autonomy.LoopUnsubstantiate {
		t.Fatalf("a PROVEN objective was routed as %s", decision.Action)
	}
}
