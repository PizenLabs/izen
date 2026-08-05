package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/classifier"
)

// ── Visual Distinction Styles ─────────────────────────────────────────────
//
// Three tiers of epistemic certainty:
//
//	CONFIRMED (green)     — verified facts, confirmed results, passed tests
//	HYPOTHESIS (yellow)   — inferred by LLM, not yet verified
//	UNKNOWN / ERROR (red) — unclassified errors, FailureClass UNKNOWN
//
// The renderer tags each output line with the appropriate style based on its
// source role and content analysis. The model does not store distinction
// metadata; it is derived at render time.

var (
	confirmedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGreen)).
			Bold(true)
	confirmedPrefix = Icon.Check + " "

	hypothesisStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorYellow)).
			Italic(true)
	hypothesisPrefix = "◇ "

	unknownStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorRed)).
			Bold(true)
	unknownPrefix = Icon.Risk + " "

	errorClassStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorMaroon)).
			Bold(true)
)

// DistinctionClassifies text based on its role and optionally a FailureClass.
// Returns the styled text with prefix icon.
func DistinguishLine(text string, role role, fc classifier.FailureClass) string {
	// Error role is always rendered as error/unknown
	if role == roleError {
		return unknownStyle.Render(unknownPrefix + text)
	}

	// Explicit failure class UNKNOWN gets error treatment
	if fc == classifier.FailureUnknownClass {
		return unknownStyle.Render(unknownPrefix + text)
	}

	// Status and activity may be confirmed facts
	if role == roleStatus || role == roleSystem {
		if isSuccessIndicator(text) {
			return confirmedStyle.Render(confirmedPrefix + text)
		}
		if isFailureIndicator(text) {
			return errorClassStyle.Render(unknownPrefix + text)
		}
		// Neutral status: no prefix
		return text
	}

	// AI responses are hypotheses until confirmed
	if role == roleAI {
		return hypothesisStyle.Render(hypothesisPrefix + text)
	}

	return text
}

// RenderFailureClassTag renders a compact tag for the given FailureClass.
func RenderFailureClassTag(fc classifier.FailureClass) string {
	switch fc {
	case classifier.FailureCodeClass:
		return errorClassStyle.Render("[CODE ERROR]")
	case classifier.FailureEnvironmentClass:
		return errorClassStyle.Render("[ENV ERROR]")
	case classifier.FailureTestClass:
		return errorClassStyle.Render("[TEST FAIL]")
	case classifier.FailureScopeClass:
		return errorClassStyle.Render("[SCOPE ERROR]")
	case classifier.FailureUnknownClass:
		return unknownStyle.Render("[UNKNOWN]")
	default:
		return unknownStyle.Render("[UNKNOWN]")
	}
}

// ── Heuristics ─────────────────────────────────────────────────────────────

func isSuccessIndicator(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "✓") ||
		strings.HasPrefix(lower, "✔") ||
		strings.HasPrefix(lower, "done") ||
		strings.HasPrefix(lower, "passed") ||
		strings.HasPrefix(lower, "succeeded") ||
		strings.HasPrefix(lower, "complete") ||
		strings.HasPrefix(lower, "[ ok ]") ||
		strings.HasPrefix(lower, "all tests passed")
}

func isFailureIndicator(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "✗") ||
		strings.HasPrefix(lower, "✘") ||
		strings.HasPrefix(lower, "fail") ||
		strings.HasPrefix(lower, "error") ||
		strings.HasPrefix(lower, "halted") ||
		strings.HasPrefix(lower, "rejected") ||
		strings.HasPrefix(lower, "[fail]") ||
		strings.Contains(lower, "test failed")
}
