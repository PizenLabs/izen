package layer2

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

func TestSorSymbolResolution(t *testing.T) {
	sor := newTestSor(t, goFixture())

	m, ok := sor.Symbol("Service.Run")
	if !ok {
		t.Fatal("expected Service.Run to resolve")
	}
	if m.Kind != kindMethod || m.Name != "Run" {
		t.Errorf("unexpected method: %+v", m)
	}
	if m.File != "svc/service.go" {
		t.Errorf("file = %q, want svc/service.go", m.File)
	}
	if m.QualName != "Service.Run" {
		t.Errorf("qual = %q", m.QualName)
	}

	typ, ok := sor.Symbol("Service")
	if !ok || typ.Kind != kindStruct {
		t.Errorf("Service should resolve to a struct, got %+v", typ)
	}

	if _, ok := sor.Symbol("does.not.Exist"); ok {
		t.Error("unexpected resolution for missing symbol")
	}

	if _, ok := sor.LookupQual("Service.helper"); !ok {
		t.Error("LookupQual failed for method")
	}
	if _, ok := sor.LookupQual("Compute"); !ok {
		t.Error("LookupQual failed for function")
	}
}

func TestSorFileLookups(t *testing.T) {
	sor := newTestSor(t, goFixture())

	files := sor.Files()
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
	if !containsStr(files, "svc/service.go") || !containsStr(files, "cmd/app/main.go") {
		t.Errorf("unexpected file set: %v", files)
	}

	syms := sor.SymbolsOfFile("svc/service.go")
	if len(syms) < 5 {
		t.Errorf("expected at least 5 symbols, got %d", len(syms))
	}

	if pkg := sor.Package("svc/service.go"); pkg != "svc" {
		t.Errorf("pkg = %q, want svc", pkg)
	}
	if !sor.HasFile("svc/service.go") || sor.HasFile("missing.go") {
		t.Error("HasFile reported wrong result")
	}

	mainFiles := sor.FilesInPackage("cmd/app")
	if len(mainFiles) != 1 || mainFiles[0] != "cmd/app/main.go" {
		t.Errorf("FilesInPackage(cmd/app) = %v", mainFiles)
	}

	if lang := sor.Language("svc/service.go"); lang != "go" {
		t.Errorf("language = %q, want go", lang)
	}
}

func TestSorImportsAndDependencies(t *testing.T) {
	sor := newTestSor(t, goFixture())

	imps := sor.ImportsOf("cmd/app/main.go")
	found := false
	for _, imp := range imps {
		if strings.HasSuffix(imp, "/fixture/svc") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected svc import, got %v", imps)
	}

	deps := sor.Dependencies("cmd/app/main.go")
	if len(deps) == 0 {
		t.Error("expected dependencies")
	}

	inRepo := sor.InRepoDependencies("cmd/app/main.go")
	if !containsStr(inRepo, "svc") {
		t.Errorf("InRepoDependencies = %v, want svc", inRepo)
	}
}

func TestSorDependents(t *testing.T) {
	sor := newTestSor(t, goFixture())

	deps := sor.Dependents("svc/service.go")
	if !containsStr(deps, "cmd/app/main.go") {
		t.Errorf("expected main.go to depend on svc/service.go, got %v", deps)
	}
}

func TestSorNeighborhood(t *testing.T) {
	sor := newTestSor(t, goFixture())

	n := sor.Neighborhood("svc/service.go")
	if !containsStr(n, "cmd/app/main.go") {
		t.Errorf("neighborhood should include main.go, got %v", n)
	}
}

func TestSorCallGraph(t *testing.T) {
	sor := newTestSor(t, goFixture())

	callees := sor.Callees("Service.Run")
	if !containsSymbol(callees, "Service.helper") {
		t.Errorf("Run should call helper, got %v", symbolQuals(callees))
	}
	callers := sor.Callers("Service.helper")
	if !containsSymbol(callers, "Service.Run") {
		t.Errorf("helper should be called by Run, got %v", symbolQuals(callers))
	}
}

func TestSorSource(t *testing.T) {
	sor := newTestSor(t, goFixture())

	src, err := sor.Source("svc/service.go")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if !strings.Contains(string(src), "type Service struct") {
		t.Error("source content missing struct")
	}
	if _, err := sor.Source("missing.go"); err == nil {
		t.Error("expected error for missing source")
	}
}

// TestSorConcurrentReadsDuringRefresh exercises the index invalidation path
// under the race detector: readers run while the SoR graph is refreshed.
func TestSorConcurrentReadsDuringRefresh(t *testing.T) {
	root := writeRepo(t, goFixture())
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	sor := NewSor(e)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = sor.Symbol("Service.Run")
					_ = sor.Files()
					_ = sor.SymbolsOfFile("svc/service.go")
					_ = sor.Dependents("svc/service.go")
					_ = sor.Callees("Service.helper")
					_ = sor.Neighborhood("svc/service.go")
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		servicePath := filepath.Join(root, "svc", "service.go")
		for i := 0; i < 50; i++ {
			if err := os.WriteFile(servicePath, []byte(goFixture()["svc/service.go"]), 0o644); err != nil {
				t.Error(err)
				return
			}
			if _, err := e.Refresh(context.Background(), []string{"svc/service.go"}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

func containsSymbol(syms []SymbolInfo, qual string) bool {
	for _, s := range syms {
		if s.QualName == qual {
			return true
		}
	}
	return false
}

func symbolQuals(syms []SymbolInfo) []string {
	out := make([]string, len(syms))
	for i := range syms {
		out[i] = syms[i].QualName
	}
	return out
}
