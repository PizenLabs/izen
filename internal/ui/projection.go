package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// ── Dynamic IR Projection ────────────────────────────────────────────────────
//
// ProjectSnapshotToView is the pure, fact-only projection of the Dynamic IR
// (an ExecutionSnapshot bound to its static ExecutionGraph) into a terminal
// tree string. It is read-only: the projection never mutates the snapshot or
// the graph and never makes a decision — it only renders the raw node states
// and attempt counts. State glyphs come strictly from tokens.go
// (SpinnerSnowflake / IconCheck / IconError / IconPending); the only other
// marks are structural tree connectors.

// ProjectSnapshotToView renders the execution tree of a Dynamic IR snapshot.
// The graph supplies node metadata (kind, description, dependency edges) and
// order; when it is nil the snapshot's own Plan graph is used, and when no
// graph is available at all the projection falls back to sorted node ids so
// fact-only reconstructions (no plan in the payload) still render deterministically.
func ProjectSnapshotToView(snap *ir.ExecutionSnapshot, graph *ir.ExecutionGraph) string {
	if snap == nil || len(snap.NodeStates) == 0 {
		return ""
	}

	nodes := projectNodeOrder(snap, graph)
	nodeByID := make(map[string]*ir.ExecutionNode, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	children := projectChildIndex(nodes)

	var b strings.Builder
	b.WriteString(projectHeader(snap))
	b.WriteString("\n")

	rendered := make(map[string]bool, len(nodes))
	for _, line := range projectLines(nodes, children, nodeByID, rendered, snap) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// projectNodeOrder returns the node order for the projection: graph nodes in
// graph insertion order (restricted to nodes present in the snapshot), then any
// snapshot-only node ids sorted alphabetically so fact-only reconstructions
// render deterministically.
func projectNodeOrder(snap *ir.ExecutionSnapshot, graph *ir.ExecutionGraph) []*ir.ExecutionNode {
	var g *ir.ExecutionGraph
	if graph != nil {
		g = graph
	} else if snap.Plan != nil {
		g = snap.Plan.Graph
	}

	out := make([]*ir.ExecutionNode, 0, len(snap.NodeStates))
	seen := make(map[string]bool, len(snap.NodeStates))
	if g != nil {
		for _, n := range g.Nodes() {
			if _, ok := snap.NodeStates[n.ID]; !ok {
				continue
			}
			seen[n.ID] = true
			out = append(out, n)
		}
	}

	var extra []string
	for id := range snap.NodeStates {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		out = append(out, &ir.ExecutionNode{ID: id})
	}
	return out
}

// projectChildIndex builds the parent → children adjacency list from the
// dependency edges, de-duplicating so a node with multiple parents renders once.
func projectChildIndex(nodes []*ir.ExecutionNode) map[string][]string {
	children := make(map[string][]string, len(nodes))
	seen := make(map[string]map[string]bool, len(nodes))
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if seen[n.ID] == nil {
				seen[n.ID] = make(map[string]bool)
			}
			if seen[n.ID][dep] {
				continue
			}
			seen[n.ID][dep] = true
			children[dep] = append(children[dep], n.ID)
		}
	}
	return children
}

// projectHeader renders the one-line snapshot summary: the live snowflake
// glyph, the run id and the per-state counts. Counts are derived facts, never
// decisions.
func projectHeader(snap *ir.ExecutionSnapshot) string {
	counts := make(map[ir.NodeState]int, 5)
	for _, s := range snap.NodeStates {
		counts[s]++
	}

	var b strings.Builder
	b.WriteString(SpinnerSnowflake())
	if snap.ID != "" {
		b.WriteString(" ")
		b.WriteString(snap.ID)
	}
	fmt.Fprintf(&b, " (%d nodes: %d success, %d running, %d failed, %d pending)",
		len(snap.NodeStates),
		counts[ir.StateSuccess],
		counts[ir.StateRunning],
		counts[ir.StateFailed],
		counts[ir.StatePending]+counts[ir.StateReady],
	)
	return accentStyle.Render(b.String())
}

// projectLines renders the dependency tree with box-drawing connectors. Each
// node appears exactly once (under its first parent), prefixed with its
// lifecycle glyph and suffixed with its metadata.
func projectLines(nodes []*ir.ExecutionNode, children map[string][]string, nodeByID map[string]*ir.ExecutionNode, rendered map[string]bool, snap *ir.ExecutionSnapshot) []string {
	hasParent := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if len(n.DependsOn) > 0 {
			hasParent[n.ID] = true
		}
	}

	var roots []string
	for _, n := range nodes {
		if !hasParent[n.ID] {
			roots = append(roots, n.ID)
		}
	}

	var lines []string
	var walk func(id string, depth int, ancestorHasSibling []bool, last bool)
	walk = func(id string, depth int, ancestorHasSibling []bool, last bool) {
		if rendered[id] {
			return
		}
		rendered[id] = true

		var prefix strings.Builder
		for i := 0; i < depth; i++ {
			if ancestorHasSibling[i] {
				prefix.WriteString("│  ")
			} else {
				prefix.WriteString("   ")
			}
		}
		if depth > 0 {
			if last {
				prefix.WriteString("└─ ")
			} else {
				prefix.WriteString("├─ ")
			}
		}

		lines = append(lines, prefix.String()+projectNodeLine(snap, nodeByID[id]))

		kids := children[id]
		for i, kid := range kids {
			if rendered[kid] {
				continue
			}
			hasSibling := i < len(kids)-1
			nextAncestors := append(append([]bool(nil), ancestorHasSibling...), hasSibling)
			walk(kid, depth+1, nextAncestors, i == len(kids)-1)
		}
	}

	for i, r := range roots {
		walk(r, 0, nil, i == len(roots)-1)
	}
	return lines
}

// projectNodeLine renders one node: lifecycle glyph, id, kind, description and
// attempt count, folding the last observation (error / skip) when present.
func projectNodeLine(snap *ir.ExecutionSnapshot, n *ir.ExecutionNode) string {
	if n == nil {
		return ""
	}
	id := n.ID

	var b strings.Builder
	b.WriteString(projectGlyph(snap.NodeStates[id]))
	b.WriteString(" ")
	b.WriteString(id)
	if n.Kind != "" {
		b.WriteString(" ")
		b.WriteString(dimmedStyle.Render("(" + n.Kind.String() + ")"))
	}
	if n.Description != "" {
		b.WriteString("  ")
		b.WriteString(n.Description)
	}
	if a := snap.AttemptCounts[id]; a > 0 {
		b.WriteString(" ")
		b.WriteString(dimmedStyle.Render(fmt.Sprintf("(x%d)", a)))
	}
	if obs, ok := snap.Observation(id); ok {
		switch {
		case obs.Err != "":
			b.WriteString(" ")
			b.WriteString(redStyle.Render(obs.Err))
		case obs.SkipReason != "":
			b.WriteString(" ")
			b.WriteString(mutedStyle.Render("(skipped: " + obs.SkipReason + ")"))
		}
	}
	return b.String()
}

// projectGlyph maps a node lifecycle state to its tokens.go glyph, colored by
// the canonical status palette. Only the four token glyphs are ever emitted.
func projectGlyph(state ir.NodeState) string {
	switch state {
	case ir.StateSuccess:
		return greenStyle.Render(IconCheck())
	case ir.StateFailed:
		return redStyle.Render(IconError())
	case ir.StateRunning:
		return orangeStyle.Render(SpinnerSnowflake())
	default:
		return mutedStyle.Render(IconPending())
	}
}
