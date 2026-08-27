package autonomy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Boundary-2 expansion: Sub-task Decomposer & DAG Planner ─────────────────
//
// When the preflight guard refuses an objective (preflight_infeasible, I5),
// the driver no longer simply parks at a generic ask-human gate. It stages a
// DECOMPOSITION_PROPOSAL: the planner partitions the target into individually
// preflight-feasible sub-tasks inside a validated ExecutionDAG and the loop
// parks at a typed boundary listing ALL of them.
//
// The human's approval authorizes the WHOLE plan as one ATOMIC TRANSACTION:
//
//   - every sub-task executes in topological order through the full executor
//     pipeline (B2 → B3 → B4 → B5), forced onto the bounded-patch protocol;
//   - the Boundary-5 WorkspaceTreeDigest is captured before AND after every
//     sub-task; the "after" digest of one sub-task is the expected "before"
//     digest of the next, so any out-of-band writer is caught between steps;
//   - on ANY sub-task failure at Boundary 3 (output gate), 4 (artifact gate)
//     or 5 (mutation authority) — or drift/cancellation anywhere — the DAG
//     aborts, the workspace is rolled back to BaseTreeDigest, and the plan is
//     marked DAG_EXECUTION_FAILED. Remaining sub-tasks NEVER execute.

// DecomposeFunc stages an ExecutionDAG for one infeasible objective. It is
// injectable for policy tests; the default wraps planner.DecomposeTarget
// (Lea semantic unit splitting with syntactic fallback).
type DecomposeFunc func(objective, target string, source []byte, baseDigest string, maxOutputTokens int) (*planner.ExecutionDAG, error)

// defaultDecompose delegates to the canonical deterministic planner: semantic
// structural units first (HTML/JSX/Go templates), syntactic splitting only as
// the low_semantic_confidence fallback.
func defaultDecompose(objective, target string, source []byte, baseDigest string, maxOutputTokens int) (*planner.ExecutionDAG, error) {
	return planner.DecomposeTarget(objective, target, source, baseDigest, maxOutputTokens)
}

// Proposal returns the parked DECOMPOSITION_PROPOSAL plan, or nil while no
// such boundary is active.
func (d *Driver) Proposal() *planner.ExecutionDAG {
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryDecomposition {
		return nil
	}
	return b.Proposal
}

// Plan returns the most recently staged decomposition plan with its lifecycle
// status (PLAN_STAGED / DAG_EXECUTING / DAG_EXECUTION_COMPLETED /
// DAG_EXECUTION_FAILED). Nil when no decomposition was ever staged.
func (d *Driver) Plan() *planner.ExecutionDAG { return d.dag }

// stageDecomposition reacts to a preflight_infeasible observation: it reads
// the target content, asks the planner for a valid ExecutionDAG and parks the
// loop at the typed proposal boundary. It returns false when decomposition is
// unavailable or fails — the caller then falls through to the plain human
// re-scope park (intent is never silently altered).
func (d *Driver) stageDecomposition() bool {
	if d.decompose == nil || d.adapter == nil || d.loop == nil {
		return false
	}
	target := firstTarget(d.req.Targets)
	if target == "" {
		target = d.obs.Target
	}
	if target == "" || !planner.Decomposable(target) {
		return false
	}
	maxOut := d.obs.MaxOutputTokens
	if maxOut <= 0 {
		// Without a known output ceiling there is no budget to decompose under.
		return false
	}
	source, ok := d.adapter.ReadTargetFile(target)
	if !ok || len(source) == 0 {
		diagnosticf("[boundary2] decomposition skipped: target %s unreadable", target)
		return false
	}
	base := d.req.WorkspaceDigest
	if base == "" {
		base = d.adapter.WorkspaceVersion([]string{target})
	}
	dag, err := d.decompose(d.prompt, target, source, base, maxOut)
	if err != nil {
		diagnosticf("[boundary2] decomposition unavailable: %v — falling back to explicit re-scope", err)
		return false
	}
	d.dag = dag
	b := autonomy.HumanBoundary{
		Reason:   dag.ProposalSummary(),
		Targets:  []string{target},
		Proposal: dag,
	}
	d.loop.AwaitHuman(b)
	d.enrichBoundary()
	d.publish(d.runCtx) //nolint:contextcheck // runCtx is the run's own cancellation context
	diagnosticf("[boundary2] DECOMPOSITION_PROPOSAL staged plan=%s target=%s sub_tasks=%d ceiling=%d tok/sub-task",
		dag.Status, target, len(dag.SubTasks), dag.Budget())
	return true
}

// ResumeApproveProposal resolves a parked DECOMPOSITION_PROPOSAL: it runs the
// approved DAG inside the atomic transaction loop until completion, failure,
// rejection of the ground truth, or cancellation.
func (d *Driver) ResumeApproveProposal(ctx context.Context) (*autonomy.LoopTermination, error) {
	dag := d.Proposal()
	if dag == nil {
		return d.term(), errors.New("autonomy: proposal approval requires a parked DECOMPOSITION_PROPOSAL boundary")
	}
	d.runID++
	d.loop.ReleaseHuman("DECOMPOSITION_PROPOSAL approved")
	d.publish(ctx)
	term := d.runProposalDAG(ctx, dag)
	if term != nil && term.State.IsTerminal() {
		d.runCtx, d.runCancel = nil, nil
	}
	return term, nil
}

// ResumeRejectProposal resolves a parked DECOMPOSITION_PROPOSAL by rejecting
// the whole plan: nothing was executed, so this is a terminal human decision.
func (d *Driver) ResumeRejectProposal(ctx context.Context, reason string) (*autonomy.LoopTermination, error) {
	if d.Proposal() == nil {
		return d.term(), errors.New("autonomy: proposal rejection requires a parked DECOMPOSITION_PROPOSAL boundary")
	}
	d.loop.ReleaseHuman("DECOMPOSITION_PROPOSAL rejected")
	d.publish(ctx)
	if d.dag != nil && !d.dag.Status.Terminal() {
		d.dag.Status = planner.DagExecutionFailed
		d.dag.FailureReason = "proposal rejected by human: " + reason
	}
	term := d.terminateAbort(ctx, "decomposition proposal rejected: "+reason, autonomy.FailurePermanent)
	d.runCtx, d.runCancel = nil, nil
	return term, nil
}

// runProposalDAG is the atomic transaction loop over the approved sub-tasks.
func (d *Driver) runProposalDAG(ctx context.Context, dag *planner.ExecutionDAG) *autonomy.LoopTermination {
	targets := dag.Targets()
	n := len(dag.SubTasks)

	// Reserve bounded headroom for exactly the consented scope: N approved
	// sub-executions must not trip the single-objective defaults mid-plan.
	// Each sub-task may consume up to maxSubTaskAttempts provider invocations
	// (intra-DAG artifact-contract retries) PLUS maxNoOpEscalations NO-OP
	// escalations, so every floor reserves that worst case explicitly — a
	// retried or escalated plan is still a bounded plan.
	totalEstimated := dag.TotalEstimatedTokens()
	worstExecs := n * (maxSubTaskAttempts + maxNoOpEscalations)
	d.loop.WidenBounds(worstExecs+1, worstExecs+1,
		totalEstimated*(maxSubTaskAttempts+maxNoOpEscalations)+worstExecs*256+1024, worstExecs+2)

	// Atomicity snapshot: the exact bytes every plan target had BEFORE the
	// first mutation. Rollback restores them verbatim.
	originals := make(map[string][]byte, len(targets))
	for _, t := range targets {
		if data, ok := d.adapter.ReadTargetFile(t); ok {
			originals[t] = data
		}
	}

	dag.Status = planner.DagExecuting
	expected := dag.BaseTreeDigest

	// The UI's streaming callback (content + reasoning deltas for the Ctrl+O
	// thought drawer) is captured ONCE for the whole DAG transaction and
	// attached to EVERY sub-task attempt — a unit boundary or an intra-DAG
	// contract retry must never blind the live thought trace mid-run.
	streamCb := d.streamCb
	d.streamCb = nil

	for i := range dag.SubTasks {
		st := dag.SubTasks[i]
		if cerr := ctx.Err(); cerr != nil {
			return d.failDAG(ctx, dag, originals, i,
				fmt.Sprintf("cancelled before %s (%d/%d): %v", st.ID, i+1, n, cerr))
		}

		// ── BOUNDARY 5 — digest BEFORE the sub-task ────────────────────
		before := d.adapter.WorkspaceVersion(targets)
		if before == "" || before != expected {
			return d.failDAG(ctx, dag, originals, i,
				fmt.Sprintf("workspace drift before %s (%d/%d): expected %s… got %s…",
					st.ID, i+1, n, short(expected), short(before)))
		}

		obs, attempts, err := d.executeSubTaskWithRetry(ctx, dag, st, i+1, n, targets, before, streamCb)
		if err != nil {
			return d.failDAG(ctx, dag, originals, i,
				fmt.Sprintf("sub-task %s (%d/%d) failed at the output/artifact gates: %v", st.ID, i+1, n, err))
		}
		d.obs = obs

		// ── NO-OP ESCALATION INTERCEPT ─────────────────────────────────
		// An unresolved no-op claim (contradicted by structural analysis even
		// after re-hydration) or a below-threshold review hold NEVER marks
		// the sub-task or DAG terminally completed. The decision returns to
		// awaiting_human with the plan marked DAG_ESCALATED — a false DAG
		// success is architecturally impossible from this path.
		if obs.Outcome == autonomy.OutcomeNoOpObjectiveUnresolved ||
			obs.Outcome == autonomy.OutcomeNoOpNoSafeMutation {
			return d.escalateDAG(ctx, dag, st, i, attempts, obs)
		}

		// The proposal approval covers each held patch: resolve the gate.
		if obs.Outcome == autonomy.OutcomePendingApproval {
			approved, aerr := d.adapter.Approve(ctx, obs.PatchID)
			if aerr != nil {
				return d.failDAG(ctx, dag, originals, i,
					fmt.Sprintf("sub-task %s approval failed: %v", st.ID, aerr))
			}
			d.obs = approved
			d.aggregateUsage(approved)
			obs = approved
		}

		if !dagOutcomeSuccess(obs) {
			attemptNote := ""
			if attempts > 1 {
				attemptNote = fmt.Sprintf(" after %d/%d contract attempts", attempts, maxSubTaskAttempts)
			}
			return d.failDAG(ctx, dag, originals, i,
				fmt.Sprintf("sub-task %s (%d/%d) terminal outcome %s (finish_reason=%q)%s — boundaries 3/4/5 refused the unit",
					st.ID, i+1, n, obs.Outcome, obs.FinishReason, attemptNote))
		}

		// ── BOUNDARY 5 — digest AFTER the sub-task ─────────────────────
		after := d.adapter.WorkspaceVersion(targets)
		if after == "" {
			return d.failDAG(ctx, dag, originals, i+1,
				fmt.Sprintf("sub-task %s: post-apply digest unavailable", st.ID))
		}
		// The "after" digest becomes the next sub-task's expected "before":
		// any out-of-band writer between steps is caught at the top of the
		// loop. A no-change apply keeps the same digest — still consistent.
		expected = after
		diagnosticf("[boundary2] sub-task %s applied outcome=%s progress=%d/%d digest=%s…",
			st.ID, obs.Outcome, i+1, n, short(after))
	}

	// ── POST-DAG GLOBAL STRUCTURAL VERIFICATION ────────────────────────
	// Every unit gate passed, yet per-unit isolation is blind to aggregate
	// regressions (st-1 removes a CSS definition st-4 still uses). Before
	// the DAG may claim completion, the WHOLE mutated document is audited
	// against its pre-DAG baseline. A rejected audit overrides completion:
	// OBJECTIVE_UNRESOLVED, decision returned to awaiting_human, applied
	// units preserved. The loop is parked — return immediately with NO
	// termination (a parked run terminates nothing), exactly like the
	// NO-OP escalation path above.
	if d.globalVerify != nil && d.verifyGlobalObjective(ctx, dag, originals) {
		return d.term()
	}

	dag.Status = planner.DagExecutionCompleted
	reason := fmt.Sprintf("decomposition executed atomically: %d/%d sub-tasks applied to %s (base digest %s… restored nowhere — all units landed)",
		n, n, dag.Target, short(dag.BaseTreeDigest))
	if _, err := d.step(ctx, autonomy.LoopDecision{
		Action: autonomy.LoopComplete,
		Reason: reason,
	}); err != nil {
		d.loop.Complete(reason)
		d.publish(ctx)
	}
	d.publish(ctx)
	return d.term()
}

// executeSubTask drives the loop state machine around ONE provider invocation
// so bounds accounting and the canonical transition history stay truthful.
func (d *Driver) executeSubTask(ctx context.Context, req autonomy.LoopRequest) (autonomy.Observation, error) {
	d.obs.AttemptNum = d.loop.Attempts()
	d.obs.RecoveryCycle = d.loop.RecoveryCycles()
	// First unit moves Observing → Deciding; later units are no-ops (the loop
	// sits at Interpreting after the previous ConsumeVerification).
	d.loop.Observe(d.obs)
	if _, err := d.step(ctx, autonomy.LoopDecision{
		Action: autonomy.LoopContinue,
		Reason: "DAG_EXECUTING sub-task " + strings.TrimPrefix(req.RequestID, d.runRequestID+"-"),
	}); err != nil {
		return autonomy.Observation{}, err
	}
	obs, err := d.adapter.Execute(ctx, req)
	if err != nil {
		return autonomy.Observation{}, err
	}
	d.loop.ConsumeExecution(obs)
	d.loop.ConsumeVerification(obs)
	d.publish(ctx)
	return obs, nil
}

// aggregateUsage folds one authoritative observation into the run totals.
func (d *Driver) aggregateUsage(obs autonomy.Observation) {
	if obs.UsageKnown {
		d.aggInput += obs.InputTokens
		d.aggOutput += obs.OutputTokens
		d.aggKnown = true
	} else if obs.TokenUsage > 0 {
		d.aggInput += obs.TokenUsage
	}
}

// dagOutcomeSuccess reports whether a terminal sub-task observation counts as
// an applied unit of the transaction. A NO_OP_OBJECTIVE_SATISFIED unit (the
// model answered NO_CHANGES_REQUIRED and structural analysis confirmed the
// claim) also counts: the contract was satisfied with no mutation required.
// The two other NO-OP sub-states NEVER count: no_safe_mutation requires human
// review, objective_unresolved is the escalation trigger.
func dagOutcomeSuccess(o autonomy.Observation) bool {
	switch o.Outcome {
	case autonomy.OutcomeChanged, autonomy.OutcomeCreated, autonomy.OutcomeNoChange,
		autonomy.OutcomeCompleted, autonomy.OutcomeNoOpObjectiveSatisfied:
		return true
	default:
		return false
	}
}

// escalateDAG implements the terminal NO-OP escalation path. A sub-task's
// sentinel claim survived re-hydration (no_op_objective_unresolved) or fell
// below the safety threshold (no_op_no_safe_mutation): the plan transitions to
// DAG_ESCALATED and the loop parks at an awaiting_human inform boundary —
// never a completed DAG, never a silent rollback of already-verified units.
//
// Atomicity note: unlike failDAG, applied units are deliberately PRESERVED.
// Every landed unit passed its own Boundary-5 digest chain; escalation is not
// a failure verdict, and discarding verified human-approved work would be a
// second false outcome. The boundary reason names exactly what landed and
// which unit escalated so the human decides with full evidence.
func (d *Driver) escalateDAG(ctx context.Context, dag *planner.ExecutionDAG, st planner.SubTask,
	completed, attempts int, obs autonomy.Observation) *autonomy.LoopTermination {
	reason := fmt.Sprintf(
		"sub-task %s (%d/%d) terminal outcome %s after %d attempt(s): the model's NO_CHANGES_REQUIRED claim could not be reconciled with structural evidence (%s)",
		st.ID, completed+1, len(dag.SubTasks), obs.Outcome, attempts, boundedEvidenceLine(obs.Diagnostic))
	if obs.Outcome == autonomy.OutcomeNoOpNoSafeMutation {
		reason = fmt.Sprintf(
			"sub-task %s (%d/%d) terminal outcome %s: candidate edits detected below the safety threshold — requires_review",
			st.ID, completed+1, len(dag.SubTasks), obs.Outcome)
	}
	dag.Status = planner.DagEscalated
	dag.FailureReason = reason
	diagnosticf("[noop-semantics] DAG_ESCALATED target=%s applied=%d/%d sub_task=%s — decision returned to awaiting_human: %s",
		dag.Target, completed, len(dag.SubTasks), st.ID, reason)
	b := &autonomy.HumanBoundary{
		Reason: "DAG_ESCALATED: " + reason +
			fmt.Sprintf(" — %d/%d approved units are applied and preserved; the remaining plan is held for your decision",
				completed, len(dag.SubTasks)),
		Targets: dag.Targets(),
	}
	autonomy.DeriveBoundaryAction(b)
	d.loop.AwaitHuman(*b)
	d.enrichBoundary()
	d.publish(ctx)
	return d.term()
}

// boundedEvidenceLine extracts the first, bounded line of a diagnostic as
// escalation evidence (bounded diagnostics travel; raw slices never do).
func boundedEvidenceLine(diagnostic string) string {
	line := strings.TrimSpace(diagnostic)
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	const maxEscalationEvidence = 200
	if len(line) > maxEscalationEvidence {
		line = line[:maxEscalationEvidence]
	}
	if line == "" {
		return "no structural detail reported"
	}
	return line
}

// failDAG enforces the atomic invariant: roll the workspace back to the
// BaseTreeDigest, mark the plan DAG_EXECUTION_FAILED and converge the loop to
// a permanent abort. completed counts the sub-tasks that DID land before the
// failure; they were rolled back too. Remaining sub-tasks never execute.
func (d *Driver) failDAG(ctx context.Context, dag *planner.ExecutionDAG, originals map[string][]byte, completed int, reason string) *autonomy.LoopTermination {
	if err := d.adapter.RestoreTargets(originals); err != nil {
		reason += "; ROLLBACK FAILED: " + err.Error()
	}
	digest := d.adapter.WorkspaceVersion(dag.Targets())
	match := digest == dag.BaseTreeDigest
	// Strict boundary telemetry: MUST emit canonical trace.
	diagnosticf("[boundary] state rollback verified digest=%s match=%v", digest, match)
	if !match {
		reason += fmt.Sprintf("; post-rollback digest %s… does not match base %s…", short(digest), short(dag.BaseTreeDigest))
	}
	dag.Status = planner.DagExecutionFailed
	dag.FailureReason = reason
	diagnosticf("[boundary2] DAG_EXECUTION_FAILED target=%s applied=%d/%d — workspace rolled back to base digest %s…: %s",
		dag.Target, completed, len(dag.SubTasks), short(dag.BaseTreeDigest), reason)
	return d.terminateAbort(ctx, "DAG_EXECUTION_FAILED: "+reason, autonomy.FailurePermanent)
}

// subTaskPrompt renders the bounded per-unit instruction. The recovery
// strategy travels separately (bounded_patch); the prompt only scopes WHERE
// the single anchored patch may land. The compressed structural context
// replaces any raw-source dump: document topology, parent/sibling relations,
// active references and targeted Lea evidence — never the file bytes.
// SEARCH_REPLACE_BOUNDED_LINES units additionally carry the STRICT patch
// contract: the exact block format the Boundary-4 gate accepts is stated up
// front so a malformed generation can never claim ambiguity about the
// artifact structure.
func subTaskPrompt(objective string, dag *planner.ExecutionDAG, st planner.SubTask, pos, total int, compressed *CompressedStructuralContext) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(objective))
	fmt.Fprintf(&b, "\n\n[DECOMPOSITION %s — sub-task %d/%d for %s]\n", st.ID, pos, total, dag.Target)
	fmt.Fprintf(&b, "Change window: %s.\nScope: %s.\n", st.Region, st.Description)
	if block := compressed.Render(); block != "" {
		b.WriteByte('\n')
		b.WriteString(block)
	}
	b.WriteString("Produce exactly ONE anchored SEARCH/REPLACE block whose SEARCH text is copied VERBATIM " +
		"from within this change window of the current file content. Do not modify any other region.")
	return injectPatchContract(b.String(), st.Kind)
}
