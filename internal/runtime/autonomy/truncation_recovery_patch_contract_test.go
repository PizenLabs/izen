package autonomy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// bigIndexHTML returns deterministic HTML of EXACTLY 7780 bytes (the
// production repro target) with stable per-line anchors a bounded patch can
// target. The fixture is built once and reused: it sits at the center of
// nearly every regression here.
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
			cand = append(cand, fmt.Sprintf(`<p class="line-%d">filler content line %d</p>`, i, i))
			cand = append(cand, tail...)
			if len(strings.Join(cand, "\n"))+1 > want {
				break
			}
			mid = append(mid, fmt.Sprintf(`<p class="line-%d">filler content line %d</p>`, i, i))
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
	return fmt.Sprintf(`<p class="line-%d">filler content line %d</p>`, n, n)
}

// tailLine is the LAST filler line of the fixture. It appears in the complete
// document but can never appear inside a bounded-patch context window (windows
// are capped far below the file size), so it proves whether the whole file
// crossed to the model.
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

func newDriver(t *testing.T, root string, responses ...*ai.Response) (*mockProvider, *execution.RuntimeExecutor, *Driver) {
	t.Helper()
	mock := &mockProvider{responses: responses}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return mock, x, NewDriver(adapter, bus, WithLoopBounds(generousBounds()))
}

func runObjective(t *testing.T, d *Driver) {
	t.Helper()
	if _, err := d.Run(context.Background(), "check this file @index.html and rewrite it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// ── 1. wire-contract change between attempt 1 and attempt 2 ────────────────

// TestTruncationChangesWireContract proves the recovery invocation differs
// MATERIALLY from the initial invocation at the wire level: different system
// contract, different artifact kind, different output contract, runtime-derived
// bounded context instead of the full file, unchanged budget, and a distinct
// attempt identity.
func TestTruncationChangesWireContract(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(3, `<p class="line-3">rewritten content line 3</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 180, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newDriver(t, root, truncated, bounded)
	runObjective(t, driver)

	reqs := mock.recordedRequests()
	if len(reqs) != 2 {
		t.Fatalf("recorded requests = %d, want 2", len(reqs))
	}
	a1, a2 := reqs[0], reqs[1]

	// System contracts differ: tolerant full-artifact vs strict patch-only.
	if !strings.Contains(a1.System, "The full modified file content") {
		t.Fatalf("attempt 1 lost the tolerant full-artifact contract: %q", a1.System)
	}
	if strings.Contains(strings.ToLower(a2.System), "full modified file") {
		t.Fatal("attempt 2 still offers the full-file option")
	}

	// The recovery USER message must not carry the complete file: a sentinel
	// from the deep tail of the document must be absent.
	u1, u2 := a1.Messages[0].Content, a2.Messages[0].Content
	if !strings.Contains(u1, tailLine) {
		t.Fatal("attempt 1 should embed the complete target file")
	}
	if strings.Contains(u2, tailLine) {
		t.Fatal("attempt 2 STILL carries the complete file — input contract unchanged")
	}
	if len(u2) >= len(u1) {
		t.Fatalf("attempt 2 prompt (%d bytes) is not materially smaller than attempt 1 (%d bytes)", len(u2), len(u1))
	}
	if !strings.Contains(u2, "OUTPUT FORMAT") || !strings.Contains(u2, "<<<<<<< SEARCH") {
		t.Fatalf("attempt 2 user message missing the patch protocol: %q", u2[:min(len(u2), 300)])
	}
	if !strings.Contains(u2, "[RECOVERY bounded_patch:") {
		t.Fatal("attempt 2 missing the recovery annotation")
	}

	// Budget unchanged; only the representation changed.
	if a2.MaxTokens != 1024 || a1.MaxTokens != 1024 {
		t.Fatalf("max_tokens a1=%d a2=%d, want both 1024", a1.MaxTokens, a2.MaxTokens)
	}
}

// ── 2. bounded request never asks for the full artifact ────────────────────

// TestBoundedPatchDoesNotRequestFullArtifact pins the INPUT side of the
// bounded contract: no rewrite instruction over the complete file, no
// full-file option anywhere, an explicitly bounded copyable source, and a
// one-block-only output format.
func TestBoundedPatchDoesNotRequestFullArtifact(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(5, `<p class="line-5">patched line 5</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 150, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newDriver(t, root, truncated, bounded)
	runObjective(t, driver)

	reqs := mock.recordedRequests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want >=2", len(reqs))
	}
	sys, user := reqs[1].System, reqs[1].Messages[0].Content

	lowerSys, lowerUser := strings.ToLower(sys), strings.ToLower(user)
	for _, forbidden := range []string{"full modified file", "rewrite the file", "provide the full new content"} {
		if strings.Contains(lowerSys, forbidden) || strings.Contains(lowerUser, forbidden) {
			t.Fatalf("bounded recovery requests %q — forbidden phrase present", forbidden)
		}
	}
	if !strings.Contains(user, "Do NOT regenerate the document") {
		t.Fatalf("bounded request does not negate the rewrite instruction: %q", user[:min(len(user), 200)])
	}
	if !strings.Contains(user, "the ONLY lines you may touch") {
		t.Fatal("bounded request does not declare the bounded copyable source")
	}
	if strings.Contains(user, tailLine) {
		t.Fatal("bounded request leaked more than the declared window")
	}
}

// ── 3+7. small budget completion and successful end-to-end recovery ────────

// TestBoundedPatchFitsSmallOutputBudget proves a compliant patch completes far
// below the 1024-token ceiling and stages successfully.
func TestBoundedPatchFitsSmallOutputBudget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(6, `<p class="line-6">patched line 6</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 96, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newDriver(t, root, truncated, bounded)
	runObjective(t, driver)

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
// truncate on attempt 1, recover with ONE bounded patch on attempt 2, stage,
// approve, apply, VERIFY — without exhausting any bound.
func TestSuccessfulRecoveryFromRealTruncation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(7, `<p class="line-7">verified rewrite line 7</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 2277, CompletionTokens: 120, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newDriver(t, root, truncated, bounded)
	runObjective(t, driver)

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
	if !strings.Contains(got, `>verified rewrite line 7<`) {
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
	before := bigIndexHTML()
	writeTarget(t, root, "index.html", before)

	truncated := &ai.Response{
		Content: before[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	fullFile := func(in int) *ai.Response {
		return &ai.Response{
			Content: "<!DOCTYPE html>\n<html><body>complete replacement</body></html>\n",
			Usage:   ai.ProviderUsage{PromptTokens: in, CompletionTokens: 300, Known: true, FinishReason: "stop"},
		}
	}
	mock, _, driver := newDriver(t, root, truncated, fullFile(2277), fullFile(2353))
	runObjective(t, driver)

	reqs := mock.recordedRequests()
	for i, r := range reqs[1:] {
		norm := strings.Join(strings.Fields(strings.ToLower(r.System)), " ")
		if strings.Contains(norm, "full modified file") ||
			!strings.Contains(r.System, "<<<<<<< SEARCH") {
			t.Fatalf("recovery request #%d regressed to the full-artifact contract: %q", i+2, r.System)
		}
		if strings.Contains(r.Messages[0].Content, tailLine) && len(r.Messages[0].Content) > 4000 {
			t.Fatalf("recovery request #%d carried the complete file despite bounded context", i+2)
		}
		if r.MaxTokens != 1024 {
			t.Fatalf("recovery request #%d raised the budget to %d", i+2, r.MaxTokens)
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

// TestAttemptThreeDoesNotRegressToFullArtifact pins the LATCH plus materiality:
// once bounded_patch engages, attempt 3 keeps the strict contract AND its
// request content is genuinely different from attempt 2 (rotated runtime
// window + accumulated failure evidence) — never a re-send of the identical
// request under a new annotation.
func TestAttemptThreeDoesNotRegressToFullArtifact(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	junkPatch := func(in int) *ai.Response { // well-formed marker, unmatchable anchor
		return &ai.Response{
			Content: "<<<<<<< SEARCH\nNO SUCH LINE IN FILE\n=======\nreplacement\n>>>>>>>",
			Usage:   ai.ProviderUsage{PromptTokens: in, CompletionTokens: 60, Known: true, FinishReason: "stop"},
		}
	}
	mock, _, driver := newDriver(t, root, truncated, junkPatch(2277), junkPatch(2330))
	runObjective(t, driver)

	reqs := mock.recordedRequests()
	if len(reqs) < 3 {
		t.Fatalf("requests = %d, want >=3", len(reqs))
	}
	a2u, a3u := reqs[1].Messages[0].Content, reqs[2].Messages[0].Content
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

// ── 8. no duplicate logical invocations ────────────────────────────────────

// TestRecoveryDoesNotDuplicateProviderInvocation proves transport retries are
// not counted as extra logical executions: one HTTP-attempt-heavy provider
// call is still exactly ONE ModelInvocation and ONE aggregated count.
func TestRecoveryDoesNotDuplicateProviderInvocation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 1000, CompletionTokens: 500, Known: true, FinishReason: "length", HTTPAttempts: 3, RateLimitedRetries: 2},
	}
	bounded := &ai.Response{
		Content: patchForLine(8, `<p class="line-8">deduped line 8</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 1100, CompletionTokens: 90, Known: true, FinishReason: "stop", HTTPAttempts: 2, RateLimitedRetries: 1},
	}
	mock, x, driver := newDriver(t, root, truncated, bounded)
	runObjective(t, driver)

	if mock.calls() != 2 {
		t.Fatalf("provider invocations = %d, want exactly one per loop attempt (2)", mock.calls())
	}
	in, out, known := driver.AggregatedUsage()
	if !known || in != 2100 || out != 590 {
		t.Fatalf("aggregate = %d/%d known=%v, want 2100/590 true (one count per logical invocation)", in, out, known)
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
	writeTarget(t, root, "index.html", bigIndexHTML())

	truncated := &ai.Response{
		Content: bigIndexHTML()[:6000],
		Usage:   ai.ProviderUsage{PromptTokens: 2182, CompletionTokens: 1022, Known: true, FinishReason: "length"},
	}
	bounded := &ai.Response{
		Content: patchForLine(9, `<p class="line-9">counted line 9</p>`),
		Usage:   ai.ProviderUsage{PromptTokens: 2303, CompletionTokens: 140, Known: true, FinishReason: "stop"},
	}
	mock, _, driver := newDriver(t, root, truncated, bounded)

	// Direct first attempt through the adapter: the observation must preserve
	// the authoritative finish_reason and usage verbatim.
	obs, err := driver.adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:  "check this file @index.html and rewrite it",
		Targets: []string{"index.html"},
	})
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if obs.FinishReason != "length" || obs.OutputTokens != 1022 {
		t.Fatalf("observation = %d toks finish=%q, want 1022/length", obs.OutputTokens, obs.FinishReason)
	}

	runObjective(t, driver)
	// Run started a fresh aggregation: it covers exactly the invocations IT
	// made (the recovery attempt that staged the patch).
	in, out, known := driver.AggregatedUsage()
	if !known {
		t.Fatal("usage unknown")
	}
	if in != 2303 || out != 140 {
		t.Fatalf("aggregate = %d/%d, want 2303/140", in, out)
	}
	if mock.calls() != 2 { // 1 explicit + 1 via Run
		t.Fatalf("calls = %d, want 2 (one count per logical invocation)", mock.calls())
	}
}
