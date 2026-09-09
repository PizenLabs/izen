package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// EffortLevel represents the intent/effort slider value.
//
// Tier 0 (EffortDefault) is the provider-factory fallback: when active, the
// reasoning_effort key is omitted from downstream model API request payloads
// so the provider applies its own default behavior.
type EffortLevel int

const (
	EffortDefault EffortLevel = iota
	EffortNone
	EffortLow
	EffortMedium
	EffortHigh
	EffortXHigh
	EffortMax
)

// EffortAuto is the legacy alias for EffortDefault (provider-factory
// fallback). It is preserved so existing call sites keep compiling and
// behaving identically.
const EffortAuto = EffortDefault

func (e EffortLevel) String() string {
	switch e {
	case EffortDefault:
		return "default"
	case EffortNone:
		return "none"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "default"
	}
}

// Description returns the provider-spec conformant label for the effort level.
// "default" preserves provider factory behavior (the reasoning_effort key is
// omitted downstream); "none" disables the reasoning pass; the remaining
// tiers map onto qualitative effort values ("low".."max"). Anthropic thinking
// budgets are mapped from these levels via the decision engine
// (low=1024, medium=4096, high=8192).
func (e EffortLevel) Description() string {
	switch e {
	case EffortDefault:
		return "default"
	case EffortNone:
		return "none"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "default"
	}
}

// ConfigTier maps effort level to the config tier key.
func (e EffortLevel) ConfigTier() string {
	switch e {
	case EffortDefault:
		return "default_intent"
	case EffortNone:
		return "none_intent"
	case EffortLow:
		return "low_intent"
	case EffortMedium:
		return "medium_intent"
	case EffortHigh:
		return "high_intent"
	case EffortXHigh:
		return "xhigh_intent"
	case EffortMax:
		return "max_intent"
	default:
		return "default_intent"
	}
}

// IsDefault reports whether the level is the provider-factory fallback. When
// true, callers must omit the reasoning_effort key from API payloads.
func (e EffortLevel) IsDefault() bool { return e == EffortDefault }

// Style returns the pre-compiled render-path style for this effort level:
func (e EffortLevel) Style() lipgloss.Style {
	switch e {
	case EffortDefault:
		return infoStyle
	case EffortNone:
		return dimmedStyle
	case EffortLow:
		return greenStyle
	case EffortMedium:
		return yellowStyle
	case EffortHigh:
		return redStyle
	case EffortXHigh:
		return redStyle
	case EffortMax:
		return redStyle
	default:
		return infoStyle
	}
}

// truncateWithEllipsis shortens s to fit within max runes, replacing the
// tail with "…" when it doesn't fit. Preserved from the legacy model picker:
// session_picker.go shares it for objective labels.
func truncateWithEllipsis(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
