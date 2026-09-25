package telemetry

import (
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/observability"
	"github.com/PizenLabs/izen/internal/protocol"
)

// CompileResult and CompileMetrics are the designated telemetry names for the
// dependency-neutral compiler projection.
type CompileResult = observability.CompileResult
type CompileMetrics = observability.CompileResult

// ProtocolBinding is the shared InteractionContract identity envelope.
type ProtocolBinding = protocol.ObservabilityBinding

// ProtocolPayload is the common interaction identity carried by protocol
// observability events. It is safe to persist in an audit log.
type ProtocolPayload struct {
	RequestID string
	SessionID string
	Mode      string
	Binding   protocol.ObservabilityBinding
}

// InteractionPayload records the protocol identity attached to one execution or
// dispatch boundary.
type InteractionPayload struct {
	ProtocolPayload
}

// ContextCompilationPayload projects a context compiler result into the
// engine telemetry stream. It contains no rendered prompt or file content.
type ContextCompilationPayload struct {
	ProtocolPayload
	Result observability.CompileResult
}

// ProviderExecutionPayload is the provider-neutral terminal dispatch record.
type ProviderExecutionPayload struct {
	ProtocolPayload
	Provider          string
	Model             string
	Duration          time.Duration
	RequestDuration   time.Duration
	FirstTokenLatency time.Duration
	StreamingDuration time.Duration
	Usage             ai.ProviderUsage
	FinishReason      string
	Truncated         bool
	NativeSchema      bool
	SchemaMode        string
	SchemaFallback    bool
	PromptChars       int
	OutputChars       int
	PromptFingerprint string
}

// AdmissionDecisionPayload records an admission/action scope verdict without
// carrying the submitted prompt or command bytes.
type AdmissionDecisionPayload struct {
	ProtocolPayload
	Strategy       string
	Allowed        bool
	RequestedScope string
	Action         string
	ActionSource   string
	Capability     string
	Reason         string
	ReasonCode     string
}

// NewInteractionTelemetry emits the protocol identity binding.
func NewInteractionTelemetry(requestID, sessionID, mode string, binding protocol.ObservabilityBinding) Event {
	return newEvent(EventInteractionTelemetry, &InteractionPayload{ProtocolPayload: protocolPayload(requestID, sessionID, mode, binding)})
}

// NewContextCompilation emits the content-free CompileResult projection.
func NewContextCompilation(requestID, sessionID string, result *observability.CompileResult, binding protocol.ObservabilityBinding) Event {
	projection := observability.CompileResult{}
	if result != nil {
		projection = *result
		projection = projection.Normalize()
	}
	mode := ""
	if binding.Mode != "" {
		mode = binding.Mode
	}
	return newEvent(EventContextCompilation, &ContextCompilationPayload{
		ProtocolPayload: protocolPayload(requestID, sessionID, mode, binding),
		Result:          projection,
	})
}

// NewProviderExecution emits standardized provider duration/usage/schema/
// truncation telemetry from the provider response metadata.
func NewProviderExecution(requestID, sessionID string, metadata ai.ResponseMetadata, outputChars int, binding protocol.ObservabilityBinding) Event {
	ai.ApplyResponseTiming(&metadata, metadata.Usage)
	mode := metadata.Mode
	if mode == "" {
		mode = binding.Mode
	}
	if binding.ContractID == "" {
		binding.ContractID = metadata.ContractID
	}
	if binding.AuthorityLevel == "" {
		binding.AuthorityLevel = metadata.AuthorityLevel
	}
	binding.SchemaFallback = metadata.SchemaFallback
	if binding.SchemaMode == "" {
		binding.SchemaMode = string(metadata.SchemaMode)
	}
	metadata.FinishReason = protocol.NormalizeFinishReason(metadata.FinishReason)
	return newEvent(EventProviderExecution, &ProviderExecutionPayload{
		ProtocolPayload:   protocolPayload(requestID, sessionID, mode, binding),
		Provider:          metadata.Provider,
		Model:             metadata.Model,
		Duration:          metadata.Duration,
		RequestDuration:   metadata.RequestDuration,
		FirstTokenLatency: metadata.FirstTokenLatency,
		StreamingDuration: metadata.StreamingDuration,
		Usage:             metadata.Usage,
		FinishReason:      metadata.FinishReason,
		Truncated:         metadata.Truncated || protocol.IsOutputTruncatedReason(metadata.FinishReason),
		NativeSchema:      metadata.NativeSchema,
		SchemaMode:        string(metadata.SchemaMode),
		SchemaFallback:    metadata.SchemaFallback,
		PromptChars:       metadata.PromptChars,
		OutputChars:       outputChars,
		PromptFingerprint: metadata.PromptFingerprint,
	})
}

// NewAdmissionDecision emits a bounded admission audit event.
func NewAdmissionDecision(requestID, sessionID, strategy string, allowed bool, requestedScope, action, actionSource, capability, reason, reasonCode string, binding protocol.ObservabilityBinding) Event {
	return newEvent(EventAdmissionDecision, &AdmissionDecisionPayload{
		ProtocolPayload: protocolPayload(requestID, sessionID, binding.Mode, binding),
		Strategy:        strategy,
		Allowed:         allowed,
		RequestedScope:  requestedScope,
		Action:          action,
		ActionSource:    actionSource,
		Capability:      capability,
		Reason:          reason,
		ReasonCode:      reasonCode,
	})
}

func protocolPayload(requestID, sessionID, mode string, binding protocol.ObservabilityBinding) ProtocolPayload {
	return ProtocolPayload{
		RequestID: requestID,
		SessionID: sessionID,
		Mode:      mode,
		Binding:   binding.Normalize(),
	}
}
