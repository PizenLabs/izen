package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxInlineSchemaBytes bounds the provider-neutral schema text that may be
// copied into a fallback prompt. Native schema transports do not use this
// limit. The bound keeps a malformed or unexpectedly large descriptor from
// consuming the context window before the model receives the actual task.
const MaxInlineSchemaBytes = 2048

// StructuralOutputSchema is the provider-neutral name for the concrete JSON
// Schema document carried by a ContractDescriptor. It is an alias rather than
// a second wire type so callers can use the protocol vocabulary without
// importing an adapter package.
type StructuralOutputSchema = json.RawMessage

// JSONSchema returns the deterministic JSON Schema document associated with
// the descriptor. A descriptor may provide a concrete document through
// StructuralOutputSchema or the historical Schema string; otherwise a compact
// provider-neutral default is derived from OutputSchema.
//
// The returned bytes are valid JSON and are compacted deterministically. A
// non-empty document that looks like JSON but is malformed is rejected rather
// than silently sent to a provider. Legacy schema identifiers (for example
// "izen.plan.atomic_tasks.v1") are treated as identifiers and use the
// corresponding default document.
func (d ContractDescriptor) JSONSchema() ([]byte, error) {
	normalized, err := d.Normalize()
	if err != nil {
		return nil, err
	}
	if !normalized.StructuredOutput && normalized.OutputSchema == SchemaText && normalized.StructuralOutputSchema == "" {
		return nil, nil
	}
	if normalized.OutputSchema == SchemaTaskBlocks {
		return nil, nil
	}

	candidate := strings.TrimSpace(normalized.StructuralOutputSchema)
	if candidate == "" {
		candidate = strings.TrimSpace(normalized.Schema)
	}
	if candidate != "" {
		if looksLikeJSONDocument(candidate) {
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(candidate)); err != nil {
				return nil, fmt.Errorf("%w: invalid structural output schema: %w", ErrContractSchema, err)
			}
			return compact.Bytes(), nil
		}
		// Schema has historically also carried a stable identifier. Do not
		// mistake that identifier for a JSON document; use the descriptor kind
		// below to select its deterministic default.
	}

	var schema string
	switch normalized.OutputSchema {
	case SchemaPlanJSON:
		schema = `{"type":"object","required":["architectural_strategy","atomic_tasks"],"properties":{"architectural_strategy":{"type":"string"},"strategic_overview":{"type":"object"},"atomic_tasks":{"type":"array","minItems":1,"items":{"type":"object","required":["task_id","strategy","file","description","rationale","solution"],"properties":{"task_id":{"type":"integer","minimum":1},"strategy":{"type":"string"},"file":{"type":"string"},"description":{"type":"string"},"rationale":{"type":"string"},"solution":{"type":"string"}}}}}}`
	case SchemaJSON:
		schema = `{"type":"object"}`
	case SchemaText:
		// A caller may attach a concrete schema to a text descriptor. Preserve
		// that explicit document rather than dropping it at the provider edge.
		schema = `{"type":"object"}`
	default:
		schema = `{"type":"object"}`
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(schema)); err != nil {
		return nil, fmt.Errorf("%w: generated structural output schema: %w", ErrContractSchema, err)
	}
	return compact.Bytes(), nil
}

// StructuralOutputSchemaJSON is a descriptive alias for JSONSchema used by
// adapter boundaries that name the wire field explicitly.
func (d ContractDescriptor) StructuralOutputSchemaJSON() ([]byte, error) {
	return d.JSONSchema()
}

// EffectiveOutputSchema returns the normalized structural output kind. It is
// useful to adapters that need to choose a wire field without repeating the
// descriptor's compatibility rules.
func (d ContractDescriptor) EffectiveOutputSchema() (OutputSchema, error) {
	normalized, err := d.Normalize()
	if err != nil {
		return "", err
	}
	return normalized.OutputSchema, nil
}

// InlineSchemaConstraint renders a compact, deterministic prompt constraint
// for providers that do not expose a native JSON Schema parameter. The output
// is intentionally positive and short: one marker, one schema document, and no
// repeated negative rule blocks. Task-block contracts use their native textual
// grammar because they are not JSON documents.
func (d ContractDescriptor) InlineSchemaConstraint() (string, error) {
	normalized, err := d.Normalize()
	if err != nil {
		return "", err
	}
	if normalized.OutputSchema == SchemaTaskBlocks {
		return strings.TrimSpace(`
[STRUCTURED_OUTPUT_CONTRACT]
Return only task blocks in this exact form: - [ ] TYPE: target | rationale
Allowed TYPE values: FILE_MUTATE, SHELL_EXEC, GIT_ACTION.
`), nil
	}
	if normalized.OutputSchema == SchemaText && normalized.StructuralOutputSchema == "" {
		return "", nil
	}
	schema, err := normalized.JSONSchema()
	if err != nil {
		return "", err
	}
	if len(schema) == 0 {
		return "", nil
	}
	if len(schema) > MaxInlineSchemaBytes {
		// A large custom document is not allowed to dominate the prompt. Use
		// the compact kind-derived schema as a bounded guard; native adapters
		// still receive the original document above.
		schema, err = normalized.defaultCompactSchema()
		if err != nil {
			return "", err
		}
	}
	var b strings.Builder
	b.WriteString("[STRUCTURED_OUTPUT_CONTRACT]\n")
	b.WriteString("Return exactly one JSON value matching this schema. No markdown or prose.\nSCHEMA:")
	b.Write(schema)
	if normalized.SchemaVersion != "" {
		b.WriteString("\nVERSION:")
		b.WriteString(normalized.SchemaVersion)
	}
	return b.String(), nil
}

func (d ContractDescriptor) defaultCompactSchema() ([]byte, error) {
	if d.OutputSchema == SchemaPlanJSON {
		return []byte(`{"type":"object","required":["architectural_strategy","atomic_tasks"],"properties":{"atomic_tasks":{"type":"array","minItems":1}}}`), nil
	}
	return []byte(`{"type":"object"}`), nil
}

func looksLikeJSONDocument(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")
}
