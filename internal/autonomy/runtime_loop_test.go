package autonomy

import (
	"context"
	"errors"
	"testing"
)

// ── state machine ──────────────────────────────────────────────────────────

func TestRuntimeLoop_HappyPath(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())

	if got := l.Start("objective"); got != RuntimeObserving {
		t.Fatalf("Start = %s, want observing", got)
	}

	// Observe → decide → execute → verify → interpret → complete.
	obs := Observation{Outcome: OutcomeArtifactProduced, Verification: VerificationOutcome{Passed: true}}
	if got := l.Observe(obs); got != RuntimeDeciding {
		t.Fatalf("observe = %s, want deciding", got)
	}
	if got, err := l.Step(ctx, LoopDecision{Action: LoopContinue, Reason: "go"}); err != nil || got != RuntimeExecuting {
		t.Fatalf("decide step = %s, err=%v; want executing", got, err)
	}
	if got := l.ConsumeExecution(obs); got != RuntimeVerifying {
		t.Fatalf("consume execution = %s, want verifying", got)
	}
	if got := l.ConsumeVerification(obs); got != RuntimeInterpreting {
		t.Fatalf("consume verification = %s, want interpreting", got)
	}
	state, err := l.Step(ctx, LoopDecision{Action: LoopComplete, Reason: "satisfied"})
	if err != nil {
		t.Fatalf("complete step err = %v", err)
	}
	if state != RuntimeCompleted {
		t.Fatalf("final state = %s, want completed", state)
	}
	if !l.State().IsTerminal() {
		t.Fatal("loop must be terminal after complete")
	}
	if term := l.Termination(); term == nil || term.State != RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if len(l.History()) == 0 {
		t.Fatal("history must record transitions")
	}
}

func TestRuntimeLoop_StepAfterTerminalRejected(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")

	l.Observe(Observation{})
	_, _ = l.Step(ctx, LoopDecision{Action: LoopAbort, Reason: "stop"})

	if _, err := l.Step(ctx, LoopDecision{Action: LoopContinue}); err == nil {
		t.Fatal("step on a terminal loop must be rejected")
	}
}

func TestRuntimeLoop_InvalidDecisionRejected(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")

	if _, err := l.Step(ctx, LoopDecision{Action: LoopAction("bogus")}); err == nil {
		t.Fatal("invalid decision action must be rejected")
	}
	if l.State() != RuntimeObserving {
		t.Fatalf("state after rejected decision = %s, want observing", l.State())
	}
}

func TestRuntimeLoop_IllegalTransitionRejected(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")

	// From Observing (before Observe) no decision is legal.
	if _, err := l.Step(ctx, LoopDecision{Action: LoopContinue}); err == nil {
		t.Fatal("continue from observing must be rejected")
	}
}

// executeCycle runs one full observe→decide→execute→verify→interpret cycle
// from whatever decision position the loop is at (Observing on the first
// cycle, Interpreting/Recovering on repeats) and returns the resulting state
// (Interpreting unless a bound aborted the loop).
func executeCycle(t *testing.T, ctx context.Context, l *RuntimeLoop, obs Observation, d LoopDecision) RuntimeState {
	t.Helper()
	switch l.State() {
	case RuntimeObserving:
		if got := l.Observe(obs); got != RuntimeDeciding {
			t.Fatalf("observe = %s, want deciding", got)
		}
	case RuntimeInterpreting, RuntimeRecovering:
		// already at a decision position
	default:
		t.Fatalf("unexpected start state %s", l.State())
	}
	if got, err := l.Step(ctx, d); err != nil || got != RuntimeExecuting {
		t.Fatalf("decide = %s, err=%v; want executing", got, err)
	}
	l.ConsumeExecution(obs)
	l.ConsumeVerification(obs)
	return l.State()
}

// ── bounds (runtime-owned termination) ──────────────────────────────────────

func TestRuntimeLoop_MaxAttemptsAbort(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(LoopBounds{MaxAttempts: 2})
	l.Start("objective")

	obs := Observation{Outcome: OutcomeFailed}
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue})

	// Second attempt consumes the last allowed execution.
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopRetry, Reason: "retry"})

	// Any further decision hits the attempt bound → abort.
	state, err := l.Step(ctx, LoopDecision{Action: LoopRetry, Reason: "retry"})
	if err != nil {
		t.Fatalf("step err = %v", err)
	}
	if state != RuntimeAborted {
		t.Fatalf("state = %s, want aborted (max attempts)", state)
	}
	if term := l.Termination(); term == nil || term.State != RuntimeAborted || term.Class != FailurePermanent {
		t.Fatalf("termination = %+v, want aborted/permanent", term)
	}
}

func TestRuntimeLoop_MaxRecoveryCyclesAbort(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(LoopBounds{MaxRecoveryCycles: 1, MaxAttempts: 10})
	l.Start("objective")

	obs := Observation{Outcome: OutcomeFailed}
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue})

	// Repair moves into Recovering (bounded recovery cycle) then re-executes.
	if got, err := l.Step(ctx, LoopDecision{Action: LoopRepair, Reason: "recover 1"}); err != nil || got != RuntimeRecovering {
		t.Fatalf("repair = %s, err=%v; want recovering", got, err)
	}
	if got, err := l.Step(ctx, LoopDecision{Action: LoopContinue, Reason: "re-execute"}); err != nil || got != RuntimeExecuting {
		t.Fatalf("re-execute = %s, err=%v; want executing", got, err)
	}
	l.ConsumeExecution(obs)
	l.ConsumeVerification(obs)

	// Second repair crosses the recovery-cycle bound → abort.
	state, err := l.Step(ctx, LoopDecision{Action: LoopRepair, Reason: "recover 2"})
	if err != nil {
		t.Fatalf("step err = %v", err)
	}
	if state != RuntimeAborted {
		t.Fatalf("state = %s, want aborted (max recovery cycles)", state)
	}
}

func TestRuntimeLoop_MaxIdenticalDecisionsAbort(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(LoopBounds{MaxIdenticalDecisions: 1, MaxAttempts: 100, MaxRecoveryCycles: 100})
	l.Start("objective")

	obs := Observation{Outcome: OutcomeArtifactProduced}
	// First cycle: fresh continue decision (identical=1).
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue})

	// Second identical continue with no intervening terminal move: the
	// post-apply bound check aborts the loop inside Step.
	state, err := l.Step(ctx, LoopDecision{Action: LoopContinue})
	if err != nil {
		t.Fatalf("step err = %v", err)
	}
	if state != RuntimeAborted {
		t.Fatalf("state = %s, want aborted (identical decisions)", state)
	}
	if term := l.Termination(); term == nil || term.Class != FailurePermanent {
		t.Fatalf("termination = %+v, want permanent", term)
	}
}

func TestRuntimeLoop_MaxTokensAbort(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(LoopBounds{MaxTotalTokens: 100, MaxAttempts: 100})
	l.Start("objective")

	obs := Observation{Outcome: OutcomeArtifactProduced, TokenUsage: 60}
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue}) // tokens=60
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue}) // tokens=120

	// Next decision crosses the token bound → abort.
	state, err := l.Step(ctx, LoopDecision{Action: LoopContinue})
	if err != nil {
		t.Fatalf("step err = %v", err)
	}
	if state != RuntimeAborted {
		t.Fatalf("state = %s, want aborted (token budget)", state)
	}
}

func TestRuntimeLoop_CancelledContextAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")
	l.Observe(Observation{})
	state, err := l.Step(ctx, LoopDecision{Action: LoopContinue})
	if err != nil {
		t.Fatalf("step err = %v", err)
	}
	if state != RuntimeAborted {
		t.Fatalf("state = %s, want aborted on cancellation", state)
	}
	if term := l.Termination(); term == nil || term.Class != FailurePermanent {
		t.Fatalf("termination = %+v, want aborted/permanent", term)
	}
}

// ── failure matrix / recovery ───────────────────────────────────────────────

func TestRecoverFailure_Matrix(t *testing.T) {
	tests := []struct {
		name   string
		obs    Observation
		class  FailureClass
		bounds LoopBounds
		want   LoopAction
	}{
		{"permanent aborts", Observation{}, FailurePermanent, DefaultLoopBounds(), LoopAbort},
		{"transient retries", Observation{AttemptNum: 0}, FailureTransient, DefaultLoopBounds(), LoopRetry},
		{"transient exhausted asks human", Observation{AttemptNum: 3}, FailureTransient, LoopBounds{MaxAttempts: 3}, LoopAskHuman},
		{"recoverable repairs", Observation{RecoveryCycle: 0}, FailureRecoverable, DefaultLoopBounds(), LoopRepair},
		{"recoverable exhausted asks human", Observation{RecoveryCycle: 2}, FailureRecoverable, LoopBounds{MaxRecoveryCycles: 2}, LoopAskHuman},
		{"unknown aborts", Observation{}, FailureClass("bogus"), DefaultLoopBounds(), LoopAbort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RecoverFailure(tc.obs, tc.class, tc.bounds)
			if got.Action != tc.want {
				t.Fatalf("RecoverFailure = %s, want %s", got.Action, tc.want)
			}
		})
	}
}

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		outcome ExecutionOutcome
		want    FailureClass
	}{
		{OutcomeNoArtifact, FailureTransient},
		{OutcomeArtifactProduced, FailureTransient},
		{OutcomeCancelled, FailurePermanent},
		{OutcomeFailed, FailureRecoverable},
		{OutcomePatchGenFailed, FailureRecoverable},
	}
	for _, tc := range tests {
		if got := ClassifyOutcome(tc.outcome); got != tc.want {
			t.Errorf("ClassifyOutcome(%s) = %s, want %s", tc.outcome, got, tc.want)
		}
	}
}

// ── human boundary (AwaitingHuman) ──────────────────────────────────────────

func TestRuntimeLoop_HumanBoundary(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")

	// Move into interpreting, then ask the human.
	obs := Observation{Outcome: OutcomeFailed, Verification: VerificationOutcome{Passed: false}}
	executeCycle(t, ctx, l, obs, LoopDecision{Action: LoopContinue})
	if got, err := l.Step(ctx, LoopDecision{Action: LoopAskHuman, Reason: "approval"}); err != nil || got != RuntimeAwaitingHuman {
		t.Fatalf("ask_human = %s, err=%v; want awaiting_human", got, err)
	}
	if b := l.Boundary(); b == nil || b.Reason != "approval" {
		t.Fatalf("boundary = %+v, want approval reason", b)
	}

	// While parked, only abort is legal via Step.
	if _, err := l.Step(ctx, LoopDecision{Action: LoopContinue}); err == nil {
		t.Fatal("continue while parked must be rejected")
	}

	// Human releases → observing.
	if got := l.ReleaseHuman("approved"); got != RuntimeObserving {
		t.Fatalf("release = %s, want observing", got)
	}
	if b := l.Boundary(); b != nil {
		t.Fatalf("boundary after release = %+v, want nil", b)
	}
}

func TestRuntimeLoop_AwaitHumanExplicit(t *testing.T) {
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")

	if got := l.AwaitHuman(HumanBoundary{Reason: "target ambiguity", Options: []string{"a", "b"}}); got != RuntimeAwaitingHuman {
		t.Fatalf("AwaitHuman = %s, want awaiting_human", got)
	}
	if b := l.Boundary(); b == nil || len(b.Options) != 2 {
		t.Fatalf("boundary options = %+v, want 2", b)
	}
}

func TestRuntimeLoop_AbortFromParked(t *testing.T) {
	ctx := context.Background()
	l := NewRuntimeLoop(DefaultLoopBounds())
	l.Start("objective")
	l.AwaitHuman(HumanBoundary{Reason: "clarify"})

	// Abort from parked state is the only Step-legal action.
	if got, err := l.Step(ctx, LoopDecision{Action: LoopAbort, Reason: "user cancelled"}); err != nil || got != RuntimeAborted {
		t.Fatalf("abort while parked = %s, err=%v; want aborted", got, err)
	}
}

// ── executors are the only authority ────────────────────────────────────────

func TestRuntimeLoop_ExecutorPortOnly(t *testing.T) {
	// The loop contract's Executor is an interface the RuntimeExecutor
	// satisfies structurally at the composition boundary. This test pins the
	// port surface so a future loop driver can only reach execution through it.
	var _ Executor = (*fakeExecutor)(nil)
}

type fakeExecutor struct{}

func (f *fakeExecutor) Execute(ctx context.Context, req LoopRequest) (Observation, error) {
	if ctx == nil {
		return Observation{}, errors.New("nil context")
	}
	return Observation{RequestID: "fake", Outcome: OutcomeArtifactProduced}, nil
}
