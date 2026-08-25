package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// fiveSubTaskDAG builds a validated 5-sub-task ExecutionDAG mirroring what
// FallbackLineSlicer stages behind a DECOMPOSITION_PROPOSAL boundary.
func fiveSubTaskDAG(t *testing.T) *planner.ExecutionDAG {
	t.Helper()
	dag := planner.NewExecutionDAG("restyle every row @index.html", "index.html", planner.SplitBlock, "basetree-digest", 1000)
	for i := range 5 {
		st := planner.SubTask{
			Kind:            planner.SplitBoundedLines,
			Description:     "row window",
			Region:          planner.Region{StartLine: i*10 + 1, EndLine: (i + 1) * 10},
			EstimatedTokens: 100,
		}
		if err := dag.AddTask(st); err != nil {
			t.Fatalf("AddTask(%d): %v", i+1, err)
		}
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("staged DAG invalid: %v", err)
	}
	return dag
}

// TestStagedDAGApprovalAuthorizesTransitionToBuilding pins the staged-DAG
// handshake: a DECOMPOSITION_PROPOSAL approved by human input must register
// its ExecutionDAG as an authorized plan so TransitionToBuilding passes the
// workflow guard instead of rejecting with "no authorized plan or micro-plan".
func TestStagedDAGApprovalAuthorizesTransitionToBuilding(t *testing.T) {
	o := New(workflow.NewWorkflowStateMachine(), testRuntime(t))

	// Production topology: the orchestrator reaches planning with NO session
	// plan — the DAG lives inside the Autonomy Loop's StagedPlan context.
	if err := o.Transition(PhaseInvestigate, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Investigate): %v", err)
	}
	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}

	dag := fiveSubTaskDAG(t)
	if o.HasAuthorizedPlan() {
		t.Fatal("nothing was approved yet — plan must not be authorized")
	}

	// Without the handshake the guard rejects the transition.
	err := o.Transition(PhaseBuild, workflow.TransitionContext{})
	if err == nil || !strings.Contains(err.Error(), "no authorized plan or micro-plan") {
		t.Fatalf("pre-approval transition error = %v, want the plan guard rejection", err)
	}
	if o.Current() != PhasePlan {
		t.Errorf("Current() = %s, want plan after rejected transition", o.Current())
	}

	// ENTER on the proposal card: the bridge binds the staged DAG.
	if err := o.BindAuthorizedMicroPlan(context.Background(), dag); err != nil {
		t.Fatalf("BindAuthorizedMicroPlan: %v", err)
	}
	if !o.HasAuthorizedPlan() {
		t.Fatal("HasAuthorizedPlan() = false after an explicit human approval")
	}
	mp := o.AuthorizedMicroPlan()
	if mp == nil {
		t.Fatal("AuthorizedMicroPlan() = nil after binding")
	}
	if mp.SubTasks != 5 || mp.Target != "index.html" {
		t.Errorf("micro-plan record = %+v, want 5 sub-tasks on index.html", mp)
	}

	// TransitionToBuilding succeeds WITHOUT guard rejection even though the
	// caller supplies no plan evidence of its own (capabilities travel as in
	// the production bridge; plan authorization comes from the binding).
	if err := o.Transition(PhaseBuild, workflow.TransitionContext{HasCapabilities: true}); err != nil {
		t.Fatalf("TransitionToBuilding after approval: %v", err)
	}
	if o.Current() != PhaseBuild {
		t.Errorf("Current() = %s, want build", o.Current())
	}
	if o.CurrentWorkflowState() != workflow.StateBuilding {
		t.Errorf("workflow state = %s, want building", o.CurrentWorkflowState())
	}
}

// TestBindAuthorizedMicroPlanValidatesInput keeps fail-closed semantics: an
// empty plan or a cancelled context never authorizes anything.
func TestBindAuthorizedMicroPlanValidatesInput(t *testing.T) {
	o := New(workflow.NewWorkflowStateMachine(), testRuntime(t))

	if err := o.BindAuthorizedMicroPlan(context.Background(), nil); err == nil {
		t.Error("nil DAG must be rejected")
	}
	empty := planner.NewExecutionDAG("obj", "t.txt", planner.SplitBlock, "", 1000)
	if err := o.BindAuthorizedMicroPlan(context.Background(), empty); err == nil {
		t.Error("an empty DAG must be rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.BindAuthorizedMicroPlan(ctx, fiveSubTaskDAG(t)); err == nil {
		t.Error("a cancelled context must be rejected")
	}
	if o.HasAuthorizedPlan() {
		t.Error("failed bindings must not authorize anything")
	}
}

// TestFastPathAskToBuildInjectsEphemeralPlan covers the $hot / "/build"
// shortcut cleanup: switching phase ask → build dynamically without an initial
// LLM plan injects an EphemeralPlan so the guard sees authorized evidence
// instead of an uninitialized planning context.
func TestFastPathAskToBuildInjectsEphemeralPlan(t *testing.T) {
	o := New(workflow.NewWorkflowStateMachine(), testRuntime(t))
	if err := o.Transition(PhaseAsk, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Ask): %v", err)
	}
	if o.HasAuthorizedPlan() {
		t.Fatal("no fast path has run yet — nothing may be authorized")
	}

	if err := o.InjectEphemeralPlan("$hot"); err != nil {
		t.Fatalf("InjectEphemeralPlan: %v", err)
	}
	if !o.HasAuthorizedPlan() {
		t.Fatal("ephemeral injection must authorize the fast path")
	}
	ep := o.ActiveEphemeralPlan()
	if ep == nil || ep.Source != "$hot" {
		t.Errorf("ActiveEphemeralPlan() = %+v, want source $hot", ep)
	}

	// Ask → Build is not a valid logical edge (the fast path uses Force).
	if err := o.Force(PhaseBuild, workflow.TransitionContext{HasCapabilities: true}); err != nil {
		t.Fatalf("Force(Build) after ephemeral injection: %v", err)
	}
	if o.CurrentWorkflowState() != workflow.StateBuilding {
		t.Errorf("workflow state = %s, want building", o.CurrentWorkflowState())
	}

	// An empty source label is refused.
	if err := o.InjectEphemeralPlan(""); err == nil {
		t.Error("empty source must be rejected")
	}
}

// TestNewestAuthorizationWinsAndClearRestoresGuard pins that micro-plans and
// ephemeral plans replace each other and that ClearAuthorizedPlan makes the
// guard reject again.
func TestNewestAuthorizationWinsAndClearRestoresGuard(t *testing.T) {
	o := New(workflow.NewWorkflowStateMachine(), testRuntime(t))

	if err := o.InjectEphemeralPlan("/build"); err != nil {
		t.Fatalf("InjectEphemeralPlan: %v", err)
	}
	if err := o.BindAuthorizedMicroPlan(context.Background(), fiveSubTaskDAG(t)); err != nil {
		t.Fatalf("BindAuthorizedMicroPlan: %v", err)
	}
	if o.ActiveEphemeralPlan() != nil {
		t.Error("micro-plan binding must replace the ephemeral plan")
	}

	// Re-injecting an ephemeral plan drops the micro-plan record.
	if err := o.InjectEphemeralPlan("$hot"); err != nil {
		t.Fatalf("InjectEphemeralPlan: %v", err)
	}
	if o.AuthorizedMicroPlan() != nil {
		t.Error("ephemeral injection must replace the bound micro-plan")
	}

	o.ClearAuthorizedPlan()
	if o.HasAuthorizedPlan() {
		t.Error("ClearAuthorizedPlan must drop the authorization")
	}
	if err := o.Transition(PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("Transition(Plan): %v", err)
	}
	err := o.Transition(PhaseBuild, workflow.TransitionContext{})
	if err == nil || !strings.Contains(err.Error(), "no authorized plan or micro-plan") {
		t.Fatalf("post-clear transition error = %v, want the plan guard rejection", err)
	}
}
