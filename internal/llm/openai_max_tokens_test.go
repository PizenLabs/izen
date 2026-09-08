package llm

import "testing"

// TestClampOpenAIMaxTokens pins the raised default output limit: an unset
// budget defaults to 4096 (long code answers clear the completion ceiling),
// explicit budgets pass through verbatim up to the 8192 hard cap.
func TestClampOpenAIMaxTokens(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"unset defaults to 4096", 0, 4096},
		{"negative defaults to 4096", -5, 4096},
		{"tight mutation budget preserved", 800, 800},
		{"legacy default preserved", 1200, 1200},
		{"casual budget preserved", 2048, 2048},
		{"explicit 4096 preserved", 4096, 4096},
		{"cap boundary preserved", 8192, 8192},
		{"oversized clamped to 8192", 32000, 8192},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampOpenAIMaxTokens(tc.input); got != tc.want {
				t.Errorf("clampOpenAIMaxTokens(%d) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
