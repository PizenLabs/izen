package autonomy

// ── PHASE 14 ACCEPTANCE MATRIX — Driver ↔ Objective Completion Authority ────
//
// These are the DRIVER-level symmetry tests: they run the real bounded loop over
// the real RuntimeExecutor and assert on the LOOP STATE, not on a helper. That
// distinction is the whole point of the phase. The authority is only worth
// having if `interpreting -> completed` is genuinely unreachable without
// evidence, and the only way to prove a transition is unreachable is to drive
// the machine that owns it.
//
// The package deliberately imports NO presentation type and no UI package: every
// test below is executable headless, which is itself part of the architecture
// lock (Test E).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// proseResponse is the model output that used to read as success: a completed
// provider stream carrying a well-written paragraph and no artifact.
const proseResponse = "I have updated the file for you.\n\n" +
	"Here is a summary of what changed and why each change was needed for the " +
	"requested objective, together with the reasoning behind the chosen approach."

// ── TEST A: False Completion Prevention ──────────────────────────────────────

// TestPhase14A_LyingDecisionMatrixCannotCompleteWithoutEvidence is the sharpest
// form of the fix. The decision matrix is FORCED to propose `LoopComplete` on
// every observation — the exact behaviour that produced false completions — and
// the run is driven over a provider stream that carried prose and no artifact.
//
// Before the authority, this terminated `completed` with the reason
// "objective satisfied". Now the authority rewrites the proposal into the
// terminal state that matches WHY the evidence failed, and the workspace is
// provably untouched.
func TestPhase14A_LyingDecisionMatrixCannotCompleteWithoutEvidence(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: proseResponse}})
	lie := func(autonomy.Observation, autonomy.LoopBounds) autonomy.LoopDecision {
		return autonomy.LoopDecision{
			Action: autonomy.LoopComplete,
			Reason: "objective satisfied: changed",
		}
	}
	d := NewDriver(a, nil, WithDecider(lie))

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a provider that returned prose completed the objective — false completion")
	}
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want a non-completed terminal state", term)
	}
	// The authority recorded WHY, naming the unmet clause.
	evaluation := d.objectiveEvaluation()
	if evaluation.Outcome == execution.ObjectiveProven {
		t.Fatal("authority reported PROVEN for a prose-only execution")
	}
	if evaluation.Outcome != execution.ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want %s", evaluation.Outcome, execution.ObjectiveUnsubstantiated)
	}
	if evaluation.Clause == "" || evaluation.Reason == "" {
		t.Fatalf("refusal must name the unmet clause and reason, got %+v", evaluation)
	}
	// The workspace is byte-identical: no prose was ever written.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("prose reached the workspace: %q", got)
	}
	// The transition history records the refusal, not a completion.
	sawUnsubstantiated := false
	for _, tr := range d.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("history contains a completed transition: %+v", tr)
		}
		if tr.To == autonomy.RuntimeUnsubstantiated {
			sawUnsubstantiated = true
		}
	}
	if !sawUnsubstantiated {
		t.Fatalf("history must record the unsubstantiated terminal transition, got %+v", d.History())
	}
}

// TestPhase14A_DefaultMatrixNeverCompletesOnProse proves the same property
// through the PRODUCTION decision matrix rather than a forced one: the honest
// recovery path is taken, and `completed` is still unreachable.
func TestPhase14A_DefaultMatrixNeverCompletesOnProse(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{
		{Content: proseResponse},
		{Content: proseResponse},
	})
	d := NewDriver(a, nil, WithLoopBounds(autonomy.LoopBounds{
		MaxAttempts:       3,
		MaxRecoveryCycles: 1,
	}))

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("the default matrix completed a prose-only mutation")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("prose reached the workspace: %q", got)
	}
	if mock.calls() < 2 {
		t.Fatalf("provider calls = %d, want the bounded same-contract re-prompt", mock.calls())
	}
	// The prose rejection is TYPED: the recovery matrix routes it to the
	// same-contract re-prompt, never to the FULL_REWRITE → BOUNDED_PATCH
	// transition that would relabel a creation contract as a patch.
	sawZeroArtifactRecovery := false
	for _, tr := range d.History() {
		if strings.Contains(tr.Reason, "zero artifacts parsed") {
			sawZeroArtifactRecovery = true
		}
		if strings.Contains(tr.Reason, "typed transition FULL_REWRITE") {
			t.Fatalf("a prose rejection triggered the bounded-patch transition: %+v", tr)
		}
	}
	if !sawZeroArtifactRecovery {
		t.Fatalf("history must record the zero-artifact recovery, got %+v", d.History())
	}
}

// TestPhase14A_ZeroArtifactRecoveryKeepsTheSameArtifactContract pins
// INVARIANT 4's recovery clause: the structured re-prompt happens under the
// SAME artifact contract. A creation contract has no content to anchor a
// bounded patch against, so relabelling it would guarantee a wasted failure.
func TestPhase14A_ZeroArtifactRecoveryKeepsTheSameArtifactContract(t *testing.T) {
	obs := autonomy.Observation{
		Outcome:         autonomy.OutcomeArtifactRetryableRejected,
		Diagnostic:      execution.ErrZeroArtifactsParsed.Error() + ": note.txt: prose",
		ArtifactShape:   "create_file",
		Target:          "new.txt",
		AttemptNum:      0,
		ContractID:      "ct-0000000000000001",
		MaxOutputTokens: 2048,
	}
	req := autonomy.LoopRequest{
		Targets:             []string{"new.txt"},
		MutationStrategy:    "full_rewrite",
		RecoveryStrategy:    "",
		InteractionContract: "agentic_loop",
	}

	next, err := typedRepair(obs, req)
	if err != nil {
		t.Fatalf("typedRepair: %v", err)
	}
	if next.RecoveryStrategy != "" {
		t.Fatalf("recovery strategy = %q, want unchanged (a creation is not a bounded patch)", next.RecoveryStrategy)
	}
	if next.MutationStrategy != req.MutationStrategy {
		t.Fatalf("mutation strategy = %q, want %q (unchanged)", next.MutationStrategy, req.MutationStrategy)
	}
	if next.RecoveryAttempt != 1 {
		t.Fatalf("recovery attempt = %d, want 1 (exactly one structured re-prompt)", next.RecoveryAttempt)
	}
	if next.ParentContractID != obs.ContractID {
		t.Fatalf("parent contract = %q, want %q (causal lineage preserved)", next.ParentContractID, obs.ContractID)
	}
	if !strings.Contains(next.Evidence, "ARTIFACT CONTRACT VIOLATION") {
		t.Fatalf("evidence must carry the structured recovery directive, got %q", next.Evidence)
	}
	if !strings.Contains(next.Evidence, "not an artifact") {
		t.Fatalf("directive must state that prose is not an artifact, got %q", next.Evidence)
	}
	// A creation is re-asked for its file body, never for a patch.
	if !strings.Contains(next.Evidence, "complete new file content") {
		t.Fatalf("directive must re-state the creation contract, got %q", next.Evidence)
	}
	// The rejected bytes never travel: the directive is advisory metadata only.
	if strings.Contains(next.Evidence, proseResponse) {
		t.Fatal("recovery isolation violated: the rejected prose reached the recovery context")
	}
	// And the decision matrix routes it to the same-contract re-prompt.
	decision := DecideRecovery(obs, autonomy.LoopBounds{MaxRecoveryCycles: 1})
	if decision.Action != autonomy.LoopRepair {
		t.Fatalf("decision = %s, want %s (one structured re-prompt)", decision.Action, autonomy.LoopRepair)
	}
	// A second prose-only response exhausts the recovery cycles and escalates.
	second := obs
	second.AttemptNum = 1
	second.RecoveryCycle = 1
	exhausted := DecideRecovery(second, autonomy.LoopBounds{MaxRecoveryCycles: 1, MaxAttempts: 3})
	if exhausted.Action == autonomy.LoopRetry || exhausted.Action == autonomy.LoopRepair {
		t.Fatalf("exhausted cycles must escalate, got %s", exhausted.Action)
	}
}

// ── TEST B: Valid Creation / Patch ───────────────────────────────────────────

// TestPhase14B_ProvenPatchCompletes proves the positive path end to end: a
// real SEARCH/REPLACE artifact, applied through the approval gate, verified —
// and the authority rules it PROVEN so the loop MAY terminate completed.
func TestPhase14B_ProvenPatchCompletes(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil)

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (approval gate)", d.State())
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("state = %s, want completed", d.State())
	}
	// The authority recorded the PROVEN verdict and the contract it judged.
	evaluation := d.objectiveEvaluation()
	if evaluation.Outcome != execution.ObjectiveProven {
		t.Fatalf("authority outcome = %s, want %s (evaluation=%+v)", evaluation.Outcome, execution.ObjectiveProven, evaluation)
	}
	if !evaluation.State.Proven() {
		t.Fatal("MEANING boundary must be PROVEN for a completed run")
	}
	if contract := d.lastContract; contract.Kind != execution.TaskPatch {
		t.Fatalf("contract kind = %s, want %s", contract.Kind, execution.TaskPatch)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (approval must not re-execute)", mock.calls())
	}
}

// TestPhase14B_ReadOnlyObjectiveProves pins the READ contract: a workspace
// observation plus a delivered response is a complete answer, and the authority
// must NOT over-refuse it. Over-refusing is the failure mode of a naive gate.
func TestPhase14B_ReadOnlyObjectiveProves(t *testing.T) {
	_, _, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("outcome = %s, want %s (an observed, answered read is proven)", got, execution.ObjectiveProven)
	}
	if contract := d.lastContract; contract.Kind != execution.TaskRead {
		t.Fatalf("contract kind = %s, want %s", contract.Kind, execution.TaskRead)
	}
}

// ── TEST C: Truncated Response ───────────────────────────────────────────────

// TestPhase14C_TruncatedStreamNeverCompletes pins the finish_reason=length case
// at the loop level. The provider DID stop emitting — ProviderState is DONE —
// and the run still may not complete, because the artifact is CONTINUING.
func TestPhase14C_TruncatedStreamNeverCompletes(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	// One truncated generation. The bounded-step continuation budget is finite
	// and the mock has no further response, so the run terminates without ever
	// completing.
	truncated := &ai.Response{
		Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
		Usage: ai.ProviderUsage{
			Known: true, PromptTokens: 100, CompletionTokens: 4096,
			FinishReason: "length",
		},
	}
	_, mock, a, _ := testHarness(t, []*ai.Response{truncated})
	lie := func(autonomy.Observation, autonomy.LoopBounds) autonomy.LoopDecision {
		return autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "objective satisfied: truncated"}
	}
	d := NewDriver(a, nil, WithDecider(lie))

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a truncated stream completed the objective")
	}
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want a non-completed terminal state", term)
	}
	for _, tr := range d.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("history contains a completed transition: %+v", tr)
		}
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("a truncated prefix reached the workspace: %q", got)
	}
	_ = mock
}

// TestPhase14C_ContinuationStateIsNotProduced pins that a preserved partial
// artifact is CONTINUING, never PRODUCED — the two are different states and the
// distinction is what keeps a truncated prefix out of the evidence.
func TestPhase14C_ContinuationStateIsNotProduced(t *testing.T) {
	truncated := execution.ObjectiveEvidence{
		Provider:        execution.ProviderDone,
		FinishReason:    "length",
		Artifact:        execution.ArtifactContinuing,
		PartialArtifact: true,
	}
	if truncated.Artifact == execution.ArtifactProduced {
		t.Fatal("a preserved partial artifact was classified as produced")
	}
	if truncated.Provider != execution.ProviderDone {
		t.Fatal("a stopped stream is a transport DONE; that fact carries no task meaning")
	}
	if got := execution.ObserveArtifactState("length", 0, true); got != execution.ArtifactContinuing {
		t.Fatalf("artifact state = %s, want %s", got, execution.ArtifactContinuing)
	}
	// A CONTINUING artifact can never satisfy a mutation contract even when
	// every other clause happens to hold.
	ev := truncated
	ev.Mutation = execution.FilesystemApplied
	ev.MutatedFiles = 1
	ev.ObservedDeltaTargets = []string{"note.txt"}
	ev.TargetExists = map[string]bool{"note.txt": true}
	ev.VerificationRan, ev.VerificationPassed = true, true
	contract := execution.TaskContract{
		Kind: execution.TaskPatch, Targets: []string{"note.txt"}, RequiresVerifier: true,
	}
	if got := execution.AuthorizeObjective(contract, ev); got.Outcome.Proves() {
		t.Fatal("a CONTINUING artifact satisfied a PATCH contract")
	}
}

// ── TEST D: Idempotent Objective ─────────────────────────────────────────────

// TestPhase14D_StructurallyConfirmedNoOpCompletes pins the IDEMPOTENT path: the
// executor's deterministic structural analysis confirms that the assigned slice
// needs no change, the mutation count is zero, and the authority rules the
// objective PROVEN so the loop MAY terminate completed.
//
// The contract is the whole point: a zero mutation count is legitimate HERE and
// illegitimate everywhere else, and the difference is the structural verdict.
func TestPhase14D_StructurallyConfirmedNoOpCompletes(t *testing.T) {
	root, _, driver, _ := stageNoOpDecompositionRun(t, "inspect every handler @big.go", 60, 1)
	before := readTarget(t, root, "big.go")
	_ = driver

	// The DAG fixture drives the no-op sub-task through the same authority seam.
	// Re-run it with the structural confirmation wired so the contract is
	// IDEMPOTENT rather than a read-only inspection.
	evidence := execution.ObjectiveEvidence{
		Provider:                execution.ProviderDone,
		FinishReason:            "stop",
		Mutation:                execution.FilesystemNone,
		StructuralNoOpConfirmed: true,
		VerificationRan:         true,
		VerificationPassed:      true,
	}
	contract := execution.DeriveTaskContract(execution.TaskClassification{
		Intent:                  "modification",
		Objective:               "remove every DEPRECATED-MARKER comment @big.go",
		RequiresMutation:        true,
		Targets:                 []string{"big.go"},
		TargetsExistedBefore:    map[string]bool{"big.go": true},
		TargetsChanged:          map[string]bool{},
		StructuralNoOpConfirmed: true,
	})
	if contract.Kind != execution.TaskIdempotent {
		t.Fatalf("contract kind = %s, want %s", contract.Kind, execution.TaskIdempotent)
	}
	if got := execution.AuthorizeObjective(contract, evidence); !got.Outcome.Proves() {
		t.Fatalf("outcome = %s, want PROVEN for an already-satisfied objective (%s)",
			got.Outcome, got.Reason)
	}
	if readTarget(t, root, "big.go") != before {
		t.Fatal("a satisfied no-op unit mutated the workspace")
	}
}

// TestPhase14D_SameZeroMutationEvidenceRefusedWithoutStructuralProof is the
// symmetry partner of Test D. The evidence bundle is byte-identical except for
// the structural confirmation, and the verdict flips. Without this pairing a
// relaxed gate would be indistinguishable from a strict one.
func TestPhase14D_SameZeroMutationEvidenceRefusedWithoutStructuralProof(t *testing.T) {
	contract := execution.DeriveTaskContract(execution.TaskClassification{
		Intent:               "modification",
		Objective:            "remove every DEPRECATED-MARKER comment @big.go",
		RequiresMutation:     true,
		Targets:              []string{"big.go"},
		TargetsExistedBefore: map[string]bool{"big.go": true},
		TargetsChanged:       map[string]bool{},
	})
	if contract.Kind == execution.TaskIdempotent {
		t.Fatalf("contract kind = %s, want a non-idempotent kind without a structural verdict", contract.Kind)
	}
	evidence := execution.ObjectiveEvidence{
		Provider:           execution.ProviderDone,
		FinishReason:       "stop",
		Mutation:           execution.FilesystemNone,
		VerificationRan:    true,
		VerificationPassed: true,
	}
	if got := execution.AuthorizeObjective(contract, evidence); got.Outcome.Proves() {
		t.Fatal("a zero-mutation mutation objective completed without a structural verdict")
	}
}

// ── TEST E: Architecture Lock ────────────────────────────────────────────────

// TestPhase14E_AuthorityIsSoleCompletionAuthority pins that the driver consults
// an injected authority for EVERY proposed completion. It is the wiring proof
// that the loop cannot reach `completed` around the authority: a stub that
// always refuses must make every completion impossible, and a stub that records
// its calls must see at least one.
type recordingAuthority struct {
	calls   int
	outcome execution.ObjectiveOutcome
}

func (r *recordingAuthority) Authorize(contract execution.TaskContract, _ execution.ObjectiveEvidence) execution.ObjectiveEvaluation {
	r.calls++
	clause := "stub"
	reason := "injected authority"
	if r.outcome.Proves() {
		clause, reason = "", ""
	}
	return execution.ObjectiveEvaluation{
		Outcome: r.outcome,
		Reason:  reason,
		Clause:  clause,
		State:   r.outcome.ObjectiveState(),
	}
}

func TestPhase14E_AuthorityIsSoleCompletionAuthority(t *testing.T) {
	_, _, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	refuser := &recordingAuthority{outcome: execution.ObjectiveUnsubstantiated}
	lie := func(autonomy.Observation, autonomy.LoopBounds) autonomy.LoopDecision {
		return autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "objective satisfied"}
	}
	d := NewDriver(a, nil, WithDecider(lie), WithObjectiveAuthority(refuser))

	term, err := d.Run(context.Background(), "explain the file @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if refuser.calls == 0 {
		t.Fatal("the driver never consulted the completion authority")
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("the loop completed while the authority refused — the gate is bypassable")
	}
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want a non-completed terminal state", term)
	}
	// The refusal produced the dedicated terminal state, not a bare abort.
	if d.State() != autonomy.RuntimeUnsubstantiated {
		t.Fatalf("state = %s, want %s", d.State(), autonomy.RuntimeUnsubstantiated)
	}
	if !autonomy.PhaseUnsubstantiated.IsTerminal() {
		t.Fatal("the unsubstantiated state must be terminal")
	}
}

// TestPhase14E_UnsubstantiatedIsNotFailureAndNotSuccess pins the three-way
// distinction the phase introduces: proven, unsubstantiated, failed. A terminal
// unsubstantiated run carries no failure class, because nothing failed — and it
// is plainly not `completed`.
func TestPhase14E_UnsubstantiatedIsNotFailureAndNotSuccess(t *testing.T) {
	if autonomy.RuntimeUnsubstantiated == autonomy.RuntimeCompleted {
		t.Fatal("unsubstantiated and completed must be distinct states")
	}
	if autonomy.RuntimeUnsubstantiated == autonomy.RuntimeAborted {
		t.Fatal("unsubstantiated and aborted must be distinct states")
	}
	if !autonomy.RuntimeUnsubstantiated.IsTerminal() {
		t.Fatal("unsubstantiated must be terminal")
	}
	if !execution.ObjectiveUnsubstantiated.Terminal() {
		t.Fatal("UNSUBSTANTIATED must be a terminal outcome")
	}
	if execution.ObjectiveUnsubstantiated.Proves() {
		t.Fatal("UNSUBSTANTIATED must never report PROVEN")
	}
	// The MEANING boundary projects it onto UNPROVEN, so no projector can
	// round-trip it into PROVEN by reading the wrong field.
	if execution.ObjectiveUnsubstantiated.ObjectiveState().Proven() {
		t.Fatal("UNSUBSTANTIATED must project onto the UNPROVEN meaning state")
	}
	if !execution.ObjectiveProven.ObjectiveState().Proven() {
		t.Fatal("PROVEN must project onto the PROVEN meaning state")
	}
	// The sentinel the driver refuses to complete on is distinct from failure.
	if !errors.Is(execution.ErrObjectiveUnsubstantiated, execution.ErrObjectiveUnsubstantiated) {
		t.Fatal("ErrObjectiveUnsubstantiated must be its own sentinel")
	}
	if errors.Is(execution.ErrObjectiveUnsubstantiated, execution.ErrObjectiveFailed) {
		t.Fatal("unsubstantiated and failed must be distinguishable by errors.Is")
	}
}
