package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
)

// TestProposalStaging_ProactiveCancellation streams an over-budget worker
// stream (1200 estimated tokens) into a buffer configured with
// EffectiveStepBudget = 1000. The guard must fire ErrProactiveBudgetExceeded
// at the buffer boundary when the real-time estimate reaches the trigger
// ceiling (~950 tokens), cancel the worker context with that exact cause, and
// discard the staging buffer with zero residue (no memory leak, zero patches).
func TestProposalStaging_ProactiveCancellation(t *testing.T) {
	const budget = 1000                               // EffectiveStepBudget
	wantCeiling := budget - maxCeilingReserve(budget) // 1000 - max(16,50) = 950

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	guard := NewStreamTokenGuard(budget, cancel)
	var emitted *ProactivelyTruncatedInfo
	buf := NewProposalStagingBuffer("step-1-of-2").
		WithTokenGuard(guard).
		OnProactivelyTruncated(func(info ProactivelyTruncatedInfo) {
			cp := info
			emitted = &cp
		})

	// 100 ASCII bytes ≈ 25 estimated tokens per chunk. 1200 tokens-worth = 48
	// chunks; the guard must halt the loop at 38 (estimate == 950).
	chunk := []byte(strings.Repeat("a", 100))
	fed := 0
	var err error
	for fed < 48 {
		_, err = buf.Write(chunk)
		fed++
		if err != nil {
			break
		}
	}
	if !errors.Is(err, ErrProactiveBudgetExceeded) {
		t.Fatalf("Write returned %v, want ErrProactiveBudgetExceeded", err)
	}
	if fed != 38 {
		t.Fatalf("guard fired on write %d, want 38 (estimate crosses 950)", fed)
	}

	// The worker step context is canceled with the EXACT proactive cause.
	if !errors.Is(context.Cause(ctx), ErrProactiveBudgetExceeded) {
		t.Fatalf("context.Cause = %v, want ErrProactiveBudgetExceeded", context.Cause(ctx))
	}
	// The cancellation is a budget cut, NOT a user abort.
	if errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatal("proactive cancellation must not alias user context.Canceled")
	}

	// Real-time estimate at cancellation ≈ trigger ceiling.
	if got := guard.EstimatedTokens.Load(); got != int64(wantCeiling) {
		t.Fatalf("EstimatedTokens at cancellation = %d, want %d", got, wantCeiling)
	}

	// The breaching chunk was never staged; prior chunks are still isolated
	// until FinalizeErr discards them.
	preDiscard := buf.Len()
	if preDiscard == 0 {
		t.Fatal("staging buffer should hold pre-ceiling chunks before finalize")
	}

	// Finalize maps the proactive cause to StepOutcomePartial (continuation).
	disp := buf.FinalizeErr(ErrProactiveBudgetExceeded)
	if disp.Outcome != StepOutcomePartial {
		t.Fatalf("FinalizeErr(ErrProactiveBudgetExceeded) outcome = %q, want partial", disp.Outcome)
	}
	if !disp.NeedsContinuation {
		t.Fatal("proactive partial must request scheduler continuation")
	}
	if !disp.Discarded {
		t.Fatal("proactive partial must discard the staging buffer")
	}
	if disp.Proposal != "" {
		t.Fatalf("partial proposal leaked %d bytes across the boundary", len(disp.Proposal))
	}
	if disp.Reason != ProactiveBudgetReason {
		t.Fatalf("partial reason = %q, want %q", disp.Reason, ProactiveBudgetReason)
	}

	// The event hook fired with the exact guard state at truncation.
	if emitted == nil {
		t.Fatal("OnProactivelyTruncated hook was not invoked")
	}
	if emitted.StepID != "step-1-of-2" {
		t.Fatalf("emitted StepID = %q", emitted.StepID)
	}
	if emitted.EstimatedTokens != wantCeiling {
		t.Fatalf("emitted EstimatedTokens = %d, want %d", emitted.EstimatedTokens, wantCeiling)
	}
	if emitted.TriggerCeiling != wantCeiling || emitted.Budget != budget {
		t.Fatalf("emitted ceiling/budget = %d/%d, want %d/%d", emitted.TriggerCeiling, emitted.Budget, wantCeiling, budget)
	}

	// Memory cleared cleanly: zero residue after discard.
	if buf.Len() != 0 {
		t.Fatalf("staging buffer residue = %d bytes after discard, want 0", buf.Len())
	}

	// Isolated workspace invariant: even with passing evidence a partial
	// commits nothing.
	sink := &countingSink{}
	n, outcome, err := CommitGate(disp, evidence.VerdictPassed, sink)
	if err != nil {
		t.Fatalf("CommitGate: %v", err)
	}
	if n != 0 || sink.patches != 0 {
		t.Fatalf("proactive partial committed %d patches to workspace, want 0", n)
	}
	if outcome != StepOutcomePartial {
		t.Fatalf("commit outcome = %q, want partial", outcome)
	}

	// Idempotency: a second FinalizeErr reports the same partial verdict and
	// emits no duplicate event.
	again := buf.FinalizeErr(ErrProactiveBudgetExceeded)
	if again.Outcome != StepOutcomePartial || !again.Discarded {
		t.Fatalf("re-finalize must stay partial/discarded, got %+v", again)
	}
}

// TestProposalStaging_GuardCeilingFormula verifies the trigger ceiling follows
// EffectiveStepBudget - max(16, 5% of budget) and stays strictly below Budget.
func TestProposalStaging_GuardCeilingFormula(t *testing.T) {
	cases := []struct {
		budget int
		want   int
	}{
		{1000, 950}, // 5% = 50 > 16 → ceiling 950
		{200, 184},  // 5% = 10 < 16 → ceiling 200-16 = 184
		{320, 300},  // 5% = 16 → ceiling 320-16 = 304? boundary check below
		{16, 1},     // reserve 16 swallows budget → floor of 1 keeps guard live
		{0, 0},      // non-positive budget disables enforcement
	}
	// 320: 5% = 16 → max(16,16)=16 → ceiling 304.
	cases[2].want = 304
	for _, c := range cases {
		guard := NewStreamTokenGuard(c.budget, nil)
		if guard.TriggerCeiling != c.want {
			t.Errorf("budget %d ceiling = %d, want %d", c.budget, guard.TriggerCeiling, c.want)
		}
		if c.budget > 0 && guard.TriggerCeiling >= c.budget {
			t.Errorf("budget %d ceiling %d must stay strictly below budget", c.budget, guard.TriggerCeiling)
		}
	}
}

// TestProposalStaging_NonBudgetCancelDistinction proves the deterministic
// classification contract: a USER cancellation (context.Canceled) aborts the
// step (StepOutcomeFailed, no continuation), while a PROACTIVE BUDGET
// cancellation (ErrProactiveBudgetExceeded) yields StepOutcomePartial and the
// scheduler continues. The two are never conflated.
func TestProposalStaging_NonBudgetCancelDistinction(t *testing.T) {
	// Side A: user cancellation aborts the task step.
	userCtx, userCancel := context.WithCancelCause(context.Background())
	userCancel(context.Canceled)
	_ = userCtx

	userBuf := NewProposalStagingBuffer("step-user-cancel")
	userBuf.AppendString("diff --git a/main.go b/main.go\n")
	userDisp := userBuf.FinalizeErr(context.Canceled)
	if userDisp.Outcome != StepOutcomeFailed {
		t.Fatalf("user cancellation outcome = %q, want failed (abort)", userDisp.Outcome)
	}
	if userDisp.NeedsContinuation {
		t.Fatal("user cancellation must NOT request continuation")
	}
	if !userDisp.Discarded || userDisp.Proposal != "" {
		t.Fatalf("user cancellation must discard cleanly, got %+v", userDisp)
	}

	// Side B: proactive budget cancellation continues the scheduler.
	budgetCtx, budgetCancel := context.WithCancelCause(context.Background())
	defer budgetCancel(nil)
	guard := NewStreamTokenGuard(1000, budgetCancel)
	budgetBuf := NewProposalStagingBuffer("step-budget-cancel").
		WithTokenGuard(guard)
	chunk := []byte(strings.Repeat("b", 400)) // ≈ 100 tokens per chunk
	for {
		if _, err := budgetBuf.Write(chunk); err != nil {
			break
		}
	}
	budgetDisp := budgetBuf.FinalizeErr(ErrProactiveBudgetExceeded)
	if budgetDisp.Outcome != StepOutcomePartial {
		t.Fatalf("budget cancellation outcome = %q, want partial (continue)", budgetDisp.Outcome)
	}
	if !budgetDisp.NeedsContinuation {
		t.Fatal("budget cancellation MUST request scheduler continuation")
	}
	if budgetDisp.Reason != ProactiveBudgetReason {
		t.Fatalf("budget cancellation reason = %q, want %q", budgetDisp.Reason, ProactiveBudgetReason)
	}

	// The cause discrimination is exact: a budget cut is not a user cancel and
	// a user cancel is not a budget cut.
	if errors.Is(context.Cause(budgetCtx), context.Canceled) {
		t.Fatal("budget cause was conflated with user cancellation")
	}
	if !errors.Is(context.Cause(budgetCtx), ErrProactiveBudgetExceeded) {
		t.Fatalf("budget cause = %v, want ErrProactiveBudgetExceeded", context.Cause(budgetCtx))
	}

	// A generic stream error also aborts (failed), never partial.
	genBuf := NewProposalStagingBuffer("step-generic-error")
	genBuf.AppendString("some bytes")
	genDisp := genBuf.FinalizeErr(errors.New("provider stream reset"))
	if genDisp.Outcome != StepOutcomeFailed || genDisp.NeedsContinuation {
		t.Fatalf("generic stream error must abort, got %+v", genDisp)
	}

	// Nil error = clean completion (complete path, committable).
	cleanBuf := NewProposalStagingBuffer("step-clean")
	cleanBuf.AppendString("complete diff")
	cleanDisp := cleanBuf.FinalizeErr(nil)
	if cleanDisp.Outcome != StepOutcomeComplete || cleanDisp.Proposal == "" {
		t.Fatalf("nil-error finalize must commit, got %+v", cleanDisp)
	}
}

// TestStreamTokenGuard_EstimateTokens pins the byte/word heuristic so the
// ceiling math in the enforcement test stays stable: ASCII collapses to ~4
// bytes/token, multibyte UTF-8 counts one token per rune, and empty input is 0.
func TestStreamTokenGuard_EstimateTokens(t *testing.T) {
	guard := NewStreamTokenGuard(1000, nil)
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii-100", strings.Repeat("a", 100), 25},
		{"ascii-400", strings.Repeat("b", 400), 100},
		{"ascii-8", "aaaaaaaa", 2},
		{"thin-ascii", "abc", 1},
		{"cjk", "里兹县自动化运行环境", 10}, // 10 runes → 10 tokens
		{"mixed", "diff --git a/main.go b/main.go\n", 8},
	}
	for _, c := range cases {
		if got := guard.EstimateTokens([]byte(c.in)); got != c.want {
			t.Errorf("%s: EstimateTokens(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}
