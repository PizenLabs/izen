package execution

// ── AUTHORIZATION SCOPE: consumption boundary ────────────────────────────────
//
// OBSERVED: after an approved mutation, verification failed with
// "authorization token … is single-use and has already been consumed" on the
// second verification command.
//
// ROOT CAUSE: one single-use token was shared by two capability families — the
// file write and every shell verification step — and the shell runner consumed
// it per command, so the first verification step starved the rest.
//
// CORRECTION: a grant minted for a mutation OPERATION carries
// ScopeMutationOperation and is consumed once, at the operation's terminal
// state; the runner consumes only capability-invocation grants. Single-use is
// preserved — it is scoped, not weakened.

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// TestMutationOperationGrantNotConsumedByRunner pins the consumption boundary
// directly: one mutation-operation grant authorizes many guarded invocations.
func TestMutationOperationGrantNotConsumedByRunner(t *testing.T) {
	runner := NewRunner(t.TempDir(), false, false)
	runner.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		SingleUse: true,
		Scope:     authorization.ScopeMutationOperation,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	for i := 1; i <= 3; i++ {
		if _, err := runner.Run(t.Context(), "true"); err != nil {
			t.Fatalf("mutation-operation grant refused invocation %d: %v", i, err)
		}
	}
}

// TestCapabilityInvocationGrantRemainsSingleUse pins that the correction did NOT
// make tokens reusable: a capability-invocation grant is still consumed by the
// first guarded invocation.
func TestCapabilityInvocationGrantRemainsSingleUse(t *testing.T) {
	runner := NewRunner(t.TempDir(), false, false)
	runner.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		SingleUse: true,
		Scope:     authorization.ScopeCapabilityInvocation,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if _, err := runner.Run(t.Context(), "true"); err != nil {
		t.Fatalf("first invocation refused: %v", err)
	}
	if _, err := runner.Run(t.Context(), "true"); err == nil {
		t.Fatal("second invocation succeeded; a single-use capability grant was made reusable")
	}
}

// TestMutationOperationGrantSurvivesMultiStepVerification is the end-to-end
// regression: a two-step verifier runs to completion under one single-use
// mutation-operation grant and is not starved by its own first command.
func TestMutationOperationGrantSurvivesMultiStepVerification(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := testExecutor(t, root, mock, bus)

	v := NewVerifier(root)
	v.SetCustomSteps([]VerificationStep{
		{Name: "step-one", Command: "true", Optional: false},
		{Name: "step-two", Command: "true", Optional: false},
	})
	x.SetVerifier(v)

	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		SingleUse: true,
		Scope:     authorization.ScopeMutationOperation,
		ExpiresAt: time.Now().Add(time.Hour),
	})

	ctx := context.Background()
	res, err := x.Execute(ctx, ExecuteRequest{
		RequestID: "r-verify-scope",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	apr, err := x.Approve(ctx, res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !apr.Verification.Passed {
		t.Fatalf("verification did not pass under one mutation-operation grant: %+v", apr.Verification)
	}
	if len(apr.Verification.Results) != 2 {
		t.Fatalf("verification steps run = %d, want 2 (multi-step verifier starved)", len(apr.Verification.Results))
	}
	for _, r := range apr.Verification.Results {
		if !r.Passed {
			t.Errorf("step %q did not pass: %s", r.Step.Name, r.Error)
		}
	}
}
