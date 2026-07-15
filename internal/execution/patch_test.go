package execution

import "testing"

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

func TestApplyUnifiedPatchExpiredContextReturnsError(t *testing.T) {
	diff := "@@ -3,1 +3,1 @@\n func main() {\n-\tprintln(\"old\")\n+\tprintln(\"new\")\n}\n"
	// Mutate the original so the hunk no longer matches anywhere.
	changed := "package main\n\nfunc main() {\n\tprintln(\"different\")\n}\n"
	_, err := applyUnifiedPatch(changed, diff)
	if err == nil {
		t.Fatal("expected an error when target context has changed, got nil")
	}
}
