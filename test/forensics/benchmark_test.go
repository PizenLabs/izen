package forensics_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/forensics"
	rtAutonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// ── THE EXECUTION BENCHMARK HARNESS ─────────────────────────────────────────
//
// Every benchmark in this package drives the SAME objects production wires:
//
//	compose.Wire → runtimeAutonomy.NewDriver(adapter, bus)
//	  → ExecutorAdapter → execution.RuntimeExecutor → ai.Provider
//
// The only substituted component is the provider, and it is substituted
// DELIBERATELY and SEPARATELY: a scripted provider makes a run reproducible, a
// real local model makes it real. Both run through this harness, so "the runtime
// is correct" and "the model behaved" are never confused for one another.
//
// Nothing here asserts an EXPECTED call count and calls that a pass. Each
// benchmark records what actually happened and asserts only the invariants the
// architecture claims to hold structurally. A benchmark that asserted the answer
// it hoped for would be the exact failure mode this investigation exists to
// catch.

// ── Providers ───────────────────────────────────────────────────────────────

// scriptedProvider answers from a fixed script and records the ACTUAL contract
// of every invocation: the prompt bytes, the requested output budget, and the
// usage the provider claims.
//
// Recording the request is what makes budget questions answerable at all: the
// runtime's own log line is not evidence, but the request object that crossed
// the boundary is.
type scriptedProvider struct {
	name      string
	responses []*ai.Response
	err       error

	mu       sync.Mutex
	calls    int
	requests []ai.Request
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	if len(p.responses) == 0 {
		return nil, fmt.Errorf("scripted provider %q: no response scripted for call #%d", p.name, p.calls)
	}
	if p.calls > len(p.responses) {
		return nil, fmt.Errorf("scripted provider %q: unexpected call #%d (script has %d)", p.name, p.calls, len(p.responses))
	}
	return p.responses[p.calls-1], nil
}

func (p *scriptedProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported by the scripted provider")
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *scriptedProvider) recorded() []ai.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.Request(nil), p.requests...)
}

// requestedBudgets returns the max_tokens of every invocation, in order.
func (p *scriptedProvider) requestedBudgets() []int {
	reqs := p.recorded()
	out := make([]int, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.MaxTokens)
	}
	return out
}

// truncatedAt builds a response that reports the provider cut generation at the
// ceiling: provider-reported usage at exactly `budget` completion tokens and
// finish_reason="length". That pairing IS the observed proof of the effective
// ceiling — not an estimate of it.
func truncatedAt(budget int) *ai.Response {
	return &ai.Response{
		Content: "partial output that stops mid-sentence because the provider hit its",
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     400,
			CompletionTokens: budget,
			FinishReason:     "length",
		},
		Truncated: true,
	}
}

func answered(promptTokens, completionTokens int, content string) *ai.Response {
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

// ── Harness ─────────────────────────────────────────────────────────────────

// run is the outcome of one benchmark run: the reconstructed trace plus the
// live objects a test needs to assert against the WORKSPACE, not the trace.
type run struct {
	t      *testing.T
	Root   string
	Bus    *events.Bus
	Driver *rtAutonomy.Driver
	Prov   *scriptedProvider
	Trace  *forensics.Trace

	sub      *events.Subscription
	recorder *forensics.Recorder
}

// harness wires the real driver over the real executor against a temp workspace.
func harness(t *testing.T, p *scriptedProvider) *run {
	t.Helper()
	return harnessOn(t, t.TempDir(), events.NewBus(events.DefaultBufferSize), p)
}

// harnessOn is harness over a caller-supplied workspace and bus.
//
// Both are parameters because the persistence test must put the REAL audit
// logger on the SAME bus the runtime publishes to — wiring a private bus here
// and attaching the logger to another would produce a log that never saw the
// run, and a passing test that proved nothing.
func harnessOn(t *testing.T, root string, bus *events.Bus, p *scriptedProvider) *run {
	t.Helper()

	rec := forensics.NewRecorder()
	sub := bus.SubscribeAll(rec.Handle)

	x := execution.NewRuntimeExecutor(root, config.Default(), p, bus, "")
	// Verification is the language-aware gate over a real temp workspace; a
	// no-op verifier would make every mutation "verified" and destroy the very
	// distinction the evidence section of the trace exists to report.
	x.SetVerifier(execution.NewVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	a := rtAutonomy.NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	// The mutation target every benchmark in this file operates on. Creating it
	// here keeps `harnessOn` honest for the persistence test, which never calls
	// `write` — a target that only exists because a benchmark remembered to make
	// it is a target the next test would silently lose.
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("foo\nbar\nbaz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &run{
		t:        t,
		Root:     root,
		Bus:      bus,
		Driver:   rtAutonomy.NewDriver(a, bus),
		Prov:     p,
		recorder: rec,
		sub:      sub,
	}
}

// trace stops recording and reconstructs the run. It is the single place the
// trace is built, so a live trace and a post-mortem trace cannot diverge.
//
// The drain is not optional: the bus delivers on its own goroutine, so reading
// without waiting drops exactly the tail that decides the verdict. See
// forensics.Recorder for why a reader that races its own publisher is worse than
// no reader at all.
func (r *run) trace() *forensics.Trace {
	// Every bounded run publishes at least one terminal summary. Wait for it,
	// then let the queue go quiet before cancelling the subscription.
	r.recorder.WaitFor(events.EventExecutionSummary, 1, 10*time.Second)
	r.recorder.WaitQuiet(50*time.Millisecond, 2*time.Second)
	r.sub.Cancel()

	tr, err := forensics.NewTrace(r.recorder.Stream())
	if err != nil {
		r.t.Fatalf("reconstruct trace: %v", err)
	}
	r.Trace = tr
	return tr
}

// write writes a workspace file.
func (r *run) write(name, content string) {
	r.t.Helper()
	p := filepath.Join(r.Root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// read reads a workspace file; a missing file is the empty string, because
// "the file was not created" is an outcome a benchmark must be able to assert
// without distinguishing it from a read error.
func (r *run) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Root, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func (r *run) exists(name string) bool {
	_, err := os.Stat(filepath.Join(r.Root, name))
	return err == nil
}

// report prints the full forensic trace and returns it. Every benchmark prints
// its trace so the evidence that decided the verdict is always in the log — a
// benchmark that fails with only an assertion message is the pattern this
// investigation is trying to eliminate.
func (r *run) report() *forensics.Trace {
	tr := r.trace()
	r.t.Logf("\n%s", tr.Render())
	return tr
}

const searchReplacePong = `<<<<<<< SEARCH
bar
=======
qux
>>>>>>> REPLACE`

// ── BENCHMARK A — SINGLE MODEL RESPONSE ─────────────────────────────────────
//
// Objective: the cheapest possible complete lifecycle. One mutation, one call,
// one verification, one proof.
//
// What it tests: that the runtime can finish WITHOUT needing a second call. A
// harness that only ever demonstrates recovery has never demonstrated success.

func TestBenchmarkA_SingleModelResponse(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(1800, 260, searchReplacePong),
	}}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Driver.State() != autonomy.RuntimeAwaitingHuman {
		r.report()
		t.Fatalf("state = %s, want awaiting_human at the approval gate", r.Driver.State())
	}
	// The approval gate must be a REAL gate: nothing may be written before the
	// human answers.
	if got := r.read("note.txt"); got != "foo\nbar\nbaz\n" {
		r.report()
		t.Fatalf("file mutated BEFORE approval: %q", got)
	}
	term, err := r.Driver.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		r.report()
		t.Fatalf("termination = %v, want completed", term)
	}

	tr := r.report()

	// ── Recorded facts (printed, not asserted into existence) ───────────
	t.Logf("provider calls: %d, requested budgets: %v", p.callCount(), p.requestedBudgets())

	// ── Structural invariants ───────────────────────────────────────────
	// These are the properties the architecture claims to hold regardless of
	// what the model said, so they are safe to assert.
	if len(tr.Patterns) != 0 {
		t.Fatalf("clean lifecycle exhibited patterns %v\n%s", tr.Patterns, tr)
	}
	if tr.Summary.Status != "completed" {
		t.Fatalf("summary status = %q, want completed\n%s", tr.Summary.Status, tr)
	}
	if got := r.read("note.txt"); !strings.Contains(got, "qux") {
		t.Fatalf("approved mutation did not land: %q\n%s", got, tr)
	}
	if len(tr.Verifications) == 0 {
		t.Fatalf("a committed mutation produced no verification record\n%s", tr)
	}
	if len(tr.Mutations) != 1 {
		t.Fatalf("mutations = %d, want exactly 1\n%s", len(tr.Mutations), tr)
	}
	// The completion must be attributable to evidence, not to a provider that
	// merely returned.
	if len(tr.ObjectiveStates) == 0 {
		t.Fatalf("no objective evaluation was recorded\n%s", tr)
	}
}

// ── BENCHMARK B — OUTPUT EXHAUSTION ─────────────────────────────────────────
//
// The §5 investigation: a model that terminates at its ceiling. The provider is
// scripted to report a provider-authoritative completion count EXACTLY equal to
// its ceiling and finish_reason="length", which is the observed proof of the
// effective budget.
//
// What it tests: that exhaustion is EXPOSED as exhaustion, that the requested
// and effective budgets are both recoverable, and that the runtime's response is
// a real continuation rather than a replay.

func TestBenchmarkB_OutputExhaustion(t *testing.T) {
	const ceiling = 1024
	p := &scriptedProvider{name: "constrained-free-tier", responses: []*ai.Response{
		truncatedAt(ceiling),
		answered(1800, 260, searchReplacePong),
	}}
	r := harness(t, p)

	term, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := r.report()

	t.Logf("termination: %v", fmtTerm(term))
	t.Logf("model calls: %d, requested budgets: %v", p.callCount(), p.requestedBudgets())
	t.Logf("output_exhausted recorded: %d", tr.OutputExhausted)

	// ── THE §5 ASSERTION ────────────────────────────────────────────────
	// Exhaustion must be observable as exhaustion. A runtime that reports a cut
	// generation as an ordinary completion has destroyed the only signal that
	// distinguishes "the model finished" from "the model was stopped".
	if tr.OutputExhausted == 0 {
		t.Fatalf("a finish_reason=length response was NOT recorded as output exhaustion\n%s", tr)
	}
	// The effective budget must be OBSERVED, not assumed: the provider-reported
	// completion count at truncation IS the ceiling it enforced.
	var exhausted *events.ProviderExecutionPayload
	for i := range tr.ModelCalls {
		if tr.ModelCalls[i].Truncated || tr.ModelCalls[i].FinishReason == "length" {
			exhausted = &tr.ModelCalls[i]
			break
		}
	}
	if exhausted == nil {
		t.Fatalf("no truncated provider record in the trace\n%s", tr)
	}
	if !exhausted.EffectiveOutputKnown {
		t.Fatalf("truncated call did not observe an effective budget\n%s", tr)
	}
	if exhausted.EffectiveOutputTokens != ceiling {
		t.Fatalf("effective output budget = %d, want the observed ceiling %d\n%s",
			exhausted.EffectiveOutputTokens, ceiling, tr)
	}
	t.Logf("requested=%d effective=%d finish=%s",
		exhausted.RequestedOutputTokens, exhausted.EffectiveOutputTokens, exhausted.FinishReason)

	// ── What the runtime DID is reported, not assumed ────────────────────
	if p.callCount() > 1 {
		second := p.recorded()[1]
		t.Logf("CONTINUATION OBSERVED: call #2 re-issued the request (identical prompt: %t, budget %d)",
			ai.RequestFingerprint(second) == ai.RequestFingerprint(p.recorded()[0]),
			second.MaxTokens)
	}
	if p.callCount() > 1 {
		for _, pat := range tr.Patterns {
			if pat == forensics.PatternRepeatedIdentical {
				t.Fatalf("exhaustion produced REPEATED_IDENTICAL_EXECUTION: %d calls, identical request\n%s",
					p.callCount(), tr)
			}
		}
	}
}

// ── BENCHMARK D — NO-OP COMPLETION ──────────────────────────────────────────
//
// A task that requires no mutation. What it tests: that the runtime does not
// INVENT work. An agent that cannot be trusted to do nothing is an agent that
// cannot be trusted to do anything.

func TestBenchmarkD_NoOpCompletion(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(900, 140, "note.txt contains three lines: foo, bar, baz. No change is required."),
	}}
	r := harness(t, p)

	term, err := r.Driver.Run(context.Background(), "explain what @note.txt contains")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := r.report()

	t.Logf("termination: %v", fmtTerm(term))
	t.Logf("model calls: %d, mutations: %d", p.callCount(), len(tr.Mutations))

	// INVARIANT: a read-only objective must not produce a mutation. This is the
	// single most important negative assertion in the suite — an invented
	// mutation is not a cosmetic defect, it is the failure the whole workspace
	// contract exists to prevent.
	if len(tr.Mutations) != 0 {
		t.Fatalf("read-only objective produced %d mutation(s)\n%s", len(tr.Mutations), tr)
	}
	for _, pat := range tr.Patterns {
		if pat == forensics.PatternUnverifiedMutation || pat == forensics.PatternCompletionWithoutEvidence {
			t.Fatalf("read-only run flagged %s\n%s", pat, tr)
		}
	}
}

// ── BENCHMARK E — MUTATION + VERIFICATION ───────────────────────────────────
//
// A creation. The file does not exist, so a successful run must produce a NEW
// file whose bytes the runtime itself can verify — never one the model merely
// claimed to have written.

func TestBenchmarkE_MutationAndVerification(t *testing.T) {
	const created = "package main\n\nfunc main() {}\n"
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		// A CREATION contract carries the new file's whole content; the runtime
		// selects that artifact shape from the declared intent, so a full-file
		// fence is the honest answer and a SEARCH/REPLACE block against a
		// non-existent file would be refused.
		answered(600, 200, "```go\n"+created+"```"),
	}}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "create @main.go with a go main function"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Resume ONLY when the runtime actually opened an approval gate. Calling
	// ResumeApprove on a clarification or information park asserts a held patch
	// exists when it does not, and the resulting error would read as a runtime
	// fault rather than a test that skipped its own precondition.
	if b := r.Driver.Boundary(); b != nil && b.PatchID != "" {
		if _, err := r.Driver.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	tr := r.report()

	t.Logf("termination state: %s", r.Driver.State())
	t.Logf("main.go exists: %t content=%q", r.exists("main.go"), r.read("main.go"))
	t.Logf("budgets requested: %v", p.requestedBudgets())

	// INVARIANT: whatever the run concluded, the trace must carry the evidence
	// it concluded it from. A terminal PROVEN state with zero evidence records is
	// structurally impossible and must be caught if it ever appears.
	if r.Driver.State() == autonomy.RuntimeCompleted {
		if len(tr.Mutations) == 0 && len(tr.Verifications) == 0 {
			t.Fatalf("completed with no mutation and no verification record\n%s", tr)
		}
		if len(tr.Mutations) > 0 && len(tr.Verifications) == 0 {
			t.Fatalf("mutation applied with no verification record\n%s", tr)
		}
		for _, pat := range tr.Patterns {
			if pat == forensics.PatternCompletionWithoutEvidence {
				t.Fatalf("PROVEN without evidence\n%s", tr)
			}
		}
		// A creation that completed must actually have created the file. The
		// runtime's own verdict is not enough: the point is to check the
		// WORKSPACE, which is the only thing that cannot be self-reported.
		if !r.exists("main.go") {
			t.Fatalf("run reported COMPLETED but @main.go does not exist\n%s", tr)
		}
		if got := r.read("main.go"); !strings.Contains(got, "func main") {
			t.Fatalf("@main.go was created without the requested content: %q\n%s", got, tr)
		}
	}
}

// ── BENCHMARK F — FAILURE RECOVERY ──────────────────────────────────────────
//
// An operation that cannot succeed. The provider fails the transport outright.
//
// What it tests: that failure produces FAILED / REQUIRES_AUTHORIZATION — never
// PROVEN, and never an unbounded retry loop. An executor that retries forever
// and one that claims success are the same defect wearing different clothes.

func TestBenchmarkF_FailureRecovery(t *testing.T) {
	p := &scriptedProvider{name: "always-fails", err: fmt.Errorf("provider transport unavailable")}
	r := harness(t, p)

	term, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run returned an error rather than a termination: %v", err)
	}
	tr := r.report()

	t.Logf("termination: %v", fmtTerm(term))
	t.Logf("provider calls: %d", p.callCount())

	// INVARIANT: a run whose provider never succeeded must not report success.
	if r.Driver.State() == autonomy.RuntimeCompleted {
		t.Fatalf("a run with zero successful provider calls reported COMPLETED\n%s", tr)
	}
	// INVARIANT: the failure must be BOUNDED. The loop's own structural bounds
	// are what make this true; the benchmark records the count rather than
	// asserting an exact number, because the bound is the runtime's policy and
	// changing it is not a regression.
	if p.callCount() > rtAutonomy.MaxContractRecoveryAttempts+2 {
		t.Fatalf("provider called %d times on a permanently failing provider\n%s", p.callCount(), tr)
	}
	// INVARIANT: no mutation may be recorded for a run that never obtained an
	// artifact. A mutation record with no preceding model success is a fabricated
	// evidence chain.
	for _, m := range tr.Mutations {
		if m.ApplyExecuted {
			t.Fatalf("mutation applied although every provider call failed\n%s", tr)
		}
	}
	// The workspace must be byte-identical.
	if got := r.read("note.txt"); got != "foo\nbar\nbaz\n" {
		t.Fatalf("workspace changed on a fully failed run: %q\n%s", got, tr)
	}
}

func fmtTerm(term *autonomy.LoopTermination) string {
	if term == nil {
		return "<nil: parked at a human boundary>"
	}
	return fmt.Sprintf("state=%s reason=%q class=%s", term.State, term.Reason, term.Class)
}
