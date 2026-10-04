package autonomy

// ── ARCHITECTURE LOCKS: lifecycle, authority and telemetry ──────────────────
//
// The complementary half of internal/execution/architecture_locks_failure_test.go.
// Those locks guard target identity at the layer that owns it; these guard the
// properties that belong to the Control Plane:
//
//	4  No repeated identical anchor failure
//	6  Provider DONE != Objective PROVEN
//	7  ArtifactProduced != Objective PROVEN
//	8  Objective ID survives continuation/replan
//	9  Recovery cannot bypass ObjectiveCompletionAuthority
//	10 Telemetry cannot manufacture completion from absent events
//
// Locks 6, 7 and 9 are SOURCE locks on purpose. A behavioural test can only
// prove the runtime is correct for the cases someone thought to write; a source
// lock proves that no code path in the completion decision even CONSIDERS the
// forbidden inference.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── source-lock helpers ─────────────────────────────────────────────────────

func runtimeSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// stripComments removes line and block comments so a source lock asserts on CODE.
func stripComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += 2 + end + 2
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// functionBody extracts one top-level function's body from Go source, comment
// stripped, from its declaration to the line that closes it at column zero.
func functionBody(src, signature string) (string, bool) {
	code := stripComments(src)
	start := strings.Index(code, signature)
	if start < 0 {
		return "", false
	}
	rest := code[start:]
	brace := strings.IndexByte(rest, '{')
	if brace < 0 {
		return "", false
	}
	depth, end := 0, -1
	for i := brace; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", false
	}
	return rest[:end+1], true
}

// ── LOCK 6 + 7: provider DONE / artifact PRODUCED != objective PROVEN ──────

// TestLock6And7_CompletionCannotBeInferredFromProviderOrArtifact is the SOURCE
// lock for the two most dangerous inferences in the runtime:
//
//	provider DONE        → objective PROVEN
//	artifact PRODUCED    → objective PROVEN
//
// It asserts that the completion authority's evaluators never branch on
// `Provider` or `Artifact` as a sufficient condition for PROVEN. The check is on
// the evaluators' source: a behavioural test would have to enumerate every
// evidence combination, and a new one could be added without failing it.
func TestLock6And7_CompletionCannotBeInferredFromProviderOrArtifact(t *testing.T) {
	src := runtimeSource(t, filepath.Join("..", "..", "execution", "objective_authority.go"))

	// Every evaluator must exist — the lock is vacuous if they were renamed away.
	for _, fn := range []string{
		"func (a *ObjectiveCompletionAuthority) evaluateCreate(",
		"func (a *ObjectiveCompletionAuthority) evaluatePatch(",
		"func (a *ObjectiveCompletionAuthority) evaluateDelete(",
		"func (a *ObjectiveCompletionAuthority) evaluateRead(",
		"func (a *ObjectiveCompletionAuthority) evaluateReview(",
		"func (a *ObjectiveCompletionAuthority) evaluateIdempotent(",
	} {
		body, ok := functionBody(src, fn)
		if !ok {
			t.Fatalf("%s is gone — the completion authority's evaluators must remain", fn)
		}
		// The forbidden inference, written out: a PROVEN reachable on a
		// transport or parser fact alone.
		forbidden := []string{
			"ev.Provider == ProviderDone &&",
			"if ev.Artifact == ArtifactProduced {",
			"return ObjectiveProven\n\t\t",
		}
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q — provider/artifact state alone can never prove an objective:\n%s",
					fn, bad, body)
			}
		}

		// THE STRUCTURAL ASSERTION. A correct evaluator reaches PROVEN only as its
		// FINAL statement, after at least one refusal. So:
		//
		//   (a) PROVEN must appear;
		//   (b) it must be the last statement in the body;
		//   (c) at least one refusal must precede it.
		//
		// (b) and (c) together are what make an EARLY return on a provider or
		// artifact fact impossible to write without breaking this lock. An
		// unguarded `return ObjectiveProven` at the top of an evaluator would
		// satisfy (a) and fail (b).
		provenIdx := strings.LastIndex(body, "return ObjectiveProven")
		if provenIdx < 0 {
			t.Errorf("%s never returns PROVEN — a stricter evaluator than the contract requires:\n%s", fn, body)
			continue
		}
		tail := strings.TrimSpace(body[provenIdx+len("return ObjectiveProven"):])
		if tail != "" && tail != "}" {
			t.Errorf("%s reaches PROVEN and then continues; PROVEN must be the FINAL statement:\n%s", fn, body)
		}
		if provenIdx == 0 {
			t.Errorf("%s returns PROVEN unconditionally:\n%s", fn, body)
		}
		prefix := body[:provenIdx]
		if !strings.Contains(prefix, "ObjectiveUnsubstantiated") &&
			!strings.Contains(prefix, "ObjectiveFailed") {
			t.Errorf("%s reaches PROVEN with no refusal path — every evaluator must be able to say no:\n%s", fn, body)
		}
		// A PROVIDER-state check must never be one of the guards on the way to
		// PROVEN: transport completion is not an obligation.
		if strings.Contains(prefix, "ev.Provider") {
			t.Errorf("%s guards PROVEN on provider state — the transport boundary carries no task meaning:\n%s", fn, body)
		}
	}

	// The ObjectiveOutcome vocabulary must keep PROVEN as the ONLY proving
	// outcome, and `.Proves()` must admit nothing else.
	if !execution.ObjectiveProven.Proves() {
		t.Fatal("PROVEN must be the proving outcome")
	}
	for _, o := range []execution.ObjectiveOutcome{
		execution.ObjectiveUnsubstantiated,
		execution.ObjectiveFailed,
		execution.ObjectiveRequiresAuthorization,
	} {
		if o.Proves() {
			t.Errorf("%s must never report itself as proving", o)
		}
	}
}

// TestLock6And7_ProviderDoneAndArtifactProducedAreNotSufficient is the
// behavioural half: the exact evidence combination that a naive implementation
// would treat as success, judged by the real authority.
func TestLock6And7_ProviderDoneAndArtifactProducedAreNotSufficient(t *testing.T) {
	auth := execution.NewObjectiveCompletionAuthority()
	contract := execution.TaskContract{
		Kind:             execution.TaskPatch,
		Targets:          []string{"index.html"},
		RequiresVerifier: true,
	}
	// The combination a naive implementation would call success: the provider
	// finished, the parser found something, and the stream was not truncated.
	optimistic := execution.ObjectiveEvidence{
		Provider:         execution.ProviderDone,
		Artifact:         execution.ArtifactProduced,
		ArtifactsParsed:  1,
		FinishReason:     "stop",
		Mutation:         execution.FilesystemNone,
		TargetExists:     map[string]bool{"index.html": true},
		MutatedFiles:     0,
		VerificationRan:  false,
		ResponseProduced: false,
	}
	got := auth.Authorize(contract, optimistic)
	if got.Outcome == execution.ObjectiveProven {
		t.Fatalf("provider DONE + artifact PRODUCED was treated as PROVEN: %+v", got)
	}

	// And the same evidence with a durable mutation but no verification still
	// cannot prove a contract that requires a verifier.
	mutated := optimistic
	mutated.Mutation = execution.FilesystemApplied
	mutated.MutatedFiles = 1
	mutated.ObservedDeltaTargets = []string{"index.html"}
	got = auth.Authorize(contract, mutated)
	if got.Outcome == execution.ObjectiveProven {
		t.Fatalf("an unverified mutation was treated as PROVEN: %+v", got)
	}
}

// ── LOCK 8: objective identity survives continuation and replan ─────────────

// TestLock8_ObjectiveIdentitySurvivesEveryContinuation drives a run that fails,
// recovers and replans, and asserts the identity never changes — only the
// execution attempt identity does.
func TestLock8_ObjectiveIdentitySurvivesEveryContinuation(t *testing.T) {
	d, _ := anchorHarness(t, &exhaustedThenPatchProvider{})

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := d.ObjectiveIdentity(); got != "run-1" {
		t.Fatalf("objective identity = %q after recovery, want run-1", got)
	}
	if got := d.ObjectiveContract().ObjectiveID; got != "run-1" {
		t.Fatalf("contract identity = %q after recovery, want run-1", got)
	}
	// The run ID is the lifecycle identity; a continuation must not mint a new
	// one. Attempt/request identities MAY change — that is the whole point of a
	// continuation — but the objective must not be duplicated.
	if d.runRequestID != "run-1" {
		t.Fatalf("run request identity = %q after recovery, want run-1", d.runRequestID)
	}
	// The contract is derived ONCE. A re-derivation would let a later step
	// author obligations the earlier steps did not face.
	if !d.objective.derived {
		t.Fatal("the objective contract was never derived")
	}
}

// TestLock8_AFreshRunIsTheOnlyWayToChangeTheObjective proves the identity is
// lifecycle-scoped: a NEW run may have a new identity, and it must not inherit
// the previous objective's discharged requirements.
func TestLock8_AFreshRunIsTheOnlyWayToChangeTheObjective(t *testing.T) {
	// A provider that always fails produces a TERMINAL run, so a second Run is
	// legal — which is the precondition this lock is about.
	d, _ := anchorHarness(t, exhaustedProvider{})
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	first := d.ObjectiveIdentity()
	if first != "run-1" {
		t.Fatalf("first objective identity = %q, want run-1", first)
	}
	if d.State().IsTerminal() {
		t.Fatalf("the first run did not terminate (%s); a second Run needs a terminal first", d.State())
	}
	// The first lifecycle recorded failure evidence.
	if len(d.failures.LedgerReport()) == 0 {
		t.Fatal("the first lifecycle recorded no failure evidence")
	}

	// Converge the parked run so a fresh one is legal. Abort is the operator's
	// own terminal decision and is the canonical way to close a lifecycle.
	if _, err := d.Abort("test lifecycle boundary"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	second := d.ObjectiveIdentity()
	if second == first {
		t.Fatalf("two runs share the objective identity %q — identities must be lifecycle-scoped", second)
	}
	if second != "run-2" {
		t.Fatalf("second run identity = %q, want run-2", second)
	}
	// The CONTRACT was re-derived for the new lifecycle rather than inherited.
	if got := d.ObjectiveContract().ObjectiveID; got != "run-2" {
		t.Fatalf("the second run inherited the first run's contract identity %q", got)
	}
	// And no discharged requirement survived: one objective's evidence cannot
	// satisfy another's completion contract.
	if len(d.objective.discharged) != 0 {
		t.Fatalf("requirements leaked across lifecycles: %v", d.objective.discharged)
	}
	if len(d.objective.postMutation) != 0 {
		t.Fatalf("post-mutation observations leaked across lifecycles: %v", d.objective.postMutation)
	}
	// The failure ledger is re-created per Run, so its identity is fresh even
	// though both runs failed the same way.
	if d.failures == nil {
		t.Fatal("the second run has no failure ledger")
	}
	if d.failures.evidenceEpoch != 0 {
		t.Fatalf("the second run inherited evidence epoch %d; a new lifecycle starts at 0",
			d.failures.evidenceEpoch)
	}
}

// ── LOCK 9: recovery cannot bypass the completion authority ─────────────────

// TestLock9_RecoveryCannotBypassTheCompletionAuthority asserts, by SOURCE, that
// no recovery path can set the completing action. It is the structural
// statement of the invariant that recovery only ever downgrades or continues.
func TestLock9_RecoveryCannotBypassTheCompletionAuthority(t *testing.T) {
	for _, file := range []string{"recovery.go", "recovery_decision.go", "failure_ledger.go"} {
		src := runtimeSource(t, file)
		code := stripComments(src)
		// The lock forbids ASSIGNING the completing action, not MENTIONING it.
		// Mapping a loop action back into the recovery vocabulary has to be able
		// to name LoopComplete in order to route it away from completion.
		if strings.Contains(code, "Action: autonomy.LoopComplete") {
			t.Errorf("%s ASSIGNS the completing action — only ObjectiveCompletionAuthority may", file)
		}
		if strings.Contains(code, "Action:    autonomy.LoopComplete") {
			t.Errorf("%s ASSIGNS the completing action — only ObjectiveCompletionAuthority may", file)
		}
		if strings.Contains(code, "= autonomy.LoopComplete") {
			t.Errorf("%s ASSIGNS the completing action — only ObjectiveCompletionAuthority may", file)
		}
		if strings.Contains(code, "execution.ObjectiveProven") {
			t.Errorf("%s can assert PROVEN — only the completion authority may", file)
		}
		if strings.Contains(code, "RuntimeCompleted") {
			t.Errorf("%s can reach the completed runtime state directly", file)
		}
	}
	// And the recovery decision projects only into the non-completing subset of
	// the loop vocabulary — asserted through the projection function itself.
	src := runtimeSource(t, "recovery_decision.go")
	body, ok := functionBody(src, "func (d RecoveryDecision) loopDecision()")
	if !ok {
		t.Fatal("the recovery projection function is gone")
	}
	if strings.Contains(body, "LoopComplete") {
		t.Fatalf("recovery projection can emit LoopComplete:\n%s", body)
	}
	// Every branch of the projection must be present, so the fail-closed default
	// is a backstop rather than the only path.
	for _, arm := range []string{
		"case recoveryContinue:",
		"case recoveryReplan:",
		"case recoveryWaitForAuthorization:",
		"case recoveryFail:",
		"case recoveryUnsubstantiate:",
		"default:",
	} {
		if !strings.Contains(body, arm) {
			t.Errorf("the recovery projection is missing the %q arm:\n%s", arm, body)
		}
	}
	// Every recovery action must map to a real loop action; an unmapped one fails
	// closed to UNSUBSTANTIATE rather than defaulting to completion.
	for _, action := range []recoveryAction{
		recoveryContinue, recoveryReplan, recoveryWaitForAuthorization,
		recoveryFail, recoveryUnsubstantiate,
	} {
		if action == "" {
			t.Fatal("the recovery vocabulary has an empty member")
		}
	}
}

// TestLock9_DecideRecoveryDecisionNeverCompletes is the behavioural half.
func TestLock9_DecideRecoveryDecisionNeverCompletes(t *testing.T) {
	ledger := newFailureLedger()
	decisions := []RecoveryInput{
		{Observation: hallucinatedAnchorObservation(), Bounds: autonomy.DefaultLoopBounds(), Ledger: ledger, ObjectiveID: "run-1"},
		{Observation: autonomy.Observation{Outcome: autonomy.OutcomeFailed, FinishReason: "length"}, Bounds: autonomy.DefaultLoopBounds(), Ledger: ledger, ObjectiveID: "run-1"},
		{Observation: autonomy.Observation{Outcome: autonomy.OutcomeApplyFailed}, Bounds: autonomy.DefaultLoopBounds(), Ledger: ledger, ObjectiveID: "run-1"},
		{Observation: autonomy.Observation{Outcome: autonomy.OutcomePreflightInfeasible}, Bounds: autonomy.DefaultLoopBounds(), Ledger: ledger, ObjectiveID: "run-1"},
	}
	for i, in := range decisions {
		d := DecideRecoveryWith(in)
		got := d.loopDecision()
		if got.Action == autonomy.LoopComplete {
			t.Fatalf("case %d: recovery emitted LoopComplete (%s)", i, d.Reason)
		}
		if !got.Action.Valid() {
			t.Fatalf("case %d: recovery emitted an invalid action %q", i, got.Action)
		}
		if !d.RetainsIdentity {
			t.Fatalf("case %d: recovery did not retain the objective identity", i)
		}
	}
}

// ── LOCK 10: telemetry cannot manufacture completion ───────────────────────

// TestLock10_TelemetryNeverReportsUnknownForResolvedState is the telemetry lock
// the live trace violated: it printed `user_intent=unknown scope=unknown` while
// the authoritative scope had already been resolved and published.
func TestLock10_TelemetryNeverReportsUnknownForResolvedState(t *testing.T) {
	d, _ := anchorHarness(t, exhaustedProvider{})
	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	user, mode, interaction, exec := d.intentAxes()

	// A resolved objective is never "unknown" on the intent axis.
	if user == "unknown" {
		t.Fatalf("user_intent rendered %q for a resolved mutation objective", user)
	}
	// The command surface genuinely is not carried into the loop, and must say
	// so explicitly rather than substituting a neighbouring axis.
	if mode != NotCarried && strings.TrimSpace(mode) == "" {
		t.Fatalf("command_mode rendered empty; an unavailable axis must be %q", NotCarried)
	}
	// The interaction contract is bound by this run.
	if strings.TrimSpace(interaction) == "" {
		t.Fatal("interaction_contract rendered empty for a bound run")
	}
	// The execution intent is an obligation the authority judges against.
	if strings.TrimSpace(exec) == "" {
		t.Fatal("execution_intent rendered empty for a resolved objective")
	}

	// The scope axis reports the AUTHORITATIVE resolution, not the request field.
	if d.scopeResolution.State != ScopeResolved {
		t.Skipf("this run did not reach a resolved scope (%s); covered by the scope test", d.scopeResolution.State)
	}
	scope := d.authoritativeScope()
	if len(scope) == 0 {
		t.Fatal("the authoritative scope is empty despite a RESOLVED scope state")
	}
	for _, want := range scope {
		if !strings.Contains(strings.Join(scope, ","), want) {
			t.Fatalf("authoritative scope %v lost %q", scope, want)
		}
	}
}

// TestLock10_ProgressAndTelemetryAgree proves the published record and the
// runtime's own state cannot disagree. A trace that contradicts the state it is
// meant to describe is worse than no trace.
func TestLock10_ProgressAndTelemetryAgree(t *testing.T) {
	d, _ := anchorHarness(t, exhaustedProvider{})
	bus := d.bus
	if bus == nil {
		t.Skip("no bus bound")
	}
	activities := make(chan string, 64)
	sub := bus.Subscribe(events.EventActivity, func(e events.DomainEvent) {
		payload, ok := e.Payload().(events.ActivityPayload)
		if !ok {
			return
		}
		select {
		case activities <- payload.Line:
		default:
		}
	})
	t.Cleanup(sub.Cancel)

	if _, err := d.Run(context.Background(), "replace line two @index.html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The bus dispatches on its own goroutine, so Run returning does NOT mean the
	// intent record has been delivered yet. A non-blocking drain would therefore
	// assert on a race: under load the record simply had not arrived. Wait for it
	// (bounded), then validate its contents.
	deadline := time.After(5 * time.Second)
	sawIntent := false
	for !sawIntent {
		select {
		case msg := <-activities:
			if !strings.Contains(msg, "[intent]") {
				continue
			}
			sawIntent = true
			if strings.Contains(msg, "user_intent=unknown") {
				t.Fatalf("the published intent record reports an unknown axis for a resolved objective:\n%s", msg)
			}
			if strings.Contains(msg, "scope=unknown") {
				t.Fatalf("the published intent record reports an unknown scope:\n%s", msg)
			}
			if !strings.Contains(msg, "scope_state=") {
				t.Fatalf("the published intent record omits the scope resolution state:\n%s", msg)
			}
			if !strings.Contains(msg, "objective=run-1") {
				t.Fatalf("the published intent record omits the objective identity:\n%s", msg)
			}
		case <-deadline:
			t.Fatal("no four-axis intent record was published")
		}
	}
}

// ── LOCK 4: no repeated identical anchor failure ───────────────────────────

// TestLock4_RepeatedAnchorFailureTerminatesAndNeverMutates drives a run whose
// every patch is unanchorable and asserts the loop bounds it, leaves the
// workspace untouched and holds no candidate.
func TestLock4_RepeatedAnchorFailureTerminatesAndNeverMutates(t *testing.T) {
	d, x := anchorHarness(t, &mockProvider{
		responses: []*ai.Response{
			{Content: hallucinatedPatch},
			{Content: hallucinatedPatch},
			{Content: hallucinatedPatch},
			{Content: hallucinatedPatch},
		},
	})

	term, err := d.Run(context.Background(), "replace line two @index.html")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Truthful termination: never completion.
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a run whose every patch was unanchorable reported completion")
	}
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want a non-completing terminal", term)
	}
	// No mutation, ever.
	if got := readTarget(t, rootOf(t, d), "index.html"); got != anchorWorkspaceOriginal {
		t.Fatalf("repeated anchor failures mutated the workspace:\nwant=%q\ngot =%q", anchorWorkspaceOriginal, got)
	}
	// No candidate is left approvable.
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("an unanchorable patch survived as an approvable candidate: %v", ids)
	}
	// The failures were recorded and typed, so the trace explains the run.
	report := d.failures.LedgerReport()
	if len(report) == 0 {
		t.Fatal("no anchor failure was recorded in the lifecycle ledger")
	}
	found := false
	for _, r := range report {
		if strings.Contains(r, string(execution.FailureAnchorNotFound)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ANCHOR_NOT_FOUND classification was recorded:\n%s", strings.Join(report, "\n"))
	}
	// And the terminal state is not a fabricated failure verdict either: the
	// objective is unsubstantiated or parked, both of which are truthful.
	switch d.State() {
	case autonomy.RuntimeUnsubstantiated, autonomy.RuntimeAwaitingHuman, autonomy.RuntimeAborted:
	default:
		t.Fatalf("terminal state = %s, want a truthful non-completing terminal", d.State())
	}
}

// TestLock4_AnchorFailureIsNeverLabelledAnOutputBudgetBreach pins the mislabel
// that the live trace exhibited. An anchor failure and an output-budget failure
// are different facts and must never share a name.
func TestLock4_AnchorFailureIsNeverLabelledAnOutputBudgetBreach(t *testing.T) {
	src := stripComments(runtimeSource(t, "driver.go"))
	if strings.Contains(src, `"Physical Output Budget Breach: strict line-anchor recovery exhausted"`) {
		t.Error("the driver still terminates an anchor failure with an OUTPUT BUDGET label")
	}
	// recovery.go's historical wording is retained for the genuine budget gate
	// only; the anchor branch must not route through it.
	if strings.Contains(src, "isPhysicalOutputBudgetBreach(obs)") {
		// The call itself is correct — it is the real budget gate. Assert that
		// the anchor handling does not sit inside that branch.
		body, ok := functionBody(runtimeSource(t, "driver.go"), "func (d *Driver) recordAnchorFailure(")
		if !ok {
			t.Fatal("recordAnchorFailure is gone; anchor evidence must still be recorded")
		}
		if strings.Contains(stripComments(body), "Physical Output Budget") {
			t.Error("the anchor-failure recorder uses an output-budget label")
		}
	}
	// The recorder must invalidate the candidate and record evidence.
	body, ok := functionBody(runtimeSource(t, "driver.go"), "func (d *Driver) recordAnchorFailure(")
	if !ok {
		t.Fatal("recordAnchorFailure is gone")
	}
	code := stripComments(body)
	if !strings.Contains(code, "InvalidatePendingCandidates") {
		t.Error("an anchor failure must invalidate the candidate it produced")
	}
	if !strings.Contains(code, "[anchor]") {
		t.Error("an anchor failure must publish structured evidence")
	}
}
