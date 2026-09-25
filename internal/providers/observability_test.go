package providers

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestContractSerializationCarriesObservabilityMetadata(t *testing.T) {
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityPropose,
		OutputSchema:     protocol.SchemaPlanJSON,
	})
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	req := ai.Request{
		Model:               "vendor/model",
		Messages:            []ai.Message{{Role: "user", Content: "private prompt"}},
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		ContractID:          "contract-42",
		Mode:                "build",
		AuthorityLevel:      descriptor.AuthorityCeiling,
		SchemaMode:          ai.SchemaModePrompt,
	}
	prepared, plan, err := PrepareContractRequest("openrouter", req)
	if err != nil {
		t.Fatalf("PrepareContractRequest: %v", err)
	}
	if prepared.ContractID != "contract-42" || prepared.Mode != "build" || prepared.AuthorityLevel != descriptor.AuthorityCeiling {
		t.Fatalf("request metadata = %+v", prepared)
	}
	if plan.ContractID != "contract-42" || plan.Mode != "build" || plan.AuthorityLevel != descriptor.AuthorityCeiling {
		t.Fatalf("plan metadata = %+v", plan)
	}
	if plan.SchemaMode != ai.SchemaModePrompt || !plan.SchemaFallback || plan.PromptChars == 0 || plan.PromptFingerprint == "" {
		t.Fatalf("schema/structural metadata = %+v", plan)
	}
}

func TestStampResponseMetadataCarriesDurationAndTruncation(t *testing.T) {
	descriptor := protocol.Describe(protocol.StructuredCompletion)
	req := ai.Request{
		Model:               "vendor/model",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		ContractID:          "contract-43",
		Mode:                "build",
		SchemaMode:          ai.SchemaModePrompt,
	}
	_, plan, err := PrepareContractRequest("anthropic", req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	start := time.Now().Add(-50 * time.Millisecond)
	resp := &ai.Response{
		Content: "partial",
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     9,
			CompletionTokens: 4,
			TotalTokens:      13,
			RequestStartedAt: start,
			FirstTokenAt:     start.Add(10 * time.Millisecond),
			CompletedAt:      time.Now(),
		},
	}
	StampResponseMetadata(resp, "anthropic", "vendor/model", plan, "max_tokens")
	metadata := resp.Metadata()
	if metadata.ContractID != "contract-43" || metadata.Mode != "build" || metadata.AuthorityLevel != descriptor.AuthorityCeiling {
		t.Fatalf("response metadata = %+v", metadata)
	}
	if metadata.FinishReason != "length" || !metadata.Truncated || metadata.RequestDuration <= 0 || metadata.FirstTokenLatency <= 0 {
		t.Fatalf("response timing/truncation = %+v", metadata)
	}
	if metadata.SchemaMode != ai.SchemaModePrompt || !metadata.SchemaFallback || metadata.PromptFingerprint == "" {
		t.Fatalf("response schema metadata = %+v", metadata)
	}
}
