package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestG3ContractBoundSynthesisUsesDescriptorAndMetadata(t *testing.T) {
	var captured ai.Request
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		PromptProfile: protocol.PromptProfileCompact,
		Archetype:     protocol.ArchetypeReactNext,
		OutputSchema:  protocol.SchemaPlanJSON,
		SchemaVersion: "plan.atomic_tasks.v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(NewPlanStore())
	engine.SetModelMetadata(protocol.ModelMetadata{
		ID:              "vendor/model",
		MaxOutputTokens: 64,
		Constrained:     true,
	})
	engine.SetProvider(func(_ context.Context, req ai.Request) (*ai.Response, error) {
		captured = req
		return &ai.Response{Content: `{"architectural_strategy":"contract","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix layout","rationale":"requested","solution":"done"}]}`, FinishReason: "stop"}, nil
	})

	tasks, err := engine.Synthesize(context.Background(), SynthesisRequest{
		ModelName:           "vendor/model",
		Problem:             "fix the layout",
		Descriptor:          &descriptor,
		Contract:            descriptor,
		InteractionContract: protocol.StructuredCompletion,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one task", tasks)
	}
	if captured.Contract == nil || captured.Contract.Contract != protocol.StructuredCompletion {
		t.Fatalf("request contract = %+v, want structured descriptor", captured.Contract)
	}
	if captured.Contract.PromptProfile != protocol.PromptProfileCompact || captured.MaxTokens != 64 {
		t.Fatalf("descriptor/binding not propagated: contract=%+v max_tokens=%d", captured.Contract, captured.MaxTokens)
	}
	if captured.Contract.Archetype != protocol.ArchetypeReactNext {
		t.Fatalf("archetype = %q, want REACT_NEXT", captured.Contract.Archetype)
	}
	if !strings.Contains(captured.Messages[0].Content, "PLAN CONTRACT") {
		t.Fatalf("compact descriptor did not select plan instructions: %q", captured.Messages[0].Content)
	}
	if got := engine.LastContract(); got == nil || got.Contract != protocol.StructuredCompletion {
		t.Fatalf("LastContract = %+v, want structured descriptor", got)
	}
}

func TestG3ExplicitDescriptorBudgetIsNotWidenedBySynthesis(t *testing.T) {
	var captured ai.Request
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		MaxOutputTokens: 64,
		MaxTasks:        1,
		OutputSchema:    protocol.SchemaPlanJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(NewPlanStore())
	engine.SetProvider(func(_ context.Context, req ai.Request) (*ai.Response, error) {
		captured = req
		return &ai.Response{Content: `{"architectural_strategy":"bounded","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix","rationale":"why","solution":"done"}]}`, FinishReason: "stop"}, nil
	})

	_, err = engine.Synthesize(context.Background(), SynthesisRequest{
		ModelName:           "vendor/model",
		Problem:             "fix the layout",
		InteractionContract: protocol.StructuredCompletion,
		Descriptor:          &descriptor,
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if captured.MaxTokens != descriptor.MaxOutputTokens {
		t.Fatalf("request max_tokens = %d, want descriptor ceiling %d", captured.MaxTokens, descriptor.MaxOutputTokens)
	}
	if got := engine.LastContract(); got == nil || got.MaxOutputTokens != descriptor.MaxOutputTokens || got.MaxTasks != descriptor.MaxTasks {
		t.Fatalf("last contract widened: %+v", got)
	}
}

func TestG3SchemaGateRejectsInvalidArtifactsBeforeTaskParsing(t *testing.T) {
	toolDescriptor := protocol.Describe(protocol.ToolEnabledCompletion)
	if err := ValidateContractOutput("not json", toolDescriptor); !errors.Is(err, ErrContractSchema) {
		t.Fatalf("tool JSON schema error = %v, want ErrContractSchema", err)
	}
	vanilla := protocol.Describe(protocol.StructuredCompletion)
	vanilla.Archetype = protocol.ArchetypeVanillaWeb
	if err := ValidateTasksForContract([]Task{{Type: "FILE_MUTATE", Target: "index.html", Description: "fix"}}, vanilla, true); err != nil {
		t.Fatalf("VANILLA_WEB fast-track file task rejected: %v", err)
	}
	descriptor := protocol.Describe(protocol.StructuredCompletion)
	for name, content := range map[string]string{
		"prose":   "I would edit index.html",
		"array":   `[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix"}]`,
		"unknown": `{"architectural_strategy":"x","atomic_tasks":[{"task_id":1,"strategy":"FS_DELETE","file":"index.html","description":"delete"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateContractOutput(content, descriptor); err == nil {
				t.Fatal("schema-invalid artifact was accepted")
			} else if !errors.Is(err, ErrContractSchema) {
				t.Fatalf("error = %v, want ErrContractSchema", err)
			}
		})
	}
}

func TestG3AuthorityCeilingRejectsPlanMutationTasks(t *testing.T) {
	direct := protocol.Describe(protocol.DirectCompletion)
	if err := ValidateTasksForContract([]Task{{Type: "FILE_MUTATE", Target: "index.html"}}, direct); !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want contract authority rejection", err)
	}
}
