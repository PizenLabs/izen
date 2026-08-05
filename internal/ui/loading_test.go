package ui

import (
	"strings"
	"testing"

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

func TestComposeDockTextThinking(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("analyzing code structure")

	got := m.composeDockText()
	if !strings.Contains(got, "✻") {
		t.Errorf("composeDockText missing snowflake: %q", got)
	}
	if !strings.Contains(got, "Thinking...") {
		t.Errorf("composeDockText missing thinking text: %q", got)
	}
	// Elapsed time should be present
	if !strings.Contains(got, "s)") {
		t.Errorf("composeDockText missing elapsed time: %q", got)
	}
}

func TestComposeDockTextThinkingComplete(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Thinking...", "analyze")
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("done reasoning")
	m.thinkingBuffer.MarkComplete()

	got := m.composeDockText()
	// When thinking is complete, should fall back to static shimmer text
	if !strings.Contains(got, "✻ Thinking...") {
		t.Errorf("composeDockText should use static text after thinking complete: %q", got)
	}
}

func TestComposeDockTextFallback(t *testing.T) {
	m := newTestModel()
	got := m.composeDockText()
	if got != "✻ Working..." {
		t.Errorf("composeDockText fallback = %q, want '✻ Working...'", got)
	}
}

// TestLoadingDockSingleSourceNoDuplicateThinking guards the single-source-of-
// truth lifecycle: while the bottom loading dock is active the viewport body
// must show EXACTLY ONE thinking indicator (the dock's "✻ Thinking... (Xs)"
// line) — the collapsed inline one-liner is suppressed so the user never sees
// two stacked "Thinking…" lines. On the first content token the dock hands off
// cleanly and the inline indicator becomes the sole thinking line.
func TestLoadingDockSingleSourceNoDuplicateThinking(t *testing.T) {
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
	if n := strings.Count(stripANSITest(dockView), "Thinking"); n != 1 {
		t.Fatalf("dock-active view shows %d thinking lines, want exactly 1 (dock only, no ghost inline duplicate): %q", n, dockView)
	}

	// First primary content token → dock hands off; inline thinking is the
	// sole live indicator now.
	m.stopShimmer()
	m.refreshViewportContent()
	streamView := m.Viewport.View()

	if strings.Contains(streamView, "Tip:") {
		t.Fatalf("dock must disappear cleanly on first token: %q", streamView)
	}
	if n := strings.Count(stripANSITest(streamView), "Thinking"); n != 1 {
		t.Fatalf("handoff view shows %d thinking lines, want exactly 1 (inline only): %q", n, streamView)
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
