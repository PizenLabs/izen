package execution

// PHASE 12 — full-artifact bounded-step continuation.
//
// A full-artifact generation ("create index.html", "rewrite note.txt") is a
// LOGICAL task that may legitimately require several bounded MODEL INVOCATIONS.
// `finish_reason=length` is an INVOCATION outcome; it is never, by itself, a
// task failure.
//
// These tests pin the corrected semantics at the narrowest correct layer:
//   - bounded invocations under ONE artifact contract and ONE authority;
//   - a preserved, non-mutating partial candidate;
//   - NO mutation without admission + authorization;
//   - a typed, recoverable exhaustion when the request budget is consumed;
//   - unchanged behavior for the bounded-patch contract.
//
// They replace the pre-Phase-12 expectation that one exhausted invocation ends
// the logical task and discards its bytes.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// exhaustedResponse builds a provider response the runtime must classify as
// OUTPUT_EXHAUSTED: a genuine content prefix plus finish_reason=length.
func exhaustedResponse(content string, promptTokens, completionTokens int) *ai.Response {
	return &ai.Response{
		Content: content,
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			FinishReason:     "length",
		},
	}
}

// completeResponse builds a normally-terminated provider response.
func completeResponse(content string, promptTokens, completionTokens int) *ai.Response {
	return &ai.Response{
		Content: content,
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			FinishReason:     "stop",
		},
	}
}

// TestPhase12_FullArtifactContinuesUnderSameContract is the primary acceptance
// test for the reported production failure. A model that is cut off at its
// output ceiling mid-artifact is continued, not abandoned: the runtime issues
// further bounded invocations under the SAME full-artifact contract, folds the
// delivered prefixes into one candidate, and produces a real patch.
func TestPhase12_FullArtifactContinuesUnderSameContract(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, filepath.Join(root, "note.txt"), sampleOriginal)
	before := sampleOriginal

	// Three exhausted invocations, then a complete one. The request budget is
	// 1 initial + DefaultMaxContinuationSteps continuations, so the fourth
	// invocation is the last affordable step.
	mock := &mockProvider{responses: []*ai.Response{
		exhaustedResponse("bar -> ", 10, 20),
		exhaustedResponse("qux\nsecond part ", 11, 21),
		exhaustedResponse("continues here\n", 12, 22),
		completeResponse("and the document closes.\n", 13, 23),
	}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "phase12-continuation",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
		Scope:     "declared",
	})
	if err != nil {
		t.Fatalf("bounded continuation must complete the artifact, got: %v", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected a result with proof")
	}
	// Every invocation is billed and recorded: the exhaustion is invisible to
	// the usage account.
	if n := len(res.Proof.ModelInvocations); n != 4 {
		t.Fatalf("model invocations = %d, want 4 (1 initial + 3 bounded continuations)", n)
	}
	if n := len(mock.requests); n != 4 {
		t.Fatalf("provider calls = %d, want 4", n)
	}
	// TASK != INVOCATION: the task produced a real patch even though three of
	// its four invocations were exhausted.
	if len(res.Mutations) == 0 && res.PendingPatchID == "" && res.ArtifactKind == "" {
		t.Fatal("a continued full-artifact task must produce an artifact")
	}
	// The candidate is truthful: one COMPLETE candidate for the target.
	if len(res.ArtifactCandidates) != 1 {
		t.Fatalf("artifact candidates = %d, want 1", len(res.ArtifactCandidates))
	}
	c := res.ArtifactCandidates[0]
	if c.Status != CandidateComplete || !c.Committed {
		t.Fatalf("candidate = %+v, want a committed complete candidate", c)
	}
	if c.ExhaustedSteps != 3 {
		t.Fatalf("candidate exhausted steps = %d, want 3", c.ExhaustedSteps)
	}
	// SAFETY (spec §16): nothing reached the workspace without authorization.
	if got := readFileAt(t, filepath.Join(root, "note.txt")); got != before {
		t.Fatal("a continued artifact mutated the workspace before authorization")
	}
}

// TestPhase12_ContinuationKeepsTheSameContract asserts the continuation is a
// step of the SAME logical task: the artifact contract does not change, the
// scope does not change, and the only thing that grows is the step ordinal.
func TestPhase12_ContinuationKeepsTheSameContract(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, filepath.Join(root, "note.txt"), sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{
		exhaustedResponse("first prefix ", 10, 20),
		completeResponse("second half", 11, 21),
	}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "phase12-contract",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
		Scope:     "declared",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	reqs := mock.requests
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	if reqs[0].MaxTokens == 0 || reqs[1].MaxTokens == 0 {
		t.Fatal("every invocation must carry a finite, explicit max_tokens bound")
	}
	if reqs[0].MaxTokens != reqs[1].MaxTokens {
		t.Fatalf("continuation changed the invocation budget: %d -> %d",
			reqs[0].MaxTokens, reqs[1].MaxTokens)
	}
	if reqs[0].System != reqs[1].System {
		t.Fatal("continuation changed the system prompt — the contract must be stable")
	}
	// The continuation is STATE-based, not transcript-based: it carries the
	// delivered prefix and an explicit resume instruction.
	cont := lastUserContent(reqs[1])
	if !strings.Contains(cont, "DELIVERED SO FAR") || !strings.Contains(cont, "first prefix") {
		t.Fatalf("continuation turn does not carry the delivered state:\n%s", truncateForLog(cont))
	}
	if strings.Contains(cont, "first prefix\n\nuser\n") {
		t.Fatal("continuation replayed a transcript")
	}
}

// TestPhase12_PartialCandidateNeverMutates is the safety lock. A run whose
// bounded-step budget is consumed with a delivered prefix must return the
// TYPED, RECOVERABLE exhaustion, must record the partial candidate as
// evidence, and must leave the workspace byte-identical.
func TestPhase12_PartialCandidateNeverMutates(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, filepath.Join(root, "note.txt"), sampleOriginal)
	before := sampleOriginal

	// Every invocation is cut off. The request budget is consumed after
	// 1 + DefaultMaxContinuationSteps invocations.
	responses := make([]*ai.Response, 0, 1+llmstep.DefaultMaxContinuationSteps)
	for i := 0; i <= llmstep.DefaultMaxContinuationSteps; i++ {
		responses = append(responses, exhaustedResponse("partial chunk "+strings.Repeat("x", 32)+"\n", 10, 20))
	}
	mock := &mockProvider{responses: responses}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "phase12-exhausted",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
		Scope:     "declared",
	})
	if err == nil {
		t.Fatal("a consumed bounded-step budget must not report success")
	}
	if !llmstep.IsOutputExhausted(err) {
		t.Fatalf("err = %v, want the typed recoverable bounded-step exhaustion", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected a result with proof even on exhaustion")
	}
	// EXHAUSTION != TASK FAILURE: the outcome stays the truthful truncation
	// classification the recovery matrix consumes.
	if res.Proof.Outcome != OutcomeTruncated {
		t.Fatalf("outcome = %q, want %q", res.Proof.Outcome, OutcomeTruncated)
	}
	// No artifact, no admission, no authorization.
	if res.PendingPatchID != "" {
		t.Fatal("an exhausted artifact opened an approval surface")
	}
	if res.Content != "" {
		t.Fatal("a partial candidate must never become result content")
	}
	// The delivered prefix survives as EVIDENCE.
	if len(res.ArtifactCandidates) != 1 {
		t.Fatalf("artifact candidates = %d, want 1", len(res.ArtifactCandidates))
	}
	c := res.ArtifactCandidates[0]
	if c.Status != CandidatePartial || c.Committed {
		t.Fatalf("candidate = %+v, want a non-committed partial candidate", c)
	}
	if c.DeliveredBytes <= 0 {
		t.Fatal("the delivered prefix must be preserved as evidence, not discarded")
	}
	if c.Fingerprint == "" {
		t.Fatal("a partial candidate must carry a content fingerprint")
	}
	// SAFETY: nothing was written.
	if got := readFileAt(t, filepath.Join(root, "note.txt")); got != before {
		t.Fatal("a partial candidate mutated the workspace")
	}
	// Every billed invocation survives the error return.
	if n := len(res.Proof.ModelInvocations); n != 1+llmstep.DefaultMaxContinuationSteps {
		t.Fatalf("model invocations = %d, want %d", n, 1+llmstep.DefaultMaxContinuationSteps)
	}
}

// TestPhase12_NoProgressGuardStopsImmediately proves an exhausted step that
// delivered NOTHING new halts instead of burning the remaining request budget
// on a loop that cannot advance the artifact.
func TestPhase12_NoProgressGuardStopsImmediately(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, filepath.Join(root, "note.txt"), sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{
		exhaustedResponse("   \n", 10, 20),
		exhaustedResponse("should never be requested", 11, 21),
	}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "phase12-no-progress",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
		Scope:     "declared",
	})
	if !llmstep.IsOutputExhausted(err) {
		t.Fatalf("err = %v, want the typed recoverable exhaustion", err)
	}
	if n := len(mock.requests); n != 1 {
		t.Fatalf("provider calls = %d, want 1 — a step that delivered nothing must not be continued", n)
	}
	if len(res.ArtifactCandidates) != 1 || res.ArtifactCandidates[0].DeliveredBytes != 0 {
		t.Fatalf("candidates = %+v, want one zero-byte partial candidate", res.ArtifactCandidates)
	}
}

// TestPhase12_BoundedPatchContractIsUnchanged proves the repair is scoped: the
// bounded-patch contract already fits any budget, so exhaustion there is a
// genuine re-scope and must still be exactly ONE invocation ending in the
// typed output-gate error.
func TestPhase12_BoundedPatchContractIsUnchanged(t *testing.T) {
	root := t.TempDir()
	src := strings.Repeat("<p class=\"line\">content</p>\n", 4)
	writeFileAt(t, filepath.Join(root, "index.html"), src)
	before := src

	profile := strategy.ExecutionStrategyProfile{
		Strategy:        strategy.TargetedMutation,
		ModelRequired:   true,
		StrategyReason:  "phase 12 bounded-patch contract must stay unchanged",
		Artifact:        strategy.ArtifactContract{Kind: "search_replace", Bounded: true},
		MaxOutputTokens: 1024,
	}

	mock := &mockProvider{responses: []*ai.Response{
		exhaustedResponse("<<<<<<< SEARCH\n<p class=\"line\">content</p>\n", 10, 20),
	}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	_, _, _, _, _, err := x.invokeMutation(context.Background(), ExecuteRequest{
		RequestID: "phase12-patch",
		Prompt:    "change the line",
		Target:    "index.html",
		Scope:     "declared",
		Strategy:  &profile,
	}, "phase12-patch", profile, []string{"index.html"}, runtimegraph.New("phase12-patch", nil))
	if err == nil {
		t.Fatal("a bounded-patch exhaustion must still fail the invocation")
	}
	var gate *OutputGateError
	if !errors.As(err, &gate) || gate.Outcome != CanonicalOutputExhausted {
		t.Fatalf("err = %v, want the typed output-gate exhaustion", err)
	}
	if n := len(mock.requests); n != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 — the bounded-patch contract is not continued", n)
	}
	if got := readFileAt(t, filepath.Join(root, "index.html")); got != before {
		t.Fatal("the bounded-patch path mutated the workspace")
	}
}

// TestPhase12_DerivedCreationRequestIsStillClamped proves the derived creation
// REQUEST never becomes an overspend. The strategy layer now asks for a larger
// per-invocation budget (see strategy.CreationTokenTiers); the shared
// capability chain must still force a constrained model's 980-token ceiling
// and the bounded-patch shape, so no provider is ever billed for more than it
// can actually produce.
func TestPhase12_DerivedCreationRequestIsStillClamped(t *testing.T) {
	// A constrained/free-tier model: the clamp wins over any derivation.
	capped := ModelProfile{OutputTokenCap: 512, ModelID: "vendor/model"}
	if !capped.IsConstrained() {
		t.Fatal("a capped provider model must classify as constrained")
	}
	for _, requested := range []int{4096, 8192, 16384} {
		if got := capped.ClampMaxTokens(requested); got != ConstrainedMaxTokens {
			t.Fatalf("clamp(%d) = %d, want the constrained ceiling %d", requested, got, ConstrainedMaxTokens)
		}
		if got := ClampMaxTokensForBudget(requested, 512); got != ConstrainedMaxTokens {
			t.Fatalf("ClampMaxTokensForBudget(%d) = %d, want %d", requested, got, ConstrainedMaxTokens)
		}
	}
	// An unconstrained model keeps the requested budget verbatim.
	unconstrained := ModelProfile{ModelID: "vendor/large-model"}
	if got := unconstrained.ClampMaxTokens(16384); got != 16384 {
		t.Fatalf("clamp for an unconstrained model = %d, want 16384", got)
	}
}

// TestPhase12_ConstrainedModelStillForcesBoundedPatch proves the constrained
// output invariant survives the budget change: a model capped at ≤1024 output
// tokens can never be dispatched a full-artifact creation, because the executor
// forces the bounded-patch shape and the pre-dispatch guardrail refuses a
// FULL_REWRITE it cannot pay for.
func TestPhase12_ConstrainedModelStillForcesBoundedPatch(t *testing.T) {
	shape := ShapeFullRewrite
	constrained := ModelProfile{OutputTokenCap: 512, ModelID: "vendor/model"}
	if !constrained.IsConstrained() {
		t.Fatal("expected a constrained classification")
	}
	// A creation has no baseline, so the guardrail is permissive by design —
	// which is exactly why the executor must switch the shape instead.
	emptyBaseline := BudgetGuardrail{
		TargetTokens:    EstimateTargetTokens(""),
		MaxOutputTokens: ConstrainedMaxTokens,
		Shape:           shape,
		Target:          "index.html",
	}
	if err := emptyBaseline.Check(); err != nil {
		t.Fatalf("an empty baseline must stay permissive, got %v", err)
	}
	// A real full rewrite that cannot fit IS refused, and the caller must fall
	// back to the bounded patch.
	tooBig := BudgetGuardrail{
		TargetTokens:    5000,
		MaxOutputTokens: ConstrainedMaxTokens,
		Shape:           shape,
		Target:          "note.txt",
	}
	if err := tooBig.Check(); !errors.Is(err, ErrOutputBudgetExceeded) {
		t.Fatalf("err = %v, want ErrOutputBudgetExceeded", err)
	}
	if FallbackShapeForBudgetExceeded() != ShapeBoundedPatch {
		t.Fatal("the canonical fallback shape must remain the bounded patch")
	}
}

// ── local helpers ───────────────────────────────────────────────────────────

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileAt(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func lastUserContent(req ai.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(req.Messages[i].Role, "system") {
			return req.Messages[i].Content
		}
	}
	return ""
}

func truncateForLog(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
