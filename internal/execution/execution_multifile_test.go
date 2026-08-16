package execution

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 5 — MULTI-FILE MUTATION CORRECTNESS ──────────────────────────────
//
// The runtime owns the multi-file mutation: resolve files → generate a
// per-target changeset → approval → ONE MutationSet transaction → apply ALL
// changes → verify all affected files → commit/rollback. The proof carries the
// affected files, the diff summary, the transaction ID and the verification
// results. No synthetic verification is ever produced.

// multiReplaceProvider returns one SEARCH/REPLACE block per call, in order,
// each replacing the first line of the target with a marker.
type multiReplaceProvider struct {
	mu       sync.Mutex
	callCt   int
	requests []ai.Request
}

func newMultiProvider() *multiReplaceProvider {
	return &multiReplaceProvider{}
}

func (p *multiReplaceProvider) Name() string { return "mock" }

func (p *multiReplaceProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCt++
	p.requests = append(p.requests, req)
	return &ai.Response{
		Content: "<<<<<<< SEARCH\nfirst\n=======\nchanged\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}, nil
}

func (p *multiReplaceProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, nil
}

func (p *multiReplaceProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCt
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		writeTarget(t, root, name, content)
	}
}

// TestMultiFileMutationSingleTransaction pins the Phase 5 contract end to end:
// two targets produce two artifacts, approval applies BOTH inside one MutationSet
// transaction, verification runs over the real verifier, and the proof carries
// the affected files, diff summary, transaction ID and verification results.
func TestMultiFileMutationSingleTransaction(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.txt": "first\naaa\n",
		"b.txt": "first\nbbb\n",
	})

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	prov := newMultiProvider()
	x := phase4Executor(t, root, prov, bus)

	// Route through the IntentGateway with an explicit multi-target request.
	g := NewIntentGateway(root)
	req, det, err := g.Gate(context.Background(), "$prompt change the first line in @a.txt and @b.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if len(req.Targets) != 2 {
		t.Fatalf("targets = %v, want 2", req.Targets)
	}

	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}
	if len(det.Profile.Targets) != 2 {
		t.Fatalf("strategy targets = %v, want 2", det.Profile.Targets)
	}
	if prov.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2 (one bounded invocation per target)", prov.calls())
	}
	// No file is mutated before approval.
	if got := mustRead(t, root, "a.txt"); !strings.HasPrefix(got, "first") {
		t.Fatalf("a.txt mutated before approval: %q", got)
	}

	// ── Approve: one transaction applies ALL targets ──────────────────
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := mustRead(t, root, "a.txt"); !strings.HasPrefix(got, "changed") {
		t.Fatalf("a.txt not mutated after approval: %q", got)
	}
	if got := mustRead(t, root, "b.txt"); !strings.HasPrefix(got, "changed") {
		t.Fatalf("b.txt not mutated after approval: %q", got)
	}
	if !apr.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("proof outcome = %s, want success", apr.Proof.Outcome)
	}
	if !apr.Verification.Passed {
		t.Fatalf("verification failed: %+v", apr.Verification)
	}

	// ── Proof carries the Phase 5 mutation evidence ───────────────────
	if len(apr.Proof.AffectedFiles) != 2 {
		t.Fatalf("affected files = %v, want [a.txt b.txt]", apr.Proof.AffectedFiles)
	}
	if len(apr.Proof.DiffSummary) != 2 {
		t.Fatalf("diff summary = %v, want 2 entries", apr.Proof.DiffSummary)
	}
	if apr.Proof.TransactionID == "" {
		t.Fatal("proof has no mutation transaction id")
	}
	if len(apr.Proof.Mutations) == 0 {
		t.Fatal("proof has no per-target mutation evidence")
	}
	if !apr.Verification.Passed {
		t.Fatal("proof verification missing or failed")
	}

	// ── No synthetic verification: the events came from the real graph ──
	collector.waitCount(events.EventVerificationCompleted, 1, time.Second)
	types := collector.types()
	last := types[len(types)-1]
	if last != events.EventExecutionFinished {
		t.Fatalf("last event = %s, want execution.finished (terminal)", last)
	}
}

// emptySecondProvider returns a valid patch for the first target and an empty
// response for the second — producing a patch with empty content that the
// apply boundary deterministically rejects.
type emptySecondProvider struct {
	mu     sync.Mutex
	callCt int
}

func (p *emptySecondProvider) Name() string { return "mock" }

func (p *emptySecondProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.callCt++
	call := p.callCt
	p.mu.Unlock()
	if call >= 2 {
		return &ai.Response{Content: ""}, nil
	}
	return &ai.Response{
		Content: "<<<<<<< SEARCH\nfirst\n=======\nchanged\n>>>>>>>",
	}, nil
}

func (p *emptySecondProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, nil
}

// TestMultiFileRollbackOnApplyFailure pins the transaction boundary: when the
// second target's apply fails (a patch the boundary rejects), the WHOLE
// mutation rolls back — no partial change survives.
func TestMultiFileRollbackOnApplyFailure(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.txt": "first\naaa\n",
		"b.txt": "first\nbbb\n",
	})
	bus := events.NewBus(events.DefaultBufferSize)
	_ = newPhase4Collector(bus)
	x := phase4Executor(t, root, &emptySecondProvider{}, bus)
	g := NewIntentGateway(root)
	req, _, err := g.Gate(context.Background(), "$prompt change the first line in @a.txt and @b.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err == nil {
		t.Fatal("expected the apply to fail")
	}
	// The mutation boundary was rolled back: no partial change survives.
	if got := mustRead(t, root, "a.txt"); !strings.HasPrefix(got, "first") {
		t.Fatalf("a.txt partial change survived rollback: %q", got)
	}
	if got := mustRead(t, root, "b.txt"); !strings.HasPrefix(got, "first") {
		t.Fatalf("b.txt changed on a failed transaction: %q", got)
	}
}
