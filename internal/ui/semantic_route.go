package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── Semantic routing reconciliation (mode authority dominates classification)
// ─────────────────────────────────────────────────────────────────────────────
// Confidence is an advisory classification signal, never an authority grant.
// The classifier may propose what the request might mean; the current mode and
// policy determine what the engine is allowed to do.

// hasExplicitExecutionAuthority reports whether the input carries explicit
// execution authority: a $prompt/$hot execution directive. Natural language
// alone never carries execution authority and never manufactures the semantic
// equivalent of $prompt/$hot.
func hasExplicitExecutionAuthority(raw string) bool {
	return hasFreeInputExecutionMarker(raw)
}

// reconciledSemanticRoute applies the mode-authority ceiling to the classifier
// candidate for observability. Confidence is carried through and never changes
// the route.
func reconciledSemanticRoute(current modes.Mode, input string, explicit bool) autonomy.ReconciledRoute {
	classified := autonomy.Classify(input, nil)
	return autonomy.ReconcileRouteFor(current.String(), classified, input, explicit)
}

// reconciliationTelemetry renders the canonical observable transition line.
func reconciliationTelemetry(r autonomy.ReconciledRoute) string {
	return r.FormatReconciliation()
}

// shouldFallbackToAsk reports whether content typed under an execution mode
// (investigate/review/plan) is a simple conversational/read-only question that
// must return to the canonical /ask path instead of creating workflow or
// execution state.
func shouldFallbackToAsk(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	classified := autonomy.Classify(trimmed, nil)
	return autonomy.IsSimpleReadOnlyQuestionFor(classified, trimmed)
}

// fallbackToAsk re-routes conversational content to the canonical /ask path.
// The mode switch is presentation-only; the content is then answered as
// read-only chat with zero workflow propagation.
func (m *model) fallbackToAsk(content string) tea.Cmd {
	r := reconciledSemanticRoute(m.resolver.Current(), content, false)
	m.push(roleSystem, infoStyle.Render("Conversational question — answering in /ask ("+r.FormatReconciliation()+")"))
	m.modeChangeAuthorized = true
	m.setMode(modes.ModeAsk)
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return m.handleMessageContent(content)
}
