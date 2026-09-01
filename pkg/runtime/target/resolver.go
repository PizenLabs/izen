package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrAbsoluteTarget is returned when rawTarget is an absolute path. The
// resolver only accepts targets relative to the working directory so that
// resolution never escapes the workspace.
var ErrAbsoluteTarget = errors.New("target: absolute path targets are not supported")

// Resolver resolves raw target strings to canonical path identities.
type Resolver interface {
	// Resolve resolves rawTarget relative to workdir, returning the
	// authoritative TargetRef for the target.
	Resolve(workdir string, rawTarget string) (*TargetRef, error)
}

// TargetResolver is the concrete Resolver implementing the VCS > Filesystem >
// Raw authority chain.
type TargetResolver struct{}

// Compile-time assertion that TargetResolver satisfies Resolver.
var _ Resolver = (*TargetResolver)(nil)

// NewTargetResolver returns a stateless TargetResolver.
func NewTargetResolver() *TargetResolver { return &TargetResolver{} }

// Resolve implements the resolution algorithm pipeline:
//
//  1. VCS check (Git Index Authority): if workdir is inside a Git repository,
//     git ls-files is queried for a case-insensitive match of rawTarget. A
//     match yields Canonical = the exact path stored in the Git index,
//     Tracked = true, Exists = true, Source = ResolutionVCS.
//  2. Filesystem check (OS Case-Preserved Identity): otherwise the parent
//     directory is inspected with os.ReadDir and matched case-insensitively
//     against the raw target. A match yields Canonical = the case-preserved
//     physical name, Tracked = false, Exists = true,
//     Source = ResolutionFilesystem.
//  3. Fallback (User Raw Spelling): if neither authority can locate the
//     target, Canonical = rawTarget, Tracked = false, Exists = false,
//     Source = ResolutionRaw.
//
// Resolve returns an error only for invalid inputs (empty working directory
// or target, or an absolute target). Resolution failures at any stage degrade
// gracefully into the next stage — the VCS stage is skipped when workdir is
// not inside a Git repository, and the filesystem stage is skipped when the
// parent directory cannot be read.
func (r *TargetResolver) Resolve(workdir string, rawTarget string) (*TargetRef, error) {
	if workdir == "" {
		return nil, errors.New("target: working directory is required")
	}
	if rawTarget == "" {
		return nil, errors.New("target: raw target is required")
	}
	if filepath.IsAbs(rawTarget) {
		return nil, fmt.Errorf("%w: %q", ErrAbsoluteTarget, rawTarget)
	}
	if ref, ok := r.resolveGit(workdir, rawTarget); ok {
		return ref, nil
	}
	if ref, ok := r.resolveFilesystem(workdir, rawTarget); ok {
		return ref, nil
	}
	return &TargetRef{
		Raw:       rawTarget,
		Canonical: rawTarget,
		Exists:    false,
		Tracked:   false,
		Source:    ResolutionRaw,
	}, nil
}

// resolveGit performs the VCS check. It returns ok = false when workdir is
// not inside a Git repository, when the Git index cannot be queried, or when
// no tracked file matches rawTarget.
func (r *TargetResolver) resolveGit(workdir, rawTarget string) (*TargetRef, bool) {
	ctx := context.Background()
	inside := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	inside.Dir = workdir
	if err := inside.Run(); err != nil {
		return nil, false
	}
	list := exec.CommandContext(ctx, "git", "ls-files", "-z")
	list.Dir = workdir
	out, err := list.Output()
	if err != nil {
		return nil, false
	}
	entries := strings.Split(string(out), "\x00")
	// Prefer an exact full-path match before falling back to a basename
	// match so a bare filename never shadows a more specific path.
	if entry := foldMatch(entries, rawTarget); entry != "" {
		return vcsRef(rawTarget, entry), true
	}
	if entry := foldBasenameMatch(entries, filepath.Base(rawTarget)); entry != "" {
		return vcsRef(rawTarget, entry), true
	}
	return nil, false
}

// foldMatch returns the first entry whose full path case-insensitively
// equals want, or an empty string when no entry matches.
func foldMatch(entries []string, want string) string {
	for _, entry := range entries {
		if entry != "" && strings.EqualFold(entry, want) {
			return entry
		}
	}
	return ""
}

// foldBasenameMatch returns the first entry whose basename
// case-insensitively equals want, or an empty string when no entry matches.
func foldBasenameMatch(entries []string, want string) string {
	for _, entry := range entries {
		if entry != "" && strings.EqualFold(filepath.Base(entry), want) {
			return entry
		}
	}
	return ""
}

// vcsRef builds a TargetRef resolved from the Git index authority.
func vcsRef(rawTarget, canonical string) *TargetRef {
	return &TargetRef{
		Raw:       rawTarget,
		Canonical: canonical,
		Exists:    true,
		Tracked:   true,
		Source:    ResolutionVCS,
	}
}

// resolveFilesystem performs the filesystem check. It inspects the parent
// directory of rawTarget and returns ok = false when the directory cannot be
// read or when no physical entry case-insensitively matches.
func (r *TargetResolver) resolveFilesystem(workdir, rawTarget string) (*TargetRef, bool) {
	parent := filepath.Dir(rawTarget)
	entries, err := os.ReadDir(filepath.Join(workdir, parent))
	if err != nil {
		return nil, false
	}
	want := filepath.Base(rawTarget)
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), want) {
			return &TargetRef{
				Raw:       rawTarget,
				Canonical: filepath.Join(parent, entry.Name()),
				Exists:    true,
				Tracked:   false,
				Source:    ResolutionFilesystem,
			}, true
		}
	}
	return nil, false
}
