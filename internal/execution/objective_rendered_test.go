package execution

import "testing"

// ── RENDERED-LAYOUT VERIFICATION GATES COMPLETION ───────────────────────────
//
// These tests pin the mission's rendered-verification contract at the authority:
// a broad objective whose source-level gate is skipped is PROVEN only when a
// REAL rendered verification ran and passed. A screenshot is never a pass, a
// failed rendered check is a failure, and a blocked check leaves the objective
// UNVERIFIED.

func broadRenderedEvidence() ObjectiveEvidence {
	ev := provenPatchEvidence()
	ev.VerificationRan = false
	ev.VerificationPassed = false
	ev.VerificationSkipped = true
	ev.BroadObjective = true
	ev.Conditions = []CompletionCondition{{
		ID:         "cond-integrity-held",
		Obligation: ObligationIntegrityHeld,
	}}
	return ev
}

func TestRendered_PassingCheckSatisfiesBroadIntegrity(t *testing.T) {
	ev := broadRenderedEvidence()
	ev.RenderedVerification = RenderedVerificationEvidence{
		Ran:          true,
		Passed:       true,
		Revision:     "rev-1",
		Browser:      "test-browser",
		Observations: []string{"rendered index", "check title: satisfied"},
	}
	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), ev)
	if outcome != ObjectiveProven {
		t.Fatalf("outcome = %s, want PROVEN with a real passing rendered check", outcome)
	}
	if got := ev.VerifierVerdict(); got != "RENDERED_PASS" {
		t.Fatalf("verifier verdict = %s, want RENDERED_PASS", got)
	}
}

func TestRendered_FailedCheckIsNotProven(t *testing.T) {
	ev := broadRenderedEvidence()
	ev.RenderedVerification = RenderedVerificationEvidence{
		Ran:     true,
		Passed:  false,
		Defects: []string{"check title failed"},
	}
	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), ev)
	if outcome.Proves() {
		t.Fatal("a failed rendered check was accepted as PROVEN")
	}
	conditions := ReduceConditions(ev.Conditions, ev)
	if len(conditions) != 1 || conditions[0].VerificationState != VerifyGateFail {
		t.Fatalf("integrity state = %+v, want GATE_FAIL", conditions)
	}
}

func TestRendered_BlockedLeavesUnverified(t *testing.T) {
	ev := broadRenderedEvidence()
	ev.RenderedVerification = RenderedVerificationEvidence{
		BlockedReason: "no local browser executable is available",
	}
	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), ev)
	if outcome.Proves() {
		t.Fatal("a blocked rendered check was accepted as PROVEN")
	}
	conditions := ReduceConditions(ev.Conditions, ev)
	if len(conditions) != 1 || conditions[0].VerificationState != VerifyGateUnverified {
		t.Fatalf("integrity state = %+v, want GATE_UNVERIFIED", conditions)
	}
}

// TestRendered_NonBroadSkippedGateUnchanged pins that the rendered integration
// did not change the legacy, content-determined behaviour: a skipped verifier
// on a non-broad objective remains provably NOT_APPLICABLE.
func TestRendered_NonBroadSkippedGateUnchanged(t *testing.T) {
	ev := provenPatchEvidence()
	ev.VerificationRan = false
	ev.VerificationPassed = false
	ev.VerificationSkipped = true
	ev.BroadObjective = false
	outcome := NewObjectiveCompletionAuthority().Evaluate(patchContract(), ev)
	if outcome != ObjectiveProven {
		t.Fatalf("outcome = %s, want PROVEN for a non-broad objective with a not-applicable gate", outcome)
	}
}
