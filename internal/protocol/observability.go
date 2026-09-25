package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ObservabilityBinding is the provider-neutral identity envelope attached to
// runtime telemetry and audit records. It contains only structural protocol
// facts: the interaction kind, its normalized descriptor, the execution
// contract identity, the presentation/runtime mode, the authority ceiling and
// the schema serialization mode. It never contains prompt or model output.
//
// The binding is descriptive evidence only. In particular, AuthorityLevel is
// a ceiling and is not an authorization grant.
type ObservabilityBinding struct {
	Contract       InteractionContract `json:"interaction_contract,omitempty"`
	Kind           InteractionContract `json:"kind,omitempty"`
	Descriptor     *ContractDescriptor `json:"interaction_contract_descriptor,omitempty"`
	ContractID     string              `json:"contract_id,omitempty"`
	Mode           string              `json:"mode,omitempty"`
	AuthorityLevel AuthorityCeiling    `json:"authority_level,omitempty"`
	SchemaMode     string              `json:"schema_mode,omitempty"`
	SchemaFallback bool                `json:"schema_fallback,omitempty"`
}

// ContractBinding and InteractionMetadata are descriptive aliases used by
// integrations that name the envelope after its protocol purpose.
type ContractBinding = ObservabilityBinding
type InteractionMetadata = ObservabilityBinding

// NewObservabilityBinding builds a defensive, normalized binding. Invalid or
// empty descriptors are represented by the contract enum alone; callers that
// require a validated descriptor should normalize it before this function.
func NewObservabilityBinding(contract InteractionContract, descriptor *ContractDescriptor, contractID, mode, schemaMode string) ObservabilityBinding {
	binding := ObservabilityBinding{
		Contract:   contract,
		Kind:       contract,
		ContractID: strings.TrimSpace(contractID),
		Mode:       strings.TrimSpace(mode),
		SchemaMode: normalizeSchemaMode(schemaMode),
	}
	if descriptor != nil {
		copy := descriptor.Clone()
		binding.Descriptor = &copy
		binding.Contract = copy.Contract
		binding.Kind = copy.Contract
		binding.AuthorityLevel = copy.AuthorityCeiling
	}
	return binding
}

// WithContractID returns a defensive copy carrying a newly resolved execution
// contract identity. It is used when admission has resolved the execution
// contract after the initial protocol descriptor was bound.
func (b ObservabilityBinding) WithContractID(id string) ObservabilityBinding {
	b.ContractID = strings.TrimSpace(id)
	return b.Clone()
}

// WithSchemaFallback returns a defensive copy with the explicit schema
// fallback bit set.
func (b ObservabilityBinding) WithSchemaFallback(fallback bool) ObservabilityBinding {
	b.SchemaFallback = fallback
	return b.Clone()
}

// Clone returns a detached binding with a detached descriptor.
func (b ObservabilityBinding) Clone() ObservabilityBinding {
	out := b
	if b.Descriptor != nil {
		descriptor := b.Descriptor.Clone()
		out.Descriptor = &descriptor
	}
	return out
}

// Normalize fills the identity aliases and schema mode without changing the
// semantic contract. It is safe to call on zero values.
func (b ObservabilityBinding) Normalize() ObservabilityBinding {
	out := b.Clone()
	if out.Contract == "" && out.Descriptor != nil {
		out.Contract = out.Descriptor.Contract
	}
	if out.Kind == "" {
		out.Kind = out.Contract
	}
	if out.AuthorityLevel == "" && out.Descriptor != nil {
		out.AuthorityLevel = out.Descriptor.AuthorityCeiling
	}
	out.ContractID = strings.TrimSpace(out.ContractID)
	out.Mode = strings.TrimSpace(out.Mode)
	out.SchemaMode = normalizeSchemaMode(out.SchemaMode)
	return out
}

func normalizeSchemaMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "auto"
	}
	return value
}

// Fingerprint returns a deterministic SHA-256 fingerprint for structural
// telemetry inputs. The input is hashed immediately and never retained; callers
// may safely use this on prompt projections when only a non-reversible audit
// fingerprint is needed.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
