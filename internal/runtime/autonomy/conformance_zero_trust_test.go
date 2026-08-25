package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── Conformance verification, runtime side ─────────────────────────────────
//
// These tests drive the REAL Driver over the REAL RuntimeExecutor and pin the
// zero-trust behavior at the loop level:
//
//	Conformance A — an infeasible FULL_REWRITE parks for explicit human
//	                re-scoping with ZERO provider requests (I5).
//	Conformance B — finish_reason=length circuit-breaks at Boundary 3,
//	                performs AT MOST ONE typed FULL_REWRITE→BOUNDED_PATCH
//	                contract transition (I3), and STRICTLY HALTS on the second
//	                exhaustion — never a mutation retry loop (I1).
//	Boundary 5    — a workspace version change between attempts aborts the
//	                run before any execution.

// incidentTargetBytes mirrors the production incident geometry (7.78 KB
// target against a 1024-token output budget).
const incidentTargetBytes = 7780

func writeIncidentTarget(t *testing.T, root string) {
	t.Helper()
	writeTarget(t, root, "index.html", strings.Repeat("<p>incident filler</p>\n", 359)[:incidentTargetBytes])
}

func loopBoundsForConformance() autonomy.LoopBounds {
	return autonomy.LoopBounds{
		MaxAttempts:           6,
		MaxRecoveryCycles:     4,
		MaxExecutionSteps:     20,
		MaxIdenticalDecisions: 20,
		MaxTotalTokens:        200000,
	}
}

// TestConformanceA_DriverPreflightInfeasibilityParksWithZeroRequests pins the
// runtime-level consequence of invariant I5: the run NEVER reaches the
// provider and parks for an EXPLICIT human re-scope — user intent is not
// silently rewritten into something else.
func TestConformanceA_DriverPreflightInfeasibilityParksWithZeroRequests(t *testing.T) {
	root := t.TempDir()
	writeIncidentTarget(t, root)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "should never be requested",
		Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
	}}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	driver := NewDriver(
		NewExecutorAdapter(root, execution.NewIntentGateway(root), x),
		bus, WithLoopBounds(loopBoundsForConformance()))

	term, err := driver.Run(context.Background(), "check @index.html and rewrite it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ZERO HTTP provider requests crossed.
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider requests = %d, want 0 (Boundary 2 traps before any invocation)", got)
	}

	// The loop parked for a human re-scope decision.
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	b := driver.Boundary()
	if b == nil || b.PatchID != "" {
		t.Fatalf("boundary = %+v, want an inform boundary without a held patch", b)
	}
	if !strings.Contains(b.Reason, "preflight infeasible") || !strings.Contains(b.Reason, "re-scope") {
		t.Fatalf("boundary reason = %q, want an explicit re-scope demand", b.Reason)
	}
	_ = term // nil termination = parked

	// The workspace is untouched.
	if got := readTarget(t, root, "index.html"); len(got) != incidentTargetBytes {
		t.Fatal("workspace changed on a preflight-rejected objective")
	}
}

// TestConformanceB_DriverOutputExhaustionSingleTypedTransitionThenHalt pins
// the complete I1+I3+I4 chain: one exhaustion triggers exactly ONE typed
// contract transition (new causal contract, atomic context rebuild, advisory
// diagnostics only); the second exhaustion STRICTLY HALTS — there is no
// third attempt and no mutation loop.
func TestConformanceB_DriverOutputExhaustionSingleTypedTransitionThenHalt(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML()) // feasible at B2

	truncated := func() *ai.Response {
		return &ai.Response{
			Content: "<<<<<<< SEARCH\nfoo\n=======\nqux\n>>>>>>>", // parseable-looking poison
			Usage:   ai.ProviderUsage{PromptTokens: 2180, CompletionTokens: 1024, Known: true, FinishReason: "length"},
		}
	}
	mock := &mockProvider{responses: []*ai.Response{truncated(), truncated(), truncated()}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)

	var transitioned *autonomy.LoopRequest
	driver := NewDriver(
		NewExecutorAdapter(root, execution.NewIntentGateway(root), x),
		bus,
		WithLoopBounds(loopBoundsForConformance()),
		WithRepair(func(o autonomy.Observation, req autonomy.LoopRequest) (autonomy.LoopRequest, error) {
			next, rerr := typedRepair(o, req)
			if rerr == nil {
				cp := next
				transitioned = &cp
			}
			return next, rerr
		}),
	)

	if _, err := driver.Run(context.Background(), "check @index.html and rewrite it"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Exactly TWO invocations: the initial full-artifact attempt and the ONE
	// typed bounded-patch continuation. No third attempt exists (I1).
	if got := mock.calls(); got != 2 {
		t.Fatalf("provider invocations = %d, want exactly 2 (one attempt + one typed transition)", got)
	}

	// The typed transition was materially correct (I3).
	if transitioned == nil {
		t.Fatal("the typed FULL_REWRITE -> BOUNDED_PATCH transition never happened")
	}
	if transitioned.RecoveryStrategy != autonomy.StrategyBoundedPatch {
		t.Fatalf("transition strategy = %q, want bounded_patch", transitioned.RecoveryStrategy)
	}
	if transitioned.ParentContractID == "" {
		t.Fatal("transition lost the causal contract lineage")
	}
	if transitioned.WorkspaceDigest == "" {
		t.Fatal("transition lost the workspace version binding")
	}
	// Recovery Isolation (I2): only advisory diagnostics cross — the rejected
	// generation's bytes are structurally absent from the new context.
	if strings.Contains(transitioned.Evidence, "<<<<<<< SEARCH\nfoo\n=======") {
		t.Fatal("rejected artifact bytes were re-injected into prompt context")
	}
	if !strings.Contains(transitioned.Evidence, "[DIAGNOSTIC subtype=OUTPUT_EXHAUSTED") {
		t.Fatalf("evidence missing the advisory diagnostic signal: %q", transitioned.Evidence)
	}

	// Strict halt: parked for a human, nothing held, nothing mutated.
	if driver.State() != autonomy.RuntimeAwaitingHuman && driver.State() != autonomy.RuntimeAborted {
		t.Fatalf("state = %s, want a terminal/human convergence", driver.State())
	}
	if b := driver.Boundary(); b != nil && b.PatchID != "" {
		t.Fatalf("a patch survived an exhausted lineage: %+v", b)
	}
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "<!DOCTYPE html>") {
		t.Fatal("workspace corrupted by an exhausted recovery")
	}

	// The wire proves the second attempt used the strict bounded protocol.
	reqs := mock.recordedRequests()
	if strings.Contains(strings.ToLower(reqs[1].System), "full modified file") {
		t.Fatal("attempt 2 did not switch to the strict bounded-patch protocol")
	}
}

// TestBoundaryFive_WorkspaceDriftAbortsBetweenAttempts pins Boundary 5: when
// the workspace version changes between attempts, the next submission is
// refused BEFORE execution with a workspace_drift observation, and the matrix
// aborts the run.
func TestBoundaryFive_WorkspaceDriftAbortsBetweenAttempts(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{ // never reached
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
	}}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	stale := adapter.WorkspaceVersion([]string{"note.txt"})

	// An out-of-band writer moves the workspace AFTER the version capture.
	writeTarget(t, root, "note.txt", "externally rewritten\n")

	obs, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		WorkspaceDigest: stale,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if obs.Outcome != autonomy.OutcomeWorkspaceDrift {
		t.Fatalf("observation outcome = %s, want workspace_drift", obs.Outcome)
	}
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider requests = %d, want 0 (drift halts before execution)", got)
	}

	// The matrix converges the drift to a permanent abort.
	d := DecideRecovery(obs, loopBoundsForConformance())
	if d.Action != autonomy.LoopAbort {
		t.Fatalf("decision = %+v, want abort", d)
	}

	// An unchanged workspace still executes normally (control).
	writeTarget(t, root, "note.txt", sampleOriginal)
	fresh := adapter.WorkspaceVersion([]string{"note.txt"})
	mock.responses = []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 10, FinishReason: "stop"},
	}}
	if _, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		WorkspaceDigest: fresh,
	}); err != nil {
		t.Fatalf("fresh-digest Execute: %v", err)
	}
	if got := mock.calls(); got != 1 {
		t.Fatalf("provider requests = %d, want 1 on the fresh-digest control", got)
	}
}

// TestRecoverySubtypeMatrix unit-covers the I4 classification determinism.
func TestRecoverySubtypeMatrix(t *testing.T) {
	cases := []struct {
		outcome autonomy.ExecutionOutcome
		want    FailureSubtype
	}{
		{autonomy.OutcomeTruncated, SubtypeOutputExhausted},
		{autonomy.OutcomeArtifactRetryableRejected, SubtypeSchemaViolation},
		{autonomy.OutcomeFailed, SubtypeTransportError},
		{autonomy.OutcomePatchGenFailed, SubtypeTransportError},
		{autonomy.OutcomeApplyFailed, SubtypeMutationFailure},
		{autonomy.OutcomeVerifyFailed, SubtypeMutationFailure},
		{autonomy.OutcomePreflightInfeasible, SubtypePreflightInfeasible},
		{autonomy.OutcomeWorkspaceDrift, SubtypeWorkspaceDrift},
		{autonomy.OutcomeChanged, ""},
	}
	for _, c := range cases {
		got := RecoverySubtype(autonomy.Observation{Outcome: c.outcome})
		if got != c.want {
			t.Errorf("RecoverySubtype(%s) = %q, want %q", c.outcome, got, c.want)
		}
	}
	// finish_reason refines generic failures (I4 keys on subtypes, not the
	// coarse outcome label).
	if got := RecoverySubtype(autonomy.Observation{
		Outcome: autonomy.OutcomeFailed, FinishReason: "length",
	}); got != SubtypeOutputExhausted {
		t.Errorf("finish_reason refinement = %q, want output_exhausted", got)
	}
}

// TestDecideRecoveryMatrix pins the closed decision vocabulary of the
// zero-trust matrix.
func TestDecideRecoveryMatrix(t *testing.T) {
	b := loopBoundsForConformance()

	// First exhaustion → the single typed transition.
	if d := DecideRecovery(autonomy.Observation{Outcome: autonomy.OutcomeTruncated}, b); d.Action != autonomy.LoopRepair {
		t.Fatalf("first exhaustion → %+v, want repair", d)
	}
	// Second exhaustion (bounded already active) → strict human halt (I1).
	if d := DecideRecovery(autonomy.Observation{
		Outcome: autonomy.OutcomeTruncated, RecoveryStrategy: autonomy.StrategyBoundedPatch,
	}, b); d.Action != autonomy.LoopAskHuman {
		t.Fatalf("second exhaustion → %+v, want ask_human", d)
	}
	// Preflight infeasibility → explicit re-scope demand (I5), never silent.
	if d := DecideRecovery(autonomy.Observation{Outcome: autonomy.OutcomePreflightInfeasible}, b); d.Action != autonomy.LoopAskHuman {
		t.Fatalf("preflight → %+v, want ask_human", d)
	}
	// Drift → abort (B5).
	if d := DecideRecovery(autonomy.Observation{Outcome: autonomy.OutcomeWorkspaceDrift}, b); d.Action != autonomy.LoopAbort {
		t.Fatalf("drift → %+v, want abort", d)
	}
	// Refusal → abort, never retried.
	if d := DecideRecovery(autonomy.Observation{Outcome: autonomy.OutcomeFailed, FinishReason: "content_filter"}, b); d.Action != autonomy.LoopAbort {
		t.Fatalf("refusal → %+v, want abort", d)
	}
	// Mutation failure → human, no auto-retry over rolled-back ground.
	if d := DecideRecovery(autonomy.Observation{Outcome: autonomy.OutcomeVerifyFailed}, b); d.Action != autonomy.LoopAskHuman {
		t.Fatalf("verify failure → %+v, want ask_human", d)
	}
}
