package execution

import (
	"errors"
	"fmt"
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
		blocks := parseSearchReplaceBlocks(input)
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
		blocks := parseSearchReplaceBlocks(input)
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
		blocks := parseSearchReplaceBlocks("just some random content\nno markers here")
		if len(blocks) != 0 {
			t.Fatalf("expected 0 blocks, got %d", len(blocks))
		}
	})

	t.Run("malformed block missing replace", func(t *testing.T) {
		input := "<<<<<<< SEARCH\nold\n=======\n>>>>>>>"
		blocks := parseSearchReplaceBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		if blocks[0].replace != "" {
			t.Fatalf("expected empty replace, got %q", blocks[0].replace)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		blocks := parseSearchReplaceBlocks("")
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
		result, ok := applySearchReplaceBlockFromBlocks(original, blocks)
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
		result, ok := applySearchReplaceBlockFromBlocks(original, blocks)
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
		_, ok := applySearchReplaceBlockFromBlocks(original, blocks)
		if ok {
			t.Fatal("expected false when search not found")
		}
	})

	t.Run("empty original returns false", func(t *testing.T) {
		_, ok := applySearchReplaceBlockFromBlocks("", []searchReplaceBlock{{search: "x", replace: "y"}})
		if ok {
			t.Fatal("expected false for empty original")
		}
	})

	t.Run("empty blocks returns false", func(t *testing.T) {
		_, ok := applySearchReplaceBlockFromBlocks("original", nil)
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

	err := pm.Apply(patch)
	if err != nil {
		t.Fatalf("expected fenced SEARCH/REPLACE block to succeed, got: %v", err)
	}
}
