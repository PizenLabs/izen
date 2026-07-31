package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyLineRangeFallbackNeverPanics(t *testing.T) {
	cases := []struct {
		name string
		orig string
		hunk diffHunk
	}{
		{"empty original", "", diffHunk{oldStart: 1, oldCount: 1, newBlock: "x"}},
		{"zero start", "a\nb\nc", diffHunk{oldStart: 0, oldCount: 1, newBlock: "x"}},
		{"negative start", "a\nb\nc", diffHunk{oldStart: -5, oldCount: 1, newBlock: "x"}},
		{"start beyond file", "a\nb\nc", diffHunk{oldStart: 100, oldCount: 5, newBlock: "x"}},
		{"count beyond file", "a\nb\nc", diffHunk{oldStart: 2, oldCount: 999, newBlock: "x"}},
		{"negative count", "a\nb\nc", diffHunk{oldStart: 1, oldCount: -3, newBlock: "x"}},
		{"empty new block", "a\nb\nc", diffHunk{oldStart: 1, oldCount: 1, newBlock: ""}},
		{"single line file", "only", diffHunk{oldStart: 5, oldCount: 10, newBlock: "replaced"}},
		{"empty file", "", diffHunk{oldStart: 1, oldCount: 1, newBlock: "x"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic under any circumstance.
			got, ok := applyLineRangeFallback(tc.orig, tc.hunk)
			_ = got
			_ = ok
		})
	}
}

func TestApplyUnifiedPatchMalformedNeverPanics(t *testing.T) {
	cases := []struct {
		name string
		orig string
		diff string
	}{
		{"hunk out of bounds", "line1\nline2\nline3", "@@ -50,5 +50,5 @@\n context\n-line2\n+line2edited\n"},
		{"no match at all", "a\nb\nc", "@@ -2,1 +2,1 @@\n totallydifferent\n-b\n+bb\n"},
		{"garbage hunk numbers", "a\nb\nc", "@@ -99999,99999 +1,1 @@\n-ignored\n+added\n"},
		{"empty original huge hunk", "", "@@ -3,10 +3,10 @@\n-x\n+y\n"},
		{"drifted context with fallback", "func a() {}\nfunc b() {}\nfunc c() {}", "@@ -1,1 +1,1 @@\nfunc a() {}\n-removed\n+added\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("applyUnifiedPatch panicked: %v", r)
					}
				}()
				got, err = applyUnifiedPatch(tc.orig, tc.diff)
			}()
			if err == nil {
				t.Logf("applied without error, result length %d", len(got))
			}
		})
	}
}

func TestSplitAndFilterPatches(t *testing.T) {
	t.Run("no headers returns original", func(t *testing.T) {
		input := "@@ -1,3 +1,3 @@\n context\n-old\n+new\n"
		got := SplitAndFilterPatches(input, "file.go")
		if got != input {
			t.Fatalf("expected unchanged input, got %q", got)
		}
	})

	t.Run("single file header returns original", func(t *testing.T) {
		input := "--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n context\n-old\n+new\n"
		got := SplitAndFilterPatches(input, "file.go")
		if got != input {
			t.Fatalf("expected unchanged input, got %q", got)
		}
	})

	t.Run("filters to matching file", func(t *testing.T) {
		input := "--- a/other.go\n+++ b/other.go\n@@ -1,1 +1,1 @@\n-other\n+other2\n--- a/target.go\n+++ b/target.go\n@@ -5,1 +5,1 @@\n-foo\n+bar\n"
		got := SplitAndFilterPatches(input, "target.go")
		if !strings.Contains(got, "target.go") {
			t.Fatalf("expected result to contain target.go, got %q", got)
		}
		if strings.Contains(got, "other.go") {
			t.Fatalf("expected result NOT to contain other.go, got %q", got)
		}
		if !strings.Contains(got, "foo") {
			t.Fatalf("expected result to contain target hunk content, got %q", got)
		}
	})

	t.Run("falls back to original when no match found", func(t *testing.T) {
		input := "--- a/other.go\n+++ b/other.go\n@@ -1,1 +1,1 @@\n-other\n+other2\n"
		got := SplitAndFilterPatches(input, "target.go")
		if got != input {
			t.Fatalf("expected fallback to original, got %q", got)
		}
	})

	t.Run("empty input returns empty", func(t *testing.T) {
		got := SplitAndFilterPatches("", "file.go")
		if got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
}

func TestFuzzyMatchHunkHandlesDriftedContext(t *testing.T) {
	// Simulate AST skeleton drift: line offsets shifted by 1, one line changed
	original := "package main\n\nfunc main() {\n\tprintln(\"old\")\n\tprintln(\"extra\")\n}\n"
	diff := "@@ -3,1 +3,1 @@\n func main() {\n-\tprintln(\"old\")\n+\tprintln(\"new\")\n}\n"
	result, err := applyUnifiedPatch(original, diff)
	if err != nil {
		t.Fatalf("expected fuzzy match to succeed on drifted context, got error: %v", err)
	}
	if !strings.Contains(result, "println(\"new\")") {
		t.Fatalf("expected result to contain new content, got: %q", result)
	}
	if !strings.Contains(result, "println(\"extra\")") {
		t.Fatalf("expected result to preserve extra lines, got: %q", result)
	}
}

func TestApplyUnifiedPatchExpiredContextReturnsError(t *testing.T) {
	diff := "@@ -3,1 +3,1 @@\n func main() {\n-\tprintln(\"old\")\n+\tprintln(\"new\")\n}\n"
	// Use a completely different function signature and body so no line from
	// the oldBlock exists in the current file — even fuzzy matching must fail.
	changed := "package main\n\nfunc completely_unrelated() {\n\tx := 42\n}\n"
	_, err := applyUnifiedPatch(changed, diff)
	if err == nil {
		t.Fatal("expected an error when target context has changed, got nil")
	}
}

func TestIsTruncated(t *testing.T) {
	t.Run("nil original is not truncated", func(t *testing.T) {
		if isTruncated("", "anything") {
			t.Fatal("expected false for empty original")
		}
	})
	t.Run("modified >= 30% of original is not truncated", func(t *testing.T) {
		if isTruncated("aaaaa", "aaa") {
			t.Fatal("expected false when modified is >= 30%")
		}
	})
	t.Run("modified < 30% of original is truncated", func(t *testing.T) {
		if !isTruncated("aaaaaaaaaa", "aa") {
			t.Fatal("expected true when modified is < 30%")
		}
	})
}

func TestApplySearchReplaceBlock(t *testing.T) {
	t.Run("empty original returns false", func(t *testing.T) {
		_, ok := applySearchReplaceBlock("", "content")
		if ok {
			t.Fatal("expected false for empty original")
		}
	})
	t.Run("empty modified returns false", func(t *testing.T) {
		_, ok := applySearchReplaceBlock("original", "")
		if ok {
			t.Fatal("expected false for empty modified")
		}
	})
	t.Run("exact match returns true", func(t *testing.T) {
		result, ok := applySearchReplaceBlock("original", "original")
		if !ok {
			t.Fatal("expected true for exact match")
		}
		if result != "original" {
			t.Fatalf("expected 'original', got %q", result)
		}
	})
	t.Run("substring match returns true unchanged", func(t *testing.T) {
		original := "line1\nline2\nline3\n"
		modified := "line2\n"
		result, ok := applySearchReplaceBlock(original, modified)
		if !ok {
			t.Fatal("expected true for exact substring match")
		}
		if result != original {
			t.Fatalf("expected original unchanged, got %q", result)
		}
	})
	t.Run("line-by-line match of contiguous block", func(t *testing.T) {
		original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
		modified := "func main() {\n\tprintln(\"hello\")\n}\n"
		result, ok := applySearchReplaceBlock(original, modified)
		if !ok {
			t.Fatal("expected true for contiguous line block")
		}
		if result != original {
			t.Fatalf("expected original unchanged, got %q", result)
		}
	})
	t.Run("no match returns false", func(t *testing.T) {
		original := "package main\n\nfunc main() {}\n"
		modified := "func foo() {}\n"
		_, ok := applySearchReplaceBlock(original, modified)
		if ok {
			t.Fatal("expected false for content not present in original")
		}
	})
}

func TestApplySingleLinePatch(t *testing.T) {
	original := `package main

import (
	"fmt"
)

func main() {
	fmt.Println("hello")
}
`
	// Unified diff that changes "hello" to "world" — single-line change
	diff := `--- a/main.go
+++ b/main.go
@@ -6,3 +6,3 @@
 func main() {
-	fmt.Println("hello")
+	fmt.Println("world")
 }
`
	result, err := applyUnifiedPatch(original, diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `fmt.Println("world")`) {
		t.Fatalf("expected result to contain 'world', got: %q", result)
	}
	if !strings.Contains(result, `"fmt"`) {
		t.Fatalf("expected result to preserve import, got: %q", result)
	}
	if !strings.Contains(result, "package main") {
		t.Fatalf("expected result to preserve package declaration, got: %q", result)
	}
}

func TestApplyMultiLinePatch(t *testing.T) {
	original := `package main

import (
	"fmt"
)

func main() {
	fmt.Println("hello")
	fmt.Println("world")
}
`
	// Unified diff that adds a new import and a function call — multi-line change
	diff := `--- a/main.go
+++ b/main.go
@@ -2,3 +2,4 @@
 import (
 	"fmt"
+	"os"
 )
@@ -6,3 +7,4 @@
 func main() {
 	fmt.Println("hello")
-	fmt.Println("world")
+	fmt.Println("world")
+	os.Exit(0)
 }
`
	result, err := applyUnifiedPatch(original, diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `"os"`) {
		t.Fatalf("expected result to contain os import, got: %q", result)
	}
	if !strings.Contains(result, "os.Exit(0)") {
		t.Fatalf("expected result to contain os.Exit(0), got: %q", result)
	}
	if !strings.Contains(result, `fmt.Println("world")`) {
		t.Fatalf("expected result to preserve 'world' print, got: %q", result)
	}
	if !strings.Contains(result, "package main") {
		t.Fatalf("expected result to preserve package declaration, got: %q", result)
	}
}

func TestApplyMultiHunkPatchPreservesSurroundingCode(t *testing.T) {
	original := `package main

import (
	"fmt"
)

func greet(name string) string {
	return "Hello, " + name
}

func main() {
	msg := greet("World")
	fmt.Println(msg)
}
`
	// Two hunks: one changing the greet function, one changing main
	diff := `--- a/main.go
+++ b/main.go
@@ -5,3 +5,3 @@
 func greet(name string) string {
-	return "Hello, " + name
+	return "Hi, " + name
 }
@@ -9,3 +9,3 @@
 func main() {
-	msg := greet("World")
+	msg := greet("Universe")
 	fmt.Println(msg)
`
	result, err := applyUnifiedPatch(original, diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `return "Hi, " + name`) {
		t.Fatalf("expected Hi greeting, got: %q", result)
	}
	if !strings.Contains(result, `greet("Universe")`) {
		t.Fatalf("expected Universe greeting, got: %q", result)
	}
	if !strings.Contains(result, `fmt.Println(msg)`) {
		t.Fatalf("expected preserved fmt.Println, got: %q", result)
	}
	if !strings.Contains(result, `package main`) {
		t.Fatalf("expected preserved package declaration, got: %q", result)
	}
	// Verify the untouched func greet declaration line is preserved
	if !strings.Contains(result, `func greet(name string) string {`) {
		t.Fatalf("expected preserved func signature, got: %q", result)
	}
}

func TestApplyNewFilePatch(t *testing.T) {
	// Empty original — should create new content
	diff := `--- a/new.go
+++ b/new.go
@@ -0,0 +1,3 @@
+package main
+
+func newFunc() int { return 42 }
`
	result, err := applyUnifiedPatch("", diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "package main") {
		t.Fatalf("expected package declaration, got: %q", result)
	}
	if !strings.Contains(result, "func newFunc() int { return 42 }") {
		t.Fatalf("expected newFunc, got: %q", result)
	}
}

func TestSanitizeDiffContentStripsDiffMarkers(t *testing.T) {
	input := "```diff\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n context\n-old\n+new\n```"
	result := SanitizeDiffContent(input)
	if strings.Contains(result, "```") {
		t.Fatalf("expected no code fences, got: %q", result)
	}
	if strings.Contains(result, "--- ") {
		t.Fatalf("expected no --- markers, got: %q", result)
	}
	if strings.Contains(result, "+++ ") {
		t.Fatalf("expected no +++ markers, got: %q", result)
	}
	if !strings.Contains(result, "new") {
		t.Fatalf("expected 'new' in result, got: %q", result)
	}
}

func TestSanitizeDiffContentPreservesNonDiffContent(t *testing.T) {
	input := "package main\n\nfunc main() {}\n"
	result := SanitizeDiffContent(input)
	if result != input {
		t.Fatalf("expected unchanged output, got: %q", result)
	}
}

func TestParseDiffHunks(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n context\n-old\n+new\n@@ -5,2 +5,2 @@\n another\n-remove\n+add\n"
	hunks := parseDiffHunks(diff)
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}
	if !strings.Contains(hunks[0].newBlock, "new") {
		t.Fatalf("expected first hunk newBlock to contain 'new', got: %q", hunks[0].newBlock)
	}
	if !strings.Contains(hunks[1].newBlock, "add") {
		t.Fatalf("expected second hunk newBlock to contain 'add', got: %q", hunks[1].newBlock)
	}
}

func TestParseHunkHeader(t *testing.T) {
	start, count := parseHunkHeader("@@ -3,5 +3,5 @@ func main() {")
	if start != 3 {
		t.Fatalf("expected start=3, got %d", start)
	}
	if count != 5 {
		t.Fatalf("expected count=5, got %d", count)
	}
}

func TestParseHunkHeaderDefault(t *testing.T) {
	start, count := parseHunkHeader("@@ -1 +1 @@")
	if start != 1 {
		t.Fatalf("expected start=1, got %d", start)
	}
	if count != 1 {
		t.Fatalf("expected count=1, got %d", count)
	}
}

func TestParseHunkHeaderNegativeStart(t *testing.T) {
	start, count := parseHunkHeader("@@ -0,5 +0,5 @@")
	if start != 1 {
		t.Fatalf("expected start=1 (clamped), got %d", start)
	}
	if count != 5 {
		t.Fatalf("expected count=5, got %d", count)
	}
}

func TestFindContextAnchor(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	idx, ok := findContextAnchor(lines, "c", 2, 5)
	if !ok {
		t.Fatal("expected to find anchor")
	}
	if idx != 2 {
		t.Fatalf("expected index 2, got %d", idx)
	}
}

func TestFindContextAnchorNotFound(t *testing.T) {
	lines := []string{"a", "b", "c"}
	_, ok := findContextAnchor(lines, "z", 1, 1)
	if ok {
		t.Fatal("expected not to find anchor")
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	if firstNonEmptyLine("\n\nhello\nworld") != "hello" {
		t.Fatalf("expected 'hello', got %q", firstNonEmptyLine("\n\nhello\nworld"))
	}
	if firstNonEmptyLine("") != "" {
		t.Fatalf("expected empty for empty input")
	}
	if firstNonEmptyLine("\n\n\n") != "" {
		t.Fatalf("expected empty for only whitespace")
	}
}

func TestParseSearchReplaceBlocks(t *testing.T) {
	t.Run("single block", func(t *testing.T) {
		input := "<<<<<<< SEARCH\nline1\nline2\n=======\nline1\nline2_modified\n>>>>>>>"
		blocks := ParseSearchReplaceBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		if blocks[0].search != "line1\nline2" {
			t.Fatalf("expected search 'line1\\nline2', got %q", blocks[0].search)
		}
		if blocks[0].replace != "line1\nline2_modified" {
			t.Fatalf("expected replace 'line1\\nline2_modified', got %q", blocks[0].replace)
		}
	})

	t.Run("multiple blocks", func(t *testing.T) {
		input := "<<<<<<< SEARCH\nold1\n=======\nnew1\n>>>>>>>\nsome stuff\n<<<<<<< SEARCH\nold2\n=======\nnew2\n>>>>>>>"
		blocks := ParseSearchReplaceBlocks(input)
		if len(blocks) != 2 {
			t.Fatalf("expected 2 blocks, got %d", len(blocks))
		}
		if blocks[0].search != "old1" || blocks[0].replace != "new1" {
			t.Fatalf("first block mismatch: search=%q replace=%q", blocks[0].search, blocks[0].replace)
		}
		if blocks[1].search != "old2" || blocks[1].replace != "new2" {
			t.Fatalf("second block mismatch: search=%q replace=%q", blocks[1].search, blocks[1].replace)
		}
	})

	t.Run("no blocks returns nil", func(t *testing.T) {
		blocks := ParseSearchReplaceBlocks("just some random content\nno markers here")
		if len(blocks) != 0 {
			t.Fatalf("expected 0 blocks, got %d", len(blocks))
		}
	})

	t.Run("malformed block missing replace", func(t *testing.T) {
		input := "<<<<<<< SEARCH\nold\n=======\n>>>>>>>"
		blocks := ParseSearchReplaceBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		if blocks[0].replace != "" {
			t.Fatalf("expected empty replace, got %q", blocks[0].replace)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		blocks := ParseSearchReplaceBlocks("")
		if len(blocks) != 0 {
			t.Fatalf("expected 0 blocks, got %d", len(blocks))
		}
	})
}

func TestApplySearchReplaceBlockFromBlocks(t *testing.T) {
	t.Run("basic replacement", func(t *testing.T) {
		original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
		blocks := []searchReplaceBlock{{
			search:  "\tprintln(\"hello\")",
			replace: "\tprintln(\"world\")",
		}}
		result, ok := ApplySearchReplaceBlocks(original, blocks)
		if !ok {
			t.Fatal("expected successful replacement")
		}
		if !strings.Contains(result, "println(\"world\")") {
			t.Fatalf("expected result to contain new content, got: %q", result)
		}
		if strings.Contains(result, "println(\"hello\")") {
			t.Fatalf("expected result NOT to contain old content, got: %q", result)
		}
	})

	t.Run("multiple replacements", func(t *testing.T) {
		original := "line1\nline2\nline3\nline4\n"
		blocks := []searchReplaceBlock{
			{search: "line1\n", replace: "changed1\n"},
			{search: "line3\n", replace: "changed3\n"},
		}
		result, ok := ApplySearchReplaceBlocks(original, blocks)
		if !ok {
			t.Fatal("expected successful replacement")
		}
		if !strings.Contains(result, "changed1") {
			t.Fatalf("expected result to contain 'changed1', got: %q", result)
		}
		if !strings.Contains(result, "changed3") {
			t.Fatalf("expected result to contain 'changed3', got: %q", result)
		}
		if !strings.Contains(result, "line2") {
			t.Fatalf("expected result to preserve 'line2', got: %q", result)
		}
	})

	t.Run("search not found returns false", func(t *testing.T) {
		original := "package main\n"
		blocks := []searchReplaceBlock{{search: "nonexistent", replace: "replacement"}}
		_, ok := ApplySearchReplaceBlocks(original, blocks)
		if ok {
			t.Fatal("expected false when search not found")
		}
	})

	t.Run("empty original returns false", func(t *testing.T) {
		_, ok := ApplySearchReplaceBlocks("", []searchReplaceBlock{{search: "x", replace: "y"}})
		if ok {
			t.Fatal("expected false for empty original")
		}
	})

	t.Run("empty blocks returns false", func(t *testing.T) {
		_, ok := ApplySearchReplaceBlocks("original", nil)
		if ok {
			t.Fatal("expected false for nil blocks")
		}
	})
}

// TestAmbiguousSnippetFailsAtParsing replicates the exact live /build failure:
// the LLM outputs a 3-line raw code snippet (no @@, no SEARCH/REPLACE markers)
// for a 100-line target file. The parser MUST reject at parse time with
// ErrInvalidPatchFormat, BEFORE any filesystem write or truncation check.
func TestAmbiguousSnippetFailsAtParsing(t *testing.T) {
	hundredLines := "package main\n\n"
	for i := 0; i < 97; i++ {
		hundredLines += fmt.Sprintf("// line %d\n", i)
	}
	hundredLines += "func main() {}\n"

	snippet := "func main() {\n\tfmt.Println(\"hello\")\n}\n"

	patch := &Patch{
		File:     "cmd/api/main.go",
		Original: hundredLines,
		Modified: snippet,
	}

	pm := NewPatchManager(t.TempDir())
	pm.SetGuardrail(nil)
	pm.SetAuthorization(testAuth())

	err := pm.Apply(patch)
	if err == nil {
		t.Fatal("expected patch to be rejected — ambiguous snippet MUST NOT overwrite 100-line file")
	}
	if !errors.Is(err, ErrInvalidPatchFormat) {
		t.Fatalf("expected ErrInvalidPatchFormat, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ambiguous snippet without SEARCH/REPLACE markers") {
		t.Fatalf("expected 'ambiguous snippet' error message, got: %v", err)
	}
}

// TestIsAmbiguousSnippet verifies the IsAmbiguousSnippet helper directly.
func TestIsAmbiguousSnippet(t *testing.T) {
	t.Run("new file is not ambiguous", func(t *testing.T) {
		if IsAmbiguousSnippet("", "content") {
			t.Fatal("expected false for empty original (new file)")
		}
	})

	t.Run("has SEARCH marker is not ambiguous", func(t *testing.T) {
		if IsAmbiguousSnippet("original content", "<<<<<<< SEARCH\nsearch\n=======\nreplace\n>>>>>>>") {
			t.Fatal("expected false when SEARCH marker present")
		}
	})

	t.Run("has @@ header is not ambiguous", func(t *testing.T) {
		if IsAmbiguousSnippet("original content", "@@ -1,3 +1,3 @@") {
			t.Fatal("expected false when @@ header present")
		}
	})

	t.Run("payload >= 80% of original is not ambiguous", func(t *testing.T) {
		original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
		snippet := "package main\n\nfunc main() {\n\tprintln(\"world\")\n}\n"
		if IsAmbiguousSnippet(original, snippet) {
			t.Fatal("expected false when payload >= 80% of original")
		}
	})

	t.Run("small payload without markers IS ambiguous", func(t *testing.T) {
		original := "package main\n\n\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
		snippet := "func main() {\n"
		if !IsAmbiguousSnippet(original, snippet) {
			t.Fatal("expected true for small snippet without markers")
		}
	})
}

// TestApplySearchReplaceBlockIntegration verifies that a SEARCH/REPLACE block
// (METHOD C) is correctly parsed and applied within the full PatchManager.Apply
// pipeline — it should succeed where the raw snippet was rejected.
func TestApplySearchReplaceBlockIntegration(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	searchReplaceContent := "<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"world\")\n>>>>>>>"

	patch := &Patch{
		File:     "cmd/api/main.go",
		Original: original,
		Modified: searchReplaceContent,
	}

	pm := NewPatchManager(t.TempDir())
	pm.SetGuardrail(nil)
	pm.SetAuthorization(testAuth())

	err := pm.Apply(patch)
	if err != nil {
		t.Fatalf("expected SEARCH/REPLACE block to succeed, got: %v", err)
	}
}

// TestApplySearchReplaceBlockInFencedBlock verifies that SEARCH/REPLACE blocks
// work when wrapped inside a ```go:path fence (the standard LLM output format).
func TestApplySearchReplaceBlockInFencedBlock(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	// LLM output as it would appear in a ```go:cmd/api/main.go block
	fencedContent := "package main\n\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"world\")\n>>>>>>>\n"

	patch := &Patch{
		File:     "cmd/api/main.go",
		Original: original,
		Modified: fencedContent,
	}

	pm := NewPatchManager(t.TempDir())
	pm.SetGuardrail(nil)
	pm.SetAuthorization(testAuth())

	err := pm.Apply(patch)
	if err != nil {
		t.Fatalf("expected fenced SEARCH/REPLACE block to succeed, got: %v", err)
	}
}

// TestExtractDiffFromLLMOutput_MarkdownFencedDiff verifies that ExtractDiffFromLLMOutput
// correctly extracts a unified diff wrapped in a ```diff markdown code fence.
func TestExtractDiffFromLLMOutput_MarkdownFencedDiff(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	raw := "```diff\ndiff --git a/cmd/main.go b/cmd/main.go\n--- a/cmd/main.go\n+++ b/cmd/main.go\n@@ -1,4 +1,4 @@\n package main\n \n func main() {\n-\tprintln(\"hello\")\n+\tprintln(\"world\")\n }\n```"

	modified, found := ExtractDiffFromLLMOutput(raw, original, "change hello to world")
	if !found {
		t.Fatal("expected to find a diff in markdown-fenced output")
	}
	if modified == original {
		t.Fatal("expected modified content to differ from original")
	}
	if !strings.Contains(modified, "println(\"world\")") {
		t.Fatalf("expected modified content to contain println(\"world\"), got:\n%s", modified)
	}
}

// TestExtractDiffFromLLMOutput_ConversationalDiff verifies that ExtractDiffFromLLMOutput
// recovers a unified diff embedded in conversational text from a cloud model.
func TestExtractDiffFromLLMOutput_ConversationalDiff(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	raw := "Here is the diff you requested:\n\ndiff --git a/cmd/main.go b/cmd/main.go\n--- a/cmd/main.go\n+++ b/cmd/main.go\n@@ -1,4 +1,4 @@\n package main\n \n func main() {\n-\tprintln(\"hello\")\n+\tprintln(\"world\")\n }\n\nLet me know if you need anything else!"

	modified, found := ExtractDiffFromLLMOutput(raw, original, "rename hello to world")
	if !found {
		t.Fatal("expected to find a diff in conversational output")
	}
	if modified == original {
		t.Fatal("expected modified content to differ from original")
	}
	if !strings.Contains(modified, "println(\"world\")") {
		t.Fatalf("expected modified content to contain println(\"world\"), got:\n%s", modified)
	}
}

// TestExtractDiffFromLLMOutput_NoDiffHeaders verifies that ExtractDiffFromLLMOutput
// returns false when the output contains no diff markers at all (e.g., pure prose)
// and the description doesn't match any fuzzy replacement pattern.
func TestExtractDiffFromLLMOutput_NoDiffHeaders(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	raw := "I have made the following change to the file:\n\nThe println statement now says world instead of hello."

	_, found := ExtractDiffFromLLMOutput(raw, original, "some unrelated description with no patterns")
	if found {
		t.Fatal("expected no diff found for plain prose output with no matching patterns")
	}
}

// TestExtractDiffFromLLMOutput_FuzzyStringReplace verifies the fuzzy string
// replacement fallback for single-file hotfixes when no diff markers exist but
// the description matches a known pattern like rename 'old' to 'new'.
func TestExtractDiffFromLLMOutput_FuzzyStringReplace(t *testing.T) {
	original := "Copyright (c) 2023 Jane Doe\n\nMIT License\n"

	raw := "I have made the following change to the file."

	modified, found := ExtractDiffFromLLMOutput(raw, original, "rename 'Jane Doe' to 'Mashashi'")
	if !found {
		t.Fatal("expected fuzzy string replacement fallback to match")
	}
	if modified == original {
		t.Fatal("expected modified content to differ from original")
	}
	if !strings.Contains(modified, "Mashashi") {
		t.Fatalf("expected modified content to contain 'Mashashi', got:\n%s", modified)
	}
	if strings.Contains(modified, "Jane Doe") {
		t.Fatal("expected 'Jane Doe' to be replaced, but it's still in the output")
	}
}

// TestExtractDiffFromLLMOutput_DiffWithoutDiffGit verifies that ExtractDiffFromLLMOutput
// handles diffs that omit the diff --git header and only have --- a/ and +++ b/ markers.
func TestExtractDiffFromLLMOutput_DiffWithoutDiffGit(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	raw := "--- a/cmd/main.go\n+++ b/cmd/main.go\n@@ -3,3 +3,3 @@ func main() {\n \tprintln(\"hello\")\n-\tprintln(\"hello\")\n+\tprintln(\"world\")\n"

	modified, found := ExtractDiffFromLLMOutput(raw, original, "change hello to world")
	if !found {
		t.Fatal("expected to find a diff in output without diff --git header")
	}
	if modified == original {
		t.Fatal("expected modified content to differ from original")
	}
	if !strings.Contains(modified, "println(\"world\")") {
		t.Fatalf("expected modified content to contain println(\"world\"), got:\n%s", modified)
	}
}

// TestApplyFuzzyStringReplace_Rename verifies that ApplyFuzzyStringReplace
// correctly handles rename patterns like rename 'old' to 'new'.
func TestApplyFuzzyStringReplace_Rename(t *testing.T) {
	original := "Copyright (c) 2023 Jane Doe\n\nMIT License\n"

	modified, ok := ApplyFuzzyStringReplace(original, "rename 'Jane Doe' to 'Mashashi'", "LICENSE")
	if !ok {
		t.Fatal("expected fuzzy string replacement to succeed")
	}
	if !strings.Contains(modified, "Mashashi") {
		t.Fatalf("expected 'Mashashi' in output, got:\n%s", modified)
	}
	if strings.Contains(modified, "Jane Doe") {
		t.Fatal("expected 'Jane Doe' to be replaced")
	}
}

// TestApplyFuzzyStringReplace_Change verifies that ApplyFuzzyStringReplace
// correctly handles change patterns like change 'old' to 'new'
// via the generic fallback path (no targetFile set).
func TestApplyFuzzyStringReplace_Change(t *testing.T) {
	// Test a case that works: change '1.0.0' to '2.0.0'
	// without a targetFile (generic fallback path).
	original := "1.0.0\n"
	modified, ok := ApplyFuzzyStringReplace(original, "change 1.0.0 to 2.0.0", "")
	if !ok {
		t.Fatal("expected fuzzy string replacement to succeed for 'change 1.0.0 to 2.0.0'")
	}
	if !strings.Contains(modified, "2.0.0") {
		t.Fatalf("expected '2.0.0' in output, got:\n%s", modified)
	}
}

// TestApplyFuzzyStringReplace_NoMatch verifies that ApplyFuzzyStringReplace
// returns false when the target string is not found in the original content.
func TestApplyFuzzyStringReplace_NoMatch(t *testing.T) {
	original := "package main\n"

	_, ok := ApplyFuzzyStringReplace(original, "rename 'foo' to 'bar'", "main.go")
	if ok {
		t.Fatal("expected fuzzy string replacement to fail when target not found")
	}
}

// apache2License is a full Apache 2.0 license text used to verify
// that ApplyContextAwareFuzzyReplace only modifies the copyright
// holder line and leaves all other sections (1–4) untouched.
const apache2License = `                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear.

   Copyright (c) 2026 Toho
   All Rights Reserved.

   Unless required by applicable law or agreed to in writing, Software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
`

// TestApplyContextAwareFuzzyReplace_Apache20License verifies that the
// function ONLY changes the copyright holder name ("Toho" → "Hashirama")
// in the copyright anchor line and leaves every other line — including
// the definitions in Sections 1–4 — completely untouched.
func TestApplyContextAwareFuzzyReplace_Apache20License(t *testing.T) {
	modified, ok := ApplyContextAwareFuzzyReplace(apache2License, "rename author in @LICENSE to 'Hashirama'", "LICENSE")
	if !ok {
		t.Fatal("expected ApplyContextAwareFuzzyReplace to succeed on Apache 2.0 license")
	}

	// The copyright line must have the new name.
	if !strings.Contains(modified, "Copyright (c) 2026 Hashirama") {
		t.Errorf("expected copyright line to contain 'Hashirama', got:\n%s", modified)
	}

	// The old name must be gone from the copyright line.
	if strings.Contains(modified, "Copyright (c) 2026 Toho") {
		t.Error("expected old copyright holder 'Toho' to be replaced")
	}

	// Sections 1–4 definitions must be preserved intact.
	if !strings.Contains(modified, "TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION") {
		t.Error("expected Section 1 heading to be preserved")
	}
	if !strings.Contains(modified, "Grant of Copyright License") {
		t.Error("expected Section 2 heading to be preserved")
	}
	if !strings.Contains(modified, "Grant of Patent License") {
		t.Error("expected Section 3 heading to be preserved")
	}
	if !strings.Contains(modified, "Redistribution") {
		t.Error("expected Section 4 heading to be preserved")
	}

	// The word "Toho" must only appear in the old copyright line (which was replaced)
	// and nowhere else in the license body.
	lines := strings.Split(modified, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Toho") {
			t.Errorf("found unreplaced 'Toho' on line %d: %s", i+1, line)
		}
	}

	// "AS IS" and other structural text must be preserved.
	if !strings.Contains(modified, `distributed on an "AS IS" BASIS`) {
		t.Error("expected general body text 'AS IS' to be preserved intact")
	}
}

// TestApplyContextAwareFuzzyReplace_NoAnchorLine returns false when the
// file contains no copyright or author anchor lines, avoiding unsafe
// global string replacements across general body text.
func TestApplyContextAwareFuzzyReplace_NoAnchorLine(t *testing.T) {
	content := "You may obtain a copy of the software at\nhttp://example.com\nAll rights reserved.\n"
	_, ok := ApplyContextAwareFuzzyReplace(content, "rename author to Hashirama", "LICENSE")
	if ok {
		t.Error("expected false when no copyright/author anchor line is present")
	}
}

// TestApplyContextAwareFuzzyReplace_AuthorLabel verifies replacement
// in an explicit "Author: NAME" anchor line.
func TestApplyContextAwareFuzzyReplace_AuthorLabel(t *testing.T) {
	content := "Author: Toho\nLicense: MIT\n"
	modified, ok := ApplyContextAwareFuzzyReplace(content, "change author to Hashirama", "LICENSE")
	if !ok {
		t.Fatal("expected success for Author: annotation")
	}
	if !strings.Contains(modified, "Author: Hashirama") {
		t.Errorf("expected 'Author: Hashirama', got:\n%s", modified)
	}
	if strings.Contains(modified, "Toho") {
		t.Error("expected old name 'Toho' to be removed")
	}
}

// TestApplyContextAwareFuzzyReplace_AtAuthor verifies replacement
// in an "@author NAME" annotation line.
func TestApplyContextAwareFuzzyReplace_AtAuthor(t *testing.T) {
	content := "SPDX-FileCopyrightText: 2026 Toho\n@author Toho\n"
	modified, ok := ApplyContextAwareFuzzyReplace(content, "update author to Hashirama", "LICENSE")
	if !ok {
		t.Fatal("expected success for @author annotation")
	}
	lines := strings.Split(modified, "\n")
	foundHashirama := false
	for _, line := range lines {
		if strings.Contains(line, "@author") && strings.Contains(line, "Hashirama") {
			foundHashirama = true
		}
	}
	if !foundHashirama {
		t.Errorf("expected '@author Hashirama' in output, got:\n%s", modified)
	}
}

func TestApplySearchReplace_WhitespaceTolerance(t *testing.T) {
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"

	t.Run("exact substring match", func(t *testing.T) {
		got, ok := ApplySearchReplace(original, "\tprintln(\"hello\")", "\tprintln(\"hi\")")
		if !ok {
			t.Fatal("expected exact match to succeed")
		}
		if !strings.Contains(got, "\tprintln(\"hi\")") {
			t.Fatalf("expected replacement present, got:\n%s", got)
		}
	})

	t.Run("indentation drift tolerated via trim-normalized match", func(t *testing.T) {
		// The search block uses 2-space indentation while the file uses tabs.
		// The match must succeed even though the indentation differs.
		search := "  println(\"hello\")"
		replace := "  println(\"hi\")"
		got, ok := ApplySearchReplace(original, search, replace)
		if !ok {
			t.Fatal("expected whitespace-normalized match to succeed")
		}
		if !strings.Contains(got, "println(\"hi\")") {
			t.Fatalf("expected replacement present, got:\n%s", got)
		}
	})

	t.Run("blank line equivalence", func(t *testing.T) {
		// Search block with a tab-only blank line matches the empty line.
		search := "package main\n\t\nfunc main() {"
		replace := "package main\n\t\nfunc newFn() {"
		got, ok := ApplySearchReplace(original, search, replace)
		if !ok {
			t.Fatal("expected blank-line equivalence match to succeed")
		}
		if !strings.Contains(got, "func newFn() {") {
			t.Fatalf("expected replacement present, got:\n%s", got)
		}
	})
}

func TestApplySearchReplace_NoMatch(t *testing.T) {
	original := "alpha\nbeta\ngamma\n"
	got, ok := ApplySearchReplace(original, "delta", "epsilon")
	if ok {
		t.Fatal("expected false when search text is absent")
	}
	if got != original {
		t.Fatalf("expected original unchanged on no-match, got:\n%s", got)
	}
}

func TestIsPatchArtifactContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"search header", "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>\n", true},
		{"file create header", "<<<<<<< FILE_CREATE: main.go\nfoo\n>>>>>>> END_FILE\n", true},
		{"stray conflict marker", "a\n<<<<<<<\nb\n>>>>>>>\nc\n", true},
		{"unified diff hunk", "@@ -1,3 +1,3 @@\n context\n", true},
		{"unified diff headers", "--- a/foo\n+++ b/foo\n", true},
		{"real file content", "package main\n\nfunc main() {}\n", false},
		{"html content", "<!DOCTYPE html>\n<div>ok</div>\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPatchArtifactContent(tc.content); got != tc.want {
				t.Fatalf("isPatchArtifactContent(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestResolvePatchContent_SEARCHMismatchForcedFallback(t *testing.T) {
	pm := NewPatchManager(t.TempDir())
	original := "line one\nline two\n"
	diffInput := "<<<<<<< SEARCH\nthis text does not exist\n=======\nreplacement\n>>>>>>>\n"
	resolved, err := pm.resolvePatchContent(original, diffInput, &Patch{File: "target.txt"})
	if err != nil {
		t.Fatalf("expected SEARCH mismatch on a small file to resolve via forced full-content fallback, got: %v", err)
	}
	if resolved != "replacement" {
		t.Fatalf("expected REPLACE payload as full content, got: %q", resolved)
	}
}

func TestApply_FreshReadStaleOriginalWins(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.html")
	if err := os.WriteFile(file, []byte("<div>alpha</div>\n<div>beta</div>\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Caller captured a STALE original that no longer matches disk. The
	// SEARCH block only matches the current disk content. Apply must re-read
	// the file and succeed instead of failing with a hunk mismatch.
	staleOriginal := "<div>old</div>\n<div>old</div>\n"
	patch := &Patch{
		ID:       "fresh-read-test",
		File:     "index.html",
		Original: staleOriginal,
		Modified: "<<<<<<< SEARCH\n<div>beta</div>\n=======\n<div>beta-edited</div>\n>>>>>>>\n",
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply failed on fresh read: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "<div>beta-edited</div>") {
		t.Fatalf("expected fresh-read content to be patched, got:\n%s", got)
	}
	if strings.Contains(got, "<div>old</div>") {
		t.Fatalf("stale original leaked into result — fresh read did not win, got:\n%s", got)
	}
	if !strings.Contains(got, "<div>alpha</div>") {
		t.Fatalf("expected sibling content preserved, got:\n%s", got)
	}
}

func TestApply_SEARCHMismatchForcedFullContentFallback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "target.txt")
	original := "hello\nworld\n"
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// SEARCH context does not match the file at all, but the REPLACE payload
	// is the model's full-content answer. The file is ≤ 50KB, so Apply MUST
	// succeed by writing the replacement payload as full content instead of
	// returning "patch hunk does not match file content".
	patch := &Patch{
		ID:       "forced-fallback-test",
		File:     "target.txt",
		Original: original,
		Modified: "<<<<<<< SEARCH\ndoes not exist\n=======\nreplacement\n>>>>>>>\n",
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("expected Apply to succeed via forced full-content fallback on a small file, got: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "replacement" {
		t.Fatalf("expected REPLACE payload written as full content, got:\n%s", got)
	}
}

func TestApply_SmallFileFullContentFallback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.html")
	original := "line one\nline two\nline three\n"
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// Unified diff whose hunk anchors are out of bounds and context cannot
	// match. The file is small, so the resolver falls back to a full-content
	// write of the sanitized diff — never writing raw @@ / - / + markers.
	patch := &Patch{
		ID:       "fallback-test",
		File:     "index.html",
		Original: original,
		Modified: "@@ -99,3 +99,3 @@\n line one\n-line two\n+line two edited\n",
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply failed on small-file fallback: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "@@") || strings.Contains(got, "\n-line") {
		t.Fatalf("raw diff markers leaked into file, got:\n%s", got)
	}
	if !strings.Contains(got, "line two edited") {
		t.Fatalf("expected sanitized full-content write, got:\n%s", got)
	}
	if !strings.Contains(got, "line one") {
		t.Fatalf("expected context line preserved, got:\n%s", got)
	}
}

func TestApplySearchReplace_CRLFNormalization(t *testing.T) {
	// File checked out with Windows line endings; SEARCH block uses LF.
	original := "line one\r\nline two\r\nline three\r\n"
	search := "line two"
	replace := "line two edited"
	got, ok := ApplySearchReplace(original, search, replace)
	if !ok {
		t.Fatal("expected CRLF/LF-tolerant match to succeed")
	}
	if !strings.Contains(got, "line two edited") {
		t.Fatalf("expected replacement present, got:\n%q", got)
	}
	if !strings.Contains(got, "line one\r\n") {
		t.Fatalf("expected untouched CRLF lines preserved, got:\n%q", got)
	}

	// Multi-line search across a CRLF file using LF search lines.
	original2 := "<!DOCTYPE html>\r\n<html>\r\n<body>\r\n  <p>old</p>\r\n</body>\r\n</html>\r\n"
	search2 := "<body>\n  <p>old</p>"
	replace2 := "<body>\n  <p>new</p>"
	got2, ok2 := ApplySearchReplace(original2, search2, replace2)
	if !ok2 {
		t.Fatal("expected multi-line CRLF-tolerant match to succeed")
	}
	if !strings.Contains(got2, "<p>new</p>") {
		t.Fatalf("expected replacement present, got:\n%q", got2)
	}
	if !strings.Contains(got2, "<!DOCTYPE html>\r\n") {
		t.Fatalf("expected untouched CRLF prefix preserved, got:\n%q", got2)
	}
}

func TestApply_IndexHTML_LeadingSpaceMismatch_FuzzyMatch(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.html")
	original := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>My Page</title>
</head>
<body>
  <div class="container">
    <p>Hello</p>
  </div>
  <div class="footer">
    <p>Footer</p>
  </div>
</body>
</html>
`
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// The model's SEARCH block uses 4-space indentation while the file uses
	// 2-space indentation. Trim-normalized fuzzy matching must still locate
	// the block and apply the edit without any error.
	patch := &Patch{
		ID:   "index-fuzzy",
		File: "index.html",
		Modified: `<<<<<<< SEARCH
    <div class="container">
      <p>Hello</p>
    </div>
=======
    <div class="container">
      <p>Hello World</p>
    </div>
>>>>>>>
`,
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("index.html fuzzy patch failed: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "<p>Hello World</p>") {
		t.Fatalf("expected fuzzy-matched replacement applied, got:\n%s", got)
	}
	if !strings.Contains(got, `<div class="footer">`) {
		t.Fatalf("expected surrounding content preserved, got:\n%s", got)
	}
	if strings.Contains(got, ">>>>>>>") || strings.Contains(got, "<<<<<<<") || strings.Contains(got, "=======") {
		t.Fatalf("SEARCH/REPLACE markers leaked into file, got:\n%s", got)
	}
}

func TestApply_IndexHTML_SEARCHMismatch_FullContentFallback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.html")
	original := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>My Page</title>
</head>
<body>
  <div class="container">
    <p>Hello</p>
  </div>
</body>
</html>
`
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// The SEARCH context does not exist in the file at all, but the REPLACE
	// payload is a complete valid HTML document. The file is ≤ 50KB, so the
	// circuit breaker must proceed to a full-content write instead of
	// returning "patch hunk does not match file content".
	replacement := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Rebuilt Page</title>
</head>
<body>
  <main>
    <h1>Rebuilt</h1>
  </main>
</body>
</html>
`
	patch := &Patch{
		ID:   "index-full",
		File: "index.html",
		Modified: "<<<<<<< SEARCH\nTHIS SNIPPET IS NOT PRESENT IN THE FILE\nANYWHERE AT ALL\n=======\n" +
			replacement + "\n>>>>>>>\n",
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("index.html full-content fallback failed: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "<title>Rebuilt Page</title>") {
		t.Fatalf("expected full-content replacement applied, got:\n%s", got)
	}
	if strings.Contains(got, "THIS SNIPPET IS NOT PRESENT") {
		t.Fatalf("search snippet leaked into file, got:\n%s", got)
	}
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("SEARCH/REPLACE markers leaked into file, got:\n%s", got)
	}
}

func TestApply_IndexHTML_SEARCHMismatch_ForcedFullContentFallback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "index.html")

	// ~2KB index.html on disk (well under MaxFullContentRewriteBytes).
	template := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>My Page</title>
</head>
<body>
  <div class="hero">
    <h1>Welcome</h1>
    <p>Original content block that the model never saw.</p>
  </div>
</body>
</html>
`
	original := strings.Repeat(template, 9)
	if len(original) < 2000 || len(original) > MaxFullContentRewriteBytes {
		t.Fatalf("test fixture size %d bytes is outside the intended 2KB range", len(original))
	}
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// The REPLACE payload is a complete, valid HTML document. It deliberately
	// contains a line starting with "@@" — without SEARCH/REPLACE precedence
	// over unified-diff detection this exact payload is misrouted into the
	// unified-diff parser and dies with "patch hunk does not match file
	// content — target code context may have changed". The SEARCH block is
	// completely absent from the file, so ApplySearchReplaceBlocks must fail
	// and the forced full-content fallback must take over.
	replacement := strings.Repeat(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Rebuilt Page</title>
</head>
<body>
@@ custom-zone @@
  <main>
    <h1>Rebuilt</h1>
    <p>Full-content replacement payload.</p>
  </main>
</body>
</html>
`, 8)
	patch := &Patch{
		ID:   "index-forced",
		File: "index.html",
		Modified: "<<<<<<< SEARCH\nTHIS SNIPPET IS NOT PRESENT IN THE FILE\nANYWHERE AT ALL\n=======\n" +
			replacement + "\n>>>>>>>\n",
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("index.html forced full-content fallback failed: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "<title>Rebuilt Page</title>") {
		t.Fatalf("expected full-content replacement applied, got:\n%s", got)
	}
	if strings.Contains(got, "THIS SNIPPET IS NOT PRESENT") {
		t.Fatalf("search snippet leaked into file, got:\n%s", got)
	}
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("SEARCH/REPLACE markers leaked into file, got:\n%s", got)
	}
}

func TestApply_ErrorWrappedOnce(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "target.txt")
	original := "line a\nline b\nline c\n"
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// Two SEARCH/REPLACE blocks, both mismatched → no full-content fallback
	// (multi-block is ambiguous) → must fail with exactly ONE "apply patch to"
	// prefix, never "apply patch to X: apply patch to X:...".
	patch := &Patch{
		ID:   "double-wrap",
		File: "target.txt",
		Modified: `<<<<<<< SEARCH
missing one
=======
replacement one
>>>>>>>
<<<<<<< SEARCH
missing two
=======
replacement two
>>>>>>>
`,
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	err := pm.Apply(patch)
	if err == nil {
		t.Fatal("expected Apply to fail on multi-block SEARCH mismatch")
	}
	if n := strings.Count(err.Error(), "apply patch to"); n != 1 {
		t.Fatalf("expected 'apply patch to' exactly once, got %d: %v", n, err)
	}
}
