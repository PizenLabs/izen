package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ShadowCheckpoint represents a lightweight internal working-tree snapshot
// stored as a Git tree object (never a visible commit).
type ShadowCheckpoint struct {
	ID        string    `json:"id"`
	TreeHash  string    `json:"tree_hash"`
	Label     string    `json:"label"`
	Timestamp time.Time `json:"timestamp"`
	ParentID  string    `json:"parent_id,omitempty"`
	// StagedOnly indicates whether the checkpoint captured only staged changes.
	// When false, both staged and unstaged worktree changes were included.
	StagedOnly bool `json:"staged_only,omitempty"`
}

// ShadowCheckpointSummary is a compact view of a checkpoint for display.
type ShadowCheckpointSummary struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Timestamp time.Time `json:"timestamp"`
	Files     int       `json:"files"`
}

// Engine manages shadow checkpoints using Git plumbing commands.
// Checkpoints are stored as tree objects in the Git object store but are
// never attached to any branch or ref — they exist only as dangling objects
// reachable via the stored tree hash, ensuring zero pollution of commit history.
type Engine struct {
	root string
	dir  string
}

// NewEngine creates a checkpoint engine for the repository rooted at root.
// The checkpoint metadata directory lives under .izen/checkpoints/.
func NewEngine(root string) *Engine {
	return &Engine{
		root: root,
		dir:  filepath.Join(root, ".izen", "checkpoints"),
	}
}

// CreatePreExecSnapshot captures the current working tree state as a
// shadow checkpoint and returns it. The snapshot is taken BEFORE a mutation
// command executes so the tree can be restored on /undo.
//
// It uses git stash create to capture both staged and unstaged changes
// as a single tree object, then stores the tree hash in .izen/checkpoints/<id>/.
// No git refs (branches, tags, stash list) are modified — zero history pollution.
func (e *Engine) CreatePreExecSnapshot(label string) (*ShadowCheckpoint, error) {
	if !e.isRepo() {
		return nil, fmt.Errorf("not a git repository")
	}

	treeHash, err := e.captureWorkingTree()
	if err != nil {
		return nil, fmt.Errorf("capture working tree: %w", err)
	}

	cp := &ShadowCheckpoint{
		ID:        fmt.Sprintf("cp-%d", time.Now().UnixNano()),
		TreeHash:  treeHash,
		Label:     label,
		Timestamp: time.Now(),
	}
	if _, err := git(e.root, "rev-parse", "--verify", "HEAD"); err == nil {
		head, _ := git(e.root, "rev-parse", "--short", "HEAD")
		cp.ParentID = strings.TrimSpace(head)
	}

	if err := e.save(cp); err != nil {
		return cp, fmt.Errorf("checkpoint captured but persist failed: %w", err)
	}

	return cp, nil
}

// captureWorkingTree returns the tree hash representing the full working tree
// (staged + unstaged changes). Uses git stash create internally; the stash
// commit is never attached to any ref and becomes a dangling object eligible
// for GC after an eventual git gc, but survives across CLI restarts.
//
// If the working tree is clean (no changes from HEAD), returns the HEAD tree
// hash directly without creating any new object.
func (e *Engine) captureWorkingTree() (string, error) {
	// Check if there are any changes vs HEAD.
	hasChanges, err := e.hasWorkingTreeChanges()
	if err != nil {
		return "", err
	}
	if !hasChanges {
		// Clean tree — return HEAD tree hash, no new object needed.
		out, err := git(e.root, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return "", fmt.Errorf("rev-parse HEAD tree: %w", err)
		}
		return strings.TrimSpace(out), nil
	}

	// Use git stash create to snapshot the working tree.
	out, err := git(e.root, "stash", "create")
	if err != nil {
		return "", fmt.Errorf("stash create: %w", err)
	}
	stashHash := strings.TrimSpace(out)
	if stashHash == "" {
		// No changes to stash — use HEAD tree.
		out, err = git(e.root, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return "", fmt.Errorf("rev-parse HEAD tree: %w", err)
		}
		return strings.TrimSpace(out), nil
	}

	// Extract the tree hash from the stash commit.
	out, err = git(e.root, "rev-parse", stashHash+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("rev-parse stash tree: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// hasWorkingTreeChanges reports whether the working tree or index differs
// from HEAD. Uses git status --porcelain to check without creating objects.
// Handles the case where HEAD does not exist (fresh repo with no commits).
func (e *Engine) hasWorkingTreeChanges() (bool, error) {
	// First verify HEAD exists.
	//nolint:nilerr // no HEAD is a valid condition meaning everything is new
	if _, err := git(e.root, "rev-parse", "--verify", "HEAD"); err != nil {
		return true, nil
	}
	out, err := git(e.root, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("status --porcelain: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// RestoreCheckpoint restores the working tree to the state captured in a
// checkpoint. Uses git read-tree to replace the index, then git checkout-index
// to force the working tree files to match.
//
// Untracked files created after the checkpoint are NOT removed by default
// (non-destructive). To clean untracked files, call CleanUntracked after restore.
func (e *Engine) RestoreCheckpoint(cp *ShadowCheckpoint) error {
	if cp == nil || cp.TreeHash == "" {
		return fmt.Errorf("invalid checkpoint: nil or empty tree hash")
	}

	// Verify the tree object exists in the object store.
	if _, err := git(e.root, "cat-file", "-e", cp.TreeHash); err != nil {
		return fmt.Errorf("checkpoint tree %s not found in object store (may have been GC'd): %w", cp.TreeHash, err)
	}

	// Replace the index with the checkpoint tree.
	if _, err := git(e.root, "read-tree", cp.TreeHash); err != nil {
		return fmt.Errorf("read-tree %s: %w", cp.TreeHash, err)
	}

	// Force checkout all files from the index to the working tree.
	if _, err := git(e.root, "checkout-index", "-f", "--all"); err != nil {
		return fmt.Errorf("checkout-index: %w", err)
	}

	return nil
}

// CleanUntracked removes files that are untracked in the current checkout
// state. Should be called after RestoreCheckpoint if a full clean is desired.
// Uses git clean -fd to remove untracked files and directories.
func (e *Engine) CleanUntracked() error {
	_, err := git(e.root, "clean", "-fd")
	if err != nil {
		return fmt.Errorf("clean untracked: %w", err)
	}
	return nil
}

// RemoveCheckpoint deletes the checkpoint metadata from disk.
// The underlying Git tree object remains in the object store until GC.
func (e *Engine) RemoveCheckpoint(id string) error {
	path := filepath.Join(e.dir, id, "checkpoint.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Remove the empty directory (best-effort).
	_ = os.Remove(filepath.Join(e.dir, id))
	return nil
}

// ListCheckpoints returns summaries of all stored checkpoints, newest first.
func (e *Engine) ListCheckpoints() ([]ShadowCheckpointSummary, error) {
	if _, err := os.Stat(e.dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return nil, fmt.Errorf("read checkpoints dir: %w", err)
	}

	var summaries []ShadowCheckpointSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cpPath := filepath.Join(e.dir, entry.Name(), "checkpoint.json")
		data, err := os.ReadFile(cpPath)
		if err != nil {
			continue
		}
		var cp ShadowCheckpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			continue
		}
		files := cp.countChangedFiles(e.root)
		summaries = append(summaries, ShadowCheckpointSummary{
			ID:        cp.ID,
			Label:     cp.Label,
			Timestamp: cp.Timestamp,
			Files:     files,
		})
	}

	// Sort newest first.
	for i := 0; i < len(summaries); i++ {
		for j := i + 1; j < len(summaries); j++ {
			if summaries[j].Timestamp.After(summaries[i].Timestamp) {
				summaries[i], summaries[j] = summaries[j], summaries[i]
			}
		}
	}

	return summaries, nil
}

// Latest returns the most recent checkpoint, or nil if none exist.
func (e *Engine) Latest() (*ShadowCheckpoint, error) {
	summaries, err := e.ListCheckpoints()
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, nil
	}
	return e.Load(summaries[0].ID)
}

// Load reads a specific checkpoint by ID from disk.
func (e *Engine) Load(id string) (*ShadowCheckpoint, error) {
	cpPath := filepath.Join(e.dir, id, "checkpoint.json")
	data, err := os.ReadFile(cpPath)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint %s: %w", id, err)
	}
	var cp ShadowCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint %s: %w", id, err)
	}
	return &cp, nil
}

// SessionStartKey is the well-known checkpoint ID for the session-start snapshot.
const SessionStartKey = "session-start"

// CreateSessionStartSnapshot creates the session-start checkpoint that marks
// the initial working state when an Izen session begins. This checkpoint is
// used by /undo session to restore the entire session.
func (e *Engine) CreateSessionStartSnapshot() (*ShadowCheckpoint, error) {
	treeHash, err := e.captureWorkingTree()
	if err != nil {
		return nil, err
	}
	cp := &ShadowCheckpoint{
		ID:        SessionStartKey,
		TreeHash:  treeHash,
		Label:     "session-start",
		Timestamp: time.Now(),
	}
	if _, err := git(e.root, "rev-parse", "--verify", "HEAD"); err == nil {
		head, _ := git(e.root, "rev-parse", "--short", "HEAD")
		cp.ParentID = strings.TrimSpace(head)
	}
	if err := e.save(cp); err != nil {
		return nil, fmt.Errorf("session checkpoint persist: %w", err)
	}
	return cp, nil
}

// RestoreSessionStart restores the working tree to the initial session state.
func (e *Engine) RestoreSessionStart() error {
	cp, err := e.Load(SessionStartKey)
	if err != nil {
		return fmt.Errorf("session start checkpoint not found: %w", err)
	}
	return e.RestoreCheckpoint(cp)
}

// save persists a checkpoint's metadata to disk under .izen/checkpoints/<id>/.
func (e *Engine) save(cp *ShadowCheckpoint) error {
	cpDir := filepath.Join(e.dir, cp.ID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(cpDir, "checkpoint.json")
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// isRepo checks whether the root contains a .git directory.
func (e *Engine) isRepo() bool {
	_, err := os.Stat(filepath.Join(e.root, ".git"))
	return err == nil
}

// countChangedFiles returns an estimate of how many files differ between the
// checkpoint tree and the current HEAD. This is best-effort; it counts the
// number of files in the diff between the checkpoint tree and HEAD.
func (cp *ShadowCheckpoint) countChangedFiles(root string) int {
	if cp.TreeHash == "" {
		return 0
	}
	out, err := git(root, "diff-tree", "--no-commit-id", "--name-only", "-r", cp.TreeHash, "HEAD")
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
}

// CheckpointDir returns the path to the checkpoint metadata directory.
func (e *Engine) CheckpointDir() string {
	return e.dir
}

// CommandContext creates an exec.Cmd for a git command in the given root directory.
// Exported for use by test helpers in other packages.
func CommandContext(root string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = root
	return cmd
}

// git runs a git command in the repository root and returns stdout.
func git(root string, args ...string) (string, error) {
	cmd := CommandContext(root, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
