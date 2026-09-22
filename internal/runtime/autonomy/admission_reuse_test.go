package autonomy

// PHASE 8 M6 — Driver-level tests for the canonical admission/continuation
// library reuse (internal/runtime/autonomy/admission.go).
//
// These tests drive the Driver-owned helpers (never the pure packages'
// schedulers — those do not exist): REFINE / BLOCK / OUTPUT_CEILING /
// STALE via AdmitDriverStep, and STALE / CONTINUE / COMPLETE via
// DeriveDriverContinuation, plus the outcome-vocabulary mapping.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/continuation"
	"github.com/PizenLabs/izen/internal/stepadmission"
)

func TestAdmitDriverStep_AdmitsBoundedStep(t *testing.T) {
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"pkg/foo.go"},
		Intent:          "refactor handler",
		MaxOutputTokens: 2048,
		AllowedScope:    []string{"pkg/"},
	})
	if decision.Action != stepadmission.ActionAdmit {
		t.Fatalf("action = %s (%s), want ADMIT", decision.Action, decision.Reason)
	}
	if decision.Step == nil {
		t.Fatal("admitted decision must carry the examined step")
	}
	if decision.Step.Intent != "refactor handler" {
		t.Fatalf("intent = %q, want preserved objective", decision.Step.Intent)
	}
}

func TestAdmitDriverStep_ZeroCeilingIsUnbounded(t *testing.T) {
	// OUTPUT_CEILING: unknown ceiling must not block admission on its own
	// (execution preflight still gates later when a real ceiling is known).
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"pkg/foo.go"},
		Intent:          "refactor handler",
		MaxOutputTokens: 0,
		AllowedScope:    []string{"pkg/"},
	})
	if decision.Action != stepadmission.ActionAdmit {
		t.Fatalf("action = %s (%s), want ADMIT for unknown ceiling", decision.Action, decision.Reason)
	}
}

func TestAdmitDriverStep_BlocksSingleOversizedTarget(t *testing.T) {
	// A single evidence-backed file cannot be split without inventing
	// targets: an estimate above the ceiling must BLOCK, never fabricate.
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"pkg/huge.go"},
		Intent:          "rewrite everything",
		MaxOutputTokens: 140, // 140-128 planning margin → 64 token budget
		AllowedScope:    []string{"pkg/"},
	})
	if decision.Action != stepadmission.ActionBlock {
		t.Fatalf("action = %s (%s), want BLOCK", decision.Action, decision.Reason)
	}
	if decision.RefinedStep != nil {
		t.Fatal("BLOCK must not carry a refined step")
	}
}

func TestAdmitDriverStep_RefinesMultiTargetStep(t *testing.T) {
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"pkg/a.go", "pkg/b.go", "pkg/c.go", "pkg/d.go"},
		Intent:          "refactor handlers",
		MaxOutputTokens: 300,
		AllowedScope:    []string{"pkg/"},
	})
	if decision.Action != stepadmission.ActionRefine {
		t.Fatalf("action = %s (%s), want REFINE", decision.Action, decision.Reason)
	}
	refined := decision.RefinedStep
	if refined == nil {
		t.Fatal("REFINE must carry a refined step")
	}
	if len(refined.Targets) >= 4 {
		t.Fatalf("refined targets = %v, want a strict subset", refined.Targets)
	}
	for _, rt := range refined.Targets {
		found := false
		for _, orig := range []string{"pkg/a.go", "pkg/b.go", "pkg/c.go", "pkg/d.go"} {
			if rt == orig {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("refined target %q was invented (not a subset)", rt)
		}
	}
	if refined.Intent != "refactor handlers" {
		t.Fatalf("refined intent = %q, want preserved objective", refined.Intent)
	}
}

func TestAdmitDriverStep_StaleDigestRefusesAdmission(t *testing.T) {
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:            []string{"pkg/foo.go"},
		Intent:             "refactor handler",
		StateDigest:        "digest-at-derivation",
		CurrentFingerprint: "digest-now-different",
		MaxOutputTokens:    2048,
		AllowedScope:       []string{"pkg/"},
	})
	if decision.Action != stepadmission.ActionStale {
		t.Fatalf("action = %s (%s), want STALE on fingerprint drift", decision.Action, decision.Reason)
	}

	explicit := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"pkg/foo.go"},
		Intent:          "refactor handler",
		IsStale:         true,
		MaxOutputTokens: 2048,
		AllowedScope:    []string{"pkg/"},
	})
	if explicit.Action != stepadmission.ActionStale {
		t.Fatalf("action = %s (%s), want STALE on explicit drift", explicit.Action, explicit.Reason)
	}
}

func TestAdmitDriverStep_ScopeEscapeRequiresApproval(t *testing.T) {
	decision := AdmitDriverStep(DriverAdmissionInput{
		Targets:         []string{"other/file.go"},
		Intent:          "refactor handler",
		MaxOutputTokens: 2048,
		AllowedScope:    []string{"pkg/allowed/"},
	})
	if decision.Action != stepadmission.ActionAwaitingApproval {
		t.Fatalf("action = %s (%s), want AWAITING_APPROVAL on scope escape", decision.Action, decision.Reason)
	}
}

func TestDeriveDriverContinuation_StaleOnDrift(t *testing.T) {
	// The Boundary-5 drift confirmation consumed by observeAndRun: caller-
	// observed drift must yield STALE through the pure transition function.
	decision := DeriveDriverContinuation(DriverContinuationInput{
		Objective:        "refactor handler",
		Targets:          []string{"pkg/foo.go"},
		StateFingerprint: "digest-at-derivation",
		HasStaleState:    true,
		PreviousOutcome:  "failed",
		AllowedScope:     []string{"pkg/"},
		ProviderCeiling:  2048,
	})
	if decision.Action != continuation.ActionStale {
		t.Fatalf("action = %s (%s), want STALE on observed drift", decision.Action, decision.Reason)
	}
}

func TestDeriveDriverContinuation_ContinuesPartialFromDurableScope(t *testing.T) {
	// finish_reason=length truncation derives the next bounded step from
	// durable scope — never a blind retry of the same prompt.
	decision := DeriveDriverContinuation(DriverContinuationInput{
		Objective:        "refactor handler",
		Targets:          []string{"pkg/foo.go"},
		StateFingerprint: "digest-now",
		IsPartialOutput:  true,
		PreviousOutcome:  "partial",
		AllowedScope:     []string{"pkg/"},
		ProviderCeiling:  2048,
	})
	if decision.Action != continuation.ActionContinue {
		t.Fatalf("action = %s (%s), want CONTINUE on partial output", decision.Action, decision.Reason)
	}
	if decision.NextStep == nil {
		t.Fatal("CONTINUE must carry the next bounded step proposal")
	}
	if len(decision.NextStep.Targets) == 0 {
		t.Fatal("next step must name evidence-backed targets")
	}
}

func TestDeriveDriverContinuation_CompletesVerifiedWork(t *testing.T) {
	decision := DeriveDriverContinuation(DriverContinuationInput{
		Objective:        "refactor handler",
		Targets:          []string{"pkg/foo.go"},
		StateFingerprint: "digest-now",
		PreviousOutcome:  "complete",
		Verified:         true,
		AllowedScope:     []string{"pkg/"},
		ProviderCeiling:  2048,
		Observations: []continuation.Observation{
			{Kind: continuation.KindExecutionResult, Subject: "pkg/foo.go", Detail: "patch applied"},
			{Kind: continuation.KindVerification, Subject: "pkg/foo.go", Detail: "build clean"},
		},
	})
	if decision.Action != continuation.ActionComplete {
		t.Fatalf("action = %s (%s), want COMPLETE for verified work", decision.Action, decision.Reason)
	}
}

func TestDriverOutcomeToStepOutcome(t *testing.T) {
	cases := []struct {
		outcome autonomy.ExecutionOutcome
		want    string
	}{
		{autonomy.OutcomeTruncated, "partial"},
		{autonomy.OutcomeFailed, "failed"},
		{autonomy.OutcomePatchGenFailed, "failed"},
		{autonomy.OutcomePatchFailed, "failed"},
		{autonomy.OutcomeApplyFailed, "failed"},
		{autonomy.OutcomeVerifyFailed, "failed"},
		{autonomy.OutcomeArtifactRejected, "failed"},
		{autonomy.OutcomeArtifactRetryableRejected, "failed"},
		{autonomy.OutcomeSkipped, "failed"},
		{autonomy.OutcomePreflightInfeasible, "failed"},
		{autonomy.OutcomeWorkspaceDrift, "failed"},
		{autonomy.OutcomeNoOpObjectiveUnresolved, "failed"},
	}
	for _, tc := range cases {
		if got := driverOutcomeToStepOutcome(tc.outcome); got != tc.want {
			t.Errorf("driverOutcomeToStepOutcome(%q) = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}
