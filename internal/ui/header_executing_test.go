package ui

import (
	"strings"
	"testing"
)

func TestRenderExecutingHeader(t *testing.T) {
	h := RenderExecutingHeader("executing", 0, 80)
	if !strings.Contains(stripANSITest(h), "EXECUTING") {
		t.Errorf("header missing title:\n%q", h)
	}
	// Sweep advances per tick.
	a := stripANSITest(RenderExecutingHeader("build", 0, 80))
	b := stripANSITest(RenderExecutingHeader("build", 1, 80))
	if a == b {
		t.Errorf("sweep must advance per tick:\n%q vs %q", a, b)
	}
	// Narrow-safe, never panics.
	if got := RenderExecutingHeader("x", 0, 10); got != "" {
		t.Errorf("width<20 must return empty, got %q", got)
	}
	if got := RenderExecutingHeader("executing", 3, 40); !strings.Contains(stripANSITest(got), "EXECUTING") {
		t.Errorf("narrow header lost title:\n%q", got)
	}
}
