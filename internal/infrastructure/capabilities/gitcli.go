package capabilities

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

// gitCommandRunner executes a git subcommand in a directory. It is an
// interface so tests can inject a fake and keep git calls isolated from the
// host repository.
type gitCommandRunner interface {
	run(ctx context.Context, dir string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// execGitRunner is the default runner backed by the git binary.
type execGitRunner struct {
	gitBin string
}

func (r *execGitRunner) run(ctx context.Context, dir string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, r.gitBin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), exitCodeOf(err), err
}

// GitCLI implements ports.GitPort by executing git subcommands through the git
// command-line interface. It runs against the repository rooted at dir.
type GitCLI struct {
	dir    string
	runner gitCommandRunner
}

// NewGitCLI returns a GitPort adapter bound to the repository at dir. The
// optional gitBin overrides the git binary path (defaults to "git").
func NewGitCLI(dir string, gitBin ...string) *GitCLI {
	bin := "git"
	if len(gitBin) > 0 && gitBin[0] != "" {
		bin = gitBin[0]
	}
	return &GitCLI{dir: dir, runner: &execGitRunner{gitBin: bin}}
}

// withRunner returns a copy of the adapter bound to a custom runner. It is
// exported for tests that need to isolate git calls.
func (g *GitCLI) withRunner(r gitCommandRunner) *GitCLI {
	return &GitCLI{dir: g.dir, runner: r}
}

// run executes args and returns a descriptive error on non-zero exit.
func (g *GitCLI) run(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, code, err := g.runner.run(ctx, g.dir, args...)
	if err != nil {
		return stdout, gitError(stderr, code, err)
	}
	return stdout, nil
}

// gitError wraps a git failure with its captured stderr for diagnosis.
func gitError(stderr string, code int, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return err
	}
	return &gitCommandError{code: code, stderr: msg, cause: err}
}

// gitCommandError describes a git subcommand that exited non-zero.
type gitCommandError struct {
	code   int
	stderr string
	cause  error
}

func (e *gitCommandError) Error() string {
	return "git: exit " + strconv.Itoa(e.code) + ": " + e.stderr
}

func (e *gitCommandError) Unwrap() error { return e.cause }

// Status returns the current working-tree changes as parsed --porcelain
// entries.
func (g *GitCLI) Status(ctx context.Context) ([]ports.GitStatusEntry, error) {
	out, err := g.run(ctx, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseGitStatus(out), nil
}

// Diff returns the unstaged diff of the working tree.
func (g *GitCLI) Diff(ctx context.Context) (string, error) {
	return g.run(ctx, "diff")
}

// DiffFile returns the diff of a single file.
func (g *GitCLI) DiffFile(ctx context.Context, path string) (string, error) {
	return g.run(ctx, "diff", "--", path)
}

// Commit records a new commit with the given subject and body. A multi-line
// body is passed as a second -m so git stores it as a commit body.
func (g *GitCLI) Commit(ctx context.Context, subject, body string) error {
	args := []string{"commit", "-m", subject}
	if strings.TrimSpace(body) != "" {
		args = append(args, "-m", body)
	}
	_, err := g.run(ctx, args...)
	return err
}

// CurrentHash returns the short hash of the current HEAD commit.
func (g *GitCLI) CurrentHash(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Branch returns the name of the current branch.
func (g *GitCLI) Branch(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// parseGitStatus turns `git status --porcelain` output into port entries. Each
// line is "XY path" where X is the staged flag and Y the worktree flag.
func parseGitStatus(out string) []ports.GitStatusEntry {
	lines := strings.Split(out, "\n")
	entries := make([]ports.GitStatusEntry, 0, len(lines))
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		staging := string(line[0])
		worktree := string(line[1])
		path := strings.TrimPrefix(line[3:], "\"")
		path = strings.TrimSuffix(path, "\"")
		// Renames render as "R  old -> new"; keep the destination.
		if arrow := strings.Index(path, " -> "); arrow >= 0 {
			path = path[arrow+len(" -> "):]
		}
		entries = append(entries, ports.GitStatusEntry{
			Path:     path,
			Staging:  staging,
			Worktree: worktree,
		})
	}
	return entries
}
