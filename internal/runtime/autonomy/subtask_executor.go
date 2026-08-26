package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
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

// patchContractBlock is the STRICT output-format instruction injected into
// bounded-line sub-task prompts. It mirrors byte-for-byte the block shape the
// executor's Boundary-4 gate accepts.
const patchContractBlock = "CRITICAL: Output MUST use exact SEARCH/REPLACE block format:\n" +
	"<<<<<<< SEARCH\n" +
	"[exact content from target lines]\n" +
	"=======\n" +
	"[proposed modification]\n" +
	">>>>>>>\n" +
	"Do not output raw code or markdown formatting outside search/replace blocks."

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

// executeSubTaskWithRetry runs ONE approved sub-task through up to
// maxSubTaskAttempts provider invocations. A retryable artifact rejection
// (artifact_retryable_rejected — the output violated the bounded SEARCH/REPLACE
// contract) does NOT abort the DAG: the validation error is appended to the
// retry prompt context and the unit re-executes under a rotated window with a
// distinct request identity. Nothing was mutated by a rejected attempt, so the
// caller's expected workspace digest remains valid for every retry. Any other
// error, or exhausted attempts, returns the last observation for the caller's
// terminal handling.
func (d *Driver) executeSubTaskWithRetry(ctx context.Context, dag *planner.ExecutionDAG, st planner.SubTask,
	pos, total int, targets []string, workspaceDigest string) (autonomy.Observation, int, error) {
	baseEvidence := fmt.Sprintf("[DAG sub_task=%s region=%s estimate=%dtok ceiling=%dtok base_digest=%s]",
		st.ID, st.Region, st.EstimatedTokens, dag.Budget(), short(dag.BaseTreeDigest))
	var (
		obs      autonomy.Observation
		err      error
		attempts int
	)
	for attempt := 1; attempt <= maxSubTaskAttempts; attempt++ {
		attempts = attempt
		req := d.subTaskRequest(dag, st, pos, total, targets, workspaceDigest, baseEvidence)
		// RecoveryAttempt rotates the executor's bounded-patch context window:
		// every retry sees materially different copyable source.
		req.RecoveryAttempt = pos + attempt - 1
		if attempt == 1 {
			req.StreamCallback = d.streamCb
			d.streamCb = nil
		} else {
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
	return obs, attempts, nil
}

// subTaskRequest builds the canonical LoopRequest for one sub-task execution
// attempt: the whole plan travels with EVERY unit so the executor's Boundary-2
// guard evaluates each window individually.
func (d *Driver) subTaskRequest(dag *planner.ExecutionDAG, st planner.SubTask, pos, total int,
	targets []string, workspaceDigest, evidence string) autonomy.LoopRequest {
	return autonomy.LoopRequest{
		RequestID:       fmt.Sprintf("%s-%s", d.runRequestID, st.ID),
		Prompt:          subTaskPrompt(d.prompt, dag, st, pos, total),
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
	}
}
