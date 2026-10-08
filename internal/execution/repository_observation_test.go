package execution

// ── REPOSITORY OBSERVATION EVIDENCE (targetless investigation) ──────────────
//
// A targetless read-only investigation must be able to prove its objective from
// REAL repository evidence. These tests pin the two halves of that contract:
//
//  1. repository observation SATISFIES a read/review observation obligation;
//  2. repository observation NEVER substitutes for a mutation — it grants no
//     authority and satisfies no mutation clause.

import "testing"

func readContract() TaskContract {
	return DeriveTaskContract(TaskClassification{
		Intent:           "investigation",
		Objective:        "investigate why the user endpoint fails",
		RequiresMutation: false,
	})
}

func TestRepositoryObservation_ProvesReadObjective(t *testing.T) {
	contract := readContract()
	if contract.Kind != TaskRead && contract.Kind != TaskReview {
		t.Fatalf("contract kind = %s, want READ or REVIEW", contract.Kind)
	}
	if !contract.RequiresObservation {
		t.Fatal("a read contract must require a workspace observation")
	}

	authority := NewObjectiveCompletionAuthority()

	// No observation of any kind → not proven. An answer alone is not evidence
	// that the runtime looked at the repository.
	blind := ObjectiveEvidence{
		Provider:         ProviderDone,
		Artifact:         ArtifactNone,
		Mutation:         FilesystemNone,
		ResponseProduced: true,
		ResponseBytes:    42,
	}
	if got := authority.Evaluate(contract, blind); got != ObjectiveUnsubstantiated {
		t.Fatalf("blind answer outcome = %s, want UNSUBSTANTIATED", got)
	}

	// Declared-target read → proven (pre-existing behaviour).
	read := blind
	read.WorkspaceObservations = 1
	if got := authority.Evaluate(contract, read); got != ObjectiveProven {
		t.Fatalf("declared-target read outcome = %s, want PROVEN", got)
	}

	// Repository observation → proven. This is the targetless-investigation
	// capability: the runtime observed the repository and the model reasoned
	// from that observed material.
	repo := blind
	repo.RepositoryObservations = 3
	if got := authority.Evaluate(contract, repo); got != ObjectiveProven {
		t.Fatalf("repository-observed outcome = %s, want PROVEN", got)
	}
}

func TestRepositoryObservation_NeverAuthorizesMutation(t *testing.T) {
	// A PATCH contract with a rich repository observation but no durable
	// mutation must remain UNSUBSTANTIATED: evidence is not authority.
	contract := DeriveTaskContract(TaskClassification{
		Intent:               "modification",
		Objective:            "fix the bug in @handler.go",
		RequiresMutation:     true,
		Targets:              []string{"handler.go"},
		TargetsExistedBefore: map[string]bool{"handler.go": true},
	})
	if !contract.RequiresMutation() {
		t.Fatalf("contract kind = %s, want a mutation contract", contract.Kind)
	}

	evidence := ObjectiveEvidence{
		Provider:               ProviderDone,
		Artifact:               ArtifactNone,
		Mutation:               FilesystemNone, // nothing was applied
		RepositoryObservations: 12,
		WorkspaceObservations:  12,
		ResponseProduced:       true,
		ResponseBytes:          42,
	}
	got := NewObjectiveCompletionAuthority().Evaluate(contract, evidence)
	if got == ObjectiveProven {
		t.Fatal("repository observation authorized a mutation it never performed")
	}
}
