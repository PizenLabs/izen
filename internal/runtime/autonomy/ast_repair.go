package autonomy

import (
	"github.com/PizenLabs/izen/internal/execution"
)

// ── AST Repair Output-Budget Guardrail (Runtime Policy Layer) ───────────────
//
// The runtime autonomy package owns the policy that orchestrates a budget
// guardrail refusal: the typed sentinel, the canonical fallback shape, and
// the reason-string formatter. The actual mechanical `Check()` lives in
// `internal/execution/budget_guardrail.go` so the executor (the enforcement
// site) can call it without a runtime-import cycle.
//
// This file is the runtime-facing surface. It re-exports the canonical
// types so callers in the autonomy package (the recovery matrix, the
// proposal builder, the decision surface) use one vocabulary without
// re-importing the execution package's budget-guardrail types at every
// call site.
//
// Why both layers exist:
//   - The executor is the SOLE enforcement site; it dispatches provider
//     calls and is the only place that can refuse them. Its types are the
//     canonical mechanical surface.
//   - The autonomy package is the policy layer; it decides WHEN the
//     executor should refuse (the recovery matrix's hard-block decision)
//     and WHAT the user-facing reason should be. It must use the same
//     sentinel as the executor so errors.Is comparisons cross package
//     boundaries cleanly.

// ErrOutputBudgetExceeded is the typed sentinel the executor raises when
// the model budget is too small to contain a FULL_REWRITE-shaped response.
// Re-exported from the execution package so autonomy callers test it via
// errors.Is without reaching across the boundary.
var ErrOutputBudgetExceeded = execution.ErrOutputBudgetExceeded

// BudgetShape enumerates the output shapes the guardrail reasons over. It is
// re-exported from the execution package so the recovery matrix and the
// proposal builder can reference shapes by name in the runtime vocabulary.
type BudgetShape = execution.BudgetShape

// Canonical shape constants — re-exports so the autonomy package owns a
// stable policy surface independent of the execution-package internals.
const (
	ShapeFullRewrite  = execution.ShapeFullRewrite
	ShapeBoundedPatch = execution.ShapeBoundedPatch
	ShapeInspectOnly  = execution.ShapeInspectOnly
)

// BudgetGuardrail is the deterministic input to the output-budget check. It
// is re-exported from the execution package so the recovery matrix and the
// proposal builder reason over the same value object the executor enforces.
type BudgetGuardrail = execution.BudgetGuardrail

// EnforceRepairBudgetGuardrail is the runtime-owned entry point for the
// repair-first path. It runs the guardrail and, on ErrOutputBudgetExceeded,
// returns the typed sentinel unchanged so the executor's repair-first branch
// can switch to BOUNDED_PATCH on the AST error offset.
//
// The runtime policy is intentionally identical to the mechanical check:
// this is a typed re-export, not a re-implementation. The recovery matrix
// may test the result with errors.Is and dispatch the fallback shape
// without parsing nuanced advice.
func EnforceRepairBudgetGuardrail(g BudgetGuardrail) error {
	return g.Check()
}

// FallbackShapeForBudgetExceeded returns the shape the repair-first branch
// MUST switch to when ErrOutputBudgetExceeded is raised. It is always
// ShapeBoundedPatch: the chunked window physically fits in any output
// budget, and the AST error offset is a small bounded region the model
// can patch with a single anchored SEARCH/REPLACE block.
func FallbackShapeForBudgetExceeded() BudgetShape {
	return execution.FallbackShapeForBudgetExceeded()
}

// FormatBudgetGuardrailMessage renders a compact, presentation-safe summary
// of a guardrail refusal. It is the canonical reason text the runtime
// publishes on the DecisionSurface when it parks awaiting_human after a
// budget-exhausted dispatch.
func FormatBudgetGuardrailMessage(target string, targetTokens, maxOutputTokens int) string {
	return execution.FormatBudgetGuardrailMessage(target, targetTokens, maxOutputTokens)
}

// EstimateTargetTokens is the deterministic char/4 → token conversion used
// at the guardrail layer. It is the same heuristic the executor uses for
// context accounting (NOT provider billing). Re-exported so the recovery
// matrix can compute the same estimate the executor will compare.
func EstimateTargetTokens(content string) int {
	return execution.EstimateTargetTokens(content)
}
