package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/protocol"
)

func legacyStructuredDescriptor(t *testing.T) protocol.ContractDescriptor {
	t.Helper()
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		StructuralOutputSchema: `{"type":"object","required":["ok"]}`,
		SchemaVersion:          "legacy.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestPreparePromptContractNativeAndFallback(t *testing.T) {
	descriptor := legacyStructuredDescriptor(t)
	native, plan, err := preparePromptContract("openai", PromptRequest{
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NativeSchema || plan.ResponseFormat == nil {
		t.Fatalf("native plan = %+v", plan)
	}
	if strings.Contains(native.System, structuredOutputPromptMarker) {
		t.Fatal("native request received a prompt fallback")
	}
	raw, err := json.Marshal(openAIReq{ResponseFormat: plan.ResponseFormat})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"json_schema"`) {
		t.Fatalf("native response format = %s", raw)
	}

	fallback, fallbackPlan, err := preparePromptContract("anthropic", PromptRequest{
		System:              "base",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallbackPlan.NativeSchema || !strings.Contains(fallback.System, structuredOutputPromptMarker) {
		t.Fatalf("fallback plan = %+v, system=%q", fallbackPlan, fallback.System)
	}
}

func TestLegacyOllamaSchemaIsOnlyNativeWhenSelected(t *testing.T) {
	descriptor := legacyStructuredDescriptor(t)
	_, plan, err := preparePromptContract("ollama", PromptRequest{
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nativePromptSchema(plan)) == 0 {
		t.Fatal("native Ollama schema was empty")
	}
	plan.NativeSchema = false
	if nativePromptSchema(plan) != nil {
		t.Fatal("fallback Ollama plan retained a native schema")
	}
}
