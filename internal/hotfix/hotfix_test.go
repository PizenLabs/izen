package hotfix

import (
	"strings"
	"testing"
)

// mismatchedHTML is the canonical production fixture: a large-enough document
// whose only structural error is a mismatched closing tag inside a nested
// sectioning element.
const mismatchedHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Large Landing</title>
</head>
<body>
  <main>
    <section class="hero">
      <h1>Welcome</h1>
      <p>Tagline</p>
    </section>
    <article class="project">
      <h3>Project Delta</h2>
      <p>Stable content</p>
    </article>
    <section class="contact">
      <h2>Contact</h2>
      <p>Contact details</p>
    </section>
  </main>
</body>
</html>
`

func TestResolveHTMLTarget_MismatchedClosingTag(t *testing.T) {
	tgt, ok := ResolveHTMLTarget(mismatchedHTML)
	if !ok {
		t.Fatal("expected a resolvable target for the mismatched closing tag")
	}
	mm := tgt.Mismatch
	if mm.Kind != KindUnmatchedClosing {
		t.Fatalf("kind = %q, want unmatched-closing", mm.Kind)
	}
	if mm.Tag != "h2" {
		t.Errorf("tag = %q, want h2", mm.Tag)
	}
	if mm.Expected != "h3" {
		t.Errorf("expected = %q, want h3", mm.Expected)
	}
	if mm.Line != 13 {
		t.Errorf("mismatch line = %d, want 13", mm.Line)
	}
	// The target block must be scoped to the enclosing <article>, never the
	// whole document.
	if tgt.StartLine != 12 || tgt.EndLine != 15 {
		t.Fatalf("target span = %d-%d, want 12-15", tgt.StartLine, tgt.EndLine)
	}
	if !strings.Contains(tgt.Block, "Project Delta") {
		t.Errorf("target block missing the offending content:\n%s", tgt.Block)
	}
	if strings.Contains(tgt.Block, "<section class=\"contact\"") {
		t.Errorf("target block leaked outside the enclosing article:\n%s", tgt.Block)
	}
	desc := mm.Describe()
	if !strings.Contains(desc, "line 13") || !strings.Contains(desc, "</h2>") || !strings.Contains(desc, "<h3>") {
		t.Errorf("mismatch description unhelpful: %q", desc)
	}
}

func TestResolveHTMLTarget_WellFormedReturnsFalse(t *testing.T) {
	wellFormed := strings.Replace(mismatchedHTML, "</h2>", "</h3>", 1)
	if _, ok := ResolveHTMLTarget(wellFormed); ok {
		t.Fatal("well-formed document must not resolve a target")
	}
}

func TestResolveHTMLTarget_UnclosedSection(t *testing.T) {
	content := `<!DOCTYPE html>
<html>
<body>
  <section>
    <p>alpha</p>
  </section>
  <section>
    <p>beta</p>
</body>
</html>
`
	tgt, ok := ResolveHTMLTarget(content)
	if !ok {
		t.Fatal("expected a resolvable target for the unclosed section")
	}
	mm := tgt.Mismatch
	if mm.Kind != KindUnclosed {
		t.Fatalf("kind = %q, want unclosed", mm.Kind)
	}
	if mm.Tag != "section" {
		t.Errorf("tag = %q, want section", mm.Tag)
	}
	if !strings.Contains(mm.Describe(), "never closed") {
		t.Errorf("description missing the unclosed reason: %q", mm.Describe())
	}
}

func TestResolveHTMLTarget_IgnoresCommentsAndScripts(t *testing.T) {
	content := `<!DOCTYPE html>
<html>
<body>
  <!-- <div> not a real tag </div> -->
  <script>
    const s = "<section> not a real tag";
    if (a < b && c > d) { log(); }
  </script>
  <h3>Project Delta</h2>
</body>
</html>
`
	tgt, ok := ResolveHTMLTarget(content)
	if !ok {
		t.Fatal("expected a resolvable target past comment/script noise")
	}
	if tgt.Mismatch.Tag != "h2" {
		t.Fatalf("tag = %q, want h2 (comment/script content must be ignored)", tgt.Mismatch.Tag)
	}
}

func TestResolveHTMLTarget_NonHTMLReturnsFalse(t *testing.T) {
	if _, ok := ResolveHTMLTarget("package main\n\nfunc main() {}\n"); ok {
		t.Fatal("non-HTML content must not resolve a target")
	}
	if _, ok := ResolveHTMLTarget(""); ok {
		t.Fatal("empty content must not resolve a target")
	}
}

func TestResolveHTMLTarget_WindowFallbackForTopLevelMismatch(t *testing.T) {
	content := `<!DOCTYPE html>
<html>
<body>
<h1>Header</h1>
<h3>Broken</h2>
<p>tail</p>
</body>
</html>
`
	tgt, ok := ResolveHTMLTarget(content)
	if !ok {
		t.Fatal("expected a resolvable target for the top-level mismatch")
	}
	if tgt.Mismatch.Tag != "h2" {
		t.Fatalf("tag = %q, want h2", tgt.Mismatch.Tag)
	}
	// body/html are sectioning ancestors whose span would be the whole file, so
	// the fallback must bound the block to a small window around the mismatch.
	if tgt.EndLine-tgt.StartLine > 20 {
		t.Fatalf("target block too large (lines %d-%d); expected a bounded window", tgt.StartLine, tgt.EndLine)
	}
	if !strings.Contains(tgt.Block, "Broken") {
		t.Errorf("target block missing the offending line:\n%s", tgt.Block)
	}
}

// TestResolveHTMLCandidates_WellFormedEmpty asserts candidate discovery yields
// nothing for a balanced document.
func TestResolveHTMLCandidates_WellFormedEmpty(t *testing.T) {
	wellFormed := strings.Replace(mismatchedHTML, "</h2>", "</h3>", 1)
	if got := ResolveHTMLCandidates(wellFormed); len(got) != 0 {
		t.Fatalf("well-formed document yielded %d candidates: %+v", len(got), got)
	}
}

// TestResolveHTMLCandidates_MultipleAnomalies asserts deterministic candidate
// discovery finds EVERY structural anomaly (not just the first), each scoped to
// its own bounded block, in scan order.
func TestResolveHTMLCandidates_MultipleAnomalies(t *testing.T) {
	content := `<!DOCTYPE html>
<html>
<body>
  <article class="alpha">
    <h3>Alpha</h2>
    <p>one</p>
  </article>
  <article class="beta">
    <h3>Beta</span>
    <p>two</p>
  </article>
</body>
</html>
`
	cands := ResolveHTMLCandidates(content)
	if len(cands) < 2 {
		t.Fatalf("expected at least 2 candidates, got %d", len(cands))
	}
	first := cands[0].Mismatch
	if first.Kind != KindUnmatchedClosing || first.Tag != "h2" || first.Expected != "h3" {
		t.Errorf("first candidate = %+v, want the Alpha </h2> vs <h3> mismatch", first)
	}
	if !strings.Contains(cands[0].Block, "Alpha") {
		t.Errorf("first candidate block missing the Alpha content:\n%s", cands[0].Block)
	}
	if strings.Contains(cands[0].Block, "Beta") {
		t.Errorf("first candidate block leaked into the second anomaly:\n%s", cands[0].Block)
	}
	second := cands[1].Mismatch
	if second.Kind != KindUnmatchedClosing || second.Tag != "span" {
		t.Errorf("second candidate = %+v, want the Beta </span> mismatch", second)
	}
	if !strings.Contains(cands[1].Block, "Beta") {
		t.Errorf("second candidate block missing the Beta content:\n%s", cands[1].Block)
	}
}
