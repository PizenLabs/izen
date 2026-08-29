package autonomy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/preflight"
	"github.com/PizenLabs/izen/internal/loop"
)

// ── REGRESSION: PREFLIGHT FAILURE MUST BE A TYPED, RECOVERABLE RUNTIME STATE ──
//
// The scenario these tests pin:
//
//	$prompt check this file @index.html and remove redandant content
//	target = index.html (~7780 bytes, corrupt AST), max_output = 2048
//	full-rewrite estimate = 5835 tokens → preflight_infeasible
//
// The runtime must:
//   1. NOT enter executing / verifying (control-plane outcome, never an
//      execution result).
//   2. Emit a typed HumanBoundaryProposal payload BEFORE parking.
//   3. Park at awaiting_human with a renderable DecisionSurface.
//   4. Recover ONLY through an explicit human choice that creates a NEW
//      execution contract and re-runs preflight.

// e2eCorruptFixture renders a ~7780-byte, structurally CORRUPT HTML document:
// an unterminated <script> element makes the deterministic validator flag the
// whole target as ASTCorrupt. The full-rewrite estimate
// (bytes/4 × FullRewriteTokenMultiplier) exceeds a 2048-token budget.
func e2eCorruptFixture() []byte {
	const targetSize = 7780
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>Broken</title></head>\n<body>\n")
	b.WriteString("<script>\n  console.log('under construction');\n")
	section := "<section id=\"s%d\"><p>lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor %d</p></section>\n"
	for i := 0; b.Len() < targetSize-40; i++ {
		fmt.Fprintf(&b, section, i, i)
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// patchProvider synthesizes ONE valid anchored SEARCH/REPLACE patch per call.
// The patch CLOSES the unterminated <script> element, so the explicitly
// authorized bounded textual mutation both anchors cleanly and leaves a
// structurally valid document (the artifact gate passes).
type patchProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *patchProvider) Name() string { return "patch" }

func (p *patchProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &ai.Response{
		Content: "<<<<<<< SEARCH\n  console.log('under construction');\n=======\n  console.log('under construction');\n</script>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 20, FinishReason: "stop"},
	}, nil
}

func (p *patchProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

func (p *patchProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// busCollector captures every published event for projection assertions.
type busCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func (c *busCollector) add(ev events.DomainEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *busCollector) hasType(typ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() == typ {
			return true
		}
	}
	return false
}

// waitType polls until an event of the given type arrives (the bus dispatches
// asynchronously) or the timeout expires.
func (c *busCollector) waitType(typ string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.hasType(typ) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c.hasType(typ)
}

func (c *busCollector) decisionSurfacePayload() (events.DecisionSurfacePayload, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if ev.Type() == events.EventDecisionSurface {
			p, ok := ev.Payload().(events.DecisionSurfacePayload)
			if ok {
				return p, true
			}
		}
	}
	return events.DecisionSurfacePayload{}, false
}

// waitDecisionSurface polls until a decision.surface payload arrives.
func (c *busCollector) waitDecisionSurface(d time.Duration) (events.DecisionSurfacePayload, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p, ok := c.decisionSurfacePayload(); ok {
			return p, true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c.decisionSurfacePayload()
}

func (c *busCollector) lifecycleStates() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var states []string
	for _, ev := range c.events {
		switch ev.Type() {
		case events.EventDecisionSurfaceCreated, events.EventDecisionSurfacePublished,
			events.EventDecisionSurfaceActivated, events.EventDecisionSurfaceResolved:
			if p, ok := ev.Payload().(events.DecisionSurfaceLifecyclePayload); ok {
				states = append(states, p.State)
			}
		}
	}
	return states
}

// ── A. Zero-token proposal ───────────────────────────────────────────────────

// TestZeroTokenDecisionSurfaceEmitsProposal pins the PRIMARY root cause: a
// zero-token preflight failure must produce a typed HumanBoundaryProposalMsg
// (the decision.surface payload) with actionable recovery BEFORE the runtime
// parks at awaiting_human. Zero LLM calls are required to build it.
func TestZeroTokenDecisionSurfaceEmitsProposal(t *testing.T) {
	root := t.TempDir()
	source := e2eCorruptFixture()
	writeTarget(t, root, "index.html", string(source))

	bus := events.NewBus(events.DefaultBufferSize)
	collector := &busCollector{}
	bus.Subscribe(events.EventDecisionSurface, collector.add)
	bus.Subscribe(events.EventDecisionSurfaceCreated, collector.add)
	bus.Subscribe(events.EventDecisionSurfacePublished, collector.add)
	bus.Subscribe(events.EventDecisionSurfaceActivated, collector.add)

	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	prompt := "$prompt check this file @index.html and remove redandant content"
	term, err := driver.Run(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the DecisionSurface barrier", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}

	// The typed proposal payload was published BEFORE parking.
	payload, ok := collector.waitDecisionSurface(2 * time.Second)
	if !ok {
		t.Fatal("decision.surface typed payload was never emitted")
	}
	if payload.Target != "index.html" {
		t.Fatalf("payload target = %q, want index.html", payload.Target)
	}
	if len(payload.Options) == 0 {
		t.Fatal("published decision surface carries no actionable recovery options")
	}
	// The full-rewrite estimate must match the Boundary-2 accounting (no
	// keyword-faked multiplier).
	if payload.EstimatedTokens != (len(source)/4)*execution.FullRewriteTokenMultiplier {
		t.Fatalf("estimated tokens = %d, want %d", payload.EstimatedTokens, (len(source)/4)*execution.FullRewriteTokenMultiplier)
	}

	// The runtime surface is the same typed artifact, with actionable recovery.
	ds := driver.DecisionSurface()
	if ds == nil {
		t.Fatal("driver exposes no DecisionSurface while parked")
	}
	for _, want := range []ProposalIntent{ProposalRescopeBoundedPatch, ProposalInspect, ProposalCancel} {
		if !ds.Has(want) {
			t.Fatalf("DecisionSurface must offer %q", want)
		}
	}
	// The boundary carries the typed options (no log parsing needed).
	b := driver.Boundary()
	if b == nil || len(b.ProposalOptions) == 0 {
		t.Fatal("parked boundary must carry typed proposal options")
	}
	if b.Action != autonomy.HumanBoundaryProposal {
		t.Fatalf("boundary action = %q, want %q", b.Action, autonomy.HumanBoundaryProposal)
	}

	// The lifecycle events followed created → published → activated.
	states := collector.lifecycleStates()
	for _, want := range []string{"created", "published", "activated"} {
		if !containsString(states, want) {
			t.Fatalf("decision-surface lifecycle must include %q, got %v", want, states)
		}
	}
	// Zero provider calls: the DecisionSurface is zero-token.
	if p.count() != 0 {
		t.Fatalf("provider calls = %d, want 0 (zero-token DecisionSurface)", p.count())
	}
}

// ── B. Awaiting-human invariant ──────────────────────────────────────────────

// TestAwaitingHumanAlwaysHasDecisionSurface pins the invariant:
// runtime_state == awaiting_human ⇒ a pending typed decision surface exists
// (a renderable HumanBoundaryProposalMsg was published before parking).
func TestAwaitingHumanAlwaysHasDecisionSurface(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	bus := events.NewBus(events.DefaultBufferSize)
	collector := &busCollector{}
	bus.Subscribe(events.EventDecisionSurface, collector.add)

	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	// The invariant holds for EVERY awaiting_human park reached via preflight:
	// the typed payload exists on the bus AND the driver exposes the surface.
	if driver.DecisionSurface() == nil {
		t.Fatal("awaiting_human without a pending DecisionSurface — deadlock invariant violated")
	}
	if _, ok := collector.waitDecisionSurface(2 * time.Second); !ok {
		t.Fatal("awaiting_human without a published decision.surface payload")
	}
}

// ── C. No execution after preflight failure ─────────────────────────────────

// TestPreflightInfeasibleNeverEntersExecuting pins acceptance criterion 1: a
// corrupt-AST preflight failure must NEVER transition the loop into executing
// (and never into verifying). Zero provider calls, zero mutations.
func TestPreflightInfeasibleNeverEntersExecuting(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	p := &patchProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeExecuting {
			t.Fatalf("loop entered executing (%+v) after a preflight rejection", tr)
		}
		if tr.To == autonomy.RuntimeVerifying {
			t.Fatalf("loop entered verifying (%+v) after a preflight rejection", tr)
		}
	}
	if p.count() != 0 {
		t.Fatalf("provider calls = %d, want 0", p.count())
	}
	if got := readTarget(t, root, "index.html"); got == "" || !strings.Contains(got, "under construction") {
		t.Fatal("the workspace must be untouched by a rejected preflight")
	}
}

// ── D. No fake verification ─────────────────────────────────────────────────

// TestPreflightFailureDoesNotEnterVerifying pins acceptance criterion 2:
// preflight_infeasible is a CONTROL-PLANE outcome — it is never consumed as an
// execution result, so the loop never fabricates a verification.
func TestPreflightFailureDoesNotEnterVerifying(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	p := &patchProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeVerifying {
			t.Fatalf("history contains a transition to verifying (%+v) — fake verification of a control-plane rejection", tr)
		}
	}
	// The execution counters must reflect ZERO consumed executions.
	if driver.loop.Attempts() != 0 {
		t.Fatalf("loop attempts = %d, want 0 (a rejected preflight was never executed)", driver.loop.Attempts())
	}
}

// ── E. Barrier resolution ───────────────────────────────────────────────────

// TestPreflightFailureAlwaysResolvesBarrier pins acceptance criterion 5: every
// preflight attempt resolves its barrier EXACTLY once — success, infeasible
// (error), cancellation and timeout all unblock a waiter, and a double Notify
// is a no-op (the first result wins).
func TestPreflightFailureAlwaysResolvesBarrier(t *testing.T) {
	t.Run("success resolves exactly once", func(t *testing.T) {
		bar := preflight.NewBarrier()
		done := make(chan struct{})
		var snap *preflight.StructuralSnapshot
		var err error
		go func() {
			snap, err = bar.Wait(context.Background())
			close(done)
		}()
		bar.Notify(&preflight.StructuralSnapshot{Target: "index.html"}, nil)
		bar.Notify(&preflight.StructuralSnapshot{Target: "index.html"}, nil) // double resolve must be a no-op
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waiter never unblocked after Notify")
		}
		if err != nil || snap == nil || snap.Target != "index.html" {
			t.Fatalf("success result = (%v, %v), want the first snapshot", snap, err)
		}
	})

	t.Run("infeasible error resolves waiter", func(t *testing.T) {
		bar := preflight.NewBarrier()
		done := make(chan struct{})
		var err error
		go func() {
			_, err = bar.Wait(context.Background())
			close(done)
		}()
		bar.Notify(nil, execution.ErrPreflightInfeasible)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waiter never unblocked after infeasible Notify")
		}
		if !errors.Is(err, execution.ErrPreflightInfeasible) {
			t.Fatalf("err = %v, want ErrPreflightInfeasible", err)
		}
	})

	t.Run("timeout resolves waiter", func(t *testing.T) {
		bar := preflight.NewBarrierWithTimeout(20 * time.Millisecond)
		done := make(chan struct{})
		var err error
		go func() {
			_, err = bar.Wait(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waiter never unblocked on timeout")
		}
		if !errors.Is(err, preflight.ErrPreflightTimeout) {
			t.Fatalf("err = %v, want ErrPreflightTimeout", err)
		}
	})

	t.Run("cancellation resolves waiter", func(t *testing.T) {
		bar := preflight.NewBarrier()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		var err error
		go func() {
			_, err = bar.Wait(ctx)
			close(done)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waiter never unblocked on cancellation")
		}
		if err == nil {
			t.Fatal("cancelled barrier must surface the cancellation error")
		}
	})

	t.Run("loop barrier forwards preflight failure", func(t *testing.T) {
		// The loop.Barrier wrapper must surface a preflight failure to the
		// waiting driver (halt → awaiting_human), never leave it blocked.
		bus := events.NewBus(events.DefaultBufferSize)
		bar := loop.NewBarrier(bus)
		done := make(chan struct{})
		var err error
		go func() {
			_, err = bar.Wait(context.Background())
			close(done)
		}()
		bar.Notify(nil, execution.ErrPreflightInfeasible)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("loop barrier waiter never unblocked")
		}
		if !errors.Is(err, execution.ErrPreflightInfeasible) {
			t.Fatalf("loop barrier err = %v, want ErrPreflightInfeasible", err)
		}
	})
}

// ── F. Recovery creates a new contract ──────────────────────────────────────

// TestBoundedPatchRecoveryCreatesNewContract pins acceptance criterion 12: a
// rescope_bounded_patch recovery creates a NEW execution contract (the rejected
// contract is never mutated in place), re-runs preflight, and executes ONLY
// after the new contract's preflight succeeds.
func TestBoundedPatchRecoveryCreatesNewContract(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	p := &patchProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	collector := &busCollector{}
	bus.Subscribe(events.EventDecisionSurfaceResolved, collector.add)
	bus.Subscribe(events.EventAutonomousResumed, collector.add)

	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}

	// The human explicitly authorizes the bounded-patch recovery. This MUST
	// create a NEW contract: the request's recovery strategy flips to
	// bounded_patch (a material change the executor's admission resolves into a
	// NEW ContractID) and the bounded protocol skips the monolithic
	// full-rewrite preflight (feasible by construction).
	term, err := driver.ResumeWithProposal(context.Background(), string(ProposalRescopeBoundedPatch))
	if err != nil {
		t.Fatalf("ResumeWithProposal: %v", err)
	}
	if term != nil {
		t.Fatalf("recovery terminated (%+v) — the bounded contract must first park at the approval gate", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human at the approval gate", driver.State())
	}
	if driver.req.RecoveryStrategy != autonomy.StrategyBoundedPatch {
		t.Fatalf("recovery strategy = %q, want bounded_patch (new contract protocol)", driver.req.RecoveryStrategy)
	}
	if driver.req.ProposalIntent != string(ProposalRescopeBoundedPatch) {
		t.Fatalf("proposal intent = %q, want rescope_bounded_patch", driver.req.ProposalIntent)
	}
	// The new contract passed its preflight and EXECUTED: the provider was
	// called exactly once and the held patch now awaits explicit approval.
	if p.count() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (only the bounded contract executed)", p.count())
	}
	if b := driver.Boundary(); b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %+v, want the approval gate for the held patch", b)
	}

	// The human approves the held patch — the SAME execution applies (no
	// re-execution, idempotency).
	term, err = driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || !term.State.IsTerminal() {
		t.Fatalf("approval termination = %+v, want a terminal outcome", term)
	}
	// The workspace was mutated by the authorized bounded patch (the <script>
	// element is now closed — a structurally valid document).
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "</script>") {
		t.Fatal("the authorized bounded patch never landed")
	}
	// The surface lifecycle resolved and the run resumed.
	if !collector.hasType(events.EventDecisionSurfaceResolved) {
		t.Fatal("decision_surface.resolved was not emitted")
	}
	if !collector.hasType(events.EventAutonomousResumed) {
		t.Fatal("autonomous.resumed was not emitted")
	}
}

// TestRetryExplicitBudgetCreatesNewContract pins the retry_with_explicit_budget
// recovery: a NEW contract carries the explicitly authorized ceiling and the
// executor re-runs Boundary-2 under it.
func TestRetryExplicitBudgetCreatesNewContract(t *testing.T) {
	// A VALID, large target whose estimate exceeds the budget parks at the
	// DECOMPOSITION_PROPOSAL gate (valid-AST over-budget path) — the Decision
	// Surface recovery options apply to the closed-gate path, so drive the
	// surface build directly for the typed-contract assertion.
	root := t.TempDir()
	source := targetedPatchFixture() // 7780 bytes, structurally valid
	writeTarget(t, root, "index.html", string(source))

	eval := EvaluateScope(ScopeInput{
		Target:          "index.html",
		Content:         source,
		MaxOutputTokens: 2048,
		Root:            root,
		Subcommand:      "$prompt",
	})
	if eval.BudgetStatus != BudgetExceeded {
		t.Fatalf("fixture BudgetStatus = %s, want exceeded", eval.BudgetStatus)
	}
	surface := BuildDecisionSurface(eval, "$prompt")
	if !surface.Has(ProposalRetryExplicitBudget) {
		t.Fatal("a budget-infeasible valid-AST surface must offer retry_with_explicit_budget")
	}
	if surface.ExplicitBudget <= eval.EstimatedTokens {
		t.Fatalf("explicit budget = %d, want > estimate %d (the authorized ceiling must cover the estimate)", surface.ExplicitBudget, eval.EstimatedTokens)
	}
	opt := surface.Option(ProposalRetryExplicitBudget)
	if !strings.Contains(opt.Label, fmt.Sprintf("%d", surface.ExplicitBudget)) {
		t.Fatalf("retry option label %q must surface the explicit budget %d before selection", opt.Label, surface.ExplicitBudget)
	}
}

// ── G. AST corruption remains fail-closed ───────────────────────────────────

// TestCorruptASTDoesNotEnableStructuralDAG pins acceptance criterion 10: a
// corrupt AST never enables structural DAG decomposition. The only permitted
// recovery is an explicitly authorized bounded TEXTUAL patch — never a
// structural DAG — and even then the new contract re-runs preflight.
func TestCorruptASTDoesNotEnableStructuralDAG(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	p := &patchProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.Proposal() != nil {
		t.Fatal("corrupt AST staged a structural DAG — forbidden")
	}
	if driver.Plan() != nil {
		t.Fatal("corrupt AST staged a plan — forbidden")
	}
	ds := driver.DecisionSurface()
	if ds == nil {
		t.Fatal("no DecisionSurface parked for the corrupt baseline")
	}
	// The bounded textual patch is offered ONLY as an explicitly authorized
	// recovery option — never applied automatically.
	if !ds.Has(ProposalRescopeBoundedPatch) {
		t.Fatal("corrupt surface must offer the explicitly authorized bounded TEXTUAL patch")
	}
	if p.count() != 0 {
		t.Fatalf("provider calls = %d, want 0 before any authorization", p.count())
	}
}

// ── H. UI projection ────────────────────────────────────────────────────────

// TestDecisionSurfacePublishedToUI pins the event path: runtime → event bus →
// typed payload → UI-facing message, with NO reliance on log parsing. The typed
// decision.surface payload and the lifecycle events are the UI protocol.
func TestDecisionSurfacePublishedToUI(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	bus := events.NewBus(events.DefaultBufferSize)
	collector := &busCollector{}
	for _, typ := range []string{
		events.EventPreflightStarted,
		events.EventPreflightRejected,
		events.EventRecoveryClassified,
		events.EventRecoveryOptionsCreated,
		events.EventDecisionSurface,
		events.EventAutonomousParked,
	} {
		bus.Subscribe(typ, collector.add)
	}

	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, typ := range []string{
		events.EventPreflightStarted,
		events.EventPreflightRejected,
		events.EventRecoveryClassified,
		events.EventRecoveryOptionsCreated,
		events.EventDecisionSurface,
		events.EventAutonomousParked,
	} {
		if !collector.waitType(typ, 2*time.Second) {
			t.Fatalf("missing typed event %q on the bus", typ)
		}
	}
	payload, ok := collector.waitDecisionSurface(2 * time.Second)
	if !ok || payload.Reason == "" {
		t.Fatal("typed decision.surface payload must carry the true-cause reason")
	}
	if payload.ASTStatus != "corrupt" {
		t.Fatalf("payload ASTStatus = %q, want corrupt (true cause, not a parsed log line)", payload.ASTStatus)
	}
}

// ── 17. End-to-end reproduction ─────────────────────────────────────────────

// TestEndToEndPreflightDeadlockReproduction reproduces the EXACT observed case:
// index.html (~7780 bytes, corrupt AST), full-rewrite estimate ~5835, max_output
// 2048. The run must reject the full rewrite with NO provider call, NO mutation,
// NO fake verification, emit a typed proposal, park at awaiting_human with a
// renderable DecisionSurface, and recover ONLY via an explicit choice.
func TestEndToEndPreflightDeadlockReproduction(t *testing.T) {
	root := t.TempDir()
	source := e2eCorruptFixture()
	writeTarget(t, root, "index.html", string(source))

	bus := events.NewBus(events.DefaultBufferSize)
	collector := &busCollector{}
	bus.Subscribe(events.EventDecisionSurface, collector.add)

	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	prompt := "$prompt check this file @index.html and remove redandant content"
	term, err := driver.Run(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at awaiting_human", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	// NO provider call, NO mutation, NO fake verification.
	if p.count() != 0 {
		t.Fatalf("provider calls = %d, want 0 after the rejected preflight", p.count())
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeExecuting || tr.To == autonomy.RuntimeVerifying {
			t.Fatalf("history enters %s after a preflight rejection — control-plane outcome consumed as execution", tr.To)
		}
	}
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "under construction") {
		t.Fatal("workspace mutated by a rejected preflight")
	}
	// Typed proposal emitted; the UI-facing surface is renderable.
	if _, ok := collector.waitDecisionSurface(2 * time.Second); !ok {
		t.Fatal("no typed decision.surface payload emitted")
	}
	b := driver.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryProposal || len(b.ProposalOptions) == 0 {
		t.Fatalf("parked boundary must be a renderable HumanBoundaryProposal, got %+v", b)
	}
	// The human can select: bounded patch / inspect / abort.
	ds := driver.DecisionSurface()
	if ds == nil || !ds.Has(ProposalRescopeBoundedPatch) || !ds.Has(ProposalInspect) || !ds.Has(ProposalCancel) {
		t.Fatalf("DecisionSurface must offer bounded patch + inspect + abort, got %+v", ds)
	}

	// Inspect remains safe: zero execution, zero mutation, still parked.
	term, err = driver.ResumeWithProposal(context.Background(), string(ProposalInspect))
	if err != nil {
		t.Fatalf("ResumeWithProposal(inspect): %v", err)
	}
	if term != nil {
		t.Fatalf("inspect terminated (%+v) — inspect must remain parked", term)
	}
	if p.count() != 0 {
		t.Fatalf("provider calls after inspect = %d, want 0", p.count())
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after inspect = %s, want awaiting_human", driver.State())
	}

	// Bounded patch recovery: new contract, re-preflight, execution only after
	// the new preflight succeeds.
	term, err = driver.ResumeWithProposal(context.Background(), string(ProposalRescopeBoundedPatch))
	if err != nil {
		t.Fatalf("ResumeWithProposal(rescope_bounded_patch): %v", err)
	}
	if term != nil {
		t.Fatalf("recovery terminated (%+v) — the bounded contract must first park at the approval gate", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after recovery = %s, want awaiting_human at the approval gate", driver.State())
	}
	if p.count() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (only the authorized bounded contract executed)", p.count())
	}
	// The approved bounded patch applies; the run completes.
	term, err = driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || !term.State.IsTerminal() {
		t.Fatalf("approval termination = %+v, want a terminal outcome", term)
	}
	if got := readTarget(t, root, "index.html"); !strings.Contains(got, "</script>") {
		t.Fatal("the authorized bounded patch never landed")
	}
}

// ── abort cannot be resurrected by late async results ───────────────────────

// TestAbortNotResurrectedByLatePreflight pins acceptance criterion 14: a late
// asynchronous preflight result must never resurrect an aborted run.
func TestAbortNotResurrectedByLatePreflight(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(e2eCorruptFixture()))

	p := &patchProvider{}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	term, err := driver.Abort("operator abort")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("abort termination = %+v, want aborted", term)
	}
	// A second resume attempt after abort must fail (the loop is terminal), and
	// the state must stay aborted — a late proposal can never resurrect it.
	if _, err := driver.ResumeWithProposal(context.Background(), string(ProposalRescopeBoundedPatch)); err == nil {
		t.Fatal("resume after abort must be refused")
	}
	if driver.State() != autonomy.RuntimeAborted {
		t.Fatalf("state after late resume = %s, want aborted", driver.State())
	}
	if p.count() != 0 {
		t.Fatalf("provider calls = %d, want 0 (aborted run never executed)", p.count())
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func TestRecovery_RescopeBoundedPatch_MutatesContractAndPassesPreflight(t *testing.T) {
	root := t.TempDir()
	source := e2eCorruptFixture()
	if len(source) != 0 && len(source)/4*3 <= 2048 {
		t.Fatalf("fixture must be over budget under 3×: estimated %d", len(source)/4*3)
	}
	// Initial zero-token evaluation: full rewrite, corrupt AST, over budget → fail.
	eval := EvaluateScope(ScopeInput{
		Target:          "index.html",
		Content:         source,
		MaxOutputTokens: 2048,
		Root:            root,
	})
	if eval.ExecutionGate() {
		t.Fatal("initial Evaluate() must fail (corrupt AST & 5835>2048)")
	}
	if eval.ASTStatus != ASTCorrupt {
		t.Fatalf("initial ASTStatus = %s, want corrupt", eval.ASTStatus)
	}
	if eval.BudgetStatus != BudgetExceeded {
		t.Fatalf("initial BudgetStatus = %s, want exceeded", eval.BudgetStatus)
	}
	if eval.EstimatedTokens <= 2048 {
		t.Fatalf("initial estimated = %d, want >2048", eval.EstimatedTokens)
	}
	// Simulate human selecting rescope_bounded_patch via driver: the contract
	// must mutate to bounded patch (0.8×) and bypass AST, then pass.
	bus := events.NewBus(events.DefaultBufferSize)
	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	writeTarget(t, root, "index.html", string(source))
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)
	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	// Mutate contract via recovery.
	term, err := driver.ResumeWithProposal(context.Background(), "rescope_bounded_patch")
	if err != nil {
		t.Fatalf("ResumeWithProposal: %v", err)
	}
	// After bounded patch, preflight must be feasible: 1945*0.8=1556 <2048 and AST bypassed.
	// Check driver mutation fields.
	if driver.mutationStrategy != StrategyBoundedPatch {
		t.Fatalf("mutationStrategy = %v, want bounded_patch", driver.mutationStrategy)
	}
	if !driver.allowASTBypass {
		t.Fatal("AllowASTBypass must be true after bounded patch")
	}
	// Direct scope evaluation with mutated strategy must pass.
	mutated := EvaluateScope(ScopeInput{
		Target:               "index.html",
		Content:              source,
		MaxOutputTokens:      2048,
		Root:                 root,
		MutationStrategy:     StrategyBoundedPatch,
		AllowASTBypass:       true,
		ExplicitOutputBudget: 0,
	})
	if !mutated.ExecutionGate() || len(mutated.RequiredProposals) != 0 {
		t.Fatalf("mutated Evaluate() must pass: gate=%v proposals=%d AST=%s Budget=%s estimated=%d", mutated.ExecutionGate(), len(mutated.RequiredProposals), mutated.ASTStatus, mutated.BudgetStatus, mutated.EstimatedTokens)
	}
	if mutated.EstimatedTokens != int(float64(len(source)/4)*0.8) {
		t.Fatalf("bounded estimated = %d, want %d", mutated.EstimatedTokens, int(float64(len(source)/4)*0.8))
	}
	if mutated.EstimatedTokens > 2048 {
		t.Fatalf("bounded estimated %d must be <2048", mutated.EstimatedTokens)
	}
	// The driver should have proceeded to the approval gate (provider called once), not re-park at DecisionSurface.
	if term != nil {
		t.Fatalf("bounded patch recovery should park at approval, not terminate: %+v", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after bounded patch = %s, want awaiting_human at approval", driver.State())
	}
	if b := driver.Boundary(); b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary after bounded patch = %+v, want approval", b)
	}
}

func TestRecovery_RepairFirst_InjectsSyntheticGoal(t *testing.T) {
	root := t.TempDir()
	source := e2eCorruptFixture()
	writeTarget(t, root, "index.html", string(source))
	bus := events.NewBus(events.DefaultBufferSize)
	p := &patchProvider{}
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)
	if _, err := driver.Run(context.Background(), "$prompt check this file @index.html and remove redundant content"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	term, err := driver.ResumeWithProposal(context.Background(), "repair_first")
	if err != nil {
		t.Fatalf("ResumeWithProposal(repair_first): %v", err)
	}
	if driver.mutationStrategy != StrategySyntaxRepair {
		t.Fatalf("mutationStrategy = %v, want syntax_repair", driver.mutationStrategy)
	}
	if driver.syntheticSubGoal != "Inspect and repair closing tags/syntax in target file" {
		t.Fatalf("syntheticSubGoal = %q, want repair prompt", driver.syntheticSubGoal)
	}
	if !driver.allowASTBypass {
		t.Fatal("repair_first must set AllowASTBypass")
	}
	// Repair should also proceed to approval (not re-park at DecisionSurface) with bypass.
	if term != nil {
		t.Fatalf("repair_first recovery should park at approval, not terminate: %+v", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after repair_first = %s, want awaiting_human at approval", driver.State())
	}
	if b := driver.Boundary(); b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary after repair_first = %+v, want approval", b)
	}
	if p.count() != 1 {
		t.Fatalf("provider calls after repair_first = %d, want 1", p.count())
	}
	// Verify the synthetic goal propagates into scope evaluation as a finding and still passes with 0.5×.
	eval := EvaluateScope(ScopeInput{
		Target:           "index.html",
		Content:          source,
		MaxOutputTokens:  2048,
		Root:             root,
		MutationStrategy: driver.mutationStrategy,
		AllowASTBypass:   driver.allowASTBypass,
		SyntheticSubGoal: driver.syntheticSubGoal,
	})
	if !containsString(eval.Findings, "synthetic sub-goal: Inspect and repair closing tags/syntax in target file") {
		// Check via substring
		found := false
		for _, f := range eval.Findings {
			if strings.Contains(f, "Inspect and repair closing tags/syntax") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("synthetic sub-goal not found in findings: %v", eval.Findings)
		}
	}
	// 1945*0.5=972 <2048 so budget must pass and AST bypassed.
	if eval.BudgetStatus != BudgetWithinLimits {
		t.Fatalf("repair BudgetStatus = %s, want within_limits", eval.BudgetStatus)
	}
	if eval.ASTStatus != ASTValid {
		t.Fatalf("repair ASTStatus = %s, want valid (bypassed)", eval.ASTStatus)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
