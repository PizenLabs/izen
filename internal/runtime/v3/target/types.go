// Package target resolves canonical path identity for raw, user-supplied
// target strings according to the strict authority chain:
//
//	VCS (Git Index Authority) > OS Filesystem (Case-Preserved Identity) > User Raw Spelling
//
// The VCS stage is authoritative because the Git index preserves the exact
// path a file was committed under, which may differ in case from both the
// user's spelling and the physical directory entry. The filesystem stage
// preserves the case of the physical directory entry for untracked files.
// Only when neither authority can locate the target does the resolver fall
// back to the raw user spelling (a new, not-yet-created file).
package target

// ResolutionSource identifies which authority resolved a target's canonical
// identity.
type ResolutionSource int

const (
	// ResolutionVCS means the canonical path came from the Git index
	// (git ls-files) of a repository containing the working directory.
	ResolutionVCS ResolutionSource = iota
	// ResolutionFilesystem means the canonical path came from a
	// case-insensitive match against physical directory entries.
	ResolutionFilesystem
	// ResolutionRaw means no authority could resolve the target; the raw
	// user spelling is used verbatim (a new/not-yet-created file).
	ResolutionRaw
)

// String returns a stable, lowercase label for the resolution source.
func (s ResolutionSource) String() string {
	switch s {
	case ResolutionVCS:
		return "vcs"
	case ResolutionFilesystem:
		return "filesystem"
	case ResolutionRaw:
		return "raw"
	default:
		return "unknown"
	}
}

// TargetRef is the resolved identity of a target. Raw preserves the exact
// prompt input, Canonical holds the authoritative case-preserved path, and
// Exists/Tracked/Source describe how the target was resolved.
type TargetRef struct {
	// Raw is the exact prompt input, e.g. "readme.md".
	Raw string `json:"raw"`
	// Canonical is the resolved path identity, e.g. "README.md".
	Canonical string `json:"canonical"`
	// Exists reports whether the target currently exists in the working
	// directory or Git index.
	Exists bool `json:"exists"`
	// Tracked reports whether the target is tracked by the Git index.
	Tracked bool `json:"tracked"`
	// Source identifies the authority that resolved the target.
	Source ResolutionSource `json:"source"`
}
