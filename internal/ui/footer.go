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
//	   Clean startup hint: "<active_model_alias>  ·  ? help".
//	   No token counters, no cost, no zero-value indicators — a brand-new
//	   session never clutters the footer with idle telemetry.
//	b. EXECUTING     (isExecuting)
//	   Live stream bar: "⠋ Generating...  ·  ↑<in> in ↓<out> out (<cost>)  ·  <rate> tok/s
//	   ·  [model]  ·  ^C stop" seeded at t=0 as "↑C_in in · ↓0 out". The token
//	   slots and rate are fixed-width (no horizontal jitter), and the "^C stop"
//	   interrupt badge is the LAST segment to ever be dropped when the pane
//	   narrows (see footerDropToFit). The spinner pulses cyan→amber. The
//	   instant execution ends, isExecuting() flips false and the bar is
//	   replaced — '^C stop' never survives past completion.
//	c. ACTIVE SESSION IDLE (sessionHasRunPrompts && !isExecuting)
//	   Persistent refined telemetry anchored on the active model name:
//	   "<Model>  ·  ↑<in> in · ↓<out> out (<ctx_pct>%)  ·  <Cost>".
//	   ↑ = input tokens, ↓ = output tokens (explicit input/output separation).
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
		s = m.renderExecutingFooter(width)
	case !m.sessionHasRunPrompts:
		s = m.renderFreshLaunchFooter()
	default:
		s = m.renderActiveIdleFooter(width, actions)
	}
	// Strictly enforce width: truncate if over, pad if under, so the footer
	// never wraps under split-pane layouts. lipgloss.Width is cell-aware and
	// ansi.Truncate is ANSI-safe (never splits an SGR sequence).
	s = fitToWidth(s, width)
	return s
}

// fitToWidth ensures s is exactly width cells: truncate with ellipsis if
// longer, pad with spaces if shorter. ANSI escapes are stripped for width
// measurement but preserved via ansi.Truncate.
func fitToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w > width {
		return ansi.Truncate(s, width, "…")
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// truncateModelName shortens a model name to max cells, adding an ellipsis if
// truncated. Cell-aware, not byte-aware, so wide glyphs are handled correctly.
func truncateModelName(name string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(name) <= max {
		return name
	}
	if max <= 1 {
		return "…"
	}
	// Use ansi.Truncate with ellipsis; it is ANSI-safe though model names
	// are plain. The ellipsis itself occupies one cell.
	return ansi.Truncate(name, max, "…")
}

// formatModelWithVariant formats the active model string with its reasoning
// variant badge: `model-id (variant)` when a variant is active, plain
// `model-id` when the variant is empty or off. The comparison is
// case-insensitive and trims whitespace; "off", "default" and "none" are all
// treated as no-variant so the footer never shows `model (off)`.
func formatModelWithVariant(modelName, variant string) string {
	v := strings.TrimSpace(variant)
	if v == "" {
		return modelName
	}
	switch strings.ToLower(v) {
	case "off", "default", "none":
		return modelName
	}
	if modelName == "" {
		return v
	}
	return modelName + " (" + v + ")"
}

// activeVariantLabel returns the active model variant / reasoning effort
// label for the footer badge. Lookup order (first non-empty wins, live on
// every render so role/variant switches reflect immediately):
//
//  1. RuntimeAuthority active binding VariantParams (picker ACTIVATE path).
//  2. Persisted config Bindings.Active.Variant (survives restarts).
//  3. Local effort selector (m.currentEffort ←/→ widget, dynamic per turn).
func (m *model) activeVariantLabel() string {
	if m == nil {
		return ""
	}
	if m.modelAuthority != nil {
		if b := m.modelAuthority.ActiveBinding(); b.VariantParams != "" {
			return string(b.VariantParams)
		}
	}
	if m.cfg != nil && strings.TrimSpace(m.cfg.Bindings.Active.Variant) != "" {
		return m.cfg.Bindings.Active.Variant
	}
	if m.currentEffort != EffortDefault && m.currentEffort != EffortNone {
		return m.currentEffort.Description()
	}
	return ""
}

// getActiveModelDisplay returns the footer-ready model label: the active
// model id with the variant badge in parentheses when a variant is active
// (e.g. `nex-agi/nex-n2.5-mini:free (medium)`), plain id otherwise.
func (m *model) getActiveModelDisplay() string {
	return formatModelWithVariant(m.getActiveModelName(), m.activeVariantLabel())
}

// renderActiveIdleFooterResponsive is the width-responsive, tiered footer
// core specified in the task. It is a pure function that strictly respects
// the available terminal width (termWidth):
//
//	Tier 1: Full Width >= 100  →  model  ·  ↑in in · ↓out out (pct%)  ·  cost  ·  [mode]
//	Tier 2: Standard 70..99     →  model  ·  ↑in in · ↓out out (pct%)  ·  cost
//	Tier 3: Compact 45..69      →  shortModel  ·  ↑in in · ↓out out
//	Tier 4: Minimal <45         →  ↑in in · ↓out out
//
// The returned string is strictly truncated or padded to exactly width.
func renderActiveIdleFooter(width int, modelName string, inTok, outTok int, ctxPct float64, cost string, mode string) string {
	in := statusArrowIn(status.FormatTokens(inTok))
	out := statusArrowOut(status.FormatTokens(outTok))
	var s string
	switch {
	case width >= 100:
		s = fmt.Sprintf("%s  ·  %s in · %s out (%d%%)  ·  %s  ·  [%s]", modelName, in, out, int(ctxPct), cost, mode)
	case width >= 70:
		s = fmt.Sprintf("%s  ·  %s in · %s out (%d%%)  ·  %s", modelName, in, out, int(ctxPct), cost)
	default:
		// Compact and minimal share the shortModel helper for 45..69.
		if width >= 45 {
			shortModel := truncateModelName(modelName, 12)
			s = fmt.Sprintf("%s  ·  %s in · %s out", shortModel, in, out)
		} else {
			s = fmt.Sprintf("%s in · %s out", in, out)
		}
	}
	return fitToWidth(s, width)
}

// renderFreshLaunchFooter renders the clean startup hint for a brand-new
// session: "<model>  ·  ? help". No counters, no cost, no
// zero-value indicators.
func (m *model) renderFreshLaunchFooter() string {
	return footerSep(
		footerModelStyle.Render(m.getActiveModelDisplay()),
		footerHelpStyle.Render("? help"),
	)
}

// renderActiveIdleFooter renders the persistent Active-Session IDLE telemetry
// with width-responsive tiers. It strictly respects the available terminal
// width so split-pane layouts never cause wrapping:
//
//	Tier 1 >=100: full model + usage (with pct) + cost
//	Tier 2 70-99: same tier 1 + chip overlay
//	Tier 3 45-69: short model (12 cells) + compact usage (↑in · ↓out)
//	Tier 4 <45:   minimal usage only (↑in · ↓out)
//
// Both compact and full usage render via the canonical status formatters
// (↑ = input, ↓ = output). The Mode Badge is deliberately absent — the Top
// Bar owns it. '^C stop' and the '⏸' icon are never present here. The caller
// (renderFixedFooter) enforces the final exact-width fit via fitToWidth.
func (m *model) renderActiveIdleFooter(width int, actions []Action) string {
	cost := llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), m.AccumulatedCost)
	costStr := llm.FormatCost(cost)
	modelName := m.getActiveModelDisplay()
	fullUsage := status.FormatUsageContext(m.InputTokens, m.OutputTokens, m.TotalTokens, m.activeContextLimit())
	compactTok := status.FormatUsageValues(m.InputTokens, m.OutputTokens)

	var base string
	switch {
	case width >= 70:
		// Tiers 1 and 2: full telemetry (model + usage with pct + cost) — Session Total only
		base = footerSep(
			footerModelStyle.Render(modelName),
			footerTokStyle.Render(fullUsage),
			footerExecMetaStyle.Render(costStr),
		)
	case width >= 45:
		shortModel := truncateModelName(modelName, 12)
		base = footerSep(
			footerModelStyle.Render(shortModel),
			footerTokStyle.Render(compactTok),
		)
	default:
		base = footerTokStyle.Render(compactTok)
	}

	chip := renderActions(actions)
	if chip == "" {
		return base
	}
	// In minimal or compact tiers, chips would overflow; only overlay when
	// there is enough width to show them alongside telemetry.
	if width < 70 {
		return base
	}
	return padRightOverlay(base, chip, width)
}

// ttftDuration resolves the live Time-To-First-Token deadline for the
// active model/provider profile (llm.ResolveTTFTTimeout): reasoning, heavy
// and free-tier models wait 45s–90s, standard/fast models fail fast at
// 15s–20s, and config.Timeout.TTFT overrides everything. It is evaluated
// on every render so model switches, variant changes and config overrides
// reflect immediately in the countdown.
func (m *model) ttftDuration() time.Duration {
	var override time.Duration
	if m.cfg != nil {
		override = m.cfg.TTFTTimeoutOverride()
	}
	return llm.ResolveTTFTTimeout(llm.ModelSpec{
		Provider:        m.getActiveProviderName(),
		ModelID:         m.getActiveModelName(),
		Variant:         m.activeVariantLabel(),
		TimeoutOverride: override,
	})
}

// ttftProviderModelLabel renders the [{provider/model}] badge for the
// connecting line. Vendor-prefixed ids (OpenRouter "vendor/model") already
// encode the path and are used as-is; bare ids are prefixed with the
// active provider.
func (m *model) ttftProviderModelLabel() string {
	display := m.getActiveModelDisplay()
	if strings.Contains(display, "/") {
		return display
	}
	if prov := m.getActiveProviderName(); prov != "" {
		return prov + "/" + display
	}
	return display
}

// firstTokenReceived reports whether the first stream token has arrived.
// The UTF-8 byte buffer is primary: the instant it holds the first valid
// byte the TTFT countdown stops. The authoritative provider token count,
// the streaming stage, and the already-emitted content are fallbacks for
// paths that bypass the byte buffer (local models without usage metadata).
// While false the footer shows the connection stopwatch; the instant it
// flips true the timer stops and the bar switches to live token metrics.
func (m *model) firstTokenReceived(st stageView) bool {
	if m.utf8StreamBuf != nil && m.utf8StreamBuf.Len() > 0 {
		return true
	}
	if st.Tokens > 0 {
		return true
	}
	if st.State == stageStreaming {
		return true
	}
	if len(m.currentStreamContent) > 0 {
		return true
	}
	if m.responseBuffer.Len() > 0 {
		return true
	}
	return false
}

// noFirstByteReceived reports whether no provider byte (content or
// reasoning) has arrived this turn. It gates the TTFT-timeout diagnosis:
// an error before the first byte is a connection-phase stall (DNS / TCP /
// TLS / response headers); the same error after bytes arrived is a
// mid-stream failure and must not be mislabeled as TTFT.
func (m *model) noFirstByteReceived() bool {
	if m.utf8StreamBuf != nil && m.utf8StreamBuf.Len() > 0 {
		return false
	}
	if len(m.currentStreamContent) > 0 || m.responseBuffer.Len() > 0 {
		return false
	}
	if m.thinkingBuffer != nil && m.thinkingBuffer.Len() > 0 {
		return false
	}
	return true
}

// renderExecutingFooter renders the live EXECUTING bar, width-aware:
//
//	pre-TTFT (no first token yet):
//	  ⠋ Connecting... 14s [groq/llama-3.3-70b]  ·  ^C stop
//	post-first-token (live cost burn):
//	  ⠋ Generating...  ·  ↑<in> in ↓<out> out ($<cost>)  ·  <rate> tok/s  ·  [model]  ·  ^C stop
//
// ↑ = input tokens, ↓ = output tokens; the in/out slots are fixed-width so
// the metric line never jitters while counts grow. The pre-TTFT countdown
// renders on every FrameTickMsg (30ms) while the first byte is awaited and
// freezes the moment it arrives. It counts DOWN the dynamic TTFT deadline
// (ttftDuration: 15s fast models, up to 90s for reasoning/free-tier) as a
// single integer — stable width, no decimal flicker. remaining = max(0,
// ttft - elapsed); when it reaches 0 before headers arrive the stall error
// path reports "provider response stalled: TTFT timeout (<ttft>s elapsed)".
// The live tok count is max(authoritative provider stage count, per-chunk
// live estimate) so the meter advances on every chunk; the cost is
// C_est = (T_in*P_in + T_out*P_out)/1M seeded at t=0 with 0 output tokens
// ($free when pricing is 0). This bar exists strictly while an operation is
// in flight; on completion it is replaced wholesale, so '^C stop' can never
// linger. When narrow, footerDropToFit drops segments in priority order
// (model → rate → tokens) and the '^C stop' badge is always last to drop.
// When in StateRetrying (retryInfo != nil), an explicit retry banner is shown
// instead of hanging on "Generating...": "[Retry N/M] <error>. Retrying in Xs..."
func (m *model) renderExecutingFooter(width int) string {
	// The interrupt badge is drop-proof: computed once, shared by all paths.
	stop := interruptLabelStyle.Render(stopBadge)
	// Retry state takes precedence: show explicit banner, not stale generating.
	if m.retryInfo != nil {
		banner := formatRetryBanner(m.retryInfo)
		return footerDropToFit(width, []string{
			m.executingSpinner() + " " + footerExecLabelStyle.Render(banner),
			stop,
		})
	}
	st := m.stageSnapshot()
	// Pre-TTFT connection phase: single-number countdown against the
	// dynamic TTFT deadline. The timer stops the instant the first token
	// arrives (see firstTokenReceived) and the bar transitions to token
	// metrics below.
	if !m.firstTokenReceived(st) && !m.executionStartedAt.IsZero() && m.isExecuting() {
		start := m.executionStartedAt
		if start.IsZero() {
			start = m.streamStartTime
		}
		elapsed := time.Since(start)
		if elapsed < 0 {
			elapsed = 0
		}
		ttft := m.ttftDuration()
		remaining := int((ttft - elapsed).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		pulse := fmt.Sprintf("Connecting... %ds [%s]",
			remaining, truncateModelName(m.ttftProviderModelLabel(), 24))
		return footerDropToFit(width, []string{
			m.executingSpinner() + " " + footerExecLabelStyle.Render(pulse),
			stop,
		})
	}
	modelName := m.getActiveModelDisplay()
	liveOut := m.streamLiveOutputTokens()
	costLabel := m.streamCostLabel()
	// FIXED-WIDTH METRICS: the in/out token slots and the rate segment are
	// padded to deterministic widths so the bar never shifts while counts
	// grow. Cost rides inside the token segment (feature-preserving); the
	// model badge truncates at 12 cells. footerDropToFit strips the least
	// critical trailing segments when the pane narrows.
	tokIn := padFixedWidth(statusArrowIn(status.FormatTokens(m.streamBaseInputTokens))+" in", execInSlotWidth)
	tokOut := padFixedWidth(statusArrowOut(status.FormatTokens(liveOut))+" out", execOutSlotWidth)
	tokSeg := tokIn + " " + tokOut + " (" + costLabel + ")"
	rateSeg := padFixedWidth(formatTokenRate(m.streamTokenRate(st))+" tok/s", execRateSegmentWidth)
	return footerDropToFit(width, []string{
		m.executingSpinner() + " " + footerExecLabelStyle.Render("Generating..."),
		footerTokStyle.Render(tokSeg),
		footerExecMetaStyle.Render(rateSeg),
		footerModelStyle.Render("[" + truncateModelName(modelName, 12) + "]"),
		stop,
	})
}

// footerDropToFit renders a footer line from ordered segments, preserving the
// LAST segment ('^C stop') as the drop-proof anchor: whenever the joined line
// exceeds width, the least critical segment is dropped (model badge → rate →
// token telemetry) and the line re-measured. The return value never exceeds
// width — fitToWidth truncates only in the extreme sub-30-cell panes where
// even the bare spinner+badge pair overflows.
func footerDropToFit(width int, segments []string) string {
	for len(segments) > 2 {
		line := footerSep(segments...)
		if lipgloss.Width(line) <= width {
			return fitToWidth(line, width)
		}
		segments = append(segments[:len(segments)-2], segments[len(segments)-1])
	}
	return fitToWidth(footerSep(segments...), width)
}

// statusArrowIn and statusArrowOut prefix a formatted token count with the
// explicit input/output glyph contract: ↑ = input (prompt), ↓ = output
// (completion).
func statusArrowIn(n string) string  { return "↑" + n }
func statusArrowOut(n string) string { return "↓" + n }

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

// estimateStreamTokens converts one streamed text chunk into a token estimate
// (~4 characters per token, minimum 1 per non-empty chunk) so EVERY chunk —
// content or reasoning/thinking — advances the live tok/s meter. Empty chunks
// contribute nothing.
func estimateStreamTokens(chunk string) int {
	if chunk == "" {
		return 0
	}
	n := len(chunk) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// streamTokenRate returns the live token-per-second rate of the active stream,
// derived from the token count over wall-clock elapsed time. The count is the
// maximum of the authoritative provider-reported stage count and the live
// per-chunk estimate (m.streamLiveTokens, which includes reasoning/thinking
// chunks that arrive before any authoritative usage chunk). The authoritative
// stage count itself is never estimated — this live estimate feeds ONLY the
// rate meter, so the footer never shows 0.0 tok/s while tokens are actively
// streaming.
func (m *model) streamTokenRate(st stageView) float64 {
	tokens := st.Tokens
	if m.streamLiveTokens > tokens {
		tokens = m.streamLiveTokens
	}
	if tokens <= 0 {
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
	return float64(tokens) / elapsed.Seconds()
}

// formatTokenRate renders a tok/s rate compactly: integers at 100+, one
// decimal below.
func formatTokenRate(rate float64) string {
	if rate >= 100 {
		return fmt.Sprintf("%d", int(rate))
	}
	return fmt.Sprintf("%.1f", rate)
}

// ── FIXED-WIDTH STATUS METRICS (streaming-scroll decoupling) ─────────────
// Token counts and tok/s rates are padded to deterministic cell widths so the
// executing footer never shifts horizontally while text streams. Padding is
// trailing (right-pad) so existing substrings ("↑128 in", "12.8", "tok/s")
// remain intact for tests and the line never wraps mid-stream. The in/out
// slots carry the explicit ↑/↓ input/output label (arrow + " in"/" out"); the
// rate slot and the drop-proof "^C stop" interrupt badge complete the bar.
const (
	execTokCountWidth    = 10 // legacy: "↓128 tok" (8 cells) + pad; covers "↓12.3k tok"
	execRateSegmentWidth = 11 // e.g. "12.8 tok/s" (10 cells) + pad
	execInSlotWidth      = 11 // fixed-width "↑12.3k in" slot (10 cells) + pad
	execOutSlotWidth     = 11 // fixed-width "↓12.3k out" slot (10 cells) + pad
)

// stopBadge is the compact interrupt affordance that anchors the executing
// footer's right edge. It replaces the legacy "⏸ Ctrl+C interrupt" pair —
// one badge, both the hint and the escape hatch, and the LAST segment a
// width-aware executing footer ever drops (see footerDropToFit).
const stopBadge = "^C stop"

// padFixedWidth right-pads s with spaces to exactly w cells (cell-aware).
// Longer strings are returned unchanged (the caller fitToWidth truncates).
func padFixedWidth(s string, w int) string {
	if w <= 0 {
		return s
	}
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
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
