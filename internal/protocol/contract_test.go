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
	if got := NormalizeFinishReason("max-output-tokens"); got != "length" {
		t.Fatalf("normalized finish reason = %q, want length", got)
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
	if got := SelectInteractionContract("change the copy", "autonomy", "mutate"); got != AgenticLoop {
		t.Fatalf("autonomy/mutate = %s, want agentic loop", got)
	}
	if got := SelectInteractionContract("explain the file", "autonomy", "read", "analyze"); got != DirectCompletion {
		t.Fatalf("autonomy/read-only = %s, want direct completion", got)
	}
	if got := SelectInteractionContract("run a command", "autonomy", "shell"); got != AgenticLoop {
		t.Fatalf("autonomy/shell = %s, want agentic loop", got)
	}
}

func TestContractCapabilityCeilings(t *testing.T) {
	agentic := Describe(AgenticLoop)
	if agentic.AllowsOperation(string(OperationShell)) {
		t.Fatal("default agentic contract must not silently grant shell execution")
	}
	if agentic.AllowsOperation(string(OperationDestructive)) {
		t.Fatal("default agentic contract must not silently grant destructive execution")
	}
	structured := Describe(StructuredCompletion)
	if !structured.AllowsTaskType("SHELL_EXEC") {
		t.Fatal("structured plan must be able to carry a shell proposal")
	}
	if structured.AllowsOperation(string(OperationShell)) {
		t.Fatal("structured proposal contract must not grant shell dispatch")
	}
}

func TestExplicitAllowedCapabilitiesCanRaiseSemanticCeiling(t *testing.T) {
	descriptor, err := NewContractDescriptor(AgenticLoop, DescriptorOptions{
		AllowedCapabilities: []Capability{
			CapabilityRead,
			CapabilityAnalyze,
			CapabilityPropose,
			CapabilityMutate,
			CapabilityShell,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.AllowsOperation(string(OperationShell)) {
		t.Fatalf("explicit shell capability was denied by a stale default: %+v", descriptor)
	}
}

func TestModelMetadataOverridesNameHeuristicWhenIdentified(t *testing.T) {
	descriptor, err := PlanDescriptorWithMetadata("vendor/nano", ModelMetadata{
		ID:          "vendor/nano",
		Constrained: false,
	}, ArchetypeGeneric, 512, 0)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.PromptProfile != PromptProfileFull || descriptor.ConstrainedModel {
		t.Fatalf("identified metadata did not override the name heuristic: %+v", descriptor)
	}
}

func TestDescriptorRejectsUnknownPolicyValues(t *testing.T) {
	if _, err := NewContractDescriptor(AgenticLoop, DescriptorOptions{AuthorityCeiling: "root"}); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("authority error = %v, want ErrInvalidContract", err)
	}
	if _, err := NewContractDescriptor(StructuredCompletion, DescriptorOptions{OutputSchema: "not-a-schema"}); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("schema error = %v, want ErrInvalidContract", err)
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
