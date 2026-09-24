package ai

import (
	"encoding/json"
	"testing"
)

func TestResponseFormatJSONSchemaRoundTrip(t *testing.T) {
	original := NewJSONSchemaResponseFormat("contract", json.RawMessage(`{"type":"object"}`))
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ResponseFormat
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "json_schema" || decoded.JSONSchema == nil || decoded.JSONSchema.Name != "contract" {
		t.Fatalf("decoded response format = %+v", decoded)
	}
	if string(decoded.Schema) != `{"type":"object"}` {
		t.Fatalf("decoded schema = %s", decoded.Schema)
	}
}
