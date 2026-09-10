package registry

import "testing"

// Known non-reasoning families served through reasoning-capable providers
// must resolve unsupported (I6): the generic provider fallback must not
// fabricate [default] low medium ... options for them.
func TestResolveNonReasoningDenylist(t *testing.T) {
	cases := []struct{ provider, id string }{
		{"openrouter", "inclusionai/ling-3.0-flash-fin:free"},
		{"openrouter", "openai/gpt-4o-mini"},
		{"openrouter", "openai/gpt-4o"},
	}
	for _, c := range cases {
		caps := ResolveReasoningCapability(c.provider, c.id)
		if caps.Supported {
			t.Errorf("Resolve(%q, %q) supported=true, want unsupported", c.provider, c.id)
		}
		if len(caps.Options) != 0 {
			t.Errorf("Resolve(%q, %q) options=%v, want zero", c.provider, c.id, caps.Options)
		}
		if got := (ModelDescriptor{ID: c.id, Provider: c.provider}).GetReasoningCapability(); got.Supported {
			t.Errorf("GetReasoningCapability(%q) supported=true, want unsupported", c.id)
		}
	}
}

// The generic provider fallback still serves genuinely configurable models.
func TestResolveGenericFallbackIntact(t *testing.T) {
	caps := ResolveReasoningCapability("openai", "openai/o1")
	if !caps.Supported || !caps.Configurable || len(caps.Options) == 0 {
		t.Fatalf("openai/o1 must stay configurable, got %+v", caps)
	}
	if caps.Options[0].ID != "default" {
		t.Errorf("first option = %q, want default tier", caps.Options[0].ID)
	}
}
