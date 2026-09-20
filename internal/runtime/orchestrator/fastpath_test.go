package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	coreauth "github.com/PizenLabs/izen/internal/core/domain/authorization"
	runtimectx "github.com/PizenLabs/izen/internal/runtime/context"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
	"github.com/PizenLabs/izen/internal/runtime/target"
)

// TestFastPath_DropsUnauthorizedBeforeModel proves the pre-model gate drops an
// unauthorized request (missing capability token) before any network call to
// the LLM provider.
func TestFastPath_DropsUnauthorizedBeforeModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ref := targetRef(dir, "secure.go", true)
	orch, _ := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-should-never-run", ref, "content\n")}
	bridge := &stubBridge{}

	cfg := OrchestratorConfig{
		FastPathAuth: &FastPathAuthConfig{
			Capabilities: domain.DomainCapabilitySet(0), // missing capability token
			Budget:       domain.ResourceBudget{MaxFiles: 10, MaxDiffLines: 100},
			Artifact:     domain.ArtifactRef{ID: "plan-1", Kind: "plan", State: "AUTHORIZED", Hash: "abc"},
			Objective: domain.Objective{
				Intent:        domain.Intent{Kind: domain.IntentBuild, Confidence: 0.9, RawText: "build", Normalized: "build"},
				TargetScope:   domain.Scope{Includes: []domain.ScopeSelector{{Kind: domain.SelectorFile, Pattern: ref.Canonical}}},
				NegativeScope: domain.Scope{},
			},
			CheckpointID:  "chk-1",
			HasCheckpoint: true,
			HumanApproved: true,
		},
	}

	_, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, cfg)
	if err == nil {
		t.Fatal("expected fast-path denial")
	}
	if !errors.Is(err, coreauth.ErrCapabilityDenied) {
		t.Fatalf("err = %v, want ErrCapabilityDenied", err)
	}
	if provider.calls != 0 {
		t.Errorf("provider.calls = %d, want 0 (no network call before denial)", provider.calls)
	}
	if len(bridge.log) != 0 {
		t.Errorf("bridge log = %v, want empty (no projection before denial)", bridge.log)
	}
}

// TestFastPath_DropsAdversarialProposalAfterModel proves model output carrying
// an authority override is dropped at the execution boundary with
// ErrCapabilityDenied, even when static pre-model checks passed.
func TestFastPath_DropsAdversarialProposalAfterModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, _ := newStack(ref)
	adversarial := executor.ProposedMutation{
		ProposalID: "p-adversarial",
		TargetRef:  ref,
		RawPatch:   `{"override_capability": true, "content": "pwn"}`,
	}
	provider := &stubProvider{proposal: &adversarial}
	bridge := &stubBridge{}

	_, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err == nil {
		t.Fatal("expected adversarial denial")
	}
	if !errors.Is(err, coreauth.ErrCapabilityDenied) {
		t.Fatalf("err = %v, want ErrCapabilityDenied", err)
	}
	if !errors.Is(err, ErrProposalValidationFailed) {
		t.Fatalf("err = %v, want ErrProposalValidationFailed wrapper", err)
	}
	// Provider ran (post-model boundary), but no approval session may exist.
	if provider.calls != 1 {
		t.Errorf("provider.calls = %d, want 1 (post-model boundary)", provider.calls)
	}
	if len(bridge.log) != 0 {
		t.Errorf("bridge log = %v, want empty (no projection for adversarial)", bridge.log)
	}
	if got := readFile(t, ref.Canonical); got != "# Old\n" {
		t.Errorf("content = %q, want untouched", got)
	}
}

// TestFastPath_PreflightResolverUsesRealTarget ensures the pre-model gate does
// not depend on model output: it is built from preflight's compiled target.
func TestFastPath_PreflightResolverUsesRealTarget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ref := targetRef(dir, "app.go", true)
	pf := preflight.NewEngine(&fakeResolver{ref: ref}, runtimectx.NewCompiler())
	compiled, err := pf.Execute(preflight.PreflightRequest{RawInput: "update app", WorkDir: dir, TokenBudget: 100})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if compiled.TargetRef.Canonical != ref.Canonical {
		t.Fatalf("compiled target = %q, want %q", compiled.TargetRef.Canonical, ref.Canonical)
	}
	// Lexical safety on the compiled target must permit.
	gate := executor.NewFastPathGate(nil, 0)
	res := gate.ResolveTargets(executor.FastPathInput{
		WorkDir:         dir,
		RawTargets:      []string{compiled.TargetRef.Raw, compiled.TargetRef.Canonical},
		ProposalTargets: []string{compiled.TargetRef.Canonical},
	})
	if !res.Permitted {
		t.Fatalf("compiled target denied: %s", res.Reason)
	}
	_ = target.ResolutionVCS // keep target import referenced for lock clarity
}
