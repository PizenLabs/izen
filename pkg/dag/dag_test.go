package dag

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// mustAdd fails the test when adding a node errors.
func mustAdd(t *testing.T, g *Graph, id string, payload any) {
	t.Helper()
	if err := g.AddNodeWithPayload(id, payload); err != nil {
		t.Fatalf("AddNode(%q): %v", id, err)
	}
}

// mustEdge fails the test when adding an edge errors.
func mustEdge(t *testing.T, g *Graph, from, to string) {
	t.Helper()
	if err := g.AddEdge(from, to); err != nil {
		t.Fatalf("AddEdge(%q, %q): %v", from, to, err)
	}
}

// edgeSet converts edges into a canonical "from:to" set for comparison.
func edgeSet(edges []Edge) map[string]bool {
	out := make(map[string]bool, len(edges))
	for _, e := range edges {
		out[e.From+":"+e.To] = true
	}
	return out
}

func TestTopoSortLinearChain(t *testing.T) {
	g := NewGraph()
	for _, id := range []string{"a", "b", "c"} {
		mustAdd(t, g, id, id)
	}
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "b", "c")

	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if g.HasCycle() {
		t.Error("linear chain must be acyclic")
	}
}

func TestTopoSortDiamond(t *testing.T) {
	g := NewGraph()
	for _, id := range []string{"a", "b", "c", "d"} {
		mustAdd(t, g, id, id)
	}
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "a", "c")
	mustEdge(t, g, "b", "d")
	mustEdge(t, g, "c", "d")

	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}
	if pos["a"] > pos["b"] || pos["a"] > pos["c"] {
		t.Fatalf("a must precede b and c, order = %v", order)
	}
	if pos["b"] > pos["d"] || pos["c"] > pos["d"] {
		t.Fatalf("d must follow b and c, order = %v", order)
	}
	if got := len(order); got != 4 {
		t.Fatalf("len(order) = %d, want 4", got)
	}
}

func TestTopoSortDeterministicForIndependentNodes(t *testing.T) {
	g := NewGraph()
	for _, id := range []string{"z", "m", "a"} {
		mustAdd(t, g, id, id)
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if want := []string{"a", "m", "z"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (ascending ID order)", order, want)
	}
}

func TestTopoSortEmptyGraph(t *testing.T) {
	order, err := NewGraph().TopoSort()
	if err != nil {
		t.Fatalf("TopoSort on empty graph: %v", err)
	}
	if len(order) != 0 {
		t.Fatalf("expected empty order, got %v", order)
	}
}

func TestCycleDetectionTwoNodeCycle(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "a", nil)
	mustAdd(t, g, "b", nil)
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "b", "a")

	_, err := g.TopoSort()
	if !errors.Is(err, ErrCyclicDependency) {
		t.Fatalf("TopoSort: expected ErrCyclicDependency, got %T %v", err, err)
	}
	var cyc *CyclicDependencyError
	if !errors.As(err, &cyc) {
		t.Fatalf("expected *CyclicDependencyError, got %T", err)
	}
	if cyc.Cycle[0] != cyc.Cycle[len(cyc.Cycle)-1] {
		t.Fatalf("cycle path must close the loop, got %v", cyc.Cycle)
	}
	if !hasAll(cyc.Cycle, "a", "b") {
		t.Fatalf("cycle path %v must contain a and b", cyc.Cycle)
	}
	if !strings.Contains(cyc.Error(), "cyclic dependency") {
		t.Fatalf("error message must describe the cycle, got %q", cyc.Error())
	}
	if !g.HasCycle() {
		t.Error("HasCycle must be true for a cyclic graph")
	}
	if cycle := g.FindCycle(); len(cycle) < 2 {
		t.Fatalf("FindCycle = %v, want a closed path", cycle)
	}
}

func TestCycleDetectionLongerCycle(t *testing.T) {
	g := NewGraph()
	for _, id := range []string{"a", "b", "c", "d"} {
		mustAdd(t, g, id, id)
	}
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "b", "c")
	mustEdge(t, g, "c", "d")
	mustEdge(t, g, "d", "b") // closes b -> c -> d -> b

	cycle := g.FindCycle()
	if len(cycle) < 3 {
		t.Fatalf("FindCycle = %v, want a cycle of at least 3 nodes", cycle)
	}
	if cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("cycle must close: %v", cycle)
	}
	// Every consecutive pair must be a real edge.
	edges := edgeSet(g.Edges())
	for i := 0; i+1 < len(cycle); i++ {
		key := cycle[i] + ":" + cycle[i+1]
		if !edges[key] {
			t.Fatalf("cycle path %v uses non-edge %s", cycle, key)
		}
	}
}

func TestSelfDependencyRejected(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "a", nil)
	if err := g.AddEdge("a", "a"); !errors.Is(err, ErrSelfDependency) {
		t.Fatalf("AddEdge(a, a): expected ErrSelfDependency, got %v", err)
	}
}

func TestAddEdgeUnknownNodes(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "a", nil)
	if err := g.AddEdge("missing", "a"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("expected ErrUnknownNode for missing from, got %v", err)
	}
	if err := g.AddEdge("a", "missing"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("expected ErrUnknownNode for missing to, got %v", err)
	}
}

func TestAddNodeValidation(t *testing.T) {
	g := NewGraph()
	if err := g.AddNode(nil); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("AddNode(nil): expected ErrInvalidNode, got %v", err)
	}
	if err := g.AddNode(&Node{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("AddNode(empty): expected ErrInvalidNode, got %v", err)
	}
	if err := g.AddNodeWithPayload("", "x"); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("AddNodeWithPayload(empty): expected ErrInvalidNode, got %v", err)
	}
	mustAdd(t, g, "a", nil)
	if err := g.AddNodeWithPayload("a", nil); !errors.Is(err, ErrDuplicateNode) {
		t.Fatalf("expected ErrDuplicateNode, got %v", err)
	}
}

func TestDuplicateEdgeIsNoOp(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "a", nil)
	mustAdd(t, g, "b", nil)
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "a", "b")
	if got := g.EdgeCount(); got != 1 {
		t.Fatalf("EdgeCount = %d, want 1 after duplicate AddEdge", got)
	}
}

func TestTopoSortNodesCarriesPayloads(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "b", "payload-b")
	mustAdd(t, g, "a", "payload-a")
	mustEdge(t, g, "a", "b")

	nodes, err := g.TopoSortNodes()
	if err != nil {
		t.Fatalf("TopoSortNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("len = %d, want 2", len(nodes))
	}
	if nodes[0].ID != "a" || nodes[0].Payload != "payload-a" {
		t.Fatalf("first node = %+v, want a/payload-a", nodes[0])
	}
	if nodes[1].ID != "b" || nodes[1].Payload != "payload-b" {
		t.Fatalf("second node = %+v, want b/payload-b", nodes[1])
	}
}

func TestNodesAndEdgesSorted(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "z", nil)
	mustAdd(t, g, "a", nil)
	mustAdd(t, g, "m", nil)
	mustEdge(t, g, "m", "z")
	mustEdge(t, g, "a", "z")

	nodes := g.Nodes()
	if nodes[0].ID != "a" || nodes[1].ID != "m" || nodes[2].ID != "z" {
		t.Fatalf("Nodes() must be sorted by ID, got %v", nodeIDs(nodes))
	}
	edges := g.Edges()
	if want := []Edge{{From: "a", To: "z"}, {From: "m", To: "z"}}; !reflect.DeepEqual(edges, want) {
		t.Fatalf("Edges() = %v, want %v", edges, want)
	}
}

func TestAcyclicGraphHasNoCycle(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "a", nil)
	mustAdd(t, g, "b", nil)
	mustAdd(t, g, "c", nil)
	mustEdge(t, g, "a", "b")
	mustEdge(t, g, "a", "c")
	if g.HasCycle() {
		t.Error("HasCycle must be false for a DAG")
	}
	if cycle := g.FindCycle(); cycle != nil {
		t.Fatalf("FindCycle = %v, want nil", cycle)
	}
}

// hasAll reports whether values contains every wanted element.
func hasAll(values []string, wanted ...string) bool {
	for _, w := range wanted {
		found := false
		for _, v := range values {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// nodeIDs extracts the IDs of nodes.
func nodeIDs(nodes []*Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

func TestConcurrentConstructionAndQuery(t *testing.T) {
	g := NewGraph()
	const workers = 8
	const perWorker = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("n-%d-%d", w, i)
				if err := g.AddNodeWithPayload(id, i); err != nil {
					t.Errorf("AddNode(%s): %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if got := g.NodeCount(); got != workers*perWorker {
		t.Fatalf("NodeCount = %d, want %d", got, workers*perWorker)
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(order) != workers*perWorker {
		t.Fatalf("TopoSort len = %d, want %d", len(order), workers*perWorker)
	}
	// Re-run to confirm determinism of the sorted order.
	again, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort (second): %v", err)
	}
	if !reflect.DeepEqual(order, again) {
		t.Fatal("TopoSort must be deterministic")
	}
}

func TestTopoSortLeavesDependenciesBeforeDependents(t *testing.T) {
	g := NewGraph()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("node-%03d", i)
		mustAdd(t, g, id, id)
		if i > 0 {
			mustEdge(t, g, fmt.Sprintf("node-%03d", i-1), id)
		}
	}
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(order) != 100 {
		t.Fatalf("len = %d, want 100", len(order))
	}
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}
	for i := 1; i < 100; i++ {
		if pos[fmt.Sprintf("node-%03d", i-1)] > pos[fmt.Sprintf("node-%03d", i)] {
			t.Fatalf("dependency must precede dependent for node-%03d", i)
		}
	}
}
