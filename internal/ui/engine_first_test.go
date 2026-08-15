package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

// engineFirstHarness builds a model over a temp workspace with the given files
// and returns it rooted in that workspace, ready for $prompt routing tests.
func engineFirstHarness(t *testing.T, files map[string]string) (*model, string) {
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
	// The read-only /ask chat path touches the execution engine's stream
	// context files seam; production always injects it.
	m.execEng = execution.NewEngine(dir, m.cfg, m.sess)
	return m, dir
}

// TestEngineFirstPromptSimpleTargeted verifies the core Phase 10 scenario (spec
// scenario A): a simple $prompt with one known file must NOT enter /investigate
// or /plan, must NOT call the ask-handoff LLM with zero context, and must route
// to the bounded mutation executor with the strategy recorded before any model
// call.
func TestEngineFirstPromptSimpleTargeted(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<html><body><p>one</p><p>two</p></body></html>",
	})

	cmd := m.routePromptDirective("fix extra contents in @index.html")
	if cmd == nil {
		t.Fatal("routePromptDirective returned nil — targeted mutation did not dispatch")
	}

	// Strategy decision recorded BEFORE any model call.
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
	if p.HasContext(strategy.ContextDependencyEvidence) {
		t.Fatal("single-file mutation must NOT require dependency evidence")
	}
	if p.Artifact.Kind != "replace_block" {
		t.Fatalf("artifact = %s, want bounded replace_block", p.Artifact.Kind)
	}
	if p.MaxOutputTokens <= 0 {
		t.Fatalf("MaxOutputTokens = %d, want a positive bounded budget", p.MaxOutputTokens)
	}

	// The engine-first router opened the mutation executor and set the
	// adaptive budget for it.
	if m.resolver.Current() != modes.ModeBuild {
		t.Errorf("mode = /%s, want /build after targeted mutation routing", m.resolver.Current())
	}
	if m.activeStrategyBudget != p.MaxOutputTokens {
		t.Errorf("activeStrategyBudget = %d, want %d", m.activeStrategyBudget, p.MaxOutputTokens)
	}
	if m.hotfixBranding != "PROMPT" {
		t.Errorf("hotfixBranding = %q, want PROMPT", m.hotfixBranding)
	}

	// It must NOT have entered the /ask handoff LLM path.
	for _, r := range m.records {
		if strings.Contains(r.text, "Refining prompt through ask handoff") {
			t.Errorf("$prompt entered the ask handoff (zero-context LLM) instead of targeted mutation: %q", r.text)
		}
		if strings.Contains(r.text, "Forward to /investigate") {
			t.Errorf("$prompt produced a /investigate action chip for a simple targeted task: %q", r.text)
		}
	}
	// It must have dispatched the bounded mutation executor.
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "[PROMPT] Urgent hotfix:") && strings.Contains(r.text, "@index.html") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected the bounded mutation executor to receive the target and goal")
	}
}

// TestEngineFirstPromptDeterministicCreate verifies spec scenario B: a task the
// engine can solve deterministically stages hardcoded tasks with ZERO model
// invocations.
func TestEngineFirstPromptDeterministicCreate(t *testing.T) {
	m, _ := engineFirstHarness(t, nil)

	cmd := m.routePromptDirective("create a .gitignore file")
	if cmd == nil {
		t.Fatal("routePromptDirective returned nil for a deterministic create")
	}
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.DirectDeterministic {
		t.Fatalf("strategy = %s, want direct_deterministic (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.ModelRequired {
		t.Fatal("ModelRequired = true, want false — zero model invocations")
	}
	if !p.Deterministic {
		t.Fatal("Deterministic = false, want true")
	}

	msg := cmd()
	pr, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want planResultMsg", msg)
	}
	if !pr.IsFastTrack {
		t.Fatal("deterministic create must fast-track (no approval gate for template create)")
	}
	if !pr.EngineFirst {
		t.Fatal("deterministic create must be marked engine-first")
	}
	if len(pr.Tasks) == 0 {
		t.Fatal("deterministic create staged no tasks")
	}
	if pr.Tasks[0].Target == "" {
		t.Fatal("deterministic create task has no target")
	}
}

// TestEngineFirstPromptUnresolvedTarget verifies spec scenarios E and L: an
// unresolved file target stops before any model call and any mutation, with no
// mode transition — human clarification is the terminal outcome.
func TestEngineFirstPromptUnresolvedTarget(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<html></html>",
	})

	cmd := m.routePromptDirective("remove the footer from @missing.html")
	if cmd != nil {
		t.Fatal("unresolved target returned a cmd — must stop before any execution")
	}
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.ModelRequired {
		t.Fatal("ModelRequired = true, want false — no model invocation for an unresolved target")
	}
	if !p.Escalation {
		t.Fatal("Escalation = false, want true (human clarification)")
	}
	if m.resolver.Current() != modes.ModeAsk {
		t.Errorf("mode = /%s, want /ask (no transition on clarification)", m.resolver.Current())
	}
	// No mutation executor was entered.
	if m.hotfixActive {
		t.Error("hotfixActive = true, want false — no mutation began")
	}
}

// TestEngineFirstPromptAmbiguousTarget verifies a target that resolves to
// multiple candidates stops before the model.
func TestEngineFirstPromptAmbiguousTarget(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"src/index.html":    "<html></html>",
		"public/index.html": "<html></html>",
	})

	cmd := m.routePromptDirective("fix the layout in @index.html")
	if cmd != nil {
		t.Fatal("ambiguous target returned a cmd — must stop before execution")
	}
	if p := m.lastExecutionStrategy; p.Strategy != strategy.HumanClarification {
		t.Fatalf("strategy = %s, want human_clarification (reason: %s)", p.Strategy, p.StrategyReason)
	}
}

// TestEngineFirstPromptRepositoryInvestigation verifies spec scenario G: a
// broad diagnostic request selects the repository-investigation strategy and
// falls through to the existing ask-handoff path — investigate/plan are entered
// because the strategy chose them, never because a mode exists.
func TestEngineFirstPromptRepositoryInvestigation(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"main.go": "package main\nfunc main() {}\n",
	})

	cmd := m.routePromptDirective("why is the build failing")
	if cmd == nil {
		t.Fatal("diagnostic $prompt must fall through to the ask-handoff path")
	}
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.RepositoryInvestigation {
		t.Fatalf("strategy = %s, want repository_investigation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if !p.ModelRequired {
		t.Fatal("ModelRequired = false, want true")
	}
	if p.Artifact.Kind != "investigation" {
		t.Fatalf("artifact = %s, want investigation", p.Artifact.Kind)
	}
	// The strategy selected investigate/plan; the fall-through path owns them.
	if m.resolver.Current() != modes.ModeAsk {
		t.Errorf("mode = /%s, want /ask fall-through", m.resolver.Current())
	}
}

// TestEngineFirstPromptTargetedReasoning verifies a read-only understanding
// request with an explicit target routes to /ask chat (not a mutation path).
func TestEngineFirstPromptTargetedReasoning(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"auth.go": "package auth\n",
	})

	cmd := m.routePromptDirective("explain the login flow in @auth.go")
	if cmd == nil {
		t.Fatal("targeted reasoning $prompt returned nil")
	}
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.TargetedReasoning {
		t.Fatalf("strategy = %s, want targeted_reasoning (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.Artifact.Kind != "explanation" {
		t.Fatalf("artifact = %s, want explanation", p.Artifact.Kind)
	}
	if m.hotfixActive {
		t.Error("hotfixActive = true, want false — read-only reasoning must not mutate")
	}
}

// TestEngineFirstPromptMultiFileEscalates verifies spec scenario D: a request
// naming multiple related files escalates the strategy (multi-file targeted
// mutation) and routes to the multi-file execution graph.
func TestEngineFirstPromptMultiFileEscalates(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"a.html": "<html></html>",
		"b.css":  "body {}\n",
	})

	cmd := m.routePromptDirective("restyle the page using @a.html and @b.css")
	if cmd == nil {
		t.Fatal("multi-file $prompt returned nil")
	}
	p := m.lastExecutionStrategy
	if p.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if got := p.FileCount(); got != 2 {
		t.Errorf("FileCount = %d, want 2", got)
	}
	if p.Complexity.Level == strategy.ComplexityLow {
		t.Errorf("complexity = low for a 2-file change, want medium+ (score=%d)", p.Complexity.Score)
	}
}

// TestEngineFirstBudgetAdaptive verifies the hotfix executor consumes the
// strategy-selected adaptive budget and falls back to the legacy fixed bound
// otherwise (spec scenario: reasoning budget follows task complexity).
func TestEngineFirstBudgetAdaptive(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<html></html>",
	})
	if got := m.hotfixOutputBudget(); got != 2048 {
		t.Errorf("default hotfixOutputBudget = %d, want 2048 (legacy fixed bound)", got)
	}

	m.activeStrategyBudget = 1024
	if got := m.hotfixOutputBudget(); got != 1024 {
		t.Errorf("adaptive hotfixOutputBudget = %d, want 1024", got)
	}
	m.clearEngineFirstMutationState()
	if got := m.hotfixOutputBudget(); got != 2048 {
		t.Errorf("budget after clear = %d, want 2048", got)
	}
}

// TestEngineFirstStrategyRenderable verifies the strategy decision renders a
// compact inspect record that answers "why did Izen call the model".
func TestEngineFirstStrategyRenderable(t *testing.T) {
	m, _ := engineFirstHarness(t, map[string]string{
		"index.html": "<html></html>",
	})
	m.routePromptDirective("remove the duplicate paragraph in @index.html")
	out := renderExecutionStrategy(m.lastExecutionStrategy)

	for _, want := range []string{"strategy=targeted_mutation", "complexity=", "model=yes", "decision=", "artifact=replace_block", "context="} {
		if !strings.Contains(out, want) {
			t.Errorf("renderExecutionStrategy missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "reasoning") && m.lastExecutionStrategy.ReasoningBudget == 0 {
		t.Error("reasoning-budget rendered as 0 while budget is set")
	}
	// The record must never claim a mutation happened — it is a decision record.
	if strings.Contains(out, "apply=") {
		t.Error("strategy record must not claim apply evidence")
	}
}
