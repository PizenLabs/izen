// Package checkpoint persistence for the Phase 3 Agent Execution Engine.
//
// The shadow git-tree Engine in engine.go is preserved untouched. This file
// adds the Phase 3 file-snapshot checkpoint manager:
//
//	.izen/checkpoints/cp-<nanoseconds>/checkpoint.json
//	.izen/checkpoints/cp-<nanoseconds>/files/<workspace-relative copies>
//
// checkpoint.json carries the timestamp, Git HEAD SHA, modified file list
// and patch diff. The files/ mirror carries exact byte snapshots so
// Rollback restores the workspace deterministically even in non-git
// workspaces. All metadata writes are atomic.
package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// CheckpointRecord is the Phase 3 checkpoint manifest stored as
// checkpoint.json.
type CheckpointRecord struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Timestamp time.Time `json:"timestamp"`
	HeadSHA   string    `json:"head_sha"`
	Files     []string  `json:"files"`
	Diff      string    `json:"diff"`
	// SkippedFiles lists workspace-relative paths excluded from the snapshot
	// because they matched .gitignore patterns or exceeded the max size
	// threshold. Rollback preserves these paths instead of deleting them.
	SkippedFiles []string `json:"skipped_files,omitempty"`
}

// MaxSnapshotFileSize caps single-file snapshot payloads. Files larger than
// this are excluded from the snapshot and recorded in SkippedFiles.
const MaxSnapshotFileSize = 10 << 20 // 10MB

// checkpointsDir returns the Phase 3 checkpoint root for a workspace.
func checkpointsDir(workDir string) string {
	return filepath.Join(workDir, ".izen", "checkpoints")
}

// CreateCheckpoint captures the workspace state into
// .izen/checkpoints/cp-<nanoseconds>/ and returns the checkpoint id.
func CreateCheckpoint(workDir string, label string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("checkpoint: empty workDir")
	}
	fi, err := os.Stat(workDir)
	if err != nil {
		return "", fmt.Errorf("checkpoint: workDir: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("checkpoint: workDir %q is not a directory", workDir)
	}

	id := fmt.Sprintf("cp-%d", time.Now().UnixNano())
	cpDir := filepath.Join(checkpointsDir(workDir), id)
	snapDir := filepath.Join(cpDir, "files")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", fmt.Errorf("checkpoint: mkdir %s: %w", cpDir, err)
	}

	head := gitHeadSHA(workDir)
	modified := gitModifiedFiles(workDir)
	diff := gitDiff(workDir)
	if len(modified) == 0 {
		// Non-git workspace (or clean tree): the modified list degrades to
		// the full tracked workspace file set so the record is never empty
		// on a non-empty workspace.
		if all, err := listWorkspaceFiles(workDir); err == nil && len(all) > 0 && head == "" {
			modified = all
		}
	}

	skipped, err := snapshotWorkspace(workDir, snapDir)
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return "", fmt.Errorf("checkpoint: snapshot: %w", err)
	}

	rec := CheckpointRecord{
		ID:           id,
		Label:        label,
		Timestamp:    time.Now(),
		HeadSHA:      head,
		Files:        modified,
		Diff:         diff,
		SkippedFiles: skipped,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return "", fmt.Errorf("checkpoint: marshal: %w", err)
	}
	if err := atomicio.WriteFileAtomic(filepath.Join(cpDir, "checkpoint.json"), data, 0o644); err != nil {
		_ = os.RemoveAll(cpDir)
		return "", fmt.Errorf("checkpoint: write manifest: %w", err)
	}
	return id, nil
}

// Rollback reverts the workspace to the exact state captured by the
// targeted checkpoint: snapshotted files are restored byte-for-byte, files
// created after the checkpoint are removed, and files deleted after the
// checkpoint are recreated. It never touches .izen or .git.
func Rollback(workDir string, checkpointID string) error {
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("checkpoint: empty workDir")
	}
	if strings.TrimSpace(checkpointID) == "" {
		return fmt.Errorf("checkpoint: empty checkpoint id")
	}
	// Contain the id to a single path element.
	if checkpointID != filepath.Base(checkpointID) || strings.Contains(checkpointID, "..") {
		return fmt.Errorf("checkpoint: invalid checkpoint id %q", checkpointID)
	}
	cpDir := filepath.Join(checkpointsDir(workDir), checkpointID)
	manifestPath := filepath.Join(cpDir, "checkpoint.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("checkpoint: read %s: %w", checkpointID, err)
	}
	var rec CheckpointRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("checkpoint: decode %s: %w", checkpointID, err)
	}
	snapDir := filepath.Join(cpDir, "files")
	if _, err := os.Stat(snapDir); err != nil {
		return fmt.Errorf("checkpoint: snapshot missing for %s: %w", checkpointID, err)
	}

	snapshots := make(map[string]struct{})
	err = filepath.WalkDir(snapDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(snapDir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		snapshots[relSlash] = struct{}{}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(workDir, filepath.FromSlash(relSlash))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := atomicio.WriteFileAtomic(dst, content, 0o644); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("checkpoint: restore %s: %w", checkpointID, err)
	}

	// Remove files created after the checkpoint. Skipped files (gitignored
	// or oversized at snapshot time) are preserved: they were never part of
	// the snapshot and must not be deleted as "post-checkpoint" files.
	preserved := make(map[string]struct{}, len(rec.SkippedFiles))
	for _, s := range rec.SkippedFiles {
		preserved[filepath.ToSlash(s)] = struct{}{}
	}
	current, err := listWorkspaceFiles(workDir)
	if err != nil {
		return fmt.Errorf("checkpoint: list workspace: %w", err)
	}
	for _, rel := range current {
		if _, ok := snapshots[rel]; !ok {
			if _, skip := preserved[rel]; skip {
				continue
			}
			_ = os.Remove(filepath.Join(workDir, filepath.FromSlash(rel)))
		}
	}
	cleanEmptyDirs(workDir)
	return nil
}

// listWorkspaceFiles returns slash-separated workspace-relative paths of all
// regular files, excluding .izen and .git.
func listWorkspaceFiles(workDir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if path != workDir && (name == ".izen" || name == ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// snapshotWorkspace copies every workspace file into snapDir, preserving
// relative layout. Files matching .gitignore patterns or exceeding
// MaxSnapshotFileSize are skipped and returned as workspace-relative paths
// so the caller can record them in SkippedFiles.
func snapshotWorkspace(workDir, snapDir string) ([]string, error) {
	files, err := listWorkspaceFiles(workDir)
	if err != nil {
		return nil, err
	}
	patterns := loadGitignorePatterns(workDir)
	var skipped []string
	for _, rel := range files {
		if matchesGitignore(rel, patterns) {
			skipped = append(skipped, rel)
			continue
		}
		src := filepath.Join(workDir, filepath.FromSlash(rel))
		if fi, statErr := os.Stat(src); statErr == nil && fi.Size() > MaxSnapshotFileSize {
			skipped = append(skipped, rel)
			continue
		}
		content, err := os.ReadFile(src)
		if err != nil {
			// File vanished mid-snapshot; skip rather than failing the
			// whole checkpoint.
			continue
		}
		dst := filepath.Join(snapDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return skipped, err
		}
		// Snapshot blobs are written once into a fresh directory; a plain
		// write is sufficient (the manifest commit is the atomic point).
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// cleanEmptyDirs removes empty directories left behind after deleting
// post-checkpoint files. It never removes workDir, .izen or .git.
func cleanEmptyDirs(workDir string) {
	var dirs []string
	_ = filepath.WalkDir(workDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // best-effort cleanup ignores walk errors
		}
		if !d.IsDir() || path == workDir {
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		first := strings.Split(filepath.ToSlash(rel), "/")[0]
		if first == ".izen" || first == ".git" {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	// Deepest first so parents become empty afterwards.
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// ── git helpers (best-effort; empty string on any failure) ───────────────

func runGit(workDir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}

func gitHeadSHA(workDir string) string {
	out, ok := runGit(workDir, "rev-parse", "HEAD")
	if !ok {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitModifiedFiles(workDir string) []string {
	out, ok := runGit(workDir, "status", "--porcelain")
	if !ok {
		return nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		name := strings.TrimSpace(line[3:])
		// Handle renames ("old -> new").
		if idx := strings.Index(name, " -> "); idx >= 0 {
			name = name[idx+4:]
		}
		name = strings.Trim(name, `"`)
		if name == "" || strings.HasPrefix(name, ".izen/") {
			continue
		}
		files = append(files, name)
	}
	return files
}

func gitDiff(workDir string) string {
	if out, ok := runGit(workDir, "diff", "HEAD", "--", ".", ":(exclude).izen"); ok {
		return out
	}
	return ""
}
