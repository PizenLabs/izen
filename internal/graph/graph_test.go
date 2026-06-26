package graph

import (
	"testing"
)

func TestBuildIzenGraph(t *testing.T) {
	e := NewEngine(".")
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if g.FileCount == 0 {
		t.Fatal("expected at least 1 file")
	}

	t.Logf("Files: %d", g.FileCount)
	t.Logf("Symbols: %d", g.SymCount)

	var hasFunction, hasType bool
	for _, f := range g.Files {
		for _, sym := range f.Symbols {
			if sym.Kind == SymbolFunction {
				hasFunction = true
			}
			if sym.Kind == SymbolStruct || sym.Kind == SymbolType {
				hasType = true
			}
			t.Logf("  %s %s (%s:%d)", sym.Kind, sym.Name, sym.File, sym.Line)
		}
	}

	if !hasFunction {
		t.Error("expected at least one function symbol")
	}
	if !hasType {
		t.Error("expected at least one type symbol")
	}

	stats := g.Stats()
	t.Logf("Stats: %+v", stats)
}

func TestScanIzen(t *testing.T) {
	cfg := DefaultScanConfig(".")
	result, err := Scan(cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(result.Files) == 0 {
		t.Fatal("expected at least 1 file")
	}

	for _, f := range result.Files {
		t.Logf("  %s (%s, %d bytes)", f.Path, f.Lang, f.Size)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	e := NewEngine(".")
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	c, err := EncodeCache(g)
	if err != nil {
		t.Fatalf("EncodeCache: %v", err)
	}

	g2, err := DecodeCache(c)
	if err != nil {
		t.Fatalf("DecodeCache: %v", err)
	}

	if g2.FileCount != g.FileCount {
		t.Errorf("FileCount mismatch: %d != %d", g2.FileCount, g.FileCount)
	}
}
