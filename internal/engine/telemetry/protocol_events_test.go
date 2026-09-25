package telemetry_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	. "github.com/PizenLabs/izen/internal/engine/telemetry"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestProtocolEventsCarryContractAndContentFreeCompileMetrics(t *testing.T) {
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityPropose,
		OutputSchema:     protocol.SchemaPlanJSON,
	})
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	binding := protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "contract-7", "build", "prompt")
	binding = binding.WithSchemaFallback(true)
	result := (&contextcompiler.CompiledContext{
		Budget:         contextcompiler.Budget{Total: 4000, Reserved: 120, Available: 3880},
		UsedTokens:     900,
		ContextTokens:  700,
		Truncated:      true,
		Dropped:        3,
		TruncatedFiles: []string{"internal/auth.go"},
		Scope:          "internal/auth.go",
		Phase:          contextcompiler.PhaseExecute,
		Policy:         "repository",
	}).CompileResult()

	event := NewContextCompilation("req-7", "sess-7", &result, binding)
	if event.Type() != EventContextCompilation {
		t.Fatalf("event type = %s", event.Type())
	}
	payload, ok := event.Payload().(*ContextCompilationPayload)
	if !ok {
		t.Fatalf("payload type = %T", event.Payload())
	}
	if payload.Binding.Contract != protocol.StructuredCompletion || payload.Binding.ContractID != "contract-7" {
		t.Fatalf("binding = %+v", payload.Binding)
	}
	if payload.Binding.AuthorityLevel != protocol.AuthorityPropose || payload.Binding.Mode != "build" || payload.Binding.SchemaMode != "prompt" || !payload.Binding.SchemaFallback {
		t.Fatalf("protocol metadata = %+v", payload.Binding)
	}
	if payload.Result.ReservedTokens != 120 || payload.Result.TruncatedFileCount != 1 || payload.Result.DropCount != 3 {
		t.Fatalf("compiler metrics = %+v", payload.Result)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "internal/auth.go") == false {
		t.Fatalf("structural file path missing: %s", data)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "prompt text") {
		t.Fatalf("content-free projection leaked raw content: %s", data)
	}
}

func TestProviderExecutionTelemetryNormalizesTimingAndTruncation(t *testing.T) {
	start := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	usage := ai.ProviderUsage{
		Known:            true,
		PromptTokens:     120,
		CompletionTokens: 30,
		TotalTokens:      150,
		RequestStartedAt: start,
		FirstTokenAt:     start.Add(40 * time.Millisecond),
		CompletedAt:      start.Add(100 * time.Millisecond),
		FinishReason:     "max_tokens",
	}
	metadata := ai.ResponseMetadata{
		Provider:          "openrouter",
		Model:             "vendor/model",
		FinishReason:      "max_tokens",
		Truncated:         true,
		SchemaMode:        ai.SchemaModePrompt,
		SchemaFallback:    true,
		PromptFingerprint: "fingerprint",
		Usage:             usage,
	}
	binding := protocol.NewObservabilityBinding(protocol.StructuredCompletion, nil, "contract-9", "build", "prompt")
	event := NewProviderExecution("req-9", "sess-9", metadata, 88, binding)
	payload := event.Payload().(*ProviderExecutionPayload)
	if payload.RequestDuration != 100*time.Millisecond || payload.FirstTokenLatency != 40*time.Millisecond || payload.StreamingDuration != 60*time.Millisecond {
		t.Fatalf("timing = %+v", payload)
	}
	if payload.FinishReason != "length" || !payload.Truncated || !payload.SchemaFallback || payload.Usage.CompletionTokens != 30 {
		t.Fatalf("provider metrics = %+v", payload)
	}
}
