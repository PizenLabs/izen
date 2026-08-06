package patch

import (
	"strings"
	"testing"
)

func TestParseFileHeader(t *testing.T) {
	cases := []struct {
		header   string
		wantLang string
		wantPath string
		wantOK   bool
	}{
		{"html:index.html", "html", "index.html", true},
		{"css:styles.css", "css", "styles.css", true},
		{"html: index.html", "html", "index.html", true},
		{"js script.js", "js", "script.js", true},
		{"go:path/to/file.go", "go", "path/to/file.go", true},
		{"file=index.html", "", "index.html", true},
		{"FILE=styles.css", "", "styles.css", true},
		{"html", "html", "", false},
		{"", "", "", false},
		{"   ", "", "", false},
	}
	for _, tc := range cases {
		lang, path, ok := ParseFileHeader(tc.header)
		if lang != tc.wantLang || path != tc.wantPath || ok != tc.wantOK {
			t.Errorf("ParseFileHeader(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.header, lang, path, ok, tc.wantLang, tc.wantPath, tc.wantOK)
		}
	}
}

// TestParseCodeFencesMultiFile is the North Mini / SLM acceptance case: a
// single pass that emits three files as ```lang:path blocks must extract all
// three, so fast-track never falls back to per-task execution.
func TestParseCodeFencesMultiFile(t *testing.T) {
	raw := "```html:index.html\n<!DOCTYPE html>\n<html></html>\n```\n" +
		"```css:styles.css\nbody { margin: 0; }\n```\n" +
		"```js:script.js\nconsole.log('hi');\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3:\n%v", len(files), files)
	}
	want := []struct{ path, lang, content string }{
		{"index.html", "html", "<!DOCTYPE html>\n<html></html>"},
		{"styles.css", "css", "body { margin: 0; }"},
		{"script.js", "js", "console.log('hi');"},
	}
	for i, w := range want {
		if files[i].Path != w.path || files[i].Lang != w.lang {
			t.Errorf("files[%d] = path %q lang %q, want path %q lang %q", i, files[i].Path, files[i].Lang, w.path, w.lang)
		}
		if got := strings.TrimSuffix(files[i].Content, "\n"); got != w.content {
			t.Errorf("files[%d] content = %q, want %q", i, got, w.content)
		}
		if files[i].Source != SourceFence {
			t.Errorf("files[%d] source = %q, want %q", i, files[i].Source, SourceFence)
		}
	}
}

// TestParseCodeFencesSpaceSeparatedHeader covers the ```lang path variation that
// small models emit when the SLM prompt shows a space-separated opener.
func TestParseCodeFencesSpaceSeparatedHeader(t *testing.T) {
	raw := "```js script.js\nconst x = 1;\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1:\n%v", len(files), files)
	}
	if files[0].Path != "script.js" || files[0].Lang != "js" {
		t.Errorf("file = %+v, want path script.js lang js", files[0])
	}
}

// TestParseCodeFencesFileEqualsHeader covers the ```file=path variation.
func TestParseCodeFencesFileEqualsHeader(t *testing.T) {
	raw := "```file=index.html\n<p>hello</p>\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1:\n%v", len(files), files)
	}
	if files[0].Path != "index.html" || files[0].Lang != "" {
		t.Errorf("file = %+v, want path index.html, no lang", files[0])
	}
}

// TestParseCodeFencesFileBlockProtocol covers the === FILE: ... === END protocol
// used by the layer3 worker.
func TestParseCodeFencesFileBlockProtocol(t *testing.T) {
	raw := "=== FILE: index.html\n<!DOCTYPE html>\n=== END\n" +
		"=== FILE: script.js\nconsole.log(1);\n=== END\n"
	files := ParseCodeFences(raw)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2:\n%v", len(files), files)
	}
	if files[0].Path != "index.html" || files[0].Source != SourceFileBlock {
		t.Errorf("files[0] = %+v, want path index.html source file-block", files[0])
	}
	if files[1].Path != "script.js" {
		t.Errorf("files[1] = %+v, want path script.js", files[1])
	}
}

// TestParseCodeFencesSkipsPathlessFences pins the guard: a bare ```lang block
// carries no target path and must be skipped (never guessed), leaving it to the
// caller's path-inference fallback.
func TestParseCodeFencesSkipsPathlessFences(t *testing.T) {
	raw := "```html\n<p>no path</p>\n```\n" +
		"```html:index.html\n<p>tagged</p>\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1 (pathless block skipped):\n%v", len(files), files)
	}
	if files[0].Path != "index.html" {
		t.Errorf("file = %+v, want path index.html", files[0])
	}
}

// TestParseCodeFencesMixedForms ensures all header variations can coexist in a
// single response, in any order.
func TestParseCodeFencesMixedForms(t *testing.T) {
	raw := "=== FILE: index.html\n<p>h</p>\n=== END\n" +
		"```css styles.css\nbody{}\n```\n" +
		"```file=script.js\nconsole.log(1);\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3:\n%v", len(files), files)
	}
	paths := []string{files[0].Path, files[1].Path, files[2].Path}
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing path %q in %v", want, paths)
		}
	}
}

// TestParseMarkdownFencesIgnoresFileProtocol pins the fence-only subset: the
// === FILE: protocol is left for its owner, never double-parsed.
func TestParseMarkdownFencesIgnoresFileProtocol(t *testing.T) {
	raw := "=== FILE: a.go\npackage a\n=== END\n```go b.go\npackage b\n```\n"
	files := ParseMarkdownFences(raw)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1 (=== FILE: excluded):\n%v", len(files), files)
	}
	if files[0].Path != "b.go" {
		t.Errorf("file = %+v, want path b.go", files[0])
	}
}

// TestCodeFileFullFilePatch verifies a fence block converts to a Tier3 full
// rewrite patch with no diff markers.
func TestCodeFileFullFilePatch(t *testing.T) {
	raw := "```html:index.html\n<!DOCTYPE html>\n<html></html>\n```\n"
	files := ParseCodeFences(raw)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	p := files[0].FullFilePatch()
	if p.File != "index.html" || p.Modified != "<!DOCTYPE html>\n<html></html>" {
		t.Errorf("patch = %+v", p)
	}
	if p.Tier != Tier3WholeFile || p.Strategy != Tier3WholeFile.String() {
		t.Errorf("patch tier = %d %s, want Tier3WholeFile", p.Tier, p.Strategy)
	}
	if strings.Contains(p.Modified, "--- a/") || strings.Contains(p.Modified, "@@") {
		t.Errorf("full-file patch must not carry diff markers:\n%s", p.Modified)
	}
}

// TestTieredParserAcceptsRawCodeForNewFile pins the per-task 0-byte fallback in
// the tiered parser: for an empty original (new/0-byte file), ANY raw code
// block is the complete file content — no diff markers required.
func TestTieredParserAcceptsRawCodeForNewFile(t *testing.T) {
	p := NewTieredParser()
	got, err := p.Parse("", "```html:index.html\n<!DOCTYPE html>\n<html></html>\n```\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Modified != "<!DOCTYPE html>\n<html></html>\n" {
		t.Errorf("Modified = %q, want raw block content", got.Modified)
	}
	if got.Tier != Tier3WholeFile {
		t.Errorf("tier = %d, want Tier3WholeFile", got.Tier)
	}
}

// TestTieredParserRawTextForNewFile pins the same fallback for a raw-text
// payload with no fences at all.
func TestTieredParserRawTextForNewFile(t *testing.T) {
	p := NewTieredParser()
	got, err := p.Parse("", "<p>raw text is the full content</p>\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Modified != "<p>raw text is the full content</p>\n" {
		t.Errorf("Modified = %q, want the raw text", got.Modified)
	}
}
