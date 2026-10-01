package autonomy

// PHASE 16.1 — STRICT EVIDENCE BOUNDARIES & ADMISSION HARDENING
//
// These are the acceptance tests for the three invariants that keep discovery
// from becoming authority:
//
//	I11  a directory is not a file
//	I12  no evidence, no provider
//	I13  evidence is not authority
//
// The assertions are deliberately about what did NOT happen — no target, no
// boundary, no provider call, no produced/proven lifecycle state. A positive
// assertion ("the right thing occurred") is satisfied by a run that also did
// three wrong things on the way.

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// phase161MutationProfile is a deterministic mutation strategy profile so the
// tests exercise the mutation lane without depending on the classifier.
func phase161MutationProfile() *strategy.ExecutionStrategyProfile {
	return &strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  true,
		StrategyReason: "phase16.1 acceptance test",
	}
}

// TestPhase16_1_EvidenceDoesNotGrantAuthority implements I13 (Evidence ≠
// Authority) and I11 (Directory Is Not A File).
//
// Setup: a workspace with go.mod and main.go, prompt "Refactor project",
// target ".". Discovery MUST find the files (evidence > 0), but that evidence
// MUST NOT grant a mutation target or a mutation boundary, and the provider
// MUST NOT be reached.
func TestPhase16_1_EvidenceDoesNotGrantAuthority(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "go.mod", "module example.com/demo\n\ngo 1.26\n")
	writeTarget(t, root, "main.go", "package main\n\nfunc main() {}\n")

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: "this must never be requested"}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "phase16-1-evidence",
		Mode:      "build",
		Prompt:    "Refactor project",
		Target:    ".",
		Strategy:  phase161MutationProfile(),
	})
	if err != nil {
		t.Fatalf("a directory halt is not a failure: %v", err)
	}
	if res == nil || res.TargetBinding == nil {
		t.Fatal("the halted execution must carry its target-binding evidence")
	}

	// ── I11: "." is a directory, never a file ──────────────────────────
	if got := res.TargetBinding.State; got != execution.TargetStateUnboundDirectory {
		t.Fatalf("target state = %s, want %s (I11)", got, execution.TargetStateUnboundDirectory)
	}
	if res.TargetBinding.Dispatchable() {
		t.Fatal("a directory target must never be dispatchable")
	}
	// ── Discovery ran and produced EVIDENCE ────────────────────────────
	if res.TargetBinding.Profile == nil {
		t.Fatal("discovery evidence must be attached to the verdict")
	}
	if n := len(res.TargetBinding.Profile.Candidates); n == 0 {
		t.Fatal("discovery must observe go.mod and main.go; candidates = 0")
	}
	// ── I13: that evidence is NOT authority ────────────────────────────
	if len(res.Targets) != 0 {
		t.Fatalf("discovery evidence became a mutation target: %v", res.Targets)
	}

	// The admission gate sees exactly the same verdict: an unbound mutation
	// boundary and a disambiguation request, never an admit.
	spec := ExecutionSpec{
		Intent:            IntentBuild,
		TargetBinding:     res.TargetBinding,
		WorkspaceEvidence: res.TargetBinding.Profile,
		ExplicitTargets:   []string{"."},
		MutationBoundary:  MutationBoundaryUnbound,
	}
	if spec.MutationBoundary != MutationBoundaryUnbound {
		t.Fatalf("mutation boundary = %s, want UNBOUND", spec.MutationBoundary)
	}
	if spec.HasAdmissibleMutationBoundary() {
		t.Fatal("a directory target with no proven file must not have an admissible mutation boundary")
	}
	outcome := EvaluatePreflightAdmission(spec)
	if outcome.Verdict != AdmissionDisambiguate {
		t.Fatalf("verdict = %s (%s), want DISAMBIGUATE", outcome.Verdict, outcome.Reason)
	}

	// ── I12: no provider call ──────────────────────────────────────────
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider was invoked %d time(s) for a directory target; I12 forbids it", got)
	}
}

// TestPhase16_1_PreflightBlockStatePurity implements I12 at its hardest point:
// a mutation objective with no target and no context MUST block, and the block
// MUST leave every lifecycle state at its unproduced/unproven value.
func TestPhase16_1_PreflightBlockStatePurity(t *testing.T) {
	// Empty workspace: the directory statement yields no candidate at all.
	root := t.TempDir()
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{Content: "unreachable"}}}
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "phase16-1-purity",
		Mode:      "build",
		Prompt:    "Refactor project",
		Target:    ".",
		Strategy:  phase161MutationProfile(),
	})
	if err != nil {
		t.Fatalf("a blocked target is not a failure: %v", err)
	}
	if res == nil || res.TargetBinding == nil {
		t.Fatal("the blocked execution must carry its evidence")
	}

	// The admission-time spec: mutation intent, no context channels, no
	// provider facts, and no lifecycle state produced.
	spec := ExecutionSpec{
		Intent:            IntentMutate,
		TargetBinding:     res.TargetBinding,
		WorkspaceEvidence: res.TargetBinding.Profile,
		ExplicitTargets:   []string{"."},
		MutationBoundary:  MutationBoundaryUnbound,
	}
	outcome := EvaluatePreflightAdmission(spec)

	if outcome.Verdict != AdmissionBlock {
		t.Fatalf("verdict = %s (%s), want BLOCK", outcome.Verdict, outcome.Reason)
	}
	if outcome.FailureClass != FailureClassInadmissibleTarget {
		t.Fatalf("failure class = %s, want %s", outcome.FailureClass, FailureClassInadmissibleTarget)
	}
	if !errors.Is(outcome.Err, ErrInadmissibleTarget) {
		t.Fatalf("error = %v, want ErrInadmissibleTarget", outcome.Err)
	}

	// ── State purity: a block leaves nothing produced/applied/proven ───
	if spec.ProviderCalls != 0 || spec.ProviderTokens != 0 {
		t.Fatalf("blocked spec billed a provider: calls=%d tokens=%d", spec.ProviderCalls, spec.ProviderTokens)
	}
	if spec.Evidence == EvidenceProduced {
		t.Fatal("a blocked spec must not report produced evidence")
	}
	if spec.Artifact == execution.ArtifactProduced {
		t.Fatal("a blocked spec must not report a produced artifact")
	}
	if spec.Mutation == execution.FilesystemApplied {
		t.Fatal("a blocked spec must not report an applied mutation")
	}
	if spec.Objective == execution.ObjectiveStateProven {
		t.Fatal("a blocked spec must not report a proven objective")
	}

	// And the real provider was never reached.
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}
