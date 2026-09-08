package layer2

import (
	"testing"
)

func TestRankerTargetSymbolOrdering(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)

	res, err := r.Rank(ContextRequest{TargetSymbol: "Service.Run"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("no ranked symbols")
	}
	if res.Symbols[0].Symbol.QualName != "Service.Run" {
		t.Errorf("target should rank first, got %+v", res.Symbols[0])
	}

	idx := func(q string) int {
		for i, sc := range res.Symbols {
			if sc.Symbol.QualName == q {
				return i
			}
		}
		return -1
	}
	helper, compute := idx("Service.helper"), idx("compute")
	if helper < 0 {
		t.Error("Service.helper missing from ranking")
	}
	if compute < 0 {
		t.Error("compute missing from ranking")
	}
	if helper >= compute {
		t.Errorf("direct callee should outrank distant symbol: helper=%d compute=%d", helper, compute)
	}
}

func TestRankerTargetFile(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)

	res, err := r.Rank(ContextRequest{TargetFile: "svc/service.go"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("no ranked symbols")
	}
	if res.Symbols[0].Symbol.File != "svc/service.go" {
		t.Errorf("target file symbols should dominate, got %+v", res.Symbols[0])
	}

	foundStruct := false
	for _, sc := range res.Symbols {
		if sc.Symbol.QualName == "Service" && sc.Symbol.Kind == kindStruct {
			foundStruct = true
		}
	}
	if !foundStruct {
		t.Error("target file struct should be ranked")
	}
}

func TestRankerDeterministic(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	a, err := r.Rank(req, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Rank(req, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Symbols) != len(b.Symbols) {
		t.Fatalf("rank length mismatch: %d != %d", len(a.Symbols), len(b.Symbols))
	}
	for i := range a.Symbols {
		if a.Symbols[i].Symbol.QualName != b.Symbols[i].Symbol.QualName {
			t.Errorf("rank order mismatch at %d: %q vs %q", i, a.Symbols[i].Symbol.QualName, b.Symbols[i].Symbol.QualName)
		}
		if a.Symbols[i].Score != b.Symbols[i].Score {
			t.Errorf("score mismatch at %d: %f vs %f", i, a.Symbols[i].Score, b.Symbols[i].Score)
		}
	}
}

func TestRankerMaxSymbolsCap(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)
	p := DefaultPolicy()
	p.MaxSymbols = 2

	res, err := r.Rank(ContextRequest{TargetSymbol: "Service.Run"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) != 2 {
		t.Errorf("expected 2 symbols, got %d", len(res.Symbols))
	}
}

func TestRankerIterations(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)

	res, err := r.Rank(ContextRequest{TargetSymbol: "Service.Run"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.Iterations <= 0 {
		t.Errorf("expected positive iteration count, got %d", res.Iterations)
	}
}

func TestRankerMissingTarget(t *testing.T) {
	sor := newTestSor(t, goFixture())
	r := NewRanker(sor)

	if _, err := r.Rank(ContextRequest{TargetSymbol: "NoSuchFunc"}, DefaultPolicy()); err == nil {
		t.Error("expected error for missing symbol")
	}
}
