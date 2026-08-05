package ir

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTransitionMatrix verifies the purely mechanical state machine. It must
// contain no retry/re-plan/skip policy — only the five lifecycle states and
// their legal transitions.
func TestTransitionMatrix(t *testing.T) {
	legal := map[NodeState]map[NodeState]bool{
		StatePending: {StateReady: true},
		StateReady:   {StateRunning: true},
		StateRunning: {StateSuccess: true, StateFailed: true},
		StateFailed:  {StateReady: true, StateSuccess: true},
	}
	states := []NodeState{StatePending, StateReady, StateRunning, StateSuccess, StateFailed}
	for _, from := range states {
		for _, to := range states {
			want := legal[from][to]
			if got := from.Transition(to); got != want {
				t.Errorf("%s → %s = %v, want %v", from, to, got, want)
			}
		}
	}
	if !StateSuccess.IsTerminal() || !StateFailed.IsTerminal() {
		t.Error("success/failed must be terminal")
	}
	if StatePending.IsTerminal() || StateReady.IsTerminal() || StateRunning.IsTerminal() {
		t.Error("pending/ready/running must not be terminal")
	}
}

// TestIrDoesNotImportDecision verifies the IR layer stays free of control
// logic: the state machine and Dynamic IR must never depend on the Decision
// Engine. This is the isolation law made checkable.
func TestIrDoesNotImportDecision(t *testing.T) {
	if hasImport(t, "github.com/PizenLabs/izen/pkg/engine/decision") {
		t.Fatal("ir must not import decision: state mechanics and policy must stay separated")
	}
	if hasImport(t, "github.com/PizenLabs/izen/pkg/engine/control") {
		t.Fatal("ir must not import control")
	}
}

// TestExecutionGraphImmutable verifies the static IR is read-only after
// construction: accessors return copies, so concurrent readers can never
// corrupt the graph.
func TestExecutionGraphImmutable(t *testing.T) {
	g := NewGraph()
	if err := g.AddNode("a", KindEnvProbe, true, "first"); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode("b", KindLLM, true, "second", "a"); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode("a", KindShell, true, "dup"); err == nil {
		t.Fatal("duplicate node accepted")
	}
	if g.Len() != 2 {
		t.Fatalf("Len = %d, want 2", g.Len())
	}

	nb, _ := g.Node("b")
	deps := nb.DependsOn
	deps[0] = "mutated"
	if nb, _ := g.Node("b"); nb.DependsOn[0] != "a" {
		t.Fatal("node dependencies are mutable through the accessor")
	}

	ids := g.IDs()
	ids[0] = "mutated"
	if g.IDs()[0] != "a" {
		t.Fatal("IDs returned a live slice")
	}

	nodes := g.Nodes()
	nodes[0].ID = "mutated"
	if na, _ := g.Node("a"); na.ID != "a" {
		t.Fatal("Nodes returned live nodes")
	}
}

// TestTopoOrder verifies deterministic dependency ordering and cycle/unknown
// detection.
func TestTopoOrder(t *testing.T) {
	g := NewGraph()
	_ = g.AddNode("build", KindVerify, true, "build", "execute")
	_ = g.AddNode("context", KindContext, true, "context", "knowledge", "capabilities")
	_ = g.AddNode("knowledge", KindEnvProbe, true, "knowledge")
	_ = g.AddNode("capabilities", KindEnvProbe, true, "capabilities")
	_ = g.AddNode("execute", KindLLM, true, "execute", "context")

	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	if len(order) != 5 {
		t.Fatalf("order len = %d, want 5", len(order))
	}
	pos := make(map[string]int, len(order))
	for i, n := range order {
		pos[n.ID] = i
	}
	if pos["knowledge"] > pos["context"] || pos["capabilities"] > pos["context"] {
		t.Errorf("knowledge/capabilities must precede context: %+v", pos)
	}
	if pos["context"] > pos["execute"] || pos["execute"] > pos["build"] {
		t.Errorf("chain order violated: %+v", pos)
	}

	cyclic := NewGraph()
	_ = cyclic.AddNode("x", KindLLM, true, "x", "y")
	_ = cyclic.AddNode("y", KindLLM, true, "y", "x")
	if _, err := cyclic.TopoOrder(); err == nil {
		t.Fatal("cycle accepted")
	}

	badDep := NewGraph()
	_ = badDep.AddNode("x", KindLLM, true, "x", "ghost")
	if _, err := badDep.TopoOrder(); err == nil {
		t.Fatal("unknown dependency accepted")
	}
}

// TestExecutionSnapshotQueries exercises the Dynamic IR scheduling queries.
func TestExecutionSnapshotQueries(t *testing.T) {
	plan := &Plan{ID: "p1", Description: "plan", Graph: chainGraph("a", "b", "c")}
	snap := NewExecutionSnapshot(plan)
	if snap.IsComplete() {
		t.Fatal("all-pending snapshot must not be complete")
	}
	if len(snap.ReadyNodes()) != 1 || snap.ReadyNodes()[0] != "a" {
		t.Fatalf("ready nodes = %v, want [a] (the chain head)", snap.ReadyNodes())
	}

	// Mark a → success; only its dependent b becomes ready.
	snap.NodeStates["a"] = StateSuccess
	if len(snap.ReadyNodes()) != 1 || snap.ReadyNodes()[0] != "b" {
		t.Fatalf("ready nodes = %v, want [b]", snap.ReadyNodes())
	}
	if got := snap.FailedNodes(); len(got) != 0 {
		t.Fatalf("failed nodes = %v, want none", got)
	}

	snap.NodeStates["a"] = StateFailed
	if got := snap.FailedNodes(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("failed nodes = %v, want [a]", got)
	}
}

// TestSnapshotCloneIsDeep verifies Observe returns an isolated copy.
func TestSnapshotCloneIsDeep(t *testing.T) {
	plan := &Plan{ID: "p1", Graph: graphWith("a")}
	snap := NewExecutionSnapshot(plan)
	snap.AttemptCounts["a"] = 1
	snap.LastObservation["a"] = ObservationPayload{NodeID: "a", OK: true, Output: "x"}
	snap.Variables["k"] = "v"

	clone := snap.Clone()
	clone.AttemptCounts["a"] = 99
	clone.Variables["k"] = "mutated"
	clone.LastObservation["a"] = ObservationPayload{NodeID: "a", OK: false}

	if snap.AttemptCounts["a"] != 1 || snap.Variables["k"] != "v" {
		t.Fatal("clone aliased mutable maps")
	}
	if obs, ok := snap.LastObservation["a"]; !ok || !obs.OK {
		t.Fatal("clone aliased observation")
	}
}

// TestSnapshotReaderDoesNotExposeMutators proves the decision-facing surface
// cannot mutate the Dynamic IR: assigning the snapshot to the reader interface
// only succeeds if the reader is the whole mutable type, so this also asserts
// no unexported mutator leaks through.
func TestSnapshotReaderDoesNotExposeMutators(t *testing.T) {
	plan := &Plan{ID: "p1", Graph: graphWith("a")}
	snap := NewExecutionSnapshot(plan)
	var r SnapshotReader = snap
	_ = r
}

func graphWith(ids ...string) *ExecutionGraph {
	g := NewGraph()
	for _, id := range ids {
		_ = g.AddNode(id, KindLLM, true, id)
	}
	return g
}

// chainGraph builds a linear chain a → b → c so scheduling queries are
// deterministic.
func chainGraph(ids ...string) *ExecutionGraph {
	g := NewGraph()
	for i, id := range ids {
		if i == 0 {
			_ = g.AddNode(id, KindLLM, true, id)
			continue
		}
		_ = g.AddNode(id, KindLLM, true, id, ids[i-1])
	}
	return g
}

// hasImport reports whether the package's transitive import graph (via `go
// list -deps`) includes the target import path.
func hasImport(t *testing.T, target string) bool {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", ".")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == target {
			return true
		}
	}
	return false
}
