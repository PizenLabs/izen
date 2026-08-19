package autonomy

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestAdapter_ResolveReadOnly asserts a read-only objective resolves to a real
// target set (never ambiguous, never guessed).
func TestAdapter_ResolveReadOnly(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	res := a.Resolve("explain the file @note.txt")
	if res.Ambiguous {
		t.Fatalf("read-only resolution marked ambiguous: %+v", res)
	}
	if len(res.Targets) != 1 || res.Targets[0] != "note.txt" {
		t.Fatalf("targets = %v, want [note.txt]", res.Targets)
	}
}

// TestAdapter_ResolveClarification asserts an unresolvable target surfaces as
// an ambiguous resolution with the raw target as the human option — before any
// execution.
func TestAdapter_ResolveClarification(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	res := a.Resolve("change @missing.txt to something")
	if !res.Ambiguous {
		t.Fatalf("unresolvable target not marked ambiguous: %+v", res)
	}
	if len(res.Targets) != 0 {
		t.Fatalf("unresolvable target leaked a target set: %v", res.Targets)
	}
	if len(res.Options) == 0 {
		t.Fatal("clarification must surface candidate options for the human")
	}
}

// TestAdapter_ExecuteReadOnly maps a read-only execution onto the canonical
// completed outcome.
func TestAdapter_ExecuteReadOnly(t *testing.T) {
	_, mock, a, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{
		Prompt: "explain the file @note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if obs.Outcome != autonomy.OutcomeCompleted {
		t.Fatalf("outcome = %s, want completed", obs.Outcome)
	}
	if obs.ClarificationRequired {
		t.Fatal("read-only execution must not demand clarification")
	}
	if obs.RequestID == "" {
		t.Fatal("observation must carry the authoritative request id")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
}

// TestAdapter_ExecuteMutationParksAtApproval asserts a targeted mutation
// execution stops at the approval gate with the authoritative patch id and the
// canonical pending_approval outcome — no file mutation yet.
func TestAdapter_ExecuteMutationParksAtApproval(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{
		Prompt: "change bar to qux @note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if obs.Outcome != autonomy.OutcomePendingApproval {
		t.Fatalf("outcome = %s, want pending_approval", obs.Outcome)
	}
	if obs.PatchID == "" {
		t.Fatal("pending_approval observation must carry the held patch id")
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
}

// TestAdapter_ClarificationNoTarget asserts an unresolved objective surfaces as
// a clarification observation (no invocation, no mutation).
func TestAdapter_ClarificationNoTarget(t *testing.T) {
	_, mock, a, _ := testHarness(t, nil)
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{
		Prompt: "change @missing.txt to something",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !obs.ClarificationRequired {
		t.Fatal("unresolved objective must demand clarification")
	}
	if obs.Outcome != autonomy.OutcomeCancelled {
		t.Fatalf("outcome = %s, want cancelled (no invocation)", obs.Outcome)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (no invocation before clarification)", mock.calls())
	}
}

// TestAdapter_ApproveMapsChanged asserts Approve applies the held patch and maps
// the terminal result onto the canonical changed outcome — the same execution,
// never a re-execution.
func TestAdapter_ApproveMapsChanged(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{Prompt: "change bar to qux @note.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if obs.PatchID == "" {
		t.Fatal("expected a held patch id")
	}
	after, err := a.Approve(context.Background(), obs.PatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if after.Outcome != autonomy.OutcomeChanged && after.Outcome != autonomy.OutcomeCreated {
		t.Fatalf("approval outcome = %s, want changed/created", after.Outcome)
	}
	if got := readTarget(t, root, "note.txt"); got == sampleOriginal {
		t.Fatal("approve did not mutate the file")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (approve must not re-invoke)", mock.calls())
	}
}

// TestAdapter_RejectMapsRejected asserts Reject terminates the held execution
// with the canonical rejected outcome and leaves the file untouched.
func TestAdapter_RejectMapsRejected(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{Prompt: "change bar to qux @note.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after, err := a.Reject(context.Background(), obs.PatchID, "not wanted")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if after.Outcome != autonomy.OutcomeRejected {
		t.Fatalf("rejection outcome = %s, want rejected", after.Outcome)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("rejection mutated the file: %q", got)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (reject must not re-invoke)", mock.calls())
	}
}

// TestAdapter_ExplicitTargetExecutesAfterClarification asserts the adapter
// hands a loop-carried resolved target to the executor's explicit-target path
// even when the raw prompt alone is a clarification.
func TestAdapter_ExplicitTargetExecutesAfterClarification(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	obs, err := a.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:  "change @missing.txt to something",
		Targets: []string{"note.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if obs.Outcome != autonomy.OutcomePendingApproval {
		t.Fatalf("outcome = %s, want pending_approval (human-specified target executed)", obs.Outcome)
	}
	if obs.PatchID == "" {
		t.Fatal("expected a held patch id")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("file mutated before approval: %q", got)
	}
}
