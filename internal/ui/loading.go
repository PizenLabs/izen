package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
	"github.com/PizenLabs/izen/pkg/tui/tips"
)

// shimmerFrameMsg is the shimmer component's animation tick. It is aliased so
// the model's update switch can forward it into the component while keeping
// the loop gated on the model's own lifecycle flag.
type shimmerFrameMsg = shimmer.FrameMsg

// shimmerTickCmd schedules the next 50ms shimmer animation frame. It returns
// nil when the shimmer is inactive, so the tick loop self-terminates the
// moment streaming output begins or a background producer completes (smooth
// clearing with no leaked goroutine).
func (m *model) shimmerTickCmd() tea.Cmd {
	if !m.shimmerActive {
		return nil
	}
	return shimmer.Tick()
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

// renderLoadingDock renders the shimmer loading line plus the contextual tip
// directly below it, with a tree-branch prefix. It is drawn above the input
// separator while any background producer is running and no streaming output
// has replaced it yet. Returns "" when the shimmer is inactive.
func (m *model) renderLoadingDock() string {
	if !m.shimmerActive {
		return ""
	}

	var b strings.Builder
	b.WriteString("  " + SpinnerStyle.Render(ProposalSpinnerFrames[m.spinnerFrame%len(ProposalSpinnerFrames)]))
	b.WriteString(" " + m.shimmerAnim.View() + "\n")
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
