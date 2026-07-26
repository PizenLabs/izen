package retrieval

import (
	"testing"
)

func TestTokenWeightEstimator_Empty(t *testing.T) {
	e := NewTokenWeightEstimator()
	if n := e.Estimate(""); n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestTokenWeightEstimator_Basic(t *testing.T) {
	e := NewTokenWeightEstimator()
	n := e.Estimate("hello world")
	if n <= 0 {
		t.Errorf("expected >0, got %d", n)
	}
	if n > 10 {
		t.Errorf("expected <=10 for short string, got %d", n)
	}
}

func TestTokenWeightEstimator_LongText(t *testing.T) {
	e := NewTokenWeightEstimator()
	text := ""
	for i := 0; i < 1000; i++ {
		text += "word "
	}
	n := e.Estimate(text)
	expected := len(text) / 4
	if n != expected {
		t.Errorf("expected %d, got %d", expected, n)
	}
}

func TestTokenWeightEstimator_EstimateAndFit(t *testing.T) {
	e := NewTokenWeightEstimator()
	text := ""
	for i := 0; i < 100; i++ {
		text += "word "
	}

	fitted, estimated := e.EstimateAndFit(text, 10)
	if estimated <= 10 {
		t.Errorf("estimated should be > max, got %d", estimated)
	}
	if fitted == text {
		t.Error("fitted text should be truncated")
	}
}

func TestTokenWeightEstimator_EstimateAndFitWithinBudget(t *testing.T) {
	e := NewTokenWeightEstimator()
	text := "hello world"

	fitted, estimated := e.EstimateAndFit(text, 10000)
	if fitted != text {
		t.Errorf("expected full text when within budget, got truncated")
	}
	if estimated <= 0 {
		t.Errorf("expected positive estimate, got %d", estimated)
	}
}

func TestTokenBudgetExceeded(t *testing.T) {
	err := TokenBudgetExceeded(100, 50)
	if err == nil {
		t.Fatal("expected error when estimated > max")
	}

	err = TokenBudgetExceeded(50, 100)
	if err != nil {
		t.Errorf("expected nil when estimated <= max, got %v", err)
	}

	err = TokenBudgetExceeded(50, 50)
	if err != nil {
		t.Errorf("expected nil when equal, got %v", err)
	}
}

func TestRecordFallbackEvidenceNilStore(t *testing.T) {
	rs := &ResultSet{Strategy: "test", Results: []Result{{File: "test.go"}}}
	ea, err := RecordFallbackEvidence(nil, "test", "query", rs)
	if err != nil {
		t.Errorf("expected nil error with nil store, got %v", err)
	}
	if ea != nil {
		t.Errorf("expected nil artifact with nil store, got %v", ea)
	}
}

func TestTruncateByLines(t *testing.T) {
	text := "line1\nline2\nline3\nline4"
	truncated := truncateByLines(text, 10)
	if len(truncated) >= len(text) {
		t.Errorf("expected truncated text shorter than original, got %d (original %d): %q", len(truncated), len(text), truncated)
	}
	if truncated == text {
		t.Error("expected text to be different after truncation")
	}
}

func TestTruncateByLinesWithinLimit(t *testing.T) {
	text := "hello"
	truncated := truncateByLines(text, 100)
	if truncated != text {
		t.Errorf("expected no truncation, got %q", truncated)
	}
}

func TestIsFallbackTier(t *testing.T) {
	if !isFallbackTier(TierGlob) {
		t.Error("TierGlob should be a fallback tier")
	}
	if !isFallbackTier(TierRipgrep) {
		t.Error("TierRipgrep should be a fallback tier")
	}
	if !isFallbackTier(TierGrep) {
		t.Error("TierGrep should be a fallback tier")
	}
	if !isFallbackTier(TierRead) {
		t.Error("TierRead should be a fallback tier")
	}
	if isFallbackTier(TierGraph) {
		t.Error("TierGraph should not be a fallback tier")
	}
	if isFallbackTier(TierLynx) {
		t.Error("TierLynx should not be a fallback tier")
	}
}

func TestTierOrder(t *testing.T) {
	tiers := []Tier{TierGraph, TierLynx, TierGlob, TierRipgrep, TierGrep, TierRead}
	for i := 1; i < len(tiers); i++ {
		if tiers[i].Order() <= tiers[i-1].Order() {
			t.Errorf("tier order violation: %s (order=%d) <= %s (order=%d)",
				tiers[i], tiers[i].Order(), tiers[i-1], tiers[i-1].Order())
		}
	}
}
