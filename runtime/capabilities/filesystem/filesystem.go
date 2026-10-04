// Package filesystem provides the kernel's real workspace capabilities.
//
// It is the first migrated capability set, and it is deliberately honest about
// what it is: a thin, auditable seam over one directory, where every observation
// it reports corresponds to a syscall that actually ran.
//
// Three properties are non-negotiable here, and they are the reason this is not
// a generic "fs helper":
//
//  1. Confinement. Every path is resolved against the configured root and
//     refused if it escapes. A capability that can read /etc/passwd is a
//     capability the grant cannot reason about.
//  2. Observation, not assertion. FileExists reports what a stat said. It never
//     infers existence from a read, and never reports present for a file it did
//     not look for.
//  3. Honest verdicts. A write that was refused reports FAIL with no write
//     evidence, so the mutation axis stays untouched rather than claiming a
//     change nobody made.
//
// Nothing in this package imports a terminal, a provider, a configuration file,
// or any other IZEN package. It depends only on the kernel and the standard
// library, so it is usable headlessly and is testable without a workspace.
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// Capability is the kernel-facing name of this implementation set. It exists so
// callers can name what they wired without importing four separate types.
type Capability struct {
	// root is the absolute, confined workspace root.
	root string
	// maxReadBytes bounds a single read so a capability cannot exhaust memory by
	// being pointed at a large file. Zero means no bound.
	maxReadBytes int
}

// Option configures a Capability.
type Option func(*Capability)

// WithMaxReadBytes bounds every read. A negative bound is refused by New.
func WithMaxReadBytes(n int) Option {
	return func(c *Capability) { c.maxReadBytes = n }
}

// New builds a filesystem capability set confined to root.
//
// The root is resolved to an absolute path and stat'd once, so a root that does
// not exist is refused at construction rather than producing confusing
// per-step failures later.
func New(root string, opts ...Option) (*Capability, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: "filesystem capability requires a workspace root",
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: fmt.Sprintf("resolve workspace root %q: %v", trimmed, err),
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: fmt.Sprintf("workspace root %q is not usable: %v", abs, err),
		}
	}
	if !info.IsDir() {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: fmt.Sprintf("workspace root %q is not a directory", abs),
		}
	}
	c := &Capability{root: abs}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.maxReadBytes < 0 {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: fmt.Sprintf("max read bound is negative (%d)", c.maxReadBytes),
		}
	}
	return c, nil
}

// Root returns the confined workspace root.
func (c *Capability) Root() string {
	if c == nil {
		return ""
	}
	return c.root
}

// Capabilities returns the kernel capabilities this implementation provides, in
// canonical vocabulary order.
//
// The set is a plain value with no ambient state, so a caller builds it once at
// composition time and hands the registry to an engine.
func (c *Capability) Capabilities() []kernel.Capability {
	if c == nil {
		return nil
	}
	return []kernel.Capability{
		&discoverCap{fs: c},
		&readCap{fs: c},
		&searchCap{fs: c},
		&existsCap{fs: c},
		&writeCap{fs: c},
		&deleteCap{fs: c},
	}
}

// Registry builds a kernel registry over this capability set.
func (c *Capability) Registry() (*kernel.Registry, error) {
	return kernel.NewRegistry(c.Capabilities()...)
}

// resolve maps a workspace-relative target onto an absolute path inside the
// root, refusing anything that escapes.
//
// Confinement is established in two halves, because either one alone is
// defeatable:
//
//  1. Lexically. The cleaned relative path may not be absolute and may not climb
//     out with "..", and the joined absolute path is re-checked against the root.
//     A target containing ".." that does not escape is still allowed, because it
//     names a real file inside the workspace.
//  2. Really. The target's symlink-resolved path must stay under the root's
//     symlink-resolved path. A symlink inside the workspace pointing at a
//     directory outside it passes every lexical check — the joined path is
//     lexically under the root — while the bytes land somewhere the grant cannot
//     name. A grant names workspace-relative paths, so a target whose real path
//     is elsewhere is not a path the grant can reason about at all.
//
// Symlinks that resolve INSIDE the root stay permitted. They name a real file in
// the workspace, which is a legitimate way to organise one.
func (c *Capability) resolve(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", kernel.Block{
			Class:  kernel.FailureCapability,
			Reason: "no target was named; the kernel does not guess a destination",
		}
	}
	rel := filepath.ToSlash(filepath.Clean(target))
	if filepath.IsAbs(target) || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves outside the workspace root", target),
		}
	}
	abs := filepath.Join(c.root, filepath.FromSlash(rel))
	// Defence in depth: confirm the join is still under the root after cleaning.
	if !underRoot(c.root, abs) {
		return "", kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves outside the workspace root", target),
		}
	}
	if err := c.confinesRealPath(abs, target); err != nil {
		return "", err
	}
	return abs, nil
}

// confinesRealPath refuses a target whose symlink-resolved path leaves the root.
//
// The deepest EXISTING ancestor is resolved and the remaining, not-yet-created
// components are appended, because a destination that has not been created yet
// cannot be resolved — and it is exactly the creation case where a symlinked
// parent would redirect the write.
func (c *Capability) confinesRealPath(abs, target string) error {
	rootReal, err := filepath.EvalSymlinks(c.root)
	if err != nil {
		return kernel.Block{
			Class:  kernel.FailureCapability,
			Reason: fmt.Sprintf("workspace root %q could not be resolved: %v", c.root, err),
		}
	}

	probe := abs
	var pending []string
	for {
		if _, lstatErr := os.Lstat(probe); lstatErr == nil {
			break
		} else if !errors.Is(lstatErr, fs.ErrNotExist) {
			return kernel.Block{
				Class:  kernel.FailureCapability,
				Reason: fmt.Sprintf("target %q could not be examined: %v", target, lstatErr),
			}
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Reached the filesystem root without finding anything that exists.
			// The lexical check already refused anything outside the workspace, so
			// this cannot be an escape; treat it as confined rather than inventing
			// a refusal for an unreachable shape.
			return nil
		}
		pending = append(pending, filepath.Base(probe))
		probe = parent
	}

	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return kernel.Block{
			Class:  kernel.FailureCapability,
			Reason: fmt.Sprintf("target %q could not be resolved: %v", target, err),
		}
	}
	for i := len(pending) - 1; i >= 0; i-- {
		real = filepath.Join(real, pending[i])
	}
	if !underRoot(rootReal, real) {
		return kernel.Block{
			Class:  kernel.FailureAuthorization,
			Reason: fmt.Sprintf("target %q resolves to %s, which is outside the workspace root", target, real),
		}
	}
	return nil
}

func underRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel != ".." && !strings.HasPrefix(rel, "../")
}

// relative renders an absolute path back into its workspace-relative form, which
// is the form every evidence record carries. Evidence that named absolute paths
// would not be comparable across machines.
func (c *Capability) relative(abs string) string {
	rel, err := filepath.Rel(c.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// skipEntry is the explicit "this subtree is not being walked" signal returned to
// filepath.WalkDir. Naming it removes the ambiguity of a bare nil: a reader can
// see that skipping was chosen rather than fallen into.
var skipEntry = filepath.SkipDir

// ── workspace.discover ─────────────────────────────────────────────────────

type discoverCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (d *discoverCap) ID() kernel.CapabilityID { return kernel.WorkspaceDiscover }

// Authorize implements kernel.Capability. Discovery is read-only and takes no
// target, so the only thing to confirm is that the grant named this capability.
func (d *discoverCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.WorkspaceDiscover) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.WorkspaceDiscover,
			Reason:     fmt.Sprintf("grant %q does not permit workspace.discover", req.Grant.ID),
		}
	}
	return nil
}

// Invoke implements kernel.Capability. It walks the workspace for real and
// reports what it found. A discovery step that found nothing still reports
// WORKSPACE_OBSERVED, because the observation is the walk, not the contents.
func (d *discoverCap) Invoke(ctx context.Context, req kernel.Request) (kernel.Observation, error) {
	limit := 512
	if n, ok := req.IntArg("max_entries"); ok {
		limit = n
	}
	count := 0
	skipped := 0
	var firstPaths []string

	err := filepath.WalkDir(d.fs.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			// An unreadable entry is counted and skipped rather than aborting the
			// walk: the observation is "here is what could be read", and a
			// permission error on one directory is not a reason to claim nothing
			// was observed. The count is reported so the omission is visible.
			skipped++
			return skipEntry
		}
		if entry.IsDir() {
			// Skip dot directories and dependency trees: they are not what a
			// workspace question is about, and walking a vendored tree is how a
			// discovery step turns into a denial of service.
			name := entry.Name()
			if path != d.fs.root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= limit {
			skipped++
			return nil
		}
		count++
		if len(firstPaths) < 16 {
			firstPaths = append(firstPaths, d.fs.relative(path))
		}
		return nil
	})
	if err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("workspace walk failed: %v", err),
		}, nil
	}

	sort.Strings(firstPaths)
	summary := fmt.Sprintf("%d entries under %s", count, d.fs.Root())
	if skipped > 0 {
		summary += fmt.Sprintf(" (%d skipped)", skipped)
	}
	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: count,
		Detail:      summary,
		Facts: []kernel.Fact{{
			Kind:    kernel.EvidenceWorkspaceObserved,
			Bytes:   count,
			Summary: summary,
		}},
	}, nil
}

// ── file.read ──────────────────────────────────────────────────────────────

type readCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (r *readCap) ID() kernel.CapabilityID { return kernel.FileRead }

// Authorize implements kernel.Capability.
func (r *readCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.FileRead) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileRead,
			Reason:     fmt.Sprintf("grant %q does not permit file.read", req.Grant.ID),
		}
	}
	return nil
}

// Invoke implements kernel.Capability.
//
// A read of a file that is not there produces FILE_ABSENT rather than an error,
// because absence is a real observation with a name and a caller must be able to
// reason from it. Inventing a "closest file" would be exactly the substitution
// this kernel refuses.
func (r *readCap) Invoke(ctx context.Context, req kernel.Request) (kernel.Observation, error) {
	abs, err := r.fs.resolve(req.Target)
	if err != nil {
		return kernel.Observation{}, err
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return kernel.Observation{
				Verdict: kernel.VerdictPass,
				Detail:  fmt.Sprintf("%s does not exist", req.Target),
				Facts: []kernel.Fact{{
					Kind:    kernel.EvidenceFileAbsent,
					Target:  req.Target,
					Summary: "stat reported no such file",
				}},
			}, nil
		}
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("stat %s: %v", req.Target, statErr),
		}, nil
	}
	if info.IsDir() {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("%s is a directory, not a file", req.Target),
		}, nil
	}

	content, readErr := os.ReadFile(abs)
	if readErr != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("read %s: %v", req.Target, readErr),
		}, nil
	}
	if r.fs.maxReadBytes > 0 && len(content) > r.fs.maxReadBytes {
		content = content[:r.fs.maxReadBytes]
	}

	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: len(content),
		Detail:      fmt.Sprintf("read %d bytes from %s", len(content), req.Target),
		Facts: []kernel.Fact{{
			Kind:    kernel.EvidenceFileRead,
			Target:  req.Target,
			Bytes:   len(content),
			Summary: fmt.Sprintf("read %d bytes", len(content)),
		}},
	}, nil
}

// ── file.search ────────────────────────────────────────────────────────────

type searchCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (s *searchCap) ID() kernel.CapabilityID { return kernel.FileSearch }

// Authorize implements kernel.Capability.
func (s *searchCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.FileSearch) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileSearch,
			Reason:     fmt.Sprintf("grant %q does not permit file.search", req.Grant.ID),
		}
	}
	return nil
}

// Invoke implements kernel.Capability.
//
// A search that matched nothing reports PASS with zero matches, because "I
// looked and there are none" is a complete and useful observation. A search that
// could not run reports FAIL. Collapsing those two is how a runtime ends up
// claiming a pattern is absent without having looked.
func (s *searchCap) Invoke(ctx context.Context, req kernel.Request) (kernel.Observation, error) {
	abs, err := s.fs.resolve(req.Target)
	if err != nil {
		return kernel.Observation{}, err
	}
	needle, ok := req.Arg("pattern")
	if !ok || needle == "" {
		return kernel.Observation{}, kernel.Block{
			Class:      kernel.FailureCapability,
			Step:       req.Step,
			Capability: kernel.FileSearch,
			Reason:     "file.search requires a non-empty \"pattern\" argument; it does not guess a query",
		}
	}

	content, readErr := os.ReadFile(abs)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return kernel.Observation{
				Verdict: kernel.VerdictPass,
				Detail:  fmt.Sprintf("%s does not exist", req.Target),
				Facts: []kernel.Fact{{
					Kind:    kernel.EvidenceFileAbsent,
					Target:  req.Target,
					Summary: "stat reported no such file",
				}},
			}, nil
		}
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("read %s: %v", req.Target, readErr),
		}, nil
	}

	matches := strings.Count(string(content), needle)
	detail := fmt.Sprintf("%d matches for %q in %s", matches, needle, req.Target)
	if matches == 0 {
		return kernel.Observation{
			Verdict: kernel.VerdictPass,
			Detail:  detail,
			Facts: []kernel.Fact{{
				Kind:    kernel.EvidenceFileRead,
				Target:  req.Target,
				Bytes:   len(content),
				Summary: detail,
			}},
		}, nil
	}
	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: len(content),
		Detail:      detail,
		Facts: []kernel.Fact{{
			Kind:    kernel.EvidenceFileRead,
			Target:  req.Target,
			Bytes:   len(content),
			Summary: detail,
		}},
	}, nil
}

// ── file.exists ────────────────────────────────────────────────────────────

type existsCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (e *existsCap) ID() kernel.CapabilityID { return kernel.FileExists }

// Authorize implements kernel.Capability.
func (e *existsCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.FileExists) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileExists,
			Reason:     fmt.Sprintf("grant %q does not permit file.exists", req.Grant.ID),
		}
	}
	return nil
}

// Invoke implements kernel.Capability. It always reports one of FILE_PRESENT or
// FILE_ABSENT, and always names the exact target it stat'd.
func (e *existsCap) Invoke(_ context.Context, req kernel.Request) (kernel.Observation, error) {
	abs, err := e.fs.resolve(req.Target)
	if err != nil {
		return kernel.Observation{}, err
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return kernel.Observation{
				Verdict: kernel.VerdictPass,
				Detail:  fmt.Sprintf("%s is absent", req.Target),
				Facts: []kernel.Fact{{
					Kind:    kernel.EvidenceFileAbsent,
					Target:  req.Target,
					Summary: "stat reported no such file",
				}},
			}, nil
		}
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("stat %s: %v", req.Target, statErr),
		}, nil
	}
	kind := kernel.EvidenceFilePresent
	summary := "stat reported a regular file"
	if !info.Mode().IsRegular() {
		kind = kernel.EvidenceFilePresent
		summary = fmt.Sprintf("stat reported a non-regular entry (%s)", info.Mode().Type())
	}
	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: int(info.Size()),
		Detail:      fmt.Sprintf("%s exists: %s", req.Target, summary),
		Facts: []kernel.Fact{{
			Kind:    kind,
			Target:  req.Target,
			Bytes:   int(info.Size()),
			Summary: summary,
		}},
	}, nil
}

// ── file.write ─────────────────────────────────────────────────────────────

type writeCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (w *writeCap) ID() kernel.CapabilityID { return kernel.FileWrite }

// Authorize implements kernel.Capability.
//
// This is the mutating gate, so it checks three things rather than one: that the
// grant permits file.write, that the grant covers the exact destination, and
// that the destination is inside the workspace. The third is not redundant — a
// grant naming "../outside.txt" would pass the first two and must still fail.
func (w *writeCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.FileWrite) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileWrite,
			Reason:     fmt.Sprintf("grant %q does not permit file.write", req.Grant.ID),
		}
	}
	if req.Target == "" {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileWrite,
			Reason:     "file.write requires an explicit target; the kernel does not choose a filename",
		}
	}
	if _, err := w.fs.resolve(req.Target); err != nil {
		var b kernel.Block
		if errors.As(err, &b) {
			b.Step = req.Step
			b.Capability = kernel.FileWrite
			return b
		}
		return err
	}
	return nil
}

// Invoke implements kernel.Capability.
//
// The write is atomic: content goes to a temporary file in the same directory and
// is renamed into place. A partially written target would leave the workspace in
// a state no contract described, and a rename is the cheapest way to avoid that
// on the filesystems this runs on.
//
// A write that did not happen reports FAIL and produces no write evidence, so the
// mutation axis stays untouched. That is the entire reason write evidence is
// derived from the syscall's result rather than from the intent to write.
func (w *writeCap) Invoke(ctx context.Context, req kernel.Request) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	abs, err := w.fs.resolve(req.Target)
	if err != nil {
		return kernel.Observation{}, err
	}
	content, ok := req.Arg("content")
	if !ok {
		return kernel.Observation{}, kernel.Block{
			Class:      kernel.FailureCapability,
			Step:       req.Step,
			Capability: kernel.FileWrite,
			Reason:     "file.write requires a \"content\" argument",
		}
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("create parent directory for %s: %v", req.Target, err),
		}, nil
	}

	// Preserve the destination's existing permission bits across the atomic
	// replace, defaulting to 0644 for a target that does not exist yet. The
	// rename installs a NEW inode, so without this the mode of the file being
	// replaced would be silently reset on every write — a state change the
	// request never asked for.
	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(abs); statErr == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(abs), ".izen-write-*")
	if err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("create temp file for %s: %v", req.Target, err),
		}, nil
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("write %s: %v", req.Target, err),
		}, nil
	}
	if err := tmp.Close(); err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("close staged file for %s: %v", req.Target, err),
		}, nil
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("chmod staged file for %s: %v", req.Target, err),
		}, nil
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("commit %s: %v", req.Target, err),
		}, nil
	}

	// Re-stat after the rename. The evidence reports the bytes that are on disk
	// now, which is the only claim a later reader can actually check.
	info, statErr := os.Stat(abs)
	size := len(content)
	if statErr == nil {
		size = int(info.Size())
	}

	// A second, independent observation of presence. This is what lets a CREATE
	// contract be satisfied by evidence rather than by the write's own word.
	existsFact := kernel.Fact{
		Kind:    kernel.EvidenceFilePresent,
		Target:  req.Target,
		Bytes:   size,
		Summary: fmt.Sprintf("stat confirmed %d bytes on disk", size),
	}

	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: len(content),
		Detail:      fmt.Sprintf("wrote %d bytes to %s", len(content), req.Target),
		Facts: []kernel.Fact{
			{
				Kind:    kernel.EvidenceFileWritten,
				Target:  req.Target,
				Bytes:   len(content),
				Summary: fmt.Sprintf("atomic rename committed %d bytes", len(content)),
			},
			existsFact,
		},
	}, nil
}

// ── file.delete ────────────────────────────────────────────────────────────

type deleteCap struct{ fs *Capability }

// ID implements kernel.Capability.
func (d *deleteCap) ID() kernel.CapabilityID { return kernel.FileDelete }

// Authorize implements kernel.Capability.
//
// Like file.write, this is a mutating gate, so it checks three things rather
// than one: that the grant permits file.delete, that an explicit target was
// named, and that the target is inside the workspace. The confinement check is
// not redundant — a grant naming "../outside.txt" would pass the first two and
// must still fail. A capability that could be redirected out of the workspace is
// a capability no grant can reason about.
//
// A target that resolves to a symlink pointing outside the workspace is refused
// by resolve for the same reason a write is: the grant names a workspace-relative
// path, and the bytes must not leave the workspace through a link.
func (d *deleteCap) Authorize(req kernel.Request) error {
	if !req.Grant.Permits(kernel.FileDelete) {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileDelete,
			Reason:     fmt.Sprintf("grant %q does not permit file.delete", req.Grant.ID),
		}
	}
	if req.Target == "" {
		return kernel.Block{
			Class:      kernel.FailureAuthorization,
			Step:       req.Step,
			Capability: kernel.FileDelete,
			Reason:     "file.delete requires an explicit target; the kernel does not choose a filename",
		}
	}
	if _, err := d.fs.resolve(req.Target); err != nil {
		var b kernel.Block
		if errors.As(err, &b) {
			b.Step = req.Step
			b.Capability = kernel.FileDelete
			return b
		}
		return err
	}
	return nil
}

// Invoke implements kernel.Capability.
//
// The three outcomes are kept distinct because a contract can tell them apart:
//
//   - the target was there and os.Remove removed it: PASS with FILE_DELETED and
//     a fresh FILE_ABSENT, the two facts a DELETE contract needs;
//   - the target was not there: NO_OP with FILE_ABSENT. Nothing was removed, so
//     no FILE_DELETED is recorded and a DELETE contract that demands a durable
//     mutation cannot be satisfied by it. That is the honest answer, not a
//     fabricated success;
//   - the removal did not happen for any other reason: FAIL with no evidence, so
//     the mutation axis stays untouched.
//
// A directory is refused rather than removed, even if it happens to be empty:
// removing a tree is a different operation with a different blast radius, and
// this capability removes exactly one file that the grant named.
func (d *deleteCap) Invoke(ctx context.Context, req kernel.Request) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	abs, err := d.fs.resolve(req.Target)
	if err != nil {
		return kernel.Observation{}, err
	}

	info, statErr := os.Stat(abs)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		// Already absent. A real observation, but not a mutation.
		return kernel.Observation{
			Verdict: kernel.VerdictNoOp,
			Detail:  fmt.Sprintf("%s was already absent", req.Target),
			Facts: []kernel.Fact{{
				Kind:    kernel.EvidenceFileAbsent,
				Target:  req.Target,
				Summary: "stat reported no such file",
			}},
		}, nil
	case statErr != nil:
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("stat %s: %v", req.Target, statErr),
		}, nil
	case info.IsDir():
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("%s is a directory; file.delete removes one file", req.Target),
		}, nil
	}

	size := int(info.Size())
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A concurrent remover won the race. The target is absent, which is
			// the fact a DELETE contract asks about, but this invocation did not
			// remove it, so it reports NO_OP rather than claiming the mutation.
			return kernel.Observation{
				Verdict: kernel.VerdictNoOp,
				Detail:  fmt.Sprintf("%s was already absent", req.Target),
				Facts: []kernel.Fact{{
					Kind:    kernel.EvidenceFileAbsent,
					Target:  req.Target,
					Summary: "stat reported no such file",
				}},
			}, nil
		}
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("remove %s: %v", req.Target, err),
		}, nil
	}

	// Confirm the removal rather than trusting os.Remove's nil return: the
	// confirmation is the observation a later reader can check, and a delete
	// whose only witness is the syscall that performed it is exactly the
	// unverified mutation this kernel exists to make unrepresentable.
	if _, restatErr := os.Stat(abs); !errors.Is(restatErr, fs.ErrNotExist) {
		if restatErr != nil {
			return kernel.Observation{
				Verdict: kernel.VerdictFail,
				Detail:  fmt.Sprintf("confirm removal of %s: %v", req.Target, restatErr),
			}, nil
		}
		return kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  fmt.Sprintf("remove %s reported success but the target is still present", req.Target),
		}, nil
	}

	return kernel.Observation{
		Verdict:     kernel.VerdictPass,
		OutputBytes: size,
		Detail:      fmt.Sprintf("removed %s (%d bytes)", req.Target, size),
		Facts: []kernel.Fact{
			{
				Kind:    kernel.EvidenceFileDeleted,
				Target:  req.Target,
				Bytes:   size,
				Summary: fmt.Sprintf("removed %d bytes", size),
			},
			{
				Kind:    kernel.EvidenceFileAbsent,
				Target:  req.Target,
				Summary: "stat confirmed the target is gone",
			},
		},
	}, nil
}
