package layer2

import (
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
		{"abcdefghi", 3},
		{"héllo wörld", 3},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestContextPolicyValid(t *testing.T) {
	if !DefaultPolicy().Valid() {
		t.Error("default policy must be valid")
	}

	p := DefaultPolicy()
	p.MaxTokenBudget = 0
	if p.Valid() {
		t.Error("zero token budget must be invalid")
	}

	p = DefaultPolicy()
	p.CompressionRatio = 1.5
	if p.Valid() {
		t.Error("ratio > 1 must be invalid")
	}

	p = DefaultPolicy()
	p.CompressionRatio = -0.1
	if p.Valid() {
		t.Error("negative ratio must be invalid")
	}
}
