package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/contract"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/workspace/snapshot"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNewCheckpointID_Format(t *testing.T) {
	id := NewCheckpointID()
	if !strings.HasPrefix(string(id), "chk_") {
		t.Fatalf("expected chk_ prefix, got %s", id)
	}
	// The ID must embed a timestamp for ordering and an 8-hex short hash.
	parts := strings.Split(string(id), "_")
	if len(parts) != 3 {
		t.Fatalf("unexpected ID shape (want chk_<ts>_<hash>): %s", id)
	}
	if parts[0] != "chk" || len(parts[2]) != 8 {
		t.Fatalf("unexpected ID shape (want chk_<ts>_<hash>): %s", id)
	}
	for _, r := range parts[2] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("hash part not hex: %s", id)
		}
	}
	a, b := NewCheckpointID(), NewCheckpointID()
	if a == b {
		t.Fatal("expected distinct checkpoint IDs")
	}
}

func TestCreateCheckpoint_CapturesOriginalBlobs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // original")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if cp.Stage != "build.executor" {
		t.Fatalf("stage = %q", cp.Stage)
	}
	if string(cp.OriginalBlobs["a.go"]) != "package a // original" {
		t.Fatalf("original blob = %q", cp.OriginalBlobs["a.go"])
	}
	if cp.MissingFiles["a.go"] {
		t.Fatal("a.go should not be marked missing")
	}
	if mgr.Open() != 1 {
		t.Fatalf("Open() = %d, want 1", mgr.Open())
	}
}

func TestCreateCheckpoint_MarksNewFilesForDeletion(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"new.go"})
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if !cp.MissingFiles["new.go"] {
		t.Fatal("new.go should be marked missing")
	}
	if _, ok := cp.OriginalBlobs["new.go"]; ok {
		t.Fatal("missing file must not have an original blob")
	}
}

func TestRollback_RestoresModifiedFileAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.go")
	writeFile(t, target, "package app // v1\nfunc A() {}\n")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"app.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a mutation: full rewrite plus content change.
	writeFile(t, target, "package app // v2 CORRUPTED\nfunc B() {}\n")

	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, target); got != "package app // v1\nfunc A() {}\n" {
		t.Fatalf("rollback did not restore original content:\n%s", got)
	}
	if mgr.Open() != 0 {
		t.Fatalf("checkpoint not consumed, Open() = %d", mgr.Open())
	}
	if mgr.Get(cp.ID) != nil {
		t.Fatal("consumed checkpoint should not be retrievable")
	}
}

func TestRollback_DeletesNewlyCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "created.go")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"created.go"})
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, target, "package main\n")

	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected created.go to be deleted, stat err = %v", err)
	}
}

func TestRollback_MultipleFilesBatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")
	writeFile(t, filepath.Join(dir, "b.go"), "package b // v1")
	nested := filepath.Join(dir, "cmd", "c.go")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go", "b.go", "cmd/c.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Mutate all three: rewrite a, delete b, create nested c.
	writeFile(t, filepath.Join(dir, "a.go"), "package a // BROKEN")
	if err := os.Remove(filepath.Join(dir, "b.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nested, "package c\n")

	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("a.go not restored: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "b.go")); got != "package b // v1" {
		t.Fatalf("b.go not restored: %q", got)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("expected nested c.go to be deleted, stat err = %v", err)
	}
}

func TestCommit_DiscardsAndConsumes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.go"), "package app // v1")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"app.go"})
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "app.go"), "package app // v2 FINAL")

	if err := mgr.Commit(cp.ID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if mgr.Open() != 0 {
		t.Fatalf("commit did not consume checkpoint, Open() = %d", mgr.Open())
	}
	// Commit is a no-op for workspace state — the new content stays.
	if got := readFile(t, filepath.Join(dir, "app.go")); got != "package app // v2 FINAL" {
		t.Fatalf("commit must not touch workspace, got %q", got)
	}
}

func TestRollbackAll_ConsumesEveryOpenCheckpoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")
	writeFile(t, filepath.Join(dir, "b.go"), "package b // v1")

	mgr := NewManager(dir)
	if _, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateCheckpoint("build.executor", []string{"b.go"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.go"), "package a // BROKEN")
	writeFile(t, filepath.Join(dir, "b.go"), "package b // BROKEN")

	if err := mgr.RollbackAll(); err != nil {
		t.Fatalf("RollbackAll: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("a.go = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "b.go")); got != "package b // v1" {
		t.Fatalf("b.go = %q", got)
	}
	if mgr.Open() != 0 {
		t.Fatalf("Open() = %d, want 0", mgr.Open())
	}
}

func TestRollback_UnknownID(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	if err := mgr.Rollback(NewCheckpointID()); err == nil {
		t.Fatal("expected ErrCheckpointNotFound")
	}
}

func TestRollback_DoubleResolutionFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.go"), "v1")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"app.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Commit(cp.ID); err == nil {
		t.Fatal("expected error when committing a consumed checkpoint")
	}
}

func TestRollback_EmitsEvents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")

	bus := events.NewBus(16)
	defer bus.Close()
	mgr := NewManager(dir).WithEventBus(bus)

	var mu sync.Mutex
	var attempts []events.DomainEvent
	bus.Subscribe(events.EventPatchAttempted, func(ev events.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, ev)
	})

	cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.go"), "package a // BROKEN")
	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for patch attempted events, got %d", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	p, ok := attempts[0].Payload().(events.PatchAttemptedPayload)
	if !ok {
		t.Fatalf("payload = %T, want PatchAttemptedPayload", attempts[0].Payload())
	}
	if p.File != "a.go" || p.Strategy != "CHECKPOINT_ROLLBACK_RESTORE" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestRollback_RefreshesSnapshotCache(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module test\n")
	writeFile(t, filepath.Join(dir, "app.go"), "package app // v1")

	cache := snapshot.NewSnapshotCache()
	mgr := NewManager(dir).WithSnapshotCache(cache)

	// Prime the cache with the original tree.
	snap, err := cache.GetSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	origID := snap.ID

	cp, err := mgr.CreateCheckpoint("build.executor", []string{"app.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Mutate and roll back. The cached snapshot must be invalidated and rebuilt.
	writeFile(t, filepath.Join(dir, "app.go"), "package app // BROKEN")
	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatal(err)
	}
	after := cache.Current()
	if after == nil {
		t.Fatal("expected a rebuilt snapshot after rollback")
	}
	if after.ID == origID {
		t.Fatal("expected snapshot cache to be refreshed (new ID) after rollback")
	}
	info, ok := after.FileTree["app.go"]
	if !ok {
		t.Fatal("expected app.go in refreshed snapshot")
	}
	if info.ContentHash == "" {
		t.Fatal("expected content hash in refreshed snapshot")
	}
}

func TestRollback_SnapshotReflectsDeletedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module test\n")

	cache := snapshot.NewSnapshotCache()
	mgr := NewManager(dir).WithSnapshotCache(cache)
	if _, err := cache.GetSnapshot(dir); err != nil {
		t.Fatal(err)
	}

	cp, err := mgr.CreateCheckpoint("build.executor", []string{"new.go"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new.go"), "package main\n")
	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatal(err)
	}

	after := cache.Current()
	if after == nil {
		t.Fatal("expected rebuilt snapshot")
	}
	if _, ok := after.FileTree["new.go"]; ok {
		t.Fatal("refreshed snapshot must not contain rolled-back file")
	}
}

func TestPermissionEnforcement(t *testing.T) {
	dir := t.TempDir()

	// No permissions configured → creation allowed (backwards compatible).
	open := NewManager(dir)
	if _, err := open.CreateCheckpoint("build.executor", []string{"a.go"}); err != nil {
		t.Fatalf("unexpected deny without permission config: %v", err)
	}

	// CREATE_CHECKPOINT granted → allowed.
	granted := NewManager(dir).WithPermissions([]contract.PermissionLevel{
		contract.PermWorkspace, contract.PermPatch, contract.PermCheckpoint,
	})
	if _, err := granted.CreateCheckpoint("build.executor", []string{"a.go"}); err != nil {
		t.Fatalf("unexpected deny with CREATE_CHECKPOINT granted: %v", err)
	}

	// CREATE_CHECKPOINT missing → denied.
	denied := NewManager(dir).WithPermissions([]contract.PermissionLevel{
		contract.PermWorkspace, contract.PermPatch,
	})
	if _, err := denied.CreateCheckpoint("build.executor", []string{"a.go"}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

func TestShadowPersistence_RemovedOnRollback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	shadowDir := filepath.Join(dir, ".izen", "checkpoints", "workspace", string(cp.ID))
	if _, err := os.Stat(shadowDir); err != nil {
		t.Fatalf("expected shadow copy directory: %v", err)
	}

	writeFile(t, filepath.Join(dir, "a.go"), "BROKEN")
	if err := mgr.Rollback(cp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shadowDir); !os.IsNotExist(err) {
		t.Fatalf("expected shadow copy removed after rollback, stat err = %v", err)
	}
}

func TestConcurrentLifecycle_Safe(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")

	mgr := NewManager(dir)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
			if err != nil {
				t.Error(err)
				return
			}
			_ = mgr.Rollback(cp.ID)
		}()
	}
	wg.Wait()

	if mgr.Open() != 0 {
		t.Fatalf("expected all checkpoints resolved, Open() = %d", mgr.Open())
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("concurrent rollbacks corrupted file: %q", got)
	}
}

func TestConcurrentLifecycle_InterleavedRollback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a // v1")

	mgr := NewManager(dir)
	cp, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Rollback(cp.ID)
		}()
	}
	wg.Wait()

	// Exactly one rollback must win; the rest observe a not-found error.
	if mgr.Open() != 0 {
		t.Fatalf("Open() = %d, want 0", mgr.Open())
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("file corrupted by racing rollbacks: %q", got)
	}
}

func TestList_OrdersByCreation(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	first, err := mgr.CreateCheckpoint("build.executor", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := mgr.CreateCheckpoint("build.executor", []string{"b.go"})
	if err != nil {
		t.Fatal(err)
	}

	list := mgr.List()
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list out of order: %s, %s", list[0].ID, list[1].ID)
	}
}
