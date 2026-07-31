package capability

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/internal/workspace/snapshot"
)

// DiagnosticCapability represents a class of diagnostics or actions that a
// project archetype supports. Unlike the runtime permission bits in
// core/capability, these describe what the workspace can meaningfully
// process — not what the operator is allowed to do.
type DiagnosticCapability int

const (
	CapFileInspect DiagnosticCapability = iota
	CapASTParse
	CapStaticServe
	CapGoBuild
	CapGoTest
	CapGoVet
	CapGoMod
	CapNpmTest
	CapNpmBuild
	CapLinter
)

func (dc DiagnosticCapability) String() string {
	switch dc {
	case CapFileInspect:
		return "file_inspect"
	case CapASTParse:
		return "ast_parse"
	case CapStaticServe:
		return "static_serve"
	case CapGoBuild:
		return "go_build"
	case CapGoTest:
		return "go_test"
	case CapGoVet:
		return "go_vet"
	case CapGoMod:
		return "go_mod"
	case CapNpmTest:
		return "npm_test"
	case CapNpmBuild:
		return "npm_build"
	case CapLinter:
		return "linter"
	default:
		return fmt.Sprintf("diagnostic_capability(%d)", int(dc))
	}
}

// ArchetypeCapabilityRegistry maps project archetypes to their allowed
// diagnostic capabilities. It ensures that only relevant diagnostics are
// dispatched for a given workspace type — e.g., Go tools are never run
// against a VANILLA_WEB project.
type ArchetypeCapabilityRegistry struct {
	mu   sync.RWMutex
	caps map[snapshot.Archetype][]DiagnosticCapability
}

// NewArchetypeCapabilityRegistry returns a registry pre-populated with
// default capability mappings for all known archetypes.
func NewArchetypeCapabilityRegistry() *ArchetypeCapabilityRegistry {
	r := &ArchetypeCapabilityRegistry{
		caps: make(map[snapshot.Archetype][]DiagnosticCapability),
	}
	for a, caps := range defaultCapabilities() {
		r.caps[a] = caps
	}
	return r
}

// GetCapabilities returns the diagnostic capabilities granted to archetype a.
// Returns an empty (non-nil) slice for unknown archetypes.
func (r *ArchetypeCapabilityRegistry) GetCapabilities(a snapshot.Archetype) []DiagnosticCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if caps, ok := r.caps[a]; ok {
		result := make([]DiagnosticCapability, len(caps))
		copy(result, caps)
		return result
	}
	return []DiagnosticCapability{}
}

// Has reports whether archetype a grants capability c.
func (r *ArchetypeCapabilityRegistry) Has(a snapshot.Archetype, c DiagnosticCapability) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if caps, ok := r.caps[a]; ok {
		for _, cap := range caps {
			if cap == c {
				return true
			}
		}
	}
	return false
}

// HasAny reports whether archetype a grants at least one of the given
// capabilities.
func (r *ArchetypeCapabilityRegistry) HasAny(a snapshot.Archetype, cs ...DiagnosticCapability) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if caps, ok := r.caps[a]; ok {
		capSet := make(map[DiagnosticCapability]bool, len(caps))
		for _, c := range caps {
			capSet[c] = true
		}
		for _, c := range cs {
			if capSet[c] {
				return true
			}
		}
	}
	return false
}

// Register associates one or more diagnostic capabilities with the given
// archetype. Existing entries are merged (not replaced).
func (r *ArchetypeCapabilityRegistry) Register(a snapshot.Archetype, caps ...DiagnosticCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caps[a] = append(r.caps[a], caps...)
}

// SetCapabilities replaces the capability list for archetype a.
func (r *ArchetypeCapabilityRegistry) SetCapabilities(a snapshot.Archetype, caps []DiagnosticCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]DiagnosticCapability, len(caps))
	copy(result, caps)
	r.caps[a] = result
}

// ArchetypeHasGoTools is a convenience method that checks whether the
// archetype supports any Go-specific capabilities (CapGoBuild, CapGoTest,
// CapGoVet, CapGoMod).
func (r *ArchetypeCapabilityRegistry) ArchetypeHasGoTools(a snapshot.Archetype) bool {
	return r.HasAny(a, CapGoBuild, CapGoTest, CapGoVet, CapGoMod)
}

// ArchetypeHasNpmTools is a convenience method that checks whether the
// archetype supports any npm-specific capabilities.
func (r *ArchetypeCapabilityRegistry) ArchetypeHasNpmTools(a snapshot.Archetype) bool {
	return r.HasAny(a, CapNpmTest, CapNpmBuild)
}

func defaultCapabilities() map[snapshot.Archetype][]DiagnosticCapability {
	return map[snapshot.Archetype][]DiagnosticCapability{
		snapshot.VANILLA_WEB: {
			CapFileInspect,
			CapASTParse,
			CapStaticServe,
		},
		snapshot.GO_MODULE: {
			CapFileInspect,
			CapASTParse,
			CapGoBuild,
			CapGoTest,
			CapGoVet,
			CapGoMod,
			CapLinter,
		},
		snapshot.NODE_APP: {
			CapFileInspect,
			CapASTParse,
			CapStaticServe,
			CapNpmTest,
			CapNpmBuild,
			CapLinter,
		},
		snapshot.RUST_CARGO: {
			CapFileInspect,
			CapASTParse,
			CapLinter,
		},
		snapshot.PYTHON_ENV: {
			CapFileInspect,
			CapASTParse,
			CapLinter,
		},
		snapshot.GENERIC_TEXT: {
			CapFileInspect,
		},
	}
}
