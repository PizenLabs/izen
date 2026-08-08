package validator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// JSONValidator validates artifact content as a single JSON value via
// encoding/json. json.Valid is strict about balanced structure, string
// escaping and number syntax; trailing garbage fails validation.
type JSONValidator struct{}

// NewJSONValidator returns a JSONValidator.
func NewJSONValidator() *JSONValidator { return &JSONValidator{} }

// ID implements ArtifactValidator.
func (v *JSONValidator) ID() string { return "json" }

// Languages implements ArtifactValidator.
func (v *JSONValidator) Languages() []string { return []string{"json"} }

// Validate returns nil when data is well-formed JSON, or a detailed
// syntax-error message otherwise.
func (v *JSONValidator) Validate(ctx context.Context, data []byte) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !json.Valid(data) {
		return fmt.Errorf("json: invalid syntax at byte %d", firstInvalidByte(data))
	}
	return nil
}

// firstInvalidByte finds the earliest byte that breaks JSON validity. It is a
// best-effort scan: json.Valid already decided the payload is invalid, so this
// only helps point the reprompt at the offending region.
func firstInvalidByte(data []byte) int {
	var val any
	if err := json.Unmarshal(data, &val); err != nil {
		var serr *json.SyntaxError
		if errors.As(err, &serr) && serr.Offset > 0 {
			return int(serr.Offset)
		}
	}
	return len(data)
}
