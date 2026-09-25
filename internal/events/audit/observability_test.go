package audit

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestAuditLoggerPersistsProtocolTelemetryWithoutRawPayloads(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(64)
	defer bus.Close()
	logger, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityPropose,
		OutputSchema:     protocol.SchemaPlanJSON,
	})
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	binding := protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "contract-audit", "build", "prompt")
	binding = binding.WithSchemaFallback(true)
	bus.Publish(events.NewExecutionStarted("req-audit", "build", "secret=do-not-persist", "sess-audit", binding))
	bus.Publish(events.NewProviderExecution(events.ProviderExecutionPayload{
		RequestID:         "req-audit",
		SessionID:         "sess-audit",
		Provider:          "openrouter",
		Model:             "vendor/model",
		UsageKnown:        true,
		PromptTokens:      10,
		CompletionTokens:  4,
		TotalTokens:       14,
		FinishReason:      "length",
		Truncated:         true,
		SchemaMode:        "prompt",
		SchemaFallback:    true,
		PromptChars:       27,
		OutputChars:       16,
		PromptFingerprint: "sha256-fingerprint",
		ProtocolTelemetry: binding,
	}))
	waitAccepted(t, logger, 2)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readNDJSON(t, filepath.Join(dir, DefaultFileName))
	if len(lines) != 2 {
		t.Fatalf("persisted %d records, want 2", len(lines))
	}
	raw := string(lines[0]) + string(lines[1])
	if strings.Contains(raw, "secret=do-not-persist") {
		t.Fatalf("audit persisted raw prompt: %s", raw)
	}
	var providerPayload map[string]any
	for _, line := range lines {
		var envelope struct {
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		var source struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(line, &source); err != nil {
			t.Fatalf("decode source: %v", err)
		}
		if source.Source == events.EventProviderExecution {
			providerPayload = envelope.Payload
			break
		}
	}
	if providerPayload == nil {
		t.Fatalf("provider execution audit record not found: %s", raw)
	}
	if providerPayload["finish_reason"] != "length" && providerPayload["FinishReason"] != "length" {
		t.Fatalf("finish reason missing from audit payload: %#v", providerPayload)
	}
	if providerPayload["prompt_fingerprint"] != "sha256-fingerprint" && providerPayload["PromptFingerprint"] != "sha256-fingerprint" {
		t.Fatalf("prompt fingerprint missing from audit payload: %#v", providerPayload)
	}
	if providerPayload["interaction_contract"] != string(protocol.StructuredCompletion) && providerPayload["Contract"] != string(protocol.StructuredCompletion) {
		t.Fatalf("contract binding missing from audit payload: %#v", providerPayload)
	}
}
