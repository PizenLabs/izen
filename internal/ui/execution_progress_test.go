package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Execution-truthful progress regression suite (Phase 2) ──────────────────
//
// These tests pin the contract that every progress indicator is derived from
// REAL execution events (the authoritative execStage / ActivityTree) and that
// no progress is fabricated: a provider wait renders as "waiting", a token
// stream as "streaming", and "Thinking..."/"Processing file mutations..."/fake
// read rows never appear without the runtime actually doing that work. On the
// RuntimeExecutor path the runtime's canonical events (provider.waiting, real
// file reads) are the evidence the projection renders — never a static
// dispatch template.

// ── 1 + 3. Real execution event produces progress; waiting ≠ thinking ──────

func TestProgressRealProviderWaitProducesWaitingState(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)
	// Wire a bus so the runtime emits its canonical provider.waiting event — the
	// truthful evidence the UI projects into the waiting stage.
	bus := events.NewBus(events.DefaultBufferSize)
	m.bus = bus
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, bp, bus, "")
	evCh := make(chan events.DomainEvent, 16)
	sub := bus.Subscribe(events.EventProviderWaiting, func(ev events.DomainEvent) { evCh <- ev })
	defer sub.Cancel()

	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	// Feed the REAL provider.waiting event into the projection (the production
	// bus loop delivers it the same way) — the runtime's own evidence.
	select {
	case ev := <-evCh:
		m.handleDomainEvent(ev)
	case <-time.After(3 * time.Second):
		t.Fatal("runtime never emitted the real provider.waiting event")
	}

	// The REAL provider invocation produced a truthful waiting stage.
	line := m.renderStageLine()
	if !strings.Contains(line, "waiting") {
		t.Fatalf("during provider wait the stage is not 'waiting': %q", line)
	}
	if !strings.Contains(line, "Model") {
		t.Fatalf("during provider wait the stage lacks the Model label: %q", line)
	}
	// The progress indicator NEVER claims thinking while waiting.
	if strings.Contains(line, "Thinking") || strings.Contains(line, "Processing") {
		t.Fatalf("waiting state claims fake work: %q", line)
	}
	// The dock text agrees.
	dock := stripANSITest(m.composeDockText())
	if !strings.Contains(dock, "waiting") {
		t.Fatalf("dock missing truthful waiting state: %q", dock)
	}
	if strings.Contains(dock, "Thinking") {
		t.Fatalf("dock claims thinking while provider waits: %q", dock)
	}

	// Release the worker.
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never cancelled")
	}
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}
}

// ── 2. No execution event means no fake progress ───────────────────────────

func TestProgressNoExecutionEventNoFakeProgress(t *testing.T) {
	m := newTestModel()

	// No stage, no operation — the renderer must not fabricate one.
	if line := m.renderStageLine(); line != "" {
		t.Fatalf("renderStageLine fabricated progress with no execution event: %q", line)
	}
	dock := stripANSITest(m.composeDockText())
	if strings.Contains(dock, "waiting") || strings.Contains(dock, "streaming") ||
		strings.Contains(dock, "Processing") || strings.Contains(dock, "Thinking") {
		t.Fatalf("idle dock fabricated an execution stage: %q", dock)
	}
	// Even with a shimmer fallback the dock must not invent a stage.
	m.startShimmer("Synthesizing plan...", "plan")
	dock2 := stripANSITest(m.composeDockText())
	if strings.Contains(dock2, "waiting") || strings.Contains(dock2, "streaming") {
		t.Fatalf("dock fabricated a provider stage without a provider event: %q", dock2)
	}
}

// ── 4. Provider streaming is rendered as streaming ─────────────────────────

func TestProgressStreamingRenderedAsStreaming(t *testing.T) {
	m := newTestModel()
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	m.setStageMetrics(0, 0, 921)

	line := m.renderStageLine()
	if !strings.Contains(line, "streaming") {
		t.Fatalf("streaming stage not rendered as streaming: %q", line)
	}
	if !strings.Contains(line, "921") {
		t.Fatalf("streaming stage missing real token count: %q", line)
	}
	if strings.Contains(line, "Thinking") || strings.Contains(line, "waiting") {
		t.Fatalf("streaming state mislabelled: %q", line)
	}

	// A real tokenMsg arrival transitions the stage to streaming.
	m2 := newTestModel()
	m2.streaming = true
	m2.state = StateChat
	res, _ := m2.Update(tokenMsg("hello world"))
	m3 := res.(*model)
	if !strings.Contains(m3.renderStageLine(), "streaming") {
		t.Fatalf("real token arrival did not produce a streaming stage: %q", m3.renderStageLine())
	}
}

// ── 5 + 6. Reads are real, retries never fabricated, never UI duplication ──

func TestProgressRetryReadsAreRealAndDistinguishable(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, smallHTML)
	// Wire the production event-logger seam: the runtime's real file reads
	// cross into the activity tree ONLY when it actually reads the disk.
	execution.SetEventLogger(func(ev interface{}) { m.handleEngineEvent(ev) })
	defer execution.SetEventLogger(nil)

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	var gem *gatedExecutionMsg
	for _, msg := range msgs {
		if g, ok := msg.(gatedExecutionMsg); ok {
			gem = &g
		}
	}
	if gem == nil || gem.err != nil {
		t.Fatalf("expected a successful execution, got %+v", gem)
	}

	if m.activityTree == nil {
		t.Fatal("activity tree not populated")
	}
	reads := 0
	for _, ev := range m.activityTree.Entries() {
		if ev.Kind == EventFileRead {
			reads++
		}
	}
	// The runtime performs exactly 1 REAL disk read on the targeted-mutation
	// path: the Observe-phase snapshot captures each target's bytes ONCE and
	// compileContext + invokeMutation consume that cached []byte (no rereads).
	// Every row corresponds to a real read and the renderer never adds or
	// removes one. The executor has no retry loop, so a retry badge would be a
	// fabricated claim.
	if reads != 1 {
		t.Fatalf("activity tree shows %d read rows, want 1 real read", reads)
	}

	// The renderer distinguishes real repeated reads from fresh reads and never
	// duplicates rows: each read row maps to one real disk read.
	rendered := stripANSITest(m.activityTree.Render(120))
	if got := strings.Count(rendered, "(retry"); got != 0 {
		t.Fatalf("render fabricated %d retry labels on the executor path (no retries exist):\n%s", got, rendered)
	}
	freshRows := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "read │ index.html") {
			freshRows++
		}
	}
	if freshRows != 1 {
		t.Fatalf("render shows %d plain fresh-read rows, want exactly 1 (one per real disk read, no UI duplication):\n%s", freshRows, rendered)
	}
}

// ── 7 + 8 + 9. Spinner stops on success / failure / cancellation ───────────

func TestProgressSpinnerStopsOnSuccess(t *testing.T) {
	// A valid SEARCH/REPLACE patch is the successful-execution terminal: the
	// runtime holds the patch at the approval gate. The presentation MUST leave
	// the execution-processing claim and hand the surface to the proposal dock —
	// never a stale "Processing" state.
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, smallHTML)
	m.startShimmer("Generating patch...", "execute")

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	feedUntil(t, m, msgs, func(msg tea.Msg) bool {
		_, ok := msg.(gatedExecutionMsg)
		return ok
	})

	if m.state == StateProcessing {
		t.Fatal("stuck in Processing after a successful execution")
	}
	if m.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval (approval gate)", m.state)
	}
	if m.executorPendingPatchID == "" {
		t.Fatal("successful execution did not stage the approval-held patch")
	}
	if m.pendingHotfixPatch == nil {
		t.Fatal("proposal patch not staged after successful execution")
	}
}

func TestProgressSpinnerStopsOnFailure(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{Content: ""}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, smallHTML)
	m.startShimmer("Generating patch...", "execute")

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	feedUntil(t, m, msgs, func(msg tea.Msg) bool {
		g, ok := msg.(gatedExecutionMsg)
		return ok && g.err != nil
	})

	if m.shimmerActive {
		t.Fatal("spinner still active after failure")
	}
	if m.isWorkflowBusy() {
		t.Fatal("busy flags still set after failure")
	}
}

func TestProgressSpinnerStopsOnCancellation(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)
	m.startShimmer("Applying hotfix...", "execute")

	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never cancelled")
	}
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}

	if m.shimmerActive {
		t.Fatal("spinner still active after cancellation")
	}
	if m.isWorkflowBusy() {
		t.Fatal("busy flags still set after cancellation")
	}
}

// ── 10. Ctrl+C remains usable during provider wait ────────────────────────

func TestProgressCtrlCUsableDuringProviderWait(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)

	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}

	// The status bar advertises that cancellation is available during the wait.
	status := stripANSITest(m.renderRuntimeStatus(120))
	if !strings.Contains(status, "Ctrl+C") {
		t.Fatalf("status bar does not advertise cancellation during provider wait: %q", status)
	}

	// Ctrl+C processes immediately while the provider is still blocked.
	done := make(chan struct{})
	go func() {
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event loop blocked by provider wait — Ctrl+C unusable")
	}
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never observed cancellation")
	}
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}
}

// ── 11. Next command is accepted after cancellation ───────────────────────

func TestProgressNextCommandAfterCancellation(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, bp, tasks, smallHTML)
	msgs := runBuildCmdsFilteredBackground(m.handleBuildRun(0))
	select {
	case <-bp.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-bp.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never cancelled")
	}
	select {
	case <-msgs:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never released")
	}

	m.ti.SetValue("!echo usable-after-cancel")
	m2, _ := m.submitEnter()
	if !strings.Contains(recordsText(m2.(*model)), "usable-after-cancel") {
		t.Fatal("next command rejected after cancellation")
	}
}

// ── 12. No stale "Processing file mutations" after terminal state ─────────

func TestProgressNoStaleProcessingAfterTerminal(t *testing.T) {
	// Success
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	tasks := []plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}}
	m := buildRunModel(t, mock, tasks, smallHTML)
	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	feedUntil(t, m, msgs, func(msg tea.Msg) bool {
		_, ok := msg.(gatedExecutionMsg)
		return ok
	})
	if v := m.renderProposalBlock(); strings.Contains(v, "Processing file mutations") {
		t.Fatalf("stale 'Processing file mutations' after success: %q", v)
	}

	// The in-flight dock is also truthful: during apply it shows the stage.
	m2 := newTestModel()
	m2.state = StateProcessing
	m2.setStage("apply", "index.html", stageRunning)
	if v := m2.renderProposalBlock(); !strings.Contains(v, "Apply") || strings.Contains(v, "Processing file mutations") {
		t.Fatalf("in-flight dock not derived from the real apply stage: %q", v)
	}
}

// ── 13. No fake "Thinking..." remains after provider completion ───────────

func TestProgressNoFakeThinkingAfterCompletion(t *testing.T) {
	m := newTestModel()
	m.startShimmer("Waiting for model...", "analyze")
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	// Provider completes → streamDoneMsg → stage done, dock must not claim
	// thinking.
	res, _ := m.Update(streamDoneMsg{content: "final answer", tokenInput: 10, tokenOutput: 20})
	m2 := res.(*model)
	if m2.stage == nil || m2.stage.State != stageDone {
		t.Fatalf("stage not terminal after provider completion: %+v", m2.stage)
	}
	dock := stripANSITest(m2.composeDockText())
	if strings.Contains(dock, "Thinking") || strings.Contains(dock, "waiting") || strings.Contains(dock, "streaming") {
		t.Fatalf("dock still claims provider activity after completion: %q", dock)
	}
}

// ── 14. No hidden model reasoning is rendered by the progress UI ──────────

func TestProgressNoHiddenReasoningRendered(t *testing.T) {
	m := newTestModel()
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("SECRET_REASONING_PAYLOAD")
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)
	m.startShimmer("Waiting for model...", "analyze")

	dock := stripANSITest(m.composeDockText())
	if strings.Contains(dock, "SECRET_REASONING_PAYLOAD") {
		t.Fatalf("progress UI leaked hidden reasoning content: %q", dock)
	}
	if strings.Contains(dock, "Thinking") {
		t.Fatalf("progress UI claims the model is thinking: %q", dock)
	}
	// The stage status itself carries no reasoning text.
	if line := m.renderStageLine(); strings.Contains(line, "SECRET_REASONING_PAYLOAD") || strings.Contains(line, "Thinking") {
		t.Fatalf("stage line leaked reasoning content: %q", line)
	}
}

// feedUntil feeds every message in msgs to the model until match returns true.
func feedUntil(t *testing.T, m *model, msgs []tea.Msg, match func(tea.Msg) bool) {
	t.Helper()
	for _, msg := range msgs {
		if match(msg) {
			res, _ := m.Update(msg)
			*m = *res.(*model)
			return
		}
	}
	t.Fatalf("no matching terminal message in %d messages", len(msgs))
}
