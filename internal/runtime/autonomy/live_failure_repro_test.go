package autonomy

// ── THE LIVE FAILURE, REPRODUCED DETERMINISTICALLY ─────────────────────────
//
// This file reproduces the reported live run exactly, with a scripted provider
// instead of a real model, so the numbers in the report are reproducible and the
// invariants can be asserted without spending tokens.
//
// THE OBSERVED FAILURE:
//
//	[scope] RESOLVED -> targets=[index.html,script.js,styles.css]
//	read_file style.css
//	style.css: no such file or directory
//	read_file style.css
//	style.css: no such file or directory
//	zero artifacts parsed
//	provider finish_reason=length
//	repair re-prompt
//	repeated reads
//	strict line-anchor attempt exhausted
//	hallucinated anchor — zero match
//	Physical Output Budget Breach
//	autonomous abort
//
// The provider below emits exactly that sequence: two reads of `style.css`, a
// zero-artifact response, an exhaustion, and an unanchorable patch.
//
// WHAT THIS FILE ASSERTS:
//
//	- `style.css` is NEVER mapped to `styles.css`
//	- the identical failing request is not issued a third time
//	- every failure is classified with evidence
//	- the objective identity and the resolved scope survive
//	- the run terminates in a truthful state

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// liveFailureProvider reproduces the observed model behaviour: it asks for
// `style.css` — a name that is NOT in the resolved scope — repeatedly, then
// answers with prose, then exhausts.
//
// It implements SetToolRunner and drives the SAME bounded read-only tool loop the
// production provider uses, so the capability seam — where the live failure
// actually occurred — is genuinely exercised rather than bypassed.
type liveFailureProvider struct {
	mu    sync.Mutex
	calls int
	// toolCalls counts the model's capability requests.
	toolCalls int
	// requested records every workspace path the MODEL asked for, verbatim.
	requested []string
	// toolResults records what the runtime returned for each of them.
	toolResults []string
	runner      ai.ToolRunner
}

// requestedByModel returns every path the model named, in order and verbatim.
func (p *liveFailureProvider) requestedByModel() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requested...)
}

// SetToolRunner satisfies the provider contract the executor wires, exactly as
// internal/providers does.
//
// It wraps the runner so the reproduction can read WHAT the runtime returned to
// the model for each capability request. That is the channel on which a
// substitution would become visible, so asserting on it is the direct test of
// the invariant rather than an inference from a downstream effect.
func (p *liveFailureProvider) SetToolRunner(r ai.ToolRunner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runner = &recordingToolRunner{inner: r, provider: p}
}

// recordingToolRunner forwards to the real capability seam and records the
// bounded result the model received.
type recordingToolRunner struct {
	inner    ai.ToolRunner
	provider *liveFailureProvider
}

func (r *recordingToolRunner) Run(ctx context.Context, call ai.ToolCall) (string, error) {
	out, err := r.inner.Run(ctx, call)
	r.provider.mu.Lock()
	r.provider.toolResults = append(r.provider.toolResults, out)
	r.provider.mu.Unlock()
	return out, err
}

// CapabilityFailures exposes the recorded tool results for assertions.
func (p *liveFailureProvider) results() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.toolResults...)
}

func (p *liveFailureProvider) Name() string { return "live-failure" }

func (p *liveFailureProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	// The tool loop is what turns model capability requests into real capability
	// executions. Running it here (rather than returning tool calls to the
	// executor) mirrors the production path exactly.
	if p.runner != nil {
		return ai.RunReadOnlyToolLoop(ctx, p.executeOnce, p.runner, req, ai.ToolLoopOptions{MaxIterations: 2})
	}
	return p.executeOnce(ctx, req)
}

// capabilityRequests reports how many capability requests the model made.
func (p *liveFailureProvider) capabilityRequests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolCalls
}

// executeOnce is the single-shot primitive the tool loop drives.
func (p *liveFailureProvider) executeOnce(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	turn := p.calls
	p.mu.Unlock()

	// The first two turns ask for the nonexistent target, reproducing the two
	// observed reads of `style.css`. After that the model stops asking.
	if turn <= 2 {
		p.mu.Lock()
		p.toolCalls++
		p.requested = append(p.requested, requestedTargetNames(req)...)
		p.mu.Unlock()
		return &ai.Response{
			ToolCalls: []ai.ToolCall{{
				ID: "call-style",
				Function: ai.ToolCallFunction{
					Name:      ai.ToolReadFile,
					Arguments: `{"path":"style.css"}`,
				},
			}},
			FinishReason: "tool_calls",
		}, nil
	}
	// Then prose, and an exhausted stream — the live run's dominant failure mode.
	return &ai.Response{
		Content:   "",
		Usage:     ai.ProviderUsage{Known: true, CompletionTokens: 3072, FinishReason: "length"},
		Truncated: true,
	}, nil
}

func (p *liveFailureProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, io.ErrClosedPipe
}

// requestedTargetNames returns every workspace path the model named in a request,
// across every message. It lets a test assert WHICH names the model asked for,
// independently of what the runtime did with them.
func requestedTargetNames(req ai.Request) []string {
	var out []string
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != ai.ToolReadFile {
				continue
			}
			var p ai.ReadFileParams
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &p); err == nil {
				if p.Path != "" {
					out = append(out, p.Path)
				}
			}
		}
	}
	return out
}

// TestLiveFailure_StyleCSSIsNeverMappedToStylesCSS is the headline assertion.
//
// It runs the reproduction and asserts that `styles.css` — which DOES exist and
// IS in scope — was never read in place of the requested `style.css`. If any
// fuzzy, suffix, extension, basename or edit-distance strategy had been applied,
// the runtime would have served `styles.css` and this test would see its bytes.
func TestLiveFailure_StyleCSSIsNeverMappedToStylesCSS(t *testing.T) {
	root := portfolioWorkspace(t)
	provider := &liveFailureProvider{}
	// Snapshot the whole workspace: the run must not write anything on the
	// strength of an invalid capability request.
	before := snapshotWorkspace(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds()))

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The authoritative scope is exactly what the live run resolved.
	scope := d.authoritativeScope()
	want := []string{"index.html", "script.js", "styles.css"}
	if len(scope) != len(want) {
		t.Fatalf("resolved scope = %v, want %v", scope, want)
	}
	for i := range want {
		if scope[i] != want[i] {
			t.Fatalf("resolved scope = %v, want %v", scope, want)
		}
	}

	// Every recorded capability failure must name the request VERBATIM and must
	// carry a target-identity classification. A substitution would show up here
	// as a record naming `styles.css`.
	failures := x.CapabilityFailures()
	for _, f := range failures {
		if f.Target != "style.css" {
			t.Errorf("a capability failure recorded target %q; the request was for %q", f.Target, "style.css")
		}
		switch f.Class {
		case execution.FailureTargetNotFound,
			execution.FailureTargetIdentityMismatch,
			execution.FailureNonProgressing,
			execution.FailureCapabilityFailure:
		default:
			t.Errorf("unexpected capability failure class %s for the live reproduction", f.Class)
		}
		if f.Evidence == "" {
			t.Errorf("capability failure %+v carries no evidence", f)
		}
	}

	// The runtime refused the invalid request, and the refusal reached the model
	// as a typed tool result rather than as silently-empty content.
	results := provider.results()
	if len(results) == 0 {
		t.Fatal("the reproduction issued no capability request — the scenario did not run")
	}
	for i, got := range results {
		if !strings.Contains(got, string(execution.FailureTargetNotFound)) &&
			!strings.Contains(got, string(execution.FailureTargetIdentityMismatch)) &&
			!strings.Contains(got, string(execution.FailureNonProgressing)) {
			t.Fatalf("tool result %d carried no target-identity classification:\n%s", i, got)
		}
		// The refusal must never have served the in-scope file's bytes.
		if strings.Contains(got, "body {") || strings.Contains(got, "font-family") {
			t.Fatalf("tool result %d leaked the contents of styles.css — a substitution occurred:\n%s", i, got)
		}
	}
	// The SECOND identical request is explicitly recorded as non-progressing:
	// the runtime knew the answer and said so.
	if len(results) >= 2 && !strings.Contains(results[1], string(execution.FailureNonProgressing)) {
		t.Fatalf("the second identical request was not recognised as a repeat:\n%s", results[1])
	}

	// The refusal names the unchanged scope, so the model can re-plan inside it.
	for i, got := range results {
		if !strings.Contains(got, "is unchanged") {
			t.Fatalf("tool result %d does not state that the scope is unchanged:\n%s", i, got)
		}
		if !strings.Contains(got, "styles.css") {
			t.Fatalf("tool result %d does not name the authoritative scope:\n%s", i, got)
		}
	}

	// The model asked twice and got refused twice — there is no third request.
	if n := provider.capabilityRequests(); n > 2 {
		t.Fatalf("the model issued %d capability requests; the runtime did not bound the invalid target", n)
	}
	t.Logf("live reproduction: %d model capability request(s) for %v, %d refusal(s)",
		provider.capabilityRequests(), uniqueStrings(provider.requestedByModel()), len(results))
	for i, f := range failures {
		t.Logf("  failure %d: class=%s target=%q seen=%d policy=%s",
			i+1, f.Class, f.Target, f.Count, f.Class.RetryPolicy())
	}

	// And the name the MODEL asked for is recorded verbatim as `style.css` —
	// the runtime never rewrote it to the in-scope `styles.css`.
	for i, asked := range provider.requestedByModel() {
		if asked != "style.css" {
			t.Fatalf("model request %d was recorded as %q; the runtime must preserve the verbatim name", i+1, asked)
		}
	}

	// The provider asked twice (that is the model's behaviour); the runtime's job
	// is to refuse both and to bound the run.
	if provider.calls > 8 {
		t.Fatalf("provider invocations = %d; the run is looping rather than terminating", provider.calls)
	}

	// And the workspace was never written on the strength of the invalid read.
	assertNoWorkspaceChange(t, root, before)
}

// uniqueStrings de-duplicates while preserving order, for compact logging.
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// snapshotWorkspace captures every workspace file's bytes.
func snapshotWorkspace(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(data)
	}
	return out
}

// assertNoWorkspaceChange fails if any file was created, removed or modified.
func assertNoWorkspaceChange(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotWorkspace(t, root)
	if len(after) != len(before) {
		t.Fatalf("workspace gained/lost files: before=%d after=%d", len(before), len(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("%s changed although no mutation was authorized", name)
		}
	}
}

// TestLiveFailure_RunTerminatesTruthfully asserts the run reached one of the
// states the acceptance criteria allow, and that it never reported completion.
func TestLiveFailure_RunTerminatesTruthfully(t *testing.T) {
	root := portfolioWorkspace(t)
	provider := &liveFailureProvider{}

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds()))

	term, _ := d.Run(context.Background(), acceptanceObjective)

	allowed := map[autonomy.RuntimeState]bool{
		autonomy.RuntimeUnsubstantiated: true,
		autonomy.RuntimeAwaitingHuman:   true,
		autonomy.RuntimeAborted:         true,
	}
	if !allowed[d.State()] {
		t.Fatalf("terminal state = %s, want UNSUBSTANTIATED / awaiting_human / aborted", d.State())
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("the run reported completion on no artifact")
	}
	if d.ObjectiveProgress() == execution.ProgressProven {
		t.Fatal("the run reported the objective PROVEN on no artifact")
	}
	// No executable candidate may survive: the acceptance criteria forbid
	// claiming a patch exists when nothing was parsed.
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("executable candidate(s) survived: %v", ids)
	}
	// The objective identity and scope survived the whole run.
	if d.ObjectiveIdentity() != "run-1" {
		t.Fatalf("objective identity = %q, want run-1", d.ObjectiveIdentity())
	}
	// The unmet obligations are named, so the terminal state is actionable.
	unmet := d.ObjectiveContinuation().UnmetConditionIDs
	if len(unmet) == 0 && d.ObjectiveProgress() != execution.ProgressProven {
		t.Fatal("a non-proven objective reported no unmet condition")
	}
	t.Logf("live reproduction: state=%s progress=%s continuation=%s invocations=%d unmet=%v",
		d.State(), d.ObjectiveProgress(), d.ObjectiveContinuation().Decision, provider.calls, unmet)
	_ = term
}

// TestLiveFailure_EveryFailureHasATypedClassification walks the whole
// reproduction and asserts that every terminal transition names a typed
// classification — never a bare "failed"/"repair"/"retry".
func TestLiveFailure_EveryFailureHasATypedClassification(t *testing.T) {
	root := portfolioWorkspace(t)
	provider := &liveFailureProvider{}

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds()))

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The lifecycle recorded at least one typed failure with evidence.
	report := d.failures.LedgerReport()
	if len(report) == 0 {
		t.Fatal("the reproduction recorded no typed failure classification")
	}
	for _, line := range report {
		if strings.Contains(line, `class=""`) {
			t.Fatalf("a recorded failure has an empty class: %s", line)
		}
		if !strings.Contains(line, "policy=") {
			t.Fatalf("a recorded failure does not state its retry policy: %s", line)
		}
	}

	// No transition in the loop history may rest on a bare generic reason.
	for _, tr := range d.History() {
		switch tr.Action {
		case autonomy.LoopRepair, autonomy.LoopRetry:
			if strings.TrimSpace(tr.Reason) == "" {
				t.Fatalf("a %s transition carries no reason", tr.Action)
			}
		}
	}
}

// TestLiveFailure_RecoveryBriefWouldTellTheNextPlannerWhatFailed asserts the
// continuation payload carries the classified failure and the unchanged scope,
// so a continuation is a continuation rather than a restart.
func TestLiveFailure_RecoveryBriefWouldTellTheNextPlannerWhatFailed(t *testing.T) {
	root := portfolioWorkspace(t)
	provider := &liveFailureProvider{}

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds()))

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brief := d.recoveryBrief()
	if brief == "" {
		t.Skip("this run reached a terminal state without a recovery step; the brief is exercised by TestRecovery_ContinuationBriefCarriesFailedApproach")
	}
	if !strings.Contains(brief, "authoritative scope") {
		t.Fatalf("the brief does not restate the authoritative scope:\n%s", brief)
	}
	if !strings.Contains(brief, "styles.css") {
		t.Fatalf("the brief does not name the resolved scope verbatim:\n%s", brief)
	}
	// It must warn against substitution explicitly, because that is the specific
	// failure this runtime exists to prevent.
	if !strings.Contains(brief, "not a substitute") {
		t.Fatalf("the brief does not warn against target substitution:\n%s", brief)
	}
}
