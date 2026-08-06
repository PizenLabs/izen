package ui

import (
	"testing"
)

// TestExtractBuildProposals_MultiFileNorthMini pins acceptance criterion #1: a
// single-pass response from a North Mini / SLM model that emits all files as
// path-tagged markdown blocks must yield one proposal per file — so the
// fast-track build creates every file in one pass instead of falling back to
// per-task execution.
func TestExtractBuildProposals_MultiFileNorthMini(t *testing.T) {
	raw := "```html:index.html\n<!DOCTYPE html>\n<html><body><h1>hi</h1></body></html>\n```\n" +
		"```css:styles.css\nbody { margin: 0; }\n```\n" +
		"```js:script.js\nconsole.log('hello');\n```\n"

	props := extractBuildProposals(raw)
	if len(props) != 3 {
		t.Fatalf("proposals = %d, want 3:\n%+v", len(props), props)
	}

	got := map[string]string{}
	for _, p := range props {
		got[p.Target.QualifiedName] = p.Diff
	}
	want := map[string]string{
		"index.html": "<!DOCTYPE html>\n<html><body><h1>hi</h1></body></html>",
		"styles.css": "body { margin: 0; }",
		"script.js":  "console.log('hello');",
	}
	for path, content := range want {
		if got[path] != content {
			t.Errorf("proposal for %s = %q, want %q (all proposals: %+v)", path, got[path], content, got)
		}
	}
}

// TestExtractBuildProposals_SpaceAndFileEqualsHeaders covers the remaining
// fence header variations so the fast-track parser is robust to every format
// a small model may emit.
func TestExtractBuildProposals_SpaceAndFileEqualsHeaders(t *testing.T) {
	raw := "```js script.js\nconst x = 1;\n```\n" +
		"```file=index.html\n<p>hi</p>\n```\n" +
		"=== FILE: styles.css\nbody{}\n=== END\n"

	props := extractBuildProposals(raw)
	if len(props) != 3 {
		t.Fatalf("proposals = %d, want 3:\n%+v", len(props), props)
	}
	got := map[string]string{}
	for _, p := range props {
		got[p.Target.QualifiedName] = p.Diff
	}
	for _, path := range []string{"script.js", "index.html", "styles.css"} {
		if _, ok := got[path]; !ok {
			t.Errorf("missing proposal for %s (all: %+v)", path, got)
		}
	}
}
