package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// staticGuard is a WorkspaceGuard test seam returning a fixed dirty set.
type staticGuard struct {
	files []string
	err   error
}

func (g *staticGuard) DirtyFiles(_ context.Context) ([]string, error) {
	return g.files, g.err
}

func guardFiles(t *testing.T, m *Manager, s SlotID) []string {
	t.Helper()
	sess, err := m.Inspect(s)
	if err != nil {
		t.Fatalf("Inspect(%s): %v", s, err)
	}
	return sess.WorkspaceDirtyFiles
}

// TestWorkspaceGuardInjectsDirtyFilesOnResume pins the workspace boundary
// guard: when uncommitted changes exist at a session switch, the dirty file
// references are injected into the TARGET session's view (and its compact
// context) so the model never silently overwrites work left by another session.
func TestWorkspaceGuardInjectsDirtyFilesOnResume(t *testing.T) {
	guard := &staticGuard{files: []string{"src/main.go", "README.md"}}
	m := openTestManager(t, newTestManager(t, WithWorkspaceGuard(guard)))

	// Create B so a resume target exists, then switch A -> B.
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.Active() != SlotB {
		t.Fatalf("Active = %q, want B after /new", m.Active())
	}

	if _, err := m.ResumeSession(context.Background(), SlotA); err != nil {
		t.Fatalf("ResumeSession A: %v", err)
	}
	if got := guardFiles(t, m, SlotA); !reflect.DeepEqual(got, []string{"src/main.go", "README.md"}) {
		t.Fatalf("slot A dirty files = %v, want injected guard output", got)
	}

	// The compact context (the Context Compiler's view) must carry them too.
	cc, err := m.CompactContext(SlotA)
	if err != nil || cc == nil {
		t.Fatalf("CompactContext(A): %v", err)
	}
	if !reflect.DeepEqual(cc.DirtyFiles, []string{"src/main.go", "README.md"}) {
		t.Fatalf("compact dirty files = %v, want injected", cc.DirtyFiles)
	}
}

// TestNewSessionCarriesDirtyFilesIntoFreshSession pins that a fresh /new
// session also receives the workspace dirt — a fresh session must never
// silently overwrite the previous session's uncommitted state.
func TestNewSessionCarriesDirtyFilesIntoFreshSession(t *testing.T) {
	guard := &staticGuard{files: []string{"src/main.go"}}
	m := openTestManager(t, newTestManager(t, WithWorkspaceGuard(guard)))

	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := guardFiles(t, m, m.Active()); !reflect.DeepEqual(got, []string{"src/main.go"}) {
		t.Fatalf("fresh session dirty files = %v, want injected", got)
	}
}

// TestWorkspaceGuardErrorIsNonFatal pins that a failing guard never blocks the
// switch (fail-open with observability): the pointer commits and the error is
// recorded as the last boundary error.
func TestWorkspaceGuardErrorIsNonFatal(t *testing.T) {
	guard := &staticGuard{err: os.ErrPermission}
	m := openTestManager(t, newTestManager(t, WithWorkspaceGuard(guard)))

	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession with failing guard: %v", err)
	}
	if m.Active() != SlotB {
		t.Fatalf("Active = %q, want B (switch must commit despite guard error)", m.Active())
	}
	if m.LastBoundaryErr() == nil {
		t.Fatal("LastBoundaryErr must record the guard failure")
	}
	if got := guardFiles(t, m, SlotB); len(got) != 0 {
		t.Fatalf("dirty files = %v, want empty on guard failure", got)
	}
}

// TestSessionRenameAtomicallyUpdatesTitle pins /session rename: the mutable
// title is persisted atomically in the slot's session.json while the ID stays
// immutable (SESSION.md §7).
func TestSessionRenameAtomicallyUpdatesTitle(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	origID := m.Session().SessionID

	if err := m.Rename(context.Background(), m.Active(), "  Redesign session lifecycle  "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	sess, err := m.Inspect(m.Active())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if sess.Title != "Redesign session lifecycle" {
		t.Fatalf("Title = %q, want trimmed title", sess.Title)
	}
	if sess.SessionID != origID {
		t.Fatalf("SessionID changed by rename: %q -> %q", origID, sess.SessionID)
	}

	// The in-memory active session is mirrored.
	if m.Session().Title != "Redesign session lifecycle" {
		t.Fatalf("live session title not mirrored: %q", m.Session().Title)
	}

	if err := m.Rename(context.Background(), m.Active(), "   "); err == nil {
		t.Fatal("Rename with a blank title must fail")
	}
}

// TestSessionArchiveLifecycle pins /session archive: ACTIVE -> ARCHIVED, an
// archived session is inspectable, resumable (ARCHIVED -> ACTIVE), and /new
// refuses to overwrite it without an explicit lifecycle command.
func TestSessionArchiveLifecycle(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	// Create B, archive the now-dormant A.
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := m.Archive(context.Background(), SlotA); err != nil {
		t.Fatalf("Archive A: %v", err)
	}
	if got := m.slotLifecycle(SlotA); got != LifecycleArchived {
		t.Fatalf("slot A lifecycle = %q, want archived", got)
	}
	sessA, err := m.Inspect(SlotA)
	if err != nil {
		t.Fatalf("Inspect archived A: %v", err)
	}
	if sessA.Lifecycle != LifecycleArchived {
		t.Fatalf("inspect lifecycle = %q, want archived", sessA.Lifecycle)
	}

	// /new must NOT silently overwrite the archived dormant slot.
	if _, err := m.NewSession(context.Background()); err == nil {
		t.Fatal("/new must refuse to overwrite an archived dormant slot")
	}
	if m.Active() != SlotB {
		t.Fatalf("Active = %q, want B (switch must not commit)", m.Active())
	}

	// Resume re-activates the archived session (SESSION.md §25).
	if _, err := m.ResumeSession(context.Background(), SlotA); err != nil {
		t.Fatalf("Resume archived A: %v", err)
	}
	if m.Active() != SlotA {
		t.Fatalf("Active = %q, want A after resume", m.Active())
	}
	if m.session.Lifecycle != LifecycleActive {
		t.Fatalf("resumed session lifecycle = %q, want active", m.session.Lifecycle)
	}

	// Archive is idempotent.
	if err := m.Archive(context.Background(), SlotA); err != nil {
		t.Fatalf("Archive A (active): %v", err)
	}
	if err := m.Archive(context.Background(), SlotA); err != nil {
		t.Fatalf("Archive A (idempotent): %v", err)
	}
}

// TestDeleteActiveSlotPreservesProjectState pins INV-SESSION-12: deleting the
// ACTIVE session purges only session-owned state, atomically moves the pointer
// to the sibling, and never touches project configuration or the global audit
// evidence.
func TestDeleteActiveSlotPreservesProjectState(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	// Seed project-owned state that deletion must NEVER touch.
	cfgPath := filepath.Join(m.root, ".izen", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"provider":"ollama"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(m.root, ".izen", "audit", "events.ndjson")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, []byte("{\"session_id\":\"sess-1\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create B so the pointer can move off A.
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.Active() != SlotB {
		t.Fatalf("Active = %q, want B", m.Active())
	}
	// B is now active; delete the dormant A first, then make B active and
	// delete it to exercise the active-slot path.
	if err := m.Delete(context.Background(), SlotA); err != nil {
		t.Fatalf("Delete dormant A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "A")); !os.IsNotExist(err) {
		t.Fatalf("slot A directory still exists after delete: %v", err)
	}

	if err := m.Delete(context.Background(), SlotB); err != nil {
		t.Fatalf("Delete active B: %v", err)
	}
	// The pointer must have atomically moved to A (the sibling) — never dangle.
	if m.Active() != SlotA {
		t.Fatalf("Active after deleting active slot = %q, want A", m.Active())
	}
	if got := pointerContent(t, m); got != "A" {
		t.Fatalf("pointer = %q, want A", got)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "B")); !os.IsNotExist(err) {
		t.Fatalf("slot B directory still exists after delete: %v", err)
	}

	// Project-owned state is byte-preserved.
	if got := readFileString(t, cfgPath); got != `{"provider":"ollama"}` {
		t.Fatalf("config.json altered by session delete: %q", got)
	}
	if got := readFileString(t, auditPath); got != "{\"session_id\":\"sess-1\"}\n" {
		t.Fatalf("audit evidence altered by session delete: %q", got)
	}
}

// TestDeleteDormantSlotPurgesOnlySessionState pins that deleting a dormant
// slot never touches project knowledge or the sibling session.
func TestDeleteDormantSlotPurgesOnlySessionState(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	// Seed project knowledge (granular chunks) that must survive.
	knowledgePath := filepath.Join(m.root, ".izen", "knowledge", "chunk-1.json")
	if err := os.MkdirAll(filepath.Dir(knowledgePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knowledgePath, []byte(`{"title":"runtime authority"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := m.Delete(context.Background(), SlotA); err != nil {
		t.Fatalf("Delete dormant A: %v", err)
	}

	if _, err := os.Stat(knowledgePath); err != nil {
		t.Fatalf("project knowledge removed by session delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "B", sessionFile)); err != nil {
		t.Fatalf("sibling session removed by session delete: %v", err)
	}
	// Deleting a non-existent slot must be idempotent-safe (RemoveAll no-op).
	if err := m.Delete(context.Background(), SlotA); err != nil {
		t.Fatalf("Delete A again: %v", err)
	}
}

// TestInspectReturnsDetachedRecord pins that /session inspect never exposes the
// live in-memory session pointer: mutations of the returned record cannot leak
// into the manager's active session.
func TestInspectReturnsDetachedRecord(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	ctx := context.Background()
	live := m.Session()
	live.Objective = "the live goal"
	if err := m.Persist(ctx); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	detached, err := m.Inspect(m.Active())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if detached.Objective != "the live goal" {
		t.Fatalf("detached objective = %q, want persisted goal", detached.Objective)
	}
	detached.Objective = "mutated by inspector"
	if m.Session().Objective != "the live goal" {
		t.Fatalf("inspector mutation leaked into the live session: %q", m.Session().Objective)
	}
}

// TestManualCompactSeam verifies the manual /session compact trigger: the
// Manager exposes the current generation and the runner sinks a refreshed
// generation through SetCompactContext without touching raw history.
func TestManualCompactSeam(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	ctx := context.Background()

	// Build the session's windowed history through the canonical mutation path
	// (AddMessage + Persist) so both session.json and the derived compact carry
	// the turns.
	m.Session().AddMessage("user", "refactor the session layer", m.maxTurns)
	m.Session().AddMessage("assistant", "separate session state from project state", m.maxTurns)
	if err := m.Persist(ctx); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	sess, err := m.Inspect(m.Active())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(sess.History) != 2 {
		t.Fatalf("inspect history = %d entries, want 2 (raw history preserved)", len(sess.History))
	}

	// SetCompactContext must accept a manual generation (nil-base full fold).
	fresh := &CompactContext{
		Version:     compactContextVersion,
		SessionID:   sess.SessionID,
		Objective:   sess.ObjectiveIntent(),
		Generation:  1,
		EventCount:  2,
		TurnCount:   2,
		Recent:      append([]Message(nil), sess.History...),
		CreatedAt:   sess.CreatedAt,
		UpdatedAt:   time.Now(),
		CompactedAt: time.Now(),
	}
	if err := m.SetCompactContext(ctx, m.Active(), fresh); err != nil {
		t.Fatalf("SetCompactContext: %v", err)
	}
	got, err := m.CompactContext(m.Active())
	if err != nil || got == nil {
		t.Fatalf("re-read CompactContext: %v", err)
	}
	if got.EventCount != 2 || got.Generation != 1 {
		t.Fatalf("compacted generation = %+v, want event_count 2 gen 1", got)
	}
}
