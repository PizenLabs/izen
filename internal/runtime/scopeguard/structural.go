package scopeguard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// StructuralCheckResult is the outcome of the structural consistency check.
type StructuralCheckResult string

const (
	// StructuralPass: target reachable via static AST import/caller/callee graph.
	StructuralPass StructuralCheckResult = "PASS"
	// StructuralAmbiguity: same module/package boundary but no explicit
	// static edge (dynamic dispatch, reflection, event handlers).
	// DO NOT hard-reject: route to Verification before granting authority.
	StructuralAmbiguity StructuralCheckResult = "STRUCTURAL_AMBIGUITY"
	// StructuralReject: unrelated module with zero dependency-graph path.
	StructuralReject StructuralCheckResult = "REJECT"
)

// GraphProvider is the structural dependency graph seam. Implementations
// answer static reachability (AST imports, caller/callee edges) and
// module/package membership without invoking the LLM.
type GraphProvider interface {
	// HasStaticPath reports whether any static import/caller/callee path
	// exists between two normalized target paths.
	HasStaticPath(from, to string) bool
	// ModuleOf returns the module/package boundary of a normalized path
	// (e.g. directory, Go package, TS module). Empty means unknown.
	ModuleOf(path string) string
}

// StructuralResult carries the check outcome plus routing guidance.
type StructuralResult struct {
	Result StructuralCheckResult `json:"result"`
	// Reason is a bounded human-readable justification.
	Reason string `json:"reason"`
	// NeedsVerification is true only for STRUCTURAL_AMBIGUITY: the
	// proposal must pass Verification (linter/test runner) before the
	// IntentGateway may grant execution authority.
	NeedsVerification bool `json:"needsVerification"`
}

// StructuralGuard evaluates proposed targets that passed the ScopeGuard
// tier but are NOT explicitly listed in the primary target list.
type StructuralGuard struct {
	graph GraphProvider
}

// NewStructuralGuard binds a guard to a dependency-graph provider.
// A nil graph is fail-closed: unlisted targets are REJECTED.
func NewStructuralGuard(graph GraphProvider) *StructuralGuard {
	return &StructuralGuard{graph: graph}
}

// moduleOf falls back to the parent directory when the graph cannot
// classify a path, so same-directory files resolve to one boundary.
func (g *StructuralGuard) moduleOf(path string) string {
	if g.graph != nil {
		if m := strings.TrimSpace(g.graph.ModuleOf(normalizeTarget(path))); m != "" {
			return m
		}
	}
	t := normalizeTarget(path)
	if dir := filepath.Dir(t); dir != "" && dir != "." {
		return dir
	}
	return t
}

// Check evaluates every unlisted in-scope target against the primary
// target list. Listed targets always PASS. The worst case across
// targets wins: REJECT > AMBIGUITY > PASS.
func (g *StructuralGuard) Check(targets, primaryScope []string, ledger ScopeLedger, taskID, proposalID string) StructuralResult {
	listed := make(map[string]struct{}, len(primaryScope))
	for _, s := range primaryScope {
		listed[normalizeTarget(s)] = struct{}{}
	}
	// Also treat scope-directory membership as listed context: a target
	// inside a listed directory is in-scope context, evaluated below.
	overall := StructuralResult{Result: StructuralPass, Reason: "all targets explicitly listed"}
	upgraded := false
	for _, t := range targets {
		nt := normalizeTarget(t)
		if _, ok := listed[nt]; ok {
			continue
		}
		r := g.checkOne(nt, primaryScope)
		switch r.Result {
		case StructuralReject:
			if ledger != nil {
				_ = ledger.RecordCustomEvent(taskID, "STRUCTURAL_REJECT", map[string]any{
					"proposalId": proposalID,
					"target":     nt,
					"reason":     r.Reason,
				})
			}
			return r
		case StructuralAmbiguity:
			if !upgraded {
				overall = r
				upgraded = true
			}
			if ledger != nil {
				_ = ledger.RecordCustomEvent(taskID, "STRUCTURAL_AMBIGUITY", map[string]any{
					"proposalId": proposalID,
					"target":     nt,
					"reason":     r.Reason,
				})
			}
		}
	}
	return overall
}

// checkOne evaluates a single unlisted target.
func (g *StructuralGuard) checkOne(target string, primaryScope []string) StructuralResult {
	if g.graph == nil {
		return StructuralResult{
			Result: StructuralReject,
			Reason: fmt.Sprintf("no dependency graph: unlisted target %q rejected", target),
		}
	}
	// Static reachability from any primary target -> PASS.
	for _, s := range primaryScope {
		ns := normalizeTarget(s)
		if g.graph.HasStaticPath(ns, target) || g.graph.HasStaticPath(target, ns) {
			return StructuralResult{
				Result: StructuralPass,
				Reason: fmt.Sprintf("static graph path between %q and %q", ns, target),
			}
		}
	}
	// Same module boundary without a static edge -> AMBIGUITY (verify first).
	tm := g.moduleOf(target)
	for _, s := range primaryScope {
		if g.moduleOf(normalizeTarget(s)) == tm {
			return StructuralResult{
				Result:            StructuralAmbiguity,
				Reason:            fmt.Sprintf("same module boundary %q without static edge: route to verification", tm),
				NeedsVerification: true,
			}
		}
	}
	return StructuralResult{
		Result: StructuralReject,
		Reason: fmt.Sprintf("no dependency-graph path for %q: unrelated module", target),
	}
}

// StaticGraph is an in-memory GraphProvider for tests and minimal
// runtimes: explicit static edges plus module membership.
type StaticGraph struct {
	edges   map[string]map[string]struct{}
	modules map[string]string
}

// NewStaticGraph returns an empty graph.
func NewStaticGraph() *StaticGraph {
	return &StaticGraph{
		edges:   make(map[string]map[string]struct{}),
		modules: make(map[string]string),
	}
}

// AddEdge records a bidirectional static edge between two targets.
func (g *StaticGraph) AddEdge(a, b string) *StaticGraph {
	na, nb := normalizeTarget(a), normalizeTarget(b)
	if g.edges[na] == nil {
		g.edges[na] = make(map[string]struct{})
	}
	if g.edges[nb] == nil {
		g.edges[nb] = make(map[string]struct{})
	}
	g.edges[na][nb] = struct{}{}
	g.edges[nb][na] = struct{}{}
	return g
}

// SetModule pins a target to a module boundary label.
func (g *StaticGraph) SetModule(path, module string) *StaticGraph {
	g.modules[normalizeTarget(path)] = strings.TrimSpace(module)
	return g
}

// HasStaticPath implements GraphProvider via BFS over static edges.
func (g *StaticGraph) HasStaticPath(from, to string) bool {
	if g == nil {
		return false
	}
	from, to = normalizeTarget(from), normalizeTarget(to)
	if from == to {
		return true
	}
	visited := map[string]struct{}{from: {}}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for nb := range g.edges[cur] {
			if nb == to {
				return true
			}
			if _, seen := visited[nb]; !seen {
				visited[nb] = struct{}{}
				queue = append(queue, nb)
			}
		}
	}
	return false
}

// ModuleOf implements GraphProvider.
func (g *StaticGraph) ModuleOf(path string) string {
	if g == nil {
		return ""
	}
	return g.modules[normalizeTarget(path)]
}
