package autonomy

// ── Acceptance: A CLARIFICATION INVALIDATES THE SCOPE DERIVED BEFORE IT ───────
//
// The ambiguity audit closed the discovery seam and proved the negative case:
// ambiguous evidence binds nothing. It left one derived fact with a second owner,
// and that fact is `d.scopeDerivation`.
//
// The runtime's authority over WHICH files a mutation may touch is established in
// two steps that do not happen at the same time:
//
//	AMBIGUOUS   discovery observed several candidates; none is proven
//	↓
//	human clarification
//	↓
//	the authoritative request changed — a human named a file
//
// Before this file, the second step rewrote only the authoritative half
// (`d.req.Targets`) and left every DERIVED half describing the first step. The
// admission gate reads the typed derivation verdict BEFORE it reads the target
// binding, so the run answered a clarification with the very question the human
// had just answered, forever:
//
//	AMBIGUOUS → clarify("index.html") → DISAMBIGUATE(6 candidates) → clarify again
//
// The invariant this file pins is a lifecycle, not a field:
//
//	authoritative request changes
//	    → every fact DERIVED from the previous resolution is invalidated
//	    → the canonical gateway re-resolves
//	    → the canonical derivation runs again
//	    → admission reads the NEW derived state
//
// and the authority half of it, which is the half that is easy to buy with
// invalidation:
//
//	invalidating a verdict must never BECOME a grant
//
// so a human naming a file the workspace does not contain still fails closed —
// the invalidation path and the refusal path are the same path.
//
// Every assertion reads runtime state or the filesystem. None of them accepts a
// progress enum, and none of them infers a proof from the absence of a crash.

import (
	"context"
	"fmt"
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
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// clarificationProbe is a scripted provider that answers each invocation for the
// file the runtime actually dispatched.
//
// It falls back to the previously dispatched target for an invocation that names
// no file (a continuation over the same target does not restate it). It never
// invents an answer for an unknown file: an invocation naming a target the
// scenario does not describe is a test failure, because it would mean the runtime
// dispatched work the clarified scope does not contain.
type clarificationProbe struct {
	mu      sync.Mutex
	calls   int
	seen    []string
	refused []string
	last    string
}

func (p *clarificationProbe) Name() string { return "clarification-scripted" }

func (p *clarificationProbe) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	target := dispatchedTarget(req)
	if target == "" {
		target = p.last
	}
	if target == "" {
		return nil, fmt.Errorf("invocation #%d carried no identifiable target", p.calls)
	}
	artifact, ok := ambiguousPortfolioArtifacts[target]
	if !ok {
		p.refused = append(p.refused, target)
		return nil, fmt.Errorf("invocation #%d targeted %q, which the clarification scenario does not describe",
			p.calls, target)
	}
	p.last = target
	p.seen = append(p.seen, target)
	resp := artifact.Response
	return &resp, nil
}

func (p *clarificationProbe) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in clarificationProbe")
}

// Calls returns the observed provider invocation count.
func (p *clarificationProbe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Dispatched returns the ordered set of files the runtime put in front of the
// model. It is the record of what the SCOPE actually was, read from the model
// boundary rather than from the driver's own bookkeeping.
func (p *clarificationProbe) Dispatched() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// Refused returns the files the runtime dispatched that the scenario does not
// describe. A non-empty result means the runtime sent work outside the clarified
// scope — the failure mode a candidate-list scope used to produce.
func (p *clarificationProbe) Refused() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.refused...)
}

// assertScopeConfined fails when the runtime dispatched anything outside the
// clarified scope. The dispatched set alone cannot catch it: an undescribed file
// is refused by the provider and therefore never recorded as dispatched.
func assertScopeConfined(t *testing.T, probe *clarificationProbe, scope ...string) {
	t.Helper()
	if refused := probe.Refused(); len(refused) != 0 {
		t.Fatalf("the runtime dispatched %v, which the clarified scope %v does not contain",
			refused, scope)
	}
	for _, target := range probe.Dispatched() {
		if !contains(scope, target) {
			t.Fatalf("the runtime dispatched %q over a scope of %v", target, scope)
		}
	}
}

// clarificationContentSnapshot is ambiguitySnapshot over the USER's workspace
// files. It skips the runtime's own `.izen/` journal — patches, mutation audit
// and OCC checkpoints — because those records are the runtime's bookkeeping about
// a mutation, not a mutation of the user's files. The zero-delta assertions use
// ambiguitySnapshot and therefore see the whole tree, `.izen/` included; only the
// "which file did the approved mutation change" assertions need this narrower
// view.
func clarificationContentSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".izen" {
				return filepath.SkipDir
			}
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

// clarificationHarness builds the real Driver → ExecutorAdapter →
// RuntimeExecutor path over a real workspace with a scripted provider.
func clarificationHarness(t *testing.T, root string) (*events.Bus, *clarificationProbe, *Driver) {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	probe := &clarificationProbe{}
	x := testExecutor(t, root, probe, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return bus, probe, NewDriver(adapter, bus)
}

// assertAmbiguousParking proves the pre-clarification half: the objective really
// was ambiguous, nothing was bound, and the disambiguation question is what the
// human was asked.
func assertAmbiguousParking(t *testing.T, d *Driver, probe *clarificationProbe) {
	t.Helper()
	if got := d.scopeDerivation.StatusOrUnresolved(); got != execution.DerivationAmbiguous {
		t.Fatalf("derivation status = %s, want AMBIGUOUS (%s)", got, d.scopeDerivation.Reason)
	}
	if d.scopeResolution.State != ScopeAmbiguous {
		t.Fatalf("scope position = %s, want AMBIGUOUS", d.scopeResolution.State)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatal("an AMBIGUOUS scope reports mutation authority")
	}
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("targets = %v; an ambiguous derivation bound a scope", d.resolved.Targets)
	}
	if len(d.req.Targets) != 0 {
		t.Fatalf("request targets = %v; an ambiguous objective stated none", d.req.Targets)
	}
	if len(d.scopeResolution.Targets) != 0 {
		t.Fatalf("the scope record carries bound targets %v", d.scopeResolution.Targets)
	}
	for _, want := range []string{"a.html", "b.html", "index.html", "a.css", "b.css", "styles.css"} {
		if !contains(d.scopeResolution.Candidates, want) {
			t.Errorf("candidate %q missing from the ambiguity record: %v", want, d.scopeResolution.Candidates)
		}
	}
	if calls := probe.Calls(); calls != 0 {
		t.Fatalf("provider was billed %d time(s) before a human answered the ambiguity", calls)
	}
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); !outcome.Blocked() {
		t.Fatalf("an ambiguous scope was ADMITTED (verdict=%s)", outcome.Verdict)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want AWAITING_HUMAN", d.State())
	}
}

// TestClarification_AmbiguousEvidenceIsInvalidatedAndReDerived is TEST 1 over the
// REAL clarification/resume mechanism: the reported prompt, the real workspace,
// one human answer, and the whole chain from the new authoritative target to real
// bytes on exactly that file.
func TestClarification_AmbiguousEvidenceIsInvalidatedAndReDerived(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)
	_, probe, d := clarificationHarness(t, root)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("the ambiguous objective errored instead of parking: %v", err)
	}
	assertAmbiguousParking(t, d, probe)

	// ── THE HUMAN ANSWERS ──────────────────────────────────────────────
	term, err := d.ResumeClarify(context.Background(), "index.html")
	if err != nil {
		t.Fatalf("ResumeClarify: %v", err)
	}

	// 1. THE AUTHORITATIVE REQUEST CHANGED. The human's statement is the input
	//    now, and it was not cleared by anything the runtime did to its own
	//    bookkeeping.
	if got := d.req.Targets; len(got) != 1 || got[0] != "index.html" {
		t.Fatalf("authoritative targets = %v, want [index.html]", got)
	}

	// 2. THE STALE AMBIGUOUS DERIVATION IS GONE. This is the reported defect:
	//    the verdict that outlived the request it described.
	if d.scopeDerivation.IsAmbiguous() {
		t.Fatalf("the pre-clarification AMBIGUOUS derivation survived clarification: %v (%s)",
			d.scopeDerivation.Targets, d.scopeDerivation.Reason)
	}
	if len(d.scopeResolution.Candidates) != 0 {
		t.Fatalf("the ambiguity record's candidates survived clarification: %v", d.scopeResolution.Candidates)
	}

	// 3. THE CANONICAL PATH RE-DERIVED, AND IT RE-RESOLVED. Only the strategy
	//    gateway can turn "@index.html" into a bound target and choose the
	//    mutation contract; a patched status field can do neither. The strategy
	//    therefore CHANGES, which is the externally visible proof that the
	//    resolution authority ran again rather than a field being overwritten.
	if d.resolved.Profile.Strategy != strategy.TargetedMutation {
		t.Fatalf("strategy = %s after clarification, want the gateway's targeted_mutation; "+
			"the clarification did not go through canonical resolution", d.resolved.Profile.Strategy)
	}
	if len(d.resolved.Targets) != 1 || d.resolved.Targets[0] != "index.html" {
		t.Fatalf("derived targets = %v, want [index.html]", d.resolved.Targets)
	}

	// 4. THE SCOPE IS RESOLVED — by the derivation, for the current request.
	if d.scopeResolution.State != ScopeResolved {
		t.Fatalf("scope position = %s, want RESOLVED after clarification (%s)",
			d.scopeResolution.State, d.scopeResolution.Reason)
	}
	if !d.scopeResolution.AuthorizesMutation() {
		t.Fatal("a clarified, gateway-resolved scope does not authorize mutation")
	}
	if got := d.scopeResolution.Targets; len(got) != 1 || got[0] != "index.html" {
		t.Fatalf("scope targets = %v, want [index.html]", got)
	}

	// 5. NO CANDIDATE LEAKED INTO THE SCOPE. The four files the human did NOT
	//    name were candidates a moment ago and are not the mutation scope now.
	for _, old := range []string{"a.html", "b.html", "a.css", "b.css", "styles.css"} {
		if contains(d.scopeResolution.Targets, old) || contains(d.resolved.Targets, old) {
			t.Fatalf("candidate %q became part of the clarified scope: record=%v resolved=%v",
				old, d.scopeResolution.Targets, d.resolved.Targets)
		}
	}

	// 6. THE OBJECTIVE CONTRACT WAS RE-AUTHORED FOR THE NEW SCOPE. Its Scope is a
	//    function of the target set, so a surviving contract would judge the
	//    mutation against the files the request had BEFORE the human answered.
	contract := d.ObjectiveContract()
	if len(contract.Scope) != 1 || contract.Scope[0] != "index.html" {
		t.Fatalf("objective contract scope = %v, want [index.html]", contract.Scope)
	}
	if contract.Semantics.Scope != execution.ScopeStateResolved {
		t.Fatalf("objective contract semantics = %+v, want RESOLVED", contract.Semantics)
	}

	// 7. ADMISSION READS THE NEW DERIVED STATE, AND ADMITS.
	spec := d.preflightExecutionSpec(context.Background())
	if spec.Derivation.IsAmbiguous() {
		t.Fatal("the admission spec still carries the stale AMBIGUOUS verdict")
	}
	if got := spec.ExplicitTargets; len(got) != 1 || got[0] != "index.html" {
		t.Fatalf("admission explicit targets = %v, want [index.html]", got)
	}
	if outcome := EvaluatePreflightAdmission(spec); outcome.Blocked() {
		t.Fatalf("a clarified, proven target was refused (verdict=%s reason=%q)", outcome.Verdict, outcome.Reason)
	}

	// 8. THE EXISTING AUTHORIZATION PATH IS UNCHANGED: the mutation is staged,
	//    not applied, and it covers EXACTLY the clarified scope.
	if term != nil && term.State.IsTerminal() {
		t.Fatalf("the clarified run terminated instead of parking at approval (state=%s reason=%q)",
			term.State, term.Reason)
	}
	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval || boundary.PatchID == "" {
		t.Fatalf("clarified evidence did not reach the approval gate: %+v", boundary)
	}
	if len(boundary.Targets) != 1 || boundary.Targets[0] != "index.html" {
		t.Fatalf("the approval gate covers %v, want exactly [index.html]", boundary.Targets)
	}
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("bytes changed before approval: %v", changed)
	}
	assertScopeConfined(t, probe, "index.html")

	// 9. AND THE AUTHORIZED MUTATION LANDS ON THE CLARIFIED FILE ALONE.
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	changed := ambiguityDiff(before, clarificationContentSnapshot(t, root))
	if len(changed) != 1 || changed[0] != "index.html" {
		t.Fatalf("changed files = %v, want exactly [index.html]", changed)
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
}

// TestClarification_BindsEveryNamedTarget proves a clarification may name SEVERAL
// files, and that the invalidation is not a narrowing to whichever single
// candidate happened to be first.
func TestClarification_BindsEveryNamedTarget(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	_, probe, d := clarificationHarness(t, root)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("park: %v", err)
	}
	assertAmbiguousParking(t, d, probe)

	// The same seam ResumeClarify uses, driven with the multi-target statement a
	// human gives when the answer is "these two files".
	targets := d.bindAuthoritativeTargets([]string{"index.html", "styles.css"},
		"a human named two targets at the clarification boundary")

	if len(targets) != 2 || targets[0] != "index.html" || targets[1] != "styles.css" {
		t.Fatalf("authoritative targets = %v, want [index.html styles.css]", targets)
	}
	if d.scopeDerivation.IsAmbiguous() {
		t.Fatal("the pre-clarification AMBIGUOUS verdict survived a two-target clarification")
	}
	if d.scopeResolution.State != ScopeResolved || !d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("scope position = %s (%s), want RESOLVED", d.scopeResolution.State, d.scopeResolution.Reason)
	}
	for _, want := range []string{"index.html", "styles.css"} {
		if !contains(d.scopeResolution.Targets, want) {
			t.Fatalf("clarified target %q is not in the scope: %v", want, d.scopeResolution.Targets)
		}
	}
	for _, old := range []string{"a.html", "b.html", "a.css", "b.css"} {
		if contains(d.scopeResolution.Targets, old) {
			t.Fatalf("unclarified candidate %q entered the scope: %v", old, d.scopeResolution.Targets)
		}
	}
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); outcome.Blocked() {
		t.Fatalf("two clarified targets were refused (verdict=%s reason=%q)", outcome.Verdict, outcome.Reason)
	}
	if calls := probe.Calls(); calls != 0 {
		t.Fatalf("scope derivation billed the provider %d time(s); derivation is a bounded read", calls)
	}
}

// TestClarification_InvalidTargetFailsClosed is TEST 2, and it is the test that
// keeps the fix from being an authorization bypass.
//
// A human may name a file the workspace does not contain. The invalidation above
// is real, so the derivation that follows is a fresh one — and a fresh derivation
// over a target the gateway refuses has to end in the same refusal the first
// preflight would have produced. The runtime must not treat "the human answered"
// as "the answer is proven".
func TestClarification_InvalidTargetFailsClosed(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)
	_, probe, d := clarificationHarness(t, root)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("park: %v", err)
	}
	assertAmbiguousParking(t, d, probe)

	term, err := d.ResumeClarify(context.Background(), "does-not-exist.html")
	if err != nil {
		t.Fatalf("ResumeClarify: %v", err)
	}

	// 1. THE AUTHORITATIVE STATEMENT IS RECORDED VERBATIM. Failing closed must not
	//    mean silently rewriting what the user said.
	if got := d.req.Targets; len(got) != 1 || got[0] != "does-not-exist.html" {
		t.Fatalf("authoritative targets = %v, want [does-not-exist.html]", got)
	}

	// 2. THE GATEWAY REFUSED IT. This is the authority's own verdict on a named
	//    destination, not an inference this package drew.
	if !d.resolved.Ambiguous {
		t.Fatal("the gateway resolved a target the workspace does not contain")
	}

	// 3. NO RESOLVED SCOPE EXISTS.
	if d.scopeResolution.State == ScopeResolved {
		t.Fatalf("a nonexistent target produced a resolved scope: %v", d.scopeResolution.Reason)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("scope position %s claims mutation authority", d.scopeResolution.State)
	}
	if contains(d.scopeResolution.Targets, "does-not-exist.html") {
		t.Fatalf("a refused target was bound: %v", d.scopeResolution.Targets)
	}

	// 4. PREFLIGHT DOES NOT ADMIT, on its own, from the current state.
	outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background()))
	if outcome.Verdict == AdmissionAdmit {
		t.Fatal("a clarified nonexistent target was ADMITTED; invalidation became an authorization bypass")
	}
	if !outcome.Blocked() {
		t.Fatalf("verdict = %s, want a blocking verdict", outcome.Verdict)
	}

	// 5. NO MUTATION, NO CANDIDATE, NO SPEND.
	if calls := probe.Calls(); calls != 0 {
		t.Fatalf("provider was billed %d time(s) over an unresolvable target", calls)
	}
	assertScopeConfined(t, probe, "does-not-exist.html")
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("the runtime mutated %v over an unresolvable target", changed)
	}
	boundary := d.Boundary()
	if boundary != nil && boundary.PatchID != "" {
		t.Fatalf("a mutation candidate was staged for an unresolvable target: %+v", boundary)
	}
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatal("the run reported completion over a target that does not exist")
	}
	if d.objectiveEvaluation().Outcome == execution.ObjectiveProven {
		t.Fatal("the objective was PROVEN over a target that does not exist")
	}
}

// TestClarification_SecondClarificationReplacesTheFirst is TEST 3: stale state
// across resume cycles.
//
// One clarification is easy to reason about; the second is where "the runtime
// remembers the previous scope" shows up. After answering twice the derived scope
// must correspond to the SECOND answer alone, the first answer must not survive as
// the mutation scope, and the candidate staged for the first answer must not be
// approvable while the run is scoped to the second.
func TestClarification_SecondClarificationReplacesTheFirst(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)
	_, probe, d := clarificationHarness(t, root)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("park: %v", err)
	}
	assertAmbiguousParking(t, d, probe)

	// First answer.
	if _, err := d.ResumeClarify(context.Background(), "a.html"); err != nil {
		t.Fatalf("clarify a.html: %v", err)
	}
	if got := d.scopeResolution.Targets; len(got) != 1 || got[0] != "a.html" {
		t.Fatalf("scope after the first clarification = %v, want [a.html]", got)
	}
	firstBoundary := d.Boundary()
	if firstBoundary == nil || firstBoundary.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("the first clarification did not reach the approval gate: %+v", firstBoundary)
	}
	if len(firstBoundary.Targets) != 1 || firstBoundary.Targets[0] != "a.html" {
		t.Fatalf("the first approval gate covers %v, want [a.html]", firstBoundary.Targets)
	}
	if !d.adapter.CandidateHeld(firstBoundary.PatchID) {
		t.Fatalf("the first clarification staged no held candidate (patch=%q)", firstBoundary.PatchID)
	}

	// Second answer, through the same public seam, without a new Run.
	if _, err := d.ResumeClarify(context.Background(), "b.html"); err != nil {
		t.Fatalf("clarify b.html: %v", err)
	}

	// 1. THE SECOND ANSWER IS THE AUTHORITATIVE SCOPE.
	if got := d.req.Targets; len(got) != 1 || got[0] != "b.html" {
		t.Fatalf("authoritative targets = %v, want [b.html]", got)
	}
	if got := d.resolved.Targets; len(got) != 1 || got[0] != "b.html" {
		t.Fatalf("derived targets = %v, want [b.html]", got)
	}
	if got := d.scopeResolution.Targets; len(got) != 1 || got[0] != "b.html" {
		t.Fatalf("scope targets = %v, want [b.html]", got)
	}
	if d.scopeResolution.State != ScopeResolved || !d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("scope position = %s (%s), want RESOLVED", d.scopeResolution.State, d.scopeResolution.Reason)
	}
	if contract := d.ObjectiveContract(); len(contract.Scope) != 1 || contract.Scope[0] != "b.html" {
		t.Fatalf("objective contract scope = %v, want [b.html]", contract.Scope)
	}

	// 2. THE FIRST ANSWER IS GONE EVERYWHERE IT COULD HAVE SURVIVED.
	if contains(d.scopeResolution.Targets, "a.html") || contains(d.resolved.Targets, "a.html") {
		t.Fatalf("the first clarification survived as scope: record=%v resolved=%v",
			d.scopeResolution.Targets, d.resolved.Targets)
	}
	if contract := d.ObjectiveContract(); contains(contract.Scope, "a.html") {
		t.Fatalf("the objective contract still judges %v", contract.Scope)
	}

	// 3. WHAT THE MODEL WAS SHOWN IS THE SECOND SCOPE. The runtime re-derived and
	//    re-dispatched over b.html; a.html was dispatched exactly once, under the
	//    scope that was authoritative at the time.
	seen := probe.Dispatched()
	if len(seen) != 2 || seen[0] != "a.html" || seen[1] != "b.html" {
		t.Fatalf("dispatched targets = %v, want [a.html b.html] — one per clarification", seen)
	}
	if refused := probe.Refused(); len(refused) != 0 {
		t.Fatalf("the runtime dispatched %v, which neither clarification named", refused)
	}
	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("the second clarification did not reach the approval gate: %+v", boundary)
	}
	if len(boundary.Targets) != 1 || boundary.Targets[0] != "b.html" {
		t.Fatalf("the approval gate covers %v, want [b.html]", boundary.Targets)
	}
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("bytes changed before approval: %v", changed)
	}

	// 4. APPROVAL MUTATES THE SECOND TARGET ALONE.
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	changed := ambiguityDiff(before, clarificationContentSnapshot(t, root))
	if len(changed) != 1 || changed[0] != "b.html" {
		t.Fatalf("changed files = %v, want exactly [b.html]", changed)
	}
	if body := readTarget(t, root, "a.html"); !strings.Contains(body, "<h1>Alpha</h1>") {
		t.Fatalf("a.html was mutated by a run scoped to b.html: %q", body)
	}
	if body := readTarget(t, root, "b.html"); !strings.Contains(body, "<h1>Rewritten</h1>") {
		t.Fatalf("b.html does not carry the approved change: %q", body)
	}
}

// TestScopeInvalidation_DropsOnlyDerivedFacts pins WHAT the invalidation removes.
// A fix that clears more than it must is its own defect: the requirement ledger
// and the lifecycle's execution choices are facts about the WORK, not about which
// files it lands on, and dropping them would make a clarification silently start a
// different objective.
func TestScopeInvalidation_DropsOnlyDerivedFacts(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	d := seamDriver(t, root)
	d.prompt = ambiguousObjective
	d.resolved = d.adapter.Resolve(d.prompt)
	d.deriveEvidenceScope()
	if !d.scopeDerivation.IsAmbiguous() {
		t.Fatalf("fixture is not ambiguous (%s)", d.scopeDerivation.StatusOrUnresolved())
	}

	// Per-lifecycle facts a scope change must NOT touch.
	d.objective.derived = true
	d.objective.contract = execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: d.runRequestID,
		Request:     d.prompt,
		Scope:       []string{"stale-target.html"},
	})
	d.objective.proposals = []execution.DerivedRequirement{{ID: "r1", Text: "keep me"}}
	d.objective.discharged = map[string]bool{"r1": true}
	d.mutationStrategy = StrategyFullRewrite
	d.derivationNote = "stale note"
	d.intentAuthority().Resolve(autonomy.IntentModification)

	d.invalidateDerivedScope("test scope change")

	// Derived from the previous resolution: gone.
	if d.scopeDerivation.StatusOrUnresolved() != execution.DerivationUnresolved || len(d.scopeDerivation.Targets) != 0 {
		t.Fatalf("the derivation verdict survived invalidation: %+v", d.scopeDerivation)
	}
	if d.scopeResolution.State != "" || len(d.scopeResolution.Targets) != 0 || len(d.scopeResolution.Candidates) != 0 {
		t.Fatalf("the scope record survived invalidation: %+v", d.scopeResolution)
	}
	if d.derivationNote != "" {
		t.Fatalf("the previous derivation note survived: %q", d.derivationNote)
	}
	if d.objective.derived {
		t.Fatal("the objective contract is still marked derived over the previous scope")
	}
	if len(d.objective.contract.Conditions) != 0 || len(d.objective.contract.Scope) != 0 {
		t.Fatalf("the previous objective contract survived invalidation: %+v", d.objective.contract)
	}
	// The canonical intent is UNCHANGED — a scope change is not an intent revision
	// — but the context bound to the old scope is no longer usable: the authority
	// moves to REVISING, which is what makes a reader fail closed instead of
	// reusing a context compiled over files the run no longer has authority over.
	if rev := d.intentAuthority().LastRevision(); rev.Revision != 0 || rev.To != "" {
		t.Fatalf("a scope change revised the canonical intent: %+v", rev)
	}
	if d.intentAuthority().Phase() != autonomy.IntentRevising {
		t.Fatalf("intent phase = %s; a context compiled over the previous scope must not stand",
			d.intentAuthority().Phase())
	}
	if d.intentAuthority().ContextValid() {
		t.Fatal("a context compiled over the previous scope is still reported valid")
	}

	// Per-lifecycle facts that are NOT about the previous target set: kept.
	if len(d.objective.proposals) != 1 || d.objective.proposals[0].ID != "r1" {
		t.Fatalf("the requirement ledger was dropped by a scope change: %+v", d.objective.proposals)
	}
	if !d.objective.discharged["r1"] {
		t.Fatal("the discharge set was dropped by a scope change")
	}
	if d.mutationStrategy != StrategyFullRewrite {
		t.Fatalf("mutation strategy = %s; a scope change must not rewrite the lifecycle's execution choice",
			d.mutationStrategy)
	}
	if d.prompt != ambiguousObjective {
		t.Fatalf("the objective text was cleared by a scope change: %q", d.prompt)
	}

	// And the lifecycle is still able to re-derive from scratch afterwards.
	if got := d.ObjectiveContract(); len(got.Scope) != 0 {
		t.Fatalf("the re-derived contract still carries the previous scope: %v", got.Scope)
	}
}

// TestClarification_AuthoritativeTargetOutranksWorkspaceAmbiguity is the direct
// statement of the precedence the invalidation depends on: after a clarification
// the derivation is re-run, and what it finds in the workspace can no longer
// outrank the file the human named. The workspace still holds six candidates for
// the declared kinds; the scope is still one file.
func TestClarification_AuthoritativeTargetOutranksWorkspaceAmbiguity(t *testing.T) {
	root := ambiguousPortfolioWorkspace(t)
	before := ambiguitySnapshot(t, root)
	_, _, d := clarificationHarness(t, root)

	if _, err := d.Run(context.Background(), ambiguousObjective); err != nil {
		t.Fatalf("park: %v", err)
	}
	if !d.scopeDerivation.IsAmbiguous() {
		t.Fatalf("fixture is not ambiguous (%s)", d.scopeDerivation.StatusOrUnresolved())
	}

	if _, err := d.ResumeClarify(context.Background(), "styles.css"); err != nil {
		t.Fatalf("ResumeClarify: %v", err)
	}

	// The workspace is unchanged, so a fresh DISCOVERY would still be ambiguous.
	// The scope is not a discovery result any more: the gateway resolved the
	// human's statement, and that is the only authority that may bind a scope.
	discovery := d.adapter.DeriveScope(d.prompt, nil)
	if !discovery.IsAmbiguous() {
		t.Fatalf("the workspace no longer offers several candidates (%v); the fixture stopped testing precedence",
			discovery.Targets)
	}
	if len(d.scopeResolution.Candidates) != 0 {
		t.Fatalf("fresh workspace candidates became the clarified scope: %v", d.scopeResolution.Candidates)
	}
	if got := d.scopeResolution.Targets; len(got) != 1 || got[0] != "styles.css" {
		t.Fatalf("clarified scope = %v, want [styles.css]", got)
	}
	if d.scopeDerivation.IsAmbiguous() {
		t.Fatal("the re-derived verdict is still the workspace's ambiguity")
	}
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); outcome.Blocked() {
		t.Fatalf("the clarified scope was refused (verdict=%s reason=%q)", outcome.Verdict, outcome.Reason)
	}
	// And the mutation is still a human decision: nothing was written to reach
	// any of the above.
	if changed := ambiguityDiff(before, ambiguitySnapshot(t, root)); len(changed) != 0 {
		t.Fatalf("bytes changed before approval: %v", changed)
	}
}
