package execution

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── PHASE 5 — EXECUTION AUTHORITY INVARIANTS (runtime enforcement) ──────────
//
// Rules 3, 4 and 6 enforced against the real executor:
//
//	3. Every execution produces an ExecutionProof.
//	4. Every verification requires a real verifier result — never synthetic.
//	6. The executor never executes without a strategy (unconditional gateway
//	   classification is preserved even for direct runtime callers).

// TestEveryExecutionHasExecutionProof pins rule 3 across every terminal path:
// mutation (pending + approved), read-only, deterministic, clarify, failure,
// reject. Every returned ExecutionResult carries a non-nil proof with the
// runtime graph evidence.
func TestEveryExecutionHasExecutionProof(t *testing.T) {
	cases := []struct {
		name     string
		provider ai.Provider
		req      ExecuteRequest
		resolve  func(*RuntimeExecutor, *ExecutionResult)
	}{
		{
			name:     "targeted mutation pending approval",
			provider: &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}},
			req:      ExecuteRequest{RequestID: "i1", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"},
		},
		{
			name:     "read-only explanation",
			provider: &mockProvider{responses: []*ai.Response{{Content: "answer"}}},
			req:      ExecuteRequest{RequestID: "i2", Mode: "ask", Prompt: "explain the login flow in @note.txt"},
		},
		{
			name:     "direct response",
			provider: &mockProvider{responses: []*ai.Response{{Content: "hi"}}},
			req:      ExecuteRequest{RequestID: "i3", Mode: "ask", Prompt: "hi"},
		},
		{
			name:     "provider failure",
			provider: &failingProvider{},
			req:      ExecuteRequest{RequestID: "i4", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"},
		},
		{
			name:     "no provider deterministic clarify",
			provider: nil,
			req:      ExecuteRequest{RequestID: "i5", Mode: "ask", Prompt: "create a .gitignore file"},
		},
		{
			name:     "human clarification",
			provider: nil,
			req:      ExecuteRequest{RequestID: "i6", Mode: "build", Prompt: "remove the footer from @missing.html"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTarget(t, root, "note.txt", sampleOriginal)
			bus := events.NewBus(events.DefaultBufferSize)
			x := phase4Executor(t, root, tc.provider, bus)
			res, err := x.Execute(context.Background(), tc.req)
			if res == nil {
				t.Fatalf("Execute returned nil result (err=%v)", err)
			}
			if res.Proof == nil {
				t.Fatal("execution produced NO ExecutionProof (rule 3)")
			}
			if res.Proof.RequestID == "" {
				t.Fatal("proof has no request id")
			}
			if res.Proof.Strategy == "" && res.Proof.Outcome != OutcomeCancelled {
				t.Fatalf("proof has no strategy decision: %+v", res.Proof)
			}
			// The runtime graph evidence is present and ordered.
			if len(res.Proof.RuntimeGraph) == 0 && res.Proof.Outcome != OutcomeCancelled {
				t.Fatal("proof carries no runtime graph evidence (rule 5)")
			}
			// Resolve the approval gate where applicable.
			if err == nil && res.PendingPatchID != "" {
				apr, aerr := x.Approve(context.Background(), res.PendingPatchID)
				if aerr != nil {
					// apply failure is a terminal path too — still needs a proof.
					if apr == nil || apr.Proof == nil {
						t.Fatal("failed approval returned no proof (rule 3)")
					}
				} else if apr.Proof == nil {
					t.Fatal("approved execution returned no proof (rule 3)")
				}
			}
		})
	}
}

// TestEveryVerificationRequiresRealVerifier pins rule 4: verification evidence
// on the proof comes ONLY from the real verifier run. A read-only execution
// (no mutation) must never fabricate verification; a mutation execution's
// verification must match the real verifier's result.
func TestEveryVerificationRequiresRealVerifier(t *testing.T) {
	// 1. Read-only: zero verification evidence, never "verified".
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	x := phase4Executor(t, root, &mockProvider{responses: []*ai.Response{{Content: "answer"}}}, events.NewBus(events.DefaultBufferSize))
	res, err := x.Execute(context.Background(), ExecuteRequest{RequestID: "v1", Mode: "ask", Prompt: "explain the login flow in @note.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Proof.Verification.Passed {
		t.Fatal("read-only execution fabricated a passing verification (rule 4)")
	}
	if len(res.Proof.Verification.Results) != 0 {
		t.Fatalf("read-only execution fabricated verification results: %+v", res.Proof.Verification)
	}

	// 2. Mutation: verification evidence comes from the real verifier steps.
	root2 := t.TempDir()
	writeTarget(t, root2, "note.txt", sampleOriginal)
	x2 := phase4Executor(t, root2, &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}, events.NewBus(events.DefaultBufferSize))
	res2, err := x2.Execute(context.Background(), ExecuteRequest{RequestID: "v2", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"})
	if err != nil {
		t.Fatalf("Execute2: %v", err)
	}
	apr, err := x2.Approve(context.Background(), res2.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !apr.Proof.Verification.Passed {
		t.Fatal("verified mutation must carry a real passing verifier result")
	}
	// The steps must be the real verifier steps ("noop" from the trivial
	// verifier), never a synthesized string.
	foundNoop := false
	for _, s := range apr.Proof.Verification.Results {
		if s.Step.Name == "noop" {
			foundNoop = true
		}
	}
	if !foundNoop {
		t.Fatalf("verification results are not the real verifier's: %+v", apr.Proof.Verification)
	}
}

// TestExecutorNeverExecutesWithoutStrategy pins rule 6 at the runtime boundary:
// even a direct caller that bypasses the gateway receives a deterministic
// strategy — the executor never executes an unclassified request.
func TestExecutorNeverExecutesWithoutStrategy(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	x := phase4Executor(t, root, &mockProvider{responses: []*ai.Response{
		{Content: sampleReplace},
		{Content: "the build fails at compile"},
	}}, events.NewBus(events.DefaultBufferSize))

	// Direct caller with a target but NO strategy profile.
	res, err := x.Execute(context.Background(), ExecuteRequest{RequestID: "s1", Mode: "build", Prompt: "change bar to qux", Target: "note.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Strategy != "targeted_mutation" {
		t.Fatalf("strategy = %q, want the deterministic fallback targeted_mutation", res.Strategy)
	}

	// Direct caller with NO target and NO strategy — the unconditional
	// classifier runs (strategy.Select), never a nil strategy.
	res2, err := x.Execute(context.Background(), ExecuteRequest{RequestID: "s2", Mode: "ask", Prompt: "why is the build failing"})
	if err != nil {
		t.Fatalf("Execute2: %v", err)
	}
	if res2.Strategy == "" {
		t.Fatal("executor executed a request with no strategy (rule 6)")
	}
	if res2.Strategy != "repository_investigation" {
		t.Fatalf("strategy = %q, want repository_investigation from the unconditional classifier", res2.Strategy)
	}

	// The proof recorded the context decisions the strategy owns.
	if len(res2.Proof.ContextDecisions) != 1 {
		t.Fatalf("context decisions = %d, want 1", len(res2.Proof.ContextDecisions))
	}
	dec := res2.Proof.ContextDecisions[0]
	if dec.Policy != "repository" {
		t.Fatalf("context policy = %q, want repository", dec.Policy)
	}
	if dec.Budget <= 0 {
		t.Fatalf("context budget = %d, want > 0 for repository investigation", dec.Budget)
	}
	if len(dec.Items) == 0 {
		t.Fatal("context decisions must explain every inclusion (rule: compiler explains why)")
	}
	for _, it := range dec.Items {
		if it.Reason == "" {
			t.Fatalf("context item %s has no inclusion reason", it.Kind)
		}
	}
}
