package telemetry

import (
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/observability"
	"github.com/PizenLabs/izen/internal/protocol"
)

// CompileResult is the content-free contextcompiler projection accepted by the
// process-wide telemetry recorder.
type CompileResult = observability.CompileResult
type CompileMetrics = observability.CompileResult
type ProtocolBinding = protocol.ObservabilityBinding
type AdmissionEvent = events.AdmissionDecisionPayload
type ProviderEvent = events.ProviderExecutionPayload

// ObservabilitySnapshot is a detached view of the protocol-aware telemetry
// attached to a Recorder. It is safe to serialize or hand to an audit sink.
type ObservabilitySnapshot struct {
	Protocol   ProtocolBinding
	Compile    *CompileResult
	Admissions []AdmissionEvent
	Providers  []ProviderEvent
}

// BindProtocol records the InteractionContract identity for subsequent
// compiler/provider/admission observations.
func (r *Recorder) BindProtocol(binding ProtocolBinding) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.protocolBinding = binding.Normalize()
	r.mu.Unlock()
}

// Protocol returns a detached protocol binding.
func (r *Recorder) Protocol() ProtocolBinding {
	if r == nil {
		return ProtocolBinding{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.protocolBinding.Clone()
}

// RecordCompileResult records reserved tokens, truncation/drop counts, fitted
// scope and the non-reversible prompt fingerprint from contextcompiler.
func (r *Recorder) RecordContextCompilation(result *CompileResult) {
	r.RecordCompileResult(result)
}

func (r *Recorder) RecordCompileResult(result *CompileResult) {
	if r == nil || result == nil {
		return
	}
	copy := result.Normalize()
	r.mu.Lock()
	r.compileResult = &copy
	r.mu.Unlock()
}

// RecordAdmission records one strategy/action scope verdict.
func (r *Recorder) RecordAdmission(event AdmissionEvent) {
	if r == nil {
		return
	}
	binding := event.ProtocolTelemetry
	event.ProtocolTelemetry = binding.Normalize()
	r.mu.Lock()
	r.admissionEvents = append(r.admissionEvents, event)
	r.mu.Unlock()
}

// RecordProviderExecution records provider duration, usage, schema fallback and
// truncation provenance without accepting raw prompt/response text.
func (r *Recorder) RecordProviderExecution(event ProviderEvent) {
	if r == nil {
		return
	}
	binding := event.ProtocolTelemetry
	event.ProtocolTelemetry = binding.Normalize()
	r.mu.Lock()
	r.providerEvents = append(r.providerEvents, event)
	r.mu.Unlock()
}

// ObservabilitySnapshot returns a detached protocol/compiler/provider view.
func (r *Recorder) ObservabilitySnapshot() ObservabilitySnapshot {
	if r == nil {
		return ObservabilitySnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := ObservabilitySnapshot{
		Protocol:   r.protocolBinding.Clone(),
		Admissions: append([]AdmissionEvent(nil), r.admissionEvents...),
		Providers:  append([]ProviderEvent(nil), r.providerEvents...),
	}
	if r.compileResult != nil {
		compile := r.compileResult.Normalize()
		out.Compile = &compile
	}
	return out
}

// ProtocolSnapshot is a short alias for integrations that use the term
// snapshot for the protocol-aware view.
func (r *Recorder) ProtocolSnapshot() ObservabilitySnapshot { return r.ObservabilitySnapshot() }

// RecordProvider is a compact constructor for callers that do not need to name
// the longer execution-event type.
func RecordProvider(requestID, sessionID string, metadata ProviderEvent) {
	metadata.RequestID = requestID
	metadata.SessionID = sessionID
	defaultRecorder.RecordProviderExecution(metadata)
}

// ProviderDuration returns the latest recorded provider duration, or zero when
// no provider invocation has been observed.
func (r *Recorder) ProviderDuration() time.Duration {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.providerEvents) == 0 {
		return 0
	}
	return r.providerEvents[len(r.providerEvents)-1].Duration
}

// Global protocol-aware recorder helpers.
func BindProtocol(binding ProtocolBinding)      { defaultRecorder.BindProtocol(binding) }
func RecordCompileResult(result *CompileResult) { defaultRecorder.RecordCompileResult(result) }
func RecordContextCompilation(result *CompileResult) {
	defaultRecorder.RecordContextCompilation(result)
}
func RecordAdmission(event AdmissionEvent)        { defaultRecorder.RecordAdmission(event) }
func RecordProviderExecution(event ProviderEvent) { defaultRecorder.RecordProviderExecution(event) }
func ObservabilitySnapshotFor(recorder *Recorder) ObservabilitySnapshot {
	return recorder.ObservabilitySnapshot()
}
