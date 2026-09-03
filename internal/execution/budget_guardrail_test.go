package execution

import (
	"errors"
	"strings"
	"testing"
)

// ── AST Repair Output-Budget Guardrail Tests ───────────────────────────────
//
// The guardrail is the LAST gate between preflight and the provider call.
// A FULL_REWRITE-shaped strategy is REFUSED when the target's estimated
// output tokens exceed the model's max_output ceiling — the response is
// GUARANTEED to truncate at the output gate, so dispatching is pure
// waste. The tests pin the typed sentinel, the boundary behavior, and the
// repair-first fallback (BOUNDED_PATCH) so the policy is observable.

// TestBudgetGuardrail_RefusesFullRewriteWhenOverBudget pins the hard refusal
// at the canonical case: a 5,835-token target against a 1,024-token budget.
// The check is the directive's headline repro: index.html at 1024 output.
func TestBudgetGuardrail_RefusesFullRewriteWhenOverBudget(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    5835, // the directive's 23,340-byte / 4-char-per-token estimate
		MaxOutputTokens: 1024,
		Shape:           ShapeFullRewrite,
		Target:          "index.html",
	}
	err := g.Check()
	if err == nil {
		t.Fatal("guardrail must refuse a 5,835-token target against a 1,024-token FULL_REWRITE budget")
	}
	if !errors.Is(err, ErrOutputBudgetExceeded) {
		t.Fatalf("error = %v, want ErrOutputBudgetExceeded", err)
	}
	// The error message carries the diagnostic surface the DecisionSurface
	// reason string uses; it MUST include the target path, the estimate,
	// and the budget so the human sees exactly why the run is parked.
	msg := err.Error()
	for _, want := range []string{"index.html", "5835", "1024", "FULL_REWRITE"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %q", want, msg)
		}
	}
}

// TestBudgetGuardrail_AllowsBoundedPatchWhenOverBudget is the recovery
// invariant: a BOUNDED_PATCH dispatch against the same over-budget target
// MUST be permitted because its worst-case response is one SEARCH/REPLACE
// block, which fits any advertised budget.
func TestBudgetGuardrail_AllowsBoundedPatchWhenOverBudget(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    5835,
		MaxOutputTokens: 1024,
		Shape:           ShapeBoundedPatch,
		Target:          "index.html",
	}
	if err := g.Check(); err != nil {
		t.Fatalf("BOUNDED_PATCH must never be refused: %v", err)
	}
}

// TestBudgetGuardrail_AllowsInspectOnly is the read-only invariant: an
// inspect dispatch produces no model output and is never refused.
func TestBudgetGuardrail_AllowsInspectOnly(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    99999,
		MaxOutputTokens: 64,
		Shape:           ShapeInspectOnly,
		Target:          "x.go",
	}
	if err := g.Check(); err != nil {
		t.Fatalf("INSPECT_ONLY must never be refused: %v", err)
	}
}

// TestBudgetGuardrail_AllowsFullRewriteWithinBudget is the happy path: a
// target whose estimate fits the budget dispatches as configured.
func TestBudgetGuardrail_AllowsFullRewriteWithinBudget(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    512,
		MaxOutputTokens: 1024,
		Shape:           ShapeFullRewrite,
		Target:          "small.txt",
	}
	if err := g.Check(); err != nil {
		t.Fatalf("guardrail must permit a target that fits the budget: %v", err)
	}
}

// TestBudgetGuardrail_PermissiveOnEmptyTarget is the new-file invariant:
// when the target has no existing content (a create path), the guardrail
// cannot reason about the baseline. The executor must NOT deadlock on a
// new file because the estimate is zero — an empty refusal would
// strand the run before the model even sees a prompt.
func TestBudgetGuardrail_PermissiveOnEmptyTarget(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    0,
		MaxOutputTokens: 1024,
		Shape:           ShapeFullRewrite,
		Target:          "new.txt",
	}
	if err := g.Check(); err != nil {
		t.Fatalf("guardrail must permit an empty baseline (new file): %v", err)
	}
}

// TestBudgetGuardrail_PermissiveOnUndeclaredBudget is the unbounded-model
// invariant: a model that advertises no max_output (0) cannot be proven
// infeasible at the guardrail layer. The check is conservative and
// permissive in that case.
func TestBudgetGuardrail_PermissiveOnUndeclaredBudget(t *testing.T) {
	g := BudgetGuardrail{
		TargetTokens:    100000,
		MaxOutputTokens: 0,
		Shape:           ShapeFullRewrite,
		Target:          "huge.bin",
	}
	if err := g.Check(); err != nil {
		t.Fatalf("guardrail must permit an undeclared model budget: %v", err)
	}
}

// TestEstimateTargetTokens pins the char/4 → token conversion used at the
// guardrail layer. It is the same heuristic the executor uses for context
// accounting (NOT provider billing).
func TestEstimateTargetTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"one char", "a", 0}, // 1/4 = 0 (integer division)
		{"four chars", "abcd", 1},
		{"23340 bytes → 5835 tokens", strings.Repeat("x", 23340), 5835},
		{"7780 bytes → 1945 tokens (e2eCorruptFixture approx)", strings.Repeat("x", 7780), 1945},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateTargetTokens(tc.in); got != tc.want {
				t.Errorf("EstimateTargetTokens(%d bytes) = %d, want %d", len(tc.in), got, tc.want)
			}
		})
	}
}

// TestFallbackShapeForBudgetExceeded pins the repair-first fallback shape.
// The executor switches to ShapeBoundedPatch on ErrOutputBudgetExceeded;
// this is the constant the executor references.
func TestFallbackShapeForBudgetExceeded(t *testing.T) {
	if got := FallbackShapeForBudgetExceeded(); got != ShapeBoundedPatch {
		t.Fatalf("fallback shape = %q, want %q", got, ShapeBoundedPatch)
	}
}

// TestFormatBudgetGuardrailMessage pins the presentation-safe summary the
// DecisionSurface reason string carries. It is the human-readable
// explanation when the runtime parks awaiting_human after a guardrail
// refusal; the message must include the target, the estimate, the
// budget, and the explicit fallback decision.
func TestFormatBudgetGuardrailMessage(t *testing.T) {
	got := FormatBudgetGuardrailMessage("index.html", 5835, 1024)
	for _, want := range []string{"index.html", "5835", "1024", "BOUNDED_PATCH"} {
		if !strings.Contains(got, want) {
			t.Errorf("format message missing %q: %q", want, got)
		}
	}
}
