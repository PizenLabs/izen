package harness

import (
	"errors"
	"strings"
	"testing"
)

// TestTier1StrictDiff covers dirty model output that wraps a unified diff in
// surrounding markdown prose. Tier 1 must parse the diff exactly.
func TestTier1StrictDiff(t *testing.T) {
	raw := `Here's the fix:

--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
 
+import "fmt"
 func main() {}

Let me know if that works.`

	p := NewExtractorPipeline()
	cand, err := p.Extract([]byte(raw), nil, "main.go")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !cand.Evidence.ExactParse {
		t.Error("expected ExactParse = true")
	}
	if cand.Evidence.Tier != Tier1Strict {
		t.Errorf("tier = %v, want Tier1Strict", cand.Evidence.Tier)
	}
	if cand.Evidence.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", cand.Evidence.Confidence)
	}
	if cand.Evidence.Inferred {
		t.Error("strict parse must not be inferred")
	}
	if cand.TargetFile != "main.go" {
		t.Errorf("target = %q, want main.go", cand.TargetFile)
	}
	if !strings.Contains(cand.Diff, "import \"fmt\"") {
		t.Error("diff should contain the added import line")
	}
}

// TestTier1StrictXML covers structured XML artifact blocks.
func TestTier1StrictXML(t *testing.T) {
	raw := `<artifact file="config.yaml">
server:
  host: 0.0.0.0
</artifact>`

	p := NewExtractorPipeline()
	cand, err := p.Extract([]byte(raw), nil, "config.yaml")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !cand.Evidence.ExactParse {
		t.Error("expected ExactParse = true")
	}
	if cand.TargetFile != "config.yaml" {
		t.Errorf("target = %q, want config.yaml", cand.TargetFile)
	}
	if !strings.Contains(string(cand.RawPatch), "host: 0.0.0.0") {
		t.Error("raw patch should contain artifact body")
	}
}

// TestTier2UniqueAnchorMatch covers a snippet recovered via anchoring against
// original file content.
func TestTier2UniqueAnchorMatch(t *testing.T) {
	original := `line1
line2
func Foo() { return 1 }
line4
line5
`
	// Snippet differs from the original by indentation (fuzzy match path).
	snippet := `func Foo() { return 1 }`

	p := NewExtractorPipeline()
	cand, err := p.Extract([]byte(snippet), []byte(original), "f.go")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if cand.Evidence.Tier != Tier2Evidence {
		t.Errorf("tier = %v, want Tier2Evidence", cand.Evidence.Tier)
	}
	if !cand.Evidence.FuzzyMatch {
		t.Error("expected FuzzyMatch = true")
	}
	if cand.Evidence.Confidence < 0.6 {
		t.Errorf("confidence = %v, want >= 0.6", cand.Evidence.Confidence)
	}
	if cand.TargetFile != "f.go" {
		t.Errorf("target = %q, want f.go", cand.TargetFile)
	}
}

// TestTier2AmbiguousFuzzyMatch must fail closed when a snippet occurs at
// multiple comparable positions.
func TestTier2AmbiguousFuzzyMatch(t *testing.T) {
	original := `a
func X() {}
b
func X() {}
c
`
	// The snippet occurs twice with identical surrounding context, so the
	// match is ambiguous and MUST be rejected.
	snippet := `func X() {}`

	p := NewExtractorPipeline()
	_, err := p.Extract([]byte(snippet), []byte(original), "f.go")
	if err == nil {
		t.Fatal("expected ErrAmbiguousMatch, got nil")
	}
	if !errors.Is(err, ErrAmbiguousMatch) {
		t.Fatalf("err = %v, want ErrAmbiguousMatch", err)
	}
}

// TestTier3TruncatedOutput covers truncated full-text output reconstruction.
// The truncated response contains new content that does not anchor to the
// original, forcing the pipeline to descend to Tier 3 reconstruction.
func TestTier3TruncatedOutput(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"existing\")\n}\n"
	// Truncated full-text rewrite that diverges from the original and is cut
	// off mid-token, so it cannot anchor via Tier 2.
	truncated := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello w"

	p := NewExtractorPipeline()
	cand, err := p.Extract([]byte(truncated), []byte(original), "main.go")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if cand.Evidence.Tier != Tier3Inference {
		t.Errorf("tier = %v, want Tier3Inference", cand.Evidence.Tier)
	}
	if !cand.Evidence.Inferred {
		t.Error("expected Inferred = true for Tier 3")
	}
	if !cand.Evidence.Truncated {
		t.Error("expected Truncated = true for truncated output")
	}
	if cand.Evidence.Confidence > 0.6 {
		t.Errorf("confidence = %v, want <= 0.6", cand.Evidence.Confidence)
	}
}

// TestProfileConservativeFallback verifies the conservative strategy below 20
// observations.
func TestProfileConservativeFallback(t *testing.T) {
	p := NewModelBehaviorProfile()
	for i := 0; i < 10; i++ {
		p.RecordOutcome(true, true, false, false)
	}
	if got := p.ResolveStrategy(); got != StrategySimpleBlocks {
		t.Errorf("strategy = %v, want StrategySimpleBlocks (conservative)", got)
	}
}

// TestProfileStrictSelection verifies the strict strategy at high reliability.
func TestProfileStrictSelection(t *testing.T) {
	p := NewModelBehaviorProfile()
	for i := 0; i < 30; i++ {
		p.RecordOutcome(true, true, false, false)
	}
	if got := p.ResolveStrategy(); got != StrategyStrictUnifiedDiff {
		t.Errorf("strategy = %v, want StrategyStrictUnifiedDiff", got)
	}
	if p.StructuredOutputReliability() != 1.0 {
		t.Errorf("reliability = %v, want 1.0", p.StructuredOutputReliability())
	}
}
