package autonomy

// ── Acceptance: the objective-compilation seam, through the REAL classifier ──
//
// The execution-package seam tests build TaskClassification by hand. These tests
// drive the SAME seam through the production classifier and the production
// ExecutorAdapter, so `$prompt check this project and rewrite it` is compiled by
// the code the runtime actually runs:
//
//	autonomy.Classify → Driver.taskContract → execution.DeriveTaskContract
//	                  → Driver.objectiveContract → execution.DeriveObjectiveContract
//
// They are deterministic and touch no provider.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// seamDriver builds a Driver over a real scripted executor and workspace, with
// no provider call. It exists so the compilation seam can be exercised without a
// full run.
func seamDriver(t *testing.T, root string) *Driver {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return NewDriver(adapter, nil)
}

// TestSeamRealClassifier_TargetlessModificationIsModifyNotCreate runs the exact
// failing objective through the production classifier.
func TestSeamRealClassifier_TargetlessModificationIsModifyNotCreate(t *testing.T) {
	root := portfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = "check this project and rewrite it"
	d.resolved = d.adapter.Resolve(d.prompt)

	// The classifier itself must read it as a mutation with no target — that is
	// the input that used to become CREATE.
	classified := autonomy.Classify(d.prompt, nil)
	if !classified.RequiresMutation() {
		t.Fatalf("classifier intent = %s, want a mutating intent", classified.Intent)
	}
	if len(classified.Targets) != 0 {
		t.Fatalf("classifier extracted targets %v; the prompt names no file", classified.Targets)
	}

	contract := d.taskContract()
	if contract.Kind == execution.TaskCreate {
		t.Fatal("the real seam compiled the targetless objective as CREATE")
	}

	sem := d.objectiveSemantics()
	if sem.Operation != execution.OperationModify {
		t.Fatalf("operation = %s, want MODIFY", sem.Operation)
	}
	if sem.Scope != execution.ScopeStateUnresolved || sem.Target != execution.TargetDeferred {
		t.Fatalf("semantics = %+v, want UNRESOLVED/DEFERRED", sem)
	}
	if sem.Discovery != execution.DiscoveryRequired {
		t.Fatalf("discovery = %s, want REQUIRED", sem.Discovery)
	}

	// The objective contract carries the same semantics and the verbatim
	// requirement.
	objective := d.objectiveContract()
	if objective.Semantics != sem {
		t.Fatalf("objective semantics = %+v, driver semantics = %+v", objective.Semantics, sem)
	}
	if objective.Request != d.prompt {
		t.Fatalf("objective request = %q, want %q", objective.Request, d.prompt)
	}
}

// TestSeamAdmissionBlocksDeferredMutation proves the fail-closed boundary: a
// mutation with no proven target is never admitted to a provider.
func TestSeamAdmissionBlocksDeferredMutation(t *testing.T) {
	out := EvaluatePreflightAdmission(ExecutionSpec{
		Intent:        IntentMutate,
		TargetBinding: nil,
	})
	if !out.Blocked() {
		t.Fatalf("a deferred mutation was admitted (verdict=%s); it must fail closed", out.Verdict)
	}
}

// TestSeamDiscoveryResolvesAndContractFollowsEvidence proves discovery resolves
// a deferred scope from OBSERVED evidence and that the objective contract is
// re-authored against it.
func TestSeamDiscoveryResolvesAndContractFollowsEvidence(t *testing.T) {
	root := portfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = "check this project and rewrite the HTML and CSS"
	d.resolved = d.adapter.Resolve(d.prompt)

	// Before discovery the scope is unresolved.
	if sem := d.objectiveSemantics(); sem.Scope != execution.ScopeStateUnresolved {
		t.Fatalf("pre-discovery semantics = %+v, want UNRESOLVED", sem)
	}

	d.deriveEvidenceScope()

	// A SNAPSHOT, not a view: the assertions below must not see a later rewrite
	// of the resolved set.
	got := append([]string(nil), d.resolved.Targets...)
	for _, want := range []string{"index.html", "styles.css"} {
		if !contains(got, want) {
			t.Fatalf("discovery bound %v, want %s among the observed evidence", got, want)
		}
	}
	// A file whose kind the objective never declared must not be bound.
	if contains(got, "readme.md") {
		t.Fatalf("discovery bound readme.md; the objective declared no markdown kind (%v)", got)
	}

	// The contract now follows the evidence.
	objective := d.objectiveContract()
	if objective.Semantics.Scope != execution.ScopeStateResolved {
		t.Fatalf("post-discovery semantics = %+v, want RESOLVED", objective.Semantics)
	}
	if objective.Semantics.Operation != execution.OperationModify {
		t.Fatalf("operation = %s, want MODIFY across the transition", objective.Semantics.Operation)
	}
}

// TestSeamReplanRederivesContractFromDiscovery proves that a deferred-scope
// replan re-observes the CURRENT workspace and re-authors the objective contract
// against the resolved scope instead of rebuilding an empty scope from the
// prompt.
func TestSeamReplanRederivesContractFromDiscovery(t *testing.T) {
	root := portfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = "check this project and rewrite the HTML and CSS"
	d.resolved = d.adapter.Resolve(d.prompt)

	// Author the contract while the scope is still deferred, and latch it.
	initial := d.objectiveContract()
	if initial.Semantics.Scope != execution.ScopeStateUnresolved {
		t.Fatalf("initial semantics = %+v, want UNRESOLVED", initial.Semantics)
	}
	if d.resolved.Targets != nil {
		t.Fatalf("fixture resolved a scope before discovery: %v", d.resolved.Targets)
	}

	// Replan: discovery runs against the current workspace.
	d.replanDeferredScope()

	if len(d.resolved.Targets) == 0 {
		t.Fatal("replan did not consume discovery evidence; the scope stayed empty")
	}
	replanned := d.objectiveContract()
	if replanned.Semantics.Scope != execution.ScopeStateResolved {
		t.Fatalf("replanned semantics = %+v, want RESOLVED", replanned.Semantics)
	}
	if len(replanned.Scope) == 0 {
		t.Fatal("replan reconstructed an empty scope from the original prompt")
	}
	if replanned.ObjectiveID != initial.ObjectiveID {
		t.Fatal("replan changed the objective identity")
	}
}

// TestSeamExplicitCreateAndModifyStillCompileCorrectly proves the fix did not
// collapse CREATE into MODIFY through the real classifier.
func TestSeamExplicitCreateAndModifyStillCompileCorrectly(t *testing.T) {
	root := portfolioWorkspace(t)

	create := seamDriver(t, root)
	create.prompt = "create a new file named example.txt"
	create.resolved = create.adapter.Resolve(create.prompt)
	if got := create.taskContract().Kind; got != execution.TaskCreate {
		t.Fatalf("explicit create compiled as %s, want CREATE", got)
	}
	if got := create.objectiveSemantics().Operation; got != execution.OperationCreate {
		t.Fatalf("explicit create operation = %s, want CREATE", got)
	}

	modify := seamDriver(t, root)
	modify.prompt = "update index.html"
	modify.resolved = modify.adapter.Resolve(modify.prompt)
	contract := modify.taskContract()
	if contract.Kind != execution.TaskPatch {
		t.Fatalf("explicit modification compiled as %s, want PATCH", contract.Kind)
	}
	sem := modify.objectiveSemantics()
	if sem.Operation != execution.OperationModify || sem.Scope != execution.ScopeStateResolved {
		t.Fatalf("explicit modification semantics = %+v, want MODIFY/RESOLVED", sem)
	}
}

// TestSeamEndToEnd_DeferredObjectiveResolvesThenProves is the real acceptance
// run over a real workspace: a mutating objective that names no file starts
// DEFERRED, discovery resolves the scope from observed evidence, the objective
// contract follows it, and the approved mutation reaches the existing
// evidence-gated PROVEN state.
//
// It is the portfolio fixture (HTML/CSS/JS declared) because that is the shape
// in which discovery CAN resolve a target from evidence without inventing one.
// The target names are never hard-coded into the runtime: they are read from the
// workspace by bounded discovery.
func TestSeamEndToEnd_DeferredObjectiveResolvesThenProves(t *testing.T) {
	root := portfolioWorkspace(t)

	before := map[string][]byte{}
	for _, f := range []string{"index.html", "styles.css", "script.js", "readme.md"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		before[f] = b
	}

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("the canonical deferred objective errored: %v", err)
	}

	// ── 1. OBJECTIVE: the initial semantic operation is MODIFY, never CREATE ──
	objective := d.ObjectiveContract()
	if objective.Semantics.Operation != execution.OperationModify {
		t.Fatalf("objective operation = %s, want MODIFY", objective.Semantics.Operation)
	}
	if objective.TaskKind == execution.TaskCreate {
		t.Fatal("a deferred modification compiled as CREATE")
	}

	// ── 2. DISCOVERY actually ran ────────────────────────────────────────────
	if d.derivationNote == "" {
		t.Fatal("discovery recorded no evidence")
	}
	if d.scopeResolution.State != ScopeResolved {
		t.Fatalf("scope resolution = %s, want RESOLVED", d.scopeResolution.State)
	}

	// ── 3. EVIDENCE resolved the target set (no hard-coded filename) ─────────
	var resolved []string
	for _, tt := range d.resolved.Profile.Targets {
		if tt.Resolved != "" && tt.Exists {
			resolved = append(resolved, tt.Resolved)
		}
	}
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		if !contains(resolved, want) {
			t.Fatalf("discovery bound %v, want %s from observed evidence", resolved, want)
		}
	}
	if contains(resolved, "readme.md") {
		t.Fatalf("discovery invented a target outside the declared artifact kinds: %v", resolved)
	}

	// ── 4. The resolved objective contract follows the evidence ──────────────
	if objective.Semantics.Scope != execution.ScopeStateResolved {
		t.Fatalf("objective scope = %s, want RESOLVED after discovery", objective.Semantics.Scope)
	}

	// ── 5. MUTATION entered through the existing authority boundary ──────────
	// The run must park at the human approval gate holding a real candidate; a
	// mutation never applies itself.
	if term != nil && term.State.IsTerminal() && d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("run terminated before the approval gate: %s (%s)", term.State, term.Reason)
	}
	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval || boundary.PatchID == "" {
		t.Fatalf("no approval gate holding a real candidate: %+v (term=%+v)", boundary, term)
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// ── 6. VERIFICATION + PROVEN ─────────────────────────────────────────────
	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied || obs.Objective.MutatedFiles == 0 {
		t.Fatalf("no durable mutation observed: mutation=%s files=%d", obs.Objective.Mutation, obs.Objective.MutatedFiles)
	}
	if !obs.Objective.VerificationRan {
		t.Fatal("verification never ran over the approved mutation")
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
	if got := d.ObjectiveProgress(); got != execution.ProgressProven {
		t.Fatalf("objective progress = %s, want PROVEN", got)
	}

	// The model's bytes really reached disk.
	changed := 0
	for name, prior := range before {
		now, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(now) != string(prior) {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("approval produced no filesystem change")
	}
}

// TestSeamLiteralFailingPromptFailsClosed runs the reported objective verbatim:
// `$prompt check this project and rewrite it`. It declares no artifact kind, so
// discovery MUST NOT invent a target. The runtime stays a deferred MODIFY, never
// CREATE, and mutates nothing.
func TestSeamLiteralFailingPromptFailsClosed(t *testing.T) {
	root := portfolioWorkspace(t)

	before := map[string][]byte{}
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(root, e.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		before[e.Name()] = b
	}

	bus := events.NewBus(events.DefaultBufferSize)
	// The provider refuses any invocation that names no target, which is the
	// proof that a target was never invented: a read-only planning call carries
	// no file target.
	mock := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	_, _ = d.Run(context.Background(), "check this project and rewrite it")

	// Discovery ran and could not resolve a target, so it did NOT invent one.
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("discovery invented targets for an objective that declared no artifact kind: %v", d.resolved.Targets)
	}
	if d.scopeResolution.State != ScopeUnresolved {
		t.Fatalf("scope resolution = %s, want UNRESOLVED", d.scopeResolution.State)
	}

	// The compiled objective is a deferred MODIFY, never a CREATE.
	objective := d.ObjectiveContract()
	if objective.TaskKind == execution.TaskCreate || objective.Semantics.Operation == execution.OperationCreate {
		t.Fatalf("the literal failing prompt compiled as CREATE: %+v", objective.Semantics)
	}
	if objective.Semantics.Operation != execution.OperationModify {
		t.Fatalf("operation = %s, want MODIFY", objective.Semantics.Operation)
	}

	// No invented target, no mutation, no approval gate holding a candidate.
	if boundary := d.Boundary(); boundary != nil && boundary.PatchID != "" {
		t.Fatalf("the runtime staged a mutation for a target it never discovered: %+v", boundary)
	}
	for name, prior := range before {
		now, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(now) != string(prior) {
			t.Fatalf("the runtime mutated %s for an unresolved objective", name)
		}
	}
}
