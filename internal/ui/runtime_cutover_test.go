package ui

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
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

// eventCollector subscribes to a set of event types on a bus and records which
// of them fired. Cross-subscription delivery is nondeterministic (concurrent
// dispatch goroutines), so assertions check by type, never by order.
type eventCollector struct {
	mu      sync.Mutex
	fired   map[string]bool
	subs    []*events.Subscription
}

func newEventCollector(bus *events.Bus, types ...string) *eventCollector {
	c := &eventCollector{fired: make(map[string]bool)}
	for _, typ := range types {
		sub := bus.Subscribe(typ, func(ev events.DomainEvent) {
			c.mu.Lock()
			c.fired[ev.Type()] = true
			c.mu.Unlock()
		})
		c.subs = append(c.subs, sub)
	}
	return c
}

func (c *eventCollector) has(typ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fired[typ]
}

func (c *eventCollector) Stop() {
	for _, s := range c.subs {
		s.Cancel()
	}
}

// TestRuntimeCutoverApproveAppliesThroughExecutor is the full-loop cutover
// proof: Execute → approval gate → Alt+A (authorize) → executor.Approve →
// PatchManager apply → verifier → canonical events. The UI never touches a
// provider or PatchManager; it authorizes the boundary and the runtime applies.
func TestRuntimeCutoverApproveAppliesThroughExecutor(t *testing.T) {
	t.Setenv("IZEN_RUNTIME_EXECUTOR", "1")
	dir := t.TempDir()
	t.Chdir(dir)
	// A fixture whose mutation preserves the file size (≥80% of the original),
	// matching the executor's bounded-mutation apply contract for existing
	// files.
	orig := "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n  <p>body</p>\n</body>\n</html>\n"
	if err := os.WriteFile("index.html", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n  <p>body</p>\n=======\n  <p>body updated</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}

	bus := events.NewBus(events.DefaultBufferSize)
	collected := newEventCollector(bus,
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventModelInvoked,
		events.EventArtifactProduced,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
	)
	defer collected.Stop()

	m := cutoverModel(t, mock)
	// Production-grade governance surface: real AuthorizationEngine + budget +
	// capabilities, and a deterministic trivial verifier (the compile-gate
	// would invoke the real Go toolchain).
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{},
		fakeCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	m.mutationBudget = budget.NewBudget(10, 1000, 100000, 3, 30*time.Minute, 10)
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	m.bus = bus
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, bus, "")
	trivial := execution.NewVerifier(".")
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(trivial)
	grantMutationCaps(m)

	cmd := m.handleInput("$prompt read @index.html and remove redundant content")
	gem := cmd().(gatedExecutionMsg)
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	res, _ := m.Update(gem)
	m2 := res.(*model)
	if m2.executorPendingPatchID == "" {
		t.Fatal("approval-held patch not staged")
	}

	// Alt+A approval: authorize the boundary and let the runtime apply.
	approveCmd := m2.runExecutorApproveCmd(m2.executorPendingPatchID)
	amsg := approveCmd()
	mr, ok := amsg.(executionResultMsg)
	if !ok {
		t.Fatalf("expected executionResultMsg from approve, got %T", amsg)
	}
	if mr.err != nil {
		t.Fatalf("approve failed: %v", mr.err)
	}
	if mr.res == nil || mr.res.Proof == nil {
		t.Fatal("approve returned no proof")
	}
	if !mr.res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("approve outcome = %q, want a successful mutation", mr.res.Proof.Outcome)
	}
	if len(mr.res.Mutations) == 0 {
		t.Fatal("approve must carry mutation evidence")
	}

	// The filesystem actually changed.
	onDisk, rerr := os.ReadFile("index.html")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(onDisk) == orig {
		t.Fatal("approve did not mutate the file")
	}

	// The canonical lifecycle events fired on the shared bus (UI projection
	// contract).
	want := []string{
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventModelInvoked,
		events.EventArtifactProduced,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		all := true
		for _, typ := range want {
			if !collected.has(typ) {
				all = false
				break
			}
		}
		if all {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, typ := range want {
		if !collected.has(typ) {
			t.Errorf("missing canonical lifecycle event %q on the cutover path", typ)
		}
	}
}