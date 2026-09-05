package autonomy

import (
	"os"
	"testing"
)

func TestAdaptiveSelectStrategy_SmallFileLargeBudget(t *testing.T) {
	d := t.TempDir()
	small := d + "/small.html"
	os.WriteFile(small, []byte("<html></html>"), 0644)

	// Small file + generous budget → FULL_REWRITE permitted.
	got := AdaptiveSelectStrategy(small, 50, 4096)
	if got != StrategyFullArtifact {
		t.Errorf("small file + large budget = %q, want FULL_REWRITE (full artifact)", got)
	}
}

func TestAdaptiveSelectStrategy_LargeFileForcesBoundedPatch(t *testing.T) {
	d := t.TempDir()
	big := d + "/big.html"
	os.WriteFile(big, []byte("<html>"+string(make([]byte, 600))+"</html>"), 0644)

	// File > 500 bytes → BOUNDED_PATCH forced regardless of budget.
	got := AdaptiveSelectStrategy(big, 600, 4096)
	if got != StrategyBoundedPatch {
		t.Errorf("large file = %q, want BOUNDED_PATCH", got)
	}
}

func TestAdaptiveSelectStrategy_SmallBudgetForcesBoundedPatch(t *testing.T) {
	d := t.TempDir()
	small := d + "/small.html"
	os.WriteFile(small, []byte("<p>hello</p>"), 0644)

	// Budget <= 2048 tokens → BOUNDED_PATCH forced.
	got := AdaptiveSelectStrategy(small, 50, 1024)
	if got != StrategyBoundedPatch {
		t.Errorf("small budget = %q, want BOUNDED_PATCH", got)
	}
}
