// ── Grant-gated workspace context barrier (Phase 15) ─────────────────────────
//
// The defect this file exists to close is an ORDERING bug with a silent
// consequence. Capability authorization and workspace-context compilation are two
// independent facts, and nothing used to order them:
//
//	grant(read+analyze+propose+mutate) ──?──▶ context compiled ──▶ barrier lowered
//
// When the compilation won that race, the prompt crossed the provider boundary
// carrying a valid-looking projection of nothing — the right scope, the right
// intent, roughly a hundred tokens of framing and no file content. The model was
// then asked to judge a workspace it had never been shown, and it answered with
// prose, which the artifact boundary correctly refused. Every symptom pointed at
// the model. The actual fault was here.
//
// The fix is structural rather than probabilistic. The gate consults the
// AUTHORITATIVE grant ledger and OBSERVES the authorization events, and it
// performs the re-compilation SYNCHRONOUSLY at the last point before the
// preflight barrier lets the loop leave `observing`. There is no window in which
// the loop can proceed on a context frozen before the grant:
//
//	grant seen ──▶ cache invalidated ──▶ recompiled ──▶ verified non-empty
//	                                                          │
//	                                barrier lowered only here ┘
//
// Two properties are load-bearing:
//
//  1. SYNCHRONOUS. The re-compilation happens on the loop goroutine, between the
//     barrier wait and the `observing -> deciding` transition. Nothing is
//     deferred, backgrounded, or awaited later: by the time the loop can decide,
//     the verdict already exists.
//
//  2. FAIL CLOSED. A re-compilation that cannot carry real workspace bytes does
//     not degrade to the pre-grant context and does not proceed on an empty one.
//     It parks the run at an explicit human boundary carrying the reason. The
//     alternative — dispatching anyway — is precisely the bug.
//
// TWO SOURCES, ONE VERDICT. The ledger is the authority for grants that already
// exist; the bus is the authority for grants issued while the run is in flight.
// Consulting only the ledger would miss a mid-run authorization, and consulting
// only the bus would miss the common case entirely — the UI authorizes, and only
// THEN dispatches the run, so the grant event is published before the loop
// exists to observe it. Reading only the bus is precisely the bug this phase
// exists to fix, reappearing one layer up.
//
// The gate is also narrow on purpose. It engages only when a full workspace grant
// is actually in force, so a read-only investigation, a headless test harness,
// and a run authorized by other means are all untouched.
package autonomy

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// WorkspaceGrantCapabilities is the capability vector that unlocks a granted
// workspace context: the runtime may read the target, analyze it, propose a
// change to it, and mutate it.
//
// `verify` is deliberately absent. Verification is what the runtime does AFTER a
// change lands; it is not a precondition for being shown the file, and requiring
// it would make the context gate depend on a permission the run has not used.
var WorkspaceGrantCapabilities = []autonomy.Capability{
	autonomy.CapRead,
	autonomy.CapAnalyze,
	autonomy.CapPropose,
	autonomy.CapMutate,
}

// grantObserver answers one question: is a full workspace grant in force right
// now? It has two independent sources and one verdict.
//
// It is safe for concurrent use. The ledger is written on the UI's goroutine
// while the loop reads it from its own, and the bus delivers on a third.
type grantObserver struct {
	mu sync.Mutex
	// observed is the union of capabilities seen on the bus since the current
	// lifecycle began. It is per-lifecycle: a grant issued for one objective
	// must not gate the context of the next one.
	observed map[string]autonomy.CapabilitySet
	// ledger is the authoritative session grant ledger, when wired. It is read on
	// every query rather than snapshotted, so a grant issued at any point in the
	// run is visible to the gate.
	ledger *autonomy.GrantLedger
	sub    *events.Subscription
}

// newGrantObserver subscribes to capability grants on the shared bus and binds
// the authoritative ledger.
//
// A nil bus or a closed bus yields an observer that observes nothing, and a nil
// ledger yields one that has only the bus to go on. Either is legal: a headless
// run with neither simply never engages the gate, which is the correct behaviour
// for a run nobody authorized.
func newGrantObserver(bus *events.Bus, ledger *autonomy.GrantLedger) *grantObserver {
	g := &grantObserver{
		observed: make(map[string]autonomy.CapabilitySet),
		ledger:   ledger,
	}
	if bus != nil {
		g.sub = bus.Subscribe(events.EventCapabilityGranted, g.observe)
	}
	return g
}

// observe records one grant from the bus. The capability names travel as the
// payload's own vocabulary rather than being parsed out of a message, so a grant
// the runtime cannot understand simply does not satisfy the gate.
func (g *grantObserver) observe(ev events.DomainEvent) {
	if g == nil || ev == nil {
		return
	}
	payload, ok := ev.Payload().(events.CapabilityGrantedPayload)
	if !ok {
		return
	}
	caps := make(autonomy.CapabilitySet, 0, len(payload.Capabilities))
	for _, name := range payload.Capabilities {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed == "" {
			continue
		}
		caps = append(caps, autonomy.Capability(trimmed))
	}
	if len(caps) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unionLocked(payload.Scope, caps)
}

// unionLocked folds capabilities into the observed set for a scope. Grants are
// append-only within a lifecycle, so the union is exact: two partial grants of
// different capabilities add up to one full grant, and re-granting the same
// capability is a no-op.
func (g *grantObserver) unionLocked(scope string, caps autonomy.CapabilitySet) {
	existing := g.observed[scope]
	for _, c := range caps {
		if !existing.Has(c) {
			existing = append(existing, c)
		}
	}
	g.observed[scope] = existing
}

// bindLedger attaches the authoritative grant ledger to an existing observer.
//
// It exists so `WithGrantLedger` can be applied to a driver that already
// subscribed. Constructing a second observer would orphan the first one's
// subscription, and a bus subscription owns a dispatch goroutine — so the leak
// would be invisible in the UI and fatal under the architecture seal's goroutine
// sweep. Rebinding is the only correct move: the driver owns exactly one
// subscription for its whole lifetime.
func (g *grantObserver) bindLedger(ledger *autonomy.GrantLedger) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ledger = ledger
}

// reset clears the per-lifecycle observation, keeping the subscription and the
// ledger binding. A new lifecycle must not inherit the previous run's observed
// authorizations.
func (g *grantObserver) reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = make(map[string]autonomy.CapabilitySet)
}

// workspaceGranted reports whether a full workspace capability vector is in force,
// from either source.
//
// Scope is intentionally not compared against the run's targets. A grant names
// the boundary it authorizes and the targets name the boundary the run is about
// to touch; requiring the two to match textually would make the gate fail on a
// cosmetic difference in scope spelling while the authorization itself is
// unambiguous — trading a security property for a string comparison is the wrong
// trade in both directions.
func (g *grantObserver) workspaceGranted() bool {
	if g == nil {
		return false
	}
	if ledgerCaps := g.ledgerCaps(); coversCapabilities(ledgerCaps, WorkspaceGrantCapabilities) {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, caps := range g.observed {
		if coversCapabilities(caps, WorkspaceGrantCapabilities) {
			return true
		}
	}
	return false
}

// ledgerCaps returns the union of capabilities across every ACTIVE grant. Scope is
// not filtered: the same reasoning as above applies, and a filtered read would
// fail closed on a run whose grant is real but whose scope label differs from the
// engine's.
func (g *grantObserver) ledgerCaps() autonomy.CapabilitySet {
	if g == nil || g.ledger == nil {
		return nil
	}
	var union autonomy.CapabilitySet
	for _, grant := range g.ledger.Active() {
		for _, c := range grant.Capabilities {
			if !union.Has(c) {
				union = append(union, c)
			}
		}
	}
	return union
}

// close releases the subscription. It is safe to call more than once.
func (g *grantObserver) close() {
	if g == nil || g.sub == nil {
		return
	}
	g.sub.Cancel()
	g.sub = nil
}

// coversCapabilities reports whether granted holds every required capability.
func coversCapabilities(granted, required autonomy.CapabilitySet) bool {
	for _, want := range required {
		if !granted.Has(want) {
			return false
		}
	}
	return true
}

// syncGrantedWorkspaceContext is the GATE. It runs on the loop goroutine at the
// last moment before the preflight barrier is lowered, and it is the only place
// a granted workspace capability is turned into a real compiled context.
//
// It is a no-op unless a full workspace grant is in force, and idempotent
// afterwards: the re-compilation runs at most once per lifecycle, so a resumed
// or re-driven run does not re-invalidate a context the current attempt is
// already using.
//
// A non-nil error means the run must NOT dispatch. The caller parks it at a
// human boundary carrying the reason verbatim.
func (d *Driver) syncGrantedWorkspaceContext(ctx context.Context) error {
	if d == nil || d.adapter == nil {
		return nil
	}
	if !d.grants.workspaceGranted() {
		// No grant in force ⇒ no gate. This is the common path for read-only
		// work, headless tests, and any run authorized by other means.
		return nil
	}
	if d.grantContextSynced {
		return nil
	}
	targets := d.objectiveTargets()
	if len(targets) == 0 {
		// A granted workspace capability with no declared target has nothing to
		// compile. That is not starvation — there is no workspace being asked
		// about — so the gate declines to engage rather than inventing a
		// requirement.
		d.grantContextSynced = true
		return nil
	}
	// A DECLARED CREATION has no pre-existing workspace material to compile: the
	// target is expected absent. Requiring bytes would make it impossible to
	// create a file through the granted path, so the contract is the CREATION
	// contract, which still requires the target to be NAMED (scope) while
	// accepting the absence. Every other mutation keeps the workspace contract
	// unchanged.
	required := contextcompiler.IntentContextWorkspace
	if d.objectiveSemantics().Operation == execution.OperationCreate {
		required = contextcompiler.IntentContextCreation
	}
	provenance, err := d.adapter.RecompileGrantedIntentContext(ctx, targets,
		string(autonomy.IntentModification), required)
	if err != nil {
		return fmt.Errorf("autonomy: granted workspace context re-compilation for %s: %w",
			strings.Join(targets, ", "), err)
	}
	// The commit is last and unconditional: the revision is only complete once a
	// context that satisfies the contract exists. Until this line the authority
	// still reports the context as invalid, so a concurrent reader fails closed.
	if err := d.intentAuthority().CommitContext(autonomy.IntentModification, provenance.Valid); err != nil {
		return fmt.Errorf("autonomy: granted workspace context commit: %w", err)
	}
	d.grantContextSynced = true
	if d.bus != nil {
		d.bus.Publish(events.NewActivity(fmt.Sprintf(
			"[context] granted workspace re-compiled for %s: scope_matched=%t workspace_material=%t tokens=%d (telemetry only)",
			strings.Join(targets, ", "), provenance.ScopeMatched,
			provenance.WorkspaceMaterialPresent, provenance.ObservedTokens)))
	}
	diagnosticf("[context] grant-gated re-compilation committed for %s: %d token(s), workspace_material=%t",
		strings.Join(targets, ", "), provenance.ObservedTokens, provenance.WorkspaceMaterialPresent)
	return nil
}

// Close releases the driver's long-lived resources, principally the grant
// observer's event-bus subscription.
//
// Lifecycle ownership is the whole reason this method exists. The subscription is
// created with the DRIVER rather than with a run, because production order is
// authorize-then-dispatch and a run-scoped observer would miss the grant — but a
// bus subscription owns a dispatch goroutine, so a driver that is never closed
// leaks one per process. The composition root owns the driver's lifetime and is
// therefore the only thing that can close it; a run can never do so, because the
// driver must keep observing authorizations between runs.
//
// It is safe to call more than once, and safe to call on a nil driver.
func (d *Driver) Close() {
	if d == nil {
		return
	}
	if d.grants != nil {
		d.grants.close()
	}
}

// releaseRunResources drops the per-lifecycle runtime handles once the run has
// reached a terminal state. It is the single release point for everything a run
// owns that outlives it, so a new Run can never inherit a stale cancellation
// context.
//
// It deliberately does NOT release the grant observer: that subscription is a
// property of the driver, not of a run, and closing it here would make the second
// Run of a session blind to authorizations issued for it. Close() is the release
// point for it.
func (d *Driver) releaseRunResources() {
	if d == nil {
		return
	}
	d.runCtx, d.runCancel = nil, nil
}

// parkOnGrantContextFailure parks the run at an explicit human boundary when the
// grant-gated re-compilation cannot satisfy its contract. The run terminates
// nothing: the human gets a decision, not a stack trace and not a silent dispatch
// over an empty workspace.
func (d *Driver) parkOnGrantContextFailure(ctx context.Context, cause error) *autonomy.LoopTermination {
	reason := "workspace context could not be compiled under the granted capabilities: " + cause.Error() +
		" — the run holds a read+analyze+propose+mutate grant but the model would be shown no workspace; " +
		"re-scope the target or revoke the grant"
	if d.loop != nil && d.loop.State() == autonomy.RuntimeObserving {
		// observing -> deciding first, so the human boundary is a legal
		// transition. The context is never dispatched either way; the transition
		// exists so the loop history records how it stopped.
		_ = d.loop.Observe(d.obs)
		d.publish(ctx)
	}
	if d.loop != nil && d.loop.State() != autonomy.RuntimeAwaitingHuman {
		d.loop.AwaitHuman(autonomy.HumanBoundary{
			Reason:  reason,
			Targets: d.objectiveTargets(),
		})
		d.enrichBoundary()
		d.publish(ctx)
	}
	return d.term()
}
