// Package lowerer implements the plan lowering stage of the IR-driven intent
// compiler. The PlanLowerer resolves the capabilities a resolved framework
// must provide, maps every Logical IR node to the concrete adapter registered
// for (Framework, Capability) in the CapabilityRegistry, and renders the
// plan into concrete FileArtifacts through the pure adapters.
//
// Lowering is a pure transformation: the LogicalPlan is never mutated and no
// filesystem is touched. The SAME LogicalPlan lowers into different file
// layouts depending only on the resolved framework.
package lowerer

import (
	"fmt"

	"github.com/PizenLabs/izen/pkg/engine/adapter"
	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// PlanLowerer resolves capabilities and maps Logical IR nodes to concrete
// adapters. It is a pure, stateless transformer over its registry.
type PlanLowerer struct {
	registry *CapabilityRegistry
}

// NewPlanLowerer returns a lowerer bound to the given registry. A nil
// registry falls back to DefaultRegistry.
func NewPlanLowerer(registry *CapabilityRegistry) *PlanLowerer {
	if registry == nil {
		registry = DefaultRegistry()
	}
	return &PlanLowerer{registry: registry}
}

// ResolveCapabilities returns the capabilities the plan requires for the
// resolved framework: the framework's declared capability set unioned with
// the capabilities demanded by the plan's node kinds. The result is returned
// in canonical capability order and drives which adapters get exercised.
func (l *PlanLowerer) ResolveCapabilities(fw adapter.Framework, plan *ir.LogicalPlan) []Capability {
	if plan == nil {
		return append([]Capability(nil), frameworkCapabilities[fw]...)
	}
	required := map[Capability]bool{}
	for _, c := range frameworkCapabilities[fw] {
		required[c] = true
	}
	for _, n := range plan.Nodes() {
		if c, ok := capabilityForNode(n.Kind()); ok {
			required[c] = true
		}
	}
	var out []Capability
	for _, c := range capabilityOrder {
		if required[c] {
			out = append(out, c)
		}
	}
	return out
}

// MapPlan binds each Logical IR node to the capability it exercises and the
// concrete adapter registered for (Framework, Capability). Every node must
// map to a known capability and a registered adapter.
func (l *PlanLowerer) MapPlan(plan *ir.LogicalPlan, fw adapter.Framework) ([]MappedNode, error) {
	if plan == nil {
		return nil, fmt.Errorf("lowerer: cannot map a nil plan")
	}
	out := make([]MappedNode, 0, plan.Len())
	for _, n := range plan.Nodes() {
		cap, ok := capabilityForNode(n.Kind())
		if !ok {
			return nil, fmt.Errorf("lowerer: node %q (%s) has no capability mapping", n.NodeID(), n.Kind())
		}
		a, err := l.registry.Resolve(fw, cap)
		if err != nil {
			return nil, fmt.Errorf("lowerer: node %q: %w", n.NodeID(), err)
		}
		out = append(out, MappedNode{Node: n, Capability: cap, Adapter: a})
	}
	return out, nil
}

// Lower renders every node of the plan through its mapped adapter into a
// flat list of concrete FileArtifacts. It is a pure transformation: the plan
// and nodes are never mutated.
func (l *PlanLowerer) Lower(plan *ir.LogicalPlan, fw adapter.Framework) ([]adapter.FileArtifact, error) {
	mapped, err := l.MapPlan(plan, fw)
	if err != nil {
		return nil, err
	}
	var out []adapter.FileArtifact
	for i := range mapped {
		arts, err := mapped[i].Adapter.RenderNode(mapped[i].Node)
		if err != nil {
			return nil, fmt.Errorf("lowerer: rendering %q (%s): %w", mapped[i].Node.NodeID(), mapped[i].Node.Kind(), err)
		}
		mapped[i].Artifacts = append(mapped[i].Artifacts, arts...)
		out = append(out, arts...)
	}
	return out, nil
}
