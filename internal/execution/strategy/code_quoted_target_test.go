package strategy

// ── REGRESSION: code-quoted prose targets are real targets ───────────────────
//
// A human names a file inside backticks or quotes as often as bare. The strategy
// gateway is the single target-resolution authority for the autonomy driver, so
// a delimiter that hid the name made an explicit CREATE compile as a targetless
// repository-level plan. These tests pin that all three spellings resolve to the
// same declared creation target.

import "testing"

func TestSelect_CodeQuotedProseTargetResolves(t *testing.T) {
	for _, input := range []string{
		"Create a new file named `zuru.md` with the content \"Hello everyone\".",
		"Create a new file named zuru.md with the content \"Hello everyone\".",
		"Create a new file named \"zuru.md\" with the content \"Hello everyone\".",
	} {
		p := Select(input, Deps{})
		if p.Strategy != TargetedMutation {
			t.Fatalf("Select(%q).Strategy = %s, want %s", input, p.Strategy, TargetedMutation)
		}
		if len(p.Targets) != 1 || p.Targets[0].Resolved != "zuru.md" {
			t.Fatalf("Select(%q).Targets = %+v, want exactly zuru.md", input, p.Targets)
		}
		if p.Artifact.Kind != "create_file" {
			t.Fatalf("Select(%q).Artifact.Kind = %q, want create_file", input, p.Artifact.Kind)
		}
	}
}
