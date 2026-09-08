package layer2

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/lea"
	leagraph "github.com/PizenLabs/izen/internal/lea/graph"
)

// SymbolInfo is an immutable structural view of a symbol from the SoR. It is a
// value type; callers must treat it as read-only.
type SymbolInfo struct {
	ID        string
	Name      string
	Kind      string
	QualName  string
	Package   string
	File      string
	Line      int
	EndLine   int
	Exported  bool
	Signature string
}

// SourceReader reads a file's content relative to the repository root. The
// default implementation reads from disk; the hook exists as a test seam.
type SourceReader func(root, path string) ([]byte, error)

// Option configures a Sor.
type Option func(*Sor)

// WithSourceReader overrides how the Sor reads source content.
func WithSourceReader(r SourceReader) Option {
	return func(s *Sor) { s.source = r }
}

// Sor is the System of Record facade of Layer 2. It wraps the lea Engine and
// exposes a thread-safe, read-only view of AST-derived symbols, the call
// graph, symbol tables and imports. An in-memory index is rebuilt only when
// the underlying graph changes; all lookups resolve against the cached index.
//
// A Sor is safe for concurrent use. Returned slices are fresh copies, so
// callers cannot corrupt the cache or observe concurrent mutation.
type Sor struct {
	engine *lea.Engine
	root   string

	source SourceReader

	mu       sync.RWMutex
	idx      *sorIndex
	indexKey int64
}

// sorIndex is an immutable, point-in-time projection of the lea graph.
type sorIndex struct {
	builtAt        int64
	files          []string
	pkgDirs        []string
	pkgOfFile      map[string]string
	pkgFiles       map[string][]string
	byName         map[string][]SymbolInfo
	byQual         map[string]SymbolInfo
	byFile         map[string][]SymbolInfo
	imports        map[string][]string
	importersByPkg map[string][]string
	callers        map[string][]string
	callees        map[string][]string
}

// NewSor wraps the lea engine as the Layer 2 System of Record.
func NewSor(engine *lea.Engine, opts ...Option) *Sor {
	s := &Sor{engine: engine}
	if engine != nil {
		s.root = engine.Root()
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Root returns the repository root the SoR indexes.
func (s *Sor) Root() string { return s.root }

// Engine returns the underlying lea Engine, the canonical System of Record.
func (s *Sor) Engine() *lea.Engine { return s.engine }

// Version returns the graph build timestamp backing the current index. It
// changes whenever the SoR refreshes, letting callers detect staleness.
func (s *Sor) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexKey
}

// index returns the current immutable index, rebuilding it when the
// underlying graph has changed.
func (s *Sor) index() *sorIndex {
	s.mu.RLock()
	idx, key := s.idx, s.indexKey
	s.mu.RUnlock()

	current := s.builtAt()
	if idx != nil && key == current {
		return idx
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current = s.builtAt()
	if s.idx != nil && s.indexKey == current {
		return s.idx
	}
	var g *leagraph.Graph
	if s.engine != nil {
		g = s.engine.Graph()
	}
	s.idx = buildIndex(g)
	s.indexKey = current
	return s.idx
}

// builtAt returns the current graph build timestamp, or zero when the engine
// is unavailable.
func (s *Sor) builtAt() int64 {
	if s.engine == nil {
		return 0
	}
	g := s.engine.Graph()
	if g == nil {
		return 0
	}
	return g.BuiltAt().UnixNano()
}

// Files returns every indexed file path, sorted.
func (s *Sor) Files() []string {
	idx := s.index()
	return append([]string(nil), idx.files...)
}

// FileCount returns the number of indexed files.
func (s *Sor) FileCount() int { return len(s.index().files) }

// HasFile reports whether path is an indexed source file.
func (s *Sor) HasFile(path string) bool {
	idx := s.index()
	_, ok := idx.pkgOfFile[path]
	return ok
}

// Symbol resolves name to the best-matching symbol. Exact qualified names win
// over simple names; among simple-name collisions, functions and methods are
// preferred, mirroring lea's query target resolution.
func (s *Sor) Symbol(name string) (SymbolInfo, bool) {
	idx := s.index()
	if si, ok := idx.byQual[name]; ok {
		return si, true
	}
	list := idx.byName[name]
	if len(list) == 0 {
		return SymbolInfo{}, false
	}
	for _, si := range list {
		if isCallable(si) {
			return si, true
		}
	}
	return list[0], true
}

// Symbols returns every symbol declared with the given simple name.
func (s *Sor) Symbols(name string) []SymbolInfo {
	idx := s.index()
	return append([]SymbolInfo(nil), idx.byName[name]...)
}

// LookupQual returns the symbol with the exact qualified name, if any.
func (s *Sor) LookupQual(qual string) (SymbolInfo, bool) {
	idx := s.index()
	si, ok := idx.byQual[qual]
	return si, ok
}

// SymbolsOfFile returns the symbols declared by a file, ordered by line.
func (s *Sor) SymbolsOfFile(path string) []SymbolInfo {
	idx := s.index()
	return append([]SymbolInfo(nil), idx.byFile[path]...)
}

// FilesInPackage returns the files declared in a package directory.
func (s *Sor) FilesInPackage(pkg string) []string {
	idx := s.index()
	return append([]string(nil), idx.pkgFiles[pkg]...)
}

// Package returns the package directory a file belongs to.
func (s *Sor) Package(path string) string {
	idx := s.index()
	return idx.pkgOfFile[path]
}

// Language returns the source language of a file, derived from its extension.
func (s *Sor) Language(path string) string {
	if lang, ok := lea.LangFromExt(filepath.Ext(path)); ok {
		return string(lang)
	}
	return ""
}

// ImportsOf returns the raw import path strings declared by a file.
func (s *Sor) ImportsOf(path string) []string {
	idx := s.index()
	return append([]string(nil), idx.imports[path]...)
}

// Dependencies returns the packages path imports, as written (raw import
// paths, including external packages that do not resolve in-repo).
func (s *Sor) Dependencies(path string) []string {
	return s.ImportsOf(path)
}

// InRepoDependencies returns the in-repo package directories path depends on.
func (s *Sor) InRepoDependencies(path string) []string {
	idx := s.index()
	seen := make(map[string]bool)
	var out []string
	for _, imp := range idx.imports[path] {
		if dir := resolvePkgDir(imp, idx.pkgDirs); dir != "" && !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out
}

// Dependents returns in-repo files that depend on path: files importing its
// package and files calling symbols declared in it.
func (s *Sor) Dependents(path string) []string {
	idx := s.index()
	seen := make(map[string]bool)
	var out []string
	pkg := idx.pkgOfFile[path]
	for _, f := range idx.importersByPkg[pkg] {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, sym := range idx.byFile[path] {
		for _, c := range idx.callers[sym.QualName] {
			if ci, ok := idx.byQual[c]; ok && !seen[ci.File] {
				seen[ci.File] = true
				out = append(out, ci.File)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Callers returns the symbols that directly call the given symbol.
func (s *Sor) Callers(qual string) []SymbolInfo {
	return s.relatedSymbols(func(idx *sorIndex) []string { return idx.callers[qual] })
}

// Callees returns the symbols the given symbol directly calls.
func (s *Sor) Callees(qual string) []SymbolInfo {
	return s.relatedSymbols(func(idx *sorIndex) []string { return idx.callees[qual] })
}

func (s *Sor) relatedSymbols(get func(*sorIndex) []string) []SymbolInfo {
	idx := s.index()
	var out []SymbolInfo
	for _, q := range get(idx) {
		if si, ok := idx.byQual[q]; ok {
			out = append(out, si)
		}
	}
	return out
}

// Neighborhood returns files directly related to path: files that import the
// file's package and files of in-repo packages the file imports.
func (s *Sor) Neighborhood(path string) []string {
	idx := s.index()
	seen := make(map[string]bool)
	var out []string
	pkg := idx.pkgOfFile[path]
	for _, f := range idx.importersByPkg[pkg] {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, imp := range idx.imports[path] {
		dir := resolvePkgDir(imp, idx.pkgDirs)
		if dir == "" {
			continue
		}
		for _, f := range idx.pkgFiles[dir] {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Source returns the raw content of an indexed file.
func (s *Sor) Source(path string) ([]byte, error) {
	if s.source != nil {
		return s.source(s.root, path)
	}
	if s.root == "" {
		return nil, fmt.Errorf("layer2: sor has no root")
	}
	return os.ReadFile(filepath.Join(s.root, filepath.FromSlash(path)))
}

// buildIndex projects the lea graph into a point-in-time immutable index.
func buildIndex(g *leagraph.Graph) *sorIndex {
	idx := &sorIndex{
		pkgOfFile:      make(map[string]string),
		pkgFiles:       make(map[string][]string),
		byName:         make(map[string][]SymbolInfo),
		byQual:         make(map[string]SymbolInfo),
		byFile:         make(map[string][]SymbolInfo),
		imports:        make(map[string][]string),
		importersByPkg: make(map[string][]string),
		callers:        make(map[string][]string),
		callees:        make(map[string][]string),
	}
	if g == nil {
		return idx
	}
	idx.builtAt = g.BuiltAt().UnixNano()

	for _, p := range g.Packages() {
		idx.pkgDirs = append(idx.pkgDirs, p.Package)
		idx.pkgFiles[p.Package] = nil
	}
	sort.Strings(idx.pkgDirs)

	for _, path := range g.Files() {
		idx.files = append(idx.files, path)
		pkg := g.Package(path)
		idx.pkgOfFile[path] = pkg
		idx.pkgFiles[pkg] = append(idx.pkgFiles[pkg], path)
		if imps := g.ImportsOf(path); len(imps) > 0 {
			idx.imports[path] = append([]string(nil), imps...)
			for _, imp := range imps {
				if dir := resolvePkgDir(imp, idx.pkgDirs); dir != "" && dir != pkg {
					idx.importersByPkg[dir] = append(idx.importersByPkg[dir], path)
				}
			}
		}
	}
	sort.Strings(idx.files)
	for pkg := range idx.pkgFiles {
		sort.Strings(idx.pkgFiles[pkg])
	}
	for dir := range idx.importersByPkg {
		idx.importersByPkg[dir] = dedupSorted(idx.importersByPkg[dir])
	}

	for _, n := range g.SymbolNodes() {
		si := toSymbolInfo(n)
		idx.byName[si.Name] = append(idx.byName[si.Name], si)
		if si.QualName != "" {
			idx.byQual[si.QualName] = si
		}
		idx.byFile[si.File] = append(idx.byFile[si.File], si)
		if n.Kind == leagraph.KindFunction || n.Kind == leagraph.KindMethod {
			for _, c := range g.Callees(n.ID) {
				if c.QualName != "" && c.QualName != n.QualName {
					idx.callees[n.QualName] = append(idx.callees[n.QualName], c.QualName)
				}
			}
			for _, c := range g.Callers(n.ID) {
				if c.QualName != "" && c.QualName != n.QualName {
					idx.callers[n.QualName] = append(idx.callers[n.QualName], c.QualName)
				}
			}
		}
	}
	for path := range idx.byFile {
		sort.Slice(idx.byFile[path], func(i, j int) bool {
			a, b := idx.byFile[path][i], idx.byFile[path][j]
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return a.Name < b.Name
		})
	}
	for q := range idx.callers {
		idx.callers[q] = dedupSorted(idx.callers[q])
	}
	for q := range idx.callees {
		idx.callees[q] = dedupSorted(idx.callees[q])
	}
	return idx
}

// toSymbolInfo converts a lea graph node into the public symbol value type.
func toSymbolInfo(n leagraph.Node) SymbolInfo {
	return SymbolInfo{
		ID:        n.ID,
		Name:      n.Name,
		Kind:      string(n.Kind),
		QualName:  n.QualName,
		Package:   n.Package,
		File:      n.File,
		Line:      n.Line,
		EndLine:   n.EndLine,
		Exported:  n.Exported,
		Signature: n.Signature,
	}
}

// resolvePkgDir matches an import path to an in-repo package directory by
// longest path suffix, mirroring the lea graph resolution strategy.
func resolvePkgDir(importPath string, pkgDirs []string) string {
	if importPath == "" {
		return ""
	}
	for _, d := range pkgDirs {
		if d == importPath {
			return d
		}
	}
	best := ""
	for _, d := range pkgDirs {
		if d == "root" {
			continue
		}
		if strings.HasSuffix(importPath, d) && len(d) > len(best) {
			best = d
		}
	}
	return best
}

// dedupSorted removes adjacent duplicates from a sorted slice.
func dedupSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for i := 1; i < len(in); i++ {
		if in[i] != out[len(out)-1] {
			out = append(out, in[i])
		}
	}
	return out
}
