package graph

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/resource/file"
	"github.com/PizenLabs/izen/pkg/resource/terminal"
)

func nodeIDs(nodes []*OpNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID())
	}
	return ids
}

func assertPending(t *testing.T, g *ExecutionGraph, want ...string) {
	t.Helper()
	got := nodeIDs(g.GetPendingNodes())
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending = %v, want %v", got, want)
	}
}

// addNodes inserts nodes into g, failing the test on error.
func addNodes(t *testing.T, g *ExecutionGraph, nodes ...*OpNode) {
	t.Helper()
	for _, n := range nodes {
		if err := g.AddNode(n); err != nil {
			t.Fatalf("AddNode(%s): %v", n.ID(), err)
		}
	}
}

func TestExecutionGraphPendingTopological(t *testing.T) {
	g := NewExecutionGraph()
	A := mustNode(t, "A", op.OpRunCommand, newTerminalTarget(t), "true", nil)
	B := mustNode(t, "B", op.OpRunCommand, newTerminalTarget(t), "true", []string{"A"})
	C := mustNode(t, "C", op.OpRunCommand, newTerminalTarget(t), "true", []string{"A"})
	D := mustNode(t, "D", op.OpRunCommand, newTerminalTarget(t), "true", []string{"B", "C"})
	addNodes(t, g, A, B, C, D)

	assertPending(t, g, "A")
	if err := g.MarkCompleted("A"); err != nil {
		t.Fatalf("MarkCompleted(A): %v", err)
	}
	assertPending(t, g, "B", "C")
	if err := g.MarkCompleted("B"); err != nil {
		t.Fatalf("MarkCompleted(B): %v", err)
	}
	assertPending(t, g, "C")
	if err := g.MarkCompleted("C"); err != nil {
		t.Fatalf("MarkCompleted(C): %v", err)
	}
	assertPending(t, g, "D")
	if err := g.MarkCompleted("D"); err != nil {
		t.Fatalf("MarkCompleted(D): %v", err)
	}
	assertPending(t, g)
	if !g.IsCompleted() {
		t.Fatal("expected graph completed")
	}
}

func TestExecutionGraphFailedPreconditionBlocksDependent(t *testing.T) {
	g := NewExecutionGraph()
	A := mustNode(t, "A", op.OpRunCommand, newTerminalTarget(t), "true", nil)
	B := mustNode(t, "B", op.OpRunCommand, newTerminalTarget(t), "true", []string{"A"})
	addNodes(t, g, A, B)

	if err := g.MarkFailed("A"); err != nil {
		t.Fatalf("MarkFailed(A): %v", err)
	}
	assertPending(t, g)
	if g.IsCompleted() {
		t.Fatal("expected graph incomplete while B is blocked")
	}
	if got, ok := g.State("A"); !ok || got != kernel.StatusFailed {
		t.Fatalf("expected A failed, got %s (%v)", got, ok)
	}
}

func TestExecutionGraphAddNodeValidation(t *testing.T) {
	g := NewExecutionGraph()
	if err := g.AddNode(nil); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("expected ErrInvalidNode for nil node, got %v", err)
	}
	if err := g.AddNode(&OpNode{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("expected ErrInvalidNode for empty node, got %v", err)
	}
	A := mustNode(t, "A", op.OpRunCommand, newTerminalTarget(t), "true", nil)
	if err := g.AddNode(A); err != nil {
		t.Fatalf("AddNode(A): %v", err)
	}
	if err := g.AddNode(A); !errors.Is(err, ErrDuplicateNode) {
		t.Fatalf("expected ErrDuplicateNode, got %v", err)
	}
	if _, ok := g.GetNode("A"); !ok {
		t.Fatal("expected GetNode(A) to succeed")
	}
	unknown := mustNode(t, "B", op.OpRunCommand, newTerminalTarget(t), "true", []string{"missing"})
	if err := g.AddNode(unknown); !errors.Is(err, ErrUnknownPrecondition) {
		t.Fatalf("expected ErrUnknownPrecondition, got %v", err)
	}
}

func TestExecutionGraphInjectRepairOps(t *testing.T) {
	g := NewExecutionGraph()
	term := newTerminalTarget(t)
	A := mustNode(t, "A", op.OpRunCommand, term, "exit 1", nil)
	B := mustNode(t, "B", op.OpRunCommand, term, "echo repaired", []string{"A"})
	C := mustNode(t, "C", op.OpRunCommand, term, "echo downstream", []string{"B"})
	addNodes(t, g, A, B, C)

	engine := kernel.NewEngine(nil)
	_, err := g.Execute(t.Context(), engine)
	var fail *ExecutionFailure
	if !errors.As(err, &fail) {
		t.Fatalf("expected *ExecutionFailure, got %v", err)
	}
	if fail.NodeID != "A" {
		t.Fatalf("expected node A to fail, got %s", fail.NodeID)
	}
	if got, ok := g.State("A"); !ok || got != kernel.StatusFailed {
		t.Fatalf("expected A failed, got %s", got)
	}

	repairs := []op.Operation{
		{ID: "R1", Type: op.OpRunCommand, TargetResource: term, Payload: "echo fixed"},
	}
	if err := g.InjectRepairOps("A", repairs); err != nil {
		t.Fatalf("InjectRepairOps: %v", err)
	}

	bNode, ok := g.GetNode("B")
	if !ok {
		t.Fatal("expected node B")
	}
	if got := bNode.Requires(); !reflect.DeepEqual(got, []string{"R1"}) {
		t.Fatalf("B requires = %v, want [R1]", got)
	}
	cNode, ok := g.GetNode("C")
	if !ok {
		t.Fatal("expected node C")
	}
	if got := cNode.Requires(); !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("C requires = %v, want [B]", got)
	}
	assertPending(t, g, "R1")

	results, err := g.Execute(t.Context(), engine)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !g.IsCompleted() {
		t.Fatal("expected graph completed after repair")
	}
	if got, ok := results["B"].Data.(string); !ok || got != "repaired\n" {
		t.Fatalf("unexpected B output %v", results["B"].Data)
	}
	if got, ok := results["C"].Data.(string); !ok || got != "downstream\n" {
		t.Fatalf("unexpected C output %v", results["C"].Data)
	}
}

func TestExecutionGraphInjectRepairOpsPreservesOtherPreconditions(t *testing.T) {
	g := NewExecutionGraph()
	term := newTerminalTarget(t)
	A := mustNode(t, "A", op.OpRunCommand, term, "exit 1", nil)
	E := mustNode(t, "E", op.OpRunCommand, term, "echo independent", nil)
	B := mustNode(t, "B", op.OpRunCommand, term, "echo repaired", []string{"A", "E"})
	addNodes(t, g, A, E, B)

	engine := kernel.NewEngine(nil)
	_, err := g.Execute(t.Context(), engine)
	var fail *ExecutionFailure
	if !errors.As(err, &fail) || fail.NodeID != "A" {
		t.Fatalf("expected A to fail, got %v", err)
	}

	repairs := []op.Operation{
		{ID: "R1", Type: op.OpRunCommand, TargetResource: term, Payload: "echo fixed"},
		{ID: "R2", Type: op.OpRunCommand, TargetResource: term, Payload: "echo fixed2"},
	}
	if err := g.InjectRepairOps("A", repairs); err != nil {
		t.Fatalf("InjectRepairOps: %v", err)
	}
	bNode, ok := g.GetNode("B")
	if !ok {
		t.Fatal("expected node B")
	}
	if got := bNode.Requires(); !reflect.DeepEqual(got, []string{"E", "R1", "R2"}) {
		t.Fatalf("B requires = %v, want [E R1 R2]", got)
	}

	results, err := g.Execute(t.Context(), engine)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !g.IsCompleted() {
		t.Fatal("expected graph completed after repair")
	}
	if got, ok := results["E"].Data.(string); !ok || got != "independent\n" {
		t.Fatalf("unexpected E output %v", results["E"].Data)
	}
	if got, ok := results["B"].Data.(string); !ok || got != "repaired\n" {
		t.Fatalf("unexpected B output %v", results["B"].Data)
	}
}

func TestExecutionGraphInjectRepairOpsErrors(t *testing.T) {
	g := NewExecutionGraph()
	term := newTerminalTarget(t)
	A := mustNode(t, "A", op.OpRunCommand, term, "exit 1", nil)
	B := mustNode(t, "B", op.OpRunCommand, term, "true", []string{"A"})
	addNodes(t, g, A, B)

	repair := op.Operation{ID: "R1", Type: op.OpRunCommand, TargetResource: term, Payload: "true"}

	if err := g.InjectRepairOps("nope", []op.Operation{repair}); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("expected ErrUnknownNode, got %v", err)
	}
	if err := g.InjectRepairOps("A", nil); !errors.Is(err, ErrEmptyRepairOps) {
		t.Fatalf("expected ErrEmptyRepairOps, got %v", err)
	}
	if err := g.InjectRepairOps("A", []op.Operation{repair}); !errors.Is(err, ErrNodeNotFailed) {
		t.Fatalf("expected ErrNodeNotFailed while A is pending, got %v", err)
	}

	if err := g.MarkFailed("A"); err != nil {
		t.Fatalf("MarkFailed(A): %v", err)
	}
	dependsOnFailed := op.Operation{
		ID: "R2", Type: op.OpRunCommand, TargetResource: term, Payload: "true",
		Preconditions: []string{"A"},
	}
	if err := g.InjectRepairOps("A", []op.Operation{dependsOnFailed}); !errors.Is(err, ErrRepairDependsOnFailed) {
		t.Fatalf("expected ErrRepairDependsOnFailed, got %v", err)
	}
	dup := op.Operation{ID: "B", Type: op.OpRunCommand, TargetResource: term, Payload: "true"}
	if err := g.InjectRepairOps("A", []op.Operation{dup}); !errors.Is(err, ErrDuplicateNode) {
		t.Fatalf("expected ErrDuplicateNode, got %v", err)
	}
	unknownPre := op.Operation{
		ID: "R3", Type: op.OpRunCommand, TargetResource: term, Payload: "true",
		Preconditions: []string{"missing"},
	}
	if err := g.InjectRepairOps("A", []op.Operation{unknownPre}); !errors.Is(err, ErrUnknownPrecondition) {
		t.Fatalf("expected ErrUnknownPrecondition, got %v", err)
	}
}

func TestExecutionGraphExecuteOnKernelEngine(t *testing.T) {
	root := t.TempDir()
	aRes, err := file.NewFileResource(root, "a.go", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	bRes, err := file.NewFileResource(root, "b.go", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	g := NewExecutionGraph()
	A := mustNode(t, "A", op.OpWriteFile, aRes, ir.NewFile("a.go", []byte("package a\n")), nil)
	B := mustNode(t, "B", op.OpWriteFile, bRes, ir.NewFile("b.go", []byte("package b\n")), []string{"A"})
	addNodes(t, g, A, B)

	results, err := g.Execute(t.Context(), kernel.NewEngine(nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !g.IsCompleted() {
		t.Fatal("expected graph completed")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, rel := range []string{"a.go", "b.go"} {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if len(got) == 0 {
			t.Fatalf("expected non-empty %s", rel)
		}
	}
}

func TestExecutionGraphExecuteEmptyGraph(t *testing.T) {
	g := NewExecutionGraph()
	results, err := g.Execute(t.Context(), kernel.NewEngine(nil))
	if err != nil {
		t.Fatalf("Execute on empty graph: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
	if !g.IsCompleted() {
		t.Fatal("expected empty graph completed")
	}
}

func TestExecutionGraphExecuteNilEngine(t *testing.T) {
	g := NewExecutionGraph()
	if _, err := g.Execute(t.Context(), nil); err == nil {
		t.Fatal("expected error for nil engine")
	}
}

func TestExecutionGraphExecuteBlockedOnUnrepairedFailure(t *testing.T) {
	g := NewExecutionGraph()
	A := mustNode(t, "A", op.OpRunCommand, newTerminalTarget(t), "exit 1", nil)
	B := mustNode(t, "B", op.OpRunCommand, newTerminalTarget(t), "true", []string{"A"})
	addNodes(t, g, A, B)

	_, err := g.Execute(t.Context(), kernel.NewEngine(nil))
	var fail *ExecutionFailure
	if !errors.As(err, &fail) || fail.NodeID != "A" {
		t.Fatalf("expected A to fail, got %v", err)
	}
	_, err = g.Execute(t.Context(), kernel.NewEngine(nil))
	if !errors.Is(err, ErrGraphBlocked) {
		t.Fatalf("expected ErrGraphBlocked, got %v", err)
	}
}

func TestExecutionGraphConcurrentUpdates(t *testing.T) {
	term, err := terminal.NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	g := NewExecutionGraph()
	seed, err := NewOpNode(op.Operation{ID: "seed", Type: op.OpRunCommand, TargetResource: term, Payload: "true"})
	if err != nil {
		t.Fatalf("NewOpNode: %v", err)
	}
	if err := g.AddNode(seed); err != nil {
		t.Fatalf("AddNode(seed): %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id := "n-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)
				node, err := NewOpNode(op.Operation{
					ID:             id,
					Type:           op.OpRunCommand,
					TargetResource: term,
					Payload:        "true",
					Preconditions:  []string{"seed"},
				})
				if err != nil {
					t.Errorf("NewOpNode(%s): %v", id, err)
					return
				}
				if err := g.AddNode(node); err != nil {
					t.Errorf("AddNode(%s): %v", id, err)
					return
				}
				_ = g.GetPendingNodes()
				_ = g.IsCompleted()
				_, _ = g.GetNode(id)
				_, _ = g.State(id)
				if err := g.MarkCompleted(id); err != nil {
					t.Errorf("MarkCompleted(%s): %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if err := g.MarkCompleted("seed"); err != nil {
		t.Fatalf("MarkCompleted(seed): %v", err)
	}
	if !g.IsCompleted() {
		t.Fatal("expected all nodes terminal after concurrent marking")
	}
}

func TestExecutionGraphTimeoutPropagation(t *testing.T) {
	g := NewExecutionGraph()
	term, err := terminal.NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	slow := mustNode(t, "slow", op.OpRunCommand, term, "sleep 5", nil)
	slow.op.Timeout = 20 * time.Millisecond
	B := mustNode(t, "B", op.OpRunCommand, term, "true", []string{"slow"})
	addNodes(t, g, slow, B)

	_, err = g.Execute(t.Context(), kernel.NewEngine(nil))
	var fail *ExecutionFailure
	if !errors.As(err, &fail) || fail.NodeID != "slow" {
		t.Fatalf("expected slow to fail with timeout, got %v", err)
	}
	if !errors.Is(fail.Result.Error, kernel.ErrTaskTimeout) {
		t.Fatalf("expected ErrTaskTimeout, got %v", fail.Result.Error)
	}
}
