package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSnapshot is a minimal resource.Snapshot used to verify Restore rejects
// foreign snapshot types.
type fakeSnapshot struct{}

func (fakeSnapshot) Hash() string { return "" }
func (fakeSnapshot) Data() any    { return nil }

// gitEnv pins an identity so commits succeed without global git config.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// initRepo creates a git repository with an initial commit containing file.txt.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "first")
}

func TestGitResourceValidateState(t *testing.T) {
	requireGit(t)
	ctx := t.Context()
	dir := t.TempDir()
	initRepo(t, dir)

	g, err := NewGitResource(dir)
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	if err := g.ValidateState(ctx); err != nil {
		t.Fatalf("ValidateState on valid repo: %v", err)
	}
	if g.Kind().String() != "res.git" {
		t.Fatalf("unexpected kind %q", g.Kind())
	}
	if g.ID() != dir {
		t.Fatalf("unexpected ID %q", g.ID())
	}
}

func TestGitResourceValidateStateNonRepo(t *testing.T) {
	requireGit(t)
	ctx := t.Context()
	g, err := NewGitResource(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	if err := g.ValidateState(ctx); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestGitResourceSnapshotAndRestore(t *testing.T) {
	requireGit(t)
	ctx := t.Context()
	dir := t.TempDir()
	initRepo(t, dir)

	g, err := NewGitResource(dir)
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}

	snap, err := g.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Hash() == "" {
		t.Fatal("expected non-empty hash")
	}
	data, ok := snap.Data().(gitSnapshotData)
	if !ok {
		t.Fatalf("unexpected snapshot data type %T", snap.Data())
	}
	if data.CommitSHA == "" {
		t.Fatal("expected non-empty commit SHA")
	}

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second")

	if err := g.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	head := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	if head != data.CommitSHA {
		t.Fatalf("expected HEAD %q, got %q", data.CommitSHA, head)
	}
	content, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(content) != "v1" {
		t.Fatalf("expected restored content %q, got %q", "v1", content)
	}
}

func TestGitResourceRestoreRejectsForeignSnapshot(t *testing.T) {
	requireGit(t)
	ctx := t.Context()
	dir := t.TempDir()
	initRepo(t, dir)

	g, err := NewGitResource(dir)
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	if err := g.Restore(ctx, nil); err == nil {
		t.Fatal("expected error restoring a nil snapshot")
	}
	if err := g.Restore(ctx, fakeSnapshot{}); err == nil {
		t.Fatal("expected error restoring a foreign snapshot type")
	}
}

func TestGitResourceCommit(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	ctx := t.Context()
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}

	g, err := NewGitResource(dir)
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	before := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	sha, err := g.Commit(ctx, "second")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	after := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	if sha != after {
		t.Fatalf("expected returned SHA %q to equal HEAD %q", sha, after)
	}
	if after == before {
		t.Fatal("expected a new commit")
	}
	if got := strings.TrimSpace(runGit(t, dir, "show", "HEAD:file.txt")); got != "v2" {
		t.Fatalf("expected committed content %q, got %q", "v2", got)
	}
}
