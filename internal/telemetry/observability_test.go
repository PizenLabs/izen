package telemetry

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/observability"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestRecorderAcceptsProtocolCompilerAndProviderMetadata(t *testing.T) {
	recorder := NewRecorder()
	descriptor, err := protocol.NewContractDescriptor(protocol.AgenticLoop, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityExecute,
	})
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	binding := protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "contract-t", "build", "native")
	recorder.BindProtocol(binding.WithSchemaFallback(true))
	compile := &observability.CompileResult{
		Phase:              "execute",
		FittedContextScope: "internal/",
		ReservedTokens:     80,
		TruncatedFileCount: 2,
		DropCount:          4,
		PromptFingerprint:  "compile-fingerprint",
	}
	recorder.RecordCompileResult(compile)
	recorder.RecordAdmission(events.AdmissionDecisionPayload{
		Strategy:          "targeted_mutation",
		Allowed:           true,
		ReasonCode:        "admitted",
		ProtocolTelemetry: binding,
	})
	recorder.RecordProviderExecution(events.ProviderExecutionPayload{
		Provider:          "openrouter",
		Model:             "vendor/model",
		Duration:          125,
		FinishReason:      "length",
		Truncated:         true,
		SchemaFallback:    true,
		PromptFingerprint: "provider-fingerprint",
		ProtocolTelemetry: binding,
	})

	snapshot := recorder.ObservabilitySnapshot()
	if snapshot.Protocol.Contract != protocol.AgenticLoop || snapshot.Protocol.AuthorityLevel != protocol.AuthorityExecute || snapshot.Protocol.ContractID != "contract-t" {
		t.Fatalf("protocol snapshot = %+v", snapshot.Protocol)
	}
	if snapshot.Compile == nil || snapshot.Compile.ReservedTokens != 80 || snapshot.Compile.TruncatedFileCount != 2 || snapshot.Compile.DropCount != 4 {
		t.Fatalf("compile snapshot = %+v", snapshot.Compile)
	}
	if len(snapshot.Admissions) != 1 || len(snapshot.Providers) != 1 || snapshot.Providers[0].FinishReason != "length" || !snapshot.Providers[0].Truncated {
		t.Fatalf("provider/admission snapshot = %+v", snapshot)
	}
}
