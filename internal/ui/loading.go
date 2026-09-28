package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/presentation"
	"github.com/PizenLabs/izen/internal/tui/components/shimmer"
	"github.com/PizenLabs/izen/internal/tui/tips"
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
// It does NOT advance the spinner frame. The snowflake used to cycle on this
// tick; it now rides the decoupled 100ms animation ticker (spinner.go), which
// has exactly one writer. This loop is a RENDER of the animation, and a second
// writer at a second rate is what the decoupling removed — the shimmer's own
// cadence already matches, so nothing is lost by sharing.
//
// UNIFIED TICK RATE: the frame is produced directly (not via shimmer.Tick) so
// every animation loop in the UI — shimmer, braille spinner, snowflake, the
// pre-execution skeleton — runs on the same ~100ms cadence regardless of
// provider or mode.
//
// A mounted pre-execution skeleton keeps the loop alive on its own: it is a
// transient indicator that can be mounted with no loading dock at all (a code
// block being formatted on a quiet stream), and a frozen indicator reads as a
// stall. Returns nil only when neither surface is animating, so the loop still
// self-terminates with no leaked timer.
func (m *model) shimmerTickCmd() tea.Cmd {
	if !m.shimmerActive && !m.skeletonActive() {
		return nil
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return shimmer.FrameMsg{}
	})
}

// advanceAnimationFrame is the ONE animation step of the master ~30 FPS frame
// tick, and it is called from the very top of the FrameTickMsg handler — before
// any of that handler's returns — so the frame counter is strictly monotonic
// across the whole holdback window.
//
// Both animated surfaces in the conversation thread read their frame from here:
//
//   - the mounted pre-execution skeleton: the emerald sine wave across its one
//     row, and the braille glyph that leads it (components.SpinnerGlyph /
//     animation.Render, both pure functions of the frame);
//   - the dock's own shimmer sweep, which is re-seated onto this same cadence so
//     the two can never show a glyph moving over a frozen gradient.
//
// The counter is a uint64 that only ever increments, so a surface that joins
// late (a mount) and a surface that leaves (a release) cannot rewind anything
// else: that is what makes "the wave never stops" a property of the loop rather
// than of any particular mount's lifetime.
func (m *model) advanceAnimationFrame() {
	m.frame++
	// The skeleton rides the master counter, so its wave and glyph advance in
	// lockstep with everything else on the tick. Seeding from m.frame (rather
	// than a private counter) is what keeps the two from drifting apart.
	m.skeletonFrame = m.frame
	if m.skeleton != nil {
		m.skeleton.SetFrame(m.skeletonFrame)
	}
	// Re-seat the dock sweep on the master cadence. Guarded on Active so an
	// inactive shimmer is not silently resurrected: stopShimmer clears the
	// flag, and renderLoadingDock renders nothing when it is clear.
	if m.shimmerAnim.Active {
		m.shimmerAnim.Frame = int(m.frame)
	}
}

// syncShimmerWidth keeps the sweep span aligned with the current pane width.
// It is called from the resize handler and from startShimmer, never from the
// render path, so View() stays a pure projection.
func (m *model) syncShimmerWidth() {
	m.shimmerAnim.Width = max(0, m.PaneWidth()-4)
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
// the runtime stage record (stage.go), or — on the legacy agent/stream paths —
// the shimmer text set by startShimmer. When no authoritative signal exists it
// returns "" so the dock renders nothing: a static "Working..." placeholder
// would be a fake progress claim.
//
// ── ONE owner for the current execution step ──────────────────────────────
// When the execution narrative panel is mounted it is the single canonical
// main-UI representation of what the execution is doing right now: it is
// derived from the same event projection, it carries the full milestone list,
// and it is rendered immediately below this dock. Restating its current step
// here — or restating the runtime stage under a second wording, e.g. "Model ●
// streaming" beside the panel's "Model responding" — would put two claims
// about ONE execution state in a single frame. Two surfaces that can disagree
// are two sources of truth, and neither is authoritative.
//
// So while the panel is mounted the dock keeps only what it uniquely owns: the
// animated glyph and the contextual tip. The stage line returns once no panel
// is mounted (the legacy agent/stream paths), which is exactly when it is the
// only surface with a claim to make.
//
// The returned text is ALWAYS plain (ANSI-free): the shimmer sweep re-colours
// every rune independently, so embedding a lipgloss-styled segment here would
// corrupt its escape sequence into a visible "[38;2;..m" leak (see
// renderLoadingDock). The hint is therefore plain text carried on the same
// swept line.
func (m *model) composeDockTextWithFlake(flake string) string {
	// The execution narrative panel owns the current step while it is mounted.
	if m.loadingDockActive() {
		return ""
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

// loadingDockActive reports whether the canonical in-flight execution line is
// currently rendered somewhere in the viewport. The execution narrative panel is
// that surface: it is derived from real runtime events, so an empty HumanStep
// means there is no authoritative in-flight state to restate — and the dock
// must not invent one.
func (m *model) loadingDockActive() bool {
	return m.execView != nil && m.executionResolving && m.execView.Active() && m.execView.HumanStep() != ""
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
// NORMAL: human narrative milestones + the live current step + the artifact
// ledger (which targets exist, which are being worked on, what actually
// changed).
// EXPANDED: NORMAL + runtime metadata (strategy, context layers, model,
// provider invocation state, tokens, duration).
// DEBUG: EXPANDED + the full machine event stream.
func renderExecutionFrame(f presentation.ExecutionFrame) string {
	steps := f.Steps
	ledger := renderArtifactLedger(f.State.Details)
	if len(steps) == 0 && ledger == "" {
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
	b.WriteString(ledger)
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

// renderArtifactLedger renders the artifact-centric execution view: which
// targets the execution is working on, which are still pending, and what the
// mutation boundary actually changed.
//
// It is the ANSWER to "what is Izen doing, on what artifact, and what
// changed" — the questions a mutation execution is actually asked. It replaces
// a stream of generic model-activity lines with a per-artifact ledger.
//
// Truthfulness rules enforced here:
//
//   - A target is listed only because a canonical runtime event announced it
//     (mutation.started / artifact.produced). Nothing is pre-listed.
//   - "mutated" is rendered only for boundary evidence that proves the apply
//     ran AND the content actually changed.
//   - Diff statistics are rendered ONLY for a target the boundary compiled a
//     real diff for. A target without a diff gets no numbers at all — never
//     "+0 -0", which would read as a measured empty diff.
func renderArtifactLedger(d presentation.ExecutionDetails) string {
	if len(d.Targets) == 0 {
		return ""
	}
	var b strings.Builder
	// Header names the phase the ledger is in, derived from the observed state.
	switch {
	case d.MutatedFiles > 0:
		b.WriteString(dimmedStyle.Render("  ── mutation ──") + "\n")
	case d.CandidateCount > 0:
		b.WriteString(dimmedStyle.Render("  ── generating ──") + "\n")
	}
	for _, t := range d.Targets {
		b.WriteString("  " + renderTargetLedgerRow(t) + "\n")
	}
	if d.MutatedFiles > 0 {
		b.WriteString("  " + greenStyle.Render(Icon.Success) + " " +
			mutedStyle.Render(fmt.Sprintf("%d file(s) updated", d.MutatedFiles)) + "\n")
	}
	return b.String()
}

// renderTargetLedgerRow renders one target of the ledger.
func renderTargetLedgerRow(t presentation.TargetEvidence) string {
	name := mutedStyle.Render(t.Target)
	switch {
	case t.Mutated():
		// A real filesystem change. The diff numbers, when present, are the
		// measured compiled-diff line counts from the apply boundary.
		row := greenStyle.Render(Icon.Success) + " " + name
		if t.DiffPresent {
			row += " " + greenStyle.Render(fmt.Sprintf("+%d", t.DiffAdds)) +
				" " + redStyle.Render(fmt.Sprintf("-%d", t.DiffRemoves))
		}
		return row
	case t.Outcome != "":
		// A terminal outcome that is not a filesystem change. The outcome is the
		// runtime's own vocabulary — never restated as a success.
		return mutedStyle.Render(Icon.Pending) + " " + name + " " + mutedStyle.Render("("+t.Outcome+")")
	case t.Candidate:
		// A candidate exists for this target and the mutation has not landed.
		return orangeStyle.Render("●") + " " + name
	default:
		// Announced by the mutation boundary, no candidate and no outcome yet.
		return mutedStyle.Render("○") + " " + name
	}
}

// renderExecutionDetails renders the EXPANDED-layer runtime metadata. It is
// visual formatting of the accumulated details only.
//
// Every token number is LAYER-LABELLED. The four token quantities in an
// execution are different measurements of different things, and conflating
// them is how a user ends up inferring that a context estimate is a billing
// figure:
//
//	compiled context  ~chars/4 of the assembled prompt the compiler sent
//	provider prompt   tokens the provider reported reading
//	provider output   tokens the provider reported writing
//	reasoning         tokens the provider attributed to reasoning
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
		b.WriteString("  " + dimmedStyle.Render("context channels:") + " " + textStyle.Render(policy) + "\n")
	}
	if d.ContextTokens > 0 {
		reuse := ""
		if d.ContextCacheHit {
			reuse = " " + mutedStyle.Render("(reused from cache)")
		}
		b.WriteString("  " + dimmedStyle.Render("compiled context (est.):") + " " +
			mutedStyle.Render(fmt.Sprintf("~%d tok", d.ContextTokens)) + reuse + "\n")
	}
	if d.Model != "" {
		b.WriteString("  " + dimmedStyle.Render("model:") + " " + textStyle.Render(d.Model))
		if d.ProviderState != "" {
			b.WriteString(" " + mutedStyle.Render("(invocation "+d.ProviderState+")"))
		}
		b.WriteString("\n")
	}
	if d.ProviderCalls > 0 {
		b.WriteString("  " + dimmedStyle.Render("provider calls:") + " " + mutedStyle.Render(fmt.Sprintf("%d", d.ProviderCalls)) + "\n")
	}
	if d.TokenInput > 0 || d.TokenOutput > 0 {
		b.WriteString("  " + dimmedStyle.Render("provider tokens:") + " " + mutedStyle.Render(
			fmt.Sprintf("%d prompt / %d completion", d.TokenInput, d.TokenOutput)) + "\n")
	}
	if d.FinishReason != "" {
		b.WriteString("  " + dimmedStyle.Render("finish reason:") + " " + mutedStyle.Render(d.FinishReason) + "\n")
	}
	if d.ReasoningDuration > 0 || d.ReasoningTokens > 0 {
		b.WriteString("  " + dimmedStyle.Render("reasoning (telemetry):") + " " + mutedStyle.Render(
			fmt.Sprintf("%s (%d tok)", d.ReasoningDuration.Round(time.Millisecond), d.ReasoningTokens)) + "\n")
	}
	if d.VerificationRan {
		verdict := greenStyle.Render("passed")
		if !d.VerificationPassed {
			verdict = redStyle.Render("failed")
		}
		steps := ""
		if len(d.VerificationSteps) > 0 {
			steps = " " + mutedStyle.Render("["+strings.Join(d.VerificationSteps, ", ")+"]")
		}
		b.WriteString("  " + dimmedStyle.Render("verification:") + " " + verdict + steps + "\n")
	}
	if d.EvidenceObserved {
		verdict := greenStyle.Render(d.EvidenceOutcome)
		if d.EvidenceTainted {
			verdict = redStyle.Render(d.EvidenceOutcome + " (tainted)")
		}
		b.WriteString("  " + dimmedStyle.Render("sealed evidence:") + " " + verdict + "\n")
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
