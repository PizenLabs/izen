package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/modes"
)

// newTestManager builds a Manager over a fresh temp workspace with a short
// lock timeout so contention tests fail fast.
func newTestManager(t *testing.T, opts ...Option) *Manager {
	t.Helper()
	root := t.TempDir()
	opts = append([]Option{
		WithLockConfig(LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}),
	}, opts...)
	m := NewManager(root, opts...)
	return m
}

// openTestManager opens a manager and fails the test on error.
func openTestManager(t *testing.T, m *Manager) *Manager {
	t.Helper()
	if err := m.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func pointerContent(t *testing.T, m *Manager) string {
	t.Helper()
	return strings.TrimSpace(readFileString(t, filepath.Join(m.dir, activeFile)))
}

// TestOpenFreshWorkspaceBootstrapsSlotA verifies a first run creates a valid
// pointer (A active), a durable session record, and a non-empty session id.
func TestOpenFreshWorkspaceBootstrapsSlotA(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	if got := m.Active(); got != SlotA {
		t.Errorf("Active = %q, want A on first run", got)
	}
	if got := pointerContent(t, m); got != "A" {
		t.Errorf("pointer = %q, want A", got)
	}
	if m.Session() == nil {
		t.Fatal("Session() = nil after Open")
	}
	if m.Session().SessionID == "" {
		t.Error("fresh session must carry a SessionID")
	}
	if _, err := os.Stat(filepath.Join(m.dir, "A", sessionFile)); err != nil {
		t.Errorf("A/session.json not persisted: %v", err)
	}
	if m.PointerRecovered() {
		t.Error("PointerRecovered should be false on a clean first run")
	}
}

// TestNewSessionSwitchesSlotAndPreservesPrevious is the /new happy path: the
// current session is persisted to its (now dormant) slot, a fresh session is
// created, and the pointer atomically names the new slot.
func TestNewSessionSwitchesSlotAndPreservesPrevious(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	first := m.Session()
	first.Objective = "first session objective"
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	second, err := m.NewSession(context.Background())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.Active() != SlotB {
		t.Errorf("Active = %q, want B after /new", m.Active())
	}
	if got := pointerContent(t, m); got != "B" {
		t.Errorf("pointer = %q, want B", got)
	}
	if second == first {
		t.Error("NewSession must return a distinct session")
	}
	if second.Objective != "" {
		t.Errorf("fresh session objective = %q, want empty", second.Objective)
	}

	// The previous session must be preserved in its dormant slot.
	var prev Session
	data := readFileString(t, filepath.Join(m.dir, "A", sessionFile))
	if err := json.Unmarshal([]byte(data), &prev); err != nil {
		t.Fatalf("decode A/session.json: %v", err)
	}
	if prev.Objective != "first session objective" {
		t.Errorf("dormant A objective = %q, want preserved", prev.Objective)
	}
}

// TestResumeSessionHandshake covers validation, idempotent no-op, and switch.
func TestResumeSessionHandshake(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	first := m.Session()
	first.Objective = "objective-A"
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	second := m.Session()
	second.Objective = "objective-B"
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Resume A.
	resumed, err := m.ResumeSession(context.Background(), SlotA)
	if err != nil {
		t.Fatalf("ResumeSession(A): %v", err)
	}
	if m.Active() != SlotA || pointerContent(t, m) != "A" {
		t.Fatalf("active = %q/%q, want A", m.Active(), pointerContent(t, m))
	}
	if resumed.Objective != "objective-A" {
		t.Errorf("resumed objective = %q, want objective-A", resumed.Objective)
	}

	// Resume A again → idempotent no-op (same session pointer).
	again, err := m.ResumeSession(context.Background(), SlotA)
	if err != nil {
		t.Fatalf("ResumeSession(A) repeat: %v", err)
	}
	if again != resumed {
		t.Error("resuming the active slot must be an idempotent no-op returning the same session")
	}

	// Invalid slot.
	if _, err := m.ResumeSession(context.Background(), SlotID("C")); err == nil {
		t.Error("ResumeSession(C) must fail for an invalid slot id")
	}

	// Valid slot with no recoverable data.
	m2 := openTestManager(t, newTestManager(t)) // only A exists with fresh data
	if _, err := m2.ResumeSession(context.Background(), SlotB); err == nil {
		t.Error("ResumeSession(B) must fail when B has no recoverable session data")
	}
}

// TestPersistWritesFullDurableSet verifies session.json + derived compact
// context + checkpoint marker are all written atomically.
func TestPersistWritesFullDurableSet(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	s := m.Session()
	s.Objective = "objective"
	s.AddMessage("user", "hello", 5)
	s.AddCheckpoint("cp-1")
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	var cc CompactContext
	data := readFileString(t, filepath.Join(m.dir, "A", compactContextFileName))
	if err := json.Unmarshal([]byte(data), &cc); err != nil {
		t.Fatalf("decode context.json: %v", err)
	}
	if cc.Objective != "objective" || cc.Checkpoint != "cp-1" || cc.TurnCount != 1 {
		t.Errorf("compact context = %+v, want objective/checkpoint/turncount", cc)
	}

	var marker CheckpointMarker
	data = readFileString(t, filepath.Join(m.dir, "A", CheckpointMarkerFileName))
	if err := json.Unmarshal([]byte(data), &marker); err != nil {
		t.Fatalf("decode checkpoint.json: %v", err)
	}
	if marker.ID != "cp-1" {
		t.Errorf("checkpoint marker ID = %q, want cp-1", marker.ID)
	}

	// No leftover tmp files after a clean persist.
	if _, err := os.Stat(filepath.Join(m.dir, sessionFile+".tmp")); !os.IsNotExist(err) {
		t.Error("session.json.tmp left behind after atomic persist")
	}
}

// TestListProjectsSlots verifies the /session listing projection.
func TestListProjectsSlots(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	m.Session().Objective = "objective"
	_ = m.Persist(context.Background())

	infos := m.List(context.Background())
	if len(infos) != 2 {
		t.Fatalf("List returned %d slots, want 2", len(infos))
	}
	var a, b *SlotInfo
	for i := range infos {
		if infos[i].Slot == SlotA {
			a = &infos[i]
		}
		if infos[i].Slot == SlotB {
			b = &infos[i]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("List must project both slots, got %+v", infos)
	}
	if !a.Active || b.Active {
		t.Errorf("A active=%v B active=%v, want A active", a.Active, b.Active)
	}
	if a.Objective != "objective" {
		t.Errorf("A objective = %q, want objective", a.Objective)
	}
	if a.SessionID == "" {
		t.Error("A SessionID must be populated")
	}
}

// TestSessionSaveRefreshesCompactContext verifies the existing UI Save()
// machinery (path-bound session) keeps the compact context fresh.
func TestSessionSaveRefreshesCompactContext(t *testing.T) {
	m := openTestManager(t, newTestManager(t))

	s := m.Session()
	s.Objective = "saved objective"
	if err := s.Save(); err != nil {
		t.Fatalf("Session.Save: %v", err)
	}
	var cc CompactContext
	data := readFileString(t, filepath.Join(m.dir, "A", compactContextFileName))
	if err := json.Unmarshal([]byte(data), &cc); err != nil {
		t.Fatalf("decode context.json after Save: %v", err)
	}
	if cc.Objective != "saved objective" {
		t.Errorf("compact context objective = %q, want saved objective", cc.Objective)
	}
}

// crashAt returns a crash hook that aborts once at the given point with a
// sentinel error.
func crashAt(point CrashPoint) func(CrashPoint) error {
	armed := true
	return func(p CrashPoint) error {
		if armed && p == point {
			armed = false
			return errors.New("simulated crash")
		}
		return nil
	}
}

// assertInvariantState verifies the universal post-crash invariant: the
// pointer names exactly one of A/B, and the named slot holds a valid session
// record. It opens a fresh manager (a new "process") over the same root,
// carrying the same policy options into the reopened process.
func assertInvariantState(t *testing.T, root string, wantActive SlotID, opts ...Option) *Manager {
	t.Helper()
	opts = append([]Option{
		WithLockConfig(LockConfig{Timeout: 2 * time.Second, Backoff: 5 * time.Millisecond}),
	}, opts...)
	reopened := NewManager(root, opts...)
	if err := reopened.Open(context.Background()); err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	if got := reopened.Active(); got != wantActive {
		t.Errorf("post-crash Active = %q, want %q (forbidden pointer state)", got, wantActive)
	}
	if got := reopened.Active(); !validSlot(got) {
		t.Errorf("post-crash pointer names invalid slot %q", got)
	}
	if reopened.Session() == nil {
		t.Fatal("post-crash session must never be nil (Active->Nonexistent forbidden)")
	}
	return reopened
}

// TestCrashAfterPersistActive: /new aborts after persisting the active slot.
// The pointer must still name the old slot; recovery converges there.
func TestCrashAfterPersistActive(t *testing.T) {
	m := newTestManager(t, withCrashHook(crashAt(CrashAfterPersistActive)))
	openTestManager(t, m)

	m.Session().Objective = "survives"
	_ = m.Persist(context.Background())

	if _, err := m.NewSession(context.Background()); err == nil {
		t.Fatal("NewSession must fail at the simulated crash point")
	}
	if m.Active() != SlotA {
		t.Errorf("Active = %q after aborted /new, want A", m.Active())
	}
	if got := pointerContent(t, m); got != "A" {
		t.Errorf("pointer = %q, want A", got)
	}

	reopened := assertInvariantState(t, m.root, SlotA)
	if reopened.Session().Objective != "survives" {
		t.Errorf("post-crash objective = %q, want survives", reopened.Session().Objective)
	}
}

// TestCrashAfterPrepareNew: /new aborts after preparing the dormant slot. The
// pointer must still name the old slot; the orphaned dormant data is harmless.
func TestCrashAfterPrepareNew(t *testing.T) {
	m := newTestManager(t, withCrashHook(crashAt(CrashAfterPrepareNew)))
	openTestManager(t, m)

	m.Session().Objective = "still active"
	_ = m.Persist(context.Background())

	if _, err := m.NewSession(context.Background()); err == nil {
		t.Fatal("NewSession must fail at the simulated crash point")
	}
	if m.Active() != SlotA {
		t.Errorf("Active = %q after aborted /new, want A", m.Active())
	}
	if got := pointerContent(t, m); got != "A" {
		t.Errorf("pointer = %q, want A", got)
	}

	// The dormant slot may hold an orphan prepared record, but the pointer
	// decision is authoritative: A stays active.
	assertInvariantState(t, m.root, SlotA)
}

// TestCrashAfterPointerTmp: crash between writing active.tmp and the rename.
// Recovery must discard the orphan tmp and keep the OLD pointer.
func TestCrashAfterPointerTmp(t *testing.T) {
	m := newTestManager(t, withCrashHook(crashAt(CrashAfterPointerTmp)))
	openTestManager(t, m)

	m.Session().Objective = "before-commit"
	_ = m.Persist(context.Background())

	if _, err := m.NewSession(context.Background()); err == nil {
		t.Fatal("NewSession must fail at the simulated crash point")
	}

	// The interruptible window left an orphan staging file.
	if _, err := os.Stat(filepath.Join(m.dir, activeTmpFile)); err != nil {
		t.Errorf("active.tmp should exist after the interrupted commit: %v", err)
	}
	if got := pointerContent(t, m); got != "A" {
		t.Errorf("pointer = %q, want A (rename never happened)", got)
	}

	reopened := assertInvariantState(t, m.root, SlotA)
	if _, err := os.Stat(filepath.Join(m.dir, activeTmpFile)); !os.IsNotExist(err) {
		t.Errorf("recovery must discard the orphan active.tmp, got %v", err)
	}
	if reopened.Session().Objective != "before-commit" {
		t.Errorf("post-crash objective = %q, want before-commit", reopened.Session().Objective)
	}
}

// TestCrashAfterPointerCommit: crash exactly at the atomic boundary. The
// pointer HAS switched to B; the switch is complete and valid.
func TestCrashAfterPointerCommit(t *testing.T) {
	m := newTestManager(t, withCrashHook(crashAt(CrashAfterPointerCommit)))
	openTestManager(t, m)

	m.Session().Objective = "first"
	_ = m.Persist(context.Background())

	sess, err := m.NewSession(context.Background())
	if err != nil {
		t.Fatalf("NewSession must treat the post-commit crash as a completed switch: %v", err)
	}
	if sess != nil && m.Active() != SlotB {
		t.Errorf("Active = %q, want B (the atomic commit boundary)", m.Active())
	}
	assertInvariantState(t, m.root, SlotB)
}

// TestCrashPointerToWipedSlot: external corruption deletes the active slot's
// durable data. The pointer must remain authoritative and the slot must be
// repaired to a recoverable (fresh) session — Active->Nonexistent is forbidden.
func TestCrashPointerToWipedSlot(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	m.Session().Objective = "about-to-wipe"
	_ = m.Persist(context.Background())
	// Populate B too so both slots have data.
	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = m.Session().Save()

	// External wipe: point at B and delete every durable B artifact.
	_ = writeFileAtomic(filepath.Join(m.dir, activeFile), []byte("B\n"))
	for _, f := range []string{sessionFile, compactContextFileName, RawHistoryFileName, CheckpointMarkerFileName} {
		_ = os.Remove(filepath.Join(m.dir, "B", f))
	}

	reopened := assertInvariantState(t, m.root, SlotB)
	if reopened.Session() == nil {
		t.Fatal("wiped slot must be repaired to a recoverable session")
	}
}

// TestCrashCorruptActivePointer: the pointer file itself is corrupted (the one
// state the rename protocol cannot produce). Recovery must default to a valid
// slot and rewrite the pointer atomically.
func TestCrashCorruptActivePointer(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	m.Session().Objective = "corrupt-pointer"
	_ = m.Persist(context.Background())

	_ = os.WriteFile(filepath.Join(m.dir, activeFile), []byte("not-a-slot\x00\x01"), 0o644)

	reopened := assertInvariantState(t, m.root, SlotA)
	if !reopened.PointerRecovered() {
		t.Error("PointerRecovered must be true after repairing a corrupt pointer")
	}
	if got := pointerContent(t, reopened); got != "A" {
		t.Errorf("recovered pointer = %q, want A", got)
	}
	if reopened.Session().Objective != "corrupt-pointer" {
		t.Errorf("objective = %q, want corrupt-pointer", reopened.Session().Objective)
	}
}

// TestInvSession14CorruptCompactContext: corrupting the compact context alone
// must not render the session unrecoverable — the authoritative record loads
// and the compact context is re-derived.
func TestInvSession14CorruptCompactContext(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	m.Session().Objective = "inv14"
	m.Session().AddMessage("user", "hello", 5)
	if err := m.Session().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_ = os.WriteFile(filepath.Join(m.dir, "A", compactContextFileName), []byte("{corrupt!!!"), 0o644)

	reopened := assertInvariantState(t, m.root, SlotA)
	if reopened.Session().Objective != "inv14" {
		t.Errorf("objective = %q, want inv14 (compact context corruption must not lose the session)", reopened.Session().Objective)
	}
	if reopened.Session().recoveredFromRawHistory() {
		t.Error("authoritative record was present; session must not be flagged as raw-history rebuilt")
	}
	// Compact context must have been re-derived.
	var cc CompactContext
	if err := json.Unmarshal([]byte(readFileString(t, filepath.Join(m.dir, "A", compactContextFileName))), &cc); err != nil {
		t.Fatalf("compact context not re-derived after corruption: %v", err)
	}
	if cc.Objective != "inv14" {
		t.Errorf("re-derived compact context objective = %q, want inv14", cc.Objective)
	}
}

// TestInvSession14RebuildFromRawHistory is the catastrophic ladder: BOTH the
// authoritative record and the compact context are lost. The session must be
// rebuilt from the raw history starting from the latest valid checkpoint.
func TestInvSession14RebuildFromRawHistory(t *testing.T) {
	m := openTestManager(t, newTestManager(t))
	s := m.Session()
	s.Objective = "fix the bug"
	s.AddMessage("user", "fix the bug", 5)
	s.AddMessage("assistant", "let me investigate", 5)
	s.AddCheckpoint("cp-7")
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	// Seed the raw-history log (the durable rebuild source).
	if err := m.AppendHistory(context.Background(), "user", "fix the bug"); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := m.AppendHistory(context.Background(), "assistant", "let me investigate"); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	// Destroy both durable records.
	for _, f := range []string{sessionFile, compactContextFileName} {
		if err := os.Remove(filepath.Join(m.dir, "A", f)); err != nil {
			t.Fatalf("remove %s: %v", f, err)
		}
	}

	reopened := assertInvariantState(t, m.root, SlotA)
	reb := reopened.Session()
	if !reb.recoveredFromRawHistory() {
		t.Error("session must be flagged as recovered from raw history")
	}
	if reb.Objective != "fix the bug" {
		t.Errorf("rebuilt objective = %q, want fix the bug", reb.Objective)
	}
	if len(reb.History) == 0 {
		t.Fatal("rebuilt session must carry raw-history turns")
	}
	if reb.History[0].Content != "fix the bug" {
		t.Errorf("rebuilt history[0] = %q, want fix the bug", reb.History[0].Content)
	}
	if len(reb.Checkpoints) == 0 || reb.Checkpoints[len(reb.Checkpoints)-1] != "cp-7" {
		t.Errorf("rebuilt checkpoint = %v, want latest valid cp-7", reb.Checkpoints)
	}
	if reb.Mode != modes.ModeAsk {
		t.Errorf("rebuilt mode = %s, want ask (no mode token in raw history)", reb.Mode)
	}
}

// TestRebuildAppliesSlidingWindow verifies the rebuilt history honors the
// configurable turn window (not a hardcoded invariant).
func TestRebuildAppliesSlidingWindow(t *testing.T) {
	m := newTestManager(t, WithMaxTurns(2))
	openTestManager(t, m)
	if err := m.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	for i := 0; i < 6; i++ {
		if err := m.AppendHistory(context.Background(), "user", "turn"); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := m.AppendHistory(context.Background(), "assistant", "reply"); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}
	for _, f := range []string{sessionFile, compactContextFileName} {
		_ = os.Remove(filepath.Join(m.dir, "A", f))
	}

	reopened := assertInvariantState(t, m.root, SlotA, WithMaxTurns(2))
	// maxTurns=2 → 4 messages max.
	if len(reopened.Session().History) != 4 {
		t.Errorf("windowed history length = %d, want 4 (maxTurns=2)", len(reopened.Session().History))
	}
}

// TestSessionBoundaryHookInvoked verifies the RuntimeExecutor integration seam
// fires on every committed switch with the correct prev/next slots.
func TestSessionBoundaryHookInvoked(t *testing.T) {
	var got []string
	hook := BoundaryHookFunc(func(_ context.Context, prev, next SlotID) error {
		got = append(got, string(prev)+"->"+string(next))
		return nil
	})
	m := openTestManager(t, newTestManager(t, WithBoundaryHook(hook)))

	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if len(got) != 1 || got[0] != "A->B" {
		t.Errorf("boundary hook = %v, want [A->B]", got)
	}
	if _, err := m.ResumeSession(context.Background(), SlotA); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(got) != 2 || got[1] != "B->A" {
		t.Errorf("boundary hook = %v, want [A->B B->A]", got)
	}
}

// TestBoundaryHookErrorDoesNotRollbackCommittedPointer verifies a failing hook
// is recorded but never rolls back the atomic pointer commit.
func TestBoundaryHookErrorDoesNotRollbackCommittedPointer(t *testing.T) {
	fail := BoundaryHookFunc(func(context.Context, SlotID, SlotID) error {
		return errors.New("drain failed")
	})
	m := openTestManager(t, newTestManager(t, WithBoundaryHook(fail)))

	if _, err := m.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession with failing hook: %v", err)
	}
	if m.Active() != SlotB {
		t.Errorf("Active = %q, want B (hook failure must not roll back the commit)", m.Active())
	}
	if m.LastBoundaryErr() == nil {
		t.Error("LastBoundaryErr must surface the drain failure")
	}
}
