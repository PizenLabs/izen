package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParsePorcelainStatus(t *testing.T) {
	modified, untracked := ParsePorcelainStatus([]byte(" M internal/a.go\n?? new.txt\nA  added.go\n"))
	if modified != 2 {
		t.Fatalf("modified = %d, want 2", modified)
	}
	if untracked != 1 {
		t.Fatalf("untracked = %d, want 1", untracked)
	}
}

func TestCollectStatusInGitRepository(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "config", "user.email", "status@example.invalid")
	runGitTest(t, root, "config", "user.name", "Status Test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := CollectStatus(context.Background(), root, StatusInputs{
		Indexer: IndexerStatus{Status: "indexed", SymbolCount: 7},
		Session: SessionStatus{SlotID: "A", Title: "Test", Goal: "Inspect", TurnCount: 2, TokenUsage: TokenUsage{InputTokens: 10, OutputTokens: 5}},
		Engine:  EngineStatus{Provider: "ollama", Model: "test", PolicyGate: "Read-Only"},
	})
	if !filepath.IsAbs(got.WorkspaceRoot) || got.WorkspaceRoot != mustAbs(t, root) {
		t.Fatalf("root = %q, want absolute %q", got.WorkspaceRoot, mustAbs(t, root))
	}
	if !got.VCS.HasGit || !got.VCS.Available {
		t.Fatalf("VCS unavailable: %+v", got.VCS)
	}
	if got.VCS.Branch == "" || got.VCS.ShortSHA == "" {
		t.Fatalf("missing branch/SHA: %+v", got.VCS)
	}
	if got.VCS.ModifiedFiles != 1 || got.VCS.UntrackedFiles != 1 || !got.VCS.IsDirty {
		t.Fatalf("dirty counts = %+v, want one modified and one untracked", got.VCS)
	}
	if got.Indexer.SymbolCount != 7 || got.Session.Tokens.TotalTokens != 15 || got.Engine.PolicyGate != "Read-Only" {
		t.Fatalf("runtime facets not preserved: %+v", got)
	}
}

func TestCollectStatusBoundsHungGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-specific")
	}
	root := t.TempDir()
	bin := t.TempDir()
	script := filepath.Join(bin, "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	got := CollectStatus(context.Background(), root)
	elapsed := time.Since(start)
	if elapsed >= 55*time.Millisecond {
		t.Fatalf("status probe took %s; subprocess was not bounded", elapsed)
	}
	if got.VCS.HasGit {
		t.Fatalf("hung git was reported as available: %+v", got.VCS)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
