package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
)

// StructuredOutputPromptMarker identifies the single compact schema block a
// provider adapter may add when native JSON Schema transport is unavailable.
// Keeping a stable marker makes retries and continuations idempotent.
const StructuredOutputPromptMarker = "[STRUCTURED_OUTPUT_CONTRACT]"

// ResponseEnvelope is the provider-adapter alias for the standardized
// contract/finish-reason payload wrapper.
type ResponseEnvelope = ai.ResponseMetadata

// ContractSerialization is the provider-neutral result of adapting one
// InteractionContract to a wire request. It is intentionally small so callers
// can inspect exactly what an adapter sent without reaching into provider wire
// structs.
type ContractSerialization struct {
	Provider          string                       `json:"provider,omitempty"`
	Contract          protocol.InteractionContract `json:"contract,omitempty"`
	Descriptor        *protocol.ContractDescriptor `json:"descriptor,omitempty"`
	Schema            json.RawMessage              `json:"schema,omitempty"`
	ResponseFormat    *ai.ResponseFormat           `json:"response_format,omitempty"`
	InlineConstraint  string                       `json:"inline_constraint,omitempty"`
	NativeSchema      bool                         `json:"native_schema,omitempty"`
	SchemaFallback    bool                         `json:"schema_fallback,omitempty"`
	StructuredOutput  bool                         `json:"structured_output,omitempty"`
	OutputSchema      protocol.OutputSchema        `json:"output_schema,omitempty"`
	SchemaVersion     string                       `json:"schema_version,omitempty"`
	ContractID        string                       `json:"contract_id,omitempty"`
	Mode              string                       `json:"mode,omitempty"`
	AuthorityLevel    protocol.AuthorityCeiling    `json:"authority_level,omitempty"`
	SchemaMode        ai.SchemaMode                `json:"schema_mode,omitempty"`
	PromptChars       int                          `json:"prompt_chars,omitempty"`
	PromptFingerprint string                       `json:"prompt_fingerprint,omitempty"`
}

// PrepareContractRequest validates and normalizes the semantic contract, then
// prepares a private request copy for one provider. It never mutates the
// caller's descriptor or message slice. Native schema-capable adapters receive
// a response_format/response_schema value; all other adapters receive one
// bounded inline constraint.
func PrepareContractRequest(provider string, req ai.Request) (ai.Request, ContractSerialization, error) {
	descriptor, err := normalizedRequestDescriptor(req)
	if err != nil {
		return ai.Request{}, ContractSerialization{}, err
	}
	plan := ContractSerialization{
		Provider:          strings.ToLower(strings.TrimSpace(provider)),
		ContractID:        strings.TrimSpace(req.ContractID),
		Mode:              strings.TrimSpace(req.Mode),
		SchemaMode:        ai.SchemaModeForTelemetry(req.SchemaMode),
		PromptChars:       ai.RequestChars(req),
		PromptFingerprint: ai.RequestFingerprint(req),
	}
	if descriptor != nil {
		copy := descriptor.Clone()
		plan.Contract = copy.Contract
		plan.Descriptor = &copy
		plan.OutputSchema = copy.OutputSchema
		plan.SchemaVersion = copy.SchemaVersion
		plan.AuthorityLevel = copy.AuthorityCeiling
		plan.StructuredOutput = copy.StructuredOutput || copy.OutputSchema != protocol.SchemaText
	}

	// Carry the normalized identity into the private request even for text
	// contracts. This makes provider responses independently auditable.
	if plan.Descriptor != nil {
		req.InteractionContract = plan.Contract
		req.Contract = plan.Descriptor
		// Tool definitions are a wire capability, not an authority grant. A
		// descriptor that does not declare tools must never leak caller-supplied
		// tools through the adapter boundary.
		if !plan.Descriptor.Tools {
			req.Tools = nil
		}
	}
	if plan.Descriptor == nil || !plan.StructuredOutput {
		plan.SchemaFallback = false
		return req, plan, nil
	}

	schema, err := plan.Descriptor.JSONSchema()
	if err != nil {
		return ai.Request{}, ContractSerialization{}, err
	}
	plan.Schema = append(json.RawMessage(nil), schema...)

	constraint, err := plan.Descriptor.InlineSchemaConstraint()
	if err != nil {
		return ai.Request{}, ContractSerialization{}, err
	}
	plan.InlineConstraint = constraint
	native := nativeSchemaSupported(plan.Provider, req.Model, req.SchemaMode, req.ModelMetadata)
	if native && len(schema) > 0 {
		plan.NativeSchema = true
		plan.SchemaFallback = false
		plan.ResponseFormat = ai.NewJSONSchemaResponseFormat(schemaName(*plan.Descriptor), schema)
		req.ResponseFormat = plan.ResponseFormat
		// A caller may have supplied a generic json_object hint. The contract
		// is authoritative and upgrades it to the exact schema envelope.
		return req, plan, nil
	}

	// Explicit prompt mode is useful for gateways that reject a nominally
	// supported response_format field. It remains a semantic constraint, not a
	// capability downgrade.
	plan.NativeSchema = false
	plan.SchemaFallback = true
	// A contract-bound fallback must not accidentally retain a stale native
	// response_format supplied by a previous attempt.
	req.ResponseFormat = nil
	if constraint != "" {
		injectInlineConstraint(&req, constraint)
	}
	return req, plan, nil
}

// SerializeInteractionContract is a convenience form of
// PrepareContractRequest for callers that need only the serialization plan.
func SerializeInteractionContract(provider string, req ai.Request) (ContractSerialization, error) {
	_, plan, err := PrepareContractRequest(provider, req)
	return plan, err
}

// SerializeContract is a short compatibility alias for
// SerializeInteractionContract.
func SerializeContract(provider string, req ai.Request) (ContractSerialization, error) {
	return SerializeInteractionContract(provider, req)
}

// normalizedRequestDescriptor is the adapter boundary's fail-closed contract
// check. ai.Request.EffectiveContract intentionally returns nil for malformed
// metadata for compatibility; a provider must not silently downgrade malformed
// metadata to an unconstrained request.
func normalizedRequestDescriptor(req ai.Request) (*protocol.ContractDescriptor, error) {
	if req.Contract != nil {
		normalized, err := req.Contract.Clone().Normalize()
		if err != nil {
			return nil, fmt.Errorf("provider: %w: %w", protocol.ErrInvalidContract, err)
		}
		if req.InteractionContract != "" && req.InteractionContract != normalized.Contract {
			return nil, fmt.Errorf("provider: %w: request contract %q does not match descriptor %q", protocol.ErrInvalidContract, req.InteractionContract, normalized.Contract)
		}
		return &normalized, nil
	}
	if req.InteractionContract == "" {
		return nil, nil
	}
	if !req.InteractionContract.Valid() {
		return nil, fmt.Errorf("provider: %w: unknown interaction contract %q", protocol.ErrInvalidContract, req.InteractionContract)
	}
	descriptor := protocol.Describe(req.InteractionContract)
	return &descriptor, nil
}

func nativeSchemaSupported(provider, model string, mode ai.SchemaMode, metadata *protocol.ModelMetadata) bool {
	if metadata != nil {
		if metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(model)) {
			metadata = nil
		}
		if metadata != nil && !metadata.SupportsStructuredOutput {
			return false
		}
		if metadata != nil && metadata.SupportsStructuredOutput {
			return nativeProviderFamily(provider)
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(ai.SchemaModePrompt):
		return false
	case string(ai.SchemaModeNative):
		// Native mode still fails closed for an unknown wire family. The
		// adapter will use its prompt fallback rather than inventing a field.
		return knownNativeProvider(provider, model)
	default:
		return knownNativeProvider(provider, model)
	}
}

// SupportsNativeStructuredOutput reports the provider/model decision used by
// the adapter in automatic mode. It is exported for capability bridges and
// diagnostics; it does not mutate a request or grant tool authority.
func SupportsNativeStructuredOutput(provider, model string) bool {
	return knownNativeProvider(provider, model)
}

func nativeProviderFamily(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openrouter", "groq", "9router", "ninerouter", "opencode", "ollama", "gemini":
		return true
	case "anthropic", "claude":
		// Anthropic Messages has no general response_format field. Native tool
		// calls are translated separately; structured output uses the compact
		// contract prompt rather than a fake tool.
		return false
	default:
		return false
	}
}

func knownNativeProvider(provider, model string) bool {
	providerName := strings.ToLower(strings.TrimSpace(provider))
	modelName := strings.ToLower(strings.TrimSpace(model))
	// Small/free gateway models commonly accept a JSON mode hint but do not
	// enforce a JSON Schema document. Keep the wire request conservative for
	// those models unless the caller supplies explicit ModelMetadata.
	if providerName != "ollama" && protocol.IsConstrainedModel(modelName) {
		return false
	}
	return nativeProviderFamily(providerName)
}

func schemaName(descriptor protocol.ContractDescriptor) string {
	name := "izen_" + string(descriptor.Contract) + "_" + string(descriptor.OutputSchema)
	if descriptor.SchemaVersion != "" {
		name += "_" + descriptor.SchemaVersion
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "izen_interaction_contract"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func injectInlineConstraint(req *ai.Request, constraint string) {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return
	}
	// A continuation or a caller that already serialized the same contract
	// must not accumulate a second schema block.
	if strings.Contains(req.System, StructuredOutputPromptMarker) {
		return
	}
	for _, message := range req.Messages {
		if strings.Contains(message.Content, StructuredOutputPromptMarker) {
			return
		}
	}
	if req.System != "" {
		req.System = strings.TrimSpace(req.System) + "\n\n" + constraint
		return
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "system" {
			req.Messages = append([]ai.Message(nil), req.Messages...)
			req.Messages[i].Content = strings.TrimSpace(req.Messages[i].Content) + "\n\n" + constraint
			return
		}
	}
	req.System = constraint
}

// NativeJSONSchema returns a detached copy of a serialized schema. It is used
// by provider-specific wire structs that cannot carry ai.ResponseFormat
// directly (for example Ollama and Gemini).
func (s ContractSerialization) NativeJSONSchema() json.RawMessage {
	if !s.NativeSchema || len(s.Schema) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.Schema...)
}

// NormalizeFinishReason maps every provider spelling of an output ceiling to
// the canonical "length" label while preserving non-truncation provider labels
// for diagnostics and compatibility with existing callers.
func NormalizeFinishReason(reason string) string {
	return protocol.NormalizeFinishReason(reason)
}

func normalizeUsageMetadata(usage ai.ProviderUsage) ai.ProviderUsage {
	usage.FinishReason = NormalizeFinishReason(usage.FinishReason)
	return usage
}

// NewResponseMetadata builds the same metadata envelope used by streaming
// results. It is exported for adapter integrations that implement a custom
// provider result type.
func NewResponseMetadata(provider, model string, plan ContractSerialization) ai.ResponseMetadata {
	return newResponseMetadata(provider, model, plan)
}

func newResponseMetadata(provider, model string, plan ContractSerialization) ai.ResponseMetadata {
	metadata := ai.ResponseMetadata{
		Provider:            provider,
		Model:               model,
		InteractionContract: plan.Contract,
		ContractID:          plan.ContractID,
		Mode:                plan.Mode,
		AuthorityLevel:      plan.AuthorityLevel,
		SchemaMode:          plan.SchemaMode,
		SchemaFallback:      plan.SchemaFallback,
		PromptChars:         plan.PromptChars,
		PromptFingerprint:   plan.PromptFingerprint,
		FinishReason:        "",
		NativeSchema:        plan.NativeSchema,
		Schema:              append(json.RawMessage(nil), plan.Schema...),
		InlineConstraint:    plan.InlineConstraint,
	}
	if plan.Descriptor != nil {
		copy := plan.Descriptor.Clone()
		metadata.Contract = &copy
		metadata.ContractMetadata = &copy
	}
	return metadata
}

// streamResponseMetadata normalizes the shared stream envelope after the
// reader has observed its terminal state. Keeping this in one helper makes
// duration, truncation and schema-fallback reporting identical across all
// provider adapters.
func streamResponseMetadata(metadata ai.ResponseMetadata, usage ai.ProviderUsage, finishReason string) ai.ResponseMetadata {
	return ai.ResponseMetadataWithUsage(metadata, normalizeUsageMetadata(usage), finishReason)
}

// StampResponseMetadata applies the standardized contract and finish-reason
// wrapper to a non-streaming response. The provider-native reason is retained
// only when it is not a truncation alias; truncation always becomes "length"
// so errors.Is(ErrOutputTruncated) is deterministic at the caller boundary.
func StampResponseMetadata(response *ai.Response, provider, model string, plan ContractSerialization, rawFinishReason string) *ai.Response {
	if response == nil {
		return nil
	}
	finishReason := NormalizeFinishReason(rawFinishReason)
	response.FinishReason = finishReason
	response.Truncated = isOutputLength(rawFinishReason)
	if response.Usage.FinishReason == "" {
		response.Usage.FinishReason = finishReason
	} else {
		response.Usage.FinishReason = NormalizeFinishReason(response.Usage.FinishReason)
	}
	if plan.Descriptor != nil {
		response.SetContractMetadata(provider, model, plan.Contract, plan.Descriptor)
	} else {
		response.SetContractMetadata(provider, model, plan.Contract, nil)
	}
	response.ContractID = plan.ContractID
	response.Mode = plan.Mode
	response.AuthorityLevel = plan.AuthorityLevel
	response.SchemaMode = plan.SchemaMode
	response.SchemaFallback = plan.SchemaFallback
	response.PromptChars = plan.PromptChars
	response.PromptFingerprint = plan.PromptFingerprint
	response.OutputChars = len(response.Content)
	response.NativeSchema = plan.NativeSchema
	response.Schema = append(json.RawMessage(nil), plan.Schema...)
	response.InlineConstraint = plan.InlineConstraint
	if !response.Usage.RequestStartedAt.IsZero() && !response.Usage.CompletedAt.IsZero() {
		response.RequestDuration = response.Usage.CompletedAt.Sub(response.Usage.RequestStartedAt)
		if response.RequestDuration < 0 {
			response.RequestDuration = 0
		}
	}
	response.Duration = response.RequestDuration
	return response
}

func marshalContractTools(tools []ai.ToolDefinition) []json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err == nil {
			out = append(out, raw)
		}
	}
	return out
}

func compactJSON(raw []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return append([]byte(nil), raw...)
	}
	return out.Bytes()
}
