package autonomy

// ── Terminal objective ordering ──────────────────────────────────────────────
//
// The invariant these tests pin:
//
//	A terminal objective state must not be committed while the same execution
//	still has authorized, in-flight work that can change its artifacts or
//	outcome.
//
// In this runtime the completion authority is not the last gate a proposed
// completion passes. The behavioral proof gate and the contract-recovery
// breaker run AFTER it and can revoke a completion. The authority therefore
// STAGES its PROVEN verdict (authorizeObjectiveCompletion) and the terminal
// event + durable commit are written only by commitObjectiveCompletion, after
// every downgrade gate has run.
//
// The tests drive the exact seam with a granting authority and a later
// downgrade, so they are deterministic and need no provider and no timing.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// objectiveGrantRecorder records every objective.evaluated verdict and whether
// it granted the objective. It subscribes BEFORE the run so it observes the
// whole sequence rather than a late tail.
type objectiveGrantRecorder struct {
	mu     sync.Mutex
	grants int
	denies int
}

func newObjectiveGrantRecorder(bus *events.Bus) *objectiveGrantRecorder {
	r := &objectiveGrantRecorder{}
	bus.Subscribe(events.EventObjectiveEvaluated, func(ev events.DomainEvent) {
		p, ok := ev.Payload().(events.ObjectiveEvaluatedPayload)
		if !ok {
			return
		}
		r.mu.Lock()
		if p.Granted {
			r.grants++
		} else {
			r.denies++
		}
		r.mu.Unlock()
	})
	return r
}

func (r *objectiveGrantRecorder) counts() (grants, denies int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.grants, r.denies
}

// waitGrants blocks (bounded) until at least want granted verdicts have been
// delivered. The bus dispatches asynchronously, so a zero-wait read would assert
// against a partially-populated stream.
func (r *objectiveGrantRecorder) waitGrants(want int) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if g, _ := r.counts(); g >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func grantingDriver(t *testing.T) (*Driver, *objectiveGrantRecorder, *events.Bus) {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	t.Cleanup(bus.Close)
	rec := newObjectiveGrantRecorder(bus)
	d := NewDriver(nil, bus, WithObjectiveAuthority(&recordingAuthority{outcome: execution.ObjectiveProven}))
	d.prompt = "redesign this project @note.txt"
	d.obs = autonomy.Observation{}
	return d, rec, bus
}

// TestObjectiveTerminalGrantIsNotCommittedWhenALaterGateDowngrades is the
// regression for the premature-terminal defect. The completion authority proves
// the objective (the first artifact's completion callback), but a later gate
// revokes it (the remaining artifact's failure). The terminal objective event
// and the durable commit must NOT have been written on the strength of the
// authority alone.
func TestObjectiveTerminalGrantIsNotCommittedWhenALaterGateDowngrades(t *testing.T) {
	d, rec, _ := grantingDriver(t)

	decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "matrix proposed completion"}
	d.authorizeObjectiveCompletion(&decision)

	// The authority proved the objective, but the verdict is STAGED: no terminal
	// objective event may be published while a later gate can still revoke it.
	if d.pendingProven == nil {
		t.Fatal("a PROVEN verdict must be staged for deferred commit")
	}
	if decision.Action != autonomy.LoopComplete {
		t.Fatalf("decision action = %s, want the staged completion untouched", decision.Action)
	}
	time.Sleep(60 * time.Millisecond)
	if grants, _ := rec.counts(); grants != 0 {
		t.Fatalf("objective.evaluated granted=true was published before the downgrade gates ran (%d)", grants)
	}

	// A later gate revokes the completion before commit.
	decision.Action = autonomy.LoopUnsubstantiate
	decision.Reason = "contract recovery exhausted"
	d.commitObjectiveCompletion(&decision)

	if grants, _ := rec.counts(); grants != 0 {
		t.Fatalf("a revoked completion committed a terminal objective grant (%d)", grants)
	}
	if d.pendingProven != nil {
		t.Fatal("a revoked completion must discard its staged grant")
	}
}

// TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGateDowngrades proves the
// positive half: when no later gate removes the completion, the terminal
// objective event is published exactly once, at the end.
func TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGateDowngrades(t *testing.T) {
	d, rec, _ := grantingDriver(t)

	decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "matrix proposed completion"}
	d.authorizeObjectiveCompletion(&decision)
	d.commitObjectiveCompletion(&decision)

	rec.waitGrants(1)
	if grants, _ := rec.counts(); grants != 1 {
		t.Fatalf("granted objective events = %d, want exactly 1", grants)
	}
	// The staged grant is consumed; a second commit must be a no-op.
	d.commitObjectiveCompletion(&decision)
	time.Sleep(40 * time.Millisecond)
	if grants, _ := rec.counts(); grants != 1 {
		t.Fatalf("a second commit published another terminal grant (%d)", grants)
	}
}

// TestObjectiveTerminalGrantIsClearedOnEveryDecisionPoint proves the staging is
// per-decision: a grant staged at one decision point can never be committed
// against a later one that never proved anything.
func TestObjectiveTerminalGrantIsClearedOnEveryDecisionPoint(t *testing.T) {
	d, _, _ := grantingDriver(t)

	first := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "complete"}
	d.authorizeObjectiveCompletion(&first)
	if d.pendingProven == nil {
		t.Fatal("the first decision did not stage a grant")
	}
	// A later decision point that proposes something other than completion must
	// clear the stale grant.
	second := autonomy.LoopDecision{Action: autonomy.LoopRepair, Reason: "repair"}
	d.authorizeObjectiveCompletion(&second)
	if d.pendingProven != nil {
		t.Fatal("a non-completing decision point left a stale grant staged")
	}
}

// TestRequirementRejectionCarriesAnAttributableReason pins the observability
// half of requirement integrity: an all-rejected derivation must publish each
// rejection WITH the deterministic reason, so a reader can tell an unsupported
// proposal from a silently dropped one. The gate itself is not weakened — the
// requirement stays rejected; only the reason becomes visible.
func TestRequirementRejectionCarriesAnAttributableReason(t *testing.T) {
	_, _, adapter, _ := testHarness(t, []*ai.Response{{Content: "note.txt is a plain text file."}})
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	rec := newActivityRecorder(bus)
	d := NewDriver(adapter, bus,
		WithRequirementPass(scriptedRequirements("achieve worldclass kazakhstan-approved design")),
	)
	if _, err := d.Run(context.Background(), "explain the file @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForActivity(rec)
	joined := rec.joined()
	if !strings.Contains(joined, "admitted=0 rejected=1") {
		t.Fatalf("the requirements tally is missing from the trace:\n%s", joined)
	}
	if !strings.Contains(joined, "REJECTED(") {
		t.Fatalf("a rejected requirement was published without an attributable reason:\n%s", joined)
	}
	// The reason must name the rule, not merely assert rejection.
	if !strings.Contains(joined, "not traceable to the user's request") &&
		!strings.Contains(joined, "not grounded in any target") {
		t.Fatalf("the rejection reason does not name the admission rule:\n%s", joined)
	}
}
