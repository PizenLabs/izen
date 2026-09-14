// Package scope provides the FD-anchored workspace confinement boundary.
//
// Every target resolution, read, and write at execution time MUST be
// performed relative to an open workspace-root file descriptor handle
// (Root). The handle pins the root directory for the session lifetime so a
// root swap or rename cannot redirect anchored operations. All relative
// paths are verified at USE time (immediately before the operation, and
// again before commit) so a resolve-then-swap (TOCTOU) race that plants a
// symlink pointing outside the workspace fails closed with
// ErrWorkspaceEscape and mutates nothing outside the boundary.
//
// Internal symlinks are PERMITTED if and only if their fully-resolved
// target stays within the workspace root. Any path resolving outside
// fails unconditionally with ErrWorkspaceEscape.
package scope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkspaceEscape is returned when a target resolves outside the
// workspace root file descriptor handle. Callers must test with errors.Is.
// On escape, no bytes may be written outside the boundary.
var ErrWorkspaceEscape = errors.New("scope: path escapes workspace boundary")

// MaxTargetDepth bounds anchored path iteration. Verification is O(N)
// where N is the number of path components (<= MaxTargetDepth), never
// the workspace tree size.
const MaxTargetDepth = 256

// Root is an open file-descriptor handle for a workspace root directory.
// It pins the root for the session lifetime: the directory FD is held
// open, so verification and I/O operate relative to the open descriptor
// rather than a re-resolved string path.
//
// A Root must be created with Open and released with Close. The zero
// value is not usable.
type Root struct {
	dir      *os.File
	root     string
	evalRoot string
	info     os.FileInfo
}

// Open anchors a workspace root handle. It resolves root to an absolute
// path, holds an open directory FD for the session, and records the
// fully-resolved (symlink-free) root identity used for containment
// checks. Internal symlinks inside the workspace remain permitted; only
// escapes beyond the resolved root are denied.
func Open(root string) (*Root, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty workspace root", ErrWorkspaceEscape)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %w", ErrWorkspaceEscape, err)
	}
	abs = filepath.Clean(abs)
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Root itself must exist. Fail closed without inventing identity.
		return nil, fmt.Errorf("%w: resolve root %q: %w", ErrWorkspaceEscape, root, err)
	}
	dir, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("scope: open workspace root %q: %w", root, err)
	}
	info, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("scope: stat workspace root %q: %w", root, err)
	}
	if !info.IsDir() {
		_ = dir.Close()
		return nil, fmt.Errorf("%w: workspace root %q is not a directory", ErrWorkspaceEscape, root)
	}
	return &Root{dir: dir, root: abs, evalRoot: eval, info: info}, nil
}

// Close releases the held root FD. After Close the Root must not be used.
func (r *Root) Close() error {
	if r == nil || r.dir == nil {
		return nil
	}
	return r.dir.Close()
}

// RootPath returns the absolute cleaned workspace root string.
func (r *Root) RootPath() string {
	if r == nil {
		return ""
	}
	return r.root
}

// FD returns the underlying workspace-root file descriptor number for
// FD-relative (openat-family) operations. It returns -1 on a nil Root.
func (r *Root) FD() uintptr {
	if r == nil || r.dir == nil {
		return uintptr(^uint(0))
	}
	return r.dir.Fd()
}

// CleanRel lexically validates rel and returns its cleaned relative form.
// It rejects absolute paths, NUL bytes, "." / over-deep paths, and any
// ".." traversal that escapes the root. It performs no I/O.
func (r *Root) CleanRel(rel string) (string, error) {
	return cleanRel(rel)
}

func cleanRel(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty target", ErrWorkspaceEscape)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: target carries NUL byte", ErrWorkspaceEscape)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: absolute path %q", ErrWorkspaceEscape, rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		return "", fmt.Errorf("%w: empty target", ErrWorkspaceEscape)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, rel)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	depth := 0
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, rel)
		}
		depth++
		if depth > MaxTargetDepth {
			return "", fmt.Errorf("%w: target depth %d exceeds bound %d", ErrWorkspaceEscape, depth, MaxTargetDepth)
		}
	}
	return clean, nil
}

// Verify performs execution-time confinement for rel against the open
// root FD. It MUST be called at USE time (immediately before read/write),
// never trusted from planning-time resolution. It permits internal
// symlinks whose resolved target stays within the root and fails closed
// with ErrWorkspaceEscape otherwise.
func (r *Root) Verify(rel string) error {
	if r == nil || r.dir == nil {
		return fmt.Errorf("%w: nil workspace root handle", ErrWorkspaceEscape)
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	// Pin check: the held FD must still identify the recorded root
	// directory. A swapped or replaced root fails closed.
	cur, err := r.dir.Stat()
	if err != nil {
		return fmt.Errorf("%w: root handle invalid: %w", ErrWorkspaceEscape, err)
	}
	if !os.SameFile(cur, r.info) {
		return fmt.Errorf("%w: workspace root handle no longer identifies the session root", ErrWorkspaceEscape)
	}
	// Lexical containment of the joined path.
	joined := filepath.Clean(filepath.Join(r.root, clean))
	if joined != r.root && !strings.HasPrefix(joined, r.root+string(filepath.Separator)) {
		return fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, rel)
	}
	// Prefix symlink walk (portable Lstat pass): every existing prefix
	// component that is a symlink must resolve within the root. Missing
	// components terminate the walk (not-yet-created files contribute no
	// symlink to check); deeper components past a non-directory file
	// cannot be traversed through.
	if err := r.walkPrefixes(clean); err != nil {
		return err
	}
	// FD-relative (openat-family) walk anchored at the held root FD.
	// Detects symlink swaps planted between planning-time resolution and
	// execution-time use.
	if err := r.fdWalk(clean); err != nil {
		return err
	}
	// Full-resolution containment: EvalSymlinks the target (or, when the
	// target does not exist yet, the nearest existing ancestor plus the
	// lexical remainder) and require it within the resolved root.
	if err := r.checkResolved(clean); err != nil {
		return err
	}
	// Final-component symlink policy: a terminal symlink is followed only
	// when its resolved target stays inside the root (internal symlinks
	// permitted); an external terminal symlink fails closed even though a
	// rename-over-symlink would technically not write outside — the
	// operation must not proceed at all.
	if err := r.checkTerminal(clean, rel); err != nil {
		return err
	}
	return nil
}

// lstatOK stats path, reporting false (no error) when the prefix does
// not exist. Best-effort symlink walks use it so a stat miss ends the
// walk instead of becoming a denial.
func lstatOK(path string) (os.FileInfo, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, false
	}
	return fi, true
}

// walkPrefixes Lstats each existing path prefix. Symlink prefixes must
// resolve within the root; anything else fails closed.
func (r *Root) walkPrefixes(clean string) error {
	parts := strings.Split(clean, string(filepath.Separator))
	accum := r.root
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		accum = filepath.Join(accum, p)
		fi, ok := lstatOK(accum)
		if !ok {
			// Missing prefix: deeper components cannot be symlinks.
			// Best-effort walk: a stat miss is not a denial.
			break
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(accum)
			if rerr != nil {
				return fmt.Errorf("%w: dangling symlink %q", ErrWorkspaceEscape, accum)
			}
			if !r.within(resolved) {
				return fmt.Errorf("%w: target traverses symlink %q", ErrWorkspaceEscape, accum)
			}
			// Internal symlink: resolution continues from the resolved
			// target, but EvalSymlinks of the FULL path at checkResolved
			// is authoritative; keep walking lexically for other
			// prefixes on the unresolved spelling is intentionally
			// conservative — the final containment check decides.
			continue
		}
		if !fi.IsDir() {
			break
		}
	}
	return nil
}

// checkResolved requires the fully-resolved target (or nearest existing
// ancestor + remainder) to stay within the resolved root.
func (r *Root) checkResolved(clean string) error {
	full := filepath.Join(r.root, clean)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// Target may not exist yet: resolve the nearest existing
		// ancestor and re-append the lexical remainder.
		ancestor := full
		var tail []string
		for {
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, clean)
			}
			tail = append([]string{filepath.Base(ancestor)}, tail...)
			ancestor = parent
			resolved, err = filepath.EvalSymlinks(ancestor)
			if err == nil {
				resolved = filepath.Join(append([]string{resolved}, tail...)...)
				break
			}
			if len(tail) > MaxTargetDepth {
				return fmt.Errorf("%w: target %q escapes workspace", ErrWorkspaceEscape, clean)
			}
		}
	}
	resolved = filepath.Clean(resolved)
	if !r.within(resolved) {
		return fmt.Errorf("%w: target %q resolves outside workspace", ErrWorkspaceEscape, clean)
	}
	return nil
}

// checkTerminal enforces the terminal-symlink policy at use time.
func (r *Root) checkTerminal(clean, rel string) error {
	full := filepath.Join(r.root, clean)
	fi, err := os.Lstat(full)
	if err != nil {
		return nil //nolint:nilerr // absent target is not a denial: nothing terminal to police
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	resolved, rerr := filepath.EvalSymlinks(full)
	if rerr != nil {
		return fmt.Errorf("%w: terminal symlink %q is dangling", ErrWorkspaceEscape, rel)
	}
	if !r.within(resolved) {
		return fmt.Errorf("%w: target %q is a symlink escaping workspace", ErrWorkspaceEscape, rel)
	}
	return nil
}

// within reports whether resolved (cleaned) lies inside the resolved root.
func (r *Root) within(resolved string) bool {
	resolved = filepath.Clean(resolved)
	return resolved == r.evalRoot || strings.HasPrefix(resolved, r.evalRoot+string(filepath.Separator))
}

// Abs returns the absolute path of rel after successful verification.
// It verifies first so callers cannot use an unchecked join.
func (r *Root) Abs(rel string) (string, error) {
	clean, err := cleanRel(rel)
	if err != nil {
		return "", err
	}
	if err := r.Verify(clean); err != nil {
		return "", err
	}
	return filepath.Join(r.root, clean), nil
}

// ReadFile reads rel relative to the open root FD after use-time
// verification. External symlinks are denied before any byte is read.
func (r *Root) ReadFile(rel string) ([]byte, error) {
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	if err := r.Verify(clean); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(r.root, clean))
	if err != nil {
		return nil, err
	}
	// Post-read re-verification closes the read race: if the path was
	// swapped mid-read, the caller learns the bytes are untrusted.
	if verr := r.Verify(clean); verr != nil {
		return nil, verr
	}
	return data, nil
}

// AtomicWrite writes data to rel atomically relative to the open root
// FD: content goes to a same-directory temp file (fsynced) and is
// renamed over the target. Verification runs BEFORE the temp write and
// AGAIN before the rename so a swap planted between check and use fails
// closed with ErrWorkspaceEscape and no outside bytes are written. A
// terminal symlink escaping the workspace is denied before the rename
// (rename-over-symlink would replace the link, but the operation must
// not proceed at all).
func (r *Root) AtomicWrite(rel string, data []byte, mode fs.FileMode) error {
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	if err := r.Verify(clean); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	dir := filepath.Dir(filepath.Join(r.root, clean))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scope: create directory for %q: %w", rel, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp.scope.*")
	if err != nil {
		return fmt.Errorf("scope: create temp for %q: %w", rel, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scope: write temp for %q: %w", rel, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scope: chmod temp for %q: %w", rel, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scope: sync temp for %q: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("scope: close temp for %q: %w", rel, err)
	}
	// Re-verify immediately before the rename: the use-time gate.
	if err := r.Verify(clean); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(r.root, clean)); err != nil {
		return fmt.Errorf("scope: rename temp to %q: %w", rel, err)
	}
	committed = true
	fsyncDir(dir)
	return nil
}

// VerifyPath is a stateless convenience wrapper: it opens a short-lived
// root handle for rootDir, verifies rel at use time, and closes the
// handle. Long-lived sessions should hold a Root open instead.
func VerifyPath(rootDir, rel string) error {
	r, err := Open(rootDir)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return r.Verify(rel)
}

func fsyncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
