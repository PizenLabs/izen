package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// ── Behavioral stage: the loop's execution-and-observation half ─────────────
//
// Before this stage existed the Driver could execute a mutation and verify it
// with a language toolchain, but it could not ask the question a real objective
// poses: does the thing RUN, and does it BEHAVE correctly? Every runtime-only
// defect was therefore invisible, and every loop failure parked at a human
// boundary because there was no evidence to continue from.
//
// BehavioralStage is a CONSUMER of the execution authority, exactly like the
// rest of this package. It adds no planner, no mutation path, and no authority:
//
//   - it observes through execution.BehavioralRuntime;
//   - it repairs through execution.BehaviorLoop, whose mutations go through the
//     SAME Substrate under the SAME authorization the rest of the runtime uses;
//   - it returns the same execution.ObjectiveEvaluation the existing completion
//     authority already consumes.
//
// What it adds is the ability to continue on evidence instead of parking.

// BehaviorStage runs the behavioral execution-and-verification phase of one
// objective.
//
// It is constructed ONCE per Driver (like the grant observer) so a grant issued
// immediately before a run — the common authorize-then-dispatch order — is
// visible to it.
type BehaviorStage struct {
	adapter  *ExecutorAdapter
	bus      eventsPublisher
	proposer execution.RepairProposer
	bounds   execution.LoopBounds
}

// newBehaviorStage builds the stage for a driver.
func newBehaviorStage(d *Driver, proposer execution.RepairProposer) *BehaviorStage {
	return &BehaviorStage{
		adapter:  d.adapter,
		bus:      d.bus,
		proposer: proposer,
		bounds:   execution.DefaultBehaviorBounds,
	}
}

// WithBehaviorProposer wires the reasoning backend the behavioral stage uses for
// repairs. Passing nil disables the stage: the Driver then behaves exactly as it
// did before, which is what keeps every existing test and every read-only
// objective unaffected.
func WithBehaviorProposer(p execution.RepairProposer) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		if p == nil {
			d.behavior = nil
			return
		}
		if d.behavior == nil {
			d.behavior = newBehaviorStage(d, p)
			return
		}
		d.behavior.proposer = p
	}
}

// WithBehaviorBounds overrides the behavioral loop's runtime-owned bounds.
func WithBehaviorBounds(b execution.LoopBounds) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		if d.behavior == nil {
			return
		}
		d.behavior.bounds = b.Normalized()
	}
}

// BehaviorRequired reports whether this objective demands behavioral proof.
//
// The predicate is about the OBJECTIVE, not the workspace: an objective that
// asks for a working result must be observed running, while an objective that
// only asks for a document to be written has no runtime to observe. Deciding it
// from the objective is what keeps the stage from turning every $prompt into a
// listener it does not need — and from skipping observation on the objectives
// that need it.
func BehaviorRequired(objective string) bool {
	lower := strings.ToLower(strings.TrimSpace(objective))
	if lower == "" {
		return false
	}
	// An objective that explicitly scopes itself to reading has nothing to run.
	for _, readOnly := range []string{
		"explain", "describe", "what is", "how does", "list the", "summar",
		"where is", "find ", "which files", "review the", "audit the",
	} {
		if strings.Contains(lower, readOnly) {
			return false
		}
	}
	// An objective that asks for a VERIFIABLE RESULT must be observed, not
	// asserted. These are the verbs that make a claim checkable.
	for _, behavioral := range []string{
		"work", "works", "working", "run", "runs", "running", "serve",
		"verif", "test", "functional", "behavio", "break", "fix", "repair",
		"debug", "correct", "valid", "load", "renders", "display", "execute",
	} {
		if strings.Contains(lower, behavioral) {
			return true
		}
	}
	return false
}

// BehaviorResult is the terminal outcome of the behavioral stage, projected onto
// the vocabulary the completion authority already understands.
type BehaviorResult struct {
	// Proven reports that the objective's observable requirements were proven to
	// hold by a real observation.
	Proven bool
	// EvidenceLine is the bounded evidence log of the proving (or failing)
	// observation.
	EvidenceLine string
	// DefectLine is the bounded defect summary, or "" when none remained.
	DefectLine string
	// Repairs counts the proposals that actually changed the workspace.
	Repairs int
	// Block is the truthful stop when the stage could not establish anything.
	Block *capability.Block
}

// Stage runs the behavioral phase for one objective.
//
// It returns a result whose Proven flag is the ONLY thing the driver may use to
// claim behavioral completion, and that flag is set exclusively from a real
// observation. A stage that could not observe returns Proven=false with a Block,
// so the driver's existing authority routes it to a human boundary with an
// attributable reason instead of completing on nothing.
func (s *BehaviorStage) Stage(ctx context.Context, objective string, provenance domain.ScopeProvenance) BehaviorResult {
	if s == nil || s.adapter == nil {
		return BehaviorResult{Block: &capability.Block{
			Class:  capability.FailureCapabilityMissing,
			Reason: "no behavioral stage is bound to this driver",
		}}
	}
	if s.proposer == nil {
		// A missing reasoning backend is a MISSING CAPABILITY, not a failed one:
		// nothing was attempted, and no amount of retrying would supply a
		// proposer. Reporting it as CAPABILITY_FAILED would send an operator
		// looking for a transient fault that does not exist.
		return BehaviorResult{Block: &capability.Block{
			Class:  capability.FailureCapabilityMissing,
			Reason: "no reasoning backend is bound to the behavioral stage; repairs cannot be proposed",
		}}
	}
	caps, ok := s.adapter.grantSnapshot(provenance)
	if !ok {
		// No usable capability set means no grant can be derived. Reporting this
		// is the truthful outcome; assuming read access would be an escalation.
		return BehaviorResult{Block: &capability.Block{
			Class:  capability.FailureAuthorizationBlocked,
			Reason: "no workspace capability set is bound; no capability can be granted",
		}}
	}
	grant := execution.GrantFor(provenance, caps)

	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: s.adapter.Root()})
	s.adapter.BindShellPort(rt)
	// Teardown runs under the pass's own context so a cancelled or timed-out
	// stage unwinds instead of blocking on a full shutdown window.
	defer func() { _ = rt.StopContext(ctx) }()

	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     grant,
		Proposer:  s.proposer,
		Mutate:    substrate.NewConcreteSubstrate(s.adapter.Root()),
		Authorize: s.adapter.AuthorizeMutation,
		Bounds:    s.bounds,
	})
	if err != nil {
		return BehaviorResult{Block: &capability.Block{
			Class:  capability.FailureCapabilityFailed,
			Reason: err.Error(),
		}}
	}

	s.publish("[behavior] " + objective)
	result := loop.Run(ctx)
	out := BehaviorResult{
		Proven:       result.Proven,
		EvidenceLine: result.Final.EvidenceLine(),
		Repairs:      result.RepairCount(),
		Block:        result.Block,
	}
	if n := result.Final.DefectCount(); n > 0 {
		out.DefectLine = result.Final.DefectLine()
	}
	if out.Proven {
		s.publish(fmt.Sprintf("[behavior] PROVEN after %d repair(s); evidence: %s", out.Repairs, out.EvidenceLine))
	} else {
		s.publish(fmt.Sprintf("[behavior] UNPROVEN after %d repair(s): %s", out.Repairs, behaviorReason(out)))
	}
	return out
}

func behaviorReason(r BehaviorResult) string {
	if r.Block != nil {
		return r.Block.Error()
	}
	if r.DefectLine != "" {
		return r.DefectLine
	}
	return "no behavioral evidence established"
}

// publish records the stage's transitions on the shared bus.
func (s *BehaviorStage) publish(msg string) {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.Publish(eventsActivity(msg))
}

// eventsPublisher is the minimal bus surface the stage needs. It is an interface
// so the stage depends on publication, not on the whole event model.
type eventsPublisher interface {
	Publish(events.DomainEvent)
}

func eventsActivity(msg string) events.DomainEvent { return events.NewActivity(msg) }

// BehaviorResultFor projects a stage result onto the driver's canonical
// behavior accessor. It exists so tests and telemetry read one shape.
func BehaviorResultFor(r BehaviorResult) BehaviorResult { return r }
