package graph

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// normalizePath converts a file path to a stable relative slash form.
func normalizePath(path string) string {
	p := filepath.ToSlash(path)
	p = strings.TrimPrefix(p, "./")
	return p
}

// symbolKindToNodeKind maps extraction symbol kinds to graph node kinds.
func symbolKindToNodeKind(k symbol.SymbolKind) (NodeKind, bool) {
	switch k {
	case symbol.SymbolFunction:
		return KindFunction, true
	case symbol.SymbolMethod:
		return KindMethod, true
	case symbol.SymbolStruct:
		return KindStruct, true
	case symbol.SymbolClass:
		return KindClass, true
	case symbol.SymbolInterface:
		return KindInterface, true
	case symbol.SymbolType:
		return KindType, true
	case symbol.SymbolEnum:
		return KindEnum, true
	default:
		return "", false
	}
}

// qualifiedName renders a symbol's qualified name: methods use
// "Receiver.Name", everything else its simple name.
func qualifiedName(sym symbol.SymbolNode) string {
	if sym.Kind == symbol.SymbolMethod {
		recv := strings.TrimPrefix(sym.Receiver, "*")
		if recv != "" {
			return recv + "." + sym.Name
		}
		if sym.Parent != "" {
			return strings.TrimPrefix(sym.Parent, "*") + "." + sym.Name
		}
	}
	return sym.Name
}

// nodeID builds a stable node identifier. Go symbols are scoped by package so
// edges survive renames between files in the same package.
func nodeID(kind NodeKind, lang, dir, file, qual string) string {
	if lang == string(symbol.LangGo) {
		return string(kind) + ":" + dir + ":" + qual
	}
	return string(kind) + ":" + dir + ":" + file + ":" + qual
}

// addImportEdgesLocked connects a file node to the in-repo package node each
// import resolves to. External imports produce no edges.
func (g *Graph) addImportEdgesLocked(fileNode, dir string, imports []symbol.DependencyEdge) {
	seen := make(map[string]bool)
	for _, imp := range imports {
		target := strings.TrimPrefix(imp.ImportPath, "./")
		if pkgID := g.resolvePackageLocked(target); pkgID != "" {
			if seen[pkgID] {
				continue
			}
			seen[pkgID] = true
			g.addEdge(Edge{From: fileNode, To: pkgID, Kind: EdgeImports})
		}
	}
}

// resolvePackageLocked matches an import path to an indexed package by longest
// path suffix.
func (g *Graph) resolvePackageLocked(importPath string) string {
	if id, ok := g.packageNodes[importPath]; ok {
		return id
	}
	best := ""
	for pkg := range g.packageNodes {
		if pkg == "root" {
			continue
		}
		if strings.HasSuffix(importPath, pkg) && len(pkg) > len(best) {
			best = pkg
		}
	}
	if best != "" {
		return g.packageNodes[best]
	}
	return ""
}

// addRouteNodesLocked creates HTTP route nodes (edges are resolved later in
// rebuildCallEdgesLocked once all files are indexed).
func (g *Graph) addRouteNodesLocked(fi symbol.FileASTInfo, dir, path string) {
	for _, r := range fi.Routes {
		id := "route:" + dir + ":" + r.Method + ":" + r.Path
		if _, exists := g.nodes[id]; !exists {
			n := &Node{
				ID:       id,
				Kind:     KindHTTPRoute,
				Name:     r.Method + " " + r.Path,
				QualName: id,
				Package:  dir,
				File:     path,
				Line:     r.Line,
			}
			g.addNode(n)
			g.nodesByFile[path] = append(g.nodesByFile[path], id)
		}
	}
}

// funcNodesInFileLocked indexes the function/method nodes declared in a file by
// qualified name.
func (g *Graph) funcNodesInFileLocked(path string) map[string]string {
	index := make(map[string]string)
	for _, id := range g.nodesByFile[path] {
		n := g.nodes[id]
		if n == nil || (n.Kind != KindFunction && n.Kind != KindMethod) {
			continue
		}
		index[n.QualName] = id
		if n.QualName != n.Name {
			index[n.Name] = id
		}
	}
	return index
}

// rebuildCallEdgesLocked deterministically re-resolves every CALLS,
// HTTP_HANDLES, and IMPORTS edge from the retained per-file extract data. It
// runs after a full build and after every incremental change so cross-file
// references stay exact regardless of file processing order.
func (g *Graph) rebuildCallEdgesLocked() {
	for from, es := range g.out {
		out := es[:0]
		for _, e := range es {
			if e.Kind != EdgeCalls && e.Kind != EdgeHTTPHandles && e.Kind != EdgeImports {
				out = append(out, e)
			}
		}
		g.out[from] = out
	}
	for to, es := range g.in {
		out := es[:0]
		for _, e := range es {
			if e.Kind != EdgeCalls && e.Kind != EdgeHTTPHandles && e.Kind != EdgeImports {
				out = append(out, e)
			}
		}
		g.in[to] = out
	}

	files := make([]string, 0, len(g.fileExtracts))
	for path := range g.fileExtracts {
		files = append(files, path)
	}
	sort.Strings(files)

	for _, path := range files {
		fe := g.fileExtracts[path]
		dir := dirOf(path)
		callerIndex := g.funcNodesInFileLocked(path)

		// Re-resolve IMPORTS now that every package node exists: addImportEdges
		// may have run before an imported package was indexed, so the edge is
		// only guaranteed here.
		fileID := g.fileNodes[path]
		if fileID != "" && len(fe.Imports) > 0 {
			seen := make(map[string]bool)
			for _, imp := range fe.Imports {
				target := strings.TrimPrefix(imp, "./")
				if pkgID := g.resolvePackageLocked(target); pkgID != "" && !seen[pkgID] {
					seen[pkgID] = true
					g.addEdge(Edge{From: fileID, To: pkgID, Kind: EdgeImports})
				}
			}
		}

		for _, call := range fe.Calls {
			fromID := callerIndex[call.InFunc]
			if fromID == "" {
				continue
			}
			toID := g.resolveCalleeLocked(call.Name, dir)
			if toID == "" {
				continue
			}
			g.addEdge(Edge{From: fromID, To: toID, Kind: EdgeCalls, Line: call.Line})
		}
		for _, r := range fe.Routes {
			if r.Handler == "" {
				continue
			}
			routeID := "route:" + dir + ":" + r.Method + ":" + r.Path
			if _, exists := g.nodes[routeID]; !exists {
				continue
			}
			handlerID := g.resolveCalleeLocked(r.Handler, dir)
			if handlerID == "" {
				continue
			}
			g.addEdge(Edge{From: routeID, To: handlerID, Kind: EdgeHTTPHandles, Line: r.Line})
		}
	}
}

// rebuildImplementsLocked recomputes all IMPLEMENTS edges from the current
// interface and type node set.
func (g *Graph) rebuildImplementsLocked() {
	for from, es := range g.out {
		out := es[:0]
		for _, e := range es {
			if e.Kind != EdgeImplements {
				out = append(out, e)
			}
		}
		g.out[from] = out
	}
	interfaces := make([]*Node, 0, len(g.interfaceById))
	for _, n := range g.interfaceById {
		interfaces = append(interfaces, n)
	}
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].ID < interfaces[j].ID })

	for _, iface := range interfaces {
		if len(iface.Methods) == 0 {
			continue
		}
		for _, tid := range g.typeByPkg[iface.Package] {
			if tid == iface.ID {
				continue
			}
			t := g.nodes[tid]
			if t == nil {
				continue
			}
			if g.typeImplementsLocked(t, iface) {
				g.addEdge(Edge{From: tid, To: iface.ID, Kind: EdgeImplements})
			}
		}
	}
}

// typeImplementsLocked reports whether a concrete type's method set satisfies
// an interface's declared methods.
func (g *Graph) typeImplementsLocked(t *Node, iface *Node) bool {
	methods := make(map[string]bool)
	collect := func(recv string) {
		for _, id := range g.methodByRecv[recv] {
			m := g.nodes[id]
			if m != nil && m.Package == t.Package {
				methods[m.Name] = true
			}
		}
	}
	collect(t.Name)
	collect("*" + t.Name)
	for _, name := range iface.Methods {
		if !methods[name] {
			return false
		}
	}
	return true
}

// resolveCalleeLocked resolves a call reference as written to a node ID in the
// same package when possible, preferring exact qualified matches.
func (g *Graph) resolveCalleeLocked(name, callerDir string) string {
	// Exact simple-name match within the caller's package first.
	if ids := g.byName[name]; len(ids) > 0 {
		if id := g.pickInPackageLocked(ids, callerDir); id != "" {
			return id
		}
		if len(ids) == 1 {
			return ids[0]
		}
	}

	lastDot := strings.LastIndex(name, ".")
	if lastDot < 0 {
		return ""
	}
	simple := name[lastDot+1:]
	base := name[:lastDot]

	// Receiver.Method or pkg.Func qualified match.
	if ids := g.byQual[name]; len(ids) > 0 {
		if id := g.pickInPackageLocked(ids, callerDir); id != "" {
			return id
		}
		return ids[0]
	}
	// Receiver-based method lookup.
	for _, id := range g.methodByRecv[base] {
		m := g.nodes[id]
		if m != nil && m.Name == simple {
			if m.Package == callerDir {
				return id
			}
		}
	}
	for _, id := range g.methodByRecv[base] {
		if m := g.nodes[id]; m != nil && m.Name == simple {
			return id
		}
	}
	// Package-scoped function lookup by suffix.
	for _, id := range g.byName[simple] {
		n := g.nodes[id]
		if n != nil && n.Kind == KindFunction && strings.HasSuffix(n.Package, base) {
			return id
		}
	}
	// Unqualified fallback within the caller's package.
	if ids := g.byName[simple]; len(ids) > 0 {
		if id := g.pickInPackageLocked(ids, callerDir); id != "" {
			return id
		}
		if len(ids) == 1 {
			return ids[0]
		}
	}
	return ""
}

// pickInPackageLocked returns the first node ID (sorted) declared in a package.
func (g *Graph) pickInPackageLocked(ids []string, pkg string) string {
	best := ""
	for _, id := range ids {
		n := g.nodes[id]
		if n != nil && n.Package == pkg {
			if best == "" || id < best {
				best = id
			}
		}
	}
	return best
}
