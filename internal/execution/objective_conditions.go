// ── Objective progress, condition evaluation, continuation ──────────────────
//
// This file is the REASONING half of the completion contract. It answers three
// questions the ObjectiveCompletionAuthority needs and that a plain
// (contract, evidence) reducer cannot answer on its own:
//
//  1. Which completion conditions does the evidence actually satisfy?  →
//     ReduceConditions
//  2. What is the objective's progress, in the vocabulary a control plane and
//     a TUI can both read? → ReduceProgress
//  3. Given incomplete progress, what may the runtime do next? →
//     DecideContinuation
//
// All three are pure functions over facts the runtime already observed. None of
// them consults a model, and none of them judges content quality.
//
// The authority stays deterministic and fail-closed: this file can only ever
// REMOVE a completion, never grant one. A condition the runtime cannot observe
// satisfied is ConditionUnsatisfied, and an incomplete objective is reported as
// incomplete — which is a successful runtime behaviour, not a failure to hide.
package execution

import (
	"fmt"
	"sort"
	"strings"
)

// ── Progress ────────────────────────────────────────────────────────────────

// ObjectiveProgress is the domain-neutral progress vocabulary of one objective
// lifecycle. It is strictly richer than the binary (proven | not proven) split
// the authority needs for its verdict, because the CONTROL PLANE needs to know
// WHICH kind of not-proven it is looking at in order to choose between
// continuing, replanning, asking a human and failing.
//
// It deliberately does not overlap the existing lifecycle states:
//
//   - ObjectiveState (PROVEN / UNPROVEN) is the MEANING boundary and stays a
//     two-valued projection of ObjectiveOutcome, so every existing consumer is
//     untouched;
//   - ObjectiveOutcome (PROVEN / UNSUBSTANTIATED / FAILED /
//     REQUIRES_AUTHORIZATION) stays the authority's total verdict vocabulary;
//   - the ExecutionOutcome vocabulary stays the execution plane's.
//
// A reader that only knows PROVEN is correct keeps working. A control plane
// that wants to know why not gets the reason from HERE, deterministically.
type ObjectiveProgress string

const (
	// ProgressDiscovered: the objective exists and its scope is being resolved.
	ProgressDiscovered ObjectiveProgress = "DISCOVERED"
	// ProgressUnderstood: the request was segmented into its deterministic
	// clause set.
	ProgressUnderstood ObjectiveProgress = "UNDERSTOOD"
	// ProgressRequirementsDerived: the model's proposals passed (or failed) the
	// admissibility gate and the requirement ledger is closed.
	ProgressRequirementsDerived ObjectiveProgress = "REQUIREMENTS_DERIVED"
	// ProgressReady: the completion contract is fully authored and execution may
	// begin.
	ProgressReady ObjectiveProgress = "READY"
	// ProgressInProgress: computation ran and produced boundary facts that do
	// not yet satisfy or violate the contract.
	ProgressInProgress ObjectiveProgress = "IN_PROGRESS"
	// ProgressPartiallySatisfied: at least one completion condition holds and at
	// least one does not. The objective is genuinely partially advanced.
	ProgressPartiallySatisfied ObjectiveProgress = "PARTIALLY_SATISFIED"
	// ProgressRequiresContinuation: nothing holds yet, the contract is still
	// admissible, and the runtime bounds allow another computation step.
	ProgressRequiresContinuation ObjectiveProgress = "REQUIRES_CONTINUATION"
	// ProgressBlocked: the contract cannot advance because a capability or an
	// authorization is missing. Only a human (or a grant) unblocks it.
	ProgressBlocked ObjectiveProgress = "BLOCKED"
	// ProgressProven: every completion condition holds.
	ProgressProven ObjectiveProgress = "PROVEN"
	// ProgressFailed: the evidence positively contradicts the contract.
	ProgressFailed ObjectiveProgress = "FAILED"
	// ProgressUnsubstantiated: execution terminated with the contract
	// unsatisfied and no admissible continuation left.
	ProgressUnsubstantiated ObjectiveProgress = "UNSUBSTANTIATED"
	// ProgressRequiresAuthorization: the evidence is sufficient but a human
	// gate owns the decision.
	ProgressRequiresAuthorization ObjectiveProgress = "REQUIRES_AUTHORIZATION"
)

// String returns the canonical progress label.
func (p ObjectiveProgress) String() string { return string(p) }

// Terminal reports whether the progress state settles the objective lifecycle.
func (p ObjectiveProgress) Terminal() bool {
	switch p {
	case ProgressProven, ProgressFailed, ProgressUnsubstantiated,
		ProgressRequiresAuthorization, ProgressBlocked:
		return true
	default:
		return false
	}
}

// Continues reports whether the lifecycle may take another computation step.
func (p ObjectiveProgress) Continues() bool {
	return p == ProgressInProgress || p == ProgressPartiallySatisfied ||
		p == ProgressRequiresContinuation
}

// Advanceable reports whether the objective is genuinely incomplete but still
// within reach: it has either advanced partially or is waiting for a
// continuation the bounds may grant.
func (p ObjectiveProgress) Advanceable() bool {
	return p == ProgressPartiallySatisfied || p == ProgressRequiresContinuation
}

// ── Condition reduction ─────────────────────────────────────────────────────

// ObjectiveEvidenceFacts is the subset of ObjectiveEvidence the condition
// reducer reads, expressed as predicates. Passing the concrete evidence keeps
// this reducer total and lets it be exercised without constructing a full
// bundle.
type conditionFacts struct {
	Mutated            bool
	DeltaTargets       map[string]bool
	Exists             map[string]bool
	Absent             map[string]bool
	Observations       int
	PostMutation       map[string]bool
	VerifierSatisfied  bool
	VerificationPassed bool
	VerifierSkipped    bool
	ResponseSatisfied  bool
	Discharged         map[string]bool
	ClaimedOnly        map[string]bool
}

// conditionFactsFrom projects an evidence bundle onto the condition predicates.
func conditionFactsFrom(ev ObjectiveEvidence) conditionFacts {
	return conditionFacts{
		Mutated:            ev.Mutated(),
		DeltaTargets:       toSet(ev.ObservedDeltaTargets),
		Exists:             toSetMap(ev.TargetExists),
		Absent:             toSetMap(ev.TargetAbsent),
		Observations:       ev.WorkspaceObservations,
		PostMutation:       toSet(ev.PostMutationObserved),
		VerifierSatisfied:  ev.verifierSatisfied(true),
		VerificationPassed: ev.VerificationPassed,
		VerifierSkipped:    ev.VerificationSkipped,
		ResponseSatisfied:  responseSatisfied(ev),
		Discharged:         toSet(ev.DischargedRequirements),
		ClaimedOnly:        toSet(ev.ClaimedRequirements),
	}
}

func toSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, v := range in {
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func toSetMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// ReduceConditions recomputes the satisfaction state of every completion
// condition from the observed evidence.
//
// It NEVER reads a caller-supplied status: the incoming conditions are treated
// as an obligation list only. That is what keeps the contract from becoming a
// channel through which a caller asserts its own homework.
//
// FAIL-CLOSED. A condition the runtime cannot observe satisfied is
// ConditionUnsatisfied. There is no third state that means "probably fine".
func ReduceConditions(conditions []CompletionCondition, ev ObjectiveEvidence) []CompletionCondition {
	if len(conditions) == 0 {
		return nil
	}
	facts := conditionFactsFrom(ev)
	out := make([]CompletionCondition, 0, len(conditions))
	for _, c := range conditions {
		out = append(out, reduceCondition(c, facts))
	}
	return out
}

func reduceCondition(c CompletionCondition, f conditionFacts) CompletionCondition {
	// A caller-supplied status is discarded, never trusted.
	c.Status = ConditionPending
	c.VerificationState = VerifyNone
	c.EvidenceRefs = nil

	scope := c.Targets
	switch c.Obligation {
	case ObligationScopeMutated:
		// Both facts are required: the boundary must hold a DURABLE change, and
		// every target this condition names must be among the observed deltas.
		if !f.Mutated {
			return unsatisfied(c, VerifyNone, "no durable filesystem mutation was observed")
		}
		missing := targetsMissing(scope, f.DeltaTargets)
		if len(missing) > 0 {
			return unsatisfied(c, VerifyNone,
				"no observed filesystem delta on declared target(s): "+strings.Join(missing, ", "))
		}
		refs := deltaRefs(scope, f.DeltaTargets)
		return satisfied(c, VerifyObserved, refs)

	case ObligationTargetExists:
		missing := targetsMissing(scope, f.Exists)
		if len(missing) > 0 {
			return unsatisfied(c, VerifyNone,
				"no durable existence observation for declared target(s): "+strings.Join(missing, ", "))
		}
		return satisfied(c, VerifyObserved, targetRefs(scope, "exists"))

	case ObligationScopeAbsent:
		missing := targetsMissing(scope, f.Absent)
		if len(missing) > 0 {
			return unsatisfied(c, VerifyNone,
				"no explicit absence observation for declared target(s): "+strings.Join(missing, ", "))
		}
		return satisfied(c, VerifyObserved, targetRefs(scope, "absent"))

	case ObligationObserved:
		// "Observed" means the runtime actually read the workspace — never that
		// the model was TOLD about it, and never that a target was merely named.
		//
		// Two independent observations satisfy it, because the lifecycle may
		// legitimately look either side of a mutation: the executor's
		// pre-dispatch snapshot (what the model was shown) or the control
		// plane's own post-mutation re-read (what the runtime checked). A
		// human-gated apply in particular produces the second without the first,
		// and refusing that objective would be refusing it for the shape of the
		// approval rather than for anything about the work.
		if f.Observations >= 1 {
			return satisfied(c, VerifyObserved, []string{"evidence:workspace_observations=" + itoa(f.Observations)})
		}
		if len(scope) > 0 && len(targetsMissing(scope, f.PostMutation)) == 0 {
			return satisfied(c, VerifyObserved, targetRefs(scope, "observed"))
		}
		return unsatisfied(c, VerifyNone,
			"no workspace observation event and no post-mutation re-read for "+strings.Join(scope, ", "))

	case ObligationPostMutationReinspected:
		// Requesting a target is not observing it, and a pre-dispatch snapshot is
		// not a post-mutation result. The runtime must have RE-READ the declared
		// targets after the mutation landed.
		missing := targetsMissing(scope, f.PostMutation)
		if len(missing) > 0 {
			return unsatisfied(c, VerifyNone,
				"declared target(s) never re-read after the mutation: "+strings.Join(missing, ", "))
		}
		return satisfied(c, VerifyObserved, targetRefs(scope, "reinspected"))

	case ObligationIntegrityHeld:
		if f.VerifierSkipped && !f.VerificationPassed {
			c.Status = ConditionSatisfied
			c.VerificationState = VerifyGateNotApplicable
			c.EvidenceRefs = []string{"evidence:verifier=NOT_APPLICABLE"}
			return c
		}
		if !f.VerifierSatisfied {
			state := VerifyGateFail
			if !f.VerificationPassed && !f.VerifierSkipped {
				// No gate ran at all and none was skipped: NOT_RUN, which is a
				// failure to establish integrity, never a pass.
				state = VerifyGateFail
			}
			return unsatisfied(c, state, "the verification gate reported "+ev0Verdict(f))
		}
		c.Status = ConditionSatisfied
		c.VerificationState = VerifyGatePass
		c.EvidenceRefs = []string{"evidence:verifier=PASS"}
		return c

	case ObligationResponseDelivered:
		if !f.ResponseSatisfied {
			return unsatisfied(c, VerifyNone, "no response and no deterministic structural verdict were produced")
		}
		return satisfied(c, VerifyObserved, []string{"evidence:response=delivered"})

	case ObligationRequirementDischarged:
		if c.RequirementID == "" {
			return unsatisfied(c, VerifyNone, "the condition names no requirement")
		}
		if f.Discharged[c.RequirementID] {
			return satisfied(c, VerifyObserved, []string{"evidence:requirement_discharged=" + c.RequirementID})
		}
		if f.ClaimedOnly[c.RequirementID] {
			// The model asserted this one. It is recorded as CLAIMED_ONLY and is
			// deliberately NOT satisfaction: a claim is not an observation.
			return unsatisfied(c, VerifyClaimedOnly,
				"the model claimed requirement "+c.RequirementID+" was satisfied, but the runtime observed no evidence for it")
		}
		return unsatisfied(c, VerifyNone,
			"no observed execution fact discharges requirement "+c.RequirementID)

	default:
		// An unknown obligation can never be satisfied. Fail closed.
		return unsatisfied(c, VerifyNone, "unrecognized obligation "+string(c.Obligation))
	}
}

func satisfied(c CompletionCondition, state VerificationState, refs []string) CompletionCondition {
	c.Status = ConditionSatisfied
	c.VerificationState = state
	c.EvidenceRefs = refs
	return c
}

func unsatisfied(c CompletionCondition, state VerificationState, _ string) CompletionCondition {
	c.Status = ConditionUnsatisfied
	c.VerificationState = state
	c.EvidenceRefs = nil
	return c
}

// targetsMissing returns the declared targets absent from an observed set. An
// empty declared scope is UNOBSERVED, never trivially satisfied.
func targetsMissing(scope []string, observed map[string]bool) []string {
	if len(scope) == 0 {
		return []string{"(no declared target)"}
	}
	var out []string
	for _, t := range scope {
		if t == "" || !observed[t] {
			out = append(out, t)
		}
	}
	return out
}

func deltaRefs(scope []string, deltas map[string]bool) []string {
	refs := make([]string, 0, len(scope))
	for _, t := range scope {
		if deltas[t] {
			refs = append(refs, "evidence:delta="+t)
		}
	}
	return refs
}

func targetRefs(scope []string, kind string) []string {
	refs := make([]string, 0, len(scope))
	for _, t := range scope {
		refs = append(refs, "evidence:"+kind+"="+t)
	}
	return refs
}

func ev0Verdict(f conditionFacts) string {
	switch {
	case f.VerifierSkipped:
		return "NOT_APPLICABLE"
	case f.VerificationPassed:
		return "PASS"
	default:
		return "NOT_RUN"
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// FirstUnmetCondition returns the FIRST unsatisfied condition in contract
// order. Contract order is the declaration order, so the reason a projection
// renders is always the reason the authority decided on — the two can never
// disagree.
func FirstUnmetCondition(conditions []CompletionCondition) (CompletionCondition, bool) {
	for _, c := range conditions {
		if !c.Satisfied() {
			return c, true
		}
	}
	return CompletionCondition{}, false
}

// ── Progress reduction ──────────────────────────────────────────────────────

// ProgressInput is the deterministic input set of ReduceProgress.
type ProgressInput struct {
	// Contract is the authoritative completion contract. A zero contract means
	// the lifecycle has not reached contract authoring yet.
	Contract ObjectiveContract
	// Conditions are the RECOMPUTED condition states. Callers must pass the
	// output of ReduceConditions, never the authored statuses.
	Conditions []CompletionCondition
	// Outcome is the authority's verdict when one has been reached.
	Outcome ObjectiveOutcome
	// Attempted reports that at least one computation step ran.
	Attempted bool
	// Blocked reports that a capability or authorization is missing. The
	// CONTROL PLANE owns that fact; this reducer only projects it.
	Blocked bool
	// AuthorizationPending reports that a human gate holds the mutation.
	AuthorizationPending bool
	// Truncated reports that the generation ended mid-stream
	// (finish_reason=length / a preserved partial artifact). A truncated
	// generation has NOT delivered a result, so it can never project PROVEN even
	// when every condition that could be evaluated happens to hold: the
	// conditions were computed against a prefix of an answer.
	Truncated bool
	// AttemptsUsed / MaxAttempts are the runtime-owned continuation bounds.
	AttemptsUsed int
	MaxAttempts  int
	// RecoveryUsed / MaxRecoveryCycles are the runtime-owned replanning bounds.
	RecoveryUsed      int
	MaxRecoveryCycles int
}

// ReduceProgress projects the objective's progress deterministically. Identical
// inputs always yield an identical progress state, and every branch is a
// function of observed facts — never of a model's opinion.
//
// in.Conditions MUST already be reduced by ReduceConditions over the same
// evidence the authority judged; ReduceProgressWith is the entry point that
// guarantees that. Passing authored (unreduced) statuses here would let a
// caller assert its own homework, which is precisely what the split prevents.
func ReduceProgress(in ProgressInput) ObjectiveProgress {
	// ── terminal verdicts own the projection ────────────────────────
	switch in.Outcome {
	case ObjectiveProven:
		return ProgressProven
	case ObjectiveFailed:
		return ProgressFailed
	case ObjectiveRequiresAuthorization:
		return ProgressRequiresAuthorization
	}

	if in.AuthorizationPending {
		return ProgressRequiresAuthorization
	}
	if in.Blocked {
		return ProgressBlocked
	}

	// ── lifecycle stages before any computation ─────────────────────
	if len(in.Conditions) == 0 {
		switch {
		// With no authored contract there is nothing to analyse, so the
		// authority's refusal IS the projection. Mapping it here (rather than in
		// the switch above) keeps the two cases distinct: an UNSUBSTANTIATED
		// verdict computed against a real condition set still reports its
		// progress, and only a lifecycle with no contract at all collapses to the
		// bare refusal.
		case in.Outcome != "":
			return ProgressUnsubstantiated
		case in.Attempted:
			// A lifecycle that ran without ever authoring a completion contract
			// cannot be judged. Fail closed and say exactly that.
			return ProgressUnsubstantiated
		case len(in.Contract.Requirements) > 0:
			return ProgressRequirementsDerived
		case len(in.Contract.Clauses) > 0:
			return ProgressUnderstood
		default:
			return ProgressDiscovered
		}
	}

	// A truncated generation has not delivered its result, so it cannot be
	// complete no matter how the computable conditions fall.
	if in.Truncated {
		return ProgressRequiresContinuation
	}

	satisfiedN, total := 0, len(in.Conditions)
	for _, c := range in.Conditions {
		if c.Satisfied() {
			satisfiedN++
		}
	}
	if satisfiedN == total {
		return ProgressProven
	}
	if !in.Attempted {
		// The contract is authored and nothing has run yet.
		return ProgressReady
	}
	if satisfiedN > 0 {
		return ProgressPartiallySatisfied
	}
	if in.AttemptsUsed > 0 && in.MaxAttempts > 0 && in.AttemptsUsed >= in.MaxAttempts {
		// The continuation budget is spent: the objective is genuinely
		// incomplete and no further step is admissible.
		return ProgressUnsubstantiated
	}
	if in.RecoveryUsed > 0 && in.MaxRecoveryCycles > 0 && in.RecoveryUsed >= in.MaxRecoveryCycles {
		return ProgressUnsubstantiated
	}
	return ProgressRequiresContinuation
}

// ReduceProgressWith is the reduction entry point that reads the SAME evidence
// the authority judged, so the progress state and the verdict can never
// disagree about which conditions hold.
func ReduceProgressWith(in ProgressInput, ev ObjectiveEvidence) ObjectiveProgress {
	reduced := ReduceConditions(in.Conditions, ev)
	in.Conditions = reduced
	// Truncation is read from the evidence, never from the conditions: every
	// condition that could be evaluated against a truncated generation was
	// computed against a PREFIX of an answer.
	in.Truncated = ev.Artifact == ArtifactContinuing || ev.PartialArtifact ||
		NormalizeFinishReason(ev.FinishReason) == CanonicalOutputExhausted
	if in.Outcome == "" {
		in.Outcome = (&ObjectiveCompletionAuthority{}).Evaluate(
			TaskContract{Kind: in.Contract.TaskKind, Targets: in.Contract.Scope},
			ev,
		)
	}
	return ReduceProgress(in)
}

// ── Continuation ────────────────────────────────────────────────────────────

// ContinuationDecision is the Control Plane's answer to "what may happen next
// given this objective's progress?". It is the decision the loop consumes; the
// provider's finish_reason is an INPUT to it, never its conclusion.
type ContinuationDecision string

const (
	// ContinueComputation: take another bounded computation step against the
	// SAME objective contract, preserving established evidence.
	ContinueComputation ContinuationDecision = "CONTINUE"
	// Replan: the current approach is no longer supported by the evidence. The
	// planner receives the current objective AND the current evidence state, and
	// the next computation starts from the same authoritative objective.
	Replan ContinuationDecision = "REPLAN"
	// WaitForAuthorization: a human gate owns the decision. The runtime holds
	// its state and burns no budget.
	WaitForAuthorization ContinuationDecision = "WAIT_FOR_AUTHORIZATION"
	// Unsubstantiate: the objective is incomplete and no admissible continuation
	// remains. This is a truthful terminal truth, not a success.
	Unsubstantiate ContinuationDecision = "UNSUBSTANTIATE"
	// Fail: the evidence positively contradicts the objective.
	Fail ContinuationDecision = "FAIL"
	// Complete: every completion condition holds.
	Complete ContinuationDecision = "COMPLETE"
)

// String returns the canonical continuation label.
func (c ContinuationDecision) String() string { return string(c) }

// Continuation is the full deterministic continuation verdict: WHAT to do and
// WHY, plus the unresolved work that justifies it.
type Continuation struct {
	Decision ContinuationDecision
	// Progress is the objective progress the decision was derived from.
	Progress ObjectiveProgress
	// Reason is the deterministic justification.
	Reason string
	// UnmetConditionIDs names the completion conditions that are not satisfied.
	// It is the "what remains unresolved" payload a continuation prompt, a trace
	// and a human boundary all read from one place.
	UnmetConditionIDs []string
	// UnmetRequirementIDs names the admitted requirements not yet discharged.
	UnmetRequirementIDs []string
	// Replan reports whether the planner must be re-consulted with the evidence
	// state attached (as opposed to a plain continuation of the same approach).
	Replan bool
}

// Continue reports whether the lifecycle may take another computation step.
func (c Continuation) Continue() bool {
	return c.Decision == ContinueComputation || c.Decision == Replan
}

// DecideContinuation reduces the objective's progress into the Control Plane's
// next action. It is deterministic and total.
//
// The precedence is deliberate and encodes the invariants of this runtime:
//
//	PROVEN                       → COMPLETE        (only ever from evidence)
//	REQUIRES_AUTHORIZATION       → WAIT            (the human decides)
//	FAILED                       → FAIL
//	BLOCKED                      → UNSUBSTANTIATE  (a grant is not a retry)
//	UNSUBSTANTIATED (no budget)   → UNSUBSTANTIATE  (truthful incompletion)
//	PARTIALLY_SATISFIED          → CONTINUE        (preserve what holds)
//	REQUIRES_CONTINUATION        → CONTINUE / REPLAN
func DecideContinuation(in ProgressInput, ev ObjectiveEvidence) Continuation {
	progress := ReduceProgressWith(in, ev)
	conditions := ReduceConditions(in.Conditions, ev)

	var unmetConds, unmetReqs []string
	for _, c := range conditions {
		if c.Satisfied() {
			continue
		}
		unmetConds = append(unmetConds, c.ID)
		if c.RequirementID != "" {
			unmetReqs = append(unmetReqs, c.RequirementID)
		}
	}
	c := Continuation{Progress: progress, UnmetConditionIDs: unmetConds, UnmetRequirementIDs: unmetReqs}

	switch progress {
	case ProgressProven:
		c.Decision = Complete
		c.Reason = "every completion condition is satisfied by observed evidence"
		return c
	case ProgressRequiresAuthorization:
		c.Decision = WaitForAuthorization
		c.Reason = "a human gate owns the decision; the runtime holds its state"
		return c
	case ProgressFailed:
		c.Decision = Fail
		c.Reason = "the evidence positively contradicts the objective"
		return c
	case ProgressBlocked:
		c.Decision = Unsubstantiate
		c.Reason = "the objective cannot advance: a capability or authorization is missing, which is not a retry"
		return c
	case ProgressUnsubstantiated:
		c.Decision = Unsubstantiate
		c.Reason = continuationReason(unmetConds, unmetReqs, "the objective is incomplete and no admissible continuation remains")
		return c
	case ProgressPartiallySatisfied:
		c.Decision = ContinueComputation
		c.Reason = fmt.Sprintf(
			"%d of %d completion condition(s) hold; continuing against the same objective contract, preserving established evidence",
			len(conditions)-len(unmetConds), len(conditions))
		return c
	case ProgressRequiresContinuation:
		// A lifecycle that has already produced boundary facts whose approach no
		// longer holds is a REPLAN, not another identical attempt. The signal is
		// structural — evidence was produced and the contract is still unmet —
		// never "the model said it was stuck".
		if in.Attempted && len(in.Conditions) > 0 && !anyMutationEvidence(ev) {
			c.Decision = Replan
			c.Replan = true
			c.Reason = "the previous computation produced no usable evidence; re-planning against the current objective and evidence state"
			return c
		}
		c.Decision = ContinueComputation
		c.Reason = continuationReason(unmetConds, unmetReqs, "the objective is not yet satisfied and the continuation budget allows another step")
		return c
	case ProgressInProgress:
		c.Decision = ContinueComputation
		c.Reason = "computation is in progress against the objective contract"
		return c
	default:
		c.Decision = Unsubstantiate
		c.Reason = "the objective lifecycle has not reached an adjudicable state"
		return c
	}
}

// anyMutationEvidence reports whether the evidence contains a durable mutation
// OR a completed artifact. A lifecycle that produced neither is a REPLAN
// candidate: repeating the same attempt would be a silent retry loop.
func anyMutationEvidence(ev ObjectiveEvidence) bool {
	return ev.Mutated() || ev.Artifact == ArtifactProduced || ev.WorkspaceObservations > 0
}

func continuationReason(unmetConds, unmetReqs []string, base string) string {
	switch {
	case len(unmetReqs) > 0:
		return base + "; undischarged requirement(s): " + strings.Join(unmetReqs, ", ")
	case len(unmetConds) > 0:
		return base + "; unsatisfied condition(s): " + strings.Join(unmetConds, ", ")
	default:
		return base
	}
}

// ── Trace projection ────────────────────────────────────────────────────────

// ObjectiveProgressReport is the machine-readable rendering of one objective
// lifecycle's state. It is the payload a TUI projects and a test asserts, so
// the surface can never drift from the runtime events that produced it.
type ObjectiveProgressReport struct {
	ObjectiveID  string               `json:"objective_id"`
	TaskKind     TaskKind             `json:"task_kind"`
	Semantics    ObjectiveSemantics   `json:"semantics"`
	Scope        []string             `json:"scope,omitempty"`
	Progress     ObjectiveProgress    `json:"progress"`
	Outcome      ObjectiveOutcome     `json:"outcome"`
	Continuation ContinuationDecision `json:"continuation"`

	Clauses      []ClauseProgress      `json:"clauses,omitempty"`
	Requirements []RequirementReport   `json:"requirements,omitempty"`
	Conditions   []CompletionCondition `json:"conditions,omitempty"`
	Pending      []string              `json:"pending,omitempty"`
}

// ClauseProgress is the projection of one user clause.
type ClauseProgress struct {
	ID         string     `json:"id"`
	Kind       ClauseKind `json:"kind"`
	Text       string     `json:"text"`
	Obligation bool       `json:"obligation"`
}

// RequirementReport is the projection of one requirement ledger entry,
// including rejections so an audit can see what the runtime refused and why.
type RequirementReport struct {
	ID           string            `json:"id"`
	Text         string            `json:"text"`
	Origin       RequirementOrigin `json:"origin"`
	Status       RequirementStatus `json:"status"`
	Targets      []string          `json:"targets,omitempty"`
	ClauseIDs    []string          `json:"clause_ids,omitempty"`
	RejectReason string            `json:"reject_reason,omitempty"`
	Discharged   bool              `json:"discharged"`
}

// BuildObjectiveProgressReport assembles the trace payload from the SAME
// recomputed conditions the authority judged. Nothing here re-decides anything.
func BuildObjectiveProgressReport(contract ObjectiveContract, conditions []CompletionCondition, progress ObjectiveProgress, outcome ObjectiveOutcome, next Continuation) ObjectiveProgressReport {
	report := ObjectiveProgressReport{
		ObjectiveID:  contract.ObjectiveID,
		TaskKind:     contract.TaskKind,
		Semantics:    contract.Semantics,
		Scope:        append([]string(nil), contract.Scope...),
		Progress:     progress,
		Outcome:      outcome,
		Continuation: next.Decision,
		Conditions:   conditions,
		Pending:      append([]string(nil), next.UnmetConditionIDs...),
	}
	for _, c := range contract.Clauses {
		report.Clauses = append(report.Clauses, ClauseProgress{
			ID: c.ID, Kind: c.Kind, Text: c.Text, Obligation: c.Obligation,
		})
	}
	discharged := map[string]bool{}
	for _, c := range conditions {
		if c.RequirementID != "" && c.Satisfied() {
			discharged[c.RequirementID] = true
		}
	}
	for _, r := range contract.Requirements {
		report.Requirements = append(report.Requirements, RequirementReport{
			ID: r.ID, Text: r.Text, Origin: r.Origin, Status: r.Status,
			Targets: append([]string(nil), r.Targets...), ClauseIDs: append([]string(nil), r.ClauseIDs...),
			RejectReason: r.RejectReason, Discharged: discharged[r.ID],
		})
	}
	sort.Strings(report.Pending)
	return report
}
