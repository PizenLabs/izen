package prompt

import (
	"strings"
	"testing"
)

// TestSetWorkspaceCapabilitiesRoundTrip verifies the global injection is
// settable and cleared.
func TestSetWorkspaceCapabilitiesRoundTrip(t *testing.T) {
	prev := SetWorkspaceCapabilities("### WORKSPACE CAPABILITIES\nSTACK: go")
	defer SetWorkspaceCapabilities(prev)

	if got := ActiveWorkspaceCapabilities(); got != "### WORKSPACE CAPABILITIES\nSTACK: go" {
		t.Errorf("ActiveWorkspaceCapabilities() = %q", got)
	}

	cleared := SetWorkspaceCapabilities("")
	if cleared != "### WORKSPACE CAPABILITIES\nSTACK: go" {
		t.Errorf("SetWorkspaceCapabilities(\"\") returned %q", cleared)
	}
	if ActiveWorkspaceCapabilities() != "" {
		t.Errorf("active capabilities not cleared")
	}
}

// TestComposeInjectsWorkspaceCapabilities is the anti-hallucination seam: a
// composed system prompt must carry the Layer 1 capability header when set.
func TestComposeInjectsWorkspaceCapabilities(t *testing.T) {
	prev := SetWorkspaceCapabilities("### WORKSPACE CAPABILITIES\nSTACK: static\nCAPABILITIES: none detected\nDo not invent commands beyond those listed.")
	defer SetWorkspaceCapabilities(prev)

	p := Compose(AskContract(), RuntimeFacts{Username: "tester"})
	if !strings.Contains(p, "### WORKSPACE CAPABILITIES") {
		t.Errorf("Compose did not inject the capabilities header:\n%s", p)
	}
	if !strings.Contains(p, "STACK: static") {
		t.Errorf("Compose capabilities header missing STACK:\n%s", p)
	}

	// Forbid hallucinated toolchains in the composed output.
	if strings.Contains(p, "go build") || strings.Contains(p, "go test") {
		t.Errorf("static workspace prompt claims a Go toolchain:\n%s", p)
	}
}

// TestApplyWorkspaceCapabilitiesIdempotent ensures re-applying the header does
// not duplicate it.
func TestApplyWorkspaceCapabilitiesIdempotent(t *testing.T) {
	prev := SetWorkspaceCapabilities("### WORKSPACE CAPABILITIES\nSTACK: go")
	defer SetWorkspaceCapabilities(prev)

	base := "You are IZEN."
	once := ApplyWorkspaceCapabilities(base)
	twice := ApplyWorkspaceCapabilities(once)
	if strings.Count(twice, "WORKSPACE CAPABILITIES") != 1 {
		t.Errorf("ApplyWorkspaceCapabilities duplicated the header:\n%s", twice)
	}
}

// TestForModeWithUserCarriesCapabilities verifies the mode system prompts
// (used by /ask, /build, /plan, /investigate) receive the header.
func TestForModeWithUserCarriesCapabilities(t *testing.T) {
	prev := SetWorkspaceCapabilities("### WORKSPACE CAPABILITIES\nSTACK: node\nBUILD: npm run build")
	defer SetWorkspaceCapabilities(prev)

	for _, mode := range []string{"ask", "build", "plan", "investigate", "review"} {
		p := ForModeWithUser(mode, "tester")
		if !strings.Contains(p, "BUILD: npm run build") {
			t.Errorf("ForModeWithUser(%q) missing capabilities header:\n%s", mode, p)
		}
	}
}
