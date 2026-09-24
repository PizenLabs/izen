package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContractJSONSchemaIsDeterministicAndCompact(t *testing.T) {
	descriptor, err := NewContractDescriptor(StructuredCompletion, DescriptorOptions{
		StructuralOutputSchema: "{\n  \"type\": \"object\",\n  \"required\": [\"ok\"]\n}",
		SchemaVersion:          "test.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := descriptor.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	second, err := descriptor.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || strings.Contains(string(first), "\n") {
		t.Fatalf("schema is not deterministic/compact: %q", first)
	}
	var value map[string]any
	if err := json.Unmarshal(first, &value); err != nil {
		t.Fatal(err)
	}
	if value["type"] != "object" {
		t.Fatalf("schema = %s", first)
	}
}

func TestInlineSchemaConstraintIsBounded(t *testing.T) {
	large := `{"type":"object","description":"` + strings.Repeat("x", MaxInlineSchemaBytes+100) + `"}`
	descriptor, err := NewContractDescriptor(StructuredCompletion, DescriptorOptions{
		StructuralOutputSchema: large,
	})
	if err != nil {
		t.Fatal(err)
	}
	constraint, err := descriptor.InlineSchemaConstraint()
	if err != nil {
		t.Fatal(err)
	}
	if len(constraint) > MaxInlineSchemaBytes+256 {
		t.Fatalf("inline constraint exceeded bound: %d", len(constraint))
	}
	if strings.Contains(constraint, strings.Repeat("x", 100)) {
		t.Fatal("oversized schema leaked into fallback prompt")
	}
}
