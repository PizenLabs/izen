package strategy

// PHASE 12 — the per-invocation output budget is DERIVED, not a universal
// constant.
//
// The reported production failure was a whole-new-file creation dispatched
// under a flat 4 096-token ceiling. Any artifact larger than ~16 KB of source
// was GUARANTEED to hit the output gate (finish_reason=length) and the
// truncated prefix was then discarded — a guaranteed failure paid for in full
// model computation.
//
// These tests pin the corrected properties:
//   - the creation budget is derived from the complexity tier, deterministically;
//   - it is monotonic and bounded;
//   - every other artifact kind keeps its contract-shaped fixed bound;
//   - the derived REQUEST is still clamped against the model's real ceiling by
//     the shared capability chain, so raising it can never overspend.

import "testing"

// TestPhase12_CreationBudgetIsDerivedFromComplexity proves the creation budget
// is a function of the measured complexity tier, not a magic constant.
func TestPhase12_CreationBudgetIsDerivedFromComplexity(t *testing.T) {
	want := map[ComplexityLevel]int{
		ComplexityLow:    CreationTokenTiers[ComplexityLow],
		ComplexityMedium: CreationTokenTiers[ComplexityMedium],
		ComplexityHigh:   CreationTokenTiers[ComplexityHigh],
	}
	for level, budget := range want {
		if got := outputForArtifact("create_file", level); got != budget {
			t.Errorf("outputForArtifact(create_file, %v) = %d, want %d", level, got, budget)
		}
	}
	// Deterministic: the same tier always yields the same request.
	for i := 0; i < 3; i++ {
		if outputForArtifact("create_file", ComplexityHigh) != CreationTokenTiers[ComplexityHigh] {
			t.Fatal("creation budget derivation is not deterministic")
		}
	}
	// Monotonic in complexity.
	if CreationTokenTiers[ComplexityLow] >= CreationTokenTiers[ComplexityMedium] ||
		CreationTokenTiers[ComplexityMedium] >= CreationTokenTiers[ComplexityHigh] {
		t.Fatalf("creation tiers are not monotonic: %v", CreationTokenTiers)
	}
	// Bounded: no derivation exceeds the hard invocation ceiling.
	for level, budget := range CreationTokenTiers {
		if budget > CreationTokenBudget {
			t.Errorf("tier %v budget %d exceeds the invocation ceiling %d", level, budget, CreationTokenBudget)
		}
		if budget <= 0 {
			t.Errorf("tier %v budget %d is not a usable request", level, budget)
		}
	}
	// An unknown tier falls back to the medium tier rather than to zero.
	unknown := ComplexityLevel(99)
	if got := outputForArtifact("create_file", unknown); got != CreationTokenTiers[ComplexityMedium] {
		t.Fatalf("unknown tier budget = %d, want the medium fallback %d", got, CreationTokenTiers[ComplexityMedium])
	}
}

// TestPhase12_CreationBudgetIsLargerThanAnchoredPatch proves the relative
// ordering the artifact shapes require is preserved: a creation must be able to
// carry a whole new file, an anchored patch never needs to.
func TestPhase12_CreationBudgetIsLargerThanAnchoredPatch(t *testing.T) {
	for _, level := range []ComplexityLevel{ComplexityLow, ComplexityMedium, ComplexityHigh} {
		create := outputForArtifact("create_file", level)
		patch := outputForArtifact("replace_block", level)
		if create <= patch {
			t.Errorf("tier %v: creation budget %d must exceed the anchored-patch budget %d", level, create, patch)
		}
	}
}

// TestPhase12_OtherArtifactKindsAreUnchanged pins the scope of the repair: the
// derived budget applies to the creation contract alone.
func TestPhase12_OtherArtifactKindsAreUnchanged(t *testing.T) {
	for _, c := range []struct {
		kind string
		want int
	}{
		{"plan", 1536},
		{"investigation", 2048},
		{"explanation", 1024},
		{"response", 512},
		{"replace_block", 1024},
		{"replace_file", 1024},
	} {
		if got := outputForArtifact(c.kind, ComplexityLow); got != c.want {
			t.Errorf("outputForArtifact(%q, low) = %d, want %d", c.kind, got, c.want)
		}
	}
	if got := outputForArtifact("replace_block", ComplexityHigh); got != 3072 {
		t.Errorf("outputForArtifact(replace_block, high) = %d, want 3072", got)
	}
}

// TestPhase12_WithBudgetsWiresTheDerivedCreationBudget proves the derivation is
// on the live path: withBudgets is what every selected profile passes through,
// so a medium-complexity creation really is dispatched with the derived request.
func TestPhase12_WithBudgetsWiresTheDerivedCreationBudget(t *testing.T) {
	profile := ExecutionStrategyProfile{
		Strategy:      TargetedMutation,
		ModelRequired: true,
		Artifact:      ArtifactContract{Kind: "create_file", Bounded: true},
		Complexity:    Complexity{Level: ComplexityMedium, Score: 5},
		ContextPolicy: ContextPolicyTargetFileOnly,
	}
	got := withBudgets(profile)
	if got.MaxOutputTokens != CreationTokenTiers[ComplexityMedium] {
		t.Fatalf("profile MaxOutputTokens = %d, want the derived %d", got.MaxOutputTokens, CreationTokenTiers[ComplexityMedium])
	}
	// A deterministic model-required=false profile still gets no output budget
	// and spends nothing.
	none := withBudgets(ExecutionStrategyProfile{Strategy: TargetedMutation, ModelRequired: false})
	if none.MaxOutputTokens != 0 {
		t.Fatalf("a no-model profile must carry no output budget, got %d", none.MaxOutputTokens)
	}
}
