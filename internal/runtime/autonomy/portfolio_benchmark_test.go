package autonomy

// ── THE $prompt PORTFOLIO BENCHMARK (re-runnable) ────────────────────────────
//
// The canonical benchmark scenario, verbatim from the report:
//
//	$prompt please review this project and redesign a professional personal
//	portfolio page for me using HTML, CSS, and JS; the author's name is
//	TomHunter, an AI Engineer.
//
// runPortfolioBenchmark drives the REAL Driver → ExecutorAdapter → RuntimeExecutor
// path over a real workspace and returns the truthful runtime outcome. It is the
// harness every portfolio run in this package goes through, so a re-run is
// comparable with any previous one.
//
// PROVIDER
//
//   - The default (and CI-safe) provider reproduces the model failure mode the
//     report describes: every invocation is cut at the provider's output ceiling
//     and delivers NO bytes. This is the scenario under which the runtime used
//     to open an approval surface it could not honor.
//   - IZEN_BENCH_LIVE=1 plus a provider key runs the SAME benchmark against a
//     real model, so a benchmark re-run can reproduce a live failure verbatim.
//
// The benchmark is NOT expected to produce a good portfolio. It is expected to
// produce a TRUTHFUL one: OUTPUT_EXHAUSTED / artifact not produced / mutation not
// admissible / objective unproven — never a fake or stale approval.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// portfolioBenchmarkTrace is the comparable record of one benchmark run.
type portfolioBenchmarkTrace struct {
	Model        string
	Invocations  int
	Outcome      string
	Terminal     string
	BoundaryKind string
	BoundaryWhy  string
	Candidates   int
	PatchID      string
	Mutations    int
	BytesChanged int
	ObjectiveVer string
	History      string
	Diagnostic   string

	// ── OBJECTIVE LIFECYCLE ────────────────────────────────────────────
	// The benchmark records the objective CONTRACT, not only the terminal
	// verdict. A run that reports "not proven" is only useful if a reader can
	// see WHICH obligation was unmet, so the trace carries the recomputed
	// condition states, the requirement tally and the objective's own progress
	// and continuation verdicts.
	ObjectiveID  string
	Progress     string
	Continuation string
	Scope        string
	Conditions   string
	Unmet        string
	Requirements string
}

func (t portfolioBenchmarkTrace) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "model                : %s\n", t.Model)
	fmt.Fprintf(&b, "provider invocations : %d\n", t.Invocations)
	fmt.Fprintf(&b, "held candidates      : %d\n", t.Candidates)
	fmt.Fprintf(&b, "approval candidate   : %q\n", t.PatchID)
	fmt.Fprintf(&b, "parked boundary      : %s\n", t.BoundaryKind)
	fmt.Fprintf(&b, "boundary reason      : %s\n", t.BoundaryWhy)
	fmt.Fprintf(&b, "terminal outcome     : %s\n", t.Terminal)
	fmt.Fprintf(&b, "mutations applied    : %d (%d byte(s) changed)\n", t.Mutations, t.BytesChanged)
	fmt.Fprintf(&b, "objective verdict    : %s\n", t.ObjectiveVer)
	fmt.Fprintf(&b, "last diagnostic     : %s\n", t.Diagnostic)
	fmt.Fprintf(&b, "objective id        : %s\n", t.ObjectiveID)
	fmt.Fprintf(&b, "objective progress  : %s\n", t.Progress)
	fmt.Fprintf(&b, "objective continuation: %s\n", t.Continuation)
	fmt.Fprintf(&b, "objective scope     : %s\n", t.Scope)
	fmt.Fprintf(&b, "completion contract : %s\n", t.Conditions)
	fmt.Fprintf(&b, "unmet conditions    : %s\n", t.Unmet)
	fmt.Fprintf(&b, "requirements        : %s\n", t.Requirements)
	fmt.Fprintf(&b, "loop history         :%s\n", t.History)
	return b.String()
}

// countingProvider wraps any ai.Provider and records how many logical
// invocations the benchmark actually made. It is a pure observer: it forwards
// the request and the response unchanged.
type countingProvider struct {
	inner ai.Provider
	mu    sync.Mutex
	calls int
}

func (c *countingProvider) Name() string { return c.inner.Name() }

func (c *countingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Execute(ctx, req)
}

func (c *countingProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.ExecuteStream(ctx, req)
}

func (c *countingProvider) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// runPortfolioBenchmark executes the canonical objective against the real
// runtime and records the authoritative outcome.
//
// model is the model id the live backend serves ("" for the scripted default).
// It seeds the adapter's model authority, which is what production binds from
// the composition root: without it the adapter resolves a default model that
// belongs to a different provider and the executor refuses locally — a truthful
// refusal, but not a benchmark.
func runPortfolioBenchmark(t *testing.T, label, model string, inner ai.Provider, mutateBudget *budget.MutationBudget) portfolioBenchmarkTrace {
	t.Helper()
	root := portfolioWorkspace(t)
	before := map[string]int{}
	for _, f := range []string{"index.html", "styles.css", "script.js", "readme.md", "docs/notes.md"} {
		p := filepathJoin(root, f)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[f] = len(b)
	}

	p := &countingProvider{inner: inner}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	if model != "" {
		auth := appruntime.NewRuntimeAuthority()
		auth.Activate(authority.ModelBinding{
			ProviderID: authority.ProviderID(p.Name()),
			ModelID:    authority.ModelID(model),
		})
		adapter.SetAuthority(auth)
	}
	// The requirement pass is wired exactly as the composition root wires it: the
	// model may PROPOSE what the objective requires, and every proposal then has
	// to clear the runtime's own admissibility gate before it becomes an
	// obligation. Without a model binding the pass refuses deterministically, so
	// the scripted default run exercises the refusal path.
	driverOpts := []Option{
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mutateBudget)),
	}
	if model != "" {
		driverOpts = append(driverOpts, WithRequirementPass(RequirementPassForExecutor(x, func() string { return model })))
	}
	d := NewDriver(adapter, bus, driverOpts...)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	term, err := d.Run(ctx, acceptanceObjective)
	if err != nil {
		t.Fatalf("portfolio run: %v", err)
	}

	tr := portfolioBenchmarkTrace{Model: label}
	tr.Invocations = p.n()
	tr.Candidates = len(x.PendingPatchIDs())
	obs := d.LastObservation()
	tr.Mutations = obs.Objective.MutatedFiles
	if ev := d.objectiveEvaluation(); ev.Outcome != "" {
		tr.ObjectiveVer = string(ev.Outcome) + " (" + ev.Reason + ")"
	} else {
		tr.ObjectiveVer = "no verdict recorded (the run never proposed completion)"
	}
	if b := d.Boundary(); b != nil {
		tr.BoundaryKind = string(b.Action)
		tr.BoundaryWhy = b.Reason
		tr.PatchID = b.PatchID
	}
	switch {
	case term != nil:
		tr.Terminal = string(term.State) + " — " + term.Reason
	default:
		tr.Terminal = "<parked, no terminal state>"
	}
	tr.Outcome = string(d.State())

	tr.Diagnostic = obs.Diagnostic

	// ── objective lifecycle facts, read from the runtime (never recomputed) ──
	tr.ObjectiveID = d.ObjectiveIdentity()
	tr.Progress = string(d.ObjectiveProgress())
	tr.Continuation = string(d.ObjectiveContinuation().Decision)
	tr.Scope = strings.Join(d.ObjectiveContract().Scope, ", ")
	allConditions := d.ObjectiveConditions()
	condLines := make([]string, 0, len(allConditions))
	unmet := make([]string, 0, len(allConditions))
	reqLines := make([]string, 0, len(d.ObjectiveContract().Requirements))
	for _, c := range allConditions {
		condLines = append(condLines, fmt.Sprintf("%s=%s", c.ID, c.Status))
		if !c.Satisfied() {
			unmet = append(unmet, c.ID)
		}
	}
	tr.Conditions = strings.Join(condLines, " ")
	tr.Unmet = strings.Join(unmet, ", ")
	for _, r := range d.ObjectiveContract().Requirements {
		line := fmt.Sprintf("%s=%s", r.ID, r.Status)
		if r.RejectReason != "" {
			line += "(" + r.RejectReason + ")"
		}
		reqLines = append(reqLines, line)
	}
	tr.Requirements = strings.Join(reqLines, " ")

	var hist strings.Builder
	for i, trn := range d.History() {
		fmt.Fprintf(&hist, "\n  %2d. %v -> %v (%s) %s", i, trn.From, trn.To, trn.Action, trn.Reason)
	}
	tr.History = hist.String()

	for f, n := range before {
		now, err := os.ReadFile(filepathJoin(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if len(now) != n {
			tr.BytesChanged += len(now) - n
			if len(now)-n < 0 {
				tr.BytesChanged = -(len(now) - n)
			}
		}
	}
	return tr
}

func filepathJoin(root, rel string) string { return root + string(os.PathSeparator) + rel }

// TestPortfolioBenchmark_OutputExhaustedIsTruthful re-runs the canonical $prompt
// portfolio benchmark under the exact model failure mode the report describes —
// the provider cuts every generation at its ceiling and delivers nothing — and
// pins the acceptable outcome: no artifact, no approval surface, no mutation,
// objective unproven.
func TestPortfolioBenchmark_OutputExhaustedIsTruthful(t *testing.T) {
	tr := runPortfolioBenchmark(t, "space-bunny-alpha (exhausts output, 0 delivered bytes)",
		"", &zeroByteProvider{}, budget.DefaultBudget())
	t.Log("\n=== $prompt PORTFOLIO BENCHMARK (output-exhaustion failure mode) ===\n" + tr.String())

	if tr.Invocations == 0 {
		t.Fatal("the provider was never invoked; the benchmark did not exercise a real computation")
	}
	if tr.PatchID != "" {
		t.Fatalf("FAILURE MODE: a computation that delivered no artifact bytes reached the approval boundary (candidate %q)", tr.PatchID)
	}
	if tr.BoundaryKind == string(autonomy.HumanBoundaryApproval) {
		t.Fatalf("FAILURE MODE: the runtime opened an approval surface it could not honor\n%s", tr)
	}
	if tr.Candidates != 0 {
		t.Fatalf("FAILURE MODE: %d executable candidate(s) survived a failed computation", tr.Candidates)
	}
	if tr.Mutations != 0 || tr.BytesChanged != 0 {
		t.Fatalf("FAILURE MODE: the failed computation mutated the workspace (%d file(s), %d byte(s))", tr.Mutations, tr.BytesChanged)
	}
	if strings.Contains(tr.Terminal, string(autonomy.RuntimeCompleted)) {
		t.Fatalf("FAILURE MODE: the run reported completion\n%s", tr)
	}
	if tr.ObjectiveVer == "" {
		t.Fatal("the run recorded no objective verdict at all")
	}

	// ── OBJECTIVE LIFECYCLE TRUTH ─────────────────────────────────────
	// The benchmark is not required to produce a good portfolio; it IS
	// required to produce a truthful LIFECYCLE. These assertions read the
	// contract the run was actually judged against.
	if tr.ObjectiveID == "" {
		t.Fatal("the run recorded no objective identity")
	}
	if tr.Scope == "" {
		t.Fatal("the run recorded no declared scope; it cannot claim a target without saying which")
	}
	if tr.Conditions == "" {
		t.Fatal("the run authored no completion conditions; the objective contract was never in force")
	}
	if tr.Unmet == "" {
		t.Fatalf("a run that delivered nothing reports every objective condition satisfied:\n%s", tr)
	}
	if tr.Progress == string(execution.ProgressProven) {
		t.Fatalf("a run that mutated nothing reports its objective PROVEN:\n%s", tr)
	}
}

// TestPortfolioBenchmark_UnderDeliveredObjectiveIsNotProven is the benchmark's
// architectural case: the model answers for ONE of the three files the
// objective is about, and the runtime must not call that success.
//
// WHAT THE RUNTIME DOES WHEN A SIBLING TARGET FAILS. The executor treats a
// provider failure on one target as a failure of the whole multi-target
// execution, so no candidate is held and nothing is mutated. That is a truthful
// outcome and the correct one — the runtime does not half-apply a scope it
// cannot complete — but it means the "one of three mutated" state is reached
// through the objective contract rather than through the executor. The
// end-to-end assertion here is therefore that NO completion is claimed and that
// the unmet obligations are named; internal/execution/objective_contract_test.go
// covers the partial-delivery contract directly, and
// TestDriverCaseA_MutationAloneDoesNotProveTheObjective covers the driver seam
// with a real applied mutation.
func TestPortfolioBenchmark_UnderDeliveredObjectiveIsNotProven(t *testing.T) {
	root := portfolioWorkspace(t)
	beforeStyles, err := os.ReadFile(filepathJoin(root, "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	beforeScript, err := os.ReadFile(filepathJoin(root, "script.js"))
	if err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, &underDeliveringProvider{}, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds()))

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The scope the runtime bound is every file the objective is about.
	scope := d.ObjectiveContract().Scope
	if len(scope) < 2 {
		t.Fatalf("declared scope = %v, want the three files the objective is about", scope)
	}

	if d.State() == autonomy.RuntimeCompleted {
		t.Fatalf("FAILURE MODE: the runtime completed an objective it delivered only a fraction of\n"+
			"progress=%s conditions=%+v", d.ObjectiveProgress(), d.ObjectiveConditions())
	}
	if got := d.objectiveEvaluation(); got.Outcome == execution.ObjectiveProven {
		t.Fatalf("FAILURE MODE: the objective authority ruled PROVEN over an under-delivered scope: %+v", got)
	}

	// The objective contract must be in force and must name what is missing, so
	// the report is actionable rather than a shrug.
	conditions := d.ObjectiveConditions()
	if len(conditions) == 0 {
		t.Fatal("no completion conditions were authored; the objective contract was never in force")
	}
	unmet := d.ObjectiveContinuation().UnmetConditionIDs
	if len(unmet) == 0 && d.ObjectiveProgress() != execution.ProgressProven {
		t.Fatal("the objective is not proven but no unmet condition was reported")
	}
	if d.ObjectiveProgress() == execution.ProgressProven {
		t.Fatalf("FAILURE MODE: progress reports PROVEN for an under-delivered objective")
	}

	// The files the model never answered for must be untouched on disk.
	if now, err := os.ReadFile(filepathJoin(root, "styles.css")); err != nil {
		t.Fatal(err)
	} else if string(now) != string(beforeStyles) {
		t.Fatal("styles.css changed although the model never answered for it")
	}
	if now, err := os.ReadFile(filepathJoin(root, "script.js")); err != nil {
		t.Fatal(err)
	} else if string(now) != string(beforeScript) {
		t.Fatal("script.js changed although the model never answered for it")
	}
	t.Logf("under-delivered portfolio objective: progress=%s continuation=%s unmet=%v scope=%v",
		d.ObjectiveProgress(), d.ObjectiveContinuation().Decision, unmet, scope)
}

// underDeliveringProvider answers for index.html only. Every other target is a
// transport failure, which is the honest way for a model to decline a file it was
// shown.
type underDeliveringProvider struct{}

func (underDeliveringProvider) Name() string { return "under-delivering" }

func (underDeliveringProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	target := targetOf(req)
	if target != "index.html" {
		return nil, fmt.Errorf("this model declined to answer for %q", target)
	}
	resp := portfolioArtifacts["index.html"].Response
	return &resp, nil
}

func (underDeliveringProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// TestPortfolioBenchmark_LiveModel re-runs the SAME benchmark against a real
// model. It is skipped unless IZEN_BENCH_LIVE=1 and a provider key are present,
// so CI never spends tokens.
//
//	IZEN_BENCH_LIVE=1 IZEN_BENCH_MODEL=space-bunny-alpha \
//	  OPENCODE_API_KEY=... go test ./internal/runtime/autonomy/ \
//	    -run TestPortfolioBenchmark_LiveModel -v
//
// Whatever the model does, the invariants are the same: a computation that fails,
// is superseded or is cancelled must not leave an executable candidate, and an
// approval surface may only exist for a mutation the runtime can authorize.
func TestPortfolioBenchmark_LiveModel(t *testing.T) {
	if os.Getenv("IZEN_BENCH_LIVE") != "1" {
		t.Skip("live benchmark disabled: set IZEN_BENCH_LIVE=1 (and a provider key) to run")
	}
	model := strings.TrimSpace(os.Getenv("IZEN_BENCH_MODEL"))
	if model == "" {
		model = "space-bunny-alpha"
	}
	provider, label := liveBenchmarkProvider(t, model)
	if provider == nil {
		t.Skip("no live provider credentials available")
	}

	tr := runPortfolioBenchmark(t, label, model, provider, budget.DefaultBudget())
	t.Log("\n=== $prompt PORTFOLIO BENCHMARK (live: " + label + ") ===\n" + tr.String())

	if tr.Invocations == 0 {
		t.Fatal("the live provider was never invoked; the benchmark did not exercise a real computation")
	}
	// INVARIANTS, not quality. The benchmark is not required to produce a good
	// portfolio; it is required to produce a truthful one.
	if tr.BoundaryKind == string(autonomy.HumanBoundaryApproval) && tr.Candidates == 0 {
		t.Fatalf("FAILURE MODE: an approval surface exists with no held candidate\n%s", tr)
	}
	if tr.PatchID != "" {
		t.Logf("approval offered for candidate %q — admissibility was proven at park time", tr.PatchID)
	}
	if strings.Contains(tr.Terminal, string(autonomy.RuntimeCompleted)) && tr.Mutations == 0 {
		t.Logf("completed with zero mutations — reported for the record:\n%s", tr)
	}
}

// liveBenchmarkProvider resolves a real provider from the environment. It returns
// nil (and the caller skips) when no credential is configured.
func liveBenchmarkProvider(t *testing.T, model string) (ai.Provider, string) {
	t.Helper()
	if key := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); key != "" {
		base := strings.TrimSpace(os.Getenv("OPENCODE_BASE_URL"))
		return providers.NewOpenCodeProvider(key, model, base), model + " @ opencode"
	}
	if key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); key != "" {
		base := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		if base == "" {
			base = "https://openrouter.ai/api/v1"
		}
		return providers.NewOpenRouterProvider(key, model, base), model + " @ openrouter"
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return providers.NewOpenAIProvider(key, model), model + " @ openai"
	}
	t.Logf("no live provider credential found for model %q", model)
	return nil, ""
}
