package execution

// ── CANDIDATE LINEAGE AT THE MUTATION BOUNDARY ──────────────────────────────
//
// The full chain is
//
//	ComputationID → ArtifactCandidate → MutationProposal → Authorization → Mutation
//
// and this file pins the last two links: a MutationAuthorization may be bound to
// ONE candidate identity, and the mutation boundary refuses a token that names a
// different one. Without that binding an authorization is a bare PERMISSION — it
// says which files may change, never WHICH artifact may be written — so an
// approval obtained for one computation would also read as an approval for a
// different, superseded or fabricated one.
//
// The tests reuse the production identity (the held patch id and the executor's
// own pending map). No second lineage system is introduced.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

func lineageExecutor(t *testing.T, root string, p ai.Provider, bus *events.Bus) *RuntimeExecutor {
	t.Helper()
	x := NewRuntimeExecutor(root, config.Default(), p, bus, "")
	x.SetVerifier(trivialVerifier(root))
	return x
}

func lineageWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	seedFile(t, root, "index.html", original)
	return root, original
}

// TestCandidateLineage_UnboundAuthorizationAppliesItsCandidate pins backward
// compatibility: a token with no CandidateID is the historical permission and
// still applies the held candidate.
func TestCandidateLineage_UnboundAuthorizationAppliesItsCandidate(t *testing.T) {
	root, original := lineageWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	x := lineageExecutor(t, root, &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
	}}, bus)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(), ExpiresAt: time.Now().Add(time.Hour),
	})

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("execute: %v", err)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("an unbound authorization must still apply its candidate: %v", err)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("outcome = %s, want changed", apr.Proof.Outcome)
	}
}

// TestCandidateLineage_BoundAuthorizationRefusesAForeignCandidate is the core
// assertion: a token issued for candidate A cannot apply candidate B, and the
// refusal leaves B intact so the operator can still authorize it properly.
func TestCandidateLineage_BoundAuthorizationRefusesAForeignCandidate(t *testing.T) {
	root, original := lineageWorkspace(t)
	seedFile(t, root, "styles.css", "body{color:#000}\n")
	bus := events.NewBus(events.DefaultBufferSize)
	x := lineageExecutor(t, root, &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
		validPatchResponse("body{color:#000}\n", "body{color:#fff}\n"),
	}}, bus)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(), ExpiresAt: time.Now().Add(time.Hour),
	})

	index, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil || index.PendingPatchID == "" {
		t.Fatalf("index execute: %v", err)
	}
	css, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "restyle styles.css", Target: "styles.css",
	})
	if err != nil || css.PendingPatchID == "" {
		t.Fatalf("css execute: %v", err)
	}

	// A token bound to the INDEX candidate.
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:          authorization.NewAuthorizationID(),
		CandidateID: index.PendingPatchID,
		ExpiresAt:   time.Now().Add(time.Hour),
	})

	// It cannot apply the CSS candidate.
	if _, err := x.Approve(context.Background(), css.PendingPatchID); err == nil {
		t.Fatal("an authorization bound to candidate A applied candidate B")
	} else if !errors.Is(err, authorization.ErrAuthorizationCandidateMismatch) {
		t.Fatalf("refusal = %v, want a candidate-identity mismatch", err)
	}
	// The refusal must be side-effect free: B is still held and still applies the
	// ORIGINAL bytes.
	if !x.CandidateHeld(css.PendingPatchID) {
		t.Fatal("a lineage refusal consumed the candidate it refused")
	}
	got, err := os.ReadFile(filepath.Join(root, "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "body{color:#000}\n" {
		t.Fatalf("a refused lineage wrote to the workspace: %q", string(got))
	}
	// And it CAN apply the candidate it was issued for.
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:          authorization.NewAuthorizationID(),
		CandidateID: css.PendingPatchID,
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if _, err := x.Approve(context.Background(), css.PendingPatchID); err != nil {
		t.Fatalf("a matching authorization must apply its candidate: %v", err)
	}
}

// TestCandidateLineage_ABoundAuthorizationRefusesASupersededCandidate joins the
// two halves of the invariant: lineage binding makes supersession total. A token
// opened for a candidate whose computation was superseded cannot be used to apply
// anything, because the superseded identity is gone.
func TestCandidateLineage_ABoundAuthorizationRefusesASupersededCandidate(t *testing.T) {
	root, original := lineageWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	x := lineageExecutor(t, root, &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
		exhaustedTruncationResponse(),
	}}, bus)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(), ExpiresAt: time.Now().Add(time.Hour),
	})

	first, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil || first.PendingPatchID == "" {
		t.Fatalf("attempt 1: %v", err)
	}
	stale := first.PendingPatchID

	// A bound authorization for the stale candidate, issued BEFORE the successor.
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(), CandidateID: stale, ExpiresAt: time.Now().Add(time.Hour),
	})

	// The successor computation exhausts its output budget.
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	}); err == nil {
		t.Fatal("attempt 2 must fail at the output gate")
	}

	if _, err := x.Approve(context.Background(), stale); err == nil {
		t.Fatal("an authorization for a superseded candidate applied it")
	}
	got, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("a superseded candidate was applied:\n%s", got)
	}
}

// TestCandidateHeldIsFalseForUnknownAndEmptyIdentities pins the freshness read's
// contract: an unknown, empty or never-created identity is never "held". A gate
// that treated "" as held would let an approval boundary with no candidate pass.
func TestCandidateHeldIsFalseForUnknownAndEmptyIdentities(t *testing.T) {
	root, original := lineageWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	x := lineageExecutor(t, root, &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
	}}, bus)

	for _, id := range []string{"", "p1", "index.html", "run-1-patch-99"} {
		if x.CandidateHeld(id) {
			t.Errorf("CandidateHeld(%q) = true for an identity that was never held", id)
		}
	}
	var nilExec *RuntimeExecutor
	if nilExec.CandidateHeld("anything") {
		t.Error("CandidateHeld on a nil executor must be false")
	}

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !x.CandidateHeld(res.PendingPatchID) {
		t.Fatalf("CandidateHeld(%s) = false for a freshly held candidate", res.PendingPatchID)
	}
	if _, err := x.Reject(context.Background(), res.PendingPatchID, "not wanted"); err != nil {
		t.Fatal(err)
	}
	if x.CandidateHeld(res.PendingPatchID) {
		t.Fatalf("CandidateHeld(%s) = true after the candidate was rejected", res.PendingPatchID)
	}
}
