package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/session/compaction"
)

// sessionCLITestModel wires a real dual-slot SessionManager and the async
// Generational Compactor into a test model, exactly like the composition root.
// It returns the model, the manager and the workspace root.
func sessionCLITestModel(t *testing.T) (*model, *session.Manager, string) {
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

	m := newTestModel()
	m.state = StateChat
	m.streaming = false
	m.agentRunning = false
	m.workspaceRoot = root
	m.gitEng = git.NewEngine(root)
	m.sessionManager = sm
	m.compactionRunner = runner
	m.sess = sm.Session()
	return m, sm, root
}

func lastSystemText(m *model) string {
	for i := len(m.records) - 1; i >= 0; i-- {
		if m.records[i].role == roleSystem {
			return m.records[i].text
		}
	}
	return ""
}

func lastErrorText(m *model) string {
	for i := len(m.records) - 1; i >= 0; i-- {
		if m.records[i].role == roleError {
			return m.records[i].text
		}
	}
	return ""
}

// TestSessionListRendersBothSlots pins /session list through the manager.
func TestSessionListRendersBothSlots(t *testing.T) {
	m, _, _ := sessionCLITestModel(t)
	m.handleCommand("/session")
	text := lastSystemText(m)
	if !strings.Contains(text, "slot A") || !strings.Contains(text, "slot B") {
		t.Fatalf("/session list output missing slots: %q", text)
	}
}

// TestSessionInspectRendersStructuredMetadata pins /session inspect: the JSON
// view carries the session identity, goal, lifecycle and artifact references.
func TestSessionInspectRendersStructuredMetadata(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	sm.Session().Objective = "redesign the session lifecycle"
	if err := sm.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	m.handleCommand("/session inspect A")
	text := lastSystemText(m)
	if !strings.Contains(text, "redesign the session lifecycle") {
		t.Fatalf("inspect output missing goal: %q", text)
	}
	// The JSON document must parse and carry the immutable session id.
	jsonStart := strings.Index(text, "{")
	if jsonStart < 0 {
		t.Fatalf("inspect output has no JSON document: %q", text)
	}
	var view map[string]interface{}
	if err := json.Unmarshal([]byte(text[jsonStart:]), &view); err != nil {
		t.Fatalf("inspect JSON invalid: %v\n%s", err, text[jsonStart:])
	}
	if view["session_id"] == "" || view["slot"] != "A" {
		t.Fatalf("inspect view incomplete: %v", view)
	}
}

// TestSessionRenameUpdatesTitle pins /session rename through the CLI.
func TestSessionRenameUpdatesTitle(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	m.handleCommand("/session rename A Redesign session lifecycle")
	if errText := lastErrorText(m); errText != "" {
		t.Fatalf("rename error: %s", errText)
	}
	sess, err := sm.Inspect(session.SlotA)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if sess.Title != "Redesign session lifecycle" {
		t.Fatalf("Title = %q, want renamed title", sess.Title)
	}
}

// TestSessionArchiveViaCLI pins /session archive transitions the dormant slot
// to ARCHIVED and /new refuses to overwrite it.
func TestSessionArchiveViaCLI(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	if _, err := sm.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	m.sess = sm.Session()

	m.handleCommand("/session archive A")
	if errText := lastErrorText(m); errText != "" {
		t.Fatalf("archive error: %s", errText)
	}
	sess, err := sm.Inspect(session.SlotA)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if sess.Lifecycle != session.LifecycleArchived {
		t.Fatalf("slot A lifecycle = %q, want archived", sess.Lifecycle)
	}

	// /new must surface the archived slot instead of silently overwriting it.
	if _, err := sm.NewSession(context.Background()); err == nil {
		t.Fatal("/new must refuse to overwrite an archived dormant slot")
	}
}

// TestSessionCompactViaCLI pins /session compact seals a generation through the
// runner and the manager's atomic sink.
func TestSessionCompactViaCLI(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	sm.Session().AddMessage("user", "refactor the session layer", 5)
	sm.Session().AddMessage("assistant", "separate session state from project state", 5)
	if err := sm.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	m.handleCommand("/session compact A")
	if errText := lastErrorText(m); errText != "" {
		t.Fatalf("compact error: %s", errText)
	}
	if text := lastSystemText(m); !strings.Contains(text, "generation") {
		t.Fatalf("compact output missing generation: %q", text)
	}
	cc, err := sm.CompactContext(session.SlotA)
	if err != nil || cc == nil {
		t.Fatalf("CompactContext after /session compact: %v", err)
	}
	if cc.EventCount != 2 {
		t.Fatalf("compacted event count = %d, want 2", cc.EventCount)
	}
}

// TestSessionDeleteViaCLI pins /session delete purges session-owned state while
// project-owned state survives (INV-SESSION-12).
func TestSessionDeleteViaCLI(t *testing.T) {
	m, sm, root := sessionCLITestModel(t)

	cfgPath := filepath.Join(root, ".izen", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"provider":"ollama"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	m.sess = sm.Session()

	m.handleCommand("/session delete A")
	if errText := lastErrorText(m); errText != "" {
		t.Fatalf("delete error: %s", errText)
	}
	if _, err := os.Stat(filepath.Join(root, ".izen", "sessions", "A")); !os.IsNotExist(err) {
		t.Fatalf("slot A still exists after delete: %v", err)
	}
	if got := readTestFile(t, cfgPath); got != `{"provider":"ollama"}` {
		t.Fatalf("project config altered by session delete: %q", got)
	}
	// The model must mirror the surviving active session (never a purged one).
	if m.sess == nil || m.sess.SessionID == "" {
		t.Fatal("model session mirror invalid after deleting the active slot")
	}
}

// TestSessionInvalidSlotRefusesCleanly pins unknown slot arguments fail with a
// usage error instead of panicking.
func TestSessionInvalidSlotRefusesCleanly(t *testing.T) {
	m, _, _ := sessionCLITestModel(t)
	m.handleCommand("/session inspect X")
	if errText := lastErrorText(m); !strings.Contains(errText, "invalid session") {
		t.Fatalf("expected invalid-session error, got: %q", errText)
	}
	m.handleCommand("/session resume A")
	if errText := lastErrorText(m); errText != "" {
		t.Fatalf("resume error: %s", errText)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
