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

// ── BOUNDARY-2 CONTRACT MATCH (constrained models) ──────────────────────────
//
// A constrained (free-tier / low-ceiling) model is forced onto the bounded
// SEARCH/REPLACE contract for every existing target. That contract fits ANY
// output budget by construction, so Boundary 2 must not judge it as an
// infeasible full rewrite. These tests pin both directions of the contract:
//
//   - constrained model  → the guard is SKIPPED and a bounded patch dispatches;
//   - unconstrained model → the guard still REFUSES an infeasible full rewrite.

// largeTargetForPreflight returns a deterministic HTML target whose full-rewrite
// estimate (bytes/4 × 3) exceeds the 2048-token budget used below.
func largeTargetForPreflight() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>constrained preflight</title></head>\n<body>\n")
	b.WriteString("<h1 id=\"preflight-anchor\">anchor line unique to the head</h1>\n")
	for i := 0; b.Len() < 8000; i++ {
		fmt.Fprintf(&b, "<p class=\"row-%d\">filler content row %d</p>\n", i, i)
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// replaceBlockProfile is the ordinary targeted-mutation artifact contract for an
// existing file: NOT patch-only by itself, so Boundary 2 evaluates the full
// rewrite unless the model is constrained.
func replaceBlockProfile() *strategy.ExecutionStrategyProfile {
	return &strategy.ExecutionStrategyProfile{
		Strategy:        strategy.TargetedMutation,
		ModelRequired:   true,
		StrategyReason:  "targeted mutation on an existing file",
		Artifact:        strategy.ArtifactContract{Kind: "replace_block", Bounded: true},
		MaxOutputTokens: 2048,
	}
}

const constrainedPreflightPatch = `<<<<<<< SEARCH
<h1 id="preflight-anchor">anchor line unique to the head</h1>
=======
<h1 id="preflight-anchor">anchor line unique to the head (redesigned)</h1>
>>>>>>> REPLACE`

// TestConstrainedModelSkipsInfeasibleFullRewritePreflight proves the fix: a
// constrained model on a large existing target is dispatched as a bounded patch
// rather than refused at Boundary 2 for a full rewrite it would never attempt.
func TestConstrainedModelSkipsInfeasibleFullRewritePreflight(t *testing.T) {
	root := t.TempDir()
	original := largeTargetForPreflight()
	writeTarget(t, root, "index.html", original)

	// Sanity: the fixture IS over budget under the full-rewrite accounting.
	if est := (len(original) / 4) * FullRewriteTokenMultiplier; est <= 2048 {
		t.Fatalf("fixture full-rewrite estimate = %d, want > 2048", est)
	}

	mock := &mockProvider{responses: []*ai.Response{{
		Content: constrainedPreflightPatch,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 30, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          "redesign this page",
		Targets:         []string{"index.html"},
		Model:           "cohere/north-mini-code:free",
		Strategy:        replaceBlockProfile(),
		MaxOutputTokens: 2048,
	})
	if err != nil {
		t.Fatalf("Execute: %v (outcome=%v)", err, res.Proof.Outcome)
	}
	if errors.Is(err, ErrPreflightInfeasible) || res.Proof.Outcome == OutcomePreflightInfeasible {
		t.Fatal("a constrained model was refused as an infeasible full rewrite")
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (the bounded patch dispatched)", mock.calls())
	}
	if res.PendingPatchID == "" {
		t.Fatalf("bounded patch was not staged: outcome=%v", res.Proof.Outcome)
	}
	// Wire truth: the model was asked for the bounded patch contract.
	if sys := mock.requests[0].System; !strings.Contains(sys, "bounded patch engine") {
		t.Fatalf("constrained dispatch did not use the bounded-patch contract: %.160q", sys)
	}
	if mock.requests[0].MaxTokens != ConstrainedMaxTokens {
		t.Fatalf("max_tokens = %d, want %d (constrained ceiling)", mock.requests[0].MaxTokens, ConstrainedMaxTokens)
	}
}

// TestUnconstrainedModelStillRefusesInfeasibleFullRewrite is the control: the
// guard is NOT weakened for models that would genuinely attempt the full
// rewrite. The same target and budget still park at PREFLIGHT_INFEASIBLE.
func TestUnconstrainedModelStillRefusesInfeasibleFullRewrite(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", largeTargetForPreflight())

	mock := &mockProvider{responses: []*ai.Response{{
		Content: constrainedPreflightPatch,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 30, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          "redesign this page",
		Targets:         []string{"index.html"},
		Model:           "some-vendor/model-large",
		Strategy:        replaceBlockProfile(),
		MaxOutputTokens: 2048,
	})
	if !errors.Is(err, ErrPreflightInfeasible) {
		t.Fatalf("err = %v, want ErrPreflightInfeasible", err)
	}
	if res.Proof.Outcome != OutcomePreflightInfeasible {
		t.Fatalf("outcome = %v, want %v", res.Proof.Outcome, OutcomePreflightInfeasible)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (zero requests cross Boundary 2 on refusal)", mock.calls())
	}
}
