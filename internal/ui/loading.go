package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/presentation"
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
// Returns "" when the dock has nothing truthful to say.
//
// The dock is the SINGLE active status indicator for the legacy agent/stream
// paths from prompt submit (t=0ms) through thinking/processing. On the gated
// RuntimeExecutor path it stays alive (spinner + tips) but its TEXT is the
// event-derived human step of the execution-view projection — never a static
// dispatch template — so nothing is claimed until a real execution event
// arrives.
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
	if dockText != "" {
		b.WriteString("  " + shimmer.Render(ansi.Strip(dockText), m.shimmerAnim.Frame, m.shimmerAnim.Width))
	} else {
		// No event-derived step yet (pre-first-event, or a conversation): the
		// animated spinner renders alone with NO text claim — progress is never
		// fabricated before a real runtime event exists.
		b.WriteString("  " + ansi.Strip(flake))
	}
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
// snowflake character. It is derived from AUTHORITATIVE execution signals only:
// the execution-view projection (event-derived human step), the runtime stage
// record (stage.go), or — on the legacy agent/stream paths — the shimmer text
// set by startShimmer. When no authoritative signal exists it returns "" so the
// dock renders nothing: a static "Working..." placeholder would be a fake
// progress claim.
//
// The returned text is ALWAYS plain (ANSI-free): the shimmer sweep re-colours
// every rune independently, so embedding a lipgloss-styled segment here would
// corrupt its escape sequence into a visible "[38;2;..m" leak (see
// renderLoadingDock). The hint is therefore plain text carried on the same
// swept line.
func (m *model) composeDockTextWithFlake(flake string) string {
	// The gated RuntimeExecutor path renders its status EXCLUSIVELY from the
	// single execution-view projection (Part 5): the human step the runtime
	// events produced — "Reading index.html", "Analyzing", "Applying changes".
	// The UI never invents execution truth. Gated on the in-flight marker so a
	// later legacy operation can never inherit a stale execution step.
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
	return ""
}

// composeDockText builds the dynamic status text using the default snowflake.
func (m *model) composeDockText() string {
	return m.composeDockTextWithFlake(SpinnerSnowflake())
}

// renderExecutionNarrative renders the Claude-like human narrative panel of the
// gated RuntimeExecutor execution. It is derived EXCLUSIVELY from the
// execution-view projection (ExecutionViewState + ExecutionNarrative) — the UI
// never authors progress text and never surfaces raw machine events here. It
// returns "" when no gated execution is in flight.
//
// Shape:
//
//	✓ Understanding request
//	✓ Inspecting index.html
//	◇ Preparing change          ← current step
func (m *model) renderExecutionNarrative() string {
	if m.execView == nil || !m.executionResolving || !m.execView.Active() {
		return ""
	}
	return renderExecutionFrame(m.execView.Frame(presentation.VisibilityNormal))
}

// renderExecutionLayered renders the gated execution panel for the ACTIVE
// visibility layer (Normal / Expanded / Debug). The renderer is a pure
// formatting function of the presentation-computed ExecutionFrame — it never
// decides what belongs in a layer.
func (m *model) renderExecutionLayered() string {
	if m.execView == nil || !m.executionResolving || !m.execView.Active() {
		return ""
	}
	return renderExecutionFrame(m.execView.Frame(m.execVisibility))
}

// renderExecutionFrame is the pure visual formatter of an ExecutionFrame. It
// contains no interpretation: it renders exactly what the presentation layer
// put into the frame.
//
// NORMAL: human narrative milestones + the live current step.
// EXPANDED: NORMAL + runtime metadata (strategy, context, model, tokens,
// duration, artifacts).
// DEBUG: EXPANDED + the full machine event stream.
func renderExecutionFrame(f presentation.ExecutionFrame) string {
	steps := f.Steps
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	last := len(steps) - 1
	for i, step := range steps {
		if step.Current {
			b.WriteString("  " + orangeStyle.Render("◇") + " " + brightStyle.Render(step.Sentence))
		} else {
			b.WriteString("  " + infoStyle.Render(Icon.Success+" "+step.Sentence))
		}
		b.WriteString("\n")
		// Every narrative step carries its derivation source (the ExecutionGraph
		// transition that produced it). The source sub-line is surfaced in the
		// EXPANDED/DEBUG layers — it proves the step is event-derived, never a
		// static template. NORMAL keeps the human milestones clean.
		if f.Visibility >= presentation.VisibilityExpanded && step.Transition != "" {
			b.WriteString("     " + mutedStyle.Render("source: "+step.Transition) + "\n")
		}
		if i == last {
			break
		}
	}
	if f.Visibility >= presentation.VisibilityExpanded {
		if detail := renderExecutionDetails(f.Details); detail != "" {
			b.WriteString(detail)
		}
	}
	if f.Visibility >= presentation.VisibilityDebug {
		b.WriteString(renderExecutionDebug(f.Events))
	}
	return b.String()
}

// renderExecutionDetails renders the EXPANDED-layer runtime metadata. It is
// visual formatting of the accumulated details only.
func renderExecutionDetails(d presentation.ExecutionDetails) string {
	if d.Empty() && d.Duration() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(dimmedStyle.Render("  ── execution details ──") + "\n")
	if d.Strategy != "" {
		b.WriteString("  " + dimmedStyle.Render("strategy:") + " " + textStyle.Render(d.Strategy) + "\n")
	}
	if len(d.ContextChannels) > 0 {
		policy := strings.Join(d.ContextChannels, ", ")
		b.WriteString("  " + dimmedStyle.Render("context policy:") + " " + textStyle.Render(policy))
		if d.ContextTokens > 0 {
			b.WriteString(" " + mutedStyle.Render(fmt.Sprintf("(~%d tok)", d.ContextTokens)))
		}
		b.WriteString("\n")
	}
	if d.Model != "" {
		b.WriteString("  " + dimmedStyle.Render("model:") + " " + textStyle.Render(d.Model) + "\n")
	}
	if d.TokenInput > 0 || d.TokenOutput > 0 {
		b.WriteString("  " + dimmedStyle.Render("tokens:") + " " + mutedStyle.Render(
			fmt.Sprintf("%d in / %d out", d.TokenInput, d.TokenOutput)) + "\n")
	}
	if dur := d.Duration(); dur > 0 {
		b.WriteString("  " + dimmedStyle.Render("duration:") + " " + mutedStyle.Render(dur.Round(time.Millisecond).String()) + "\n")
	}
	for _, a := range d.Artifacts {
		b.WriteString("  " + dimmedStyle.Render("artifact:") + " " + renderArtifactSummary(a) + "\n")
	}
	return b.String()
}

// renderArtifactSummary renders one artifact through the semantic renderer and
// collapses it to a single summary line. Structured artifacts (plans) never
// render as raw JSON.
func renderArtifactSummary(a presentation.ArtifactView) string {
	lines := presentation.RenderArtifact(a.Kind, a.Target, a.Content)
	if len(lines) == 0 {
		return a.Kind
	}
	return strings.Join(lines, " ")
}

// renderExecutionDebug renders the DEBUG-layer machine event stream.
func renderExecutionDebug(events []string) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(dimmedStyle.Render("  ── runtime events ──") + "\n")
	for _, e := range events {
		b.WriteString("  " + mutedStyle.Render(e) + "\n")
	}
	return b.String()
}
