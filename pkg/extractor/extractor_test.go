package extractor

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
)

func TestMarkdownColonHeader(t *testing.T) {
	raw := "```go:main.go\npackage main\n\nfunc main() {}\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)

	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(res.Artifacts))
	}
	a := res.Artifacts[0]
	if a.Path != "main.go" {
		t.Errorf("path = %q, want main.go", a.Path)
	}
	if a.Kind != ir.ArtifactFile {
		t.Errorf("kind = %s, want file", a.Kind)
	}
	want := "package main\n\nfunc main() {}"
	if string(a.Content) != want {
		t.Errorf("content = %q, want %q", a.Content, want)
	}
	if a.Hash != ir.ComputeHash(a.Content) {
		t.Errorf("hash %q does not match content", a.Hash)
	}
	if a.Metadata.Language != "go" {
		t.Errorf("language metadata = %q, want go", a.Metadata.Language)
	}
	for _, f := range []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8} {
		if !res.HasEvidence(f) {
			t.Errorf("missing evidence flag %s", f)
		}
	}
	if res.HasEvidence(EvValidJSONSchema) {
		t.Error("markdown extraction must not emit EvValidJSONSchema")
	}
}

func TestMarkdownSpaceHeader(t *testing.T) {
	raw := "```python script.py\nprint('hi')\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	a := res.Artifacts[0]
	if a.Path != "script.py" || string(a.Content) != "print('hi')" {
		t.Errorf("artifact = %+v, want script.py with content", a)
	}
}

func TestMarkdownFileMarker(t *testing.T) {
	raw := "=== FILE: README.md\n# Title\nSome text.\n\n=== FILE: NOTES.txt\nanother file\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(res.Artifacts))
	}
	if res.Artifacts[0].Path != "README.md" || string(res.Artifacts[0].Content) != "# Title\nSome text." {
		t.Errorf("first artifact = %+v", res.Artifacts[0])
	}
	if res.Artifacts[1].Path != "NOTES.txt" || string(res.Artifacts[1].Content) != "another file" {
		t.Errorf("second artifact = %+v", res.Artifacts[1])
	}
}

func TestMarkdownFileMarkerTrailingEquals(t *testing.T) {
	raw := "=== FILE: notes.txt ===\nhello\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	if res.Artifacts[0].Path != "notes.txt" || string(res.Artifacts[0].Content) != "hello" {
		t.Errorf("artifact = %+v", res.Artifacts[0])
	}
}

func TestMarkdownMultipleBlocks(t *testing.T) {
	raw := "```go:a.go\npackage a\n```\n\n```go:b.go\npackage b\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(res.Artifacts))
	}
	if res.Artifacts[0].Path != "a.go" || res.Artifacts[1].Path != "b.go" {
		t.Errorf("paths = %q, %q, want a.go, b.go", res.Artifacts[0].Path, res.Artifacts[1].Path)
	}
}

func TestMarkdownMessyProseTolerated(t *testing.T) {
	raw := "Sure! Here is the file you asked for:\n\n```go:main.go\npackage main\n```\n\nHope that helps! Let me know if you need anything else."
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != "main.go" {
		t.Errorf("artifacts = %+v, want main.go", res.Artifacts)
	}
}

func TestMarkdownUnclosedFenceRejects(t *testing.T) {
	cases := []struct {
		raw       string
		wantArtif int
	}{
		{"```go:main.go\npackage main\n", 0},
		{"Here is the code:\n```go:main.go\npackage main\n```\n\nthen an unclosed one:\n```go:x.go\nbody\n", 1},
	}
	for _, c := range cases {
		res := NewMarkdownFenceExtractor().Extract(context.Background(), c.raw)
		if res.Evaluate() != DecisionRejectAndRetry {
			t.Errorf("decision = %v, want reject for %q (evidence: %v)", res.Evaluate(), c.raw, res.Evidences)
		}
		if len(res.Artifacts) != c.wantArtif {
			t.Errorf("artifacts = %d, want %d for %q", len(res.Artifacts), c.wantArtif, c.raw)
		}
		if res.HasEvidence(EvFenceClosed) {
			t.Errorf("EvFenceClosed set for unclosed input %q", c.raw)
		}
	}
}

func TestMarkdownMissingPathRejects(t *testing.T) {
	cases := []string{
		"```go\npackage main\n```\n", // lang but no path
		"```\njust a fence\n```\n",   // bare fence, no header
		"no code blocks here at all",
		"",
	}
	for _, raw := range cases {
		res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
		if res.Evaluate() != DecisionRejectAndRetry {
			t.Errorf("decision = %v, want reject for %q (evidence: %v)", res.Evaluate(), raw, res.Evidences)
		}
		if len(res.Artifacts) != 0 {
			t.Errorf("artifacts = %d, want 0 for %q", len(res.Artifacts), raw)
		}
	}
}

func TestMarkdownInvalidUTF8Rejects(t *testing.T) {
	raw := string([]byte{0xff, 0xfe, 0x00}) + "```go:main.go\npackage main\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
	if res.HasEvidence(EvValidUTF8) {
		t.Error("EvValidUTF8 must not be set for invalid UTF-8")
	}
	if len(res.Artifacts) != 0 {
		t.Error("no artifacts may be produced from invalid UTF-8")
	}
}

func TestMarkdownEmptyFileAccepted(t *testing.T) {
	raw := "```go:empty.go\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	a := res.Artifacts[0]
	if string(a.Content) != "" {
		t.Errorf("content = %q, want empty", a.Content)
	}
	if a.Hash != ir.ComputeHash(nil) {
		t.Errorf("empty content hash mismatch")
	}
}

func TestJSONEnvelopeAccept(t *testing.T) {
	raw := `{"artifacts":[{"path":"src/a.go","content":"package a"},{"path":"README.md","content":"# doc"}]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(res.Artifacts))
	}
	if res.Artifacts[0].Path != "src/a.go" || string(res.Artifacts[0].Content) != "package a" {
		t.Errorf("first artifact = %+v", res.Artifacts[0])
	}
	if res.Artifacts[1].Path != "README.md" {
		t.Errorf("second path = %q, want README.md", res.Artifacts[1].Path)
	}
	for _, f := range []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8, EvValidJSONSchema} {
		if !res.HasEvidence(f) {
			t.Errorf("missing evidence flag %s", f)
		}
	}
}

func TestJSONTopLevelArrayAccept(t *testing.T) {
	raw := `[{"path":"a.txt","content":"hi"},{"path":"b.txt","content":"there"}]`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	if len(res.Artifacts) != 2 || res.Artifacts[1].Path != "b.txt" {
		t.Errorf("artifacts = %+v", res.Artifacts)
	}
}

func TestJSONFencedAccept(t *testing.T) {
	raw := "```json\n{\"artifacts\":[{\"path\":\"a.txt\",\"content\":\"x\"}]}\n```\n"
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != "a.txt" {
		t.Errorf("artifacts = %+v", res.Artifacts)
	}
}

func TestJSONSymlinkKindPreserved(t *testing.T) {
	raw := `{"artifacts":[{"path":"link","kind":"symlink","content":"target"}]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	if res.Artifacts[0].Kind != ir.ArtifactSymlink {
		t.Errorf("kind = %s, want symlink", res.Artifacts[0].Kind)
	}
}

func TestJSONMissingPathRejects(t *testing.T) {
	raw := `{"artifacts":[{"content":"no path here"}]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject (evidence: %v)", res.Evaluate(), res.Evidences)
	}
	if len(res.Artifacts) != 0 {
		t.Error("no artifacts allowed when a path is missing")
	}
	if res.HasEvidence(EvPathInHeader) || res.HasEvidence(EvValidJSONSchema) {
		t.Error("path/schema evidence must be absent")
	}
}

func TestJSONInvalidJSONRejects(t *testing.T) {
	raw := `{"artifacts": [{"path": "a.txt", "content": "unterminated}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
	if res.HasEvidence(EvFenceClosed) {
		t.Error("EvFenceClosed must not be set for malformed JSON")
	}
	if len(res.Artifacts) != 0 {
		t.Error("no artifacts from malformed JSON")
	}
}

func TestJSONInvalidKindRejects(t *testing.T) {
	raw := `{"artifacts":[{"path":"a.txt","kind":"bogus","content":"x"}]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
	if res.HasEvidence(EvValidJSONSchema) {
		t.Error("EvValidJSONSchema must be absent for invalid kind")
	}
}

func TestJSONEmptyArtifactsRejects(t *testing.T) {
	raw := `{"artifacts":[]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
}

func TestJSONUnclosedFenceRejects(t *testing.T) {
	raw := "```json\n{\"artifacts\":[{\"path\":\"a.txt\",\"content\":\"x\"}]}\n"
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
	if res.HasEvidence(EvFenceClosed) {
		t.Error("EvFenceClosed must not be set for an unclosed fence")
	}
}

func TestJSONInvalidUTF8Rejects(t *testing.T) {
	raw := string([]byte{0xff, 0xfe}) + `{"artifacts":[]}`
	res := NewJSONExtractor().Extract(context.Background(), raw)
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject", res.Evaluate())
	}
}

func TestEvaluatePartialEvidenceRejects(t *testing.T) {
	art := []ir.Artifact{ir.NewFile("a.txt", []byte("x"))}
	base := []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8}
	for i := range base {
		missing := make([]EvidenceFlag, 0, len(base)-1)
		for j, f := range base {
			if i != j {
				missing = append(missing, f)
			}
		}
		res := ExtractionResult{Artifacts: art, Evidences: missing, Raw: "raw"}
		if res.Evaluate() != DecisionRejectAndRetry {
			t.Errorf("missing %s: decision = %v, want reject", base[i], res.Evaluate())
		}
	}
}

func TestEvaluateEmptyArtifactsRejects(t *testing.T) {
	res := ExtractionResult{
		Artifacts: nil,
		Evidences: []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8},
		Raw:       "raw",
	}
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Errorf("decision = %v, want reject for empty artifact set", res.Evaluate())
	}
}

func TestEvaluateEmptyResultRejects(t *testing.T) {
	res := ExtractionResult{}
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Errorf("decision = %v, want reject for empty result", res.Evaluate())
	}
	if len(res.Artifacts) != 0 || len(res.Evidences) != 0 {
		t.Error("zero-value result must carry no artifacts or evidence")
	}
}

func TestEvidenceSetDeduplicates(t *testing.T) {
	res := ExtractionResult{
		Evidences: []EvidenceFlag{EvValidUTF8, EvValidUTF8, EvFenceClosed},
	}
	got := res.EvidenceSet()
	if len(got) != 2 {
		t.Errorf("EvidenceSet = %v, want 2 entries", got)
	}
}

func TestExtractionDecisionString(t *testing.T) {
	if DecisionAccept.String() != "accept" {
		t.Errorf("accept label = %q", DecisionAccept.String())
	}
	if DecisionRejectAndRetry.String() != "reject_and_retry" {
		t.Errorf("reject label = %q", DecisionRejectAndRetry.String())
	}
}

func TestMarkdownArtifactsMatchRawSource(t *testing.T) {
	raw := "```go:app.go\npackage app\nvar x = 1\n```\n"
	res := NewMarkdownFenceExtractor().Extract(context.Background(), raw)
	if res.Raw != raw {
		t.Error("result must preserve the raw input")
	}
	if res.Artifacts[0].Hash != ir.ComputeHash([]byte("package app\nvar x = 1")) {
		t.Error("artifact hash must be SHA-256 of exact content")
	}
	if strings.Count(string(res.Artifacts[0].Content), "var x = 1") != 1 {
		t.Error("content corrupted during extraction")
	}
}
