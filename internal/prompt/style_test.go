package prompt

import (
	"strings"
	"testing"
)

func TestParseStylePolicy(t *testing.T) {
	cases := []struct {
		in   string
		want StylePolicy
		ok   bool
	}{
		{"verbose", StyleVerbose, true},
		{"balanced", StyleBalanced, true},
		{"terse", StyleTerse, true},
		{"ultra", StyleUltra, true},
		{"TERSE", StyleTerse, true},
		{" Terse ", StyleTerse, true},
		{"", StyleBalanced, true},
		{"bogus", "", false},
		{"wordy", "", false},
	}
	for _, c := range cases {
		got, err := ParseStylePolicy(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseStylePolicy(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseStylePolicy(%q) = %q, want error", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseStylePolicy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsValidStylePolicy(t *testing.T) {
	for _, name := range []string{"verbose", "balanced", "terse", "ultra", "TERSE"} {
		if !IsValidStylePolicy(name) {
			t.Errorf("IsValidStylePolicy(%q) = false, want true", name)
		}
	}
	if IsValidStylePolicy("loud") {
		t.Errorf("IsValidStylePolicy(%q) = true, want false", "loud")
	}
}

func TestDefaultStylePolicy(t *testing.T) {
	if got := DefaultStylePolicy(); got != StyleBalanced {
		t.Errorf("DefaultStylePolicy() = %q, want %q", got, StyleBalanced)
	}
}

func TestStyleDirective(t *testing.T) {
	required := map[StylePolicy]string{
		StyleVerbose:  "Verbose",
		StyleBalanced: "Balanced",
		StyleTerse:    "Terse",
		StyleUltra:    "Ultra",
	}
	for p, header := range required {
		d := StyleDirective(p)
		if !strings.Contains(d, "OUTPUT STYLE: "+header) {
			t.Errorf("StyleDirective(%q) missing header %q", p, header)
		}
		if d == "" {
			t.Errorf("StyleDirective(%q) = empty, want directive text", p)
		}
	}
	if d := StyleDirective(StylePolicy("bogus")); d != "" {
		t.Errorf("StyleDirective(bogus) = %q, want empty", d)
	}
}

func TestStylePoliciesDiffer(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range ValidStylePolicies {
		d := StyleDirective(p)
		if seen[d] {
			t.Errorf("style directive for %q duplicates another policy", p)
		}
		seen[d] = true
	}
}

func TestApplyStyle(t *testing.T) {
	base := "base system prompt"
	got := ApplyStyle(base, StyleTerse)
	if !strings.HasPrefix(got, base) {
		t.Errorf("ApplyStyle dropped the base prompt: %q", got)
	}
	if !strings.Contains(got, "OUTPUT STYLE: Terse") {
		t.Errorf("ApplyStyle missing terse directive: %q", got)
	}
	if !strings.HasSuffix(got, strings.TrimSpace(StyleDirective(StyleTerse))) {
		t.Errorf("ApplyStyle directive not appended at end: %q", got)
	}
	if got := ApplyStyle(base, StylePolicy("bogus")); got != base {
		t.Errorf("ApplyStyle(bogus) = %q, want unchanged base", got)
	}
}

func TestActiveStyleDefaultsToBalanced(t *testing.T) {
	prev := SetActiveStyle(StyleBalanced)
	defer SetActiveStyle(prev)
	if got := ActiveStyle(); got != StyleBalanced {
		t.Errorf("ActiveStyle() = %q, want %q", got, StyleBalanced)
	}
}

func TestSetActiveStyle(t *testing.T) {
	prev := SetActiveStyle(StyleBalanced)
	defer SetActiveStyle(prev)

	got := SetActiveStyle(StyleTerse)
	if got != StyleBalanced {
		t.Errorf("SetActiveStyle(StyleTerse) returned %q, want previous %q", got, StyleBalanced)
	}
	if ActiveStyle() != StyleTerse {
		t.Errorf("ActiveStyle() = %q, want %q", ActiveStyle(), StyleTerse)
	}
}

// TestComposeInjectsActiveStyle is the end-to-end seam check: the composed
// system prompt must change when the active policy changes.
func TestComposeInjectsActiveStyle(t *testing.T) {
	prev := SetActiveStyle(StyleBalanced)
	defer SetActiveStyle(prev)

	balanced := Compose(AskContract(), RuntimeFacts{Username: "tester"})
	SetActiveStyle(StyleTerse)
	terse := Compose(AskContract(), RuntimeFacts{Username: "tester"})

	if !strings.Contains(balanced, "OUTPUT STYLE: Balanced") {
		t.Error("Compose did not inject the Balanced directive")
	}
	if !strings.Contains(terse, "OUTPUT STYLE: Terse") {
		t.Error("Compose did not inject the Terse directive")
	}
	if balanced == terse {
		t.Error("Compose output did not change when the active style changed")
	}
}
