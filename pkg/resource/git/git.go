// Package git implements the git repository Resource adapter. A GitResource
// wraps a workspace git repository and exposes deterministic, non-destructive
// snapshot/restore of its HEAD commit.
package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/PizenLabs/izen/pkg/resource"
)

// GitResource wraps the git repository rooted at a workspace directory. It is
// the concrete resource.Resource implementation for git targets.
type GitResource struct {
	workspaceDir string
}

// Compile-time assertion that GitResource satisfies resource.Resource.
var _ resource.Resource = (*GitResource)(nil)

// NewGitResource returns a GitResource bound to workspaceDir.
func NewGitResource(workspaceDir string) (*GitResource, error) {
	if workspaceDir == "" {
		return nil, errors.New("git: workspace directory is required")
	}
	return &GitResource{workspaceDir: workspaceDir}, nil
}

// ID returns the workspace directory the resource wraps.
func (g *GitResource) ID() string { return g.workspaceDir }

// Kind returns resource.KindGitRepo.
func (g *GitResource) Kind() resource.ResourceKind { return resource.KindGitRepo }

// ValidateState checks that the workspace directory is a valid git work tree
// by running `git rev-parse --is-inside-work-tree`.
func (g *GitResource) ValidateState(ctx context.Context) error {
	if err := g.runGit(ctx, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("git: %q is not a valid git workspace: %w", g.workspaceDir, err)
	}
	return nil
}

// gitSnapshot is the concrete Snapshot implementation for GitResource.
type gitSnapshot struct {
	hash string
	data gitSnapshotData
}

// gitSnapshotData is the typed payload of a GitResource snapshot.
type gitSnapshotData struct {
	// CommitSHA is the HEAD commit SHA captured at snapshot time.
	CommitSHA string
}

// Hash returns a stable identifier derived from the captured commit SHA.
func (s *gitSnapshot) Hash() string { return s.hash }

// Data returns the typed snapshot payload.
func (s *gitSnapshot) Data() any { return s.data }

// Snapshot captures the current HEAD commit SHA. The capture is read-only.
func (g *GitResource) Snapshot(ctx context.Context) (resource.Snapshot, error) {
	if err := g.ValidateState(ctx); err != nil {
		return nil, err
	}
	out, err := g.gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git: resolve HEAD: %w", err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return nil, errors.New("git: empty HEAD commit SHA")
	}
	sum := sha256.Sum256([]byte(sha))
	return &gitSnapshot{
		hash: hex.EncodeToString(sum[:]),
		data: gitSnapshotData{CommitSHA: sha},
	}, nil
}

// Restore hard-resets the workspace to the captured HEAD commit SHA. Only
// snapshots produced by a GitResource are accepted.
func (g *GitResource) Restore(ctx context.Context, s resource.Snapshot) error {
	if s == nil {
		return errors.New("git: restore requires a non-nil snapshot")
	}
	snap, ok := s.(*gitSnapshot)
	if !ok {
		return fmt.Errorf("git: incompatible snapshot type %T", s)
	}
	if err := g.runGit(ctx, "reset", "--hard", snap.data.CommitSHA); err != nil {
		return fmt.Errorf("git: hard reset to %q: %w", snap.data.CommitSHA, err)
	}
	return nil
}

// gitCommand returns an exec.Cmd for git scoped to the workspace directory and
// bound to ctx so cancellation interrupts the subprocess.
func (g *GitResource) gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.workspaceDir
	return cmd
}

// runGit runs git and returns the first error, if any.
func (g *GitResource) runGit(ctx context.Context, args ...string) error {
	return g.gitCommand(ctx, args...).Run()
}

// gitOutput runs git and returns its trimmed stdout.
func (g *GitResource) gitOutput(ctx context.Context, args ...string) (string, error) {
	out, err := g.gitCommand(ctx, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
