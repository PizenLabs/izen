// ── Driver ↔ Objective Completion Authority (Phase 14) ──────────────────────
//
// This file is the SEAM between the bounded loop and the objective authority.
// Its whole job is to make one transition structurally impossible:
//
//	interpreting --(outcome == changed)--> completed
//
// which the legacy decision matrix allowed on the strength of a single
// execution outcome. The authority replaces that shortcut with an evidence
// evaluation:
//
//	interpreting --(PROVEN)--> completed
//	interpreting --(UNSUBSTANTIATED)--> unsubstantiated
//	interpreting --(FAILED)--> aborted
//	interpreting --(REQUIRES_AUTHORIZATION)--> awaiting_human
//
// DEPENDENCY DIRECTION. This package imports internal/execution (domain) and
// internal/autonomy (loop contract). It never imports internal/ui or
// internal/presentation: the TUI projects the terminal state, it never decides
// it. The whole seam is therefore exercisable headless — `go test
// ./internal/runtime/autonomy/...` needs no terminal.
//
// LIFECYCLE OWNERSHIP. The driver owns ONE canonical TaskContract per execution
// lifecycle. It is derived from facts the runtime already holds (the classified
// intent, the resolved targets, the dispatched artifact shape, the
// pre-execution target state and the executor's structural NO-OP verdict) and
// is re-derived ONLY when a new execution lifecycle begins. It is never
// re-derived to make a completion claim succeed.
package autonomy

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// WithObjectiveAuthority overrides the completion authority. The default is the
// canonical reducer; the option exists so a test can prove the seam consults an
// authority rather than re-deriving a verdict locally.
func WithObjectiveAuthority(a ObjectiveAuthority) Option {
	return func(d *Driver) {
		if a != nil {
			d.objectiveAuthority = a
		}
	}
}

// ObjectiveAuthority is the driver-side port of the completion authority. It is
// the ONLY way the loop learns that an objective was proven; the driver has no
// second completion path.
type ObjectiveAuthority interface {
	Authorize(contract execution.TaskContract, ev execution.ObjectiveEvidence) execution.ObjectiveEvaluation
}

// objectiveReducer is the default ObjectiveAuthority: the pure domain reducer
// over (TaskContract, ObjectiveEvidence).
type objectiveReducer struct{}

func (objectiveReducer) Authorize(contract execution.TaskContract, ev execution.ObjectiveEvidence) execution.ObjectiveEvaluation {
	return execution.AuthorizeObjective(contract, ev)
}

// objectiveTargets returns the canonical declared target set of the current
// lifecycle: the active request's targets, falling back to the resolved set.
// A completion claim is always judged against a DECLARED target set — an
// undeclared change can never substitute for a declared one.
func (d *Driver) objectiveTargets() []string {
	if d == nil {
		return nil
	}
	if targets := uniqueNonEmpty(d.req.Targets); len(targets) > 0 {
		return targets
	}
	if d.req.Target != "" {
		return []string{d.req.Target}
	}
	return uniqueNonEmpty(d.resolved.Targets)
}

// changedTargetSet projects the observed filesystem deltas onto the
// target→changed shape the contract derivation consumes.
func changedTargetSet(observed []string) map[string]bool {
	out := make(map[string]bool, len(observed))
	for _, t := range observed {
		if t != "" {
			out[t] = true
		}
	}
	return out
}

// capturePreExecutionTargets records the durable target state BEFORE the first
// dispatch of a lifecycle. It is the only admissible evidence for "the
// objective was already satisfied": a post-hoc reading cannot distinguish
// "already done" from "done by this run".
func (d *Driver) capturePreExecutionTargets() {
	if d == nil || d.adapter == nil {
		return
	}
	targets := d.objectiveTargets()
	if len(targets) == 0 {
		d.preTargets = nil
		return
	}
	d.preTargets = d.adapter.TargetExistence(targets)
}

// allTargetsExistedBefore reports whether every declared target was present
// before the lifecycle began.
func (d *Driver) allTargetsExistedBefore(targets []string) bool {
	if d == nil || len(d.preTargets) == 0 || len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if !d.preTargets[t] {
			return false
		}
	}
	return true
}

// taskContract derives the ONE canonical TaskContract of the current lifecycle.
//
// The intent that decides the contract is the CANONICAL resolved intent, not the
// raw provider outcome: an execution that reported `changed` does not get to
// re-classify itself as a CREATE merely because it created a file.
func (d *Driver) taskContract() execution.TaskContract {
	if d == nil {
		return execution.TaskContract{}
	}
	targets := d.objectiveTargets()
	ev := d.obs.Objective
	intent := string(d.obs.Intent)
	if intent == "" {
		intent = string(autonomy.Classify(d.prompt, nil).Intent)
	}
	requiresMutation := autonomy.Classify(d.prompt, nil).RequiresMutation()
	// A lifecycle that carries an explicit mutation-strategy contract is
	// mutation authority evidence regardless of how the wording classified: the
	// executor was dispatched under a mutation contract, so the objective's
	// evidence clauses must be the mutation ones. The DISPATCHED ARTIFACT SHAPE
	// is deliberately NOT used as that signal — a read-only objective that
	// decomposed into sub-tasks still dispatches through the mutation lane, and
	// treating that as mutation authority would demand a filesystem change the
	// user never asked for.
	if !requiresMutation && strings.TrimSpace(d.req.MutationStrategy) != "" {
		requiresMutation = true
	}
	return execution.DeriveTaskContract(execution.TaskClassification{
		Intent:                  intent,
		Objective:               d.prompt,
		RequiresMutation:        requiresMutation,
		Targets:                 targets,
		TargetsExistedBefore:    d.preTargets,
		TargetsChanged:          changedTargetSet(ev.ObservedDeltaTargets),
		ArtifactShape:           d.obs.ArtifactShape,
		StructuralNoOpConfirmed: ev.StructuralNoOpConfirmed,
	})
}

// objectiveEvaluation is the cached verdict of the last authorization check.
// It is exposed for tests and for the structured execution-failure emission;
// it is never consulted to decide anything.
func (d *Driver) objectiveEvaluation() execution.ObjectiveEvaluation {
	if d == nil {
		return execution.ObjectiveEvaluation{}
	}
	return d.lastObjective
}

// authorizeObjectiveCompletion is the GATE every proposed completion passes
// through. It is deliberately a MUTATION of the decision, not a boolean veto:
// a refused claim is rewritten into the terminal state that matches WHY it was
// refused, so the loop history records the true transition rather than a
// complete that silently did not happen.
//
// The proposed reason is preserved and the authority's own clause is appended,
// so the recorded transition names both the execution outcome the matrix saw
// and the obligation the evidence failed.
func (d *Driver) authorizeObjectiveCompletion(decision *autonomy.LoopDecision) {
	if d == nil || decision == nil || decision.Action != autonomy.LoopComplete {
		return
	}
	contract := d.taskContract()
	// The evidence bundle the authority judges is the SAME bundle the objective
	// progress reducer and the trace read. One computation of "what holds",
	// consulted from three places — a projection that could disagree with the
	// verdict would reintroduce exactly the ambiguity this seam removes.
	evidence := d.objectiveEvidenceWithContract()
	evaluation := d.objectiveAuthority.Authorize(contract, evidence)
	d.lastObjective = evaluation
	d.lastContract = contract

	if evaluation.Outcome.Proves() {
		// The authority proved the objective: the workspace work it
		// authorized is committed, and that is a truth boundary the journal
		// must hold before the loop is allowed to move on.
		d.ledgerExecutionCommitted()
		decision.Reason = strings.TrimSpace(decision.Reason + "; objective PROVEN by evidence")
		d.emitObjectiveEvaluated(evaluation, string(autonomy.LoopComplete), true)
		return
	}

	// Publish the refusal as infrastructure telemetry BEFORE the transition, so
	// the trace overlay records the reason the run stopped even though the
	// narrative surface must not carry it.
	d.emitObjectiveUnsubstantiated(contract, evaluation)

	reason := fmt.Sprintf("objective UNPROVEN (%s): %s — refused completion claim over outcome %q",
		evaluation.Outcome, evaluation.Reason, string(d.obs.Outcome))
	// The refusal is published BEFORE the rewrite so the record carries the
	// verdict the authority reached, not merely the action it downgraded to.
	d.emitObjectiveEvaluated(evaluation, string(autonomy.LoopComplete), false)
	switch evaluation.Outcome {
	case execution.ObjectiveFailed:
		decision.Action = autonomy.LoopAbort
		decision.Reason = reason
	case execution.ObjectiveRequiresAuthorization:
		decision.Action = autonomy.LoopAskHuman
		decision.PatchID = d.obs.PatchID
		decision.Reason = reason
	default:
		decision.Action = autonomy.LoopUnsubstantiate
		decision.Reason = reason
	}
}

// authorizeBehavioralCompletion gates a proposed completion on REAL behavioral
// evidence.
//
// It is a MUTATION of the decision, never a source of completion, and that
// ordering is the whole point:
//
//   - it runs after authorizeObjectiveCompletion, so it can only downgrade;
//   - it runs the behavioral stage, which OBSERVES the workspace's real runtime
//     (serve → readiness → fetch → probe subresources → structural audit) and
//     drives evidence-driven repair through the same execution authority;
//   - PROVEN is set only from a real observation in which every observed
//     requirement held.
//
// When the objective does not demand behavioral proof, or no stage is wired, the
// decision is returned untouched — so a read-only objective, and every existing
// caller, keeps exactly the behaviour it had.
func (d *Driver) authorizeBehavioralCompletion(ctx context.Context, decision *autonomy.LoopDecision) {
	if d == nil || decision == nil || decision.Action != autonomy.LoopComplete {
		return
	}
	if d.behavior == nil || !BehaviorRequired(d.prompt) {
		return
	}

	result := d.behavior.Stage(ctx, d.prompt, d.scopeProvenance())
	d.lastBehavior = result
	if result.Proven {
		decision.Reason = strings.TrimSpace(decision.Reason +
			"; behavioral requirements PROVEN by runtime observation (" +
			strconv.Itoa(result.Repairs) + " repair(s) applied, evidence: " +
			boundedReason(result.EvidenceLine) + ")")
		return
	}

	// The objective demanded a verifiable result and the workspace could not
	// prove one. Route it the way the truthful outcome demands rather than
	// completing on a mutation alone.
	reason := "objective BEHAVIORALLY UNPROVEN: " + behavioralStopReason(result)
	d.emitBehaviorUnproven(reason)
	switch {
	case result.Block != nil && result.Block.Class == capability.FailureAuthorizationBlocked:
		decision.Action = autonomy.LoopAskHuman
		decision.PatchID = ""
		decision.Reason = reason
	case result.Block != nil:
		// A capability that could not run is a hard block, not a retry: the fix
		// is an authorization or capability change, never another attempt.
		decision.Action = autonomy.LoopUnsubstantiate
		decision.PatchID = ""
		decision.Reason = reason
	default:
		// The workspace ran and was observed defective after bounded repair
		// rounds. Park for a human rather than spin: the evidence is retained and
		// the decision belongs to the operator.
		decision.Action = autonomy.LoopAskHuman
		decision.PatchID = ""
		decision.Reason = reason
	}
}

// scopeProvenance returns the scope provenance this run executes under. The
// driver records it on dispatch; a run that never dispatched one is read-only.
func (d *Driver) scopeProvenance() domain.ScopeProvenance {
	if d == nil || d.req.Scope == "" {
		return domain.ScopeNone
	}
	if strings.EqualFold(strings.TrimSpace(d.req.Scope), "$prompt") {
		return domain.ScopeDynamic
	}
	if strings.EqualFold(strings.TrimSpace(d.req.Scope), "$hot") {
		return domain.ScopeDeclared
	}
	return domain.ScopeNone
}

// emitBehaviorUnproven records the behavioral refusal as infrastructure
// telemetry so the trace retains WHY the run stopped.
func (d *Driver) emitBehaviorUnproven(reason string) {
	if d == nil || d.bus == nil {
		return
	}
	d.bus.Publish(events.NewActivity("[behavior] " + reason))
}

// behavioralStopReason renders the attributable reason for an unproven
// behavioral result, preferring the capability block's class over a defect
// summary because the class is what tells an operator what to change.
func behavioralStopReason(r BehaviorResult) string {
	if r.Block != nil {
		return r.Block.Error()
	}
	if r.DefectLine != "" {
		return r.DefectLine
	}
	return "the workspace could not be observed to satisfy the objective"
}

// boundedReason caps a reason string so a long evidence log can never become an
// unbounded UI payload.
// boundedReasonCaps the total size of a bounded reason, marker included, so the
// bound is the number a caller can actually rely on.
const boundedReasonCaps = 600

func boundedReason(s string) string {
	if len(s) <= boundedReasonCaps {
		return s
	}
	const marker = "…"
	cut := boundedReasonCaps - len(marker)
	// Do not split a UTF-8 rune: a half-rune in an evidence string renders as a
	// replacement character in the trace, which looks like a corrupt payload.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// objectiveEvidence assembles the evidence bundle the authority judges. The
// observation's sealed bundle is authoritative; the driver only adds the
// lifecycle-level facts it alone owns.
func (d *Driver) objectiveEvidence() execution.ObjectiveEvidence {
	if d == nil {
		return execution.ObjectiveEvidence{}
	}
	ev := d.obs.Objective
	targets := d.objectiveTargets()
	// The IDEMPOTENT precondition is a PRE-EXECUTION observation. The driver
	// never asserts it from post-hoc evidence; only the executor's
	// deterministic structural NO-OP verdict can license the contract, and it
	// already travels on the bundle.
	ev.PreconditionSatisfied = ev.PreconditionSatisfied && d.allTargetsExistedBefore(targets)
	if ev.TargetExists == nil {
		ev.TargetExists = map[string]bool{}
	}
	if ev.TargetAbsent == nil {
		ev.TargetAbsent = map[string]bool{}
	}
	// A target with no observation at all is UNOBSERVED, never absent. The
	// DELETE clause reads TargetAbsent, so an unobserved target can never
	// satisfy it by default.
	for _, t := range targets {
		if !ev.TargetExists[t] && !ev.TargetAbsent[t] {
			ev.TargetAbsent[t] = false
		}
	}
	return ev
}

// uniqueNonEmpty returns the de-duplicated non-empty entries of a slice,
// preserving first-appearance order.
func uniqueNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ── Blocking Intent Revision (Phase 14) ─────────────────────────────────────
//
// The driver owns the ONE canonical intent of a lifecycle. When the
// deterministic strategy gateway selects a MUTATION contract for an objective
// the text classifier read as read-only, the lifecycle is split-brained: the
// preflight classification says "ask", the executor is about to dispatch a
// mutation, and the context was compiled under the read-only policy. Running
// that is how a request ends up judging a workspace it was never shown.
//
// The repair is a BLOCKING transaction with three mandatory steps:
//
//  1. INVALIDATE the previous intent and every artefact derived from it — the
//     cached context descriptor and the compiled payload. Current() then fails
//     closed while the revision is in flight, so no concurrent component can
//     read the stale intent.
//  2. SYNCHRONIZE the canonical intent to `modification`.
//  3. RE-COMPILE the workspace context under the modification contract BEFORE
//     the loop dispatches, and commit it only if its SEMANTIC provenance holds
//     (scope matched, workspace material present, compiled under this intent).
//
// The gate is fail-closed: a re-compilation that cannot satisfy the contract
// parks the run at an explicit human boundary. It never proceeds on the
// read-only context, and it never falls back to a token-count heuristic.

func (d *Driver) intentAuthority() *autonomy.IntentAuthority {
	if d == nil {
		return nil
	}
	if d.intents == nil {
		d.intents = autonomy.NewIntentAuthority()
	}
	return d.intents
}

// syncCanonicalIntent resolves the lifecycle's canonical intent and performs a
// blocking revision when the gateway elevates a read-only classification to a
// mutation contract. It returns a non-nil error when the re-compiled context
// cannot satisfy the modification contract; the caller parks the run.
func (d *Driver) syncCanonicalIntent(ctx context.Context, objective string) error {
	authority := d.intentAuthority()
	classified := autonomy.Classify(objective, nil)
	authority.Resolve(classified.Intent)

	// Elevation is a one-way, deterministic fact: the gateway selected a
	// mutation-shaped strategy for an objective the classifier read as
	// read-only. It reads the SAME canonical projection admission reads
	// (strategy.MutationSemanticsOf), so the two can never disagree about
	// whether a strategy carries mutation semantics — which is exactly the split
	// brain this revision exists to repair, one layer earlier.
	needsMutationContract := strategy.MutationSemanticsOf(d.resolved.Profile.Strategy).RequiresMutationContract()
	if !needsMutationContract || classified.Intent.RequiresMutation() {
		// No elevation: the classification and the dispatched contract already
		// agree. Bind the compiled context to the canonical intent so a later
		// consumer can see WHICH intent produced it.
		return nil
	}
	if d.adapter == nil {
		return nil
	}
	targets := d.objectiveTargets()
	revised, err := authority.Elevate(autonomy.IntentModification,
		"strategy gateway dispatched a mutation contract over a read-only classification ("+
			string(classified.Intent)+")")
	if err != nil {
		return fmt.Errorf("autonomy: intent revision: %w", err)
	}
	if !revised.ContextInvalidated {
		// Nothing changed: the canonical intent already is the modification
		// contract. Re-binding is a no-op, not a revision.
		return nil
	}
	diagnosticf("[intent] blocking revision %s -> %s rev=%d: %s",
		revised.From, revised.To, revised.Revision, revised.Reason)
	if d.bus != nil {
		d.bus.Publish(events.NewActivity(fmt.Sprintf(
			"[intent] revision %s -> %s rev=%d: %s — previous context descriptor invalidated",
			revised.From, revised.To, revised.Revision, revised.Reason)))
	}

	// ── 3 — RE-COMPILE under the modification contract, BEFORE dispatch ──
	provenance, compileErr := d.adapter.RecompileIntentContext(ctx, targets,
		string(autonomy.IntentModification), contextcompiler.IntentContextWorkspace)
	if compileErr != nil {
		// The revision stays OPEN: the canonical intent is modification and no
		// valid context exists for it. A subsequent Current() fails closed.
		return fmt.Errorf("autonomy: intent context re-compilation under %q failed: %w",
			autonomy.IntentModification, compileErr)
	}
	diagnosticf("[intent] context re-compiled under %s: scope_matched=%t workspace_material=%t truncated=%d tokens=%d (telemetry only)",
		autonomy.IntentModification, provenance.ScopeMatched, provenance.WorkspaceMaterialPresent,
		len(provenance.TruncatedTargets), provenance.ObservedTokens)
	return authority.CommitContext(autonomy.IntentModification, provenance.Valid)
}
