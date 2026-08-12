package changeset

import (
	"errors"
	"strings"
	"testing"
)

// multiH3IndexHTML is the DoD anchor-extractor fixture: index.html carries
// MULTIPLE <h3> tags, so a bare snippet like "<h3>Project Delta</h3>" must be
// anchored to the unique "Delta" section via fuzzy element matching — never
// rejected as ambiguous just because the file has several h3 elements.
const multiH3IndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Delta</title>
</head>
<body>
  <h3>Old Delta</h3>
  <p>Stable content</p>
  <h3>Header Links</h3>
  <p>Some links</p>
  <h3>Footer Note</h3>
  <p>Footer content</p>
</body>
</html>
`

// TestAnchorExtractorSingleLineAgainstMultipleH3 is DoD Test 2: a single-line
// snippet "<h3>Project Delta</h3>" against an index.html containing multiple
// <h3> tags must be extracted as a REPLACE_BLOCK anchored to the "Old Delta"
// section — without throwing ErrAmbiguousChange.
func TestAnchorExtractorSingleLineAgainstMultipleH3(t *testing.T) {
	root := writeFixture(t, multiH3IndexHTML)
	output := "Here is the corrected block:\n```html\n<h3>Project Delta</h3>\n```\n"

	p := NewPipeline()
	compiled, err := p.Run(output, "index.html", []byte(multiH3IndexHTML))
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
	if cc.ChangeSet.OldContent != "<h3>Old Delta</h3>" {
		t.Errorf("anchor = %q, want %q", cc.ChangeSet.OldContent, "<h3>Old Delta</h3>")
	}
	if !cc.Validation.Valid {
		t.Errorf("validation invalid: %v", cc.Validation.Reasons)
	}
	got := applyPatch(t, root, cc.Diff)
	if !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("applied content missing the new h3:\n%s", got)
	}
	if strings.Contains(got, "<h3>Old Delta</h3>") {
		t.Errorf("applied content still contains the old h3:\n%s", got)
	}
}

// TestAnchorExtractorMultiLineBlockAgainstSiblings is the block-window upgrade
// regression guard: a multi-line HTML snippet ("<h3>Project Delta</h3>" +
// unchanged sibling "<p>Stable content</p>") previously raised
// ErrAmbiguousChange because whole-block similarity to any single line can never
// cross the threshold. The sliding-window matcher must anchor it to the Old
// Delta section and compile an applyable patch.
func TestAnchorExtractorMultiLineBlockAgainstSiblings(t *testing.T) {
	root := writeFixture(t, multiH3IndexHTML)
	output := "```html\n  <h3>Project Delta</h3>\n  <p>Stable content</p>\n```\n"

	p := NewPipeline()
	compiled, err := p.Run(output, "index.html", []byte(multiH3IndexHTML))
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
	if cc.ChangeSet.OldContent != "  <h3>Old Delta</h3>\n  <p>Stable content</p>" {
		t.Errorf("anchor = %q, want the Old Delta block", cc.ChangeSet.OldContent)
	}
	if cc.ChangeSet.NewContent != "  <h3>Project Delta</h3>\n  <p>Stable content</p>" {
		t.Errorf("new content = %q, want the corrected Delta block", cc.ChangeSet.NewContent)
	}
	if !cc.Validation.Valid {
		t.Errorf("validation invalid: %v", cc.Validation.Reasons)
	}
	got := applyPatch(t, root, cc.Diff)
	if !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("applied content missing the corrected section:\n%s", got)
	}
	if strings.Contains(got, "<h3>Old Delta</h3>") {
		t.Errorf("applied content still contains the old section:\n%s", got)
	}
	// The untouched sibling sections must survive the patch.
	if !strings.Contains(got, "<h3>Header Links</h3>") || !strings.Contains(got, "<h3>Footer Note</h3>") {
		t.Errorf("untouched h3 sections were clobbered:\n%s", got)
	}
}

// TestAnchorExtractorUnmatchedBlockStillPauses guards the strict ambiguity
// contract: an HTML snippet that genuinely matches NO section (neither by
// single-line fuzzy match nor by a contiguous window) must still pause the
// pipeline with ErrAmbiguousChange — the enhanced matcher must never lower the
// safety bar.
func TestAnchorExtractorUnmatchedBlockStillPauses(t *testing.T) {
	root := writeFixture(t, multiH3IndexHTML)
	output := "```html\n<section class=\"unmatched-widget\">does not exist here</section>\n<p>also absent</p>\n```\n"

	p := NewPipeline()
	_, err := p.Run(output, "index.html", []byte(multiH3IndexHTML))
	if err == nil {
		t.Fatal("pipeline succeeded, want ambiguous-change pause")
	}
	if !strings.Contains(err.Error(), "Pipeline PAUSED") {
		t.Errorf("error missing pipeline pause contract: %v", err)
	}
	if got := readFile(t, root, "index.html"); got != multiH3IndexHTML {
		t.Errorf("file was mutated on ambiguous change:\n%s", got)
	}
}

// TestCompilerTruncatedHTMLAborts is DoD Test 2: a KindReplaceFile payload cut
// off mid-generation (unbalanced HTML tags) MUST be refused by the Diff
// Compiler with the truncation sentinel — never compiled into a broken diff.
func TestCompilerTruncatedHTMLAborts(t *testing.T) {
	truncated := `<!DOCTYPE html>
<html lang="en">
<body>
  <h3>Project Delta</h3>
  <p>Stable content</p>
</body>
`
	// </html> and </body> are missing — the canonical mid-stream truncation.
	compiler := NewCompiler()
	_, err := compiler.CompileToPatch(ChangeSet{
		TargetFile: "index.html",
		Kind:       KindReplaceFile,
		NewContent: truncated,
	}, []byte(multiH3IndexHTML))
	if err == nil {
		t.Fatal("compiler accepted a truncated HTML payload")
	}
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("error = %v, want ErrTruncatedOutput", err)
	}
	if !strings.Contains(err.Error(), "truncated before completion") {
		t.Errorf("error missing truncation pause contract: %v", err)
	}
}

// TestCompilerTruncatedFenceAborts covers the unclosed-markdown-fence
// truncation signature on a KindReplaceBlock payload.
func TestCompilerTruncatedFenceAborts(t *testing.T) {
	truncated := "<h3>Project Delta</h3>\n<p>Stable content\n"
	compiler := NewCompiler()
	_, err := compiler.CompileToPatch(ChangeSet{
		TargetFile: "index.html",
		Kind:       KindReplaceBlock,
		OldContent: "  <h3>Old Delta</h3>\n  <p>Stable content</p>",
		NewContent: truncated,
	}, []byte(multiH3IndexHTML))
	if err == nil {
		t.Fatal("compiler accepted an unclosed-fence payload")
	}
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("error = %v, want ErrTruncatedOutput", err)
	}
}

// TestCompilerCompleteHTMLCompiles is the negative control for the truncation
// guard: a structurally COMPLETE replacement must compile normally — the
// balance check must never false-flag well-formed HTML.
func TestCompilerCompleteHTMLCompiles(t *testing.T) {
	complete := `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Delta</title>
</head>
<body>
  <h3>Project Delta</h3>
  <p>Stable content</p>
</body>
</html>
`
	compiler := NewCompiler()
	diff, err := compiler.CompileToPatch(ChangeSet{
		TargetFile: "index.html",
		Kind:       KindReplaceFile,
		NewContent: complete,
	}, []byte(multiH3IndexHTML))
	if err != nil {
		t.Fatalf("compiler rejected complete HTML: %v", err)
	}
	if len(diff) == 0 {
		t.Fatal("compiler produced an empty diff for complete HTML")
	}
}

// TestPipelineTruncatedOutputDoesNotMutateFile drives the full pipeline with a
// truncated code block (unclosed fence) and asserts the on-disk target is
// NEVER touched — the truncation guard pauses the pipeline, it does not emit a
// corrupting diff.
func TestPipelineTruncatedOutputDoesNotMutateFile(t *testing.T) {
	root := writeFixture(t, multiH3IndexHTML)
	output := "```html\n<h3>Project Delta</h3>\n<p>Stable content\n"

	_, err := NewPipeline().Run(output, "index.html", []byte(multiH3IndexHTML))
	if err == nil {
		t.Fatal("pipeline succeeded on truncated output, want pause")
	}
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("error = %v, want ErrTruncatedOutput", err)
	}
	if got := readFile(t, root, "index.html"); got != multiH3IndexHTML {
		t.Errorf("target file was mutated on truncated output:\n%s", got)
	}
}
