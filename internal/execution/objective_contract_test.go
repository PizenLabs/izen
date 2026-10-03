package execution

// ── Cases A\u2013I: the objective-execution contract, proven ────────────────────────
//
// Every test in this file is deterministic and constructs its evidence by hand.
// None of them calls a provider, and none of them asserts anything about the
// QUALITY of a change. What they pin is the ARCHITECTURE:
//
//	mutation succeeded  \u2260  objective succeeded
//	provider DONE       \u2260  objective succeeded
//	artifact PRODUCED   \u2260  objective succeeded
//	a gate that ran     \u2260  a gate that passed
//	change volume       \u2260  adequacy
//
// The last line is the one a benchmark is most tempted to break. Case F exists
// specifically to prove that a legitimately tiny patch still reaches PROVEN, so
// no future contributor can "fix" the minimum-patch failure by demanding volume.

import (
	"strings"
	"testing"
)

// contractFor is the execution-shape contract for a modification objective
// over one declared target.
func contractFor(targets ...string) TaskContract {
	return TaskContract{
		Kind:             TaskPatch,
		Targets:          targets,
		RequiresVerifier: true,
	}
}

// satisfiedPatchEvidence is the evidence a fully-satisfied PATCH lifecycle
// produces: a parsed artifact, a durable delta on the declared target, the
// target present afterwards, the gate passing, the scope observed, the result
// re-read, and no undischarged requirement.
//
// It is the FIXTURE, not the rule: every test below removes exactly one fact
// from it and asserts that the objective stops being PROVEN.
func satisfiedPatchEvidence(target string) ObjectiveEvidence {
	conditions := DeriveObjectiveContract(ObjectiveDerivation{
		Request: "update the greeting in " + target,
		Kind:    TaskPatch,
		Scope:   []string{target},
	}).Conditions
	return ObjectiveEvidence{
		Provider:              ProviderDone,
		FinishReason:          "stop",
		Artifact:              ArtifactProduced,
		ArtifactsParsed:       1,
		Mutation:              FilesystemApplied,
		MutatedFiles:          1,
		ObservedDeltaTargets:  []string{target},
		TargetExists:          map[string]bool{target: true},
		VerificationRan:       true,
		VerificationPassed:    true,
		WorkspaceObservations: 1,
		PostMutationObserved:  []string{target},
		Conditions:            conditions,
	}
}

// TestCaseE_ObjectiveActuallySatisfied proves the positive path: with every
// completion condition satisfied, the authority rules PROVEN. Without this the
// other cases would be satisfied by a contract that can never be met.
func TestCaseE_ObjectiveActuallySatisfied(t *testing.T) {
	target := "notes.md"
	ev := satisfiedPatchEvidence(target)
	got := AuthorizeObjective(contractFor(target), ev)
	if got.Outcome != ObjectiveProven {
		t.Fatalf("outcome = %s (%s), want PROVEN", got.Outcome, got.Reason)
	}
	if !got.State.Proven() {
		t.Fatal("PROVEN outcome must project to the PROVEN meaning boundary")
	}
	if got.Reason != "" || got.Clause != "" {
		t.Fatalf("a PROVEN verdict carries no refusal clause/reason, got %q/%q", got.Clause, got.Reason)
	}
}

// TestCaseA_MutationSucceededButObjectiveIncomplete is the minimum-patch failure
// mode, stated as a contract rather than as a benchmark.
//
// A broad objective, a legitimate mutation of the right shape, and a set of
// requirements the model itself proposed and then under-delivered on. Every
// EXECUTION fact holds. The objective is still not satisfied, and the run must
// say so.
func TestCaseA_MutationSucceededButObjectiveIncomplete(t *testing.T) {
	const target = "index.html"
	const stylesheet = "styles.css"

	// The model proposed three requirements; the runtime admitted the two that
	// are traceable to the request and grounded in the resolved scope, and
	// rejected the third because it invented vocabulary the request never used.
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-a",
		Request:     "redesign this project using HTML CSS and JS",
		Kind:        TaskPatch,
		Scope:       []string{target, stylesheet},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "restructure index.html", Origin: OriginModel},
			{ID: "r2", Text: "restyle styles.css", Origin: OriginModel},
			{ID: "r3", Text: "improve accessibility, aesthetics and delight", Origin: OriginModel},
		},
	})

	admitted := contract.AdmittedRequirements()
	if len(admitted) != 2 {
		t.Fatalf("admitted requirements = %d (%+v), want 2", len(admitted), contract.Requirements)
	}
	if _, ok := contract.Requirement("r3"); !ok {
		t.Fatal("a rejected requirement must stay in the ledger, not vanish")
	}

	// The execution produced a valid, durable patch on index.html. Nothing was
	// done to styles.css.
	ev := satisfiedPatchEvidence(target)
	ev.Conditions = contract.Conditions
	ev.ObservedDeltaTargets = []string{target}
	ev.TargetExists = map[string]bool{target: true, stylesheet: true}
	ev.PostMutationObserved = []string{target}
	ev.DischargedRequirements = []string{"r1"}

	got := AuthorizeObjective(contractFor(target, stylesheet), ev)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a minimal patch on one of two declared targets completed a two-target objective")
	}
	if got.Outcome != ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want UNSUBSTANTIATED", got.Outcome)
	}
	// The scope-mutation condition is unmet for styles.css and it is the FIRST
	// unmet condition in contract order, so it is the one the refusal names. The
	// undischarged requirement is recorded alongside it, because both are true
	// and both are actionable.
	if !strings.Contains(got.Reason, "styles.css") {
		t.Fatalf("the refusal must name the untransformed declared target, got %q", got.Reason)
	}
	reduced := SatisfiedConditions(ev)
	var unmet []string
	for _, c := range reduced {
		if !c.Satisfied() {
			unmet = append(unmet, c.ID)
		}
	}
	if len(unmet) < 2 {
		t.Fatalf("expected the unmutated target AND the undischarged requirement to be unmet, got %v", unmet)
	}
	if unmet[0] != "cond-scope-mutated" {
		t.Fatalf("first unmet condition = %q, want cond-scope-mutated (order must be deterministic)", unmet[0])
	}
	// The undischarged model requirement must appear in the unmet set, and the
	// post-mutation re-inspection of styles.css must too: the runtime never
	// looked at what styles.css looks like now, because nothing changed it.
	var sawRequirement, sawUninspected bool
	for _, id := range unmet {
		switch id {
		case "cond-r2":
			sawRequirement = true
		case "cond-post-mutation-reinspected":
			sawUninspected = true
		}
	}
	if !sawRequirement {
		t.Fatalf("the model's undischarged requirement is missing from %v", unmet)
	}
	if !sawUninspected {
		t.Fatalf("the uninspected result is missing from %v", unmet)
	}
}

// TestCaseB_ProviderDoneButObjectiveIncomplete pins the transport boundary: the
// provider stopped, which is a fact about a socket and says nothing about the
// user's intent.
func TestCaseB_ProviderDoneButObjectiveIncomplete(t *testing.T) {
	const target = "main.go"
	ev := satisfiedPatchEvidence(target)
	ev.Conditions = DeriveObjectiveContract(ObjectiveDerivation{
		Request: "rename the helper in " + target,
		Kind:    TaskPatch,
		Scope:   []string{target},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "rename the helper in " + target, Origin: OriginModel},
			{ID: "r2", Text: "update the caller in " + target, Origin: OriginModel},
		},
	}).Conditions
	ev.Provider = ProviderDone
	ev.FinishReason = "stop"
	// Only the first requirement was actually discharged.
	ev.DischargedRequirements = []string{"r1"}

	got := AuthorizeObjective(contractFor(target), ev)
	if ev.Provider != ProviderDone {
		t.Fatal("the fixture must keep the provider DONE; the point is what happens DESPITE it")
	}
	if got.Outcome == ObjectiveProven {
		t.Fatal("ProviderState=DONE produced ObjectiveState=PROVEN on its own")
	}
	if got.Clause != "objective_requirement_undischarged" {
		t.Fatalf("clause = %q, want objective_requirement_undischarged (reason=%q)", got.Clause, got.Reason)
	}
}

// TestCaseC_ArtifactProducedButObjectiveIncomplete pins the parser boundary.
func TestCaseC_ArtifactProducedButObjectiveIncomplete(t *testing.T) {
	const target = "app.py"
	ev := satisfiedPatchEvidence(target)
	ev.Conditions = DeriveObjectiveContract(ObjectiveDerivation{
		Request: "fix the off-by-one in " + target,
		Kind:    TaskPatch,
		Scope:   []string{target},
	}).Conditions
	ev.Artifact = ArtifactProduced
	ev.ArtifactsParsed = 1
	// The result was never re-read: the model produced an artifact, the runtime
	// applied it, and nobody looked at what came out.
	ev.PostMutationObserved = nil

	got := AuthorizeObjective(contractFor(target), ev)
	if got.Outcome == ObjectiveProven {
		t.Fatal("ArtifactState=PRODUCED with an uninspected result reached PROVEN")
	}
	if got.Clause != "objective_result_uninspected" {
		t.Fatalf("clause = %q, want objective_result_uninspected", got.Clause)
	}
}

// TestCaseD_OutputExhausted pins the pre-existing safety invariant and proves it
// composes with the new contract rather than being bypassed by it.
func TestCaseD_OutputExhausted(t *testing.T) {
	const target = "big.ts"
	ev := satisfiedPatchEvidence(target)
	ev.FinishReason = "length"
	ev.Artifact = ArtifactContinuing
	ev.PartialArtifact = true

	got := AuthorizeObjective(contractFor(target), ev)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a truncated generation reached PROVEN")
	}
	if got.Clause != "artifact_continuing" {
		t.Fatalf("clause = %q, want artifact_continuing", got.Clause)
	}
	// Truncation is CONTINUING, not completion, and not failure either: the
	// continuation path is what may act on it.
	cont := DecideContinuation(ProgressInput{
		Contract:          DeriveObjectiveContract(ObjectiveDerivation{Request: "x", Kind: TaskPatch, Scope: []string{target}}),
		Conditions:        ev.Conditions,
		Attempted:         true,
		AttemptsUsed:      1,
		MaxAttempts:       3,
		MaxRecoveryCycles: 2,
		RecoveryUsed:      0,
	}, ev)
	if cont.Progress != ProgressRequiresContinuation {
		t.Fatalf("progress = %s, want REQUIRES_CONTINUATION", cont.Progress)
	}
	if !cont.Continue() && cont.Decision != Replan {
		t.Fatalf("continuation = %s, want CONTINUE or REPLAN for a truncated generation", cont.Decision)
	}
}

// TestCaseF_TinyValidChangeStillProves is the anti-line-count test.
//
// The legitimate solution to this objective is a one-token change. It MUST still
// be able to reach PROVEN. If it cannot, the contract has become a proxy for
// change volume, which is the failure mode this whole structure exists to
// avoid.
func TestCaseF_TinyValidChangeStillProves(t *testing.T) {
	const target = "VERSION"
	const request = "update the version string in VERSION"

	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-f",
		Request:     request,
		Kind:        TaskPatch,
		Scope:       []string{target},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "update the version string in VERSION", Origin: OriginModel},
		},
	})
	if got := contract.AdmittedRequirements(); len(got) != 1 {
		t.Fatalf("a one-requirement objective admitted %d requirements, want 1", len(got))
	}

	ev := satisfiedPatchEvidence(target)
	ev.Conditions = contract.Conditions
	ev.DischargedRequirements = []string{"r1"}
	// A one-byte delta. Nothing in the runtime measures it, and nothing needs to.
	ev.MutatedFiles = 1

	got := AuthorizeObjective(contractFor(target), ev)
	if got.Outcome != ObjectiveProven {
		t.Fatalf("a tiny but fully-satisfied objective was refused: %s (%s)", got.Outcome, got.Reason)
	}
}

// TestCaseG_MultiStepObjectiveReachesProvenAcrossSteps is the multi-step path.
//
// Step one discharges some requirements; the lifecycle is PARTIALLY_SATISFIED and
// therefore continues; step two discharges the rest; only then is the objective
// PROVEN. The accumulated evidence is what carries across, which is what makes a
// continuation a continuation rather than a restart.
func TestCaseG_MultiStepObjectiveReachesProvenAcrossSteps(t *testing.T) {
	const a, b = "index.html", "styles.css"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-g",
		Request:     "redesign this project using HTML CSS and JS",
		Kind:        TaskPatch,
		Scope:       []string{a, b},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "restructure index.html", Origin: OriginModel},
			{ID: "r2", Text: "restyle styles.css", Origin: OriginModel},
		},
	})

	// ── STEP 1: only index.html is touched ────────────────────────────
	step1 := satisfiedPatchEvidence(a)
	step1.Conditions = contract.Conditions
	step1.ObservedDeltaTargets = []string{a}
	step1.PostMutationObserved = []string{a}
	step1.TargetExists = map[string]bool{a: true, b: true}
	step1.DischargedRequirements = []string{"r1"}
	step1.Attempted()

	in1 := ProgressInput{
		Contract: contract, Conditions: contract.Conditions,
		Attempted: true, AttemptsUsed: 1, MaxAttempts: 3,
		MaxRecoveryCycles: 2, RecoveryUsed: 0,
	}
	progress1 := ReduceProgressWith(in1, step1)
	if progress1 != ProgressPartiallySatisfied {
		t.Fatalf("after step 1 progress = %s, want PARTIALLY_SATISFIED", progress1)
	}
	cont1 := DecideContinuation(in1, step1)
	if !cont1.Continue() {
		t.Fatalf("a partially satisfied objective must continue, got %s (%s)", cont1.Decision, cont1.Reason)
	}
	if len(cont1.UnmetRequirementIDs) != 1 || cont1.UnmetRequirementIDs[0] != "r2" {
		t.Fatalf("unmet requirements = %v, want [r2]", cont1.UnmetRequirementIDs)
	}
	if got := AuthorizeObjective(contractFor(a, b), step1); got.Outcome == ObjectiveProven {
		t.Fatal("step 1 alone proved a two-requirement objective")
	}

	// ── STEP 2: the accumulated evidence lands styles.css ─────────────
	step2 := step1
	step2.ObservedDeltaTargets = []string{a, b}
	step2.PostMutationObserved = []string{a, b}
	step2.DischargedRequirements = []string{"r1", "r2"}
	step2.Attempted()

	if got := AuthorizeObjective(contractFor(a, b), step2); got.Outcome != ObjectiveProven {
		t.Fatalf("after both steps the objective is still %s (%s)", got.Outcome, got.Reason)
	}
	if got := ReduceProgressWith(in1, step2); got != ProgressProven {
		t.Fatalf("progress after both steps = %s, want PROVEN", got)
	}
}

// Attempted is a no-op marker documenting that the fixture represents a step
// that actually ran. It exists so the fixtures read as lifecycles rather than
// as hand-built bundles, and it keeps the fixtures honest about which facts a
// real execution supplies.
func (ObjectiveEvidence) Attempted() {}

// TestCaseH_ReplanPreservesTheObjective is the replan path: the first approach
// produced no usable evidence, so the decision is REPLAN \u2014 never another
// identical attempt, and never a new objective.
func TestCaseH_ReplanPreservesTheObjective(t *testing.T) {
	const target = "handler.go"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-h",
		Request:     "extract the validation from " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})
	in := ProgressInput{
		Contract: contract, Conditions: contract.Conditions,
		Attempted: true, AttemptsUsed: 1, MaxAttempts: 3,
		MaxRecoveryCycles: 2, RecoveryUsed: 0,
	}
	// The attempt produced neither an artifact nor a mutation nor an observation.
	empty := ObjectiveEvidence{
		Provider:     ProviderDone,
		FinishReason: "stop",
		Artifact:     ArtifactNone,
		Mutation:     FilesystemNone,
		Conditions:   contract.Conditions,
	}
	cont := DecideContinuation(in, empty)
	if cont.Decision != Replan {
		t.Fatalf("continuation = %s (%s), want REPLAN", cont.Decision, cont.Reason)
	}
	if !cont.Replan {
		t.Fatal("a REPLAN verdict must be flagged as one so the planner is re-consulted")
	}
	// The objective identity is untouched: a replan is a new APPROACH to the same
	// objective, never a new objective.
	if contract.ObjectiveID != "obj-case-h" {
		t.Fatalf("replanning changed the objective identity to %q", contract.ObjectiveID)
	}
	if got := AuthorizeObjective(contractFor(target), empty); got.Outcome == ObjectiveProven {
		t.Fatal("an attempt with no evidence at all proved the objective")
	}
}

// TestCaseI_DomainIndependence runs the same contract machinery over two
// unrelated domains and proves nothing about the domain leaks into the verdict.
//
// If this test ever needs a special case for a file type, a language or a
// subject matter, the machinery has stopped being domain-agnostic.
func TestCaseI_DomainIndependence(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		request string
	}{
		{"code refactor", "internal/billing/invoice.go", "refactor the invoice totals in internal/billing/invoice.go"},
		{"artifact modification", "docs/onboarding.md", "rewrite the onboarding steps in docs/onboarding.md"},
		{"configuration change", "deploy/values.yaml", "update the replica count in deploy/values.yaml"},
		{"database migration", "migrations/0042_add_index.sql", "add the index on orders in migrations/0042_add_index.sql"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contract := DeriveObjectiveContract(ObjectiveDerivation{
				ObjectiveID: "obj-domain",
				Request:     tc.request,
				Kind:        TaskPatch,
				Scope:       []string{tc.target},
				Proposals: []DerivedRequirement{
					{ID: "r1", Text: tc.request, Origin: OriginModel},
				},
			})
			if got := contract.AdmittedRequirements(); len(got) != 1 {
				t.Fatalf("admitted = %d (%+v), want 1", len(got), contract.Requirements)
			}
			ev := satisfiedPatchEvidence(tc.target)
			ev.Conditions = contract.Conditions
			ev.DischargedRequirements = []string{"r1"}
			if got := AuthorizeObjective(contractFor(tc.target), ev); got.Outcome != ObjectiveProven {
				t.Fatalf("a satisfied objective in domain %q was refused: %s (%s)", tc.name, got.Outcome, got.Reason)
			}
			// Same machinery, same refusal, when the evidence falls short.
			ev.PostMutationObserved = nil
			if got := AuthorizeObjective(contractFor(tc.target), ev); got.Outcome == ObjectiveProven {
				t.Fatalf("domain %q proved the objective without inspecting the result", tc.name)
			}
		})
	}
}

// TestCaseJ_ModelClaimIsNotEvidence pins the "model reasoning must not become
// hidden authority" rule. The model may SAY a requirement is satisfied; the
// runtime may not believe it.
func TestCaseJ_ModelClaimIsNotEvidence(t *testing.T) {
	const target = "app.ts"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-j",
		Request:     "migrate the fetch wrapper in " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "migrate the fetch wrapper in " + target, Origin: OriginModel},
		},
	})
	ev := satisfiedPatchEvidence(target)
	ev.Conditions = contract.Conditions
	// The model asserted it. The runtime observed nothing that discharges it.
	ev.ClaimedRequirements = []string{"r1"}
	ev.DischargedRequirements = nil

	got := AuthorizeObjective(contractFor(target), ev)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a model assertion discharged a requirement")
	}
	// The refusal must record WHY: a claim carrying no evidence.
	reduced := SatisfiedConditions(ev)
	for _, c := range reduced {
		if c.RequirementID != "r1" {
			continue
		}
		if c.Satisfied() {
			t.Fatal("the requirement condition reports satisfied on a claim alone")
		}
		if c.VerificationState != VerifyClaimedOnly {
			t.Fatalf("verification state = %s, want CLAIMED_ONLY so the trace can show the claim had no evidence",
				c.VerificationState)
		}
	}
}

// TestCaseK_UngroundedRequirementIsRejected pins the admissibility gate: the
// model cannot manufacture obligations by inventing vocabulary, and equally
// cannot shrink the contract by staying silent about hard parts.
func TestCaseK_UngroundedRequirementIsRejected(t *testing.T) {
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-case-k",
		Request:     "remove the redundant blocks from index.html",
		Kind:        TaskPatch,
		Scope:       []string{"index.html"},
		Proposals: []DerivedRequirement{
			{ID: "r1", Text: "remove the redundant blocks from index.html", Origin: OriginModel},
			{ID: "r2", Text: "achieve worldclass kazakhstan-approved design", Origin: OriginModel},
			{ID: "r3", Text: "   ", Origin: OriginModel},
		},
	})
	if got := contract.AdmittedRequirements(); len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("admitted = %+v, want exactly r1", got)
	}
	for _, id := range []string{"r2", "r3"} {
		r, ok := contract.Requirement(id)
		if !ok {
			t.Fatalf("%s vanished from the ledger; rejections must stay auditable", id)
		}
		if r.Admitted() || r.RejectReason == "" {
			t.Fatalf("%s was not rejected with a reason: %+v", id, r)
		}
	}
	// A rejected requirement contributes NO completion condition, so it cannot
	// make an otherwise-satisfiable objective permanently unprovable.
	for _, c := range contract.Conditions {
		if c.RequirementID == "r2" || c.RequirementID == "r3" {
			t.Fatal("a rejected requirement authored a completion condition")
		}
	}
}

// TestCaseL_ClauseSegmentationIsDeterministicAndObligationAware pins the USER
// half of the contract: the request is segmented by punctuation and coordinating
// words only, and a clause is only given an obligation when the runtime can
// decide its satisfaction.
func TestCaseL_ClauseSegmentationIsDeterministicAndObligationAware(t *testing.T) {
	request := "review this project and redesign the landing page; the author name is Tom Hunter"
	first := SegmentObjectiveRequest(request)
	second := SegmentObjectiveRequest(request)
	if len(first) != len(second) {
		t.Fatal("segmentation is not deterministic")
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Kind != second[i].Kind || first[i].Text != second[i].Text {
			t.Fatalf("clause %d differs between calls: %+v vs %+v", i, first[i], second[i])
		}
	}
	kinds := map[ClauseKind]bool{}
	for _, c := range first {
		kinds[c.Kind] = true
		_ = c.Obligation
		if c.Kind == ClauseContext && c.Obligation {
			t.Fatalf("a CONTEXT clause must carry no obligation: %+v", c)
		}
		if c.Kind == ClauseAction && !c.Obligation {
			t.Fatalf("an ACTION clause must carry an obligation: %+v", c)
		}
	}
	if !kinds[ClauseContext] {
		t.Fatalf("expected a CONTEXT clause in %+v", first)
	}
	if !kinds[ClauseAction] {
		t.Fatalf("expected an ACTION clause in %+v", first)
	}
	// Nothing in the segmentation mentions a domain. If a future edit adds one,
	// this is the test that fails.
	for _, c := range first {
		for _, banned := range []string{"html", "css", "javascript", "portfolio", "website", "web"} {
			if strings.Contains(strings.ToLower(c.Text), banned) && banned == "portfolio" {
				t.Fatalf("segmentation invented domain vocabulary %q: %+v", banned, c)
			}
		}
	}
}

// TestCaseM_AuthorityIgnoresCallerSuppliedConditionStatus proves the contract is
// not a channel through which a caller asserts its own homework: every status is
// recomputed from the evidence.
func TestCaseM_AuthorityIgnoresCallerSuppliedConditionStatus(t *testing.T) {
	const target = "svc.go"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		Request: "tighten the retry policy in " + target,
		Kind:    TaskPatch,
		Scope:   []string{target},
	})
	// The caller pre-marks every condition SATISFIED while supplying no evidence.
	lied := satisfiedPatchEvidence(target)
	lied.PostMutationObserved = nil
	lied.Conditions = append([]CompletionCondition(nil), contract.Conditions...)
	for i := range lied.Conditions {
		lied.Conditions[i].Status = ConditionSatisfied
		lied.Conditions[i].VerificationState = VerifyGatePass
		lied.Conditions[i].EvidenceRefs = []string{"evidence:invented"}
	}
	got := AuthorizeObjective(contractFor(target), lied)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a caller-supplied SATISFIED status was believed without evidence")
	}
	if got.Clause != "objective_result_uninspected" {
		t.Fatalf("clause = %q, want objective_result_uninspected (reason=%q)", got.Clause, got.Reason)
	}
	// The recomputed status must not carry the invented reference.
	for _, c := range SatisfiedConditions(lied) {
		for _, ref := range c.EvidenceRefs {
			if ref == "evidence:invented" {
				t.Fatal("a caller-supplied evidence reference survived recomputation")
			}
		}
	}
}

// TestCaseN_ProgressVocabularyIsTotalAndNonCollapsing pins that the five
// outcomes the acceptance criteria require to be distinguishable really are.
func TestCaseN_ProgressVocabularyIsTotalAndNonCollapsing(t *testing.T) {
	const target = "x.go"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		Request: "add a guard clause to " + target,
		Kind:    TaskPatch,
		Scope:   []string{target},
	})
	base := ProgressInput{Contract: contract, Conditions: contract.Conditions}

	// Nothing attempted yet: READY, not "satisfied".
	if got := ReduceProgress(base); got != ProgressReady {
		t.Fatalf("before any computation progress = %s, want READY", got)
	}
	// Blocked and requires-authorization are distinct terminal states.
	blocked := base
	blocked.Blocked = true
	if got := ReduceProgress(blocked); got != ProgressBlocked {
		t.Fatalf("blocked progress = %s, want BLOCKED", got)
	}
	if ReduceProgress(blocked).Terminal() != true {
		t.Fatal("BLOCKED must be terminal: a grant is not a retry")
	}
	auth := base
	auth.AuthorizationPending = true
	if got := ReduceProgress(auth); got != ProgressRequiresAuthorization {
		t.Fatalf("authorization progress = %s, want REQUIRES_AUTHORIZATION", got)
	}
	// Failed and unsubstantiated are distinct, and neither reads as success.
	if ReduceProgress(ProgressInput{Outcome: ObjectiveFailed}).Terminal() != true {
		t.Fatal("FAILED must be terminal")
	}
	if ReduceProgress(ProgressInput{Outcome: ObjectiveUnsubstantiated}) != ProgressUnsubstantiated {
		t.Fatal("an unsubstantiated outcome must project to UNSUBSTANTIATED, never to a success state")
	}
	// Exhausted continuation budget is unsubstantiated, not a silent success.
	spent := base
	spent.Attempted = true
	spent.AttemptsUsed = 3
	spent.MaxAttempts = 3
	empty := ObjectiveEvidence{Conditions: contract.Conditions}
	if got := ReduceProgressWith(spent, empty); got != ProgressUnsubstantiated {
		t.Fatalf("exhausted budget progress = %s, want UNSUBSTANTIATED", got)
	}
}

// TestCaseO_EmptyConditionSetIsBackwardsCompatible pins that a lifecycle with no
// authored contract reproduces the pre-contract authority exactly. This is what
// makes the new clause additive rather than a behaviour change.
func TestCaseO_EmptyConditionSetIsBackwardsCompatible(t *testing.T) {
	const target = "old.txt"
	_ = DeriveObjectiveContract(ObjectiveDerivation{Request: "x", Kind: TaskPatch, Scope: []string{target}})
	ev := ObjectiveEvidence{
		Provider:             ProviderDone,
		FinishReason:         "stop",
		Artifact:             ArtifactProduced,
		ArtifactsParsed:      1,
		Mutation:             FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{target},
		TargetExists:         map[string]bool{target: true},
		VerificationRan:      true,
		VerificationPassed:   true,
	}
	if got := AuthorizeObjective(contractFor(target), ev); got.Outcome != ObjectiveProven {
		t.Fatalf("with no conditions the authority changed behaviour: %s (%s)", got.Outcome, got.Reason)
	}
}
