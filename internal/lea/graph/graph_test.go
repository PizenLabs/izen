package graph

import (
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
)

// goFixture parses a Go source string with the real go/ast extractor.
func goFixture(t *testing.T, path, content string) symbol.FileASTInfo {
	t.Helper()
	ex := extractors.NewGoExtractor()
	info, err := ex.ExtractSymbols(path, []byte(content))
	if err != nil {
		t.Fatalf("extract %s: %v", path, err)
	}
	return *info
}

func buildFixture(t *testing.T, files map[string]string) *Graph {
	t.Helper()
	g := NewGraph("repo")
	infos := make([]symbol.FileASTInfo, 0, len(files))
	for path, content := range files {
		infos = append(infos, goFixture(t, path, content))
	}
	if err := g.Build(infos); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g
}

func callGraphFixture(t *testing.T) *Graph {
	return buildFixture(t, map[string]string{
		"main.go": `package main

import "fmt"

func main() {
	svc := NewService()
	svc.Run()
	fmt.Println("ok")
}

func NewService() *Service {
	return &Service{}
}
`,
		"service.go": `package main

type Service struct{}

func (s *Service) Run() {
	s.helper()
}

func (s *Service) helper() {
	util()
}
`,
		"util.go": `package main

func util() {}
`,
	})
}

func findNode(t *testing.T, g *Graph, name string) Node {
	t.Helper()
	nodes := g.Lookup(name)
	for _, n := range nodes {
		if n.Name == name && n.Kind != KindFile && n.Kind != KindPackage {
			return n
		}
	}
	t.Fatalf("node %q not found", name)
	return Node{}
}

func TestBuildCallGraph(t *testing.T) {
	g := callGraphFixture(t)
	stats := g.Stats()
	if stats.NodeCount == 0 || stats.CallEdgeCount == 0 {
		t.Fatalf("expected populated graph, got %+v", stats)
	}

	main := findNode(t, g, "main")
	callees := nodeNames(g.Callees(main.ID))
	want := []string{"NewService", "Service.Run"}
	for _, w := range want {
		if !contains(callees, w) {
			t.Errorf("main outbound missing %q, got %v", w, callees)
		}
	}

	helper := findNode(t, g, "helper")
	if callers := nodeNames(g.Callers(helper.ID)); !contains(callers, "Service.Run") {
		t.Errorf("helper callers missing Service.Run, got %v", callers)
	}

	util := findNode(t, g, "util")
	if callers := nodeNames(g.Callers(util.ID)); !contains(callers, "Service.helper") {
		t.Errorf("util callers missing Service.helper, got %v", callers)
	}

	// main must call util transitively; direct edge must not exist.
	if direct := g.Callees(main.ID); contains(nodeNames(direct), "util") {
		t.Error("main should not call util directly")
	}
}

func TestBuildDefinesAndImportsEdges(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"internal/lea/engine.go": `package lea

import "fmt"

func NewEngine() {}
`,
		"internal/retrieval/retriever.go": `package retrieval

import "github.com/PizenLabs/izen/internal/lea"

func Build() { lea.NewEngine() }
`,
	})

	// Package and file nodes.
	pkg, ok := g.Node("package:internal/lea")
	if !ok {
		t.Fatal("missing package:internal/lea node")
	}
	file, ok := g.File("internal/lea/engine.go")
	if !ok {
		t.Fatal("missing file node")
	}
	// DEFINES: package -> file, file -> function.
	defineTargets := g.Outgoing(pkg.ID)
	found := false
	for _, e := range defineTargets {
		if e.Kind == EdgeDefines && e.To == file.ID {
			found = true
		}
	}
	if !found {
		t.Error("missing DEFINES edge package->file")
	}

	// IMPORTS: retriever file imports internal/lea package.
	rf := findNode(t, g, "Build")
	imports := g.Incoming(pkg.ID)
	importedByFile := false
	for _, e := range imports {
		if e.Kind == EdgeImports && e.From == rf.ID {
			importedByFile = false
		}
	}
	_ = importedByFile

	// The import edge originates from the file node of retriever.go.
	retrieverFile, ok := g.File("internal/retrieval/retriever.go")
	if !ok {
		t.Fatal("missing retriever file node")
	}
	impEdge := false
	for _, e := range g.Outgoing(retrieverFile.ID) {
		if e.Kind == EdgeImports && e.To == pkg.ID {
			impEdge = true
		}
	}
	if !impEdge {
		t.Error("missing IMPORTS edge retriever.go -> internal/lea")
	}

	if deps := g.PackageDeps("internal/retrieval"); !contains(deps, "internal/lea") {
		t.Errorf("retrieval deps missing internal/lea, got %v", deps)
	}
}

func TestImplementsEdge(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"types.go": `package app

type Runner interface {
	Run() error
}

type Task struct{}

func (t *Task) Run() error { return nil }
`,
	})
	task := findNode(t, g, "Task")
	runner := findNode(t, g, "Runner")
	implemented := false
	for _, e := range g.Outgoing(task.ID) {
		if e.Kind == EdgeImplements && e.To == runner.ID {
			implemented = true
		}
	}
	if !implemented {
		t.Error("expected IMPLEMENTS edge Task -> Runner")
	}
}

func TestHTTPRouteEdges(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"server.go": `package main

import "net/http"

func main() {
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/v1/users", listUsers)
	_ = http.ListenAndServe(":8080", nil)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`,
	})
	routes := g.NodesByKind(KindHTTPRoute)
	if len(routes) != 2 {
		t.Fatalf("expected 2 route nodes, got %d", len(routes))
	}
	for _, r := range routes {
		handled := false
		for _, e := range g.Outgoing(r.ID) {
			if e.Kind == EdgeHTTPHandles {
				handled = true
			}
		}
		if !handled {
			t.Errorf("route %s has no HTTP_HANDLES edge", r.Name)
		}
	}
}

func TestIncrementalUpsertAccuracy(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"a.go": `package p

func A() { B() }

func B() {}
`,
	})
	b := findNode(t, g, "B")
	if callers := nodeNames(g.Callers(b.ID)); !contains(callers, "A") {
		t.Fatalf("expected A->B edge, got callers=%v", callers)
	}

	// A stops calling B and starts calling C.
	changed := goFixture(t, "a.go", `package p

func A() { C() }

func B() {}

func C() {}
`)
	if err := g.UpsertFile(changed); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	callers := nodeNames(g.Callers(findNode(t, g, "B").ID))
	if contains(callers, "A") {
		t.Error("stale A->B edge survived incremental update")
	}
	c := findNode(t, g, "C")
	if callers := nodeNames(g.Callers(c.ID)); !contains(callers, "A") {
		t.Errorf("new A->C edge missing, got %v", callers)
	}
	// B still exists as a node (only its incoming edge was removed).
	if _, ok := g.Node(findNode(t, g, "B").ID); !ok {
		t.Error("B node should still exist")
	}
}

func TestIncrementalMetadataUpdate(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"a.go": `package p

func A() {}
`,
	})
	a := findNode(t, g, "A")
	oldLine := a.Line

	changed := goFixture(t, "a.go", `package p

// comment shifts lines

func A() {}
`)
	if err := g.UpsertFile(changed); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	a2 := findNode(t, g, "A")
	if a2.Line <= oldLine {
		t.Errorf("expected line metadata updated, got %d (was %d)", a2.Line, oldLine)
	}
	if a2.ID != a.ID {
		t.Error("node ID should be stable across edits")
	}
}

func TestRemoveFile(t *testing.T) {
	g := buildFixture(t, map[string]string{
		"a.go": `package p

func A() { B() }
`,
		"b.go": `package p

func B() { C() }
`,
		"c.go": `package p

func C() {}
`,
	})
	a := findNode(t, g, "A")
	b := findNode(t, g, "B")
	c := findNode(t, g, "C")

	g.RemoveFile("b.go")
	if _, ok := g.File("b.go"); ok {
		t.Error("file node b.go should be removed")
	}
	if _, ok := g.Node(b.ID); ok {
		t.Error("B node should be removed with b.go")
	}
	callers := nodeNames(g.Callers(b.ID))
	if contains(callers, "A") {
		t.Error("A->B edge should be removed with b.go")
	}
	callees := nodeNames(g.Callees(b.ID))
	if contains(callees, "C") {
		t.Error("B->C edge should be removed with b.go")
	}
	// C is now unreachable but must still exist.
	if _, ok := g.Node(c.ID); !ok {
		t.Error("C node should still exist after b.go removal")
	}
	if callees := nodeNames(g.Callees(a.ID)); contains(callees, "B") {
		t.Error("A outbound should no longer include B")
	}
}

func TestSnapshotRestore(t *testing.T) {
	g := callGraphFixture(t)
	snap := g.Snapshot()
	if len(snap.Nodes) == 0 || len(snap.Edges) == 0 {
		t.Fatal("empty snapshot")
	}

	g2 := NewGraph("other")
	g2.Restore(snap)
	if g2.Stats() != g.Stats() {
		t.Errorf("stats mismatch after restore: %+v != %+v", g2.Stats(), g.Stats())
	}
	main := findNode(t, g2, "main")
	callees := nodeNames(g2.Callees(main.ID))
	if !contains(callees, "NewService") {
		t.Errorf("restored graph lost CALLS edges: %v", callees)
	}
}

func nodeNames(nodes []Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.QualName)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, v := range hay {
		if v == needle {
			return true
		}
	}
	return false
}
