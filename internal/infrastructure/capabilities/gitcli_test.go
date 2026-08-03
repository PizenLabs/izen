package capabilities

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

// gitEnv runs a git subcommand inside dir with a stable identity configured so
// commits succeed in throwaway repositories.
func gitEnv(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=izen-test",
		"GIT_AUTHOR_EMAIL=izen-test@example.com",
		"GIT_COMMITTER_NAME=izen-test",
		"GIT_COMMITTER_EMAIL=izen-test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestGitCLIIntegration(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitEnv(t, dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Configure a repo-local identity so commits work on bare CI runners that
	// have no global git config (GitCLI commits run without the env vars that
	// gitEnv injects).
	for _, kv := range []struct{ key, value string }{
		{"user.name", "izen-test"},
		{"user.email", "izen-test@example.com"},
	} {
		if _, err := gitEnv(t, dir, "config", kv.key, kv.value); err != nil {
			t.Fatalf("git config %s: %v", kv.key, err)
		}
	}

	g := NewGitCLI(dir)
	var _ ports.GitPort = g
	ctx := context.Background()

	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := gitEnv(t, dir, "add", "a.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := gitEnv(t, dir, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	hash, err := g.CurrentHash(ctx)
	if err != nil {
		t.Fatalf("CurrentHash: %v", err)
	}
	if len(hash) < 7 {
		t.Errorf("CurrentHash = %q, want short hash", hash)
	}

	branch, err := g.Branch(ctx)
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branch == "" {
		t.Error("Branch returned empty")
	}

	// Modify the file and check status/diff.
	if err := os.WriteFile(file, []byte("line1\nline2 changed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	status, err := g.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status) != 1 {
		t.Fatalf("Status len = %d, want 1 (entries %+v)", len(status), status)
	}
	if status[0].Path != "a.go" || status[0].Worktree != "M" {
		t.Errorf("Status[0] = %+v, want path a.go worktree M", status[0])
	}

	diff, err := g.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "-line2") || !strings.Contains(diff, "+line2 changed") {
		t.Errorf("Diff missing expected hunk:\n%s", diff)
	}

	diffFile, err := g.DiffFile(ctx, "a.go")
	if err != nil {
		t.Fatalf("DiffFile: %v", err)
	}
	if diffFile != diff {
		t.Errorf("DiffFile mismatch:\n%s\n---\n%s", diffFile, diff)
	}

	// Commit the change and confirm status is clean. Commit records staged
	// changes, so stage the modified file first (matching the existing git
	// engine contract of a separate StageAll step).
	if _, err := gitEnv(t, dir, "add", "a.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := g.Commit(ctx, "update", ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	status, err = g.Status(ctx)
	if err != nil {
		t.Fatalf("Status after commit: %v", err)
	}
	if len(status) != 0 {
		t.Errorf("Status after commit = %+v, want clean", status)
	}

	// Multi-body commit.
	if err := os.WriteFile(file, []byte("line1\nline2 changed\nline3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := gitEnv(t, dir, "add", "a.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := g.Commit(ctx, "subject", "detailed body"); err != nil {
		t.Fatalf("Commit with body: %v", err)
	}
}

func TestGitCLIStatusParsing(t *testing.T) {
	f := &fakeGitRunner{out: " M foo.go\nA  new.go\n?? untracked.txt\nR  old.go -> renamed.go\n"}
	g := NewGitCLI("").withRunner(f)

	entries, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := []ports.GitStatusEntry{
		{Path: "foo.go", Staging: " ", Worktree: "M"},
		{Path: "new.go", Staging: "A", Worktree: " "},
		{Path: "untracked.txt", Staging: "?", Worktree: "?"},
		{Path: "renamed.go", Staging: "R", Worktree: " "},
	}
	if len(entries) != len(want) {
		t.Fatalf("parsed %d entries, want %d (%+v)", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestGitCLIReturnsErrorOnFailure(t *testing.T) {
	f := &fakeGitRunner{err: context.DeadlineExceeded, stderr: "not a git repository", code: 128}
	g := NewGitCLI("").withRunner(f)

	_, err := g.CurrentHash(context.Background())
	if err == nil {
		t.Fatal("CurrentHash returned nil error for failing runner")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %q, want stderr embedded", err)
	}
	if !strings.Contains(err.Error(), "128") {
		t.Errorf("error = %q, want exit code embedded", err)
	}
}

func TestParseGitStatusEmpty(t *testing.T) {
	if entries := parseGitStatus(""); len(entries) != 0 {
		t.Errorf("parseGitStatus('') = %+v, want empty", entries)
	}
}

// fakeGitRunner is a scriptable gitCommandRunner for isolated unit tests.
type fakeGitRunner struct {
	out    string
	stderr string
	code   int
	err    error
}

func (f *fakeGitRunner) run(_ context.Context, _ string, _ ...string) (string, string, int, error) {
	return f.out, f.stderr, f.code, f.err
}
