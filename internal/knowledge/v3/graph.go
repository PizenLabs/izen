package knowledge

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// contentCap bounds how much of a file is read for content markers and symbol
// extraction, keeping the initial workspace scan cheap even for large files.
const contentCap = 256 * 1024

// skippedDirs are workspace-local directories the scan never descends into.
var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".izen": true, ".codebase-memory": true,
}

// WorkspaceState is the observable snapshot of one scanned workspace. It is a
// plain value: every map is owned by the snapshot and safe to read without
// locking.
type WorkspaceState struct {
	// Root is the workspace directory the state was derived from.
	Root string
	// Empty reports whether the workspace holds no recognisable content.
	Empty bool
	// FileCount is the number of files scanned.
	FileCount int
	// AppTypes maps every application-level target type detected on disk.
	AppTypes map[string]bool
	// Archetypes maps every technical marker detected on disk.
	Archetypes map[string]bool
	// Frameworks maps framework tags (react, next, ...) detected on disk.
	Frameworks map[string]bool
	// Markers are the human-readable markers that fired, in discovery order.
	Markers []string
	// Files indexes every scanned file by its workspace-relative path.
	Files map[string]FileRecord
}

// FileRecord is the metadata the graph keeps for one scanned file.
type FileRecord struct {
	// Path is the workspace-relative path.
	Path string
	// Language is the rule-set key used to extract symbols ("" when not a
	// recognised source file).
	Language string
	// Bytes is the file size in bytes.
	Bytes int64
	// LineCount is the number of lines read.
	LineCount int
	// Symbols are the declarations discovered in the file.
	Symbols []Symbol
}

// FileSummary is the compact per-file digest consumed by planners that want a
// structural overview without re-reading the source.
type FileSummary struct {
	Path       string
	Language   string
	Bytes      int64
	LineCount  int
	Symbols    []Symbol
	Archetypes []string
}

// KnowledgeGraph is the RuntimeKnowledge symbol table: an in-memory graph of
// the project structure that is scanned once and then queried by reference,
// so planners and the intent compiler never re-walk the disk.
//
// The graph is safe for concurrent use. Ensure scans lazily on the first
// query for a root and caches the result; Refresh forces a re-scan.
type KnowledgeGraph struct {
	mu      sync.RWMutex
	root    string
	scanned bool
	state   WorkspaceState
	table   *SymbolTable
}

// NewKnowledgeGraph builds an empty graph. Call Ensure(root) or Scan(root) to
// populate it from disk.
func NewKnowledgeGraph() *KnowledgeGraph {
	return &KnowledgeGraph{table: NewSymbolTable()}
}

// Ensure lazily scans root and caches the resulting WorkspaceState. It is a
// no-op when the graph is already bound to root; call Refresh to invalidate.
// The returned state is the graph's live snapshot, safe to read without
// locking because it is replaced wholesale on refresh.
func (g *KnowledgeGraph) Ensure(root string) WorkspaceState {
	if root == "" {
		root = "."
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.scanned && g.root == root {
		return g.state
	}
	g.scanLocked(root)
	return g.state
}

// Scan forces a fresh disk sweep of root and replaces the cached state.
func (g *KnowledgeGraph) Scan(root string) WorkspaceState {
	if root == "" {
		root = "."
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scanLocked(root)
	return g.state
}

// Refresh re-scans the currently bound root. It is a no-op when the graph has
// never been scanned.
func (g *KnowledgeGraph) Refresh() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.scanned {
		return
	}
	g.scanLocked(g.root)
}

// scanLocked walks root and rebuilds state and the symbol table. Callers hold
// the write lock.
func (g *KnowledgeGraph) scanLocked(root string) {
	state := WorkspaceState{
		Root:       root,
		AppTypes:   make(map[string]bool),
		Archetypes: make(map[string]bool),
		Frameworks: make(map[string]bool),
		Files:      make(map[string]FileRecord),
	}
	table := NewSymbolTable()

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		state.Empty = true
		g.root = root
		g.scanned = true
		g.state = state
		g.table = table
		return
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries (permission errors, files deleted mid-scan)
			// are skipped so the sweep continues over the rest of the tree.
			return nil //nolint:nilerr // best-effort scan, keep walking
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && skippedDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		state.FileCount++
		g.scanFile(path, root, name, &state, table)
		return nil
	})
	state.Empty = state.FileCount == 0

	g.root = root
	g.scanned = true
	g.state = state
	g.table = table
}

// scanFile folds one file's markers, framework tags and symbols into the
// state and the symbol table.
func (g *KnowledgeGraph) scanFile(path, root, name string, state *WorkspaceState, table *SymbolTable) {
	lower := strings.ToLower(name)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}

	// Application-level target type markers are file-name driven first.
	switch {
	case strings.Contains(lower, "todo"):
		g.mark(state, "todo_app", "file %s matches todo marker", name)
	case strings.Contains(lower, "portfolio"):
		g.mark(state, "portfolio", "file %s matches portfolio marker", name)
	}

	// Technical archetype markers.
	switch {
	case lower == "go.mod":
		g.markArchetype(state, "go", "go.mod present")
	case lower == "package.json":
		if hasDependency(path, "react") {
			g.markArchetype(state, "react", "package.json declares react")
		}
		if hasDependency(path, "next") {
			g.markArchetype(state, "nextjs", "package.json declares next")
		}
		if hasDependency(path, "vue") {
			g.markArchetype(state, "vue", "package.json declares vue")
		}
		if hasDependency(path, "svelte") {
			g.markArchetype(state, "svelte", "package.json declares svelte")
		}
	case strings.HasSuffix(lower, ".html"):
		g.markArchetype(state, "vanilla_web", "html file present")
	case strings.HasSuffix(lower, ".go"):
		g.markArchetype(state, "go", "go source file present")
	case strings.HasSuffix(lower, ".py"):
		g.markArchetype(state, "python", "python source file present")
	case strings.HasSuffix(lower, ".rs"):
		g.markArchetype(state, "rust", "rust source file present")
	}

	// Content markers and symbol extraction only run for small text files.
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		return
	}
	if info.Size() > contentCap {
		return
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return
	}
	content := strings.ToLower(string(data))
	if g.hasAny(content, []string{"todo app", "task list", "tasklist", "addtask", "newtodo", "todolist", "let todos", "todos =", "to-do list"}) {
		g.mark(state, "todo_app", "content of %s matches todo marker", name)
	}
	if strings.Contains(content, "portfolio") {
		g.mark(state, "portfolio", "content of %s matches portfolio marker", name)
	}

	lang := languageFor(name)
	var symbols []Symbol
	if lang != "" && info.Size() > 0 {
		symbols = ExtractSymbols(lang, rel, string(data))
		table.AddMany(symbols)
	}
	state.Files[rel] = FileRecord{
		Path:      rel,
		Language:  lang,
		Bytes:     info.Size(),
		LineCount: lineCount(data),
		Symbols:   symbols,
	}
}

// mark records an application-level target type marker.
func (g *KnowledgeGraph) mark(state *WorkspaceState, appType, format string, args ...any) {
	if state.AppTypes[appType] {
		return
	}
	state.AppTypes[appType] = true
	state.Markers = append(state.Markers, fmt.Sprintf(format, args...))
}

// markArchetype records a technical archetype marker and folds the matching
// framework tag when the archetype is framework-shaped.
func (g *KnowledgeGraph) markArchetype(state *WorkspaceState, arch, detail string) {
	if state.Archetypes[arch] {
		return
	}
	state.Archetypes[arch] = true
	state.Markers = append(state.Markers, detail)
	switch arch {
	case "react", "nextjs", "vue", "svelte":
		state.Frameworks[arch] = true
	}
}

// hasAny reports whether content contains any of the needles.
func (g *KnowledgeGraph) hasAny(content string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(content, n) {
			return true
		}
	}
	return false
}

// hasDependency reports whether the JSON file at path declares one of the
// given dependency names. It is best-effort and never fails on malformed input.
func hasDependency(path string, names ...string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	for _, n := range names {
		if strings.Contains(lower, `"`+n+`"`) {
			return true
		}
	}
	return false
}

// lineCount returns the number of newline-separated lines in data.
func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := 1
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	return count
}

// --- read accessors (no disk I/O) -------------------------------------------

// Snapshot returns a defensive copy of the current WorkspaceState.
func (g *KnowledgeGraph) Snapshot() WorkspaceState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := g.state
	out.AppTypes = copyBoolMap(g.state.AppTypes)
	out.Archetypes = copyBoolMap(g.state.Archetypes)
	out.Frameworks = copyBoolMap(g.state.Frameworks)
	out.Markers = append([]string(nil), g.state.Markers...)
	out.Files = make(map[string]FileRecord, len(g.state.Files))
	for k, v := range g.state.Files {
		rec := v
		rec.Symbols = append([]Symbol(nil), v.Symbols...)
		out.Files[k] = rec
	}
	return out
}

// Root returns the currently bound workspace root.
func (g *KnowledgeGraph) Root() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.root
}

// Scanned reports whether the graph has been populated for its root.
func (g *KnowledgeGraph) Scanned() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.scanned
}

// HasFile reports whether rel names a scanned workspace file.
func (g *KnowledgeGraph) HasFile(rel string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.state.Files[rel]
	return ok
}

// File returns the record for rel, or the zero value when absent.
func (g *KnowledgeGraph) File(rel string) (FileRecord, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rec, ok := g.state.Files[rel]
	if ok {
		rec.Symbols = append([]Symbol(nil), rec.Symbols...)
	}
	return rec, ok
}

// Files returns every scanned file record in stable path order.
func (g *KnowledgeGraph) Files() []FileRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()
	paths := make([]string, 0, len(g.state.Files))
	for p := range g.state.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]FileRecord, 0, len(paths))
	for _, p := range paths {
		rec := g.state.Files[p]
		rec.Symbols = append([]Symbol(nil), rec.Symbols...)
		out = append(out, rec)
	}
	return out
}

// Archetypes returns the detected technical archetypes in stable sorted order.
func (g *KnowledgeGraph) Archetypes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.state.Archetypes)
}

// FrameworkTags returns the detected framework tags in stable sorted order.
func (g *KnowledgeGraph) FrameworkTags() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.state.Frameworks)
}

// AppTypes returns the detected application-level target types in stable
// sorted order.
func (g *KnowledgeGraph) AppTypes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedKeys(g.state.AppTypes)
}

// LookupSymbol resolves a symbol name against the in-memory table, returning
// every declaration site. It never touches the disk.
func (g *KnowledgeGraph) LookupSymbol(name string) []Symbol {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.table.Lookup(name)
}

// SymbolCount returns the number of indexed symbols.
func (g *KnowledgeGraph) SymbolCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.table.Count()
}

// Summaries returns the compact per-file digests in stable path order.
func (g *KnowledgeGraph) Summaries() []FileSummary {
	records := g.Files()
	out := make([]FileSummary, 0, len(records))
	for _, r := range records {
		out = append(out, FileSummary{
			Path:       r.Path,
			Language:   r.Language,
			Bytes:      r.Bytes,
			LineCount:  r.LineCount,
			Symbols:    append([]Symbol(nil), r.Symbols...),
			Archetypes: append([]string(nil), g.archetypesFor(r.Language)...),
		})
	}
	return out
}

// archetypesFor maps a scanned language onto its dominant technical marker so
// a summary always carries a readable tag even when the archetype marker was
// file-name driven.
func (g *KnowledgeGraph) archetypesFor(lang string) []string {
	switch lang {
	case "go":
		return []string{"go"}
	case "ts", "js", "jsx", "tsx":
		return []string{"vanilla_web"}
	case "python":
		return []string{"python"}
	case "rust":
		return []string{"rust"}
	default:
		return nil
	}
}

// sortedKeys returns the true-valued keys of set in stable sorted order.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k, on := range set {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// copyBoolMap returns a defensive copy of src.
func copyBoolMap(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return make(map[string]bool)
	}
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
