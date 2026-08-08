package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// TestProposalApplyDeadlineGuaranteesTerminalMessage drives the real
// applyProposalCmd closure (the file-mutation apply path that now routes
// through the 30s patch-apply deadline) with a model that has no execution
// engine and asserts a terminal mutationResultMsg is ALWAYS dispatched. It then
// feeds the terminal message through Update to prove isProcessing is cleared
// and the tick spinner loop halts.
func TestProposalApplyDeadlineGuaranteesTerminalMessage(t *testing.T) {
	m := newTestModel()
	// A nil workflow SM makes transitionToBuilding a no-op so the closure
	// deterministically reaches the applyPatchWithDeadline engine guard.
	m.workflowSM = nil
	m.execEng = nil
	m.pendingProposals = nil
	m.resolveApprovalState()

	proposal := SemanticProposal{
		ID:     "dod-1",
		Target: SemanticTarget{QualifiedName: "file.txt"},
		Diff:   "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n",
	}

	msg := m.applyProposalCmd(proposal)()
	mr, ok := msg.(mutationResultMsg)
	if !ok {
		t.Fatalf("expected terminal mutationResultMsg, got %T", msg)
	}
	if mr.err == nil {
		t.Fatal("expected an error for a missing execution engine (30s apply deadline guard)")
	}
	if !strings.Contains(mr.err.Error(), "engine not configured") {
		t.Errorf("unexpected error: %v", mr.err)
	}

	// Deliver the terminal message to a mid-apply model: isProcessing must be
	// cleared and the spinner loop must halt.
	busy := newHotfixBusyModel(t)
	res, _ := busy.Update(mr)
	m2 := res.(*model)
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after apply deadline terminal error", m2.state)
	}
	if m2.agentRunning || m2.streaming || m2.pipelineRunning || m2.reviewRunning {
		t.Errorf("processing flags still set after apply deadline terminal error: agent=%v stream=%v pipeline=%v review=%v",
			m2.agentRunning, m2.streaming, m2.pipelineRunning, m2.reviewRunning)
	}
	_, cmd := m2.Update(smoothStreamTickMsg{})
	if cmd != nil {
		t.Fatalf("tick loop still alive after apply deadline terminal error — spinner never stops")
	}
}

// TestBuildShellExecGuaranteesTerminalMessageOnPanic drives runBuildShellExec
// into a panic (a nil task dereferenced in the authorization call) and asserts
// the panic is converted into an error-carrying buildResultMsg — the shell
// spinner can never be orphaned by a crash mid-execution.
func TestBuildShellExecGuaranteesTerminalMessageOnPanic(t *testing.T) {
	m := newTestModel()
	m.authEngine = nil

	msg := m.runBuildShellExec(nil)()
	br, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected terminal buildResultMsg, got %T", msg)
	}
	if br.err == nil {
		t.Fatal("expected the nil-task panic to be surfaced as a terminal error")
	}
	if !strings.Contains(br.err.Error(), "panic") {
		t.Errorf("error does not report the panic: %v", br.err)
	}
}

// panickingPlanProvider is a provider whose Execute always panics, simulating
// a catastrophic plan-synthesis engine failure inside the async runPlanEngineCmd
// goroutine. It proves the goroutine panic guard converts the crash into an
// error outcome instead of freezing the plan spinner for the full deadline.
type panickingPlanProvider struct{}

func (panickingPlanProvider) Name() string { return "panic" }

func (panickingPlanProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	panic("simulated plan synthesis failure")
}

func (panickingPlanProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	panic("simulated plan synthesis failure")
}

// TestPlanPipelinePanicGuaranteesTerminalMessage drives the real
// runPlanEngineCmd closure into a plan-synthesis panic (a provider that panics
// inside the LLM goroutine) and asserts a terminal error-carrying planResultMsg
// is ALWAYS dispatched — the /plan spinner can never be orphaned by a crash.
func TestPlanPipelinePanicGuaranteesTerminalMessage(t *testing.T) {
	m := newTestModel()
	m.intentCompiler = nil
	m.microkernel = nil
	m.planEngine = plan.NewEngine(plan.NewPlanStore())
	m.planEngine.SetRootPath(t.TempDir())
	m.planEngine.SetProvider(panickingPlanProvider{}.Execute)

	handoff := HandoffContext{LastFailurePayload: "the handler crashes with a nil pointer on startup"}
	cmd := m.runPlanEngineCmd(
		"the handler crashes with a nil pointer on startup",
		"the handler crashes with a nil pointer on startup",
		"qwen2.5-coder:7b",
		handoff,
	)
	if cmd == nil {
		t.Fatal("runPlanEngineCmd returned nil cmd")
	}
	msg := cmd()
	pmsg, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("expected terminal planResultMsg, got %T", msg)
	}
	if pmsg.Err == nil {
		t.Fatal("expected the plan-synthesis panic to be surfaced as a terminal error")
	}
	if !strings.Contains(pmsg.Err.Error(), "panic") {
		t.Errorf("error does not report the panic: %v", pmsg.Err)
	}

	// Deliver the terminal error to a busy plan model: isProcessing must be
	// cleared and the spinner loop must halt.
	busy := newTestModel()
	busy.state = StateProcessing
	busy.streaming = true
	busy.agentRunning = true
	busy.planPending = true

	res, _ := busy.Update(pmsg)
	m2 := res.(*model)
	if m2.state != StateChat {
		t.Fatalf("state = %v, want StateChat after plan pipeline panic", m2.state)
	}
	if m2.agentRunning || m2.streaming || m2.planPending {
		t.Errorf("processing flags still set after plan pipeline panic: agent=%v stream=%v planPending=%v",
			m2.agentRunning, m2.streaming, m2.planPending)
	}
	_, cmd2 := m2.Update(smoothStreamTickMsg{})
	if cmd2 != nil {
		t.Fatalf("tick loop still alive after plan pipeline panic — spinner never stops")
	}
}

// TestPatchApplyDeadlineHelperNilEngine asserts applyPatchWithDeadline degrades
// cleanly to a terminal error on a missing execution engine — the "Applying
// hotfix..." spinner can never hang on a wedged filesystem or a nil engine.
func TestPatchApplyDeadlineHelperNilEngine(t *testing.T) {
	m := newTestModel()
	m.execEng = nil

	if err := m.applyPatchWithDeadline(&execution.Patch{}); err == nil {
		t.Fatal("applyPatchWithDeadline must error on a nil execution engine")
	}
	if err := m.applyPatchWithDeadline(nil); err == nil {
		t.Fatal("applyPatchWithDeadline must error on a nil patch with a nil engine")
	}
}
