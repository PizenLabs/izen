package ui

import (
	"strings"

	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Phase 6.4.5 Ledger Context Isolation ────────────────────────────────
//
// Ledger Context Isolation Invariant: diagnostic outputs from /investigate
// MUST NOT pollute subsequent $prompt or plan synthesis prompts. Plan context
// must be clean unless explicitly chaining investigation steps.

// isNewExplicitPrompt reports whether the user input is a new explicit
// $prompt directive (case-insensitive, optional leading whitespace). A new
// $prompt starts a fresh intent: stale forensic state from a prior
// /investigate run must never leak into its context.
func isNewExplicitPrompt(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "$prompt")
}

// ClearForensicStateForNewPrompt clears ephemeral investigation context when
// a new explicit $prompt arrives. It wipes the transient in-memory forensic
// buffers (handoffLedgerContent, lastInvestigateLedger, handoffCtx forensic
// fields) so plan synthesis prompts start clean. Structured staged tasks
// (PendingTodos / sess.CurrentTasks) are preserved — they are the explicit
// chaining contract, not ephemeral diagnostics.
func (m *model) ClearForensicStateForNewPrompt() {
	m.handoffLedgerContent = ""
	m.lastInvestigateLedger = nil
	m.handoffCtx.ProposedFix = ""
	m.handoffCtx.LastFailurePayload = ""
	m.handoffCtx.TargetScope = ""
	// Strip (do not preserve) synthetic placeholders from any session ledger
	// diagnostics that would otherwise re-prime the next handoff. Real build
	// signal is preserved by StripSyntheticPackageRootPlaceholders itself.
	if m.sess != nil && m.sess.ContextLedger != nil {
		cleaned := plan.StripSyntheticPackageRootPlaceholders(m.sess.ContextLedger.Diagnostics)
		if cleaned != m.sess.ContextLedger.Diagnostics {
			m.sess.ContextLedger.Diagnostics = cleaned
			_ = m.sess.ContextLedger.Save()
		}
	}
}

// clearForensicStateOnInvestigateExit clears ephemeral investigation context
// when transitioning OUT of investigate mode to a mode that does not
// explicitly chain forensic findings. Plan and investigate targets preserve
// the structured ledger (explicit chaining); every other target gets a clean
// slate so stale 'package root (:0)' instructions never leak.
func (m *model) clearForensicStateOnInvestigateExit(targetModeName string) {
	lower := strings.ToLower(targetModeName)
	if lower == "plan" || lower == "investigate" {
		return
	}
	m.lastInvestigateLedger = nil
}

// stripSyntheticPlaceholdersFromHandoff removes synthetic 'package root (:0)'
// placeholders from an in-memory handoff payload when no active build errors
// are present. Real error coordinates are preserved verbatim.
func stripSyntheticPlaceholdersFromHandoff(handoff string) string {
	return plan.StripSyntheticPackageRootPlaceholders(handoff)
}
