package changeset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/patch"
)

// largeIndexHTML is a structurally complete HTML document whose newline count
// exceeds the small-file boundary (SmallFileLineThreshold = 100). It is the
// deterministic fixture proving the bounded-change invariant: a $hot request
// against a LARGE file must never produce a whole-file generation contract.
func largeIndexHTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("<head>\n")
	b.WriteString("  <title>Large Landing</title>\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("  <header>\n")
	b.WriteString("    <h3>Old Delta</h3>\n")
	b.WriteString("    <p>Stable content</p>\n")
	b.WriteString("  </header>\n")
	b.WriteString("  <main>\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "    <section id=\"sect-%02d\">\n      <p>Section %02d body text</p>\n    </section>\n", i, i)
	}
	b.WriteString("  </main>\n")
	b.WriteString("  <footer>\n")
	b.WriteString("    <p>Footer note</p>\n")
	b.WriteString("  </footer>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// writeLargeFixture persists content as index.html and asserts the fixture is
// actually LARGE (over the small-file boundary), so the test can never silently
// degrade into the small-file path.
func writeLargeFixture(t *testing.T, content string) string {
	t.Helper()
	if strings.Count(content, "\n") < smallFileLineThreshold {
		t.Fatalf("fixture must exceed smallFileLineThreshold=%d newlines, got %d",
			smallFileLineThreshold, strings.Count(content, "\n"))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// applyLargePatch runs a compiled diff through the application's authoritative
// patch engine and asserts a clean apply to the given original content.
func applyLargePatch(t *testing.T, root, original string, diff []byte) string {
	t.Helper()
	eng := patch.NewEngine()
	result, err := eng.Apply(root, patch.Request{
		File:          "index.html",
		Original:      original,
		Raw:           string(diff),
		TaskObjective: "fix the large landing page",
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

// TestLargeFileWholeFileOutputRejected is the KEY architectural invariant test:
// a simple $hot request against a LARGE file where the model responds with a
// whole-file re-emission must NOT produce a full-file generation contract. The
// extractor rejects the out-of-contract artifact with ErrFullFileRejected and
// the on-disk file is never mutated.
func TestLargeFileWholeFileOutputRejected(t *testing.T) {
	large := largeIndexHTML()
	root := writeLargeFixture(t, large)
	output := "```html\n" + large + "\n```\n"

	_, err := NewPipeline().Run(output, "index.html", []byte(large))
	if err == nil {
		t.Fatal("pipeline accepted a whole-file re-emission for a large file")
	}
	if !errors.Is(err, ErrFullFileRejected) {
		t.Fatalf("error = %v, want ErrFullFileRejected", err)
	}
	if !strings.Contains(err.Error(), "bounded hotfix contract") {
		t.Errorf("error missing bounded-contract pause reason: %v", err)
	}
	if got := readFile(t, root, "index.html"); got != large {
		t.Fatal("target file was mutated on full-file rejection")
	}
}

// TestLargeFilePathTaggedBlockRejected covers the explicit full-file indicator
// path: a fence header carrying a target path claims whole-file replacement,
// which is out of contract for a large existing file.
func TestLargeFilePathTaggedBlockRejected(t *testing.T) {
	large := largeIndexHTML()
	root := writeLargeFixture(t, large)
	output := "```html:index.html\n" + large + "\n```\n"

	_, err := NewPipeline().Run(output, "index.html", []byte(large))
	if err == nil {
		t.Fatal("pipeline accepted a path-tagged whole-file block for a large file")
	}
	if !errors.Is(err, ErrFullFileRejected) {
		t.Fatalf("error = %v, want ErrFullFileRejected", err)
	}
	if got := readFile(t, root, "index.html"); got != large {
		t.Fatal("target file was mutated on full-file rejection")
	}
}

// TestLargeFileSnippetMapsToReplaceBlock is the positive half of the invariant:
// a LARGE file CAN be edited, but ONLY through the anchored ReplaceBlock
// contract. A corrected snippet must map to REPLACE_BLOCK, compile into a valid
// diff, and apply cleanly while preserving the untouched rest of the document.
func TestLargeFileSnippetMapsToReplaceBlock(t *testing.T) {
	large := largeIndexHTML()
	root := writeLargeFixture(t, large)
	output := "```html\n  <h3>Project Delta</h3>\n  <p>Stable content</p>\n```\n"

	compiled, err := NewPipeline().Run(output, "index.html", []byte(large))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled changes = %d, want 1", len(compiled))
	}
	cc := compiled[0]
	if cc.ChangeSet.Kind != KindReplaceBlock {
		t.Fatalf("kind = %s, want REPLACE_BLOCK (large files are snippet-only)", cc.ChangeSet.Kind)
	}
	if cc.ChangeSet.TargetFile != "index.html" {
		t.Errorf("target = %q, want index.html", cc.ChangeSet.TargetFile)
	}
	if !cc.Validation.Valid {
		t.Fatalf("validation invalid: %v", cc.Validation.Reasons)
	}
	got := applyLargePatch(t, root, large, cc.Diff)
	if !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("applied content missing the corrected header:\n%.400s", got)
	}
	if strings.Contains(got, "<h3>Old Delta</h3>") {
		t.Errorf("applied content still contains the old header")
	}
	// The untouched tail of the large document must survive the anchored patch.
	if !strings.Contains(got, "sect-39") || !strings.Contains(got, "</html>") {
		t.Errorf("untouched document tail was clobbered by the snippet patch:\n%.400s", got)
	}
}

// TestSmallFileWholeFileOutputStillReplaceFile is the negative control proving
// the bounded-rewrite contract for stub/small files is preserved: a whole-file
// output for a small file still maps to REPLACE_FILE.
func TestSmallFileWholeFileOutputStillReplaceFile(t *testing.T) {
	small := indexHTML
	root := writeFixture(t, small)
	modified := strings.Replace(small, "</html>", "  <p>Appended</p>\n</html>", 1)
	if modified == small {
		t.Fatal("fixture modification produced a no-op")
	}
	output := "```html\n" + modified + "\n```\n"

	compiled, err := NewPipeline().Run(output, "index.html", []byte(small))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled changes = %d, want 1", len(compiled))
	}
	cc := compiled[0]
	if cc.ChangeSet.Kind != KindReplaceFile {
		t.Fatalf("kind = %s, want REPLACE_FILE for a small file", cc.ChangeSet.Kind)
	}
	if !cc.Validation.Valid {
		t.Fatalf("validation invalid: %v", cc.Validation.Reasons)
	}
	got := applyLargePatch(t, root, small, cc.Diff)
	if !strings.Contains(got, "Appended") {
		t.Errorf("applied content missing the appended line:\n%s", got)
	}
}
