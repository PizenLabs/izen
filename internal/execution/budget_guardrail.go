package execution

import (
	"errors"
	"fmt"
	"strings"
)

// ── AST Repair Output-Budget Guardrail ──────────────────────────────────────
//
// A FULL_REWRITE (and its AST-repair sibling StrategySyntaxRepair) requires
// the model to emit the entire target file as output. When the target's
// estimated token count exceeds the model's max_output ceiling, dispatching
// the rewrite is GUARANTEED to truncate at the output gate (Boundary 3
// finish_reason=length). The truncated response is invalid, the run burns a
// recovery cycle for nothing, and the surface republishes — yet the next
// attempt is no different because the same FULL_REWRITE strategy is replayed.
//
// The guardrail is a HARD pre-dispatch check. It compares the target's
// estimated tokens to the selected model's MaxOutputTokens, and REFUSES to
// dispatch a FULL_REWRITE-shaped strategy when the budget cannot contain it.
// The repair-first path is special-cased: on ErrOutputBudgetExceeded the
// executor MUST fall back to a strictly BOUNDED_PATCH targeting the AST error
// offset (the chunked window physically fits in any output budget).
//
// Why a hard error instead of an advice value:
//   - The runtime already runs Boundary-2 preflight; this guardrail is the
//     LAST gate between preflight and the provider call. The decision MUST
//     be binary (dispatch or abort) so callers can branch without parsing
//     nuanced advice.
//   - The error is a typed sentinel: callers (the executor) test for it via
//     errors.Is, fall through to BOUNDED_PATCH for repair_first, and re-raise
//     for hard-block UI parking.

var ErrOutputBudgetExceeded = errors.New("execution: output budget exceeded for FULL_REWRITE — refusing to dispatch")

// ErrTargetEmpty is returned when no target content is supplied for the
// guardrail. The check is a no-op on empty input; callers should never see
// it in production. It is exported so tests can assert the typed sentinel.
var ErrTargetEmpty = errors.New("execution: output-budget guardrail requires non-empty target content")

// BudgetShape enumerates the output shapes the guardrail reasons over. It
// is the minimal projection the executor and the runtime recovery loop need
// to convey the strategy shape to the guardrail — independent of the
// strategy package's broader taxonomy. The mapping the executor uses is:
//
//	FULL_REWRITE     — TargetedMutation / StrategyFullRewrite / repair_first
//	BOUNDED_PATCH    — search_replace artifact, StrategyBoundedPatch
//	INSPECT_ONLY     — read-only, no model output
type BudgetShape string

const (
	// ShapeFullRewrite demands whole-file output from the model. A
	// dispatch against this shape is refused when the target's
	// estimated token count exceeds the model's max_output budget.
	ShapeFullRewrite BudgetShape = "full_rewrite"
	// ShapeBoundedPatch demands ONE anchored SEARCH/REPLACE block. The
	// worst-case response is a small diff; it fits any advertised
	// budget. The guardrail never refuses this shape.
	ShapeBoundedPatch BudgetShape = "bounded_patch"
	// ShapeInspectOnly produces no model output. The guardrail never
	// refuses this shape.
	ShapeInspectOnly BudgetShape = "inspect_only"
)

// BudgetGuardrail is the deterministic input to the output-budget check. It
// is a value object — pure data, no I/O, no provider calls. Callers construct
// it from the resolved target content and the execution strategy profile.
type BudgetGuardrail struct {
	// TargetTokens is the deterministic estimate of the target file in
	// output tokens. It is computed via the runtime's coarse chars/4
	// heuristic; provider usage is never used here.
	TargetTokens int
	// MaxOutputTokens is the selected model's max output budget. A value
	// of 0 (provider default) disables the guardrail — the model may
	// silently truncate, but the runtime cannot prove a truncation in
	// advance, so the check is conservative and never refuses.
	MaxOutputTokens int
	// Shape is the execution contract shape. ShapeFullRewrite triggers
	// the guardrail; ShapeBoundedPatch and ShapeInspectOnly never do.
	Shape BudgetShape
	// Target is the resolved canonical target path. It is only used for
	// the wrapped error message — the check itself is path-agnostic.
	Target string
}

// EstimateTargetTokens is the deterministic char/4 → token conversion used
// at the guardrail layer. It is the same heuristic the executor uses for
// context accounting (NOT provider billing). Returning 0 for empty content
// keeps the public surface aligned with the executor's behavior.
func EstimateTargetTokens(content string) int {
	if content == "" {
		return 0
	}
	return len(content) / 4
}

// Check enforces the output-budget guardrail for a FULL_REWRITE-shaped
// strategy. It returns ErrOutputBudgetExceeded when ALL of:
//   - TargetTokens > 0 (a new file has no existing content; the guardrail
//     cannot reason about an empty baseline and is permissive).
//   - MaxOutputTokens > 0 (a model with no declared ceiling is never
//     refused; the runtime cannot prove truncation in advance).
//   - The strategy demands full-file output (ShapeFullRewrite).
//   - TargetTokens > MaxOutputTokens (the worst-case response is
//     GUARANTEED to truncate at the output gate).
//
// The check is a pure function: it never invokes the provider, never reads
// the filesystem, and never mutates state. It is the runtime-owned decision
// the executor must run BEFORE handing the request to ai.Provider.
func (g BudgetGuardrail) Check() error {
	if g.TargetTokens <= 0 {
		// Empty baseline (new file, or the executor had no snapshot).
		// The guardrail cannot reason about a non-existent file: the
		// caller is responsible for upstream feasibility, and an
		// empty content error here would deadlock new-file creates.
		return nil
	}
	if g.MaxOutputTokens <= 0 {
		// Undeclared model ceiling: the runtime cannot prove a
		// truncation in advance, so the guardrail is permissive.
		return nil
	}
	if g.Shape != ShapeFullRewrite {
		// ShapeBoundedPatch and ShapeInspectOnly never demand full-file
		// output. Their worst-case response is one SEARCH/REPLACE
		// block, which fits any advertised budget.
		return nil
	}
	if g.TargetTokens <= g.MaxOutputTokens {
		return nil
	}
	return fmt.Errorf("%w: target %q estimates ~%d output tokens, model max_output=%d — refusing FULL_REWRITE dispatch",
		ErrOutputBudgetExceeded, g.Target, g.TargetTokens, g.MaxOutputTokens)
}

// FallbackShapeForBudgetExceeded returns the shape the repair-first branch
// MUST switch to when ErrOutputBudgetExceeded is raised. It is always
// ShapeBoundedPatch: the chunked window physically fits in any output
// budget, and the AST error offset is a small bounded region the model
// can patch with a single anchored SEARCH/REPLACE block.
//
// The function is exported so the executor can reference the canonical
// fallback without re-encoding the shape constant at the call site.
func FallbackShapeForBudgetExceeded() BudgetShape {
	return ShapeBoundedPatch
}

// FormatBudgetGuardrailMessage renders a compact, presentation-safe summary
// of a guardrail refusal. It is used by the DecisionSurface reason string
// when the runtime parks awaiting_human after a budget-exhausted dispatch.
func FormatBudgetGuardrailMessage(target string, targetTokens, maxOutputTokens int) string {
	var b strings.Builder
	b.WriteString("Output budget exceeded: target ")
	b.WriteString(target)
	b.WriteString(" estimates ~")
	fmt.Fprintf(&b, "%d", targetTokens)
	b.WriteString(" output tokens but model max_output=")
	fmt.Fprintf(&b, "%d", maxOutputTokens)
	b.WriteString(". FULL_REWRITE dispatch refused; falling back to BOUNDED_PATCH on the AST error offset.")
	return b.String()
}
