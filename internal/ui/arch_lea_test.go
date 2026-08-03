package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lea"
)

// leaArchRepo writes a small Go repo with symbols, imports and a main entry
// point so the /arch analysis can be served from the Lea structural engine.
// The importing file sorts AFTER the imported package (the lea graph resolves
// import edges during the sorted file walk).
func leaArchRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"cmd/app/main.go": `package main

func main() {}
`,
		"internal/aapi/service.go": `package aapi

type Service struct{}

func (s *Service) Run() {}

func NewService() *Service { return &Service{} }
`,
		"internal/zzcore/main.go": `package zzcore

import "github.com/acme/archrepo/internal/aapi"

func Build() *aapi.Service { return aapi.NewService() }
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

func newLeaArchModel(t *testing.T) *model {
	t.Helper()
	root := leaArchRepo(t)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatalf("lea Index: %v", err)
	}
	// No native graph attached: the arch analysis must come entirely from the
	// Lea structural engine.
	return &model{leaEng: e, workspaceRoot: root}
}

// TestRenderArchFromLea verifies /arch is served from the Phase 3 Lea engine
// when the native graph is absent.
func TestRenderArchFromLea(t *testing.T) {
	m := newLeaArchModel(t)

	result := m.renderArch("")
	if result == "" {
		t.Fatal("expected non-empty architecture report from the lea engine")
	}
	if !strings.Contains(result, "ARCHITECTURE") {
		t.Errorf("report missing ARCHITECTURE section:\n%s", result)
	}
	if !strings.Contains(result, "aapi") {
		t.Errorf("report missing the aapi package (lea-sourced structure):\n%s", result)
	}

	full := m.renderArch("--all")
	if !strings.Contains(full, "cmd/app") {
		t.Errorf("full report missing cmd/app package:\n%s", full)
	}
}

// TestGraphFromLea verifies the lea→native graph converter carries files,
// symbols, package names and import edges.
func TestGraphFromLea(t *testing.T) {
	root := leaArchRepo(t)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatalf("lea Index: %v", err)
	}

	g := graphFromLea(e.Graph())
	if g == nil {
		t.Fatal("graphFromLea returned nil")
	}
	if len(g.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(g.Files))
	}
	if g.Files[0].Language != graph.LangGo {
		t.Errorf("language = %q, want go", g.Files[0].Language)
	}
	if got := g.LookupSymbol("NewService"); len(got) != 1 {
		t.Errorf("expected 1 NewService symbol, got %d", len(got))
	}
	// Import edge: internal/zzcore imports internal/aapi.
	foundImport := false
	for _, f := range g.Files {
		if f.Path == "internal/zzcore/main.go" {
			for _, imp := range f.Imports {
				if imp == "internal/aapi" {
					foundImport = true
				}
			}
		}
	}
	if !foundImport {
		t.Errorf("internal/zzcore/main.go missing import of internal/aapi: %+v", g.Files)
	}
}
