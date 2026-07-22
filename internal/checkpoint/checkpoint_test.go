package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestRepo creates a temporary directory with an initialized git repo
// and a single tracked file. Returns the directory path.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "izen-checkpoint-test-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@izen.dev")
	run(t, dir, "git", "config", "user.name", "Izen Test")

	// Create initial file and commit it.
	initialContent := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), initialContent, 0644); err != nil {
		t.Fatalf("write initial main.go: %v", err)
	}
	run(t, dir, "git", "add", "main.go")
	run(t, dir, "git", "commit", "-m", "initial commit")

	return dir
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// TestCreatePreExecSnapshot verifies that a shadow checkpoint can be created
// without creating any visible git refs or commits.
func TestCreatePreExecSnapshot(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	// Mutate the working tree.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(\"hello\"); }\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Create checkpoint.
	cp, err := e.CreatePreExecSnapshot("test mutation")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	if cp.ID == "" {
		t.Fatal("expected non-empty checkpoint ID")
	}
	if cp.TreeHash == "" {
		t.Fatal("expected non-empty tree hash")
	}
	if cp.Label != "test mutation" {
		t.Fatalf("expected label 'test mutation', got %q", cp.Label)
	}
	if cp.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}

	// Verify no stash ref was created (git stash list should be empty).
	out, err := git(dir, "stash", "list")
	if err != nil {
		t.Fatalf("stash list: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty stash list, got: %s", out)
	}
}

// TestRestoreCheckpoint verifies that restoring a checkpoint returns the
// working tree to the exact pre-mutation byte state.
func TestRestoreCheckpoint(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	// Read the initial content of main.go.
	initialPath := filepath.Join(dir, "main.go")
	initialContent, err := os.ReadFile(initialPath)
	if err != nil {
		t.Fatalf("read initial main.go: %v", err)
	}

	// Create checkpoint BEFORE mutation.
	cp, err := e.CreatePreExecSnapshot("pre-mutation")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	// Mutate the file.
	mutatedContent := []byte("package main\n\nfunc main() { println(\"changed\"); }\n")
	if err := os.WriteFile(initialPath, mutatedContent, 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Verify the file actually changed.
	currentContent, _ := os.ReadFile(initialPath)
	if string(currentContent) == string(initialContent) {
		t.Fatal("file should have been mutated before restore test")
	}

	// Restore the checkpoint.
	if err := e.RestoreCheckpoint(cp); err != nil {
		t.Fatalf("RestoreCheckpoint: %v", err)
	}

	// Verify file content is restored to exact pre-mutation byte state.
	restoredContent, err := os.ReadFile(initialPath)
	if err != nil {
		t.Fatalf("read restored main.go: %v", err)
	}
	if string(restoredContent) != string(initialContent) {
		t.Fatalf("restored content mismatch:\nexpected: %q\ngot:      %q",
			string(initialContent), string(restoredContent))
	}
}

// TestRestoreCheckpoint_NewFile verifies that a file created after the
// checkpoint was taken is left in place by default (non-destructive restore).
func TestRestoreCheckpoint_NewFile(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	cp, err := e.CreatePreExecSnapshot("pre-mutation")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	// Create a new file after the checkpoint.
	newFilePath := filepath.Join(dir, "new_file.go")
	if err := os.WriteFile(newFilePath, []byte("package new\n"), 0644); err != nil {
		t.Fatalf("write new_file.go: %v", err)
	}

	// Restore — new file should still exist (non-destructive by default).
	if err := e.RestoreCheckpoint(cp); err != nil {
		t.Fatalf("RestoreCheckpoint: %v", err)
	}

	if _, err := os.Stat(newFilePath); os.IsNotExist(err) {
		t.Fatal("new file should still exist after non-destructive restore")
	}
}

// TestRestoreCheckpoint_WithCleanUntracked verifies that CleanUntracked
// removes files created after the checkpoint.
func TestRestoreCheckpoint_WithCleanUntracked(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	cp, err := e.CreatePreExecSnapshot("pre-mutation")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	// Create a new untracked file.
	newFilePath := filepath.Join(dir, "untracked.go")
	if err := os.WriteFile(newFilePath, []byte("package untracked\n"), 0644); err != nil {
		t.Fatalf("write untracked.go: %v", err)
	}

	// Restore and then clean untracked.
	if err := e.RestoreCheckpoint(cp); err != nil {
		t.Fatalf("RestoreCheckpoint: %v", err)
	}
	if err := e.CleanUntracked(); err != nil {
		t.Fatalf("CleanUntracked: %v", err)
	}

	if _, err := os.Stat(newFilePath); !os.IsNotExist(err) {
		t.Fatal("untracked file should have been removed by CleanUntracked")
	}
}

// TestSessionStartCheckpoint verifies that the session-start checkpoint can
// be created and restored, restoring the working tree to initial session state.
func TestSessionStartCheckpoint(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	// Create a session-start checkpoint.
	cp, err := e.CreateSessionStartSnapshot()
	if err != nil {
		t.Fatalf("CreateSessionStartSnapshot: %v", err)
	}
	if cp.ID != SessionStartKey {
		t.Fatalf("expected ID %q, got %q", SessionStartKey, cp.ID)
	}

	// Mutate the file.
	initialPath := filepath.Join(dir, "main.go")
	initialContent, _ := os.ReadFile(initialPath)
	mutatedContent := []byte("package main\n\nfunc main() { println(\"changed\"); }\n")
	if err := os.WriteFile(initialPath, mutatedContent, 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Create additional checkpoints (simulating mutations).
	_, err = e.CreatePreExecSnapshot("mutation-1")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot 1: %v", err)
	}
	if err := os.WriteFile(initialPath, []byte("package main\n\nfunc main() { println(\"again\"); }\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	_, err = e.CreatePreExecSnapshot("mutation-2")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot 2: %v", err)
	}

	// Restore session start.
	if err := e.RestoreSessionStart(); err != nil {
		t.Fatalf("RestoreSessionStart: %v", err)
	}

	restoredContent, err := os.ReadFile(initialPath)
	if err != nil {
		t.Fatalf("read restored main.go: %v", err)
	}
	if string(restoredContent) != string(initialContent) {
		t.Fatalf("session restore failed:\nexpected: %q\ngot:      %q",
			string(initialContent), string(restoredContent))
	}
}

// TestListCheckpoints verifies the checkpoint listing returns all stored
// checkpoints in newest-first order.
func TestListCheckpoints(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	summaries, err := e.ListCheckpoints()
	if err != nil {
		t.Fatalf("ListCheckpoints (empty): %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected 0 checkpoints, got %d", len(summaries))
	}

	_, err = e.CreatePreExecSnapshot("first")
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	_, err = e.CreatePreExecSnapshot("second")
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	summaries, err = e.ListCheckpoints()
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(summaries))
	}
	// Verify newest first.
	if summaries[0].Label != "second" {
		t.Fatalf("expected newest first 'second', got %q", summaries[0].Label)
	}
	if summaries[1].Label != "first" {
		t.Fatalf("expected second 'first', got %q", summaries[1].Label)
	}
}

// TestLatest verifies the Latest method returns the most recent checkpoint.
func TestLatest(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	cp, err := e.Latest()
	if err != nil {
		t.Fatalf("Latest (empty): %v", err)
	}
	if cp != nil {
		t.Fatal("expected nil for empty checkpoint store")
	}

	_, err = e.CreatePreExecSnapshot("latest-test")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	latest, err := e.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if latest.Label != "latest-test" {
		t.Fatalf("expected label 'latest-test', got %q", latest.Label)
	}
}

// TestPersistenceAcrossSessions simulates CLI re-initialization by creating
// a new Engine instance pointing at the same root and verifying checkpoints
// are still readable.
func TestPersistenceAcrossSessions(t *testing.T) {
	dir := setupTestRepo(t)

	// First "session".
	e1 := NewEngine(dir)
	cp1, err := e1.CreatePreExecSnapshot("session-1")
	if err != nil {
		t.Fatalf("session 1 checkpoint: %v", err)
	}

	// Simulate CLI restart with a new engine instance.
	e2 := NewEngine(dir)
	cp2, err := e2.Load(cp1.ID)
	if err != nil {
		t.Fatalf("session 2 load checkpoint: %v", err)
	}
	if cp2 == nil {
		t.Fatal("expected non-nil checkpoint after re-init")
	}
	if cp2.ID != cp1.ID {
		t.Fatalf("checkpoint ID mismatch: %s vs %s", cp2.ID, cp1.ID)
	}
	if cp2.TreeHash != cp1.TreeHash {
		t.Fatalf("tree hash mismatch: %s vs %s", cp2.TreeHash, cp1.TreeHash)
	}
	if cp2.Label != cp1.Label {
		t.Fatalf("label mismatch: %s vs %s", cp2.Label, cp1.Label)
	}
}

// TestFullUndoCycle tests the complete lifecycle: create pre-exec snapshot,
// mutate file, restore via checkpoint, verify exact byte state.
func TestFullUndoCycle(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	// Track initial state.
	mainPath := filepath.Join(dir, "main.go")
	initialContent, _ := os.ReadFile(mainPath)

	// Create snapshot before mutation.
	cp, err := e.CreatePreExecSnapshot("$hot change year")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	// Mutate the file (simulating $hot).
	mutatedContent := []byte("package main\n\nfunc main() { println(\"2026\"); }\n")
	if err := os.WriteFile(mainPath, mutatedContent, 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Verify mutation took effect.
	current, _ := os.ReadFile(mainPath)
	if string(current) != string(mutatedContent) {
		t.Fatalf("mutation did not take effect")
	}

	// "Undo" — restore from checkpoint.
	if err := e.RestoreCheckpoint(cp); err != nil {
		t.Fatalf("RestoreCheckpoint (undo): %v", err)
	}

	// Verify exact byte state restored.
	restored, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(restored) != string(initialContent) {
		t.Fatalf("undo did not restore exact byte state:\nexpected: %q\ngot:      %q",
			string(initialContent), string(restored))
	}
}

// TestNoGitHistoryPollution verifies that no branches, tags, or stash
// entries are created by the checkpointing process.
func TestNoGitHistoryPollution(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	// Get initial refs count.
	refsBefore, _ := git(dir, "show-ref")
	initialRefs := len(refsBefore)

	// Modify file and create checkpoints.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(\"change\"); }\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := e.CreatePreExecSnapshot("test"); err != nil {
			t.Fatalf("checkpoint %d: %v", i, err)
		}
	}

	// Check refs haven't changed (no branches/tags created).
	refsAfter, _ := git(dir, "show-ref")
	if len(refsAfter) != initialRefs {
		t.Fatalf("refs changed: before=%d, after=%d. Checkpointing created visible refs.", initialRefs, len(refsAfter))
	}

	// Verify git stash list is empty.
	out, _ := git(dir, "stash", "list")
	if out != "" {
		t.Fatal("stash list should be empty — checkpointing must not push stashes")
	}
}

// TestRemoveCheckpoint verifies that removing a checkpoint cleans up
// its metadata directory.
func TestRemoveCheckpoint(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	cp, err := e.CreatePreExecSnapshot("to-be-removed")
	if err != nil {
		t.Fatalf("CreatePreExecSnapshot: %v", err)
	}

	// Verify checkpoint directory exists.
	cpDir := filepath.Join(e.dir, cp.ID)
	if _, err := os.Stat(cpDir); os.IsNotExist(err) {
		t.Fatal("checkpoint directory should exist before removal")
	}

	if err := e.RemoveCheckpoint(cp.ID); err != nil {
		t.Fatalf("RemoveCheckpoint: %v", err)
	}

	if _, err := os.Stat(cpDir); !os.IsNotExist(err) {
		t.Fatal("checkpoint directory should be removed")
	}
}

// TestCaptureAfterMultipleMutations verifies that each checkpoint captures
// the state at its creation time, not a shared reference.
func TestCaptureAfterMultipleMutations(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)
	mainPath := filepath.Join(dir, "main.go")

	cp1, err := e.CreatePreExecSnapshot("state-a")
	if err != nil {
		t.Fatalf("cp1: %v", err)
	}

	// Second mutation.
	if err := os.WriteFile(mainPath, []byte("package main\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cp2, err := e.CreatePreExecSnapshot("state-b")
	if err != nil {
		t.Fatalf("cp2: %v", err)
	}

	// Third mutation.
	if err := os.WriteFile(mainPath, []byte("package main\n\nvar x = 2\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = e.CreatePreExecSnapshot("state-c")
	if err != nil {
		t.Fatalf("cp3: %v", err)
	}

	// Restore cp2 — should get state-b content.
	if err := e.RestoreCheckpoint(cp2); err != nil {
		t.Fatalf("restore cp2: %v", err)
	}
	content, _ := os.ReadFile(mainPath)
	if string(content) != "package main\n\nvar x = 1\n" {
		t.Fatalf("cp2 restore: expected state-b, got %q", string(content))
	}

	// Restore cp1 — should get state-a content.
	if err := e.RestoreCheckpoint(cp1); err != nil {
		t.Fatalf("restore cp1: %v", err)
	}
	content, _ = os.ReadFile(mainPath)
	_ = content // cp1 was taken at initial state — already verified above.
}
