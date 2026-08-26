package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── STRICT PATCH CONTRACT INJECTION (Task 1) ────────────────────────────────
//
// SEARCH_REPLACE_BOUNDED_LINES sub-tasks must carry the exact SEARCH/REPLACE
// block format in their prompt: the Boundary-4 gate rejects anything that is
// not this structure, so the payload must state it verbatim.

// TestSubTaskPromptInjectsStrictPatchContractForBoundedLines proves a
// bounded-lines sub-task prompt contains every line of the strict contract.
func TestSubTaskPromptInjectsStrictPatchContractForBoundedLines(t *testing.T) {
	dag := &planner.ExecutionDAG{
		Objective: "restyle every row", Target: "page.html",
		Kind: planner.SplitBoundedLines, MaxOutputTokens: 1000,
	}
	st := planner.SubTask{
		ID: "st-1", Index: 1, Kind: planner.SplitBoundedLines,
		Region:      planner.Region{StartLine: 4, EndLine: 12},
		Description: `<section id="nav">`,
	}
	got := subTaskPrompt("restyle every row @page.html", dag, st, 1, 3)
	for _, want := range []string{
		"CRITICAL: Output MUST use exact SEARCH/REPLACE block format:",
		"<<<<<<< SEARCH",
		"[exact content from target lines]",
		"=======",
		"[proposed modification]",
		">>>>>>>",
		"Do not output raw code or markdown formatting outside search/replace blocks.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bounded-lines prompt missing strict contract fragment %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Change window: lines 4–12") {
		t.Errorf("contract injection must not disturb the change-window scoping:\n%s", got)
	}
	if len(got) > 2048 {
		t.Errorf("prompt unbounded after injection: %d chars", len(got))
	}
}

// TestSubTaskPromptContractGatedToBoundedLines proves other split kinds keep
// their prompt unchanged (no CRITICAL block injected).
func TestSubTaskPromptContractGatedToBoundedLines(t *testing.T) {
	for _, kind := range []planner.SplitKind{planner.SplitAST, planner.SplitBlock, ""} {
		dag := &planner.ExecutionDAG{Objective: "obj", Target: "big.go", MaxOutputTokens: 1000, Kind: kind}
		st := planner.SubTask{ID: "st-2", Index: 2, Kind: kind,
			Region: planner.Region{StartLine: 10, EndLine: 20}, Description: "type Handler1 struct"}
		got := subTaskPrompt("refactor @big.go", dag, st, 2, 5)
		if strings.Contains(got, "CRITICAL:") {
			t.Errorf("kind %q must not carry the strict contract block:\n%s", kind, got)
		}
		if got := injectPatchContract("base", kind); got != "base" {
			t.Errorf("injection must be a no-op for kind %q, got %q", kind, got)
		}
	}
	if requiresPatchContract(planner.SplitAST) || requiresPatchContract(planner.SplitBlock) ||
		requiresPatchContract("") {
		t.Error("only SplitBoundedLines is bound to the strict patch contract")
	}
}

// ── RETRY SELF-CORRECTION CONTEXT ───────────────────────────────────────────

// TestRetryDirectiveCarriesValidationFeedback proves the retry prompt context
// names the rejection, includes the concrete validation error and restates the
// strict contract.
func TestRetryDirectiveCarriesValidationFeedback(t *testing.T) {
	obs := autonomy.Observation{
		Outcome:    autonomy.OutcomeArtifactRetryableRejected,
		Target:     "big.go",
		Diagnostic: "executor: mutation artifact rejected with retry directive: big.go: bounded patch contract requires SEARCH/REPLACE blocks or unified diff hunks",
	}
	st := planner.SubTask{ID: "st-2"}

	directive := retryDirective(st, obs, 2)
	for _, want := range []string{
		"[RETRY 2/3 — your previous output for st-2 was REJECTED by the artifact gate as artifact_retryable_rejected]",
		"Validation error:",
		"bounded patch contract requires SEARCH/REPLACE blocks",
		"CRITICAL: Output MUST use exact SEARCH/REPLACE block format:",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("retry directive missing %q:\n%s", want, directive)
		}
	}

	signal := retryEvidenceSignal(st, obs, 3)
	for _, want := range []string{
		"[DIAGNOSTIC subtype=SCHEMA_VIOLATION boundary=B4-artifact-gate",
		"sub_task=st-2 attempt=3/3",
		"bounded patch contract requires SEARCH/REPLACE blocks",
	} {
		if !strings.Contains(signal, want) {
			t.Errorf("retry evidence signal missing %q:\n%s", want, signal)
		}
	}

	// Recovery Isolation fallback: no diagnostic text still yields actionable
	// evidence naming the outcome.
	fallback := retryEvidenceSignal(st, autonomy.Observation{Outcome: autonomy.OutcomeArtifactRetryableRejected}, 2)
	if !strings.Contains(fallback, "artifact_retryable_rejected") {
		t.Errorf("fallback evidence must name the outcome:\n%s", fallback)
	}
}

// ── INTRA-DAG SUB-TASK RETRY (Task 2, integration) ──────────────────────────

// TestDriver_DecompositionIntraDAGRetryRecoversFromInvalidPatch drives an
// approved DAG whose SECOND provider invocation violates the bounded
// SEARCH/REPLACE contract. The intra-DAG retry loop must recover the unit on
// its next attempt instead of aborting the whole plan:
//
//	attempt 1 → artifact_retryable_rejected (malformed output)
//	retry    → validation error + strict contract appended to the prompt → applied
func TestDriver_DecompositionIntraDAGRetryRecoversFromInvalidPatch(t *testing.T) {
	root, driver, dag, p := stageDecompositionRun(t, 60)

	before := readTarget(t, root, "big.go")

	// st-2's FIRST generation is garbage ("this is not a SEARCH/REPLACE
	// artifact at all"): exactly one invalid-patch rejection, then recovery.
	p.poison = map[int]bool{2: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed (retry must recover the rejected unit)", term)
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}

	// One extra invocation for st-2's retry; every other unit ran once.
	if got := p.calls; got != len(dag.SubTasks)+1 {
		t.Fatalf("provider calls = %d, want %d (one contract retry)", got, len(dag.SubTasks)+1)
	}

	// The whole transaction landed: even the retried unit mutated the file.
	after := readTarget(t, root, "big.go")
	if after == before {
		t.Fatal("approved sub-tasks never mutated the workspace")
	}
	for _, st := range dag.SubTasks {
		if !strings.Contains(after, "patched-by-"+st.ID) {
			t.Errorf("%s never applied — the retry did not recover the unit", st.ID)
		}
	}

	// The retry prompt carried the self-correction context: the concrete
	// validation error from the artifact gate plus the strict block contract.
	prompts := p.recordedPrompts()
	first, retry := prompts[1], prompts[2]
	if strings.Contains(first, "[RETRY") {
		t.Errorf("st-2 attempt 1 must carry no retry context:\n%s", first)
	}
	for _, want := range []string{
		"artifact_retryable_rejected",
		"bounded patch contract requires SEARCH/REPLACE blocks",
		"CRITICAL: Output MUST use exact SEARCH/REPLACE block format:",
		"<<<<<<< SEARCH",
	} {
		if !strings.Contains(retry, want) {
			t.Errorf("retry prompt missing validation feedback %q:\n%s", want, retry)
		}
	}
}
