package pipeline

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

func TestIntentForMode(t *testing.T) {
	cases := map[string]Intent{
		"plan":                 IntentReasoning,
		"investigate":          IntentReasoning,
		"review":               IntentReasoning,
		"build":                IntentExecution,
		"ask":                  IntentInformational,
		"totally-unknown-mode": IntentExecution,
	}
	for mode, want := range cases {
		if got := IntentForMode(mode); got != want {
			t.Errorf("IntentForMode(%q) = %s, want %s", mode, got, want)
		}
	}
}

func TestRouterDefaults(t *testing.T) {
	r := NewRouter()
	for _, i := range AllIntents() {
		p := r.RouteFor(i)
		if !p.Valid() {
			t.Fatalf("RouteFor(%s) = %+v, want valid profile", i, p)
		}
		if p.Policy != DefaultPolicies[i] {
			t.Errorf("RouteFor(%s) policy = %+v, want default %+v", i, p.Policy, DefaultPolicies[i])
		}
		if p.Intent != i {
			t.Errorf("RouteFor(%s) intent = %s", i, p.Intent)
		}
	}
}

func TestRouterRouteForMode(t *testing.T) {
	r := NewRouter()
	if got := r.RouteForMode("plan"); got.Intent != IntentReasoning {
		t.Errorf("RouteForMode(plan) intent = %s, want reasoning", got.Intent)
	}
	if got := r.RouteForMode("build"); got.Intent != IntentExecution {
		t.Errorf("RouteForMode(build) intent = %s, want execution", got.Intent)
	}
	if got := r.RouteForMode("ask"); got.Intent != IntentInformational {
		t.Errorf("RouteForMode(ask) intent = %s, want informational", got.Intent)
	}
}

func TestRouterModelOverrides(t *testing.T) {
	r := NewRouter(
		WithModel(IntentReasoning, "heavy-1"),
		WithModel(IntentInformational, "mini-1"),
		WithFallbackModel("fallback-1"),
	)
	if got := r.RouteFor(IntentReasoning).Model; got != "heavy-1" {
		t.Errorf("reasoning model = %q, want heavy-1", got)
	}
	// Execution has no pin, so it falls back to the fallback model.
	if got := r.RouteFor(IntentExecution).Model; got != "fallback-1" {
		t.Errorf("execution model = %q, want fallback-1", got)
	}
	if got := r.RouteFor(IntentInformational).Model; got != "mini-1" {
		t.Errorf("informational model = %q, want mini-1", got)
	}
}

func TestRouterPolicyOverrides(t *testing.T) {
	p := layer2.ContextPolicy{
		MaxTokenBudget:     2000,
		MaxFiles:           4,
		MaxSymbols:         64,
		AllowBinary:        true,
		ExpandDependencies: false,
		CompressionRatio:   0.5,
	}
	r := NewRouter(WithPolicy(IntentInformational, p))
	if got := r.RouteFor(IntentInformational).Policy; got != p {
		t.Errorf("informational policy = %+v, want %+v", got, p)
	}
	// Invalid policies are rejected and the default stays in place.
	bad := p
	bad.MaxTokenBudget = 0
	r2 := NewRouter(WithPolicy(IntentExecution, bad))
	if got := r2.RouteFor(IntentExecution).Policy; got != DefaultPolicies[IntentExecution] {
		t.Errorf("invalid override leaked; got %+v", got)
	}
}

func TestRouteProfileValid(t *testing.T) {
	if (RouteProfile{Model: "m", Policy: DefaultPolicies[IntentExecution]}).Valid() {
		t.Log("valid profile")
	}
	if (RouteProfile{Policy: DefaultPolicies[IntentExecution]}).Valid() {
		t.Error("profile without model reported valid")
	}
}
