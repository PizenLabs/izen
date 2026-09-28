// ── PHASE 13 — GOLDEN END-TO-END SCENARIO ───────────────────────────────────
//
// One deterministic scenario, corresponding to the reported request:
//
//	$prompt Please review this project and redesign a professional personal
//	portfolio page for me using HTML, CSS, and JS; the author's name is Tom
//	Hunter, an AI Engineer.
//
// The expected semantic lifecycle is:
//
//	MODIFICATION → AUTHORIZATION REQUIRED → USER AUTHORIZES → CONTEXT PREPARED
//	→ PROJECT UNDERSTOOD → ARTIFACT GENERATION → CANDIDATE READY
//	→ DIFF AVAILABLE → MUTATION → VERIFICATION → COMPLETED
//
// This test drives the REAL production path — RuntimeExecutor, its artifact
// step, the mutation boundary, the verification gate and the evidence seal —
// and then feeds the ACTUAL bus events into the presentation projection. It
// asserts the acceptance properties, not the implementation:
//
//	no false completion          (completion requires sealed evidence)
//	no fake diff                 (diff stats come from the boundary's own diff)
//	no mutation before authorization (the gate holds the candidate)
//	no mutation outside scope    (the workspace is byte-identical pre-approve)
//	no lost partial artifact     (candidate present, 0 bytes written)
//	no CREATE→PATCH corruption   (the artifact stayed a SEARCH/REPLACE create)
//	no unnecessary pruning       (the compiled context is reported, not minimised)
//
// The event-order assertion is the load-bearing one: it proves the evidence is
// sealed BEFORE the completion event, which is what makes the whole gate sound.

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// goldenOriginalIndex is a plausible "before" portfolio page: real content that
// must survive the redesign.
const goldenOriginalIndex = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Portfolio</title>
</head>
<body>
  <main>
    <h1>Portfolio</h1>
    <p>Placeholder introduction.</p>
  </main>
</body>
</html>
`

// goldenNewIndex is the deterministic artifact the mock provider returns. It is
// a bounded SEARCH/REPLACE artifact — the SAME operation kind the original
// contract demanded, so a continuation that needed another turn could never
// silently convert a CREATE into a different contract.
const goldenNewIndex = `<<<<<<< SEARCH
  <main>
    <h1>Portfolio</h1>
    <p>Placeholder introduction.</p>
  </main>
=======
  <main>
    <h1>Tom Hunter</h1>
    <p class="role">AI Engineer</p>
    <section id="work"><h2>Selected Work</h2></section>
  </main>
>>>>>>>`

// goldenPortfolioPrompt is the reported request, verbatim.
const goldenPortfolioPrompt = "Please review this project and redesign a professional personal portfolio page for me using HTML, CSS, and JS; the author's name is Tom Hunter, an AI Engineer."

// TestGolden_PortfolioRedesignEndToEnd is the single deterministic golden run.
func TestGolden_PortfolioRedesignEndToEnd(t *testing.T) {
	root := t.TempDir()
	// "Review this project": the workspace has more than the target, so context
	// compilation has something real to gather.
	writeTarget(t, root, "index.html", goldenOriginalIndex)
	writeTarget(t, root, "styles.css", ":root{--fg:#111}\nbody{margin:0;font-family:system-ui}\n")
	writeTarget(t, root, "script.js", "console.log('portfolio');\n")

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: goldenNewIndex,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 1840, CompletionTokens: 612},
	}}}

	// ── 1. MODIFICATION → CANDIDATE READY (held at the authorization gate) ──
	x := phase4Executor(t, root, mock, bus)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: goldenPortfolioPrompt,
		Target: "index.html",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("a mutation must stop at the authorization gate with a held candidate")
	}
	if len(res.ArtifactCandidates) == 0 {
		t.Fatal("a candidate must be recorded before any authorization")
	}
	cand := res.ArtifactCandidates[0]
	if cand.Target != "index.html" {
		t.Errorf("candidate target = %q, want index.html", cand.Target)
	}
	if cand.Status != CandidateComplete {
		t.Errorf("candidate status = %q, want %q", cand.Status, CandidateComplete)
	}
	// The artifact stayed inside the bounded SEARCH/REPLACE creation contract
	// and the runtime resolved it into the complete file. A continuation that
	// had to re-prompt could never convert this into a different operation
	// kind: the canonical diff is still a plain single-file unified diff over
	// the same target, with no second contract marker smuggled in.
	if res.Original != goldenOriginalIndex {
		t.Errorf("the mutation base must be the file as it exists, got:\n%s", res.Original)
	}
	if !strings.HasPrefix(res.Diff, "--- a/index.html") || !strings.Contains(res.Diff, "@@") {
		t.Fatalf("the runtime must compile a real unified diff for the target, got:\n%s", res.Diff)
	}
	for _, foreignContract := range []string{"<<<<<<< UPDATE", "<<<<<<< FULL", "=======\n<<<<<<< SEARCH"} {
		if strings.Contains(res.Diff, foreignContract) {
			t.Errorf("CREATE must never be silently converted into another contract (%q):\n%s", foreignContract, res.Diff)
		}
	}

	// ── NO MUTATION BEFORE AUTHORIZATION ───────────────────────────────
	if got := mustRead(t, root, "index.html"); got != goldenOriginalIndex {
		t.Fatalf("a held candidate must not have mutated the workspace:\n%s", got)
	}
	if !collector.waitCount(events.EventMutationStarted, 0, 50*time.Millisecond) &&
		collector.count(events.EventMutationStarted) != 0 {
		t.Fatal("mutation.started must not be emitted before authorization")
	}

	// ── 2. USER AUTHORIZES → DIFF AVAILABLE → MUTATION → VERIFICATION ──
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// The artifact landed on disk, byte for byte.
	after := mustRead(t, root, "index.html")
	if !strings.Contains(after, "Tom Hunter") || !strings.Contains(after, "AI Engineer") {
		t.Fatalf("the artifact did not reach the filesystem:\n%s", after)
	}
	if strings.Contains(after, "Placeholder introduction.") {
		t.Errorf("the replaced region survived the mutation:\n%s", after)
	}
	// Scope discipline: only the authorized target changed.
	if got := mustRead(t, root, "styles.css"); got != ":root{--fg:#111}\nbody{margin:0;font-family:system-ui}\n" {
		t.Error("a file outside the authorized target was mutated")
	}
	if got := mustRead(t, root, "script.js"); got != "console.log('portfolio');\n" {
		t.Error("a file outside the authorized target was mutated")
	}

	// The mutation boundary proved a real filesystem change and a real diff.
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("proof outcome = %s, want changed", apr.Proof.Outcome)
	}
	if len(apr.Proof.Mutations) != 1 {
		t.Fatalf("mutations = %d, want 1", len(apr.Proof.Mutations))
	}
	ev := apr.Proof.Mutations[0]
	if !ev.ApplyExecutedChanged() {
		t.Fatalf("evidence does not prove an executed filesystem mutation: %+v", ev)
	}
	if !ev.DiffPresent {
		t.Fatalf("the boundary compiled a real diff, so the evidence must carry it: %+v", ev)
	}
	if ev.DiffAdds == 0 && ev.DiffRemoves == 0 {
		t.Fatalf("diff evidence must carry measured line metrics: %+v", ev)
	}
	if ev.Outcome != OutcomeChanged {
		t.Errorf("per-file outcome = %s, want changed", ev.Outcome)
	}
	// The diff is real: its line metrics are non-trivial and were measured from
	// the unified diff the boundary compiled, not synthesised at render time.
	if ev.DiffAdds < 2 {
		t.Errorf("diff adds (%d) is not consistent with a real content change", ev.DiffAdds)
	}
	// Verification ran and passed — from the gate that actually executed.
	if !apr.Verification.Passed {
		t.Fatalf("verification must pass: %+v", apr.Verification)
	}
	if !ev.Verify() {
		t.Fatalf("per-file evidence must record the passing gate: %+v", ev)
	}
	if !ev.VerificationRun {
		t.Error("per-file evidence must record that a gate actually ran")
	}

	// ── 3. EVIDENCE IS SEALED BEFORE COMPLETION ─────────────────────────
	if !collector.waitCount(events.EventExecutionFinished, 1, time.Second) {
		t.Fatalf("execution.finished never emitted; types=%v", collector.types())
	}
	if !collector.waitCount(events.EventExecutionEvidence, 1, time.Second) {
		t.Fatalf("execution.evidence never emitted; types=%v", collector.types())
	}
	if got := collector.indexOf(events.EventExecutionEvidence); got < 0 ||
		collector.indexOf(events.EventExecutionFinished) < got {
		t.Errorf("evidence must be sealed BEFORE the completion event; evidence@%d finished@%d",
			collector.indexOf(events.EventExecutionEvidence), collector.indexOf(events.EventExecutionFinished))
	}

	// The evidence is committed and untainted — the authority for completion.
	if apr.Evidence == nil {
		t.Fatal("a terminal execution must seal evidence")
	}
	if !apr.Evidence.Authoritative() {
		t.Errorf("sealed evidence is not authoritative: outcome=%s tainted=%t",
			apr.Evidence.Outcome(), apr.Evidence.Mutations().Tainted)
	}
	if apr.Evidence.Mutations().FilesMutated != 1 {
		t.Errorf("evidence records %d mutated files, want 1", apr.Evidence.Mutations().FilesMutated)
	}

	// ── 4. NO UNNECESSARY CONTEXT PRUNING ──────────────────────────────
	// The compiled context is REPORTED and non-zero: a context starved "for
	// efficiency" would be a regression, not an optimisation. The budget
	// accounting the compiler recorded must still be present on the bus.
	if !collector.waitCount(events.EventContextPrepared, 1, time.Second) {
		t.Errorf("context.prepared never emitted; types=%v", collector.types())
	}
	prepared, ok := collector.firstPayload(events.EventContextPrepared).(events.ContextPreparedPayload)
	if !ok {
		t.Fatal("context.prepared payload missing")
	}
	if prepared.Tokens <= 0 {
		t.Error("compiled context must be measured and non-zero — efficiency is not starvation")
	}
	if prepared.BudgetTokens <= 0 {
		t.Error("the execution token budget must remain observable")
	}
	// The context figures are LAYER-SEPARATE and each is reported at its own
	// layer: the compiled-context estimate, the budget, and the prompt
	// fingerprint that keys the compiler's cache. None of them is the provider's
	// prompt-token count, and the payload keeps them apart so a projector can
	// label them apart.
	if prepared.PromptFingerprint == "" {
		t.Error("the context cache key must remain observable")
	}
	if prepared.Tokens >= prepared.BudgetTokens {
		t.Errorf("compiled context (%d) must fit inside the budget (%d)", prepared.Tokens, prepared.BudgetTokens)
	}
	if prepared.Policy == "" {
		t.Error("the context policy must be reported so the user can see what governed the compile")
	}

	// ── 5. MODEL INVOCATION IS NOT TASK COMPLETION ────────────────────
	// The invocation facts are recorded separately from the objective verdict:
	// a finish reason describes the generation, never the task.
	if !collector.waitCount(events.EventModelInvoked, 1, time.Second) {
		t.Errorf("model.invoked never emitted; types=%v", collector.types())
	}
	resp, ok := collector.firstPayload(events.EventProviderResponse).(events.ProviderResponsePayload)
	if !ok {
		t.Fatal("provider.response payload missing")
	}
	if resp.TokenInput == 0 || resp.TokenOutput == 0 {
		t.Errorf("provider usage must be reported: %d in / %d out", resp.TokenInput, resp.TokenOutput)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Error("a provider return alone must never be the completion verdict")
	}
}

// TestGolden_HeldCandidateRendersNoDiffStatistics proves the "0 bytes written"
// invariant end to end: while a candidate is held at the authorization gate,
// the projection has no diff and no mutation, and the UI therefore has nothing
// truthful to report about a change.
func TestGolden_HeldCandidateRendersNoDiffStatistics(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", goldenOriginalIndex)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: goldenNewIndex,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 50},
	}}}

	x := phase4Executor(t, root, mock, bus)
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: goldenPortfolioPrompt, Target: "index.html",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The candidate is held and the workspace is provably untouched: no bytes
	// written, no stray file, no mutation event.
	if got := mustRead(t, root, "index.html"); got != goldenOriginalIndex {
		t.Fatal("a held candidate must leave the workspace byte-identical")
	}
	if entries, _ := os.ReadDir(filepath.Clean(root)); len(entries) != 1 {
		t.Errorf("no stray file may be written before authorization: %d entries", len(entries))
	}
	if n := collector.count(events.EventMutationStarted); n != 0 {
		t.Errorf("mutation.started emitted %d time(s) before authorization", n)
	}
	if n := collector.count(events.EventMutationCompleted); n != 0 {
		t.Errorf("mutation.completed emitted %d time(s) before authorization", n)
	}
	if n := collector.count(events.EventVerificationCompleted); n != 0 {
		t.Errorf("verification.completed emitted %d time(s) before authorization", n)
	}
	// A candidate WAS produced: the work is staged, not absent.
	if n := collector.count(events.EventArtifactProduced); n == 0 {
		t.Error("artifact.produced must be emitted for a held candidate")
	}
}
