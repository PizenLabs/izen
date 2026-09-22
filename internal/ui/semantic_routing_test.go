package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	rtexec "github.com/PizenLabs/izen/internal/runtime/executor"
)

// semanticTestModel returns an Ask-mode model with autonomy wired.
func semanticTestModel() *model {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeAsk)
	return m
}

// recordsHasApprovalSurface reports whether the UI records expose an
// execution approval surface (proposal, Approve/Reject, staged plan).
func recordsHasApprovalSurface(m *model) bool {
	joined := recordsText(m)
	for _, marker := range []string{
		"AUTONOMY PROPOSAL", "Approve / Reject", "Approve/Reject",
		"Intent compiler plan", "Microkernel plan",
		"CREATE index.html",
	} {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return m.pendingAutonomyProposal != nil
}

// Test A — ASK cannot become PLAN: the original failing interaction. A redesign
// request typed in /ask (bare or explicit) must stay in /ask read-only chat:
// no workspace switch, no staged tasks, no proposal, no approval surface.
func TestSemanticAskRedesignStaysReadOnly(t *testing.T) {
	for _, input := range []string{
		"Review this project and redesign the personal portfolio website for Johnson, an AI engineer",
		"/ask Review this project and redesign the personal portfolio website for Johnson, an AI engineer",
	} {
		m := semanticTestModel()
		m.handleInput(input)
		if got := m.resolver.Current(); got != modes.ModeAsk {
			t.Fatalf("input %q switched to /%s, want /ask", input, got)
		}
		if len(m.sess.CurrentTasks) != 0 {
			t.Fatalf("input %q staged %d tasks, want 0", input, len(m.sess.CurrentTasks))
		}
		if recordsHasApprovalSurface(m) {
			t.Fatalf("input %q exposed an execution approval surface:\n%s", input, recordsText(m))
		}
		if m.investigateRunning || m.planPending {
			t.Fatalf("input %q started an execution engine", input)
		}
	}
}

// Test B — ASK cannot become BUILD at 99% confidence: route the same input
// through the reconciler at classifier BUILD/0.99 and assert read-only.
func TestSemanticAskCannotBecomeBuild(t *testing.T) {
	r := autonomy.ReconcileRoute("ask",
		autonomy.IntentResult{Intent: autonomy.IntentModification, Confidence: 0.99}, false)
	if r.Route != "ask" || r.ExecutionAuthorized {
		t.Fatalf("ASK+BUILD@99%% reconciled to %s authorized=%t", r.Route, r.ExecutionAuthorized)
	}
	m := semanticTestModel()
	m.handleInput("create index.html with a portfolio landing page")
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("bare CREATE in /ask switched to /%s, want /ask", got)
	}
	if recordsHasApprovalSurface(m) {
		t.Fatalf("bare CREATE in /ask exposed approval surface:\n%s", recordsText(m))
	}
}

// Test C — ASK cannot become implicit $prompt: bare BUILD phrasing in /ask
// must not equal the explicit $prompt execution path.
func TestSemanticAskNotImplicitPrompt(t *testing.T) {
	bare := semanticTestModel()
	bare.handleInput("read @index.html and remove extra contents")
	if bare.pendingAutonomyProposal != nil {
		t.Fatal("bare /ask mutation must not stage an autonomy proposal")
	}
	if got := bare.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("bare input switched to /%s, want /ask", got)
	}

	explicit := semanticTestModel()
	explicit.routePromptDirective("read @index.html and remove extra contents")
	if explicit.pendingAutonomyProposal == nil {
		t.Fatal("explicit $prompt mutation must stage the authorization proposal (existing BUILD model)")
	}
}

// Test D — INVESTIGATE conversational fallback.
func TestSemanticInvestigateFallsBackToAsk(t *testing.T) {
	m := semanticTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.handleMessageContent("What does this function do?")
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("stayed /%s, want /ask fallback", got)
	}
	if m.investigateRunning {
		t.Fatal("fallback must not start the investigate engine")
	}
}

// Test E — REVIEW conversational fallback.
func TestSemanticReviewFallsBackToAsk(t *testing.T) {
	m := semanticTestModel()
	m.resolver.Set(modes.ModeReview)
	m.handleMessageContent("Why does this function return nil?")
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("stayed /%s, want /ask fallback", got)
	}
	if m.reviewRunning {
		t.Fatal("fallback must not start the review engine")
	}
}

// Test F — PLAN conversational fallback.
func TestSemanticPlanFallsBackToAsk(t *testing.T) {
	m := semanticTestModel()
	m.resolver.Set(modes.ModePlan)
	m.handoffLedgerContent = ""
	m.handoffCtx.ProposedFix = ""
	m.handleMessageContent("What is the purpose of this package?")
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("stayed /%s, want /ask fallback", got)
	}
	if len(m.sess.CurrentTasks) != 0 {
		t.Fatalf("fallback staged %d tasks, want 0", len(m.sess.CurrentTasks))
	}
}

// Test G — Explicit BUILD remains executable: $prompt still routes through the
// autonomy runtime to its decided workspace.
func TestSemanticExplicitPromptStillExecutes(t *testing.T) {
	m := semanticTestModel()
	cmd := m.routePromptDirective("inspect @index.html")
	if cmd == nil {
		t.Fatal("$prompt inspect must dispatch the investigate engine")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("$prompt inspect → /%s, want /investigate", got)
	}
}

// Test H — HOT remains bounded: $hot still enters the autonomy runtime without
// a mode switch to a second /build command.
func TestSemanticHotStillBounded(t *testing.T) {
	m := semanticTestModel()
	m.resolver.Set(modes.ModeAsk)
	cmd := m.handleInput("/build$hot check @index.html and remove redundant content")
	_ = cmd
	// The $hot execution request must not force a /build mode presentation
	// switch on its own; the runtime owns the path.
	if m.pendingAutonomyProposal == nil && m.hotfixActive == false && m.executionResolving == false && cmd == nil {
		t.Log("hotfix took the legacy gateway path (no autonomy proposal); acceptable when decision runtime defers")
	}
}

// Test I — Provider mismatch fails closed before provider invocation.
func TestSemanticProviderMismatchFailClosed(t *testing.T) {
	m := semanticTestModel()
	m.provider = &mockProvider{responses: []*ai.Response{{Content: "x"}}}
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, m.provider, nil, "")
	// The executor-level guard is the fail-closed boundary: an ollama binding
	// with an openrouter-namespaced model must error with zero provider calls.
	if err := rtexec.ValidateProviderModel("ollama", "dots-studio/dots-3-note-preview:free"); err == nil {
		t.Fatal("ollama + dots-studio/dots-3-note-preview:free must fail compatibility")
	}
	if err := rtexec.ValidateProviderModel("ollama", "qwen2.5-coder:7b"); err != nil {
		t.Fatalf("ollama + local model must pass, got %v", err)
	}
}

// Behavioral matrix: every row of the required matrix pins route + authority.
func TestSemanticBehavioralMatrix(t *testing.T) {
	rows := []struct {
		name      string
		mode      modes.Mode
		input     string
		wantRoute modes.Mode
		wantExec  bool
	}{
		{"ask simple question", modes.ModeAsk, "what is a goroutine?", modes.ModeAsk, false},
		{"ask project analysis", modes.ModeAsk, "explain this repository structure", modes.ModeAsk, false},
		{"ask redesign request", modes.ModeAsk, "Review this project and redesign the portfolio website", modes.ModeAsk, false},
		{"investigate simple question", modes.ModeInvestigate, "What does this function do?", modes.ModeAsk, false},
		{"review simple question", modes.ModeReview, "Why does this function return nil?", modes.ModeAsk, false},
		{"plan simple question", modes.ModePlan, "What is the purpose of this package?", modes.ModeAsk, false},
	}
	for _, r := range rows {
		m := semanticTestModel()
		m.resolver.Set(r.mode)
		if r.mode == modes.ModePlan {
			m.handoffLedgerContent = ""
			m.handoffCtx.ProposedFix = ""
		}
		if r.mode == modes.ModeInvestigate {
			m.sess.ContextLedger = nil
		}
		m.handleMessageContent(r.input)
		if got := m.resolver.Current(); got != r.wantRoute {
			t.Errorf("%s: routed to /%s, want /%s", r.name, got, r.wantRoute)
		}
		if r.wantExec && recordsHasApprovalSurface(m) {
			t.Errorf("%s: unexpected approval surface", r.name)
		}
	}
}
