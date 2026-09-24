package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestInteractionContractDescriptorMatrix(t *testing.T) {
	cases := []struct {
		contract   InteractionContract
		structured bool
		tools      bool
		context    ContextProfile
	}{
		{DirectCompletion, false, false, ContextObjective},
		{StructuredCompletion, true, false, ContextRepository},
		{ToolEnabledCompletion, false, true, ContextTarget},
		{AgenticLoop, false, true, ContextRepository},
	}
	for _, tc := range cases {
		d := Describe(tc.contract)
		if d.Contract != tc.contract || d.Kind != tc.contract {
			t.Fatalf("%s descriptor identity = %+v", tc.contract, d)
		}
		if d.StructuredOutput != tc.structured || d.Tools != tc.tools || d.ContextProfile != tc.context {
			t.Fatalf("%s descriptor = %+v", tc.contract, d)
		}
	}
	if _, err := NewContractDescriptor("unknown", DescriptorOptions{}); err == nil {
		t.Fatal("unknown interaction contract was accepted")
	}
}

func TestOutputTruncatedTypedIdentity(t *testing.T) {
	err := NewOutputTruncated("openrouter", "length")
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("errors.Is(%v, ErrOutputTruncated) = false", err)
	}
	var typed *OutputTruncatedError
	if !errors.As(err, &typed) || typed.FinishReason != "length" {
		t.Fatalf("typed truncation carrier = %#v", typed)
	}
	if !IsOutputTruncatedReason("MAX_TOKENS") || !IsOutputTruncatedReason("token_limit") {
		t.Fatal("provider output-ceiling reason was not normalized")
	}
}

func TestSelectInteractionContractRespectsModeCeiling(t *testing.T) {
	if got := SelectInteractionContract("summarize", "ask", "tools"); got != DirectCompletion {
		t.Fatalf("ask/tools = %s, want direct completion", got)
	}
	if got := SelectInteractionContract("return json", "ask", "structured_output"); got != StructuredCompletion {
		t.Fatalf("ask/structured = %s, want structured completion", got)
	}
	if got := SelectInteractionContract("mutate", "plan", "tools"); got != StructuredCompletion {
		t.Fatalf("plan/tools = %s, want structured completion", got)
	}
	if got := SelectInteractionContract("mutate", "build"); got != AgenticLoop {
		t.Fatalf("build = %s, want agentic loop", got)
	}
}

func TestCompactPlanInstructionsArePositive(t *testing.T) {
	instructions := CompactPlanInstructions(`{"atomic_tasks":[]}`, ArchetypeVanillaWeb, 2)
	lower := strings.ToLower(instructions)
	for _, phrase := range []string{"forbidden", "do not", "never"} {
		if strings.Contains(lower, phrase) {
			t.Fatalf("compact instructions contain %q: %s", phrase, instructions)
		}
	}
	if !strings.Contains(instructions, "VANILLA_WEB") || !strings.Contains(instructions, "at most 2") {
		t.Fatalf("compact instructions lost positive contract context: %s", instructions)
	}
}
