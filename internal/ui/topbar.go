package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Top Bar Toast Transient Overlay ────────────────────────────────────────
//
// Toast notifications are transient by design: they surface on the fixed Top
// Bar's far-right boundary, never in the lifecycle footer. A toast lives for
// toastDuration and then auto-clears — the Top Bar is a fixed 1-line chrome so
// an active toast must REPLACE the mode badge rather than expand the top bar or
// overlap the workflow state on the left.
const toastDuration = 2500 * time.Millisecond

var (
	// toastStyle is the subtle teal frame + message (Catppuccin Cyan #89dceb).
	toastStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))
	// toastCheckStyle is the crisp mint-green confirmation glyph (Catppuccin
	// Green #a6e3a1).
	toastCheckStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreen))
)

// setToast stages a transient top-bar notification and anchors its display
// window. Every caller migrates from the old uiNotice channel to this method;
// the message auto-clears after toastDuration, so no explicit teardown is
// required.
func (m *model) setToast(msg string) {
	m.toast = msg
	m.toastSetAt = time.Now()
}

// toastActive reports whether the current toast is still inside its display
// window. An expired (or empty) toast is treated as inactive so the Top Bar
// falls back to its normal right-side status indicators.
func (m *model) toastActive() bool {
	if m.toast == "" {
		return false
	}
	return time.Since(m.toastSetAt) < toastDuration
}

// clearToast force-clears the toast (e.g. /clear). The toast would also expire
// on its own after toastDuration.
func (m *model) clearToast() {
	m.toast = ""
	m.toastSetAt = time.Time{}
}

// renderToast renders the "[✓ <msg>]" overlay token. The confirmation glyph is
// mint green (#a6e3a1) and the frame + message read in subtle teal (#89dceb).
func renderToast(msg string) string {
	return toastStyle.Render("[") +
		toastCheckStyle.Render("✓") +
		" " +
		toastStyle.Render(msg) +
		toastStyle.Render("]")
}

// topBarToast returns the toast token to overlay on the Top Bar's far-right
// boundary, or "" when no toast is currently active.
func (m *model) topBarToast() string {
	if !m.toastActive() {
		return ""
	}
	return renderToast(m.toast)
}

// renderTopBar assembles the fixed top bar with an optional transient toast
// overlaid on the far-right boundary. When a toast is active it replaces the
// mode badge so the bar stays a single fixed line and never overlaps the
// workflow-state badge.
func (m *model) renderTopBar(width int) string {
	return renderFixedHeader(m.runtimeCtx, m.workflowSM, m.resolver.Current(), width, m.indexingStatus, m.topBarToast())
}

// padRightOverlay right-aligns an overlay token (toast or capability chip) onto
// a base content line, truncating the token so the combined line never exceeds
// the bar width. The base line keeps its left-side content; the right-side
// indicators (indexing/caps) are already dropped by the caller when a toast is
// active.
func padRightOverlay(base, overlay string, width int) string {
	if overlay == "" {
		return base
	}
	baseW := lipgloss.Width(base)
	maxOverlay := width - baseW - 1
	if maxOverlay < 1 {
		return base
	}
	if lipgloss.Width(overlay) > maxOverlay {
		overlay = ansi.Truncate(overlay, maxOverlay, "…")
	}
	pad := width - baseW - lipgloss.Width(overlay)
	if pad < 0 {
		pad = 0
	}
	return base + strings.Repeat(" ", pad) + overlay
}
