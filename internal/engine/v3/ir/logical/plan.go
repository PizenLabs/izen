package logical

import (
	"fmt"
	"strings"
)

// RelationKind classifies a relationship between two Logical IR nodes.
type RelationKind string

// Relationship kinds between IR nodes. Relationships are pure structure:
// they describe how nodes compose, depend and define each other without any
// physical detail.
const (
	// RelationDependsOn declares that From cannot be realized before To.
	RelationDependsOn RelationKind = "depends_on"
	// RelationComposes declares that From is composed of To (e.g. a page
	// composed of sections).
	RelationComposes RelationKind = "composes"
	// RelationDefines declares that From defines To (e.g. an endpoint that
	// a page consumes).
	RelationDefines RelationKind = "defines"
)

// Valid reports whether k is a known relation kind.
func (k RelationKind) Valid() bool {
	switch k {
	case RelationDependsOn, RelationComposes, RelationDefines:
		return true
	default:
		return false
	}
}

// Relation is one directed relationship between two node ids.
type Relation struct {
	// From is the source node id.
	From string
	// To is the target node id.
	To string
	// Kind classifies the relationship.
	Kind RelationKind
}

// NewRelation constructs a validated relation.
func NewRelation(from, to string, kind RelationKind) (Relation, error) {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return Relation{}, fmt.Errorf("logical: relation requires both endpoints, got %q -> %q", from, to)
	}
	if !kind.Valid() {
		return Relation{}, fmt.Errorf("logical: unknown relation kind %q", kind)
	}
	return Relation{From: from, To: to, Kind: kind}, nil
}

// LogicalPlan is the immutable, framework-agnostic plan produced by the
// Planner. It consists purely of Logical IR nodes and the relationships
// between them; it contains no physical commands, file paths or framework
// conventions. Adapters translate the exact same plan into different file
// layouts.
type LogicalPlan struct {
	id    string
	nodes []IRNode
	rels  []Relation
}

// NewLogicalPlan constructs an immutable LogicalPlan. Nodes are validated for
// non-nil kinds and unique ids; relations are validated against the node ids.
// The input slices are copied so callers cannot mutate the plan afterwards.
func NewLogicalPlan(nodes []IRNode, rels []Relation) (*LogicalPlan, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("logical: a plan requires at least one node")
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n == nil {
			return nil, fmt.Errorf("logical: plan contains a nil node")
		}
		if !n.Kind().Valid() {
			return nil, fmt.Errorf("logical: node %q has unknown kind %q", n.NodeID(), n.Kind())
		}
		id := n.NodeID()
		if seen[id] {
			return nil, fmt.Errorf("logical: duplicate node id %q", id)
		}
		seen[id] = true
	}

	clean := make([]Relation, 0, len(rels))
	for _, r := range rels {
		if !r.Kind.Valid() {
			return nil, fmt.Errorf("logical: relation %q -> %q has unknown kind %q", r.From, r.To, r.Kind)
		}
		if !seen[r.From] {
			return nil, fmt.Errorf("logical: relation from unknown node %q", r.From)
		}
		if !seen[r.To] {
			return nil, fmt.Errorf("logical: relation to unknown node %q", r.To)
		}
		clean = append(clean, r)
	}

	return &LogicalPlan{
		id:    "logical-plan",
		nodes: cloneNodes(nodes),
		rels:  clean,
	}, nil
}

// cloner is implemented by IR nodes that support deep cloning so plans can
// hold immutable private copies.
type cloner interface {
	clone() IRNode
}

// cloneNodes deep-copies every node so a plan never shares a mutable
// reference with its caller.
func cloneNodes(nodes []IRNode) []IRNode {
	out := make([]IRNode, 0, len(nodes))
	for _, n := range nodes {
		if c, ok := n.(cloner); ok {
			out = append(out, c.clone())
			continue
		}
		out = append(out, n)
	}
	return out
}

// ID returns the immutable plan identifier.
func (p *LogicalPlan) ID() string { return p.id }

// Nodes returns a deep copy of the plan's nodes in declaration order.
func (p *LogicalPlan) Nodes() []IRNode {
	return cloneNodes(p.nodes)
}

// Node returns a deep copy of the node with the given id, if any.
func (p *LogicalPlan) Node(id string) (IRNode, bool) {
	for _, n := range p.nodes {
		if n.NodeID() == id {
			if c, ok := n.(cloner); ok {
				return c.clone(), true
			}
			return n, true
		}
	}
	return nil, false
}

// Relations returns a defensive copy of the plan's relationships.
func (p *LogicalPlan) Relations() []Relation {
	return append([]Relation(nil), p.rels...)
}

// Len returns the number of nodes in the plan.
func (p *LogicalPlan) Len() int { return len(p.nodes) }

// KindCount returns the number of nodes of the given kind.
func (p *LogicalPlan) KindCount(kind NodeKind) int {
	n := 0
	for _, node := range p.nodes {
		if node.Kind() == kind {
			n++
		}
	}
	return n
}

// OutgoingRelations returns the relations whose source is the given node id.
func (p *LogicalPlan) OutgoingRelations(id string) []Relation {
	var out []Relation
	for _, r := range p.rels {
		if r.From == id {
			out = append(out, r)
		}
	}
	return out
}
