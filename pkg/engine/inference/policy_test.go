package inference

import "testing"

// TestPolicyEscalateOnCloseConfidence is the policy mandate: when the top two
// hypotheses' confidence delta is below 0.15, the engine must EscalateToHuman
// rather than choose unilaterally.
func TestPolicyEscalateOnCloseConfidence(t *testing.T) {
	set := NewInferenceSet()
	set.set(TypeFramework, []hypothesis{
		{label: "Next.js", evidence: []EvidenceTrace{
			{Source: SourceConfig, ID: "next.config.ts", Weight: 0.30, Reason: "config"},
			{Source: SourceDependency, ID: "next", Weight: 0.60, Reason: "dep"},
		}},
		{label: "Astro", evidence: []EvidenceTrace{
			{Source: SourceConfig, ID: "astro.config.mjs", Weight: 0.30, Reason: "config"},
			{Source: SourceDependency, ID: "astro", Weight: 0.50, Reason: "dep"},
		}},
	})

	verdict := NewPolicyEngine().Evaluate(set, TypeFramework)
	if verdict.Decision != DecisionEscalateToHuman {
		t.Fatalf("decision = %q, want %q (delta %.2f < 0.15)", verdict.Decision, DecisionEscalateToHuman, verdict.Delta)
	}
	if verdict.Top.Label != "Next.js" {
		t.Fatalf("top = %q, want Next.js", verdict.Top.Label)
	}
	if verdict.RunnerUp == nil || verdict.RunnerUp.Label != "Astro" {
		t.Fatalf("runner-up = %+v, want Astro", verdict.RunnerUp)
	}
}

// TestPolicyProceedOnClearSeparation: a decisive winner proceeds.
func TestPolicyProceedOnClearSeparation(t *testing.T) {
	set := NewInferenceSet()
	set.set(TypeFramework, []hypothesis{
		{label: "Next.js", evidence: []EvidenceTrace{
			{Source: SourceConfig, ID: "next.config.ts", Weight: 0.30, Reason: "config"},
			{Source: SourceDependency, ID: "next", Weight: 0.60, Reason: "dep"},
		}},
		{label: "Astro", evidence: []EvidenceTrace{
			{Source: SourcePrompt, ID: "astro", Weight: 0.20, Reason: "prompt"},
		}},
	})

	verdict := NewPolicyEngine().Evaluate(set, TypeFramework)
	if verdict.Decision != DecisionProceed {
		t.Fatalf("decision = %q, want %q (delta %.2f)", verdict.Decision, DecisionProceed, verdict.Delta)
	}
}

// TestPolicyFallbackOnThinEvidence: no hypothesis or a weak winner falls back.
func TestPolicyFallbackOnThinEvidence(t *testing.T) {
	// No hypothesis at all.
	empty := NewInferenceSet()
	verdict := NewPolicyEngine().Evaluate(empty, TypeFramework)
	if verdict.Decision != DecisionFallback {
		t.Fatalf("empty decision = %q, want %q", verdict.Decision, DecisionFallback)
	}

	// Single weak hypothesis below the confidence threshold.
	weak := NewInferenceSet()
	weak.set(TypeFramework, []hypothesis{
		{label: "Astro", evidence: []EvidenceTrace{
			{Source: SourcePrompt, ID: "astro", Weight: 0.20, Reason: "prompt"},
		}},
	})
	verdict = NewPolicyEngine().Evaluate(weak, TypeFramework)
	if verdict.Decision != DecisionFallback {
		t.Fatalf("weak decision = %q, want %q", verdict.Decision, DecisionFallback)
	}
}

func TestPolicyCustomThresholds(t *testing.T) {
	set := NewInferenceSet()
	set.set(TypeFramework, []hypothesis{
		{label: "Next.js", evidence: []EvidenceTrace{{Source: SourceConfig, ID: "next.config.ts", Weight: 0.30, Reason: "cfg"}}},
		{label: "Astro", evidence: []EvidenceTrace{{Source: SourceConfig, ID: "astro.config.mjs", Weight: 0.25, Reason: "cfg"}}},
	})

	// Default: 0.30 below the 0.45 confidence threshold → fallback.
	if v := NewPolicyEngine().Evaluate(set, TypeFramework); v.Decision != DecisionFallback {
		t.Fatalf("default decision = %q, want fallback", v.Decision)
	}
	// Relaxed confidence threshold: 0.05 delta < 0.15 → escalate.
	relaxed := NewPolicyEngine(WithConfidenceThreshold(0.20))
	if v := relaxed.Evaluate(set, TypeFramework); v.Decision != DecisionEscalateToHuman {
		t.Fatalf("relaxed decision = %q, want escalate", v.Decision)
	}
	// Relaxed delta threshold too: 0.05 >= 0.05 → proceed.
	both := NewPolicyEngine(WithConfidenceThreshold(0.20), WithDeltaThreshold(0.05))
	if v := both.Evaluate(set, TypeFramework); v.Decision != DecisionProceed {
		t.Fatalf("fully relaxed decision = %q, want proceed", v.Decision)
	}
}
