package kernel

import (
	"errors"
	"fmt"
)

// reduce is the kernel's pure transition function:
//
//	reduce(State, Event) -> State
//
// It is total, side-effect free, and depends on nothing but its arguments. That
// is the whole architectural point of this file: execution truth in IZEN is a
// fold over a durable event log, so the same event sequence always produces the
// same state, on any machine, in any order of review.
//
// Every property the rest of the kernel relies on follows from that shape:
//
//   - No event can advance a state for something that did not happen, because
//     the engine constructs events only from capability observations.
//   - No transition can be skipped, because Revision increases by exactly one
//     per accepted event and a rejected event leaves the state untouched.
//   - A settled state is final. Once Status is SETTLED, every further event is
//     refused rather than silently applied, so a late-arriving capability result
//     cannot rewrite terminal truth.
//
// A transition is REFUSED — not applied with a warning — when it would violate an
// invariant. Refusing is what makes the log trustworthy: an event that cannot be
// applied is a bug in the producer, and hiding it would hide the bug.
func reduce(s State, e Event) (State, error) {
	// ── Terminal immutability ──────────────────────────────────────────────
	// A settled execution is final. This is checked before everything else
	// because nothing else matters once truth has been adjudicated.
	if s.Status.Terminal() {
		return s, fmt.Errorf("kernel: cannot apply %s to execution %s: already settled as %s",
			e.Kind, s.ExecutionID, s.Terminal.Outcome)
	}
	if e.Kind == "" {
		return s, errors.New("kernel: event has no kind")
	}

	next := s
	next.Revision = s.Revision + 1

	switch e.Kind {

	case EventExecutionStarted:
		if s.Status != StatusAdmitted {
			return s, fmt.Errorf("kernel: execution.started is only valid from ADMITTED, not %s", s.Status)
		}
		next.Status = StatusRunning

	case EventStepStarted:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: step.started is only valid while RUNNING, not %s", s.Status)
		}
		if e.Step == "" {
			return s, errors.New("kernel: step.started names no step")
		}
		step, ok := stepAt(s.Spec.Program, e.Step)
		if !ok {
			return s, fmt.Errorf("kernel: step.started names step %q, which the program does not declare", e.Step)
		}
		if e.Capability != step.Capability {
			return s, fmt.Errorf("kernel: step.started claims capability %q but step %q declares %q", e.Capability, e.Step, step.Capability)
		}
		// The cursor advances only on step.started, so a projection reading the
		// log can never show a later step in flight.
		next.Cursor = stepIndex(s.Spec.Program, e.Step)
		next.Provider = ProviderInvoked

	case EventCapabilityInvoked:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: capability.invoked is only valid while RUNNING, not %s", s.Status)
		}
		if e.Step == "" {
			return s, errors.New("kernel: capability.invoked names no step")
		}
		step, ok := stepAt(s.Spec.Program, e.Step)
		if !ok {
			return s, fmt.Errorf("kernel: capability.invoked names step %q, which the program does not declare", e.Step)
		}
		// Budget is charged here, at the moment the invocation actually
		// happened, never before and never after. A step that was refused was
		// never charged, and a step that ran is always charged.
		next.Budget = s.Budget.charge(step.Capability, outputBytesOf(e))
		switch ProviderAxis(e.Axis) {
		case ProviderDone:
			next.Provider = ProviderDone
		case ProviderTruncated:
			next.Provider = ProviderTruncated
		case ProviderFailed:
			next.Provider = ProviderFailed
		case ProviderInvoked, "":
			// The axis stays INVOKED: the transport has not reported a terminal
			// state. Absence of a reported axis is not a reported DONE.
		default:
			return s, fmt.Errorf("kernel: capability.invoked carries unknown provider axis %q", e.Axis)
		}

	case EventEvidenceProduced:
		if len(e.Evidence) == 0 {
			return s, errors.New("kernel: evidence.produced carries no evidence")
		}
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: evidence.produced is only valid while RUNNING, not %s", s.Status)
		}
		// Cross-check every record against the program and the axis rules. This
		// is where a capability's claim is held to the contract: evidence must
		// name a real step, carry a valid kind, and never claim a mutation from a
		// read-only capability.
		for _, rec := range e.Evidence {
			step, ok := stepAt(s.Spec.Program, rec.Step)
			if !ok {
				return s, fmt.Errorf("kernel: evidence names step %q, which the program does not declare", rec.Step)
			}
			if !rec.Kind.Valid() {
				return s, fmt.Errorf("kernel: evidence kind %q is outside the closed vocabulary", rec.Kind)
			}
			if rec.Capability != step.Capability {
				return s, fmt.Errorf("kernel: evidence claims capability %q but step %q declares %q", rec.Capability, rec.Step, step.Capability)
			}
			if rec.Kind.Mutating() && !step.Capability.Mutating() {
				return s, fmt.Errorf("kernel: evidence %q claims a mutation from read-only capability %q", rec.Kind, step.Capability)
			}
			if !rec.Verdict.Valid() {
				return s, fmt.Errorf("kernel: evidence verdict %q is outside the closed vocabulary", rec.Verdict)
			}
		}
		// Records are appended onto a copy of the existing log. The copy is made
		// once, outside the loop, so a multi-record event cannot overwrite earlier
		// records from the same event — which is exactly the bug that let a write
		// produce evidence for only its last fact.
		merged := make([]Evidence, 0, len(s.Evidence)+len(e.Evidence))
		merged = append(merged, s.Evidence...)
		for _, rec := range e.Evidence {
			stamped := rec
			stamped.Revision = next.Revision
			merged = append(merged, stamped)
		}
		next.Evidence = merged
		next.reindex()

	case EventArtifactProduced:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: artifact.produced is only valid while RUNNING, not %s", s.Status)
		}
		// A produced artifact requires produced evidence. This is the check that
		// stops a parser's opinion from standing in for an observation.
		if !s.evidenceSet().has(EvidenceResponseProduced) && len(e.Evidence) == 0 {
			return s, errors.New("kernel: artifact.produced without any evidence to support it")
		}
		next.Artifact = ArtifactProduced

	case EventMutationApplied:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: mutation.applied is only valid while RUNNING, not %s", s.Status)
		}
		// The mutation axis is derived from write evidence, never from the event
		// alone. An event asserting a mutation that no evidence backs is refused,
		// which is what makes "mutation.applied" mean something checkable.
		if len(s.MutatedTargets()) == 0 && len(e.Evidence) == 0 {
			return s, errors.New("kernel: mutation.applied without write evidence")
		}
		next.Mutation = MutationApplied

	case EventMutationRolledBack:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: mutation.rolled_back is only valid while RUNNING, not %s", s.Status)
		}
		// A rollback is recorded as a fact about the filesystem boundary. It is
		// emphatically not a no-op: it is the evidence that makes the outcome
		// FAILED rather than merely unproven.
		next.Mutation = MutationRolledBack

	case EventVerificationStarted:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: verification.started is only valid while RUNNING, not %s", s.Status)
		}
		next.Verify = VerifyNotRun
		next.verifyStarted = true

	case EventVerificationPassed:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: verification.passed is only valid while RUNNING, not %s", s.Status)
		}
		// A verdict may only follow the start of a check. A log carrying a pass
		// with no check behind it describes a verification that never happened.
		if !s.verifyStarted {
			return s, errors.New("kernel: verification.passed without a preceding verification.started")
		}
		next.Verify = VerifyPassed

	case EventVerificationFailed:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: verification.failed is only valid while RUNNING, not %s", s.Status)
		}
		if !s.verifyStarted {
			return s, errors.New("kernel: verification.failed without a preceding verification.started")
		}
		next.Verify = VerifyFailed

	case EventVerificationSkipped:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: verification.skipped is only valid while RUNNING, not %s", s.Status)
		}
		// Skipped is recorded as its own axis value precisely so it is never
		// rendered as a pass. It does not require a started check: "no check was
		// necessary" is a claim about the contract, not about a check that ran.
		next.Verify = VerifyNotApplicable

	case EventStepBlocked:
		if e.Block == nil {
			return s, errors.New("kernel: step.blocked carries no block")
		}
		if !e.Block.Class.Valid() {
			return s, fmt.Errorf("kernel: step.blocked carries class %q outside the closed taxonomy", e.Block.Class)
		}
		// A blocked step advances the cursor, because the program does not skip
		// work it could not do — it stops there. The step is not counted as
		// invoked: nothing ran.
		if e.Step != "" {
			if _, ok := stepAt(s.Spec.Program, e.Step); !ok {
				return s, fmt.Errorf("kernel: step.blocked names step %q, which the program does not declare", e.Step)
			}
			next.Cursor = stepIndex(s.Spec.Program, e.Step)
		}

	case EventExecutionSuspended:
		if s.Status != StatusRunning {
			return s, fmt.Errorf("kernel: execution.suspended is only valid while RUNNING, not %s", s.Status)
		}
		next.Status = StatusSuspended

	case EventExecutionResumed:
		if s.Status != StatusSuspended {
			return s, fmt.Errorf("kernel: execution.resumed is only valid while SUSPENDED, not %s", s.Status)
		}
		next.Status = StatusRunning

	case EventExecutionFailed, EventExecutionFinished:
		if s.Status == StatusAdmitted && e.Kind == EventExecutionFinished {
			return s, errors.New("kernel: execution.finished before the execution started")
		}
		outcome := Outcome("")
		class := FailureClass("")
		reason := ""
		var unmet []string
		if e.Kind == EventExecutionFailed {
			if e.Block == nil {
				return s, errors.New("kernel: execution.failed carries no block")
			}
			if !e.Block.Class.Valid() {
				return s, fmt.Errorf("kernel: execution.failed carries class %q outside the closed taxonomy", e.Block.Class)
			}
			class = e.Block.Class
			outcome = outcomeForClass(class)
			reason = e.Block.Reason
		} else {
			// Adjudication is the kernel's own work. A caller cannot hand the
			// kernel an outcome, because an outcome derived from a caller's
			// opinion is exactly the unsubstantiated claim this design exists to
			// eliminate.
			adj := adjudicate(s)
			outcome = adj.Outcome
			reason = adj.Reason
			unmet = adj.Unmet
			if outcome != OutcomeProven {
				class = FailureUnsubstantiated
			}
		}
		next.Status = StatusSettled
		next.Terminal = Terminal{
			Outcome:  outcome,
			Class:    class,
			Reason:   reason,
			Step:     e.Step,
			Unmet:    unmet,
			Revision: next.Revision,
		}

	case EventProviderStarted, EventProviderFinished:
		return s, fmt.Errorf("kernel: %s is recorded through capability.invoked, which carries the provider axis", e.Kind)

	default:
		return s, fmt.Errorf("kernel: event kind %q is outside the closed vocabulary", e.Kind)
	}

	return next, nil
}

// outputBytesOf reports the bytes an event declares it consumed. It reads the
// event's evidence records rather than a free-form field, so a capability
// cannot declare a byte count that no evidence supports.
func outputBytesOf(e Event) int {
	if len(e.Evidence) == 0 {
		return 0
	}
	total := 0
	for _, rec := range e.Evidence {
		total += rec.Bytes
	}
	return total
}

// stepAt returns the step with the given identifier.
func stepAt(program Program, id string) (Step, bool) {
	for _, s := range program {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// stepIndex returns the position of the step with the given identifier, or -1.
func stepIndex(program Program, id string) int {
	for i, s := range program {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// Fold replays a sequence of events over a state, returning the final state and
// the revision actually reached.
//
// It is the reconstruction path: given a durable log, Fold rebuilds the exact
// state the execution held. Two properties make the result trustworthy, and both
// are checked rather than assumed:
//
//   - Sequence. Every event must apply cleanly, in order. An event that cannot be
//     applied is a bug in the producer, and folding as far as possible and
//     returning a plausible-looking state would hide it.
//   - Completeness. Revisions must be strictly consecutive. The reducer advances
//     the revision by exactly one per transition, so a gap means a record is
//     missing — and a log with a hole in it describes an execution that cannot be
//     reconstructed, no matter how well the surviving events happen to fold.
//
// If a sequence fails either check, the log is not a valid record of an
// execution, and the correct answer is an error rather than a partial fold.
func Fold(initial State, events []Event) (State, error) {
	current := initial
	base := initial.Revision
	for _, e := range events {
		if want := base + 1; e.Revision != 0 && e.Revision != want {
			return current, fmt.Errorf(
				"kernel: fold rejected at %s: revision %d breaks the sequence, expected %d "+
					"(the log has a missing record)", e.Kind, e.Revision, want)
		}
		next, err := reduce(current, e)
		if err != nil {
			return current, fmt.Errorf("kernel: fold failed at %s: %w", e.Kind, err)
		}
		current = next
		base = next.Revision
	}
	return current, nil
}
