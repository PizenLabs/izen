package autonomy

// ── Objective Lifecycle: contract, evidence binding, continuation ────────────
//
// This file is the CONTROL PLANE side of the objective contract. The domain
// types and the authority live in internal/execution; what lives here is the
// lifecycle that keeps them honest across bounded provider calls:
//
//	derive contract once → observe → bind evidence → reduce → continue or finish
//
// THE INVARIANT THIS FILE EXISTS TO ENFORCE:
//
//	"a valid mutation happened"  ≠  "the user's objective was satisfied"
//
// Before this file, a lifecycle's only completion question was the SHAPE of the
// work it happened to do (CREATE / PATCH / …). A minimal admissible patch
// satisfied every clause of that question while leaving a broad objective almost
// entirely unaddressed, and the run terminated on the strength of the mutation.
// The shape question is still asked — it is genuinely necessary — but it is now
// asked ALONGSIDE an outcome question the runtime owns:
//
//	which obligations did the runtime itself author, and has the evidence it
//	observed satisfied every one of them?
//
// THREE AUTHORITY LEVELS, KEPT APART. A user constraint is the request text,
// segmented deterministically with no model consulted. A model-derived
// requirement is a structured interpretation the model proposed: traceable,
// admissibility-gated, and discharged only by evidence. An authoritative
// completion condition is an obligation this file (and the domain reducer)
// authored; nothing the model says can mark one satisfied.
//
// NOTHING HERE IS A QUALITY SCORE. There is no minimum change size, no file-type
// rule, no domain vocabulary and no "is this good enough" judgement. The runtime
// cannot know whether a redesign is beautiful, and the honest way to express
// that is to hold the objective's OWN requirements to evidence and report
// truthfully when the evidence does not reach.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── The requirement-derivation seam ─────────────────────────────────────────

// RequirementPassFunc issues the read-only requirement-derivation request and
// returns the model's proposals. Implementations MUST be read-only and MUST NOT
// assert completion: the proposals are inputs to the admissibility gate, never
// verdicts.
type RequirementPassFunc func(ctx context.Context, objective string, scope []string) ([]execution.DerivedRequirement, error)

// WithRequirementPass wires the objective requirement derivation into the
// driver. Passing nil disables it, and a driver without it keeps exactly the
// behaviour it had: the runtime's own obligations still apply, there are simply
// no model-derived requirements to discharge.
func WithRequirementPass(f RequirementPassFunc) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		d.requirementPass = f
	}
}

// RequirementPassForExecutor binds a RequirementPassFunc to the
// RuntimeExecutor's provider. The executor remains the single authority that
// invokes the provider; the driver only supplies the objective and the resolved
// scope.
//
// resolveModel is REQUIRED. The requirement pass contributes to the completion
// contract, so the model that derives it must be the Workspace Target model the
// operator selected — exactly the model the main lane will dispatch. A pass that
// guessed its own model would make the objective contract depend on a model
// nobody authorised.
func RequirementPassForExecutor(exec *execution.RuntimeExecutor, resolveModel func() string) RequirementPassFunc {
	return func(ctx context.Context, objective string, scope []string) ([]execution.DerivedRequirement, error) {
		if exec == nil {
			return nil, fmt.Errorf("autonomy: requirement pass requires a runtime executor")
		}
		hint := ""
		if resolveModel != nil {
			hint = strings.TrimSpace(resolveModel())
		}
		raw, err := exec.InvokeRequirementPass(ctx, objective, scope, hint)
		if err != nil {
			return nil, fmt.Errorf("autonomy: requirement pass: %w", err)
		}
		proposals, err := execution.ParseProposedRequirements([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("autonomy: requirement pass: %w", err)
		}
		out := make([]execution.DerivedRequirement, 0, len(proposals))
		for _, p := range proposals {
			out = append(out, execution.DerivedRequirement{
				ID:      p.ID,
				Text:    p.Requirement,
				Origin:  execution.OriginModel,
				Targets: append([]string(nil), p.Targets...),
			})
		}
		return out, nil
	}
}

// ── Objective identity and contract derivation ──────────────────────────────

// ObjectiveIdentity returns the stable identity of the lifecycle's objective.
//
// The identity is bound to the RUN, not to the prompt alone, so the same text
// issued twice in one session produces two distinguishable contracts while every
// continuation and replanning step of ONE run keeps a single identity. That is
// what makes "requirement set survives bounded provider calls" a fact rather
// than a hope: the contract is looked up by identity and nothing rewrites it.
func (d *Driver) ObjectiveIdentity() string {
	if d == nil {
		return ""
	}
	if d.objective.ID != "" {
		return d.objective.ID
	}
	return execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: d.runRequestID,
		Request:     d.prompt,
	}).ObjectiveID
}

// ObjectiveContract returns the authoritative completion contract for the
// lifecycle. It is an exported READ of the derived contract so a trace, a test
// and a human-facing report all see the same object the authority judged; there
// is no second derivation and no way to edit it through this accessor.
func (d *Driver) ObjectiveContract() execution.ObjectiveContract { return d.objectiveContract() }

// objectiveContract derives the authoritative completion contract for the
// lifecycle. It is derived ONCE per lifecycle and cached; every later read
// returns the same object.
//
// It is never re-derived to make a completion claim succeed. That is the whole
// point of caching it: the only way the contract can change is by starting a new
// lifecycle, which means starting a new objective.
func (d *Driver) objectiveContract() execution.ObjectiveContract {
	if d == nil {
		return execution.ObjectiveContract{}
	}
	if d.objective.derived {
		return d.objective.contract
	}
	d.objective.derived = true
	d.objective.contract = execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: d.runRequestID,
		Request:     d.prompt,
		Kind:        d.taskContract().Kind,
		Scope:       d.objectiveTargets(),
		Proposals:   d.objective.proposals,
	})
	d.publishObjectiveContract()
	return d.objective.contract
}

// resetObjectiveContract clears the derived contract so a NEW lifecycle starts
// from a clean slate. Nothing about the previous objective survives: its
// requirements, its discharged set and its evidence bindings are all per-run
// state, and carrying them across objectives is exactly how stale evidence ends
// up satisfying a newer contract.
func (d *Driver) resetObjectiveContract() {
	if d == nil {
		return
	}
	d.objective = objectiveLifecycle{}
}

// ── Requirement derivation ─────────────────────────────────────────────────

// deriveObjectiveRequirements runs the read-only requirement pass exactly once
// per lifecycle and closes the requirement ledger.
//
// It is best-effort BY DESIGN and its failure modes are all truthful:
//
//	no pass wired        → zero model-derived requirements; the runtime's own
//	                      obligations still apply and still gate completion
//	pass failed          → zero requirements, recorded with the reason
//	pass returned junk   → zero requirements, recorded with the reason
//	pass returned N      → N proposals, each individually admitted or rejected
//
// A failed derivation NEVER loosens the contract. It can only leave the model
// with fewer obligations to discharge, and the runtime's own obligations —
// including the post-mutation re-inspection — are unconditional.
func (d *Driver) deriveObjectiveRequirements(ctx context.Context) {
	if d == nil {
		return
	}
	if d.requirementPass == nil || d.objective.requirementsDerived {
		d.objective.requirementsDerived = true
		return
	}
	d.objective.requirementsDerived = true

	proposals, err := d.requirementPass(ctx, d.prompt, d.objectiveTargets())
	if err != nil {
		d.objective.requirementNote = "requirement derivation unavailable: " + err.Error()
		diagnosticf("[objective] requirement derivation unavailable: %v — "+
			"the runtime's own completion obligations still gate this objective", err)
		return
	}
	d.objective.proposals = proposals
	admitted, rejected := 0, 0
	for _, p := range proposals {
		// The gate itself runs inside DeriveObjectiveContract; this loop only
		// reports the outcome it produced so the trace carries the tally.
		_ = p
	}
	diagnosticf("[objective] requirement derivation proposed %d requirement(s) for scope %v",
		len(proposals), d.objectiveTargets())
	d.objective.requirementNote = fmt.Sprintf("%d requirement(s) proposed", len(proposals))
	admitted, rejected = d.objectiveTally()
	d.publishObjectiveRequirements(admitted, rejected)
}

// objectiveTally counts the admitted/rejected split of the derived ledger.
func (d *Driver) objectiveTally() (admitted, rejected int) {
	for _, r := range d.objectiveContract().Requirements {
		if r.Admitted() {
			admitted++
			continue
		}
		rejected++
	}
	return admitted, rejected
}

// ── Evidence binding ───────────────────────────────────────────────────────

// bindPostMutationObservation records that the runtime RE-READ a declared target
// after the mutation landed.
//
// This is the observation the pre-dispatch snapshot can never provide. A
// pre-mutation read proves the model was shown the old bytes; a post-mutation
// read proves the runtime looked at what it actually produced. Without it, "the
// intended change is present in the result" is not a claim the runtime is
// entitled to make at all.
func (d *Driver) bindPostMutationObservation(targets []string) []string {
	if d == nil {
		return nil
	}
	if d.adapter == nil {
		return nil
	}
	var observed []string
	for _, t := range targets {
		if t == "" {
			continue
		}
		// A read that fails is an ABSENCE of observation, never a negative
		// observation: an unreadable result is not a passing result.
		if _, ok := d.adapter.ReadTargetFile(t); ok {
			observed = appendUniqueTarget(observed, t)
		}
	}
	return observed
}

// bindRequirementDischarge attributes observed execution facts to the admitted
// requirements they discharge.
//
// ATTRIBUTION IS RUNTIME-SIDE AND EVIDENCE-BOUND. A requirement is discharged
// only when the runtime itself observed a durable delta, or a workspace
// observation, on a target the requirement was grounded in — after the
// requirement was admitted. A model that says "all requirements satisfied"
// discharges nothing; that claim is recorded as CLAIMED_ONLY so the trace shows
// a claim carrying no evidence.
//
// The accumulation is monotonic WITHIN one objective lifecycle, which is what
// lets a multi-step objective discharge different requirements in different
// steps and still reach PROVEN at the end.
func (d *Driver) bindRequirementDischarge(contract execution.ObjectiveContract, ev *execution.ObjectiveEvidence) {
	if d == nil || ev == nil {
		return
	}
	touched := map[string]bool{}
	for _, t := range ev.ObservedDeltaTargets {
		touched[t] = true
	}
	// An objective whose contract admits no requirements has nothing to
	// discharge; recording nothing is the correct outcome, not a gap.
	for _, r := range contract.Requirements {
		if !r.Admitted() {
			continue
		}
		if _, done := d.objective.discharged[r.ID]; done {
			continue
		}
		if requirementTouched(r, touched, ev) {
			d.objective.discharged[r.ID] = true
		}
	}
}

// requirementTouched reports whether the observed facts reach a requirement's
// grounded scope.
//
// A durable delta on the requirement's own target is the strongest signal. A
// workspace observation is accepted too, because a read-only requirement ("find
// every caller of X") is discharged by looking, not by writing — but the
// observation must have happened, so a zero-observation execution can never
// discharge anything.
func requirementTouched(r execution.DerivedRequirement, touched map[string]bool, ev *execution.ObjectiveEvidence) bool {
	for _, t := range r.Targets {
		if touched[t] {
			return true
		}
	}
	if ev != nil && ev.WorkspaceObservations > 0 && len(r.Targets) > 0 {
		for _, t := range r.Targets {
			if ev.TargetExists[t] || ev.TargetAbsent[t] {
				return true
			}
		}
	}
	return false
}

func appendUniqueTarget(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// ── Evidence assembly ──────────────────────────────────────────────────────

// objectiveEvidenceWithContract assembles the evidence bundle the authority
// judges, including the objective-contract half.
//
// It deliberately returns the SAME bundle the completion gate uses, so there is
// exactly one computation of "what holds" in the runtime. A trace that read a
// different bundle from the verdict would be able to disagree with it, which is
// the failure mode this whole structure exists to remove.
func (d *Driver) objectiveEvidenceWithContract() execution.ObjectiveEvidence {
	ev := d.objectiveEvidence()
	contract := d.objectiveContract()
	if len(contract.Conditions) == 0 {
		// No contract → the objective half of the bundle stays empty, and the
		// authority reproduces its pre-contract behaviour exactly.
		return ev
	}
	ev.Conditions = contract.Conditions
	ev.PostMutationObserved = d.objective.postMutation
	ev.DischargedRequirements = sortedObjectiveKeys(d.objective.discharged)
	ev.ClaimedRequirements = sortedObjectiveKeys(d.objective.claimed)
	return ev
}

// ObjectiveProgress projects the lifecycle's progress onto the domain-neutral
// progress vocabulary. It is a pure read of runtime state and is safe to call
// before, during and after a run.
func (d *Driver) ObjectiveProgress() execution.ObjectiveProgress {
	if d == nil {
		return execution.ProgressDiscovered
	}
	return d.objectiveProgress()
}

func (d *Driver) objectiveProgress() execution.ObjectiveProgress {
	contract := d.objectiveContract()
	ev := d.objectiveEvidenceWithContract()
	attempted := d.obs.Outcome != "" || d.objective.steps > 0
	return execution.ReduceProgressWith(execution.ProgressInput{
		Contract:             contract,
		Conditions:           contract.Conditions,
		Attempted:            attempted,
		Blocked:              false,
		AuthorizationPending: ev.ApprovalPending,
		AttemptsUsed:         d.loop.Attempts(),
		MaxAttempts:          d.loop.Bounds().MaxAttempts,
		RecoveryUsed:         d.loop.RecoveryCycles(),
		MaxRecoveryCycles:    d.loop.Bounds().MaxRecoveryCycles,
	}, ev)
}

// ObjectiveContinuation is the Control Plane's deterministic answer to "what may
// happen next?". It reads the objective's own progress, never the provider's
// finish reason in isolation.
func (d *Driver) ObjectiveContinuation() execution.Continuation {
	if d == nil {
		return execution.Continuation{Decision: execution.Unsubstantiate}
	}
	contract := d.objectiveContract()
	ev := d.objectiveEvidenceWithContract()
	return execution.DecideContinuation(execution.ProgressInput{
		Contract:             contract,
		Conditions:           contract.Conditions,
		Attempted:            d.obs.Outcome != "" || d.objective.steps > 0,
		Blocked:              false,
		AuthorizationPending: ev.ApprovalPending,
		AttemptsUsed:         d.loop.Attempts(),
		MaxAttempts:          d.loop.Bounds().MaxAttempts,
		RecoveryUsed:         d.loop.RecoveryCycles(),
		MaxRecoveryCycles:    d.loop.Bounds().MaxRecoveryCycles,
	}, ev)
}

// ObjectiveConditions returns the RUNTIME-COMPUTED satisfaction state of every
// completion condition. Tests and the trace read this; nobody can write to it.
func (d *Driver) ObjectiveConditions() []execution.CompletionCondition {
	if d == nil {
		return nil
	}
	contract := d.objectiveContract()
	if len(contract.Conditions) == 0 {
		return nil
	}
	return execution.SatisfiedConditions(d.objectiveEvidenceWithContract())
}

// ObjectiveReport assembles the machine-readable projection of the lifecycle for
// telemetry and the TUI. It re-decides nothing.
func (d *Driver) ObjectiveReport() execution.ObjectiveProgressReport {
	if d == nil {
		return execution.ObjectiveProgressReport{}
	}
	contract := d.objectiveContract()
	conditions := d.ObjectiveConditions()
	next := d.ObjectiveContinuation()
	outcome := d.lastObjective.Outcome
	if outcome == "" {
		outcome = (&execution.ObjectiveCompletionAuthority{}).
			Authorize(d.taskContract(), d.objectiveEvidenceWithContract()).Outcome
	}
	return execution.BuildObjectiveProgressReport(
		contract, conditions, d.objectiveProgress(), outcome, next)
}

// ── Continuation evidence carry-over ───────────────────────────────────────

// continuationEvidence renders the objective's current state as the bounded
// evidence string a follow-up step carries.
//
// This is what makes a continuation a continuation rather than a restart: the
// next computation is told which requirements are already discharged, which are
// not, and what evidence exists — under the SAME objective contract. Work that
// already landed is never re-done, and the model is never restarted from an
// empty prompt.
//
// ORDER IS PART OF THE CONTRACT. The most actionable facts come first —
// identity, scope, unresolved requirements, unmet conditions — and the reference
// material (the derived clause set, the derivation note) comes last. A bound that
// truncates the tail therefore costs the reader context, never the reason to
// continue.
func (d *Driver) continuationEvidence() string {
	contract := d.objectiveContract()
	if len(contract.Conditions) == 0 && len(contract.Requirements) == 0 {
		return ""
	}
	conditions := d.ObjectiveConditions()
	var b strings.Builder
	fmt.Fprintf(&b, "[OBJECTIVE %s] kind=%s progress=%s\n", contract.ObjectiveID, contract.TaskKind, d.objectiveProgress())
	if len(contract.Scope) > 0 {
		fmt.Fprintf(&b, "declared scope: %s\n", strings.Join(contract.Scope, ", "))
	}
	if len(contract.Requirements) > 0 {
		b.WriteString("requirements:\n")
		for _, r := range contract.Requirements {
			switch {
			case !r.Admitted():
				fmt.Fprintf(&b, "  - %s REJECTED (%s): %s\n", r.ID, r.RejectReason, r.Text)
			case d.objective.discharged[r.ID]:
				fmt.Fprintf(&b, "  - %s DISCHARGED: %s\n", r.ID, r.Text)
			default:
				claim := ""
				if d.objective.claimed[r.ID] {
					claim = " [CLAIMED BY MODEL — no observed evidence]"
				}
				fmt.Fprintf(&b, "  - %s UNRESOLVED%s: %s\n", r.ID, claim, r.Text)
			}
		}
	}
	if len(conditions) > 0 {
		b.WriteString("completion conditions:\n")
		for _, c := range conditions {
			note := ""
			if c.Status == execution.ConditionUnsatisfied && len(c.Targets) > 0 {
				note = " (target scope: " + strings.Join(c.Targets, ", ") + ")"
			}
			fmt.Fprintf(&b, "  - %s %s%s\n", c.ID, c.Status, note)
		}
	}
	if len(d.objective.postMutation) > 0 {
		fmt.Fprintf(&b, "post-mutation re-inspected: %s\n", strings.Join(d.objective.postMutation, ", "))
	}
	if len(contract.Clauses) > 0 {
		b.WriteString("user constraints:\n")
		for _, c := range contract.Clauses {
			fmt.Fprintf(&b, "  - [%s]%s %s\n", c.Kind, obligationMark(c.Obligation), c.Text)
		}
	}
	if d.objective.requirementNote != "" {
		fmt.Fprintf(&b, "requirement derivation: %s\n", d.objective.requirementNote)
	}
	return boundContinuationEvidence(b.String())
}

func obligationMark(obligation bool) string {
	if obligation {
		return ""
	}
	return " (reference only)"
}

// MaxContinuationEvidenceBytes bounds the continuation payload. It is larger
// than the presentation bound because this string is MODEL CONTEXT for the next
// step, not a UI payload: the executor's own input budget is the real ceiling,
// and a continuation that arrives without its unresolved work is a restart in
// disguise.
const MaxContinuationEvidenceBytes = 4096

// boundContinuationEvidence caps the payload at a rune-safe boundary. It reuses
// the package's shared rune-boundary predicate so a truncated evidence string can
// never render as a replacement character.
func boundContinuationEvidence(s string) string {
	if len(s) <= MaxContinuationEvidenceBytes {
		return s
	}
	const marker = "…(truncated)"
	cut := MaxContinuationEvidenceBytes - len(marker)
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// ── Continuation routing ────────────────────────────────────────────────────

// routeObjectiveContinuation turns a REFUSED completion claim into the next
// action the objective's own progress calls for.
//
// It runs after authorizeObjectiveCompletion and can therefore only ever
// downgrade a terminal verdict into a further step — it can never create a
// completion, because it never sets LoopComplete.
//
// THE RULE IS DELIBERATELY NARROW. It re-opens the loop only on
// PARTIALLY_SATISFIED: at least one completion condition holds AND at least one
// does not. That is the one state that is positive proof the current approach
// works and simply has not been carried far enough — precisely the case where a
// minimal first patch must not end the run.
//
// Every other incompleteness keeps its terminal truth:
//
//	nothing holds yet   → UNSUBSTANTIATED  (the first step did not land; the
//	                                       recovery matrix owns retry/repair,
//	                                       and inventing a second opinion here
//	                                       would be a silent retry loop)
//	bounds exhausted    → UNSUBSTANTIATED  (truthful incompletion)
//	blocked             → UNSUBSTANTIATED  (a grant is not a retry)
//
// The rule is a function of OBSERVED condition states only. There is no
// threshold, no score and no notion of "enough" — one satisfied obligation out
// of two is as much a partial result as nine out of ten.
func (d *Driver) routeObjectiveContinuation(decision *autonomy.LoopDecision) {
	if d == nil || decision == nil {
		return
	}
	// Only a refused completion is in scope. Every other action (retry, repair,
	// ask_human, abort) was chosen by the recovery matrix or a gate on its own
	// evidence and must pass through untouched.
	if decision.Action != autonomy.LoopUnsubstantiate {
		return
	}
	// A human-gated step is a DECISION POINT, not a bounded computation step.
	// The runtime does not get to re-dispatch against a proposal a human already
	// ruled on — regenerating it would answer a question the human answered.
	// This preserves the pre-existing invariant that an approved mutation never
	// auto-repairs itself into a second provider invocation.
	if d.objective.humanGated {
		return
	}
	next := d.ObjectiveContinuation()
	d.publishObjectiveProgress()
	if next.Progress != execution.ProgressPartiallySatisfied {
		return
	}
	switch next.Decision {
	case execution.ContinueComputation:
		decision.Action = autonomy.LoopContinue
		decision.Reason = "objective PARTIALLY_SATISFIED — " + next.Reason
		d.carryObjectiveForward()
	case execution.Replan:
		decision.Action = autonomy.LoopRepair
		decision.Reason = "objective PARTIALLY_SATISFIED, current approach unsupported by the evidence — " +
			next.Reason
		d.carryObjectiveForward()
	}
}

// markHumanGated records that the current observation came back through a human
// gate, so the continuation router knows the next step is a human decision's
// consequence rather than a bounded computation's.
func (d *Driver) markHumanGated() {
	if d == nil {
		return
	}
	d.objective.humanGated = true
}

// carryObjectiveForward attaches the objective's current state to the request
// the next step will dispatch.
//
// This is what makes a continuation a continuation and not a restart. The next
// computation runs under the SAME objective identity and contract, is told which
// requirements are already discharged and which are not, and is handed the
// evidence that exists — so established work is neither lost nor repeated.
func (d *Driver) carryObjectiveForward() {
	if d == nil {
		return
	}
	evidence := d.continuationEvidence()
	if evidence == "" {
		return
	}
	d.req.Evidence = joinEvidence(d.req.Evidence, evidence)
}

// ── Recovery brief ─────────────────────────────────────────────────────────

// recoveryBrief renders what the previous attempt FAILED at, in the runtime's
// own classified vocabulary, together with the authoritative scope and the
// failure ledger.
//
// It fills the gap that made recovery indistinguishable from a restart. The
// continuation evidence already carried the objective identity, the contract and
// the discharged/unresolved requirements; what it did NOT carry was the reason
// the last attempt did not work, so the next planner invocation had no way to
// avoid repeating an approach the runtime had already proven unsupported.
//
// Everything here is derived from RUNTIME state: the structured classification,
// the objective lifecycle and the failure ledger. Nothing is inferred from
// model prose, and nothing is invented when no failure was recorded.
func (d *Driver) recoveryBrief() string {
	if d == nil {
		return ""
	}
	verdict := d.lastRecovery
	failure := verdict.Failure
	if failure.Class == execution.FailureNone {
		failure = ClassifyObservation(d.obs)
	}
	if failure.Class == execution.FailureNone && len(d.failures.entriesOrNil()) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[FAILED APPROACH] class=%s policy=%s retry_admissible=%t\n",
		failure.Class, failure.Class.RetryPolicy(), failure.RetryAdmissible())
	if failure.Target != "" {
		fmt.Fprintf(&b, "  requested target: %s (this is the EXACT name requested; it is not a substitute)\n", failure.Target)
	}
	if failure.Evidence != "" {
		fmt.Fprintf(&b, "  evidence: %s\n", failure.Evidence)
	}
	fmt.Fprintf(&b, "  provider_state=%s artifact_state=%s finish_reason=%s\n",
		failure.ProviderState, failure.ArtifactState, orNotCarried(failure.FinishReason))
	fmt.Fprintf(&b, "  authoritative scope (unchanged; do not substitute a target): [%s]\n",
		strings.Join(d.authoritativeScope(), ","))
	if entries := d.failures.LedgerReport(); len(entries) > 0 {
		b.WriteString("  previously observed failures in this objective:\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "    - %s\n", e)
		}
	}
	return boundContinuationEvidence(b.String())
}

// orNotCarried renders an absent scalar explicitly rather than as an empty
// string, so a reader can tell "not reported" from "reported as nothing".
func orNotCarried(v string) string {
	if strings.TrimSpace(v) == "" {
		return NotCarried
	}
	return v
}

// ── Telemetry ──────────────────────────────────────────────────────────────

// publishObjectiveContract emits the structured OBJECTIVE event. It is emitted
// ONCE per lifecycle, before any execution, so the trace carries the contract
// the run will be judged against rather than a contract reconstructed after the
// fact.
func (d *Driver) publishObjectiveContract() {
	if d == nil || d.bus == nil {
		return
	}
	contract := d.objective.contract
	clauses := make([]string, 0, len(contract.Clauses))
	for _, c := range contract.Clauses {
		clauses = append(clauses, fmt.Sprintf("%s:%s:%s", c.ID, c.Kind, c.Text))
	}
	conditions := make([]string, 0, len(contract.Conditions))
	for _, c := range contract.Conditions {
		conditions = append(conditions, c.ID)
	}
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[objective] id=%s kind=%s progress=%s scope=[%s] clauses=%d conditions=%d",
		contract.ObjectiveID, contract.TaskKind, execution.ProgressUnderstood,
		strings.Join(contract.Scope, ","), len(contract.Clauses), len(contract.Conditions))))
	_ = clauses
	_ = conditions
}

// publishObjectiveRequirements emits the REQUIREMENTS event with the
// admissibility tally, so a rejected requirement is visible rather than
// silently absent.
func (d *Driver) publishObjectiveRequirements(admitted, rejected int) {
	if d == nil || d.bus == nil {
		return
	}
	contract := d.objective.contract
	var parts []string
	for _, r := range contract.Requirements {
		switch {
		case !r.Admitted():
			parts = append(parts, fmt.Sprintf("%s=REJECTED", r.ID))
		default:
			parts = append(parts, fmt.Sprintf("%s=ADMITTED", r.ID))
		}
	}
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[objective] requirements admitted=%d rejected=%d %s",
		admitted, rejected, strings.Join(parts, " "))))
}

// publishObjectiveProgress emits the PROGRESS event after every evaluation. It
// reports what the runtime OBSERVED (satisfied / unresolved), so a projection
// can never render a checklist the runtime did not actually produce.
func (d *Driver) publishObjectiveProgress() {
	if d == nil || d.bus == nil {
		return
	}
	conditions := d.ObjectiveConditions()
	var satisfied, unresolved []string
	for _, c := range conditions {
		if c.Satisfied() {
			satisfied = append(satisfied, c.ID)
			continue
		}
		unresolved = append(unresolved, c.ID)
	}
	next := d.ObjectiveContinuation()
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[objective] progress=%s continuation=%s satisfied=[%s] unresolved=[%s]",
		next.Progress, next.Decision,
		strings.Join(satisfied, ","), strings.Join(unresolved, ","))))
	if next.Reason != "" {
		d.bus.Publish(events.NewActivity("[objective] " + next.Reason))
	}
}

func sortedObjectiveKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── The lifecycle state itself ─────────────────────────────────────────────

// objectiveLifecycle is the per-run objective state. It is deliberately SEPARATE
// from the Driver's other fields and is reset wholesale at the start of every
// run, because every field in it is objective-scoped: mixing it with the loop's
// cross-run state is how one objective's discharged requirements end up
// satisfying another's completion contract.
type objectiveLifecycle struct {
	// contract is the derived, cached completion contract.
	contract execution.ObjectiveContract
	// derived latches the one-shot derivation.
	derived bool
	// ID is the objective identity.
	ID string

	// requirementPass proposals, verbatim, before the admissibility gate.
	proposals []execution.DerivedRequirement
	// requirementsDerived latches the one-shot requirement pass.
	requirementsDerived bool
	// requirementNote is the bounded note describing how derivation went.
	requirementNote string

	// discharged is the runtime-attributed requirement→evidence map.
	discharged map[string]bool
	// claimed is the model-claimed-but-unevidenced set.
	claimed map[string]bool

	// postMutation is the set of declared targets the runtime re-read AFTER the
	// mutation landed.
	postMutation []string
	// steps counts computation steps executed under this objective.
	steps int
	// humanGated records that the current observation came back through a human
	// boundary. Continuation routing stands down while it is set: a human-gated
	// step is a decision, not a bounded computation the runtime may extend.
	humanGated bool
}

func (d *Driver) objectiveState() *objectiveLifecycle {
	if d.objective.discharged == nil {
		d.objective.discharged = map[string]bool{}
	}
	if d.objective.claimed == nil {
		d.objective.claimed = map[string]bool{}
	}
	return &d.objective
}

// bindStepEvidence folds one finished execution into the objective lifecycle.
//
// It does three things, in the order the objective lifecycle demands:
//
//  1. RE-READ the declared targets AFTER the mutation landed. This is the
//     observation a pre-dispatch snapshot cannot provide, and it is what makes
//     "inspect the result" a structural step rather than an aspiration.
//  2. Attribute the step's durable evidence to the requirements it reaches.
//  3. Latch the fact that a computation step ran under this objective, so
//     progress reporting can distinguish "nothing has been attempted yet" from
//     "something was attempted and did not reach the contract".
func (d *Driver) bindStepEvidence() {
	if d == nil {
		return
	}
	state := d.objectiveState()
	state.steps++

	// Re-inspection is recorded for whatever the runtime can meaningfully read
	// back: the targets the mutation boundary reported a delta for, or the
	// declared scope when it reported a durable change without per-file detail.
	//
	// It is deliberately NOT gated on the mutation having succeeded. The
	// obligation is authored per condition, and a lifecycle that mutated nothing
	// fails `SCOPE_MUTATED` regardless of what else it recorded — so reading the
	// current state after an unsuccessful attempt adds information and cannot
	// manufacture a completion. Gating it on success instead would mean an
	// approved mutation whose observation did not carry the boundary detail left
	// the objective permanently incomplete for the want of a single read.
	observed := d.bindPostMutationObservation(d.observedDeltaTargets())
	for _, t := range observed {
		state.postMutation = appendUniqueTarget(state.postMutation, t)
	}
	if len(observed) > 0 && d.bus != nil {
		d.bus.Publish(events.NewActivity(
			"[objective] result re-inspected: " + strings.Join(observed, ", ")))
	}

	// A durable mutation is the ONE thing that genuinely changes the evidence
	// state: the workspace the next attempt reads is not the workspace the last
	// one failed against. Opening a new ledger epoch here is what lets an
	// otherwise-identical request be re-tried legitimately AFTER a change, while
	// keeping repeats under UNCHANGED evidence non-progressing.
	//
	// It is gated on the runtime having OBSERVED a post-mutation state, not on a
	// claim: an unobserved mutation must not manufacture permission to retry.
	if len(observed) > 0 {
		d.failures.AdvanceEvidence()
	}

	ev := d.obs.Objective
	d.bindRequirementDischarge(d.objectiveContract(), &ev)
}

// observedDeltaTargets returns the durable target set the mutation boundary
// actually reported a delta for, falling back to the declared scope when the
// boundary recorded a durable change without per-file detail.
//
// The fallback is the declared scope rather than "every file in the workspace":
// re-reading more than what the lifecycle touched would let an unrelated file's
// presence masquerade as an inspected result.
func (d *Driver) observedDeltaTargets() []string {
	if d == nil {
		return nil
	}
	if deltas := uniqueNonEmpty(d.obs.Objective.ObservedDeltaTargets); len(deltas) > 0 {
		return deltas
	}
	if d.obs.Objective.MutatedFiles > 0 || d.obs.Objective.Mutation.Durable() {
		return d.objectiveTargets()
	}
	return nil
}

// intentAxes names the four intent-shaped axes one lifecycle carries.
//
// §14 AUDIT FINDING. A single execution legitimately reports "modification",
// "ask" and "build" at once, and a trace that printed them without labels made
// it look as though the runtime were mutating one semantic intent into another.
// It is not. They are four distinct, independently-owned concepts:
//
//	UserIntent          autonomy.Intent     what the USER wants, classified
//	                                         from the request text
//	CommandMode         modes.Mode          which command SURFACE the user
//	                                         invoked (ask / plan / build / …)
//	InteractionContract protocol.…         the wire contract this step speaks
//	ExecutionIntent     execution.TaskKind  what THIS lifecycle's evidence must
//	                                         prove before completion is allowed
//
// The first is a goal, the second is an entry point, the third is a protocol and
// the fourth is an obligation. They are NOT merged — merging them to make a
// trace prettier would destroy the very distinction the runtime relies on:
//
//	Intent ≠ Authorization ≠ Grant ≠ Execution ≠ Evidence ≠ Verification
//
// Where the driver cannot see an axis (the command mode is chosen by the UI
// composition root and is not carried into the loop), it reports
// (not-carried) rather than substituting a neighbouring axis. A missing value is
// visible; a plausible wrong one is not.
// NotCarried is the explicit representation for an axis the runtime genuinely
// does not have.
//
// It exists because "unknown" is ambiguous: it reads as "the runtime looked and
// could not determine this", when the truth in several cases is "this concept is
// not carried into this layer at all". A missing value must be VISIBLE as a
// missing value; substituting a neighbouring field is how a trace ends up
// asserting something the runtime never established.
const NotCarried = "(not-carried)"

// intentAxes renders the four intent-shaped axes, using the AUTHORITATIVE owner
// for each and an explicit not-carried marker where the runtime has none.
//
// The two defects this replaces:
//
//   - user_intent was read from `d.obs.Intent`, which `contextObservation` fills
//     with `autonomy.ParseIntent(d.prompt)`. `ParseIntent` maps canonical LABELS,
//     not prompt text, so it returned IntentUnknown for every real objective and
//     the axis rendered `unknown` while the run was provably a modification.
//   - scope was read from `d.req.Scope`, which `Run` never assigns, even though
//     the authoritative resolved scope was already established and published by
//     `noteScopeTransition`.
func (d *Driver) intentAxes() (userIntent, commandMode, interaction, executionIntent string) {
	if d == nil {
		return "(none)", NotCarried, "(none)", "(none)"
	}
	// The canonical intent AUTHORITY is the lifecycle's own, not a re-parse of
	// the prompt: it already reflects the gateway's revision, which a re-parse
	// would silently undo.
	if d.intents != nil {
		if canonical, err := d.intents.Current(); err == nil && strings.TrimSpace(string(canonical)) != "" {
			userIntent = string(canonical)
		}
	}
	if userIntent == "" {
		// Fall back to the deterministic classifier, which reads the prompt text
		// correctly. This is a real classification, not a placeholder.
		if classified := autonomy.Classify(d.prompt, nil).Intent; classified != autonomy.IntentUnknown {
			userIntent = classified.String()
		}
	}
	if userIntent == "" {
		userIntent = NotCarried
	}

	// The command SURFACE is chosen by the UI composition root and is not
	// carried into the loop. It is reported as not-carried rather than guessed
	// from a neighbouring axis.
	commandMode = d.subcommand
	if strings.TrimSpace(commandMode) == "" {
		commandMode = NotCarried
	}

	interaction = d.activeInteraction.String()
	if strings.TrimSpace(interaction) == "" {
		interaction = "(none)"
	}
	executionIntent = string(d.taskContract().Kind)
	if strings.TrimSpace(executionIntent) == "" {
		executionIntent = NotCarried
	}
	return userIntent, commandMode, interaction, executionIntent
}

// publishIntentAxes emits the explicit four-axis intent record on the bus so a
// trace shows four concepts rather than four interchangeable words. It is
// emitted once per lifecycle, alongside the objective contract.
//
// It also carries the AUTHORITATIVE scope resolution — state and targets — so a
// reader never has to correlate this line with an earlier one to learn that the
// scope was resolved.
func (d *Driver) publishIntentAxes() {
	if d == nil || d.bus == nil {
		return
	}
	user, mode, interaction, exec := d.intentAxes()
	d.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[intent] user_intent=%s command_mode=%s interaction_contract=%s execution_intent=%s objective=%s scope_state=%s scope=[%s]",
		user, mode, interaction, exec, d.ObjectiveIdentity(),
		d.scopeResolution.State, strings.Join(d.authoritativeScope(), ","))))
}
