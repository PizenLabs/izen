package providers

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
)

func structuredTestDescriptor(t *testing.T) protocol.ContractDescriptor {
	t.Helper()
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		OutputSchema:           protocol.SchemaJSON,
		StructuralOutputSchema: `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`,
		SchemaVersion:          "test.schema.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestPrepareContractRequestUsesNativeJSONSchema(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	req := ai.Request{
		Model:               "gpt-4o",
		System:              "answer the request",
		Messages:            []ai.Message{{Role: "user", Content: "hello"}},
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	}
	prepared, plan, err := PrepareContractRequest("openai", req)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NativeSchema || plan.ResponseFormat == nil || plan.ResponseFormat.Type != "json_schema" {
		t.Fatalf("native serialization plan = %+v", plan)
	}
	if strings.Contains(prepared.System, StructuredOutputPromptMarker) {
		t.Fatal("native schema unexpectedly added a prompt fallback")
	}
	body := openaiRequest{Model: req.Model, Messages: []openaiMessage{{Role: "user", Content: prepared.System}}, ResponseFormat: prepared.ResponseFormat}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	format, ok := payload["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("response_format = %#v", payload["response_format"])
	}
	if _, ok := format["json_schema"].(map[string]any); !ok {
		t.Fatalf("json_schema envelope missing: %#v", format)
	}
}

func TestOpenRouterBuildRequestUsesNativeSchemaWithoutPromptInflation(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	req := ai.Request{
		Model:               "openai/gpt-4o",
		System:              "base",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	}
	prepared, plan, err := PrepareContractRequest("openrouter", req)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NativeSchema {
		t.Fatal("OpenRouter model should use native schema transport")
	}
	body := NewOpenRouterProvider("k", "openai/gpt-4o", "").buildRequest(
		"openai/gpt-4o",
		NewOpenRouterProvider("k", "openai/gpt-4o", "").buildMessages(prepared),
		prepared,
		false,
	)
	for _, message := range body.Messages {
		if strings.Contains(message.Content, StructuredOutputPromptMarker) {
			t.Fatalf("native OpenRouter request carried fallback prompt: %+v", body.Messages)
		}
	}
	if body.ResponseFormat == nil || body.ResponseFormat.Type != "json_schema" {
		t.Fatalf("OpenRouter response format = %+v", body.ResponseFormat)
	}
}

func TestPrepareContractRequestFallsBackToOneCompactPromptConstraint(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	req := ai.Request{
		Model:               "claude-3-7-sonnet",
		System:              "follow the repository contract",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	}
	prepared, plan, err := PrepareContractRequest("anthropic", req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NativeSchema || plan.ResponseFormat != nil {
		t.Fatalf("anthropic plan = %+v", plan)
	}
	if strings.Count(prepared.System, StructuredOutputPromptMarker) != 1 {
		t.Fatalf("fallback constraint count in system = %d; system=%q", strings.Count(prepared.System, StructuredOutputPromptMarker), prepared.System)
	}
	if !strings.Contains(prepared.System, `"required":["ok"]`) {
		t.Fatalf("fallback omitted schema: %q", prepared.System)
	}
	preparedAgain, _, err := PrepareContractRequest("anthropic", prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(preparedAgain.System, StructuredOutputPromptMarker) != 1 {
		t.Fatal("schema constraint was injected more than once")
	}
}

func TestOllamaRequestUsesSchemaObjectForNativeContract(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	_, plan, err := PrepareContractRequest("ollama", ai.Request{
		Model:               "qwen",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ollamaRequest{FormatSchema: plan.NativeJSONSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"format":{"type":"object"`) {
		t.Fatalf("ollama native schema missing: %s", raw)
	}
}

func TestFallbackPlanDoesNotExposeNativeSchema(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	_, plan, err := PrepareContractRequest("ollama", ai.Request{
		Model:               "qwen",
		SchemaMode:          ai.SchemaModePrompt,
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NativeSchema || plan.NativeJSONSchema() != nil {
		t.Fatal("prompt fallback exposed a native schema document")
	}
}

func TestStampResponseMetadataNormalizesTruncation(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	plan, err := SerializeInteractionContract("openrouter", ai.Request{
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := &ai.Response{Content: "partial", Usage: ai.ProviderUsage{FinishReason: "max_tokens", Known: true}}
	StampResponseMetadata(response, "openrouter", "vendor/model", plan, "MAX_TOKENS")
	if response.FinishReason != "length" || !response.Truncated {
		t.Fatalf("normalized response = %+v", response)
	}
	if response.Usage.FinishReason != "length" {
		t.Fatalf("usage finish reason = %q", response.Usage.FinishReason)
	}
	if response.InteractionContract != descriptor.Contract || response.Contract == nil {
		t.Fatalf("contract metadata missing: %+v", response.Metadata())
	}
	if !errors.Is(ai.NewOutputTruncated("openrouter", response.FinishReason), ai.ErrOutputTruncated) {
		t.Fatal("truncation sentinel identity was lost")
	}
}

func TestPrepareContractRequestHonorsPromptMode(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	prepared, plan, err := PrepareContractRequest("openai", ai.Request{
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		SchemaMode:          ai.SchemaModePrompt,
		ResponseFormat:      &ai.ResponseFormat{Type: "json_schema", Schema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NativeSchema || prepared.ResponseFormat != nil {
		t.Fatalf("prompt mode retained native schema: %+v", plan)
	}
	if !strings.Contains(prepared.System, StructuredOutputPromptMarker) {
		t.Fatal("prompt mode omitted the fallback constraint")
	}
}

func TestPrepareContractRequestHonorsExplicitSchemaCapability(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	prepared, plan, err := PrepareContractRequest("openai", ai.Request{
		Model:               "vendor/model:free",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		ModelMetadata: &protocol.ModelMetadata{
			ID:                       "vendor/model:free",
			SupportsStructuredOutput: false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NativeSchema || prepared.ResponseFormat != nil {
		t.Fatalf("metadata-forced fallback was not selected: %+v", plan)
	}
	if !strings.Contains(prepared.System, StructuredOutputPromptMarker) {
		t.Fatal("metadata fallback did not inject the compact constraint")
	}
}

func TestPrepareContractRequestDoesNotGrantTools(t *testing.T) {
	direct := protocol.Describe(protocol.DirectCompletion)
	prepared, _, err := PrepareContractRequest("openai", ai.Request{
		InteractionContract: direct.Contract,
		Contract:            &direct,
		Tools:               []ai.ToolDefinition{ai.NewWriteFileTool()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Tools) != 0 {
		t.Fatal("direct completion request leaked tools through the adapter")
	}
}

func TestStreamResultExposesStandardizedMetadata(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	plan, err := SerializeInteractionContract("openai", ai.Request{
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &openaiSSEReader{body: io.NopCloser(strings.NewReader(openAICompatBody("partial", "max_tokens")))}
	result := &OpenAIStreamResult{ReadCloser: reader, sr: reader, metadata: newResponseMetadata("openai", "gpt-4o", plan)}
	if got := drainStream(t, result); got != "partial" {
		t.Fatalf("content = %q", got)
	}
	metadata := result.ResponseMetadata()
	if metadata.FinishReason != "length" || !metadata.Truncated || metadata.Contract == nil {
		t.Fatalf("stream metadata = %+v", metadata)
	}
	if !errors.Is(result.TruncationError(), ai.ErrOutputTruncated) {
		t.Fatal("stream truncation did not expose the canonical error")
	}
}

func TestPrepareContractRequestRejectsMismatchedMetadata(t *testing.T) {
	descriptor := structuredTestDescriptor(t)
	_, _, err := PrepareContractRequest("openai", ai.Request{
		InteractionContract: protocol.DirectCompletion,
		Contract:            &descriptor,
	})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("error = %v, want ErrInvalidContract", err)
	}
}
