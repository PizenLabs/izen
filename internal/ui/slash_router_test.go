package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/presentation"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/session/compaction"
)

// recordingSubmitBridge records every SubmitPromptCmd the Application-layer
// runtime receives, so tests can prove slash commands NEVER degrade into a
// prompt submission (Slash Command Fallthrough Guard). It is a real
// CommandDispatcher with a recording handler bound to a real presentation
// Bridge — the same wiring `runtimeSubmitCmd` crosses.
type recordingSubmitBridge struct {
	mu      sync.Mutex
	pres    *presentation.Bridge
	submits []appruntime.SubmitPromptCmd
}

func (b *recordingSubmitBridge) Handle(_ context.Context, cmd appruntime.RuntimeCommand) error {
	if c, ok := cmd.(appruntime.SubmitPromptCmd); ok {
		b.mu.Lock()
		b.submits = append(b.submits, c)
		b.mu.Unlock()
	}
	return nil
}

func (b *recordingSubmitBridge) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.submits)
}

func newRecordingSubmitBridge() *recordingSubmitBridge {
	b := &recordingSubmitBridge{}
	disp := appruntime.NewCommandDispatcher()
	_ = disp.Register(appruntime.CommandSubmitPrompt, appruntime.HandlerFunc(b.Handle))
	b.pres = presentation.New(appruntime.NewRuntime(disp))
	return b
}

// slashRouterTestModel wires the strict-router surfaces: a real dual-slot
// SessionManager + Generational Compactor (SessionManager APIs, never bypassed)
// and a recording Application-layer runtime bridge.
func slashRouterTestModel(t *testing.T) (*model, *session.Manager, *recordingSubmitBridge, string) {
	t.Helper()
	root := t.TempDir()
	sm := session.NewManager(root,
		session.WithLockConfig(session.LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}),
	)
	if err := sm.Open(context.Background()); err != nil {
		t.Fatalf("open session manager: %v", err)
	}
	t.Cleanup(func() { _ = sm.Close() })

	runner := compaction.NewRunner(compaction.DefaultPolicy(),
		func(ctx context.Context, j compaction.Job, cc *session.CompactContext) error {
			return sm.SetCompactContext(ctx, j.Slot, cc)
		})
	runner.Start()
	t.Cleanup(runner.Close)

	br := newRecordingSubmitBridge()

	m := newTestModel()
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.workspaceRoot = root
	m.gitEng = git.NewEngine(root)
	m.sessionManager = sm
	m.compactionRunner = runner
	m.sess = sm.Session()
	m.pres = br.pres
	return m, sm, br, root
}

// executeBatch invokes a tea.Cmd and drains any BatchMsg/SequenceMsg it yields,
// recursively executing every sub-command synchronously — the same dispatch the
// Bubble Tea update loop performs. This is what makes the Application-layer
// runtime submissions observable in a test without running the event loop.
func executeBatch(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	drainBatch(t, cmd())
}

func drainBatch(t *testing.T, msg tea.Msg) {
	t.Helper()
	if m, ok := msg.(tea.BatchMsg); ok {
		for _, c := range m {
			if c == nil {
				continue
			}
			drainBatch(t, c())
		}
	}
}

// TestSlashRouterNewExecutesSessionManagerWithoutSubmit is the DoD #1
// assertion: `/new` through the interactive Enter path executes
// SessionManager.NewSession with ZERO parser errors and ZERO submit_prompt
// emissions (no IntentGateway, no background prompt workers).
func TestSlashRouterNewExecutesSessionManagerWithoutSubmit(t *testing.T) {
	m, sm, br, _ := slashRouterTestModel(t)
	before := sm.Active()

	m.ti.SetValue("/new")
	_, cmd := m.submitEnter()
	executeBatch(t, cmd)

	if sm.Active() == before {
		t.Fatal("/new must switch the active session via SessionManager")
	}
	if br.count() != 0 {
		t.Fatalf("/new emitted %d SubmitPromptCmd(s), want 0 (Slash Command Fallthrough Guard)", br.count())
	}
	// The parser must no longer reject /new: no "parser: unknown command" error
	// was pushed, and a valid session record exists.
	if errText := lastErrorText(m); strings.Contains(errText, "parser: unknown command") {
		t.Fatalf("/new surfaced a parser error: %q", errText)
	}
	if sm.Session() == nil || sm.Session().SessionID == "" {
		t.Fatal("/new did not produce a valid session")
	}
}

// TestSlashRouterUnknownCommandFailsFastWithoutSideEffects is the DoD #2
// assertion: an invalid slash command surfaces a UI error and terminates the
// turn completely — no prompt event, no IntentGateway, no runtime submission.
func TestSlashRouterUnknownCommandFailsFastWithoutSideEffects(t *testing.T) {
	m, sm, br, _ := slashRouterTestModel(t)
	activeBefore := sm.Active()

	m.ti.SetValue("/unknown_cmd")
	_, cmd := m.submitEnter()
	executeBatch(t, cmd)

	if errText := lastErrorText(m); errText == "" {
		t.Fatal("invalid slash command must surface a UI error")
	} else if !strings.Contains(errText, "unknown command") && !strings.Contains(errText, "unknown_cmd") {
		t.Fatalf("error does not name the unknown command: %q", errText)
	}
	if br.count() != 0 {
		t.Fatalf("/unknown_cmd emitted %d SubmitPromptCmd(s), want 0", br.count())
	}
	// The turn terminated with zero session side effects.
	if sm.Active() != activeBefore {
		t.Fatal("/unknown_cmd must not switch the session")
	}
	// No prompt admission / intent classification artifacts were produced.
	for _, r := range m.records {
		if strings.Contains(r.text, "intent parsed") || strings.Contains(r.text, "PromptAdmitted") {
			t.Fatalf("invalid slash command produced a prompt-classification artifact: %q", r.text)
		}
	}
}

// TestSlashRouterNonSlashStillSubmitsPrompt pins the guard's complement: a
// free-text (non-slash) input STILL crosses the Application-layer runtime as a
// submit_prompt. The strict rule only owns slash-prefixed input.
func TestSlashRouterNonSlashStillSubmitsPrompt(t *testing.T) {
	m, _, br, _ := slashRouterTestModel(t)

	m.ti.SetValue("what is Go?")
	_, cmd := m.submitEnter()
	executeBatch(t, cmd)

	if br.count() != 1 {
		t.Fatalf("free text emitted %d SubmitPromptCmd(s), want 1", br.count())
	}
	if got := br.submits[0].Prompt; got != "what is Go?" {
		t.Fatalf("submitted prompt = %q, want the original line", got)
	}
}

// TestSlashRouterSessionSubcommandsPreserveArguments pins the dispatchASTIntent
// args-preservation fix: a single global slash command is routed with its FULL
// original line, so every /session subcommand reaches session_cmds.go with its
// arguments intact.
func TestSlashRouterSessionSubcommandsPreserveArguments(t *testing.T) {
	m, sm, _, _ := slashRouterTestModel(t)

	// Create B so resume has a target.
	if _, err := sm.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	m.sess = sm.Session()

	// /session resume A — the FULL line must reach the resume handler.
	if cmd := m.handleInput("/session resume A"); cmd != nil {
		t.Fatalf("handleInput(/session resume A) returned a cmd, want nil (synchronous resume)")
	}
	if sm.Active() != session.SlotA {
		t.Fatalf("active after /session resume A = %q, want A (args must survive dispatch)", sm.Active())
	}

	// /session inspect A — structured metadata rendered.
	if cmd := m.handleInput("/session inspect A"); cmd != nil {
		t.Fatalf("handleInput(/session inspect A) returned a cmd, want nil")
	}
	text := lastSystemText(m)
	if !strings.Contains(text, `"slot": "A"`) {
		t.Fatalf("/session inspect A output missing slot metadata: %q", text)
	}

	// /session rename A <title> — title persisted atomically.
	if cmd := m.handleInput("/session rename A Strict Router"); cmd != nil {
		t.Fatalf("handleInput(/session rename A) returned a cmd, want nil")
	}
	sess, err := sm.Inspect(session.SlotA)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if sess.Title != "Strict Router" {
		t.Fatalf("renamed title = %q, want Strict Router", sess.Title)
	}

	// /session archive B — lifecycle transition.
	if cmd := m.handleInput("/session archive B"); cmd != nil {
		t.Fatalf("handleInput(/session archive B) returned a cmd, want nil")
	}
	sessB, err := sm.Inspect(session.SlotB)
	if err != nil {
		t.Fatalf("Inspect B: %v", err)
	}
	if sessB.Lifecycle != session.LifecycleArchived {
		t.Fatalf("slot B lifecycle = %q, want archived", sessB.Lifecycle)
	}

	// /session compact A — generation sealed through the runner + manager sink.
	if cmd := m.handleInput("/session compact A"); cmd != nil {
		t.Fatalf("handleInput(/session compact A) returned a cmd, want nil")
	}
	cc, err := sm.CompactContext(session.SlotA)
	if err != nil || cc == nil {
		t.Fatalf("CompactContext after /session compact: %v", err)
	}
	if cc.Generation < 1 {
		t.Fatalf("compacted generation = %d, want >= 1", cc.Generation)
	}

	// /session delete B — purges session-owned state only.
	if cmd := m.handleInput("/session delete B"); cmd != nil {
		t.Fatalf("handleInput(/session delete B) returned a cmd, want nil")
	}
	if sessB, err = sm.Inspect(session.SlotB); err == nil {
		t.Fatalf("slot B still inspectable after /session delete: %q", sessB.SessionID)
	}
}

// TestSlashRouterBareSessionLists pins that a bare /session still routes to the
// list handler.
func TestSlashRouterBareSessionLists(t *testing.T) {
	m, _, _, _ := slashRouterTestModel(t)
	if cmd := m.handleInput("/session"); cmd != nil {
		t.Fatalf("handleInput(/session) returned a cmd, want nil")
	}
	text := lastSystemText(m)
	if !strings.Contains(text, "slot A") {
		t.Fatalf("/session output missing slot listing: %q", text)
	}
}
