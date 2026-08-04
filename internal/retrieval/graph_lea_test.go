package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// leaTestRepo writes a small Go repo with two packages so the lea-backed
// graph lookups can be exercised: a shared symbol, a file, a package, an
// import, and a dependent. The importing file lives in a directory that sorts
// AFTER the imported package (the lea graph resolves import edges during the
// sorted file walk: "internal/aapi" < "internal/core").
func leaTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/aapi/service.go": `package aapi

type Service struct{}

func (s *Service) Run() {}

func NewService() *Service { return &Service{} }
`,
		"internal/core/main.go": `package core

import "github.com/acme/learepo/internal/aapi"

func Build() *aapi.Service {
	return aapi.NewService()
}
`,
		"util.go": `package util

func util() {}
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

func newLeaLookup(t *testing.T) *GraphLookup {
	t.Helper()
	root := leaTestRepo(t)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatalf("lea Index: %v", err)
	}
	return NewLeaGraphLookup(e, root)
}

// TestLeaGraphLookupSymbol verifies symbol resolution is served from the Lea
// structural engine (Task A redirect).
func TestLeaGraphLookupSymbol(t *testing.T) {
	gl := newLeaLookup(t)
	if !gl.HasGraph() {
		t.Fatal("HasGraph false for indexed lea engine")
	}

	rs := gl.SearchSymbol("NewService")
	if rs.Empty() {
		t.Fatal("expected to resolve NewService from the lea graph")
	}
	if rs.Results[0].SymbolName != "NewService" {
		t.Errorf("symbol = %s, want NewService", rs.Results[0].SymbolName)
	}
	if rs.Confidence < 0.9 {
		t.Errorf("confidence = %f, want >= 0.9", rs.Confidence)
	}
}

// TestLeaGraphLookupFileAndPackage verifies file and package discovery.
func TestLeaGraphLookupFileAndPackage(t *testing.T) {
	gl := newLeaLookup(t)

	frs := gl.SearchFile("internal/aapi/service.go")
	if frs.Empty() {
		t.Fatal("expected to find internal/aapi/service.go")
	}

	prs := gl.SearchPackage("aapi")
	if prs.Empty() {
		t.Fatal("expected files in package aapi")
	}
	found := false
	for _, r := range prs.Results {
		if r.File == "internal/aapi/service.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("package aapi lookup missed service.go: %+v", prs.Results)
	}
}

// TestLeaGraphLookupImportsAndDependents verifies import edges and the reverse
// dependent map are computed from the lea graph.
func TestLeaGraphLookupImportsAndDependents(t *testing.T) {
	gl := newLeaLookup(t)

	irs := gl.SearchImports("internal/aapi")
	if irs.Empty() {
		t.Fatal("expected an import edge matching internal/aapi")
	}
	importedBy := ""
	for _, r := range irs.Results {
		if r.File == "internal/core/main.go" {
			importedBy = r.File
			break
		}
	}
	if importedBy == "" {
		t.Fatalf("import lookup missed internal/core/main.go: %+v", irs.Results)
	}

	drs := gl.SearchDependents("internal/aapi/service.go")
	if drs.Empty() {
		t.Fatal("expected dependents of internal/aapi/service.go")
	}
	found := false
	for _, r := range drs.Results {
		if r.File == "internal/core/main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("dependents missed internal/core/main.go: %+v", drs.Results)
	}
}

// TestLeaGraphLookupListFiles verifies glob listing over the lea file set.
func TestLeaGraphLookupListFiles(t *testing.T) {
	gl := newLeaLookup(t)

	rs := gl.ListFiles("internal/aapi/*.go")
	if rs.Empty() {
		t.Fatal("expected glob match for internal/aapi/*.go")
	}
	if rs.Results[0].File != "internal/aapi/service.go" {
		t.Errorf("glob result = %s, want internal/aapi/service.go", rs.Results[0].File)
	}
}

// TestLeaGraphLookupSearchAll verifies the combined search over lea data.
func TestLeaGraphLookupSearchAll(t *testing.T) {
	gl := newLeaLookup(t)

	rs := gl.SearchAll("NewService")
	if rs.Empty() {
		t.Fatal("SearchAll returned empty for NewService")
	}
}
