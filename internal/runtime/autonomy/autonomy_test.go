package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── PREFLIGHT × DECOMPOSITION PIPELINE ──────────────────────────────────────
//
// A target whose whole-file generation estimate exceeds max_output must be
// decomposed, NOT refused twice: once the planner stages a validated
// ExecutionDAG whose every sub-task fits the budget individually, Boundary 2
// must evaluate those units individually and never re-run the monolithic
// full-rewrite estimation against the original target (the false-positive
// preflight_infeasible pipeline leak).

// TestPipeline_DecomposedPreflightJudgesSubTasksIndividually drives a
// preflight-infeasible monolith through staging and approved DAG execution,
// proving:
//
//  1. the monolithic estimate IS infeasible (the failure decomposition exists
//     for), while every staged sub-task passes EvaluatePreflight
//     INDIVIDUALLY;
//  2. the approved plan executes every unit through the full executor
//     pipeline WITHOUT any sub-task being refused by a monolithic budget
//     verdict (zero preflight_infeasible observations);
//  3. the staged scopes reach the executor's Boundary-2 view intact.
func TestPipeline_DecomposedPreflightJudgesSubTasksIndividually(t *testing.T) {
	root := t.TempDir()
	source := string(htmlMonolithFixture(120))
	writeTarget(t, root, "index.html", source)
	sourceBytes := []byte(source)

	p := &dagProvider{root: root, target: "index.html"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	// ── Stage: the run must park at the DECOMPOSITION_PROPOSAL boundary ──
	if _, err := driver.Run(context.Background(), "restyle every row @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dag := driver.Proposal()
	if dag == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL parked at the boundary")
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub-tasks = %d, want a real line-sliced decomposition", len(dag.SubTasks))
	}

	// The monolithic target exceeds the budget — that is the failure the
	// decomposition escapes. It must never be measured again once the plan
	// is staged.
	maxOut := dag.MaxOutputTokens
	if v := execution.EvaluatePreflight(execution.PreflightRequest{
		TargetBytes:     len(sourceBytes),
		MaxOutputTokens: maxOut,
	}); v.Feasible {
		t.Fatalf("fixture is not preflight-infeasible monolithically (max_output=%d): %+v", maxOut, v)
	}

	// Every staged unit passes the SAME guard individually, by construction.
	for _, st := range dag.SubTasks {
		if !planner.PreflightFeasible(sourceBytes, st.Region, maxOut) {
			t.Errorf("%s region %s fails EvaluatePreflight individually (max_output=%d)",
				st.ID, st.Region, maxOut)
		}
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("staged DAG failed Validate: %v", err)
	}

	// The staged scopes project onto the executor's Boundary-2 view intact.
	scopes := stagedSubTaskScopes(dag)
	if len(scopes) != len(dag.SubTasks) {
		t.Fatalf("staged scopes = %d, want %d", len(scopes), len(dag.SubTasks))
	}
	for i, sc := range scopes {
		st := dag.SubTasks[i]
		if sc.ID != st.ID || sc.StartLine != st.Region.StartLine ||
			sc.EndLine != st.Region.EndLine || sc.EstimatedTokens != st.EstimatedTokens {
			t.Errorf("scope[%d] = %+v, want the projection of %s", i, sc, st.ID)
		}
	}
	if v := execution.EvaluatePreflight(execution.PreflightRequest{
		TargetBytes:     len(sourceBytes),
		StagedScopes:    scopes,
		MaxOutputTokens: maxOut,
	}); !v.Feasible {
		t.Fatalf("staged-scope prelight verdict = %+v, want feasible", v)
	}
	if stagedSubTaskScopes(nil) != nil {
		t.Fatal("a nil plan must project to nil scopes")
	}

	// ── Approve: every unit crosses the executor pipeline un-refused ──────
	before := readTarget(t, root, "index.html")
	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if got := p.calls; got != len(dag.SubTasks) {
		t.Fatalf("provider calls = %d, want one per sub-task (%d)", got, len(dag.SubTasks))
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}

	// NO sub-task was refused by a monolithic budget verdict: every unit
	// landed its anchored patch.
	after := readTarget(t, root, "index.html")
	if after == before {
		t.Fatal("approved sub-tasks never mutated the workspace")
	}
	for _, st := range dag.SubTasks {
		if !strings.Contains(after, "patched-by-"+st.ID) {
			t.Errorf("%s never applied — its submission was refused mid-pipeline", st.ID)
		}
	}
	if driver.LastObservation().Outcome == autonomy.OutcomePreflightInfeasible {
		t.Fatal("final observation carries preflight_infeasible (monolithic budget leaked into DAG execution)")
	}
}
