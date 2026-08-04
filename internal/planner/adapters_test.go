package planner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// testAdapterRepo writes a small Go repo with a call chain and HTTP routes so
// the LeaAdapter's structural queries can be verified against a real engine.
func testAdapterRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
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

func newAdapterEngine(t *testing.T) *lea.Engine {
	t.Helper()
	root := testAdapterRepo(t)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	return e
}

// TestLeaAdapterCallChain verifies the CallChain adapter surfaces the inbound
// call tree reconstructed by the Lea engine (Task C2).
func TestLeaAdapterCallChain(t *testing.T) {
	e := newAdapterEngine(t)
	a := NewLeaAdapter(e)

	tree, err := a.CallChain(context.Background(), "util", 2)
	if err != nil {
		t.Fatalf("CallChain: %v", err)
	}
	if tree == "" {
		t.Fatal("CallChain returned empty tree for indexed symbol")
	}
	// util is called by Service.helper, which is called by Service.Run.
	if !strings.Contains(tree, "helper") {
		t.Errorf("call chain missing inbound caller helper:\n%s", tree)
	}
	if !strings.Contains(tree, "Run") {
		t.Errorf("call chain missing transitive caller Run:\n%s", tree)
	}

	// Unknown symbol degrades to empty, never an error.
	if tree, err := a.CallChain(context.Background(), "doesNotExist", 2); err != nil || tree != "" {
		t.Errorf("unknown symbol: tree=%q err=%v, want empty+nil", tree, err)
	}
}

// TestLeaAdapterRoutes verifies the Routes adapter maps HTTP paths to handlers
// from the Lea graph (Task C2).
func TestLeaAdapterRoutes(t *testing.T) {
	e := newAdapterEngine(t)
	a := NewLeaAdapter(e)

	routes, err := a.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if routes == "" {
		t.Fatal("Routes returned empty route map for indexed repo")
	}
	if !strings.Contains(routes, "/health") {
		t.Errorf("route map missing /health:\n%s", routes)
	}
	if !strings.Contains(routes, "/api/v1/users") {
		t.Errorf("route map missing /api/v1/users:\n%s", routes)
	}
	if !strings.Contains(routes, "listUsers") {
		t.Errorf("route map missing listUsers handler:\n%s", routes)
	}
}

// TestLeaAdapterArchitectureSummary verifies the architecture summary and
// symbol resolution against the Lea graph.
func TestLeaAdapterArchitectureSummary(t *testing.T) {
	e := newAdapterEngine(t)
	a := NewLeaAdapter(e)

	summary, err := a.ArchitectureSummary(context.Background())
	if err != nil {
		t.Fatalf("ArchitectureSummary: %v", err)
	}
	if summary == "" {
		t.Fatal("ArchitectureSummary returned empty for indexed repo")
	}
	if !strings.Contains(summary, "Stats:") {
		t.Errorf("summary missing stats line:\n%s", summary)
	}

	refs, err := a.ResolveSymbol(context.Background(), "NewService")
	if err != nil {
		t.Fatalf("ResolveSymbol: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("ResolveSymbol found no definitions for NewService")
	}
	if refs[0].Name != "NewService" {
		t.Errorf("resolved symbol = %s, want NewService", refs[0].Name)
	}
}
