package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Engine is the kernel's scheduler and ledger driver.
//
// It is deliberately the ONLY mutable object in the package. It owns no
// knowledge of any domain, any provider, any terminal, and any configuration
// file: it holds a capability registry, an authorization gate, a durable event
// log, and the single authoritative state it advances.
//
// Everything it decides is decided by reduce. The engine's job is narrow and
// worth stating precisely, because the temptation to widen it is what produces
// a god object:
//
//  1. Admit a Spec: validate it, refuse what is not executable.
//  2. For each Step, in order: check the budget, check authorization, check the
//     capability's own gate, invoke it, convert its Observation into Evidence,
//     and emit the events that describe what happened.
//  3. Adjudicate once, from evidence alone.
//
// It does not decide what the objective means, what an artifact should contain,
// what a good patch looks like, or what the caller should display next. Those
// belong to the layers above and below it respectively.
type Engine struct {
	registry   *Registry
	authorizer Authorizer
	verifier   Verifier
	log        *EventLog

	mu    sync.Mutex
	state State
	seq   atomic.Uint64
	seqMu sync.Mutex

	// cancel records the most recent cancellation request so a stopped execution
	// can be distinguished from a failed one.
	cancelled atomic.Bool

	// admissionErr records why Open refused, so Run can report that exact cause.
	//
	// Without it, a refused admission would surface as UNSUBSTANTIATED — telling
	// an operator the runtime knows nothing, when in fact it knows precisely why
	// the execution cannot start. Losing a named cause in favour of a vague one
	// is how a real defect becomes an unexplainable one.
	admissionErr error
}

// Option configures an Engine. Options are the only way to inject a dependency,
// which keeps the kernel free of globals, singletons, and ambient configuration.
type Option func(*Engine)

// WithAuthorizer installs the authorization gate. The kernel's DefaultAuthorizer
// is used when this is not supplied, so an unconfigured engine still refuses
// anything outside its grant.
func WithAuthorizer(a Authorizer) Option {
	return func(e *Engine) {
		if a != nil {
			e.authorizer = a
		}
	}
}

// WithVerifier installs the verification seam. Without one, a contract that
// requires verification can never be satisfied, which is the correct behavior:
// the kernel reports UNSUBSTANTIATED rather than inventing a pass.
func WithVerifier(v Verifier) Option {
	return func(e *Engine) { e.verifier = v }
}

// WithEventLog installs a caller-owned event log so the caller can drain or
// persist it. The kernel always builds one when this is absent.
func WithEventLog(l *EventLog) Option {
	return func(e *Engine) {
		if l != nil {
			e.log = l
		}
	}
}

// NewEngine builds an engine over the given registry.
//
// A nil registry is refused rather than defaulted. An engine with no capability
// surface can only ever fail, and failing at construction says so plainly
// instead of producing an engine that refuses every step at runtime.
func NewEngine(registry *Registry, opts ...Option) (*Engine, error) {
	if registry == nil {
		return nil, blockf(FailureCapabilityUnavailable, "", "", "engine requires a capability registry")
	}
	e := &Engine{
		registry:   registry,
		authorizer: DefaultAuthorizer{},
		log:        NewEventLog(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e, nil
}

// Registry exposes the engine's capability surface.
func (e *Engine) Registry() *Registry {
	if e == nil {
		return nil
	}
	return e.registry
}

// Log exposes the durable event log. A consumer reads runtime truth from here;
// there is no other supported way to observe an execution in progress.
func (e *Engine) Log() *EventLog {
	if e == nil {
		return nil
	}
	return e.log
}

// State returns the single authoritative execution state.
//
// The returned value is a copy, including its evidence index, so a consumer
// reading it concurrently with the engine cannot observe a torn view. This is
// what lets a UI poll the state and subscribe to the log without any lock of its
// own.
func (e *Engine) State() State {
	if e == nil {
		return State{}
	}
	e.mu.Lock()
	s := e.state
	// Deep-copy the evidence log and rebuild the index from the copy. Without
	// this, a consumer holding a State could mutate the engine's own record
	// through the slice — which would make the evidence log writable by anyone
	// who read it.
	s.Evidence = append([]Evidence(nil), e.state.Evidence...)
	s.Terminal.Unmet = append([]string(nil), e.state.Terminal.Unmet...)
	s.reindex()
	e.mu.Unlock()
	return s
}

// Cancel withdraws the execution.
//
// Cancellation is explicit and classified: it produces OutcomeCancelled, which
// is distinguishable from a failure and from an interruption. Recovery may not
// reinterpret a cancelled execution as anything else.
func (e *Engine) Cancel() {
	if e == nil {
		return
	}
	e.cancelled.Store(true)
}

// Cancelled reports whether cancellation was requested.
func (e *Engine) Cancelled() bool {
	return e != nil && e.cancelled.Load()
}

// nextEvidenceID returns a deterministic evidence identifier. Ids are sequential
// within an execution so a log line's evidence references are stable.
func (e *Engine) nextEvidenceID() string {
	e.seqMu.Lock()
	defer e.seqMu.Unlock()
	n := e.seq.Add(1)
	return fmt.Sprintf("ev-%d", n)
}

// emit applies an event to the state and appends it to the log, in that order.
//
// The ordering matters and is not negotiable: state first, log second. If the
// log append could fail the caller would be able to return a Result whose state
// no log line explains, which is precisely the drift a single authoritative
// state exists to eliminate. The log append cannot fail.
func (e *Engine) emit(ev Event) (Event, error) {
	if e == nil {
		return Event{}, errors.New("kernel: nil engine")
	}
	if ev.ExecutionID == "" {
		ev.ExecutionID = e.State().ExecutionID
	}
	e.mu.Lock()
	next, err := reduce(e.state, ev)
	if err != nil {
		e.mu.Unlock()
		return Event{}, err
	}
	e.state = next
	applied := e.state.Revision
	e.mu.Unlock()

	// The event's revision is the one the reducer assigned. Stamping it here
	// rather than trusting the caller is what makes a hand-constructed event
	// indistinguishable from an engine-produced one.
	ev.Revision = applied
	if e.log != nil {
		e.log.Append(ev)
	}
	return ev, nil
}

// Open admits a spec and grant, creating the single authoritative state.
//
// Admission happens before anything is dispatched, and it is total: a spec that
// cannot be executed is refused here, so a program that would fail halfway
// through having already mutated something never starts.
func (e *Engine) Open(spec Spec, grant Grant) error {
	if e == nil {
		return errors.New("kernel: nil engine")
	}
	if err := e.recordAdmissionFailure(spec.Budget.Validate()); err != nil {
		return err
	}
	if err := e.recordAdmissionFailure(spec.Validate(e.registry)); err != nil {
		return err
	}
	if strings.TrimSpace(grant.ID) == "" {
		return e.recordAdmissionFailure(blockf(FailureAuthorization, "", "",
			"no grant supplied for execution %s", spec.ExecutionID))
	}
	// Every declared mutating step must be authorized up front. Checking only at
	// dispatch time would let a program mutate its first two files and then stop
	// at the third, which is exactly the partial-authority outcome a grant is
	// supposed to make impossible.
	for _, step := range spec.Program {
		if !grant.Permits(step.Capability) {
			return e.recordAdmissionFailure(blockf(FailureAuthorization, step.ID, step.Capability,
				"grant %q does not permit capability %q required by step %q", grant.ID, step.Capability, step.ID))
		}
		if !grant.Covers(step.Target, spec.Contract.Targets) {
			return e.recordAdmissionFailure(blockf(FailureAuthorization, step.ID, step.Capability,
				"grant %q does not cover target %q required by step %q", grant.ID, step.Target, step.ID))
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.ExecutionID != "" {
		return blockf(FailureInvalidSpec, "", "", "engine already holds execution %s", e.state.ExecutionID)
	}
	e.state = newState(spec, grant)
	e.admissionErr = nil
	return nil
}

// recordAdmissionFailure stores and returns an admission refusal.
func (e *Engine) recordAdmissionFailure(err error) error {
	if err == nil {
		return nil
	}
	e.mu.Lock()
	e.admissionErr = err
	e.mu.Unlock()
	return err
}

// Run executes an admitted program and returns the terminal result.
//
// Run never returns a Result whose Outcome is PROVEN unless adjudicate derived
// it from evidence in the log. There is no path through this function that
// manufactures a completion.
func (e *Engine) Run(ctx context.Context) Result {
	if e == nil {
		return Result{Outcome: OutcomeFailed, Class: FailureInvalidSpec, Reason: "nil engine"}
	}
	e.mu.Lock()
	admitted := e.state.Status == StatusAdmitted
	spec := e.state.Spec
	grant := e.state.Grant
	admissionErr := e.admissionErr
	e.mu.Unlock()

	if !admitted {
		// Report the recorded admission cause rather than a vague one. An engine
		// holding no execution because admission refused it must name the refusal.
		if admissionErr != nil {
			block := asBlock(admissionErr, FailureInvalidSpec, Step{})
			if spec.ExecutionID != "" {
				block.Reason = spec.ExecutionID + ": " + block.Reason
			}
			return Result{
				ExecutionID: spec.ExecutionID,
				Outcome:     outcomeForClass(classOf(block.Class)),
				Class:       classOf(block.Class),
				Reason:      block.Reason,
				State:       e.State(),
			}
		}
		return Result{
			ExecutionID: spec.ExecutionID,
			Outcome:     OutcomeUnsubstantiated,
			Reason:      "engine holds no admitted execution",
			State:       e.State(),
		}
	}

	if _, err := e.emit(Event{Kind: EventExecutionStarted, ExecutionID: spec.ExecutionID}); err != nil {
		return e.settleFailure(spec.ExecutionID, blockf(FailureInvalidSpec, "", "", "execution.started refused: %v", err))
	}

	for _, step := range spec.Program {
		if err := e.runStep(ctx, step, spec, grant); err != nil {
			if e.Cancelled() {
				return e.settleCancellation(spec.ExecutionID)
			}
			var block Block
			if errors.As(err, &block) {
				if classOf(block.Class) == FailureCancelled {
					return e.settleCancellation(spec.ExecutionID)
				}
				if classOf(block.Class) == FailureInterrupted {
					return e.settleInterruption(spec.ExecutionID, block)
				}
				return e.settleFailure(spec.ExecutionID, block)
			}
			return e.settleFailure(spec.ExecutionID, blockf(FailureExecution, step.ID, step.Capability, "%v", err))
		}
		if err := ctx.Err(); err != nil {
			if e.Cancelled() {
				return e.settleCancellation(spec.ExecutionID)
			}
			return e.settleInterruption(spec.ExecutionID, blockf(FailureInterrupted, step.ID, step.Capability, "%v", err))
		}
	}

	if err := e.verify(ctx, spec); err != nil {
		if e.Cancelled() {
			return e.settleCancellation(spec.ExecutionID)
		}
		return e.settleFailure(spec.ExecutionID, asBlock(err, FailureVerification, Step{}))
	}

	return e.settle(spec.ExecutionID)
}
