package registry

import "testing"

// TestRequiresAgenticHarness: a policy-listed or explicitly-flagged model
// requires the agentic wire harness; sibling and normal models do not.
func TestRequiresAgenticHarness(t *testing.T) {
	cases := []struct {
		name string
		m    ModelDescriptor
		want bool
	}{
		{
			name: "policy listed",
			m:    ModelDescriptor{ID: "thinkingmachines/inkling:free", Provider: "openrouter"},
			want: true,
		},
		{
			name: "legacy IneligibleReason is not a wire signal",
			m:    ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter", IneligibleReason: string(ReasonAgenticHarnessOnly)},
			want: false,
		},
		{
			name: "normal",
			m:    ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter"},
			want: false,
		},
		{
			name: "sibling small free",
			m:    ModelDescriptor{ID: "thinkingmachines/inkling-small:free", Provider: "openrouter"},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := RequiresAgenticHarness(tc.m); got != tc.want {
			t.Errorf("%s: RequiresAgenticHarness = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRuntimePathFor pins the exact adaptive-path labels the Model Registry
// preview renders.
func TestRuntimePathFor(t *testing.T) {
	agentic := ModelDescriptor{ID: "thinkingmachines/inkling:free", Provider: "openrouter"}
	if got := RuntimePathFor(agentic); got != RuntimePathAutoPromote {
		t.Errorf("RuntimePathFor(agentic) = %q, want %q", got, RuntimePathAutoPromote)
	}
	if got := RuntimePathFor(agentic); got != "Auto-Promote (Binds ReadOnly Tools for /ask)" {
		t.Errorf("agentic runtime path must be the spec string, got %q", got)
	}
	normal := ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter"}
	if got := RuntimePathFor(normal); got != RuntimePathStandard {
		t.Errorf("RuntimePathFor(normal) = %q, want %q", got, RuntimePathStandard)
	}
	if got := RuntimePathFor(normal); got != "DirectCompletion / AgenticLoop (Standard)" {
		t.Errorf("standard runtime path must be the spec string, got %q", got)
	}
}
