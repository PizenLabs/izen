package forensics_test

// R4 — BOUNDED CONTINUATION: DETERMINISTIC REGRESSION
//
// This file pins the continuation STATE MACHINE without any live model. The
// live benchmark (test/live_r4) is the source of truth for provider behaviour;
// these tests pin the runtime's own decisions:
//
//	length + incomplete objective  → continuation selected
//	already complete + verified    → no unnecessary continuation
//	continuation limit reached     → never PROVEN
//	continuation preserves identity → same run, same target, same workspace
//	mutation already applied       → no blind duplicate mutation
//
// The runtime owns TWO continuation layers, and both are covered here:
//
//	layer 1  the EXECUTOR's full-artifact bounded-step continuation
//	         (internal/execution/artifact_step.go): the same artifact contract
//	         is advanced across bounded invocations, bounded by
//	         llmstep.DefaultMaxContinuationSteps. It emits reasoning.step.* /
//	         reasoning.continuation.* / reasoning.state.*.
//
//	layer 2  the DRIVER's recovery matrix (internal/runtime/autonomy): when the
//	         executor's bounded-step budget is consumed with no complete
//	         artifact, the typed OUTPUT_EXHAUSTED condition reaches the driver,
//	         which materialises a materially different contract (FULL_REWRITE →
//	         BOUNDED_PATCH). It emits the canonical
//	         continuation.evaluated / continuation.selected records.
//
// Every test drives the SAME objects production wires
// (autonomy.Driver → ExecutorAdapter → RuntimeExecutor) over a scripted
// provider, and reads only authoritative runtime events.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/continuation"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/forensics"
	rtAutonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// truncatedEmpty is a genuine provider truncation that delivered NO artifact
// bytes (e.g. the whole budget went to hidden reasoning). The executor's
// no-progress guard refuses to continue an exhausted step that advanced nothing,
// so this deterministically reaches the DRIVER continuation layer.
func truncatedEmpty(budget int) *ai.Response {
	return &ai.Response{
		Content: "",
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     400,
			CompletionTokens: budget,
			FinishReason:     "length",
		},
		Truncated: true,
	}
}

// ── TEST 1 — LAYER 1: the executor continues a full-artifact step ────────────
//
// Call #1 is cut at the ceiling with finish_reason=length and delivered a real
// prefix. The executor must advance the SAME artifact contract to a second
// bounded invocation — not fail the task, not relabel it — and converge.

func TestR4_ExecutorBoundedStepContinuation(t *testing.T) {
	const ceiling = 1024
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		truncatedAt(ceiling),                   // step 1: genuine output exhaustion
		answered(1800, 260, searchReplacePong), // step 2: the bounded continuation
	}}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Driver.State() == autonomy.RuntimeAwaitingHuman {
		if _, err := r.Driver.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	tr := r.report()

	t.Logf("requested budgets: %v (calls=%d)", p.requestedBudgets(), p.callCount())
	t.Logf("output_exhausted=%d continuations=%d", tr.OutputExhausted, tr.Continuations)

	// (a) REAL exhaustion is recorded as exhaustion, attributed to a budget.
	if tr.OutputExhausted == 0 {
		t.Fatalf("finish_reason=length was not recorded as output exhaustion\n%s", tr)
	}
	var exhausted *events.ProviderExecutionPayload
	for i := range tr.ModelCalls {
		if tr.ModelCalls[i].Truncated || tr.ModelCalls[i].FinishReason == "length" {
			exhausted = &tr.ModelCalls[i]
			break
		}
	}
	if exhausted == nil || !exhausted.EffectiveOutputKnown || exhausted.EffectiveOutputTokens != ceiling {
		t.Fatalf("exhaustion was not attributed to the observed ceiling %d: %+v\n%s", ceiling, exhausted, tr)
	}

	// (b) the executor SCHEDULED a bounded continuation. The event vocabulary is
	// the executor's own reasoning.continuation.* record.
	if tr.Gaps[events.EventContinuationScheduled] == 0 && tr.Gaps[events.EventContinuationStarted] == 0 {
		t.Fatalf("executor did not schedule a bounded-step continuation\n%s", tr)
	}

	// (c) a second call happened in the SAME execution, with a different prompt.
	if p.callCount() < 2 {
		t.Fatalf("exhaustion produced no continuation call (calls=%d)\n%s", p.callCount(), tr)
	}
	if tr.RunID == "" {
		t.Fatalf("trace carries no run identity\n%s", tr)
	}
	root := tr.RunID
	for i, c := range tr.ModelCalls {
		if !strings.HasPrefix(c.RequestID, root) {
			t.Fatalf("model call #%d RequestID=%q is not part of run %q\n%s", i+1, c.RequestID, root, tr)
		}
	}
	if len(tr.Spec.Targets) != 1 || tr.Spec.Targets[0] != "note.txt" {
		t.Fatalf("execution spec was not preserved across continuation: %+v\n%s", tr.Spec, tr)
	}
	reqs := p.recorded()
	if len(reqs) >= 2 && ai.RequestFingerprint(reqs[0]) == ai.RequestFingerprint(reqs[1]) {
		t.Fatalf("continuation re-issued the identical prompt — a blind retry\n%s", tr)
	}

	// (d) convergence is evidence-gated.
	if !objectiveProven(tr) {
		t.Fatalf("objective was never PROVEN by the completion authority\n%s", tr)
	}
	if r.Driver.State() != autonomy.RuntimeCompleted {
		t.Fatalf("final state=%s, want completed\n%s", r.Driver.State(), tr)
	}

	// (e) exactly one mutation landed — no duplicate application.
	if len(tr.Mutations) != 1 {
		t.Fatalf("mutations=%d, want exactly 1 (no duplicate application)\n%s", len(tr.Mutations), tr)
	}
	if got := r.read("note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("workspace = %q, want a single applied patch", got)
	}
	for _, pat := range tr.Patterns {
		if pat == forensics.PatternRepeatedIdentical || pat == forensics.PatternNonProgressingContinue {
			t.Fatalf("continuation exhibited %s\n%s", pat, tr)
		}
	}
}

// ── TEST 2 — LAYER 2: the driver continues after executor exhaustion ─────────
//
// Call #1 is cut at the ceiling and delivered NO artifact bytes, so the
// executor's no-progress guard refuses a same-contract continuation and returns
// the typed OUTPUT_EXHAUSTED condition. The DRIVER must explicitly evaluate
// continuation, materialise a materially different contract and make the second
// call inside the same execution.

func TestR4_DriverContinuationAfterBoundedStepBudgetExhausted(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		truncatedEmpty(1024),                   // call #1: exhaustion delivering nothing
		answered(1800, 260, searchReplacePong), // call #2: the driver's bounded patch
	}}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Driver.State() == autonomy.RuntimeAwaitingHuman {
		if _, err := r.Driver.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	tr := r.report()

	t.Logf("requested budgets: %v (calls=%d)", p.requestedBudgets(), p.callCount())
	t.Logf("output_exhausted=%d continuations=%d", tr.OutputExhausted, tr.Continuations)

	if tr.OutputExhausted == 0 {
		t.Fatalf("finish_reason=length was not recorded as output exhaustion\n%s", tr)
	}

	// The DRIVER continuation decision is explicit and the matrix proposed a
	// repair (FULL_REWRITE → BOUNDED_PATCH). It is never a blind retry.
	var repairSelected bool
	for _, d := range tr.Decisions {
		if d.SelectedAction == string(autonomy.LoopRepair) && d.ProposedAction != "" {
			repairSelected = true
		}
	}
	if !repairSelected {
		t.Fatalf("no explicit driver continuation decision (repair) was selected after exhaustion:\n%+v\n%s", tr.Decisions, tr)
	}

	if p.callCount() < 2 {
		t.Fatalf("exhaustion produced no continuation call (calls=%d)\n%s", p.callCount(), tr)
	}
	root := tr.RunID
	for i, c := range tr.ModelCalls {
		if !strings.HasPrefix(c.RequestID, root) {
			t.Fatalf("model call #%d RequestID=%q is not part of run %q\n%s", i+1, c.RequestID, root, tr)
		}
	}
	if !objectiveProven(tr) {
		t.Fatalf("objective was never PROVEN by the completion authority\n%s", tr)
	}
	if r.Driver.State() != autonomy.RuntimeCompleted {
		t.Fatalf("final state=%s, want completed\n%s", r.Driver.State(), tr)
	}
	if len(tr.Mutations) != 1 {
		t.Fatalf("mutations=%d, want exactly 1\n%s", len(tr.Mutations), tr)
	}
	if got := r.read("note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("workspace = %q, want a single applied patch", got)
	}
}

// ── TEST 3 — continuation limit reached → never PROVEN ───────────────────────
//
// A provider that never stops truncating must NOT be converted into success.
// The run must terminate without a PROVEN objective and with a bounded number
// of provider calls.

func TestR4_RepeatedExhaustionNeverCompletes(t *testing.T) {
	// A provider that truncates every call. The script keeps the provider
	// honest (it never fabricates a `stop`); it is deliberately longer than any
	// bounded run could consume.
	truncations := make([]*ai.Response, 0, 40)
	for i := 0; i < 40; i++ {
		truncations = append(truncations, truncatedAt(1024))
	}
	p := &scriptedProvider{name: "always-truncated", responses: truncations}
	r := harness(t, p)

	term, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run returned an error rather than a termination: %v", err)
	}
	tr := r.report()

	t.Logf("termination=%v provider_calls=%d output_exhausted=%d", fmtTerm(term), p.callCount(), tr.OutputExhausted)

	// NEVER PROVEN.
	if objectiveProven(tr) {
		t.Fatalf("repeated exhaustion was converted into a PROVEN objective\n%s", tr)
	}
	if r.Driver.State() == autonomy.RuntimeCompleted {
		t.Fatalf("repeated exhaustion reached completed\n%s", tr)
	}
	// BOUNDED: the loop's own bounds are the authority; the count must not run away.
	if n := p.callCount(); n > rtAutonomy.MaxContractRecoveryAttempts+4 {
		t.Fatalf("provider was called %d times on permanent exhaustion\n%s", n, tr)
	}
	// The workspace must be byte-identical: a truncated artifact is never applied.
	if got := r.read("note.txt"); got != "foo\nbar\nbaz\n" {
		t.Fatalf("workspace changed on a fully exhausted run: %q\n%s", got, tr)
	}
}

// ── TEST 4 — mutation already applied → no blind duplicate mutation ──────────
//
// A single successful call produces exactly one applied mutation. The runtime
// must not issue a second call that re-applies it.

func TestR4_AppliedMutationIsNotReapplied(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(1800, 260, searchReplacePong),
	}}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Driver.State() == autonomy.RuntimeAwaitingHuman {
		if _, err := r.Driver.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	tr := r.report()

	if p.callCount() != 1 {
		t.Fatalf("a single successful call produced %d provider calls\n%s", p.callCount(), tr)
	}
	if len(tr.Mutations) != 1 {
		t.Fatalf("mutations=%d, want exactly 1\n%s", len(tr.Mutations), tr)
	}
	if got := r.read("note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("workspace = %q, want exactly one applied patch", got)
	}
}

// ── TEST 5 — the pure continuation transition function ───────────────────────
//
// The Driver consults continuation.DeriveNextStep as a PURE library. These
// assertions pin the four boundaries the state machine depends on, with no
// runtime and no provider.

func TestR4_PureContinuationStateMachine(t *testing.T) {
	base := func() rtAutonomy.DriverContinuationInput {
		return rtAutonomy.DriverContinuationInput{
			Objective:        "change bar to qux @note.txt",
			Targets:          []string{"note.txt"},
			StateFingerprint: "digest-1",
			AllowedScope:     []string{"note.txt"},
			ProviderCeiling:  1024,
		}
	}

	// (1) partial output + an evidence-backed target → CONTINUE (not failure).
	in := base()
	in.IsPartialOutput = true
	in.PreviousOutcome = "partial"
	in.PreviousReason = "OUTPUT_CEILING"
	in.Observations = []continuation.Observation{{
		Kind: continuation.KindExecutionResult, Subject: "note.txt",
		Detail: "exhausted", StateFingerprint: "digest-1",
	}}
	if d := rtAutonomy.DeriveDriverContinuation(in); d.Action != continuation.ActionContinue {
		t.Fatalf("partial + target: action=%s, want CONTINUE (%s)", d.Action, d.Reason)
	} else if d.NextStep == nil || len(d.NextStep.Targets) == 0 {
		t.Fatalf("CONTINUE carried no bounded next step: %+v", d)
	}

	// (2) already complete + verified → COMPLETE, no unnecessary continuation.
	done := base()
	done.PreviousOutcome = "complete"
	done.Verified = true
	if d := rtAutonomy.DeriveDriverContinuation(done); d.Action != continuation.ActionComplete {
		t.Fatalf("complete + verified: action=%s, want COMPLETE (%s)", d.Action, d.Reason)
	}

	// (3) partial output with NO evidence-backed target → BLOCKED, never a
	// fabricated next step.
	empty := rtAutonomy.DriverContinuationInput{
		Objective:       "change bar to qux",
		IsPartialOutput: true,
		PreviousOutcome: "partial",
	}
	if d := rtAutonomy.DeriveDriverContinuation(empty); d.Action != continuation.ActionBlocked {
		t.Fatalf("partial with no target: action=%s, want BLOCKED (%s)", d.Action, d.Reason)
	}

	// (4) partial output whose next target exceeds the pre-approved scope →
	// AWAITING_APPROVAL — continuation may never widen authority. The Driver
	// wrapper derives no plan steps, so this boundary is pinned on the pure
	// library directly with an out-of-scope plan reference.
	inScope := continuation.DerivationInput{
		Task: continuation.TaskStateView{
			Intent:      "change bar to qux",
			ActiveScope: []string{"note.txt"},
		},
		PlanSteps: []continuation.PlanStepView{{
			ID: "st-2", Kind: "MUTATE", References: []string{"secret.go"},
			Rationale: "next bounded step",
		}},
		PreviousOutcome: "partial",
		IsPartialOutput: true,
		AllowedScope:    []string{"note.txt"},
	}
	if d := continuation.DeriveNextStep(inScope); d.Action != continuation.ActionAwaitingApproval {
		t.Fatalf("scope-exceeding continuation: action=%s, want AWAITING_APPROVAL (%s)", d.Action, d.Reason)
	}
}

// objectiveProven reports whether the completion authority granted PROVEN.
func objectiveProven(tr *forensics.Trace) bool {
	for _, o := range tr.ObjectiveStates {
		if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), execution.ObjectiveProven.String()) {
			return true
		}
	}
	return false
}
