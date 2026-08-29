package autonomy

import (
	"context"
	"fmt"
	"path/filepath"
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
const patchContractBlock = "CRITICAL: DO NOT REWRITE THE FULL FILE/SECTION. Return STRICTLY targeted <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE blocks only for the exact lines needing change.\n" +
	"\n" +
	"CRITICAL: Output MUST use exact SEARCH/REPLACE block format:\n" +
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
// Every sub-task — a bounded fallback window OR a manifest-scoped semantic
// block — executes under the executor's bounded-patch protocol, so both carry
// the strict delta-format contract that forbids full-file rewrites.
func requiresPatchContract(kind planner.SplitKind) bool {
	return kind == planner.SplitBoundedLines || kind == planner.SplitSemantic
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
// strict block contract restated verbatim. A structural audit rejection
// (unterminated <script>, unbalanced HTML) is rewritten into the AST-aware
// [CONTRACT FAILURE] Line <N>: <ParseError> directive so the successor
// anchors its correction at the exact defect instead of resending raw code.
func retryDirective(st planner.SubTask, o autonomy.Observation, attempt int) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"[RETRY %d/%d — your previous output for %s was REJECTED by the artifact gate as %s]",
		attempt, maxSubTaskAttempts, st.ID, o.Outcome)
	if detail := strings.TrimSpace(o.Diagnostic); detail != "" {
		b.WriteString("\nValidation error: ")
		b.WriteString(execution.StructuralAuditDirective(detail))
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
// into the sub-task's structural orientation payload. Unlike
// buildCompressedStructuralContext, it ALWAYS returns a context for a readable
// target: formats without a Lea scanner (Go, JSON, XML, …) get a minimal
// context carrying the DOCUMENT OUTLINE CONTEXT so their bounded sub-task
// prompts still retain global structure. Any read failure returns nil — the
// prompt simply omits the block.
func (d *Driver) compressedContextFor(target string, st planner.SubTask) *CompressedStructuralContext {
	if d == nil || d.adapter == nil || target == "" {
		return nil
	}
	source, ok := d.adapter.ReadTargetFile(target)
	if !ok || len(source) == 0 {
		return nil
	}
	c := buildCompressedStructuralContext(target, source, st)
	if c == nil {
		c = &CompressedStructuralContext{
			Target:     target,
			TotalLines: planner.LineCount(source),
			ScopeID:    st.ID,
			Scope:      st.Region,
		}
	}
	c.Outline = buildTargetOutline(source, target, st.Region)
	return c
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

// ── DOCUMENT OUTLINE CONTEXT ────────────────────────────────────────────────
//
// Blind fallback window slicing (≤40 lines per chunk) strips global context:
// an individual bounded sub-task is syntactically valid in isolation, so a
// small LLM (Cohere North Mini Code and friends) answers it with an immediate
// NO_CHANGES_REQUIRED no-op. buildTargetOutline injects a compact global
// structure map — the target's top-level blocks with their line ranges plus
// the sub-task's own position — into every bounded sub-task prompt, so the
// model reasons over the whole document skeleton instead of an isolated byte
// window.

// maxOutlineBlocks caps the block list rendered in one outline header. A
// pathological document degrades by elision, never by unbounded size.
const maxOutlineBlocks = 12

// buildTargetOutline renders the DOCUMENT OUTLINE CONTEXT header for one
// sub-task: a bounded global structure map of the WHOLE target (top-level
// HTML/XML blocks or source-code declaration signatures, each with its line
// range) plus the sub-task's own line window within it. The rendered header is
// injected before the strict patch contract so every bounded sub-task retains
// whole-document awareness.
func buildTargetOutline(content []byte, path string, scope planner.Region) string {
	total := planner.LineCount(content)
	if total < 1 {
		return ""
	}
	var b strings.Builder
	b.WriteString("DOCUMENT OUTLINE CONTEXT:\n")
	fmt.Fprintf(&b, "Global Scope Summary: target file has %d total lines containing blocks ", total)
	blocks := targetOutlineBlocks(path, content)
	if len(blocks) == 0 {
		b.WriteString("(no structural blocks detected)")
	} else {
		shown := blocks
		if len(shown) > maxOutlineBlocks {
			shown = shown[:maxOutlineBlocks]
		}
		for i, blk := range shown {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s [%s]", blk.label, blk.region)
		}
		if len(blocks) > maxOutlineBlocks {
			fmt.Fprintf(&b, ", … +%d more blocks", len(blocks)-maxOutlineBlocks)
		}
	}
	fmt.Fprintf(&b, ". You are editing %s within this context. Check for redundancies against the overall structure.", scope)
	return b.String()
}

// outlineBlock is one structural block of the outline map: an inclusive line
// region plus its bounded identity ("<head> metadata", "func NewHandler5()").
type outlineBlock struct {
	region planner.Region
	label  string
}

// targetOutlineBlocks extracts the high-level block map of one target: Lea
// semantic units when the format is Lea-scannable (HTML/JSX/Go templates),
// top-level XML element spans for .xml, and the registered structural/block
// decomposers' sections otherwise (Go/Rust/TS declarations, Markdown/Config
// blocks). Returns nil when no trustworthy structural topology exists — the
// outline then reports the absence instead of inventing structure.
func targetOutlineBlocks(path string, content []byte) []outlineBlock {
	if scan := planner.LeaStructuralScan(path, content); scan != nil && !scan.LowConfidence && len(scan.Units) >= 2 {
		blocks := make([]outlineBlock, 0, len(scan.Units))
		for _, u := range scan.Units {
			blocks = append(blocks, outlineBlock{region: u.Region, label: u.Label})
		}
		return blocks
	}
	if isXMLTarget(path) {
		sections := xmlTopLevelBlocks(content)
		if len(sections) >= 2 {
			return sectionsToOutlineBlocks(sections)
		}
		return nil
	}
	if d := planner.ForTarget(path); d != nil {
		sections, err := d.Split(path, content)
		if err == nil && len(sections) >= 2 {
			return sectionsToOutlineBlocks(sections)
		}
	}
	return nil
}

// sectionsToOutlineBlocks flattens planner sections into outline blocks.
func sectionsToOutlineBlocks(sections []planner.Section) []outlineBlock {
	blocks := make([]outlineBlock, 0, len(sections))
	for _, s := range sections {
		blocks = append(blocks, outlineBlock{region: s.Region, label: s.Label})
	}
	return blocks
}

// isXMLTarget reports whether the target is an XML document. XML has no
// registered planner decomposer, so it is handled by the dedicated top-level
// tag scanner.
func isXMLTarget(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".xml")
}

// xmlTopLevelBlocks scans top-level element boundaries of an XML target and
// lifts each depth-0 element (plus any preamble) into a structural section.
// It respects <?xml …?> declarations, comments, CDATA and self-closing
// elements; nested children never qualify as top-level blocks.
func xmlTopLevelBlocks(content []byte) []planner.Section {
	lines := strings.Split(string(content), "\n")
	var (
		depth  int
		starts []int // 1-indexed lines where a depth-0 element opens
	)
	scan := func(s string, lineNo int) {
		for x := 0; x < len(s); {
			// Comments.
			if strings.HasPrefix(s[x:], "<!--") {
				if end := strings.Index(s[x:], "-->"); end >= 0 {
					x += end + 3
					continue
				}
				break
			}
			// Declarations and processing instructions (<?xml …?>, <!DOCTYPE …>).
			if strings.HasPrefix(s[x:], "<?") || strings.HasPrefix(s[x:], "<!") {
				if end := strings.IndexByte(s[x:], '>'); end >= 0 {
					x += end + 1
					continue
				}
				break
			}
			lt := strings.IndexByte(s[x:], '<')
			if lt < 0 {
				break
			}
			x += lt
			gt := strings.IndexByte(s[x:], '>')
			if gt < 0 {
				break // multi-line element: resume on the next line
			}
			interior := strings.TrimSpace(s[x+1 : x+gt])
			x += gt + 1
			switch {
			case strings.HasPrefix(interior, "/"): // closing tag
				if depth > 0 {
					depth--
				}
			case strings.HasSuffix(interior, "/"), interior == "": // self-closing / malformed
			default:
				if depth == 0 && (len(starts) == 0 || starts[len(starts)-1] != lineNo) {
					starts = append(starts, lineNo)
				}
				depth++
			}
		}
	}
	for i, line := range lines {
		scan(line, i+1)
	}
	if len(starts) == 0 {
		return nil
	}
	sections := make([]planner.Section, 0, len(starts)+1)
	if starts[0] > 1 {
		sections = append(sections, planner.Section{
			Region: planner.Region{StartLine: 1, EndLine: starts[0] - 1},
			Label:  "(xml declaration/header)",
		})
	}
	for i, start := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		label := strings.TrimSpace(lines[start-1])
		if idx := strings.IndexByte(label, '>'); idx >= 0 && strings.HasPrefix(label, "<") {
			label = label[:idx+1]
		}
		sections = append(sections, planner.Section{
			Region: planner.Region{StartLine: start, EndLine: end},
			Label:  label,
		})
	}
	return sections
}
