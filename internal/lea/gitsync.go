package lea

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// SyncFromGit re-indexes files that changed while Izen was offline. It runs
// `git diff --name-only` for unstaged changes, `git diff --cached --name-only`
// for staged changes, and `git ls-files --others --exclude-standard` for
// untracked files (which fsnotify catches during live sessions but not on
// boot). Returns the relative paths to refresh.
func (e *Engine) SyncFromGit(ctx context.Context) ([]string, error) {
	changed := make(map[string]bool)
	diff, err := e.gitOutput(ctx, "diff", "--name-only")
	if err == nil {
		collectPaths(diff, changed)
	}
	staged, err := e.gitOutput(ctx, "diff", "--cached", "--name-only")
	if err == nil {
		collectPaths(staged, changed)
	}
	untracked, err := e.gitOutput(ctx, "ls-files", "--others", "--exclude-standard")
	if err == nil {
		collectPaths(untracked, changed)
	}

	// No git repository at all.
	if len(changed) == 0 && err != nil {
		return nil, fmt.Errorf("git unavailable: %w", err)
	}

	var out []string
	for p := range changed {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (e *Engine) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = e.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

func collectPaths(out string, set map[string]bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			set[line] = true
		}
	}
}
