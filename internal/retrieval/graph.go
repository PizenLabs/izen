package retrieval

import (
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/lea"
)

type GraphLookup struct {
	// lea is the Phase 3 structural engine. Every structural lookup is served
	// from the Lea graph (symbol resolution, file/package discovery, import
	// edges, dependents) via its file-centric projection.
	lea  *lea.Engine
	root string
}

func NewGraphLookup(e *lea.Engine, root string) *GraphLookup {
	return &GraphLookup{lea: e, root: root}
}

// NewLeaGraphLookup wraps a Lea structural engine as the graph-tier source.
// It redirects structural lookups (symbols, files, packages, imports,
// dependents, glob) onto the Lea index, which is richer (call edges, routes)
// and stays fresh via incremental sync.
func NewLeaGraphLookup(e *lea.Engine, root string) *GraphLookup {
	return &GraphLookup{lea: e, root: root}
}

// leaFileGraph returns the current file-centric projection of the lea engine,
// or nil when the engine is absent or not yet indexed.
func (gl *GraphLookup) leaFileGraph() *lea.FileGraph {
	if gl == nil || gl.lea == nil {
		return nil
	}
	fg := gl.lea.FileGraph()
	if fg == nil || len(fg.Files) == 0 {
		return nil
	}
	return fg
}

func (gl *GraphLookup) HasGraph() bool {
	if gl == nil {
		return false
	}
	return gl.leaFileGraph() != nil
}

func (gl *GraphLookup) SearchSymbol(name string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.exact"}

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	symbols := fg.LookupSymbol(name)
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

func (gl *GraphLookup) SearchFile(path string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.file"}

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	fn := fg.LookupFile(path)
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
	rs := &ResultSet{Strategy: "graph.fuzzy"}

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	seen := make(map[string]bool)
	for _, f := range fg.Files {
		if f.Package != pkg {
			continue
		}
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
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
	rs := &ResultSet{Strategy: "graph.imports"}

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	for file, imports := range fg.Imports {
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

func (gl *GraphLookup) SearchDependents(file string) *ResultSet {
	rs := &ResultSet{Strategy: "graph.imports"}

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	seen := make(map[string]bool)
	add := func(dep string) {
		if dep == file || seen[dep] {
			return
		}
		seen[dep] = true
		rs.Add(Score(ConfPartial, Result{
			File:       dep,
			Strategy:   "graph.imports",
			SymbolName: file,
		}))
	}

	// Files that import the target string directly (legacy Dependents map).
	for _, dep := range fg.Dependents[file] {
		add(dep)
	}

	// Files importing the package directory of the target file, resolved from
	// the lea graph's import edges.
	if gl.lea != nil {
		dir := filepath.Dir(filepath.ToSlash(file))
		for _, dep := range gl.lea.Graph().ImportingFiles(dir) {
			add(dep)
		}
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

	fg := gl.leaFileGraph()
	if fg == nil {
		return rs
	}

	for _, fn := range fg.Files {
		matched, err := filepath.Match(pattern, fn.Path)
		if err == nil && matched {
			symName := fn.Package
			if symName == "" {
				symName = fn.Path
			}
			rs.Add(Score(ConfPartial, Result{
				File:       fn.Path,
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
