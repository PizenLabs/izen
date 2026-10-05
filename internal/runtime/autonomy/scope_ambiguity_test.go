package autonomy

// ── Acceptance: AMBIGUOUS EVIDENCE ≠ RESOLVED SCOPE ──────────────────────────
//
// The invariant this file pins, in the runtime's own words:
//
//	UNRESOLVED TARGET ≠ CREATE            (established)
//	AMBIGUOUS EVIDENCE ≠ RESOLVED SCOPE  (this file)
//
// The reported failure was a non-empty target list being read as a resolution.
// "rewrite the HTML and CSS" over a workspace holding three html files and three
// stylesheets produced six targets, and six targets is not an answer to "which
// of these did you mean?".
//
// The tests run the REAL Driver → ExecutorAdapter → RuntimeExecutor path over a
// REAL directory with a scripted provider, and they measure the four things that
// matter, in order of strength:
//
//	derivation status → resolved scope → mutation authority → bytes on disk
//
// Every assertion below reads runtime state or the filesystem. None of them
// accepts a progress enum as proof, because a progress enum can be made to say
// the right thing while the mutation candidate is still authorized.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ambiguousObjective is the reported prompt. It names ARTIFACT KINDS and no file.
const ambiguousObjective = "check this project and rewrite the HTML and CSS"

// uniqueObjective is the same prompt against a workspace with exactly one file
// per declared kind. The ONLY difference is the workspace, so any divergence
// between the two runs is attributable to the evidence and not to the wording.
const uniqueObjective = "check this project and rewrite the HTML and CSS"

// ambiguitySnapshot reads every regular file under root so a test can compare
// the whole tree before and after, not just the files it expected to change.
func ambiguitySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ambiguityDiff returns the paths whose bytes differ between two snapshots.
func ambiguityDiff(before, after map[string]string) []string {
	var changed []string
	for name, prior := range before {
		if now, ok := after[name]; !ok || now != prior {
			changed = append(changed, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changed = append(changed, name)
		}
	}
	return changed
}

// TestAmbiguity_AmbiguousEvidenceBindsNoScope proves the defect is closed at the
// driver seam: an AMBIGUOUS derivation binds nothing, and the scope position
// records the ambiguity rather than a resolution.
func TestAmbiguity_AmbiguousEvidenceBindsNoScope(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = ambiguousObjective
	d.resolved = d.adapter.Resolve(d.prompt)

	// The gateway genuinely resolves nothing: the objective names no file.
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("fixture resolved a scope from a file-less objective: %v", d.resolved.Targets)
	}

	d.deriveEvidenceScope()

	// 1. THE DERIVATION IS AMBIGUOUS — a typed verdict, not a length.
	if got := d.scopeDerivation.Status; got != execution.DerivationAmbiguous {
		t.Fatalf("derivation status = %s, want AMBIGUOUS (%v / %s)",
			got, d.scopeDerivation.Targets, d.scopeDerivation.Reason)
	}

	// 2. NO SCOPE IS BOUND. This is the specific defect: before the fix this was
	// all six files.
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("an ambiguous derivation bound a scope: %v", d.resolved.Targets)
	}
	for _, candidate := range d.scopeDerivation.Targets {
		if contains(d.resolved.Targets, candidate) {
			t.Fatalf("candidate %q was bound as a target; ambiguity must bind nothing", candidate)
		}
	}

	// 3. THE SCOPE POSITION IS AMBIGUOUS AND AUTHORIZES NOTHING.
	if d.scopeResolution.State != ScopeAmbiguous {
		t.Fatalf("scope position = %s, want AMBIGUOUS", d.scopeResolution.State)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatal("an AMBIGUOUS scope position reports itself as authorizing mutation")
	}
	if len(d.scopeResolution.Targets) != 0 {
		t.Fatalf("the scope record carries bound targets %v; candidates belong in Candidates",
			d.scopeResolution.Targets)
	}

	// 4. THE CANDIDATES ARE PRESERVED AS EVIDENCE. The human needs to be asked
	// a real question, and the candidates must be the KIND-FILTERED set the
	// objective is about — not every file in the workspace.
	for _, want := range []string{"a.html", "b.html", "index.html", "a.css", "b.css", "styles.css"} {
		if !contains(d.scopeResolution.Candidates, want) {
			t.Errorf("candidate %q is missing from the ambiguity record: %v", want, d.scopeResolution.Candidates)
		}
	}
	if contains(d.scopeResolution.Candidates, "script.js") {
		t.Errorf("the objective declared no javascript kind, so script.js is not a candidate: %v",
			d.scopeResolution.Candidates)
	}

	// 5. THE OBJECTIVE REMAINS A DEFERRED MODIFICATION. UNRESOLVED TARGET ≠ CREATE
	// still holds; nothing about this fix may disturb it.
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
	if d.taskContract().Kind == execution.TaskCreate {
		t.Fatal("an ambiguous objective compiled as CREATE")
	}
}

// TestAmbiguity_ExplicitTargetsOutrankAmbiguousEvidence proves the distinction the
// task calls out: SEVERAL INTENDED TARGETS is not AMBIGUITY. A user who names
// both files has stated a scope, and discovery may neither narrow it nor widen
// it.
func TestAmbiguity_ExplicitTargetsOutrankAmbiguousEvidence(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = "rewrite a.html and b.html"
	d.resolved = d.adapter.Resolve(d.prompt)

	// A SNAPSHOT, not a view: the assertions below must not see a later rewrite
	// of the resolved set.
	got := append([]string(nil), d.resolved.Targets...)
	for _, want := range []string{"a.html", "b.html"} {
		if !contains(got, want) {
			t.Fatalf("explicit target %q was not bound: %v", want, got)
		}
	}

	// deriveEvidenceScope must be a NO-OP here. The prompt declares no artifact
	// kind, so a derivation would report UNRESOLVED — and a driver that mistook
	// that for a negative verdict would be one refactor away from discarding an
	// explicit scope.
	d.deriveEvidenceScope()

	if d.scopeResolution.State != ScopeResolved {
		t.Fatalf("scope position = %s, want RESOLVED for an explicitly named scope", d.scopeResolution.State)
	}
	if !d.scopeResolution.AuthorizesMutation() {
		t.Fatal("an explicitly resolved scope does not authorize mutation")
	}
	if len(d.scopeDerivation.Targets) != 0 {
		t.Fatalf("derivation ran over an already-proven scope and produced %v", d.scopeDerivation.Targets)
	}
	after := append([]string(nil), d.resolved.Targets...)
	for _, want := range []string{"a.html", "b.html"} {
		if !contains(after, want) {
			t.Fatalf("the explicit scope lost %q: %v", want, after)
		}
	}
	// The ambiguity in the workspace is real and irrelevant here: the user named
	// the files, so no disambiguation question is asked.
	if len(d.scopeResolution.Candidates) != 0 {
		t.Fatalf("an explicitly scoped run raised candidates %v", d.scopeResolution.Candidates)
	}
}

// TestAmbiguity_UniqueDiscoveryStillResolves is the positive control. The SAME
// prompt against a workspace with one html and one css file must still resolve,
// and it must resolve through the same seam — otherwise this fix would have
// simply disabled evidence-bound discovery.
func TestAmbiguity_UniqueDiscoveryStillResolves(t *testing.T) {
	root := portfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = uniqueObjective
	d.resolved = d.adapter.Resolve(d.prompt)

	d.deriveEvidenceScope()

	if got := d.scopeDerivation.Status; got != execution.DerivationUnique {
		t.Fatalf("derivation status = %s, want UNIQUE (%v / %s)",
			got, d.scopeDerivation.Targets, d.scopeDerivation.Reason)
	}
	if d.scopeResolution.State != ScopeResolved || !d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("scope position = %s, want RESOLVED", d.scopeResolution.State)
	}
	// A SNAPSHOT, not a view: the assertions below must not see a later rewrite
	// of the resolved set.
	bound := append([]string(nil), d.resolved.Targets...)
	for _, want := range []string{"index.html", "styles.css"} {
		if !contains(bound, want) {
			t.Fatalf("unique derivation did not bind %q: %v", want, bound)
		}
	}
	// script.js is observed but its kind was never declared: a UNIQUE verdict is
	// still evidence-bounded, not "everything in the directory".
	if contains(bound, "script.js") {
		t.Fatalf("unique derivation bound script.js, whose kind was never declared: %v", bound)
	}
	if contains(bound, "readme.md") {
		t.Fatalf("unique derivation bound readme.md: %v", bound)
	}

	sem := d.objectiveSemantics()
	if sem.Scope != execution.ScopeStateResolved || sem.Operation != execution.OperationModify {
		t.Fatalf("semantics = %+v, want MODIFY/RESOLVED", sem)
	}
}

// TestAmbiguity_GateRefusesAmbiguousScopeAtAdmission proves the authority half:
// the ADMISSION GATE itself rejects an ambiguous scope, on its own, with no
// driver cooperation. This is the test that would fail if someone "fixed" the
// bug by changing a progress enum while the mutation candidate stayed authorized.
func TestAmbiguity_GateRefusesAmbiguousScopeAtAdmission(t *testing.T) {
	// A spec that would otherwise be fully admissible: a proven target, a bound
	// boundary, authoritative evidence. The ONLY thing wrong with it is that the
	// derivation is ambiguous, and the verdict must still be a halt.
	spec := ExecutionSpec{
		Intent: IntentMutate,
		TargetBinding: &execution.TargetBindingResult{
			State:              execution.TargetStateResolvedSet,
			Phase:              execution.PhaseTargetBindingResolved,
			Status:             execution.BindingResolved,
			Paths:              []string{"a.html", "b.html"},
			Binding:            &execution.TargetBinding{Path: "a.html", Explicit: false, Exists: true},
			DiscoveryPerformed: true,
			Reason:             "a perfect looking binding",
		},
		ExplicitTargets:  []string{"a.html", "b.html"},
		ContextChannels:  []ContextChannel{{Kind: "target", Source: "a.html", Authoritative: true}},
		MutationBoundary: MutationBoundaryBound,
		Evidence:         EvidenceProduced,
		Derivation: execution.Derivation{
			Status:  execution.DerivationAmbiguous,
			Targets: []string{"a.html", "b.html", "index.html"},
			Kinds:   []string{"html"},
		},
	}

	outcome := EvaluatePreflightAdmission(spec)
	if !outcome.Blocked() {
		t.Fatalf("an ambiguous scope was ADMITTED (verdict=%s): the gate is the authority and must halt", outcome.Verdict)
	}
	if outcome.Verdict != AdmissionDisambiguate {
		t.Fatalf("verdict = %s, want DISAMBIGUATE — a human remedy exists and must be offered", outcome.Verdict)
	}
	if len(outcome.Candidates) != 3 {
		t.Fatalf("candidates = %v, want all three observed html files", outcome.Candidates)
	}
	if spec.ProviderCalls != 0 || spec.ProviderTokens != 0 {
		t.Fatal("a blocked spec must carry zero provider facts")
	}

	// Removing the ambiguity — and nothing else — admits it. That is the whole
	// point: the gate is discriminating on the verdict, not on the target count.
	spec.Derivation = execution.Derivation{
		Status:  execution.DerivationUnique,
		Targets: []string{"a.html", "b.html"},
		Kinds:   []string{"html"},
	}
	if admitted := EvaluatePreflightAdmission(spec); admitted.Verdict != AdmissionAdmit {
		t.Fatalf("a uniquely derived scope was refused (verdict=%s reason=%q)", admitted.Verdict, admitted.Reason)
	}
}

// TestAmbiguity_AmbiguousRunMutatesNothing is the end-to-end proof on a real
// workspace: no provider call, no approval candidate, zero bytes changed.
//
// It asserts the four negative facts independently, because they fail
// independently: an approval gate can exist without a provider call, and a
// provider call can happen without a write.
func TestAmbiguity_AmbiguousRunMutatesNothing(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &ambiguityProbeProvider{}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	_, err := d.Run(context.Background(), ambiguousObjective)
	if err != nil {
		t.Fatalf("the ambiguous objective errored instead of parking: %v", err)
	}

	// 1. THE AMBIGUITY WAS OBSERVED AND TYPED.
	if got := d.scopeDerivation.Status; got != execution.DerivationAmbiguous {
		t.Fatalf("derivation status = %s, want AMBIGUOUS (%s)", got, d.scopeDerivation.Reason)
	}
	if d.scopeResolution.State != ScopeAmbiguous {
		t.Fatalf("scope position = %s, want AMBIGUOUS", d.scopeResolution.State)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatal("the scope record claims mutation authority for an ambiguous objective")
	}

	// 2. NO SCOPE, NO SEMANTIC RESOLUTION.
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("targets = %v; an ambiguous derivation bound a scope", d.resolved.Targets)
	}
	if got := d.objectiveSemantics(); got.Scope == execution.ScopeStateResolved {
		t.Fatalf("objective semantics = %+v; an ambiguous scope is not RESOLVED", got)
	}

	// 3. THE MUTATION CANDIDATE IS NOT AUTHORIZED. No approval boundary, and
	// therefore no held patch a human could approve by accident.
	boundary := d.Boundary()
	if boundary != nil {
		if boundary.Action == autonomy.HumanBoundaryApproval {
			t.Fatalf("an approval candidate was created for an ambiguous scope: patch=%q targets=%v",
				boundary.PatchID, boundary.Targets)
		}
		if boundary.PatchID != "" {
			t.Fatalf("a parked boundary holds a mutation candidate: %+v", boundary)
		}
	}

	// 4. NO PROVIDER WAS BILLED. "No Evidence, No Provider" is an observable
	// fact, not an inference from the filesystem delta.
	if calls := provider.Calls(); calls != 0 {
		t.Fatalf("provider was invoked %d time(s) over an ambiguous scope; a disambiguation request must cost nothing", calls)
	}

	// 5. ZERO FILESYSTEM DELTA across the WHOLE tree.
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("the runtime mutated %v under an ambiguous scope; a candidate list is never a scope", changed)
	}

	// 6. The run is parked for a human with the candidates as OPTIONS — the
	// correct outcome is a question, not a silent success or a refusal to look.
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("run state = %s, want AWAITING_HUMAN", d.State())
	}
	if boundary == nil || len(boundary.Options) == 0 {
		t.Fatalf("the run parked without offering the human a choice: %+v", boundary)
	}
	for _, want := range []string{"a.html", "index.html"} {
		if !contains(boundary.Options, want) {
			t.Errorf("disambiguation option %q missing from %v", want, boundary.Options)
		}
	}
}

// TestAmbiguity_UniqueRunStillMutatesAndProves is the other end of the same
// pair: with UNIQUE evidence the runtime takes its existing authority path —
// approval gate, real bytes, verification, PROVEN. A fix that made ambiguity
// safe by disabling derivation would pass the negative test above and fail this
// one.
func TestAmbiguity_UniqueRunStillMutatesAndProves(t *testing.T) {
	root := portfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	term, err := d.Run(context.Background(), uniqueObjective)
	if err != nil {
		t.Fatalf("the unique objective errored: %v", err)
	}
	if got := d.scopeDerivation.Status; got != execution.DerivationUnique {
		t.Fatalf("derivation status = %s, want UNIQUE (%s)", got, d.scopeDerivation.Reason)
	}
	if d.scopeResolution.State != ScopeResolved {
		t.Fatalf("scope position = %s, want RESOLVED", d.scopeResolution.State)
	}

	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval || boundary.PatchID == "" {
		t.Fatalf("unique evidence did not reach the approval gate: %+v (term=%+v)", boundary, term)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied || obs.Objective.MutatedFiles == 0 {
		t.Fatalf("no durable mutation: mutation=%s files=%d", obs.Objective.Mutation, obs.Objective.MutatedFiles)
	}
	if !obs.Objective.VerificationRan {
		t.Fatal("verification never ran over the approved mutation")
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) == 0 {
		t.Fatal("the approved mutation wrote no bytes; the unique path regressed")
	}
}

// TestStrategyProjection_CanonicalAndAdmissionAgree pins the secondary defect:
// ONE canonical semantic projection from strategy to mutation semantics, read by
// both the canonical intent authority and the admission gate.
//
// The audit found the same strategy meaning two things. If this test ever needs
// a special case for MultiFilePlanning, the projection has been forked again.
func TestStrategyProjection_CanonicalAndAdmissionAgree(t *testing.T) {
	strategies := []strategy.ExecutionStrategy{
		strategy.DirectDeterministic,
		strategy.TargetedMutation,
		strategy.TargetedReasoning,
		strategy.RepositoryInvestigation,
		strategy.MultiFilePlanning,
		strategy.DirectResponse,
		strategy.HumanClarification,
	}
	for _, s := range strategies {
		semantics := strategy.MutationSemanticsOf(s)
		admission := intentClassForStrategy(s)
		canonicalNeedsMutation := semantics.RequiresMutationContract()

		// THE CORE INVARIANT. The canonical intent authority and the admission
		// gate read the SAME projection, so a strategy that carries mutation
		// semantics can never be one that admission calls read-only. There is no
		// third answer and no per-strategy exception to remember.
		admissionIsMutationShaped := admission.IsMutation() || admission == IntentPlan
		if canonicalNeedsMutation != admissionIsMutationShaped {
			t.Errorf("%s: canonical intent requires a mutation contract (%t) but admission classified it %s (%t); "+
				"one strategy must have one mutation semantics", s, canonicalNeedsMutation, admission, admissionIsMutationShaped)
		}

		switch semantics {
		case strategy.MutationSemanticsApplied:
			// The dispatched turn writes the workspace, so admission must hold it
			// to the mutation gate.
			if !admission.IsMutation() {
				t.Errorf("%s: semantics say APPLIED but admission says %s", s, admission)
			}
		case strategy.MutationSemanticsProposal:
			// The architecturally justified difference, asserted explicitly rather
			// than tolerated: a PROPOSAL turn admits as PLAN (it writes nothing on
			// its own) while the LIFECYCLE still requires the mutation contract.
			// This is the one place the two axes differ, and the reason is that
			// "what this turn does" and "what the objective will do" are different
			// questions. admissionIntent (below) closes the gap for a mutating
			// objective, so PROPOSAL can never wave an unresolved scope through.
			if admission != IntentPlan {
				t.Errorf("%s: PROPOSAL must admit as PLAN, got %s", s, admission)
			}
			if !canonicalNeedsMutation {
				t.Errorf("%s: PROPOSAL must require a mutation contract for the lifecycle", s)
			}
		case strategy.MutationSemanticsReadOnly:
			if admission.IsMutation() || admission == IntentPlan {
				t.Errorf("%s: READ-ONLY semantics admitted as %s", s, admission)
			}
		}
	}
}

// TestStrategyProjection_MutatingObjectiveIsGatedUnderEveryMutationStrategy is
// the half of the invariant that actually held the defect in place.
//
// MultiFilePlanning admits as PLAN, and read-only admission skips the target /
// boundary / evidence gate entirely. A mutating objective carried by a PROPOSAL
// strategy was therefore waved through the gate with an unresolved scope — the
// same hole, reached through the strategy axis instead of the derivation axis.
//
// admissionIntent closes it by consulting the objective's own mutation
// semantics: whatever the strategy's SHAPE, an objective that will write is
// admitted as a mutation.
func TestStrategyProjection_MutatingObjectiveIsGatedUnderEveryMutationStrategy(t *testing.T) {
	for _, s := range []strategy.ExecutionStrategy{
		strategy.DirectDeterministic,
		strategy.TargetedMutation,
		strategy.MultiFilePlanning,
	} {
		if !strategy.MutationSemanticsOf(s).RequiresMutationContract() {
			t.Fatalf("%s: fixture is not a mutation-capable strategy", s)
		}
		// A mutating objective under this strategy.
		root := ambiguousPortfolioWorkspace(t)
		d := seamDriver(t, root)
		d.prompt = ambiguousObjective
		d.resolved = d.adapter.Resolve(d.prompt)
		d.resolved.Profile.Strategy = s
		d.deriveEvidenceScope()

		sem := d.objectiveSemantics()
		if !sem.Operation.RequiresMutation() {
			t.Fatalf("%s: fixture objective is not mutating (%s)", s, sem.Operation)
		}
		if got := d.admissionIntent(); !got.IsMutation() {
			t.Errorf("%s: a mutating objective admitted as %s; the mutation gate was skipped", s, got)
		}

		// And the gate must actually halt it.
		if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); !outcome.Blocked() {
			t.Errorf("%s: an ambiguous scope was ADMITTED (verdict=%s)", s, outcome.Verdict)
		}
	}
}

// TestStrategyProjection_ReadOnlyObjectiveStaysReadOnly is the counterweight: the
// gate must not be widened into blocking honest read-only planning. A PROPOSAL
// strategy carrying a read-only objective still admits as PLAN.
func TestStrategyProjection_ReadOnlyObjectiveStaysReadOnly(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = "explain what this project does"
	d.resolved = d.adapter.Resolve(d.prompt)
	d.resolved.Profile.Strategy = strategy.MultiFilePlanning

	sem := d.objectiveSemantics()
	if sem.Operation.RequiresMutation() {
		t.Fatalf("fixture objective is mutating (%s); the test no longer covers the read-only case", sem.Operation)
	}
	if got := d.admissionIntent(); got.IsMutation() {
		t.Fatalf("a read-only objective admitted as %s; read-only work needs no mutation target", got)
	}
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); outcome.Blocked() {
		t.Fatalf("a read-only objective was blocked (verdict=%s reason=%q)", outcome.Verdict, outcome.Reason)
	}
}

// TestStrategyProjection_UnknownStrategyCannotWidenAuthority proves the
// projection is fail-closed: a strategy the taxonomy does not know carries no
// mutation semantics, so adding one without deciding its semantics can never
// grant authority by omission.
func TestStrategyProjection_UnknownStrategyCannotWidenAuthority(t *testing.T) {
	unknown := strategy.ExecutionStrategy("some_future_strategy")
	if got := strategy.MutationSemanticsOf(unknown); got != strategy.MutationSemanticsReadOnly {
		t.Fatalf("unknown strategy semantics = %s, want READ-ONLY", got)
	}
	if got := intentClassForStrategy(unknown); got != IntentAsk {
		t.Fatalf("unknown strategy admission = %s, want ASK", got)
	}
	if strategy.MutationSemanticsOf(unknown).RequiresMutationContract() {
		t.Fatal("an unknown strategy must never require a mutation contract")
	}
}

// TestAmbiguity_ReplanOnAmbiguousScopeBindsNothing proves the recovery path does
// not treat ambiguity as resolution. A replan re-derives from CURRENT workspace
// evidence; while the evidence is still ambiguous, nothing may bind and the
// objective contract must not be re-opened as though a scope existed.
func TestAmbiguity_ReplanOnAmbiguousScopeBindsNothing(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = ambiguousObjective
	d.resolved = d.adapter.Resolve(d.prompt)

	// Author the objective contract while the scope is deferred, and note it: a
	// replan must not silently replace it.
	initial := d.objectiveContract()
	if initial.Semantics.Scope != execution.ScopeStateUnresolved {
		t.Fatalf("initial scope = %s, want UNRESOLVED", initial.Semantics.Scope)
	}

	d.replanDeferredScope()

	if got := d.scopeDerivation.Status; got != execution.DerivationAmbiguous {
		t.Fatalf("replan derivation status = %s, want AMBIGUOUS", got)
	}
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("a replan bound %v from ambiguous evidence", d.resolved.Targets)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("a replan turned ambiguity into mutation authority (%s)", d.scopeResolution.State)
	}
	if got := d.objectiveContract(); got.ObjectiveID != initial.ObjectiveID {
		t.Fatalf("the replan changed the objective identity: %q -> %q", initial.ObjectiveID, got.ObjectiveID)
	}

	// The converse half: NEW EVIDENCE that makes the scope unique must still be
	// able to reach UNIQUE, so the runtime is not permanently wedged in
	// ambiguity. Leaving exactly one observed file per declared kind is exactly
	// that new evidence.
	for _, name := range []string{"a.html", "b.html", "a.css", "b.css"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	d.replanDeferredScope()
	if got := d.scopeDerivation.Status; got != execution.DerivationUnique {
		t.Fatalf("after new evidence the derivation status = %s, want UNIQUE (%s)", got, d.scopeDerivation.Reason)
	}
	if !d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("new evidence did not reach a resolved scope (%s)", d.scopeResolution.State)
	}
}

// TestAmbiguity_ZeroProviderCallsIsObservable guards the measurement itself: the
// negative tests above read provider.Calls(), so that counter has to be real.
func TestAmbiguity_ZeroProviderCallsIsObservable(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	bus := events.NewBus(events.DefaultBufferSize)
	provider := &ambiguityProbeProvider{}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("park: %v", err)
	}
	if got := provider.Calls(); got != 0 {
		t.Fatalf("provider calls = %d; the ambiguity fix must halt before dispatch", got)
	}

	// The same counter must be non-zero once the scope is unambiguous, otherwise
	// "zero calls" would be an artifact of an unwired provider rather than a
	// consequence of the invariant.
	uniqueRoot := portfolioWorkspace(t)
	mock := &portfolioProvider{served: map[string]int{}}
	bus2 := events.NewBus(events.DefaultBufferSize)
	x2 := testExecutor(t, uniqueRoot, mock, bus2)
	adapter2 := NewExecutorAdapter(uniqueRoot, execution.NewIntentGateway(root), x2)
	d2 := NewDriver(adapter2, bus2)
	if _, err := d2.Run(context.Background(), uniqueObjective); err != nil {
		t.Fatalf("unique run: %v", err)
	}
	if mock.calls == 0 {
		t.Fatal("the unique run made no provider call; the unique path is not exercising the provider at all")
	}
	if !strings.Contains(d2.derivationNote, "observed workspace evidence") {
		t.Errorf("unique run derivation note = %q", d2.derivationNote)
	}
}
