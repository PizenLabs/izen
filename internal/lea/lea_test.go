package lea

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/lea/graph"
)

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func newTestEngine(t *testing.T, root string, opts ...Option) *Engine {
	t.Helper()
	e := NewEngine(root, opts...)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func mustIndex(t *testing.T, e *Engine) IndexStats {
	t.Helper()
	stats, err := e.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return stats
}

func testRepoFiles() map[string]string {
	return map[string]string{
		"main.go": `package main

import "fmt"

func main() {
	svc := NewService()
	svc.Run()
	fmt.Println("done")
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

func deadCode() {}
`,
		"util.go": `package main

func util() {}
`,
		"server.go": `package main

import "net/http"

func registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/v1/users", listUsers)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`,
	}
}

func lookup(t *testing.T, e *Engine, name string) graph.Node {
	t.Helper()
	nodes := e.Graph().Lookup(name)
	for _, n := range nodes {
		if n.Name == name && n.Kind != graph.KindFile && n.Kind != graph.KindPackage {
			return n
		}
	}
	t.Fatalf("node %q not found in graph", name)
	return graph.Node{}
}

func collectNames(nodes []graph.Node) []string {
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

func TestEngineIndexTraceCallChain(t *testing.T) {
	root := writeRepo(t, testRepoFiles())
	e := newTestEngine(t, root)
	stats := mustIndex(t, e)
	if stats.Files != 4 {
		t.Errorf("expected 4 files indexed, got %d", stats.Files)
	}
	if stats.Nodes == 0 || stats.Edges == 0 {
		t.Errorf("expected populated graph, got %+v", stats)
	}

	// Outbound: main -> {NewService, Service.Run} -> helper -> util.
	tree := e.TraceCallChain("main", Outbound, 3)
	if tree.Node.Name != "main" {
		t.Fatalf("root should be main, got %q", tree.Node.Name)
	}
	firstLevel := make([]string, 0, len(tree.Children))
	for _, c := range tree.Children {
		firstLevel = append(firstLevel, c.Node.QualName)
	}
	if !contains(firstLevel, "Service.Run") || !contains(firstLevel, "NewService") {
		t.Errorf("main outbound first level = %v, want Service.Run + NewService", firstLevel)
	}
	// Inbound: helper <- Service.Run <- main.
	tree = e.TraceCallChain("helper", Inbound, 3)
	if tree.Node.Name != "helper" {
		t.Fatalf("root should be helper, got %q", tree.Node.Name)
	}
	found := false
	for _, c := range tree.Children {
		if c.Node.QualName == "Service.Run" {
			found = true
			for _, g := range c.Children {
				if g.Node.QualName == "main" {
					return // exact chain helper <- Service.Run <- main
				}
			}
		}
	}
	if !found {
		t.Errorf("inbound chain incomplete: %+v", tree)
	}
}

func TestEngineIncrementalRefreshAccuracy(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a.go": `package p

func A() { B() }

func B() {}
`,
		"b.go": `package p

func B2() {}
`,
	})
	e := newTestEngine(t, root)
	mustIndex(t, e)

	b := lookup(t, e, "B")
	if callers := collectNames(e.Graph().Callers(b.ID)); !contains(callers, "A") {
		t.Fatalf("expected A->B edge, got %v", callers)
	}

	// A stops calling B, starts calling C (declared in the same file).
	changed := "a.go"
	newContent := `package p

func A() { C() }

func B() {}

func C() {}
`
	if err := os.WriteFile(filepath.Join(root, changed), []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := e.Refresh(context.Background(), []string{changed})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !stats.Incremental {
		t.Error("refresh should report incremental=true")
	}

	// Stale edge removed.
	if callers := collectNames(e.Graph().Callers(lookup(t, e, "B").ID)); contains(callers, "A") {
		t.Error("stale A->B edge survived refresh")
	}
	// New edge added.
	c := lookup(t, e, "C")
	if callers := collectNames(e.Graph().Callers(c.ID)); !contains(callers, "A") {
		t.Errorf("new A->C edge missing, got %v", callers)
	}
}

func TestEngineRefreshRemovesDeletedFile(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a.go": `package p

func A() { B() }
`,
		"b.go": `package p

func B() {}
`,
	})
	e := newTestEngine(t, root)
	mustIndex(t, e)

	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Refresh(context.Background(), []string{"b.go"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := e.Graph().File("b.go"); ok {
		t.Error("file b.go should be removed from graph")
	}
	a := lookup(t, e, "A")
	if callees := collectNames(e.Graph().Callees(a.ID)); contains(callees, "B") {
		t.Error("A outbound should not contain removed B")
	}
}

func TestEngineFindDeadCode(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"main.go": `package main

func main() {
	used()
}

func used() {}

func unused() {}

func ExportedAPI() {}
`,
		"iface.go": `package main

type Runner interface {
	run()
}

func (r Runner) run() {}
`,
	})
	e := newTestEngine(t, root)
	mustIndex(t, e)

	dead := e.FindDeadCode()
	names := make([]string, 0, len(dead))
	for _, d := range dead {
		names = append(names, d.Name)
	}
	if !contains(names, "unused") {
		t.Errorf("expected unused() in dead code, got %v", names)
	}
	if contains(names, "used") {
		t.Errorf("used() must not be dead code: %v", names)
	}
	if contains(names, "main") {
		t.Errorf("main() entry point must not be dead code: %v", names)
	}
	if contains(names, "ExportedAPI") {
		t.Errorf("exported API must not be dead code: %v", names)
	}
	if contains(names, "run") {
		t.Errorf("interface method must not be dead code: %v", names)
	}
}

func TestEngineFindRoutes(t *testing.T) {
	root := writeRepo(t, testRepoFiles())
	e := newTestEngine(t, root)
	mustIndex(t, e)

	routes := e.FindRoutes()
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	for _, r := range routes {
		if r.Path == "/health" && (r.Handler != "healthHandler" || r.Method != "ANY") {
			t.Errorf("bad /health route: %+v", r)
		}
		if r.Path == "/api/v1/users" && (r.Handler != "listUsers" || r.HandlerFile != "server.go") {
			t.Errorf("bad /api/v1/users route: %+v", r)
		}
	}
}

func TestEngineArchSummary(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"cmd/app/main.go": `package main

func main() { run() }

func run() {}
`,
		"internal/lea/engine.go": `package lea

import "fmt"

func NewEngine() {}

func Index() {}
`,
		"internal/retrieval/retriever.go": `package retrieval

import "github.com/PizenLabs/izen/internal/lea"

func Build() {}
`,
		"internal/lea/server.go": `package lea

import "net/http"

func Setup() { http.HandleFunc("/ping", ping) }

func ping(w http.ResponseWriter, r *http.Request) {}
`,
		"internal/ui/screen.go": `package ui

import "github.com/PizenLabs/izen/internal/lea"

func Render() { lea.NewEngine() }
`,
	})
	e := newTestEngine(t, root)
	mustIndex(t, e)

	sum := e.GetArchitectureSummary()
	if len(sum.Packages) == 0 {
		t.Fatal("expected packages in summary")
	}
	// Entry point main() in cmd/.
	entryFound := false
	for _, ep := range sum.EntryPoints {
		if ep.Name == "main" && strings.HasPrefix(ep.Package, "cmd") {
			entryFound = true
		}
	}
	if !entryFound {
		t.Errorf("missing main entry point, got %+v", sum.EntryPoints)
	}
	if len(sum.HTTPRoutes) != 1 {
		t.Errorf("expected 1 route, got %d", len(sum.HTTPRoutes))
	}
	// Cross-layer dependency: internal/ui (interface) imports internal/lea.
	foundDep := false
	for _, d := range sum.LayerDirection {
		if d.From == "interface" && d.To == "internal" {
			foundDep = true
		}
	}
	if !foundDep {
		t.Errorf("expected interface->internal layer direction, got %+v", sum.LayerDirection)
	}
	pkgDirIndex := make(map[string]PackageInfo)
	for _, p := range sum.Packages {
		pkgDirIndex[p.Dir] = p
	}
	leaPkg, ok := pkgDirIndex["internal/lea"]
	if !ok || leaPkg.FileCount != 2 {
		t.Errorf("internal/lea package missing or wrong file count: %+v", leaPkg)
	}
	// retrieval must depend on lea.
	if retr, ok := pkgDirIndex["internal/retrieval"]; ok && !contains(retr.DependsOn, "internal/lea") {
		t.Errorf("retrieval deps missing internal/lea, got %v", retr.DependsOn)
	}
}

func TestEngineStoreRoundTrip(t *testing.T) {
	root := writeRepo(t, testRepoFiles())
	e := newTestEngine(t, root)
	mustIndex(t, e)

	snapBefore := e.Graph().Snapshot()
	if !fileExists(filepath.Join(root, ".izen", "graph.bin.zst")) {
		t.Fatal("store file not written after Index")
	}

	// Reload into a fresh engine.
	e2 := newTestEngine(t, root)
	start := time.Now()
	stats, err := e2.Index(context.Background())
	if err != nil {
		t.Fatalf("Index from cache: %v", err)
	}
	loadDur := time.Since(start)
	if !stats.FromCache {
		t.Error("expected Index to load from cache")
	}

	snapAfter := e2.Graph().Snapshot()
	if len(snapAfter.Nodes) != len(snapBefore.Nodes) {
		t.Errorf("node count mismatch after reload: %d != %d", len(snapAfter.Nodes), len(snapBefore.Nodes))
	}
	if len(snapAfter.Edges) != len(snapBefore.Edges) {
		t.Errorf("edge count mismatch after reload: %d != %d", len(snapAfter.Edges), len(snapBefore.Edges))
	}
	t.Logf("cache load took %v", loadDur)
}

func TestEngineGitSync(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	_ = gitBin
	root := writeRepo(t, map[string]string{
		"a.go": `package p

func A() {}
`,
	})
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init")

	e := newTestEngine(t, root)
	mustIndex(t, e)

	// Modify a tracked file and add an untracked file while "offline".
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(`package p

func A() { B() }

func B() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte(`package p

func New() { B() }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := e.SyncFromGit(context.Background())
	if err != nil {
		t.Fatalf("SyncFromGit: %v", err)
	}
	if !contains(changed, "a.go") || !contains(changed, "new.go") {
		t.Errorf("git sync should report a.go and new.go, got %v", changed)
	}

	if _, err := e.Refresh(context.Background(), changed); err != nil {
		t.Fatalf("Refresh after git sync: %v", err)
	}
	b := lookup(t, e, "B")
	if callers := collectNames(e.Graph().Callers(b.ID)); !contains(callers, "A") {
		t.Errorf("A->B edge missing after git sync, got %v", callers)
	}
	if _, ok := e.Graph().File("new.go"); !ok {
		t.Error("untracked new.go should be indexed")
	}
}

func TestEngineWatcher(t *testing.T) {
	root := writeRepo(t, map[string]string{})
	e := newTestEngine(t, root, WithAutoSync(true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Create a file after watching begins; the watcher should index it.
	newFile := filepath.Join(root, "w.go")
	if err := os.WriteFile(newFile, []byte("package w\n\nfunc Watched() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := e.Graph().File("w.go"); ok {
			nodes := e.Graph().Lookup("Watched")
			if len(nodes) > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("watcher did not index created file within 5s")
}

func TestEngineIndexPerformance20k(t *testing.T) {
	if raceEnabled || testing.CoverMode() != "" {
		// Race and coverage instrumentation inflate every timing measurement
		// by 5-20x. Performance budgets are verified on an uninstrumented run:
		//   go test -run TestEngineIndexPerformance20k ./internal/lea
		t.Skip("perf budgets require an uninstrumented run (no -race, no -cover)")
	}
	root := t.TempDir()
	const files = 100
	const funcsPerFile = 50
	// Each function is ~4 lines (realistic), giving ~20k LOC across 100 files
	// and ~5k functions with ~10k call sites.
	for i := 0; i < files; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "package bench\n\n")
		for j := 0; j < funcsPerFile; j++ {
			next := fmt.Sprintf("F%03d_%03d", i, (j+1)%funcsPerFile)
			fmt.Fprintf(&b, "func F%03d_%03d() {\n\tx := %s()\n\t_ = x\n}\n\n", i, j, next)
		}
		path := filepath.Join(root, fmt.Sprintf("bench%03d.go", i))
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	e := newTestEngine(t, root)
	start := time.Now()
	stats := mustIndex(t, e)
	fullDur := time.Since(start)
	t.Logf("full index of 20k LOC: %v (files=%d nodes=%d edges=%d)", fullDur, stats.Files, stats.Nodes, stats.Edges)
	if stats.Files != files {
		t.Errorf("expected %d files, got %d", files, stats.Files)
	}
	if fullDur >= time.Second {
		t.Errorf("full index must complete in <1s, took %v", fullDur)
	}

	// Incremental refresh of a single file must be <100ms.
	one := filepath.Join(root, "bench000.go")
	if err := os.WriteFile(one, []byte("package bench\n\nfunc F000_000() {\n\tx := F000_001()\n\t_ = x\n}\n\nfunc F000_049() {\n\tx := F000_000()\n\t_ = x\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err := e.Refresh(context.Background(), []string{"bench000.go"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	refreshDur := time.Since(start)
	t.Logf("incremental refresh of 1 file: %v", refreshDur)
	if refreshDur >= 100*time.Millisecond {
		t.Errorf("incremental refresh must complete in <100ms, took %v", refreshDur)
	}

	// Cache load must be <10ms.
	e2 := newTestEngine(t, root)
	start = time.Now()
	stats2, err := e2.Index(context.Background())
	if err != nil {
		t.Fatalf("cached Index: %v", err)
	}
	loadDur := time.Since(start)
	t.Logf("cache load: %v", loadDur)
	if !stats2.FromCache {
		t.Error("expected cache load")
	}
	if loadDur >= 10*time.Millisecond {
		t.Errorf("cache load must complete in <10ms, took %v", loadDur)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
