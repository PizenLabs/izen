package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── PHASE 7 — EXECUTION UX CONTRACT ──────────────────────────────────────────
//
// The normal UI must render facts from the authoritative operation / stage /
// telemetry / mutation-evidence infrastructure — never a fabricated progress
// claim. These regression tests pin the Phase 7 invariants:
//
//   - the streaming indicator only ever shows AUTHORITATIVE provider-reported
//     token counts (a character-count estimate is never presented as tokens),
//   - unknown usage renders as plain "streaming" (never a fabricated number),
//   - the compact thought summary omits the token count when the provider
//     reports no reasoning-token split,
//   - a new operation owns the action surface (stale chips disappear),
//   - $fix outside /build executes continuously (one intent → one execution).

// TestUxStreamUsageMsgContinuesDrain guards the channel drain chain: the
// streamUsageMsg handler must return the next readStream() command, otherwise
// the token/done messages behind it on the stream channel are never consumed
// and the stream stalls mid-response with a frozen indicator.
func TestUxStreamUsageMsgContinuesDrain(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	// The stage exists in the real flow (a tokenMsg established it before the
	// usage update); mirror that so setStageMetrics has a record to feed.
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)

	res, cmd := m.Update(streamUsageMsg{input: 10, output: 2048, reasoning: 96})
	m2 := res.(*model)
	if cmd == nil {
		t.Fatal("streamUsageMsg must chain the next channel read or the stream stalls")
	}
	if snap := m2.stageSnapshot(); snap.Tokens != 2048 {
		t.Fatalf("stage tokens = %d, want the authoritative 2048", snap.Tokens)
	}
}

// TestUxStreamingNoUsageRendersNoFabricatedCount guards Section 7: a real
// content-token arrival with NO provider-reported usage must render plain
// "streaming" — the count is never derived from the response buffer length.
func TestUxStreamingNoUsageRendersNoFabricatedCount(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true

	res, _ := m.Update(tokenMsg("hello world"))
	m2 := res.(*model)

	line := m2.renderStageLine()
	if !strings.Contains(line, "streaming") {
		t.Fatalf("real token arrival did not produce a streaming stage: %q", line)
	}
	if strings.Contains(line, "tok") {
		t.Fatalf("streaming stage fabricated a token count without provider usage: %q", line)
	}
	// The stage token counter must NOT have been fed from the buffer length.
	if snap := m2.stageSnapshot(); snap.Tokens != 0 {
		t.Fatalf("stage tokens = %d — a buffer-length estimate leaked into the authoritative record", snap.Tokens)
	}
}

// TestUxStreamingAuthoritativeUsageRendersCount guards the positive case: an
// authoritative provider-reported count (via streamUsageMsg) is the ONLY thing
// that populates the streaming token display.
func TestUxStreamingAuthoritativeUsageRendersCount(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true

	res, _ := m.Update(tokenMsg("hello world"))
	m2 := res.(*model)
	// The provider reports an authoritative cumulative usage mid-stream.
	res, _ = m2.Update(streamUsageMsg{input: 2522, output: 2048, reasoning: 96})
	m3 := res.(*model)

	line := m3.renderStageLine()
	if !strings.Contains(line, "streaming") {
		t.Fatalf("streaming stage missing: %q", line)
	}
	if !strings.Contains(line, "2.0k") {
		t.Fatalf("streaming stage missing the authoritative token count: %q", line)
	}
	if strings.Contains(line, "3") {
		t.Fatalf("streaming stage shows a stale buffer-derived count: %q", line)
	}
	if snap := m3.stageSnapshot(); snap.Tokens != 2048 {
		t.Fatalf("stage tokens = %d, want the authoritative 2048", snap.Tokens)
	}
}

// TestUxStreamUsageMsgSetsReasoningTokens guards the compact thought summary:
// the provider-reported reasoning-token split backs the "N tokens" suffix.
func TestUxStreamUsageMsgSetsReasoningTokens(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("analyze the failure mode")

	res, _ := m.Update(streamUsageMsg{input: 100, output: 50, reasoning: 512})
	m2 := res.(*model)

	if got := m2.thinkingBuffer.ReasoningTokens(); got != 512 {
		t.Fatalf("thinking buffer reasoning tokens = %d, want 512", got)
	}
}

// TestUxStreamingStageTerminalizes guards Section 6/14: once the stream
// completes, no "streaming" indicator survives.
func TestUxStreamingStageTerminalizes(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true

	res, _ := m.Update(tokenMsg("hello world"))
	m2 := res.(*model)
	if !strings.Contains(m2.renderStageLine(), "streaming") {
		t.Fatalf("precondition: streaming stage missing: %q", m2.renderStageLine())
	}

	res, _ = m2.Update(streamDoneMsg{content: "hello world", tokenInput: 10, tokenOutput: 20})
	m3 := res.(*model)
	if line := m3.renderStageLine(); line != "" {
		t.Fatalf("terminal stream left a live stage indicator: %q", line)
	}
	if snap := m3.stageSnapshot(); snap.active() {
		t.Fatalf("stage still active after stream terminalization: %+v", snap)
	}
}

// TestUxBeginOperationClearsStaleChips guards Section 10 ownership: the moment
// a new operation begins, chips left over from a PREVIOUS operation's result
// are stale and disappear. The operation's own terminal result re-populates
// the surface.
func TestUxBeginOperationClearsStaleChips(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.currentResult = buildVerifyResult(true)
	if len(m.currentResultActions()) == 0 {
		t.Fatal("precondition: chips must be present")
	}

	m.beginOperation(OpHotfix)

	if m.currentResult != nil {
		t.Fatal("a new operation must own the action surface — stale chips must disappear")
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatal("beginOperation must register the new operation")
	}
	// The new operation's terminal result message re-populates chips; the
	// surface is never left permanently empty by ownership handoff.
}

// TestUxNoOperationNoStageRendersNothing guards Section 2/14: with no
// execution event the renderer never fabricates a stage or spinner.
func TestUxNoOperationNoStageRendersNothing(t *testing.T) {
	m := newTestModel()
	m.activeOp = nil
	m.stage = nil

	if line := m.renderStageLine(); line != "" {
		t.Fatalf("renderer fabricated a stage with no operation: %q", line)
	}
}

// TestUxFixOutsideBuildExecutesContinuously guards Section 11: $fix typed
// outside /build performs the internal alignment to BUILD and dispatches the
// fix pipeline — the user never types /build again. One intent → one execution.
// The test context (lastTestOutput) must survive the internal transition so the
// fix actually executes instead of dead-ending on a cleared payload.
func TestUxFixOutsideBuildExecutesContinuously(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false
	m.lastTestOutput = "=== FAIL: TestSomething ===\n\tsyntax error"
	m.lastTestTarget = "internal/x.go"

	cmd := m.handleInput("$fix resolve the compile error")
	if cmd == nil {
		t.Fatal("handleInput($fix in ask) returned nil cmd — continuous execution must dispatch")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build after internal auto-transition", got)
	}
	// The fix payload must survive the transition (the HANDOFF SANITIZER
	// would otherwise wipe it before runFixCmd consumes it).
	if m.lastTestOutput == "" || m.lastTestTarget != "internal/x.go" {
		t.Fatalf("$fix lost its test context across the transition: output=%q target=%q", m.lastTestOutput, m.lastTestTarget)
	}

	// The dispatched pipeline must produce a fixResultMsg carrying the
	// auto-recovery instruction — the $fix execution actually ran.
	found := false
	for _, msg := range drainCmds(t, cmd) {
		if fm, ok := msg.(fixResultMsg); ok {
			if strings.Contains(fm.content, "AUTO-RECOVERY") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected the auto-recovery fix pipeline to dispatch after the internal /build transition")
	}
}

// TestUxPromptDirectiveNoDuplicateOperation guards Section 12: $prompt from
// outside /ask routes through the ask handoff without creating a spurious
// proposal/operation chain in the originating mode.
func TestUxPromptDirectiveNoDuplicateOperation(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "plan"}}}, nil)
	m.resolver.Set(modes.ModeBuild)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.routePromptDirective("add a dark mode toggle")
	if cmd == nil {
		t.Fatal("routePromptDirective returned nil — $prompt must dispatch")
	}
	// $prompt is an execution request: it never transitions to /ask.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("mode = /%s, want /build (no transition — $prompt is an execution request)", got)
	}
	if !hasDispatchRecord(m, "resolving intent deterministically") {
		t.Error("expected $prompt to dispatch through the unified gateway")
	}
}

// TestUxEstimatedUsageNeverFeedsStreamingCount guards Section 7 end-to-end:
// an estimated/unknown provider usage must never populate the streaming token
// display. Only Known && !Estimated counts reach the stage.
func TestUxEstimatedUsageNeverFeedsStreamingCount(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true

	// Simulate a provider that reports only an interrupted-stream estimate.
	res, _ := m.Update(tokenMsg("hello world"))
	m2 := res.(*model)
	res, _ = m2.Update(streamUsageMsg{input: 0, output: 0, reasoning: 0})
	m3 := res.(*model)

	line := m3.renderStageLine()
	if strings.Contains(line, "tok") {
		t.Fatalf("unknown/estimated usage rendered a token count: %q", line)
	}
	if snap := m3.stageSnapshot(); snap.Tokens != 0 {
		t.Fatalf("stage tokens = %d with no authoritative usage", snap.Tokens)
	}
}
