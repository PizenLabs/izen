package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/dag"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/planner/greenfield"
)

func TestPlanExecutionOrderSequencesDependencies(t *testing.T) {
	mod := ir.NewFile("go.mod", []byte("module demo\ngo 1.26\n"))
	main := ir.NewFile("main.go", []byte("package main\n"))
	main.Metadata.Set(greenfield.DependsOnKey, mod.ID)
	config := ir.NewFile("config.go", []byte("package main\n"))
	config.Metadata.Set(greenfield.DependsOnKey, mod.ID+", "+main.ID)

	order, err := planExecutionOrder([]ir.Artifact{config, main, mod})
	if err != nil {
		t.Fatalf("planExecutionOrder: %v", err)
	}
	pos := make(map[string]int, len(order))
	for i, n := range order {
		pos[n.ID] = i
	}
	if pos[mod.ID] > pos[main.ID] || pos[main.ID] > pos[config.ID] {
		t.Fatalf("expected go.mod -> main.go -> config.go, got %v", nodeIDsFromOrder(order))
	}
}

func TestPlanExecutionOrderRejectsCycles(t *testing.T) {
	a := ir.NewFile("a.txt", []byte("a"))
	b := ir.NewFile("b.txt", []byte("b"))
	a.Metadata.Set(greenfield.DependsOnKey, b.ID)
	b.Metadata.Set(greenfield.DependsOnKey, a.ID)

	_, err := planExecutionOrder([]ir.Artifact{a, b})
	if !errors.Is(err, dag.ErrCyclicDependency) {
		t.Fatalf("expected ErrCyclicDependency, got %T %v", err, err)
	}
	var cyc *dag.CyclicDependencyError
	if !errors.As(err, &cyc) {
		t.Fatalf("expected *dag.CyclicDependencyError, got %T", err)
	}
	if len(cyc.Cycle) == 0 {
		t.Fatal("cycle error must carry the explicit cycle path")
	}
}

func TestPlanExecutionOrderSkipsNonFileAndUnknownDependencies(t *testing.T) {
	meta := ir.Artifact{ID: "meta", Path: "notes.md", Kind: ir.ArtifactMeta}
	fileArtifact := ir.NewFile("a.txt", []byte("a"))
	fileArtifact.Metadata.Set(greenfield.DependsOnKey, "meta,missing")

	order, err := planExecutionOrder([]ir.Artifact{meta, fileArtifact})
	if err != nil {
		t.Fatalf("planExecutionOrder: %v", err)
	}
	if len(order) != 1 || order[0].ID != fileArtifact.ID {
		t.Fatalf("order = %v, want only the file artifact", nodeIDsFromOrder(order))
	}
}

func TestPlanExecutionOrderRejectsSelfDependency(t *testing.T) {
	a := ir.NewFile("a.txt", []byte("a"))
	a.Metadata.Set(greenfield.DependsOnKey, a.ID)
	if _, err := planExecutionOrder([]ir.Artifact{a}); !errors.Is(err, dag.ErrSelfDependency) {
		t.Fatalf("expected ErrSelfDependency, got %v", err)
	}
}

func TestPipelineRollsBackGreenfieldExecutionFailure(t *testing.T) {
	root := t.TempDir()
	// A pre-existing file that must survive the failed run untouched.
	if err := os.WriteFile(filepath.Join(root, "survivor.txt"), []byte("intact"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Block the target path with a directory so the atomic Commit fails after
	// the kernel executed the staged graph.
	if err := os.Mkdir(filepath.Join(root, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); err == nil {
		t.Fatal("expected the run to fail on Commit")
	}
	if got := readFile(t, filepath.Join(root, "survivor.txt")); got != "intact" {
		t.Fatalf("survivor.txt = %q, want %q (workspace must stay pristine)", got, "intact")
	}
	if info, err := os.Stat(filepath.Join(root, "index.html")); err != nil || !info.IsDir() {
		t.Fatal("blocking directory must be preserved by rollback")
	}
	// The staged artifact must never reach disk.
	if _, err := os.Stat(filepath.Join(root, "index.html", "index.html")); err == nil {
		t.Fatal("staged artifact must not be written")
	}
	if entries, err := os.ReadDir(root); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".index.html.txfs-") {
				t.Fatalf("temp file %q left behind", e.Name())
			}
		}
	}
}

func TestPipelineRollsBackWhenGateNeverAccepts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "survivor.txt"), []byte("intact"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", todoPage), fenced("html", "index.html", todoPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithMaxRepairs(1))

	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); err == nil {
		t.Fatal("expected the run to fail when the Semantic Alignment Gate never accepts")
	}
	if got := readFile(t, filepath.Join(root, "survivor.txt")); got != "intact" {
		t.Fatalf("survivor.txt = %q, want %q", got, "intact")
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); err == nil {
		t.Fatal("rejected output must never reach disk")
	}
}

func TestPipelineRollsBackLeavesNoDirectoryScaffolding(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fresh", "workspace")
	// A file blocks the workspace root's parent, so Commit cannot create the
	// root and fails during the prepare phase.
	if err := os.WriteFile(filepath.Join(parent, "fresh"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); err == nil {
		t.Fatal("expected the run to fail on Commit")
	}
	// The blocker survives and the failed run left no directory scaffolding
	// behind: the fresh workspace root must not exist.
	if data, err := os.ReadFile(filepath.Join(parent, "fresh")); err != nil || string(data) != "blocker" {
		t.Fatalf("blocker must survive: %q, %v", data, err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("rolled-back workspace root must not exist")
	}
	if entries, err := os.ReadDir(parent); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Fatalf("parent must contain only the blocker, got %v", entries)
	}
}

// nodeIDsFromOrder extracts the node IDs of a topological order.
func nodeIDsFromOrder(order []*dag.Node) []string {
	out := make([]string, 0, len(order))
	for _, n := range order {
		out = append(out, n.ID)
	}
	return out
}
