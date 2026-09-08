package knowledge

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// todoWorkspace materialises a recognisable To-Do App workspace carrying both
// filename and content markers plus a Go backend so symbol extraction runs.
func todoWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), `<!DOCTYPE html>
<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script src="todo.js"></script>
</body></html>`)
	writeFile(t, filepath.Join(root, "todo.js"), `let todos = [];
function addTask() { todos.push(document.getElementById("newTodo").value); render(); }
function render() { document.getElementById("taskList").textContent = todos.join(","); }`)
	writeFile(t, filepath.Join(root, "server.go"), `package main

var port = 8080

type todo struct {
	ID   int
	Text string
}

func (t todo) render() string { return t.Text }

func main() {
	_ = port
}`)
	return root
}

// portfolioWorkspace materialises a recognisable Portfolio workspace.
func portfolioWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), `<!DOCTYPE html>
<html><head><title>My Portfolio</title></head><body>
<main><section id="about"><h1>About me</h1></section></main>
</body></html>`)
	writeFile(t, filepath.Join(root, "styles.css"), "body { font-family: sans-serif; }")
	return root
}

func TestScanTodoWorkspace(t *testing.T) {
	g := NewKnowledgeGraph()
	state := g.Scan(todoWorkspace(t))

	if state.Empty {
		t.Error("todo workspace reported empty")
	}
	if !state.AppTypes["todo_app"] {
		t.Errorf("AppTypes = %v, want todo_app", state.AppTypes)
	}
	if !state.Archetypes["vanilla_web"] {
		t.Errorf("Archetypes = %v, want vanilla_web", state.Archetypes)
	}
	if !state.Archetypes["go"] {
		t.Errorf("Archetypes = %v, want go (server.go)", state.Archetypes)
	}
	if len(state.Markers) == 0 {
		t.Error("expected markers")
	}
	if state.FileCount < 3 {
		t.Errorf("FileCount = %d, want >= 3", state.FileCount)
	}
}

func TestScanPortfolioWorkspace(t *testing.T) {
	g := NewKnowledgeGraph()
	state := g.Scan(portfolioWorkspace(t))
	if state.Empty {
		t.Error("portfolio workspace reported empty")
	}
	if !state.AppTypes["portfolio"] {
		t.Errorf("AppTypes = %v, want portfolio", state.AppTypes)
	}
	if !state.Archetypes["vanilla_web"] {
		t.Errorf("Archetypes = %v, want vanilla_web", state.Archetypes)
	}
}

func TestScanEmptyAndMissing(t *testing.T) {
	g := NewKnowledgeGraph()
	if state := g.Scan(t.TempDir()); !state.Empty {
		t.Errorf("empty workspace state = %+v, want Empty", state)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	g2 := NewKnowledgeGraph()
	if state := g2.Scan(missing); !state.Empty {
		t.Errorf("missing workspace must be empty, got %+v", state)
	}
}

func TestScanSkipsVendoredDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "<title>Portfolio</title>")
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "todo_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "node_modules", "todo_app", "index.html"), "<div>todo app</div>")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".git", "config"), "todo app")
	g := NewKnowledgeGraph()
	state := g.Scan(root)
	if state.AppTypes["todo_app"] {
		t.Error("vendored/git todo markers leaked into the scan")
	}
	if !state.AppTypes["portfolio"] {
		t.Error("portfolio marker lost alongside skipped dirs")
	}
}

func TestEnsureCachesAcrossQueries(t *testing.T) {
	root := todoWorkspace(t)
	g := NewKnowledgeGraph()
	first := g.Ensure(root)
	if !g.Scanned() {
		t.Error("Ensure must mark the graph scanned")
	}
	if g.Root() != root {
		t.Errorf("Root = %q, want %q", g.Root(), root)
	}

	// A second Ensure over the same root must reuse the cache: add a file and
	// verify the cached snapshot is unchanged until Refresh.
	writeFile(t, filepath.Join(root, "extra.html"), "<title>Todo App</title>")
	second := g.Ensure(root)
	if second.FileCount != first.FileCount {
		t.Errorf("Ensure re-scanned the root: FileCount %d -> %d", first.FileCount, second.FileCount)
	}
	if g.HasFile("extra.html") {
		t.Error("cached graph observed a file written after the scan")
	}

	// Refresh must pick the new file up.
	g.Refresh()
	if !g.HasFile("extra.html") {
		t.Error("Refresh did not re-scan the workspace")
	}
}

func TestHasFileAndFile(t *testing.T) {
	root := todoWorkspace(t)
	g := NewKnowledgeGraph()
	g.Scan(root)
	if !g.HasFile("server.go") {
		t.Error("server.go not indexed")
	}
	if g.HasFile("nope.go") {
		t.Error("unknown file reported present")
	}
	rec, ok := g.File("server.go")
	if !ok {
		t.Fatal("File(server.go) not found")
	}
	if rec.Language != "go" || rec.LineCount == 0 {
		t.Errorf("File(server.go) record = %+v", rec)
	}
}

func TestLookupSymbol(t *testing.T) {
	root := todoWorkspace(t)
	g := NewKnowledgeGraph()
	g.Scan(root)

	hits := g.LookupSymbol("addTask")
	if len(hits) != 1 {
		t.Fatalf("LookupSymbol(addTask) = %d hits, want 1", len(hits))
	}
	if hits[0].File != "todo.js" || hits[0].Kind != SymbolFunc {
		t.Errorf("addTask hit = %+v, want todo.js func", hits[0])
	}

	methodHits := g.LookupSymbol("render")
	if len(methodHits) < 2 {
		t.Fatalf("LookupSymbol(render) = %d hits, want 2 (js func + go method)", len(methodHits))
	}

	if hits := g.LookupSymbol("definitely_missing"); len(hits) != 0 {
		t.Errorf("LookupSymbol(missing) = %+v, want none", hits)
	}
}

func TestSymbolCount(t *testing.T) {
	g := NewKnowledgeGraph()
	g.Scan(todoWorkspace(t))
	if g.SymbolCount() == 0 {
		t.Error("expected indexed symbols")
	}
}

func TestFilesStableOrder(t *testing.T) {
	g := NewKnowledgeGraph()
	g.Scan(todoWorkspace(t))
	files := g.Files()
	for i := 1; i < len(files); i++ {
		if files[i-1].Path > files[i].Path {
			t.Errorf("Files not sorted: %q before %q", files[i-1].Path, files[i].Path)
		}
	}
}

func TestArchetypesAndFrameworks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "package.json"), `{"dependencies": {"react": "^18", "next": "^14"}}`)
	writeFile(t, filepath.Join(root, "app.tsx"), `export function App() { return null; }`)
	g := NewKnowledgeGraph()
	g.Scan(root)

	if got := g.Archetypes(); !reflect.DeepEqual(got, []string{"nextjs", "react"}) {
		t.Errorf("Archetypes = %v, want [nextjs react]", got)
	}
	if got := g.FrameworkTags(); !reflect.DeepEqual(got, []string{"nextjs", "react"}) {
		t.Errorf("FrameworkTags = %v, want [nextjs react]", got)
	}
}

func TestSummaries(t *testing.T) {
	g := NewKnowledgeGraph()
	g.Scan(todoWorkspace(t))
	byPath := map[string]FileSummary{}
	for _, s := range g.Summaries() {
		byPath[s.Path] = s
	}
	goFile, ok := byPath["server.go"]
	if !ok {
		t.Fatal("summary missing server.go")
	}
	if goFile.Language != "go" || len(goFile.Symbols) == 0 {
		t.Errorf("server.go summary = %+v, want go language with symbols", goFile)
	}
	if !reflect.DeepEqual(goFile.Archetypes, []string{"go"}) {
		t.Errorf("server.go Archetypes = %v, want [go]", goFile.Archetypes)
	}
}

func TestSnapshotIsDefensive(t *testing.T) {
	root := todoWorkspace(t)
	g := NewKnowledgeGraph()
	g.Scan(root)

	snap := g.Snapshot()
	snap.AppTypes["bogus"] = true
	snap.Markers = append(snap.Markers, "bogus")
	if _, ok := g.Snapshot().AppTypes["bogus"]; ok {
		t.Error("Snapshot mutation leaked into the graph")
	}

	rec, _ := g.File("server.go")
	rec.Symbols = append(rec.Symbols, Symbol{Name: "bogus", Kind: SymbolFunc, File: "server.go", Line: 1})
	fresh, _ := g.File("server.go")
	if len(fresh.Symbols) != len(rec.Symbols)-1 {
		t.Error("File mutation leaked into the graph")
	}
}
