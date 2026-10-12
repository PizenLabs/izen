package autonomy

// ── RECOVERY EVIDENCE FOR A CONSTRAINED BOUNDED-PATCH ATTEMPT ───────────────
//
// When a constrained model violates the bounded SEARCH/REPLACE contract, the
// recovery attempt must be a MATERIAL continuation, not a relabelled retry: the
// next request must carry the classified failure and the exact output contract
// the model must satisfy. This drives the REAL Driver → ExecutorAdapter →
// RuntimeExecutor path with a scripted provider.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

const constrainedRecoveryModel = "cohere/north-mini-code:free"

// TestConstrainedBoundedPatchRecoveryCarriesFailedApproach proves that a
// rejected bounded-patch attempt produces a materially-changed continuation:
// the second provider request carries the failed-approach evidence AND the
// SEARCH/REPLACE contract, and a corrected second attempt stages through the
// same canonical validator.
func TestConstrainedBoundedPatchRecoveryCarriesFailedApproach(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	// Attempt 1 violates the contract (unfenced prose/full-file). Attempt 2 is a
	// valid anchored SEARCH/REPLACE patch.
	mock := &mockProvider{responses: []*ai.Response{
		{
			Content: "<!DOCTYPE html><html><body>an unstructured response with no patch envelope</body></html>",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 200, CompletionTokens: 40, FinishReason: "stop"},
		},
		{
			Content: patchForLine(5, "<p class=\"line-5\">recovered line 5</p>"),
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 220, CompletionTokens: 30, FinishReason: "stop"},
		},
	}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	auth := appruntime.NewRuntimeAuthority()
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID(mock.Name()),
		ModelID:    authority.ModelID(constrainedRecoveryModel),
	})
	adapter.SetAuthority(auth)
	d := NewDriver(adapter, bus, WithLoopBounds(generousBounds()))

	if _, err := d.Run(context.Background(), "check this file @index.html and rewrite it"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := mock.recordedRequests()
	if len(reqs) < 2 {
		t.Fatalf("provider requests = %d, want at least 2 (initial + recovery)", len(reqs))
	}
	// Attempt 1 was dispatched under the constrained bounded-patch contract.
	if !strings.Contains(reqs[0].System, "bounded patch engine") {
		t.Fatalf("attempt 1 did not use the bounded-patch contract: %.160q", reqs[0].System)
	}
	// The recovery request carries the failed approach and the required
	// contract — a continuation, not a restart.
	second := reqs[1]
	user := ""
	if len(second.Messages) > 0 {
		user = second.Messages[len(second.Messages)-1].Content
	}
	if !strings.Contains(user, "[FAILED APPROACH]") {
		t.Fatalf("recovery request does not carry the failed approach:\n%s", user)
	}
	if !strings.Contains(second.System, "<<<<<<< SEARCH") {
		t.Fatalf("recovery request does not restate the SEARCH/REPLACE contract: %.160q", second.System)
	}
	// The corrected second attempt staged a candidate at the approval boundary.
	if b := d.Boundary(); b == nil || b.PatchID == "" {
		t.Fatalf("corrected recovery did not stage a candidate: boundary=%+v state=%s", b, d.State())
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human at the approval gate", d.State())
	}
}
