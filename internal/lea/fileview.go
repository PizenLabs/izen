package lea

import (
	"os"
	"path/filepath"
	"time"

	leagraph "github.com/PizenLabs/izen/internal/lea/graph"
)

// SymbolKind classifies a symbol in the file-centric view.
type SymbolKind string

const (
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolStruct    SymbolKind = "struct"
	SymbolInterface SymbolKind = "interface"
	SymbolClass     SymbolKind = "class"
	SymbolType      SymbolKind = "type"
	SymbolEnum      SymbolKind = "enum"
	SymbolVariable  SymbolKind = "variable"
	SymbolConstant  SymbolKind = "constant"
	SymbolField     SymbolKind = "field"
	SymbolImport    SymbolKind = "import"
	SymbolPackage   SymbolKind = "package"
)

func (k SymbolKind) String() string { return string(k) }

// Language identifies the source language of a file.
type Language string

const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangRust       Language = "rust"
	LangJava       Language = "java"
	LangTypeScript Language = "typescript"
	LangJavaScript Language = "javascript"
)

// LangFromExt maps a file extension to a Language.
func LangFromExt(ext string) (Language, bool) {
	switch ext {
	case ".go":
		return LangGo, true
	case ".py":
		return LangPython, true
	case ".rs":
		return LangRust, true
	case ".java":
		return LangJava, true
	case ".ts":
		return LangTypeScript, true
	case ".tsx":
		return LangTypeScript, true
	case ".js":
		return LangJavaScript, true
	case ".jsx":
		return LangJavaScript, true
	default:
		return "", false
	}
}

// Symbol is a structural symbol in the file-centric view.
type Symbol struct {
	Name      string
	Kind      SymbolKind
	File      string
	Line      int
	Column    int
	EndLine   int
	EndColumn int
	Parent    string
	Signature string
	Exported  bool
}

// FileNode is a source file in the file-centric view.
type FileNode struct {
	Path     string
	Language Language
	Symbols  []Symbol
	Imports  []string
	Package  string
	Size     int64
	Lines    int
	Mtime    int64
}

// FileGraph is a file-centric projection of the lea structural graph. It
// mirrors the legacy native graph shape (files, per-file symbols and imports,
// dependents, symbol index) so analysis and context-assembly consumers can be
// served from the canonical lea engine without depending on a legacy index.
type FileGraph struct {
	Root       string
	Files      []FileNode
	Imports    map[string][]string
	Dependents map[string][]string
	SymbolIdx  map[string][]Symbol
	FileMap    map[string]*FileNode
	BuiltAt    time.Time
	FileCount  int
	SymCount   int
}

// NewFileGraph creates an empty file-centric graph rooted at root.
func NewFileGraph(root string) *FileGraph {
	return &FileGraph{
		Root:       root,
		Imports:    make(map[string][]string),
		Dependents: make(map[string][]string),
		SymbolIdx:  make(map[string][]Symbol),
		FileMap:    make(map[string]*FileNode),
		BuiltAt:    time.Now(),
	}
}

// AddFile appends a file to the file-centric graph and indexes it.
func (g *FileGraph) AddFile(fn FileNode) {
	g.Files = append(g.Files, fn)
	g.FileMap[fn.Path] = &g.Files[len(g.Files)-1]
	g.FileCount++
	g.SymCount += len(fn.Symbols)

	for _, sym := range fn.Symbols {
		g.SymbolIdx[sym.Name] = append(g.SymbolIdx[sym.Name], sym)
	}

	if len(fn.Imports) > 0 {
		g.Imports[fn.Path] = fn.Imports
		for _, imp := range fn.Imports {
			g.Dependents[imp] = append(g.Dependents[imp], fn.Path)
		}
	}
}

// LookupSymbol returns every definition of a symbol by simple name.
func (g *FileGraph) LookupSymbol(name string) []Symbol {
	return g.SymbolIdx[name]
}

// LookupFile returns the file-centric node for a path, or nil.
func (g *FileGraph) LookupFile(path string) *FileNode {
	return g.FileMap[path]
}

// FilesByPackage returns the files declared in a package directory.
func (g *FileGraph) FilesByPackage(pkg string) []FileNode {
	var result []FileNode
	for _, f := range g.Files {
		if f.Package == pkg {
			result = append(result, f)
		}
	}
	return result
}

// FileGraphStats summarizes the file-centric graph contents.
type FileGraphStats struct {
	FileCount     int
	SymbolCount   int
	ImportCount   int
	FunctionCount int
	MethodCount   int
	TypeCount     int
}

// Stats computes summary statistics over the file-centric view.
func (g *FileGraph) Stats() FileGraphStats {
	s := FileGraphStats{
		FileCount:   g.FileCount,
		SymbolCount: g.SymCount,
	}
	for _, f := range g.Files {
		s.ImportCount += len(f.Imports)
		for _, sym := range f.Symbols {
			switch sym.Kind {
			case SymbolFunction:
				s.FunctionCount++
			case SymbolMethod:
				s.MethodCount++
			case SymbolType, SymbolStruct, SymbolInterface, SymbolClass, SymbolEnum:
				s.TypeCount++
			}
		}
	}
	return s
}

// FromLea projects a lea structural graph into the file-centric view. File
// metadata the lea graph does not index (Size, Lines, Mtime) is read from
// disk on a best-effort basis; Language is derived from the file extension;
// method Parent is derived from the qualified name. This is the canonical
// redirect seam for consumers that operate on the file-centric shape.
func FromLea(lg *leagraph.Graph) *FileGraph {
	if lg == nil {
		return NewFileGraph("")
	}
	out := NewFileGraph(lg.Root())
	out.BuiltAt = lg.BuiltAt()

	// Package names: the lea package node carries the Go package name (e.g.
	// "aapi") while the file node carries the directory (e.g. "internal/aapi").
	// The legacy file-centric view exposed the package NAME on FileNode.Package,
	// so we resolve dir -> name once and apply it to every file.
	pkgNameByDir := make(map[string]string)
	for _, p := range lg.Packages() {
		if p.Name != "" {
			pkgNameByDir[p.Package] = p.Name
		}
	}

	for _, path := range lg.Files() {
		fn, ok := lg.File(path)
		if !ok {
			continue
		}
		fnode := FileNode{
			Path:    path,
			Package: fn.Package,
		}
		if name, ok := pkgNameByDir[fn.Package]; ok {
			fnode.Package = name
		}
		if lang, ok := LangFromExt(filepath.Ext(path)); ok {
			fnode.Language = lang
		}
		fnode.Size, fnode.Lines, fnode.Mtime = statFile(lg.Root(), path)
		for _, s := range lg.SymbolsOfFile(path) {
			kind, ok := nodeKindToSymbolKind(s.Kind)
			if !ok {
				continue
			}
			fnode.Symbols = append(fnode.Symbols, Symbol{
				Name:      s.Name,
				Kind:      kind,
				File:      s.File,
				Line:      s.Line,
				EndLine:   s.EndLine,
				Parent:    receiverFromQual(s.QualName),
				Signature: s.Signature,
				Exported:  s.Exported,
			})
		}
		out.AddFile(fnode)
	}

	// Imports: raw import paths are retained per file by the lea graph (the
	// file-centric consumers rely on the exact import strings). In-repo
	// imports additionally appear as IMPORTS edges; we prefer the raw list so
	// external imports are preserved too.
	raw := make(map[string][]string)
	for _, path := range lg.Files() {
		if imps := lg.ImportsOf(path); len(imps) > 0 {
			raw[path] = imps
		}
	}
	for i := range out.Files {
		imps, ok := raw[out.Files[i].Path]
		if !ok {
			// Fall back to resolved IMPORTS edges for graphs persisted before
			// raw import retention (older cache versions).
			for _, e := range lg.ImportEdges() {
				from, okFrom := lg.Node(e.From)
				to, okTo := lg.Node(e.To)
				if !okFrom || !okTo || from.File != out.Files[i].Path {
					continue
				}
				imps = append(imps, to.Package)
			}
		}
		if len(imps) > 0 {
			out.Files[i].Imports = imps
			out.Imports[out.Files[i].Path] = imps
			for _, imp := range imps {
				out.Dependents[imp] = append(out.Dependents[imp], out.Files[i].Path)
			}
		}
	}
	return out
}

// FileGraph returns the current file-centric projection of the lea graph.
// The projection is cached and invalidated whenever the underlying graph is
// rebuilt or refreshed (its BuiltAt timestamp changes), so repeated access
// does not re-read file metadata from disk.
func (e *Engine) FileGraph() *FileGraph {
	if e == nil {
		return nil
	}
	g := e.Graph()
	built := g.BuiltAt()

	e.mu.RLock()
	cached := e.fileViewCache
	e.mu.RUnlock()
	if cached != nil && built.Equal(e.fileViewBuilt) {
		return cached
	}

	fv := FromLea(g)
	e.mu.Lock()
	e.fileViewCache = fv
	e.fileViewBuilt = built
	e.mu.Unlock()
	return fv
}

// nodeKindToSymbolKind maps a lea node kind to a file-centric symbol kind.
func nodeKindToSymbolKind(k leagraph.NodeKind) (SymbolKind, bool) {
	switch k {
	case leagraph.KindFunction:
		return SymbolFunction, true
	case leagraph.KindMethod:
		return SymbolMethod, true
	case leagraph.KindStruct:
		return SymbolStruct, true
	case leagraph.KindClass:
		return SymbolClass, true
	case leagraph.KindInterface:
		return SymbolInterface, true
	case leagraph.KindType:
		return SymbolType, true
	case leagraph.KindEnum:
		return SymbolEnum, true
	default:
		return "", false
	}
}

// receiverFromQual extracts the receiver from a method qualified name
// (e.g. "Workspace.render" -> "Workspace") for the Parent field.
func receiverFromQual(qual string) string {
	if i := lastIndexByte(qual, '.'); i >= 0 {
		return qual[:i]
	}
	return ""
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// statFile reads best-effort file metadata (size, line count, mtime) for a
// relative path so the file-centric view preserves the fields the legacy
// scanner populated. A missing/unreadable file yields zero values.
func statFile(root, rel string) (size int64, lines int, mtime int64) {
	abs := filepath.Join(root, rel)
	info, err := os.Stat(abs)
	if err != nil {
		return 0, 0, 0
	}
	n := 0
	data, err := os.ReadFile(abs)
	if err == nil {
		for _, b := range data {
			if b == '\n' {
				n++
			}
		}
	}
	return info.Size(), n + 1, info.ModTime().Unix()
}
