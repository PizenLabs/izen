package ui

// ── PHASE 15: LINEAR SEQUENTIAL NARRATIVE PROJECTION ─────────────────────────
//
// The Main Viewport used to be one flat slice of records, appended to by every
// source with something to say. The tests below assert the four properties the
// two-tier projection was built to restore. Each one is a property of the STATE,
// not of a screenshot, which is what makes it assertable at all:
//
//  1. INFRASTRUCTURE NEVER REACHES THE NARRATIVE. Every canonical tag is diverted
//     to Trace, verbatim, and Trace is the only place it lands — the boundary
//     moves information rather than destroying it.
//  2. THE SINGLE ACTIVE NODE INVARIANT. At most one node is ever mutable; a kind
//     change seals it; a byte-identical restatement replaces it in place. That last
//     clause is the duplicate-status fix: "Model waiting..." eleven times is one
//     fact, not eleven.
//  3. HISTORY IS APPEND-ONLY AND ORDERED. Sealing is the only writer, so scrollback
//     is stable and the sequence is linear.
//  4. TELEMETRY IS NOT NARRATIVE. The HUD has no record sink, and the reducer has
//     no path from a metric to a node.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ── 1. INFRASTRUCTURE DIVERSION ──────────────────────────────────────────────

func TestPhase15_ReducerDivertsEveryInfrastructureTag(t *testing.T) {
	for _, tag := range InfrastructureTelemetryPrefixes {
		trace := NewTelemetryDemuxer()
		r := NewProjectionReducer(trace, NewSidebarHUD())
		lines := []string{
			tag + " observing -> deciding",
			"  " + tag + " indented machine line",
			strings.ToUpper(tag) + " SHOUTED MACHINE LINE",
			tag + " multi\nline body",
		}
		for _, line := range lines {
			if got := r.Reduce(record{role: roleActivity, text: line, turnID: 1}); got != ProjectDiverted {
				t.Errorf("%q: decision = %s, want %s", line, got, ProjectDiverted)
			}
		}
		state := r.State()
		if state.Len() != 0 {
			t.Fatalf("%s leaked into the narrative: %+v", tag, state.Records())
		}
		if state.DivertedCount != len(lines) {
			t.Errorf("%s: diverted count = %d, want %d", tag, state.DivertedCount, len(lines))
		}
		// The information MOVED, not destroyed. Trace holds at least one step per
		// diverted line (a multi-line record becomes several steps, which is the
		// right granularity for a per-step overlay), and every line's content is
		// present verbatim.
		steps := trace.Steps()
		if len(steps) < len(lines) {
			t.Fatalf("%s: trace steps = %d, want at least %d — the boundary must not discard",
				tag, len(steps), len(lines))
		}
		var joined strings.Builder
		for _, step := range steps {
			joined.WriteString(step.Message)
			joined.WriteString("\n")
		}
		for _, line := range lines {
			for _, piece := range strings.Split(line, "\n") {
				piece = strings.TrimSpace(piece)
				if piece == "" {
					continue
				}
				if !strings.Contains(joined.String(), piece) {
					t.Errorf("%s: %q never reached Trace — moved, not destroyed", tag, piece)
				}
			}
		}
	}
}

func TestPhase15_ReducerKeepsLinesThatOnlyMentionAMachineTag(t *testing.T) {
	// The classifier tests a LEADING tag only. A user-visible sentence can
	// legitimately mention "[preflight]" as subject matter, and refusing it would
	// be over-filtering — as broken as a filter that refuses nothing.
	trace := NewTelemetryDemuxer()
	r := NewProjectionReducer(trace, NewSidebarHUD())
	line := "You asked about [preflight] behaviour and here is what it does."
	if got := r.Reduce(record{role: roleAI, text: line, turnID: 1}); got != ProjectOpened {
		t.Fatalf("decision = %s, want %s — a sentence mentioning a tag is not a machine line", got, ProjectOpened)
	}
	if trace.StepCount() != 0 {
		t.Fatal("a human sentence was diverted to Trace")
	}
}

func TestPhase15_ReducerMovesInformationWhenTraceIsUnwired(t *testing.T) {
	// A nil Trace is a reason to lose the information, never a reason to show it
	// in the wrong place. The narrative refusal is unconditional.
	r := NewProjectionReducer(nil, nil)
	if got := r.Reduce(record{role: roleActivity, text: "[loop] observing -> deciding", turnID: 1}); got != ProjectDiverted {
		t.Fatalf("decision = %s, want %s", got, ProjectDiverted)
	}
	if r.State().Len() != 0 {
		t.Fatal("an unwired Trace let a machine line into the narrative")
	}
}

func TestPhase15_ReducerRejectsEmptyContent(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	for _, text := range []string{"", "   ", "\n\t\n"} {
		if got := r.Reduce(record{role: roleSystem, text: text, turnID: 1}); got != ProjectRejected {
			t.Errorf("%q: decision = %s, want %s", text, got, ProjectRejected)
		}
	}
	if r.State().Len() != 0 {
		t.Fatal("empty content became a node")
	}
}

// ── 2. THE SINGLE ACTIVE NODE INVARIANT ─────────────────────────────────────

func TestPhase15_OneActiveNodeAtATime(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	if r.State().Active != nil {
		t.Fatal("a fresh state has an active node")
	}
	if got := r.Reduce(record{role: roleStatus, text: "Model waiting...", turnID: 1}); got != ProjectOpened {
		t.Fatalf("decision = %s, want %s", got, ProjectOpened)
	}
	state := r.State()
	if state.Active == nil {
		t.Fatal("the first step did not become active")
	}
	if state.SealedCount != 0 {
		t.Fatalf("the first step sealed something: %d", state.SealedCount)
	}
	// A different KIND is a different step: the current one is sealed.
	if got := r.Reduce(record{role: roleAI, text: "Here is the answer.", turnID: 1}); got != ProjectSealed {
		t.Fatalf("decision = %s, want %s", got, ProjectSealed)
	}
	state = r.State()
	if state.Active == nil {
		t.Fatal("the new step did not become active")
	}
	if state.SealedCount != 1 {
		t.Fatalf("sealed count = %d, want exactly 1 — the invariant is at most ONE active node", state.SealedCount)
	}
	if len(state.History) != 1 || state.History[0].Text != "Model waiting..." {
		t.Fatalf("history = %+v, want the sealed step", state.History)
	}
}

func TestPhase15_RepeatedStatusReplacesInPlace(t *testing.T) {
	// The duplicate-status defect, stated as a test. An indicator that re-announces
	// itself every tick must occupy ONE node, however many times it speaks.
	const status = "Model waiting..."
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleStatus, text: status, turnID: 1})
	for i := 0; i < 10; i++ {
		if got := r.Reduce(record{role: roleStatus, text: status, turnID: 1}); got != ProjectReplaced {
			t.Fatalf("re-announcement %d: decision = %s, want %s", i, got, ProjectReplaced)
		}
	}
	state := r.State()
	if state.Len() != 1 {
		t.Fatalf("nodes = %d, want exactly 1 for eleven identical announcements", state.Len())
	}
	if state.ReplacedCount != 10 {
		t.Errorf("replaced count = %d, want 10", state.ReplacedCount)
	}
	if state.Active.Revisions != 10 {
		t.Errorf("active revisions = %d, want 10", state.Active.Revisions)
	}
	// The single node carries the status exactly once.
	audit := strings.Join(r.Audit(), "\n")
	if strings.Count(audit, status) != 1 {
		t.Fatalf("the status appears %d time(s) in the projection:\n%s",
			strings.Count(audit, status), audit)
	}
}

func TestPhase15_SameStepDifferentContentContinuesRatherThanMultiplying(t *testing.T) {
	// A streaming answer grows line by line. Sealing each fragment would turn one
	// answer into dozens of nodes and destroy the linear reading.
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleAI, text: "line one", turnID: 1})
	if got := r.Reduce(record{role: roleAI, text: "line two", turnID: 1}); got != ProjectAppended {
		t.Fatalf("decision = %s, want %s", got, ProjectAppended)
	}
	if got := r.Reduce(record{role: roleAI, text: "line three", turnID: 1}); got != ProjectAppended {
		t.Fatalf("decision = %s, want %s", got, ProjectAppended)
	}
	state := r.State()
	if state.Len() != 1 {
		t.Fatalf("nodes = %d, want 1 — a growing answer is ONE step", state.Len())
	}
	if !strings.Contains(state.Active.Text, "line one") ||
		!strings.Contains(state.Active.Text, "line two") ||
		!strings.Contains(state.Active.Text, "line three") {
		t.Fatalf("the continuation lost content: %q", state.Active.Text)
	}
}

func TestPhase15_ANewTurnSealsTheOpenStep(t *testing.T) {
	// The reader's next question starts a new sequence. Merging it into the previous
	// turn's open node would attribute this answer to the old question.
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleAI, text: "answer to question one", turnID: 1})
	if got := r.Reduce(record{role: roleAI, text: "answer to question two", turnID: 2}); got != ProjectSealed {
		t.Fatalf("decision = %s, want %s", got, ProjectSealed)
	}
	state := r.State()
	if state.SealedCount != 1 || state.History[0].Text != "answer to question one" {
		t.Fatalf("the previous turn's step was not sealed: %+v", state)
	}
	if state.Active.TurnID != 2 {
		t.Fatalf("active turn = %d, want 2", state.Active.TurnID)
	}
}

func TestPhase15_UserTurnIsABoundaryNotAStep(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleStatus, text: "Model waiting...", turnID: 1})
	if got := r.Reduce(record{role: roleUser, text: "actually, do this instead", turnID: 2}); got != ProjectSealed {
		t.Fatalf("decision = %s, want %s", got, ProjectSealed)
	}
	state := r.State()
	// The user turn is history, never active: it has no in-flight lifetime.
	if state.Active != nil {
		t.Fatalf("a user turn became an active node: %+v", state.Active)
	}
	if len(state.History) != 2 {
		t.Fatalf("history = %d nodes, want 2 (the sealed step and the boundary)", len(state.History))
	}
	if state.History[1].Kind != NodeUser {
		t.Fatalf("the boundary was not classified as a user node: %+v", state.History[1])
	}
}

// ── 3. HISTORY IS APPEND-ONLY AND ORDERED ───────────────────────────────────

func TestPhase15_HistoryIsAppendOnlyAndGapFree(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	steps := []struct {
		role role
		text string
	}{
		{roleStatus, "Understanding request"},
		{roleAI, "Here is the answer."},
		{roleStatus, "Inspecting index.html"},
		{roleStatus, "Applying changes"},
		{roleSystem, "Mutation applied and verified."},
	}
	for _, s := range steps {
		r.Reduce(record{role: s.role, text: s.text, turnID: 1})
	}
	r.SealActive()
	state := r.State()
	if state.Active != nil {
		t.Fatal("SealActive left a mutable node behind")
	}
	if len(state.History) != len(steps) {
		t.Fatalf("history = %d nodes, want %d", len(state.History), len(steps))
	}
	for i, node := range state.History {
		if node.Seq != uint64(i) {
			t.Errorf("node %d has Seq %d — the sequence must be gap-free so a lost node is detectable", i, node.Seq)
		}
	}
	// The projection order is the production order.
	for i, node := range state.History {
		if node.Text != steps[i].text {
			t.Errorf("node %d = %q, want %q", i, node.Text, steps[i].text)
		}
	}
	// And the sealed history is genuinely immutable from the outside.
	snapshot := r.State()
	snapshot.History[0].Text = "TAMPERED"
	if again := r.State(); again.History[0].Text != steps[0].text {
		t.Fatalf("State() handed out a mutable view of sealed history: %q", again.History[0].Text)
	}
}

func TestPhase15_SealIsIdempotent(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleStatus, text: "Applying changes", turnID: 1})
	r.SealActive()
	r.SealActive()
	r.SealActive()
	if got := len(r.State().History); got != 1 {
		t.Fatalf("history = %d nodes after three seals, want 1 — sealing a nil active node must be a no-op", got)
	}
}

func TestPhase15_RecordsProjectsBothTiersInOrder(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleStatus, text: "step one", turnID: 1})
	r.Reduce(record{role: roleStatus, text: "step two", turnID: 1})
	records := r.Records()
	// The active node is part of the sequence the reader is following: omitting it
	// would make the last line of the viewport a lie. Duplicating a node already in
	// history would be a different lie, and the invariant rules it out.
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (one sealed, one active)", len(records))
	}
	if records[0].text != "step one" || records[1].text != "step two" {
		t.Fatalf("records out of order: %+v", records)
	}
}

// ── 4. TELEMETRY IS NOT NARRATIVE ────────────────────────────────────────────

func TestPhase15_TelemetryCannotReachTheNarrative(t *testing.T) {
	hud := NewSidebarHUD()
	r := NewProjectionReducer(NewTelemetryDemuxer(), hud)
	r.Reduce(record{role: roleStatus, text: "Model waiting...", turnID: 1})
	before := r.State().Len()

	r.RouteTelemetry(HUDContextTokens, "12k")
	r.RouteTelemetry(HUDCost, "$0.0042")
	r.RouteTelemetry(HUDMCPStatus, "3 servers")

	if after := r.State().Len(); after != before {
		t.Fatalf("routing telemetry added %d narrative node(s)", after-before)
	}
	// The metrics landed on the HUD, which is the only surface they can reach.
	if hud.Get(HUDContextTokens) != "12k" {
		t.Errorf("context tokens = %q", hud.Get(HUDContextTokens))
	}
	if hud.Get(HUDCost) != "$0.0042" {
		t.Errorf("cost = %q", hud.Get(HUDCost))
	}
	if hud.Get(HUDMCPStatus) != "3 servers" {
		t.Errorf("mcp status = %q", hud.Get(HUDMCPStatus))
	}
	// An unknown metric is a no-op rather than a new slot: letting callers invent
	// slot names is how a fixed layout becomes a variable one.
	r.RouteTelemetry(HUDMetric("invented slot"), "value")
	if hud.Get(HUDMetric("invented slot")) != HUDUnknown {
		t.Error("an unknown metric created a slot")
	}
}

func TestPhase15_ReducerIsDeterministic(t *testing.T) {
	// "The viewport is clean and linear" is only a testable property if the same
	// event sequence always produces the same state.
	sequence := []record{
		{role: roleUser, text: "refactor the auth module", turnID: 1},
		{role: roleStatus, text: "Understanding request", turnID: 1},
		{role: roleStatus, text: "Model waiting...", turnID: 1},
		{role: roleStatus, text: "Model waiting...", turnID: 1},
		{role: roleActivity, text: "[loop] observing -> deciding", turnID: 1},
		{role: roleStatus, text: "Inspecting internal/auth/token.go", turnID: 1},
		{role: roleAI, text: "Here is the change.", turnID: 1},
		{role: roleSystem, text: "Applied change to internal/auth/token.go", turnID: 1},
	}
	project := func() []string {
		r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
		for _, rec := range sequence {
			r.Reduce(rec)
		}
		r.SealActive()
		return r.Audit()
	}
	first, second := project(), project()
	if len(first) != len(second) {
		t.Fatalf("projection length is not deterministic: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("projection diverged at %d: %q vs %q", i, first[i], second[i])
		}
	}
	// And the projection is free of the machine tag.
	for _, line := range first {
		if strings.HasPrefix(strings.TrimSpace(line), "[loop]") {
			t.Fatalf("machine line reached the projection: %q", line)
		}
	}
}

// ── THE MODEL WIRING ────────────────────────────────────────────────────────

// TestPhase15_ModelRoutesPushAndLogActivityThroughOneReducer pins that there is
// ONE ingestion authority. Two writers with the same policy is how they drift.
func TestPhase15_ModelRoutesPushAndLogActivityThroughOneReducer(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.narrative = nil

	// logActivity with a machine line: refused by the narrative, kept in Trace.
	m.logActivity("[barrier] waiting preflight")
	if len(m.records) != 0 {
		t.Fatalf("logActivity leaked a machine line: %+v", m.records)
	}
	if m.telemetryDemuxer.StepCount() != 1 {
		t.Fatalf("trace steps = %d, want 1", m.telemetryDemuxer.StepCount())
	}
	// logActivity with a human line: admitted.
	m.logActivity("Applied 2 changes to index.html")
	if len(m.records) != 1 {
		t.Fatalf("records = %d, want 1", len(m.records))
	}
	// push with a machine line: the SAME boundary applies, because push now shares
	// the reducer. Before this phase a push could bypass it entirely.
	m.push(roleError, "[context] execution hand-off refused: budget")
	if len(m.records) != 1 {
		t.Fatalf("push leaked a machine line: %+v", m.records)
	}
	if m.telemetryDemuxer.StepCount() != 2 {
		t.Fatalf("trace steps = %d, want 2", m.telemetryDemuxer.StepCount())
	}
	if m.narrative == nil {
		t.Fatal("the reducer was not wired")
	}
	if m.narrative.State().DivertedCount != 2 {
		t.Errorf("diverted count = %d, want 2", m.narrative.State().DivertedCount)
	}
}

func TestPhase15_ModelRepeatedStatusOccupiesOneNode(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.currentTurnID = 7
	for i := 0; i < 5; i++ {
		m.logActivity("Model waiting...")
	}
	// The render input IS the projection, so the dedupe is visible: five
	// announcements of one step occupy one record, and therefore one row of the
	// document layout, one hit-map entry and one selectable line.
	if len(m.records) != 1 {
		t.Fatalf("records = %d, want 1 for five identical announcements:\n%+v", len(m.records), m.records)
	}
	if m.records[0].text != "Model waiting..." {
		t.Fatalf("record text = %q, want the status exactly once", m.records[0].text)
	}
	state := m.narrativeState()
	if state.Len() != 1 {
		t.Fatalf("projected nodes = %d, want 1", state.Len())
	}
	if state.ReplacedCount != 4 {
		t.Errorf("replaced count = %d, want 4", state.ReplacedCount)
	}
	// The projection and the render input agree, which is what makes the
	// invariant a property of what is on screen rather than of a parallel state.
	if projected := m.narrative.Records(); len(projected) != len(m.records) {
		t.Fatalf("projection has %d nodes, render input has %d — they must stay aligned",
			len(projected), len(m.records))
	}
}

func TestPhase15_ModelDistinctStepsEachGetTheirOwnNode(t *testing.T) {
	// The negative of the dedupe test: collapsing everything into one node would
	// satisfy the invariant while destroying the history. Two different steps are
	// two nodes, and the layout must show both.
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.currentTurnID = 1
	for _, step := range []string{
		"Understanding request",
		"Inspecting internal/auth/token.go",
		"Applying changes",
		"Applied change to internal/auth/token.go",
	} {
		m.logActivity("%s", step)
	}
	if len(m.records) != 4 {
		t.Fatalf("records = %d, want one per distinct step:\n%+v", len(m.records), m.records)
	}
	for i, step := range []string{
		"Understanding request",
		"Inspecting internal/auth/token.go",
		"Applying changes",
		"Applied change to internal/auth/token.go",
	} {
		if m.records[i].text != step {
			t.Errorf("record %d = %q, want %q — the step sequence must stay linear and ordered",
				i, m.records[i].text, step)
		}
	}
}

func TestPhase15_ResetKeepsTelemetryAndDropsTheNarrative(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.push(roleStatus, "Understanding request")
	m.routeContextTokens(4096)
	m.routeCost("$0.01")
	m.sealNarrative()
	if m.narrativeState().Len() == 0 {
		t.Fatal("nothing was projected")
	}
	// /clear drops the transcript on BOTH tiers, or the first record of the new
	// session would merge into a step that belongs to the cleared one.
	m.records = nil
	m.narrative.Reset()
	if m.narrativeState().Len() != 0 {
		t.Fatal("Reset left narrative content")
	}
	// Session telemetry outlives the transcript that produced it: zeroing it on
	// /clear would report a spend that did not happen.
	if m.HUD().Get(HUDContextTokens) == HUDUnknown {
		t.Error("Reset cleared the session's context-token total")
	}
}

func TestPhase15_ModelSealsTheActiveNodeOnTerminalExecution(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.currentTurnID = 3
	m.push(roleStatus, "Applying changes")
	if m.narrativeState().Active == nil {
		t.Fatal("the in-flight step is not active")
	}
	m.sealNarrative()
	state := m.narrativeState()
	if state.Active != nil {
		t.Fatal("the terminal execution left a mutable node behind")
	}
	if len(state.History) != 1 || state.History[0].Text != "Applying changes" {
		t.Fatalf("the final step did not enter history: %+v", state.History)
	}
}

func TestPhase15_ProjectionRebindsWhenTheTraceBufferIsReplaced(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.logActivity("Understanding request")
	first := m.narrative
	// A /clear or a re-init replaces the demuxer. A reducer still pointing at the
	// orphaned buffer would divert into nothing — losing infrastructure telemetry
	// instead of moving it — and a reducer REPLACED rather than rebound would drop
	// the projected history along with the buffer.
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.logActivity("[loop] observing -> deciding")
	if m.narrative != first {
		t.Fatal("the reducer was replaced instead of rebound — the projection history was discarded")
	}
	if m.narrative.Trace() != m.telemetryDemuxer {
		t.Fatal("the reducer's trace is not the live buffer")
	}
	if m.telemetryDemuxer.StepCount() != 1 {
		t.Fatalf("the diverted line went to the orphaned buffer: %d step(s)", m.telemetryDemuxer.StepCount())
	}
	// The narrative kept its history across the rebind.
	state := m.narrativeState()
	if state.Len() == 0 {
		t.Fatal("the rebind discarded the projection")
	}
	if !strings.Contains(strings.Join(m.narrative.Audit(), "\n"), "Understanding request") {
		t.Fatalf("the rebind lost projected content: %+v", m.narrative.Audit())
	}
}

func TestPhase15_NilModelAndNilReducerAreInert(t *testing.T) {
	var m *model
	if m.HUD() != nil {
		t.Error("a nil model returned a HUD")
	}
	if got := m.renderSidebarHUD(); got != "" {
		t.Errorf("a nil model rendered a HUD: %q", got)
	}
	var r *ProjectionReducer
	if r.Reduce(record{role: roleStatus, text: "x", turnID: 1}) != ProjectRejected {
		t.Error("a nil reducer admitted a record")
	}
	if r.State().Len() != 0 || r.Records() != nil || r.Audit() != nil {
		t.Error("a nil reducer reported state")
	}
	// Routing on a nil reducer is a no-op, never a panic.
	r.RouteTelemetry(HUDCost, "$1")
	r.SealActive()
	r.Reset()
	_ = tea.Msg(nil) // keep the bubbletea import honest for the update-loop types
}
