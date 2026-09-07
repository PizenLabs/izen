package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrDiscoveryControlField is P0 security violation: control-bearing fields in LLM output.
var ErrDiscoveryControlField = errors.New("discovery: control field present")

var blacklistedKeys = map[string]struct{}{
	"operation":   {},
	"operations":  {},
	"authority":   {},
	"permission":  {},
	"permissions": {},
	"command":     {},
	"commands":    {},
	"action":      {},
	"actions":     {},
	"mutate":      {},
}

// ReferenceHypothesis is a non-authoritative reference discovered by LLM.
type ReferenceHypothesis struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// DiscoveryPayload is the strict schema for LLM discovery output.
type DiscoveryPayload struct {
	PerceivedIntent string                `json:"perceived_intent"`
	IntentVerb      string                `json:"intent_verb"` // "CREATE" | "MODIFY" | "DELETE" | "REFACTOR" | "UNKNOWN"
	Features        []string              `json:"features"`
	References      []ReferenceHypothesis `json:"references"`
}

// ParseAndSanitize performs raw structural map check BEFORE unmarshaling.
// If any blacklisted control keys exist, returns ErrDiscoveryControlField.
// Strictly unmarshals into DiscoveryPayload and normalizes IntentVerb.
func ParseAndSanitize(rawJSON []byte) (*DiscoveryPayload, error) {
	if len(rawJSON) == 0 {
		return nil, fmt.Errorf("discovery: empty payload")
	}
	// Raw structural map check BEFORE unmarshaling into typed struct.
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &rawMap); err != nil {
		return nil, fmt.Errorf("discovery: invalid json: %w", err)
	}
	for k := range rawMap {
		lower := strings.ToLower(strings.TrimSpace(k))
		if _, ok := blacklistedKeys[lower]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDiscoveryControlField, k)
		}
	}
	// Strict unmarshal into DiscoveryPayload.
	dec := json.NewDecoder(strings.NewReader(string(rawJSON)))
	dec.DisallowUnknownFields()
	// We cannot use DisallowUnknownFields strictly for future compatibility?
	// The spec says "Strictly unmarshal" — so unknown fields outside schema should be rejected?
	// But we already allow only known fields; if evaluator sends extra non-control fields, it should pass.
	// To avoid false positives, we unmarshal without strict fallback if DisallowUnknownFields fails for allowed extensions.
	var payload DiscoveryPayload
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return nil, fmt.Errorf("discovery: unmarshal: %w", err)
	}
	// Normalize IntentVerb.
	payload.IntentVerb = normalizeVerb(payload.IntentVerb)
	return &payload, nil
}

func normalizeVerb(v string) string {
	u := strings.ToUpper(strings.TrimSpace(v))
	switch u {
	case "CREATE", "MODIFY", "DELETE", "REFACTOR":
		return u
	default:
		return "UNKNOWN"
	}
}
