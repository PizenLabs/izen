package kernel

// ── Step dispatch ───────────────────────────────────────────────────────────
//
// The functions below are the kernel's only contact with a capability: the
// ordered path from "a step is next" to "evidence exists". They live apart from
// the execution lifecycle in engine.go so that the two responsibilities —
// admitting and settling an execution, and doing one step's work — can be read
// independently.

import (
	"context"
	"errors"
	"strings"
)

// classOf normalises a class, defaulting anything unexpected to a generic
// execution failure rather than letting an unknown label escape the taxonomy.
func classOf(c FailureClass) FailureClass {
	if c.Valid() {
		return c
	}
	return FailureExecution
}

// runStep executes one step: budget, authorization, capability gate, invoke,
// evidence, transitions.
//
// The order is the contract. Budget is checked before work begins so an
// exhausted bound stops work rather than reporting it afterwards; authorization
// is checked before the capability is touched at all; and the capability's own
// gate is checked after authorization because a capability's preconditions are
// things a grant cannot express.
func (e *Engine) runStep(ctx context.Context, step Step, spec Spec, grant Grant) error {
	if e.Cancelled() {
		return blockf(FailureCancelled, step.ID, step.Capability, "execution cancelled before step %q", step.ID)
	}
	if err := ctx.Err(); err != nil {
		return blockf(FailureInterrupted, step.ID, step.Capability, "%v", err)
	}

	// ── Budget, before any work ──────────────────────────────────────────
	e.mu.Lock()
	accounting := e.state.Budget
	e.mu.Unlock()
	if err := accounting.allows(step.Capability); err != nil {
		return e.blockStep(step, err)
	}

	// ── Authorization ────────────────────────────────────────────────────
	req := Request{
		Capability: step.Capability,
		Step:       step.ID,
		Target:     step.Target,
		Args:       cloneArgs(step.Args),
		Grant:      grant,
	}
	if err := grant.Authorize(req, spec.Contract.Targets); err != nil {
		return e.blockStep(step, err)
	}
	if err := e.authorizer.Authorize(req, grant); err != nil {
		return e.blockStep(step, err)
	}

	cap, ok := e.registry.Lookup(step.Capability)
	if !ok {
		return e.blockStep(step, blockf(FailureCapabilityUnavailable, step.ID, step.Capability,
			"capability %q is not registered", step.Capability))
	}
	if err := cap.Authorize(req); err != nil {
		return e.blockStep(step, asBlock(err, FailureAuthorization, step))
	}

	// ── Dispatch ─────────────────────────────────────────────────────────
	if _, err := e.emit(Event{
		Kind:        EventStepStarted,
		ExecutionID: spec.ExecutionID,
		Step:        step.ID,
		Capability:  step.Capability,
		Target:      step.Target,
	}); err != nil {
		return blockf(FailureExecution, step.ID, step.Capability, "step.started refused: %v", err)
	}

	obs, invokeErr := cap.Invoke(ctx, req)
	if invokeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if e.Cancelled() {
				return blockf(FailureCancelled, step.ID, step.Capability, "%v", invokeErr)
			}
			return blockf(FailureInterrupted, step.ID, step.Capability, "%v", invokeErr)
		}
		return blockf(classForCapabilityError(step, invokeErr), step.ID, step.Capability, "%v", invokeErr)
	}

	// ── Output budget, before the output is trusted ──────────────────────
	// The size of an invocation's output is unknowable until it returns, so this
	// check necessarily happens after the work. Its purpose is not to prevent the
	// overshoot but to refuse the result: an invocation whose output breached the
	// declared bound delivered an incomplete prefix, and treating that prefix as
	// whole is how `finish_reason=length` becomes a success.
	if obs.OutputBytes > 0 {
		e.mu.Lock()
		before := e.state.Budget
		e.mu.Unlock()
		if err := before.allowsOutput(obs.OutputBytes); err != nil {
			return e.blockStep(step, err)
		}
	}

	// ── Observation becomes evidence ─────────────────────────────────────
	// An observation with an invalid verdict is refused rather than coerced. A
	// capability that cannot describe its own outcome has not produced evidence,
	// and treating that as a pass would be the exact failure this kernel exists
	// to prevent.
	if !obs.Verdict.Valid() {
		return blockf(FailureCapability, step.ID, step.Capability,
			"capability returned verdict %q outside the closed vocabulary", obs.Verdict)
	}
	// A capability that reports FAIL did attempt the work and it did not succeed.
	// That is a positive failure, and the execution stops here rather than
	// continuing to adjudicate over an incomplete record — otherwise a failed
	// step would surface as UNSUBSTANTIATED, telling an operator the runtime
	// knows nothing when in fact it knows exactly what broke.
	if obs.Verdict == VerdictFail {
		return blockf(FailureCapability, step.ID, step.Capability,
			"capability reported FAIL%s", detailSuffix(obs.Detail))
	}
	if obs.Verdict == VerdictUnknown {
		return blockf(FailureCapability, step.ID, step.Capability,
			"capability could not determine its own outcome%s", detailSuffix(obs.Detail))
	}
	revision := e.State().Revision + 1
	records := observation(e.nextEvidenceID, revision, step, obs)

	providerAxis := providerAxisFor(step.Capability, obs, invokeErr)

	// capability.invoked charges the budget and records the provider axis. The
	// evidence rides along so the budget charge is derived from observed bytes
	// rather than from a declared count.
	if _, err := e.emit(Event{
		Kind:        EventCapabilityInvoked,
		ExecutionID: spec.ExecutionID,
		Step:        step.ID,
		Capability:  step.Capability,
		Target:      step.Target,
		Axis:        string(providerAxis),
		Evidence:    records,
		Detail:      obs.Detail,
	}); err != nil {
		return blockf(FailureExecution, step.ID, step.Capability, "capability.invoked refused: %v", err)
	}

	if len(records) > 0 {
		if _, err := e.emit(Event{
			Kind:        EventEvidenceProduced,
			ExecutionID: spec.ExecutionID,
			Step:        step.ID,
			Capability:  step.Capability,
			Target:      step.Target,
			Evidence:    records,
		}); err != nil {
			return blockf(FailureExecution, step.ID, step.Capability, "evidence.produced refused: %v", err)
		}
	}

	if err := e.projectAxes(spec.ExecutionID, step, records, obs); err != nil {
		return err
	}
	// Verification is deliberately NOT run here. It runs once, after every step
	// has completed — see verify. Running it per-step would judge the contract
	// against a workspace that is still mid-transformation.
	return nil
}

// projectAxes emits the boundary-crossing events implied by the evidence the
// capability actually produced.
//
// Note the shape: these events are DERIVED from evidence, never asserted by the
// capability. A capability reports "I wrote these bytes"; the kernel decides that
// means the mutation boundary was crossed. That separation is what keeps the
// axes from becoming a second, looser opinion about what happened.
func (e *Engine) projectAxes(executionID string, step Step, records []Evidence, obs Observation) error {
	mutated := false
	for _, rec := range records {
		if rec.Kind.Mutating() {
			mutated = true
			break
		}
	}
	if mutated {
		if _, err := e.emit(Event{
			Kind:        EventMutationApplied,
			ExecutionID: executionID,
			Step:        step.ID,
			Capability:  step.Capability,
			Target:      step.Target,
			Evidence:    records,
		}); err != nil {
			return blockf(FailureMutation, step.ID, step.Capability, "mutation.applied refused: %v", err)
		}
	}
	// A produced artifact requires a response the parser accepted. The kernel
	// only promotes the artifact axis when a response actually exists.
	if obs.Verdict.Succeeded() {
		produced := false
		for _, rec := range records {
			if rec.Kind == EvidenceResponseProduced {
				produced = true
				break
			}
		}
		if produced {
			if _, err := e.emit(Event{
				Kind:        EventArtifactProduced,
				ExecutionID: executionID,
				Step:        step.ID,
				Capability:  step.Capability,
				Target:      step.Target,
				Evidence:    records,
			}); err != nil {
				return blockf(FailureArtifact, step.ID, step.Capability, "artifact.produced refused: %v", err)
			}
		}
	}
	return nil
}

// verify runs the verification seam once, after every step has completed.
//
// It runs ONCE and it runs LAST, and both facts are load-bearing:
//
//   - Running once means the verifier judges the execution, not each step. A
//     verifier consulted after the first step of a three-step program would be
//     judging a workspace that is still mid-transformation, and a contract like
//     "the target exists at the end" would fail on a program that is about to
//     satisfy it.
//   - Running last means the verifier sees the complete evidence record. A
//     verifier that sees only its own step's evidence is not verifying the
//     execution.
//
// A contract that does not require verification records VerifyNotApplicable
// explicitly, so no projection can later render "verified" for an execution that
// was never checked.
func (e *Engine) verify(ctx context.Context, spec Spec) error {
	if !spec.Contract.RequiresVerification {
		if _, err := e.emit(Event{
			Kind:        EventVerificationSkipped,
			ExecutionID: spec.ExecutionID,
			Detail:      "contract does not require verification",
		}); err != nil {
			return blockf(FailureVerification, "", "", "verification.skipped refused: %v", err)
		}
		return nil
	}
	if e.verifier == nil {
		// The contract requires verification and nothing can perform it. The
		// requirement is left UNSATISFIED and the execution stops with a named
		// cause.
		//
		// Recording this as NOT_APPLICABLE would be the convenient choice and the
		// dishonest one: "no verifier was bound" is not "verification was provably
		// unnecessary", and conflating them lets an execution reach PROVEN with a
		// check that never ran.
		return blockf(FailureVerification, "", "",
			"contract requires verification but no verifier is bound")
	}

	if _, err := e.emit(Event{
		Kind:        EventVerificationStarted,
		ExecutionID: spec.ExecutionID,
	}); err != nil {
		return blockf(FailureVerification, "", "", "verification.started refused: %v", err)
	}

	verdict, err := e.verifier.Verify(ctx, VerificationRequest{
		ExecutionID: spec.ExecutionID,
		Contract:    spec.Contract,
		Targets:     spec.Contract.Targets,
		Evidence:    e.State().Evidence,
	})
	if err != nil {
		if _, emitErr := e.emit(Event{
			Kind:        EventVerificationFailed,
			ExecutionID: spec.ExecutionID,
			Detail:      err.Error(),
		}); emitErr != nil {
			return blockf(FailureVerification, "", "", "%v", err)
		}
		return blockf(FailureVerification, "", "", "%v", err)
	}
	if !verdict.Valid() {
		return blockf(FailureVerification, "", "", "verifier returned verdict %q outside the closed vocabulary", verdict)
	}

	kind := EventVerificationFailed
	satisfied := false
	switch verdict {
	case VerdictPass, VerdictNoOp:
		// A real pass. Only this case contributes VerifyPassed.
		kind = EventVerificationPassed
		satisfied = true
	case VerdictNotApplicable:
		// Provably unnecessary. It satisfies the requirement, and it is recorded
		// as its own axis value so no projection can later render it as a pass.
		kind = EventVerificationSkipped
		satisfied = true
	case VerdictFail, VerdictUnknown:
		kind = EventVerificationFailed
	}
	if _, err := e.emit(Event{
		Kind:        kind,
		ExecutionID: spec.ExecutionID,
		Detail:      verdict.String(),
	}); err != nil {
		return blockf(FailureVerification, "", "", "verification verdict refused: %v", err)
	}
	if !satisfied {
		return blockf(FailureVerification, "", "", "verification returned %s", verdict)
	}
	return nil
}

// detailSuffix renders a capability's detail line for inclusion in a failure
// reason, without producing a dangling colon.
func detailSuffix(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return ": " + detail
}

// blockStep records a step-level block and returns it.
func (e *Engine) blockStep(step Step, err error) error {
	block := asBlock(err, FailureExecution, step)
	if _, emitErr := e.emit(Event{
		Kind:        EventStepBlocked,
		ExecutionID: e.State().ExecutionID,
		Step:        step.ID,
		Capability:  step.Capability,
		Target:      step.Target,
		Block:       &block,
	}); emitErr != nil {
		return emitErr
	}
	return block
}

// settle adjudicates and records terminal truth.
func (e *Engine) settle(executionID string) Result {
	terminal, err := e.emit(Event{Kind: EventExecutionFinished, ExecutionID: executionID})
	if err != nil {
		// Adjudication refusing to settle is a kernel bug, and the honest
		// response is to say so rather than to publish a verdict nobody derived.
		state := e.State()
		return Result{
			ExecutionID: executionID,
			Outcome:     OutcomeUnsubstantiated,
			Reason:      "adjudication refused: " + err.Error(),
			State:       state,
		}
	}
	state := e.State()
	return Result{
		ExecutionID: executionID,
		Outcome:     state.Terminal.Outcome,
		Class:       state.Terminal.Class,
		Reason:      state.Terminal.Reason,
		Unmet:       append([]string(nil), state.Terminal.Unmet...),
		Evidence:    append([]Evidence(nil), state.Evidence...),
		Budget:      state.Budget,
		State:       state,
		Revision:    terminal.Revision,
	}
}

// settleFailure records a failure block and settles as that class.
func (e *Engine) settleFailure(executionID string, block Block) Result {
	// A failure arriving after the execution already settled (for example a
	// cancellation observed late) must not overwrite adjudicated truth.
	if e.State().Status.Terminal() {
		return e.settle(executionID)
	}
	if _, err := e.emit(Event{
		Kind:        EventExecutionFailed,
		ExecutionID: executionID,
		Step:        block.Step,
		Capability:  block.Capability,
		Block:       &block,
	}); err != nil {
		state := e.State()
		return Result{
			ExecutionID: executionID,
			Outcome:     outcomeForClass(classOf(block.Class)),
			Class:       classOf(block.Class),
			Reason:      block.Reason,
			State:       state,
		}
	}
	state := e.State()
	return Result{
		ExecutionID: executionID,
		Outcome:     state.Terminal.Outcome,
		Class:       state.Terminal.Class,
		Reason:      state.Terminal.Reason,
		Unmet:       append([]string(nil), state.Terminal.Unmet...),
		Evidence:    append([]Evidence(nil), state.Evidence...),
		Budget:      state.Budget,
		State:       state,
	}
}

// settleCancellation records a deliberate withdrawal.
func (e *Engine) settleCancellation(executionID string) Result {
	block := blockf(FailureCancelled, "", "", "execution cancelled by request")
	return e.settleFailure(executionID, block)
}

// settleInterruption records a stop the kernel did not choose.
func (e *Engine) settleInterruption(executionID string, block Block) Result {
	block.Class = FailureInterrupted
	return e.settleFailure(executionID, block)
}

// providerAxisFor derives the transport axis from what the capability reported.
//
// The axis is derived, never taken on trust: a capability that truncated its own
// output says so, and the kernel records TRUNCATED rather than DONE. This is the
// single place truncation is prevented from becoming success.
func providerAxisFor(cap CapabilityID, obs Observation, invokeErr error) ProviderAxis {
	if invokeErr != nil {
		return ProviderFailed
	}
	for _, f := range obs.Facts {
		if f.Kind == EvidenceCommandTruncated {
			return ProviderTruncated
		}
	}
	if !obs.Verdict.Valid() {
		return ProviderFailed
	}
	if obs.Verdict == VerdictFail || obs.Verdict == VerdictUnknown {
		return ProviderFailed
	}
	return ProviderDone
}

// classForCapabilityError maps an invocation error onto the failure taxonomy.
//
// The distinction preserved here is the one that matters operationally: a
// refused or failed invocation is a FAILURE, while a capability that does not
// exist at all is UNAVAILABLE and no amount of retrying will change that.
func classForCapabilityError(step Step, err error) FailureClass {
	switch {
	case errors.Is(err, context.Canceled):
		return FailureCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return FailureInterrupted
	default:
		return FailureCapability
	}
}

// asBlock normalises an arbitrary error into a Block, defaulting to the given
// class. Normalising at the boundary is what guarantees no untyped error ever
// escapes into the log or a result.
func asBlock(err error, fallback FailureClass, step Step) Block {
	if err == nil {
		return Block{Class: fallback, Step: step.ID, Capability: step.Capability, Reason: "unspecified failure"}
	}
	var b Block
	if errors.As(err, &b) {
		return b
	}
	return Block{
		Class:      classOf(fallback),
		Step:       step.ID,
		Capability: step.Capability,
		Reason:     err.Error(),
	}
}

// cloneArgs copies a step's arguments so a capability cannot retain or mutate the
// program's own map.
func cloneArgs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
