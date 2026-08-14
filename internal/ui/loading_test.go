package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
)

func TestStartShimmerActivates(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")

	if !m.shimmerActive {
		t.Fatal("startShimmer did not set shimmerActive")
	}
	if m.shimmerText != "Thinking..." {
		t.Fatalf("shimmerText = %q, want %q", m.shimmerText, "Thinking...")
	}
	if !m.shimmerAnim.Active {
		t.Fatal("startShimmer left the component inactive")
	}
	if m.loadingTip == "" {
		t.Fatal("startShimmer did not select a contextual tip")
	}
}

func TestStartShimmerIdempotentForSameText(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	firstTip := m.loadingTip
	m.startShimmer("Thinking...", "analyze")
	if m.loadingTip != firstTip {
		t.Fatalf("restarting the same text re-rolled the tip: %q -> %q", firstTip, m.loadingTip)
	}
}

func TestStartShimmerLazyTipProvider(t *testing.T) {
	m := newTestModel()
	if m.tipProvider != nil {
		t.Fatal("test model unexpectedly has a tip provider")
	}
	m.startShimmer("Synthesizing plan...", "plan")
	if m.tipProvider == nil {
		t.Fatal("startShimmer did not lazily construct the tip provider")
	}
}

func TestStopShimmerClears(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.stopShimmer()

	if m.shimmerActive {
		t.Fatal("stopShimmer left shimmerActive set")
	}
	if m.shimmerText != "" || m.loadingTip != "" {
		t.Fatalf("stopShimmer did not clear text/tip: %q / %q", m.shimmerText, m.loadingTip)
	}
	if m.shimmerAnim.Active {
		t.Fatal("stopShimmer left the component active")
	}
}

func TestRenderLoadingDock(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "execute")

	dock := m.renderLoadingDock()
	plain := stripANSITest(dock)
	if !strings.Contains(plain, "Thinking") {
		t.Fatalf("dock missing shimmer text: %q", dock)
	}
	if !strings.Contains(plain, "✻") {
		t.Fatalf("dock missing snowflake icon: %q", dock)
	}
	if !strings.Contains(dock, "Tip:") {
		t.Fatalf("dock missing tip prefix: %q", dock)
	}
	if !strings.Contains(dock, "└") {
		t.Fatalf("dock missing tree-branch prefix: %q", dock)
	}
	if !strings.Contains(dock, "\n") {
		t.Fatal("dock must place the tip on the line below the shimmer")
	}
}

func TestRenderLoadingDockInactive(t *testing.T) {
	m := newTestModel()
	if dock := m.renderLoadingDock(); dock != "" {
		t.Fatalf("inactive dock rendered %q, want empty", dock)
	}
}

// TestLoadingDockNoANSILeak guards the raw-ANSI leak reported during thinking
// streams. The shimmer sweep re-colours every rune of the dock text, so any
// pre-styled (ANSI-carrying) segment embedded in the sweep text gets its leading
// ESC byte swallowed by the adjacent per-rune colour code — leaving the bare SGR
// parameters ("[38;2;88;91;112m[Ctrl+O to expand][0m") visible as literal text
// on screen. composeDockTextWithFlake must therefore emit plain text, and
// renderLoadingDock must strip any stray escape sequence before the sweep.
func TestLoadingDockNoANSILeak(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Waiting for model...", "analyze")
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	// The sweep text must be plain — zero escape sequences.
	got := m.composeDockTextWithFlake("✻")
	if strings.Contains(got, "\x1b") {
		t.Fatalf("dock sweep text contains raw ANSI: %q", got)
	}
	if !strings.Contains(got, "waiting") {
		t.Fatalf("dock missing the truthful waiting state: %q", got)
	}

	// The rendered dock must never leak bare SGR parameters as literal text.
	// Valid per-rune colour codes from the sweep legitimately contain "38;2;"
	// inside escape sequences, so the leak is asserted only AFTER stripping —
	// a corrupted embedded sequence survives stripping because its ESC was
	// already consumed by the adjacent per-rune code.
	plain := stripANSITest(m.renderLoadingDock())
	if strings.Contains(plain, "38;2;") || strings.Contains(plain, "[0m") {
		t.Fatalf("loading dock leaks escape parameters after strip: %q", plain)
	}
}

// TestLoadingDockANSIStripDefendsSweep is the mechanism guard behind the leak
// fix: the dock path strips ANSI from the sweep text BEFORE shimmer re-colours
// every rune. It feeds the exact real-TTY input that used to corrupt (a styled
// "[Ctrl+O to expand]" segment) and asserts the strip+shimmer pipeline emits
// no bare SGR parameters — while also proving the un-stripped path WOULD leak,
// so the guard cannot silently rot.
func TestLoadingDockANSIStripDefendsSweep(t *testing.T) {
	// Real-TTY output of dimmedStyle.Render("[Ctrl+O to expand]") — the SGR
	// sequence that shimmer's per-rune re-colouring corrupted into visible text.
	embedded := "\x1b[38;2;88;91;112m[Ctrl+O to expand]\x1b[0m"
	dockText := "✻ Thinking... (3s)  " + embedded

	// Production path: renderLoadingDock strips before the sweep. No leak.
	sweep := shimmer.Render(ansi.Strip(dockText), 3, 0)
	if plain := stripANSITest(sweep); strings.Contains(plain, "38;2;") || strings.Contains(plain, "[0m") {
		t.Fatalf("stripped sweep leaked SGR parameters: %q", plain)
	}

	// Regression: WITHOUT the strip the same input leaks bare parameters —
	// proving this test actually guards the guard.
	raw := shimmer.Render(dockText, 3, 0)
	if plain := stripANSITest(raw); !strings.Contains(plain, "38;2;88;91;112m") {
		t.Fatalf("raw sweep did not expose the leak (guard no longer meaningful): %q", plain)
	}
}

func TestShimmerFrameAdvancesAndSchedules(t *testing.T) {
	m := newTestModel()
	m.agentRunning = true
	m.startShimmer("Thinking...", "analyze")
	m.shimmerAnim.Frame = 0

	nm, cmd := m.Update(shimmer.FrameMsg{})
	m2 := nm.(*model)

	if m2.shimmerAnim.Frame != 1 {
		t.Fatalf("shimmer frame = %d, want 1", m2.shimmerAnim.Frame)
	}
	if cmd == nil {
		t.Fatal("active shimmer must re-schedule the next frame")
	}
}

// stripANSITest removes ANSI escape sequences for plain-text assertions.
func stripANSITest(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestShimmerFrameStopsWhenInactive(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.stopShimmer()
	m.shimmerAnim.Frame = 0

	nm, cmd := m.Update(shimmer.FrameMsg{})
	m2 := nm.(*model)

	if m2.shimmerAnim.Frame != 0 {
		t.Fatalf("inactive shimmer advanced frame: %d", m2.shimmerAnim.Frame)
	}
	if cmd != nil {
		t.Fatal("inactive shimmer must not re-schedule the tick loop")
	}
}

func TestShimmerFrameSelfStopsWhenNoProducer(t *testing.T) {
	// Safety net: if the terminal handler forgot to call stopShimmer, the
	// frame handler must self-stop once every background producer releases
	// its flags.
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.streaming = false
	m.agentRunning = false
	m.reviewRunning = false
	m.pipelineRunning = false
	m.planPending = false

	nm, cmd := m.Update(shimmer.FrameMsg{})
	m2 := nm.(*model)

	if m2.shimmerActive {
		t.Fatal("shimmer did not self-stop with no owning producer")
	}
	if cmd != nil {
		t.Fatal("self-stopped shimmer must not re-schedule the tick loop")
	}
}

func TestShimmerFrameKeepsRunningWhileProducerActive(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.streaming = true

	nm, cmd := m.Update(shimmer.FrameMsg{})
	m2 := nm.(*model)

	if !m2.shimmerActive {
		t.Fatal("shimmer self-stopped while a producer still owns the flags")
	}
	if cmd == nil {
		t.Fatal("shimmer must keep ticking while the producer is active")
	}
}

func TestFirstTokenSmoothClearsShimmer(t *testing.T) {
	// Requirement: when the LLM starts streaming tokens, gracefully stop the
	// shimmer tick loop and replace the tip line with the streaming output.
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.streamCh = nil
	m.currentPrompt = "hello"
	m.startShimmer("Thinking...", "analyze")

	nm, _ := m.Update(tokenMsg("Hi there!"))
	m2 := nm.(*model)

	if m2.shimmerActive {
		t.Fatal("first content token did not stop the shimmer")
	}
	if m2.shimmerText != "" || m2.loadingTip != "" {
		t.Fatalf("first token left stale loading text/tip: %q / %q", m2.shimmerText, m2.loadingTip)
	}
}

func TestStrategyHintDirectChat(t *testing.T) {
	m := newTestModel()
	m.currentPrompt = "hi how are you doing today"
	if got := m.strategyHint(); got != "direct_chat" {
		t.Fatalf("strategyHint(casual) = %q, want direct_chat", got)
	}
	m.currentPrompt = "fix the bug in main.go"
	if got := m.strategyHint(); got != "" {
		t.Fatalf("strategyHint(code task) = %q, want empty", got)
	}
}

func TestShimmerPhaseForAgentLabel(t *testing.T) {
	cases := map[string]string{
		"synthesizing plan": "plan",
		"blueprinting":      "plan",
		"building":          "execute",
		"hotfix":            "execute",
		"shell exec":        "execute",
		"reviewing":         "validate",
		"testing":           "validate",
		"analyzing trace":   "analyze",
		"investigating":     "analyze",
		"unknown label":     "analyze",
	}
	for in, want := range cases {
		if got := shimmerPhaseForAgentLabel(in); got != want {
			t.Errorf("shimmerPhaseForAgentLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentShimmerText(t *testing.T) {
	cases := map[string]string{
		"synthesizing plan":   "Synthesizing plan...",
		"building":            "Executing strategy...",
		"hotfix":              "Applying hotfix...",
		"reviewing":           "Reviewing...",
		"$log trace analysis": "Analyzing trace...",
		"custom agent":        "custom agent...",
		"":                    "Working...",
	}
	for in, want := range cases {
		if got := agentShimmerText(in); got != want {
			t.Errorf("agentShimmerText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComposeDockTextStatic(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Executing strategy...", "execute")
	got := m.composeDockText()
	if !strings.Contains(got, "✻") {
		t.Errorf("composeDockText missing snowflake: %q", got)
	}
	if !strings.Contains(got, "Executing strategy...") {
		t.Errorf("composeDockText missing shimmer text: %q", got)
	}
}

func TestComposeDockTextProviderWaiting(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Waiting for model...", "analyze")
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	got := m.composeDockText()
	if !strings.Contains(got, "✻") {
		t.Errorf("composeDockText missing snowflake: %q", got)
	}
	// The provider wait must render as a truthful waiting state with an
	// elapsed time — never as "Thinking...".
	if !strings.Contains(got, "waiting") {
		t.Errorf("composeDockText missing waiting state: %q", got)
	}
	if strings.Contains(got, "Thinking") {
		t.Errorf("composeDockText claims the model is thinking: %q", got)
	}
}

func TestComposeDockTextProviderStreaming(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Executing strategy...", "execute")
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 921)

	got := m.composeDockText()
	if !strings.Contains(got, "streaming") {
		t.Errorf("composeDockText missing streaming state: %q", got)
	}
	if !strings.Contains(got, "921") {
		t.Errorf("composeDockText missing real token count: %q", got)
	}
}

func TestComposeDockTextNoFabricatedStage(t *testing.T) {
	// No execution event has occurred — the dock must fall back to the
	// caller-supplied shimmer text and MUST NOT fabricate a stage.
	m := newTestModel()
	m.startShimmer("Executing strategy...", "execute")

	got := m.composeDockText()
	if !strings.Contains(got, "Executing strategy...") {
		t.Errorf("composeDockText missing shimmer fallback: %q", got)
	}
	if strings.Contains(got, "waiting") || strings.Contains(got, "streaming") {
		t.Errorf("composeDockText fabricated a provider stage with no execution event: %q", got)
	}
}

func TestComposeDockTextFallback(t *testing.T) {
	m := newTestModel()
	got := m.composeDockText()
	if got != "✻ Working..." {
		t.Errorf("composeDockText fallback = %q, want '✻ Working...'", got)
	}
}

// TestLoadingDockTruthfulNoThinking guards execution-truthful progress: while
// the loading dock is live the viewport MUST NOT render any "Thinking..." claim
// — the dock derives its status from the authoritative execution stage, and the
// reasoning drawer only appears after the dock hands off to streaming.
func TestLoadingDockTruthfulNoThinking(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("analyzing code structure")
	m.startShimmer("Synthesizing plan...", "plan")

	m.refreshViewportContent()
	dockView := m.Viewport.View()

	if !strings.Contains(dockView, "Tip:") {
		t.Fatalf("dock-active view missing the tip line: %q", dockView)
	}
	// No "Thinking..." progress claim while the dock is live.
	if n := strings.Count(stripANSITest(dockView), "Thinking"); n != 0 {
		t.Fatalf("dock-active view shows %d fake Thinking lines, want 0 (progress must be truthful): %q", n, dockView)
	}

	// Hand off to streaming: the reasoning drawer becomes the sole thinking
	// line (explicit inspection, not a progress claim).
	m.stopShimmer()
	m.refreshViewportContent()
	streamView := m.Viewport.View()

	if strings.Contains(streamView, "Tip:") {
		t.Fatalf("dock must disappear cleanly on first token: %q", streamView)
	}
	if n := strings.Count(stripANSITest(streamView), "Thinking"); n != 1 {
		t.Fatalf("handoff view shows %d thinking lines, want exactly 1 (inline drawer only): %q", n, streamView)
	}
}

// TestLoadingDockExpandedInlineWhileActive guards the Ctrl+O inspection
// override: even while the loading dock is live, expanding the ThinkingBuffer
// must render the full faint reasoning box in the viewport body immediately.
func TestLoadingDockExpandedInlineWhileActive(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("secret reasoning content")
	m.thinkingBuffer.SetExpanded(true)
	m.startShimmer("Synthesizing plan...", "plan")

	m.refreshViewportContent()
	view := m.Viewport.View()
	if !strings.Contains(view, "secret reasoning content") {
		t.Fatalf("expanded inline reasoning missing while dock active: %q", view)
	}
	if !strings.Contains(view, "Tip:") {
		t.Fatalf("dock should remain visible while expanded box is inspected: %q", view)
	}
}
