package extractor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestContractParserFormalArtifactFence(t *testing.T) {
	raw := "Here is the file you need:\n\n:::artifact index.html\n<!DOCTYPE html>\n<html></html>\n:::\n\nLet me know if you need changes."
	blocks, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Path != "index.html" {
		t.Errorf("path = %q, want index.html", b.Path)
	}
	got := strings.Join(b.Lines, "\n")
	if got != "<!DOCTYPE html>\n<html></html>" {
		t.Errorf("content = %q, want fenced body without prose", got)
	}
}

func TestContractParserCodeFence(t *testing.T) {
	raw := "```go:main.go\npackage main\n\nfunc main() {}\n```\n"
	blocks, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].Path != "main.go" {
		t.Errorf("path = %q, want main.go", blocks[0].Path)
	}
	if blocks[0].Language != "go" {
		t.Errorf("language = %q, want go", blocks[0].Language)
	}
}

func TestContractParserLanguageAnnotation(t *testing.T) {
	raw := ":::artifact go:main.go\npackage main\n:::\n"
	blocks, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if blocks[0].Path != "main.go" || blocks[0].Language != "go" {
		t.Errorf("block = %+v, want path main.go / lang go", blocks[0])
	}
}

func TestContractParserMixedFences(t *testing.T) {
	raw := ":::artifact README.md\n# Title\n:::\n\n" +
		"```json:config.json\n{\"a\":1}\n```\n\n" +
		"prose that must be dropped entirely\n"
	blocks, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Path != "README.md" {
		t.Errorf("first path = %q, want README.md", blocks[0].Path)
	}
	if blocks[1].Path != "config.json" {
		t.Errorf("second path = %q, want config.json", blocks[1].Path)
	}
}

func TestContractParserDropsUncontractedText(t *testing.T) {
	raw := "Sure! Here is the output:\n\n:::artifact a.txt\nline one\nline two\n:::\n\nHope that helps!"
	blocks, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	joined := strings.Join(blocks[0].Lines, "\n")
	if strings.Contains(joined, "Sure!") || strings.Contains(joined, "Hope") {
		t.Errorf("un-contracted prose leaked into content: %q", joined)
	}
	if joined != "line one\nline two" {
		t.Errorf("content = %q, want exactly the fenced body", joined)
	}
}

func TestContractParserNoContractViolation(t *testing.T) {
	for _, raw := range []string{
		"",
		"just some narrative prose with no fences at all",
		"```go\npackage main\n```\n", // fence without a path is not a contract
		":::artifact\nno path here\n:::\n",
		"<html><body>plain html, not a contract</body></html>",
	} {
		_, err := NewArtifactContractParser().Parse(context.Background(), raw)
		if !errors.Is(err, ErrContractViolation) {
			t.Errorf("Parse(%q) error = %v, want ErrContractViolation", raw, err)
		}
	}
}

func TestContractParserUnclosedFenceIsViolation(t *testing.T) {
	raw := ":::artifact main.go\npackage main\n"
	_, err := NewArtifactContractParser().Parse(context.Background(), raw)
	if !errors.Is(err, ErrContractViolation) {
		t.Fatalf("unclosed fence error = %v, want ErrContractViolation", err)
	}
}

func TestContractParserExtractEvidence(t *testing.T) {
	res := NewArtifactContractParser().Extract(context.Background(),
		":::artifact a.txt\nhello\n:::\n")
	if res.Evaluate() != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Evaluate())
	}
	for _, f := range []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8} {
		if !res.HasEvidence(f) {
			t.Errorf("missing evidence %s", f)
		}
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != "a.txt" {
		t.Errorf("artifacts = %+v, want a.txt", res.Artifacts)
	}
	if res.Artifacts[0].Metadata.Source != "contract-parser" {
		t.Errorf("source metadata = %q, want contract-parser", res.Artifacts[0].Metadata.Source)
	}
}

func TestContractParserExtractRejectsProse(t *testing.T) {
	res := NewArtifactContractParser().Extract(context.Background(),
		"Here is a summary of what I did with no fenced artifacts.")
	if res.Evaluate() != DecisionRejectAndRetry {
		t.Fatalf("decision = %v, want reject for prose-only output", res.Evaluate())
	}
	if len(res.Artifacts) != 0 {
		t.Error("prose-only output must yield no artifacts")
	}
}
