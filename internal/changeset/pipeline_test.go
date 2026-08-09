package changeset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/patch"
)

// indexHTML is the deterministic fixture for the DoD pipeline tests. The
// anchor line "<h3>Old Delta</h3>" is the unique high-similarity match for the
// snippet "<h3>Project Delta</h3>".
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Delta</title>
</head>
<body>
  <h3>Old Delta</h3>
  <p>Stable content</p>
</body>
</html>
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// applyPatch runs the compiled diff through the application's authoritative
// patch engine and asserts a clean apply to index.html.
func applyPatch(t *testing.T, root string, diff []byte) string {
	t.Helper()
	eng := patch.NewEngine()
	result, err := eng.Apply(root, patch.Request{
		File:          "index.html",
		Original:      indexHTML,
		Raw:           string(diff),
		TaskObjective: "update the header delta",
		FileType:      ".html",
	})
	if err != nil {
		t.Fatalf("apply patch: %v\n%s", err, diff)
	}
	if !result.Applied {
		t.Fatalf("patch not applied: %+v", result)
	}
	return result.Content
}

// TestStandardDiffPipeline (DoD Test 1) asserts a raw unified-diff model output
// converts into a valid, applyable patch through the full pipeline.
func TestStandardDiffPipeline(t *testing.T) {
	root := writeFixture(t, indexHTML)
	output := "--- a/index.html\n+++ b/index.html\n@@ -7,1 +7,1 @@\n-  <h3>Old Delta</h3>\n+  <h3>Project Delta</h3>\n"

	p := NewPipeline()
	compiled, err := p.Run(output, "index.html", []byte(indexHTML))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled changes = %d, want 1", len(compiled))
	}
	cc := compiled[0]
	if cc.ChangeSet.Kind != KindApplyDiff {
		t.Errorf("kind = %s, want APPLY_DIFF", cc.ChangeSet.Kind)
	}
	if !cc.Validation.Valid {
		t.Errorf("validation invalid: %v", cc.Validation.Reasons)
	}
	if got := applyPatch(t, root, cc.Diff); !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("applied content missing new anchor:\n%s", got)
	}
	if got := readFile(t, root, "index.html"); !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("on-disk content missing new anchor:\n%s", got)
	}
}

// TestMarkdownSnippetPipeline (DoD Test 2) asserts a markdown snippet with no
// ---/+++ markers is extracted as a REPLACE_BLOCK ChangeSet, compiled into a
// valid unified diff, and applies cleanly to index.html.
func TestMarkdownSnippetPipeline(t *testing.T) {
	root := writeFixture(t, indexHTML)
	output := "Here is the proposed change:\n```html\n<h3>Project Delta</h3>\n```\n"

	p := NewPipeline()
	compiled, err := p.Run(output, "index.html", []byte(indexHTML))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled changes = %d, want 1", len(compiled))
	}
	cc := compiled[0]
	if cc.ChangeSet.Kind != KindReplaceBlock {
		t.Errorf("kind = %s, want REPLACE_BLOCK", cc.ChangeSet.Kind)
	}
	if cc.ChangeSet.TargetFile != "index.html" {
		t.Errorf("target = %q, want index.html", cc.ChangeSet.TargetFile)
	}
	if cc.ChangeSet.OldContent != "<h3>Old Delta</h3>" {
		t.Errorf("anchor = %q, want %q", cc.ChangeSet.OldContent, "<h3>Old Delta</h3>")
	}
	if cc.ChangeSet.NewContent != "<h3>Project Delta</h3>" {
		t.Errorf("new content = %q, want %q", cc.ChangeSet.NewContent, "<h3>Project Delta</h3>")
	}
	if !cc.Validation.Valid {
		t.Errorf("validation invalid: %v", cc.Validation.Reasons)
	}
	got := applyPatch(t, root, cc.Diff)
	if !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("applied content missing new anchor:\n%s", got)
	}
	if strings.Contains(got, "<h3>Old Delta</h3>") {
		t.Errorf("applied content still contains old anchor:\n%s", got)
	}
}

// TestAmbiguousSnippetGuard (DoD Test 3) asserts an unmatchable snippet pauses
// the pipeline with the ambiguity sentinel and never mutates files.
func TestAmbiguousSnippetGuard(t *testing.T) {
	root := writeFixture(t, indexHTML)
	output := "```html\n<div class=\"unmatched-widget\">does not exist here</div>\n```\n"

	p := NewPipeline()
	_, err := p.Run(output, "index.html", []byte(indexHTML))
	if err == nil {
		t.Fatal("pipeline succeeded, want ambiguous-change pause")
	}
	if !errors.Is(err, ErrAmbiguousChange) {
		t.Fatalf("error = %v, want ErrAmbiguousChange", err)
	}
	if !strings.Contains(err.Error(), "Pipeline PAUSED") {
		t.Errorf("error message missing pipeline pause contract: %v", err)
	}
	// The guard must be non-destructive: the on-disk file is untouched.
	if got := readFile(t, root, "index.html"); got != indexHTML {
		t.Errorf("file was mutated on ambiguous change:\n%s", got)
	}
}

// TestReplaceFileTaggedBlock covers the explicit full-file indicator path: a
// fence header carrying a target path classifies as REPLACE_FILE.
func TestReplaceFileTaggedBlock(t *testing.T) {
	root := writeFixture(t, indexHTML)
	output := "```html:index.html\n<!DOCTYPE html>\n<html><body>New site</body></html>\n```\n"

	p := NewPipeline()
	compiled, err := p.Run(output, "index.html", []byte(indexHTML))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled changes = %d, want 1", len(compiled))
	}
	cc := compiled[0]
	if cc.ChangeSet.Kind != KindReplaceFile {
		t.Errorf("kind = %s, want REPLACE_FILE", cc.ChangeSet.Kind)
	}
	if !cc.Validation.Valid {
		t.Errorf("validation invalid: %v", cc.Validation.Reasons)
	}
	if got := applyPatch(t, root, cc.Diff); !strings.Contains(got, "New site") {
		t.Errorf("applied content missing full-file rewrite:\n%s", got)
	}
}

// TestNormalizeClassifiesFormat pins the format classifier used by the
// extractor across the three supported formats.
func TestNormalizeClassifiesFormat(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		want    Format
		wantLen int
	}{
		{"diff", "preamble\n--- a/index.html\n+++ b/index.html\n@@ -1,1 +1,1 @@", FormatDiff, 1},
		{"block", "```html\n<p>x</p>\n```\n", FormatCodeBlock, 1},
		{"text", "some plain text", FormatText, 1},
		{"empty", "   ", FormatUnknown, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			no := Normalize(tc.output)
			if no.Format != tc.want {
				t.Errorf("format = %v, want %v", no.Format, tc.want)
			}
			if tc.want == FormatCodeBlock && len(no.Blocks) != tc.wantLen {
				t.Errorf("blocks = %d, want %d", len(no.Blocks), tc.wantLen)
			}
		})
	}
}
