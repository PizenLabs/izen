package graph

import (
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// FileExtract retains the raw call/route data of an indexed file so CALLS and
// HTTP_HANDLES edges can be deterministically re-resolved after every
// incremental change.
type FileExtract struct {
	File   string
	Calls  []symbol.CallSite
	Routes []symbol.HTTPRoute
}

// Build replaces the graph contents with the structural view of files.
func (g *Graph) Build(files []symbol.FileASTInfo) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reset()
	for _, fi := range files {
		g.upsertFileLocked(fi)
	}
	g.rebuildCallEdgesLocked()
	g.rebuildImplementsLocked()
	g.builtAt = time.Now()
	return nil
}

// UpsertFile incrementally replaces the nodes and edges produced by one file,
// leaving untouched nodes from other files in place.
func (g *Graph) UpsertFile(fi symbol.FileASTInfo) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removeFileLocked(fi.FilePath)
	g.upsertFileLocked(fi)
	g.rebuildCallEdgesLocked()
	g.rebuildImplementsLocked()
	g.builtAt = time.Now()
	return nil
}

// RemoveFile drops every node and edge contributed by a file path.
func (g *Graph) RemoveFile(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removeFileLocked(normalizePath(path))
	g.rebuildCallEdgesLocked()
	g.rebuildImplementsLocked()
	g.builtAt = time.Now()
}

func (g *Graph) reset() {
	g.nodes = make(map[string]*Node)
	g.byName = make(map[string][]string)
	g.byQual = make(map[string][]string)
	g.byKind = make(map[NodeKind][]string)
	g.fileNodes = make(map[string]string)
	g.packageNodes = make(map[string]string)
	g.pkgFiles = make(map[string][]string)
	g.nodesByFile = make(map[string][]string)
	g.typeByPkg = make(map[string][]string)
	g.methodByRecv = make(map[string][]string)
	g.interfaceById = make(map[string]*Node)
	g.out = make(map[string][]Edge)
	g.in = make(map[string][]Edge)
	g.fileExtracts = make(map[string]FileExtract)
	g.builtAt = time.Now()
}

func (g *Graph) upsertFileLocked(fi symbol.FileASTInfo) {
	path := normalizePath(fi.FilePath)
	dir := dirOf(path)

	pkgNode := g.ensurePackageLocked(dir, fi.Package)
	fileNode := g.ensureFileLocked(fi, path, dir)
	g.addEdge(Edge{From: pkgNode, To: fileNode, Kind: EdgeDefines})

	for _, sym := range fi.Symbols {
		kind, ok := symbolKindToNodeKind(sym.Kind)
		if !ok {
			continue
		}
		qual := qualifiedName(sym)
		id := nodeID(kind, string(fi.Language), dir, path, qual)
		if _, exists := g.nodes[id]; exists {
			continue
		}
		n := &Node{
			ID:        id,
			Kind:      kind,
			Name:      sym.Name,
			QualName:  qual,
			Package:   dir,
			File:      path,
			Line:      sym.StartLine,
			EndLine:   sym.EndLine,
			Exported:  sym.Exported,
			Signature: sym.Signature,
			Methods:   append([]string(nil), sym.Methods...),
		}
		g.addNode(n)
		g.nodesByFile[path] = append(g.nodesByFile[path], id)
		g.addEdge(Edge{From: fileNode, To: id, Kind: EdgeDefines})
	}

	g.addImportEdgesLocked(fileNode, dir, fi.Imports)
	g.addRouteNodesLocked(fi, dir, path)
	g.fileExtracts[path] = FileExtract{
		File:   path,
		Calls:  fi.Calls,
		Routes: fi.Routes,
	}
}

func (g *Graph) ensurePackageLocked(dir, goPkg string) string {
	if id, ok := g.packageNodes[dir]; ok {
		return id
	}
	name := dir
	if goPkg != "" {
		name = goPkg
	}
	n := &Node{
		ID:       "package:" + dir,
		Kind:     KindPackage,
		Name:     name,
		QualName: dir,
		Package:  dir,
	}
	g.addNode(n)
	g.pkgFiles[n.ID] = nil
	return n.ID
}

func (g *Graph) ensureFileLocked(fi symbol.FileASTInfo, path, dir string) string {
	if id, ok := g.fileNodes[path]; ok {
		return id
	}
	n := &Node{
		ID:      "file:" + path,
		Kind:    KindFile,
		Name:    filepath.Base(path),
		Package: dir,
		File:    path,
	}
	g.addNode(n)
	g.nodesByFile[path] = append(g.nodesByFile[path], n.ID)
	if pkgID, ok := g.packageNodes[dir]; ok {
		g.pkgFiles[pkgID] = append(g.pkgFiles[pkgID], n.ID)
	}
	return n.ID
}

// removeFileLocked drops a file node, its declared symbols and routes, then
// prunes any package left with no files.
func (g *Graph) removeFileLocked(path string) {
	ids := g.nodesByFile[path]
	var pkgID string
	if id, ok := g.packageNodes[dirOf(path)]; ok {
		pkgID = id
	}
	for _, id := range ids {
		g.removeNode(id)
	}
	delete(g.nodesByFile, path)
	delete(g.fileExtracts, path)
	if pkgID != "" && len(g.pkgFiles[pkgID]) == 0 {
		g.removeNode(pkgID)
	}
}
