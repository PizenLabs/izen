package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/provider"
	"github.com/PizenLabs/izen/internal/runtime/authorization"
	"github.com/PizenLabs/izen/internal/runtime/executor"
)

// Dynamic Budget Resolution Test: Config A (Free, 1024) => concise clamped;
// Config B (Paid, 16384) => expanded detail.
func TestASKBudgetResolverDynamicTiers(t *testing.T) {
	r := NewASKBudgetResolver()
	a := r.Resolve(provider.ProviderCapabilities{OutputTokenCap: 1024}, ModelClassStandard, TaskComplexityMedium)
	if a.DetailLevel != "concise" {
		t.Errorf("free tier detail = %q, want concise", a.DetailLevel)
	}
	if a.MaxTokens > 1024 || !a.Clamped {
		t.Errorf("free tier max=%d clamped=%v, want <=1024 clamped", a.MaxTokens, a.Clamped)
	}
	if a.ReasoningBudget != 0 {
		t.Errorf("free tier reasoning=%d, want 0 (clamped to prevent exhaustion)", a.ReasoningBudget)
	}
	b := r.Resolve(provider.ProviderCapabilities{
		OutputTokenCap:          16384,
		SupportsReasoningBudget: true,
		SupportsReasoningEffort: true,
	}, ModelClassHighOutput, TaskComplexityHigh)
	if b.DetailLevel != "expanded" {
		t.Errorf("paid tier detail = %q, want expanded", b.DetailLevel)
	}
	if b.MaxTokens <= 1000 {
		t.Errorf("paid tier max=%d, want >1000 (detailed analysis/code blocks)", b.MaxTokens)
	}
}

// Universal Length Outcome Test: paid provider returning finish_reason
// length at token 4096 records PARTIAL without a terminal error.
func TestRunCycleLengthMapsToPartial(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)
	_ = path
	orch, _ := newStack(ref)
	prov := &stubProvider{proposal: nil, err: fmt.Errorf("%w: finish_reason=length", llm.ErrPayloadTruncated)}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionCancel}}
	res, err := orch.RunCycle(context.Background(), baseRequest(ref), prov, bridge, OrchestratorConfig{})
	if err != nil {
		t.Fatalf("RunCycle(length) must not throw terminal error, got %v", err)
	}
	if res.Verdict != evidence.PARTIAL {
		t.Fatalf("Verdict = %v, want PARTIAL", res.Verdict)
	}
	if res.Status != string(provider.StreamPartial) {
		t.Fatalf("Status = %q, want PARTIAL", res.Status)
	}
	if res.Committed {
		t.Fatal("PARTIAL must never commit")
	}
}

// Static Authority Lockdown Test: ASK intent with high-capability model still
// denies disk writes; even 2000 tokens of patch text cannot escalate.
func TestStaticAuthorityAskDeniesMutate(t *testing.T) {
	if !executor.HasExecutionMarker("$prompt rewrite everything") {
		t.Error("$prompt must carry execution marker")
	}
	if !executor.HasExecutionMarker("$hot fix") {
		t.Error("$hot must carry execution marker")
	}
	if !executor.HasExecutionMarker("/build feature") {
		t.Error("/build must carry execution marker")
	}
	if executor.HasExecutionMarker("delete file") {
		t.Error("bare ASK text (even 'delete file') must not authorize")
	}
	grant := domain.DomainCapabilitySet(domain.CapRead | domain.CapSearch | domain.CapWrite | domain.CapPatch)
	eff := executor.ConstrainCapabilitiesForIntent(domain.IntentAsk, grant)
	if eff.Has(domain.CapWrite) || eff.Has(domain.CapPatch) {
		t.Fatal("ASK must mask CapWrite/CapPatch even with high-capability model")
	}
	g := executor.NewFastPathGate(nil, 0)
	in := executor.FastPathInput{
		WorkDir:         ".",
		RawTargets:      []string{"main.go"},
		ProposalTargets: []string{"main.go"},
		Objective: domain.Objective{
			Intent: domain.Intent{Kind: domain.IntentAsk, Confidence: 1},
		},
		Capabilities:      grant,
		Artifact:          domain.ArtifactRef{ID: "a", Kind: "k", State: "AUTHORIZED"},
		CheckpointID:      "c",
		HasCheckpoint:     true,
		HumanApproved:     true,
		ProposalDiffLines: 2000,
		ProposalFiles:     1,
		Provider:          "openrouter",
		Model:             "openai/gpt-5",
	}
	if res := g.Authorize(context.Background(), in); res.Permitted {
		t.Fatal("RuntimeExecutor must block ASK disk writes with ErrCapabilityDenied")
	}
}
