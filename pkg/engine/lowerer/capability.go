package lowerer

import (
	"fmt"
	"sort"

	"github.com/PizenLabs/izen/pkg/engine/adapter"
	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// Capability is a composable capability node of the lowerer. Each capability
// is resolved independently: the lowerer determines which capabilities a plan
// needs, then maps every IR node to the adapter registered for
// (Framework, Capability).
type Capability string

// Supported capabilities.
const (
	// CapabilityRouting renders API endpoint and route definitions.
	CapabilityRouting Capability = "routing"
	// CapabilityRendering renders pages, sections and components.
	CapabilityRendering Capability = "rendering"
	// CapabilityStyling renders styling artifacts.
	CapabilityStyling Capability = "styling"
	// CapabilityTesting renders test scaffolds.
	CapabilityTesting Capability = "testing"
	// CapabilityData renders database migrations.
	CapabilityData Capability = "data"
	// CapabilityScript renders behaviour/script artifacts.
	CapabilityScript Capability = "script"
)

// String returns the machine-readable capability label.
func (c Capability) String() string { return string(c) }

// nodeCapability maps each Logical IR node kind to the capability it
// exercises. This is the single source of truth for node → capability.
var nodeCapability = map[ir.NodeKind]Capability{
	ir.NodeDefineEndpoint:  CapabilityRouting,
	ir.NodeCreatePage:      CapabilityRendering,
	ir.NodeCreateSection:   CapabilityRendering,
	ir.NodeCreateComponent: CapabilityRendering,
	ir.NodeCreateMigration: CapabilityData,
	ir.NodeCreateStyle:     CapabilityStyling,
	ir.NodeCreateScript:    CapabilityScript,
}

// capabilityForNode returns the capability a node kind exercises.
func capabilityForNode(kind ir.NodeKind) (Capability, bool) {
	c, ok := nodeCapability[kind]
	return c, ok
}

// frameworkCapabilities is the capability dependency graph per framework:
// the capabilities a framework makes available and the lowerer may satisfy.
var frameworkCapabilities = map[adapter.Framework][]Capability{
	adapter.FrameworkNextJS:    {CapabilityRouting, CapabilityRendering, CapabilityStyling, CapabilityTesting, CapabilityData, CapabilityScript},
	adapter.FrameworkAstro:     {CapabilityRouting, CapabilityRendering, CapabilityStyling, CapabilityTesting, CapabilityData, CapabilityScript},
	adapter.FrameworkReactVite: {CapabilityRouting, CapabilityRendering, CapabilityStyling, CapabilityTesting, CapabilityData, CapabilityScript},
	adapter.FrameworkGoGin:     {CapabilityRouting, CapabilityRendering, CapabilityStyling, CapabilityTesting, CapabilityData, CapabilityScript},
	adapter.FrameworkStaticWeb: {CapabilityRendering, CapabilityStyling, CapabilityData, CapabilityScript},
}

// capabilityOrder is the canonical declaration order used for deterministic
// iteration.
var capabilityOrder = []Capability{
	CapabilityRouting, CapabilityRendering, CapabilityStyling, CapabilityTesting, CapabilityData, CapabilityScript,
}

// MappedNode binds one Logical IR node to the capability and concrete
// adapter that will render it.
type MappedNode struct {
	// Node is the Logical IR node.
	Node ir.IRNode
	// Capability is the capability the node exercises.
	Capability Capability
	// Adapter is the concrete (Framework, Capability) adapter resolved from
	// the registry.
	Adapter adapter.FrameworkAdapter
	// Artifacts holds the rendered files once Lower has run.
	Artifacts []adapter.FileArtifact
}

// CapabilityRegistry maps (Framework, Capability) pairs to concrete adapter
// implementations. Every registration is auditable: resolving a framework
// shows exactly which capability is served by which adapter.
type CapabilityRegistry struct {
	adapters map[adapter.Framework]map[Capability]adapter.FrameworkAdapter
}

// NewCapabilityRegistry returns an empty registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{adapters: map[adapter.Framework]map[Capability]adapter.FrameworkAdapter{}}
}

// Register binds an adapter to a (Framework, Capability) pair. A nil adapter
// is rejected; re-registering a pair replaces the previous adapter.
func (r *CapabilityRegistry) Register(fw adapter.Framework, cap Capability, a adapter.FrameworkAdapter) error {
	if a == nil {
		return fmt.Errorf("lowerer: cannot register a nil adapter for %s/%s", fw, cap)
	}
	if r.adapters[fw] == nil {
		r.adapters[fw] = map[Capability]adapter.FrameworkAdapter{}
	}
	r.adapters[fw][cap] = a
	return nil
}

// Resolve returns the concrete adapter serving (Framework, Capability).
func (r *CapabilityRegistry) Resolve(fw adapter.Framework, cap Capability) (adapter.FrameworkAdapter, error) {
	fwCaps, ok := r.adapters[fw]
	if !ok {
		return nil, fmt.Errorf("lowerer: no adapters registered for framework %q", fw)
	}
	a, ok := fwCaps[cap]
	if !ok {
		return nil, fmt.Errorf("lowerer: framework %q has no adapter for capability %q", fw, cap)
	}
	return a, nil
}

// CapabilitiesFor returns the capabilities a framework serves, in canonical
// order.
func (r *CapabilityRegistry) CapabilitiesFor(fw adapter.Framework) []Capability {
	fwCaps := r.adapters[fw]
	var out []Capability
	for _, c := range capabilityOrder {
		if _, ok := fwCaps[c]; ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// DefaultRegistry returns a registry pre-wired with every supported
// framework adapter serving every capability it declares.
func DefaultRegistry() *CapabilityRegistry {
	r := NewCapabilityRegistry()
	for fw, a := range map[adapter.Framework]adapter.FrameworkAdapter{
		adapter.FrameworkNextJS:    adapter.NewNextJSAdapter(),
		adapter.FrameworkAstro:     adapter.NewAstroAdapter(),
		adapter.FrameworkReactVite: adapter.NewReactViteAdapter(),
		adapter.FrameworkGoGin:     adapter.NewGoGinAdapter(),
		adapter.FrameworkStaticWeb: adapter.NewStaticWebAdapter(),
	} {
		for _, cap := range frameworkCapabilities[fw] {
			_ = r.Register(fw, cap, a)
		}
	}
	return r
}

// ResolveFramework maps an inference hypothesis label onto the concrete
// adapter framework that renders it. It returns false for labels that are not
// a known framework (e.g. a language or styling hypothesis).
func ResolveFramework(label string) (adapter.Framework, bool) {
	switch label {
	case "Next.js":
		return adapter.FrameworkNextJS, true
	case "Astro":
		return adapter.FrameworkAstro, true
	case "React + Vite":
		return adapter.FrameworkReactVite, true
	case "Go + Gin":
		return adapter.FrameworkGoGin, true
	case "Static HTML/CSS/JS":
		return adapter.FrameworkStaticWeb, true
	default:
		return "", false
	}
}
