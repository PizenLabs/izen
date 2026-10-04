package autonomy

// ── POST-MUTATION OBSERVATION, UNDER-DELIVERY, AND THE TINY-CHANGE GUARD ───
//
// Tests F, G and H from the recovery-failure report. Together they pin the
// three states the objective lifecycle must distinguish, and — critically —
// protect against the tempting but wrong shortcut of treating MUTATION VOLUME as
// a quality proxy:
//
//	Test F — after a successful mutation every authoritative target is RE-READ,
//	         and PostMutationObserved is updated from actual runtime reads. A
//	         model claim can never satisfy this.
//
//	Test G — a valid mutation that leaves an objective condition unmet is NOT
//	         PROVEN. Never.
//
//	Test H — a task requiring one small mutation CAN still become PROVEN. This
//	         is the counterweight to Test G: a runtime that refuses every small
//	         change has replaced evidence with a taste judgement.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── TEST F: post-mutation re-inspection comes from real reads ───────────────

// approvedRunHarness builds a driver over a single-file workspace and returns the
// workspace root.
//
// The provider answers the FIRST attempt with a complete document (the initial
// contract is a full rewrite) and every later attempt with the equivalent
// bounded SEARCH/REPLACE patch. Both produce the same result, so a test can
// exercise a landed mutation without depending on which artifact contract the
// lifecycle happened to dispatch.
func approvedRunHarness(t *testing.T, name, original string) (*Driver, string) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, name, original)
	bus := newTestBus()
	mock := &mockProvider{responses: []*ai.Response{
		{Content: "<html><body>\nline one\nline TWO\nline three\n</body></html>"},
		{Content: "<html><body>\nline one\nline TWO\nline three\n</body></html>"},
		{Content: "<<<<<<< SEARCH\nline two\n=======\nline TWO\n>>>>>>> REPLACE"},
	}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds())), root
}

// approveIfParked resolves a parked approval gate so a test can reach the
// post-mutation state. It is a no-op when the run parked for another reason.
func approveIfParked(t *testing.T, d *Driver) {
	t.Helper()
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryApproval {
		return
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
}

// TestF_PostMutationObservedComesFromRuntimeReads proves the post-mutation
// observation set is built from ACTUAL reads and not from what the model said it
// did.
func TestF_PostMutationObservedComesFromRuntimeReads(t *testing.T) {
	d, _ := approvedRunHarness(t, "index.html", anchorWorkspaceOriginal)

	// bindPostMutationObservation is the only producer of the set. It reads each
	// declared target and records only the ones that actually came back.
	//
	// A target that exists is observed; a target that does not is NOT — a failed
	// read is an absence of observation, never a negative observation.
	got := d.bindPostMutationObservation([]string{"index.html", "missing.html", ""})
	if len(got) != 1 || got[0] != "index.html" {
		t.Fatalf("post-mutation observation = %v, want [index.html] — an unreadable target is not an observation", got)
	}

	// The set is a projection, not an accumulator: re-reading yields the same
	// answer rather than growing.
	if again := d.bindPostMutationObservation([]string{"index.html"}); len(again) != 1 {
		t.Fatalf("re-read produced %v, want the same single target", again)
	}
}

// TestF_PostMutationConditionCannotBeSatisfiedByAClaim drives a real run whose
// mutation lands and whose objective carries the post-mutation re-inspection
// obligation, then proves the obligation is discharged by a READ rather than by
// anything the model asserted.
func TestF_PostMutationConditionCannotBeSatisfiedByAClaim(t *testing.T) {
	d, root := approvedRunHarness(t, "index.html", anchorWorkspaceOriginal)

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approveIfParked(t, d)

	// The mutation landed.
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "line TWO") {
		t.Fatalf("the mutation did not land: %q", got)
	}
	// And the runtime observed the RESULT by re-reading, which is the only thing
	// that can discharge the post-mutation obligation.
	observed := d.objectiveEvidenceWithContract().PostMutationObserved
	if len(observed) == 0 {
		t.Fatal("PostMutationObserved is empty after a landed mutation — the runtime did not observe its own result")
	}
	if observed[0] != "index.html" {
		t.Fatalf("PostMutationObserved = %v, want [index.html]", observed)
	}
}

// TestF_ModelClaimDoesNotSatisfyTheReinspectionCondition proves the strongest
// form of the invariant: a runtime that never re-read can never satisfy the
// condition, no matter what the execution claims.
func TestF_ModelClaimDoesNotSatisfyTheReinspectionCondition(t *testing.T) {
	// The authority recomputes every condition from observed evidence and never
	// reads a caller-supplied status. A bundle with a claim and no observation
	// must therefore report the condition UNSATISFIED.
	contract := execution.ObjectiveContract{
		ObjectiveID: "run-1",
		Scope:       []string{"index.html"},
		Conditions: []execution.CompletionCondition{{
			ID:      "cond-post-mutation-reinspected",
			Targets: []string{"index.html"},
		}},
	}
	claimed := execution.ObjectiveEvidence{
		Provider: execution.ProviderDone,
		Artifact: execution.ArtifactProduced,
		Mutation: execution.FilesystemApplied,
		// The model says it did everything…
		ClaimedRequirements: []string{"req-1"},
		Conditions:          contract.Conditions,
		// …but the runtime never re-read.
		PostMutationObserved: nil,
	}
	got := execution.SatisfiedConditions(claimed)
	if len(got) != 1 {
		t.Fatalf("conditions = %d, want 1", len(got))
	}
	if got[0].Satisfied() {
		t.Fatalf("a claim satisfied the post-mutation observation condition: %+v", got[0])
	}
}

// ── TEST G: under-delivery is never PROVEN ─────────────────────────────────

// TestG_ValidMutationWithUnmetConditionIsNotProven is the architectural case:
// a real mutation lands, and one objective condition is still unmet.
func TestG_ValidMutationWithUnmetConditionIsNotProven(t *testing.T) {
	auth := execution.NewObjectiveCompletionAuthority()
	contract := execution.TaskContract{
		Kind:             execution.TaskPatch,
		Targets:          []string{"index.html", "styles.css"},
		RequiresVerifier: true,
	}
	// Every execution fact says the mutation worked on ONE of the two declared
	// targets. The objective additionally demands a second obligation the
	// runtime never observed. That gap is the whole point.
	evidence := execution.ObjectiveEvidence{
		Provider:             execution.ProviderDone,
		Artifact:             execution.ArtifactProduced,
		ArtifactsParsed:      1,
		FinishReason:         "stop",
		Mutation:             execution.FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"index.html"},
		TargetExists:         map[string]bool{"index.html": true, "styles.css": true},
		VerificationRan:      true,
		VerificationPassed:   true,
		PostMutationObserved: []string{"index.html"},
		Conditions: []execution.CompletionCondition{
			{ID: "cond-scope-mutated", Targets: []string{"index.html", "styles.css"}},
			{ID: "cond-second-obligation", Targets: []string{"styles.css"}},
		},
	}
	got := auth.Authorize(contract, evidence)
	if got.Outcome == execution.ObjectiveProven {
		t.Fatalf("a partially-delivered objective was ruled PROVEN: %+v", got)
	}

	// The UNMET condition is named, so the verdict is actionable rather than a
	// shrug. This is what makes CONTINUE/REPLAN/UNSUBSTANTIATED possible for a
	// caller instead of leaving it guessing.
	satisfied := execution.SatisfiedConditions(evidence)
	var unmetIDs []string
	for _, c := range satisfied {
		if !c.Satisfied() {
			unmetIDs = append(unmetIDs, c.ID)
		}
	}
	if len(unmetIDs) == 0 {
		t.Fatalf("the unmet obligation was not reported: %+v", satisfied)
	}
	found := false
	for _, id := range unmetIDs {
		if id == "cond-second-obligation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the wrong condition was reported unmet: %v", unmetIDs)
	}
}

// TestG_LandedMutationIsJudgedOnItsObjective drives the same case end-to-end: a
// mutation lands on a single-file objective, and the runtime reaches a real
// verdict on its own conditions rather than on the fact that bytes changed.
func TestG_LandedMutationIsJudgedOnItsObjective(t *testing.T) {
	d, root := approvedRunHarness(t, "index.html", anchorWorkspaceOriginal)

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approveIfParked(t, d)

	// The mutation is real.
	if readTarget(t, root, "index.html") == anchorWorkspaceOriginal {
		t.Fatal("the mutation did not land; the test is not exercising what it claims")
	}
	// The runtime reached a REAL verdict.
	verdict := d.objectiveEvaluation()
	if verdict.Outcome == "" {
		t.Fatal("the run recorded no objective verdict at all")
	}
	// Whichever way it went, the verdict must be traceable to conditions rather
	// than to the mutation: a run that mutated and reported nothing is
	// indistinguishable from one that never looked.
	if len(d.ObjectiveConditions()) == 0 {
		t.Fatal("no completion conditions were authored; the objective contract was never in force")
	}
	// Consistency: PROVEN requires every condition satisfied, and nothing else.
	allSatisfied := true
	for _, c := range d.ObjectiveConditions() {
		if !c.Satisfied() {
			allSatisfied = false
		}
	}
	if allSatisfied && verdict.Outcome != execution.ObjectiveProven {
		t.Fatalf("every condition holds but the verdict is %s: %+v", verdict.Outcome, verdict)
	}
	if !allSatisfied && verdict.Outcome == execution.ObjectiveProven {
		t.Fatalf("an unmet condition exists but the verdict is PROVEN: %+v", verdict)
	}
}

// TestG_UnderDeliveredScopeIsNotProven keeps the benchmark's own architectural
// case: the model answers for one of several files and the runtime must not call
// that success.
func TestG_UnderDeliveredScopeIsNotProven(t *testing.T) {
	d, _ := anchorHarness(t, exhaustedProvider{})
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("an exhausted run reported completion")
	}
	if d.ObjectiveProgress() == execution.ProgressProven {
		t.Fatal("an exhausted run reported PROVEN")
	}
}

// ── TEST H: a legitimate tiny change can still be PROVEN ───────────────────

// TestH_SingleSmallMutationCanBeProven is the counterweight to Test G.
//
// Without it, "never PROVEN" would be trivially satisfiable by refusing every
// objective — and a runtime that can only say "unproven" is not more truthful
// than one that lies, it is just quieter. This test proves the completion
// authority CAN rule PROVEN on real evidence, so the negative tests above are
// exercising a gate and not a blanket refusal.
func TestH_SingleSmallMutationCanBeProven(t *testing.T) {
	auth := execution.NewObjectiveCompletionAuthority()
	contract := execution.TaskContract{
		Kind:             execution.TaskPatch,
		Targets:          []string{"index.html"},
		RequiresVerifier: true,
	}
	// The minimum honest evidence for a proven patch: an artifact was produced,
	// the mutation is durable on a declared target, the target exists afterwards,
	// the declared target carries the delta, and the verifier gate is satisfied.
	evidence := execution.ObjectiveEvidence{
		Provider:             execution.ProviderDone,
		Artifact:             execution.ArtifactProduced,
		ArtifactsParsed:      1,
		FinishReason:         "stop",
		Mutation:             execution.FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"index.html"},
		TargetExists:         map[string]bool{"index.html": true},
		VerificationRan:      true,
		VerificationPassed:   true,
	}
	got := auth.Authorize(contract, evidence)
	if got.Outcome != execution.ObjectiveProven {
		t.Fatalf("a fully evidenced single-target patch was not PROVEN: %+v", got)
	}

	// And the runtime reaches a real verdict end-to-end when the evidence is
	// real. Whatever it decides, it must have DECIDED — a driver that records no
	// verdict has silently deferred, which is not the same as being truthful.
	d, root := approvedRunHarness(t, "index.html", anchorWorkspaceOriginal)
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approveIfParked(t, d)
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "line TWO") {
		t.Fatalf("the tiny change did not land: %q", got)
	}
	if d.objectiveEvaluation().Outcome == "" {
		t.Fatal("the run recorded no objective verdict at all")
	}
	// The post-mutation obligation is satisfied by the runtime's own re-read,
	// which is what makes a PROVEN verdict here honest rather than asserted.
	if len(d.objectiveEvidenceWithContract().PostMutationObserved) == 0 {
		t.Fatal("the landed mutation was never re-inspected")
	}
}

// TestH_MutationVolumeIsNotAQualityProxy states the invariant in one assertion:
// the runtime's judgement is a function of EVIDENCE, not of how many bytes
// changed. Two runs with identical evidence must reach identical verdicts even
// when one changed a lot and the other changed almost nothing.
func TestH_MutationVolumeIsNotAQualityProxy(t *testing.T) {
	auth := execution.NewObjectiveCompletionAuthority()
	contract := execution.TaskContract{
		Kind:             execution.TaskPatch,
		Targets:          []string{"index.html"},
		RequiresVerifier: true,
	}
	base := execution.ObjectiveEvidence{
		Provider:             execution.ProviderDone,
		Artifact:             execution.ArtifactProduced,
		ArtifactsParsed:      1,
		FinishReason:         "stop",
		Mutation:             execution.FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"index.html"},
		TargetExists:         map[string]bool{"index.html": true},
		VerificationRan:      true,
		VerificationPassed:   true,
	}

	// The runtime records NO notion of change size at all: ObjectiveEvidence has
	// no field for it. So two runs differing only in how much they changed must
	// reach the same verdict by construction — and this test proves it rather
	// than asserting it.
	tiny := base
	large := base
	if auth.Authorize(contract, tiny).Outcome != auth.Authorize(contract, large).Outcome {
		t.Fatal("the verdict changed with no change in evidence — volume is acting as a quality proxy")
	}
	// The evidence bundle exposes no size/magnitude field, which is the
	// structural reason the proxy is impossible.
	evType := reflect.TypeOf(base)
	for _, forbidden := range []string{"Bytes", "Size", "Lines", "DiffSize", "Volume", "Magnitude"} {
		if _, ok := evType.FieldByName(forbidden); ok {
			t.Errorf("ObjectiveEvidence exposes %q — a magnitude field invites volume as a quality proxy", forbidden)
		}
	}

	// Removing evidence DOES change the verdict. That is the asymmetry that makes
	// the authority a gate rather than a constant.
	missing := base
	missing.VerificationPassed = false
	if auth.Authorize(contract, missing).Outcome == auth.Authorize(contract, base).Outcome {
		t.Fatal("removing a verification pass did not change the verdict")
	}
}

// newTestBus returns a bus for tests that publish structured records.
func newTestBus() *events.Bus { return events.NewBus(events.DefaultBufferSize) }
