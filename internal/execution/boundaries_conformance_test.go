package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// tolerantFullRewriteProfile is the pre-incident contract: a targeted mutation
// whose artifact tolerates a complete-file replacement (the FULL_REWRITE shape
// Boundary 2 must feasibility-check).
func tolerantFullRewriteProfile(maxOutput int) strategy.ExecutionStrategyProfile {
	return strategy.ExecutionStrategyProfile{
		Strategy:        strategy.TargetedMutation,
		ModelRequired:   true,
		StrategyReason:  "conformance: full-artifact mutation contract",
		Artifact:        strategy.ArtifactContract{Kind: "replace_block"},
		MaxOutputTokens: maxOutput,
	}
}

// tolerantFullRewriteProfilePtr is the pointer form carried on ExecuteRequest.
func tolerantFullRewriteProfilePtr(maxOutput int) *strategy.ExecutionStrategyProfile {
	p := tolerantFullRewriteProfile(maxOutput)
	return &p
}

// ── Conformance verification of the 5-Boundary Zero-Trust Architecture ─────
//
// Test Case A — Preflight Infeasibility Trapping:
//   a FULL_REWRITE request whose EstimatedTokens
//   (= TargetFileTokens × FullRewriteTokenMultiplier) exceeds max_output must
//   HALT at Boundary 2 with ZERO HTTP provider requests.
//
// Test Case B — Output Exhaustion Circuit Breaking:
//   a simulated finish_reason="length" response must be caught at Boundary 3,
//   DISCARDING the output without parsing artifacts or staging mutations.

// TestConformanceA_PreflightInfeasibilityTrapping pins Boundary 2: an
// infeasible FULL_REWRITE is refused BEFORE any provider request. The mock
// provider records every call; zero calls is the assertion.
func TestConformanceA_PreflightInfeasibilityTrapping(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", reproIndexHTML)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "any generation",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 10, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "conformance-a",
		Mode:            "autonomy",
		Prompt:          "rewrite index.html",
		Targets:         []string{"index.html"},
		Strategy:        tolerantFullRewriteProfilePtr(1024),
		MaxOutputTokens: 1024,
	})

	// The boundary rejects deterministically.
	if !errors.Is(err, ErrPreflightInfeasible) {
		t.Fatalf("err = %v, want ErrPreflightInfeasible", err)
	}
	if res == nil || res.Proof == nil || res.Proof.Outcome != OutcomePreflightInfeasible {
		t.Fatalf("proof outcome = %+v, want %s", res.Proof, OutcomePreflightInfeasible)
	}

	// ZERO provider requests crossed Boundary 2.
	if got := mock.calls(); got != 0 {
		t.Fatalf("provider requests = %d, want 0 (Boundary 2 must trap before any HTTP request)", got)
	}

	// No artifact exists, nothing is staged, nothing was applied.
	if res.PendingPatchID != "" || res.ArtifactKind != "" || res.Content != "" {
		t.Fatalf("a trapped preflight produced artifact surface: patch=%q kind=%q",
			res.PendingPatchID, res.ArtifactKind)
	}
	if got := mustRead(t, root, "index.html"); got != reproIndexHTML {
		t.Fatal("workspace changed on a preflight-rejected request")
	}

	// The advisory diagnostic carries the feasibility math, never content.
	if n := len(res.Diagnostics); n != 1 {
		t.Fatalf("diagnostics = %d, want 1", n)
	}
	d := res.Diagnostics[0]
	if d.Subtype != SignalPreflightInfeasible || d.Target != "index.html" || d.Retryable {
		t.Fatalf("diagnostic = %+v, want preflight_infeasible / non-retryable", d)
	}
	if !strings.Contains(d.Detail, "estimated=") || strings.Contains(d.Detail, "generation") {
		t.Fatalf("diagnostic detail must be metadata only: %q", d.Detail)
	}
}

// TestConformanceA_PreflightFeasibleStillExecutes guards the inverse: a
// feasible full rewrite crosses Boundary 2 and reaches the provider normally.
func TestConformanceA_PreflightFeasibleStillExecutes(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal) // tiny target

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "foo\nQUX\nbaz\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "conformance-a-feasible",
		Mode:            "build",
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("feasible rewrite did not stage at the approval gate")
	}
	if got := mock.calls(); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

// TestConformanceB_OutputExhaustionCircuitBreaking pins Boundary 3: a
// finish_reason=length response is circuit-broken BEFORE artifact parsing.
// The simulated payload is a PERFECTLY VALID SEARCH/REPLACE block — if the
// runtime parsed it, it would stage a mutation. It must not.
func TestConformanceB_OutputExhaustionCircuitBreaking(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", reproIndexHTML)

	// The truncated stream carries a well-formed hunk: parsing it would be a
	// violation. The output gate must discard it on the authoritative
	// finish_reason alone.
	truncatedButParseable := &ai.Response{
		Content: "<<<<<<< SEARCH\n<p>incident filler line with stable anchor text</p>\n=======\n<p>REWRITTEN</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1024, Known: true, FinishReason: "length"},
	}
	mock := &mockProvider{responses: []*ai.Response{truncatedButParseable}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "conformance-b",
		Mode:            "autonomy",
		Prompt:          "rewrite index.html",
		Targets:         []string{"index.html"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})

	// Canonical classification at the gate.
	var gateErr *OutputGateError
	if !errors.As(err, &gateErr) || gateErr.Outcome != CanonicalOutputExhausted {
		t.Fatalf("err = %v, want OutputGateError OUTPUT_EXHAUSTED", err)
	}
	if !errors.Is(err, ErrOutputExhausted) || !errors.Is(err, ErrOutputTruncated) {
		t.Fatal("canonical error chain lost ErrOutputExhausted / legacy sentinel")
	}
	if res == nil || res.Proof.Outcome != OutcomeTruncated {
		t.Fatalf("proof outcome = %+v, want truncated", res.Proof)
	}

	// NO parsing artifacts, NO mutation loop inside the executor.
	if got := mock.calls(); got != 1 {
		t.Fatalf("provider requests = %d, want exactly 1 (circuit break forbids internal retries)", got)
	}
	if res.PendingPatchID != "" || res.ArtifactKind != "" || res.Content != "" || res.Diff != "" {
		t.Fatalf("exhausted generation leaked into artifact surfaces: patch=%q kind=%q content=%d bytes",
			res.PendingPatchID, res.ArtifactKind, len(res.Content))
	}
	if got := mustRead(t, root, "index.html"); got != reproIndexHTML {
		t.Fatal("the discarded generation touched the workspace")
	}

	// Only the advisory diagnostic signal crosses toward recovery (I2).
	if n := len(res.Diagnostics); n != 1 {
		t.Fatalf("diagnostics = %d, want 1", n)
	}
	d := res.Diagnostics[0]
	if d.Subtype != SignalOutputExhausted || !d.Retryable {
		t.Fatalf("diagnostic = %+v, want OUTPUT_EXHAUSTED retryable", d)
	}
	if strings.Contains(d.Detail, "SEARCH") || strings.Contains(res.Diagnostics[0].Detail, "REWRITTEN") {
		t.Fatalf("diagnostic leaked rejected artifact bytes: %q", d.Detail)
	}

	// The billed invocation survives the circuit break (truthful accounting).
	if len(res.Proof.ModelInvocations) != 1 || res.Proof.ModelInvocations[0].FinishReason != "length" {
		t.Fatalf("model invocations = %+v, want one invocation with finish_reason=length", res.Proof.ModelInvocations)
	}
}

// TestConformanceB_RefusalCircuitBreaks covers the PROVIDER_REFUSAL arm.
func TestConformanceB_RefusalCircuitBreaks(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "I cannot help with that",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 20, CompletionTokens: 6, FinishReason: "content_filter"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "conformance-b-refusal",
		Mode:      "autonomy",
		Prompt:    "change bar to qux",
		Targets:   []string{"note.txt"},
		Strategy:  searchReplaceProfile(),
	})
	var gateErr *OutputGateError
	if !errors.As(err, &gateErr) || gateErr.Outcome != CanonicalProviderRefusal {
		t.Fatalf("err = %v, want PROVIDER_REFUSAL gate error", err)
	}
	if !errors.Is(err, ErrProviderRefused) {
		t.Fatal("refusal sentinel lost")
	}
	if res.PendingPatchID != "" || mock.calls() != 1 {
		t.Fatalf("refusal staged or looped: patch=%q calls=%d", res.PendingPatchID, mock.calls())
	}
}

// TestNormalizeFinishReasonVocabulary unit-covers the canonical mapping.
func TestNormalizeFinishReasonVocabulary(t *testing.T) {
	cases := []struct {
		in   string
		want CanonicalOutcome
	}{
		{"stop", CanonicalComplete},
		{"end_turn", CanonicalComplete},
		{"length", CanonicalOutputExhausted},
		{"max_tokens", CanonicalOutputExhausted},
		{"content_filter", CanonicalProviderRefusal},
		{"refusal", CanonicalProviderRefusal},
		{"", CanonicalUnknown},
		{"something_new", CanonicalUnknown},
	}
	for _, c := range cases {
		if got := NormalizeFinishReason(c.in); got != c.want {
			t.Errorf("NormalizeFinishReason(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestEvaluatePreflightFormula unit-covers the I5 estimate formula.
func TestEvaluatePreflightFormula(t *testing.T) {
	// 7780 bytes ≈ 1945 tokens × 3 = 5835 > 1024 ⇒ infeasible.
	v := EvaluatePreflight(PreflightRequest{TargetBytes: 7780, MaxOutputTokens: 1024})
	if v.Feasible || v.EstimatedTokens != 1945*FullRewriteTokenMultiplier {
		t.Fatalf("verdict = %+v, want infeasible with multiplier math", v)
	}
	// Small target fits.
	if v := EvaluatePreflight(PreflightRequest{TargetBytes: 700, MaxOutputTokens: 1024}); !v.Feasible {
		t.Fatalf("700-byte target verdict = %+v, want feasible", v)
	}
	// Bounded artifacts are exempt by construction.
	if v := EvaluatePreflight(PreflightRequest{ArtifactBounded: true, TargetBytes: 1 << 20, MaxOutputTokens: 64}); !v.Feasible {
		t.Fatalf("bounded artifact verdict = %+v, want feasible", v)
	}
	// Creations have no estimable baseline.
	if v := EvaluatePreflight(PreflightRequest{TargetBytes: 0, MaxOutputTokens: 128}); !v.Feasible {
		t.Fatalf("creation verdict = %+v, want feasible", v)
	}
	// Unbounded budgets defer to the output gate.
	if v := EvaluatePreflight(PreflightRequest{TargetBytes: 1 << 20, MaxOutputTokens: 0}); !v.Feasible {
		t.Fatalf("unbounded verdict = %+v, want feasible", v)
	}
}

// TestTreeDigestStabilityAndSensitivity unit-covers the Boundary-5 digest.
func TestTreeDigestStabilityAndSensitivity(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "a.txt", "alpha\n")
	writeTarget(t, root, "b.txt", "beta\n")
	v := NewOCCVerifier(root)

	d1 := v.TreeDigest([]string{"a.txt", "b.txt"})
	d2 := v.TreeDigest([]string{"b.txt", "a.txt"}) // order-independent
	if d1 == "" || d1 != d2 {
		t.Fatalf("digest unstable: %q vs %q", d1, d2)
	}

	writeTarget(t, root, "b.txt", "BETA\n")
	if d3 := v.TreeDigest([]string{"a.txt", "b.txt"}); d3 == d1 {
		t.Fatal("content change did not fork the workspace digest")
	}

	writeTarget(t, root, "c.txt", "new\n")
	if d4 := v.TreeDigest([]string{"a.txt", "b.txt", "c.txt"}); d4 == d1 && len(d4) > 0 {
		t.Fatal("target-set change did not fork the workspace digest")
	}
	if d := v.TreeDigest(nil); d == "" {
		t.Fatal("empty set must still hash deterministically")
	}
}

// calls returns the number of provider invocations the mock served.
func (m *mockProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}
