package greenfield

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/op"
)

// prepareDirs creates the parent directories of every artifact path under
// root so write operations targeting nested trees succeed.
func prepareDirs(t *testing.T, root string, artifacts []ir.Artifact) {
	t.Helper()
	for _, a := range artifacts {
		dir := filepath.Dir(filepath.Join(root, a.Path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func TestGreenfieldPlannerMultiFileWorkspace(t *testing.T) {
	root := t.TempDir()
	const count = 12
	artifacts := make([]ir.Artifact, 0, count)
	for i := 0; i < count; i++ {
		artifacts = append(artifacts, ir.NewFile(
			fmt.Sprintf("src/file%02d.txt", i),
			[]byte(fmt.Sprintf("content-%02d", i)),
		))
	}
	prepareDirs(t, root, artifacts)

	p := NewGreenfieldPlanner(root)
	result, err := p.Plan(t.Context(), "scaffold a service", artifacts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if result.Graph == nil {
		t.Fatal("expected a graph")
	}
	// Every write node is ready immediately: the batch is fully parallel.
	if got := result.Graph.GetPendingNodes(); len(got) != count {
		t.Fatalf("expected %d pending write nodes, got %d", count, len(got))
	}
	if len(result.Artifacts) != count {
		t.Fatalf("expected %d artifacts in result, got %d", count, len(result.Artifacts))
	}
	if result.Metadata["roundtrips"] != "0" {
		t.Fatalf("expected zero roundtrips, got %q", result.Metadata["roundtrips"])
	}

	engine := kernel.NewEngine(nil)
	if _, err := result.Graph.Execute(t.Context(), engine); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Graph.IsCompleted() {
		t.Fatal("expected graph completed")
	}
	for i := 0; i < count; i++ {
		rel := fmt.Sprintf("src/file%02d.txt", i)
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if want := fmt.Sprintf("content-%02d", i); string(got) != want {
			t.Fatalf("%s content = %q, want %q", rel, got, want)
		}
	}
}

func TestGreenfieldPlannerTopologicalDependencies(t *testing.T) {
	root := t.TempDir()
	mod := ir.NewFile("go.mod", []byte("module demo\ngo 1.26\n"))
	main := ir.NewFile("main.go", []byte("package main\n"))
	main.Metadata = map[string]string{DependsOnKey: mod.ID}
	config := ir.NewFile("config.go", []byte("package main\n"))
	config.Metadata = map[string]string{DependsOnKey: mod.ID + "," + main.ID}

	p := NewGreenfieldPlanner(root)
	result, err := p.Plan(t.Context(), "generate a go project", []ir.Artifact{mod, main, config})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	assertRequires := func(t *testing.T, id string, want []string) {
		t.Helper()
		node, ok := result.Graph.GetNode(id)
		if !ok {
			t.Fatalf("missing node %q", id)
		}
		if got := node.Requires(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s requires %v, want %v", id, got, want)
		}
	}
	assertRequires(t, p.nodePrefix+":"+mod.ID, nil)
	assertRequires(t, p.nodePrefix+":"+main.ID, []string{p.nodePrefix + ":" + mod.ID})
	assertRequires(t, p.nodePrefix+":"+config.ID,
		[]string{p.nodePrefix + ":" + mod.ID, p.nodePrefix + ":" + main.ID})
}

func TestGreenfieldPlannerSkipsNonFileArtifacts(t *testing.T) {
	root := t.TempDir()
	fileArtifact := ir.NewFile("a.txt", []byte("hello"))
	meta := ir.Artifact{
		ID:       "notes",
		Path:     "notes.md",
		Kind:     ir.ArtifactMeta,
		Metadata: map[string]string{"purpose": "doc"},
	}

	result, err := NewGreenfieldPlanner(root).Plan(t.Context(), "create", []ir.Artifact{meta, fileArtifact})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := result.Graph.GetPendingNodes(); len(got) != 1 {
		t.Fatalf("expected exactly one write node, got %d", len(got))
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("expected both artifacts retained, got %d", len(result.Artifacts))
	}
}

func TestGreenfieldPlannerAllWriteOps(t *testing.T) {
	root := t.TempDir()
	result, err := NewGreenfieldPlanner(root).Plan(t.Context(), "gen", []ir.Artifact{
		ir.NewFile("a.txt", []byte("a")),
		ir.NewFile("b.txt", []byte("b")),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, id := range []string{"gf-write:a.txt", "gf-write:b.txt"} {
		node, ok := result.Graph.GetNode(id)
		if !ok {
			t.Fatalf("missing node %q", id)
		}
		if got := node.Operation().Type; got != op.OpWriteFile {
			t.Fatalf("%s operation type = %s, want %s", id, got, op.OpWriteFile)
		}
	}
}

func TestGreenfieldPlannerDeterministicNodeIDs(t *testing.T) {
	artifacts := []ir.Artifact{
		ir.NewFile("a.txt", []byte("a")),
		ir.NewFile("b.txt", []byte("b")),
	}
	rootA, rootB := t.TempDir(), t.TempDir()

	resultA, err := NewGreenfieldPlanner(rootA).Plan(t.Context(), "gen", artifacts)
	if err != nil {
		t.Fatalf("Plan A: %v", err)
	}
	resultB, err := NewGreenfieldPlanner(rootB).Plan(t.Context(), "gen", artifacts)
	if err != nil {
		t.Fatalf("Plan B: %v", err)
	}
	for _, a := range resultA.Graph.GetPendingNodes() {
		if _, ok := resultB.Graph.GetNode(a.ID()); !ok {
			t.Fatalf("node %q missing from the second identical plan", a.ID())
		}
	}
}

func TestGreenfieldPlannerErrors(t *testing.T) {
	if _, err := (*GreenfieldPlanner)(nil).Plan(t.Context(), "x", []ir.Artifact{ir.NewFile("a", []byte("b"))}); err == nil {
		t.Fatal("expected nil-receiver error")
	}

	p := NewGreenfieldPlanner("")
	if _, err := p.Plan(t.Context(), "x", []ir.Artifact{ir.NewFile("a", []byte("b"))}); !errors.Is(err, ErrEmptyWorkspaceRoot) {
		t.Fatalf("expected ErrEmptyWorkspaceRoot, got %v", err)
	}

	p2 := NewGreenfieldPlanner(t.TempDir())
	if _, err := p2.Plan(t.Context(), "x", nil); !errors.Is(err, ErrNoArtifacts) {
		t.Fatalf("expected ErrNoArtifacts, got %v", err)
	}
	if _, err := p2.Plan(t.Context(), "x", []ir.Artifact{{ID: "m", Kind: ir.ArtifactMeta}}); !errors.Is(err, ErrNoArtifacts) {
		t.Fatalf("expected ErrNoArtifacts for meta-only input, got %v", err)
	}

	dup := []ir.Artifact{ir.NewFile("a", []byte("1")), ir.NewFile("a", []byte("2"))}
	if _, err := NewGreenfieldPlanner(t.TempDir()).Plan(t.Context(), "x", dup); !errors.Is(err, ErrDuplicateArtifactID) {
		t.Fatalf("expected ErrDuplicateArtifactID, got %v", err)
	}
}
