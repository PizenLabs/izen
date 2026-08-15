package ui

import (
	"strings"
	"testing"
)

// TestRenderTokenUsage_UsageTruth pins the footer USAGE TRUTH contract:
//   - usage unknown (never reported) renders "usage unknown", never "0 tok"
//   - a known zero renders "0 tok" (a genuine provider-reported zero)
//   - known provider usage renders the exact provider count
func TestRenderTokenUsage_UsageTruth(t *testing.T) {
	cases := []struct {
		name    string
		known   bool
		input   int
		output  int
		total   int
		limit   int
		substrs []string
	}{
		{"unknown renders unknown", false, 0, 0, 0, 0, []string{"usage unknown"}},
		{"unknown with stale counters renders count", false, 10, 20, 30, 0, []string{"↓10 + ↑20 tok"}},
		{"known zero renders 0 tok", true, 0, 0, 0, 0, []string{"0 tok"}},
		{"known provider usage", true, 2860, 2048, 4908, 0, []string{"↓2.9k + ↑2.0k tok"}},
		{"known zero with context window", true, 0, 0, 0, 128000, []string{"0 tok (0%)"}},
	}
	for _, c := range cases {
		got := renderTokenUsage(c.known, c.input, c.output, c.total, c.limit)
		for _, want := range c.substrs {
			if !strings.Contains(got, want) {
				t.Errorf("%s: renderTokenUsage = %q, want it to contain %q", c.name, got, want)
			}
		}
	}
}

// TestRenderTokenUsage_NeverFabricatesZero is the direct regression guard for
// the reported bug: a provider that consumed 2048 completion tokens must render
// 2048 tok — never "0 tok" from an unset usage state.
func TestRenderTokenUsage_NeverFabricatesZero(t *testing.T) {
	got := renderTokenUsage(true, 2860, 2048, 4908, 128000)
	if strings.Contains(got, "0 tok") {
		t.Fatalf("provider-reported usage rendered as 0 tok: %q", got)
	}
	if !strings.Contains(got, "2.0k") {
		t.Fatalf("provider completion tokens not rendered: %q", got)
	}
}
