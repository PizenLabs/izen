package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/providers"
)

// contractConformanceCase is the executable contract matrix used by the G8
// suite. The matrix is intentionally data-driven: adding a new interaction
// contract must add one row here rather than silently escaping conformance
// coverage.
type contractConformanceCase struct {
	name               string
	mode               string
	contract           protocol.InteractionContract
	authority          protocol.AuthorityCeiling
	outputSchema       protocol.OutputSchema
	structuredOutput   bool
	tools              bool
	promptProfile      protocol.PromptProfile
	contextPhase       contextcompiler.Phase
	allowedOperations  []protocol.Operation
	proposalOperations []protocol.Operation
}

func contractConformanceCases() []contractConformanceCase {
	return []contractConformanceCase{
		{
			name:               "ask/direct",
			mode:               "ask",
			contract:           protocol.DirectCompletion,
			authority:          protocol.AuthorityReadOnly,
			outputSchema:       protocol.SchemaText,
			structuredOutput:   false,
			tools:              false,
			promptProfile:      protocol.PromptProfileFull,
			contextPhase:       contextcompiler.PhaseInvestigate,
			allowedOperations:  []protocol.Operation{protocol.OperationRead, protocol.OperationVerify},
			proposalOperations: nil,
		},
		{
			name:               "plan/structured",
			mode:               "plan",
			contract:           protocol.StructuredCompletion,
			authority:          protocol.AuthorityPropose,
			outputSchema:       protocol.SchemaPlanJSON,
			structuredOutput:   true,
			tools:              false,
			promptProfile:      protocol.PromptProfileFull,
			contextPhase:       contextcompiler.PhasePlan,
			allowedOperations:  []protocol.Operation{protocol.OperationRead, protocol.OperationVerify},
			proposalOperations: []protocol.Operation{protocol.OperationFileMutate, protocol.OperationShell},
		},
		{
			name:               "execute/tool-enabled",
			mode:               "execute",
			contract:           protocol.ToolEnabledCompletion,
			authority:          protocol.AuthorityPropose,
			outputSchema:       protocol.SchemaJSON,
			structuredOutput:   false,
			tools:              true,
			promptProfile:      protocol.PromptProfileFull,
			contextPhase:       contextcompiler.PhaseExecute,
			allowedOperations:  []protocol.Operation{protocol.OperationRead, protocol.OperationVerify, protocol.OperationTool},
			proposalOperations: []protocol.Operation{protocol.OperationFileMutate, protocol.OperationShell, protocol.OperationTool},
		},
		{
			name:               "execute/agentic",
			mode:               "execute",
			contract:           protocol.AgenticLoop,
			authority:          protocol.AuthorityExecute,
			outputSchema:       protocol.SchemaText,
			structuredOutput:   false,
			tools:              true,
			promptProfile:      protocol.PromptProfileFull,
			contextPhase:       contextcompiler.PhaseExecute,
			allowedOperations:  []protocol.Operation{protocol.OperationRead, protocol.OperationVerify, protocol.OperationFileMutate},
			proposalOperations: []protocol.Operation{protocol.OperationFileMutate},
		},
	}
}

func TestContractConformanceMatrix(t *testing.T) {
	for _, tc := range contractConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := protocol.Describe(tc.contract)
			if descriptor.Contract != tc.contract || descriptor.Kind != tc.contract {
				t.Fatalf("identity = %q/%q, want %q", descriptor.Contract, descriptor.Kind, tc.contract)
			}
			if descriptor.AuthorityCeiling != tc.authority {
				t.Fatalf("authority ceiling = %q, want %q", descriptor.AuthorityCeiling, tc.authority)
			}
			if descriptor.OutputSchema != tc.outputSchema {
				t.Fatalf("output schema = %q, want %q", descriptor.OutputSchema, tc.outputSchema)
			}
			if descriptor.StructuredOutput != tc.structuredOutput || descriptor.Tools != tc.tools {
				t.Fatalf("wire flags = structured:%v tools:%v, want structured:%v tools:%v", descriptor.StructuredOutput, descriptor.Tools, tc.structuredOutput, tc.tools)
			}
			if descriptor.PromptProfile != tc.promptProfile {
				t.Fatalf("prompt profile = %q, want %q", descriptor.PromptProfile, tc.promptProfile)
			}
			if err := descriptor.Validate(); err != nil {
				t.Fatalf("default descriptor is invalid: %v", err)
			}
			if tc.contract == protocol.ToolEnabledCompletion {
				// Tool-enabled completion is a bounded execute sub-step, not
				// the default contract for a whole /execute turn. Selection is
				// therefore checked through the explicit capability request;
				// the mode still permits the descriptor below.
				if got := protocol.SelectInteractionContract("propose a tool call", "autonomy", "tool"); got != tc.contract {
					t.Fatalf("tool capability selected %q, want %q", got, tc.contract)
				}
			} else if got := protocol.SelectInteractionContract("summarize the current state", tc.mode); got != tc.contract {
				t.Fatalf("mode %q selected %q, want %q", tc.mode, got, tc.contract)
			}
			if !protocol.ModeAllowsInteraction(tc.mode, tc.contract) {
				t.Fatalf("mode %q rejected its own contract %q", tc.mode, tc.contract)
			}
			for _, operation := range tc.allowedOperations {
				if !descriptor.AllowsOperation(string(operation)) {
					t.Fatalf("%q rejected allowed operation %q", tc.contract, operation)
				}
			}
			for _, operation := range []protocol.Operation{protocol.OperationShell, protocol.OperationDestructive} {
				if tc.contract != protocol.AgenticLoop && descriptor.AllowsOperation(string(operation)) {
					t.Fatalf("%q unexpectedly allowed %q", tc.contract, operation)
				}
			}
			for _, operation := range tc.proposalOperations {
				if !descriptor.AllowsTaskType(string(operation)) {
					t.Fatalf("%q rejected proposal task %q", tc.contract, operation)
				}
			}
			if tc.contract == protocol.DirectCompletion {
				for _, operation := range []protocol.Operation{protocol.OperationFileMutate, protocol.OperationShell, protocol.OperationTool} {
					if descriptor.AllowsTaskType(string(operation)) {
						t.Fatalf("read-only contract unexpectedly allowed proposal %q", operation)
					}
				}
			}
		})
	}
}

func TestContractConformanceRejectsUnknownPromptProfiles(t *testing.T) {
	if _, err := protocol.NewContractDescriptor(protocol.DirectCompletion, protocol.DescriptorOptions{
		PromptProfile: "unbounded",
	}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("unknown prompt profile error = %v, want ErrInvalidContract", err)
	}
}

func TestContractConformanceModeCeilings(t *testing.T) {
	cases := []struct {
		mode     string
		contract protocol.InteractionContract
		allowed  bool
	}{
		{mode: "ask", contract: protocol.DirectCompletion, allowed: true},
		{mode: "ask", contract: protocol.StructuredCompletion, allowed: true},
		{mode: "ask", contract: protocol.ToolEnabledCompletion, allowed: false},
		{mode: "ask", contract: protocol.AgenticLoop, allowed: false},
		{mode: "plan", contract: protocol.DirectCompletion, allowed: true},
		{mode: "plan", contract: protocol.StructuredCompletion, allowed: true},
		{mode: "plan", contract: protocol.ToolEnabledCompletion, allowed: false},
		{mode: "plan", contract: protocol.AgenticLoop, allowed: false},
		{mode: "execute", contract: protocol.AgenticLoop, allowed: true},
		{mode: "execute", contract: protocol.StructuredCompletion, allowed: true},
	}
	for _, tc := range cases {
		if got := protocol.ModeAllowsInteraction(tc.mode, tc.contract); got != tc.allowed {
			t.Errorf("ModeAllowsInteraction(%q, %q) = %v, want %v", tc.mode, tc.contract, got, tc.allowed)
		}
	}

	// A provider capability bit must never override a restricted mode.
	if got := protocol.SelectInteractionContract("summarize", "ask", "tools", "mutate"); got != protocol.DirectCompletion {
		t.Fatalf("ask with tool/mutation requirements selected %q", got)
	}
	if got := protocol.SelectInteractionContract("plan the migration", "plan", "mutate", "shell"); got != protocol.StructuredCompletion {
		t.Fatalf("plan with mutation requirements selected %q", got)
	}
}

func TestContractConformanceSchemaModeProjection(t *testing.T) {
	baseRequest := func(contract protocol.InteractionContract, descriptor protocol.ContractDescriptor, mode ai.SchemaMode) ai.Request {
		return ai.Request{
			Model:               "openai/gpt-4o",
			InteractionContract: contract,
			Contract:            &descriptor,
			SchemaMode:          mode,
			System:              "runtime contract",
			Messages:            []ai.Message{{Role: "user", Content: "return the requested artifact"}},
			Tools:               ai.FileMutationTools(),
		}
	}

	t.Run("direct has no schema or tools", func(t *testing.T) {
		descriptor := protocol.Describe(protocol.DirectCompletion)
		prepared, plan, err := providers.PrepareContractRequest("openai", baseRequest(descriptor.Contract, descriptor, ai.SchemaModeNative))
		if err != nil {
			t.Fatal(err)
		}
		if plan.StructuredOutput || plan.SchemaFallback || plan.NativeSchema || plan.ResponseFormat != nil {
			t.Fatalf("direct projection acquired a schema: %+v", plan)
		}
		if len(prepared.Tools) != 0 {
			t.Fatalf("direct projection retained %d tools", len(prepared.Tools))
		}
	})

	t.Run("structured prompt fallback", func(t *testing.T) {
		descriptor := protocol.Describe(protocol.StructuredCompletion)
		prepared, plan, err := providers.PrepareContractRequest("anthropic", baseRequest(descriptor.Contract, descriptor, ai.SchemaModePrompt))
		if err != nil {
			t.Fatal(err)
		}
		if !plan.StructuredOutput || !plan.SchemaFallback || plan.NativeSchema || plan.ResponseFormat != nil {
			t.Fatalf("prompt projection = %+v, want one bounded fallback", plan)
		}
		if !strings.Contains(prepared.System, providers.StructuredOutputPromptMarker) {
			t.Fatal("prompt fallback did not carry the schema constraint")
		}
		if len(prepared.Tools) != 0 {
			t.Fatalf("structured proposal projection retained %d tools", len(prepared.Tools))
		}
	})

	t.Run("structured native schema", func(t *testing.T) {
		descriptor := protocol.Describe(protocol.StructuredCompletion)
		prepared, plan, err := providers.PrepareContractRequest("openai", baseRequest(descriptor.Contract, descriptor, ai.SchemaModeNative))
		if err != nil {
			t.Fatal(err)
		}
		if !plan.StructuredOutput || plan.SchemaFallback || !plan.NativeSchema || plan.ResponseFormat == nil {
			t.Fatalf("native projection = %+v", plan)
		}
		if prepared.ResponseFormat == nil || prepared.ResponseFormat.Type != "json_schema" {
			t.Fatalf("native response format = %+v", prepared.ResponseFormat)
		}
	})

	t.Run("tool-enabled preserves tool ceiling and schema mode", func(t *testing.T) {
		descriptor := protocol.Describe(protocol.ToolEnabledCompletion)
		prepared, plan, err := providers.PrepareContractRequest("anthropic", baseRequest(descriptor.Contract, descriptor, ai.SchemaModePrompt))
		if err != nil {
			t.Fatal(err)
		}
		if !plan.StructuredOutput || !plan.SchemaFallback || plan.NativeSchema {
			t.Fatalf("tool-enabled prompt projection = %+v", plan)
		}
		if len(prepared.Tools) != len(ai.FileMutationTools()) {
			t.Fatalf("tool-enabled tools = %d, want %d", len(prepared.Tools), len(ai.FileMutationTools()))
		}
		if !strings.Contains(prepared.System, providers.StructuredOutputPromptMarker) {
			t.Fatal("tool-enabled prompt fallback omitted schema constraint")
		}
	})

	t.Run("agentic preserves tool ceiling but not schema", func(t *testing.T) {
		descriptor := protocol.Describe(protocol.AgenticLoop)
		prepared, plan, err := providers.PrepareContractRequest("openai", baseRequest(descriptor.Contract, descriptor, ai.SchemaModeNative))
		if err != nil {
			t.Fatal(err)
		}
		if plan.StructuredOutput || plan.NativeSchema || plan.ResponseFormat != nil {
			t.Fatalf("text agentic projection acquired schema: %+v", plan)
		}
		if len(prepared.Tools) != len(ai.FileMutationTools()) {
			t.Fatalf("agentic tools = %d, want %d", len(prepared.Tools), len(ai.FileMutationTools()))
		}
	})
}

func TestContractConformanceTokenBudgets(t *testing.T) {
	for _, tc := range contractConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := protocol.NewContractDescriptor(tc.contract, protocol.DescriptorOptions{
				MaxOutputTokens: 37,
				MaxTasks:        2,
				PromptProfile:   protocol.PromptProfileCompact,
			})
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.MaxOutputTokens != 37 || descriptor.MaxTasks != 2 || descriptor.PromptProfile != protocol.PromptProfileCompact {
				t.Fatalf("budget metadata = %+v", descriptor)
			}
			if !strings.Contains(descriptor.PlanInstructions(`{"atomic_tasks":[]}`), "37") {
				t.Fatal("plan instruction did not carry the descriptor token budget")
			}
			profile := strategy.ExecutionStrategyProfile{Strategy: strategy.DirectResponse, MaxOutputTokens: 64}
			err = execution.ValidateContractAdmission(execution.ExecuteRequest{
				Mode:            tc.mode,
				Operation:       string(protocol.OperationRead),
				MaxOutputTokens: 38,
			}, profile, &descriptor)
			if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
				t.Fatalf("over-budget request error = %v, want authority ceiling violation", err)
			}
			err = execution.ValidateContractAdmission(execution.ExecuteRequest{
				Mode:            tc.mode,
				Operation:       string(protocol.OperationRead),
				MaxOutputTokens: 37,
			}, profile, &descriptor)
			if err != nil {
				t.Fatalf("within-budget request rejected: %v", err)
			}
		})
	}
}

func TestContractConformanceContextProjectionBudgets(t *testing.T) {
	for _, tc := range contractConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := protocol.Describe(tc.contract)
			limits := contextcompiler.ResolveTokenBudget(tc.contextPhase, contextcompiler.ModelLimits{
				ContextWindow:         32_000,
				MaxOutputTokens:       1_024,
				RequestedOutputTokens: 256,
			})
			if limits.OutputReserve != 256 || limits.Phase != tc.contextPhase {
				t.Fatalf("resolved token budget = %+v, want reserve 256 for %s", limits, tc.contextPhase)
			}
			compiler := contextcompiler.New()
			agent, err := compiler.CompileAgentContext(t.Context(), contextcompiler.Input{
				Contract:              &descriptor,
				Provider:              "openai",
				Model:                 "openai/gpt-4o",
				ContextWindow:         32_000,
				MaxOutputTokens:       1_024,
				RequestedOutputTokens: 256,
				SystemInstructions:    "system contract",
				UserRequest:           "explain the bounded objective",
			})
			if err != nil {
				t.Fatal(err)
			}
			if agent.Phase != tc.contextPhase {
				t.Fatalf("context phase = %q, want %q", agent.Phase, tc.contextPhase)
			}
			if agent.Budget.Total <= 0 || agent.Budget.Total > tc.contextPhase.ContextBudget() {
				t.Fatalf("context budget = %d, phase ceiling = %d", agent.Budget.Total, tc.contextPhase.ContextBudget())
			}
			if agent.Compiled == nil || agent.Compiled.UsedTokens > agent.Budget.Total {
				t.Fatalf("compiled context exceeded budget: %+v", agent.Compiled)
			}
		})
	}
}

func TestContractConformanceLengthMetadataStopsStructuralConsumers(t *testing.T) {
	response := &ai.Response{
		Provider: "g8-provider",
		Model:    "g8-model",
		Content:  `{"atomic_tasks":[{"task_id":1`,
		Usage: ai.ProviderUsage{
			Known:        true,
			FinishReason: "max_tokens",
		},
	}
	if err := response.OutputError(); !errors.Is(err, protocol.ErrOutputTruncated) {
		t.Fatalf("OutputError = %v, want protocol.ErrOutputTruncated", err)
	}
	metadata := response.Metadata()
	if metadata.FinishReason != "length" || !metadata.Truncated {
		t.Fatalf("normalized response metadata = %+v, want length/truncated", metadata)
	}
	if err := response.OutputError(); !errors.Is(err, protocol.ErrOutputTruncated) {
		t.Fatal("repeated structural-boundary check lost truncation identity")
	}
}

func TestContractConformanceIllegalCapabilitiesUseAuthoritySentinel(t *testing.T) {
	cases := []struct {
		contract  protocol.InteractionContract
		operation protocol.Operation
	}{
		{contract: protocol.DirectCompletion, operation: protocol.OperationFileMutate},
		{contract: protocol.StructuredCompletion, operation: protocol.OperationShell},
		{contract: protocol.AgenticLoop, operation: protocol.OperationShell},
	}
	for _, tc := range cases {
		t.Run(string(tc.contract)+"/"+string(tc.operation), func(t *testing.T) {
			descriptor := protocol.Describe(tc.contract)
			err := execution.ValidateContractAdmission(execution.ExecuteRequest{
				Mode:      map[protocol.InteractionContract]string{protocol.DirectCompletion: "ask", protocol.StructuredCompletion: "plan", protocol.AgenticLoop: "execute"}[tc.contract],
				Operation: string(tc.operation),
			}, strategy.ExecutionStrategyProfile{Strategy: strategy.DirectResponse}, &descriptor)
			if !errors.Is(err, execution.ErrAuthorityExceeded) {
				t.Fatalf("illegal capability error = %v, want ErrAuthorityExceeded", err)
			}
		})
	}
}

func TestContractConformanceAskRejectsStagedActions(t *testing.T) {
	direct := protocol.Describe(protocol.DirectCompletion)
	profile := strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation}
	for _, operation := range []protocol.Operation{protocol.OperationFileMutate, protocol.OperationShell} {
		t.Run(string(operation), func(t *testing.T) {
			err := execution.ValidateContractAdmission(execution.ExecuteRequest{
				Mode: "ask",
				StagedSubTasks: []execution.SubTaskScope{{
					ID:        "g8-ask",
					Operation: string(operation),
				}},
			}, profile, &direct)
			if !errors.Is(err, execution.ErrAuthorityExceeded) {
				t.Fatalf("staged %s error = %v, want ErrAuthorityExceeded", operation, err)
			}
		})
	}
}
