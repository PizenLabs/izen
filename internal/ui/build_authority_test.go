// Phase 0 execution-authority remediation — behavioral regression tests.
//
// These tests prove at runtime what the AST guards in
// internal/architecture pin statically: every staged /build plan (pure-file
// or mixed), every amendment, and every unknown-task submission crosses the
// RuntimeExecutor admission boundary or fails closed. There is no caller-side
// fallback that could execute a mutation outside the runtime.
package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

var buildPatchResponse = &ai.Response{
	Content: "<<<<<<< SEARCH\n<h1>Hello</h1>\n=======\n<h1>Goodbye</h1>\n>>>>>>>",
	Usage:   ai.ProviderUsage{Known: true},
}

func gatedResults(msgs []tea.Msg) []*gatedExecutionMsg {
	var out []*gatedExecutionMsg
	for _, msg := range msgs {
		if g, ok := msg.(gatedExecutionMsg); ok {
			out = append(out, &g)
		}
	}
	return out
}

// stagedTasks returns the current session task list of the model.
func stagedTasks(m *model) []plan.Task {
	return m.sess.CurrentTasks
}

// TestMixedPlanDispatchesPerTaskThroughExecutor proves P0 requirement 2 for
// mixed plans: a staged plan containing a SHELL_EXEC step must NOT fall back
// wholesale to a legacy caller-side loop. The FILE_MUTATE task crosses the
// RuntimeExecutor; once the executor's authoritative result is projected, the
// queue advances to the next pending task across its own admission gate
// (here: the interactive SHELL_EXEC approval gate) — never a direct mutation.
func TestMixedPlanDispatchesPerTaskThroughExecutor(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{buildPatchResponse}}
	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"},
		{StepNum: 2, Type: "SHELL_EXEC", Target: "echo hello", Status: "idle"},
	}
	m := buildRunModel(t, mock, tasks, smallHTML)

	msgs := runBuildCmdsFiltered(t, m.runStagedBuildViaRuntime())
	gated := gatedResults(msgs)
	if len(gated) != 1 {
		t.Fatalf("expected exactly one RuntimeExecutor dispatch for the FILE_MUTATE task, got %d", len(gated))
	}
	gem := gated[0]
	if gem.err != nil {
		t.Fatalf("executor dispatch failed: %v", gem.err)
	}
	if len(gem.res.Targets) != 1 || gem.res.Targets[0] != "index.html" {
		t.Fatalf("executor targets = %v, want [index.html]", gem.res.Targets)
	}
	if got := stagedTasks(m); got[1].Status != "idle" || got[1].Type != "SHELL_EXEC" {
		t.Fatalf("SHELL_EXEC task must stay untouched until its own admission gate: %+v", got[1])
	}

	// The execution stopped at the approval gate; stage it through the shared
	// projection, then project an authoritative approved+applied proof.
	res, _ := m.Update(*gem)
	m2 := res.(*model)
	if m2.executorPendingPatchID == "" {
		t.Fatal("approval-held patch not staged after executor dispatch")
	}

	approved := &execution.ExecutionResult{
		RequestID: gem.res.RequestID,
		Targets:   gem.res.Targets,
		Mutations: []execution.MutationEvidence{{File: "index.html", Outcome: execution.OutcomeChanged}},
		Proof:     &execution.ExecutionProof{Outcome: execution.OutcomeChanged},
	}
	mdl, cmd := m2.executionResultUpdate(executionResultMsg{res: approved})
	m3 := mdl.(*model)

	got := stagedTasks(m3)
	if got[0].Status != "completed" {
		t.Fatalf("task 1 must be completed from authoritative execution evidence, status=%q", got[0].Status)
	}

	// Drain the advance command: handleBuildRun(0) selects the SHELL_EXEC
	// task and stages its interactive permission gate — never a direct run.
	_ = runBuildCmdsFiltered(t, cmd)
	if !m3.pendingBuildApproval {
		t.Fatal("queue must advance to the SHELL_EXEC interactive admission gate")
	}
	if m3.pendingBuildTask == nil || m3.pendingBuildTask.Target != "echo hello" {
		t.Fatalf("wrong task staged at the shell gate: %+v", m3.pendingBuildTask)
	}
	if m3.currentBuildTaskID != 2 {
		t.Fatalf("currentBuildTaskID = %d, want 2", m3.currentBuildTaskID)
	}
}

// TestUnwiredExecutionAuthorityFailsClosed proves the fail-closed rule: with
// no gateway and no RuntimeExecutor wired, submitting a staged plan produces
// NO execution and NO fallback — the tasks stay untouched.
func TestUnwiredExecutionAuthorityFailsClosed(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{buildPatchResponse}}
	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"},
		{StepNum: 2, Type: "SHELL_EXEC", Target: "echo hello", Status: "idle"},
	}
	m := buildRunModel(t, mock, tasks, smallHTML)
	m.gateway = nil
	m.executor = nil

	msgs := runBuildCmdsFiltered(t, m.runStagedBuildViaRuntime())
	if gated := gatedResults(msgs); len(gated) != 0 {
		t.Fatalf("unwired runtime must not execute anything, got %d executor dispatches", len(gated))
	}
	got := stagedTasks(m)
	for _, tk := range got {
		if tk.Status != "idle" {
			t.Fatalf("task %d changed status to %q under an unwired runtime — fail-closed violated", tk.StepNum, tk.Status)
		}
	}
}

// TestUnknownTaskTypeFailsClosed pins the retired legacy tail of
// handleBuildRun: an unsupported typed task never reaches a provider stream
// or any other caller-side path — it fails closed and stalls.
func TestUnknownTaskTypeFailsClosed(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{buildPatchResponse}}
	tasks := []plan.Task{
		{StepNum: 1, Type: "MAKESHIFT", Target: "index.html", Status: "idle"},
	}
	m := buildRunModel(t, mock, tasks, smallHTML)

	msgs := runBuildCmdsFiltered(t, m.handleBuildRun(0))
	if gated := gatedResults(msgs); len(gated) != 0 {
		t.Fatalf("unknown task type must not be executed, got %d dispatches", len(gated))
	}
	if got := stagedTasks(m)[0].Status; got != "stalled" {
		t.Fatalf("unknown task type must stall, status=%q", got)
	}
}

// TestAmendBuildTaskSubmitsThroughExecutor proves P0 requirement 2 for
// amendments (survey L2): amendBuildTask books the corrective feedback and
// re-executes the amended task strictly through the admitted intent factory,
// landing on the RuntimeExecutor.
func TestAmendBuildTaskSubmitsThroughExecutor(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{buildPatchResponse}}
	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "failed"},
	}
	m := buildRunModel(t, mock, tasks, smallHTML)

	msgs := runBuildCmdsFiltered(t, m.amendBuildTask(1, "make it dark"))
	gated := gatedResults(msgs)
	if len(gated) != 1 {
		t.Fatalf("amended task must be re-executed through the RuntimeExecutor, got %d dispatches", len(gated))
	}
	if gated[0].err != nil {
		t.Fatalf("amended execution failed: %v", gated[0].err)
	}
	got := stagedTasks(m)[0]
	if !strings.Contains(got.Description, "AMENDMENT: make it dark") {
		t.Fatalf("amendment feedback not recorded in the task description: %q", got.Description)
	}
	if got.Status != "processing" {
		t.Fatalf("amended task must be processing, status=%q", got.Status)
	}
}
