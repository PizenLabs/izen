//go:build !unix

package scope

import "fmt"

// fdWalk is the portable fallback for non-Unix platforms: the Lstat
// prefix walk plus EvalSymlinks containment in scope.go is authoritative
// there. It is a no-op returning nil so Verify stays correct everywhere.
func (r *Root) fdWalk(clean string) error { return nil }

// wrapEscapef is defined in fd_unix.go for Unix builds; the portable
// fallback needs the same helper.
func wrapEscapef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrWorkspaceEscape, fmt.Sprintf(format, args...))
}
