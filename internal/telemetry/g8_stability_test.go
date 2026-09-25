package telemetry

import (
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/observability"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestG8TelemetrySnapshotsDetachProtocolMetadata(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	binding := protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "g8-contract", "execute", "prompt")
	recorder := NewRecorder()
	recorder.BindProtocol(binding)
	recorder.RecordCompileResult(&observability.CompileResult{
		Phase:              "execute",
		FittedContextScope: "internal/",
		ReservedTokens:     32,
		PromptFingerprint:  "g8-fingerprint",
	})

	snapshot := recorder.ObservabilitySnapshot()
	if snapshot.Protocol.Contract != protocol.AgenticLoop || snapshot.Protocol.AuthorityLevel != protocol.AuthorityExecute {
		t.Fatalf("protocol snapshot = %+v", snapshot.Protocol)
	}
	if snapshot.Protocol.Descriptor == nil {
		t.Fatal("protocol snapshot lost its descriptor")
	}
	// Mutating the caller's descriptor/binding must not rewrite the recorded
	// snapshot or the recorder's detached copy.
	descriptor.AuthorityCeiling = protocol.AuthorityReadOnly
	descriptor.AllowedCapabilities[0] = protocol.CapabilityShell
	binding.Descriptor.AuthorityCeiling = protocol.AuthorityReadOnly
	binding.Descriptor.AllowedCapabilities[0] = protocol.CapabilityShell

	after := recorder.ObservabilitySnapshot()
	if after.Protocol.AuthorityLevel != protocol.AuthorityExecute || after.Protocol.Descriptor == nil || after.Protocol.Descriptor.AuthorityCeiling != protocol.AuthorityExecute {
		t.Fatalf("recorded binding was aliased to caller state: %+v", after.Protocol)
	}
	if after.Protocol.Descriptor.AllowedCapabilities[0] == protocol.CapabilityShell {
		t.Fatal("recorded capability slice was aliased to caller state")
	}
}

func TestG8TelemetryConcurrentRecordingRemainsSnapshotSafe(t *testing.T) {
	recorder := NewRecorder()
	descriptor := protocol.Describe(protocol.StructuredCompletion)
	binding := protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "g8-concurrent", "plan", "auto")

	const writers = 8
	const records = 32
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < records; j++ {
				recorder.RecordProviderExecution(events.ProviderExecutionPayload{
					Provider:          "g8",
					FinishReason:      "stop",
					ProtocolTelemetry: binding,
				})
				recorder.RecordAdmission(events.AdmissionDecisionPayload{
					Allowed:           true,
					ReasonCode:        "admitted",
					ProtocolTelemetry: binding,
				})
				_ = recorder.ObservabilitySnapshot()
			}
		}()
	}
	wg.Wait()
	snapshot := recorder.ObservabilitySnapshot()
	if len(snapshot.Providers) != writers*records || len(snapshot.Admissions) != writers*records {
		t.Fatalf("concurrent records = providers:%d admissions:%d, want %d each", len(snapshot.Providers), len(snapshot.Admissions), writers*records)
	}
}
