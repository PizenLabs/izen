package capability

import "strings"

// reasoningEffortBase is the effort set for reasoning models that expose a
// qualitative control but no extended tier: auto, low, medium, high.
var reasoningEffortBase = []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh}

// reasoningEffortExtended extends the base set with the xhigh tier, supported
// by high-capacity reasoning families (OpenAI o1/o3, DeepSeek-R1/Reasoner).
var reasoningEffortExtended = []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh}

// effortLevelsForModel derives the supported effort levels for a reasoning
// model from its model family. Non-reasoning models always yield an empty
// list (the UI binds zero effort options for them). The mapping is
// intentionally declarative: it lives here and nowhere in the UI.
func effortLevelsForModel(provider, modelID string, reasoning bool) []EffortLevel {
	if !reasoning {
		return nil
	}
	id := strings.ToLower(modelID)
	vendor, bare := splitVendor(id)

	// OpenAI o1/o3 reasoning families advertise an extended effort tier.
	if (vendor == "openai" || vendor == "") && hasAnyPrefix(bare, "o1", "o3") {
		return append([]EffortLevel(nil), reasoningEffortExtended...)
	}
	// DeepSeek-R1 / Reasoner expose extended effort levels, even when served
	// through OpenRouter ("deepseek/deepseek-r1").
	if strings.Contains(id, "deepseek-r1") || strings.Contains(id, "deepseek-reasoner") {
		return append([]EffortLevel(nil), reasoningEffortExtended...)
	}
	// Every other reasoning-capable model (e.g. anthropic/claude-3-7-sonnet)
	// gets the base tier set.
	return append([]EffortLevel(nil), reasoningEffortBase...)
}

// SupportsEffortWithProvider reports whether a model exposes a qualitative
// reasoning effort control, falling back to family heuristics when no dynamic
// capability record is available. It mirrors the decision the legacy runtime
// made with its model whitelist so the dynamic registry stays consistent with
// existing provider adapters.
func SupportsEffortWithProvider(provider, modelID string) bool {
	prov := strings.ToLower(strings.TrimSpace(provider))
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	vendor, bare := "", id
	if idx := strings.Index(id, "/"); idx >= 0 {
		vendor, bare = strings.TrimSpace(id[:idx]), strings.TrimSpace(id[idx+1:])
		if prov == "openrouter" || prov == "" {
			prov = vendor
		}
	}
	switch prov {
	case "openai":
		return hasAnyPrefix(bare, "o1", "o3")
	case "anthropic":
		return hasAnyPrefix(bare, "claude-3-7-sonnet")
	case "deepseek":
		return hasAnyPrefix(bare, "deepseek-r1")
	case "openrouter":
		return hasAnyPrefix(id, "openai/o1", "openai/o3", "anthropic/claude-3-7-sonnet", "deepseek/deepseek-r1")
	default:
		// Unknown providers (e.g. local ollama "deepseek-r1:7b", a custom
		// gateway) fall back to family-name detection.
		return strings.Contains(id, "deepseek-r1") || hasAnyPrefix(bare, "o1", "o3")
	}
}

// reasoningParameterNames are the OpenRouter supported_parameters values that
// advertise a qualitative reasoning control. A model advertising any of these
// is treated as reasoning-capable.
var reasoningParameterNames = []string{
	"reasoning",
	"reasoning_effort",
	"thinking",
	"include_reasoning",
}

// HasReasoningParameter reports whether the provider-advertised supported
// parameters include a reasoning control.
func HasReasoningParameter(parameters []string) bool {
	for _, p := range parameters {
		lower := strings.ToLower(p)
		for _, name := range reasoningParameterNames {
			if lower == name || strings.Contains(lower, name) {
				return true
			}
		}
	}
	return false
}
