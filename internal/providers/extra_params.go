package providers

import "encoding/json"

// mergeExtraParams merges arbitrary provider-native fields into a marshalled
// request map. Native keys win: ExtraParams keys colliding with native fields
// are ignored so the typed contract is never clobbered.
func mergeExtraParams(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return base
	}
	for k, v := range extra {
		if _, exists := base[k]; !exists {
			base[k] = v
		}
	}
	return base
}

// marshalWithExtra marshals v via its JSON tags into a map, merges extra,
// and re-marshals. v should be an alias value without a MarshalJSON method
// to avoid recursion.
func marshalWithExtra(v any, extra map[string]any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return json.Marshal(mergeExtraParams(m, extra))
}
