package autonomy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── AST Repair Output-Budget Guardrail: E2E Test ───────────────────────────
//
// The directive's headline test: a 5,835-token target against a
// 1,024-token output ceiling (Dots3-Note-class model). The system MUST
// NOT call the full-rewrite endpoint, and MUST immediately fall back
// to the chunked BOUNDED_PATCH protocol on the AST error offset.
//
// The test exercises the full recovery flow:
//
//	1. Run $prompt with a corrupt, over-budget target → parks at
//	   DecisionSurface awaiting_human (output-budget guardrail refused
//	   the FULL_REWRITE dispatch).
//	2. Human selects ProposalRepairFirst.
//	3. The runtime continues: the driver routes the bounded-patch
//	   contract through the executor's patchOnlyArtifact path.
//	4. The executor's invokeMutation re-applies the guardrail — this
//	   time the SHAPE is BOUNDED_PATCH and the check is permissive.
//	5. The provider is invoked exactly once (the bounded-patch call),
//	   and the run parks at the approval gate (not the surface).
//
// The provider mock is instrumented: it records the request envelope
// (max_tokens, system prompt signal) on every call. The test asserts
// that NO call was made with the full-file envelope and that exactly
// ONE call was made with the bounded-patch envelope.

func TestASTRepair_BudgetExceeded_EnforcesBoundedPatch(t *testing.T) {
	root := t.TempDir()
	source := e2eCorruptFixture()
	writeTarget(t, root, "index.html", string(source))

	// Provider mock that records the request envelope on every call so
	// the e2e test can assert the contract shape. The execution
	// truth-matrix test pattern: a real RuntimeExecutor over a mock
	// provider with a trivial always-true verifier and a fresh
	// authorization grant.
	mock := &recordingProvider{
		responses: []*ai.Response{{
			Content: "<<<<<<< SEARCH\n  console.log('under construction');\n=======\n  console.log('under construction');\n</script>\n>>>>>>>",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 20, FinishReason: "stop"},
		}},
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)

	// The directive's headline repro: index.html at 1024 output, full
	// rewrite estimate ≈ 5,835 tokens. The corrupt fixture is 7,780
	// bytes → ~1,945 tokens at the chars/4 heuristic; the runtime
	// applies the FULL_REWRITE multiplier (3×) so the actual estimate
	// is well over 5,000 tokens. The executor's guardrail refuses the
	// FULL_REWRITE dispatch and the run parks at the DecisionSurface
	// awaiting_human. We pin the directive's exact values in the
	// unit-level guardrail test below; this e2e test exercises the
	// full recovery flow against the canonical corrupt fixture.
	targetBytes := 7780
	if got := execution.EstimateTargetTokens(strings.Repeat("x", targetBytes)); got < 1024 {
		t.Fatalf("test fixture: target estimate %d must exceed 1024 tokens for the FULL_REWRITE refusal", got)
	}
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := driver.Run(context.Background(),
		"$prompt @index.html remove redundant content, model=dots3-note max_output=1024"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (guardrail refusal must park)", driver.State())
	}
	b := driver.Boundary()
	if b == nil {
		t.Fatal("boundary is nil — guardrail refusal must produce a typed HumanBoundaryProposal")
	}
	if b.Action != autonomy.HumanBoundaryProposal {
		t.Fatalf("boundary action = %q, want HumanBoundaryProposal (the DecisionSurface barrier)", b.Action)
	}
	// NO provider call should have been made — the guardrail refused
	// before any ai.Provider call could complete.
	if calls := mock.count(); calls != 0 {
		t.Fatalf("provider calls before surface resolution = %d, want 0 (guardrail refused before dispatch)", calls)
	}
	if fullRewriteCalls := mock.countFullRewrite(); fullRewriteCalls != 0 {
		t.Fatalf("FULL_REWRITE invocations = %d, want 0 (the directive: NEVER call full rewrite on over-budget target)", fullRewriteCalls)
	}
	// The surface MUST offer the three hard-block recovery options so
	// the UI never deadlocks on a guardrail refusal.
	for _, want := range []ProposalIntent{
		ProposalAbortRun,
		ProposalForceBoundedPatch,
		ProposalSwitchModel,
	} {
		found := false
		for _, opt := range b.ProposalOptions {
			if ProposalIntent(opt.Intent) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("hard-block surface must offer %q", want)
		}
	}

	// Human selects repair_first. The driver builds a NEW contract and
	// re-runs the loop. The executor's guardrail now sees the
	// BOUNDED_PATCH shape and is permissive — the chunked patch is
	// dispatched.
	term, err := driver.ResumeWithProposal(context.Background(), "repair_first")
	if err != nil {
		t.Fatalf("ResumeWithProposal(repair_first): %v", err)
	}
	if term != nil {
		t.Fatalf("repair_first recovery should park at approval, not terminate: %+v", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state after repair_first = %s, want awaiting_human at approval", driver.State())
	}
	if b := driver.Boundary(); b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary after repair_first = %+v, want approval", b)
	}
	// Exactly ONE provider call: the BOUNDED_PATCH dispatch, never a
	// full rewrite. The directive: "System blocks full rewrite attempt,
	// does NOT truncate response at 1024 tokens".
	if calls := mock.count(); calls != 1 {
		t.Fatalf("provider calls after repair_first = %d, want 1 (the bounded-patch dispatch)", calls)
	}
	if fullRewriteCalls := mock.countFullRewrite(); fullRewriteCalls != 0 {
		t.Fatalf("FULL_REWRITE invocations = %d, want 0 (must fall back to BOUNDED_PATCH on AST error offset)", fullRewriteCalls)
	}
	if boundedCalls := mock.countBoundedPatch(); boundedCalls != 1 {
		t.Fatalf("BOUNDED_PATCH invocations = %d, want 1 (chunked BOUNDED_PATCH is the only viable path)", boundedCalls)
	}
}

// TestASTRepair_GuardrailRefusesFullRewriteDispatch pins the executor-side
// guardrail: a FULL_REWRITE dispatch against an over-budget target returns
// ErrOutputBudgetExceeded as a typed sentinel. The recovery matrix
// classifies it as SubtypeOutputExhausted and transitions to BOUNDED_PATCH.
func TestASTRepair_GuardrailRefusesFullRewriteDispatch(t *testing.T) {
	// 23,340 bytes (4× the directive's reference) → 5,835 tokens at
	// the chars/4 heuristic. The directive's headline repro.
	target := strings.Repeat("x", 23340)
	if got := execution.EstimateTargetTokens(target); got != 5835 {
		t.Fatalf("test setup: target estimate = %d, want 5835", got)
	}
	err := BudgetGuardrail{
		TargetTokens:    5835,
		MaxOutputTokens: 1024,
		Shape:           ShapeFullRewrite,
		Target:          "index.html",
	}.Check()
	if err == nil {
		t.Fatal("guardrail must refuse a 5,835-token target against a 1,024-token budget")
	}
	if !errors.Is(err, ErrOutputBudgetExceeded) {
		t.Fatalf("error = %v, want ErrOutputBudgetExceeded", err)
	}
}

// TestASTRepair_GuardrailAllowsBoundedPatchFallback pins the repair-first
// invariant: when the same target is re-evaluated under ShapeBoundedPatch,
// the guardrail is permissive. The chunked window physically fits in any
// output budget, so the recovery matrix can safely re-dispatch.
func TestASTRepair_GuardrailAllowsBoundedPatchFallback(t *testing.T) {
	target := strings.Repeat("x", 23340)
	err := BudgetGuardrail{
		TargetTokens:    execution.EstimateTargetTokens(target),
		MaxOutputTokens: 1024,
		Shape:           FallbackShapeForBudgetExceeded(),
		Target:          "index.html",
	}.Check()
	if err != nil {
		t.Fatalf("BOUNDED_PATCH fallback must be permitted: %v", err)
	}
	if FallbackShapeForBudgetExceeded() != ShapeBoundedPatch {
		t.Fatalf("fallback shape = %q, want %q", FallbackShapeForBudgetExceeded(), ShapeBoundedPatch)
	}
}

// TestASTRepair_HardBlockSurfaceNeverEmpty pins the deadlock-guard
// invariant: every DecisionSurface built for a hard-block failure
// carries at least one selectable option. The UI must never park
// awaiting_human on an unresolvable surface.
func TestASTRepair_HardBlockSurfaceNeverEmpty(t *testing.T) {
	for _, cat := range []PreflightFailureCategory{
		PreflightBudgetExceeded,
		PreflightASTCorrupt,
		PreflightCapabilityDenied,
	} {
		t.Run(string(cat), func(t *testing.T) {
			// Build a minimal PreflightEvaluation that classifies
			// to the given category. Budget and AST are the two
			// distinct categories the runtime parks on; the
			// other categories are derived from the same fields.
			eval := PreflightEvaluation{
				Target:           "index.html",
				ASTStatus:        ASTValid,
				BudgetStatus:     BudgetWithinLimits,
				DependencyStatus: DependenciesResolved,
			}
			switch cat {
			case PreflightBudgetExceeded:
				eval.BudgetStatus = BudgetExceeded
				eval.EstimatedTokens = 5835
				eval.MaxOutputTokens = 1024
			case PreflightASTCorrupt:
				eval.ASTStatus = ASTCorrupt
			case PreflightCapabilityDenied:
				eval.DependencyStatus = DependenciesUnresolved
			}
			surface := BuildDecisionSurface(eval, "$prompt")
			if len(surface.Options) == 0 {
				t.Fatalf("hard-block surface %q must carry at least one option", cat)
			}
			// The three hard-block recovery options MUST be present
			// so the human can always resolve the park.
			for _, want := range []ProposalIntent{
				ProposalAbortRun,
				ProposalForceBoundedPatch,
				ProposalSwitchModel,
			} {
				if !surface.Has(want) {
					t.Errorf("hard-block surface %q missing %q", cat, want)
				}
			}
		})
	}
}

// TestASTRepair_EnsureHardBlockOptionsIdempotent pins the helper's
// idempotence: appending the three recovery options to a list that
// already contains one of them produces a list with no duplicates.
func TestASTRepair_EnsureHardBlockOptionsIdempotent(t *testing.T) {
	first := EnsureHardBlockOptions(nil)
	if len(first) != 3 {
		t.Fatalf("EnsureHardBlockOptions(nil) length = %d, want 3", len(first))
	}
	second := EnsureHardBlockOptions(first)
	if len(second) != 3 {
		t.Fatalf("EnsureHardBlockOptions(idempotent) length = %d, want 3", len(second))
	}
	// And again with one option already present.
	pre := []ProposalOption{{ID: "x", Label: "X", Intent: ProposalCancel}}
	out := EnsureHardBlockOptions(pre)
	if len(out) != 4 {
		t.Fatalf("EnsureHardBlockOptions(pre) length = %d, want 4 (1 + 3 hard-block)", len(out))
	}
}

// recordingProvider is the test double for the directive's provider. It
// records the request envelope (max_tokens) on every call so the e2e
// test can assert that no FULL_REWRITE was attempted and exactly one
// BOUNDED_PATCH was.
type recordingProvider struct {
	mu        sync.Mutex
	responses []*ai.Response
	calls     []recordingCall
}

type recordingCall struct {
	MaxTokens int
	// SystemPromptHead is the first 80 chars of the system prompt so
	// the test can disambiguate full-file (boundedMutationSystemPrompt)
	// from search_replace (boundedPatchSystemPrompt).
	SystemPromptHead string
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, recordingCall{
		MaxTokens:        req.MaxTokens,
		SystemPromptHead: head80(req.System),
	})
	respIdx := len(p.calls) - 1
	if respIdx >= len(p.responses) {
		respIdx = len(p.responses) - 1
	}
	if respIdx < 0 {
		p.mu.Unlock()
		return nil, errors.New("recordingProvider: no responses configured")
	}
	resp := p.responses[respIdx]
	p.mu.Unlock()
	return resp, nil
}

func (p *recordingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

func (p *recordingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *recordingProvider) countFullRewrite() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if !isBoundedPatchSystemPrompt(c.SystemPromptHead) {
			n++
		}
	}
	return n
}

func (p *recordingProvider) countBoundedPatch() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if isBoundedPatchSystemPrompt(c.SystemPromptHead) {
			n++
		}
	}
	return n
}

// isBoundedPatchSystemPrompt reports whether the system prompt head
// matches the STRICT bounded-patch protocol's header. The executor
// switches to boundedPatchSystemPrompt() when the profile.Artifact.Bounded
// and Kind=search_replace, and to boundedMutationSystemPrompt() otherwise.
// The two headers are distinct: "bounded patch engine" vs "bounded
// mutation engine". The strict-prompt header is the deterministic
// signal the e2e test uses to classify the contract.
func isBoundedPatchSystemPrompt(head string) bool {
	return strings.Contains(head, "bounded patch engine")
}

func head80(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:80]
}
