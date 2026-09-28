package execution

import (
	"errors"
	"testing"
)

// ── PHASE 14 ACCEPTANCE MATRIX — Objective Completion Authority ──────────────
//
// The five tests below are the acceptance matrix for objective-completion
// enforcement. They are written as SYMMETRY pairs: each one states the evidence
// that PROVES an objective and the single missing fact that leaves it
// UNPROVEN. A rule that only has a positive test is a rule that can be
// satisfied by accident.
//
// The property under test throughout is INVARIANT 1: none of the four
// "done"-shaped facts (a provider that returned, a parser that ran, a
// filesystem that was touched, a verifier that executed) authorizes completion
// on its own. Only the conjunction does.

// provenPatchEvidence is a fully-satisfied PATCH bundle: a completed provider
// stream, a parsed artifact, a durable mutation on a declared target, and a
// verification pass. Every test derives from it by REMOVING one fact, so a
// passing suite proves each fact is individually necessary.
func provenPatchEvidence() ObjectiveEvidence {
	return ObjectiveEvidence{
		Provider:             ProviderDone,
		FinishReason:         "stop",
		Artifact:             ArtifactProduced,
		ArtifactsParsed:      1,
		Mutation:             FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"note.txt"},
		TargetExists:         map[string]bool{"note.txt": true},
		VerificationRan:      true,
		VerificationPassed:   true,
	}
}

func patchContract() TaskContract {
	return TaskContract{
		Kind:             TaskPatch,
		Targets:          []string{"note.txt"},
		RequiresVerifier: true,
	}
}

// ── TEST A: False Completion Prevention ──────────────────────────────────────

// TestA_ProviderDoneWithNoEvidenceIsUnsubstantiated is the headline fix. The
// input is the exact shape the runtime used to read as success: the provider
// returned with finish_reason=stop, the execution step returned, and nothing
// was wrong. Artifacts = 0, mutations = 0.
//
// INVARIANT 1 lists four events that must NEVER independently authorize
// completion; this test pins the conjunction of all four simultaneously.
func TestA_ProviderDoneWithNoEvidenceIsUnsubstantiated(t *testing.T) {
	evidence := ObjectiveEvidence{
		Provider:        ProviderDone,
		FinishReason:    "stop",
		Artifact:        ArtifactNone,
		ArtifactsParsed: 0,
		Mutation:        FilesystemNone,
		MutatedFiles:    0,
	}

	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), evidence)

	if outcome != ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want %s (provider DONE is not task completion)", outcome, ObjectiveUnsubstantiated)
	}
	if outcome.Proves() {
		t.Fatal("outcome reports PROVEN for an execution that delivered nothing")
	}
	// The sentinel error the driver must refuse to complete on.
	if _, err := AuthorizeCompletion(patchContract(), evidence); !errors.Is(err, ErrObjectiveUnsubstantiated) {
		t.Fatalf("AuthorizeCompletion err = %v, want ErrObjectiveUnsubstantiated", err)
	}
	// The verdict must name the obligation that failed, not just refuse.
	evaluation := AuthorizeObjective(patchContract(), evidence)
	if evaluation.Clause != "artifact_missing" {
		t.Fatalf("clause = %q, want artifact_missing (the first unmet obligation)", evaluation.Clause)
	}
	if evaluation.State.Proven() {
		t.Fatal("MEANING boundary reported PROVEN for an unsubstantiated claim")
	}
}

// TestA_SymmetryEachMissingFactRefusesCompletion removes exactly one satisfied
// clause at a time from a proven PATCH bundle. Every removal must flip the
// verdict to UNSUBSTANTIATED — that is what makes the positive case meaningful.
func TestA_SymmetryEachMissingFactRefusesCompletion(t *testing.T) {
	authority := NewObjectiveCompletionAuthority()

	mutate := map[string]func(*ObjectiveEvidence){
		"no artifact": func(e *ObjectiveEvidence) { e.Artifact = ArtifactNone; e.ArtifactsParsed = 0 },
		"no mutation": func(e *ObjectiveEvidence) {
			e.Mutation = FilesystemNone
			e.MutatedFiles = 0
			e.ObservedDeltaTargets = nil
		},
		"no delta":      func(e *ObjectiveEvidence) { e.ObservedDeltaTargets = nil },
		"verify failed": func(e *ObjectiveEvidence) { e.VerificationRan = true; e.VerificationPassed = false },
		"target gone":   func(e *ObjectiveEvidence) { e.TargetExists = map[string]bool{"note.txt": false} },
	}
	for name, break_ := range mutate {
		t.Run(name, func(t *testing.T) {
			evidence := provenPatchEvidence()
			break_(&evidence)
			if got := authority.Evaluate(patchContract(), evidence); got.Proves() {
				t.Fatalf("outcome = %s, want UNSUBSTANTIATED when %s", got, name)
			}
		})
	}

	// Symmetry partner: with every clause restored the same contract PROVES.
	if got := authority.Evaluate(patchContract(), provenPatchEvidence()); !got.Proves() {
		t.Fatalf("restored evidence outcome = %s, want PROVEN", got)
	}
}

// TestA_MutationCountIsNotAUniversalRule pins INVARIANT 2's explicit warning.
// One applied file mutation is NOT sufficient evidence on its own: the same
// bundle proves a PATCH, but it cannot prove a CREATE whose target never landed
// on disk, nor a DELETE whose target is still there.
func TestA_MutationCountIsNotAUniversalRule(t *testing.T) {
	authority := NewObjectiveCompletionAuthority()
	applied := provenPatchEvidence()

	// CREATE: the mutation was applied, but the target does not exist.
	noTarget := applied
	noTarget.TargetExists = map[string]bool{"note.txt": false}
	noTarget.TargetAbsent = map[string]bool{"note.txt": true}
	create := TaskContract{Kind: TaskCreate, Targets: []string{"note.txt"}, RequiresVerifier: true}
	if got := authority.Evaluate(create, noTarget); got.Proves() {
		t.Fatal("CREATE proved from an applied mutation whose target is absent")
	}

	// DELETE: the mutation was applied, but the target still exists.
	del := TaskContract{Kind: TaskDelete, Targets: []string{"note.txt"}, RequiresVerifier: true}
	if got := authority.Evaluate(del, applied); got.Proves() {
		t.Fatal("DELETE proved while the declared target still exists")
	}

	// And the satisfied case still proves: the same mutation, correctly read.
	if got := authority.Evaluate(patchContract(), applied); !got.Proves() {
		t.Fatalf("PATCH outcome = %s, want PROVEN", got)
	}
}

// TestA_VerificationInfrastructureAloneDoesNotComplete pins INVARIANT 1's
// fourth bullet: a verification pass with no artifact and no mutation proves
// nothing about a mutation objective.
func TestA_VerificationInfrastructureAloneDoesNotComplete(t *testing.T) {
	evidence := ObjectiveEvidence{
		Provider:           ProviderDone,
		FinishReason:       "stop",
		VerificationRan:    true,
		VerificationPassed: true,
	}
	if got := NewObjectiveCompletionAuthority().Evaluate(patchContract(), evidence); got.Proves() {
		t.Fatalf("outcome = %s, want UNSUBSTANTIATED (verification alone is infrastructure)", got)
	}
}

// TestA_SealedFailureRecordIsAFailure pins that a positive contradiction maps to
// FAILED, not UNSUBSTANTIATED — the two are distinguished so the recovery
// matrix can apply the failure matrix rather than a re-prompt.
func TestA_SealedFailureRecordIsAFailure(t *testing.T) {
	evidence := provenPatchEvidence()
	evidence.SealedRecord = SealFromScalars(SealEvidenceScalars{
		ContractID: "ct-deadbeefdeadbeef",
		AttemptID:  1,
		Outcome:    string(EvidenceFailed),
	})
	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), evidence)
	if outcome != ObjectiveFailed {
		t.Fatalf("outcome = %s, want %s", outcome, ObjectiveFailed)
	}
	if _, err := AuthorizeCompletion(patchContract(), evidence); !errors.Is(err, ErrObjectiveFailed) {
		t.Fatalf("err = %v, want ErrObjectiveFailed", err)
	}
}

// ── TEST B: Valid Creation / Patch ───────────────────────────────────────────

// TestB_ValidCreationProves pins the CREATE contract end to end: artifact
// parsed + mutation applied + durable target exists + verifier PASS.
func TestB_ValidCreationProves(t *testing.T) {
	evidence := ObjectiveEvidence{
		Provider:             ProviderDone,
		FinishReason:         "stop",
		Artifact:             ArtifactProduced,
		ArtifactsParsed:      1,
		Mutation:             FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"new.txt"},
		TargetExists:         map[string]bool{"new.txt": true},
		VerificationRan:      true,
		VerificationPassed:   true,
	}
	contract := TaskContract{Kind: TaskCreate, Targets: []string{"new.txt"}, RequiresVerifier: true}

	if got := NewObjectiveCompletionAuthority().Evaluate(contract, evidence); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN for a fully-evidenced CREATE", got)
	}
	if _, err := AuthorizeCompletion(contract, evidence); err != nil {
		t.Fatalf("AuthorizeCompletion err = %v, want nil", err)
	}
	// The verdict is PROVEN exactly, not "proven-ish".
	evaluation := AuthorizeObjective(contract, evidence)
	if evaluation.Reason != "" {
		t.Fatalf("PROVEN evaluation carries a refusal reason: %q", evaluation.Reason)
	}
}

// TestB_ValidPatchProves is the PATCH contract: the delta must land on a
// DECLARED target, and an out-of-scope change never substitutes for it.
func TestB_ValidPatchProves(t *testing.T) {
	if got := NewObjectiveCompletionAuthority().Evaluate(patchContract(), provenPatchEvidence()); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN for a fully-evidenced PATCH", got)
	}
	offScope := provenPatchEvidence()
	offScope.ObservedDeltaTargets = []string{"unrelated.go"}
	if got := NewObjectiveCompletionAuthority().Evaluate(patchContract(), offScope); got.Proves() {
		t.Fatal("an out-of-scope delta must not prove an in-scope PATCH")
	}
}

// TestB_NotApplicableVerifierIsDistinguishedFromAPass pins that a language with
// no verification contract is NOT APPLICABLE rather than PASS, and that neither
// state is silently relabelled as the other. Both satisfy the clause; the
// recorded verdict is what a projection must read.
func TestB_NotApplicableVerifierIsDistinguishedFromAPass(t *testing.T) {
	skipped := provenPatchEvidence()
	skipped.VerificationRan = false
	skipped.VerificationPassed = false
	skipped.VerificationSkipped = true
	if skipped.VerifierVerdict() != "NOT_APPLICABLE" {
		t.Fatalf("verifier verdict = %s, want NOT_APPLICABLE", skipped.VerifierVerdict())
	}
	if got := NewObjectiveCompletionAuthority().Evaluate(patchContract(), skipped); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN (a provably not-applicable gate is not a failure)", got)
	}
	never := provenPatchEvidence()
	never.VerificationRan = false
	never.VerificationPassed = false
	if never.VerifierVerdict() != "NOT_RUN" {
		t.Fatalf("verifier verdict = %s, want NOT_RUN", never.VerifierVerdict())
	}
	if got := NewObjectiveCompletionAuthority().Evaluate(patchContract(), never); got.Proves() {
		t.Fatal("a never-run gate must not satisfy a required verifier")
	}
}

// ── TEST C: Truncated Response ───────────────────────────────────────────────

// TestC_TruncatedResponseIsContinuingNotComplete pins the finish_reason=length
// case: the delivered prefix is PRESERVED for token continuation, so the
// artifact state is CONTINUING and the objective is unproven.
//
// The provider boundary is still DONE here — it stopped emitting tokens — which
// is exactly the confusion the four-state split exists to prevent.
func TestC_TruncatedResponseIsContinuingNotComplete(t *testing.T) {
	evidence := provenPatchEvidence()
	evidence.FinishReason = "length"
	evidence.Artifact = ArtifactContinuing
	evidence.PartialArtifact = true
	evidence.ArtifactsParsed = 0
	// The provider DID stop emitting. It carries no task meaning.
	evidence.Provider = ObserveProviderState("length", 1, nil)
	if evidence.Provider != ProviderDone {
		t.Fatalf("provider state = %s, want DONE (transport fact)", evidence.Provider)
	}
	if evidence.Artifact != ArtifactContinuing {
		t.Fatalf("artifact state = %s, want %s", evidence.Artifact, ArtifactContinuing)
	}

	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), evidence)
	if outcome.Proves() {
		t.Fatal("a truncated stream completed the objective")
	}
	if outcome != ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want %s", outcome, ObjectiveUnsubstantiated)
	}
	evaluation := AuthorizeObjective(patchContract(), evidence)
	if evaluation.Clause != "artifact_continuing" {
		t.Fatalf("clause = %q, want artifact_continuing", evaluation.Clause)
	}
	// The partial artifact is PRESERVED, not discarded: the outcome explains
	// that the prefix is kept for continuation.
	if evaluation.Reason == "" {
		t.Fatal("truncation refusal must explain that the partial artifact is preserved")
	}
}

// TestC_TruncationIsClassifiedBeforeParsing pins that a truncated stream is
// classified CONTINUING even when a parser happened to find something in the
// prefix — the length verdict outranks the parse count, because an artifact cut
// off mid-generation is by definition incomplete.
func TestC_TruncationIsClassifiedBeforeParsing(t *testing.T) {
	if got := ObserveArtifactState("length", 3, false); got != ArtifactContinuing {
		t.Fatalf("artifact state = %s, want %s", got, ArtifactContinuing)
	}
	if got := ObserveArtifactState("max_tokens", 0, true); got != ArtifactContinuing {
		t.Fatalf("artifact state = %s, want %s", got, ArtifactContinuing)
	}
	if got := ObserveArtifactState("stop", 1, false); got != ArtifactProduced {
		t.Fatalf("artifact state = %s, want %s", got, ArtifactProduced)
	}
	if got := ObserveArtifactState("stop", 0, false); got != ArtifactNone {
		t.Fatalf("artifact state = %s, want %s (prose is not an artifact)", got, ArtifactNone)
	}
}

// ── TEST D: Idempotent Objective ─────────────────────────────────────────────

// TestD_AlreadySatisfiedObjectiveProves pins the IDEMPOTENT contract: the
// target state was already satisfied on disk (a deterministic structural
// verdict), the mutation count is zero, and the verifier passed. Zero mutations
// is the POINT of this contract, not a failure of evidence.
func TestD_AlreadySatisfiedObjectiveProves(t *testing.T) {
	evidence := ObjectiveEvidence{
		Provider:                ProviderDone,
		FinishReason:            "stop",
		Mutation:                FilesystemNone,
		MutatedFiles:            0,
		PreconditionSatisfied:   true,
		StructuralNoOpConfirmed: true,
		VerificationRan:         true,
		VerificationPassed:      true,
	}
	contract := TaskContract{Kind: TaskIdempotent, Targets: []string{"note.txt"}, RequiresVerifier: true}

	if got := NewObjectiveCompletionAuthority().Evaluate(contract, evidence); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN for an already-satisfied objective", got)
	}
	if _, err := AuthorizeCompletion(contract, evidence); err != nil {
		t.Fatalf("AuthorizeCompletion err = %v, want nil", err)
	}
}

// TestD_ZeroMutationsWithoutStructuralProofIsExecutionInertia is the
// anti-relaxation of Test D. A mutation objective that applied NOTHING proves
// nothing unless a structural verdict says the work was genuinely already done —
// otherwise it is execution inertia, the exact false completion the phase
// exists to stop.
func TestD_ZeroMutationsWithoutStructuralProofIsExecutionInertia(t *testing.T) {
	contract := TaskContract{Kind: TaskPatch, Targets: []string{"note.txt"}, RequiresVerifier: true}
	idempotent := TaskContract{Kind: TaskIdempotent, Targets: []string{"note.txt"}, RequiresVerifier: true}
	inertia := ObjectiveEvidence{
		Provider:              ProviderDone,
		FinishReason:          "stop",
		Mutation:              FilesystemNone,
		PreconditionSatisfied: true, // the file existed before the run
		VerificationRan:       true,
		VerificationPassed:    true,
	}
	// PreconditionSatisfied (the file existed) is deliberately NOT enough: it is
	// indistinguishable from a run that simply did nothing.
	if got := NewObjectiveCompletionAuthority().Evaluate(idempotent, inertia); got.Proves() {
		t.Fatal("existence alone proved the IDEMPOTENT contract")
	}
	if got := NewObjectiveCompletionAuthority().Evaluate(contract, inertia); got.Proves() {
		t.Fatal("execution inertia completed the objective")
	}

	// The same inertia under the IDEMPOTENT contract is equally refused without
	// the structural confirmation.
	if got := NewObjectiveCompletionAuthority().Evaluate(idempotent, inertia); got.Proves() {
		t.Fatal("IDEMPOTENT proved without a structural confirmation")
	}
	evaluation := AuthorizeObjective(idempotent, inertia)
	if evaluation.Clause != "precondition_unproven" {
		t.Fatalf("clause = %q, want precondition_unproven", evaluation.Clause)
	}

	// The executor's deterministic structural NO-OP verdict IS the confirmation,
	// and it doubles as the verification for a contract with nothing applied.
	confirmed := inertia
	confirmed.PreconditionSatisfied = false
	confirmed.StructuralNoOpConfirmed = true
	if got := NewObjectiveCompletionAuthority().Evaluate(idempotent, confirmed); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN under a confirmed structural no-op", got)
	}
}

// TestD_IdempotentDerivationRequiresPreExecutionState pins the derivation: the
// IDEMPOTENT contract is reachable only with a pre-execution observation, never
// from a post-hoc "nothing changed" reading.
func TestD_IdempotentDerivationRequiresPreExecutionState(t *testing.T) {
	base := TaskClassification{
		Intent:               "modification",
		Objective:            "change bar to qux",
		RequiresMutation:     true,
		Targets:              []string{"note.txt"},
		TargetsExistedBefore: map[string]bool{"note.txt": true},
		TargetsChanged:       map[string]bool{},
	}

	structural := base
	structural.StructuralNoOpConfirmed = true
	if got := DeriveTaskContract(structural); got.Kind != TaskIdempotent {
		t.Fatalf("contract kind = %s, want %s", got.Kind, TaskIdempotent)
	}

	// No structural confirmation and no pre-execution determination: the
	// contract stays a PATCH, whose clauses the zero-mutation evidence fails.
	inertia := base
	if got := DeriveTaskContract(inertia); got.Kind != TaskPatch {
		t.Fatalf("contract kind = %s, want %s (inertia is not idempotence)", got.Kind, TaskPatch)
	}
	// A target that did not exist before the run can never be idempotent.
	fresh := base
	fresh.TargetsExistedBefore = map[string]bool{}
	fresh.StructuralNoOpConfirmed = true
	if got := DeriveTaskContract(fresh); got.Kind != TaskCreate {
		t.Fatalf("contract kind = %s, want %s (no pre-existing content is a creation)", got.Kind, TaskCreate)
	}
}

// ── Contract derivation symmetry ────────────────────────────────────────────

// TestDeriveTaskContract_PositionalDeletionBinding pins that a DELETE contract
// requires the verb to bind to a DECLARED target. "Remove every comment from
// @big.go" removes CONTENT from a file that must survive; judging it as DELETE
// would demand the absence of the very file the objective keeps.
func TestDeriveTaskContract_PositionalDeletionBinding(t *testing.T) {
	cases := []struct {
		objective string
		target    string
		want      TaskKind
	}{
		{"delete note.txt", "note.txt", TaskDelete},
		{"remove the file big.go", "big.go", TaskDelete},
		{"delete @index.html", "index.html", TaskDelete},
		{"remove every DEPRECATED-MARKER comment @big.go", "big.go", TaskPatch},
		{"remove the redundant divs from index.html", "index.html", TaskPatch},
		{"rewrite the documentation for note.txt", "note.txt", TaskPatch},
	}
	for _, tc := range cases {
		t.Run(tc.objective, func(t *testing.T) {
			got := DeriveTaskContract(TaskClassification{
				Intent:               "modification",
				Objective:            tc.objective,
				RequiresMutation:     true,
				Targets:              []string{tc.target},
				TargetsExistedBefore: map[string]bool{tc.target: true},
			})
			if got.Kind != tc.want {
				t.Fatalf("contract kind = %s, want %s", got.Kind, tc.want)
			}
		})
	}
}

// TestDeriveTaskContract_CreationShapeIsNeverRelabelled pins INVARIANT 4's
// explicit prohibition: recovery must never turn a creation into a patch,
// because a new file has no content to anchor a patch against.
func TestDeriveTaskContract_CreationShapeIsNeverRelabelled(t *testing.T) {
	got := DeriveTaskContract(TaskClassification{
		Intent:               "modification",
		Objective:            "add a header",
		RequiresMutation:     true,
		Targets:              []string{"index.html"},
		TargetsExistedBefore: map[string]bool{"index.html": true},
		ArtifactShape:        "create_file",
	})
	if got.Kind != TaskCreate {
		t.Fatalf("contract kind = %s, want %s (a creation shape is authoritative)", got.Kind, TaskCreate)
	}
}

// TestReadAndReviewContractsRequireObservation pins the READ/REVIEW evidence
// clause: the model must have actually looked at the workspace, and a response
// alone is not evidence that it did.
func TestReadAndReviewContractsRequireObservation(t *testing.T) {
	authority := NewObjectiveCompletionAuthority()
	contract := DeriveTaskContract(TaskClassification{
		Intent:    "verification",
		Objective: "verify the handler wiring",
		Targets:   []string{"big.go"},
	})
	if contract.Kind != TaskReview {
		t.Fatalf("contract kind = %s, want %s", contract.Kind, TaskReview)
	}
	silent := ObjectiveEvidence{ResponseProduced: true, ResponseBytes: 512}
	if got := authority.Evaluate(contract, silent); got.Proves() {
		t.Fatal("a review with no workspace observation proved the objective")
	}
	observed := silent
	observed.WorkspaceObservations = 1
	if got := authority.Evaluate(contract, observed); !got.Proves() {
		t.Fatalf("outcome = %s, want PROVEN for an observed, answered review", got)
	}
}

// TestAuthorizationIsDeterministic pins that the authority is a pure reducer:
// identical inputs always yield an identical outcome AND an identical reason.
func TestAuthorizationIsDeterministic(t *testing.T) {
	first := AuthorizeObjective(patchContract(), provenPatchEvidence())
	for i := 0; i < 32; i++ {
		got := AuthorizeObjective(patchContract(), provenPatchEvidence())
		if got != first {
			t.Fatalf("iteration %d = %+v, want %+v", i, got, first)
		}
	}
}

// TestUnknownContractKindCanNeverProve pins the fail-closed default: a contract
// outside the canonical vocabulary proves nothing, rather than falling through
// to a permissive branch.
func TestUnknownContractKindCanNeverProve(t *testing.T) {
	got := NewObjectiveCompletionAuthority().Evaluate(
		TaskContract{Kind: TaskKind("SOMETHING_ELSE"), Targets: []string{"note.txt"}},
		provenPatchEvidence())
	if got.Proves() {
		t.Fatalf("outcome = %s, want a non-PROVEN verdict for an unknown contract", got)
	}
}
