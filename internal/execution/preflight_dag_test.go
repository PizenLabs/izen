package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// ── Boundary-2 × staged decomposition (DAG execution) ───────────────────────
//
// When an approved ExecutionDAG is active, the preflight guard must judge
// every staged sub-task window INDIVIDUALLY and never re-run the monolithic
// full-file rewrite estimation against the original target — a 5-sub-task
// plan staged by the line-slicing fallback can otherwise be refused for the
// very size it was decomposed to escape (the false-positive
// preflight_infeasible pipeline leak).

// dagMonolithFixture renders ~targetBytes of single-section HTML content.
func dagMonolithFixture(targetBytes int) string {
	var b strings.Builder
	b.WriteString("<section id=\"monolith\">\n")
	line := fmt.Sprintf("<div class=\"row\"><p>%s</p></div>\n",
		strings.Repeat("content block padding ", 4))
	for b.Len() < targetBytes {
		b.WriteString(line)
	}
	b.WriteString("</section>\n")
	return b.String()
}

// TestEvaluatePreflightStagedScopesJudgeEachUnitIndividually unit-covers the
// DAG branch: every scope passes on its own estimate; a single oversize unit
// fails closed naming its identity; the monolithic TargetBytes is ignored.
func TestEvaluatePreflightStagedScopesJudgeEachUnitIndividually(t *testing.T) {
	scopes := []SubTaskScope{
		{ID: "st-1", StartLine: 1, EndLine: 4, EstimatedTokens: 120},
		{ID: "st-2", StartLine: 5, EndLine: 37, EstimatedTokens: 1433},
		{ID: "st-3", StartLine: 38, EndLine: 64, EstimatedTokens: 990},
	}
	// The monolithic size would estimate far beyond the budget; the staged
	// scopes must suppress that estimation entirely.
	v := EvaluatePreflight(PreflightRequest{
		TargetBytes:     7780,
		StagedScopes:    scopes,
		MaxOutputTokens: 2048,
	})
	if !v.Feasible {
		t.Fatalf("verdict = %+v, want feasible for individually-fitting sub-tasks", v)
	}
	if v.EstimatedTokens != 1433 {
		t.Fatalf("estimate = %d, want the largest staged scope (1433)", v.EstimatedTokens)
	}

	// One oversize unit refuses the whole plan, naming its identity.
	scopes[1].EstimatedTokens = 4096
	v = EvaluatePreflight(PreflightRequest{
		TargetBytes:     7780,
		StagedScopes:    scopes,
		MaxOutputTokens: 2048,
	})
	if v.Feasible {
		t.Fatal("a sub-task above max_output must fail the plan closed")
	}
	if !strings.Contains(v.Reason, "st-2") {
		t.Fatalf("reason %q must name the offending sub-task", v.Reason)
	}

	// A non-positive estimate is invalid evidence — fail closed.
	v = EvaluatePreflight(PreflightRequest{
		TargetBytes:     7780,
		StagedScopes:    []SubTaskScope{{ID: "st-1", EstimatedTokens: 0}},
		MaxOutputTokens: 2048,
	})
	if v.Feasible || !strings.Contains(v.Reason, "st-1") {
		t.Fatalf("zero-estimate scope verdict = %+v, want infeasible naming st-1", v)
	}

	// Unbounded budgets defer to the output gate, as for monolithic requests.
	v = EvaluatePreflight(PreflightRequest{
		TargetBytes:     7780,
		StagedScopes:    scopes,
		MaxOutputTokens: 0,
	})
	if !v.Feasible {
		t.Fatalf("unbounded budget verdict = %+v, want feasible", v)
	}
}

// TestExecutor_PreflightSuppressesMonolithicEstimateWhenDAGActive proves the
// pipeline contract end-to-end at the executor boundary: the SAME infeasible
// monolithic request is trapped WITHOUT staged scopes and proceeds THROUGH
// Boundary 2 WITH them (one provider invocation, patch staged).
func TestExecutor_PreflightSuppressesMonolithicEstimateWhenDAGActive(t *testing.T) {
	root := t.TempDir()
	source := dagMonolithFixture(7780)
	writeTarget(t, root, "index.html", source)

	patch := "<<<<<<< SEARCH\n" +
		strings.SplitN(strings.TrimSuffix(source, "\n"), "\n", 2)[0] + "\n" +
		"=======\n<div class=\"row patched\"></div>\n>>>>>>>"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: patch,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	base := ExecuteRequest{
		Mode:    "autonomy",
		Prompt:  "restyle every row @index.html",
		Targets: []string{"index.html"},
		// The FULL_REWRITE-tolerant artifact contract is what Boundary 2
		// feasibility-checks against the whole target size.
		Strategy:        tolerantFullRewriteProfilePtr(1024),
		MaxOutputTokens: 1024,
	}

	// WITHOUT staged scopes: the monolithic full-rewrite estimate traps the
	// request at Boundary 2 with zero provider requests.
	res, err := x.Execute(context.Background(), ExecuteRequest{RequestID: "mono", Strategy: base.Strategy,
		Prompt: base.Prompt, Targets: base.Targets, MaxOutputTokens: base.MaxOutputTokens})
	if !errors.Is(err, ErrPreflightInfeasible) {
		t.Fatalf("monolithic err = %v, want ErrPreflightInfeasible", err)
	}
	if res == nil || res.Proof.Outcome != OutcomePreflightInfeasible {
		t.Fatalf("monolithic outcome = %+v, want %s", res.Proof, OutcomePreflightInfeasible)
	}
	if got := mock.calls(); got != 0 {
		t.Fatalf("monolithic provider calls = %d, want 0", got)
	}

	// WITH staged scopes (the approved DAG): every unit is judged
	// individually — the guard passes and the provider is invoked once.
	staged := []SubTaskScope{
		{ID: "st-1", StartLine: 1, EndLine: 30, EstimatedTokens: 200},
		{ID: "st-2", StartLine: 31, EndLine: 60, EstimatedTokens: 200},
		{ID: "st-3", StartLine: 61, EndLine: 90, EstimatedTokens: 200},
	}
	res2, err2 := x.Execute(context.Background(), ExecuteRequest{RequestID: "dag-st-1",
		Strategy: base.Strategy, Prompt: base.Prompt, Targets: base.Targets,
		MaxOutputTokens: base.MaxOutputTokens, StagedSubTasks: staged})
	if err2 != nil {
		t.Fatalf("dag-scoped execute: %v", err2)
	}
	if res2.Proof.Outcome == OutcomePreflightInfeasible {
		t.Fatal("a DAG-scoped submission was judged by the monolithic estimate (pipeline leak)")
	}
	if got := mock.calls(); got != 1 {
		t.Fatalf("dag-scoped provider calls = %d, want 1", got)
	}
	if res2.PendingPatchID == "" {
		t.Fatal("dag-scoped submission did not stage at the approval gate")
	}
}

// TestExecutor_OversizeStagedScopeStillRefused guards the inverse: a staged
// plan carrying one over-budget unit must NOT slip through the suppressed
// monolithic check — the per-unit evaluation stays authoritative.
func TestExecutor_OversizeStagedScopeStillRefused(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", dagMonolithFixture(512))
	mock := &mockProvider{}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "dag-oversize",
		Mode:            "autonomy",
		Prompt:          "restyle every row @index.html",
		Targets:         []string{"index.html"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 256,
		StagedSubTasks:  []SubTaskScope{{ID: "st-1", StartLine: 1, EndLine: 10, EstimatedTokens: 9999}},
	})
	if !errors.Is(err, ErrPreflightInfeasible) {
		t.Fatalf("err = %v, want ErrPreflightInfeasible for the oversize unit", err)
	}
	if res.Proof.Outcome != OutcomePreflightInfeasible {
		t.Fatalf("outcome = %s, want %s", res.Proof.Outcome, OutcomePreflightInfeasible)
	}
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}
