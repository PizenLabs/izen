package ephemeral

import (
	"context"
	"errors"
	"testing"
)

func TestClassifierTaxonomy(t *testing.T) {
	c := FailureClassifier{}
	cases := []struct {
		name string
		in   ProviderError
		want FailureReason
	}{
		{"finish-length", ProviderError{FinishReason: "length"}, FailureTokenLimit},
		{"finish-length-case", ProviderError{FinishReason: "Length"}, FailureTokenLimit},
		{"token-context", ProviderError{Err: errors.New("this model's maximum context length is 128000 tokens")}, FailureTokenLimit},
		{"payload-truncated", ProviderError{Err: errors.New("model output exceeded max_tokens limit: ErrPayloadTruncated")}, FailureTokenLimit},
		{"http-429", ProviderError{HTTPStatus: 429}, FailureRateLimited},
		{"rate-dialect", ProviderError{Err: errors.New("rate_limit_exceeded, please retry")}, FailureRateLimited},
		{"http-503", ProviderError{HTTPStatus: 503}, FailureProviderUnavailable},
		{"http-500", ProviderError{HTTPStatus: 500}, FailureProviderUnavailable},
		{"conn-reset", ProviderError{Err: errors.New("read: connection reset by peer")}, FailureNetworkInterruption},
		{"user-cancel", ProviderError{Err: context.Canceled}, FailureUserCancelled},
		{"schema", ProviderError{Err: errors.New("response does not match JSON schema: missing required field")}, FailureSchemaValidation},
		{"conflict-412", ProviderError{HTTPStatus: 412}, FailureTargetConflict},
		{"drift", ProviderError{Err: errors.New("semantic drift detected in plan")}, FailureSemanticDrift},
		{"verification", ProviderError{Err: errors.New("verification failed: postcondition mismatch")}, FailureVerificationFailure},
		{"unknown", ProviderError{Err: errors.New("something entirely novel")}, FailureUnknown},
		{"empty", ProviderError{}, FailureUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Classify(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if !tc.want.Valid() {
				t.Fatalf("reason %q not in closed taxonomy", tc.want)
			}
		})
	}
}

func TestRouterTokenLimitCapacityRule(t *testing.T) {
	r := NewWorkerRouter([]WorkerDescriptor{
		{ID: "small", Provider: "ollama", ContextTokens: 8000, Capabilities: []string{"code-edit"}, Healthy: true},
		{ID: "large", Provider: "anthropic", ContextTokens: 200000, Capabilities: []string{"code-edit"}, Healthy: true},
	})
	// Failed worker had 128k: only the larger worker is eligible.
	res, err := r.Route(RouteRequest{
		Reason: FailureTokenLimit, FailedWorkerID: "mid", FailedProvider: "openai",
		Step:                 StepDefinition{ID: "s", RequiredCaps: []string{"code-edit"}},
		CurrentContextTokens: 128000, BudgetLeft: 2,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Worker.ID != "large" {
		t.Fatalf("got %q want large (equal-or-higher capacity)", res.Worker.ID)
	}
}

func TestRouterSchemaPrefersStrict(t *testing.T) {
	r := NewWorkerRouter([]WorkerDescriptor{
		{ID: "loose", Provider: "ollama", ContextTokens: 128000, Healthy: true},
		{ID: "strict", Provider: "openai", ContextTokens: 128000, StrictStructuredOutput: true, Healthy: true},
	})
	res, err := r.Route(RouteRequest{
		Reason: FailureSchemaValidation, FailedWorkerID: "x", FailedProvider: "other",
		Step: StepDefinition{ID: "s"}, BudgetLeft: 2,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Worker.ID != "strict" {
		t.Fatalf("got %q want strict structured-output worker", res.Worker.ID)
	}
}

func TestRouterExcludesUnhealthyAndFailedProvider(t *testing.T) {
	r := NewWorkerRouter([]WorkerDescriptor{
		{ID: "dead", Provider: "openai", ContextTokens: 128000, Healthy: false},
		{ID: "same-prov", Provider: "openai", ContextTokens: 128000, Healthy: true},
		{ID: "alt", Provider: "anthropic", ContextTokens: 128000, Healthy: true},
	})
	res, err := r.Route(RouteRequest{
		Reason: FailureRateLimited, FailedWorkerID: "dead-client", FailedProvider: "openai",
		Step: StepDefinition{ID: "s"}, BudgetLeft: 1,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.Worker.ID != "alt" {
		t.Fatalf("got %q want cross-provider failover to alt", res.Worker.ID)
	}
}

func TestRouterNoEligibleEscalates(t *testing.T) {
	r := NewWorkerRouter([]WorkerDescriptor{
		{ID: "only", Provider: "openai", ContextTokens: 8000, Healthy: true},
	})
	_, err := r.Route(RouteRequest{
		Reason: FailureTokenLimit, FailedWorkerID: "z", FailedProvider: "openai",
		Step: StepDefinition{ID: "s"}, CurrentContextTokens: 200000, BudgetLeft: 1,
	})
	if err == nil {
		t.Fatal("expected no-eligible-worker error (engine maps to ESCALATE)")
	}
}

func TestAssessmentGates(t *testing.T) {
	a := RecoveryAssessment{}
	cap := TaskCapsule{TaskID: "t", CurrentStep: StepDefinition{ID: "s"}}
	base := AssessmentInput{
		Reason: TrueReason(), PlanValid: true, TargetValid: true,
		EvidenceValid: true, CurrentStepClear: true,
		RecoveryAttemptsLeft: 2, Capsule: cap,
	}
	res, err := a.Assess(base, "w-b")
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if res.Action != ActionResume || res.TargetWorker != "w-b" {
		t.Fatalf("got %+v want RESUME to w-b", res)
	}
	// Drift veto.
	drift := base
	drift.SemanticDriftObserved = true
	if res, _ := a.Assess(drift, "w-b"); res.Action != ActionRePlan {
		t.Fatalf("drift action=%q want RE_PLAN", res.Action)
	}
	// Verification invalidation.
	inv := base
	inv.VerificationInvalidated = true
	if res, _ := a.Assess(inv, "w-b"); res.Action != ActionRePlan {
		t.Fatalf("invalidated action=%q want RE_PLAN", res.Action)
	}
	// Missing gate.
	nogate := base
	nogate.EvidenceValid = false
	if res, _ := a.Assess(nogate, "w-b"); res.Action != ActionRePlan {
		t.Fatalf("missing-gate action=%q want RE_PLAN", res.Action)
	}
	// Budget zero.
	nobudget := base
	nobudget.RecoveryAttemptsLeft = 0
	if res, _ := a.Assess(nobudget, "w-b"); res.Action != ActionEscalate {
		t.Fatalf("exhausted action=%q want ESCALATE", res.Action)
	}
}

func TrueReason() FailureReason { return FailureNetworkInterruption }
