package investigate

import (
	"context"
	"io"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// strategyProvider is an ai.Provider stub that returns a fixed dispatch
// strategy JSON. It models an LLM classifier that hallucinates a Go dependency
// lookup for a frontend workspace.
type strategyProvider struct {
	content string
}

func (p *strategyProvider) Name() string { return "strategy-stub" }

func (p *strategyProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return &ai.Response{Content: p.content}, nil
}

func (p *strategyProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	return nil, nil
}

// TestDispatchStrategyVanillaWebDowngradesLxRemotePackage pins the FRONTEND
// DOMAIN ISOLATION guard: on a VANILLA_WEB workspace, an LLM classifier that
// routes a remote package import path to lx is downgraded to diagnose so the Go
// dependency remediation blueprint (go mod tidy / go get) is never spawned.
func TestDispatchStrategyVanillaWebDowngradesLxRemotePackage(t *testing.T) {
	prov := &strategyProvider{content: `{"tool":"lx","target":"github.com/moby/moby/client"}`}
	s := DispatchStrategy(context.Background(), prov, "test-model",
		"cannot find module providing package github.com/moby/moby/client", 0, true)
	if s.Tool != ToolDiagnose {
		t.Fatalf("vanilla-web lx remote package = %s, want diagnose (Go dependency heuristics invalid)", s.Tool)
	}
	if s.Target != "" {
		t.Errorf("downgraded strategy target = %q, want empty", s.Target)
	}
}

// TestDispatchStrategyFrontendIntentDowngradesLxRemotePackage pins the same
// guard for a FRONTEND_UI intent in a non-vanilla workspace: an HTML/CSS/JS
// intent must never route a remote package to the Go remediation blueprint.
func TestDispatchStrategyFrontendIntentDowngradesLxRemotePackage(t *testing.T) {
	prov := &strategyProvider{content: `{"tool":"lx","target":"github.com/foo/bar"}`}
	s := DispatchStrategy(context.Background(), prov, "test-model",
		"no required module provides package github.com/foo/bar", IntentFrontendUI)
	if s.Tool != ToolDiagnose {
		t.Fatalf("frontend intent lx remote package = %s, want diagnose", s.Tool)
	}
}

// TestDispatchStrategyVanillaWebKeepsLocalLx pins that the guard is scoped to
// remote package targets: a local file/symbol lx lookup is still allowed on a
// VANILLA_WEB workspace (CSS/DOM context search).
func TestDispatchStrategyVanillaWebKeepsLocalLx(t *testing.T) {
	prov := &strategyProvider{content: `{"tool":"lx","target":"index.html"}`}
	s := DispatchStrategy(context.Background(), prov, "test-model",
		"duplicate content in index.html", 0, true)
	if s.Tool != ToolLX {
		t.Fatalf("vanilla-web local lx = %s, want lx preserved", s.Tool)
	}
}
