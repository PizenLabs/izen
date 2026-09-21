package understanding

// EvidenceKind classifies where one understanding evidence record came
// from. Every record is repository-derived except ModelProposal, which is
// explicitly marked as unverified hypothesis and can never silently become
// authoritative state.
type EvidenceKind string

const (
	// EvidenceManifest is a module/package manifest (go.mod, package.json,
	// Cargo.toml, pyproject.toml, ...).
	EvidenceManifest EvidenceKind = "manifest"
	// EvidenceConfig is a framework/build configuration file
	// (vite.config.*, tailwind.config.*, tsconfig.json, Makefile, ...).
	EvidenceConfig EvidenceKind = "config"
	// EvidenceStructure is filesystem topology (entrypoint file, source
	// directory, asset directory, ...).
	EvidenceStructure EvidenceKind = "structure"
	// EvidenceLanguage is source-language detection by file extension.
	EvidenceLanguage EvidenceKind = "language"
	// EvidenceDependency is a declared dependency inside a manifest
	// (e.g. package.json declares react).
	EvidenceDependency EvidenceKind = "dependency"
	// EvidenceReference is an intra-workspace reference (HTML script /
	// stylesheet link, import edge, ...).
	EvidenceReference EvidenceKind = "reference"
	// EvidenceVCS is version-control state already available to the
	// workspace (git presence/branch/dirty).
	EvidenceVCS EvidenceKind = "vcs"
	// EvidenceAbsence records the meaningful absence of an expected
	// signal. Absence is evidence of uncertainty, never proof of
	// GREENFIELD on its own.
	EvidenceAbsence EvidenceKind = "absence"
)

// Evidence is one auditable repository fact supporting a ProjectUnderstanding.
// It is immutable after construction and always traceable to a concrete
// signal (file path, manifest entry, directory, VCS state).
type Evidence struct {
	// Kind classifies the signal source.
	Kind EvidenceKind `json:"kind"`
	// ID is the exact signal identifier, e.g. "manifest:go.mod" or
	// "structure:index.html".
	ID string `json:"id"`
	// Detail is a human-readable justification.
	Detail string `json:"detail"`
	// Weight is the contribution to the understanding confidence score.
	Weight float64 `json:"weight"`
}

// Key returns the canonical signal key ("<kind>:<id>").
func (e Evidence) Key() string { return string(e.Kind) + ":" + e.ID }
