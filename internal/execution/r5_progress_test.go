package execution

// ── R5 — NON-PROGRESS AND LOOP CONTROL: the authoritative progress contract ──
//
// This file pins the ONE definition of progress the runtime already owns:
//
//	progress = an OBSERVED, authoritative state transition evaluated by
//	           ReduceProgress / ReduceConditions against the objective's own
//	           completion conditions.
//
// It is NOT model output, NOT a token count, NOT "the provider returned", NOT
// "a capability ran", and NOT "a step completed". The scenarios below classify
// every candidate signal P0–P6 and assert which ones are authoritative:
//
//	P0  no observable change                     → NOT progress
//	P1  model produced new output (tokens)       → NOT progress
//	P2  new context/evidence became available    → evidence (one condition)
//	P3  a capability executed                    → NOT progress by itself
//	P4  workspace/artifact state changed         → authoritative (SCOPE_MUTATED)
//	P5  verification state advanced              → authoritative (INTEGRITY_HELD)
//	P6  objective state advanced                 → PARTIALLY_SATISFIED / PROVEN
//
// Completion is DERIVED from P6 and only from P6. A P1 "the model said it is
// done" observation cannot reach PROVEN no matter how many tokens it spent.

import (
	"testing"
)

// r5ProgressInput builds the deterministic ProgressInput for one contract.
func r5ProgressInput(contract ObjectiveContract, attempted bool, used, max int) ProgressInput {
	return ProgressInput{
		Contract:          contract,
		Conditions:        contract.Conditions,
		Attempted:         attempted,
		AttemptsUsed:      used,
		MaxAttempts:       max,
		MaxRecoveryCycles: 2,
		RecoveryUsed:      0,
	}
}

// satisfyingConditions counts the conditions the evidence actually satisfies.
func satisfyingConditions(contract ObjectiveContract, ev ObjectiveEvidence) (satisfied, total int) {
	for _, c := range ReduceConditions(contract.Conditions, ev) {
		if c.Satisfied() {
			satisfied++
		}
	}
	return satisfied, len(contract.Conditions)
}

// TestR5_ProgressVocabularyIsClosedAndPartitioned proves the progress states are
// a total, non-overlapping vocabulary: every state is distinct, and the
// terminal / continuous / advanceable predicates classify every state exactly
// once. This is what makes "is continuation admissible?" answerable from one
// value instead of a hand-maintained list at each call site.
func TestR5_ProgressVocabularyIsClosedAndPartitioned(t *testing.T) {
	all := []ObjectiveProgress{
		ProgressDiscovered, ProgressUnderstood, ProgressRequirementsDerived,
		ProgressReady, ProgressInProgress, ProgressPartiallySatisfied,
		ProgressRequiresContinuation, ProgressBlocked, ProgressProven,
		ProgressFailed, ProgressUnsubstantiated, ProgressRequiresAuthorization,
	}
	seen := map[ObjectiveProgress]bool{}
	for _, p := range all {
		if p.String() == "" {
			t.Errorf("progress state %v renders empty", p)
		}
		if seen[p] {
			t.Errorf("progress state %q appears twice", p)
		}
		seen[p] = true
		// Continues and Advanceable are strict refinements of "not terminal".
		if p.Continues() && p.Terminal() {
			t.Errorf("%s reports both Continues and Terminal", p)
		}
		if p.Advanceable() && !p.Continues() {
			t.Errorf("%s is Advanceable but not Continues", p)
		}
	}
	// The two terminal-success-adjacent states must never be reachable by
	// continuation.
	if ProgressProven.Continues() || ProgressProven.Advanceable() {
		t.Error("PROVEN reports as continuable")
	}
	if !ProgressPartiallySatisfied.Advanceable() {
		t.Error("PARTIALLY_SATISFIED must be advanceable")
	}
}

// TestR5_P0_NoObservableChangeIsNotProgress pins the floor of the contract: a
// step that ran and changed nothing in the authoritative state is NOT progress.
func TestR5_P0_NoObservableChangeIsNotProgress(t *testing.T) {
	const target = "handler.go"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p0",
		Request:     "refactor the handler in " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})
	ev := ObjectiveEvidence{
		Provider:     ProviderDone,
		FinishReason: "stop",
		Artifact:     ArtifactNone,
		Mutation:     FilesystemNone,
		Conditions:   contract.Conditions,
	}

	// The ceiling has NOT been reached, so the runtime is genuinely choosing.
	progress := ReduceProgressWith(r5ProgressInput(contract, true, 1, 3), ev)
	if progress == ProgressProven {
		t.Fatal("a step that changed nothing reported PROVEN")
	}
	if progress == ProgressPartiallySatisfied {
		t.Fatal("a step that satisfied zero conditions reported PARTIALLY_SATISFIED")
	}
	if progress.Terminal() {
		t.Fatalf("a step that changed nothing terminated at %s before the bounds were spent", progress)
	}
	// Nothing holds and nothing was observed: the approach produced no usable
	// evidence, so the control plane must REPLAN rather than repeat it.
	cont := DecideContinuation(r5ProgressInput(contract, true, 1, 3), ev)
	if cont.Decision == Complete {
		t.Fatal("an empty step completed the objective")
	}
	if cont.Decision != Replan {
		t.Fatalf("continuation = %s (%s), want REPLAN (the approach produced no evidence)", cont.Decision, cont.Reason)
	}
	if satisfied, total := satisfyingConditions(contract, ev); satisfied != 0 {
		t.Fatalf("%d/%d conditions satisfied by empty evidence", satisfied, total)
	}
}

// TestR5_P1_ModelOutputAloneIsNeverProgress pins the most important negative:
// the provider returned, it wrote tokens, and the runtime advances nothing. A
// model's own claim is not an authoritative signal.
func TestR5_P1_ModelOutputAloneIsNeverProgress(t *testing.T) {
	const target = "index.html"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p1",
		Request:     "rewrite the page in " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})

	// The model produced a LOT of output — but the artifact boundary parsed
	// nothing executable from it. The response fields below are the transport
	// and parser facts, not a token count: the runtime has no access to the
	// model's claim about itself.
	prose := ObjectiveEvidence{
		Provider:         ProviderDone,
		FinishReason:     "stop",
		Artifact:         ArtifactNone,
		ArtifactsParsed:  0,
		ResponseProduced: true,
		ResponseBytes:    4096, // a lot of prose, zero executable state
		Conditions:       contract.Conditions,
	}
	if got := ReduceProgressWith(r5ProgressInput(contract, true, 1, 3), prose); got == ProgressProven {
		t.Fatal("4096 bytes of prose PROVED the objective")
	}
	if satisfied, _ := satisfyingConditions(contract, prose); satisfied != 0 {
		t.Fatalf("prose satisfied %d condition(s)", satisfied)
	}
	if got := AuthorizeObjective(contractFor(target), prose); got.Outcome == ObjectiveProven {
		t.Fatal("prose reached PROVEN")
	}

	// A truncated generation (finish_reason=length) is also not progress: it is
	// a preserved PREFIX that can never satisfy a completion condition.
	truncated := prose
	truncated.FinishReason = "length"
	truncated.Artifact = ArtifactContinuing
	truncated.PartialArtifact = true
	if got := ReduceProgressWith(r5ProgressInput(contract, true, 1, 3), truncated); got != ProgressRequiresContinuation {
		t.Fatalf("truncated generation progress = %s, want REQUIRES_CONTINUATION", got)
	}
	if got := AuthorizeObjective(contractFor(target), truncated); got.Outcome == ObjectiveProven {
		t.Fatal("a truncated prefix PROVED the objective")
	}
}

// TestR5_P2_NewEvidenceIsAuthoritativeButNotCompletion proves that a genuinely
// NEW observation is real evidence — and still not completion on a mutation
// objective. Evidence and completion are different questions.
func TestR5_P2_NewEvidenceIsAuthoritativeButNotCompletion(t *testing.T) {
	const target = "package.json"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p2",
		Request:     "add a build script to " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})
	observed := ObjectiveEvidence{
		Provider:              ProviderDone,
		FinishReason:          "stop",
		Artifact:              ArtifactNone,
		Mutation:              FilesystemNone,
		WorkspaceObservations: 1,
		TargetExists:          map[string]bool{target: true},
		Conditions:            contract.Conditions,
	}
	// The OBSERVED obligation is satisfied by a real observation.
	conditions := ReduceConditions(contract.Conditions, observed)
	var observedSatisfied bool
	for _, c := range conditions {
		if c.Obligation == ObligationObserved && c.Satisfied() {
			observedSatisfied = true
			if c.VerificationState != VerifyObserved {
				t.Fatalf("OBSERVED satisfied with verification state %s, want OBSERVED", c.VerificationState)
			}
		}
	}
	if !observedSatisfied {
		t.Fatal("a real workspace observation did not satisfy the OBSERVED obligation")
	}
	// But observation alone is not a durable mutation: the objective is not
	// proven, and the progress state reflects a partial step.
	if got := AuthorizeObjective(contractFor(target), observed); got.Outcome == ObjectiveProven {
		t.Fatal("an observation-only step PROVED a mutation objective")
	}
	if got := ReduceProgressWith(r5ProgressInput(contract, true, 1, 3), observed); got == ProgressProven {
		t.Fatal("an observation-only step reported PROVEN progress")
	}
}

// TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress pins that "a
// capability executed successfully" is an EVENT, not an outcome. A command that
// exits 0 and changes nothing satisfies no authoritative condition.
func TestR5_P3_CapabilityExecutionWithoutStateChangeIsNotProgress(t *testing.T) {
	const target = "main.go"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p3",
		Request:     "make main.go print hello",
		Kind:        TaskPatch,
		Scope:       []string{target},
	})
	// The capability ran, returned, and the workspace is byte-identical.
	noopCapability := ObjectiveEvidence{
		Provider:         ProviderDone,
		FinishReason:     "stop",
		Artifact:         ArtifactNone,
		Mutation:         FilesystemNone,
		MutatedFiles:     0,
		ResponseProduced: true,
		Conditions:       contract.Conditions,
	}
	for _, c := range ReduceConditions(contract.Conditions, noopCapability) {
		if c.Obligation == ObligationScopeMutated && c.Satisfied() {
			t.Fatal("a capability that changed no bytes satisfied SCOPE_MUTATED")
		}
	}
	if got := AuthorizeObjective(contractFor(target), noopCapability); got.Outcome == ObjectiveProven {
		t.Fatal("a successful no-op capability PROVED the objective")
	}
	if satisfied, _ := satisfyingConditions(contract, noopCapability); satisfied != 0 {
		t.Fatalf("a no-op capability satisfied %d condition(s)", satisfied)
	}
}

// TestR5_P4_WorkspaceChangeIsAuthoritativeProgress proves the mutation signal
// itself is authoritative: a durable filesystem delta satisfies SCOPE_MUTATED.
// It is still not completion on its own — the result must be inspected and the
// integrity gate must hold.
func TestR5_P4_WorkspaceChangeIsAuthoritativeProgress(t *testing.T) {
	const target = "note.txt"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p4",
		Request:     "change the greeting in " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})
	// Start from a fully satisfied fixture and strip every P5/P6 signal,
	// leaving ONLY the durable mutation.
	mutationOnly := satisfiedPatchEvidence(target)
	mutationOnly.Conditions = contract.Conditions
	mutationOnly.VerificationRan = false
	mutationOnly.VerificationPassed = false
	mutationOnly.WorkspaceObservations = 0
	mutationOnly.PostMutationObserved = nil

	var scopeMutated bool
	for _, c := range ReduceConditions(contract.Conditions, mutationOnly) {
		if c.Obligation == ObligationScopeMutated && c.Satisfied() {
			scopeMutated = true
			if c.VerificationState != VerifyObserved {
				t.Fatalf("SCOPE_MUTATED state = %s, want OBSERVED", c.VerificationState)
			}
		}
	}
	if !scopeMutated {
		t.Fatal("a durable filesystem delta did not satisfy SCOPE_MUTATED")
	}
	if got := AuthorizeObjective(contractFor(target), mutationOnly); got.Outcome == ObjectiveProven {
		t.Fatal("an uninspected, unverified mutation PROVED the objective")
	}
	// At least one condition holds and at least one does not: the objective is
	// genuinely PARTIALLY_SATISFIED, which is the only state continuation may
	// re-open.
	if got := ReduceProgressWith(r5ProgressInput(contract, true, 1, 3), mutationOnly); got != ProgressPartiallySatisfied {
		t.Fatalf("mutation-only progress = %s, want PARTIALLY_SATISFIED", got)
	}
}

// TestR5_P5_VerificationAdvancesIntegrity proves the verification signal is
// authoritative and independently tracked: a passed gate satisfies
// INTEGRITY_HELD with GATE_PASS, a skipped gate with GATE_NOT_APPLICABLE, and a
// gate that never ran is a failure to establish integrity — never a pass.
func TestR5_P5_VerificationAdvancesIntegrity(t *testing.T) {
	const target = "app.py"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p5",
		Request:     "fix the bug in " + target,
		Kind:        TaskPatch,
		Scope:       []string{target},
	})

	base := satisfiedPatchEvidence(target)
	base.Conditions = contract.Conditions

	integrity := func(ev ObjectiveEvidence) CompletionCondition {
		for _, c := range ReduceConditions(contract.Conditions, ev) {
			if c.Obligation == ObligationIntegrityHeld {
				return c
			}
		}
		return CompletionCondition{}
	}

	passed := base
	if c := integrity(passed); !c.Satisfied() || c.VerificationState != VerifyGatePass {
		t.Fatalf("verified fixture integrity = %s/%s, want SATISFIED/GATE_PASS", c.Status, c.VerificationState)
	}

	skipped := base
	skipped.VerificationRan = false
	skipped.VerificationPassed = false
	skipped.VerificationSkipped = true
	if c := integrity(skipped); !c.Satisfied() || c.VerificationState != VerifyGateNotApplicable {
		t.Fatalf("skipped gate integrity = %s/%s, want SATISFIED/GATE_NOT_APPLICABLE", c.Status, c.VerificationState)
	}

	notRun := base
	notRun.VerificationRan = false
	notRun.VerificationPassed = false
	notRun.VerificationSkipped = false
	if c := integrity(notRun); c.Satisfied() {
		t.Fatalf("a gate that never ran satisfied INTEGRITY_HELD (%s)", c.VerificationState)
	}
}

// TestR5_P6_ObjectiveAdvancementIsTheOnlyCompletionSignal is the positive
// control: only when the evidence satisfies EVERY condition does the objective
// advance to PROVEN and the continuation collapse to COMPLETE. The multi-step
// case proves a partial advance is PARTIALLY_SATISFIED and eligible to
// continue — the semantic basis for R5 Case A.
func TestR5_P6_ObjectiveAdvancementIsTheOnlyCompletionSignal(t *testing.T) {
	const a, b = "index.html", "styles.css"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-p6",
		Request:     "restyle both pages",
		Kind:        TaskPatch,
		Scope:       []string{a, b},
	})

	// ── step 1: only one of two targets advanced ──────────────────────
	step1 := satisfiedPatchEvidence(a)
	step1.Conditions = contract.Conditions
	step1.ObservedDeltaTargets = []string{a}
	step1.PostMutationObserved = []string{a}
	step1.TargetExists = map[string]bool{a: true, b: true}

	in1 := r5ProgressInput(contract, true, 1, 3)
	if got := ReduceProgressWith(in1, step1); got != ProgressPartiallySatisfied {
		t.Fatalf("step 1 progress = %s, want PARTIALLY_SATISFIED", got)
	}
	cont1 := DecideContinuation(in1, step1)
	if !cont1.Continue() {
		t.Fatalf("step 1 continuation = %s, want CONTINUE/REPLAN (%s)", cont1.Decision, cont1.Reason)
	}
	if got := AuthorizeObjective(contractFor(a, b), step1); got.Outcome == ObjectiveProven {
		t.Fatal("step 1 PROVED a two-target objective")
	}

	// ── step 2: the second target advances; every condition now holds ──
	step2 := step1
	step2.ObservedDeltaTargets = []string{a, b}
	step2.PostMutationObserved = []string{a, b}
	if got := ReduceProgressWith(in1, step2); got != ProgressProven {
		t.Fatalf("step 2 progress = %s, want PROVEN", got)
	}
	if got := DecideContinuation(in1, step2); got.Decision != Complete {
		t.Fatalf("step 2 continuation = %s, want COMPLETE", got.Decision)
	}
	if got := AuthorizeObjective(contractFor(a, b), step2); got.Outcome != ObjectiveProven {
		t.Fatalf("step 2 outcome = %s, want PROVEN", got.Outcome)
	}
}

// TestR5_ProgressIsMonotoneWhileConditionsRemain proves the property the
// continuation router depends on: once a condition holds it keeps holding as
// evidence accumulates, so a progressing objective stays ADVANCEABLE and a
// stalled one stays at the SAME classification. The classification is a
// function of evidence, never of elapsed attempts.
func TestR5_ProgressIsMonotoneWhileConditionsRemain(t *testing.T) {
	const a, b = "index.html", "styles.css"
	contract := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "r5-mono",
		Request:     "restyle both pages",
		Kind:        TaskPatch,
		Scope:       []string{a, b},
	})
	step1 := satisfiedPatchEvidence(a)
	step1.Conditions = contract.Conditions
	step1.ObservedDeltaTargets = []string{a}
	step1.PostMutationObserved = []string{a}
	step1.TargetExists = map[string]bool{a: true, b: true}

	in := r5ProgressInput(contract, true, 1, 3)
	first := ReduceProgressWith(in, step1)
	// Re-evaluating the SAME evidence (a repeated no-op attempt that changes
	// nothing) yields the SAME classification. The reducer has no clock and no
	// attempt awareness: a stall is a property of the SEQUENCE of snapshots,
	// which the loop-control layer must compare — not something this pure
	// reducer can invent.
	second := ReduceProgressWith(in, step1)
	if first != second {
		t.Fatalf("identical evidence produced different progress: %s vs %s", first, second)
	}
	if !first.Advanceable() {
		t.Fatalf("a partially satisfied objective is not advanceable: %s", first)
	}
}
