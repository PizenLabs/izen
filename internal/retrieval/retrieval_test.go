package retrieval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/graph"
)

func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func TestGraphLookup(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	gl := NewGraphLookup(g, root)

	rs := gl.SearchSymbol("NewEngine")
	if rs.Empty() {
		t.Fatal("expected to find NewEngine symbol")
	}
	if rs.Results[0].SymbolName != "NewEngine" {
		t.Errorf("expected NewEngine, got %s", rs.Results[0].SymbolName)
	}
	if rs.Confidence < 0.9 {
		t.Errorf("expected high confidence, got %f", rs.Confidence)
	}
	t.Logf("Found %s in %s (confidence: %.2f)", rs.Results[0].SymbolName, rs.Results[0].File, rs.Confidence)
}

func TestGraphLookupPackage(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	pkgMap := make(map[string]int)
	for _, f := range g.Files {
		pkgMap[f.Package]++
	}
	t.Logf("Packages found: %v", pkgMap)

	gl := NewGraphLookup(g, root)

	rs := gl.SearchPackage("graph")
	if rs.Empty() {
		t.Skip("no 'graph' package files found")
	}
	t.Logf("Found %d files in 'graph' package", rs.Count())
	for _, r := range rs.Results {
		t.Logf("  %s", r.File)
	}
}

func TestGraphLookupImports(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	gl := NewGraphLookup(g, root)

	rs := gl.SearchImports("smacker")
	if rs.Empty() {
		t.Fatal("expected to find smacker imports")
	}
	t.Logf("Found %d imports referencing 'smacker'", rs.Count())
}

func TestFullRetrieval(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	r := NewRetriever(root, g)

	rs := r.SearchSymbol("NewEngine")
	if rs.Empty() {
		t.Fatal("Retrieve(Symbol=NewEngine) returned empty")
	}
	t.Logf("Strategy: %s, Confidence: %.2f, Results: %d",
		rs.Strategy, rs.Confidence, rs.Count())

	rs = r.SearchText("types.go")
	if rs.Empty() {
		t.Fatal("Retrieve(Text=types.go) returned empty")
	}
	t.Logf("Strategy: %s, Confidence: %.2f, Results: %d",
		rs.Strategy, rs.Confidence, rs.Count())
}

func TestResultSetOperations(t *testing.T) {
	rs := &ResultSet{}

	r1 := Result{File: "a.go", Confidence: 0.8, Strategy: "graph"}
	r2 := Result{File: "b.go", Confidence: 0.5, Strategy: "rg"}
	rs.Add(r1)
	rs.Add(r2)

	if rs.Count() != 2 {
		t.Errorf("expected 2 results, got %d", rs.Count())
	}

	best := rs.Best()
	if best.File != "a.go" {
		t.Errorf("best result should be a.go, got %s", best.File)
	}

	other := &ResultSet{}
	other.Add(Result{File: "c.go", Confidence: 0.9, Strategy: "graph"})
	rs.Merge(other)

	if rs.Count() != 3 {
		t.Errorf("expected 3 after merge, got %d", rs.Count())
	}

	files := rs.Files()
	if len(files) != 3 {
		t.Errorf("expected 3 unique files, got %d", len(files))
	}
}

func TestConfidenceLabels(t *testing.T) {
	tests := []struct {
		strategy string
		label    string
	}{
		{"graph.exact", "exact"},
		{"graph.fuzzy", "high"},
		{"lynx.semantic", "medium"},
		{"rg.pattern", "low"},
		{"grep.text", "fallback"},
		{"read.file", "fallback"},
	}

	for _, tt := range tests {
		c := ConfidenceFromStrategy(tt.strategy)
		if c.Label() != tt.label {
			t.Errorf("ConfidenceFromStrategy(%q).Label() = %q, want %q", tt.strategy, c.Label(), tt.label)
		}
	}
}

func TestFallbackGlob(t *testing.T) {
	root := projectRoot()
	fc := NewFallbackChain(root)
	rs := fc.Glob("internal/graph/*.go")
	if rs.Empty() {
		t.Fatal("expected matches for internal/graph/*.go")
	}
	t.Logf("Glob internal/graph/*.go found %d files", rs.Count())
	for _, r := range rs.Results {
		t.Logf("  %s", r.File)
	}
}

func TestFallbackReadFile(t *testing.T) {
	root := projectRoot()
	fc := NewFallbackChain(root)
	rs := fc.ReadFile("go.mod")
	if rs.Empty() {
		t.Fatal("expected to read go.mod")
	}
	if rs.Results[0].Content == "" {
		t.Fatal("expected non-empty content")
	}
	t.Logf("Read go.mod (%d chars)", len(rs.Results[0].Content))
}

func TestQueryOrdering(t *testing.T) {
	tiers := []Tier{TierGraph, TierLynx, TierGlob, TierRipgrep, TierGrep, TierRead}
	for i, tier := range tiers {
		if tier.Order() != i {
			t.Errorf("Tier %s expected order %d, got %d", tier, i, tier.Order())
		}
	}
}