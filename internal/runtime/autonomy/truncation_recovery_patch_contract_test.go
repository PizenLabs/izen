package autonomy

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// bigIndexHTML returns deterministic HTML of EXACTLY 7780 bytes (the
// production repro target) with stable per-line anchors a bounded patch can
// target. Since Boundary 2 (preflight guard, invariant I5), an infeasible
// full rewrite of this file can never reach the provider inside a loop run,
// so loop-level tests use the feasibility-sized compactIndexHTML; the big
// fixture remains the reference geometry for the bounded-patch WINDOW tests,
// which drive the executor directly through its explicit bounded_patch
// contract (ArtifactBounded ⇒ preflight-exempt by construction).
var (
	bigHTMLOnce   sync.Once
	bigHTMLCached string
)

func bigIndexHTML() string {
	bigHTMLOnce.Do(func() {
		const want = 7780
		head := []string{"<!DOCTYPE html>", "<html>", "<head><title>demo</title></head>", "<body>"}
		tail := []string{"</body>", "</html>"}
		var mid []string
		for i := 0; ; i++ {
			cand := append(append([]string{}, head...), mid...)
			cand = append(cand, indexHTMLLine(i))
			cand = append(cand, tail...)
			if len(strings.Join(cand, "\n"))+1 > want {
				break
			}
			mid = append(mid, indexHTMLLine(i))
		}
		all := append(append([]string{}, head...), mid...)
		all = append(all, tail...)
		s := strings.Join(all, "\n") + "\n"
		for len(s) < want {
			s += " "
		}
		s = s[:want]
		bigHTMLCached = s
	})
	return bigHTMLCached
}

func indexHTMLLine(n int) string {
	return "<p class=\"line-" + itoa(n) + "\">filler content line " + itoa(n) + "</p>"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// compactIndexHTML is a feasibility-sized full-rewrite target (~700 bytes):
// TargetFileTokens × FullRewriteTokenMultiplier fits a 1024-token budget at
// Boundary 2, so a loop-level FULL_REWRITE attempt legitimately reaches the
// provider and the typed OUTPUT_EXHAUSTED path stays exercisable end to end.
func compactIndexHTML() string {
	head := []string{"<!DOCTYPE html>", "<html>", "<head><title>demo</title></head>", "<body>"}
	tail := []string{"</body>", "</html>"}
	all := append([]string{}, head...)
	for i := 0; i < 12; i++ {
		all = append(all, indexHTMLLine(i))
	}
	all = append(all, tail...)
	return strings.Join(all, "\n") + "\n"
}

// tailLine is the LAST filler line of the big fixture. It appears in the
// complete document but can never appear inside a bounded-patch context
// window (windows are capped far below the file size), so it proves whether
// the whole file crossed to the model.
var tailLine = func() string {
	s := bigIndexHTML()
	i := strings.LastIndex(s, "<p ")
	j := strings.Index(s[i:], "\n")
	if j < 0 || i < 0 {
		return s[len(s)-40:]
	}
	return s[i : i+j]
}()

func patchForLine(n int, replacement string) string {
	return "<<<<<<< SEARCH\n" + indexHTMLLine(n) + "\n=======\n" + replacement + "\n>>>>>>>"
}

func generousBounds() autonomy.LoopBounds {
	return autonomy.LoopBounds{
		MaxAttempts:           5,
		MaxRecoveryCycles:     4,
		MaxExecutionSteps:     20,
		MaxIdenticalDecisions: 20,
		MaxTotalTokens:        200000,
	}
}

func newCompactDriver(t *testing.T, root string, responses ...*ai.Response) (*mockProvider, *execution.RuntimeExecutor, *Driver) {
	t.Helper()
	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return mock, x, NewDriver(adapter, bus, WithLoopBounds(generousBounds()))
}

// ── 1. wire contracts of both artifact protocols (adapter-driven) ──────────

// TestTolerantAttemptEmbedsCompleteTarget pins the INITIAL full-artifact wire
// contract: the tolerant mutation prompt offers the full-file option and the
// user message embeds the complete target.
func TestTolerantAttemptEmbedsCompleteTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	truncated := &ai.Response{
		Content: compactIndexHTML(),
		Usage:   ai.ProviderUsage{PromptTokens: 300, CompletionTokens: 200, Known: true, FinishReason: "stop"},
	}
	mock, _, adapter, _ := harnessWithAdapter(t, root, truncated)
	_, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:  "check this file @index.html and rewrite it",
		Targets: []string{"index.html"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	reqs := mock.recordedRequests()
	if len(reqs) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "The full modified file content") {
		t.Fatalf("attempt lost the tolerant full-artifact contract: %q", reqs[0].System)
	}
	if !strings.Contains(reqs[0].Messages[0].Content, "</html>") {
		t.Fatal("tolerant attempt should embed the complete target file")
	}
}

// TestBoundedPatchWireContract pins the STRICT bounded-patch wire contract as
// the executor actually sends it: patch-only system prompt, runtime-derived
// window instead of the complete file, the patch protocol in the user
// message, the recovery annotation, and the unchanged budget.
func TestBoundedPatchWireContract(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	bounded := &ai.Response{
		Content: patchForLine(3, "<p class=\"line-3\">rewritten content line 3</p>"),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 180, Known: true, FinishReason: "stop"},
	}
	mock, _, adapter, _ := harnessWithAdapter(t, root, bounded)
	res, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:           "check this file @index.html and rewrite it",
		Targets:          []string{"index.html"},
		RecoveryStrategy: autonomy.StrategyBoundedPatch,
		RecoveryAttempt:  2,
		RecoveryReason:   "output_budget_exhausted: typed transition after finish_reason=length",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Outcome != autonomy.OutcomePendingApproval {
		t.Fatalf("outcome = %s, want pending_approval", res.Outcome)
	}

	reqs := mock.recordedRequests()
	if len(reqs) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(reqs))
	}
	a := reqs[0]

	// Strict system contract: no full-file option, patch protocol required.
	if strings.Contains(strings.ToLower(a.System), "full modified file") {
		t.Fatal("bounded attempt still offers the full-file option")
	}
	if !strings.Contains(a.System, "<<<<<<< SEARCH") {
		t.Fatalf("bounded system prompt missing SEARCH/REPLACE requirement: %q", a.System)
	}

	// The USER message must not carry the complete file: a sentinel from the
	// deep tail of the document must be absent.
	u := a.Messages[0].Content
	if strings.Contains(u, tailLine) {
		t.Fatal("bounded attempt STILL carries the complete file — input contract unchanged")
	}
	if len(u) > 4000 {
		t.Fatalf("bounded user message is %d bytes — window escaped its cap", len(u))
	}
	if !strings.Contains(u, "OUTPUT FORMAT") || !strings.Contains(u, "<<<<<<< SEARCH") {
		t.Fatalf("bounded user message missing the patch protocol: %q", u[:min(len(u), 300)])
	}
	if !strings.Contains(u, "[RECOVERY bounded_patch:") {
		t.Fatal("bounded attempt missing the recovery annotation")
	}
	if a.MaxTokens != 1024 {
		t.Fatalf("max_tokens=%d, want 1024 (representation change, never a budget raise)", a.MaxTokens)
	}
}

// TestAttemptRotationMateriallyChangesRequest proves successive bounded-patch
// attempts are genuinely different requests: rotated runtime windows (the
// CONTEXT WINDOW header names a different region) — never a re-send of the
// identical request under a new annotation.
func TestAttemptRotationMateriallyChangesRequest(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	junkPatch := func(in int) *ai.Response { // well-formed marker, unmatchable anchor
		return &ai.Response{
			Content: "<<<<<<< SEARCH\nNO SUCH LINE IN FILE\n=======\nreplacement\n>>>>>>>",
			Usage:   ai.ProviderUsage{PromptTokens: in, CompletionTokens: 60, Known: true, FinishReason: "stop"},
		}
	}
	mock, _, adapter, _ := harnessWithAdapter(t, root, junkPatch(2277), junkPatch(2330))
	for _, attempt := range []int{2, 3} {
		if _, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
			Prompt:           "check this file @index.html and rewrite it",
			Targets:          []string{"index.html"},
			RecoveryStrategy: autonomy.StrategyBoundedPatch,
			RecoveryAttempt:  attempt,
		}); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}

	reqs := mock.recordedRequests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	a2u, a3u := reqs[0].Messages[0].Content, reqs[1].Messages[0].Content
	for _, u := range []string{a2u, a3u} {
		if strings.Contains(u, tailLine) {
			t.Fatal("bounded attempt carried the complete file")
		}
		if !strings.Contains(u, "<<<<<<< SEARCH") {
			t.Fatal("bounded attempt missing the patch protocol")
		}
	}
	if a2u == a3u {
		t.Fatal("attempt 3 re-sent the IDENTICAL request of attempt 2 — no material difference")
	}
	// Rotated windows: the CONTEXT WINDOW header names a different region.
	if strings.Contains(a3u, "lines 1-") && strings.Contains(a2u, "lines 1-") &&
		extractWindowHeader(a2u) == extractWindowHeader(a3u) {
		t.Fatalf("attempts 2 and 3 show the same window: %q", extractWindowHeader(a2u))
	}
}

func extractWindowHeader(user string) string {
	idx := strings.Index(user, "### CONTEXT WINDOW")
	if idx < 0 {
		return ""
	}
	line := user[idx:]
	if nl := strings.Index(line, "\n"); nl >= 0 {
		line = line[:nl]
	}
	return line
}

// ── 3+7. small budget completion and successful end-to-end recovery ────────

// TestBoundedPatchFitsSmallOutputBudget proves a compliant patch completes far
// below the 1024-token ceiling and stages successfully.
func TestBoundedPatchFitsSmallOutputBudget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	truncated := &ai.Response{
		Content: compactIndexHTML()[:500],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(6, "<p class=\"line-6\">patched line 6</p>"),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 96, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newCompactDriver(t, root, truncated, bounded)
	runObjectiveCompact(t, driver)

	if mock.calls() != 2 {
		t.Fatalf("invocations = %d, want 2", mock.calls())
	}
	last := mock.responses[1].Usage.CompletionTokens
	if last >= 1024 {
		t.Fatalf("patch response used %d tokens — does not fit the small budget", last)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %v, want approval gate", driver.State())
	}
	if b := driver.Boundary(); b == nil || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want held patch", b)
	}
}

// TestSuccessfulRecoveryFromRealTruncation is the end-to-end acceptance path:
// truncate on attempt 1, recover with ONE typed bounded-patch transition on
// attempt 2, stage, approve, apply, VERIFY — without exhausting any bound.
func TestSuccessfulRecoveryFromRealTruncation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	truncated := &ai.Response{
		Content: compactIndexHTML()[:500],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(7, "<p class=\"line-7\">verified rewrite line 7</p>"),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 120, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newCompactDriver(t, root, truncated, bounded)
	runObjectiveCompact(t, driver)

	if mock.calls() > 2 {
		t.Fatalf("recovery did not converge: %d invocations", mock.calls())
	}
	b := driver.Boundary()
	if b == nil || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want approval gate holding the recovered patch", b)
	}
	term, err := driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	got := readTarget(t, root, "index.html")
	if !strings.Contains(got, ">verified rewrite line 7<") {
		t.Fatal("recovered patch was not applied to the target")
	}
	if strings.Count(got, "<!DOCTYPE html>") != 1 {
		t.Fatal("applied mutation corrupted the document structure")
	}
}

// ── 6+10. misbehaving model stays safe and stays bounded ───────────────────

// TestMisbehavingModelDoesNotCorruptFile proves the safety half: when the
// model keeps ignoring the patch protocol, every recovery attempt remains
// strictly bounded-patch, the file is never touched, nothing is staged, and
// the loop parks for a human within its bounds.
func TestMisbehavingModelDoesNotCorruptFile(t *testing.T) {
	root := t.TempDir()
	before := compactIndexHTML()
	writeTarget(t, root, "index.html", before)

	truncated := &ai.Response{
		Content: before[:500],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	fullFile := func(in int) *ai.Response {
		return &ai.Response{
			Content: "<!DOCTYPE html>\n<html><body>complete replacement</body></html>\n",
			Usage:   ai.ProviderUsage{PromptTokens: in, CompletionTokens: 300, Known: true, FinishReason: "stop"},
		}
	}
	responses := exhaustedFullArtifactStep(truncated.Content, 2182, 1022)
	responses = append(responses, fullFile(2277), fullFile(2353))
	mock, _, driver := newCompactDriver(t, root, responses...)
	runObjectiveCompact(t, driver)

	reqs := mock.recordedRequests()
	// The bounded continuations of the FIRST attempt keep the full-artifact
	// contract on purpose (PHASE 12: the contract is what stays stable). The
	// Driver-driven RECOVERY attempts after them must be bounded patches.
	recovery := reqs[exhaustedFullArtifactStepCount:]
	if len(recovery) == 0 {
		t.Fatalf("no driver-driven recovery request was issued (%d total requests)", len(reqs))
	}
	for i, r := range recovery {
		norm := strings.Join(strings.Fields(strings.ToLower(r.System)), " ")
		if strings.Contains(norm, "full modified file") ||
			!strings.Contains(r.System, "<<<<<<< SEARCH") {
			t.Fatalf("recovery request #%d regressed to the full-artifact contract: %q", i+1, r.System)
		}
		if r.MaxTokens != 1024 {
			t.Fatalf("recovery request #%d raised the budget to %d", i+1, r.MaxTokens)
		}
	}
	if got := readTarget(t, root, "index.html"); got != before {
		t.Fatal("a rejected artifact modified the target file")
	}
	b := driver.Boundary()
	if b == nil || b.PatchID != "" {
		t.Fatalf("boundary = %+v, want inform boundary with NO held patch", b)
	}
}

// ── 8. no duplicate logical invocations ────────────────────────────────────

// TestRecoveryDoesNotDuplicateProviderInvocation proves transport retries are
// not counted as extra logical executions: one HTTP-attempt-heavy provider
// call is still exactly ONE ModelInvocation and ONE aggregated count.
func TestRecoveryDoesNotDuplicateProviderInvocation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	truncated := &ai.Response{
		Content: compactIndexHTML()[:500],
		Usage:   ai.ProviderUsage{PromptTokens: 1000, CompletionTokens: 500, Known: true, FinishReason: "length", HTTPAttempts: 3, RateLimitedRetries: 2},
	}
	bounded := &ai.Response{
		Content: patchForLine(8, "<p class=\"line-8\">deduped line 8</p>"),
		Usage:   ai.ProviderUsage{PromptTokens: 1100, CompletionTokens: 90, Known: true, FinishReason: "stop", HTTPAttempts: 2, RateLimitedRetries: 1},
	}
	responses := exhaustedFullArtifactStep(truncated.Content, truncated.Usage.PromptTokens, truncated.Usage.CompletionTokens)
	responses = append(responses, bounded)
	mock, x, driver := newCompactDriver(t, root, responses...)
	runObjectiveCompact(t, driver)

	// One exhausted full-artifact bounded step, then exactly ONE loop attempt
	// that staged the patch.
	if got := mock.calls(); got != exhaustedFullArtifactStepCount+1 {
		t.Fatalf("provider invocations = %d, want %d", got, exhaustedFullArtifactStepCount+1)
	}
	// The driver accounts EVERY billed invocation it observed: the exhausted
	// full-artifact bounded step AND the single recovery attempt that staged
	// the patch. Nothing is hidden, and nothing is double counted.
	in, out, known := driver.AggregatedUsage()
	wantIn := exhaustedFullArtifactStepCount*truncated.Usage.PromptTokens + bounded.Usage.PromptTokens
	wantOut := exhaustedFullArtifactStepCount*truncated.Usage.CompletionTokens + bounded.Usage.CompletionTokens
	if !known || in != wantIn || out != wantOut {
		t.Fatalf("aggregate = %d/%d known=%v, want %d/%d true (one count per logical invocation)",
			in, out, known, wantIn, wantOut)
	}
	b := driver.Boundary()
	if b == nil || b.PatchID == "" {
		t.Fatal("recovered patch not staged")
	}
	res, err := x.Approve(context.Background(), b.PatchID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	// The approval resolves the SAME execution (attempt 2): its proof carries
	// exactly ONE logical invocation even though that invocation performed 2
	// transport attempts — HTTP retries are never separate logical calls.
	if n := len(res.Proof.ModelInvocations); n != 1 {
		t.Fatalf("proof model invocations = %d, want 1 (transport retries are not logical invocations)", n)
	}
	if res.Proof.ModelInvocations[0].HTTPAttempts != 2 || res.Proof.ModelInvocations[0].RateLimitedRetries != 1 {
		t.Fatalf("HTTPAttempts forensics lost: %+v", res.Proof.ModelInvocations[0])
	}
}

// ── 9. truthful usage aggregation across recovery ──────────────────────────

// TestUsageAggregationAcrossRecovery keeps the original guarantee: usage is
// aggregated once per logical invocation and the authoritative finish_reason
// of the truncating invocation survives into the observation.
func TestUsageAggregationAcrossRecovery(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", compactIndexHTML())

	truncated := &ai.Response{
		Content: compactIndexHTML()[:500],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(9, "<p class=\"line-9\">counted line 9</p>"),
		Usage:   ai.ProviderUsage{PromptTokens: 2303, CompletionTokens: 140, Known: true, FinishReason: "stop"},
	}
	responses := exhaustedFullArtifactStep(truncated.Content, 2182, 1022)
	responses = append(responses, exhaustedFullArtifactStep(truncated.Content, 2182, 1022)...)
	responses = append(responses, bounded)
	mock, _, adapter, _ := harnessWithAdapter(t, root, responses...)

	// Direct first attempt through the adapter: the observation must preserve
	// the authoritative finish_reason and usage verbatim.
	obs, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:  "check this file @index.html and rewrite it",
		Targets: []string{"index.html"},
	})
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	// The observation accounts the WHOLE exhausted bounded step, not just its
	// first invocation: those tokens were really spent on this logical task.
	wantOut := int64(exhaustedFullArtifactStepCount) * 1022
	if obs.FinishReason != "length" || obs.OutputTokens != int(wantOut) {
		t.Fatalf("observation = %d toks finish=%q, want %d/length", obs.OutputTokens, obs.FinishReason, wantOut)
	}
	if obs.RecoveryStrategy != autonomy.StrategyFullArtifact {
		t.Fatalf("first attempt strategy = %q, want full_artifact", obs.RecoveryStrategy)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	driver := NewDriver(adapter, bus, WithLoopBounds(generousBounds()))
	if _, err := driver.Run(context.Background(), "check this file @index.html and rewrite it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Run started a fresh aggregation: it covers exactly the invocations IT
	// made — its own exhausted full-artifact bounded step plus the single
	// recovery attempt that staged the patch.
	in, out, known := driver.AggregatedUsage()
	if !known {
		t.Fatal("usage unknown")
	}
	aggIn := exhaustedFullArtifactStepCount*2182 + bounded.Usage.PromptTokens
	aggOut := exhaustedFullArtifactStepCount*1022 + bounded.Usage.CompletionTokens
	if in != aggIn || out != aggOut {
		t.Fatalf("aggregate = %d/%d, want %d/%d", in, out, aggIn, aggOut)
	}
	// 1 explicit exhausted bounded step + 1 via Run (its own exhausted step
	// plus the single recovery attempt that staged the patch).
	if want := 2*exhaustedFullArtifactStepCount + 1; mock.calls() != want {
		t.Fatalf("calls = %d, want %d (one count per logical invocation)", mock.calls(), want)
	}
}

// exhaustedFullArtifactStep builds the provider responses ONE full-artifact
// attempt costs when the model never stops: the initial bounded invocation plus
// every affordable bounded continuation (llmstep.DefaultMaxContinuationSteps).
//
// PHASE 12 (Task != Invocation): a full-artifact generation is a LOGICAL task
// that continues the SAME artifact contract across bounded MODEL INVOCATIONS
// before the Driver ever sees an exhaustion. Tests that exercise the
// Driver-driven contract transition must therefore supply a whole exhausted
// step before their typed-transition response.
func exhaustedFullArtifactStep(content string, promptTokens, completionTokens int) []*ai.Response {
	out := make([]*ai.Response, 0, 1+llmstep.DefaultMaxContinuationSteps)
	for i := 0; i <= llmstep.DefaultMaxContinuationSteps; i++ {
		out = append(out, &ai.Response{
			Content: content,
			Usage:   ai.ProviderUsage{PromptTokens: promptTokens, CompletionTokens: completionTokens, Known: true, FinishReason: "length"},
		})
	}
	return out
}

// exhaustedFullArtifactStepCount is the number of provider invocations one
// never-completing full-artifact attempt costs under the bounded-step contract.
const exhaustedFullArtifactStepCount = 1 + llmstep.DefaultMaxContinuationSteps

// ── shared helpers ──────────────────────────────────────────────────────────

func harnessWithAdapter(t *testing.T, root string, responses ...*ai.Response) (*mockProvider, *execution.RuntimeExecutor, *ExecutorAdapter, *events.Bus) {
	t.Helper()
	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	return mock, x, NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus
}

func runObjectiveCompact(t *testing.T, d *Driver) {
	t.Helper()
	if _, err := d.Run(context.Background(), "check this file @index.html and rewrite it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
