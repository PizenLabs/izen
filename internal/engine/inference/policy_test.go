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

// TestPolicySingleHypothesisNeverEscalates is the DoD single-candidate guard:
// a lone confident hypothesis (no runner-up — zero competing frameworks) must
// Proceed, never EscalateToHuman. Escalation semantics require TWO credible
// hypotheses competing within the delta threshold; a single Static HTML/CSS/JS
// candidate in a Vanilla/Static Web project must never deadlock planning on
// "cannot choose a framework unilaterally".
func TestPolicySingleHypothesisNeverEscalates(t *testing.T) {
	set := NewInferenceSet()
	set.set(TypeFramework, []hypothesis{
		{label: "Static HTML/CSS/JS", evidence: []EvidenceTrace{
			{Source: SourceWorkspace, ID: "index.html", Weight: 0.20, Reason: "file"},
			{Source: SourceWorkspace, ID: "styles.css", Weight: 0.20, Reason: "file"},
			{Source: SourceWorkspace, ID: "script.js", Weight: 0.20, Reason: "file"},
		}},
	})

	verdict := NewPolicyEngine().Evaluate(set, TypeFramework)
	if verdict.Decision != DecisionProceed {
		t.Fatalf("decision = %q, want %q (single hypothesis, runner_up 0.00)", verdict.Decision, DecisionProceed)
	}
	if verdict.Top.Label != "Static HTML/CSS/JS" {
		t.Fatalf("top = %q, want Static HTML/CSS/JS", verdict.Top.Label)
	}
	if verdict.RunnerUp != nil {
		t.Fatalf("runner-up = %+v, want nil for a single hypothesis", verdict.RunnerUp)
	}
}

// TestPolicyTwoHypothesesWithinDeltaStillEscalate pins the guard's boundary:
// the delta escalation must still fire for a genuine two-candidate race even
// at a high confidence level.
func TestPolicyTwoHypothesesWithinDeltaStillEscalate(t *testing.T) {
	set := NewInferenceSet()
	set.set(TypeFramework, []hypothesis{
		{label: "Static HTML/CSS/JS", evidence: []EvidenceTrace{
			{Source: SourceWorkspace, ID: "index.html", Weight: 0.20, Reason: "file"},
			{Source: SourceWorkspace, ID: "styles.css", Weight: 0.20, Reason: "file"},
			{Source: SourceWorkspace, ID: "script.js", Weight: 0.20, Reason: "file"},
		}},
		{label: "React + Vite", evidence: []EvidenceTrace{
			{Source: SourcePrompt, ID: "react", Weight: 0.20, Reason: "prompt"},
			{Source: SourceWorkspace, ID: "src/", Weight: 0.20, Reason: "dir"},
			{Source: SourceConfig, ID: "vite.config.ts", Weight: 0.10, Reason: "cfg"},
		}},
	})

	// The runner-up (0.50) is within 0.15 of the top (0.60): real competition
	// must still escalate — the single-candidate guard must not rescue it.
	verdict := NewPolicyEngine().Evaluate(set, TypeFramework)
	if verdict.Decision != DecisionEscalateToHuman {
		t.Fatalf("decision = %q, want %q (real competition within delta)", verdict.Decision, DecisionEscalateToHuman)
	}
	if verdict.RunnerUp == nil || verdict.RunnerUp.Label != "React + Vite" {
		t.Fatalf("runner-up = %+v, want React + Vite", verdict.RunnerUp)
	}
}
