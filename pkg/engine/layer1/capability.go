// Package layer1 implements the Workspace Capability Graph of the Izen
// engine. It auto-detects a workspace stack and maps it onto a strongly-typed
// Capability enum, resolving each capability to a concrete, deterministic
// command. It is fully decoupled from Layer 0 (pkg/engine/layer0) to preserve
// the strict separation between knowledge resolution and capability graph.
package layer1

// Capability is a strongly-typed workspace capability.
type Capability string

const (
	CapBuild     Capability = "build"
	CapTest      Capability = "test"
	CapLint      Capability = "lint"
	CapFormat    Capability = "format"
	CapContainer Capability = "container"
)

var allCapabilities = []Capability{CapBuild, CapTest, CapLint, CapFormat, CapContainer}

// Valid reports whether c is one of the defined capabilities.
func (c Capability) Valid() bool {
	for _, x := range allCapabilities {
		if c == x {
			return true
		}
	}
	return false
}

// String returns the string form of the capability.
func (c Capability) String() string { return string(c) }

// AllCapabilities returns a defensive copy of every defined capability.
func AllCapabilities() []Capability {
	return append([]Capability(nil), allCapabilities...)
}
