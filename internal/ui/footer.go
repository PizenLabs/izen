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
//	   Live stream bar: "⠋ Generating...  ·  ↑<sessionIn> · ↓<sessionOut> (<cost>)  ·  <rate> tok/s
//	   ·  [model]  ·  ^C stop" seeded at t=0 as "↑C_in · ↓0" where C_in is the
//	   session cumulative (prior turns + current prompt). The token slots (8
//	   cells each) and rate (12 cells) are fixed-width (no horizontal jitter),
//	   and the "^C stop" badge (10 cells, pinned right) is the LAST segment to
//	   ever be dropped when the pane narrows (see footerDropToFit). The spinner
//	   pulses cyan→amber. The instant execution ends, isExecuting() flips false
//	   and the bar is replaced — '^C stop' never survives past completion.
//	c. ACTIVE SESSION IDLE (sessionHasRunPrompts && !isExecuting)
//	   Persistent refined telemetry anchored on the active model name:
//	   "<Model:22>  ·  ↑<in:8> · ↓<out:8>  ·  <Cost:12>  ·  <Action:10>".
//	   ↑ = input tokens, ↓ = output tokens (minimalist glyphs, no suffixes).
//	   The Mode Badge belongs EXCLUSIVELY to the Top Bar right side — it never
//	   appears in the footer.
//
// Toasts NEVER render here — they belong to the Top Bar transient overlay
// (internal/ui/topbar.go).

// Footer styles (Catppuccin Mocha).
var (
	footerHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	footerModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	footerTokStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorTeal))

	footerExecLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	footerExecMetaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
)

// footerDotStyle renders the inline telemetry separator dot dimmed and
// faint so numerical data stays visually dominant.
var footerDotStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorSubtle))

// footerSep joins footer segments with the tight single-space inline
// separator: " " + dot + " ". Telemetry metrics use their natural character
// width — trailing space padding inside individual slots is forbidden
// (INVARIANT 1).
func footerSep(segments ...string) string {
	return strings.Join(segments, " "+footerDotStyle.Render("·")+" ")
}

// menuBadge is the idle-state right-block affordance, pinned to the exact
// right edge opposite the executing "^C stop" badge.
const menuBadge = "^P menu"

// flexPinRight implements the two-zone flex dispatch (INVARIANT 2): the left
// telemetry cluster keeps its natural inline flow, the right action badge is
// pinned to the exact right edge, and the middle gap is filled with exact
// whitespace padding: gap = totalWidth - width(Left) - width(Right).
// On narrow viewports the left cluster is truncated dynamically with an
// ellipsis so the right badge never detaches. The result is always exactly
// width cells (via fitToWidth).
func flexPinRight(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	if rightW >= width {
		return fitToWidth(right, width)
	}
	if leftW+rightW >= width {
		budget := width - rightW - 1
		if budget < 1 {
			return fitToWidth(right, width)
		}
		left = ansi.Truncate(left, budget, "…")
		leftW = lipgloss.Width(left)
	}
	spacer := width - leftW - rightW
	if spacer < 0 {
		spacer = 0
	}
	return fitToWidth(left+strings.Repeat(" ", spacer)+right, width)
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
		s = m.renderFreshLaunchFooter(width)
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

// ── FLEX-FLOW FOOTER GEOMETRY (ZERO-GAP TELEMETRY) ──────────────────────
// The footer is a two-zone flex dispatch: a left telemetry cluster with
// natural inline flow (metrics joined by tight " · " separators, zero
// trailing padding) and a right action badge (^C stop / ^P menu) pinned to
// the exact right edge. The middle gap is dynamic whitespace:
// gap = totalWidth - width(Left) - width(Right).
//
// Token counts use status.FormatTokens quantization (712, 1.2k, 14.8k) so
// numeric updates never reflow surrounding text (INVARIANT 3).

// renderActiveIdleFooter is the width-responsive, tiered footer core as a
// pure function:
//
//	Tier 1: Full Width >= 100  →  model · ↑in · ↓out (pct%) · cost · [mode]   + ^P menu pinned right
//	Tier 2: Standard 70..99     →  model · ↑in · ↓out (pct%) · cost           + ^P menu pinned right
//	Tier 3: Compact 45..69      →  shortModel · ↑in · ↓out                    + ^P menu pinned right
//	Tier 4: Minimal <45         →  ↑in · ↓out                                + ^P menu pinned right
//
// Flex-flow: the left cluster uses natural widths joined by tight " · ",
// the right badge is pinned via flexPinRight. Minimalist glyphs, zero
// "in"/"out" suffixes. The result is always exactly width cells.
func renderActiveIdleFooter(width int, modelName string, inTok, outTok int, ctxPct float64, cost string, mode string) string {
	in := statusArrowIn(status.FormatTokens(inTok))
	out := statusArrowOut(status.FormatTokens(outTok))
	var left string
	switch {
	case width >= 100:
		left = footerSep(modelName, in+" "+out+fmt.Sprintf(" (%d%%)", int(ctxPct)), cost, "["+mode+"]")
	case width >= 70:
		left = footerSep(modelName, in+" "+out+fmt.Sprintf(" (%d%%)", int(ctxPct)), cost)
	default:
		if width >= 45 {
			shortModel := truncateModelName(modelName, 12)
			left = footerSep(shortModel, in, out)
		} else {
			left = footerSep(in, out)
		}
	}
	return flexPinRight(left, footerExecMetaStyle.Render(menuBadge), width)
}

// renderFreshLaunchFooter renders the clean startup hint for a brand-new
// session as a flex line: left "<model> · ? help", right "^P menu" pinned
// via flexPinRight. No counters, no cost, no zero-value indicators.
func (m *model) renderFreshLaunchFooter(width int) string {
	left := footerSep(
		footerModelStyle.Render(m.getActiveModelDisplay()),
		footerHelpStyle.Render("? help"),
	)
	return flexPinRight(left, footerExecMetaStyle.Render(menuBadge), width)
}

// renderActiveIdleFooter renders the persistent Active-Session IDLE telemetry
// as a flex-flow line: left cluster
// "<model> · ↑<in> · ↓<out> · <cost>" with natural widths joined by tight
// " · ", right block "^P menu" (or the capability chip when actions are
// present) pinned via flexPinRight. Session totals are monotonic
// (m.InputTokens/m.OutputTokens).
//
// Minimalist glyph syntax: ↑<count> / ↓<count>, zero "in"/"out" suffixes.
// The Mode Badge is deliberately absent — the Top Bar owns it. '^C stop' and
// the '⏸' icon are never present here. Narrow widths tier down (cost drops
// first, then the model truncates) but the right badge is never dropped.
func (m *model) renderActiveIdleFooter(width int, actions []Action) string {
	cost := llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), m.AccumulatedCost)
	costStr := llm.FormatCost(cost)
	modelName := m.getActiveModelDisplay()

	inPlain := statusArrowIn(status.FormatTokens(m.InputTokens))
	outPlain := statusArrowOut(status.FormatTokens(m.OutputTokens))

	var left string
	switch {
	case width >= 70:
		left = footerSep(
			footerModelStyle.Render(modelName),
			footerTokStyle.Render(inPlain),
			footerTokStyle.Render(outPlain),
			footerExecMetaStyle.Render(costStr),
		)
	case width >= 45:
		shortModel := truncateModelName(modelName, 12)
		left = footerSep(
			footerModelStyle.Render(shortModel),
			footerTokStyle.Render(inPlain),
			footerTokStyle.Render(outPlain),
		)
	default:
		left = footerSep(
			footerTokStyle.Render(inPlain),
			footerTokStyle.Render(outPlain),
		)
	}

	chip := renderActions(actions)
	right := footerExecMetaStyle.Render(menuBadge)
	if chip != "" && width >= 70 {
		right = chip
	}
	return flexPinRight(left, right, width)
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

// renderExecutingFooter renders the live EXECUTING bar as a flex-flow line:
// left cluster "Generating... · ↑<sessionIn> · ↓<sessionOut> · <rate> tok/s"
// with natural widths joined by tight " · ", right block "^C stop" pinned
// via flexPinRight.
//
//	pre-TTFT (no first token yet):
//	  left "Connecting... Ns [provider/model]", right "^C stop"
//	post-first-token (live session burn):
//	  left "Generating... · ↑<in> · ↓<out> · <rate> tok/s", right "^C stop"
//
// INVARIANT 2 (monotonic session accumulation): while streaming,
// sessionInput = priorTurnsInput + currentTurnPrompt and sessionOutput =
// priorTurnsOutput + liveStreamTokens, so multi-turn sessions grow
// monotonically. Minimalist glyphs, zero "in"/"out" suffixes. The pre-TTFT
// countdown renders on every FrameTickMsg while the first byte is awaited.
// Narrow panes drop the rate first, then token telemetry — '^C stop' is
// never dropped. When in StateRetrying, an explicit retry banner replaces
// "Generating...".
func (m *model) renderExecutingFooter(width int) string {
	// The interrupt badge is drop-proof and pinned to the exact right edge.
	stop := interruptLabelStyle.Render(stopBadge)
	// Retry state takes precedence: show explicit banner, not stale generating.
	if m.retryInfo != nil {
		banner := formatRetryBanner(m.retryInfo)
		left := m.executingSpinner() + " " + footerExecLabelStyle.Render(banner)
		return flexPinRight(left, stop, width)
	}
	st := m.stageSnapshot()
	// Pre-TTFT connection phase: single-number countdown against the
	// dynamic TTFT deadline. The timer stops the instant the first token
	// arrives and the bar transitions to token metrics below.
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
		left := m.executingSpinner() + " " + footerExecLabelStyle.Render(pulse)
		return flexPinRight(left, stop, width)
	}
	sess := m.snapshotSessionMetrics()
	tokIn := footerTokStyle.Render(statusArrowIn(status.FormatTokens(sess.TotalInput())))
	tokOut := footerTokStyle.Render(statusArrowOut(status.FormatTokens(sess.TotalOutput())))
	rateSeg := footerExecMetaStyle.Render(formatTokenRate(m.streamTokenRate(st)) + " tok/s")
	stateSeg := m.executingSpinner() + " " + footerExecLabelStyle.Render("Generating...")

	// Priority drop: rate first, then output, then input — state + stop
	// always survive. Each candidate left cluster is flex-pinned; the first
	// candidate whose natural width fits wins, otherwise the minimal pair
	// is truncated dynamically by flexPinRight.
	candidates := [][]string{
		{stateSeg, tokIn, tokOut, rateSeg},
		{stateSeg, tokIn, tokOut},
		{stateSeg, tokIn},
		{stateSeg},
	}
	for _, tokens := range candidates {
		left := footerSep(tokens...)
		if lipgloss.Width(left)+lipgloss.Width(stop)+1 <= width {
			return flexPinRight(left, stop, width)
		}
	}
	return flexPinRight(footerSep(stateSeg), stop, width)
}

// footerDropToFit renders a footer line from ordered segments, preserving the
// LAST segment ('^C stop') as the drop-proof anchor: whenever the joined line
// exceeds width, the least critical segment is dropped and the line
// re-measured. Segments use natural widths with the tight " · " separator;
// the surviving line is right-pinned via flexPinRight so the anchor sits on
// the exact right edge.
//
//nolint:unused
func footerDropToFit(width int, segments []string) string {
	if len(segments) == 0 {
		return fitToWidth("", width)
	}
	right := segments[len(segments)-1]
	leftTokens := segments[:len(segments)-1]
	for len(leftTokens) > 1 {
		left := footerSep(leftTokens...)
		if lipgloss.Width(left)+lipgloss.Width(right)+1 <= width {
			return flexPinRight(left, right, width)
		}
		leftTokens = append(leftTokens[:len(leftTokens)-2], leftTokens[len(leftTokens)-1])
	}
	if len(leftTokens) == 0 {
		return flexPinRight("", right, width)
	}
	return flexPinRight(footerSep(leftTokens...), right, width)
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

// ── FLEX-FLOW STATUS METRICS (streaming-scroll decoupling) ─────────────
// Token counts use status.FormatTokens quantization (712, 1.2k, 14.8k) with
// fixed precision so numeric updates never reflow surrounding text
// (INVARIANT 3). Metrics render at natural width with tight " · "
// separators and zero trailing padding; the line never wraps mid-stream.
// The drop-proof "^C stop" interrupt badge anchors the exact right edge.

// stopBadge is the compact interrupt affordance that anchors the executing
// footer's right edge. It replaces the legacy "⏸ Ctrl+C interrupt" pair —
// one badge, both the hint and the escape hatch, and the LAST segment a
// width-aware executing footer ever drops (see footerDropToFit).
const stopBadge = "^C stop"

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
