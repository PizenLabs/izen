package lea

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/lea/graph"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// bench20kFixture writes a ~20k LOC Go repository (100 files x 50 four-line
// functions, ~5k functions, ~10k call sites) and returns the pre-extracted
// symbol data so benchmarks measure graph work rather than fixture I/O.
func bench20kFixture(b *testing.B) (string, []symbol.FileASTInfo) {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < 100; i++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "package bench\n\n")
		for j := 0; j < 50; j++ {
			next := fmt.Sprintf("F%03d_%03d", i, (j+1)%50)
			fmt.Fprintf(&sb, "func F%03d_%03d() {\n\tx := %s()\n\t_ = x\n}\n\n", i, j, next)
		}
		path := filepath.Join(root, fmt.Sprintf("bench%03d.go", i))
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	e := NewEngine(root)
	files, err := e.walkSourceFiles()
	if err != nil {
		b.Fatal(err)
	}
	extracted, err := e.extractAll(context.Background(), files)
	if err != nil {
		b.Fatal(err)
	}
	return root, extracted
}

// Budgets (uninstrumented): full index <1s, incremental <100ms, cache load
// <10ms. Benchmarks report numbers without asserting, so CI stays green while
// regressions show up in the trend.
func BenchmarkGraphBuild20kLOC(b *testing.B) {
	_, extracted := bench20kFixture(b)
	g := graph.NewGraph("repo")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := g.Build(extracted); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGraphUpsertFile(b *testing.B) {
	_, extracted := bench20kFixture(b)
	g := graph.NewGraph("repo")
	if err := g.Build(extracted); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := g.UpsertFile(extracted[0]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreLoad20k(b *testing.B) {
	root, extracted := bench20kFixture(b)
	e := NewEngine(root)
	if err := e.Graph().Build(extracted); err != nil {
		b.Fatal(err)
	}
	if err := e.save(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := graph.NewGraph(root)
		if _, err := e.store.Load(g); err != nil {
			b.Fatal(err)
		}
	}
}
