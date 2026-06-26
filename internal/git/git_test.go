package git

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "izen-git-test-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@izen.dev")
	run(t, dir, "git", "config", "user.name", "Izen Test")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "initial")
	return dir
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	e := &Engine{root: dir}
	out, err := e.git(args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestIsRepo(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)
	if !e.IsRepo() {
		t.Fatal("expected IsRepo to be true")
	}
}

func TestNotRepo(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-nonrepo-*")
	defer os.RemoveAll(dir)
	e := NewEngine(dir)
	if e.IsRepo() {
		t.Fatal("expected IsRepo to be false")
	}
}

func TestBranch(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)
	branch, err := e.Branch()
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branch != "main" && branch != "master" {
		t.Fatalf("unexpected branch: %s", branch)
	}
}

func TestStatusEmpty(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)
	status, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status) != 0 {
		t.Fatalf("expected empty status, got %d entries", len(status))
	}
}

func TestStatusWithChange(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	status, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status) == 0 {
		t.Fatal("expected non-empty status")
	}
}

func TestCheckpoint(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := e.Checkpoint("test checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	commits, err := e.RecentCommits(1)
	if err != nil {
		t.Fatalf("RecentCommits: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected at least 1 commit")
	}
	if commits[0].Message != "test checkpoint" {
		t.Fatalf("expected message 'test checkpoint', got %q", commits[0].Message)
	}
}

func TestUndo(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	hash1, _ := e.CurrentHash()

	run(t, dir, "git", "commit", "--allow-empty", "-m", "extra-commit")

	hash2, _ := e.CurrentHash()
	if hash1 == hash2 {
		t.Fatal("expected hash to change after new commit")
	}

	_, err := e.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}

	hash3, _ := e.CurrentHash()
	if hash3 != hash1 {
		t.Fatalf("expected hash to be restored to %s, got %s", hash1, hash3)
	}
}

func TestHasChanges(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	if e.HasChanges() {
		t.Fatal("expected no changes initially")
	}

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !e.HasChanges() {
		t.Fatal("expected changes after file creation")
	}
}

func TestCurrentHash(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	run(t, dir, "git", "commit", "--allow-empty", "-m", "initial")

	hash, err := e.CurrentHash()
	if err != nil {
		t.Fatalf("CurrentHash: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
}

func TestRecentCommitsOrder(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	run(t, dir, "git", "commit", "--allow-empty", "-m", "first")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "second")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "third")

	commits, err := e.RecentCommits(3)
	if err != nil {
		t.Fatalf("RecentCommits: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}
	if commits[0].Message != "third" {
		t.Fatalf("expected newest first 'third', got %q", commits[0].Message)
	}
}

func TestDiff(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run(t, dir, "git", "add", "test.txt")
	run(t, dir, "git", "commit", "-m", "add test.txt")

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("modified"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	diff, err := e.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
}

func TestDiffFile(t *testing.T) {
	dir := setupTestRepo(t)
	e := NewEngine(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run(t, dir, "git", "add", "a.txt")
	run(t, dir, "git", "commit", "-m", "add a.txt")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("modified"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	diff, err := e.DiffFile("a.txt")
	if err != nil {
		t.Fatalf("DiffFile: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff for a.txt")
	}

	diff2, err := e.DiffFile("nonexistent.txt")
	if err != nil {
		t.Fatalf("DiffFile nonexistent: %v", err)
	}
	if diff2 != "" {
		t.Fatal("expected empty diff for nonexistent file")
	}
}

func TestParseStatus(t *testing.T) {
	entries := parseStatus(" M test.txt\nA  new.go\n?? untracked.md\n")
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[1].Staging != "A" {
		t.Fatalf("expected staging 'A', got %q", entries[1].Staging)
	}
	if entries[2].Path != "untracked.md" {
		t.Fatalf("expected path 'untracked.md', got %q", entries[2].Path)
	}
}

func TestParseCommits(t *testing.T) {
	out := "abc123|feat: init|test user|2025-01-01T00:00:00Z\ndef456|fix: bug|test user|2025-01-02T00:00:00Z\n"
	commits := parseCommits(out)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if commits[0].Hash != "abc123" {
		t.Fatalf("expected hash 'abc123', got %q", commits[0].Hash)
	}
	if commits[1].Message != "fix: bug" {
		t.Fatalf("expected message 'fix: bug', got %q", commits[1].Message)
	}
}
