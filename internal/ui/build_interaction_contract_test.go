package ui

// ── REGRESSION: /build is an execution interaction contract ──────────────────
//
// OBSERVED (black-box trace): inside /build, an ordinary prompt
//
//	Create a new file named `zuru.md` with the content "Hello everyone".
//
// produced a planning/analysis "Prepared findings" result and mutated nothing;
// the user had to name $prompt explicitly.
//
// CAUSAL (first divergence): the /build interaction contract did not authorize
// execution. A plain goal inside /build parses with ScopeProvenance=ScopeNone
// (the parser only grants a scope for explicit $prompt/$hot directives), so
// runRuntimePrompt refused the mutation and fell back to runGatedLine, where the
// gateway downgraded the mutation to a read-only RepositoryInvestigation.
//
// The fix binds the runtime-resolved mutation scope (ScopeDynamic) from the
// /build contract itself, without collapsing /build into $prompt. This test
// drives the exact prompt through the real strategy gateway and executor.

import (
	"os"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

func TestBuildModeOrdinaryPromptIsAnExecutionRequest(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Unrelated markdown files: the observed trace discovered these. They must
	// not influence the explicitly named target.
	if err := os.WriteFile("README.md", []byte("# Sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testfile.md", []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel()
	m.state = StateChat
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	mock := &mockProvider{responses: []*ai.Response{{Content: "Hello everyone\n"}}}
	m.provider = mock
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(dir, m.cfg, mock, nil, "")
	m.resolver.Set(modes.ModeBuild)
	// No explicit directive bound a scope: this is the failing precondition.
	m.bindScopeProvenance(coredomain.ScopeNone)

	cmd := m.runRuntimePrompt("Create a new file named `zuru.md` with the content \"Hello everyone\".")
	if cmd == nil {
		t.Fatal("an ordinary /build prompt produced no execution command")
	}
	// The /build contract authorized the runtime-resolved mutation scope.
	if !m.sess.ScopeProvenance.AllowsMutation() {
		t.Fatalf("the /build interaction contract did not authorize mutation (scope=%v)", m.sess.ScopeProvenance)
	}
	// The authoritative strategy is a CREATE on the named target — never a
	// read-only investigation ("Prepared findings").
	if m.lastExecutionStrategy.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s, want %s (read-only downgrade is the defect)", m.lastExecutionStrategy.Strategy, strategy.TargetedMutation)
	}
	if m.lastExecutionStrategy.Artifact.Kind != "create_file" {
		t.Fatalf("artifact = %q, want create_file", m.lastExecutionStrategy.Artifact.Kind)
	}

	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("execution: %v", gem.err)
	}
	if gem.res == nil || len(gem.res.Targets) != 1 || gem.res.Targets[0] != "zuru.md" {
		t.Fatalf("execution targets = %+v, want [zuru.md]", gem.res)
	}
}

// TestBuildModePromptDoesNotOverrideHotfixScope proves the /build grant is NOT a
// blanket override of an explicit human-declared scope: when a $hot directive
// already bound the bounded scope, the /build contract leaves it untouched.
func TestBuildModePromptDoesNotOverrideHotfixScope(t *testing.T) {
	t.Chdir(t.TempDir())
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, &mockProvider{}, nil, "")
	// Simulate the $hot directive binding its declared scope before a follow-up
	// plain prompt is submitted on the same turn.
	ast, err := m.intentFromInput("$hot fix @index.html")
	if err != nil {
		t.Fatal(err)
	}
	m.bindScopeProvenance(ast.ScopeProvenance)
	if m.sess.ScopeProvenance != coredomain.ScopeDeclared {
		t.Fatalf("precondition scope = %v, want ScopeDeclared", m.sess.ScopeProvenance)
	}

	_ = m.runRuntimePrompt("apply that change now")
	if m.sess.ScopeProvenance != coredomain.ScopeDeclared {
		t.Fatalf("the /build contract replaced the human-declared $hot scope with %v", m.sess.ScopeProvenance)
	}
}
