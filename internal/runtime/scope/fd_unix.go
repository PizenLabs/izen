//go:build unix

package scope

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// wrapEscapef formats an ErrWorkspaceEscape error.
func wrapEscapef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrWorkspaceEscape, fmt.Sprintf(format, args...))
}

// fdWalk performs an FD-relative (openat-family) verification walk
// anchored at the held workspace-root FD. Intermediate components are
// opened with O_NOFOLLOW so a symlink planted between planning-time
// resolution and execution-time use is detected at use time rather than
// trusted from a string path. Internal symlinks resolving within the
// root are permitted; external ones fail closed with ErrWorkspaceEscape.
// Missing (not-yet-created) components terminate the walk — the portable
// EvalSymlinks containment checks in scope.go remain authoritative for
// them.
func (r *Root) fdWalk(clean string) error {
	parts := splitParts(clean)
	if len(parts) == 0 {
		return nil
	}
	parent := int(r.dir.Fd())
	var owned []int
	defer func() {
		for _, fd := range owned {
			_ = unix.Close(fd)
		}
	}()
	cur := parent
	for i, p := range parts {
		last := i == len(parts)-1
		if last {
			var st unix.Stat_t
			if err := unix.Fstatat(cur, p, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				// Absent or unreadable terminal: portable checks decide.
				return nil
			}
			if st.Mode&unix.S_IFMT != unix.S_IFLNK {
				return nil
			}
			// Terminal symlink at use time: resolve the FULL path and
			// require containment. checkTerminal re-enforces this
			// portably; this FD-anchored observation just fails fast.
			full := filepath.Join(r.root, filepath.Join(parts...))
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil {
				return wrapEscapef("terminal symlink %q is dangling", clean)
			}
			if !r.within(resolved) {
				return wrapEscapef("target %q is a symlink escaping workspace", clean)
			}
			return nil
		}
		fd, err := unix.Openat(cur, p,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if err == unix.ENOENT || err == unix.ENOTDIR {
				// Missing component or file prefix: portable checks
				// own the verdict for not-yet-created paths.
				return nil
			}
			if err == unix.ELOOP {
				// Symlink directory at use time: containment decides.
				full := filepath.Join(r.root, filepath.Join(parts[:i+1]...))
				resolved, rerr := filepath.EvalSymlinks(full)
				if rerr != nil {
					return wrapEscapef("dangling symlink %q", full)
				}
				if !r.within(resolved) {
					return wrapEscapef("target traverses symlink %q", full)
				}
				// Internal symlink: continue the walk from the
				// resolved directory (followed, still inside root).
				fd2, oerr := unix.Open(resolved,
					unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				if oerr != nil {
					if os.IsNotExist(oerr) {
						return nil
					}
					return nil
				}
				owned = append(owned, fd2)
				cur = fd2
				continue
			}
			// Any other error (permissions, etc.): do not invent a
			// denial here; portable checks produce the verdict.
			return nil
		}
		owned = append(owned, fd)
		cur = fd
	}
	return nil
}

func splitParts(clean string) []string {
	raw := strings.Split(clean, string(filepath.Separator))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p == "" || p == "." {
			continue
		}
		out = append(out, p)
	}
	return out
}
