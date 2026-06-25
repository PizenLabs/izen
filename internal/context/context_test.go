package context

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/session"
)

var testRoot = filepath.Join("..", "..")

func TestBuilderEmpty(t *testing.T) {
	b := NewBuilder(testRoot, nil, nil, session.New())
	ctx := b.Build(BuildRequest{})
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if len(ctx.Files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(ctx.Files))
	}
}

func TestBuilderWithGraph(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	s := session.New()
	s.SetObjective("test objective")
	s.SetMode(1)

	b := NewBuilder(testRoot, g, nil, s)
	ctx := b.Build(BuildRequest{
		IncludeAll: true,
		MaxFiles:   3,
	})

	if ctx.Objective != "test objective" {
		t.Fatalf("expected 'test objective', got %q", ctx.Objective)
	}
	if ctx.Mode != "plan" {
		t.Fatalf("expected mode 'plan', got %q", ctx.Mode)
	}
	if len(ctx.Files) == 0 {
		t.Fatal("expected at least 1 file")
	}
	if len(ctx.Files) > 3 {
		t.Fatalf("expected at most 3 files, got %d", len(ctx.Files))
	}
}

func TestBuilderSymbolLookup(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	ctx := b.Build(BuildRequest{
		Symbols:  []string{"NewEngine"},
		MaxFiles: 5,
	})

	if len(ctx.Files) == 0 {
		t.Fatal("expected at least 1 file for symbol lookup")
	}

	found := false
	for _, f := range ctx.Files {
		for _, sym := range f.Symbols {
			if sym.Name == "NewEngine" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected to find 'NewEngine' symbol in context")
	}
}

func TestBuilderFileLookup(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	ctx := b.Build(BuildRequest{
		Files:    []string{"internal/graph/engine.go"},
		MaxFiles: 5,
	})

	if len(ctx.Files) == 0 {
		t.Fatal("expected to find specific file")
	}
	if ctx.Files[0].Path != "internal/graph/engine.go" {
		t.Fatalf("expected 'internal/graph/engine.go', got %q", ctx.Files[0].Path)
	}
}

func TestBuilderQueryLookup(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	ctx := b.Build(BuildRequest{
		Query:    "graph",
		MaxFiles: 3,
	})

	if len(ctx.Files) == 0 {
		t.Fatal("expected files matching query 'graph'")
	}
}

func TestBuilderWithDiff(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-ctx-diff-*")
	defer os.RemoveAll(dir)

	ge := git.NewEngine(dir)
	gitInit(t, dir)
	gitCommit(t, dir, "initial")

	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	b := NewBuilder(dir, nil, ge, session.New())
	ctx := b.Build(BuildRequest{
		IncludeDiff: true,
	})

	if len(ctx.Status) == 0 {
		t.Fatal("expected non-empty status")
	}
}

func TestRenderFull(t *testing.T) {
	ctx := &Context{
		Objective: "add logging",
		Mode:      "build",
		Query:     "implement logger",
		Files: []FileSlice{
			{
				Path:    "internal/foo/bar.go",
				Package: "foo",
				Imports: []string{"fmt", "log"},
				Symbols: []SymbolRef{
					{Name: "Bar", Kind: "struct", Line: 10, Exported: true},
					{Name: "Do", Kind: "function", Line: 20, Signature: "(a int) error", Exported: true},
				},
				Lines: 50,
			},
		},
		Diff: "--- a/old\n+++ b/new\n@@ -1 +1 @@\n-hello\n+world",
		Status: []string{"modified test.txt"},
		Errors: []string{"something went wrong"},
	}

	r := DefaultRenderer()
	output := r.Render(ctx)

	if !strings.Contains(output, "add logging") {
		t.Fatal("expected objective in output")
	}
	if !strings.Contains(output, "build") {
		t.Fatal("expected mode in output")
	}
	if !strings.Contains(output, "Bar") {
		t.Fatal("expected symbol in output")
	}
	if !strings.Contains(output, "diff") {
		t.Fatal("expected diff section")
	}
	if !strings.Contains(output, "something went wrong") {
		t.Fatal("expected errors section")
	}
}

func TestRenderCompact(t *testing.T) {
	ctx := &Context{
		Objective: "fix bug",
		Mode:      "build",
		Files: []FileSlice{
			{Path: "main.go", Symbols: []SymbolRef{{Name: "main", Kind: "function", Line: 1}}},
		},
		Status: []string{"modified main.go"},
	}

	r := DefaultRenderer()
	output := r.RenderCompact(ctx)

	if !strings.Contains(output, "main.go") {
		t.Fatal("expected file in compact output")
	}
	if !strings.Contains(output, "1 syms") {
		t.Fatal("expected symbol count")
	}
	if !strings.Contains(output, "modified main.go") {
		t.Fatal("expected status in compact output")
	}
}

func TestRenderEmpty(t *testing.T) {
	ctx := &Context{Mode: "ask"}
	r := DefaultRenderer()
	output := r.Render(ctx)

	if !strings.Contains(output, "ask") {
		t.Fatal("expected mode in output")
	}
}

func TestSymbolPrioritization(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	ctx := b.Build(BuildRequest{
		Symbols:    []string{"NewEngine", "Graph", "Symbol"},
		MaxFiles:   10,
		MaxSymbols: 5,
	})

	if len(ctx.Files) == 0 {
		t.Fatal("expected files for symbol lookup")
	}

	for _, f := range ctx.Files {
		if len(f.Symbols) > 5 {
			t.Fatalf("file %s has %d symbols, max is 5", f.Path, len(f.Symbols))
		}
	}
}

func TestStats(t *testing.T) {
	ctx := &Context{
		Objective: "test",
		Mode:      "ask",
		Files: []FileSlice{
			{Path: "a.go", Symbols: []SymbolRef{{Name: "A", Kind: "func"}}},
			{Path: "b.go", Symbols: []SymbolRef{{Name: "B", Kind: "func"}, {Name: "C", Kind: "func"}}},
		},
		Diff: "line1\nline2\nline3\n",
	}

	s := ctx.Stats()
	if s.FileCount != 2 {
		t.Fatalf("expected 2 files, got %d", s.FileCount)
	}
	if s.SymbolCount != 3 {
		t.Fatalf("expected 3 symbols, got %d", s.SymbolCount)
	}
	if s.DiffLines != 3 {
		t.Fatalf("expected 3 diff lines, got %d", s.DiffLines)
	}
}

func TestImportGraph(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	output := b.ImportGraph()

	if output == "" {
		t.Fatal("expected non-empty import graph")
	}
	if !strings.Contains(output, "imports:") {
		t.Fatal("expected 'imports:' in output")
	}
}

func TestDependentsOf(t *testing.T) {
	e := graph.NewEngine(testRoot)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	b := NewBuilder(testRoot, g, nil, session.New())
	files := b.DependentsOf("fmt")
	t.Logf("found %d dependents of fmt", len(files))
}

func TestCompressFile(t *testing.T) {
	fn := &graph.FileNode{
		Path:    "test.go",
		Package: "test",
		Lines:   100,
		Size:    500,
		Imports: []string{"fmt"},
		Symbols: []graph.Symbol{
			{Name: "Foo", Kind: graph.SymbolFunction, Line: 5, Exported: true, Signature: "()"},
			{Name: "bar", Kind: graph.SymbolFunction, Line: 10, Exported: false, Signature: "()"},
			{Name: "Baz", Kind: graph.SymbolType, Line: 15, Exported: true},
		},
	}

	fs := compressFile(fn, 2)
	if len(fs.Symbols) > 2 {
		t.Fatalf("expected max 2 symbols, got %d", len(fs.Symbols))
	}
}

func TestBuilderAddError(t *testing.T) {
	b := NewBuilder(".", nil, nil, session.New())
	ctx := b.Build(BuildRequest{})
	b.AddError(ctx, nil)
	if len(ctx.Errors) != 0 {
		t.Fatal("expected 0 errors for nil error")
	}
	b.AddError(ctx, os.ErrNotExist)
	if len(ctx.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(ctx.Errors))
	}
}

func TestRenderStats(t *testing.T) {
	ctx := &Context{
		Objective: "test",
		Mode:      "ask",
		Files: []FileSlice{
			{Path: "a.go", Symbols: []SymbolRef{{Name: "A", Kind: "func"}}},
		},
	}

	r := DefaultRenderer()
	s := r.Size(ctx)
	if s.FileCount != 1 {
		t.Fatalf("expected 1 file, got %d", s.FileCount)
	}
	if s.PromptChars == 0 {
		t.Fatal("expected non-zero prompt chars")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	sh(t, dir, "git", "init")
	sh(t, dir, "git", "config", "user.email", "test@izen.dev")
	sh(t, dir, "git", "config", "user.name", "Izen Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ge := git.NewEngine(dir)
	if _, err := ge.Checkpoint("init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}

func sh(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %s", name, args, string(out))
	}
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	runner := git.NewEngine(dir)
	if _, err := runner.Checkpoint(msg); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}