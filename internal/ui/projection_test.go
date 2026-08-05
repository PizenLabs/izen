package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

func mustAddNode(t *testing.T, g *ir.ExecutionGraph, id string, kind ir.NodeKind, desc string, deps ...string) {
	t.Helper()
	if err := g.AddNode(id, kind, true, desc, deps...); err != nil {
		t.Fatalf("AddNode(%s): %v", id, err)
	}
}

// TestProjectSnapshotToViewRendersTree exercises the full Static+Dynamic IR
// projection: header counts, dependency-tree connectors, node metadata and the
// strict token glyphs for each lifecycle state.
func TestProjectSnapshotToViewRendersTree(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "knowledge", ir.KindEnvProbe, "resolve workspace knowledge")
	mustAddNode(t, g, "capabilities", ir.KindEnvProbe, "detect workspace capability graph", "knowledge")
	mustAddNode(t, g, "context", ir.KindContext, "assemble governed context", "knowledge")
	mustAddNode(t, g, "execute", ir.KindLLM, "propose patches", "context")

	snap := ir.NewExecutionSnapshot(&ir.Plan{ID: "plan-1", Graph: g})
	snap.NodeStates["knowledge"] = ir.StateSuccess
	snap.NodeStates["capabilities"] = ir.StateSuccess
	snap.NodeStates["context"] = ir.StateRunning
	snap.NodeStates["execute"] = ir.StatePending
	snap.AttemptCounts["context"] = 2

	view := ProjectSnapshotToView(snap, g)
	if view == "" {
		t.Fatal("empty projection")
	}
	plain := ansi.Strip(view)

	// Header: live snowflake glyph + run id + per-state counts.
	for _, want := range []string{"snap-", "4 nodes", "2 success", "1 running"} {
		if !strings.Contains(plain, want) {
			t.Errorf("header missing %q:\n%s", want, plain)
		}
	}

	// Node lines carry id, kind, description and attempt suffix.
	for _, want := range []string{"knowledge", "resolve workspace knowledge", "(env_probe)", "(x2)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("node line missing %q:\n%s", want, plain)
		}
	}

	// Strict token glyphs for success / running / pending.
	if !strings.Contains(plain, IconCheck()) {
		t.Errorf("success glyph %q missing:\n%s", IconCheck(), plain)
	}
	if !strings.Contains(plain, SpinnerSnowflake()) {
		t.Errorf("running glyph %q missing:\n%s", SpinnerSnowflake(), plain)
	}
	if !strings.Contains(plain, IconPending()) {
		t.Errorf("pending glyph %q missing:\n%s", IconPending(), plain)
	}

	// Dependency-tree connectors.
	for _, want := range []string{"├─", "└─"} {
		if !strings.Contains(plain, want) {
			t.Errorf("tree connector %q missing:\n%s", want, plain)
		}
	}
}

// TestProjectSnapshotToViewFailedGlyph verifies the failed state renders the
// token error glyph.
func TestProjectSnapshotToViewFailedGlyph(t *testing.T) {
	snap := &ir.ExecutionSnapshot{
		ID:         "run-1",
		NodeStates: map[string]ir.NodeState{"validate": ir.StateFailed},
	}
	plain := ansi.Strip(ProjectSnapshotToView(snap, nil))
	if !strings.Contains(plain, IconError()) {
		t.Errorf("failed glyph %q missing:\n%s", IconError(), plain)
	}
}

// TestProjectSnapshotToViewFactOnly verifies a fact-only reconstruction (no
// plan, no graph) renders deterministically in sorted id order.
func TestProjectSnapshotToViewFactOnly(t *testing.T) {
	snap := &ir.ExecutionSnapshot{
		ID:            "run-1",
		NodeStates:    map[string]ir.NodeState{"zeta": ir.StateFailed, "alpha": ir.StatePending},
		AttemptCounts: map[string]int{"zeta": 3},
	}
	plain := ansi.Strip(ProjectSnapshotToView(snap, nil))
	if !strings.Contains(plain, "run-1") {
		t.Errorf("run id missing: %s", plain)
	}
	if !strings.Contains(plain, IconError()) || !strings.Contains(plain, IconPending()) {
		t.Errorf("fact-only glyphs missing:\n%s", plain)
	}
	if !strings.Contains(plain, "(x3)") {
		t.Errorf("attempt suffix missing: %s", plain)
	}
	if strings.Index(plain, "alpha") > strings.Index(plain, "zeta") {
		t.Errorf("fact-only order not sorted:\n%s", plain)
	}
}

// TestProjectSnapshotToViewObservationError verifies a folded failure
// observation is rendered on the node line.
func TestProjectSnapshotToViewObservationError(t *testing.T) {
	snap := &ir.ExecutionSnapshot{
		ID:         "run-1",
		NodeStates: map[string]ir.NodeState{"execute": ir.StateFailed},
		LastObservation: map[string]ir.ObservationPayload{
			"execute": {NodeID: "execute", OK: false, Err: "compile error"},
		},
	}
	plain := ansi.Strip(ProjectSnapshotToView(snap, nil))
	if !strings.Contains(plain, "compile error") {
		t.Errorf("observation error missing:\n%s", plain)
	}
}

// TestProjectSnapshotToViewNil guards the nil / empty degenerate cases.
func TestProjectSnapshotToViewNil(t *testing.T) {
	if got := ProjectSnapshotToView(nil, nil); got != "" {
		t.Errorf("nil snapshot = %q, want empty", got)
	}
	if got := ProjectSnapshotToView(&ir.ExecutionSnapshot{}, nil); got != "" {
		t.Errorf("empty snapshot = %q, want empty", got)
	}
}

// TestProjectSnapshotToViewDedupesSharedParent verifies a DAG node with two
// parents renders exactly once.
func TestProjectSnapshotToViewDedupesSharedParent(t *testing.T) {
	g := ir.NewGraph()
	mustAddNode(t, g, "root", ir.KindEnvProbe, "root")
	mustAddNode(t, g, "a", ir.KindEnvProbe, "a", "root")
	mustAddNode(t, g, "b", ir.KindEnvProbe, "b", "root")
	mustAddNode(t, g, "shared", ir.KindShell, "shared", "a", "b")

	snap := ir.NewExecutionSnapshot(&ir.Plan{ID: "plan-1", Graph: g})
	view := ansi.Strip(ProjectSnapshotToView(snap, g))

	// "shared" must render exactly once as a node line (id + kind); the
	// description is distinct so substring counting is unambiguous.
	count := strings.Count(view, "shared (shell)")
	if count != 1 {
		t.Errorf("shared node rendered %d time(s), want 1:\n%s", count, view)
	}
}
