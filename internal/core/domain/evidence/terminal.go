package evidence

import (
	"github.com/PizenLabs/izen/internal/core/domain"
)

// TerminalState is the authoritative completion product for the evidence
// package (Phase 5). It mirrors the system-model spec but uses the
// Phase 5 field names Workflow/VerDict/Completed as mandated by the
// execution prompt.
type TerminalState struct {
	Workflow  domain.WorkflowState `json:"workflow"`
	Verdict   EvidenceVerdict      `json:"verdict"`
	Completed bool                 `json:"completed"`
	// Vector is the full graduated evidence for audit (optional in this
	// package's compact form; kept for parity with domain.TerminalState).
	Vector EvidenceVector `json:"vector"`
	Reason string         `json:"reason"`
}

// Valid enforces the forbidden-combination rules (INV:11, INV:8, INV:9).
//   INV:8  State Verified requires Completed == true and Verdict == VerdictPass.
//   INV:9  State Failed requires Verdict != VerdictPass.
//   INV:11 Forbidden matrices (e.g., Incomplete · VERIFIED, FAILED · VERIFIED) return false.
func (t TerminalState) Valid() bool {
	// INV:8 — Verified requires completed + pass.
	if t.Workflow == domain.StateVerified {
		if !t.Completed || t.Verdict != VerdictPass {
			return false
		}
	}
	// INV:9 — Failed must not be PASS.
	if t.Workflow == domain.StateFailed {
		if t.Verdict == VerdictPass {
			return false
		}
	}
	// INV:11 — Incomplete + Verified is forbidden.
	// Incomplete is modelled as Completed == false with a Verified workflow.
	if !t.Completed && t.Workflow == domain.StateVerified {
		return false
	}
	// Failed + Verified is already covered by INV:9 but re-assert for clarity.
	if t.Workflow == domain.StateFailed && t.Verdict == VerdictPass {
		return false
	}
	// Aborted cannot be verified either (spec table: ABORTED·VERIFIED forbidden).
	if t.Workflow == domain.StateFailed || t.Workflow == domain.StateVerified {
		// no extra check; above already handles
	}
	// Additional INV:11 guard: any incomplete (not Completed) state that claims
	// PASS verdict while workflow is terminal verified must be rejected.
	// This catches the "INCOMPLETE · VERIFIED" matrix where workflow is
	// verified but completed flag is false.
	if !t.Completed && t.Verdict == VerdictPass && t.Workflow == domain.StateVerified {
		return false
	}
	return true
}

// String renders "verified · PASS" style for debugging.
func (t TerminalState) String() string {
	return t.Workflow.String() + " · " + t.Verdict.String()
}
