package understanding

// ProjectKind classifies the structural state of the relevant workspace
// area. The semantics are strict:
//
//	EXISTING   — repository evidence demonstrates an existing relevant
//	             project structure (manifest, source tree, entrypoint, ...).
//	GREENFIELD — evidence demonstrates the relevant area is genuinely
//	             absent AND creation is the expected structural state
//	             (effectively empty workspace: no manifests, no source).
//	             A missing single target file NEVER implies GREENFIELD.
//	UNKNOWN    — evidence is insufficient, contradictory, or unavailable
//	             (unreadable root, unrecognized content, discovery failure).
//	             Derivation failure is UNKNOWN, never GREENFIELD.
type ProjectKind string

const (
	// KindExisting marks an evidence-backed existing project structure.
	KindExisting ProjectKind = "EXISTING"
	// KindGreenfield marks a genuinely empty workspace awaiting creation.
	KindGreenfield ProjectKind = "GREENFIELD"
	// KindUnknown marks insufficient/contradictory/unavailable evidence.
	// The system must fail toward this value, never toward GREENFIELD.
	KindUnknown ProjectKind = "UNKNOWN"
)

// String returns the canonical kind label.
func (k ProjectKind) String() string { return string(k) }

// Valid reports whether k is a known classification.
func (k ProjectKind) Valid() bool {
	switch k {
	case KindExisting, KindGreenfield, KindUnknown:
		return true
	}
	return false
}
