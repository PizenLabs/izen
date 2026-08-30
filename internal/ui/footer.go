package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/ui/status"
)

// ── Footer Lifecycle State Machine (Refined 3-State) ─────────────────────────
//
// The fixed footer is a single dynamic 1-line bar whose content is a pure
// projection of the interaction lifecycle. It has exactly three states:
//
//	a. FRESH LAUNCH  (!sessionHasRunPrompts && !isExecuting)
//	   Clean startup hint: "<active_model_alias>  ·  Alt+E trace  ·  Ctrl+H help".
//	   No token counters, no cost, no zero-value indicators — a brand-new
//	   session never clutters the footer with idle telemetry.
//	b. EXECUTING     (isExecuting)
//	   Live stream bar: "⠋ Generating...  ·  ↓<live_tok> tok  ·  <rate> tok/s
//	   ·  Ctrl+C interrupt". The spinner pulses cyan→amber. The instant
//	   execution ends, isExecuting() flips false and the bar is replaced —
//	   'Ctrl+C interrupt' and the '⏸' icon never survive past completion.
//	c. ACTIVE SESSION IDLE (sessionHasRunPrompts && !isExecuting)
//	   Persistent refined telemetry anchored on the active model name:
//	   "<Model>  ·  ↓<in> + ↑<out> tok (<ctx_pct>%)  ·  <Cost>  ·  Alt+E trace".
//	   The Mode Badge belongs EXCLUSIVELY to the Top Bar right side — it never
//	   appears in the footer.
//
// Toasts NEVER render here — they belong to the Top Bar transient overlay
// (internal/ui/topbar.go).

// Footer styles (Catppuccin Mocha).
var (
	footerHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	footerSepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	footerModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	footerTokStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorTeal))

	footerExecLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	footerExecMetaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
)

// footerSep joins footer segments with the canonical "  ·  " separator.
func footerSep(segments ...string) string {
	return strings.Join(segments, "  "+footerSepStyle.Render("·")+"  ")
}

// isExecuting reports whether a foreground operation is in flight (streaming,
// agent/review/investigate run, pipeline synthesis, shell execution, mutation
// processing or an owned foreground operation). It drives the footer's
// EXECUTING ↔ idle transition.
func (m *model) isExecuting() bool {
	if m.state == StateProcessing {
		return true
	}
	return m.streaming || m.execStreaming || m.agentRunning || m.reviewRunning ||
		m.investigateRunning || m.shellRunning || m.pipelineRunning ||
		m.planPending || m.activeOp != nil
}

// renderFixedFooter renders the anchored single-line bottom bar as a pure
// projection of the interaction lifecycle:
//
//	EXECUTING while an operation is in flight
//	FRESH LAUNCH before the first submitted prompt
//	ACTIVE SESSION IDLE otherwise
//
// actions are the current result's capability chips; when present they
// right-align on the idle line so the affordance survives without stealing a
// second row.
func (m *model) renderFixedFooter(width int, actions []Action) string {
	if width < 20 {
		return ""
	}
	var s string
	switch {
	case m.isExecuting():
		s = m.renderExecutingFooter()
	case !m.sessionHasRunPrompts:
		s = m.renderFreshLaunchFooter()
	default:
		s = m.renderActiveIdleFooter(width, actions)
	}
	// Never let a busy execution bar overflow a narrow split pane: truncate to
	// the available width instead of wrapping to a second row.
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	return s
}

// renderFreshLaunchFooter renders the clean startup hint for a brand-new
// session: "<model>  ·  Alt+E trace  ·  Ctrl+H help". No counters, no cost, no
// zero-value indicators.
func (m *model) renderFreshLaunchFooter() string {
	return footerSep(
		footerModelStyle.Render(m.getActiveModelName()),
		footerHelpStyle.Render("Alt+E trace"),
		footerHelpStyle.Render("Ctrl+H help"),
	)
}

// renderActiveIdleFooter renders the persistent Active-Session IDLE telemetry,
// anchored on the active model name:
//
//	<Model>  ·  ↓<in> + ↑<out> tok (<ctx_pct>%)  ·  <Cost>  ·  Alt+E trace
//
// e.g. "qwen2.5-coder:7b  ·  ↓0 + ↑170 tok (1%)  ·  $free  ·  Alt+E trace".
// The Mode Badge is deliberately absent — the Top Bar owns it. 'Ctrl+C
// interrupt' and the '⏸' icon are never present here.
func (m *model) renderActiveIdleFooter(width int, actions []Action) string {
	cost := llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), m.AccumulatedCost)
	base := footerSep(
		footerModelStyle.Render(m.getActiveModelName()),
		footerTokStyle.Render(status.FormatUsageContext(m.InputTokens, m.OutputTokens, m.TotalTokens, m.activeContextLimit())),
		footerExecMetaStyle.Render(llm.FormatCost(cost)),
		footerHelpStyle.Render("Alt+E trace"),
	)

	chip := renderActions(actions)
	if chip == "" {
		return base
	}
	return padRightOverlay(base, chip, width)
}

// renderExecutingFooter renders the live EXECUTING bar:
//
//	⠋ Generating...  ·  ↓<tok> tok  ·  <rate> tok/s  ·  Ctrl+C interrupt
//
// The tok count is ONLY the authoritative provider-reported stage count (fed
// via setStageMetrics from the stream's ProviderUsage) — never a character
// estimate. The rate is derived from that authoritative count over the
// stream's wall-clock elapsed time. This bar exists strictly while an
// operation is in flight; on completion it is replaced wholesale, so
// 'Ctrl+C interrupt' / '⏸' can never linger.
func (m *model) renderExecutingFooter() string {
	st := m.stageSnapshot()
	return footerSep(
		m.executingSpinner()+" "+footerExecLabelStyle.Render("Generating..."),
		footerTokStyle.Render("↓"+status.FormatTokens(st.Tokens)+" tok"),
		footerExecMetaStyle.Render(formatTokenRate(m.streamTokenRate(st))+" tok/s"),
		interruptLabelStyle.Render(Icon.Interrupt+" Ctrl+C interrupt"),
	)
}

// executingSpinner renders the braille spinner frame with a cyan→amber
// pulsation, signalling live background activity during EXECUTING.
func (m *model) executingSpinner() string {
	n := len(ProposalSpinnerFrames)
	frameStr := ProposalSpinnerFrames[m.spinnerFrame%n]

	phase := float64(m.spinnerFrame) * (2 * math.Pi / float64(n))
	t := (math.Sin(phase) + 1) / 2
	t = t * t * (3 - 2*t)

	from := lipgloss.Color(colorCyan) // #89dceb teal
	to := lipgloss.Color(colorYellow) // #f9e2af amber
	return SpinnerStyle.Foreground(interpolateColor(from, to, t)).Render(frameStr)
}

// streamTokenRate returns the live token-per-second rate of the active stream,
// derived from the authoritative stage token count over wall-clock elapsed time.
func (m *model) streamTokenRate(st stageView) float64 {
	if st.Tokens <= 0 {
		return 0
	}
	start := m.streamStartTime
	if start.IsZero() {
		return 0
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		return 0
	}
	return float64(st.Tokens) / elapsed.Seconds()
}

// formatTokenRate renders a tok/s rate compactly: integers at 100+, one
// decimal below.
func formatTokenRate(rate float64) string {
	if rate >= 100 {
		return fmt.Sprintf("%d", int(rate))
	}
	return fmt.Sprintf("%.1f", rate)
}

// renderModeBadge renders the current mode as a compact capability badge:
// read-only modes → "[READ-ONLY]", build → "[WRITE]", investigate → "[EXECUTE]".
// It belongs EXCLUSIVELY to the fixed Top Bar's right side — the footer never
// renders it.
func renderModeBadge(mode modes.Mode) string {
	var label string
	switch mode {
	case modes.ModeBuild:
		label = "WRITE"
	case modes.ModeInvestigate:
		label = "EXECUTE"
	default:
		label = "READ-ONLY"
	}
	var style lipgloss.Style
	if isCoreEngineeringMode(mode) {
		style = modeBoldFgStyles[mode]
	} else {
		style = secondaryModeStyle
	}
	return style.Render("[" + label + "]")
}
