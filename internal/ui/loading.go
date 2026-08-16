package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
	"github.com/PizenLabs/izen/pkg/tui/tips"
)

// shimmerFrameMsg is the shimmer component's animation tick. It is aliased so
// the model's update switch can forward it into the component while keeping
// the loop gated on the model's own lifecycle flag.
type shimmerFrameMsg = shimmer.FrameMsg

// shimmerTickCmd schedules the next ~100ms shimmer animation frame. It returns
// nil when the shimmer is inactive, so the tick loop self-terminates the
// moment streaming output begins or a background producer completes (smooth
// clearing with no leaked goroutine).
//
// This also advances the spinner frame so the animated snowflake character
// (✻ ❅ ❆ ✦) cycles on the shimmer tick cadence, keeping the snowflake
// animation in sync with the shimmer sweep.
//
// UNIFIED TICK RATE: the frame is produced directly (not via shimmer.Tick) so
// every animation loop in the UI — shimmer, braille spinner, snowflake — runs
// on the same ~100ms cadence regardless of provider or mode.
func (m *model) shimmerTickCmd() tea.Cmd {
	if !m.shimmerActive {
		return nil
	}
	m.spinnerFrame++
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return shimmer.FrameMsg{}
	})
}

// syncShimmerWidth keeps the sweep span aligned with the current pane width.
// It is called from the resize handler and from startShimmer, never from the
// render path, so View() stays a pure projection.
func (m *model) syncShimmerWidth() {
	m.shimmerAnim.Width = max(0, m.width-4)
}

// startShimmer activates the loading shimmer with the given status text and a
// contextual tip derived from the current phase. Re-starting the same text is
// a no-op so the animation frame never visibly resets mid-execution.
func (m *model) startShimmer(text, phase string) {
	if m.tipProvider == nil {
		m.tipProvider = tips.Default()
	}
	if m.shimmerActive && m.shimmerText == text {
		return
	}
	m.shimmerActive = true
	m.shimmerText = text
	m.shimmerAnim = shimmer.New(text)
	m.shimmerAnim.SetActive(true)
	m.syncShimmerWidth()
	m.loadingTip = m.tipProvider.TipForPhaseString(phase, m.strategyHint())
}

// stopShimmer deactivates the loading shimmer and clears the tip line. It is
// the "smooth clearing" seam: called when the first stream token arrives or
// when any background producer terminates, so the animated line is replaced by
// the streaming output. The tick loop stops itself on the next frame.
func (m *model) stopShimmer() {
	m.shimmerActive = false
	m.shimmerAnim.SetActive(false)
	m.shimmerText = ""
	m.loadingTip = ""
}

// strategyHint reports the active strategy name used to pick strategy-aware
// tips, when it can be determined cheaply. Conversational prompts route
// through the DirectChatStrategy (single-pass, no workspace scan); everything
// else returns "" and tips fall back to the phase bucket.
func (m *model) strategyHint() string {
	p := m.currentPrompt
	if p == "" {
		return ""
	}
	if gateway.IsCasualChat(p) {
		return tips.StrategyChat
	}
	return ""
}

// shimmerPhaseForAgentLabel maps an agent label / pipeline step onto the
// canonical tip phase so the contextual tip matches what the engine is doing.
func shimmerPhaseForAgentLabel(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "synthesizing plan", "blueprinting", "planning", "evaluating policy":
		return "plan"
	case "building", "hotfix", "hotfix apply", "shell exec", "patching",
		"template", "hybrid template", "stdlib patch", "fixing", "executing":
		return "execute"
	case "reviewing", "review+test", "testing", "verifying", "validating":
		return "validate"
	default:
		return "analyze"
	}
}

// agentShimmerText derives the loading status text from an agent label.
func agentShimmerText(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "synthesizing plan":
		return "Synthesizing plan..."
	case "evaluating policy":
		return "Evaluating policy..."
	case "building":
		return "Executing strategy..."
	case "hotfix", "hotfix apply":
		return "Applying hotfix..."
	case "shell exec":
		return "Executing command..."
	case "patching", "template", "hybrid template", "stdlib patch", "fixing":
		return "Generating patch..."
	case "testing", "review+test":
		return "Running tests..."
	case "reviewing":
		return "Reviewing..."
	case "investigating":
		return "Investigating..."
	case "$log trace analysis", "env diagnostics", "local slm diagnosis":
		return "Analyzing trace..."
	case "refining architectural idea":
		return "Refining idea..."
	default:
		if label == "" {
			return "Working..."
		}
		return label + "..."
	}
}

// renderLoadingDock renders the unified dynamic shimmer + thinking + tip bar.
// The snowflake icon cycles through the 4-frame animated sequence (✻ ❅ ❆ ✦)
// while the cosine shimmer sweep animates across the full status text. The
// contextual tip line sits directly underneath with a tree-branch prefix.
// Returns "" when the shimmer is inactive.
//
// The dock is the SINGLE active status indicator from prompt submit (t=0ms)
// through thinking/processing. It clears smoothly ONLY when the first
// primary output token arrives (tokenMsg handler calls stopShimmer).
//
// ANSI-LEAK HARDENING: the shimmer sweep re-colours every rune of the status
// text independently, so the sweep text MUST NEVER carry pre-styled (ANSI-
// carrying) segments. An embedded SGR sequence (e.g. dimmedStyle.Render(...))
// gets its leading ESC byte swallowed by the adjacent per-rune colour code,
// which leaves the bare parameters — "[38;2;88;91;112m[Ctrl+O to expand][0m" —
// visible as literal garbage on screen. composeDockTextWithFlake therefore
// emits plain text, and ansi.Strip is applied defensively before the sweep so
// no raw escape sequence can ever reach the viewport regardless of what a
// future caller injects.
func (m *model) renderLoadingDock() string {
	if !m.shimmerActive {
		return ""
	}

	// Compose the animated snowflake text: use the current flowing spinner
	// frame so the snowflake character cycles (✻ ❅ ❆ ✦) on the shimmer
	// tick cadence. The shimmer.Render sweep animates across this text.
	flake := flowingSpinnerFrames[m.spinnerFrame%len(flowingSpinnerFrames)]
	dockText := m.composeDockTextWithFlake(flake)

	var b strings.Builder
	b.WriteString("  " + shimmer.Render(ansi.Strip(dockText), m.shimmerAnim.Frame, m.shimmerAnim.Width))
	b.WriteString("\n")
	if m.loadingTip != "" {
		b.WriteString("  ")
		b.WriteString(subtleStyle.Render("└"))
		b.WriteString(" ")
		b.WriteString(orangeStyle.Render("Tip:"))
		b.WriteString(" ")
		b.WriteString(mutedStyle.Render(m.loadingTip))
		b.WriteString("\n")
	}
	return b.String()
}

// composeDockTextWithFlake builds the dynamic status text using the given
// snowflake character. It is derived from the AUTHORITATIVE execution stage
// (stage.go): the dock can only ever claim work the runtime actually reported —
// a provider wait renders as "waiting" (never "thinking"), a token stream as
// "streaming", a local stage as its canonical label. When no stage is active
// it falls back to the shimmer text set by startShimmer.
//
// The returned text is ALWAYS plain (ANSI-free): the shimmer sweep re-colours
// every rune independently, so embedding a lipgloss-styled segment here would
// corrupt its escape sequence into a visible "[38;2;..m" leak (see
// renderLoadingDock). The hint is therefore plain text carried on the same
// swept line.
func (m *model) composeDockTextWithFlake(flake string) string {
	// The gated RuntimeExecutor path renders its status EXCLUSIVELY from the
	// single execution-view projection (Part 5): the human step the runtime
	// events produced — "Thinking...", "Found target index.html",
	// "Generated change", "Applying...". The UI never invents execution truth.
	// Gated on the in-flight marker so a later legacy operation can never
	// inherit a stale execution step.
	if m.execView != nil && m.executionResolving && m.execView.Active() {
		if step := m.execView.HumanStep(); step != "" {
			return flake + " " + step
		}
	}
	if st := m.stageSnapshot(); st.active() {
		if line := renderStageStatus(st); line != "" {
			return flake + " " + line
		}
	}
	if m.shimmerText != "" {
		return flake + " " + m.shimmerText
	}
	return flake + " Working..."
}

// composeDockText builds the dynamic status text using the default snowflake.
func (m *model) composeDockText() string {
	return m.composeDockTextWithFlake(SpinnerSnowflake())
}
