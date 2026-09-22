// Package autonomy — mode authority reconciliation.
//
// Confidence is an advisory classification signal, never an authority grant:
// ClassifierIntent != AuthorizedIntent, and confidence cannot raise the
// authority ceiling of the current mode. The mode policy dominates the
// classifier: the classifier proposes what the request might mean; the
// current mode determines what the engine is allowed to do.
package autonomy

import (
	"fmt"
	"strings"
)

// ReconciledRoute is the mode-policy reconciliation of a classifier candidate.
// It carries the full observable transition so telemetry can distinguish every
// stage of the semantic routing decision.
type ReconciledRoute struct {
	// ClassifiedIntent is the raw classifier candidate (advisory only).
	ClassifiedIntent Intent
	// Confidence is the classifier confidence (advisory only, never authority).
	Confidence float64
	// CurrentMode is the explicit user mode / policy boundary.
	CurrentMode string
	// Route is the reconciled semantic route (the only authoritative route).
	Route string
	// ExecutionAuthorized is true only when the reconciled route may enter
	// execution semantics. ASK is never execution-authorized.
	ExecutionAuthorized bool
	// Reason is the human-readable reconciliation justification.
	Reason string
	// LoweringKind describes what IR lowering may produce for this route:
	// "read_only" (no mutation tasks) or "workflow" (existing mode policy).
	LoweringKind string
}

// FormatReconciliation renders the canonical telemetry line for a reconciled
// route. It never fabricates metrics: every field comes from the inputs.
func (r ReconciledRoute) FormatReconciliation() string {
	return fmt.Sprintf(
		"classified_intent=%s confidence=%.2f current_mode=%s reconciled_route=%s execution_authorized=%t lowering_kind=%s",
		r.ClassifiedIntent.String(), r.Confidence, r.CurrentMode, r.Route, r.ExecutionAuthorized, r.LoweringKind,
	)
}

// ReconcileRoute applies the mode-authority ceiling to a classifier candidate.
//
// Authority ordering (canonical):
//
//	Explicit User Mode / Modifier → Mode Policy → Classification Candidate
//	→ Confidence → Semantic Route → IR Lowering → Authorization → Execution
//
// Rules:
//  1. Explicit execution authority (hasExecutionMarker, i.e. $prompt/$hot or an
//     explicit /build execution surface) bypasses the ASK ceiling: the
//     classifier's preferred workspace stands and the existing BUILD
//     authorization model owns the rest.
//  2. Current mode ASK always reconciles to ASK (read-only), regardless of the
//     classified intent or confidence. ASK + 99.9% BUILD confidence != BUILD.
//  3. Non-ASK modes with a simple conversational/read-only question reconcile
//     back to ASK (conversational auto-return). Otherwise the current mode is
//     preserved (existing workflow policy owns genuine requests).
//  4. Confidence is carried for observability only and never changes the route.
func ReconcileRoute(currentMode string, classified IntentResult, hasExecutionMarker bool) ReconciledRoute {
	return ReconcileRouteFor(currentMode, classified, "", hasExecutionMarker)
}

// ReconcileRouteFor is ReconcileRoute with the raw input supplied for
// question-form detection in the conversational auto-return rule. Callers with
// the user text in hand must prefer this entry point; ReconcileRoute (no raw)
// applies only the intent-shape rules.
func ReconcileRouteFor(currentMode string, classified IntentResult, raw string, hasExecutionMarker bool) ReconciledRoute {
	mode := strings.ToLower(strings.TrimSpace(currentMode))
	if mode == "" {
		mode = "ask"
	}
	intent := classified.Intent
	conf := classified.Confidence

	// Rule 1: explicit execution authority bypasses the ceiling.
	if hasExecutionMarker {
		route := string(PreferredWorkspace(intent))
		return ReconciledRoute{
			ClassifiedIntent:    intent,
			Confidence:          conf,
			CurrentMode:         mode,
			Route:               route,
			ExecutionAuthorized: route != string(WorkspaceAsk),
			Reason:              "explicit execution authority — classifier workspace stands under existing authorization",
			LoweringKind:        loweringKindFor(route),
		}
	}

	// Rule 2: ASK ceiling dominates classification.
	if mode == string(WorkspaceAsk) {
		return ReconciledRoute{
			ClassifiedIntent:    intent,
			Confidence:          conf,
			CurrentMode:         mode,
			Route:               string(WorkspaceAsk),
			ExecutionAuthorized: false,
			Reason:              "mode ceiling: /ask is read-only — classifier candidate is advisory only",
			LoweringKind:        "read_only",
		}
	}

	// Rule 3: conversational auto-return to ASK.
	if IsSimpleReadOnlyQuestionFor(classified, raw) {
		return ReconciledRoute{
			ClassifiedIntent:    intent,
			Confidence:          conf,
			CurrentMode:         mode,
			Route:               string(WorkspaceAsk),
			ExecutionAuthorized: false,
			Reason:              "conversational auto-return: read-only question returns to canonical /ask path",
			LoweringKind:        "read_only",
		}
	}

	// Rule 4: preserve the explicit mode for genuine workflow requests.
	return ReconciledRoute{
		ClassifiedIntent:    intent,
		Confidence:          conf,
		CurrentMode:         mode,
		Route:               mode,
		ExecutionAuthorized: mode != string(WorkspaceAsk),
		Reason:              "explicit mode preserved for genuine workflow request under existing policy",
		LoweringKind:        loweringKindFor(mode),
	}
}

func loweringKindFor(route string) string {
	if route == string(WorkspaceAsk) {
		return "read_only"
	}
	return "workflow"
}

// mutationVerbs mirrors the deterministic mutation-verb set so the fallback
// detector stays in sync with the classifier's mutation signals.
var reconcileMutationVerbs = []string{
	"remove ", "delete ", "add ", "create ", "generate ", "implement ",
	"write ", "update ", "modify ", "change ", "fix ", "correct ",
	"edit ", "insert ", "replace ", "rewrite ", "build ", "refactor ",
	"restructure ", "reorganize ", "rename ", "redesign ",
}

// IsSimpleReadOnlyQuestion reports whether a classified input is a simple
// conversational/read-only question that should return to the canonical /ask
// path instead of creating workflow/execution state.
//
// It fires when the classified intent does not require mutation AND one of:
//   - intent is conversation (always conversational),
//   - intent is explanation with no mutation verb,
//   - input is question-formed (trailing "?" or interrogative opener) with no
//     mutation verb and no strong workflow-imperative signal.
//
// Planning imperatives ("design the architecture", "plan the migration") and
// investigation imperatives ("investigate the crash") are NOT simple questions
// and return false. Confidence is never consulted.
func IsSimpleReadOnlyQuestion(classified IntentResult) bool {
	return IsSimpleReadOnlyQuestionFor(classified, "")
}

// IsSimpleReadOnlyQuestionFor is IsSimpleReadOnlyQuestion with the raw input
// supplied for question-form detection. When raw is empty, only the
// intent-shape rules apply.
//
// It fires when the classified intent does not require mutation AND one of:
//   - intent is conversation (always conversational),
//   - input is question-formed (trailing "?" or interrogative opener) with no
//     mutation verb.
//
// Planning/investigation imperatives ("design the architecture", "investigate
// the crash", "review the auth diff") are not question-formed and return
// false, so genuine workflow requests preserve their explicit mode.
// Confidence is never consulted.
func IsSimpleReadOnlyQuestionFor(classified IntentResult, raw string) bool {
	if classified.Intent.RequiresMutation() {
		return false
	}
	if classified.Intent == IntentConversation {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		// Without raw text only conversation is safely simple; an
		// explanation-shaped default (e.g. classifier fallback 0.6) may be a
		// genuine workflow imperative ("review the auth diff"), so it must
		// not auto-return blind.
		return false
	}
	for _, v := range reconcileMutationVerbs {
		if strings.Contains(lower, strings.TrimSpace(v)) {
			return false
		}
	}
	return isQuestionForm(lower)
}

// isQuestionForm reports whether the raw input is question-formed: a trailing
// "?" or an interrogative opener. Imperative workflow directives
// ("design …", "plan …", "investigate …") are not question-formed.
func isQuestionForm(lower string) bool {
	trimmed := strings.TrimSpace(lower)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, "?") {
		return true
	}
	for _, opener := range []string{
		"what ", "why ", "how ", "who ", "when ", "where ", "which ",
		"explain ", "describe ", "tell me ", "summarize ", "walk me through ",
		"what's ", "whats ", "is ", "are ", "does ", "do ", "can you explain",
	} {
		if strings.HasPrefix(trimmed, opener) {
			return true
		}
	}
	return false
}
