package retrieval

import (
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
)

type GraphLookup struct {
	graph *graph.Graph
	root  string
}

func NewGraphLookup(g *graph.Graph, root string) *GraphLookup {
	return &GraphLookup{graph: g, root: root}
}

func (gl *GraphLookup) SearchSymbol(name string) *ResultSet {
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

func (gl *GraphLookup) SearchFile(path string) *ResultSet {
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

func (gl *GraphLookup) SearchDependents(file string) *ResultSet {
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

	for _, f := range gl.graph.Files {
		matched, err := filepath.Match(pattern, f.Path)
		if err == nil && matched {
			symName := f.Package
			if symName == "" {
				symName = f.Path
			}
			rs.Add(Score(ConfPartial, Result{
				File:       f.Path,
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
