package ui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// spyWorkerEngine records ExecuteProposal calls for trace assertions.
type spyWorkerEngine struct {
	calls atomic.Int32
	last  scopeguard.Proposal
}

func (s *spyWorkerEngine) ExecuteProposal(_ context.Context, p scopeguard.Proposal) error {
	s.calls.Add(1)
	s.last = p
	return nil
}

// TestBuildWorkerProposalTargetlessIsForensics pins Phase 2: even a
// target-less prompt such as "hi" in INVESTIGATE mode yields a VALID
// scopeguard.Proposal scoped to workspace inspection/forensics — never
// conversational chat.
func TestBuildWorkerProposalTargetlessIsForensics(t *testing.T) {
	p := BuildWorkerProposal("hi", modes.ModeInvestigate, "")
	if err := p.Validate(); err != nil {
		t.Fatalf("target-less proposal invalid: %v", err)
	}
	if p.Op != scopeguard.OpRead {
		t.Fatalf("op = %s, want READ (forensics, never chat)", p.Op)
	}
	if p.Workspace != scopeguard.WorkspaceInvestigate {
		t.Fatalf("workspace = %s, want investigate", p.Workspace)
	}
	if len(p.TargetFiles) == 0 {
		t.Fatal("target-less prompt must default to workspace-inspection scope")
	}
	if p.Op.Mutating() {
		t.Fatal("forensics default must never be mutating")
	}
}

// TestEvaluateWorkerProposalAllowsForensics pins that the ScopeGuard
// authority tier authorizes the forensics default.
func TestEvaluateWorkerProposalAllowsForensics(t *testing.T) {
	p := BuildWorkerProposal("hi", modes.ModeInvestigate, "")
	res := EvaluateWorkerProposal(context.Background(), p, nil)
	if res.Decision == scopeguard.DecisionDeny {
		t.Fatalf("forensics proposal denied: %s", res.Reason)
	}
}

// TestInvestigateModeHiNeverTakesDirectResponse is the trace-assertion test:
// sending "hi" in INVESTIGATE mode must cross ScopeGuard.EvaluateProposal +
// WorkerEngine.ExecuteProposal and must NOT select direct_response.
func TestInvestigateModeHiNeverTakesDirectResponse(t *testing.T) {
	spy := &spyWorkerEngine{}
	prev := ActiveWorkerEngine
	ActiveWorkerEngine = spy
	defer func() { ActiveWorkerEngine = prev }()

	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "forensic findings"}},
	}, nil)
	m.resolver.Set(modes.ModeInvestigate)
	m.state = StateChat

	cmd := m.runGatedLine("hi")
	if cmd == nil {
		t.Fatal("investigate dispatch returned nil command — hi must route through the runtime engine")
	}
	if spy.calls.Load() != 1 {
		t.Fatalf("WorkerEngine.ExecuteProposal calls = %d, want 1", spy.calls.Load())
	}
	if err := spy.last.Validate(); err != nil {
		t.Fatalf("executed worker proposal invalid: %v", err)
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("investigate execution failed: %v", gem.err)
	}
	if gem.res != nil && gem.res.Strategy == string(strategy.DirectResponse) {
		t.Fatalf("strategy = direct_response in INVESTIGATE mode — hard enforcement forbids the phantom trace path")
	}
	if detStr := string(gem.det.Profile.Strategy); detStr == string(strategy.DirectResponse) {
		t.Fatalf("detected strategy = direct_response in INVESTIGATE mode")
	}
	// The full operation-lifecycle path owns an execution projection; the
	// deprecated direct_response shortcut leaves it nil.
	if m.execView == nil {
		t.Fatal("INVESTIGATE hi must take the full runtime path (execView != nil), not the direct_response shortcut")
	}
}

// TestAskModeHiKeepsDirectResponse pins the boundary: ASK is the single
// pure-conversation mode where DirectResponse remains legal.
func TestAskModeHiKeepsDirectResponse(t *testing.T) {
	prev := ActiveWorkerEngine
	ActiveWorkerEngine = nil
	defer func() { ActiveWorkerEngine = prev }()

	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "Hello!"}},
	}, nil)
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat

	cmd := m.runGatedLine("hi")
	if cmd == nil {
		t.Fatal("ask dispatch returned nil command")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.res == nil || gem.res.Strategy != string(strategy.DirectResponse) {
		t.Fatalf("ASK hi strategy = %v, want direct_response", gem.res)
	}
}

// TestInvestigateShortPromptReachesEngine pins the commands.go fix: a short
// admitted prompt in INVESTIGATE mode reaches the investigate engine instead
// of the removed "describe what to investigate" early return.
func TestInvestigateShortPromptReachesEngine(t *testing.T) {
	prev := ActiveWorkerEngine
	ActiveWorkerEngine = nil
	defer func() { ActiveWorkerEngine = prev }()

	m := gatedDispatchModel(t, &mockProvider{
		responses: []*ai.Response{{Content: "x"}},
	}, nil)
	m.resolver.Set(modes.ModeInvestigate)
	m.state = StateChat
	m.sess.ContextLedger = nil
	m.handoffLedgerContent = ""
	m.handoffCtx.LastFailurePayload = ""
	m.handoffCtx.ProposedFix = ""

	cmd := m.handleMessageContent("hi")
	if cmd == nil {
		t.Fatal("short INVESTIGATE prompt returned nil — it must dispatch the investigate engine via the worker proposal path")
	}
	joined := recordsText(m)
	if strings.Contains(joined, "No handoff context in ledger") {
		t.Fatalf("short prompt hit the removed handoff early-return: %q", joined)
	}
}
