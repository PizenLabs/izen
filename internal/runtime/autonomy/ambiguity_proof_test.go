package autonomy

// ── Proof harness: capture the REAL state transition, do not assert it ───────
//
// The other tests in this file assert. This one RECORDS, and prints the whole
// chain for both cases so the evidence can be read rather than inferred:
//
//	classification → strategy → discovery → derivation → targets → scope →
//	objective semantics → preflight → approval candidate → provider calls →
//	filesystem delta → completion state
//
// It runs the production Driver → ExecutorAdapter → RuntimeExecutor path over a
// real directory with a scripted provider. Nothing about the mutation,
// admission, verification or approval machinery is stubbed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ambiguityTrace is the observed state of one run, read back from runtime state
// and the filesystem rather than reconstructed from the run's own claims.
type ambiguityTrace struct {
	Classification  string
	Strategy        string
	Derivation      string
	Kinds           []string
	Targets         []string
	Candidates      []string
	ScopeState      string
	ScopeTargets    []string
	Authorizes      bool
	Objective       string
	ScopeSemantics  string
	TargetSemantics string
	Discovery       string
	Preflight       string
	BoundaryAction  string
	PatchID         string
	ProviderCalls   int
	FilesystemDelta []string
	Completion      string
}

func (tr ambiguityTrace) String() string {
	var b strings.Builder
	row := func(label, value string) {
		fmt.Fprintf(&b, "  %-22s %s\n", label, value)
	}
	list := func(v []string) string {
		if len(v) == 0 {
			return "(none)"
		}
		return strings.Join(v, ",")
	}
	row("classification", tr.Classification)
	row("strategy", tr.Strategy)
	row("derivation status", tr.Derivation)
	row("declared kinds", list(tr.Kinds))
	row("derivation targets", list(tr.Targets))
	row("scope candidates", list(tr.Candidates))
	row("scope state", tr.ScopeState)
	row("scope targets", list(tr.ScopeTargets))
	row("scope authorizes mut", fmt.Sprintf("%t", tr.Authorizes))
	row("objective operation", tr.Objective)
	row("semantics scope", tr.ScopeSemantics)
	row("semantics target", tr.TargetSemantics)
	row("semantics discovery", tr.Discovery)
	row("preflight", tr.Preflight)
	row("boundary action", tr.BoundaryAction)
	row("approval patch id", tr.PatchID)
	row("provider calls", fmt.Sprintf("%d", tr.ProviderCalls))
	row("filesystem delta", list(tr.FilesystemDelta))
	row("completion", tr.Completion)
	return b.String()
}

func observeTrace(t *testing.T, root string, objective string, d *Driver, providerCalls int, before map[string]string) ambiguityTrace {
	t.Helper()
	classified := autonomy.Classify(objective, nil)
	sem := d.objectiveSemantics()
	outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background()))

	tr := ambiguityTrace{
		Classification:  fmt.Sprintf("intent=%s requires_mutation=%t", classified.Intent, classified.RequiresMutation()),
		Strategy:        string(d.resolved.Profile.Strategy),
		Derivation:      string(d.scopeDerivation.StatusOrUnresolved()),
		Kinds:           d.scopeDerivation.Kinds,
		Targets:         d.scopeDerivation.Targets,
		Candidates:      d.scopeResolution.Candidates,
		ScopeState:      string(d.scopeResolution.State),
		ScopeTargets:    d.scopeResolution.Targets,
		Authorizes:      d.scopeResolution.AuthorizesMutation(),
		Objective:       string(sem.Operation),
		ScopeSemantics:  string(sem.Scope),
		TargetSemantics: string(sem.Target),
		Discovery:       string(sem.Discovery),
		Preflight:       string(outcome.Verdict),
		Completion:      string(d.ObjectiveProgress()),
		ProviderCalls:   providerCalls,
	}
	if boundary := d.Boundary(); boundary != nil {
		tr.BoundaryAction = string(boundary.Action)
		tr.PatchID = boundary.PatchID
	}
	tr.FilesystemDelta = ambiguityDiff(before, ambiguitySnapshot(t, root))
	sort.Strings(tr.FilesystemDelta)
	return tr
}

// TestProof_AmbiguousWorkspaceChain records the whole transition for the
// reported failure. It asserts nothing by design — its job is to make the chain
// readable, and the assertions live in the sibling tests.
func TestProof_AmbiguousWorkspaceChain(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &ambiguityProbeProvider{}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	term, err := d.Run(context.Background(), ambiguousObjective)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	t.Logf("AMBIGUOUS CASE (workspace: a.html b.html index.html a.css b.css styles.css script.js)\n%s",
		observeTrace(t, root, ambiguousObjective, d, provider.Calls(), before))
	t.Logf("  %-22s %v", "termination", term)

	// The assertions below are the claim this trace has to support.
	tr := observeTrace(t, root, ambiguousObjective, d, provider.Calls(), before)
	if tr.Derivation != string(execution.DerivationAmbiguous) || tr.ScopeState != string(ScopeAmbiguous) {
		t.Fatalf("expected AMBIGUOUS chain, got %s / %s", tr.Derivation, tr.ScopeState)
	}
	if tr.Authorizes || tr.PatchID != "" || tr.ProviderCalls != 0 || len(tr.FilesystemDelta) != 0 {
		t.Fatalf("ambiguous chain leaked authority:\n%s", tr)
	}
	if tr.Preflight != string(AdmissionDisambiguate) {
		t.Fatalf("preflight = %s, want DISAMBIGUATE", tr.Preflight)
	}
}

// TestProof_UniqueWorkspaceChain records the same chain for the same prompt over
// a workspace with one file per declared kind, so the two traces differ only
// where the evidence differs.
func TestProof_UniqueWorkspaceChain(t *testing.T) {
	root := portfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	term, err := d.Run(context.Background(), uniqueObjective)
	if err != nil {
		t.Fatalf("unique run: %v", err)
	}
	t.Logf("UNIQUE CASE (workspace: index.html styles.css script.js readme.md docs/notes.md)\n%s",
		observeTrace(t, root, uniqueObjective, d, mock.calls, before))
	t.Logf("  %-22s %v", "termination", term)

	// Approve, so the trace shows the WHOLE authority path rather than stopping
	// at the gate.
	if boundary := d.Boundary(); boundary != nil && boundary.Action == autonomy.HumanBoundaryApproval {
		if _, err := d.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("approve: %v", err)
		}
	}
	t.Logf("UNIQUE CASE AFTER APPROVAL\n%s", observeTrace(t, root, uniqueObjective, d, mock.calls, before))
	t.Logf("  %-22s %v", "mutation state", d.LastObservation().Objective.Mutation)
	t.Logf("  %-22s %v", "verification ran", d.LastObservation().Objective.VerificationRan)
	t.Logf("  %-22s %d", "mutated files", d.LastObservation().Objective.MutatedFiles)
	t.Logf("  %-22s %s (%s)", "objective outcome", d.objectiveEvaluation().Outcome, d.objectiveEvaluation().Reason)

	tr := observeTrace(t, root, uniqueObjective, d, mock.calls, before)
	if tr.Derivation != string(execution.DerivationUnique) || tr.ScopeState != string(ScopeResolved) {
		t.Fatalf("expected UNIQUE chain, got %s / %s", tr.Derivation, tr.ScopeState)
	}
	if !tr.Authorizes || tr.ProviderCalls == 0 || len(tr.FilesystemDelta) == 0 {
		t.Fatalf("unique chain did not reach mutation:\n%s", tr)
	}
	if tr.Completion != string(execution.ProgressProven) {
		t.Fatalf("completion = %s, want PROVEN\n%s", tr.Completion, tr)
	}
}

// TestProof_StrategyProjectionTable prints the projection so the single source of
// truth is visible rather than asserted into existence.
func TestProof_StrategyProjectionTable(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "  %-24s %-11s %-8s %-10s %s\n", "strategy", "semantics", "applied", "mutation", "admission intent")
	for _, s := range []strategy.ExecutionStrategy{
		strategy.DirectDeterministic,
		strategy.TargetedMutation,
		strategy.TargetedReasoning,
		strategy.RepositoryInvestigation,
		strategy.MultiFilePlanning,
		strategy.DirectResponse,
		strategy.HumanClarification,
		strategy.ExecutionStrategy("some_future_strategy"),
	} {
		sem := strategy.MutationSemanticsOf(s)
		fmt.Fprintf(&b, "  %-24s %-11s %-8t %-10t %s\n",
			s, sem, sem.IsApplied(), sem.RequiresMutationContract(), intentClassForStrategy(s))
	}
	t.Logf("CANONICAL STRATEGY PROJECTION\n%s", b.String())
}

// TestProof_CandidatesAreNotScope prints the candidate set beside the bound
// target set for the ambiguous case, which is the distinction the audit turned
// on: both are non-empty, only one of them is authority — and it is empty.
func TestProof_CandidatesAreNotScope(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = ambiguousObjective
	d.resolved = d.adapter.Resolve(d.prompt)
	d.deriveEvidenceScope()

	t.Logf("derivation status : %s", d.scopeDerivation.StatusOrUnresolved())
	t.Logf("declared kinds    : %v", d.scopeDerivation.Kinds)
	for _, r := range d.scopeDerivation.Resolutions {
		t.Logf("  kind %-6s ambiguous=%-5t matches=%v", r.Kind, r.Ambiguous, r.Matches)
	}
	t.Logf("candidates (evidence) : %v", d.scopeResolution.Candidates)
	t.Logf("scope targets (authority): %v", d.scopeResolution.Targets)
	t.Logf("authorizes mutation     : %t", d.scopeResolution.AuthorizesMutation())
	t.Logf("reason: %s", d.scopeResolution.Reason)

	for _, f := range []string{"a.html", "b.html", "index.html", "a.css", "b.css", "styles.css"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("workspace fixture is missing %s: %v", f, err)
		}
	}
	if len(d.scopeResolution.Targets) != 0 {
		t.Fatalf("candidates leaked into the scope: %v", d.scopeResolution.Targets)
	}
}
