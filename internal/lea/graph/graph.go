package graph

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Stats summarizes the graph contents.
type Stats struct {
	NodeCount     int `json:"node_count"`
	EdgeCount     int `json:"edge_count"`
	FileCount     int `json:"file_count"`
	PackageCount  int `json:"package_count"`
	FunctionCount int `json:"function_count"`
	MethodCount   int `json:"method_count"`
	TypeCount     int `json:"type_count"`
	RouteCount    int `json:"route_count"`
	CallEdgeCount int `json:"call_edge_count"`
}

// Graph is a thread-safe in-memory structural graph. It is indexed by node ID
// plus denormalized name/kind lookups for fast resolution during builds and
// queries.
type Graph struct {
	mu    sync.RWMutex
	root  string
	nodes map[string]*Node

	byName map[string][]string
	byQual map[string][]string
	byKind map[NodeKind][]string

	fileNodes     map[string]string // rel path -> file node ID
	packageNodes  map[string]string // package dir -> package node ID
	pkgFiles      map[string][]string
	nodesByFile   map[string][]string
	fileExtracts  map[string]FileExtract
	typeByPkg     map[string][]string // package dir -> type node IDs (struct/interface)
	methodByRecv  map[string][]string // receiver name -> method node IDs
	interfaceById map[string]*Node    // interface node ID -> node (for IMPLEMENTS)

	out map[string][]Edge
	in  map[string][]Edge

	builtAt time.Time
}

// NewGraph creates an empty graph rooted at root.
func NewGraph(root string) *Graph {
	return &Graph{
		root:          root,
		nodes:         make(map[string]*Node),
		byName:        make(map[string][]string),
		byQual:        make(map[string][]string),
		byKind:        make(map[NodeKind][]string),
		fileNodes:     make(map[string]string),
		packageNodes:  make(map[string]string),
		pkgFiles:      make(map[string][]string),
		nodesByFile:   make(map[string][]string),
		fileExtracts:  make(map[string]FileExtract),
		typeByPkg:     make(map[string][]string),
		methodByRecv:  make(map[string][]string),
		interfaceById: make(map[string]*Node),
		out:           make(map[string][]Edge),
		in:            make(map[string][]Edge),
		builtAt:       time.Now(),
	}
}

// Root returns the repository root the graph was built from.
func (g *Graph) Root() string {
	return g.root
}

// BuiltAt returns when the graph was last built or refreshed.
func (g *Graph) BuiltAt() time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.builtAt
}

// SymbolsOfFile returns every non-file node declared by a relative path.
func (g *Graph) SymbolsOfFile(path string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Node
	for _, id := range g.nodesByFile[path] {
		n, ok := g.nodes[id]
		if ok && n.Kind != KindFile {
			out = append(out, *n)
		}
	}
	return out
}

func (g *Graph) addNode(n *Node) {
	g.nodes[n.ID] = n
	g.byName[n.Name] = append(g.byName[n.Name], n.ID)
	if n.QualName != "" {
		g.byQual[n.QualName] = append(g.byQual[n.QualName], n.ID)
	}
	g.byKind[n.Kind] = append(g.byKind[n.Kind], n.ID)
	if n.Kind == KindFile {
		g.fileNodes[n.File] = n.ID
	}
	if n.Kind == KindPackage {
		g.packageNodes[n.Package] = n.ID
	}
	if n.Kind == KindStruct || n.Kind == KindInterface || n.Kind == KindClass || n.Kind == KindType {
		if n.Package != "" {
			g.typeByPkg[n.Package] = append(g.typeByPkg[n.Package], n.ID)
		}
	}
	if n.Kind == KindInterface {
		g.interfaceById[n.ID] = n
	}
	if n.Kind == KindMethod {
		recv := receiverName(n.QualName)
		if recv != "" {
			g.methodByRecv[recv] = append(g.methodByRecv[recv], n.ID)
		}
	}
}

func (g *Graph) addEdge(e Edge) {
	g.out[e.From] = append(g.out[e.From], e)
	g.in[e.To] = append(g.in[e.To], e)
}

// removeNode removes a node and every edge touching it.
func (g *Graph) removeNode(id string) {
	n, ok := g.nodes[id]
	if !ok {
		return
	}
	for _, e := range g.out[id] {
		g.in[e.To] = dropEdge(g.in[e.To], id)
	}
	for _, e := range g.in[id] {
		g.out[e.From] = dropEdge(g.out[e.From], id)
	}
	delete(g.nodes, id)
	delete(g.out, id)
	delete(g.in, id)
	g.byName[n.Name] = dropID(g.byName[n.Name], id)
	if n.QualName != "" {
		g.byQual[n.QualName] = dropID(g.byQual[n.QualName], id)
	}
	g.byKind[n.Kind] = dropID(g.byKind[n.Kind], id)
	switch n.Kind {
	case KindFile:
		delete(g.fileNodes, n.File)
	case KindPackage:
		delete(g.packageNodes, n.Package)
		delete(g.pkgFiles, id)
	case KindStruct, KindInterface, KindClass, KindType:
		g.typeByPkg[n.Package] = dropID(g.typeByPkg[n.Package], id)
		if n.Kind == KindInterface {
			delete(g.interfaceById, id)
		}
	case KindMethod:
		recv := receiverName(n.QualName)
		if recv != "" {
			g.methodByRecv[recv] = dropID(g.methodByRecv[recv], id)
		}
	}
}

func dropEdge(es []Edge, from string) []Edge {
	out := es[:0]
	for _, e := range es {
		if e.From != from {
			out = append(out, e)
		}
	}
	return out
}

func dropID(ids []string, id string) []string {
	out := ids[:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// Node returns a node by ID.
func (g *Graph) Node(id string) (Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// File returns the file node for a relative path.
func (g *Graph) File(relPath string) (Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	id, ok := g.fileNodes[relPath]
	if !ok {
		return Node{}, false
	}
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Files returns all indexed file paths, sorted.
func (g *Graph) Files() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.fileNodes))
	for path := range g.fileNodes {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// Lookup returns all nodes with a given simple name.
func (g *Graph) Lookup(name string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.byName[name])
}

// LookupQual returns all nodes with a given qualified name.
func (g *Graph) LookupQual(qual string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.byQual[qual])
}

// NodesByKind returns all nodes of a kind.
func (g *Graph) NodesByKind(kind NodeKind) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.byKind[kind])
}

// Packages returns all package nodes.
func (g *Graph) Packages() []Node {
	return g.NodesByKind(KindPackage)
}

// Outgoing returns edges leaving a node.
func (g *Graph) Outgoing(id string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]Edge(nil), g.out[id]...)
}

// Incoming returns edges entering a node.
func (g *Graph) Incoming(id string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]Edge(nil), g.in[id]...)
}

// Callees returns the outbound function/method nodes called by a node.
func (g *Graph) Callees(id string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edgeTargets(id, EdgeCalls, false)
}

// Callers returns the inbound function/method nodes that call a node.
func (g *Graph) Callers(id string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edgeTargets(id, EdgeCalls, true)
}

func (g *Graph) edgeTargets(id string, kind EdgeKind, incoming bool) []Node {
	var edges []Edge
	if incoming {
		edges = g.in[id]
	} else {
		edges = g.out[id]
	}
	var ids []string
	for _, e := range edges {
		if e.Kind != kind {
			continue
		}
		if incoming {
			ids = append(ids, e.From)
		} else {
			ids = append(ids, e.To)
		}
	}
	return g.idsToNodes(ids)
}

// IncomingCalls returns the raw inbound CALLS edges of a node.
func (g *Graph) IncomingCalls(id string) []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Edge
	for _, e := range g.in[id] {
		if e.Kind == EdgeCalls {
			out = append(out, e)
		}
	}
	return out
}

// TypeNodesInPackage returns struct/interface node IDs declared in a package.
func (g *Graph) TypeNodesInPackage(pkg string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.typeByPkg[pkg])
}

// MethodsOf returns method nodes whose receiver matches a type name.
func (g *Graph) MethodsOf(recv string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.methodByRecv[recv])
}

// Interfaces returns all interface nodes.
func (g *Graph) Interfaces() []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.idsToNodes(g.byKind[KindInterface])
}

// FilesInPackage returns the file nodes declared in a package directory.
func (g *Graph) FilesInPackage(dir string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	pkgID := g.packageNodes[dir]
	if pkgID == "" {
		return nil
	}
	return g.idsToNodes(g.pkgFiles[pkgID])
}

// PackageDeps returns the distinct package directories a package depends on
// via IMPORTS edges.
func (g *Graph) PackageDeps(dir string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	pkgID := g.packageNodes[dir]
	if pkgID == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, fileID := range g.pkgFiles[pkgID] {
		for _, e := range g.out[fileID] {
			if e.Kind != EdgeImports {
				continue
			}
			if t, ok := g.nodes[e.To]; ok && t.Kind == KindPackage {
				if !seen[t.Package] {
					seen[t.Package] = true
					out = append(out, t.Package)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// ImportEdges returns every IMPORTS edge in the graph.
func (g *Graph) ImportEdges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Edge
	for _, es := range g.out {
		for _, e := range es {
			if e.Kind == EdgeImports {
				out = append(out, e)
			}
		}
	}
	return out
}

// SymbolNodes returns all function, method and type nodes.
func (g *Graph) SymbolNodes() []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var ids []string
	for _, k := range []NodeKind{KindFunction, KindMethod, KindStruct, KindClass, KindInterface, KindType, KindEnum} {
		ids = append(ids, g.byKind[k]...)
	}
	return g.idsToNodes(ids)
}

func (g *Graph) idsToNodes(ids []string) []Node {
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := g.nodes[id]; ok {
			out = append(out, *n)
		}
	}
	return out
}

// Stats returns graph statistics.
func (g *Graph) Stats() Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := Stats{NodeCount: len(g.nodes)}
	for _, es := range g.out {
		s.EdgeCount += len(es)
	}
	s.FileCount = len(g.fileNodes)
	s.PackageCount = len(g.packageNodes)
	for _, id := range g.byKind[KindFunction] {
		if _, ok := g.nodes[id]; ok {
			s.FunctionCount++
		}
	}
	for _, id := range g.byKind[KindMethod] {
		if _, ok := g.nodes[id]; ok {
			s.MethodCount++
		}
	}
	s.TypeCount = len(g.byKind[KindStruct]) + len(g.byKind[KindInterface]) +
		len(g.byKind[KindType]) + len(g.byKind[KindEnum]) + len(g.byKind[KindClass])
	s.RouteCount = len(g.byKind[KindHTTPRoute])
	for _, es := range g.out {
		for _, e := range es {
			if e.Kind == EdgeCalls {
				s.CallEdgeCount++
			}
		}
	}
	return s
}

// Snapshot captures the graph for persistence. The in-memory indexes are
// rebuilt on load.
type Snapshot struct {
	Root     string
	BuiltAt  time.Time
	Nodes    []Node
	Edges    []Edge
	FileList []string
	Extracts []FileExtract
}

// Snapshot returns a persistable copy of the graph.
func (g *Graph) Snapshot() Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := Snapshot{
		Root:    g.root,
		BuiltAt: g.builtAt,
		Nodes:   make([]Node, 0, len(g.nodes)),
		Edges:   make([]Edge, 0, len(g.out)),
	}
	for _, n := range g.nodes {
		s.Nodes = append(s.Nodes, *n)
	}
	for _, es := range g.out {
		s.Edges = append(s.Edges, es...)
	}
	s.FileList = make([]string, 0, len(g.fileNodes))
	for p := range g.fileNodes {
		s.FileList = append(s.FileList, p)
	}
	for _, fe := range g.fileExtracts {
		s.Extracts = append(s.Extracts, fe)
	}
	sort.Strings(s.FileList)
	sort.Slice(s.Extracts, func(i, j int) bool { return s.Extracts[i].File < s.Extracts[j].File })
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	sort.Slice(s.Edges, func(i, j int) bool {
		if s.Edges[i].From != s.Edges[j].From {
			return s.Edges[i].From < s.Edges[j].From
		}
		return s.Edges[i].To < s.Edges[j].To
	})
	return s
}

// Restore rebuilds the graph from a snapshot.
func (g *Graph) Restore(s Snapshot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.root = s.Root
	g.builtAt = s.BuiltAt
	g.nodes = make(map[string]*Node, len(s.Nodes))
	g.byName = make(map[string][]string, len(s.Nodes))
	g.byQual = make(map[string][]string, len(s.Nodes)/4)
	g.byKind = make(map[NodeKind][]string, 8)
	g.fileNodes = make(map[string]string, len(s.FileList))
	g.packageNodes = make(map[string]string, len(s.Nodes)/32)
	g.pkgFiles = make(map[string][]string, len(s.Nodes)/32)
	g.nodesByFile = make(map[string][]string, len(s.FileList))
	g.fileExtracts = make(map[string]FileExtract, len(s.Extracts))
	g.typeByPkg = make(map[string][]string, len(s.Nodes)/32)
	g.methodByRecv = make(map[string][]string, len(s.Nodes)/16)
	g.interfaceById = make(map[string]*Node, len(s.Nodes)/32)
	g.out = make(map[string][]Edge, len(s.Edges)/4)
	g.in = make(map[string][]Edge, len(s.Edges)/4)
	for i := range s.Nodes {
		g.addNode(&s.Nodes[i])
	}
	for path, id := range g.fileNodes {
		if pkgID, ok := g.packageNodes[dirOf(path)]; ok {
			g.pkgFiles[pkgID] = append(g.pkgFiles[pkgID], id)
		}
	}
	for _, e := range s.Edges {
		g.addEdge(e)
	}
	for _, fe := range s.Extracts {
		g.fileExtracts[fe.File] = fe
	}
}

// receiverName extracts the receiver from a method qualified name.
func receiverName(qual string) string {
	if i := strings.LastIndex(qual, "."); i >= 0 {
		return qual[:i]
	}
	return ""
}

// dirOf returns the package directory key for a relative file path. Root-level
// files map to "root".
func dirOf(relPath string) string {
	dir := filepath.Dir(filepath.ToSlash(relPath))
	if dir == "." || dir == "" {
		return "root"
	}
	return dir
}
