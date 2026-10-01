package autonomy

// ── PHASE 15: GRANT-GATED WORKSPACE CONTEXT BARRIER ──────────────────────────
//
// The defect: capability authorization and workspace-context compilation were two
// unordered facts. When compilation won the race, the prompt crossed the provider
// boundary carrying a valid projection of nothing — the right scope, the right
// intent, and no file content — and the model was asked to judge a workspace it
// had never been shown.
//
// The tests below pin the ORDER, not the intent:
//
//  1. A run with a full workspace grant re-compiles the target context
//     SYNCHRONOUSLY, before the preflight barrier is lowered.
//  2. A run with no grant is completely unaffected — the gate is narrow, and a
//     read-only investigation or a headless harness must not pay for it.
//  3. A re-compilation that produces no workspace material PARKS the run instead
//     of dispatching, and parks it at a human boundary rather than a raw error.
//  4. The context the re-compilation produces is real: the target's bytes are in
//     it. A gate that verified a number instead of the material would be the
//     token-threshold rule this phase exists alongside, not instead of.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/loop"
)

// noteTarget is the harness's default target, named once so the assertions read
// as statements about the workspace rather than as string literals.
const noteTarget = "note.txt"

// fullWorkspaceGrantLedger issues the complete workspace capability vector through
// the AUTHORITATIVE session ledger — the same path production uses.
func fullWorkspaceGrantLedger() *autonomy.GrantLedger {
	ledger := autonomy.NewGrantLedger()
	ledger.GrantCapability("repository", WorkspaceGrantCapabilities...)
	return ledger
}

// fullWorkspaceGrantEvent publishes the same vector on the bus, which is the path
// a mid-run authorization takes.
func fullWorkspaceGrantEvent(bus *events.Bus) {
	names := make([]string, 0, len(WorkspaceGrantCapabilities))
	for _, c := range WorkspaceGrantCapabilities {
		names = append(names, string(c))
	}
	bus.Publish(events.NewCapabilityGranted("grant-phase15", "repository", names, ""))
}

// TestPhase15_GrantGatedContextRecompilesBeforeTheBarrierLowers is the primary
// acceptance test. The grant is issued BEFORE the run — which is the production
// order, authorize-then-dispatch — and the gate is engaged on the very first
// observation, the exact ordering that used to starve the context.
func TestPhase15_GrantGatedContextRecompilesBeforeTheBarrierLowers(t *testing.T) {
	_, _, a, _ := testHarness(t, alwaysProse(6))
	d := NewDriver(a, nil, WithGrantLedger(fullWorkspaceGrantLedger()))

	if _, err := d.Run(context.Background(), "change bar to qux @"+noteTarget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !d.grantContextSynced {
		t.Fatal("the grant-gated re-compilation never committed")
	}
	// The commit is visible through the intent authority: a concurrent reader now
	// sees a valid context bound to the modification intent, instead of the
	// fail-closed "revision in flight" state a stale intent would leave behind.
	if !d.intentAuthority().ContextValid() {
		t.Fatal("the intent authority still reports the compiled context as invalid after the gate committed it")
	}
}

// TestPhase15_GrantGatedContextCarriesTheTargetMaterial pins the strongest form of
// the acceptance criterion: the target's workspace material really is in the
// compiled context. A gate that checked a token COUNT would pass this test with a
// payload full of framing, which is the failure it exists to catch.
func TestPhase15_GrantGatedContextCarriesTheTargetMaterial(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	provenance, err := a.RecompileGrantedIntentContext(context.Background(), []string{noteTarget},
		string(autonomy.IntentModification), contextcompiler.IntentContextWorkspace)
	if err != nil {
		t.Fatalf("RecompileGrantedIntentContext: %v", err)
	}
	if !provenance.Valid {
		t.Fatalf("provenance refused a real target: %+v", provenance)
	}
	if !provenance.WorkspaceMaterialPresent {
		t.Fatalf("no workspace material for an existing target: %+v", provenance)
	}
	if !provenance.ScopeMatched {
		t.Fatalf("the requested target is not in the payload: %+v", provenance)
	}
	if provenance.ObservedTokens <= 0 {
		t.Fatalf("the compiled context is %d token(s)", provenance.ObservedTokens)
	}
}

// TestPhase15_GrantGatedContextRefusesAnUnreadableTarget pins the fail-closed
// half. A granted workspace capability over a target the compiler cannot read
// must be refused, never satisfied by an empty payload.
func TestPhase15_GrantGatedContextRefusesAnUnreadableTarget(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	_, err := a.RecompileGrantedIntentContext(context.Background(), []string{"absent-target.txt"},
		string(autonomy.IntentModification), contextcompiler.IntentContextWorkspace)
	if err == nil {
		t.Fatal("a granted workspace capability over an unreadable target produced a valid context")
	}
	if !errors.Is(err, execution.ErrIntentContextProvenance) {
		t.Fatalf("err = %v, want ErrIntentContextProvenance", err)
	}
}

// TestPhase15_GrantedContextStarvationIsTheEmptyValidContext pins the
// CONDITION the Phase 15 gate adds on top of the provenance gate, as a pure
// function so it can be asserted without engineering a broken compiler.
//
// This is the state the pre-grant freeze actually produced: every provenance
// clause holds — the scope matches, the intent matches, the contract is a
// workspace contract — and the prompt still crosses the provider boundary with
// nothing in it. Provenance alone cannot see it, which is why the gate exists.
func TestPhase15_GrantedContextStarvationIsTheEmptyValidContext(t *testing.T) {
	valid := contextcompiler.ContextProvenance{
		Valid:                    true,
		ScopeMatched:             true,
		IntentMatched:            true,
		WorkspaceMaterialPresent: true,
		ObservedTokens:           0,
	}
	err := execution.GrantedContextStarvation(valid, []string{noteTarget}, contextcompiler.IntentContextWorkspace)
	if !errors.Is(err, execution.ErrIntentContextStarvation) {
		t.Fatalf("a valid-but-empty context was accepted: %v", err)
	}
	// The same payload with content is accepted.
	valid.ObservedTokens = 512
	if err := execution.GrantedContextStarvation(valid, []string{noteTarget}, contextcompiler.IntentContextWorkspace); err != nil {
		t.Fatalf("a populated context was refused: %v", err)
	}
	// A context with no workspace material at all is the same starvation.
	valid.ObservedTokens = 512
	valid.WorkspaceMaterialPresent = false
	if err := execution.GrantedContextStarvation(valid, []string{noteTarget}, contextcompiler.IntentContextWorkspace); !errors.Is(err, execution.ErrIntentContextStarvation) {
		t.Fatalf("err = %v, want ErrIntentContextStarvation for a payload with no workspace material", err)
	}
	// A workspace-free contract is NEVER starved: an empty payload is correct.
	valid = contextcompiler.ContextProvenance{Valid: true, IntentMatched: true}
	for _, required := range []string{contextcompiler.IntentContextNone, contextcompiler.IntentContextSelfContained} {
		if err := execution.GrantedContextStarvation(valid, []string{noteTarget}, required); err != nil {
			t.Fatalf("required=%q: a workspace-free contract must never be starved: %v", required, err)
		}
	}
	// An already-refused payload is NOT re-diagnosed: the provenance gate's reason
	// is the better answer, and two competing explanations for one failure is
	// worse than either.
	refused := contextcompiler.ContextProvenance{Valid: false, Reason: "scope mismatch"}
	if err := execution.GrantedContextStarvation(refused, []string{noteTarget}, contextcompiler.IntentContextWorkspace); err != nil {
		t.Fatalf("the starvation check re-diagnosed a refusal: %v", err)
	}
}

// TestPhase15_GrantGateDoesNotApplyToSelfContainedContracts pins that the
// starvation condition is scoped to the workspace contract. A zero-workspace or
// self-contained turn is CORRECT with an empty payload, and refusing it would
// reintroduce the token-threshold rule through the back door.
func TestPhase15_GrantGateDoesNotApplyToSelfContainedContracts(t *testing.T) {
	_, _, a, _ := testHarness(t, nil)
	for _, required := range []string{contextcompiler.IntentContextNone, contextcompiler.IntentContextSelfContained} {
		provenance, err := a.RecompileGrantedIntentContext(context.Background(), []string{noteTarget},
			string(autonomy.IntentModification), required)
		if err != nil {
			t.Fatalf("required=%q: a workspace-free contract must never be refused for starvation: %v", required, err)
		}
		if !provenance.Valid {
			t.Fatalf("required=%q: provenance refused a satisfiable contract: %+v", required, provenance)
		}
	}
}

// TestPhase15_GrantGatedContextIsInertWithoutAGrant pins that the gate is NARROW.
// Read-only work, headless harnesses and any run authorized by other means must
// be completely unaffected — a gate that engaged unconditionally would
// re-compile context for every chat turn in the product.
func TestPhase15_GrantGatedContextIsInertWithoutAGrant(t *testing.T) {
	_, _, a, _ := testHarness(t, alwaysProse(4))
	d := NewDriver(a, nil)
	if _, err := d.Run(context.Background(), "change bar to qux @"+noteTarget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.grantContextSynced {
		t.Fatal("the grant gate engaged with no grant issued")
	}
}

// TestPhase15_GrantGateIgnoresAPartialGrant pins that a grant missing any one of
// the four capabilities does not unlock workspace context. The gate keys on the
// COMPLETE vector on purpose: read+analyze+propose without mutate authorizes
// inspection, and a mutation context compiled for it would be a capability
// escalation dressed up as a cache warm.
func TestPhase15_GrantGateIgnoresAPartialGrant(t *testing.T) {
	_, _, a, bus := testHarness(t, alwaysProse(4))
	partial := autonomy.NewGrantLedger()
	partial.GrantCapability("repository", autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose)
	d := NewDriver(a, bus, WithGrantLedger(partial))
	if _, err := d.Run(context.Background(), "change bar to qux @"+noteTarget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.grantContextSynced {
		t.Fatal("a partial grant unlocked the workspace context gate")
	}
}

// TestPhase15_GrantGateAccumulatesSplitGrants pins that grants ADD UP within a
// lifecycle. The UI issues capabilities in whatever sets the decision model asked
// for, so a workspace may be authorized across two events; a gate that only
// looked at the most recent grant would miss a fully-authorized run.
func TestPhase15_GrantGateAccumulatesSplitGrants(t *testing.T) {
	_, _, a, bus := testHarness(t, alwaysProse(4))
	d := NewDriver(a, bus)
	bus.Publish(events.NewCapabilityGranted("grant-a", "repository", []string{"read", "analyze"}, ""))
	bus.Publish(events.NewCapabilityGranted("grant-b", "repository", []string{"propose", "mutate"}, ""))
	waitForWorkspaceGrant(t, d)
}

// TestPhase15_GrantGateSeesGrantsIssuedDuringTheRun covers the second source. A
// grant issued while a run is in flight must be observed, which is the only way a
// bus-only gate could work — and the reason a ledger-only gate would not.
func TestPhase15_GrantGateSeesGrantsIssuedDuringTheRun(t *testing.T) {
	_, _, a, bus := testHarness(t, alwaysProse(4))
	d := NewDriver(a, bus)
	if d.grants.workspaceGranted() {
		t.Fatal("a fresh observer must not report a grant that was never issued")
	}
	fullWorkspaceGrantEvent(bus)
	waitForWorkspaceGrant(t, d)
}

// TestPhase15_GrantGatedContextParksRatherThanDispatchingOnStarvation is the
// behavioural half of the fail-closed property. A target whose context cannot be
// compiled must stop the run at a human boundary with a reason a human can act on
// — not dispatch a provider call over an empty workspace, and not return a raw
// error.
//
// The workspace is built so target RESOLUTION succeeds while the CONTEXT
// COMPILATION cannot: the resolver reads one root and the executor's snapshot
// read is rooted in another. That is the real shape of the defect — the runtime
// knows perfectly well which file it was asked about and still cannot show it.
func TestPhase15_GrantGatedContextParksRatherThanDispatchingOnStarvation(t *testing.T) {
	resolveRoot := t.TempDir()
	writeTarget(t, resolveRoot, noteTarget, sampleOriginal)
	readRoot := t.TempDir() // deliberately empty: the executor can see no bytes
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: alwaysProse(4)}
	x := testExecutor(t, readRoot, mock, bus)
	a := NewExecutorAdapter(resolveRoot, execution.NewIntentGateway(resolveRoot), x)
	d := NewDriver(a, nil, WithGrantLedger(fullWorkspaceGrantLedger()))

	term, err := d.Run(context.Background(), "change bar to qux @"+noteTarget)
	if err != nil {
		t.Fatalf("a starved context must park, not return an error: %v", err)
	}
	if term != nil {
		t.Fatalf("a starved run must not terminate; it must park. termination = %+v", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want %s", d.State(), autonomy.RuntimeAwaitingHuman)
	}
	boundary := d.Boundary()
	if boundary == nil {
		t.Fatal("a parked run must carry a human boundary")
	}
	if !strings.Contains(boundary.Reason, "workspace context") {
		t.Fatalf("park reason %q does not name the context as the cause", boundary.Reason)
	}
	if !strings.Contains(boundary.Reason, noteTarget) {
		t.Fatalf("park reason %q does not name the target it could not compile", boundary.Reason)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 — no model may be asked to judge an empty workspace", mock.calls())
	}
	// The loop history records the park, so an audit can see how the run stopped.
	sawPark := false
	for _, tr := range d.History() {
		if tr.To == autonomy.RuntimeAwaitingHuman && strings.Contains(tr.Reason, "workspace context") {
			sawPark = true
		}
	}
	if !sawPark {
		t.Fatal("the loop history does not record the context-gate park")
	}
	// And the workspace is untouched.
	if got := readTarget(t, resolveRoot, noteTarget); got != sampleOriginal {
		t.Fatalf("workspace changed during a starved run: %q", got)
	}
}

// TestPhase15_GrantGatedContextRunsBeforeThePreflightBarrier pins the ORDER
// itself, which is the whole point of the phase: the context gate is evaluated
// before the barrier is waited on, never after. A re-compilation that happened
// after the barrier lowered would race the first dispatch — and a gate that ran
// after a barrier wait would never run at all when the wait times out.
func TestPhase15_GrantGatedContextRunsBeforeThePreflightBarrier(t *testing.T) {
	root, mock, a, bus := testHarness(t, alwaysProse(6))
	// A barrier that is never satisfied: if the gate ran AFTER the barrier wait,
	// the run would park on PREFLIGHT_TIMEOUT and the gate would never execute.
	barrier := loop.NewBarrierWithTimeout(30*time.Millisecond, bus)
	d := NewDriver(a, bus, WithPreflightBarrier(barrier), WithGrantLedger(fullWorkspaceGrantLedger()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The run's own error is irrelevant here — the barrier never lowers, so it
	// terminates on the PREFLIGHT_TIMEOUT path and returns a nil error. What this
	// test owns is the ordering of the gate relative to that wait.
	if _, runErr := d.Run(ctx, "change bar to qux @"+noteTarget); runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	if !d.grantContextSynced {
		t.Fatal("the grant gate did not run before the preflight barrier was lowered")
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0 (the barrier never lowered)", mock.calls())
	}
	if got := readTarget(t, root, noteTarget); got != sampleOriginal {
		t.Fatalf("workspace changed: %q", got)
	}
}

// TestPhase15_GrantObservationResetsPerLifecycle pins the reset. An observed grant
// must not gate the NEXT run: the second lifecycle below is unauthorized, and a
// carried-over observation would compile a mutation context for it.
func TestPhase15_GrantObservationResetsPerLifecycle(t *testing.T) {
	_, _, a, bus := testHarness(t, alwaysProse(8))
	d := NewDriver(a, bus)
	fullWorkspaceGrantEvent(bus)
	waitForWorkspaceGrant(t, d)
	if !d.grants.workspaceGranted() {
		t.Fatal("the observer never recorded the bus grant")
	}
	// A fresh Run clears the per-lifecycle observation.
	d.intents = autonomy.NewIntentAuthority()
	d.grants.reset()
	if d.grants.workspaceGranted() {
		t.Fatal("the grant observation survived into a new lifecycle")
	}
}

// TestPhase15_ExpiredGrantDoesNotUnlockTheGate pins that authorization has an
// expiry and the gate respects it. A grant that lapsed mid-session is not
// authorization, and compiling a mutation context for it would be acting on a
// permission that no longer exists.
func TestPhase15_ExpiredGrantDoesNotUnlockTheGate(t *testing.T) {
	_, _, a, _ := testHarness(t, alwaysProse(4))
	ledger := autonomy.NewGrantLedger()
	ledger.Issue(autonomy.Grant{
		Scope:        "repository",
		Capabilities: WorkspaceGrantCapabilities,
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	d := NewDriver(a, nil, WithGrantLedger(ledger))
	if d.grants.workspaceGranted() {
		t.Fatal("an expired grant unlocked the workspace context gate")
	}
	if _, err := d.Run(context.Background(), "change bar to qux @"+noteTarget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.grantContextSynced {
		t.Fatal("an expired grant caused a workspace re-compilation")
	}
}

// TestPhase15_GrantGateOwnsExactlyOneSubscription pins the lifecycle of the
// grant observer's bus subscription.
//
// A bus subscription owns a dispatch goroutine, and the option that binds the
// ledger runs AFTER the driver has already subscribed. Constructing a second
// observer instead of rebinding the first orphans a live subscription: invisible
// in the UI, and fatal under the architecture seal's goroutine sweep.
//
// The assertion is on the driver's own state rather than on a goroutine count,
// because a goroutine count here would be measuring the runtime scheduler. Close
// must leave no live subscription, and the observer must still be the SAME one
// the option rebound rather than a replacement.
func TestPhase15_GrantGateOwnsExactlyOneSubscription(t *testing.T) {
	_, _, a, bus := testHarness(t, nil)
	d := NewDriver(a, bus)
	observer := d.grants
	if observer == nil {
		t.Fatal("the driver has no grant observer")
	}
	if observer.sub == nil {
		t.Fatal("the grant observer is not subscribed to the bus")
	}

	// Applying the ledger option must REBIND. A replacement would orphan the
	// subscription above, and `observer` is exactly what would be orphaned.
	WithGrantLedger(fullWorkspaceGrantLedger())(d)
	if d.grants != observer {
		t.Fatal("WithGrantLedger replaced the observer and orphaned its bus subscription")
	}
	if observer.ledger == nil {
		t.Fatal("WithGrantLedger did not bind the ledger")
	}
	if observer.sub == nil {
		t.Fatal("rebinding dropped the bus subscription")
	}
	if !observer.workspaceGranted() {
		t.Fatal("the bound ledger is not consulted by the gate")
	}

	// Close releases the subscription, and is idempotent.
	d.Close()
	if observer.sub != nil {
		t.Fatal("Close left a live subscription on the grant observer")
	}
	d.Close()
	// A released observer still answers, it simply observes nothing new.
	observer.observe(events.NewCapabilityGranted("post-close", "repository",
		[]string{"read", "analyze", "propose", "mutate"}, ""))
}

// TestPhase15_GrantedContextReachesTheDispatchedPrompt is the acceptance
// criterion in its strongest form: the workspace material the gate verified is
// the material the MODEL actually receives.
//
// The gate's own payload is a proof, not the dispatch. What makes the criterion
// hold is that the gate drops the previous intent's cached projection, so the
// compilation the executor performs when it builds the request cannot be a cache
// hit on a read-only payload. This test asserts that end to end: it grants the
// workspace vector, lets the gate run, then reads the PROMPT the provider was
// actually handed and looks for the target's own bytes.
func TestPhase15_GrantedContextReachesTheDispatchedPrompt(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{
		{Content: sampleReplace},
		{Content: sampleReplace},
		{Content: sampleReplace},
	})
	d := NewDriver(a, nil, WithGrantLedger(fullWorkspaceGrantLedger()))
	if _, err := d.Run(context.Background(), "change bar to qux @"+noteTarget); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := mock.recordedRequests()
	if len(requests) == 0 {
		t.Fatal("no provider request was dispatched — nothing to inspect")
	}
	// A distinctive byte from the target file. If the prompt carried a read-only or
	// empty projection, this string would be absent even though the scope matched.
	const targetFingerprint = "baz"
	sawMaterial := false
	for i, req := range requests {
		prompt := req.System
		for _, msg := range req.Messages {
			prompt += "\n" + msg.Content
		}
		if strings.Contains(prompt, targetFingerprint) {
			sawMaterial = true
			break
		}
		_ = i
	}
	if !sawMaterial {
		t.Fatal("the dispatched prompt carried no workspace material — a granted " +
			"workspace capability reached the model with an empty context")
	}
	if !d.grantContextSynced {
		t.Fatal("the grant gate did not run for this lifecycle")
	}
	_ = root
}

// waitForWorkspaceGrant blocks until the observer has recorded a workspace grant
// from the bus. The bus dispatches on its own goroutine, so without this a test
// would race the dispatch for a reason that has nothing to do with the behaviour
// under test.
func waitForWorkspaceGrant(t *testing.T, d *Driver) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.grants.workspaceGranted() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the grant observer never recorded the workspace grant")
}
