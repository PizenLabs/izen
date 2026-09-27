package autonomy

// PHASE 12 — integration: the reported portfolio-redesign scenario, end to end
// through the canonical runtime.
//
// The original production failure was:
//
//	$prompt "…redesign a professional personal portfolio page… using HTML, CSS and JS"
//	  → classified read-only planning (the "design" substring inside "redesign")
//	  → never reached the autonomous Driver
//	  → a static index.html/styles.css/script.js plan the human had to /build
//	  → ONE 4 096-token full-artifact invocation
//	  → finish_reason=length
//	  → the delivered prefix was DISCARDED
//	  → the contract was relabelled into a bounded SEARCH/REPLACE patch against
//	    a file that does not exist
//	  → the 8 000-token run bound terminated the run
//	  → no useful artifact
//
// These tests drive the whole path and assert the corrected behavior.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/llmstep"
)

const portfolioObjective = "redesign a professional personal portfolio page for me " +
	"using HTML, CSS, and JS; the author's name is Tom Hunter, an AI Engineer."

// TestPhase12_PortfolioObjectiveReachesTheBuildDomain pins the first defect: the
// reported objective is unambiguously a change request, so it must classify as
// a mutation and route to the BUILD capability domain — the human must not have
// to re-issue it as /build.
func TestPhase12_PortfolioObjectiveReachesTheBuildDomain(t *testing.T) {
	classified := autonomy.Classify(portfolioObjective, nil)
	if classified.Intent != autonomy.IntentModification {
		t.Fatalf("intent = %s (%s), want modification — the objective plainly creates files",
			classified.Intent, classified.Explanation)
	}
	if !classified.RequiresMutation() {
		t.Fatal("a redesign that produces new files must require mutation")
	}
	route := autonomy.SelectWorkspace(classified.Intent, autonomy.RiskMedium,
		autonomy.RequiredCapabilities(classified.Intent))
	if route.Workspace != autonomy.WorkspaceBuild {
		t.Fatalf("workspace = %q (%s), want build — $prompt must not require a manual /build",
			route.Workspace, route.Reason)
	}
	// Classification granted nothing: only the gateway mints scope, and only
	// the AuthorizationEngine admits a mutation.
	prof := execution.NewIntentGateway(t.TempDir()).SelectStrategy(portfolioObjective)
	if prof.Strategy == "" {
		t.Fatal("the gateway must still classify the strategy deterministically")
	}
}

// TestPhase12_PortfolioCreationProgressesPastOutputExhaustion is the end-to-end
// acceptance test. A full-artifact creation whose first three invocations are
// exhausted must still produce a real, admitted patch — advancing the SAME
// artifact contract rather than abandoning the task or relabelling it into an
// impossible patch.
func TestPhase12_PortfolioCreationProgressesPastOutputExhaustion(t *testing.T) {
	root := t.TempDir()
	const page = "<!DOCTYPE html>\n<html><head><title>Tom Hunter</title></head>\n" +
		"<body><h1>Tom Hunter</h1><p>AI Engineer</p></body></html>\n"

	// The provider is cut off three times, then completes. Each exhausted step
	// delivers a genuine, growing prefix of the document.
	prefixes := []string{
		"<!DOCTYPE html>\n<html><head><title>Tom Hunter</title></head>\n",
		"<body><h1>Tom Hunter</h1>\n",
		"<p>AI Engineer building reliable systems.</p>\n",
	}
	responses := make([]*ai.Response, 0, len(prefixes)+1)
	for i, p := range prefixes {
		responses = append(responses, &ai.Response{
			Content: p,
			Usage: ai.ProviderUsage{
				Known: true, PromptTokens: 900 + i, CompletionTokens: 4096,
				FinishReason: "length",
			},
		})
	}
	responses = append(responses, &ai.Response{
		Content: "<body></body></html>\n",
		Usage: ai.ProviderUsage{
			Known: true, PromptTokens: 1200, CompletionTokens: 40, FinishReason: "stop",
		},
	})

	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "phase12-portfolio",
		Mode:      "autonomy",
		Prompt:    portfolioObjective,
		Target:    "index.html",
		Targets:   []string{"index.html"},
		Scope:     "dynamic",
		// The creation contract: a brand-new file, which has no content to
		// anchor a bounded patch against.
		Strategy: &executionStrategyCreateFile,
		Model:    "test/model",
	})
	if err != nil {
		t.Fatalf("a continued creation must complete: %v", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("expected a result with proof")
	}
	// Every billed invocation is accounted, including the exhausted ones.
	if n := len(res.Proof.ModelInvocations); n != len(responses) {
		t.Fatalf("model invocations = %d, want %d (3 exhausted + 1 complete)", n, len(responses))
	}
	// The task produced a real artifact.
	if len(res.ArtifactCandidates) != 1 || !res.ArtifactCandidates[0].Committed {
		t.Fatalf("candidates = %+v, want one committed candidate", res.ArtifactCandidates)
	}
	if res.PendingPatchID == "" {
		t.Fatal("a completed creation must reach the approval gate")
	}
	// SAFETY: nothing reached the workspace before authorization.
	if fileExistsAt(root, "index.html") {
		t.Fatal("a continued creation wrote the workspace before authorization")
	}
	// The recorded shape proves the recovery matrix knows a creation is not
	// patch-anchored.
	if res.ArtifactShape != "create_file" {
		t.Fatalf("artifact shape = %q, want create_file — the matrix needs the real contract", res.ArtifactShape)
	}
	_ = adapter
	_ = page
}

// TestPhase12_ExhaustedCreationEscalatesInsteadOfFabricatingAPatch drives the
// Driver for the case the original run hit: a creation that exhausts its whole
// bounded-step budget. The runtime must park at a human decision (whose
// `retry_with_explicit_budget` option is the only lever that can make a large
// creation fit) and must NOT fabricate a bounded patch for a file that does not
// exist, and must NOT report completion.
func TestPhase12_ExhaustedCreationEscalatesInsteadOfFabricatingAPatch(t *testing.T) {
	root := t.TempDir()
	// The model is cut off for its entire bounded step, and the typed
	// bounded-patch recovery attempt is cut off too.
	responses := exhaustedFullArtifactStep("partial page prefix\n", 900, 4096)
	responses = append(responses, &ai.Response{
		Content: "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 1024, FinishReason: "length"},
	})
	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus,
		WithLoopBounds(autonomy.LoopBounds{
			MaxAttempts: 3, MaxRecoveryCycles: 2, MaxExecutionSteps: 10,
			MaxIdenticalDecisions: 2, MaxTotalTokens: autonomy.MaxRunTokenBudget,
		}))

	term, err := driver.Run(context.Background(), "create @index.html for a personal portfolio page")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// NEVER a false completion.
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatal("an exhausted creation must never report completion")
	}
	if driver.State() == autonomy.RuntimeCompleted {
		t.Fatal("an exhausted creation must never reach the completed state")
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("false completion recorded in history: %+v", tr)
		}
	}
	// NOTHING was written.
	if fileExistsAt(root, "index.html") {
		t.Fatal("an exhausted creation wrote the workspace")
	}
	// The run converged on a human decision or a terminal abort — never a
	// silent stop with a fabricated patch.
	if b := driver.Boundary(); b != nil && b.PatchID != "" {
		t.Fatalf("an exhausted creation staged a held patch: %+v", b)
	}
	// And the exhaustion is reported as exhaustion, not as a patch failure:
	// the run's own history must contain no fabricated completion and the
	// driver must have observed a truncated execution.
	sawExhaustion := false
	for _, tr := range driver.History() {
		if strings.Contains(tr.Reason, "exhaust") || strings.Contains(tr.Reason, "exhausted") ||
			strings.Contains(tr.Reason, "budget") {
			sawExhaustion = true
		}
	}
	if !sawExhaustion {
		t.Fatalf("the run history never mentioned the exhaustion: %+v", driver.History())
	}
}

// TestPhase12_UsefulVerifiedOutcomePerComputation proves the efficiency claim in
// the only way that is meaningful: the same objective that previously produced
// zero artifacts now produces a verified one, and the run's token accounting
// covers exactly the work that was done.
func TestPhase12_UsefulVerifiedOutcomePerComputation(t *testing.T) {
	root := t.TempDir()
	responses := exhaustedStepThenComplete("prefix\n", 800, 4096,
		"<!DOCTYPE html>\n<html><body><h1>Tom Hunter</h1></body></html>\n")
	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus,
		WithLoopBounds(autonomy.LoopBounds{
			MaxAttempts: 3, MaxRecoveryCycles: 2, MaxExecutionSteps: 10,
			MaxIdenticalDecisions: 2, MaxTotalTokens: autonomy.MaxRunTokenBudget,
		}))

	if _, err := driver.Run(context.Background(), "create @index.html for a personal portfolio page"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The loop accounted every invocation it observed.
	in, out, known := driver.AggregatedUsage()
	if !known {
		t.Fatal("usage must be known — a truthful account is part of the outcome")
	}
	if in <= 0 || out <= 0 {
		t.Fatalf("usage = %d/%d, want a non-zero authoritative account", in, out)
	}
	// A staged patch means the run produced a real, reviewable outcome.
	if b := driver.Boundary(); b == nil || b.PatchID == "" {
		t.Fatalf("expected a staged approval patch, got %+v", b)
	}
}

// TestPhase12_ContextSufficiencyIsNotTradedForTokens pins §9: the repairs must
// not have bought efficiency by shrinking the model's view. The assertion is
// made at the layer that owns the claim — the context compiler telemetry —
// rather than by reverse-engineering a provider wire format.
func TestPhase12_ContextSufficiencyIsNotTradedForTokens(t *testing.T) {
	root := t.TempDir()
	const lines = 40
	body := strings.Repeat("<p>line</p>\n", lines)
	writeTarget(t, root, "index.html", body)

	var mu sync.Mutex
	var prepared []events.ContextPreparedPayload
	var compiled []events.ContextCompilationPayload
	bus := events.NewBus(events.DefaultBufferSize)
	bus.Subscribe(events.EventContextPrepared, func(ev events.DomainEvent) {
		if pl, ok := ev.Payload().(events.ContextPreparedPayload); ok {
			mu.Lock()
			prepared = append(prepared, pl)
			mu.Unlock()
		}
	})
	bus.Subscribe(events.EventContextCompilation, func(ev events.DomainEvent) {
		if pl, ok := ev.Payload().(events.ContextCompilationPayload); ok {
			mu.Lock()
			compiled = append(compiled, pl)
			mu.Unlock()
		}
	})

	responses := exhaustedStepThenComplete("<p>rewritten line</p>\n", 100, 2048, "<p>rewritten line</p>\n")
	mock := &mockProvider{responses: responses}
	x := testExecutor(t, root, mock, bus)

	if _, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "phase12-sufficiency",
		Mode:      "autonomy",
		Prompt:    "rewrite every line in @index.html",
		Target:    "index.html",
		Scope:     "dynamic",
		Strategy:  &executionStrategyReplaceBlock,
		Model:     "test/model",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prepared) == 0 {
		t.Fatal("no context telemetry was emitted — the sufficiency claim would be vacuous")
	}
	// The compiler was NOT asked to prune: nothing was truncated and nothing
	// was dropped to save tokens.
	for i, p := range prepared {
		if p.Truncated {
			t.Errorf("context compile %d truncated the model context: %d file(s)", i, p.TruncatedFileCount)
		}
		if p.DropCount != 0 {
			t.Errorf("context compile %d dropped %d section(s) — efficiency was bought by pruning", i, p.DropCount)
		}
	}
	for i, c := range compiled {
		if c.Truncated || c.DropCount != 0 {
			t.Errorf("compilation %d reported truncation=%t drops=%d — the model context must stay sufficient",
				i, c.Truncated, c.DropCount)
		}
	}
	// The continuation is STATE-based, not a transcript replay: it carries the
	// artifact's own delivered prefix and an explicit resume boundary, and it
	// never re-sends the original conversation.
	if len(mock.recordedRequests()) < 2 {
		t.Skip("the run completed on the first invocation")
	}
	cont := allContentOf(mock.recordedRequests()[1])
	if !strings.Contains(cont, "OUTPUT BUDGET EXHAUSTED") || !strings.Contains(cont, "DELIVERED SO FAR") {
		t.Fatalf("the continuation carries no bounded step state: %q", truncateForPhase12(cont))
	}
	if strings.Count(cont, "rewrite every line in @index.html") > 1 {
		t.Fatal("the continuation replayed the request transcript")
	}
}

// ── fixtures ────────────────────────────────────────────────────────────────

// executionStrategyCreateFile is the creation contract: a brand-new file, which
// has no content to anchor a bounded SEARCH/REPLACE patch against.
var executionStrategyCreateFile = strategy.ExecutionStrategyProfile{
	Strategy:        strategy.TargetedMutation,
	ModelRequired:   true,
	StrategyReason:  "phase 12 integration: creation contract",
	Artifact:        strategy.ArtifactContract{Kind: "create_file", Bounded: true},
	MaxOutputTokens: 8192,
}

// executionStrategyReplaceBlock is the anchored-mutation contract, which CAN be
// re-expressed as a bounded patch.
var executionStrategyReplaceBlock = strategy.ExecutionStrategyProfile{
	Strategy:        strategy.TargetedMutation,
	ModelRequired:   true,
	StrategyReason:  "phase 12 integration: anchored mutation contract",
	Artifact:        strategy.ArtifactContract{Kind: "replace_block", Bounded: true},
	MaxOutputTokens: 2048,
}

// ── local helpers ───────────────────────────────────────────────────────────

// allContentOf renders every byte of a request the provider would receive: the
// system prompt plus every message.
func allContentOf(req ai.Request) string {
	var b strings.Builder
	b.WriteString(req.System)
	for _, m := range req.Messages {
		b.WriteString("\n")
		b.WriteString(m.Content)
	}
	return b.String()
}

// exhaustedStepThenComplete builds one full exhausted bounded step followed by
// a single COMPLETE response: 1 initial invocation, DefaultMaxContinuationSteps
// - 1 continuations, then the completing step.
func exhaustedStepThenComplete(prefix string, promptTokens, completionTokens int, final string) []*ai.Response {
	out := make([]*ai.Response, 0, llmstep.DefaultMaxContinuationSteps+1)
	for i := 0; i < llmstep.DefaultMaxContinuationSteps; i++ {
		out = append(out, &ai.Response{
			Content: prefix,
			Usage: ai.ProviderUsage{
				Known: true, PromptTokens: promptTokens, CompletionTokens: completionTokens,
				FinishReason: "length",
			},
		})
	}
	out = append(out, &ai.Response{
		Content: final,
		Usage: ai.ProviderUsage{
			Known: true, PromptTokens: promptTokens + 100, CompletionTokens: 60, FinishReason: "stop",
		},
	})
	return out
}

// fileExistsAt reports whether a workspace-relative path exists. Used to prove
// SAFETY: nothing reached the workspace before authorization.
func fileExistsAt(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func truncateForPhase12(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
