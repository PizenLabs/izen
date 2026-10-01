package autonomy

// PHASE 12 — task ≠ invocation, at the control-plane boundary.
//
// Two corrections are locked here:
//
//  1. CREATION CONTRACTS ARE NOT RELABELLED. The zero-trust recovery matrix's
//     typed transition rewrites an exhausted attempt as a bounded
//     SEARCH/REPLACE patch. That transition is only sound when the target
//     already has content to anchor the patch against. A creation contract
//     ("create index.html") has none — the relabelled attempt asks the model
//     for a patch against a file that does not exist, so it can never succeed.
//     The truthful decision is to escalate to the existing ZERO-TOKEN
//     DecisionSurface, whose `retry_with_explicit_budget` option is the only
//     lever that can make a large creation fit.
//
//  2. THE RUN-LEVEL TOKEN BOUND IS DERIVED, NOT FIXED. A budget bounds model
//     invocations; it must not truncate a logical task that is making
//     progress. The single fixed 8 000-token ceiling was smaller than one
//     legitimate multi-invocation generation, so it — not the structural
//     bounds — was the first thing to terminate a productive run.
//
// Neither correction grants authority, mutates anything, or changes /ask,
// /plan or $hot semantics.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestPhase12_CreationExhaustionEscalatesInsteadOfRelabelling proves a bounded
// patch is never fabricated for a file that does not exist.
func TestPhase12_CreationExhaustionEscalatesInsteadOfRelabelling(t *testing.T) {
	o := autonomy.Observation{
		Outcome:         autonomy.OutcomeTruncated,
		FinishReason:    "length",
		Target:          "index.html",
		MaxOutputTokens: 4096,
		ArtifactShape:   "create_file",
		AttemptNum:      0,
		RecoveryCycle:   0,
	}
	decision := DecideRecovery(o, autonomy.DefaultLoopBounds())
	if decision.Action != autonomy.LoopAskHuman {
		t.Fatalf("decision = %s (%s), want ask_human — a creation contract cannot be relabelled into a patch",
			decision.Action, decision.Reason)
	}
	if decision.PatchID != "" {
		t.Fatal("an escalated exhaustion must not stage a held patch")
	}
	if _, err := typedRepair(o, autonomy.LoopRequest{Prompt: "create index.html", Targets: []string{"index.html"}}); err == nil {
		t.Fatal("typedRepair must refuse to relabel a creation contract into a bounded patch")
	}
}

// TestPhase12_AnchoredExhaustionStillTransitions proves the existing typed
// transition is preserved for every contract that CAN be expressed as a
// bounded patch. The repair is scoped, not a blanket behaviour change.
func TestPhase12_AnchoredExhaustionStillTransitions(t *testing.T) {
	for _, shape := range []string{"replace_block", "replace_file", "search_replace", ""} {
		t.Run("shape="+shape, func(t *testing.T) {
			o := autonomy.Observation{
				Outcome:         autonomy.OutcomeTruncated,
				FinishReason:    "length",
				Target:          "index.html",
				MaxOutputTokens: 2048,
				ArtifactShape:   shape,
			}
			decision := DecideRecovery(o, autonomy.DefaultLoopBounds())
			if decision.Action != autonomy.LoopRepair {
				t.Fatalf("decision = %s (%s), want repair", decision.Action, decision.Reason)
			}
			next, err := typedRepair(o, autonomy.LoopRequest{Prompt: "rewrite @index.html", Targets: []string{"index.html"}})
			if err != nil {
				t.Fatalf("typedRepair: %v", err)
			}
			if next.RecoveryStrategy != autonomy.StrategyBoundedPatch {
				t.Fatalf("recovery strategy = %q, want the typed bounded-patch transition", next.RecoveryStrategy)
			}
		})
	}
}

// TestPhase12_PatchAnchoredIsPure pins the classification itself: a pure
// function of the dispatched artifact contract, never a filesystem probe and
// never a heuristic.
func TestPhase12_PatchAnchoredIsPure(t *testing.T) {
	cases := map[string]bool{
		"create_file":     false,
		"create_scaffold": false,
		"replace_block":   true,
		"replace_file":    true,
		"search_replace":  true,
		"":                true, // unknown keeps the historical behavior
		"plan":            true,
	}
	for shape, want := range cases {
		if got := patchAnchored(shape); got != want {
			t.Errorf("patchAnchored(%q) = %v, want %v", shape, got, want)
		}
	}
}

// TestPhase12_RunTokenBudgetIsDerived proves the run-level ceiling is a
// deterministic, inspectable function of the per-invocation budget — and that
// it is large enough for a legitimate multi-invocation task.
func TestPhase12_RunTokenBudgetIsDerived(t *testing.T) {
	// The reported production repro: a 4 096-token full-artifact creation.
	creation := autonomy.RunTokenBudget(4096, 3, 3)
	if creation <= 4096*3 {
		t.Fatalf("run budget %d must afford more than one output ceiling per attempt", creation)
	}
	// Deterministic: identical inputs, identical output.
	if creation != autonomy.RunTokenBudget(4096, 3, 3) {
		t.Fatal("RunTokenBudget must be deterministic")
	}
	// Monotonic in the per-invocation budget.
	if autonomy.RunTokenBudget(8192, 3, 3) <= creation {
		t.Fatal("run budget must grow with the per-invocation budget")
	}
	// Bounded on both sides.
	if got := autonomy.RunTokenBudget(1, 1, 0); got != autonomy.MinRunTokenBudget {
		t.Fatalf("floor = %d, want %d", got, autonomy.MinRunTokenBudget)
	}
	if got := autonomy.RunTokenBudget(1_000_000, 1000, 1000); got != autonomy.MaxRunTokenBudget {
		t.Fatalf("ceiling = %d, want %d", got, autonomy.MaxRunTokenBudget)
	}
	// Degenerate inputs never produce a zero/negative bound.
	for _, in := range [][3]int{{0, 0, -1}, {-5, -5, -5}} {
		if got := autonomy.RunTokenBudget(in[0], in[1], in[2]); got < autonomy.MinRunTokenBudget {
			t.Fatalf("RunTokenBudget%v = %d, want at least the floor", in, got)
		}
	}
	// The default bounds carry the floor, so the run-level token bound can
	// never be the tightest constraint before the structural bounds apply.
	if got := autonomy.DefaultLoopBounds().MaxTotalTokens; got != autonomy.MinRunTokenBudget {
		t.Fatalf("default MaxTotalTokens = %d, want the derived floor %d", got, autonomy.MinRunTokenBudget)
	}
	// The structural bounds still terminate a pathological loop.
	b := autonomy.DefaultLoopBounds()
	if b.MaxAttempts < 2 || b.MaxIdenticalDecisions < 1 || b.MaxExecutionSteps < 2 {
		t.Fatalf("structural bounds must remain the primary termination path, got %+v", b)
	}
}

// TestPhase12_RunTokenBoundDoesNotAuthorize pins the invariant that a budget
// terminates a run and never grants capability: widening it is a pure
// accounting operation with no effect on the loop's state or decisions.
func TestPhase12_RunTokenBoundDoesNotAuthorize(t *testing.T) {
	loop := autonomy.NewRuntimeLoop(autonomy.LoopBounds{MaxAttempts: 3, MaxTotalTokens: 10, MaxExecutionSteps: 10, MaxIdenticalDecisions: 2})
	loop.Start("objective")
	if loop.State() != autonomy.RuntimeObserving {
		t.Fatalf("state = %s, want observing", loop.State())
	}
	loop.WidenBounds(0, 0, autonomy.RunTokenBudget(4096, 3, 3), 0)
	if got := loop.Bounds().MaxTotalTokens; got != autonomy.RunTokenBudget(4096, 3, 3) {
		t.Fatalf("widened budget = %d, want the derived value", got)
	}
	// Widening the budget did not move the loop and did not grant anything.
	if loop.State() != autonomy.RuntimeObserving {
		t.Fatalf("state = %s after widening — a budget must never advance the loop", loop.State())
	}
	if loop.Boundary() != nil {
		t.Fatal("a budget must never open a human boundary")
	}
	// And it is monotonic: a lower request is ignored, never applied.
	loop.WidenBounds(0, 0, 1, 0)
	if got := loop.Bounds().MaxTotalTokens; got != autonomy.RunTokenBudget(4096, 3, 3) {
		t.Fatalf("budget = %d after a lower widen request — widening must only raise", got)
	}
}

// TestPhase12_ExhaustionIsNotTaskFailure pins the outcome classification the
// whole phase turns on: an exhausted invocation is a RECOVERABLE condition the
// loop may continue from, never a terminal task failure.
func TestPhase12_ExhaustionIsNotTaskFailure(t *testing.T) {
	if got := autonomy.ClassifyOutcome(autonomy.OutcomeTruncated); got != autonomy.FailureRecoverable {
		t.Fatalf("autonomy.ClassifyOutcome(truncated) = %s, want recoverable — exhaustion is not task failure", got)
	}
	// The adapter maps the loop outcome onto the scheduler's partial
	// vocabulary, which is what makes the pure continuation function treat it
	// as CONTINUE rather than failure.
	if got := driverOutcomeToStepOutcome(autonomy.OutcomeTruncated); got != "partial" {
		t.Fatalf("driverOutcomeToStepOutcome(truncated) = %q, want partial", got)
	}
}
