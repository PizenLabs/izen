package command

import "strings"

// Permission is a single capability bit in a permission mask. A workspace or a
// command descriptor carries a PermissionSet that answers one question: "What
// may this actor do?"
type Permission uint8

const (
	// PermRead grants read-only access to files, git history, and the AST graph.
	PermRead Permission = 1 << 0
	// PermAnalyze grants bounded forensic analysis: searching, tracing, and
	// diagnostics without mutating the workspace.
	PermAnalyze Permission = 1 << 1
	// PermExecute grants running binaries: build steps, test suites, and the
	// application itself.
	PermExecute Permission = 1 << 2
	// PermWrite grants mutation of workspace files.
	PermWrite Permission = 1 << 3
)

// String returns the canonical label of a single permission bit.
func (p Permission) String() string {
	switch p {
	case PermRead:
		return "read"
	case PermAnalyze:
		return "analyze"
	case PermExecute:
		return "execute"
	case PermWrite:
		return "write"
	default:
		return "unknown"
	}
}

// PermissionSet is a bitmask of Permission bits carried by a workspace or a
// command descriptor.
type PermissionSet uint8

// Has reports whether the set contains the given permission.
func (s PermissionSet) Has(p Permission) bool {
	return s&PermissionSet(p) != 0
}

// Contains reports whether the set contains every permission in required
// (bitwise subset logic). The empty set is contained by every set.
func (s PermissionSet) Contains(required PermissionSet) bool {
	return s&required == required
}

// String returns a compact, sorted-by-name list of the set bits.
func (s PermissionSet) String() string {
	var out []string
	for _, p := range []Permission{PermRead, PermAnalyze, PermExecute, PermWrite} {
		if s.Has(p) {
			out = append(out, p.String())
		}
	}
	return strings.Join(out, "|")
}
