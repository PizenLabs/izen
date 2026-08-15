package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/changeset"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/parser"
)

// ── Multi-file $hot — deterministic execution graph (Phase 9B) ──────────────
//
// A $hot request naming two or more explicit @file targets runs as ONE user
// mutation under ONE ExecutionGraph and ONE MutationSet:
//
//	dispatch
//	  → resolve targets (deterministic, dedup, existence)
//	  → begin MutationSet + construct graph
//	  → Phase A (prepare): per node read → generate → extract → validate
//	  → approval surface (N proposals, aggregate +/-)
//	  → Phase B (apply): apply all nodes under the same MutationSet
//	  → verify
//	  → COMMIT (all verified) or ROLLBACK (any failure/cancellation)
//
// The single-file $hot path is untouched: this file only activates when the
// resolver yields ≥2 explicit file targets.

// resolveMultiHotfixTargets deterministically extracts the explicit file
// targets of a $hot prompt. Returns:
//
//   - targets, "", false — ≥2 explicit file targets (multi-file execution)
//   - one target, "", false — exactly one explicit file target (the caller
//     falls back to the existing single-file path)
//   - nil, "", true — no scope tokens at all (the caller falls back to the
//     existing single-file path and its keyword/ambiguity handling)
//   - nil, hardErr, false — deterministic failure (e.g. a named target does
//     not exist), no provider call
//   - nil, "", true with ≥1 non-file scopes — ambiguous target set, no
//     provider call
func resolveMultiHotfixTargets(prompt string) ([]execution.Target, string, bool) {
	ast, err := parser.Parse(prompt, nil)
	if err != nil || ast == nil {
		return nil, "", true
	}
	var filePaths []string
	var scopeCount int
	for _, s := range ast.Scopes {
		scopeCount++
		if s.Type != parser.ScopeFile {
			continue
		}
		p := gateway.CanonicalizeFileName(s.Target)
		if p == "" || p == "." {
			continue
		}
		// Self-patching guard: .izen/ metadata and .patch artifacts are never
		// mutation targets (mirrors the single-file resolver).
		if strings.HasPrefix(p, ".izen/") || strings.Contains(p, "/.izen/") || strings.HasSuffix(p, ".patch") {
			continue
		}
		filePaths = append(filePaths, p)
	}
	// No scopes at all: the single-file keyword/ambiguity path owns this.
	if scopeCount == 0 {
		return nil, "", false
	}
	// Scopes exist but fewer than two resolve to files: either a single-file
	// request (single path) or an ambiguous multi-file request. A request with
	// ≥2 scopes but <2 file targets is ambiguous — stop before the provider.
	if len(filePaths) < 2 {
		if len(filePaths) == 1 && scopeCount == 1 {
			return nil, "", false // exactly one file scope: single-file path
		}
		return nil, "", true // ambiguous target set
	}
	res, rerr := execution.ResolveTargetSet(filePaths, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if rerr != nil {
		return nil, rerr.Error(), false // deterministic failure
	}
	if res.Ambiguous {
		return nil, "", true
	}
	return res.Targets, "", false
}

// runMultiHotfix dispatches a multi-file $hot: it begins the single owning
// MutationSet, constructs the deterministic ExecutionGraph, and runs Phase A
// (prepare) — resolve → read → generate → extract → validate — for every node.
// No filesystem mutation occurs before human approval.
func (m *model) runMultiHotfix(prompt string, targets []execution.Target) tea.Cmd {
	// Stage 1: stash the current plan (mirrors the single-file $hot contract).
	if hasTasks := len(m.sess.CurrentTasks) > 0; hasTasks {
		if err := m.stashPlan(); err != nil {
			m.push(roleError, fmt.Sprintf("[%s] Failed to stash current plan: %v", m.hotfixBrandingLabel(), err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
	}
	m.sess.ClearTasks()
	m.hotfixActive = true
	m.push(roleStatus, fmt.Sprintf("[%s] Multi-file urgent hotfix: %s (%d files)", m.hotfixBrandingLabel(), prompt, len(targets)))

	// ── SINGLE OWNERSHIP + SINGLE MUTATION BOUNDARY ────────────────
	// One user intent → one operation → one MutationSet → one graph.
	m.beginOperation(OpHotfix)
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "multi-hotfix"
	m.push(roleSystem, fmt.Sprintf("  ⚙ Targeting %d files...", len(targets)))
	if m.execEng != nil {
		m.execEng.BeginTransaction()
	}
	ms := m.execEng.MutationSet()
	graph := execution.NewExecutionGraph(m.activeOp.ID, targets, ms)
	graph.Transition(execution.GraphPreparing)
	m.activeGraph = graph
	m.lastExecutionGraph = nil
	// Stage the node tasks into the session ledger so the workflow transition
	// guard (EventBuild requires an authorized plan) admits the apply — the
	// same contract the single-file $hot path satisfies.
	nodes := graph.Nodes
	tasks := make([]plan.Task, 0, len(nodes))
	for i, n := range nodes {
		tasks = append(tasks, plan.Task{StepNum: i + 1, Status: "idle", Type: "FILE_MUTATE", Target: n.Target, Description: prompt})
	}
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
	m.startShimmer("Preparing multi-file hotfix...", "execute")
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "multi-hotfix"} },
		m.proposeMultiHotfixPatch(graph, prompt),
		m.smoothStreamTickCmd(),
		m.shimmerTickCmd(),
	)
}

// proposeMultiHotfixPatch runs Phase A for every node in stable order. Each
// node owns only its own context (its file content + the shared task
// instruction) and gets exactly one provider invocation (plus the single
// truncated-SSE retry the single-file path also permits). Any failure stops
// the graph BEFORE apply — the MutationSet owns nothing yet and is rolled back
// by the handler.
func (m *model) proposeMultiHotfixPatch(graph *execution.ExecutionGraph, prompt string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = multiHotfixProposalMsg{Err: fmt.Errorf("multi-hotfix generation panic: %v", r), Graph: graph}
			}
		}()

		totalIn, totalOut := 0, 0
		var raws []string
		var proposals []SemanticProposal

		for _, node := range graph.Nodes {
			if m.operationContext().Err() != nil {
				graph.State = execution.GraphCancelled
				node.State = execution.NodeCancelled
				return multiHotfixProposalMsg{Err: m.operationContext().Err(), Graph: graph, TokenInput: totalIn, TokenOutput: totalOut}
			}

			// ── read (real work, recorded) ──
			node.State = execution.NodeReading
			m.setStage("read", node.Target, stageRunning)
			var orig string
			if data, rerr := os.ReadFile(node.Target); rerr == nil {
				orig = string(data)
			}
			node.OriginalContent = orig
			m.setStage("read", node.Target, stageDone)

			contract := hotfixContractFor(orig)
			handoff := buildMultiHotfixNodeHandoff(node, prompt)
			system := hotfixSystemPrompt(contract)

			// ── provider invocation (one per node) ──
			node.State = execution.NodeGenerating
			req := ai.Request{
				Model:     m.activeRouteModel(),
				System:    system,
				Stream:    false,
				MaxTokens: 2048,
				Messages:  []ai.Message{{Role: "user", Content: handoff}},
				Reasoning: m.effortFromTasks(),
			}
			timeout := buildGenerationTimeout
			if m.hotfixTimeout > 0 {
				timeout = m.hotfixTimeout
			}
			m.setStage("model", node.Target, stageWaiting)
			ctx, cancel := context.WithTimeout(m.operationContext(), timeout)
			resp, perr := m.provider.Execute(ctx, req)
			cancel()
			m.setStage("model", node.Target, stageDone)
			if perr != nil {
				if isContextCancelled(perr) {
					node.State = execution.NodeCancelled
					graph.State = execution.GraphCancelled
				} else {
					node.State = execution.NodeFailed
					graph.State = execution.GraphFailed
				}
				return multiHotfixProposalMsg{Err: fmt.Errorf("generation failed for %s: %w", node.Target, perr), Graph: graph, TokenInput: totalIn, TokenOutput: totalOut}
			}
			if resp != nil {
				totalIn += resp.TokenInput
				totalOut += resp.TokenOutput
			}

			// ── truncated-SSE retry (stays inside this node, same MutationSet) ──
			if resp != nil && len(resp.ToolCalls) == 0 && responseEffectivelyEmpty(resp) {
				retryReq := ai.Request{
					Model:     m.activeRouteModel(),
					System:    hotfixSystemPrompt(contract),
					Stream:    false,
					MaxTokens: 2048,
					Messages:  []ai.Message{{Role: "user", Content: buildHotfixFallbackHandoff(nodeAsTask(node), orig, contract, nil)}},
					Reasoning: m.effortFromTasks(),
				}
				m.setStage("model", node.Target, stageWaiting)
				retryCtx, retryCancel := context.WithTimeout(m.operationContext(), timeout)
				retryResp, retryErr := m.provider.Execute(retryCtx, retryReq)
				retryCancel()
				m.setStage("model", node.Target, stageDone)
				if retryErr == nil && retryResp != nil {
					resp = retryResp
					totalIn += retryResp.TokenInput
					totalOut += retryResp.TokenOutput
				}
			}

			if resp == nil || strings.TrimSpace(resp.Content) == "" {
				node.State = execution.NodeFailed
				graph.State = execution.GraphFailed
				return multiHotfixProposalMsg{Err: fmt.Errorf("provider returned empty response for %s", node.Target), Graph: graph, TokenInput: totalIn, TokenOutput: totalOut}
			}
			if resp != nil {
				raws = append(raws, resp.Content)
			}

			// ── artifact extraction + validation ──
			m.setStage("patch", node.Target, stageRunning)
			diffContent, patch, aerr := m.multiHotfixArtifactFor(node, orig, resp, prompt, contract)
			if aerr != nil {
				node.State = execution.NodeFailed
				graph.State = execution.GraphFailed
				return multiHotfixProposalMsg{Err: fmt.Errorf("patch extraction failed for %s: %w", node.Target, aerr), Graph: graph, TokenInput: totalIn, TokenOutput: totalOut}
			}
			node.Patch = patch
			node.State = execution.NodeArtifact
			node.Evidence = execution.MutationEvidence{
				Stage:           execution.StageArtifact,
				File:            node.Target,
				ArtifactPresent: true,
				DiffPresent:     diffContent != "",
			}
			m.setStage("patch", node.Target, stageDone)

			proposals = append(proposals, SemanticProposal{
				ID:       "hotfix-" + node.ID,
				Target:   SemanticTarget{QualifiedName: node.Target, Module: filepath.Dir(node.Target), Language: langFromPath(node.Target)},
				Diff:     diffContent,
				Patch:    patch,
				Expanded: true,
			})
		}

		// Phase A complete: every node has a validated artifact.
		graph.Transition(execution.GraphReady)
		for _, n := range graph.Nodes {
			n.State = execution.NodeReady
		}
		return multiHotfixProposalMsg{
			Graph:       graph,
			Proposals:   proposals,
			TokenInput:  totalIn,
			TokenOutput: totalOut,
			RawOutput:   strings.Join(raws, "\n"),
		}
	}
}

// multiHotfixArtifactFor mirrors the single-file extraction contract for one
// graph node: new files use the bounded whole-file rewrite; existing files run
// through the changeset pipeline (the SINGLE authoritative diff source), with
// the deterministic fuzzy fallback on pipeline pause.
func (m *model) multiHotfixArtifactFor(node *execution.ExecutionNode, orig string, resp *ai.Response, prompt string, contract hotfixContract) (string, *execution.Patch, error) {
	rawContent := ""
	if resp != nil {
		rawContent = resp.Content
	}
	// New / empty file: the code block IS the complete new file content.
	if orig == "" {
		var resolved string
		if extracted, ok := execution.ExtractRawCodeBlock(rawContent); ok {
			resolved = execution.SanitizeRawCodeBlock(extracted)
		} else if extracted, ok := execution.ExtractCodeBlockContent(rawContent); ok {
			resolved = execution.SanitizeRawCodeBlock(extracted)
		} else {
			resolved = execution.SanitizeRawCodeBlock(rawContent)
		}
		diff := computeUnifiedDiff(node.Target, orig, resolved)
		return diff, &execution.Patch{
			ID:            "hotfix-" + node.ID,
			File:          node.Target,
			Original:      orig,
			Modified:      resolved,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: true,
		}, nil
	}
	// Existing file: the changeset pipeline compiles the authoritative diff.
	compiled, pipeErr := changeset.NewPipeline().Run(rawContent, node.Target, []byte(orig))
	if pipeErr == nil && len(compiled) > 0 {
		cc := compiled[0]
		if cc.Validation.Valid {
			diff := string(cc.Diff)
			return diff, &execution.Patch{
				ID:        "hotfix-" + node.ID,
				File:      node.Target,
				Original:  orig,
				Modified:  diff,
				ContextID: m.sess.ContextID,
			}, nil
		}
	}
	// Pipeline pause / invalid patch: deterministic fuzzy fallback, then fail.
	if modified, ok := execution.ApplyFuzzyStringReplace(orig, prompt, node.Target); ok {
		diff := computeUnifiedDiff(node.Target, orig, modified)
		return diff, &execution.Patch{
			ID:            "hotfix-" + node.ID,
			File:          node.Target,
			Original:      orig,
			Modified:      modified,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: true,
		}, nil
	}
	if pipeErr != nil {
		return "", nil, pipeErr
	}
	return "", nil, fmt.Errorf("changeset pipeline could not map model output to %s", node.Target)
}

// buildMultiHotfixNodeHandoff constructs the per-node context handoff. Each
// node receives ONLY its own target file content plus the shared task
// instruction — no other node's content crosses, so no context is duplicated
// inside a single provider request.
func buildMultiHotfixNodeHandoff(node *execution.ExecutionNode, prompt string) string {
	var b strings.Builder
	b.WriteString("## MULTI-FILE HOTFIX TASK\n")
	b.WriteString("You are fixing ONE of several files. Change ONLY this target file.\n\n")
	b.WriteString("### TARGET FILE\n")
	b.WriteString(node.Target + "\n\n")
	if node.OriginalContent != "" {
		b.WriteString("### TARGET_FILE_CONTENT (reference only)\n```\n" + node.OriginalContent + "\n```\n\n")
	}
	b.WriteString("### TASK\n")
	b.WriteString(prompt + "\n\n")
	cb := "```"
	b.WriteString("### OUTPUT CONTRACT\n")
	b.WriteString("Output ONLY the exact lines that must change for THIS file — the corrected snippet — inside a single markdown code block (e.g. " + cb + "html ... " + cb + "). ")
	b.WriteString("Do NOT output other files. Do NOT reproduce unchanged lines. ")
	b.WriteString("Match the surrounding formatting exactly so the change can be located. ")
	b.WriteString("Do NOT use SEARCH/REPLACE blocks or unified diff format.\n\n")
	fmt.Fprintf(&b, "CURRENT_YEAR: %d\n", time.Now().Year())
	return strings.TrimSpace(b.String())
}

// nodeAsTask adapts an execution node to the plan.Task shape reused by the
// existing fallback handoff builder.
func nodeAsTask(node *execution.ExecutionNode) *plan.Task {
	if node == nil {
		return nil
	}
	return &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: node.Target, Description: ""}
}

// applyMultiHotfixGraph runs Phase B: it applies every node's validated
// artifact under the graph's single MutationSet, then returns ONE terminal
// buildResultMsg. Any apply/verification failure rolls the WHOLE set back via
// the existing hotfix terminal (buildResultMsg exitCode != 0 → rollback).
func (m *model) applyMultiHotfixGraph(graph *execution.ExecutionGraph) tea.Cmd {
	// Single ownership: the apply is a fresh operation but reuses the SAME
	// MutationSet the graph began at dispatch — never a second boundary.
	m.beginOperation(OpHotfix)
	m.agentRunning = true
	m.pipelineRunning = true
	m.agentDone = false
	m.agentLabel = "multi-hotfix apply"
	graph.Transition(execution.GraphApplying)
	m.syncUIState()
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = buildResultMsg{output: "", exitCode: -1, err: fmt.Errorf("multi-hotfix apply panic: %v", r)}
			}
		}()
		if err := m.transitionToBuilding(); err != nil {
			return buildResultMsg{output: "", exitCode: -1, err: fmt.Errorf("multi-hotfix workflow transition: %w", err)}
		}
		targets := make([]string, 0, len(graph.Nodes))
		for _, n := range graph.Nodes {
			targets = append(targets, n.Target)
		}
		if err := m.authorizeBuildExecution(targets, true); err != nil {
			return buildResultMsg{output: "", exitCode: -1, err: fmt.Errorf("multi-hotfix authorization failed: %w", err)}
		}
		if !graph.HasAllArtifacts() {
			graph.State = execution.GraphFailed
			return buildResultMsg{output: "", exitCode: 1, err: fmt.Errorf("multi-hotfix apply aborted: graph is missing artifacts")}
		}
		for _, node := range graph.Nodes {
			if m.operationContext().Err() != nil {
				node.State = execution.NodeCancelled
				graph.State = execution.GraphCancelled
				return buildResultMsg{output: "", exitCode: 1, err: m.operationContext().Err()}
			}
			m.setStage("apply", node.Target, stageRunning)
			node.State = execution.NodeApplying
			applyErr := m.applyPatchWithDeadline(node.Patch)
			if applyErr != nil {
				node.State = execution.NodeFailed
				graph.State = execution.GraphFailed
				return buildResultMsg{output: "", exitCode: 1, err: fmt.Errorf("multi-hotfix apply failed at %s: %w", node.Target, applyErr)}
			}
			node.State = execution.NodeVerified
			node.Evidence = multiHotfixNodeEvidence(node)
			m.setStage("apply", node.Target, stageDone)
		}
		graph.Transition(execution.GraphVerifying)
		return buildResultMsg{
			output:   fmt.Sprintf("Applied multi-file hotfix to %d files", len(graph.Nodes)),
			exitCode: 0,
		}
	}
}

// multiHotfixNodeEvidence assembles the per-node semantic mutation evidence
// after a successful apply. It reuses the existing MutationEvidence vocabulary.
func multiHotfixNodeEvidence(node *execution.ExecutionNode) execution.MutationEvidence {
	ev := execution.MutationEvidence{
		Stage:              execution.StageResult,
		File:               node.Target,
		ArtifactPresent:    node.Patch != nil && node.Patch.Modified != "",
		DiffPresent:        node.Patch != nil && strings.Contains(node.Patch.Modified, "@@"),
		ApplyExecuted:      true,
		FilesystemChanged:  true,
		VerificationRun:    true,
		VerificationPassed: true,
		Outcome:            execution.OutcomeChanged,
	}
	if node.Patch != nil {
		ev.DiffAdds, ev.DiffRemoves = countLinesDelta(node.Patch.Modified)
	}
	return ev
}

// rejectMultiHotfix is the Alt+R rejection terminal for a staged multi-file
// proposal. Nothing has been applied — the graph and its proposals are
// discarded and the stashed plan restored.
func (m *model) rejectMultiHotfix() {
	m.pendingHotfixGraph = nil
	m.activeGraph = nil
	m.pendingProposals = nil
	m.hotfixActive = false
	m.resolveApprovalState()
	m.ti.Focus()
	m.recalcViewportHeight()
	m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Rejected — multi-file hotfix aborted. No files were modified."))
	if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
		m.sess.StageTaskList(&stashedTasks)
		_ = m.sess.Save()
	}
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// terminalizeMultiHotfixGraph is the single terminal path for the multi-file
// graph at the buildResultMsg hotfix terminal. It:
//
//   - sets the graph + node terminal states from the real outcome,
//   - assembles the multi-file ExecutionProof (per-node evidence + MutationSet
//     terminal state),
//   - retains the graph for $inspect,
//   - clears the pending/active graph pointers.
func (m *model) terminalizeMultiHotfixGraph(success bool, outcome OperationOutcome) {
	graph := m.activeGraph
	if graph == nil {
		return
	}
	switch {
	case success:
		graph.Transition(execution.GraphCommitted)
		for _, n := range graph.Nodes {
			if !n.State.Terminal() {
				n.State = execution.NodeVerified
			}
		}
	case outcome == OpOutcomeCancelled:
		graph.State = execution.GraphCancelled
		for _, n := range graph.Nodes {
			if !n.State.Terminal() {
				n.State = execution.NodeCancelled
			}
		}
	default:
		if graph.State == execution.GraphFailed {
			// keep failed; nodes already marked
		} else {
			graph.State = execution.GraphRolledBack
		}
		for _, n := range graph.Nodes {
			if !n.State.Terminal() {
				n.State = execution.NodeCancelled
			}
		}
	}
	m.completeMultiHotfixProof(success, graph)
	m.lastExecutionGraph = graph
	m.pendingHotfixGraph = nil
	m.activeGraph = nil
	// ── STRATEGY GRAPH (Phase 11) ────────────────────────────────────
	// The compiled execution graph records the multi-file apply terminal from
	// the real outcome: committed (mutate+verify complete), rolled back (mutate
	// failed) or cancelled. No-op outside the strategy layer.
	switch {
	case success:
		m.recordStrategyGraphMutation(true)
	case outcome == OpOutcomeCancelled:
		m.cancelStrategyGraph("multi-file apply cancelled by the runtime")
	default:
		m.recordStrategyGraphMutation(false)
	}
}

// multiHotfixProposalSummary renders the compact aggregate approval header.
func multiHotfixProposalSummary(proposals []SemanticProposal) string {
	adds, removes := 0, 0
	for _, p := range proposals {
		a, r := countLinesDelta(p.Diff)
		adds += a
		removes += r
	}
	return fmt.Sprintf("Proposed changes · %d files  (+%d -%d)", len(proposals), adds, removes)
}
