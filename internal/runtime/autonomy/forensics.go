package autonomy

import (
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── EXECUTION FORENSICS: MAKING THE CONTROL PLANE OBSERVABLE ────────────────
//
// This file adds NO new decision authority. Every emitter here publishes a
// transition some other component already performed. Its only job is to make
// four otherwise-invisible facts readable from the event stream:
//
//	1. Was this run authorized, and under which boundary?
//	2. What ExecutionSpec was frozen before anything was dispatched?
//	3. Why did the runtime execute another step — and did an authority rewrite
//	   the decision matrix's proposal?
//	4. Why was the objective PROVEN, or not?
//
// Plus one terminal record: the run's own totals.
//
// WHY THIS EXISTS AS A SEPARATE FILE. `loop.transition` already records the
// action and the reason string, which is why the defect looked absent from the
// outside. What it cannot express is the difference between:
//
//	deciding -> completed   reason="objective satisfied"
//
// reached by the matrix on its own, and reached because an authority found the
// evidence sufficient and appended "; objective PROVEN by evidence". Both render
// as a transition. Only one of them was ever proved. Proposed-vs-selected is the
// distinction this file exists to preserve, because it is the difference between
// "the runtime decided" and "the runtime was told it could".

// ── Authority names ─────────────────────────────────────────────────────────
//
// Stable identifiers, not prose. A consumer routes on these; a human reads them.

const (
	AuthorityAdmissionGate      = "preflight_admission_gate"
	AuthorityCompletionGate     = "objective_completion_authority"
	AuthorityContinuationRouter = "objective_continuation_router"
	AuthorityBehaviorGate       = "behavioral_completion_gate"
	AuthorityContractRecovery   = "contract_recovery_circuit_breaker"
	AuthorityContinuationLib    = "continuation_library"
	AuthorityLoop               = "runtime_loop_bounds"
)

// decisionRecord is the forensic record of ONE continuation decision: what the
// matrix proposed, which authorities inspected it, and what was finally applied.
//
// It is deliberately a value the driver fills in as the decision travels through
// the gate chain, rather than a reconstruction assembled at the end. A
// reconstruction could only report the winner; the record reports the contest.
type decisionRecord struct {
	// index is the 1-based decision ordinal within this run.
	index int
	// set reports whether a proposal has been observed and not yet settled.
	set bool
	// settled reports whether the applied decision has been published.
	settled bool

	proposed    autonomy.LoopDecision
	proposedAt  time.Time
	authorities []string

	// specPublished records that this run's frozen ExecutionSpec has already
	// been published, so a second admission pass cannot emit a second "frozen"
	// record for a spec that never changed.
	specPublished bool

	// summaryRevision counts how many terminal observations this run has published.
	//
	// A summary is emitted on EVERY return path, not once per run, because a
	// parked run is not finished: Run returns at the approval gate having
	// established almost nothing, and the resume that follows produces a
	// materially different summary. A once-per-run guard would publish the
	// parked summary and then suppress the real one — reporting the run's
	// starting state as its outcome. Each summary therefore carries its
	// revision, and the LAST summary for a run id is its outcome.
	summaryRevision int

	// ── R5 progress transition (observability only) ─────────────────────
	//
	// progress is the authoritative objective-progress classification at the
	// CURRENT decision point; previousProgress is the value at the PREVIOUS
	// one. The transition flags describe what the attempt behind this
	// decision point changed in the authoritative state. They are computed
	// from runtime-observed evidence and are NEVER consulted to decide
	// anything — they exist so a forensic reader can answer "what changed
	// between attempt N and N+1?" and "why was another attempt authorized?"
	// from the record instead of reconstructing it.
	//
	// progressDelta is the R5.1 continuation-router comparison: true when the
	// objective's authoritative progress fingerprint advanced since the last
	// continuation evaluation. It is the composite the router consumes; the
	// individual flags below describe the same transition in more detail.
	progress             string
	previousProgress     string
	newEvidence          bool
	newArtifact          bool
	mutationApplied      bool
	verificationAdvanced bool
	objectiveAdvanced    bool
	progressDelta        bool
	prevSnapshot         progressSnapshot
	havePrevSnapshot     bool
}

// progressSnapshot is the bounded, authoritative state one decision point is
// classified from. Every field is a fact the driver already owns; none is
// derived from model prose.
type progressSnapshot struct {
	progress  execution.ObjectiveProgress
	satisfied int
	mutated   bool
	artifact  bool
	verified  bool
	observed  int
}

// snapshotProgress reads the current authoritative progress state. It is a pure
// read of the same evidence the completion authority and the continuation
// router consume, so the forensic classification cannot disagree with the
// decision it describes.
func (d *Driver) snapshotProgress() progressSnapshot {
	if d == nil {
		return progressSnapshot{}
	}
	snap := progressSnapshot{
		progress: d.objectiveProgress(),
		observed: d.obs.Objective.WorkspaceObservations,
		mutated:  d.obs.Objective.Mutated(),
		artifact: d.obs.Objective.Artifact == execution.ArtifactProduced ||
			d.obs.Objective.Artifact == execution.ArtifactContinuing,
		verified: d.obs.Objective.VerificationPassed,
	}
	for _, c := range d.ObjectiveConditions() {
		if c.Satisfied() {
			snap.satisfied++
		}
	}
	return snap
}

// captureProgress folds one decision point's snapshot into the record and
// derives the deltas relative to the previous point. Called once per proposal
// (not per event) so the evaluated and selected records report identical
// facts.
func (r *decisionRecord) captureProgress(cur progressSnapshot) {
	if r == nil {
		return
	}
	r.progress = string(cur.progress)
	if !r.havePrevSnapshot {
		// The first decision point has no predecessor. Reporting a delta
		// against an empty state would invent a transition; instead the
		// previous classification is left absent and the flags state only
		// what this first observed state already contains.
		r.previousProgress = ""
		r.newEvidence = cur.observed > 0 || cur.mutated
		r.newArtifact = cur.artifact
		r.mutationApplied = cur.mutated
		r.verificationAdvanced = cur.verified
		r.objectiveAdvanced = cur.satisfied > 0
	} else {
		prev := r.prevSnapshot
		r.previousProgress = string(prev.progress)
		r.newEvidence = (cur.mutated && !prev.mutated) || cur.observed > prev.observed
		r.newArtifact = cur.artifact && !prev.artifact
		r.mutationApplied = cur.mutated
		r.verificationAdvanced = cur.verified && !prev.verified
		r.objectiveAdvanced = cur.satisfied > prev.satisfied
	}
	r.prevSnapshot = cur
	r.havePrevSnapshot = true
}

// resetProgress clears the per-run progress tracker. It is called at the start
// of every run so one objective's satisfied-condition count cannot render as a
// change against a different objective.
func (r *decisionRecord) resetProgress() {
	if r == nil {
		return
	}
	r.progress = ""
	r.previousProgress = ""
	r.newEvidence = false
	r.newArtifact = false
	r.mutationApplied = false
	r.verificationAdvanced = false
	r.objectiveAdvanced = false
	r.progressDelta = false
	r.prevSnapshot = progressSnapshot{}
	r.havePrevSnapshot = false
}

// noteAuthority records that an authority CHANGED the current proposal.
//
// Only a real change is recorded. An authority that ran, inspected, and agreed
// has not influenced anything, and naming it would make the trace claim a
// contribution the runtime never made — which is precisely the kind of invented
// causation this whole package exists to eliminate. On a `continue` decision,
// for instance, the completion authority and the behavioural gate do not engage
// at all, and the record must say so.
//
// A change of REASON counts as a change of action: the loop applied a different
// sentence than the matrix wrote, and a reader comparing the two halves needs to
// see that they differ.
func (r *decisionRecord) noteAuthority(name string, before, after autonomy.LoopDecision) {
	if r == nil || !r.set || r.settled {
		return
	}
	if before.Action == "" || before.Action != after.Action {
		return // the authority declined to engage, or changed the action itself
	}
	if before.Reason == after.Reason {
		return // ran, inspected, agreed — nothing to attribute
	}
	r.authorities = append(r.authorities, name)
}

// beginDecision opens a decision record for a fresh proposal and publishes the
// matrix's PROPOSAL — before any authority may rewrite it.
//
// Publishing the proposal separately is what makes an authority rewrite
// distinguishable from the matrix having chosen that action itself.
func (d *Driver) beginDecision(proposed autonomy.LoopDecision) {
	if d == nil || d.bus == nil {
		return
	}
	// A new proposal always supersedes an unsettled one: an unsettled record
	// means no transition followed the previous proposal, so publishing it again
	// would invent an applied decision.
	d.forensics.index++
	d.forensics.set = true
	d.forensics.settled = false
	d.forensics.proposed = proposed
	d.forensics.proposedAt = time.Now()
	d.forensics.authorities = nil
	// R5: classify the authoritative progress at THIS decision point before
	// any authority may rewrite the proposal.
	d.forensics.captureProgress(d.snapshotProgress())
	// R5.1: record whether the authoritative progress fingerprint advanced
	// since the previous continuation evaluation. Observability only; the
	// router re-reads the same pure function when it acts.
	_, d.forensics.progressDelta = d.progressFingerprintAndDelta()

	d.bus.Publish(events.NewContinuationEvaluated(d.continuationPayload(proposed, "", false)))
}

// settleDecision publishes the decision the loop ACTUALLY applied, matched
// against the recorded proposal.
//
// It is driven by the loop's own transition history rather than by the call site
// that stepped the loop, because that history is the one place every applied
// decision passes through — including the parks that bypass Driver.step
// (decomposition staging, continuation-library escalation, the admission gate).
// A transition is a real application of a decision; nothing else is.
func (d *Driver) settleDecision(t autonomy.RuntimeTransition) {
	if d == nil || d.bus == nil {
		return
	}
	if !d.forensics.set || d.forensics.settled {
		return
	}
	// Only a transition that APPLIES the proposal settles it. The loop's own
	// bookkeeping moves (Observe → Deciding, ConsumeExecution → Verifying) are
	// not decisions and must not be reported as one.
	if !appliesProposal(t.Action, d.forensics.proposed.Action) {
		return
	}
	d.forensics.settled = true

	applied := autonomy.LoopDecision{Action: t.Action, Reason: t.Reason}
	d.bus.Publish(events.NewContinuationSelected(d.continuationPayload(applied, t.To.String(), true)))
}

// appliesProposal reports whether a transition is the application of a proposal,
// as opposed to one of the loop's own state-consumption moves.
//
// The identity cases are the ones where the matrix's word and the loop's word
// are the same token and therefore uninformative; they are accepted. The
// non-identity cases (an authority downgraded a completion into an ask_human,
// the bounds terminated an otherwise-legal continue) are accepted too, because
// those ARE the transitions being reported. What must never be accepted is a
// transition whose action the proposal could never produce — which would be the
// loop deciding something on its own.
func appliesProposal(applied, proposed autonomy.LoopAction) bool {
	switch applied {
	case autonomy.LoopContinue, autonomy.LoopRetry, autonomy.LoopRepair,
		autonomy.LoopComplete, autonomy.LoopUnsubstantiate,
		autonomy.LoopAskHuman, autonomy.LoopAbort:
		return true
	default:
		return proposed == applied
	}
}

// continuationPayload assembles the typed continuation record. It reads ONLY
// runtime state: the loop's counters, the observation, and the recorded
// proposal. Nothing here is derived from model prose.
func (d *Driver) continuationPayload(decision autonomy.LoopDecision, nextState string, settled bool) events.ContinuationDecisionPayload {
	payload := events.ContinuationDecisionPayload{
		RunID:          d.runRequestID,
		Step:           d.forensics.index,
		Attempt:        d.loop.Attempts(),
		RecoveryCycle:  d.loop.RecoveryCycles(),
		Outcome:        string(d.obs.Outcome),
		FailureClass:   string(autonomy.ClassifyOutcome(d.obs.Outcome)),
		PreviousState:  d.loop.State().String(),
		NextState:      nextState,
		Targets:        append([]string(nil), d.req.Targets...),
		Evidence:       bounded(d.obs.Evidence, 200),
		ProposedAction: string(d.forensics.proposed.Action),
		ProposedReason: d.forensics.proposed.Reason,
		Authorities:    append([]string(nil), d.forensics.authorities...),
	}
	payload.SelectedAction = string(decision.Action)
	payload.SelectedReason = decision.Reason
	if settled {
		payload.Rewritten = d.forensics.proposed.Action != decision.Action
		// An authority that only sharpened the reason still intervened: the
		// loop applied a different sentence than the matrix wrote.
		if !payload.Rewritten && payload.ProposedReason != payload.SelectedReason {
			payload.Rewritten = len(payload.Authorities) > 0
		}
	} else {
		// The proposal: nothing has been selected yet.
		payload.SelectedAction = ""
		payload.SelectedReason = ""
	}
	if ev := d.objectiveEvaluation(); ev.Outcome != "" {
		payload.ObjectiveState = string(ev.Outcome)
	}
	payload.PendingWork = string(d.ObjectiveContinuation().Decision)
	// R5: the authoritative progress classification and the per-attempt
	// transition flags. These are observability only; no decision reads them.
	payload.Progress = d.forensics.progress
	payload.PreviousProgress = d.forensics.previousProgress
	payload.NewEvidence = d.forensics.newEvidence
	payload.NewArtifact = d.forensics.newArtifact
	payload.MutationApplied = d.forensics.mutationApplied
	payload.VerificationAdvanced = d.forensics.verificationAdvanced
	payload.ObjectiveAdvanced = d.forensics.objectiveAdvanced
	payload.ProgressDelta = d.forensics.progressDelta
	return payload
}

// emitAuthorization publishes the authorization verdict for this run.
//
// A REFUSAL is published as explicitly as a grant. That is the point: an absent
// event must never be the only evidence that a run was denied, because "no
// authorization event" and "authorization was never attempted" are
// indistinguishable after the process exits.
func (d *Driver) emitAuthorization(verdict, reason, reasonCode string, granted, blocked bool) {
	if d == nil || d.bus == nil {
		return
	}
	payload := events.ExecutionAuthorizedPayload{
		RunID:      d.runRequestID,
		Granted:    granted,
		Blocked:    blocked,
		Verdict:    verdict,
		Reason:     bounded(reason, 400),
		ReasonCode: reasonCode,
		// ScopeProvenanceLabel is the existing authority for the human-readable
		// directive label ($prompt / $hot / read_only). It is reused rather than
		// re-spelled: a second label vocabulary would let the forensic record
		// and the behavioral grant's own evidence disagree about which authority
		// was in force.
		Mode:    execution.ScopeProvenanceLabel(d.scopeProvenance()),
		Intent:  string(d.admissionIntent()),
		Scope:   string(d.scopeResolution.State),
		Targets: append([]string(nil), d.resolved.Targets...),
		// The DISCOVERED evidence and the PROPOSED set are published beside the
		// verdict so a refusal explains itself from its own record: a run parked
		// for disambiguation shows the candidates it observed and the proposal
		// it formed (if any), rather than an empty target list. ProposedTargets
		// is deliberately empty once the scope is RESOLVED — an authorized
		// target travels in Targets, and a proposal must never be readable as an
		// authorization.
		Candidates: append([]string(nil), d.scopeResolution.Candidates...),
		Authority:  AuthorityAdmissionGate,
	}
	if d.scopeResolution.State != ScopeResolved {
		payload.ProposedTargets = append([]string(nil), d.scopeResolution.Targets...)
	}
	if payload.Mode == "" {
		payload.Mode = string(d.resolved.Profile.Strategy)
	}
	payload.Capabilities = d.grantedCapabilities()
	d.bus.Publish(events.NewExecutionAuthorized(payload))
}

// grantedCapabilities lists the capabilities this run actually holds, read from
// the grant ledger the run itself is bound to. An empty list means the run holds
// no session grant — which is a legitimate state for a read-only objective and
// is recorded as such rather than omitted.
func (d *Driver) grantedCapabilities() []string {
	if d == nil || d.grants == nil {
		return nil
	}
	caps := d.grants.ledgerCaps()
	if len(caps) == 0 {
		return nil
	}
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, string(c))
	}
	sort.Strings(names)
	return names
}

// emitSpecFrozen publishes the ExecutionSpec committed to before dispatch.
func (d *Driver) emitSpecFrozen(spec ExecutionSpec) {
	if d == nil || d.bus == nil {
		return
	}
	payload := events.ExecutionSpecFrozenPayload{
		RunID:                 d.runRequestID,
		Intent:                string(spec.Intent),
		Strategy:              string(d.resolved.Profile.Strategy),
		InteractionContract:   string(d.activeInteraction),
		Targets:               append([]string(nil), spec.ExplicitTargets...),
		ExplicitTargets:       append([]string(nil), spec.ExplicitTargets...),
		MutationBoundary:      string(spec.MutationBoundary),
		Evidence:              string(spec.Evidence),
		ScopeState:            string(d.scopeResolution.State),
		ScopeReason:           bounded(d.scopeResolution.Reason, 300),
		ScopeTargets:          append([]string(nil), d.scopeResolution.Targets...),
		DerivationState:       string(spec.Derivation.Status),
		DerivationKinds:       append([]string(nil), spec.Derivation.Kinds...),
		DerivationCandidates:  append([]string(nil), spec.Derivation.Candidates...),
		WorkspaceDigest:       d.req.WorkspaceDigest,
		RequestedOutputTokens: d.resolved.Profile.MaxOutputTokens,
	}
	if d.activeDescriptor != nil {
		payload.ContractID = string(d.activeDescriptor.Kind)
		payload.AuthorityCeiling = string(d.activeDescriptor.AuthorityCeiling)
	}
	for _, ch := range spec.ContextChannels {
		payload.ContextChannels = append(payload.ContextChannels, ch.Kind+":"+ch.Source)
	}
	d.bus.Publish(events.NewExecutionSpecFrozen(payload))
}

// emitObjectiveEvaluated publishes the completion authority's verdict for one
// decision point: the canonical objective state, the obligation that was or was
// not discharged, and the authority that ruled.
func (d *Driver) emitObjectiveEvaluated(evaluation execution.ObjectiveEvaluation, proposed string, granted bool) {
	if d == nil || d.bus == nil {
		return
	}
	ev := d.objectiveEvidenceWithContract()
	payload := events.ObjectiveEvaluatedPayload{
		RunID:       d.runRequestID,
		State:       string(evaluation.Outcome),
		Proposed:    proposed,
		Granted:     granted,
		Reason:      bounded(evaluation.Reason, 400),
		UnmetClause: evaluation.Clause,
		Targets:     append([]string(nil), d.objectiveTargets()...),
		Mutations:   ev.MutatedFiles,
		Verified:    ev.VerificationPassed,
		Authority:   AuthorityCompletionGate,
	}
	// Requirements and behaviours are counted from the ADMITTED lifecycle, not
	// from the contract's fixed clause list: the number that matters is how many
	// obligations this run actually took on, which is a runtime fact.
	if prog := d.ObjectiveContinuation(); len(prog.UnmetRequirementIDs) > 0 {
		payload.Requirements = len(prog.UnmetRequirementIDs)
		payload.Discharged = 0
	}
	payload.BehaviorsProven = d.lastBehavior.Repairs
	d.bus.Publish(events.NewObjectiveEvaluated(payload))
}

// emitRunSummary publishes the terminal, self-contained record of this run.
//
// The counters are runtime-owned: they are read from the loop's own accounting
// and the driver's observed usage aggregation, never counted by a projection. A
// summary a projection assembles could disagree with the runtime about how many
// calls happened, which is the one number nobody can afford to be wrong about.
func (d *Driver) emitRunSummary() {
	if d == nil || d.bus == nil {
		return
	}
	d.forensics.summaryRevision++
	state := d.State()
	term := d.loop.Termination()

	payload := events.ExecutionSummaryPayload{
		RunID:          d.runRequestID,
		Revision:       d.forensics.summaryRevision,
		Objective:      bounded(d.prompt, 300),
		Status:         string(state),
		Attempts:       d.loop.Attempts(),
		RecoveryCycles: d.loop.RecoveryCycles(),
		// RuntimeSteps is the number of steps this run EXECUTED — not the
		// loop's structural bound. The bound is what the run was allowed to
		// do; this is what it did, and conflating the two is exactly how a
		// run that stopped after one call reports "10 steps".
		RuntimeSteps: d.forensicsSteps,
		InputTokens:  d.aggInput,
		OutputTokens: d.aggOutput,
		TotalTokens:  d.aggInput + d.aggOutput,
		UsageKnown:   d.aggKnown,
	}

	if term != nil {
		payload.TerminationReason = bounded(term.Reason, 500)
		payload.TerminationState = string(term.State)
	}
	if b := d.Boundary(); b != nil {
		payload.Parked = true
		payload.BoundaryAction = string(b.Action)
	}
	d.bus.Publish(events.NewExecutionSummary(payload))
}

// bounded truncates a human-readable reason so the durable audit record cannot
// be grown by an unbounded error string. Structure is preserved; volume is not.
func bounded(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
