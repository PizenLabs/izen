package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// ── FAILED / SUPERSEDED COMPUTATION MUST NOT PRODUCE AN EXECUTABLE CANDIDATE ──
//
// The absolute invariant (spec §14):
//
//	FAILED / EXHAUSTED / INTERRUPTED / SUPERSEDED computation
//	MUST NOT produce an executable MutationCandidate
//
// These tests pin the SUPERSEDED half of it, which is the half that reaches the
// mutation boundary. An approval-held candidate is derived from ONE computation.
// When a later computation is dispatched for the same file, the earlier claim
// on that file ends — whether the later one succeeds, fails, or exhausts its
// output budget. Human approval cannot resurrect it: approval answers "may this
// authorized operation occur?", never "is this abandoned computation a valid
// artifact?".

func supersededExecutor(t *testing.T, root string, p ai.Provider, bus *events.Bus) *RuntimeExecutor {
	t.Helper()
	x := NewRuntimeExecutor(root, config.Default(), p, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

func seedFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validPatchResponse(original, replacement string) *ai.Response {
	return &ai.Response{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n" + replacement + ">>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 20, FinishReason: "stop"},
	}
}

// exhaustedResponse is finish_reason=length — the canonical OUTPUT_EXHAUSTED
// computation. It carries partial bytes, which is exactly what must never
// become executable.
func exhaustedTruncationResponse() *ai.Response {
	return &ai.Response{
		Content: "<<<<<<< SEARCH\n<html><body><p>trunc",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 1024, FinishReason: "length"},
	}
}

func TestSupersededCandidate_CannotSurviveAFailedSuccessorComputation(t *testing.T) {
	root := t.TempDir()
	original := "<html><body><p>one</p><p>two</p></body></html>\n"
	seedFile(t, root, "index.html", original)

	bus := events.NewBus(events.DefaultBufferSize)
	// Attempt 1 succeeds and holds a candidate. Attempt 2 exhausts its output.
	mock := &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
		exhaustedTruncationResponse(),
	}}
	x := supersededExecutor(t, root, mock, bus)

	first, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	stalePatchID := first.PendingPatchID
	if stalePatchID == "" {
		t.Fatal("attempt 1 must hold a candidate at the approval gate")
	}

	// Attempt 2 is a NEW computation over the same target and it FAILS at the
	// output gate.
	second, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err == nil {
		t.Fatalf("attempt 2 must fail at the output gate, got outcome=%s", second.Proof.Outcome)
	}
	if second.PendingPatchID != "" {
		t.Fatalf("a failed computation produced an approval-held candidate %q", second.PendingPatchID)
	}

	// The superseded candidate is NOT executable any more.
	for _, id := range x.PendingPatchIDs() {
		if id == stalePatchID {
			t.Fatalf("candidate %q from the superseded computation is still held", id)
		}
	}
	if _, err := x.Approve(context.Background(), stalePatchID); err == nil {
		t.Fatal("Approve accepted a candidate whose producing computation was superseded")
	}

	// And nothing was written.
	got, readErr := os.ReadFile(filepath.Join(root, "index.html"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("workspace was mutated by a superseded candidate:\n%s", got)
	}
}

// The candidate is drained by the DISPATCH of the successor, not by its
// outcome: a successor that itself holds a valid candidate must not leave two
// live candidates for one file competing for a single approval.
func TestSupersededCandidate_IsDrainedByDispatchNotByOutcome(t *testing.T) {
	root := t.TempDir()
	original := "<html><body><p>one</p><p>two</p></body></html>\n"
	seedFile(t, root, "index.html", original)

	bus := events.NewBus(events.DefaultBufferSize)
	replacement := "<html><body><p>three</p></body></html>\n"
	mock := &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
		validPatchResponse(original, replacement),
	}}
	x := supersededExecutor(t, root, mock, bus)

	first, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	stalePatchID := first.PendingPatchID

	second, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if second.PendingPatchID == "" {
		t.Fatal("attempt 2 must hold its own candidate")
	}
	if second.PendingPatchID == stalePatchID {
		t.Fatal("attempt 2 re-used attempt 1's candidate identity")
	}

	ids := x.PendingPatchIDs()
	if len(ids) != 1 || ids[0] != second.PendingPatchID {
		t.Fatalf("exactly the live computation's candidate must remain held, got %v", ids)
	}
	if _, err := x.Approve(context.Background(), stalePatchID); err == nil {
		t.Fatal("Approve accepted the superseded candidate")
	}

	// The live candidate is still executable, and applies the LIVE artifact.
	apr, err := x.Approve(context.Background(), second.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve(live): %v", err)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("live candidate outcome = %s, want changed", apr.Proof.Outcome)
	}
	got, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != replacement {
		t.Fatalf("the applied artifact is not the live computation's:\n%s", got)
	}
}

// Scope discipline: a candidate held for an UNRELATED file is a different
// approval and must survive. Draining it would destroy a legitimate
// concurrent approval, which is exactly as wrong as letting a stale one live.
func TestSupersededCandidate_DoesNotTouchUnrelatedTargets(t *testing.T) {
	root := t.TempDir()
	indexOriginal := "<html><body><p>one</p><p>two</p></body></html>\n"
	cssOriginal := "body{color:#000}\n"
	seedFile(t, root, "index.html", indexOriginal)
	seedFile(t, root, "styles.css", cssOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{
		validPatchResponse(indexOriginal, "<html><body><p>one</p></body></html>\n"),
		validPatchResponse(cssOriginal, "body{color:#fff}\n"),
		validPatchResponse(indexOriginal, "<html><body><p>three</p></body></html>\n"),
	}}
	x := supersededExecutor(t, root, mock, bus)

	indexHeld, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatalf("index attempt: %v", err)
	}
	cssHeld, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "restyle styles.css", Target: "styles.css",
	})
	if err != nil {
		t.Fatalf("css attempt: %v", err)
	}
	// A third computation over index.html supersedes ONLY the index candidate.
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	}); err != nil {
		t.Fatalf("second index attempt: %v", err)
	}

	if _, err := x.Approve(context.Background(), indexHeld.PendingPatchID); err == nil {
		t.Fatal("the superseded index candidate was still executable")
	}
	if _, err := x.Approve(context.Background(), cssHeld.PendingPatchID); err != nil {
		t.Fatalf("the unrelated css candidate was destroyed by supersession: %v", err)
	}
}

// A read-only request cannot become a mutation by naming a file: it is refused
// at the interaction-contract ceiling BEFORE it can supersede anything, and the
// refusal leaves the live candidate untouched.
func TestSupersededCandidate_ReadOnlyRequestIsRefusedAndDisturbsNothing(t *testing.T) {
	root := t.TempDir()
	original := "<html><body><p>one</p><p>two</p></body></html>\n"
	seedFile(t, root, "index.html", original)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{
		validPatchResponse(original, "<html><body><p>one</p></body></html>\n"),
	}}
	x := supersededExecutor(t, root, mock, bus)

	held, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "shrink index.html", Target: "index.html",
	})
	if err != nil {
		t.Fatalf("mutation attempt: %v", err)
	}

	// A conversational request that names a file is a QUESTION, not a mutation
	// request. It must not resolve into a file mutation, and must not take the
	// held candidate down with it.
	before := mock.calls()
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "ask", Prompt: "what does index.html do?", Target: "index.html",
	}); err == nil {
		t.Fatal("a read-only interaction contract was allowed to resolve a FILE_MUTATE")
	}
	if got := mock.calls(); got != before {
		t.Fatalf("the refused read-only request still invoked the provider %d time(s)", got-before)
	}

	ids := x.PendingPatchIDs()
	if len(ids) != 1 || ids[0] != held.PendingPatchID {
		t.Fatalf("the refused request disturbed a held candidate: %v", ids)
	}
	if _, err := x.Approve(context.Background(), held.PendingPatchID); err != nil {
		t.Fatalf("the held candidate became unapprovable: %v", err)
	}
}
