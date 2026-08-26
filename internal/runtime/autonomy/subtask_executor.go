package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Sub-task executor: strict patch contract + intra-DAG retry loop ────────
//
// The approved DAG executes every sub-task through the full executor pipeline
// under the bounded-patch protocol. Two feed-forward mechanisms keep a single
// malformed generation from killing the whole atomic transaction:
//
//  1. STRICT PATCH CONTRACT INJECTION — every SEARCH_REPLACE_BOUNDED_LINES
//     sub-task prompt carries the exact SEARCH/REPLACE block format up front,
//     so the model cannot claim ignorance of the artifact structure the
//     Boundary-4 gate enforces.
//
//  2. INTRA-SUB-TASK RETRY — an artifact_retryable_rejected outcome (the
//     bounded-patch contract refused the output STRUCTURE) retries the SAME
//     sub-task up to maxSubTaskAttempts times instead of aborting the whole
//     DAG. Every retry appends the gate's validation error to the prompt
//     context so the model can self-correct, and rotates the bounded-patch
//     context window via RecoveryAttempt (materially different input). Only
//     exhausted attempts converge to the DAG failure path.

// maxSubTaskAttempts caps provider invocations per sub-task inside one DAG
// execution (1 initial attempt + maxSubTaskAttempts-1 contract retries).
const maxSubTaskAttempts = 3

// maxNoOpEscalations caps the NO-OP escalation circuit per sub-task: after a
// NO_OP_OBJECTIVE_UNRESOLVED observation the unit is re-hydrated AT MOST this
// many times (elevated structural context + broader boundary window) before
// the DAG transitions to DAG_ESCALATED and returns the decision to a human.
const maxNoOpEscalations = 1

// noOpSentinel is the exact token a sub-task model MUST emit when its assigned
// slice requires no edit. The executor's bounded-patch sanitizer recognizes it
// and converges the unit to a successful no-op instead of burning the retry
// budget on prose that can never satisfy the SEARCH/REPLACE gate.
const noOpSentinel = "NO_CHANGES_REQUIRED"

// patchContractBlock is the STRICT output-format instruction injected into
// bounded-line sub-task prompts. It mirrors byte-for-byte the block shape the
// executor's Boundary-4 gate accepts and carries two FEW-SHOT templates — one
// mutation example and one NO-OP sentinel example — so weak/free-tier models
// (finish_reason="stop" + prose) cannot claim ambiguity about what to emit
// when their slice needs no change.
const patchContractBlock = "CRITICAL: Output MUST use exact SEARCH/REPLACE block format:\n" +
	"<<<<<<< SEARCH\n" +
	"[exact content from target lines]\n" +
	"=======\n" +
	"[proposed modification]\n" +
	">>>>>>>\n" +
	"\n" +
	"MUTATION EXAMPLE:\n" +
	"<<<<<<< SEARCH\n" +
	"<div class=\"old\">\n" +
	"=======\n" +
	"<div class=\"new\">\n" +
	">>>>>>>\n" +
	"\n" +
	"NO-OP EXAMPLE — if the assigned lines require NO change, output EXACTLY this single line and nothing else:\n" +
	noOpSentinel + "\n" +
	"\n" +
	"Do not output raw code or markdown formatting outside search/replace blocks.\n" +
	"If a slice needs no edit you MUST emit exactly " + noOpSentinel +
	" instead of conversational prose, an apology, or an explanation."

// requiresPatchContract reports whether the sub-task's split kind is bound to
// the explicit line-interval SEARCH_REPLACE_BOUNDED_LINES mutation contract.
func requiresPatchContract(kind planner.SplitKind) bool {
	return kind == planner.SplitBoundedLines
}

// injectPatchContract appends the strict artifact-formatting contract when the
// sub-task carries the bounded-lines contract; other kinds return the prompt
// unchanged.
func injectPatchContract(prompt string, kind planner.SplitKind) string {
	if !requiresPatchContract(kind) {
		return prompt
	}
	return prompt + "\n\n" + patchContractBlock
}

// retryEvidenceSignal builds the DiagnosticSignal-class evidence appended to a
// retry attempt's evidence ledger: the gate's validation error plus the
// corrective directive — never the rejected bytes.
func retryEvidenceSignal(st planner.SubTask, o autonomy.Observation, attempt int) string {
	detail := strings.TrimSpace(o.Diagnostic)
	if detail == "" {
		detail = "artifact failed the bounded patch contract validation"
	}
	return fmt.Sprintf(
		"[DIAGNOSTIC subtype=SCHEMA_VIOLATION boundary=B4-artifact-gate target=%s sub_task=%s attempt=%d/%d] "+
			"Previous output was REJECTED by the artifact gate (%s): %s",
		o.Target, st.ID, attempt, maxSubTaskAttempts, o.Outcome, detail)
}

// retryDirective builds the self-correction context appended to a retry
// attempt's prompt: what was rejected, the concrete validation error, and the
// strict block contract restated verbatim.
func retryDirective(st planner.SubTask, o autonomy.Observation, attempt int) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"[RETRY %d/%d — your previous output for %s was REJECTED by the artifact gate as %s]",
		attempt, maxSubTaskAttempts, st.ID, o.Outcome)
	if detail := strings.TrimSpace(o.Diagnostic); detail != "" {
		b.WriteString("\nValidation error: ")
		b.WriteString(detail)
	}
	b.WriteString("\n")
	b.WriteString(patchContractBlock)
	return b.String()
}

// noOpEscalationDirective builds the RE-HYDRATED prompt context of a NO-OP
// escalation attempt: the structural contradiction is stated explicitly, the
// raw claim is quoted as a claim (never as truth), and the strict block
// contract is restated so the widened window can actually produce an artifact.
func noOpEscalationDirective(st planner.SubTask, o autonomy.Observation, attempt int) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"[NO-OP ESCALATION %d/%d — your previous NO_CHANGES_REQUIRED answer for %s CONFLICTS with structural analysis]",
		attempt, maxNoOpEscalations+1, st.ID)
	if detail := strings.TrimSpace(o.Diagnostic); detail != "" {
		b.WriteString("\nStructural evidence: ")
		b.WriteString(detail)
	}
	b.WriteString("\nThe assigned change window has been WIDENED — re-examine it fully. " +
		"If the objective's targeted content IS present in this window you MUST produce the anchored SEARCH/REPLACE block that addresses it. " +
		"Emit exactly " + noOpSentinel + " only if the window provably contains nothing to change.\n")
	b.WriteString(patchContractBlock)
	return b.String()
}

// noOpEscalationEvidenceSignal builds the DiagnosticSignal-class evidence
// appended to an escalation attempt's evidence ledger.
func noOpEscalationEvidenceSignal(st planner.SubTask, o autonomy.Observation, attempt int) string {
	detail := strings.TrimSpace(o.Diagnostic)
	if detail == "" {
		detail = "no-op claim conflicts with structural evidence"
	}
	return fmt.Sprintf(
		"[DIAGNOSTIC subtype=NO_OP_OBJECTIVE_UNRESOLVED boundary=B4-noop-semantics target=%s sub_task=%s escalation=%d/%d] "+
			"Previous NO_CHANGES_REQUIRED claim was CONTRADICTED by structural analysis: %s",
		o.Target, st.ID, attempt, maxNoOpEscalations, detail)
}

// executeSubTaskWithRetry runs ONE approved sub-task through up to
// maxSubTaskAttempts provider invocations plus the bounded NO-OP escalation
// circuit. A retryable artifact rejection
// (artifact_retryable_rejected — the output violated the bounded SEARCH/REPLACE
// contract) does NOT abort the DAG: the validation error is appended to the
// retry prompt context and the unit re-executes under a rotated window with a
// distinct request identity. A NO_OP_OBJECTIVE_UNRESOLVED observation never
// terminates the unit either: it triggers ONE re-hydrated escalation attempt
// with elevated structural context and a broader boundary window; if the claim
// STILL conflicts with structure, the observation carries Escalated=true and
// the caller transitions the DAG to DAG_ESCALATED instead of logging a false
// completion. Nothing was mutated by any rejected attempt, so the caller's
// expected workspace digest remains valid throughout.
func (d *Driver) executeSubTaskWithRetry(ctx context.Context, dag *planner.ExecutionDAG, st planner.SubTask,
	pos, total int, targets []string, workspaceDigest string, streamCb execution.StreamCallback) (autonomy.Observation, int, error) {
	baseEvidence := fmt.Sprintf("[DAG sub_task=%s region=%s estimate=%dtok ceiling=%dtok base_digest=%s]",
		st.ID, st.Region, st.EstimatedTokens, dag.Budget(), short(dag.BaseTreeDigest))
	// Compressed structural context: compress the CURRENT target bytes (the
	// file mutates between units) into topology + targeted evidence. A
	// read failure degrades gracefully to a context-free scoped prompt.
	compressed := d.compressedContextFor(dag.Target, st)
	var (
		obs      autonomy.Observation
		err      error
		attempts int
	)
	for attempt := 1; attempt <= maxSubTaskAttempts; attempt++ {
		attempts = attempt
		req := d.subTaskRequest(dag, st, pos, total, targets, workspaceDigest, baseEvidence, compressed)
		req.StreamCallback = streamCb
		// RecoveryAttempt rotates the executor's bounded-patch context window:
		// every retry sees materially different copyable source.
		req.RecoveryAttempt = pos + attempt - 1
		if attempt > 1 {
			req.RequestID = fmt.Sprintf("%s-retry-%d", req.RequestID, attempt-1)
			req.Evidence = joinEvidence(baseEvidence, retryEvidenceSignal(st, obs, attempt))
			req.Prompt += "\n\n" + retryDirective(st, obs, attempt)
		}
		obs, err = d.executeSubTask(ctx, req)
		if err != nil {
			return autonomy.Observation{}, attempts, err
		}
		d.obs = obs
		d.aggregateUsage(obs)
		if obs.Outcome == autonomy.OutcomeArtifactRetryableRejected && attempt < maxSubTaskAttempts {
			diagnosticf("[boundary2] sub-task %s attempt %d/%d rejected (%s) — retrying with contract feedback",
				st.ID, attempt, maxSubTaskAttempts, obs.Outcome)
			continue
		}
		break
	}

	// ── NO-OP ESCALATION CIRCUIT ───────────────────────────────────────────
	// Attempt 1: re-hydrate the sub-task prompt with elevated structural
	// context and a BROADER boundary window. If the sentinel claim survives
	// even the escalated judgment, the caller escalates the DAG to a human —
	// the unit is NEVER terminally completed on a contradicted claim.
	escalations := 0
	for obs.Outcome == autonomy.OutcomeNoOpObjectiveUnresolved && escalations < maxNoOpEscalations {
		escalations++
		attempts++
		diagnosticf("[noop-semantics] sub-task %s NO_OP_OBJECTIVE_UNRESOLVED — escalating %d/%d with elevated structural context",
			st.ID, escalations, maxNoOpEscalations)
		req := d.subTaskRequest(dag, st, pos, total, targets, workspaceDigest, baseEvidence, compressed)
		req.StreamCallback = streamCb
		req.NoOpEscalation = true
		// A distinct rotated window: materially different copyable source.
		req.RecoveryAttempt = pos + maxSubTaskAttempts + escalations - 1
		req.RequestID = fmt.Sprintf("%s-escalation-%d", req.RequestID, escalations)
		req.Evidence = joinEvidence(baseEvidence, noOpEscalationEvidenceSignal(st, obs, escalations))
		req.Prompt += "\n\n" + noOpEscalationDirective(st, obs, escalations)
		obs, err = d.executeSubTask(ctx, req)
		if err != nil {
			return autonomy.Observation{}, attempts, err
		}
		d.obs = obs
		d.aggregateUsage(obs)
	}
	if obs.Outcome == autonomy.OutcomeNoOpObjectiveUnresolved {
		obs.Escalated = true
	}
	return obs, attempts, nil
}

// compressedContextFor reads the target's current bytes and compresses them
// into the sub-task's structural orientation payload. Any read failure or
// unscannable format returns nil — the prompt simply omits the block.
func (d *Driver) compressedContextFor(target string, st planner.SubTask) *CompressedStructuralContext {
	if d == nil || d.adapter == nil || target == "" {
		return nil
	}
	source, ok := d.adapter.ReadTargetFile(target)
	if !ok || len(source) == 0 {
		return nil
	}
	return buildCompressedStructuralContext(target, source, st)
}

// subTaskRequest builds the canonical LoopRequest for one sub-task execution
// attempt: the whole plan travels with EVERY unit so the executor's Boundary-2
// guard evaluates each window individually, the FOCUS region pins the
// bounded-patch copyable window to THIS unit's assigned lines (a retry can
// never be shown — nor anchor on — another unit's content), and the prompt
// carries the compressed structural topology instead of raw source.
func (d *Driver) subTaskRequest(dag *planner.ExecutionDAG, st planner.SubTask, pos, total int,
	targets []string, workspaceDigest, evidence string, compressed *CompressedStructuralContext) autonomy.LoopRequest {
	return autonomy.LoopRequest{
		RequestID:       fmt.Sprintf("%s-%s", d.runRequestID, st.ID),
		Prompt:          subTaskPrompt(d.prompt, dag, st, pos, total, compressed),
		Target:          dag.Target,
		Targets:         append([]string(nil), targets...),
		Evidence:        evidence,
		Intent:          "mutate",
		MaxOutputTokens: dag.MaxOutputTokens,
		WorkspaceDigest: workspaceDigest,
		// The approved plan forces the bounded-patch protocol on every unit.
		RecoveryStrategy: autonomy.StrategyBoundedPatch,
		RecoveryReason:   fmt.Sprintf("decomposition sub-task %d/%d scoped to %s", pos, total, st.Region),
		StagedPlan:       dag,
		// Region focus: the executor derives its deterministic context window
		// from exactly this unit's line interval.
		FocusStartLine: st.Region.StartLine,
		FocusEndLine:   st.Region.EndLine,
	}
}
