package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── ARTIFACT PRE-VALIDATION SANITIZER (Task 2) ──────────────────────────────

// TestSanitizeBoundedPatchResponse_StripsFencesAndProse pins the pre-parsing
// cleanup step: markdown codeblock fences (```html / ```diff / ```) and
// conversational intro/outro text are stripped, embedded SEARCH/REPLACE blocks
// survive verbatim.
func TestSanitizeBoundedPatchResponse_StripsFencesAndProse(t *testing.T) {
	t.Run("fenced_html_block", func(t *testing.T) {
		raw := "```html\n<<<<<<< SEARCH\n<div class=\"old\">\n=======\n<div class=\"new\">\n>>>>>>>\n```"
		want := "<<<<<<< SEARCH\n<div class=\"old\">\n=======\n<div class=\"new\">\n>>>>>>>"
		if got := SanitizeBoundedPatchResponse(raw); got != want {
			t.Fatalf("sanitized = %q, want %q", got, want)
		}
	})

	t.Run("prose_wrapped_block", func(t *testing.T) {
		raw := "Sure! Here is the patch you asked for:\n```diff\n" +
			"<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>\n```\nLet me know if anything else is needed."
		got := SanitizeBoundedPatchResponse(raw)
		if !strings.Contains(got, "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>") {
			t.Fatalf("embedded block lost during sanitization: %q", got)
		}
		if strings.Contains(got, "Sure!") || strings.Contains(got, "```") || strings.Contains(got, "Let me know") {
			t.Fatalf("conversational noise survived sanitization: %q", got)
		}
	})

	t.Run("multiple_blocks_all_kept", func(t *testing.T) {
		raw := "<<<<<<< SEARCH\none\n=======\ntwo\n>>>>>>>\nsome chatter\n<<<<<<< SEARCH\nthree\n=======\nfour\n>>>>>>>"
		got := SanitizeBoundedPatchResponse(raw)
		if strings.Count(got, "<<<<<<< SEARCH") != 2 || strings.Contains(got, "chatter") {
			t.Fatalf("multi-block extraction wrong: %q", got)
		}
	})

	t.Run("fenced_diff_survives", func(t *testing.T) {
		raw := "```diff\n--- a/n.txt\n+++ b/n.txt\n@@ -1 +1 @@\n-a\n+b\n```"
		want := "--- a/n.txt\n+++ b/n.txt\n@@ -1 +1 @@\n-a\n+b"
		if got := SanitizeBoundedPatchResponse(raw); got != want {
			t.Fatalf("sanitized = %q, want %q", got, want)
		}
	})

	t.Run("clean_block_is_identity", func(t *testing.T) {
		raw := "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>"
		if got := SanitizeBoundedPatchResponse(raw); got != raw {
			t.Fatalf("clean input mutated: %q", got)
		}
	})
}

// TestExtractBoundedPatch_NoisyProseResponse proves a prose-wrapped, fenced
// response — the exact non-compliance mode of free-tier small models — now
// passes the strict artifact boundary after sanitization.
func TestExtractBoundedPatch_NoisyProseResponse(t *testing.T) {
	orig := "foo\nbar\nbaz\n"
	raw := "Certainly! Below is the required change:\n\n```html\n" +
		"<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>\n```\n\nThis edit updates line 2."
	got, ok := ExtractBoundedPatch(orig, raw)
	if !ok || got != "foo\nqux\nbaz\n" {
		t.Fatalf("got %q ok=%v, want patched file", got, ok)
	}
}

// ── NO-OP SENTINEL VALIDATION (Task 2.2) ────────────────────────────────────

func TestIsNoOpBoundedPatchResponse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"bare sentinel", "NO_CHANGES_REQUIRED", true},
		{"sentinel with whitespace", "  NO_CHANGES_REQUIRED \n", true},
		{"fenced sentinel", "```\nNO_CHANGES_REQUIRED\n```", true},
		{"sentinel with period", "NO_CHANGES_REQUIRED.", true},
		{"short lead-in sentence", "The assigned slice already matches.\nNO_CHANGES_REQUIRED", true},
		{"plain prose apology", "I could not find anything to change in these lines.", false},
		{"real patch block", "<<<<<<< SEARCH\na\n=======\nb\n>>>>>>>", false},
		{"unified diff", "@@ -1 +1 @@\n-a\n+b", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNoOpBoundedPatchResponse(tc.raw); got != tc.want {
				t.Errorf("IsNoOpBoundedPatchResponse(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestSearchReplaceContractNoOpSentinelSucceeds drives the full executor: a
// bounded-patch invocation whose model answers exactly NO_CHANGES_REQUIRED —
// with an objective carrying no structural directive that contradicts it —
// must converge to no_op_objective_satisfied WITHOUT staging a patch, tripping
// Boundary 3/4 or burning an error — and without touching the filesystem.
func TestSearchReplaceContractNoOpSentinelSucceeds(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "NO_CHANGES_REQUIRED",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 4, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("no-op sentinel must not fail execution, got error: %v", err)
	}
	if res == nil || res.Proof.Outcome != OutcomeNoOpObjectiveSatisfied {
		t.Fatalf("proof outcome = %v, want %s", res.Proof.Outcome, OutcomeNoOpObjectiveSatisfied)
	}
	if res.PendingPatchID != "" {
		t.Fatal("no-op staged a patch at the approval gate")
	}
	if res.Content != "" || res.ArtifactKind != "" {
		t.Fatalf("no-op leaked artifact bytes: kind=%q content=%q", res.ArtifactKind, res.Content)
	}
	// The billed invocation survives so usage stays authoritative.
	if len(res.Proof.ModelInvocations) != 1 || !res.Proof.ModelInvocations[0].Known {
		t.Fatalf("model invocations = %+v, want one known invocation", res.Proof.ModelInvocations)
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("filesystem changed on a no-op success: %q", got)
	}
}

// TestSearchReplaceContractNoOpSentinelWithNoise proves the sentinel still
// converges to no_op_objective_satisfied when the model wraps it in its usual
// fence/prose noise — but a genuine patch inside the same noise is NEVER
// swallowed by the no-op path.
func TestSearchReplaceContractNoOpSentinelWithNoise(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "Nothing to do here.\n```\nNO_CHANGES_REQUIRED\n```\nThanks!",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 9, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "autonomy", Prompt: "change bar to qux", Targets: []string{"note.txt"},
		Strategy: searchReplaceProfile(), MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("no-op with conversational noise must succeed, got: %v", err)
	}
	if res.Proof.Outcome != OutcomeNoOpObjectiveSatisfied {
		t.Fatalf("proof outcome = %s, want %s", res.Proof.Outcome, OutcomeNoOpObjectiveSatisfied)
	}
	if errors.Is(res.Err, ErrArtifactRetryableRejected) {
		t.Fatal("no-op classified as artifact rejection")
	}
}
