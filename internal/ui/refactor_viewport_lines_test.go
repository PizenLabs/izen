package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/modes"
)

// TestLineKindStronglyTypedAssignment tests that each line ingested is assigned
// its proper strongly-typed LineKind.
func TestLineKindStronglyTypedAssignment(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	records := []record{
		{role: roleUser, text: "what is Go?", turnID: 1},
		{role: roleActivity, text: "[event] PromptAdmitted intent=conversation latency=12ms", turnID: 1},
		{role: roleActivity, text: "command received: hi", turnID: 1},
		{role: roleActivity, text: "intent parsed: conversation", turnID: 1},
		{role: roleAI, text: "Go is an open source programming language.", turnID: 1},
		{role: roleError, text: "command switch_mode failed: State Error: Transition from /ask to /build not allowed", turnID: 1},
	}

	dl := BuildDocumentLayout(records, 100, "Developer")

	var foundUser, foundSummary, foundAI, foundError bool
	for _, l := range dl.Lines {
		raw := ansi.Strip(l.RawText)
		switch l.Kind {
		case LineKindUserPrompt:
			foundUser = true
			if !strings.Contains(raw, "what is Go?") {
				t.Errorf("LineKindUserPrompt unexpected content: %q", raw)
			}
		case LineKindTraceSummary:
			foundSummary = true
			if !strings.HasPrefix(raw, "▸ Trace:") {
				t.Errorf("LineKindTraceSummary unexpected content: %q", raw)
			}
		case LineKindAIResponse:
			foundAI = true
			if !strings.Contains(raw, "Go is an open source") {
				t.Errorf("LineKindAIResponse unexpected content: %q", raw)
			}
		case LineKindSystemError:
			foundError = true
			if !strings.Contains(raw, "✖ State Error: Transition from /ask to /build not allowed") {
				t.Errorf("LineKindSystemError unexpected content: %q", raw)
			}
			if strings.Contains(raw, "command switch_mode failed:") {
				t.Errorf("LineKindSystemError leaked raw wrapper: %q", raw)
			}
		case LineKindEngineTrace:
			t.Errorf("LineKindEngineTrace should have been dropped in quiet mode, but found: %q", raw)
		}
	}

	if !foundUser {
		t.Error("missing LineKindUserPrompt")
	}
	if !foundSummary {
		t.Error("missing LineKindTraceSummary")
	}
	if !foundAI {
		t.Error("missing LineKindAIResponse")
	}
	if !foundError {
		t.Error("missing LineKindSystemError")
	}
}

// TestQuietModeDropsEngineTraceAndSingleSummaryPerTurn tests that quiet mode
// completely drops all LineKindEngineTrace lines and outputs exactly one LineKindTraceSummary per turn.
func TestQuietModeDropsEngineTraceAndSingleSummaryPerTurn(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	traces := []string{
		"command received: /plan",
		"intent parsed: architecture (98%)",
		"[phase] ask → plan",
		"[event] PromptAdmitted intent=plan latency=30ms",
		"[preflight] decision surface staged target=app.go",
		"[autonomy decision]\n  intent: modification\n  decision: direct_response (15ms)",
	}

	recs := make([]record, 0, 1+len(traces)+1)
	recs = append(recs, record{role: roleUser, text: "plan architecture", turnID: 1})
	for _, tr := range traces {
		recs = append(recs, record{role: roleActivity, text: tr, turnID: 1})
	}
	recs = append(recs, record{role: roleAI, text: "Here is the architectural plan.", turnID: 1})

	dl := BuildDocumentLayout(recs, 100, "Developer")

	summaryCount := 0
	for _, l := range dl.Lines {
		raw := ansi.Strip(l.RawText)
		if l.Kind == LineKindEngineTrace {
			t.Errorf("LineKindEngineTrace line was not dropped in quiet mode: %q", raw)
		}
		if l.Kind == LineKindTraceSummary {
			summaryCount++
		}
		for _, leaked := range []string{"command received:", "intent parsed:", "[phase]", "[event]", "[preflight]", "[autonomy decision]"} {
			if strings.Contains(raw, leaked) {
				t.Errorf("raw engine trace leaked in quiet mode: %q", raw)
			}
		}
	}

	if summaryCount != 1 {
		t.Fatalf("expected exactly 1 LineKindTraceSummary for turn 1, got %d", summaryCount)
	}
}

// TestTurnBoundTraceDeduplicationStreamingUpdates tests that streaming sub-updates
// within the same TurnID do NOT re-emit or duplicate the summary line.
func TestTurnBoundTraceDeduplicationStreamingUpdates(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	recs := make([]record, 0, 8)
	recs = append(recs, []record{
		{role: roleUser, text: "create a file", turnID: 10},
		{role: roleActivity, text: "[event] PromptAdmitted intent=mutate latency=15ms", turnID: 10},
	}...)

	dl := BuildDocumentLayout(recs, 100, "Developer")

	// Incremental streaming sub-updates within same TurnID 10
	recs = append(recs,
		record{role: roleActivity, text: "[phase] ask → build", turnID: 10},
		record{role: roleActivity, text: "intent parsed: mutation", turnID: 10},
		record{role: roleAI, text: "Creating the requested file...", turnID: 10},
	)

	dl2 := IncrementalLayoutUpdate(&dl, recs, 100, "Developer")

	summaries := 0
	for _, l := range dl2.Lines {
		if l.Kind == LineKindTraceSummary {
			summaries++
		}
		if l.Kind == LineKindEngineTrace {
			t.Errorf("engine trace line leaked in incremental layout: %q", l.RawText)
		}
	}

	if summaries != 1 {
		t.Fatalf("expected exactly 1 trace summary after streaming sub-updates, got %d", summaries)
	}

	// Turn 2 arrives with TurnID 11
	recs = append(recs,
		record{role: roleUser, text: "run tests", turnID: 11},
		record{role: roleActivity, text: "[event] PromptAdmitted intent=test latency=25ms", turnID: 11},
		record{role: roleAI, text: "Tests passed.", turnID: 11},
	)

	dl3 := IncrementalLayoutUpdate(&dl2, recs, 100, "Developer")

	summariesTurn2 := 0
	for _, l := range dl3.Lines {
		if l.Kind == LineKindTraceSummary {
			summariesTurn2++
		}
	}

	if summariesTurn2 != 2 {
		t.Fatalf("expected 2 trace summaries across 2 distinct turns, got %d", summariesTurn2)
	}
}

// TestTopBarSynchronizesModeStateOnPhaseChange tests that Top Bar mode badge
// updates dynamically when switching workspace modes (e.g. executing /build updates right side to [WRITE]).
func TestTopBarSynchronizesModeStateOnPhaseChange(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.runtimeCtx = &runtime.RuntimeContext{}
	m.workflowSM = workflow.NewWorkflowStateMachine()
	m.indexingStatus = "indexed"

	// Initial mode: ask -> [READ-ONLY]
	m.resolver.Set(modes.ModeAsk)
	topBar := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBar, "[READ-ONLY]") {
		t.Fatalf("expected Top Bar to render [READ-ONLY], got %q", topBar)
	}

	// Receive PhaseChanged domain event -> build
	m.handleDomainEvent(events.NewPhaseChanged("ask", "build"))
	topBarBuild := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBarBuild, "[WRITE]") {
		t.Fatalf("expected Top Bar to update immediately to [WRITE] on phase change to build, got %q", topBarBuild)
	}

	// Receive PhaseChanged domain event -> investigate
	m.handleDomainEvent(events.NewPhaseChanged("build", "investigating"))
	topBarInvestigate := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBarInvestigate, "[EXECUTE]") {
		t.Fatalf("expected Top Bar to update immediately to [EXECUTE] on phase change to investigate, got %q", topBarInvestigate)
	}

	// Switch back to ask
	m.handleDomainEvent(events.NewPhaseChanged("investigating", "ask"))
	topBarAsk := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBarAsk, "[READ-ONLY]") {
		t.Fatalf("expected Top Bar to update back to [READ-ONLY], got %q", topBarAsk)
	}
}

// TestInteractiveSequence_Hi_Build_Ask tests the full sequence: hi -> /build -> /ask
func TestInteractiveSequence_Hi_Build_Ask(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	m := readyChatModel(newTestModel())
	m.runtimeCtx = &runtime.RuntimeContext{}
	m.workflowSM = workflow.NewWorkflowStateMachine()
	m.indexingStatus = "indexed"
	m.resolver.Set(modes.ModeAsk)

	// 1. Step 1: User types "hi"
	m.currentTurnID = 1
	m.handleInput("hi")
	m.push(roleActivity, "command received: hi")
	m.push(roleActivity, "intent parsed: conversation")
	m.push(roleActivity, "[event] PromptAdmitted intent=conversation latency=10ms")
	m.push(roleAI, "Hello! How can I help you today?")
	m.refreshViewportContent()

	topBar1 := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBar1, "[READ-ONLY]") {
		t.Errorf("Step 1 ('hi') Top Bar should be [READ-ONLY], got %q", topBar1)
	}

	// Verify Viewport for Step 1
	var turn1Summaries int
	for _, l := range m.docLayout.Lines {
		raw := ansi.Strip(l.RawText)
		if l.Kind == LineKindEngineTrace {
			t.Errorf("Step 1 raw engine trace line leaked: %q", raw)
		}
		if l.Kind == LineKindTraceSummary {
			turn1Summaries++
		}
		for _, leaked := range []string{"command received:", "intent parsed:", "[event]"} {
			if strings.Contains(raw, leaked) {
				t.Errorf("Step 1 leaked raw trace token %q in %q", leaked, raw)
			}
		}
	}
	if turn1Summaries != 1 {
		t.Errorf("Step 1 expected 1 trace summary, got %d", turn1Summaries)
	}

	// 2. Step 2: User types "/build"
	m.currentTurnID = 2
	m.handleInput("/build")
	m.clearToast()
	m.push(roleActivity, "command received: /build")
	m.push(roleActivity, "[phase] ask → build")
	m.push(roleActivity, "[preflight] snapshot ready target=all")
	m.push(roleError, "State Transition Blocked: File modifications are only allowed inside /build mode after /plan approval. Please run /plan first, then use /build.")
	m.refreshViewportContent()

	// Verify Top Bar updated to [WRITE] (since mode was switched to /build)
	topBar2 := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBar2, "[WRITE]") {
		t.Errorf("Step 2 ('/build') Top Bar should be [WRITE], got %q", topBar2)
	}

	// Verify Viewport for Step 2
	var foundError bool
	var turn2Summaries int
	for _, l := range m.docLayout.Lines {
		raw := ansi.Strip(l.RawText)
		if l.Kind == LineKindEngineTrace {
			t.Errorf("Step 2 raw engine trace line leaked: %q", raw)
		}
		if l.Kind == LineKindTraceSummary {
			turn2Summaries++
		}
		if l.Kind == LineKindSystemError {
			if strings.Contains(raw, "✖ State Error: File modifications are only allowed inside /build mode after /plan approval.") {
				foundError = true
			}
		}
		for _, leaked := range []string{"command received:", "intent parsed:", "[phase]", "[preflight]"} {
			if strings.Contains(raw, leaked) {
				t.Errorf("Step 2 leaked raw trace token %q in %q", leaked, raw)
			}
		}
	}
	if !foundError {
		t.Errorf("Step 2 missing styled 1-line SystemError")
	}
	if turn2Summaries != 2 {
		t.Errorf("Step 2 total trace summaries should be 2 (one per turn), got %d", turn2Summaries)
	}

	// 3. Step 3: User types "/ask"
	m.currentTurnID = 3
	m.handleInput("/ask")
	m.clearToast()
	m.push(roleActivity, "command received: /ask")
	m.push(roleActivity, "[phase] build → ask")
	m.push(roleAI, "Switched to /ask mode.")
	m.refreshViewportContent()

	// Verify Top Bar updated back to [READ-ONLY]
	topBar3 := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(topBar3, "[READ-ONLY]") {
		t.Errorf("Step 3 ('/ask') Top Bar should be [READ-ONLY], got %q", topBar3)
	}

	// Verify Viewport for Step 3
	var turn3Summaries int
	for _, l := range m.docLayout.Lines {
		raw := ansi.Strip(l.RawText)
		if l.Kind == LineKindEngineTrace {
			t.Errorf("Step 3 raw engine trace line leaked: %q", raw)
		}
		if l.Kind == LineKindTraceSummary {
			turn3Summaries++
		}
		for _, leaked := range []string{"command received:", "[phase]"} {
			if strings.Contains(raw, leaked) {
				t.Errorf("Step 3 leaked raw trace token %q in %q", leaked, raw)
			}
		}
	}
	if turn3Summaries != 3 {
		t.Errorf("Step 3 total trace summaries should be 3 (one per turn), got %d", turn3Summaries)
	}
}
