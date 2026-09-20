package target

import (
	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// ErrWorkspaceEscape is the execution-boundary sentinel for targets that
// resolve outside the workspace root FD handle. It aliases
// scope.ErrWorkspaceEscape so callers can match with errors.Is against
// either package.
var ErrWorkspaceEscape = scope.ErrWorkspaceEscape

// ResolveAnchored resolves rawTarget relative to workdir through the
// VCS > Filesystem > Raw authority chain, then verifies the canonical
// identity at USE time against the open workspace-root FD handle.
//
// Planning-time resolution alone MUST NOT be trusted at execution time:
// the caller must pass the session's *scope.Root so a symlink swapped in
// between resolution and use fails closed with ErrWorkspaceEscape.
// Internal symlinks resolving within the root remain permitted.
//
// A nil root skips FD verification (legacy behavior) and is only
// appropriate for read-only planning paths; every execution-time caller
// must provide a non-nil root.
func (r *TargetResolver) ResolveAnchored(workdir string, rawTarget string, root *scope.Root) (*TargetRef, error) {
	ref, err := r.Resolve(workdir, rawTarget)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return ref, nil
	}
	// Verify the canonical identity at use time. ResolutionRaw targets
	// (not-yet-created files) verify their nearest existing ancestor;
	// existing targets verify full symlink resolution.
	if verr := root.Verify(ref.Canonical); verr != nil {
		return nil, verr
	}
	return ref, nil
}

// VerifyAtUse re-verifies an already-resolved TargetRef against the open
// root FD immediately before patch application. Execution paths must
// call this after any untrusted interval (e.g. between approval and
// commit) so a TOCTOU symlink swap fails closed with ErrWorkspaceEscape.
func VerifyAtUse(root *scope.Root, ref *TargetRef) error {
	if ref == nil {
		return ErrWorkspaceEscape
	}
	if root == nil {
		return nil
	}
	return root.Verify(ref.Canonical)
}
