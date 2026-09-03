package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// searchReplaceProfile is the recovery artifact contract the adapter emits for
// a bounded_patch recovery: a strict structured-patch output representation.
func searchReplaceProfile() *strategy.ExecutionStrategyProfile {
	return &strategy.ExecutionStrategyProfile{
		Strategy:        strategy.TargetedMutation,
		ModelRequired:   true,
		StrategyReason:  "recovery: bounded_patch after truncation",
		Artifact:        strategy.ArtifactContract{Kind: "search_replace", Bounded: true},
		MaxOutputTokens: 1024,
	}
}

// TestSearchReplaceContractRejectsFullFileOutput pins the artifact boundary:
// under the search_replace contract the executor structurally cannot accept a
// complete-file response — it must be rejected as retryable before any
// approval or mutation surface, and the wire contract sent to the model must
// never offer the full-file option.
func TestSearchReplaceContractRejectsFullFileOutput(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "foo\nQUX\nbaz\n", // a COMPLETE replacement file, no patch markers
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if !errors.Is(err, ErrArtifactRetryableRejected) {
		t.Fatalf("err = %v, want ErrArtifactRetryableRejected", err)
	}
	if res != nil && res.PendingPatchID != "" {
		t.Fatalf("full-file output staged a proposal (patch %s) — bounded contract violated", res.PendingPatchID)
	}
	if res == nil || res.Proof.Outcome != OutcomeArtifactRetryableRejected {
		t.Fatalf("proof outcome = %v, want %s", res.Proof.Outcome, OutcomeArtifactRetryableRejected)
	}
	// The billed invocation survives the rejection.
	if len(res.Proof.ModelInvocations) != 1 || !res.Proof.ModelInvocations[0].Known {
		t.Fatalf("model invocations = %+v, want one known invocation", res.Proof.ModelInvocations)
	}
	// Wire truth: the strict prompt crossed, not the tolerant one.
	reqs := mock.requests
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if strings.Contains(strings.ToLower(reqs[0].System), "full modified file") {
		t.Fatalf("patch-only system prompt offers the full-file option: %q", reqs[0].System)
	}
	if !strings.Contains(reqs[0].System, "<<<<<<< SEARCH") {
		t.Fatalf("patch-only system prompt missing SEARCH/REPLACE requirement: %q", reqs[0].System)
	}
	if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("filesystem changed on a rejected artifact: %q", got)
	}
}

// TestSearchReplaceContractStagesBoundedPatch proves the same contract accepts
// a genuine SEARCH/REPLACE block and stages it as an applicable patch.
func TestSearchReplaceContractStagesBoundedPatch(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 12, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          "change bar to qux",
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("bounded patch was not staged at the approval gate")
	}
	if res.Content != "foo\nqux\nbaz\n" {
		t.Fatalf("staged content = %q, want patched file", res.Content)
	}
	if reqs := mock.requests; len(reqs) != 1 || strings.Contains(strings.ToLower(reqs[0].System), "full modified file") {
		t.Fatalf("wire contract = %+v, want strict patch-only prompt", reqs[0].System)
	}

	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("approve outcome = %s, want changed", apr.Proof.Outcome)
	}
	if got := mustRead(t, root, "note.txt"); got != "foo\nqux\nbaz\n" {
		t.Fatalf("apply did not change the target: %q", got)
	}
}

// TestFullArtifactAttemptStillTolerant guards backward compatibility: the
// INITIAL (non-recovery) mutation attempt keeps the tolerant contract in which
// a full-file replacement is accepted.
func TestFullArtifactAttemptStillTolerant(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "foo\nQUX\nbaz\n", // complete replacement, no markers
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:    "build",
		Prompt:  "change bar to qux",
		Targets: []string{"note.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("initial full-artifact attempt no longer tolerates a full-file replacement")
	}
	if reqs := mock.requests; len(reqs) != 1 || !strings.Contains(reqs[0].System, "The full modified file content") {
		t.Fatalf("initial attempt wire contract = %q, want tolerant mutation prompt", reqs[0].System)
	}
}

// TestExtractBoundedPatch unit-covers the structured-only extraction boundary.
func TestExtractBoundedPatch(t *testing.T) {
	orig := "foo\nbar\nbaz\n"

	t.Run("search_replace_applies", func(t *testing.T) {
		got, ok := ExtractBoundedPatch(orig, "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>")
		if !ok || got != "foo\nqux\nbaz\n" {
			t.Fatalf("got %q ok=%v", got, ok)
		}
	})

	t.Run("fenced_search_replace_applies", func(t *testing.T) {
		raw := "```html\n<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>\n```"
		got, ok := ExtractBoundedPatch(orig, raw)
		if !ok || got != "foo\nqux\nbaz\n" {
			t.Fatalf("got %q ok=%v", got, ok)
		}
	})

	t.Run("unified_diff_applies", func(t *testing.T) {
		raw := "--- a/note.txt\n+++ b/note.txt\n@@ -1,3 +1,3 @@\n foo\n-bar\n+qux\n baz\n"
		got, ok := ExtractBoundedPatch(orig, raw)
		if !ok || got != "foo\nqux\nbaz\n" {
			t.Fatalf("got %q ok=%v", got, ok)
		}
	})

	t.Run("full_file_rejected", func(t *testing.T) {
		if _, ok := ExtractBoundedPatch(orig, "foo\nQUX\nbaz\n"); ok {
			t.Fatal("full-file output passed the bounded patch boundary")
		}
	})

	t.Run("unmatchable_search_rejected", func(t *testing.T) {
		if _, ok := ExtractBoundedPatch(orig, "<<<<<<< SEARCH\nnope\n=======\nqux\n>>>>>>>"); ok {
			t.Fatal("unmatchable SEARCH block passed the boundary")
		}
	})

	t.Run("empty_rejected", func(t *testing.T) {
		if _, ok := ExtractBoundedPatch(orig, "   "); ok {
			t.Fatal("empty output passed the boundary")
		}
	})
}

// TestTruncatedBoundedPatchIsRejected pins the artifact boundary against
// output-ceiling damage: a SEARCH/REPLACE block severed before its terminator
// must be rejected as retryable BEFORE any approval surface, and a VALID patch
// arriving with finish_reason=length must STILL classify as truncated (the
// authoritative finish-reason guarantee is never weakened by the patch path).
func TestTruncatedBoundedPatchIsRejected(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	profile := searchReplaceProfile()

	t.Run("severed_block_rejected", func(t *testing.T) {
		mock := &mockProvider{responses: []*ai.Response{{
			Content: "<<<<<<< SEARCH\nbar\n=======\nQUX and more and more",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 1024, FinishReason: "stop"},
		}}}
		x := phase4Executor(t, root, mock, nil)
		res, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "autonomy", Prompt: "change bar", Targets: []string{"note.txt"},
			Strategy: profile, MaxOutputTokens: 1024,
		})
		if !errors.Is(err, ErrArtifactRetryableRejected) {
			t.Fatalf("err = %v, want retryable rejection", err)
		}
		if res.PendingPatchID != "" {
			t.Fatal("severed patch was staged")
		}
		if got := mustRead(t, root, "note.txt"); got != sampleOriginal {
			t.Fatalf("rejected artifact touched the file: %q", got)
		}
	})

	t.Run("valid_patch_but_length_still_truncated", func(t *testing.T) {
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 1024, FinishReason: "length"},
		}}}
		x := phase4Executor(t, root, mock, nil)
		res, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "autonomy", Prompt: "change bar", Targets: []string{"note.txt"},
			Strategy: profile, MaxOutputTokens: 1024,
		})
		if !errors.Is(err, ErrOutputTruncated) {
			t.Fatalf("err = %v, want ErrOutputTruncated", err)
		}
		if res.Proof.Outcome != OutcomeTruncated {
			t.Fatalf("outcome = %s, want truncated", res.Proof.Outcome)
		}
	})

	t.Run("duplicate_anchor_rejected", func(t *testing.T) {
		dup := sampleOriginal + sampleOriginal // every line appears twice now
		writeTarget(t, root, "dup.txt", dup)
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 20, FinishReason: "stop"},
		}}}
		x := phase4Executor(t, root, mock, nil)
		res, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "autonomy", Prompt: "change bar", Targets: []string{"dup.txt"},
			Strategy: profile, MaxOutputTokens: 1024,
		})
		// Ambiguous anchors are now circuit-broken as NonRetryableArtifactError
		// (max 1 API request, no duplicate LLM retry).
		if !errors.Is(err, ErrNonRetryableArtifactError) && !errors.Is(err, ErrArtifactRejected) {
			t.Fatalf("err = %v, want non-retryable rejection for ambiguous anchor (ErrNonRetryableArtifactError or ErrArtifactRejected)", err)
		}
		if res.PendingPatchID != "" {
			t.Fatal("ambiguous-anchor patch was staged")
		}
		// Circuit breaker must also expose DecisionSurface options.
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "line-offset") && !strings.Contains(lower, "full-file") {
			t.Fatalf("error should contain DecisionSurface options [1] line-offset [2] full-file, got: %q", err.Error())
		}
	})
}

// TestBoundedPatchWindowRotation unit-covers the runtime-derived windowing:
// windows are line-aligned, byte-capped, deterministic per attempt, rotate
// across attempts, and a single-line file is byte-clamped.
func TestBoundedPatchWindowRotation(t *testing.T) {
	big := bigIndexHTMLForWindowTest()
	w1 := selectBoundedPatchWindow(big, 1)
	w2 := selectBoundedPatchWindow(big, 2)
	if w1.startLine != 1 || w1.endLine <= w1.startLine {
		t.Fatalf("first window = lines %d-%d", w1.startLine, w1.endLine)
	}
	if len(w1.content) > maxBoundedPatchContextBytes || len(w2.content) > maxBoundedPatchContextBytes {
		t.Fatalf("window exceeds cap: %d / %d", len(w1.content), len(w2.content))
	}
	if w2.startLine == w1.startLine && w2.endLine == w1.endLine {
		t.Fatal("attempt 2 did not rotate to a different window")
	}
	if got := selectBoundedPatchWindow(big, 1); got.content != w1.content {
		t.Fatal("window selection is not deterministic")
	}
	single := strings.Repeat("x", 7780)
	sw := selectBoundedPatchWindow(single, 3)
	if len(sw.content) != maxBoundedPatchContextBytes {
		t.Fatalf("single-line clamp = %d bytes, want %d", len(sw.content), maxBoundedPatchContextBytes)
	}
}

func bigIndexHTMLForWindowTest() string {
	var b strings.Builder
	b.WriteString("<html>\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "<p class=\"w-%d\">window fixture content %d</p>\n", i, i)
	}
	b.WriteString("</html>\n")
	return b.String()
}

// TestBoundedPatchDisablesHiddenReasoning pins the reasoning-control half of
// the bounded contract: reasoning models spend the shared output budget on
// hidden chain-of-thought (live repro: 1024 output tokens, ZERO visible
// content; the gateway ignores reasoning.max_tokens), so the bounded-patch
// invocation MUST disable the hidden reasoning pass while the tolerant
// initial attempt stays untouched.
func TestBoundedPatchDisablesHiddenReasoning(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	t.Run("bounded_attempt_capped", func(t *testing.T) {
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 12, FinishReason: "stop"},
		}}}
		x := phase4Executor(t, root, mock, nil)
		if _, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "autonomy", Prompt: "change bar", Targets: []string{"note.txt"},
			Strategy: searchReplaceProfile(), MaxOutputTokens: 1024,
		}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		r := mock.requests[0].Reasoning
		if r == nil || !r.Disabled {
			t.Fatalf("bounded request reasoning = %+v, want Disabled=true", r)
		}
	})

	t.Run("tolerant_attempt_uncapped", func(t *testing.T) {
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 12, FinishReason: "stop"},
		}}}
		x := phase4Executor(t, root, mock, nil)
		if _, err := x.Execute(context.Background(), ExecuteRequest{
			Mode: "build", Prompt: "change bar", Targets: []string{"note.txt"},
		}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if r := mock.requests[0].Reasoning; !r.IsZero() {
			t.Fatalf("initial attempt reasoning = %+v, want none (unchanged semantics)", r)
		}
	})
}
