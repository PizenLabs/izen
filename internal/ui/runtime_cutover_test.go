package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Phase 1 cutover tests ─────────────────────────────────────────────────
//
// These tests pin the IZEN_RUNTIME_EXECUTOR rollback boundary:
//   - flag ON  → autonomy-decided BUILD mutations route through RuntimeExecutor
//     (Execute → approval gate → proposal dock), never the legacy build engine.
//   - flag OFF → the legacy mode-engine path is preserved verbatim.
//
// The flag is a migration mechanism, not a permanent execution mode.

// cutoverModel wires the full runtime composition surface a production TUI
// exposes to an autonomy-decided mutation: autonomy runtime, IntentGateway,
// RuntimeExecutor, provider, and the legacy execution engine (for the
// flag-off comparison path).
func cutoverModel(t *testing.T, mock *mockProvider) *model {
	t.Helper()
	m := autonomyAcceptanceModel()
	m.resolver.Set(modes.ModeBuild)
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	// Deterministic E2E: the compile-gate verifier would invoke the real Go
	// toolchain; disable it for the apply assertions these tests exercise.
	if m.execEng != nil && m.execEng.Patches != nil {
		m.execEng.Patches.SetVerifier(nil)
	}
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	return m
}

// grantMutationCaps authorizes the full BUILD capability vector so a mutation
// request auto-continues instead of gating on a proposal.
func grantMutationCaps(m *model) {
	if m.autonomy != nil {
		m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	}
}

// TestRuntimeCutoverFlagOnRoutesPromptMutationThroughExecutor is the core
// Phase 1 cutover proof: with IZEN_RUNTIME_EXECUTOR=1 a $prompt mutation
// crosses the RuntimeExecutor and stops at its approval gate with a held
// patch (PendingPatchID + staged proposal) — the UI never calls a provider or
// a PatchManager on this path.
func TestRuntimeCutoverFlagOnRoutesPromptMutationThroughExecutor(t *testing.T) {
	t.Setenv("IZEN_RUNTIME_EXECUTOR", "1")
	writeIndexFixture(t)
	fixed := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n</body>\n</html>\n"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```html\n" + fixed + "```",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m := cutoverModel(t, mock)
	grantMutationCaps(m)
	if m.executor == nil || m.gateway == nil {
		t.Fatal("cutover model must wire the runtime executor + gateway")
	}

	cmd := m.handleInput("$prompt read @index.html and remove redundant content")
	if cmd == nil {
		t.Fatal("granted $prompt mutation must dispatch an execution")
	}

	// The dispatched cmd must be the executor submission, and its terminal
	// message must be a gated execution result — never a legacy planResultMsg.
	msg := cmd()
	if _, ok := msg.(planResultMsg); ok {
		t.Fatal("flag-on cutover must not route through the legacy build staging (planResultMsg)")
	}
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("expected gatedExecutionMsg from the executor, got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("executor returned no result")
	}
	// The runtime stopped at the approval gate with a held patch.
	if gem.res.PendingPatchID == "" {
		t.Fatalf("executor must hold a patch at the approval gate, targets=%v strategy=%s", gem.res.Targets, gem.res.Strategy)
	}
	if gem.res.Strategy != string(strategy.TargetedMutation) {
		t.Fatalf("strategy = %s, want targeted_mutation (autonomy-decided mutation preserved)", gem.res.Strategy)
	}
	if mock.callCount == 0 {
		t.Fatal("the executor must have invoked the provider")
	}
	if len(gem.res.Targets) == 0 || gem.res.Targets[0] != "index.html" {
		t.Fatalf("execution target set = %v, want [index.html]", gem.res.Targets)
	}

	// ── AUTONOMY HANDOFF PRESERVATION (Step 6) ─────────────────────
	// The already-classified intent and its confidence travel into the
	// execution proof — never dropped between autonomy and execution.
	if gem.res.Proof == nil || gem.res.Proof.Intent != "modification" {
		t.Fatalf("execution proof must preserve the autonomy intent, got %+v", gem.res.Proof)
	}
	if gem.res.Proof.IntentConfidence == 0 {
		t.Error("execution proof must preserve the intent confidence")
	}
	if gem.res.Proof.TargetConfidence == 0 {
		t.Error("execution proof must preserve the target confidence")
	}
	if gem.res.Proof.Scope != "build" {
		t.Fatalf("execution proof scope = %q, want build", gem.res.Proof.Scope)
	}

	// ── BOUNDED EVIDENCE INJECTION (Step 5) ─────────────────────────
	// The deterministic evidence ledger crosses into the model as the
	// authoritative evidence contract — the model never re-discovers
	// deterministic facts from raw text.
	if len(mock.requests) == 0 {
		t.Fatal("provider must have received the execution request")
	}
	userContent := ""
	for _, m := range mock.requests[0].Messages {
		if m.Role == "user" {
			userContent += m.Content
		}
	}
	if !strings.Contains(userContent, "EVIDENCE LEDGER") {
		t.Errorf("mutation prompt missing the evidence ledger contract:\n%s", userContent)
	}
	if !strings.Contains(userContent, "Context Evidence Ledger") {
		t.Errorf("mutation prompt missing the compiled evidence:\n%s", userContent)
	}

	// Feeding the terminal result into the event loop stages the standard
	// proposal dock with the held patch ID routed to executor.Approve.
	res, _ := m.Update(gem)
	m2 := res.(*model)
	if m2.executorPendingPatchID == "" {
		t.Fatal("approval-held patch must be staged for RuntimeExecutor.Approve")
	}
	if len(m2.pendingProposals) == 0 || m2.pendingProposals[0].ID != gem.res.PendingPatchID {
		t.Fatalf("proposal dock must carry the executor-held patch, got %+v", m2.pendingProposals)
	}
}

// TestRuntimeCutoverFlagOffPreservesLegacyPath proves the rollback boundary:
// with the flag unset (default) the same input routes through the legacy build
// staging (planResultMsg) exactly as before the cutover.
func TestRuntimeCutoverFlagOffPreservesLegacyPath(t *testing.T) {
	writeIndexFixture(t)
	mock := &mockProvider{responses: []*ai.Response{{Content: "ok", TokenOutput: 5}}}
	m := cutoverModel(t, mock)
	grantMutationCaps(m)

	cmd := m.handleInput("$prompt read @index.html and remove redundant content")
	if cmd == nil {
		t.Fatal("flag-off granted mutation must dispatch the legacy builder")
	}
	msg := cmd()
	prm, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("flag-off must route through legacy build staging (planResultMsg), got %T", msg)
	}
	if len(prm.Tasks) == 0 || prm.Tasks[0].Target != "index.html" {
		t.Fatalf("legacy staged build target = %+v, want index.html", prm.Tasks)
	}
}

// TestRuntimeCutoverFlagOnRoutesHotThroughExecutor proves the fast-track/$hot
// requirement: a $hot execution request under the flag routes through the
// RuntimeExecutor (TargetedMutation), never a special legacy hotfix authority.
func TestRuntimeCutoverFlagOnRoutesHotThroughExecutor(t *testing.T) {
	t.Setenv("IZEN_RUNTIME_EXECUTOR", "1")
	writeIndexFixture(t)
	fixed := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n</body>\n</html>\n"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```html\n" + fixed + "```",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m := cutoverModel(t, mock)
	grantMutationCaps(m)

	cmd := m.handleInput("/build$hot check @index.html and remove redundant content")
	if cmd == nil {
		t.Fatal("granted $hot must dispatch an execution")
	}
	msg := cmd()
	gem, ok := msg.(gatedExecutionMsg)
	if !ok {
		t.Fatalf("$hot must route through the executor (gatedExecutionMsg), got %T", msg)
	}
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatal("$hot must stop at the executor approval gate with a held patch")
	}
	if mock.callCount == 0 {
		t.Fatal("$hot must invoke the provider through the executor")
	}
}

// TestRuntimeCutoverFlagOnAmbiguousTargetStaysExplicit proves ambiguity stays
// explicit on the cutover path: an unresolvable target surfaces the autonomy
// target-not-found diagnosis before any provider call.
func TestRuntimeCutoverFlagOnAmbiguousTargetStaysExplicit(t *testing.T) {
	t.Setenv("IZEN_RUNTIME_EXECUTOR", "1")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("README.md", []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{Content: "x"}}}
	m := cutoverModel(t, mock)
	grantMutationCaps(m)

	cmd := m.handleInput("$prompt remove redundant content from @index.html")
	if cmd != nil {
		t.Fatalf("missing target must stop before execution, got cmd %T", cmd)
	}
	logged := recordsText(m)
	if !strings.Contains(logged, "target not found") {
		t.Errorf("missing target must surface the not-found diagnosis:\n%s", logged)
	}
	if mock.callCount != 0 {
		t.Fatal("no provider call may precede the ambiguity diagnosis")
	}
}

// TestRuntimeCutoverFlagOnBuildCommandRoutesThroughExecutor proves the manual
// /build command (staged FILE_MUTATE plan) routes through the executor when
// the flag is on.
func TestRuntimeCutoverFlagOnBuildCommandRoutesThroughExecutor(t *testing.T) {
	t.Setenv("IZEN_RUNTIME_EXECUTOR", "1")
	writeIndexFixture(t)
	fixed := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n</body>\n</html>\n"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```html\n" + fixed + "```",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m := cutoverModel(t, mock)
	grantMutationCaps(m)
	// Stage a fast-track FILE_MUTATE plan like /plan would.
	m.sess.StageTaskList(&[]plan.Task{{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      "index.html",
		Description: "remove redundant content",
		Status:      "idle",
	}})

	cmd := m.handleInput("/build")
	if cmd == nil {
		t.Fatal("/build with staged FILE_MUTATE work must dispatch an execution")
	}
	// The auto-trigger batches the switch command + the executor submission.
	cmds := unwrapBatch(cmd)
	if len(cmds) == 0 {
		t.Fatal("no commands dispatched")
	}
	var executed bool
	for _, c := range cmds {
		if c == nil {
			continue
		}
		msg := c()
		if gem, ok := msg.(gatedExecutionMsg); ok {
			executed = true
			if gem.err != nil {
				t.Fatalf("executor failed: %v", gem.err)
			}
			if gem.res == nil || gem.res.PendingPatchID == "" {
				t.Fatal("/build must stop at the executor approval gate")
			}
		}
	}
	if !executed {
		t.Fatal("/build did not route through the executor (no gatedExecutionMsg)")
	}
}

// unwrapBatch invokes a tea.Batch command and returns its sub-commands. tea's
// Batch compacts to a single command when only one non-nil sub-command exists,
// so a non-batch message is wrapped back into a single-command slice.
func unwrapBatch(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch v := msg.(type) {
	case tea.BatchMsg:
		return v
	default:
		return []tea.Cmd{func() tea.Msg { return msg }}
	}
}