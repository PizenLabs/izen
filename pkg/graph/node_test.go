package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/resource"
	"github.com/PizenLabs/izen/pkg/resource/file"
	"github.com/PizenLabs/izen/pkg/resource/git"
	"github.com/PizenLabs/izen/pkg/resource/terminal"
)

// Compile-time assertion that OpNode satisfies kernel.Executable.
var _ kernel.Executable = (*OpNode)(nil)

// gitEnv pins an identity so commits succeed without global git config.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

// newFileTarget returns a file resource targeting rel inside a fresh temp dir.
func newFileTarget(t *testing.T, rel string) *file.FileResource {
	t.Helper()
	r, err := file.NewFileResource(t.TempDir(), rel, 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	return r
}

func newTerminalTarget(t *testing.T) *terminal.TerminalResource {
	t.Helper()
	r, err := terminal.NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	return r
}

// mustNode builds a validated OpNode or fails the test.
func mustNode(t *testing.T, id string, typ op.OpType, target resource.Resource, payload any, pre []string) *OpNode {
	t.Helper()
	node, err := NewOpNode(op.Operation{
		ID:             id,
		Type:           typ,
		TargetResource: target,
		Payload:        payload,
		Preconditions:  pre,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpNode(%s): %v", id, err)
	}
	return node
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

func revParse(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func TestOpNodeExecutableContract(t *testing.T) {
	target := newFileTarget(t, "a.go")
	node := mustNode(t, "n-1", op.OpWriteFile, target, ir.NewFile("a.go", []byte("x")), []string{"dep"})
	if node.ID() != "n-1" {
		t.Fatalf("unexpected ID %q", node.ID())
	}
	if got := node.Requires(); len(got) != 1 || got[0] != "dep" {
		t.Fatalf("unexpected requires %v", got)
	}
	if node.Timeout() != time.Second {
		t.Fatalf("unexpected timeout %v", node.Timeout())
	}
	if got := node.Operation(); got.ID != "n-1" {
		t.Fatalf("unexpected operation ID %q", got.ID)
	}
}

func TestOpNodeRequiresDefensiveCopy(t *testing.T) {
	pre := []string{"dep"}
	node := mustNode(t, "n-1", op.OpRunCommand, newTerminalTarget(t), "true", pre)
	req := node.Requires()
	req[0] = "mutated"
	if node.Requires()[0] != "dep" {
		t.Fatalf("expected defensive copy, got %v", node.Requires())
	}
}

func TestOpNodeExecuteWriteFile(t *testing.T) {
	engine := kernel.NewEngine(nil)
	target := newFileTarget(t, "src/a.go")
	if err := os.MkdirAll(filepath.Dir(target.ID()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	node := mustNode(t, "write", op.OpWriteFile, target, ir.NewFile("src/a.go", []byte("package a\n")), nil)

	res := engine.ExecuteTask(t.Context(), node)
	if res.Status != kernel.StatusCompleted {
		t.Fatalf("expected %s, got %s: %v", kernel.StatusCompleted, res.Status, res.Error)
	}
	got, err := target.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "package a\n" {
		t.Fatalf("unexpected content %q", got)
	}
}

func TestOpNodeExecuteDeleteFile(t *testing.T) {
	engine := kernel.NewEngine(nil)
	target := newFileTarget(t, "gone.go")
	abs := target.ID()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	node := mustNode(t, "delete", op.OpDeleteFile, target, nil, nil)

	res := engine.ExecuteTask(t.Context(), node)
	if res.Status != kernel.StatusCompleted {
		t.Fatalf("expected %s, got %s: %v", kernel.StatusCompleted, res.Status, res.Error)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err %v", err)
	}
}

func TestOpNodeExecuteRunCommand(t *testing.T) {
	engine := kernel.NewEngine(nil)
	node := mustNode(t, "run", op.OpRunCommand, newTerminalTarget(t), "echo hello", nil)

	res := engine.ExecuteTask(t.Context(), node)
	if res.Status != kernel.StatusCompleted {
		t.Fatalf("expected %s, got %s: %v", kernel.StatusCompleted, res.Status, res.Error)
	}
	out, ok := res.Data.(string)
	if !ok || !strings.Contains(out, "hello") {
		t.Fatalf("unexpected output %v", res.Data)
	}
}

func TestOpNodeExecuteRunCommandFailure(t *testing.T) {
	engine := kernel.NewEngine(nil)
	node := mustNode(t, "fail", op.OpRunCommand, newTerminalTarget(t), "exit 1", nil)

	res := engine.ExecuteTask(t.Context(), node)
	if res.Status != kernel.StatusFailed {
		t.Fatalf("expected %s, got %s", kernel.StatusFailed, res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected a failure error")
	}
}

func TestOpNodeExecuteGitCommit(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	before := revParse(t, dir)

	repo, err := git.NewGitResource(dir)
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	node := mustNode(t, "commit", op.OpGitCommit, repo, "second", nil)

	res := engineExecute(t, node)
	if res.Status != kernel.StatusCompleted {
		t.Fatalf("expected %s, got %s: %v", kernel.StatusCompleted, res.Status, res.Error)
	}
	after := revParse(t, dir)
	if after == before {
		t.Fatal("expected a new commit")
	}
	sha, ok := res.Data.(string)
	if !ok || strings.TrimSpace(sha) != after {
		t.Fatalf("unexpected commit sha %v", res.Data)
	}
}

func engineExecute(t *testing.T, node *OpNode) kernel.TaskResult {
	t.Helper()
	engine := kernel.NewEngine(nil)
	return engine.ExecuteTask(t.Context(), node)
}
