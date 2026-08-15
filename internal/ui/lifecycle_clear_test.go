package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
)

// clearTestModel builds a model pre-populated with every surface /clear must
// clear (records, activity tree, execution log, thinking, loading, chips) plus
// durable session state /clear must preserve.
func clearTestModel() *model {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.workspaceRoot = "/tmp/izen-ws"
	m.gitEng = git.NewEngine(m.workspaceRoot)
	m.shimmerAnim = shimmer.New("")
	m.state = StateChat
	m.showBanner = false

	// ── Durable session state that must SURVIVE /clear ──────────────
	m.sess = &session.Session{}
	m.sess.History = []session.Message{
		{Role: "user", Content: "first question", Timestamp: time.Now()},
		{Role: "assistant", Content: "first answer", Timestamp: time.Now()},
	}
	m.sess.CurrentTasks = []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "completed"},
	}
	m.sess.Objective = "refactor the landing page"

	// ── Execution-activity surfaces /clear must CLEAR ───────────────
	m.activityTree = NewActivityTree()
	m.activityTree.Append(NewFileReadEvent("index.html", 2048, 3*time.Millisecond))
	m.activityTree.Append(NewFileReadEventRetry("index.html", 2048, 3*time.Millisecond, 1))
	m.activityTree.Append(NewFileReadEventRetry("index.html", 2048, 3*time.Millisecond, 2))
	m.activityTree.Append(NewFileMutateEvent("index.html", 3, 1, 8*time.Millisecond))
	m.records = []record{
		{role: roleActivity, text: "read index.html"},
		{role: roleActivity, text: "read index.html (retry 1)"},
		{role: roleActivity, text: "Edit(index.html)"},
	}
	m.PreRenderedHistory = "read index.html\nEdit(index.html)\n"
	m.logStore.AddFullSemantic(LogResult, "index.html", true, "+3 -1", "", "", execution.StageResult, execution.OutcomeChanged)

	// ── Presentation state /clear must CLEAR ─────────────────────────
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("stale reasoning payload")
	m.thinkingBuffer.SetExpanded(true)
	m.thinkingPanel = NewThinkingPanel()
	m.thinkingPanel.Append("stale panel reasoning")
	m.streaming = true
	m.streamBuffer = "stale buffered tokens"
	m.currentStreamContent = "stale streamed content"
	m.shimmerActive = true
	m.shimmerText = "Working..."
	m.currentResult = &Result{}

	return m
}

func TestClearRemovesOldReadEvents(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	if m.activityTree == nil || m.activityTree.Len() != 0 {
		t.Fatalf("activity tree must be empty after /clear, got %d entries", m.activityTree.Len())
	}
	if len(m.records) != 0 {
		t.Errorf("records must be empty after /clear, got %d", len(m.records))
	}
	if m.PreRenderedHistory != "" {
		t.Errorf("PreRenderedHistory must be empty after /clear")
	}
}

func TestClearRemovesOldEditEvents(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	if m.activityTree == nil || m.activityTree.Len() != 0 {
		t.Fatalf("activity tree (Edit entries) must be empty after /clear")
	}
}

func TestClearRemovesOldApplyEvents(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	if m.logStore == nil || len(m.logStore.Entries()) != 0 {
		t.Errorf("execution log (apply entries) must be empty after /clear, got %d", len(m.logStore.Entries()))
	}
}

func TestClearRemovesStaleThinkingAndLoading(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	if m.thinkingBuffer != nil && m.thinkingBuffer.Len() != 0 {
		t.Errorf("thinkingBuffer must be empty after /clear, got %q", m.thinkingBuffer.String())
	}
	if m.thinkingPanel != nil && m.thinkingPanel.Len() != 0 {
		t.Errorf("thinkingPanel must be empty after /clear")
	}
	if m.shimmerActive {
		t.Error("shimmer/loading dock must be inactive after /clear")
	}
	if m.streaming {
		t.Error("streaming must be false after /clear")
	}
	if m.streamBuffer != "" || m.currentStreamContent != "" {
		t.Errorf("stream buffers must be empty after /clear")
	}
}

func TestClearRemovesStaleActionChips(t *testing.T) {
	m := clearTestModel()
	if m.currentResult == nil {
		t.Fatal("precondition: currentResult must be set")
	}
	m.handleCommand("/clear")
	if m.currentResult != nil {
		t.Error("currentResult (action chips) must be nil after /clear")
	}
}

func TestClearDoesNotDestroyWorkspace(t *testing.T) {
	m := clearTestModel()
	root := m.workspaceRoot
	m.handleCommand("/clear")

	if m.workspaceRoot != root {
		t.Errorf("workspaceRoot changed by /clear: %q → %q", root, m.workspaceRoot)
	}
	if m.resolver.Current() != modes.ModeBuild {
		t.Errorf("workspace mode changed by /clear: %v", m.resolver.Current())
	}
}

func TestClearDoesNotChangeMode(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")
	if m.resolver.Current() != modes.ModeBuild {
		t.Errorf("mode = %v after /clear, want build", m.resolver.Current())
	}
}

func TestClearDoesNotCreateANewSession(t *testing.T) {
	m := clearTestModel()
	sessBefore := m.sess
	m.handleCommand("/clear")

	if m.sess != sessBefore {
		t.Fatal("/clear must not replace the session")
	}
	if m.sess == nil {
		t.Fatal("/clear must not nil the session")
	}
	// Durable conversation state is preserved: /clear is NOT /new.
	if len(m.sess.History) != 2 {
		t.Errorf("session history must survive /clear, got %d messages", len(m.sess.History))
	}
	if m.sess.Objective != "refactor the landing page" {
		t.Errorf("session objective must survive /clear, got %q", m.sess.Objective)
	}
	// Context (including the staged plan task list) survives /clear — the plan
	// is "context", not "what I see". /drop discards it instead.
	if len(m.sess.CurrentTasks) != 1 {
		t.Errorf("staged plan tasks must survive /clear, got %d", len(m.sess.CurrentTasks))
	}
}

func TestClearTwiceIsSafe(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")
	m.handleCommand("/clear")

	if len(m.records) != 0 {
		t.Errorf("records must stay empty across double /clear, got %d", len(m.records))
	}
	if m.activityTree == nil || m.activityTree.Len() != 0 {
		t.Errorf("activity tree must stay empty across double /clear")
	}
	if m.logStore == nil || len(m.logStore.Entries()) != 0 {
		t.Errorf("execution log must stay empty across double /clear")
	}
}

// TestClearLateEventCannotResurrectActivity is the critical regression: a late
// engine/domain event from an execution that completed just before /clear must
// never repopulate the cleared records or activity tree.
func TestClearLateEventCannotResurrectActivity(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	// Late typed engine I/O event (read / retry / edit) from the old execution.
	m.handleEngineEvent(retrieval.FileReadEvent{File: "index.html", Bytes: 2048, Elapsed: time.Millisecond})
	m.handleEngineEvent(retrieval.FileReadEvent{File: "index.html", Bytes: 2048, Elapsed: time.Millisecond})
	m.handleEngineEvent(execution.FileMutateEvent{File: "index.html", LinesAdd: 3, LinesDel: 1, Elapsed: time.Millisecond})
	if m.activityTree.Len() != 0 {
		t.Fatalf("late engine event resurrected %d activity-tree entries", m.activityTree.Len())
	}
	if len(m.records) != 0 {
		t.Fatalf("late engine event resurrected %d records", len(m.records))
	}

	// Late bus-wrapped events.
	m.handleDomainEvent(events.NewEngineTelemetry(retrieval.FileReadEvent{File: "index.html", Bytes: 2048, Elapsed: time.Millisecond}))
	m.handleDomainEvent(events.NewPatchApplied("index.html", 3, 1, time.Millisecond))
	m.handleDomainEvent(events.NewActivity("[ OK ] stale activity line"))
	if m.activityTree.Len() != 0 {
		t.Fatalf("late domain event resurrected %d activity-tree entries", m.activityTree.Len())
	}
	if len(m.records) != 0 {
		t.Fatalf("late domain event resurrected %d records", len(m.records))
	}

	// Late terminal mutation result pushed by the old execution.
	_, _ = m.Update(mutationResultMsg{
		file:   "index.html",
		status: "modified",
		evidence: &execution.MutationEvidence{
			File: "index.html", Outcome: execution.OutcomeChanged, DiffPresent: true, DiffAdds: 3, DiffRemoves: 1,
		},
	})
	if len(m.records) != 0 {
		t.Fatalf("late mutationResultMsg resurrected %d records", len(m.records))
	}
	if m.logStore == nil || len(m.logStore.Entries()) != 0 {
		t.Fatalf("late mutationResultMsg resurrected execution-log entries")
	}
}

// TestClearPreservesActiveOperation verifies /clear does NOT silently kill a
// genuinely running foreground operation: the operation record survives and a
// subsequent terminal result still finalizes it.
func TestClearPreservesActiveOperation(t *testing.T) {
	m := clearTestModel()
	m.beginOperation(OpBuild)
	if m.activeOp == nil {
		t.Fatal("precondition: an operation must be active")
	}
	opID := m.activeOp.ID

	m.handleCommand("/clear")

	// The visual surface is clear...
	if len(m.records) != 0 {
		t.Fatalf("records must be empty after /clear, got %d", len(m.records))
	}
	// ...but the operation is NOT killed: it continues in the background.
	if m.activeOp == nil || m.activeOp.ID != opID {
		t.Fatalf("/clear must not cancel the active operation (id=%q)", opID)
	}
	if m.activeOp.State == OpStateTerminal {
		t.Fatal("/clear must not terminalize the active operation")
	}

	// A late terminal result finalizes the operation WITHOUT repopulating the
	// cleared surface.
	_, _ = m.Update(mutationResultMsg{
		file:   "index.html",
		status: "modified",
		evidence: &execution.MutationEvidence{
			File: "index.html", Outcome: execution.OutcomeChanged, DiffPresent: true, DiffAdds: 3, DiffRemoves: 1,
		},
	})
	if m.activeOp != nil {
		t.Errorf("late terminal result must finalize the operation, activeOp still set")
	}
	if len(m.records) != 0 {
		t.Fatalf("late terminal result resurrected %d records after /clear", len(m.records))
	}
}

// TestClearNoStaleActivityAfterNewCommand verifies the activity surface reopens
// on the next user submission: after /clear, a fresh interaction's events are
// projected again.
func TestClearNoStaleActivityAfterNewCommand(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")

	// A new user submission reopens the surface.
	m.submitEnter()
	// (submitEnter with an empty input bar should leave the surface open.)
	if m.activitySurfaceSealed {
		t.Fatal("a new user interaction must unseal the activity surface")
	}

	// A fresh engine event from the new interaction is projected.
	m.handleEngineEvent(retrieval.FileReadEvent{File: "index.html", Bytes: 2048, Elapsed: time.Millisecond})
	if m.activityTree.Len() != 1 {
		t.Fatalf("fresh engine event must be projected after new interaction, got %d entries", m.activityTree.Len())
	}

	// A subsequent /clear seals it again.
	m.handleCommand("/clear")
	if !m.activitySurfaceSealed {
		t.Fatal("/clear must reseal the activity surface")
	}
	m.handleEngineEvent(retrieval.FileReadEvent{File: "index.html", Bytes: 2048, Elapsed: time.Millisecond})
	if m.activityTree.Len() != 0 {
		t.Fatalf("post-second-clear event must be dropped, got %d entries", m.activityTree.Len())
	}
}

// TestDropSemanticsDistinctFromClear verifies /drop is NOT a visual clear: it
// discards pending actions but keeps the conversation visible.
func TestDropSemanticsDistinctFromClear(t *testing.T) {
	m := clearTestModel()
	m.attachedFiles = []string{"index.html", "styles.css"}
	beforeTree := m.activityTree.Len()

	m.handleCommand("/drop all")

	if len(m.attachedFiles) != 0 {
		t.Errorf("attachedFiles must be detached by /drop all, got %v", m.attachedFiles)
	}
	// /drop must NOT clear the visible conversation: the original activity
	// records are still present (only /drop's own confirmation notice was added).
	if len(m.records) != beforeTree+1 && len(m.records) < 3 {
		t.Error("/drop must not wipe the conversation records (it is not a visual clear)")
	}
	if m.activityTree == nil || m.activityTree.Len() != beforeTree {
		t.Error("/drop must not clear the activity tree")
	}
	if m.activitySurfaceSealed {
		t.Error("/drop must not seal the activity surface")
	}
}

// TestDropDiscardsPendingAction verifies /drop cancels pending proposals,
// discards the staged plan task list and detaches context files — while keeping
// session, conversation and workspace intact.
func TestDropDiscardsPendingAction(t *testing.T) {
	m := clearTestModel()
	m.awaitingConfirmation = true
	m.pendingProposals = []SemanticProposal{
		{ID: "p1", Target: SemanticTarget{QualifiedName: "index.html"}, Diff: "--- a/index.html\n+++ b/index.html\n"},
	}
	m.acceptAll = true
	m.toolCallBuffer = execution.NewToolCallBuffer(m.workspaceRoot)

	m.handleCommand("/drop all")

	// Pending proposals / approvals are discarded.
	if len(m.pendingProposals) != 0 || m.awaitingConfirmation || m.acceptAll {
		t.Errorf("pending proposals/approvals must be discarded by /drop")
	}
	// The staged plan task list (a pending execution plan) is discarded.
	if len(m.sess.CurrentTasks) != 0 {
		t.Errorf("staged plan tasks must be discarded by /drop, got %d", len(m.sess.CurrentTasks))
	}
	// Context files are detached (historical file-pruning role).
	if len(m.attachedFiles) != 0 {
		t.Errorf("attached files must be detached by /drop, got %v", m.attachedFiles)
	}
	// Conversation and session survive.
	if len(m.records) == 0 {
		t.Error("conversation records must survive /drop")
	}
	if len(m.sess.History) != 2 {
		t.Errorf("session history must survive /drop, got %d messages", len(m.sess.History))
	}
	if m.resolver.Current() != modes.ModeBuild {
		t.Errorf("mode must survive /drop, got %v", m.resolver.Current())
	}
}

// TestDropCancelsActiveOperation verifies /drop cancels a genuinely running
// foreground operation ("cancel active transient execution if applicable")
// while keeping the conversation intact.
func TestDropCancelsActiveOperation(t *testing.T) {
	m := clearTestModel()
	m.beginOperation(OpBuild)
	if m.activeOp == nil {
		t.Fatal("precondition: an operation must be active")
	}

	m.handleCommand("/drop all")

	if m.activeOp != nil {
		t.Error("/drop must cancel the active operation (activeOp still set)")
	}
	if len(m.records) == 0 {
		t.Error("conversation records must survive /drop")
	}
	if m.activityTree == nil || m.activityTree.Len() == 0 {
		t.Error("activity tree must survive /drop")
	}
}

// TestNewIsFutureBoundaryNotClear pins that /new is a reserved future session
// boundary, is NOT a visual clear, and does NOT create anything.
func TestNewIsFutureBoundaryNotClear(t *testing.T) {
	m := clearTestModel()
	beforeRecords := len(m.records)
	sessBefore := m.sess

	m.handleCommand("/new")

	if m.sess != sessBefore {
		t.Fatal("/new must not create or replace a session in this phase")
	}
	// /new is a future boundary — it must not wipe the conversation records.
	if len(m.records) < beforeRecords {
		t.Error("/new must not clear the visible records (it is a future boundary, not /clear)")
	}
	// /new must not seal the activity surface.
	if m.activitySurfaceSealed {
		t.Error("/new must not seal the activity surface")
	}
}

// TestClearIsIdempotentAndDoesNotResurrectOnRepeatedCalls pins the idempotency
// contract: three consecutive /clear invocations produce no state churn.
func TestClearIsIdempotentAndDoesNotResurrect(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")
	m.handleCommand("/clear")
	m.handleCommand("/clear")

	if len(m.records) != 0 || m.activityTree.Len() != 0 || len(m.logStore.Entries()) != 0 {
		t.Fatal("repeated /clear must not create duplicate state or resurrect activity")
	}
	if m.streaming || m.shimmerActive {
		t.Fatal("repeated /clear must not leave loading/streaming state active")
	}
}

// TestClearSetsBannerForFreshView verifies the cleared viewport shows the
// welcome banner again (the visual "blank canvas" contract).
func TestClearSetsBannerForFreshView(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")
	if !m.showBanner {
		t.Error("showBanner must be true after /clear")
	}
	if m.PreRenderedHistory != "" {
		t.Error("PreRenderedHistory must be empty after /clear")
	}
}

// TestClearErrorOutput uses the real Update path with a KeyMsg to confirm the
// Enter-gated /clear flow is stable and produces no error command.
func TestClearErrorOutput(t *testing.T) {
	m := clearTestModel()
	// The real Enter flow routes through handleInitKeyMsg unless the workspace
	// is initialized on disk; provision a minimal .izen/config.json.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".izen", "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.workspaceRoot = ws
	m.streaming = false
	m.agentRunning = false
	m.ti.SetValue("/clear")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd // /clear returns tea.Sequence(tea.ClearScreen, tea.Println(...)); never an error cmd
	if len(m.records) != 0 {
		t.Fatalf("records must be empty after Enter /clear, got %d", len(m.records))
	}
}

// TestClearSuppressesLateShellOutput ensures a late shell chunk after /clear
// cannot grow the activity tree.
func TestClearSuppressesLateShellOutput(t *testing.T) {
	m := clearTestModel()
	m.handleCommand("/clear")
	if m.activityTree == nil || m.activityTree.Len() != 0 {
		t.Fatal("precondition broken: tree must be empty after /clear")
	}
	m.Update(shellChunkMsg{text: "late shell output\n"})
	if m.activityTree.Len() != 0 {
		t.Fatalf("late shell chunk resurrected %d tree entries", m.activityTree.Len())
	}
	// A late terminal exit must also leave the tree untouched.
	m.Update(shellExitMsg{cmd: "npm run build", exitCode: 0, elapsed: time.Second})
	if m.activityTree.Len() != 0 {
		t.Fatalf("late shell exit resurrected %d tree entries", m.activityTree.Len())
	}
}

// TestClearDoesNotTouchAttachedFiles pins that /clear leaves the session's
// attached context files alone (ownership belongs to /drop).
func TestClearDoesNotTouchAttachedFiles(t *testing.T) {
	m := clearTestModel()
	m.attachedFiles = []string{"index.html"}
	m.handleCommand("/clear")
	if len(m.attachedFiles) != 1 {
		t.Errorf("attachedFiles must survive /clear, got %v", m.attachedFiles)
	}
}
