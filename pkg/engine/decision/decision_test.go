package decision

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

func mustAdd(t *testing.T, g *ir.ExecutionGraph, id string, kind ir.NodeKind, critical bool, deps ...string) {
	t.Helper()
	if err := g.AddNode(id, kind, critical, id, deps...); err != nil {
		t.Fatal(err)
	}
}

// pipelinePlan is a small DAG: a → b → c where a is a critical retryable LLM
// step, b is a non-critical deterministic file check, and c is a critical
// verify step. It exercises every decision path.
func pipelinePlan(t *testing.T) *ir.Plan {
	t.Helper()
	g := ir.NewGraph()
	mustAdd(t, g, "a", ir.KindLLM, true)
	mustAdd(t, g, "b", ir.KindFileCheck, false, "a")
	mustAdd(t, g, "c", ir.KindVerify, true, "b")
	return &ir.Plan{ID: "pipeline", Graph: g}
}

func failNode(t *testing.T, snap *ir.ExecutionSnapshot, id string, attempts int, signals ...ir.EnvSignal) {
	t.Helper()
	snap.NodeStates[id] = ir.StateFailed
	snap.AttemptCounts[id] = attempts
	obs := ir.ObservationPayload{NodeID: id, OK: false, Err: "boom", EnvSignals: signals}
	snap.LastObservation[id] = obs
}

func succeedNode(snap *ir.ExecutionSnapshot, id string) {
	snap.NodeStates[id] = ir.StateSuccess
	snap.LastObservation[id] = ir.ObservationPayload{NodeID: id, OK: true}
}

// TestDecideRetryWithinBudget: a retryable critical failure within the retry
// budget must produce a Retry directive with a computed backoff.
func TestDecideRetryWithinBudget(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine(WithRetryPolicy(RetryBudget{
		MaxAttempts: 2,
		Backoff:     func(attempt int) time.Duration { return time.Duration(attempt) * time.Millisecond },
	}))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveRetry {
		t.Fatalf("directive = %s, want retry", d.Directive)
	}
	if d.NodeID != "a" {
		t.Errorf("node = %q, want a", d.NodeID)
	}
	if d.Backoff != 2*time.Millisecond {
		t.Errorf("backoff = %v, want 2ms (attempt 2)", d.Backoff)
	}
}

// TestDecideRetryExhaustedSkipsToCriticalHandling: a retryable critical node
// whose budget is spent must not be retried again.
func TestDecideRetryExhaustedSkipsToCriticalHandling(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine(WithRetryPolicy(RetryBudget{MaxAttempts: 2}))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 2)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveAbort {
		t.Fatalf("directive = %s, want abort (critical, exhausted, no invalidation)", d.Directive)
	}
}

// TestDecideFileCheckNeverRetried verifies deterministic probes are never
// retried even when the retry budget is fresh — they self-heal by skipping.
func TestDecideFileCheckNeverRetried(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine(WithRetryPolicy(RetryBudget{MaxAttempts: 5}))
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "a")
	failNode(t, snap, "b", 1) // non-critical file check, within budget

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveContinue {
		t.Fatalf("directive = %s, want continue (skip)", d.Directive)
	}
	if len(d.Skip) != 1 || d.Skip[0] != "b" {
		t.Fatalf("skip = %v, want [b]", d.Skip)
	}
	if len(d.Dispatch) != 0 {
		t.Fatalf("dispatch = %v, want none", d.Dispatch)
	}
}

// TestDecideSkipNonCriticalWithoutRePlan is the canonical self-healing path:
// a missing optional file fails but the graph proceeds without any LLM
// re-planning.
func TestDecideSkipNonCriticalWithoutRePlan(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine()
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "a")
	failNode(t, snap, "b", 1)
	// c is pending, blocked on b.

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveContinue {
		t.Fatalf("directive = %s, want continue", d.Directive)
	}
	if len(d.Skip) != 1 || d.Skip[0] != "b" {
		t.Fatalf("skip = %v, want [b]", d.Skip)
	}
	if d.Reason == "" {
		t.Error("skip decision missing rationale")
	}
}

// TestDecideRePlanOnGraphInvalidation: when live tool output carries a graph
// invalidation signal, an exhausted critical failure forces a fresh plan.
func TestDecideRePlanOnGraphInvalidation(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine(WithRetryPolicy(RetryBudget{MaxAttempts: 1}))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1, ir.EnvSignal{Kind: ir.SignalGraphInvalidation, Name: "toolchain.moved"})

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveRePlan {
		t.Fatalf("directive = %s, want replan", d.Directive)
	}
	if d.NodeID != "a" {
		t.Errorf("node = %q, want a", d.NodeID)
	}
}

// TestDecideHumanApprovalGate: a ready destructive action requires a human.
func TestDecideHumanApprovalGate(t *testing.T) {
	g := ir.NewGraph()
	mustAdd(t, g, "probe", ir.KindEnvProbe, true)
	mustAdd(t, g, "mutation", ir.KindShell, true, "probe")
	if err := g.MarkApprovalRequired("mutation"); err != nil {
		t.Fatal(err)
	}

	plan := &ir.Plan{ID: "p", Graph: g}
	eng := NewStandardDecisionEngine()
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "probe") // mutation becomes ready

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveHumanApproval {
		t.Fatalf("directive = %s, want human_approval", d.Directive)
	}
	if d.NodeID != "mutation" {
		t.Errorf("node = %q, want mutation", d.NodeID)
	}
}

// TestDecideDispatchReadyBatch: a clean graph dispatches the next ready batch.
func TestDecideDispatchReadyBatch(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine()
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "a") // b becomes ready

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveContinue {
		t.Fatalf("directive = %s, want continue", d.Directive)
	}
	if len(d.Dispatch) != 1 || d.Dispatch[0] != "b" {
		t.Fatalf("dispatch = %v, want [b]", d.Dispatch)
	}
	if len(d.Skip) != 0 {
		t.Fatalf("skip = %v, want none", d.Skip)
	}
}

// TestDecideComplete: an all-success snapshot is terminal.
func TestDecideComplete(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine()
	snap := ir.NewExecutionSnapshot(plan)
	for _, id := range plan.Graph.IDs() {
		succeedNode(snap, id)
	}

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveContinue {
		t.Fatalf("directive = %s, want continue (terminal)", d.Directive)
	}
	if len(d.Dispatch) != 0 {
		t.Fatalf("dispatch = %v, want none for a complete graph", d.Dispatch)
	}
}

// TestDecideContextCancelled propagates cancellation.
func TestDecideContextCancelled(t *testing.T) {
	eng := NewStandardDecisionEngine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := eng.Decide(ctx, ir.NewExecutionSnapshot(pipelinePlan(t))); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// TestDecisionEngineDoesNotMutateSnapshot verifies the purity law: Decide
// operates on the ExecutionSnapshot without ever mutating it.
func TestDecisionEngineDoesNotMutateSnapshot(t *testing.T) {
	plan := pipelinePlan(t)
	eng := NewStandardDecisionEngine()
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "a")
	failNode(t, snap, "b", 1)

	before := snap.Clone()
	if _, err := eng.Decide(context.Background(), snap); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if _, err := eng.Decide(context.Background(), snap); err != nil {
		t.Fatalf("Decide (2nd): %v", err)
	}
	if !reflect.DeepEqual(snap, before) {
		t.Fatal("decision engine mutated the ExecutionSnapshot")
	}
}

// TestRetryPolicyBounds covers the backoff policy arithmetic.
func TestRetryPolicyBounds(t *testing.T) {
	p := RetryBudget{MaxAttempts: 3, Backoff: func(a int) time.Duration { return time.Duration(a) * time.Second }}
	for attempts, wantExhausted := range map[int]bool{0: false, 1: false, 2: false, 3: true, 4: true} {
		if got := p.Exhausted(attempts); got != wantExhausted {
			t.Errorf("Exhausted(%d) = %v, want %v", attempts, got, wantExhausted)
		}
	}
	if p.BackoffFor(2) != 2*time.Second {
		t.Errorf("BackoffFor(2) = %v, want 2s", p.BackoffFor(2))
	}

	single := RetryBudget{MaxAttempts: 1}
	if !single.Exhausted(1) {
		t.Error("MaxAttempts=1 must be exhausted after the first attempt")
	}

	none := RetryBudget{}
	if !none.Exhausted(0) {
		t.Error("zero-value retry policy must not allow retries")
	}
	if none.BackoffFor(1) != 0 {
		t.Error("nil backoff must return zero delay")
	}

	cap := DefaultRetryPolicy.BackoffFor(20)
	if cap > 8*time.Second {
		t.Errorf("default backoff exceeds 8s cap: %v", cap)
	}
}

// recordingRetryPolicy records every ShouldRetry invocation so tests can prove
// the Decision Engine delegates retry bounds to the injected strategy.
type recordingRetryPolicy struct {
	calls    []retryCall
	decision bool
}

type retryCall struct {
	attempt int
	err     string
}

func (p *recordingRetryPolicy) ShouldRetry(attempt int, err error) bool {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	p.calls = append(p.calls, retryCall{attempt: attempt, err: msg})
	return p.decision
}

func (p *recordingRetryPolicy) BackoffFor(attempt int) time.Duration {
	return time.Duration(attempt) * time.Millisecond
}

// recordingBudgetPolicy records every HasRemainingBudget invocation so tests
// can prove budget bounds are delegated, not computed inline.
type recordingBudgetPolicy struct {
	calls   []int
	allowed bool
}

func (p *recordingBudgetPolicy) HasRemainingBudget(tokens int) bool {
	p.calls = append(p.calls, tokens)
	return p.allowed
}

// TestDecideDelegatesRetryToInjectedPolicy proves the Decision Engine asks the
// injected RetryPolicy for the retry decision and honors the answer.
func TestDecideDelegatesRetryToInjectedPolicy(t *testing.T) {
	plan := pipelinePlan(t)
	rp := &recordingRetryPolicy{decision: true}
	eng := NewStandardDecisionEngine(WithRetryPolicy(rp))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveRetry {
		t.Fatalf("directive = %s, want retry (policy allowed)", d.Directive)
	}
	if d.NodeID != "a" {
		t.Errorf("node = %q, want a", d.NodeID)
	}
	if len(rp.calls) != 1 {
		t.Fatalf("ShouldRetry calls = %d, want exactly 1", len(rp.calls))
	}
	if rp.calls[0].attempt != 1 {
		t.Errorf("ShouldRetry attempt = %d, want 1 (one attempt already made)", rp.calls[0].attempt)
	}
	if rp.calls[0].err != "boom" {
		t.Errorf("ShouldRetry err = %q, want the observation error", rp.calls[0].err)
	}
	// The policy also implements BackoffProvider, so the computed backoff must
	// be consulted rather than hardcoded.
	if d.Backoff != 2*time.Millisecond {
		t.Errorf("backoff = %v, want 2ms from injected policy", d.Backoff)
	}
}

// TestDecideHonorsRetryPolicyVeto proves a RetryPolicy veto skips the retry
// and the engine falls through to critical-failure handling without inventing
// its own retry bound.
func TestDecideHonorsRetryPolicyVeto(t *testing.T) {
	plan := pipelinePlan(t)
	rp := &recordingRetryPolicy{decision: false}
	eng := NewStandardDecisionEngine(WithRetryPolicy(rp))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveAbort {
		t.Fatalf("directive = %s, want abort (critical, policy vetoed retry)", d.Directive)
	}
	if len(rp.calls) != 1 {
		t.Fatalf("ShouldRetry calls = %d, want exactly 1", len(rp.calls))
	}
}

// TestDecideDelegatesBudgetToInjectedPolicy proves the injected BudgetPolicy
// is consulted with the snapshot-derived token consumption before a retry is
// dispatched.
func TestDecideDelegatesBudgetToInjectedPolicy(t *testing.T) {
	plan := pipelinePlan(t)
	rp := &recordingRetryPolicy{decision: true}
	bp := &recordingBudgetPolicy{allowed: true}
	eng := NewStandardDecisionEngine(WithRetryPolicy(rp), WithBudgetPolicy(bp))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveRetry {
		t.Fatalf("directive = %s, want retry", d.Directive)
	}
	if len(bp.calls) != 1 {
		t.Fatalf("HasRemainingBudget calls = %d, want exactly 1", len(bp.calls))
	}
	if want := snapshotConsumption(snap); bp.calls[0] != want {
		t.Errorf("budget tokens = %d, want snapshot-derived %d", bp.calls[0], want)
	}
}

// TestDecideBudgetBlocksRetryAndDispatch proves an exhausted budget gates both
// the retry path and the next dispatch without any inline budget arithmetic.
func TestDecideBudgetBlocksRetryAndDispatch(t *testing.T) {
	plan := pipelinePlan(t)
	rp := &recordingRetryPolicy{decision: true}
	bp := &recordingBudgetPolicy{allowed: false}
	eng := NewStandardDecisionEngine(WithRetryPolicy(rp), WithBudgetPolicy(bp))
	snap := ir.NewExecutionSnapshot(plan)
	failNode(t, snap, "a", 1)

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveAbort {
		t.Fatalf("directive = %s, want abort (budget exhausted)", d.Directive)
	}
	if len(rp.calls) != 1 {
		t.Fatalf("ShouldRetry calls = %d, want exactly 1", len(rp.calls))
	}
	if len(bp.calls) != 1 {
		t.Fatalf("HasRemainingBudget calls = %d, want exactly 1", len(bp.calls))
	}
}

// TestDecideAbortsWhenBudgetExhaustedAtDispatch proves the injected
// BudgetPolicy gates the ready-batch dispatch.
func TestDecideAbortsWhenBudgetExhaustedAtDispatch(t *testing.T) {
	plan := pipelinePlan(t)
	bp := &recordingBudgetPolicy{allowed: false}
	eng := NewStandardDecisionEngine(WithBudgetPolicy(bp))
	snap := ir.NewExecutionSnapshot(plan)
	succeedNode(snap, "a") // b becomes ready

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveAbort {
		t.Fatalf("directive = %s, want abort (budget exhausted before dispatch)", d.Directive)
	}
	if len(d.Dispatch) != 0 {
		t.Fatalf("dispatch = %v, want none when budget is exhausted", d.Dispatch)
	}
	if len(bp.calls) != 1 {
		t.Fatalf("HasRemainingBudget calls = %d, want exactly 1", len(bp.calls))
	}
}

// TestDecideDispatchBypassesBudgetForCompleteGraph proves the terminal paths
// are not gated on the budget: a complete graph is terminal even under an
// exhausted budget.
func TestDecideDispatchBypassesBudgetForCompleteGraph(t *testing.T) {
	plan := pipelinePlan(t)
	bp := &recordingBudgetPolicy{allowed: false}
	eng := NewStandardDecisionEngine(WithBudgetPolicy(bp))
	snap := ir.NewExecutionSnapshot(plan)
	for _, id := range plan.Graph.IDs() {
		succeedNode(snap, id)
	}

	d, err := eng.Decide(context.Background(), snap)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Directive != DirectiveContinue {
		t.Fatalf("directive = %s, want continue (terminal)", d.Directive)
	}
	if len(bp.calls) != 0 {
		t.Fatalf("HasRemainingBudget calls = %d, want 0 for a terminal graph", len(bp.calls))
	}
}

// TestDefaultBudgetPolicyBounds covers the reference budget strategy.
func TestDefaultBudgetPolicyBounds(t *testing.T) {
	unlimited := DefaultBudgetPolicy{}
	for _, tokens := range []int{0, 1, 1_000_000, -5} {
		if !unlimited.HasRemainingBudget(tokens) {
			t.Errorf("unlimited budget rejected tokens=%d", tokens)
		}
	}

	limited := DefaultBudgetPolicy{MaxTokens: 100}
	if limited.HasRemainingBudget(100) {
		t.Error("budget of 100 must not have room at exactly 100 consumed")
	}
	if !limited.HasRemainingBudget(99) {
		t.Error("budget of 100 must have room at 99 consumed")
	}
}

// TestRetryBudgetImplementsContract proves the reference strategy honours the
// injected RetryPolicy contract semantics.
func TestRetryBudgetImplementsContract(t *testing.T) {
	p := RetryBudget{MaxAttempts: 3}
	for attempts, want := range map[int]bool{0: true, 1: true, 2: true, 3: false, 4: false} {
		if got := p.ShouldRetry(attempts, nil); got != want {
			t.Errorf("ShouldRetry(%d) = %v, want %v", attempts, got, want)
		}
	}
	none := RetryBudget{}
	if none.ShouldRetry(0, nil) {
		t.Error("zero-value budget must not allow retries")
	}
}
