// Package capability models the reasoning capabilities of LLM providers. It
// answers exactly one question: "What capabilities and reasoning mechanisms
// does this model support?" It carries no prompts, no effort budgets and no
// dispatch directives — those belong to the decision engine and the prompt
// tier adapters.
//
// Dependency rule: the package imports only the Go standard library.
package capability

import "strings"

// ModelTier is the coarse capability class assigned to a model. Tier is an
// ordinal ladder from the least to the most capable so callers can compare
// tiers with < / > when selecting prompts or budgets.
type ModelTier int

const (
	// TierSLM is a small language model (e.g. Cohere North Mini, GPT-4o-mini,
	// Gemini Flash). These models benefit from compact prompts and tight
	// chain-of-thought limits.
	TierSLM ModelTier = iota
	// TierMid is an intermediate-capability model (mid-size Llama, Mistral,
	// GPT-4o class).
	TierMid
	// TierFrontier is a high-capability frontier model (Claude 3.7 Sonnet /
	// Opus, o1/o3/o4, GPT-5, Gemini 2.5 Pro).
	TierFrontier
)

// String returns the canonical tier label.
func (t ModelTier) String() string {
	switch t {
	case TierSLM:
		return "slm"
	case TierMid:
		return "mid"
	case TierFrontier:
		return "frontier"
	default:
		return "unknown"
	}
}

// IsSLM reports whether the tier is the small-model class.
func (t ModelTier) IsSLM() bool { return t == TierSLM }

// IsFrontier reports whether the tier is the high-capability class.
func (t ModelTier) IsFrontier() bool { return t == TierFrontier }

// ReasoningKind names the native reasoning control mechanism a model (or the
// gateway in front of it) exposes. Exactly one mechanism is authoritative per
// model; it determines how an effort directive is translated into the native
// API payload.
type ReasoningKind string

const (
	// ReasoningKindNone means the model exposes no reasoning control
	// (non-reasoning mid-tier models). Effort directives are not mapped.
	ReasoningKindNone ReasoningKind = "NONE"
	// ReasoningKindLevel means the model accepts a qualitative effort level
	// (OpenAI reasoning_effort: low / medium / high / xhigh).
	ReasoningKindLevel ReasoningKind = "LEVEL"
	// ReasoningKindBudget means the model accepts a token budget for
	// thinking (Anthropic thinking.budget_tokens).
	ReasoningKindBudget ReasoningKind = "BUDGET"
	// ReasoningKindCoT means the model supports a chain-of-thought token cap
	// (small models whose reasoning must be bounded tightly, e.g. an OpenRouter
	// reasoning.max_tokens cap).
	ReasoningKindCoT ReasoningKind = "COT_CAP"
)

// ReasoningControl is the concrete native control payload the model accepts:
//
//	Level        — qualitative effort (reasoning_effort style)
//	BudgetTokens — thinking token budget (thinking.budget_tokens style)
//	CoTLimit     — chain-of-thought token cap (SLM reasoning cap)
//
// Only the field matching ReasoningKind is authoritative for a given model;
// the others stay zero.
type ReasoningControl struct {
	Level        string `json:"level"`
	BudgetTokens int    `json:"budget_tokens"`
	CoTLimit     int    `json:"cot_limit"`
}

// IsZero reports whether no reasoning control is set at all.
func (c ReasoningControl) IsZero() bool {
	return c.Level == "" && c.BudgetTokens == 0 && c.CoTLimit == 0
}

// ModelCapability is the profiler's answer for a single model: its tier, its
// native reasoning mechanism, the maximum reasoning budget it supports, and
// the safe default control to start from.
type ModelCapability struct {
	Tier          ModelTier        `json:"tier"`
	ReasoningKind ReasoningKind    `json:"reasoning_kind"`
	MaxBudget     int              `json:"max_budget"`
	Default       ReasoningControl `json:"default_control"`
}

// SLM marker substrings shared with the plan package's mini-model detection.
var slmMarkers = []string{
	"mini", "nano", "flash", "lite", "small", "tiny",
	"haiku", "gemma", "command-r", "command r", "moe", "draft",
	"1b", "3b", "7b", "8b",
}

// frontierMarkers identify the highest-capability model families.
var frontierMarkers = []string{
	"opus", "claude-4", "claude-3.7", "claude-3.5",
	"o1", "o3", "o4", "gpt-5", "gpt-4.5", "gpt-4o",
	"gemini-2.5-pro", "gemini-3", "deepseek-r1", "deepseek-reasoner",
	"sonnet", "mistral-large", "llama-4", "kimi", "qwen3-max",
}

// vendorPrefixes map a leading model-name segment to a provider so a bare
// model ID without an explicit provider argument can still be classified.
var vendorPrefixes = []struct {
	prefix string
	prov   string
}{
	{"anthropic/", "anthropic"},
	{"openai/", "openai"},
	{"meta-llama/", "meta"},
	{"cohere/", "cohere"},
	{"google/", "google"},
	{"gemini-", "google"},
	{"mistralai/", "mistral"},
	{"deepseek/", "deepseek"},
	{"qwen/", "qwen"},
	{"amazon/", "aws"},
	{"x-ai/", "xai"},
	{"nousresearch/", "nous"},
}

// ResolveModelCapability classifies a model (model name + provider) into a
// ModelCapability: its tier, native reasoning kind, maximum reasoning budget
// and default control. The classifier is heuristic and deliberately broad —
// it must never reject an unknown model, only fall back to a conservative
// mid-tier default with no reasoning control.
func ResolveModelCapability(modelName, provider string) ModelCapability {
	name := strings.ToLower(strings.TrimSpace(modelName))
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "" {
		prov = inferProvider(name)
	}

	tier := classifyTier(name, prov)
	kind, maxBudget := classifyReasoning(tier, name, prov)

	return ModelCapability{
		Tier:          tier,
		ReasoningKind: kind,
		MaxBudget:     maxBudget,
		Default:       defaultControl(kind, tier),
	}
}

// inferProvider guesses the provider from a vendor-prefixed model ID.
func inferProvider(name string) string {
	for _, vp := range vendorPrefixes {
		if strings.HasPrefix(name, vp.prefix) {
			return vp.prov
		}
	}
	return ""
}

// classifyTier returns the coarse capability tier for a model. Family-specific
// rules run first so "mini" variants are classified consistently: o-series
// reasoning models (o1/o3/o4/gpt-5) are frontier even with a mini suffix, while
// gpt-4o-mini / claude-haiku are SLMs.
func classifyTier(name, provider string) ModelTier {
	if isAnthropicFamily(name, provider) {
		switch {
		case strings.Contains(name, "haiku"):
			return TierSLM
		case strings.Contains(name, "opus"), strings.Contains(name, "sonnet"):
			return TierFrontier
		}
	}
	// OpenAI o-series reasoning models are frontier regardless of a mini suffix.
	if isOpenAIFamily(name, provider) && hasAnySubstring(name, "o1", "o3", "o4", "gpt-5") {
		return TierFrontier
	}
	// SLM markers are matched as whole tokens so "mini" inside "gemini" never
	// triggers a false positive. The command-r family is handled as a special
	// case because its space/hyphen variants split across tokens.
	tokens := nameTokens(name)
	for _, s := range slmMarkers {
		if hasToken(tokens, s) {
			return TierSLM
		}
	}
	if strings.Contains(name, "command") {
		return TierSLM
	}
	for _, f := range frontierMarkers {
		if strings.Contains(name, f) {
			return TierFrontier
		}
	}
	return TierMid
}

// nameTokens splits a model name on every non-alphanumeric character, keeping
// digit-letter runs (e.g. "1b", "70b", "4o") intact as single tokens.
func nameTokens(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// hasToken reports whether tokens contains an exact match of s.
func hasToken(tokens []string, s string) bool {
	for _, tok := range tokens {
		if tok == s {
			return true
		}
	}
	return false
}

// hasAnySubstring reports whether s contains any of the given substrings.
func hasAnySubstring(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// classifyReasoning maps a model to its native reasoning mechanism and the
// maximum reasoning budget it can accept. Mid-tier models without a documented
// reasoning control surface yield ReasoningKindNone.
func classifyReasoning(tier ModelTier, name, provider string) (ReasoningKind, int) {
	switch tier {
	case TierSLM:
		return ReasoningKindCoT, 512
	case TierFrontier:
		if isAnthropicFamily(name, provider) {
			return ReasoningKindBudget, 32768
		}
		if isOpenAIFamily(name, provider) {
			return ReasoningKindLevel, 16384
		}
		return ReasoningKindLevel, 8192
	default: // TierMid
		if isAnthropicFamily(name, provider) {
			return ReasoningKindBudget, 8192
		}
		if isOpenAIFamily(name, provider) {
			return ReasoningKindLevel, 8192
		}
		return ReasoningKindNone, 0
	}
}

// isAnthropicFamily reports whether the model belongs to the Anthropic/Claude
// family (native thinking.budget_tokens control).
func isAnthropicFamily(name, provider string) bool {
	if provider == "anthropic" {
		return true
	}
	return strings.Contains(name, "claude") || strings.Contains(name, "anthropic")
}

// isOpenAIFamily reports whether the model belongs to the OpenAI family
// (native reasoning_effort control).
func isOpenAIFamily(name, provider string) bool {
	if provider == "openai" {
		return true
	}
	return strings.Contains(name, "gpt") || strings.Contains(name, "o1") ||
		strings.Contains(name, "o3") || strings.Contains(name, "o4")
}

// defaultControl returns the conservative starting reasoning control for a
// model class. It is the "auto" starting point before task complexity is
// factored in.
func defaultControl(kind ReasoningKind, tier ModelTier) ReasoningControl {
	switch kind {
	case ReasoningKindCoT:
		return ReasoningControl{CoTLimit: 512}
	case ReasoningKindBudget:
		if tier == TierFrontier {
			return ReasoningControl{Level: "high", BudgetTokens: 8192}
		}
		return ReasoningControl{Level: "medium", BudgetTokens: 4096}
	case ReasoningKindLevel:
		if tier == TierFrontier {
			return ReasoningControl{Level: "high"}
		}
		return ReasoningControl{Level: "medium"}
	default:
		return ReasoningControl{}
	}
}
