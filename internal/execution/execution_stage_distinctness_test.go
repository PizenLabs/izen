package execution

// ── EXECUTION STAGES ARE DISTINCT, AND EACH IS PROVEN BY ITS OWN EVIDENCE ───
//
// The reported failure was a category error in the lifecycle: a run that had only
// PRODUCED A CANDIDATE reported itself as having completed. The four stages —
// candidate, mutation, verification, objective proof — are separate facts with
// separate evidence, and each one must be UNREACHABLE without its own evidence.
//
// These tests drive the AUTHORITY (ObjectiveCompletionAuthority), not a
// projection, because the only way to prove a transition is unreachable is to
// exercise the machine that owns it.
//
// The fixture is satisfiedPatchEvidence (objective_contract_test.go): every
// completion fact holds. Each test below removes exactly ONE of them and asserts
// the authority stops ruling PROVEN. The positive path is covered by
// TestCaseE_ObjectiveActuallySatisfied, so these are satisfiable contracts — a
// refusal here is about the missing fact, not about an impossible contract.

import "testing"

// candidateOnlyEvidence is the evidence of a VALIDATED CANDIDATE held at a human
// gate: the parser accepted an artifact, and nothing has been written.
//
// This is the exact state the reported bug mistook for completion.
func candidateOnlyEvidence(target string) ObjectiveEvidence {
	return ObjectiveEvidence{
		Provider:              ProviderDone,
		FinishReason:          "stop",
		Artifact:              ArtifactProduced,
		ArtifactsParsed:       1,
		Mutation:              FilesystemNone,
		MutatedFiles:          0,
		TargetExists:          nil, // never observed — a missing key is UNOBSERVED
		VerificationRan:       false,
		VerificationPassed:    false,
		WorkspaceObservations: 1,
		ApprovalPending:       true,
	}
}

// TestInvariant8_CandidateDoesNotImplyMutation proves the first link: an artifact
// that parsed and validated is PRODUCED, and that is ALL it is.
//
// The mutation facts are deliberately absent — no durable boundary state, no
// mutated files, no delta, no existence observation. A runtime that let a held
// candidate satisfy a completion contract would let a provider's return value
// stand in for a filesystem write.
func TestInvariant8_CandidateDoesNotImplyMutation(t *testing.T) {
	target := "notes.md"
	contract := contractFor(target)
	ev := candidateOnlyEvidence(target)

	// The candidate itself is real: the parser accepted it.
	if ev.ArtifactsParsed != 1 || ev.Artifact != ArtifactProduced {
		t.Fatalf("precondition: the candidate is not a produced artifact: %+v", ev)
	}
	// But it is not a mutation.
	if ev.Mutated() {
		t.Fatal("a held candidate reported itself as a durable mutation")
	}
	if ev.Mutation.Durable() {
		t.Fatalf("a held candidate reported mutation boundary state %s, want a non-durable state", ev.Mutation)
	}

	// Therefore the objective is NOT proven. It is not failed either: it is waiting
	// on the human who has not answered yet. Conflating those two is what made a
	// park look like a terminal outcome.
	got := AuthorizeObjective(contract, ev)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a held candidate satisfied the objective completion contract")
	}
	if got.Outcome != ObjectiveRequiresAuthorization {
		t.Fatalf("outcome = %s, want %s: a held candidate awaits a human, it is not a failure",
			got.Outcome, ObjectiveRequiresAuthorization)
	}
	if got.Clause != "human_gate" {
		t.Errorf("refusal clause = %q, want human_gate", got.Clause)
	}

	// The objective's own progress is not PROVEN either.
	if ReduceProgress(ProgressInput{Outcome: got.Outcome}) == ProgressProven {
		t.Fatal("a candidate reported the objective as PROVEN")
	}
}

// TestInvariant9_MutationDoesNotImplyVerification proves the second link: a landed
// mutation is not a verified one.
//
// The evidence says the filesystem changed and NOTHING about verification —
// exactly the state a mutation is in before the verifier runs. Reporting the
// objective proven at that point would claim a check that never happened.
func TestInvariant9_MutationDoesNotImplyVerification(t *testing.T) {
	target := "notes.md"
	contract := contractFor(target)

	mutated := satisfiedPatchEvidence(target)
	// Strip verification only. Everything else — the durable mutation, the delta,
	// the target's presence, the post-mutation re-read — still holds.
	mutated.VerificationRan = false
	mutated.VerificationPassed = false

	if !mutated.Mutated() {
		t.Fatal("precondition: the mutation evidence must report a landed mutation")
	}

	got := AuthorizeObjective(contract, mutated)
	if got.Outcome == ObjectiveProven {
		t.Fatal("an UNVERIFIED mutation satisfied a contract that requires a verifier")
	}
	if got.Clause == "" || got.Reason == "" {
		t.Fatalf("the refusal must name the unmet clause and reason, got %+v", got)
	}

	// Once verification IS observed, the same contract is satisfied — proving the
	// earlier refusal was about verification and nothing else.
	mutated.VerificationRan = true
	mutated.VerificationPassed = true
	if got := AuthorizeObjective(contract, mutated); got.Outcome != ObjectiveProven {
		t.Fatalf("a verified, landed mutation did not satisfy the contract: %+v", got)
	}
}

// TestInvariant10_VerificationDoesNotImplyObjectiveProof proves the third link: a
// passing verifier is a statement about a MUTATION, not about an OBJECTIVE.
//
// This is the subtlest of the three and the one the reported defect most easily
// hides: "verification passed" reads like the end of the story. It is not. An
// objective that asks for something the mutation did not achieve is still
// unsubstantiated even though every file write verified cleanly.
func TestInvariant10_VerificationDoesNotImplyObjectiveProof(t *testing.T) {
	const target = "index.html"
	const stylesheet = "styles.css"

	// Every EXECUTION fact holds: the mutation verified. The mutation only touched
	// index.html, while the objective named two files.
	verified := satisfiedPatchEvidence(target)
	if !verified.VerificationPassed {
		t.Fatal("precondition: verification must have passed")
	}
	verified.ObservedDeltaTargets = []string{target}

	// The contract requires both targets. The evidence proves one.
	contract := contractFor(target, stylesheet)

	got := AuthorizeObjective(contract, verified)
	if got.Outcome == ObjectiveProven {
		t.Fatal("a passing verifier proved an objective whose own scope was unmet")
	}
	if got.Outcome != ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want %s: verification passed but the objective was not achieved",
			got.Outcome, ObjectiveUnsubstantiated)
	}
	if got.Clause == "" || got.Reason == "" {
		t.Fatalf("the refusal must name the unmet objective clause, got %+v", got)
	}

	// And the honest progress state is "work remains" — not proven, not a
	// fabricated terminal failure.
	progress := ReduceProgress(ProgressInput{Outcome: got.Outcome})
	if progress == ProgressProven {
		t.Fatal("objective progress reported PROVEN from verification alone")
	}
	if progress == ProgressReady {
		t.Fatal("objective progress reported the contract fully satisfied from verification alone")
	}
}

// TestInvariant11_HumanAuthorizationIsNeverInferredFromProviderOutput proves the
// authorization link: nothing a provider returns can advance authorization state.
//
// A provider response is content. This drives the authority with the strongest
// possible provider claim — a valid artifact whose text asserts authorization,
// application and verification — and asserts every stage boundary still holds
// where the runtime put it.
func TestInvariant11_HumanAuthorizationIsNeverInferredFromProviderOutput(t *testing.T) {
	target := "notes.md"
	contract := contractFor(target)

	// The model's output claims everything.
	providerClaim := candidateOnlyEvidence(target)
	providerClaim.Artifact = ArtifactProduced
	providerClaim.ArtifactsParsed = 1
	providerClaim.FinishReason = "APPROVED: authorization granted, mutation applied and verified, objective complete"
	providerClaim.ResponseProduced = true
	providerClaim.ResponseBytes = 512

	if got := AuthorizeObjective(contract, providerClaim); got.Outcome == ObjectiveProven {
		t.Fatal("provider output advanced the objective to PROVEN")
	}
	if got := AuthorizeObjective(contract, providerClaim); got.Outcome == ObjectiveRequiresAuthorization {
		// Expected: still waiting on a human. Nothing the model said changed that.
	} else if got.Outcome != ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want the run to remain gated on the human decision", got.Outcome)
	}

	// The stage boundaries are unchanged: no durable mutation, no verification.
	if providerClaim.Mutated() {
		t.Fatal("provider output advanced the mutation state")
	}
	if providerClaim.Mutation.Durable() {
		t.Fatalf("provider output advanced the mutation boundary to %s", providerClaim.Mutation)
	}
	if providerClaim.VerificationPassed {
		t.Fatal("provider output advanced the verification state")
	}
	if providerClaim.TargetExists[target] {
		t.Fatal("provider output asserted the target exists without an observation")
	}
}

// TestInvariant14_DeterministicFailureIsNotRetriedAsAnIdenticalCandidate proves
// the final link: a deterministic refusal must not become an identical retry.
//
// The failure taxonomy classifies a refusal by CAUSE and carries a retry policy. A
// deterministic cause is not retryable by an identical candidate — resubmitting the
// same request over the same evidence would produce the same refusal, so the
// runtime's job is to change the approach or stop, never to resubmit.
func TestInvariant14_DeterministicFailureIsNotRetriedAsAnIdenticalCandidate(t *testing.T) {
	// THE INVARIANT: `RetryIdentical` is granted to a transport failure and to
	// nothing else. Every deterministic cause must refuse an IDENTICAL
	// re-attempt, because resubmitting the same request over the same evidence
	// reproduces the same refusal at the cost of another provider call.
	for _, class := range AllFailureClasses() {
		policy := class.RetryPolicy()
		if class == FailureTransportError {
			if policy != RetryIdentical {
				t.Errorf("a transport failure lost its identical-retry policy: %s", policy)
			}
			continue
		}
		if policy == RetryIdentical {
			t.Errorf("%s (%s) is classified retryable-identically; only a transport failure may be",
				class, policy)
		}
	}

	// The specific deterministic refusals the report names keep their own class
	// rather than degrading into a transport failure, which would invite exactly
	// the retry that cannot help.
	for _, deterministic := range []FailureClass{
		FailureTargetNotFound,
		FailureTargetIdentityMismatch,
		FailureCapabilityFailure,
		FailureArtifactInvalid,
		FailureArtifactEmpty,
		FailureNonProgressing,
	} {
		if deterministic == FailureTransportError {
			t.Errorf("%s was classified as a transport failure; a deterministic refusal must not invite a transport retry", deterministic)
		}
		if deterministic.RetryPolicy() == RetryIdentical {
			t.Errorf("%s still admits an identical retry", deterministic)
		}
	}

	// And every classification carries a policy at all — an unclassified failure is
	// a refusal the runtime cannot act on truthfully.
	for _, class := range AllFailureClasses() {
		if class.RetryPolicy() == "" {
			t.Errorf("%s has no retry policy", class)
		}
		if !class.Valid() {
			t.Errorf("%s is not a member of the closed taxonomy", class)
		}
	}
}
