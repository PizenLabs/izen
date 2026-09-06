package ui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var presentationAnsiRegex = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

var _ = presentationAnsiRegex
var _ = ansiRegex

// PresentationMiddleware intercepts runtime/executor events, errors, and
// terminal execution states before they reach the view projection.
// It enforces humanized error rendering and honest completion semantics.
type PresentationMiddleware struct{}

// NewPresentationMiddleware creates a new PresentationMiddleware instance.
func NewPresentationMiddleware() *PresentationMiddleware {
	return &PresentationMiddleware{}
}

// InterceptError intercepts runtime error text and returns humanized presentation
// formatting if applicable. If it detects a bounded patch automatic recovery
// (e.g. from OUTPUT_EXHAUSTED or Feedback Controller recovery), it renders the
// concise warning badge instead of a raw stack trace.
func (pm *PresentationMiddleware) InterceptError(errText string) (intercepted bool, rendered string) {
	if isBoundedPatchRecovery(errText) {
		badge := RenderBoundedPatchRecoveryBadge()
		return true, badge
	}
	return false, ""
}

// isBoundedPatchRecovery reports whether the error text indicates an automatic
// recovery from model output exhaustion into a bounded patch contract.
func isBoundedPatchRecovery(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "output_exhausted") {
		return true
	}
	if strings.Contains(lower, "token limit reached") {
		return true
	}
	if strings.Contains(lower, "full_rewrite -> bounded_patch") {
		return true
	}
	if strings.Contains(lower, "auto-recovering via bounded patch") {
		return true
	}
	if strings.Contains(lower, "bounded_patch") && (strings.Contains(lower, "exhaust") || strings.Contains(lower, "limit") || strings.Contains(lower, "recover")) {
		return true
	}
	return false
}

// RenderBoundedPatchRecoveryBadge renders the concise humanized recovery badge:
// ⚡ Token limit reached. Auto-recovering via bounded patch...
func RenderBoundedPatchRecoveryBadge() string {
	const (
		iconYellow = "\x1b[1;38;2;249;226;175m" // Catppuccin Yellow #f9e2af
		textPeach  = "\x1b[38;2;250;179;135m"   // Catppuccin Peach #fab387
		reset      = "\x1b[0m"
	)
	return iconYellow + "⚡ " + textPeach + "Token limit reached. Auto-recovering via bounded patch..." + reset
}

// FormatCompletionState returns the canonical Section 30 tuple format:
// ExecutionOutcome × EvidenceState (e.g., COMPLETED · PARTIALLY_VERIFIED).
func FormatCompletionState(outcome string, evidenceState string) string {
	outcomeClean := strings.ToUpper(strings.TrimSpace(outcome))
	if outcomeClean == "" {
		outcomeClean = "COMPLETED"
	}
	evidenceClean := strings.ToUpper(strings.TrimSpace(evidenceState))
	if evidenceClean == "" {
		evidenceClean = "VERIFIED"
	}

	outcomeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreen))
	switch outcomeClean {
	case "FAILED", "ABORTED", "ERROR":
		outcomeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	case "PARTIAL", "REQUIRES_REVIEW":
		outcomeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorYellow))
	}

	evidenceStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))

	return outcomeStyle.Render(outcomeClean) + " · " + evidenceStyle.Render(evidenceClean)
}
