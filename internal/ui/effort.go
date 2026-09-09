package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// EffortLevel represents the intent/effort slider value.
type EffortLevel int

const (
	EffortAuto EffortLevel = iota
	EffortLow
	EffortMedium
	EffortHigh
)

func (e EffortLevel) String() string {
	switch e {
	case EffortAuto:
		return "auto"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	default:
		return "auto"
	}
}

// Description returns the provider-spec conformant label for the effort level.
// It strictly uses the official API values ("auto","low","medium","high") without
// fabricated marketing strings. Anthropic thinking budgets are mapped from these
// levels via the decision engine (low=1024, medium=4096, high=8192).
func (e EffortLevel) Description() string {
	switch e {
	case EffortAuto:
		return "auto"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	default:
		return "auto"
	}
}

// ConfigTier maps effort level to the config tier key.
func (e EffortLevel) ConfigTier() string {
	switch e {
	case EffortAuto:
		return "auto_intent"
	case EffortLow:
		return "low_intent"
	case EffortMedium:
		return "medium_intent"
	case EffortHigh:
		return "high_intent"
	default:
		return "auto_intent"
	}
}

// Style returns the pre-compiled render-path style for this effort level:
func (e EffortLevel) Style() lipgloss.Style {
	switch e {
	case EffortAuto:
		return infoStyle
	case EffortLow:
		return greenStyle
	case EffortMedium:
		return yellowStyle
	case EffortHigh:
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
