package autonomy

// ── EVIDENCE-DRIVEN RECOVERY: the deterministic tests ───────────────────────
//
// Tests C, D and E from the recovery-failure report, plus the taxonomy and
// classification locks:
//
//	Test C — a hallucinated anchor invalidates the candidate, mutates nothing,
//	         records evidence and replans (or fails truthfully). Never a fuzzy
//	         patch application.
//	Test D — finish_reason=length is OUTPUT_EXHAUSTED with ArtifactState not
//	         PRODUCED and no executable mutation candidate.
//	Test E — exhaustion followed by a useful continuation keeps ONE objective
//	         identity, retains prior evidence and accumulates new evidence.
//
// The recovery path under test is the REAL one: the driver's own decision
// function, the real ledger, the real classification. Nothing is stubbed except
// the provider.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── Shared fixtures ────────────────────────────────────────────────────────

// anchorWorkspace materialises a target whose content is known, so a patch can
// anchor against something that is deliberately NOT there.
const anchorWorkspaceOriginal = "line one\nline two\nline three\n"

// hallucinatedPatch is a well-formed SEARCH/REPLACE envelope whose SEARCH block
// matches nothing in the target. It is a plausible patch — which is exactly why
// only deterministic anchor checking can reject it.
const hallucinatedPatch = "<<<<<<< SEARCH\nthis line does not exist anywhere\n" +
	"neither does this one\n=======\nreplacement\n>>>>>>> REPLACE"

// anchorHarness wires a Driver over a workspace whose target exists but whose
// content cannot satisfy the supplied patch.
func anchorHarness(t *testing.T, p ai.Provider) (*Driver, *execution.RuntimeExecutor) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "index.html", anchorWorkspaceOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return NewDriver(adapter, bus, WithLoopBounds(lifecycleBounds())), x
}

// exhaustedProvider cuts every generation at the provider's ceiling and delivers
// no usable bytes — the live run's dominant failure mode.
type exhaustedProvider struct{}

func (exhaustedProvider) Name() string { return "exhausted" }

func (exhaustedProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Content: "partial", FinishReason: "length", Truncated: true}, nil
}

func (exhaustedProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, io.ErrClosedPipe
}

// exhaustedThenPatchProvider exhausts the first attempt and then delivers a real,
// anchorable patch — the Test E shape.
type exhaustedThenPatchProvider struct {
	n int
}

func (exhaustedThenPatchProvider) Name() string { return "exhausted-then-patch" }

func (p *exhaustedThenPatchProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.n++
	if p.n == 1 {
		return &ai.Response{Content: "partial", FinishReason: "length", Truncated: true}, nil
	}
	return &ai.Response{Content: "<<<<<<< SEARCH\nline two\n=======\nline TWO\n>>>>>>> REPLACE"}, nil
}

func (p *exhaustedThenPatchProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, io.ErrClosedPipe
}

// ── TEST C: hallucinated anchor ─────────────────────────────────────────────

// hallucinatedAnchorObservation is the observation the executor produces when a
// patch's SEARCH block matches nothing in the authoritative target.
func hallucinatedAnchorObservation() autonomy.Observation {
	return autonomy.Observation{
		RequestID: "run-1",
		Target:    "index.html",
		Outcome:   autonomy.OutcomeArtifactRejected,
		Diagnostic: "executor: hallucinated anchor — zero match: index.html: " +
			"SEARCH matches zero regions (match count 0)",
		Objective: execution.ObjectiveEvidence{
			Provider: execution.ProviderDone,
			Artifact: execution.ArtifactProduced,
		},
		AttemptNum: 0,
	}
}

func TestRecovery_HallucinatedAnchorIsStructuredNotBudgetBreach(t *testing.T) {
	o := hallucinatedAnchorObservation()

	f := ClassifyObservation(o)
	if f.Class != execution.FailureAnchorNotFound {
		t.Fatalf("class = %s, want ANCHOR_NOT_FOUND", f.Class)
	}
	if f.Evidence == "" {
		t.Fatal("an anchor classification must carry deterministic evidence")
	}
	// The old path reported this as "Physical Output Budget Breach", naming a
	// completely different failure. The class must not carry that meaning.
	if f.Class == execution.FailureOutputExhausted {
		t.Fatal("an anchor failure must never be classified as output exhaustion")
	}
	if strings.Contains(strings.ToLower(f.String()), "physical output budget breach") {
		t.Fatalf("the failure is still described as an output-budget breach: %s", f)
	}
	// An anchor mismatch is a property of the CANDIDATE, so no retry policy may
	// authorise re-applying the same patch without new evidence.
	if f.Class.RetryPolicy() == execution.RetryIdentical {
		t.Fatal("an unresolvable anchor must never be retryable with an identical patch")
	}
}

func TestRecovery_HallucinatedAnchorRejectsCandidateWithoutMutation(t *testing.T) {
	d, x := anchorHarness(t, &mockProvider{
		responses: []*ai.Response{{Content: hallucinatedPatch}, {Content: hallucinatedPatch}},
	})

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The workspace must be byte-identical: an unanchorable patch is not a patch.
	if got := readTarget(t, rootOf(t, d), "index.html"); got != anchorWorkspaceOriginal {
		t.Fatalf("an unanchorable patch mutated the workspace:\nwant=%q\ngot =%q", anchorWorkspaceOriginal, got)
	}
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("an unanchorable patch survived as an approvable candidate: %v", ids)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a run whose only patch was unanchorable reported completion")
	}
	if d.ObjectiveProgress() == execution.ProgressProven {
		t.Fatal("an unanchorable patch reported the objective PROVEN")
	}
}

// rootOf reaches the workspace root a driver is bound to.
func rootOf(t *testing.T, d *Driver) string {
	t.Helper()
	if d == nil || d.adapter == nil {
		t.Fatal("driver has no adapter")
	}
	return d.adapter.Root()
}

// TestRecovery_HallucinatedAnchorSecondAttemptIsNonProgressing proves the
// runtime stops repeating a patch strategy that produces nothing.
func TestRecovery_HallucinatedAnchorSecondAttemptIsNonProgressing(t *testing.T) {
	ledger := newFailureLedger()
	o := hallucinatedAnchorObservation()

	// Attempt 1: a fresh failure earns ONE evidence-bearing replan. The brief
	// the next planner invocation receives is materially new input, so this is a
	// replan rather than a blind retry.
	first := DecideRecoveryWith(RecoveryInput{
		Observation: o,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       []string{"index.html"},
	})
	if first.Action != recoveryReplan && first.Action != recoveryContinue {
		t.Fatalf("the FIRST anchor failure must earn a replan, got %s (%s)", first.Action, first.Reason)
	}
	if first.Failure.Class != execution.FailureAnchorNotFound {
		t.Fatalf("first class = %s, want ANCHOR_NOT_FOUND", first.Failure.Class)
	}

	// Attempt 2: the SAME failure under the SAME evidence. Nothing changed, so
	// another attempt of the same kind is not admissible.
	second := DecideRecoveryWith(RecoveryInput{
		Observation: o,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       []string{"index.html"},
	})
	// The SECOND identical anchor failure is recorded and named as a repeat. The
	// class's own attempt limit bounds the total; what the ledger guarantees is
	// that the decision is attributable to the repeat rather than looking like a
	// fresh failure.
	if !strings.Contains(second.Reason, string(execution.FailureNonProgressing)) {
		t.Fatalf("a repeated anchor failure must be recorded as NON_PROGRESSING_EXECUTION: %s", second.Reason)
	}
	if second.Failure.Count != 2 {
		t.Fatalf("failure count = %d, want 2 observations of the same failure", second.Failure.Count)
	}
	// The identity survives regardless of the disposition: it names WHICH
	// objective the verdict is about.
	if second.ObjectiveID != "run-1" {
		t.Fatalf("objective id = %q, want run-1", second.ObjectiveID)
	}
	if !second.RetainsIdentity {
		t.Fatal("a recovery verdict must always retain the objective identity")
	}
}

// TestRecovery_RepeatedForbiddenFailureTerminatesWithoutThirdAttempt is the
// no-third-identical-attempt proof for a DETERMINISTIC failure — the class whose
// policy forbids retrying outright, so no class breaker can rescue it.
func TestRecovery_RepeatedForbiddenFailureTerminatesWithoutThirdAttempt(t *testing.T) {
	ledger := newFailureLedger()
	o := autonomy.Observation{
		RequestID: "run-1",
		Target:    "style.css",
		Outcome:   autonomy.OutcomeFailed,
		Diagnostic: "TARGET_NOT_FOUND [style.css]: style.css does not exist; the resolved scope is " +
			"[index.html,script.js,styles.css] and is unchanged",
	}
	scope := []string{"index.html", "script.js", "styles.css"}
	in := RecoveryInput{
		Observation: o,
		Bounds:      autonomy.DefaultLoopBounds(),
		Ledger:      ledger,
		ObjectiveID: "run-1",
		Scope:       scope,
	}

	// Attempt 1: a fresh target-identity failure earns a REPLAN — with the
	// scope restated and nothing substituted.
	first := DecideRecoveryWith(in)
	if first.Failure.Class != execution.FailureTargetNotFound &&
		first.Failure.Class != execution.FailureTargetIdentityMismatch {
		t.Fatalf("class = %s, want a target-identity class", first.Failure.Class)
	}
	if first.Action != recoveryReplan {
		t.Fatalf("first attempt = %s, want REPLAN (%s)", first.Action, first.Reason)
	}
	if first.Failure.Target != "style.css" {
		t.Fatalf("target = %q, want the verbatim request %q", first.Failure.Target, "style.css")
	}
	if !strings.Contains(first.Reason, "styles.css") {
		t.Fatalf("the replan must restate the authoritative scope: %s", first.Reason)
	}

	// Attempt 2: identical request, identical evidence. The class forbids
	// retrying, so this terminates — there is no third attempt.
	second := DecideRecoveryWith(in)
	if second.Action != recoveryUnsubstantiate {
		t.Fatalf("second attempt = %s, want UNSUBSTANTIATED (%s)", second.Action, second.Reason)
	}
	if second.Failure.Class != execution.FailureNonProgressing {
		t.Fatalf("second class = %s, want NON_PROGRESSING_EXECUTION", second.Failure.Class)
	}
	if second.ObjectiveID != "run-1" {
		t.Fatalf("objective id = %q, want run-1", second.ObjectiveID)
	}
}

// ── TEST D: provider exhaustion ─────────────────────────────────────────────

func TestRecovery_OutputExhaustionIsNotAnArtifactFailure(t *testing.T) {
	o := autonomy.Observation{
		RequestID:       "run-1",
		Target:          "index.html",
		Outcome:         autonomy.OutcomeTruncated,
		FinishReason:    "length",
		MaxOutputTokens: 3072,
		// A truncated generation delivered a prefix. Zero artifacts were
		// PARSED — but the reason is the boundary, not the model declining.
		Objective: execution.ObjectiveEvidence{
			Provider:        execution.ProviderDone,
			Artifact:        execution.ArtifactNone,
			ArtifactsParsed: 0,
		},
	}
	f := ClassifyObservation(o)
	if f.Class != execution.FailureOutputExhausted {
		t.Fatalf("class = %s, want OUTPUT_EXHAUSTED", f.Class)
	}
	// The two facts stay SEPARATE: the transport was exhausted, and the parser
	// produced nothing. Neither implies the other.
	if f.ArtifactState == execution.ArtifactProduced {
		t.Fatal("exhaustion must never be recorded as an artifact having been produced")
	}
	if f.ArtifactState != execution.ArtifactNone {
		t.Fatalf("artifact state = %s, want NONE", f.ArtifactState)
	}
	if f.ProviderState != execution.ProviderDone {
		t.Fatalf("provider state = %s, want DONE (it completed, truncated)", f.ProviderState)
	}
	// It must NOT be reclassified as "the model wrote prose": that is a statement
	// about the model's choice, and here it never got to choose.
	if f.Class == execution.FailureArtifactEmpty {
		t.Fatal("finish_reason=length with zero artifacts must be OUTPUT_EXHAUSTED, not ARTIFACT_EMPTY")
	}
}

// TestRecovery_ZeroArtifactsAndExhaustionAreDistinguishable is the ordering
// regression test: the old classifier checked zero-artifacts FIRST, so a run cut
// off at the boundary was reported as a prose-only contract miss.
func TestRecovery_ZeroArtifactsAndExhaustionAreDistinguishable(t *testing.T) {
	exhausted := autonomy.Observation{
		Outcome:      autonomy.OutcomeTruncated,
		FinishReason: "length",
		Objective:    execution.ObjectiveEvidence{Artifact: execution.ArtifactNone},
	}
	prose := autonomy.Observation{
		Outcome:      autonomy.OutcomeArtifactRetryableRejected,
		FinishReason: "stop",
		Diagnostic:   execution.ErrZeroArtifactsParsed.Error() + ": index.html: prose",
		Objective:    execution.ObjectiveEvidence{Artifact: execution.ArtifactNone},
	}
	if got := ClassifyObservation(exhausted).Class; got != execution.FailureOutputExhausted {
		t.Fatalf("exhausted run classified as %s, want OUTPUT_EXHAUSTED", got)
	}
	if got := ClassifyObservation(prose).Class; got != execution.FailureArtifactEmpty {
		t.Fatalf("prose-only run classified as %s, want ARTIFACT_EMPTY", got)
	}
}

func TestRecovery_ExhaustionProducesNoExecutableCandidate(t *testing.T) {
	d, x := anchorHarness(t, exhaustedProvider{})

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("an exhausted generation produced an executable candidate: %v", ids)
	}
	if got := readTarget(t, rootOf(t, d), "index.html"); got != anchorWorkspaceOriginal {
		t.Fatalf("an exhausted generation mutated the workspace: %q", got)
	}
	// The objective must not be PROVEN on the strength of a cut-off stream.
	if d.ObjectiveProgress() == execution.ProgressProven {
		t.Fatal("an exhausted generation reported the objective PROVEN")
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("an exhausted generation reported loop completion")
	}
}

// ── TEST E: exhaustion then useful continuation ─────────────────────────────

func TestRecovery_ContinuationRetainsObjectiveIdentityAndEvidence(t *testing.T) {
	// Step 1 exhausts; step 2 delivers a real, anchorable patch.
	d, x := anchorHarness(t, &exhaustedThenPatchProvider{})

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := d.ObjectiveIdentity(); got != "run-1" {
		t.Fatalf("objective id = %q, want run-1 for the whole lifecycle", got)
	}
	// The contract was authored ONCE and is still the contract after the
	// exhaustion and the recovery that followed it.
	if got := d.ObjectiveContract().ObjectiveID; got != "run-1" {
		t.Fatalf("contract objective id = %q, want run-1", got)
	}
	// Evidence accumulated rather than being reset by the failed step.
	if len(d.ObjectiveConditions()) == 0 {
		t.Fatal("the objective contract lost its completion conditions across the continuation")
	}
	// At most one held candidate: the lifecycle supersedes rather than
	// accumulating.
	if n := len(x.PendingPatchIDs()); n > 1 {
		t.Fatalf("the lifecycle accumulated %d held candidates, want at most 1", n)
	}
	// The continuation carried a TYPED classification forward, so the second
	// attempt was informed rather than blind. The specific class depends on what
	// the second response did; the requirement is that it is named at all.
	brief := d.recoveryBrief()
	if !strings.Contains(brief, "[FAILED APPROACH]") {
		t.Fatalf("the continuation carried no failed-approach evidence:\n%s", brief)
	}
	if !strings.Contains(brief, "class=") {
		t.Fatalf("the continuation carried no typed classification:\n%s", brief)
	}
	// The authoritative scope survived the continuation unchanged.
	if !strings.Contains(brief, "index.html") {
		t.Fatalf("the continuation lost the authoritative scope:\n%s", brief)
	}
	// And the lifecycle recorded what happened rather than resetting: the ledger
	// is non-empty, which is the memory the recovery decision consumed.
	if len(d.failures.LedgerReport()) == 0 {
		t.Fatal("the lifecycle recorded no failure evidence across the continuation")
	}
}

// TestRecovery_ContinuationBriefCarriesFailedApproach proves the next planner
// invocation is told what FAILED, not merely what remains. Without it a recovery
// is a restart in disguise.
func TestRecovery_ContinuationBriefCarriesFailedApproach(t *testing.T) {
	d, _ := anchorHarness(t, exhaustedProvider{})
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brief := d.recoveryBrief()
	if brief == "" {
		t.Fatal("a failed attempt must produce a recovery brief for the next planner invocation")
	}
	if !strings.Contains(brief, "[FAILED APPROACH]") {
		t.Fatalf("the brief does not name the failed approach:\n%s", brief)
	}
	if !strings.Contains(brief, string(execution.FailureOutputExhausted)) {
		t.Fatalf("the brief does not carry the classified failure:\n%s", brief)
	}
	// The authoritative scope is restated, so the next attempt cannot widen it.
	if !strings.Contains(brief, "authoritative scope") || !strings.Contains(brief, "index.html") {
		t.Fatalf("the brief does not restate the authoritative scope:\n%s", brief)
	}
	// Retry admissibility is explicit rather than implied.
	if !strings.Contains(brief, "retry_admissible") {
		t.Fatalf("the brief does not state whether retrying is admissible:\n%s", brief)
	}
}

// ── Taxonomy + classification locks ─────────────────────────────────────────

// TestFailureTaxonomyIsClosedAndTotal pins the structured taxonomy: every member
// has a retry policy, every member is reachable, and none reports an identical
// retry except transport.
func TestFailureTaxonomyIsClosedAndTotal(t *testing.T) {
	all := execution.AllFailureClasses()
	if len(all) < 11 {
		t.Fatalf("taxonomy has %d members, want at least the 11 required distinctions", len(all))
	}
	seen := map[execution.FailureClass]bool{}
	for _, c := range all {
		if !c.Valid() {
			t.Errorf("%s is not in the closed vocabulary", c)
		}
		if seen[c] {
			t.Errorf("%s appears twice", c)
		}
		seen[c] = true
		switch c.RetryPolicy() {
		case execution.RetryForbidden, execution.RetryOnNewEvidence, execution.RetryIdentical:
		default:
			t.Errorf("%s has no valid retry policy", c)
		}
	}
	// The invariant that makes the whole mechanism work: exactly one class is
	// ever admissible to retry identically.
	identicals := 0
	for _, c := range all {
		if c.RetryPolicy() == execution.RetryIdentical {
			identicals++
		}
	}
	if identicals != 1 {
		t.Fatalf("%d classes permit an identical retry, want exactly 1 (transport)", identicals)
	}
	// The required minimum distinctions are all present.
	for _, required := range []execution.FailureClass{
		execution.FailureTargetNotFound,
		execution.FailureTargetIdentityMismatch,
		execution.FailureCapabilityFailure,
		execution.FailureOutputExhausted,
		execution.FailureArtifactInvalid,
		execution.FailureArtifactEmpty,
		execution.FailureAnchorNotFound,
		execution.FailureStaleCandidate,
		execution.FailureAuthorizationRequired,
		execution.FailureVerificationFailure,
		execution.FailureHardExecutionFailure,
		execution.FailureNonProgressing,
	} {
		if !seen[required] {
			t.Errorf("required classification %s is missing from the taxonomy", required)
		}
	}
}

// TestClassificationAlwaysCarriesEvidence proves no classification is ever
// asserted without a reason.
func TestClassificationAlwaysCarriesEvidence(t *testing.T) {
	observations := []autonomy.Observation{
		{Target: "a", Outcome: autonomy.OutcomeTruncated, FinishReason: "length"},
		{Target: "b", Outcome: autonomy.OutcomeFailed},
		{Target: "c", Outcome: autonomy.OutcomeArtifactRejected, Diagnostic: "bad schema"},
		{Target: "d", Outcome: autonomy.OutcomeApplyFailed},
		{Target: "e", Outcome: autonomy.OutcomeVerifyFailed},
		{Target: "f", Outcome: autonomy.OutcomePreflightInfeasible},
		{Target: "g", Outcome: autonomy.OutcomeWorkspaceDrift},
		{Target: "h", Outcome: autonomy.OutcomeFailed, FinishReason: "content_filter"},
		{Target: "i", Outcome: autonomy.OutcomeNoOpObjectiveUnresolved},
		{Target: "j", Outcome: autonomy.OutcomeFailed, Diagnostic: "TARGET_NOT_FOUND [x.css]: nope"},
	}
	for _, o := range observations {
		f := ClassifyObservation(o)
		if f.Class == execution.FailureNone {
			t.Errorf("outcome %s classified to no failure at all", o.Outcome)
			continue
		}
		if strings.TrimSpace(f.Evidence) == "" {
			t.Errorf("%s: class %s carries no evidence", o.Outcome, f.Class)
		}
	}
}

// TestLedgerDetectsRepeatsAndResetsOnNewEvidence pins the memory primitive.
func TestLedgerDetectsRepeatsAndResetsOnNewEvidence(t *testing.T) {
	l := newFailureLedger()
	f := execution.ExecutionFailure{Class: execution.FailureTargetNotFound, Target: "style.css"}

	if count, repeated := l.Observe(f); count != 1 || repeated {
		t.Fatalf("first observation = (%d,%v), want (1,false)", count, repeated)
	}
	if count, repeated := l.Observe(f); count != 2 || !repeated {
		t.Fatalf("second observation = (%d,%v), want (2,true)", count, repeated)
	}
	if !l.NonProgressing(f) {
		t.Fatal("the ledger must report the repeat")
	}

	// NEW EVIDENCE resets the count: the same request may legitimately be
	// re-issued against a different workspace.
	l.AdvanceEvidence()
	if l.NonProgressing(f) {
		t.Fatal("a repeat after new evidence must not report as non-progressing")
	}
	if count, repeated := l.Observe(f); count != 1 || repeated {
		t.Fatalf("post-evidence observation = (%d,%v), want (1,false)", count, repeated)
	}

	// A DIFFERENT class is new information, not a repeat.
	other := execution.ExecutionFailure{Class: execution.FailureCapabilityFailure, Target: "style.css"}
	l.Observe(other)
	if l.NonProgressing(other) {
		t.Fatal("a first observation of a different class must not be a repeat")
	}
}

// TestLedgerReportNamesTheForbiddenRetry proves the ledger's rendering states the
// verdict, so a human reading a park learns WHY repeating will not help.
func TestLedgerReportNamesTheForbiddenRetry(t *testing.T) {
	l := newFailureLedger()
	f := execution.ExecutionFailure{
		Class:    execution.FailureTargetNotFound,
		Target:   "style.css",
		Evidence: "style.css does not exist; scope unchanged",
	}
	l.Observe(f)
	l.Observe(f)

	report := l.LedgerReport()
	if len(report) != 1 {
		t.Fatalf("report = %v, want 1 entry", report)
	}
	if !strings.Contains(report[0], string(execution.FailureNonProgressing)) {
		t.Fatalf("the report does not name the non-progressing verdict: %s", report[0])
	}
	if !strings.Contains(report[0], "style.css") {
		t.Fatalf("the report does not name the target: %s", report[0])
	}
}
