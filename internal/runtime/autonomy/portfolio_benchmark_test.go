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
	d := NewDriver(adapter, bus,
		WithLoopBounds(lifecycleBounds()),
		WithApprovalAdmission(realAdmissionProbe(mutateBudget)),
	)

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
