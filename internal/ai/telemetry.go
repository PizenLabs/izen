package ai

import (
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/protocol"
)

// RequestFingerprint returns a non-reversible fingerprint of the structural
// request projection. The prompt is hashed immediately and is never retained
// in telemetry. Callers should pass the final prepared request, not the raw
// caller-owned slice.
func RequestFingerprint(req Request) string {
	var b strings.Builder
	b.WriteString(req.Model)
	b.WriteByte(0)
	b.WriteString(req.System)
	for _, message := range req.Messages {
		b.WriteByte(0)
		b.WriteString(message.Role)
		b.WriteByte(0)
		b.WriteString(message.Content)
	}
	return protocol.Fingerprint(b.String())
}

// RequestChars returns the number of structural prompt characters that cross
// the provider boundary. It deliberately counts bytes-free Go string
// characters through len for compatibility with the existing token heuristic.
func RequestChars(req Request) int {
	n := len(req.System)
	for _, message := range req.Messages {
		n += len(message.Content)
	}
	return n
}

// SchemaModeForTelemetry normalizes the request's schema choice for audit
// records. Empty means the adapter's automatic decision.
func SchemaModeForTelemetry(mode SchemaMode) SchemaMode {
	if strings.TrimSpace(string(mode)) == "" {
		return SchemaModeAuto
	}
	return mode
}

// ProtocolBinding projects the request's semantic identity into the shared
// protocol observability envelope. It does not validate or grant authority.
func ProtocolBinding(req Request) protocol.ObservabilityBinding {
	contract := req.InteractionContract
	var descriptor *protocol.ContractDescriptor
	if req.Contract != nil {
		copy := req.Contract.Clone()
		descriptor = &copy
		if contract == "" {
			contract = copy.Contract
		}
	}
	binding := protocol.NewObservabilityBinding(contract, descriptor, req.ContractID, req.Mode, string(SchemaModeForTelemetry(req.SchemaMode)))
	if req.AuthorityLevel != "" {
		binding.AuthorityLevel = req.AuthorityLevel
	}
	return binding
}

// ApplyResponseTiming fills the standardized duration fields from authoritative
// usage timestamps. It is safe to call repeatedly; non-negative values already
// present are preserved.
func ApplyResponseTiming(metadata *ResponseMetadata, usage ProviderUsage) {
	if metadata == nil {
		return
	}
	metadata.Usage = usage
	if !usage.RequestStartedAt.IsZero() && !usage.CompletedAt.IsZero() {
		metadata.RequestDuration = usage.CompletedAt.Sub(usage.RequestStartedAt)
		if metadata.RequestDuration < 0 {
			metadata.RequestDuration = 0
		}
	}
	metadata.Duration = metadata.RequestDuration
	if !usage.RequestStartedAt.IsZero() && !usage.FirstTokenAt.IsZero() {
		metadata.FirstTokenLatency = usage.FirstTokenAt.Sub(usage.RequestStartedAt)
		if metadata.FirstTokenLatency < 0 {
			metadata.FirstTokenLatency = 0
		}
	}
	if !usage.FirstTokenAt.IsZero() && !usage.CompletedAt.IsZero() {
		metadata.StreamingDuration = usage.CompletedAt.Sub(usage.FirstTokenAt)
		if metadata.StreamingDuration < 0 {
			metadata.StreamingDuration = 0
		}
	}
}

// ResponseMetadataWithUsage returns a detached metadata view with the response
// usage and timing fields normalized consistently across adapters.
func ResponseMetadataWithUsage(metadata ResponseMetadata, usage ProviderUsage, finishReason string) ResponseMetadata {
	if metadata.SchemaMode == "" {
		metadata.SchemaMode = SchemaModeAuto
	}
	metadata.FinishReason = protocol.NormalizeFinishReason(finishReason)
	metadata.Truncated = protocol.IsOutputTruncatedReason(metadata.FinishReason)
	ApplyResponseTiming(&metadata, usage)
	return metadata
}

// UsageDuration returns the provider request duration, or zero when the
// provider did not expose enough timestamps.
func UsageDuration(usage ProviderUsage) time.Duration {
	if usage.RequestStartedAt.IsZero() || usage.CompletedAt.IsZero() {
		return 0
	}
	d := usage.CompletedAt.Sub(usage.RequestStartedAt)
	if d < 0 {
		return 0
	}
	return d
}
