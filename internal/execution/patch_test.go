package execution

import (
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
