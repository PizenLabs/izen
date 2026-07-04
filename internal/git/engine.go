package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type StatusEntry struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
}

type DiffEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type CommitInfo struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
}

type Engine struct {
	root string
}

func NewEngine(root string) *Engine {
	return &Engine{root: root}
}

func (e *Engine) IsRepo() bool {
	_, err := os.Stat(filepath.Join(e.root, ".git"))
	return err == nil
}

func (e *Engine) Init() error {
	out, err := e.git("init")
	if err != nil {
		return err
	}
	_ = out
	return nil
}

func (e *Engine) Status() ([]StatusEntry, error) {
	out, err := e.git("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

func (e *Engine) Diff() (string, error) {
	return e.git("diff", "--no-color")
}

func (e *Engine) DiffFile(path string) (string, error) {
	return e.git("diff", "--no-color", "--", path)
}

func (e *Engine) DiffCached() (string, error) {
	return e.git("diff", "--cached", "--no-color")
}

func (e *Engine) Branch() (string, error) {
	out, err := e.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (e *Engine) CurrentHash() (string, error) {
	out, err := e.git("rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (e *Engine) Checkpoint(message string) (string, error) {
	if !e.IsRepo() {
		return "", fmt.Errorf("not a git repository")
	}

	if _, err := e.git("add", "-A"); err != nil {
		return "", fmt.Errorf("add: %w", err)
	}

	out, err := e.git("commit", "--allow-empty", "-m", message)
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return strings.TrimSpace(out), nil
}

func (e *Engine) Undo() (string, error) {
	if !e.IsRepo() {
		return "", fmt.Errorf("not a git repository")
	}

	out, err := e.git("reset", "--soft", "HEAD~1")
	if err != nil {
		return "", fmt.Errorf("undo: %w", err)
	}

	return strings.TrimSpace(out), nil
}

func (e *Engine) LastCommitDiff() (string, error) {
	return e.git("diff", "HEAD~1..HEAD", "--no-color")
}

func (e *Engine) AmendCommit(message string) error {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("izen-amend-%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, []byte(message), 0644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpFile) }()
	_, err := e.git("commit", "--amend", "-F", tmpFile)
	return err
}

func (e *Engine) ResetHard(ref string) error {
	_, err := e.git("reset", "--hard", ref)
	return err
}

func (e *Engine) RecentCommits(n int) ([]CommitInfo, error) {
	format := "%H|%s|%an|%aI"
	out, err := e.git("log", fmt.Sprintf("-%d", n), fmt.Sprintf("--format=%s", format))
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

func (e *Engine) HasChanges() bool {
	out, err := e.Status()
	return err == nil && len(out) > 0
}

func (e *Engine) Stash() (string, error) {
	out, err := e.git("stash", "push", "-m", "izen-checkpoint")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (e *Engine) StashPop() (string, error) {
	out, err := e.git("stash", "pop")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (e *Engine) git(args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = e.root

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}

func parseStatus(out string) []StatusEntry {
	var entries []StatusEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) < 4 {
			continue
		}
		entries = append(entries, StatusEntry{
			Staging:  string(line[0]),
			Worktree: string(line[1]),
			Path:     strings.TrimSpace(line[3:]),
		})
	}
	return entries
}

func parseCommits(out string) []CommitInfo {
	var commits []CommitInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		t, err := time.Parse(time.RFC3339, parts[3])
		if err != nil {
			t = time.Now()
		}
		commits = append(commits, CommitInfo{
			Hash:    parts[0],
			Message: parts[1],
			Author:  parts[2],
			Time:    t,
		})
	}
	return commits
}
