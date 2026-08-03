package retrieval

import (
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lea"
	leagraph "github.com/PizenLabs/izen/internal/lea/graph"
)

type GraphLookup struct {
	graph *graph.Graph
	root  string
	// lea is the Phase 3 structural engine. When set, every structural
	// lookup is served from the Lea graph (symbol resolution, file/package
	// discovery, import edges, dependents) instead of the legacy native
	// graph. The native graph remains a fallback for headless/test harnesses.
	lea *lea.Engine
}

func NewGraphLookup(g *graph.Graph, root string) *GraphLookup {
	return &GraphLookup{graph: g, root: root}
}

// NewLeaGraphLookup wraps a Lea structural engine as the graph-tier source.
// It redirects structural lookups (symbols, files, packages, imports,
// dependents, glob) onto the Lea index, which is richer (call edges, routes)
// and stays fresh via incremental sync.
func NewLeaGraphLookup(e *lea.Engine, root string) *GraphLookup {
	return &GraphLookup{lea: e, root: root}
}

// leaGraph returns the current Lea structural graph, or nil when the engine
// is absent or not yet indexed.
func (gl *GraphLookup) leaGraph() *leagraph.Graph {
	if gl == nil || gl.lea == nil {
		return nil
	}
	g := gl.lea.Graph()
	if g == nil {
		return nil
	}
	return g
}

func (gl *GraphLookup) HasGraph() bool {
	if gl == nil {
		return false
	}
	if lg := gl.leaGraph(); lg != nil {
		return len(lg.Files()) > 0
	}
	return gl.graph != nil
}

func (gl *GraphLookup) SearchSymbol(name string) *ResultSet {
	if lg := gl.leaGraph(); lg != nil {
		return gl.searchSymbolLea(lg, name)
	}
	rs := &ResultSet{Strategy: "graph.exact"}

	symbols := gl.graph.LookupSymbol(name)
	for _, sym := range symbols {
		rs.Add(Score(ConfExact, Result{
			File:       sym.File,
			Line:       sym.Line,
			Column:     sym.Column,
			Strategy:   "graph.exact",
			SymbolName: sym.Name,
			SymbolKind: sym.Kind.String(),
			Content:    sym.Signature,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfExact.Float64()
	}

	return rs
}

// searchSymbolLea resolves a symbol against the Lea graph: exact qualified
// matches first, then simple-name matches.
func (gl *GraphLookup) searchSymbolLea(lg *leagraph.Graph, name string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.exact"}

	add := func(n leagraph.Node) {
		rs.Add(Score(ConfExact, Result{
			File:       n.File,
			Line:       n.Line,
			Strategy:   "graph.exact",
			SymbolName: n.Name,
			SymbolKind: string(n.Kind),
			Content:    n.Signature,
		}))
	}
	for _, n := range lg.LookupQual(name) {
		add(n)
	}
	for _, n := range lg.Lookup(name) {
		add(n)
	}

	if !rs.Empty() {
		rs.Confidence = ConfExact.Float64()
	}
	return rs
}

func (gl *GraphLookup) SearchFile(path string) *ResultSet {
	if lg := gl.leaGraph(); lg != nil {
		rs := &ResultSet{Strategy: "graph.file"}
		if n, ok := lg.File(path); ok {
			rs.Add(Score(ConfExact, Result{
				File:       n.File,
				Strategy:   "graph.file",
				SymbolName: n.Package,
				Content:    n.File,
			}))
			rs.Confidence = ConfExact.Float64()
		}
		return rs
	}
	rs := &ResultSet{Strategy: "graph.file"}

	fn := gl.graph.LookupFile(path)
	if fn != nil {
		rs.Add(Score(ConfExact, Result{
			File:       fn.Path,
			Strategy:   "graph.file",
			SymbolName: fn.Package,
			Content:    fn.Path,
		}))
		rs.Confidence = ConfExact.Float64()
	}

	return rs
}

func (gl *GraphLookup) SearchPackage(pkg string) *ResultSet {
	if lg := gl.leaGraph(); lg != nil {
		rs := &ResultSet{Strategy: "graph.fuzzy"}
		seen := make(map[string]bool)
		add := func(f leagraph.Node) {
			if seen[f.File] {
				return
			}
			seen[f.File] = true
			rs.Add(Score(ConfFuzzy, Result{
				File:       f.File,
				Strategy:   "graph.fuzzy",
				SymbolName: pkg,
			}))
		}
		// Exact directory key first.
		for _, f := range lg.FilesInPackage(pkg) {
			add(f)
		}
		// Fall back to package nodes whose simple name or directory matches.
		for _, p := range lg.Packages() {
			if p.Name != pkg && p.Package != pkg && !strings.HasSuffix(p.Package, "/"+pkg) {
				continue
			}
			for _, f := range lg.FilesInPackage(p.Package) {
				add(f)
			}
		}
		if !rs.Empty() {
			rs.Confidence = ConfFuzzy.Float64()
		}
		return rs
	}
	rs := &ResultSet{Strategy: "graph.fuzzy"}

	files := gl.graph.FilesByPackage(pkg)
	for _, f := range files {
		rs.Add(Score(ConfFuzzy, Result{
			File:       f.Path,
			Strategy:   "graph.fuzzy",
			SymbolName: pkg,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfFuzzy.Float64()
	}

	return rs
}

func (gl *GraphLookup) SearchImports(target string) *ResultSet {
	if lg := gl.leaGraph(); lg != nil {
		return gl.searchImportsLea(lg, target)
	}
	rs := &ResultSet{Strategy: "graph.imports"}

	for file, imports := range gl.graph.Imports {
		for _, imp := range imports {
			if strings.Contains(imp, target) {
				rs.Add(Score(ConfPartial, Result{
					File:       file,
					Strategy:   "graph.imports",
					SymbolName: target,
					Content:    imp,
				}))
			}
		}
	}

	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}

	return rs
}

// searchImportsLea matches the target against the Lea graph's IMPORTS edges:
// every file node importing a package whose path contains the target.
func (gl *GraphLookup) searchImportsLea(lg *leagraph.Graph, target string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.imports"}
	for _, e := range lg.ImportEdges() {
		from, okFrom := lg.Node(e.From)
		to, okTo := lg.Node(e.To)
		if !okFrom || !okTo {
			continue
		}
		if strings.Contains(to.Package, target) {
			rs.Add(Score(ConfPartial, Result{
				File:       from.File,
				Strategy:   "graph.imports",
				SymbolName: target,
				Content:    to.Package,
			}))
		}
	}
	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}
	return rs
}

func (gl *GraphLookup) SearchDependents(file string) *ResultSet {
	if lg := gl.leaGraph(); lg != nil {
		return gl.searchDependentsLea(lg, file)
	}
	rs := &ResultSet{Strategy: "graph.imports"}

	deps := gl.graph.Dependents[file]
	for _, dep := range deps {
		rs.Add(Score(ConfPartial, Result{
			File:       dep,
			Strategy:   "graph.imports",
			SymbolName: file,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}

	return rs
}

// searchDependentsLea returns every file that imports the package of the given
// file, excluding the file itself.
func (gl *GraphLookup) searchDependentsLea(lg *leagraph.Graph, file string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.imports"}
	self, ok := lg.File(file)
	if !ok {
		return rs
	}
	targetPkg := self.Package
	if targetPkg == "" {
		return rs
	}
	for _, e := range lg.ImportEdges() {
		to, okTo := lg.Node(e.To)
		if !okTo || to.Package != targetPkg {
			continue
		}
		from, okFrom := lg.Node(e.From)
		if !okFrom || from.File == file {
			continue
		}
		rs.Add(Score(ConfPartial, Result{
			File:       from.File,
			Strategy:   "graph.imports",
			SymbolName: file,
		}))
	}
	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}
	return rs
}

func (gl *GraphLookup) SearchAll(query string) *ResultSet {
	rs := &ResultSet{Strategy: "graph"}

	exact := gl.SearchSymbol(query)
	if !exact.Empty() {
		rs.Merge(exact)
	}

	pkg := gl.SearchPackage(query)
	if !pkg.Empty() {
		rs.Merge(pkg)
	}

	imports := gl.SearchImports(query)
	if !imports.Empty() {
		rs.Merge(imports)
	}

	if !rs.Empty() {
		rs.Confidence = rs.Results[0].Confidence
	}

	return rs
}

func (gl *GraphLookup) ListFiles(pattern string) *ResultSet {
	rs := &ResultSet{Strategy: "glob.file"}

	files := []string{}
	if lg := gl.leaGraph(); lg != nil {
		files = lg.Files()
	} else {
		for _, f := range gl.graph.Files {
			files = append(files, f.Path)
		}
	}

	for _, path := range files {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			symName := ""
			if lg := gl.leaGraph(); lg != nil {
				if n, ok := lg.File(path); ok {
					symName = n.Package
				}
			} else if fn := gl.graph.LookupFile(path); fn != nil {
				symName = fn.Package
			}
			if symName == "" {
				symName = path
			}
			rs.Add(Score(ConfPartial, Result{
				File:       path,
				Strategy:   "glob.file",
				SymbolName: symName,
			}))
		}
	}

	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}

	return rs
}
