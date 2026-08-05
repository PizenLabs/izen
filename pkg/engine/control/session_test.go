package control

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/decision"
	"github.com/PizenLabs/izen/pkg/engine/ir"
)

func nodeGraph(t *testing.T, ids ...string) *ir.ExecutionGraph {
	t.Helper()
	g := ir.NewGraph()
	for _, id := range ids {
		if err := g.AddNode(id, ir.KindEnvProbe, true, id); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// TestSessionMechanicalTransitions verifies the session applies only the five
// mechanical state transitions and rejects every illegal move. No retry /
// re-plan / skip decision lives here.
func TestSessionMechanicalTransitions(t *testing.T) {
	session := NewSession(&ir.Plan{ID: "s", Graph: nodeGraph(t, "a")})

	// Illegal: Apply on a node that never ran.
	if err := session.Apply(ir.ObservationPayload{NodeID: "a", OK: true}); err == nil {
		t.Fatal("Apply on a Pending node must be rejected")
	}
	// Illegal: Skip on a node that never failed.
	if err := session.Skip("a", "reason"); err == nil {
		t.Fatal("Skip on a Pending node must be rejected")
	}
	// Illegal: ResetForRetry on a node that never failed.
	if err := session.ResetForRetry("a"); err == nil {
		t.Fatal("ResetForRetry on a Pending node must be rejected")
	}

	// Dispatch mechanics: Pending → Ready → Running.
	if err := session.MarkRunning([]string{"a"}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	// Illegal: double dispatch.
	if err := session.MarkRunning([]string{"a"}); err == nil {
		t.Fatal("MarkRunning on a Running node must be rejected")
	}
	// Illegal: Skip a Running node.
	if err := session.Skip("a", "reason"); err == nil {
		t.Fatal("Skip on a Running node must be rejected")
	}

	// Running → Failed.
	if err := session.Apply(ir.ObservationPayload{NodeID: "a", OK: false, Err: "boom"}); err != nil {
		t.Fatalf("Apply(fail): %v", err)
	}
	if session.Observe().State("a") != ir.StateFailed {
		t.Fatal("node must be Failed after a failed observation")
	}
	// Retry mechanics: Failed → Ready.
	if err := session.ResetForRetry("a"); err != nil {
		t.Fatalf("ResetForRetry: %v", err)
	}
	// Skip mechanics: Failed → Success.
	if err := session.MarkRunning([]string{"a"}); err != nil {
		t.Fatalf("MarkRunning after retry: %v", err)
	}
	if err := session.Apply(ir.ObservationPayload{NodeID: "a", OK: false, Err: "boom again"}); err != nil {
		t.Fatalf("Apply(fail 2nd): %v", err)
	}
	if err := session.Skip("a", "non-critical"); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	snap := session.Observe()
	if snap.State("a") != ir.StateSuccess {
		t.Fatalf("state = %s, want success after skip", snap.State("a"))
	}
	if obs, ok := snap.Observation("a"); !ok || obs.SkipReason != "non-critical" {
		t.Fatal("skip must retain its provenance on the observation")
	}
	// Illegal: Skip a Success node.
	if err := session.Skip("a", "again"); err == nil {
		t.Fatal("Skip on a Success node must be rejected")
	}
}

// TestSessionLoopIntegratesVariables proves variables flow from one node's
// VariableMutations into the next node's dispatch-time variable snapshot.
func TestSessionLoopIntegratesVariables(t *testing.T) {
	g := ir.NewGraph()
	if err := g.AddNode("resolve", ir.KindEnvProbe, true, "resolve config path"); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode("use", ir.KindShell, true, "use the config", "resolve"); err != nil {
		t.Fatal(err)
	}

	var seenVars ir.Variables
	exec := ExecutorFunc(func(_ context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		if node.ID == "resolve" {
			return ir.ObservationPayload{
				OK:                true,
				VariableMutations: ir.Variables{"config_path": "cfg/dev.yaml"},
			}, nil
		}
		if node.ID == "use" {
			seenVars = vars
		}
		return ir.ObservationPayload{OK: true}, nil
	})

	session := NewSession(&ir.Plan{ID: "vars", Graph: g})
	orch := NewControlLoopOrchestrator(session, decision.NewStandardDecisionEngine(), NewWorkerPool(1, exec), WithMaxIterations(10))

	res, err := orch.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Directive != "continue" {
		t.Fatalf("terminal directive = %s, want continue", res.Directive)
	}
	if seenVars["config_path"] != "cfg/dev.yaml" {
		t.Fatalf("use node saw vars %v, want config_path=cfg/dev.yaml", seenVars)
	}
}
