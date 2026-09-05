package lea

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/lea/graph"
)

// toSymbolNode converts a graph node to the public query type.
func toSymbolNode(n graph.Node) SymbolNode {
	return SymbolNode{
		Name:      n.Name,
		Kind:      string(n.Kind),
		QualName:  n.QualName,
		Package:   n.Package,
		File:      n.File,
		Line:      n.Line,
		Exported:  n.Exported,
		Signature: n.Signature,
	}
}

// GetArchitectureSummary returns the top-level structural overview: packages,
// entry points, HTTP routes and cross-layer dependency directions.
func (e *Engine) GetArchitectureSummary() ArchSummary {
	g := e.Graph()
	summary := ArchSummary{
		Root:    e.root,
		Stats:   g.Stats(),
		BuiltAt: g.BuiltAt(),
	}

	pkgs := g.Packages()
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].QualName < pkgs[j].QualName })
	for _, p := range pkgs {
		files := g.FilesInPackage(p.Package)
		symCount := 0
		for _, f := range files {
			symCount += len(g.SymbolsOfFile(f.File))
		}
		deps := g.PackageDeps(p.Package)
		summary.Packages = append(summary.Packages, PackageInfo{
			Name:        p.Name,
			Dir:         p.Package,
			FileCount:   len(files),
			SymbolCount: symCount,
			ImportCount: len(deps),
			DependsOn:   deps,
		})
	}

	summary.EntryPoints = e.entryPoints()
	summary.HTTPRoutes = e.FindRoutes()
	summary.LayerDirection = e.layerDirections()
	return summary
}

// entryPoints locates main/init functions and binaries under cmd/.
func (e *Engine) entryPoints() []SymbolNode {
	g := e.Graph()
	var out []SymbolNode
	for _, n := range g.SymbolNodes() {
		if n.Kind != graph.KindFunction {
			continue
		}
		if n.Name == "main" || n.Name == "init" || strings.HasPrefix(n.Package, "cmd") {
			out = append(out, toSymbolNode(n))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// layerFor maps a package directory to an architectural layer.
func layerFor(dir string) string {
	dir = filepath.ToSlash(dir)
	switch {
	case dir == "cmd" || strings.HasPrefix(dir, "cmd/"):
		return "entry"
	case dir == "api" || strings.HasPrefix(dir, "api/"):
		return "entry"
	case strings.HasPrefix(dir, "internal/ui"), strings.HasPrefix(dir, "internal/modes"),
		strings.HasPrefix(dir, "internal/agents"):
		return "interface"
	case dir == "internal/core" || strings.HasPrefix(dir, "internal/core/"):
		return "domain"
	case strings.Contains(dir, "domain") || strings.Contains(dir, "model") || strings.Contains(dir, "entity"):
		return "domain"
	default:
		return "internal"
	}
}

// layerDirections aggregates IMPORTS edges between architectural layers.
func (e *Engine) layerDirections() []LayerDirection {
	g := e.Graph()
	counts := make(map[string]int)
	for _, edge := range g.ImportEdges() {
		fromFile, ok := g.Node(edge.From)
		if !ok {
			continue
		}
		toPkg, ok := g.Node(edge.To)
		if !ok {
			continue
		}
		a, b := layerFor(fromFile.Package), layerFor(toPkg.Package)
		if a == b {
			continue
		}
		counts[a+"\x00"+b]++
	}
	out := make([]LayerDirection, 0, len(counts))
	for key, n := range counts {
		parts := strings.SplitN(key, "\x00", 2)
		out = append(out, LayerDirection{From: parts[0], To: parts[1], EdgeCount: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// TraceCallChain reconstructs the inbound (callers) or outbound (callees)
// call tree for a target symbol, up to the given depth.
func (e *Engine) TraceCallChain(symbol string, direction CallDirection, depth int) CallTree {
	g := e.Graph()
	target, ok := resolveQueryTarget(g, symbol)
	if !ok {
		return CallTree{}
	}
	root := CallTree{Node: toSymbolNode(target), Depth: 0}
	if depth <= 0 {
		return root
	}
	visited := map[string]bool{target.ID: true}
	e.buildCallTree(g, target.ID, direction, depth, 1, &root, visited)
	return root
}

func (e *Engine) buildCallTree(g *graph.Graph, id string, direction CallDirection, maxDepth, depth int, parent *CallTree, visited map[string]bool) {
	if depth > maxDepth {
		return
	}
	var targets []graph.Node
	if direction == Inbound {
		targets = g.Callers(id)
	} else {
		targets = g.Callees(id)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].File != targets[j].File {
			return targets[i].File < targets[j].File
		}
		return targets[i].Line < targets[j].Line
	})
	for _, t := range targets {
		if visited[t.ID] {
			child := CallTree{Node: toSymbolNode(t), Depth: depth}
			parent.Children = append(parent.Children, child)
			continue
		}
		visited[t.ID] = true
		child := CallTree{Node: toSymbolNode(t), Depth: depth}
		e.buildCallTree(g, t.ID, direction, maxDepth, depth+1, &child, visited)
		parent.Children = append(parent.Children, child)
	}
}

// resolveQueryTarget finds the best-matching function or method node for a
// symbol reference. Exact qualified matches win over simple names.
func resolveQueryTarget(g *graph.Graph, name string) (graph.Node, bool) {
	if q := g.LookupQual(name); len(q) > 0 {
		return q[0], true
	}
	byName := g.Lookup(name)
	if len(byName) == 0 {
		return graph.Node{}, false
	}
	// Prefer a function/method node, then the first deterministic result.
	for _, n := range byName {
		if n.Kind == graph.KindFunction || n.Kind == graph.KindMethod {
			return n, true
		}
	}
	return byName[0], true
}

// FindDeadCode returns unreferenced functions and methods: nodes with zero
// inbound CALLS edges, excluding entry points, exported symbols and interface
// methods.
func (e *Engine) FindDeadCode() []SymbolNode {
	g := e.Graph()
	interfaceRecvs := make(map[string]bool)
	for _, iface := range g.Interfaces() {
		interfaceRecvs[iface.Name] = true
	}
	funcs := g.NodesByKind(graph.KindFunction)
	funcs = append(funcs, g.NodesByKind(graph.KindMethod)...)
	sort.Slice(funcs, func(i, j int) bool {
		if funcs[i].File != funcs[j].File {
			return funcs[i].File < funcs[j].File
		}
		return funcs[i].Line < funcs[j].Line
	})

	var dead []SymbolNode
	for _, n := range funcs {
		if n.Exported {
			continue
		}
		if n.Name == "main" || n.Name == "init" {
			continue
		}
		if n.Kind == graph.KindMethod {
			recv := receiverOf(n.QualName)
			if interfaceRecvs[recv] {
				continue
			}
		}
		if len(g.IncomingCalls(n.ID)) > 0 {
			continue
		}
		dead = append(dead, toSymbolNode(n))
	}
	return dead
}

// FindRoutes maps HTTP paths/verbs to their handler functions.
func (e *Engine) FindRoutes() []RouteNode {
	g := e.Graph()
	routes := g.NodesByKind(graph.KindHTTPRoute)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Name != routes[j].Name {
			return routes[i].Name < routes[j].Name
		}
		return routes[i].File < routes[j].File
	})

	out := make([]RouteNode, 0, len(routes))
	for _, r := range routes {
		rn := RouteNode{
			File:   r.File,
			Line:   r.Line,
			Method: methodOf(r.Name),
			Path:   pathOf(r.Name),
		}
		for _, edge := range g.Outgoing(r.ID) {
			if edge.Kind != graph.EdgeHTTPHandles {
				continue
			}
			if h, ok := g.Node(edge.To); ok {
				rn.Handler = h.Name
				rn.HandlerFile = h.File
				rn.HandlerLine = h.Line
				break
			}
		}
		out = append(out, rn)
	}
	return out
}

func methodOf(name string) string {
	if i := strings.Index(name, " "); i >= 0 {
		return name[:i]
	}
	return ""
}

func pathOf(name string) string {
	if i := strings.Index(name, " "); i >= 0 {
		return name[i+1:]
	}
	return name
}

func receiverOf(qual string) string {
	if i := strings.LastIndex(qual, "."); i >= 0 {
		return qual[:i]
	}
	return ""
}
