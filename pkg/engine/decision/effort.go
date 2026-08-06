package decision

import (
	"strings"

	"github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/domain/intent"
)

// EffortConfig is the resolved reasoning directive for a single dispatch. It
// carries the prompt tier to use and the concrete native reasoning control
// values the provider adapters translate into API payloads:
//
//	Level        — qualitative effort (OpenAI reasoning_effort / OpenRouter)
//	BudgetTokens — thinking token budget (Anthropic thinking.budget_tokens)
//	CoTLimit     — chain-of-thought token cap (SLM reasoning cap)
//
// Only the fields matching the model's ReasoningKind are authoritative; the
// others stay zero and providers skip them.
type EffortConfig struct {
	// Tier is the prompt tier to dispatch for this task (SLM / Mid / Frontier).
	Tier capability.ModelTier `json:"tier"`
	// ReasoningKind is the model's native reasoning mechanism.
	ReasoningKind capability.ReasoningKind `json:"reasoning_kind"`
	// Level is the qualitative effort level ("" when unused).
	Level string `json:"level"`
	// BudgetTokens is the thinking token budget (0 when unused).
	BudgetTokens int `json:"budget_tokens"`
	// CoTLimit is the chain-of-thought token cap (0 when unused).
	CoTLimit int `json:"cot_limit"`
}

// Description returns a compact human-readable summary of the resolved effort.
func (e EffortConfig) Description() string {
	switch e.ReasoningKind {
	case capability.ReasoningKindLevel:
		if e.Level == "" {
			return "off"
		}
		return "effort=" + e.Level
	case capability.ReasoningKindBudget:
		return "budget=" + itoa(e.BudgetTokens)
	case capability.ReasoningKindCoT:
		return "cot_cap=" + itoa(e.CoTLimit)
	default:
		return "off"
	}
}

// itoa is a tiny integer formatter avoiding strconv in the hot path.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ComplexityLevel is the coarse task-complexity classification used to steer
// auto effort.
type ComplexityLevel int

const (
	// ComplexityLow is a simple create or small single-file edit.
	ComplexityLow ComplexityLevel = iota
	// ComplexityMedium is a moderate single- or few-file change.
	ComplexityMedium
	// ComplexityHigh is an architectural or multi-file change.
	ComplexityHigh
)

// SLMMaxCoTLimit is the absolute ceiling on the reasoning cap for SLM tiers,
// even under explicit high-effort overrides.
const SLMMaxCoTLimit = 2048

// ResolveEffortConfig computes the effort budget and prompt tier to dispatch
// for a task. It owns exactly the question "what effort budget and prompt tier
// should be dispatched?":
//
//   - userEffortSetting == "auto" (or ""): infer the optimal reasoning budget
//     from task complexity and model tier.
//   - userEffortSetting is a manual override ("low"…"xhigh", or a token
//     budget like "8192" / "16k", or "off"): map it dynamically onto the
//     model's native reasoning mechanism.
func ResolveEffortConfig(in *intent.UserIntent, cap capability.ModelCapability, userEffortSetting string) EffortConfig {
	base := EffortConfig{
		Tier:          cap.Tier,
		ReasoningKind: cap.ReasoningKind,
	}

	setting := strings.ToLower(strings.TrimSpace(userEffortSetting))
	switch setting {
	case "", "auto":
		return resolveAuto(in, cap, base)
	case "none", "off", "disabled":
		return base
	}

	return resolveManual(setting, cap, base)
}

// resolveAuto infers the effort from task complexity and model tier.
func resolveAuto(in *intent.UserIntent, cap capability.ModelCapability, base EffortConfig) EffortConfig {
	cx := ComplexityMedium
	if in != nil {
		cx = assessComplexity(in.Goal.RawPrompt)
	}

	switch cap.Tier {
	case capability.TierSLM:
		switch cx {
		case ComplexityHigh:
			base.Level = "medium"
			base.CoTLimit = 1024
		default:
			base.Level = "low"
			base.CoTLimit = 512
		}
		return base
	case capability.TierMid:
		switch cx {
		case ComplexityLow:
			base.Level = "low"
			base.BudgetTokens = 2048
			base.CoTLimit = 512
		case ComplexityHigh:
			base.Level = "high"
			base.BudgetTokens = clampBudget(8192, cap.MaxBudget)
		default:
			base.Level = "medium"
			base.BudgetTokens = clampBudget(4096, cap.MaxBudget)
		}
		return base
	default: // TierFrontier
		switch cx {
		case ComplexityLow:
			base.Level = "medium"
			base.BudgetTokens = clampBudget(8192, cap.MaxBudget)
		case ComplexityHigh:
			base.Level = "xhigh"
			base.BudgetTokens = cap.MaxBudget
		default:
			base.Level = "high"
			base.BudgetTokens = clampBudget(16384, cap.MaxBudget)
		}
		return base
	}
}

// resolveManual maps a manual override onto the model's native reasoning
// mechanism. Unknown settings fall back to the model's safe default control.
func resolveManual(setting string, cap capability.ModelCapability, base EffortConfig) EffortConfig {
	if lvl, ok := parseLevel(setting); ok {
		return base.applyManualLevel(lvl, cap)
	}
	if budget, ok := parseBudget(setting); ok {
		return base.applyManualBudget(budget, cap)
	}
	def := cap.Default
	base.Level, base.BudgetTokens, base.CoTLimit = def.Level, def.BudgetTokens, def.CoTLimit
	return base
}

// applyManualLevel maps a qualitative effort level onto the native mechanism.
func (e EffortConfig) applyManualLevel(lvl string, cap capability.ModelCapability) EffortConfig {
	switch cap.ReasoningKind {
	case capability.ReasoningKindLevel:
		e.Level = lvl
	case capability.ReasoningKindBudget:
		e.BudgetTokens = clampBudget(levelToBudget(lvl), cap.MaxBudget)
	case capability.ReasoningKindCoT:
		e.CoTLimit = minInt(levelToBudget(lvl), SLMMaxCoTLimit)
	}
	return e
}

// applyManualBudget maps a token budget onto the native mechanism.
func (e EffortConfig) applyManualBudget(budget int, cap capability.ModelCapability) EffortConfig {
	switch cap.ReasoningKind {
	case capability.ReasoningKindLevel:
		e.Level = budgetToLevel(budget)
	case capability.ReasoningKindBudget:
		e.BudgetTokens = clampBudget(budget, cap.MaxBudget)
	case capability.ReasoningKindCoT:
		e.CoTLimit = minInt(budget, SLMMaxCoTLimit)
	}
	return e
}

// parseLevel normalizes a qualitative effort string. Returns the canonical
// value and whether it is a recognized level.
func parseLevel(s string) (string, bool) {
	switch s {
	case "minimal", "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "xhigh", "extra-high", "max":
		return "xhigh", true
	default:
		return "", false
	}
}

// parseBudget parses a token budget setting: plain digits ("8192") or a
// k-suffixed shorthand ("16k" → 16000). Returns the budget and whether it is
// a recognized numeric setting.
func parseBudget(s string) (int, bool) {
	t := strings.ToLower(s)
	mult := 1
	if strings.HasSuffix(t, "k") {
		mult = 1000
		t = strings.TrimSuffix(t, "k")
	}
	if t == "" {
		return 0, false
	}
	n := 0
	for _, r := range t {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n * mult, true
}

// levelToBudget maps a qualitative level onto a token budget.
func levelToBudget(lvl string) int {
	switch lvl {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	case "xhigh":
		return 16384
	default:
		return 0
	}
}

// budgetToLevel maps a token budget onto the nearest qualitative level.
func budgetToLevel(budget int) string {
	switch {
	case budget < 2048:
		return "low"
	case budget < 8192:
		return "medium"
	case budget < 16384:
		return "high"
	default:
		return "xhigh"
	}
}

// clampBudget bounds a budget to the model's maximum when a positive maximum
// is declared.
func clampBudget(budget, max int) int {
	if max > 0 && budget > max {
		return max
	}
	return budget
}

// minInt returns the smaller of two non-negative integers.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// assessComplexity classifies a raw task prompt into Low / Medium / High using
// the same keyword families as the plan package's complexity heuristic so the
// effort decision stays consistent with prompt selection.
func assessComplexity(rawPrompt string) ComplexityLevel {
	lower := strings.ToLower(rawPrompt)

	highKeywords := []string{
		"migration", "architect", "architecture", "redesign", "restructure",
		"cross-cutting", "concurrency", "distributed", "multi-file",
		"database", "schema", "api design", "protocol", "refactor",
		"security", "authentication", "authorization",
		"pipeline", "event-driven", "message queue", "implement",
	}
	lowKeywords := []string{
		"license", "readme", "typo", "comment", "format",
		"rename", "spelling", "grammar", "whitespace",
		"capitalize", "version bump", "copy", "fix typo",
	}

	for _, kw := range highKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityHigh
		}
	}
	for _, kw := range lowKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityLow
		}
	}
	return ComplexityMedium
}
