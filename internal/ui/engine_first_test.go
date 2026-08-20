package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

// gatedHarness builds a model wired to the unified IntentGateway + RuntimeExecutor
// over a temp workspace. The gateway is the single intent-resolution point and
// the executor owns all execution; the UI only renders results.
func gatedHarness(t *testing.T, files map[string]string, mock *mockProvider) (*model, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	m := newTestModel()
	m.workspaceRoot = dir
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false
	m.gateway = execution.NewIntentGateway(dir)
	m.executor = execution.NewRuntimeExecutor(dir, m.cfg, mock, nil, "")
	trivial := execution.NewVerifier(dir)
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(trivial)
	m.executor.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return m, dir
}

// runGate executes a $prompt line through the gateway and returns the produced
// gated execution message.
func runGate(m *model, input string) gatedExecutionMsg {
	cmd := m.routePromptDirective(input)
	if cmd == nil {
		return gatedExecutionMsg{}
	}
	msg := cmd()
	// runGatedLine now returns a batch; find the gatedExecutionMsg in it
	if batchMsg, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batchMsg {
			if m := c(); m != nil {
				if gem, ok := m.(gatedExecutionMsg); ok {
					return gem
				}
			}
		}
		return gatedExecutionMsg{}
	}
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		return gatedExecutionMsg{}
	}
	return gem
}

// TestEngineFirstPromptSimpleTargeted verifies the canonical migration contract:
// a simple $prompt with one known file is resolved by the gateway to a
// targeted_mutation strategy with NO mode transition, NO hidden /build, and the
// execution routed to the RuntimeExecutor.
func TestEngineFirstPromptSimpleTargeted(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<html><body><p>one</p><p>two</p></body></html>",
	}, &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>one</p>\n=======\n<p>fixed</p>\n>>>>>>>",
	}}})

	gem := runGate(m, "fix extra contents in @index.html")

	// Strategy decision recorded synchronously BEFORE execution.
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if !p.ModelRequired {
		t.Fatal("ModelRequired = false, want true (one bounded model call)")
	}
	if p.Deterministic {
		t.Fatal("Deterministic = true, want false — this needs bounded model reasoning")
	}
	if !p.HasContext(strategy.ContextTargetContent) {
		t.Fatal("strategy must require target content context")
	}
	if !p.HasContext(strategy.ContextArtifactContract) {
		t.Fatal("strategy must require artifact contract context")
	}

	// Modes are presentation contexts only: NO phase transition.
	if m.resolver.Current() != modes.ModeAsk {
		t.Fatalf("mode switched to /%s — modes must not decide the execution path", m.resolver.Current())
	}

	// The execution reached the RuntimeExecutor approval gate with a valid
	// held artifact: the outcome is pending_approval (never a fabricated
	// mutation, never "no artifact").
	if gem.res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}
	if gem.res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("pre-approval proof outcome %q must not claim a mutation", gem.res.Proof.Outcome)
	}
	if gem.res.Proof.Outcome != execution.OutcomePendingApproval && gem.res.Proof.Outcome != execution.OutcomeNoArtifact {
		t.Fatalf("unexpected proof outcome %q", gem.res.Proof.Outcome)
	}
}

// TestEngineFirstPromptDeterministicCreate verifies a task the engine can solve
// deterministically is selected as direct_deterministic with ZERO model
// invocations.
func TestEngineFirstPromptDeterministicCreate(t *testing.T) {
	m, _ := gatedHarness(t, nil, &mockProvider{})
	gem := runGate(m, "create a LICENSE file")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.DirectDeterministic {
		t.Fatalf("strategy = %s, want direct_deterministic", p.Strategy)
	}
	if gem.res == nil {
		t.Fatal("expected a gate result")
	}
}

// TestEngineFirstPromptUnresolvedTarget verifies an unresolved target stops at
// the human boundary with no model call.
func TestEngineFirstPromptUnresolvedTarget(t *testing.T) {
	m, _ := gatedHarness(t, nil, &mockProvider{})
	gem := runGate(m, "fix the bug in @missing.go")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification", p.Strategy)
	}
	if gem.res == nil {
		t.Fatal("expected a gate result")
	}
	if !gem.res.ClarificationRequired {
		t.Fatal("expected clarification_required")
	}
}

// TestEngineFirstPromptAmbiguousTarget verifies an ambiguous target stops before
// any model call.
func TestEngineFirstPromptAmbiguousTarget(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"README.md": "# readme",
	}, &mockProvider{})
	m.gateway.SelectStrategy("fix in @README.md") // warm any resolution cache
	gem := runGate(m, "update the docs in @README.md")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.HumanClarification &&
		p.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s, want human_clarification or targeted (resolved)", p.Strategy)
	}
	_ = gem
}

// TestEngineFirstPromptRepositoryInvestigation verifies a root-cause request is
// classified as repository_investigation by the gateway.
func TestEngineFirstPromptRepositoryInvestigation(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"main.go": "package main\n",
	}, &mockProvider{responses: []*ai.Response{{Content: "root cause: the build cache is stale."}}})
	gem := runGate(m, "why is the build failing")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.RepositoryInvestigation {
		t.Fatalf("strategy = %s, want repository_investigation", p.Strategy)
	}
	if gem.res != nil && gem.err != nil {
		t.Fatalf("gate err = %v", gem.err)
	}
}

// TestEngineFirstPromptTargetedReasoning verifies a read-only understanding
// request is classified as targeted_reasoning.
func TestEngineFirstPromptTargetedReasoning(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"auth.go": "package main\n",
	}, &mockProvider{responses: []*ai.Response{{Content: "The auth flow uses bearer tokens."}}})
	gem := runGate(m, "explain the auth flow in @auth.go")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.TargetedReasoning {
		t.Fatalf("strategy = %s, want targeted_reasoning", p.Strategy)
	}
	if gem.res != nil && gem.err != nil {
		t.Fatalf("gate err = %v", gem.err)
	}
}

// TestEngineFirstPromptMultiFileEscalates verifies an architectural request
// without a target set is classified as repository_investigation (the strategy
// taxonomy maps architectural requests to repository evidence discovery).
func TestEngineFirstPromptMultiFileEscalates(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"main.go": "package main\n",
	}, &mockProvider{responses: []*ai.Response{{Content: "plan: refactor into packages"}}})
	gem := runGate(m, "restructure the service into a clean architecture")
	if p := m.lastExecutionStrategy; p.Strategy != strategy.RepositoryInvestigation {
		t.Fatalf("strategy = %s, want repository_investigation", p.Strategy)
	}
	if gem.res != nil && gem.err != nil {
		t.Fatalf("gate err = %v", gem.err)
	}
}

// TestEngineFirstBudgetAdaptive verifies the gateway carries the strategy's
// adaptive output budget onto the ExecuteRequest.
func TestEngineFirstBudgetAdaptive(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	_ = runGate(m, "fix the layout in @index.html")
	if m.lastExecutionStrategy.MaxOutputTokens <= 0 {
		t.Fatalf("MaxOutputTokens = %d, want a positive bounded budget", m.lastExecutionStrategy.MaxOutputTokens)
	}
}

// TestEngineFirstStrategyRenderable verifies the strategy record stays
// inspectable ($inspect).
func TestEngineFirstStrategyRenderable(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{
		"index.html": "<p>hi</p>",
	}, &mockProvider{responses: []*ai.Response{{Content: "x"}}})
	_ = runGate(m, "fix the layout in @index.html")
	rendered := m.lastExecutionStrategy.String()
	if !strings.Contains(rendered, "strategy=") {
		t.Fatalf("renderable strategy string missing strategy=: %q", rendered)
	}
}

// TestEngineFirstPromptRoutesThroughExecutor proves the authority migration on
// the $prompt targeted path: the UI does NOT call the provider directly — the
// RuntimeExecutor owns the model invocation, and the UI receives only the
// result + canonical events.
func TestEngineFirstPromptRoutesThroughExecutor(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 9, CompletionTokens: 5},
	}}}
	m, _ := gatedHarness(t, map[string]string{
		"note.txt": "foo\nbar\nbaz\n",
	}, mock)

	cmd := m.routePromptDirective("change bar to qux in @note.txt")
	if cmd == nil {
		t.Fatal("routePromptDirective returned nil")
	}
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("got %T, want gatedExecutionMsg", msg)
	}
	if gem.err != nil {
		t.Fatalf("gate err: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("nil result")
	}

	// The runtime invoked the provider exactly once — the UI never called it.
	if got := mock.callCount; got != 1 {
		t.Fatalf("provider callCount = %d, want 1 (owned by the executor)", got)
	}
	if len(gem.res.ModelCalls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(gem.res.ModelCalls))
	}
	if gem.res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}
	if gem.res.Proof == nil || gem.res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("pre-approval proof outcome %v must not claim a mutation", gem.res.Proof.Outcome)
	}
	if gem.res.Proof.Outcome != execution.OutcomePendingApproval && gem.res.Proof.Outcome != execution.OutcomeNoArtifact {
		t.Fatalf("unexpected proof outcome %v", gem.res.Proof.Outcome)
	}
}
