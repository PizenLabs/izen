package execution

import (
	"strings"
	"testing"
)

// ── RMAH Pipeline Integration Tests ───────────────────────────────────────
//
// These tests exercise the package-level rmahPipeline — the fully wired
// instance that invokeMutation uses as a fallback when Tier 1 (bounded patch)
// fails. It uses the real ExtractBoundedPatch, ExtractCodeBlockContent, and
// V3 validation, so these tests prove the end-to-end behavior.

func TestRMAHIntegration_Tier1PassesForSearchReplace(t *testing.T) {
	// Valid SEARCH/REPLACE output should pass Tier 1 directly.
	raw := "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>"
	original := "before foo after"

	result := rmahPipeline.Process(raw, "file.txt", original)
	if !result.Passed {
		t.Fatalf("Tier 1 should pass for valid SEARCH/REPLACE, got rejected: %s", result.RejectReason)
	}
	if result.Candidate == "" {
		t.Fatal("candidate should be non-empty")
	}
}

func TestRMAHIntegration_Tier2ExtractsFromHTMLFence(t *testing.T) {
	// Malformed free-tier output: raw HTML in a fence, no SEARCH/REPLACE.
	raw := "Here is the updated file:\n\n```html\n<!DOCTYPE html>\n<html>\n<body><p>Hello World</p></body>\n</html>\n```"
	original := "<!DOCTYPE html>\n<html>\n<body><p>Old</p></body>\n</html>"

	result := rmahPipeline.Process(raw, "index.html", original)

	// Tier 1 fails (no SEARCH/REPLACE), Tier 2 extracts and passes AST.
	if !result.Passed {
		t.Fatalf("RMAH Tier 2 should handle fenced HTML, got rejected: %s", result.RejectReason)
	}
	if !strings.Contains(result.Candidate, "Hello World") {
		t.Fatalf("candidate should contain 'Hello World', got: %q", result.Candidate)
	}
}

func TestRMAHIntegration_Tier2RejectsCorruptHTML(t *testing.T) {
	// Baseline is clean HTML; candidate is corrupt (unterminated <script>).
	// The HTML validator flags unclosed raw-text elements like <script>.
	raw := "```html\n<html>\n<body>\n<script>var x = 1;\n</body>\n</html>\n```"
	original := "<html>\n<body>\n<p>clean</p>\n</body>\n</html>"

	result := rmahPipeline.Process(raw, "index.html", original)

	// Tier 2 MUST reject: baseline was clean, candidate degrades to corrupt.
	if result.Passed {
		t.Fatal("RMAH MUST reject corrupt HTML that degrades a clean baseline")
	}
	if !result.Rejected {
		t.Fatal("corrupt candidate should be explicitly rejected")
	}
}

func TestRMAHIntegration_Tier2ExtractsFromGoFence(t *testing.T) {
	// Free-tier model returns Go code in a fence.
	raw := "Here is the fix:\n\n```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
	original := "package main\n\nfunc main() {}\n"

	result := rmahPipeline.Process(raw, "main.go", original)

	if !result.Passed {
		t.Fatalf("RMAH Tier 2 should handle fenced Go code, got rejected: %s", result.RejectReason)
	}
	if !strings.Contains(result.Candidate, "fmt.Println") {
		t.Fatalf("candidate should contain fmt.Println, got: %q", result.Candidate)
	}
}

func TestRMAHIntegration_Tier2RejectsCorruptGo(t *testing.T) {
	// Baseline is clean Go; candidate is corrupt syntax.
	raw := "```go\npackage main\nfunc main( {\n```"
	original := "package main\n\nfunc main() {}\n"

	result := rmahPipeline.Process(raw, "main.go", original)

	if result.Passed {
		t.Fatal("RMAH MUST reject corrupt Go that degrades a clean baseline")
	}
	if !result.Rejected {
		t.Fatal("corrupt candidate should be explicitly rejected")
	}
}

func TestRMAHIntegration_ProseOnlyRejected(t *testing.T) {
	// Pure prose with no code blocks — both tiers should fail.
	raw := "I analyzed the code and there are no changes needed."
	original := "package main\n\nfunc main() {}\n"

	result := rmahPipeline.Process(raw, "main.go", original)

	if result.Passed {
		t.Fatal("prose-only output must not pass RMAH")
	}
	if !result.Rejected {
		t.Fatal("prose-only output should be explicitly rejected")
	}
}

func TestRMAHIntegration_NoBaseline_VerificationSkipped(t *testing.T) {
	// When there's no baseline (new file), Tier 2 skips AST verification
	// and passes any extractable content.
	raw := "```go\npackage main\n\nfunc main() {\n\tfmt.Println(\"new\")\n}\n```"

	result := rmahPipeline.Process(raw, "new.go", "")

	if !result.Passed {
		t.Fatalf("RMAH should pass when no baseline exists, got rejected: %s", result.RejectReason)
	}
}

func TestRMAHIntegration_UnregisteredLanguagePasses(t *testing.T) {
	// Unregistered language (e.g., .md) passes Tier 2 even without strict
	// schema — there's no AST baseline to degrade.
	raw := "```markdown\n# Updated Title\n\nNew content here.\n```"
	original := "# Old Title\n\nOld content.\n"

	result := rmahPipeline.Process(raw, "README.md", original)

	if !result.Passed {
		t.Fatalf("RMAH should pass for unregistered language, got rejected: %s", result.RejectReason)
	}
}

// TestRMAHIntegration_HardBlockAtAwaitingHuman verifies that when RMAH rejects
// a candidate, the execution path transitions to a hard-block (the caller
// must escalate to human review / awaiting_human). This pins the requirement
// that RMAH rejection is a deterministic escalation trigger, not a silent
// fallthrough.
func TestRMAHIntegration_HardBlockAtAwaitingHuman(t *testing.T) {
	// Corrupt output that degrades a clean baseline: unterminated <script>.
	raw := "```html\n<html>\n<body>\n<script>var x = 1;\n</body>\n</html>\n```"
	original := "<html>\n<body>\n<p>clean</p>\n</body>\n</html>"

	result := rmahPipeline.Process(raw, "index.html", original)

	// Assert: RMAH rejects → caller must escalate to awaiting_human.
	if result.Passed {
		t.Fatal("corrupt output MUST be rejected")
	}

	// The reject reason is the signal the state machine uses to transition
	// to awaiting_human. It must be non-empty so the escalation has a reason.
	if result.RejectReason == "" {
		t.Fatal("RMAH rejection must carry a reason for the escalation path")
	}

	// Verify the rejection is explicit (not a silent no-op).
	if !result.Rejected {
		t.Fatal("rejection must be explicit (Rejected=true)")
	}
}
