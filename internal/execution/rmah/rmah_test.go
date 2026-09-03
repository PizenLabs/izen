package rmah

import (
	"fmt"
	"strings"
	"testing"
)

// ── Tier 1: Strict Schema Parser ──────────────────────────────────────────

func TestTier1_StrictSchema_AcceptsSearchReplace(t *testing.T) {
	p := NewPipeline().WithStrictExtractor(func(raw, orig string) (string, bool) {
		// Simulate successful SEARCH/REPLACE extraction.
		if strings.Contains(raw, "<<<<<<< SEARCH") {
			return "modified content", true
		}
		return "", false
	})

	result := p.Process("<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>", "file.go", "original")
	if !result.Passed {
		t.Fatalf("Tier 1 should pass for valid SEARCH/REPLACE, got rejected: %s", result.RejectReason)
	}
	if result.Candidate != "modified content" {
		t.Fatalf("candidate = %q, want 'modified content'", result.Candidate)
	}
}

func TestTier1_StrictSchema_RejectsProse(t *testing.T) {
	p := NewPipeline().WithStrictExtractor(func(raw, orig string) (string, bool) {
		// Tier 1 rejects: no SEARCH/REPLACE markers.
		return "", false
	}).WithCodeExtractor(func(raw string) (string, bool) {
		// Tier 2 also has nothing to extract from prose.
		return "", false
	})

	result := p.Process("Here is some analysis but no code.", "file.go", "original")
	if result.Passed {
		t.Fatal("Tier 1 + Tier 2 should both fail for prose-only output")
	}
	if !result.Rejected {
		t.Fatal("prose-only output should be explicitly rejected")
	}
}

// ── Tier 2: Conservative Code Extractor ──────────────────────────────────

func TestTier2_ExtractsFromFencedBlock(t *testing.T) {
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false // Tier 1 fails
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			// Simulate extracting from a fenced block.
			return "<html><body>hello</body></html>", true
		}).
		WithBaselineVerifier(func(content, target string) bool {
			// Baseline is clean HTML, candidate is also clean.
			return true
		})

	result := p.Process("```html\n<html><body>hello</body></html>\n```", "index.html", "<html><body>old</body></html>")
	if !result.Passed {
		t.Fatalf("Tier 2 should pass for extractable fenced content, got rejected: %s", result.RejectReason)
	}
	if result.Candidate != "<html><body>hello</body></html>" {
		t.Fatalf("candidate = %q", result.Candidate)
	}
}

func TestTier2_RejectsWhenBaselineDegrades(t *testing.T) {
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false // Tier 1 fails
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return "<html><body>unclosed", true // corrupt HTML
		}).
		WithBaselineVerifier(func(content, target string) bool {
			// Baseline is clean, candidate is corrupt.
			return !strings.Contains(content, "unclosed")
		})

	result := p.Process("```html\n<html><body>unclosed\n```", "index.html", "<html><body>clean</body></html>")
	if result.Passed {
		t.Fatal("Tier 2 MUST reject when baseline degrades to corrupt")
	}
	if !result.Rejected {
		t.Fatal("degraded candidate should be explicitly rejected")
	}
	if !strings.Contains(result.RejectReason, "degrades") {
		t.Fatalf("reject reason should mention degradation, got: %s", result.RejectReason)
	}
}

func TestTier2_RejectsOversizedCandidate(t *testing.T) {
	p := NewPipelineWithLimit(10). // 10 byte limit
					WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return "this content is way more than 10 bytes", true
		})

	result := p.Process("```\nthis content is way more than 10 bytes\n```", "file.go", "orig")
	if result.Passed {
		t.Fatal("oversized candidate must be rejected")
	}
	if !result.Rejected {
		t.Fatal("oversized candidate should be explicitly rejected")
	}
	if !strings.Contains(result.RejectReason, "exceeds max size") {
		t.Fatalf("reject reason should mention size, got: %s", result.RejectReason)
	}
}

// TestTier2_RejectsDestructiveTruncation verifies the Content Retention Floor
// guard: a partial snippet (15 lines) replacing a structurally sound 200-line
// clean HTML target passes AST syntax validation but silently truncates the
// majority of the file. Tier 2 MUST reject it.
func TestTier2_RejectsDestructiveTruncation(t *testing.T) {
	var baseline strings.Builder
	baseline.WriteString("<!DOCTYPE html>\n<html>\n<head><title>App</title></head>\n<body>\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&baseline, "<div class=\"row-%d\">content-%d</div>\n", i, i)
	}
	baseline.WriteString("</body>\n</html>\n")

	var snippet strings.Builder
	snippet.WriteString("<!DOCTYPE html>\n<html>\n<body>\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&snippet, "<p>snippet-%d</p>\n", i)
	}
	snippet.WriteString("</body>\n</html>\n")

	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false // Tier 1 fails (no SEARCH/REPLACE)
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return snippet.String(), true
		}).
		WithBaselineVerifier(func(content, target string) bool {
			// Both baseline and candidate are structurally sound HTML.
			return strings.Contains(content, "<html>") && strings.Contains(content, "</html>")
		})

	result := p.Process("```html\n"+snippet.String()+"\n```", "index.html", baseline.String())

	if result.Passed {
		t.Fatal("Tier 2 MUST reject a partial snippet replacing a full clean document")
	}
	if !result.Rejected {
		t.Fatal("destructive truncation should be explicitly rejected")
	}
	if !strings.Contains(result.RejectReason, "destructive truncation") ||
		!strings.Contains(result.RejectReason, "retains < 60% of target content") {
		t.Fatalf("reject reason should cite the retention floor guard, got: %s", result.RejectReason)
	}
}

func TestTier2_RejectsNoExtractableContent(t *testing.T) {
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return "", false // nothing to extract
		})

	result := p.Process("Just some analysis prose without any code.", "file.go", "orig")
	if result.Passed {
		t.Fatal("prose-only output must not pass Tier 2")
	}
	if !result.Rejected {
		t.Fatal("prose-only output should be explicitly rejected")
	}
}

// ── Full Pipeline Integration ─────────────────────────────────────────────

func TestPipeline_Tier1PassesSkipsTier2(t *testing.T) {
	tier2Called := false
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "tier1-candidate", true
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			tier2Called = true
			return "tier2-candidate", true
		})

	result := p.Process("<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>", "file.go", "orig")
	if !result.Passed {
		t.Fatal("Tier 1 should pass")
	}
	if result.Candidate != "tier1-candidate" {
		t.Fatalf("candidate = %q, want tier1-candidate", result.Candidate)
	}
	if tier2Called {
		t.Fatal("Tier 2 must NOT be called when Tier 1 passes")
	}
}

func TestPipeline_FullFallback_Tier1Fails_Tier2Passes(t *testing.T) {
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false // Tier 1 fails
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return "package main\nfunc main() {}\n", true
		}).
		WithBaselineVerifier(func(content, target string) bool {
			return true // both baseline and candidate are clean
		})

	// Malformed LLM output: raw code fence without SEARCH/REPLACE blocks.
	raw := "```go\npackage main\nfunc main() {}\n```"
	result := p.Process(raw, "main.go", "package main\nfunc main() {}\n")

	if !result.Passed {
		t.Fatalf("Tier 2 should pass for extractable Go code, got rejected: %s", result.RejectReason)
	}
	if result.Candidate != "package main\nfunc main() {}\n" {
		t.Fatalf("candidate = %q", result.Candidate)
	}
}

func TestPipeline_FullFallback_BothTiersFail(t *testing.T) {
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) {
			return "", false
		}).
		WithCodeExtractor(func(raw string) (string, bool) {
			return "", false
		})

	result := p.Process("This is just analysis with no code blocks.", "file.go", "orig")
	if result.Passed {
		t.Fatal("both tiers failing must not produce a candidate")
	}
	if !result.Rejected {
		t.Fatal("both tiers failing should be explicitly rejected")
	}
}

func TestPipeline_Tier3SynthesizesRawOutputWithContext(t *testing.T) {
	baseline := "package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n"
	raw := "package main\n\nfunc main() {\n\tprintln(\"new\")\n}\n"
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) { return "", false }).
		WithCodeExtractor(func(raw string) (string, bool) { return "", false }).
		WithBaselineVerifier(func(content, target string) bool {
			return strings.Contains(content, "package main") && strings.Contains(content, "func main")
		})

	result := p.ProcessArtifact(raw, "main.go", baseline)
	if !result.Passed {
		t.Fatalf("Tier 3 should synthesize raw output: %s", result.RejectReason)
	}
	if !strings.Contains(result.Candidate, "<<<<<<< SEARCH") || !strings.Contains(result.Candidate, "new") {
		t.Fatalf("candidate is not a SEARCH/REPLACE patch: %q", result.Candidate)
	}
	patched, ok := applySynthesizedPatch(baseline, result.Candidate)
	if !ok || patched != raw {
		t.Fatalf("synthesized patch result = %q, want %q", patched, raw)
	}
}

func TestPipeline_Tier3RejectsDestructiveTruncation(t *testing.T) {
	baseline := "package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n"
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) { return "", false }).
		WithCodeExtractor(func(raw string) (string, bool) { return "", false }).
		WithBaselineVerifier(func(content, target string) bool { return true })

	result := p.Process("package main\n", "main.go", baseline)
	if result.Passed || !strings.Contains(result.RejectReason, "RMAH Tier 3: synthesized patch rejected due to destructive truncation (<60% retention)") {
		t.Fatalf("expected Tier 3 truncation rejection, got %+v", result)
	}
}

func TestPipeline_Tier3RejectsASTCorruption(t *testing.T) {
	baseline := "<html>\n<body>\n<p>old</p>\n</body>\n</html>\n"
	raw := "<html>\n<body>\n<p>new\n</body>\n</html>\n"
	p := NewPipeline().
		WithStrictExtractor(func(raw, orig string) (string, bool) { return "", false }).
		WithCodeExtractor(func(raw string) (string, bool) { return "", false }).
		WithBaselineVerifier(func(content, target string) bool {
			return strings.Contains(content, "</p>") && strings.Contains(content, "</html>")
		})

	result := p.Process(raw, "index.html", baseline)
	if result.Passed || !strings.Contains(result.RejectReason, "candidate degrades baseline AST") {
		t.Fatalf("expected Tier 3 AST rejection, got %+v", result)
	}
}

// ── Nil function safety ───────────────────────────────────────────────────

func TestPipeline_NilFunctionsDoNotPanic(t *testing.T) {
	p := NewPipeline() // all functions nil
	result := p.Process("anything", "file.go", "orig")
	if result.Passed {
		t.Fatal("nil functions must not produce a candidate")
	}
	if !result.Rejected {
		t.Fatal("nil functions should result in rejection")
	}
}

// ── Malformed LLM output fixtures ─────────────────────────────────────────

// TestPipeline_MalformedFreeTierOutput simulates the exact failure mode that
// RMAH is designed to handle: a free-tier model (e.g., dots-3-note-preview:free)
// returning raw code fences instead of SEARCH/REPLACE blocks.
func TestPipeline_MalformedFreeTierOutput(t *testing.T) {
	// The baseline file is clean, parseable HTML.
	baseline := "<!DOCTYPE html>\n<html>\n<head><title>App</title></head>\n<body><div id=\"app\">Hello</div></body>\n</html>"

	// The model returns the full replacement inside a ```html fence — no
	// SEARCH/REPLACE markers, no unified diff. This is the classic free-tier
	// failure mode.
	malformedOutput := "Here is the updated file:\n\n```html\n<!DOCTYPE html>\n<html>\n<head><title>App</title></head>\n<body><div id=\"app\">Hello World</div></body>\n</html>\n```\n\nLet me know if you need anything else!"

	p := NewConfiguredPipeline(
		defaultMaxCandidateBytes,
		func(raw, orig string) (string, bool) {
			// Tier 1: no SEARCH/REPLACE → fail
			return "", false
		},
		func(raw string) (string, bool) {
			// Tier 2: extract from fenced block
			if strings.Contains(raw, "```html") {
				start := strings.Index(raw, "```html") + len("```html")
				end := strings.Index(raw[start:], "```")
				if end < 0 {
					return raw[start:], true
				}
				return raw[start : start+end], true
			}
			return "", false
		},
		func(content, target string) bool {
			// Baseline verifier: both should be clean HTML
			return strings.Contains(content, "<html>") && strings.Contains(content, "</html>")
		},
	)

	result := p.Process(malformedOutput, "index.html", baseline)

	// Assert: Tier 1 fails (no SEARCH/REPLACE), Tier 2 extracts and passes.
	if !result.Passed {
		t.Fatalf("RMAH Tier 2 should handle malformed free-tier output, got rejected: %s", result.RejectReason)
	}
	if !strings.Contains(result.Candidate, "Hello World") {
		t.Fatalf("candidate should contain the updated content, got: %q", result.Candidate)
	}
}

// TestPipeline_MalformedFreeTierOutput_DegradationRejected verifies the
// fail-closed behavior: when the baseline is clean but the free-tier model
// returns corrupt output, RMAH rejects it (never passes corrupt content to
// safety barriers).
func TestPipeline_MalformedFreeTierOutput_DegradationRejected(t *testing.T) {
	baseline := "<!DOCTYPE html>\n<html>\n<head><title>App</title></head>\n<body><div id=\"app\">Hello</div></body>\n</html>"

	// Model returns corrupt HTML (unclosed <div>) inside a fence.
	corruptOutput := "Here is the fix:\n```html\n<!DOCTYPE html>\n<html>\n<body><div id=\"app\">Hello\n```" //nolint:gosec

	p := NewConfiguredPipeline(
		defaultMaxCandidateBytes,
		func(raw, orig string) (string, bool) {
			return "", false // Tier 1 fails
		},
		func(raw string) (string, bool) {
			if strings.Contains(raw, "```html") {
				start := strings.Index(raw, "```html") + len("```html")
				end := strings.Index(raw[start:], "```")
				if end < 0 {
					return raw[start:], true
				}
				return raw[start : start+end], true
			}
			return "", false
		},
		func(content, target string) bool {
			// Clean HTML has balanced tags; corrupt HTML does not.
			return strings.Contains(content, "</div>") || strings.Contains(content, "</html>")
		},
	)

	result := p.Process(corruptOutput, "index.html", baseline)

	// Assert: RMAH rejects the corrupt candidate.
	if result.Passed {
		t.Fatal("RMAH MUST reject corrupt output that degrades a clean baseline")
	}
	if !result.Rejected {
		t.Fatal("corrupt output should be explicitly rejected")
	}
}
