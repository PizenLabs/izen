package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/command"
	"github.com/PizenLabs/izen/internal/config"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	objengine "github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
)

var validSystemCommands = map[string]struct{}{
	"/help":       {},
	"/?":          {},
	"/quit":       {},
	"/usage":      {},
	"/provider":   {},
	"/model":      {},
	"/objective":  {},
	"/clear":      {},
	"/drop":       {},
	"/undo":       {},
	"/commit":     {},
	"/checkpoint": {},
	"/arch":       {},
}

// ansiRe strips terminal ANSI escape color codes (e.g. \x1b[31m) that can
// corrupt regex-based stack frame parsers in auto-trace.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// inputANSIRe strips ALL escape sequences from the interactive text-input
// buffer, including SGR mouse-tracking reports (\x1b[<0;26;37M / \x1b[<...m)
// and trailing coordinate remnants. Bubble Tea parses genuine mouse events
// into tea.MouseMsg before they reach the textinput, so under normal operation
// nothing leaks — but a defensive strip here guarantees no raw terminal escape
// can ever pollute the editable command buffer (e.g. during /build shell
// execution context switches where raw-mode state could briefly differ).
var inputANSIRe = regexp.MustCompile(`\x1b\[[<?][0-9;]*[a-zA-Z]`)

// sanitizeInputBuffer strips ANSI / mouse-tracking escape sequences from a
// string so it is safe to store in the prompt's text buffer.
func sanitizeInputBuffer(s string) string {
	return inputANSIRe.ReplaceAllString(s, "")
}

// stashedPlanPath is the deterministic cache file path where the active /build
// plan is serialized before a $hot hotfix execution. The Go engine restores
// from this file after the hotfix completes — the LLM never sees the stash,
// preventing 7B context drift across urgent interventions.
const stashedPlanPath = ".izen/stashed_plan.json"

// stashPlan serializes the current /build task queue to a static cache file so
// it can be restored deterministically after a $hot hotfix completes. Returns
// nil if there are no tasks to stash (no-op).
func (m *model) stashPlan() error {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize plan: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(stashedPlanPath), 0755); err != nil {
		return fmt.Errorf("create .izen: %w", err)
	}
	return os.WriteFile(stashedPlanPath, data, 0644)
}

// restorePlan reads the stashed plan from the deterministic cache file and
// re-hydrates the active /build execution queue. The cache file is deleted
// after a successful read. Returns nil, nil if no stash exists.
func (m *model) restorePlan() ([]plan.Task, error) {
	data, err := os.ReadFile(stashedPlanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stashed plan: %w", err)
	}
	var tasks []plan.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parse stashed plan: %w", err)
	}
	// Delete the stash file immediately after successful read so the LLM
	// never sees it — the restoration is purely a Go-level operation.
	_ = os.Remove(stashedPlanPath)
	return tasks, nil
}

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// Clear any stale error bar on new user input
	m.lastApplyError = ""

	// Rigid active guards to block spamming inputs during background processes
	if m.streaming || m.agentRunning {
		m.push(roleSystem, "Input blocked: task active.")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// Safety gate confirmation: pending test/run confirmation for large repos
	if m.pendingTestConfirm {
		return m.handleReviewTestConfirm(line)
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		currentMode := m.resolver.Current()
		if !currentMode.CanShell() {
			m.push(roleError, fmt.Sprintf("shell execution blocked in /%s mode (no CapShell)", currentMode))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		// ── Shell Guard Rail: Security-aware command firewall ──
		if blocked, _ := m.shellFirewall(shellCmd); blocked {
			m.reviewRunning = false
			m.agentRunning = false
			m.agentLabel = ""
			m.push(roleError, fmt.Sprintf("[SECURITY ALERT] Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.", shellCmd))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		m.push(roleSystem, "$ "+shellCmd)
		out, err := execShell(shellCmd)
		if err != nil {
			m.push(roleError, err.Error())
		}
		scanner := bufio.NewScanner(strings.NewReader(strings.TrimRight(out, "\r\n")))
		for scanner.Scan() {
			m.push(roleSystem, scanner.Text())
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── Composite fast-query routing: /review $test ───────────────────
	// MUST be evaluated at the very top of the evaluation tree, strictly
	// before parseModeShorthand (which would otherwise match the "/review "
	// prefix and route this to a plain static /review, silently bypassing the
	// dynamic-test-then-review composite shortcut).
	if command.IsReviewTestComposite(line) {
		m.push(roleSystem, accentStyle.Render("⚡ [IZEN Shortcut] Running dynamic test suite before auditing commit risks..."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.runReviewTestComposite()
	}

	// ── $prompt — GLOBAL MODE-GUARD ROUTER TO /ask ───────────────────────
	// $prompt is a global routing entry point, not an execution mode. From
	// ANY active mode it transitions cleanly to /ask, injecting the query as
	// /ask input for structured Forensic Context Ledger generation. It MUST
	// NEVER execute /build, /review, /plan, or /investigate logic inside the
	// originating mode — the only allowed action is the transition to /ask.
	if line == "$prompt" || strings.HasPrefix(line, "$prompt ") {
		m.cancelStaleAgentOps()
		if line == "$prompt" {
			m.push(roleError, "[Usage] $prompt <your raw architectural idea or description>")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		rawInput := strings.TrimSpace(line[8:])

		// ── INTENT PRE-GUARD: Fast-track direct file mutations ──────────
		// Inspect the raw input before dispatching to the Senior Architect
		// pipeline. If the user is requesting a simple single-file mutation
		// on a non-code file (e.g. $prompt rename author in @LICENSE),
		// classify it and route directly to /build as a FILE_MUTATE task
		// with zero LLM involvement — no forensic analysis, no go test.
		if target, isDirect := gateway.ClassifyDirectMutation(rawInput); isDirect {
			m.push(roleSystem, accentStyle.Render("[Fast-Track] Direct file mutation detected. Bypassing architect analysis."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			tasks := command.GenerateFallbackPlan(target)
			return func() tea.Msg {
				return planResultMsg{
					Tasks:       tasks,
					IsFastTrack: true,
				}
			}
		}

		currentMode := m.resolver.Current()
		if currentMode != modes.ModeAsk {
			// Mode Guard Enforced: request state transition to /ask, then
			// queue the $prompt synthesis directly via runAskPromptHandoffCmd.
			// This preserves the Senior Architect system template
			// (AskPromptHandoffContract) — we MUST NOT re-enter handleInput
			// because the raw input no longer carries the $prompt prefix and
			// would be routed to the normal AskContract() streaming path,
			// producing conversational noise instead of the structured
			// 5-point Forensic Context Ledger.
			m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
				"$prompt from /%s — transitioning to /ask for structured analysis...", currentMode)))
			m.modeChangeAuthorized = true
			cmd := m.setMode(modes.ModeAsk)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return tea.Batch(cmd, m.runAskPromptHandoffCmd(rawInput))
		}

		m.push(roleSystem, infoStyle.Render("Refining architectural idea through Senior Architect analysis..."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.runAskPromptHandoffCmd(rawInput)
	}

	// $ sub-command prefix — delegates to handleReviewDollar for routing.
	if strings.HasPrefix(line, "$") {
		// ANTI-DEADLOCK: unconditionally sanitize stale execution flags
		// before spawning any background task. Prevents ghost spinner lock
		// when sequential $ commands are issued without a clean reset.
		cmd := m.handleReviewDollar(line)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return cmd
	}

	if mode, content, ok := parseModeShorthand(line); ok {
		m.modeChangeAuthorized = true
		if content != "" {
			m.setMode(mode)
			return m.handleMessageContent(content)
		}
		if mode == modes.ModeReview {
			m.setMode(mode)
			m.push(roleSystem, infoStyle.Render("Running review pipeline..."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m.runReviewCmd("")
		}
		return m.setMode(mode)
	}

	if strings.HasPrefix(line, "/") {
		return m.handleCommand(line)
	}

	if m.resolver.Current() == modes.ModeBuild {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "run" {
			var stepNum int
			if len(fields) >= 2 {
				stepNum, _ = strconv.Atoi(fields[1])
			}
			return m.handleBuildRun(stepNum)
		}

		// DEFAULT FEEDBACK: amend a failed/stalled task without stashing.
		// If the last task was rejected or failed, the user's text is routed
		// as an amendment (appended to the task description) and the task is
		// reset to "idle" for re-execution. This replaces the old behavior of
		// stubbornly re-running the exact same failed command.
		//
		// If no task is failed/stalled, execution falls through to normal chat.
		if failedStep := m.findFailedBuildTask(); failedStep > 0 {
			m.push(roleStatus, fmt.Sprintf("Amending task %d with feedback: %s", failedStep, line))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m.amendBuildTask(failedStep, line)
		}
	}

	// ── SYNCHRONOUS STATE COMMIT (1-TURN LATENCY FIX) ────────────────
	// Persist the freshly captured user input to the session history and disk
	// BEFORE launching the LLM stream. This guarantees the in-memory + on-disk
	// state leads the API dispatch (Write-After-Read ordering): the model never
	// receives a turn that is one query behind because the current input is
	// committed first, not retrofitted at stream completion.
	m.sess.AddMessage("user", line, 5)
	_ = m.sess.Save()

	return m.handleMessageContent(line)
}

func (m *model) handleMessageContent(line string) tea.Cmd {
	var fileCtx strings.Builder
	var refFiles []string
	var trace *ctxpkg.CodebaseTrace
	for _, field := range strings.Fields(line) {
		if !strings.HasPrefix(field, "@") {
			continue
		}
		ref := filepath.Clean(field[1:])
		if ref == "" || ref == "." {
			continue
		}
		refFiles = append(refFiles, ref)
	}
	refFiles = append(refFiles, m.pendingFileRefs...)
	m.pendingFileRefs = nil

	if m.graph != nil && len(refFiles) > 0 {
		cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
		renderer := ctxpkg.DefaultRenderer()
		seen := make(map[string]bool)
		for _, ref := range refFiles {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			symName := filepath.Base(ref)
			symExt := filepath.Ext(symName)
			if symExt != "" {
				symName = strings.TrimSuffix(symName, symExt)
			}
			depCtx := cb.BuildDependencySlice(symName)
			if len(depCtx.Files) == 0 {
				fn := m.graph.LookupFile(ref)
				if fn != nil {
					fs := ctxpkg.CompressFile(fn, 30)
					depCtx.Files = append(depCtx.Files, fs)
				}
			}
			if len(depCtx.Files) > 0 {
				if fileCtx.Len() > 0 {
					fileCtx.WriteString("\n")
				}
				fileCtx.WriteString(renderer.Render(depCtx))
				if depCtx.Trace != nil {
					trace = depCtx.Trace
				}
			}
			// Force sync: always include CURRENT file content read fresh from disk
			// so the AI sees the exact byte content rather than relying on cached
			// graph metadata alone.
			data, err := os.ReadFile(ref)
			if err == nil {
				ext := filepath.Ext(ref)
				lang := strings.TrimPrefix(ext, ".")
				// REFORM B: Anti-prompt injection — strip legacy comments/TODOs
				// from source code before feeding to LLM. This prevents stale
				// developer notes in the codebase from hijacking the agent's
				// attention away from the actual task.
				sanitized := ctxpkg.SanitizeSourceForLLM(string(data), lang)
				lines := strings.Split(sanitized, "\n")
				if len(lines) > 50 {
					lines = lines[:50]
				}
				if fileCtx.Len() > 0 {
					fileCtx.WriteString("\n\n")
				}
				fmt.Fprintf(&fileCtx, "## Current Content of: %s\n```%s\n%s\n```",
					ref, lang, strings.Join(lines, "\n"))
			}
		}
	} else if len(refFiles) > 0 {
		for _, ref := range refFiles {
			data, err := os.ReadFile(ref)
			if err != nil {
				continue
			}
			if fileCtx.Len() > 0 {
				fileCtx.WriteString("\n\n")
			}
			ext := filepath.Ext(ref)
			lang := strings.TrimPrefix(ext, ".")
			// REFORM B: Sanitize source to remove prompt-injection comments
			sanitized := ctxpkg.SanitizeSourceForLLM(string(data), lang)
			fmt.Fprintf(&fileCtx, "File: %s\n```%s\n%s\n```", ref, lang, sanitized)
		}
	}

	// Inject semantic mapping rules for legal/text files to guide local SLMs
	// that struggle with author/copyright targeting in LICENSE documents.
	if fileCtx.Len() > 0 {
		ctxStr := fileCtx.String()
		lowerCtx := strings.ToLower(ctxStr)
		if strings.Contains(lowerCtx, "license") || strings.Contains(lowerCtx, "readme") {
			semanticRule := `[SEMANTIC MAPPING RULE]: In legal text/LICENSE documents, the "Author", "Holder", or "Organization" corresponds specifically to the string immediately following the "Copyright (c) <Year>" marker. You must strictly target your line mutation to THAT specific line. Do not alter any other paragraph.`
			fileCtx.WriteString("\n\n" + semanticRule)
		}
	}

	line = m.expandFileRefs(line)

	content := strings.TrimSpace(line)
	if fileCtx.Len() > 0 {
		content = fileCtx.String() + "\n\n" + content
	}

	if m.resolver.Current() == modes.ModeBuild && m.graph != nil {
		compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.ObjectiveIntent())
		compressed := compressor.CompressLines(content)
		if compressed != "" && compressed != content {
			content = retrieval.FormatCompressedFrame(compressed) + "\n\n" + content
		}
		// Capture snapshot for background goroutine to avoid data race
		// on m.graph when the main loop assigns a new graph.
		g := m.graph
		go retrieval.BuildGlobalCompressor(g, m.sess.ObjectiveIntent())
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		if m.investigateInvocationCount >= maxInvestigateInvocations {
			m.push(roleError, fmt.Sprintf("max investigate invocations (%d) reached", maxInvestigateInvocations))
			m.push(roleSystem, infoStyle.Render("start a new session with /objective <desc> or restart"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		// Graceful handoff guard: if the ContextLedger's ask_handoff payload
		// was cleared (e.g. by /clear) and no other handoff context exists,
		// prompt for input rather than running the engine with stale or empty
		// content. This prevents silent degradation on the local model.
		trimmed := strings.TrimSpace(content)
		hasHandoff := m.handoffLedgerContent != "" ||
			m.handoffCtx.LastFailurePayload != "" ||
			m.handoffCtx.ProposedFix != ""
		if !hasHandoff && m.sess != nil && m.sess.ContextLedger != nil {
			l := m.sess.ContextLedger
			hasHandoff = l.Diagnostics != "" || len(l.Packets) > 0
		}
		if !hasHandoff && (trimmed == "" || len(trimmed) < 15) {
			m.push(roleSystem, infoStyle.Render("No handoff context in ledger. Describe what to investigate (e.g. a test failure, error log, or crash report):"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		m.investigateInvocationCount++
		return m.runInvestigateCmd(content)
	case modes.ModeReview:
		trimmed := strings.TrimSpace(content)

		target := ""
		if strings.HasPrefix(strings.ToLower(trimmed), "check ") {
			target = strings.TrimSpace(trimmed[6:])
		}
		return m.runReviewCmd(target)
	case modes.ModePlan:
		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)

		// ── STRUCTURAL ENGINE PATH (Handoff from /investigate) ──────────
		// When the Context-Ledger or a proposed fix is present, bypass the
		// conversational streaming path entirely. Call the PlanEngine with
		// structured JSON output enforcement, then stage the parsed tasks
		// directly into the session.
		ledgerContent := ctxpkg.SanitizeLedger(m.handoffLedgerContent)
		proposedFix := m.handoffCtx.ProposedFix
		handoffSource := ledgerContent
		if handoffSource == "" {
			handoffSource = proposedFix
		}

		// ── ANTI-WIPEOUT FALLBACK ───────────────────────────────────────
		// The live handoff (handoffLedgerContent / ProposedFix) can be empty
		// after a plan rejection or an environmental correction (e.g. the user
		// clarifies "this is macOS, not Linux"). That MUST NOT discard the
		// authoritative root-cause diagnostics held in the session ContextLedger.
		// When the live handoff is empty but the ledger still carries the
		// diagnostic payload, repopulate handoffSource from the ledger so the
		// compilation/dependency error survives the mode transition instead of
		// crashing the engine with a false "data flow regression".
		if handoffSource == "" && m.sess.ContextLedger != nil {
			l := m.sess.ContextLedger
			if l.Diagnostics != "" {
				handoffSource = ctxpkg.SanitizeLedger(l.Diagnostics)
			}
			if handoffSource == "" {
				if packets := l.FormatPacketsForPlan(); packets != "" {
					handoffSource = packets
				}
			}
		}

		// SAFETY GUARD: only fires when there is genuinely no material to
		// synthesize from — not when a plan was rejected/corrected and the
		// diagnostics simply live in the ContextLedger (handled above).
		if handoffSource == "" && m.sess.ContextLedger != nil && m.sess.ContextLedger.Diagnostics != "" {
			m.push(roleError, "[SYSTEM ERROR] Context ledger has diagnostics but handoff source is empty after sanitization. Data flow regression detected.")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		if handoffSource != "" {
			if m.planEngine == nil {
				m.handoffLedgerContent = ""
				m.handoffCtx.ProposedFix = ""
				m.handoffCtx.PendingTodos = nil
				m.push(roleError, "plan engine not configured")
				m.resetStreamingState()
				m.refreshViewportContent()
				return m.flushPendingRecords()
			}

			problem := m.handoffCtx.LastFailurePayload
			if problem == "" {
				problem = m.sess.ObjectiveIntent()
			}
			if problem == "" {
				problem = "Investigation results require structured execution plan"
			}

			// Reset handoff triggers so the async result cannot re-enter this
			// path. The synthesized tasks are applied in planResultMsg handler.
			m.handoffLedgerContent = ""
			m.handoffCtx.ProposedFix = ""
			m.handoffCtx.PendingTodos = nil

			// Keep the UI alive: show a live spinner while the (potentially
			// slow) LLM call runs in a background goroutine. This MUST NOT
			// block the Bubble Tea event loop — ProcessFromLedger executes
			// inside runPlanEngineCmd, not here.
			m.streaming = true
			m.spinnerFrame = 0
			m.lastSpinnerAdvance = time.Time{}
			m.agentRunning = true
			m.agentLabel = "synthesizing plan"
			m.planPending = true
			m.planStartedAt = time.Now()
			m.push(roleSystem, infoStyle.Render("Synthesizing structured execution plan from investigation data..."))
			// FAST-TRACK NOTICE: when there are zero pre-parsed TODOs the
			// synthesis runs purely on the forensic ledger. Surface an implicit
			// hint so the user understands the engine is working (not hung) and
			// that a first-token guard will bail fast if the local model is
			// unresponsive.
			if len(m.handoffCtx.PendingTodos) == 0 {
				m.push(roleSystem, mutedStyle.Render(
					"0 pending TODOs — synthesizing from forensic ledger. If your local model is stuck, this aborts within ~8s instead of hanging."))
			}
			m.refreshViewportContent()
			m.Viewport.GotoBottom()

			// Start the smooth tick loop. It repaints the viewport AND (since
			// the frozen-spinner fix) physically advances m.spinnerFrame while
			// m.agentRunning/m.streaming stay set, so the braille indicator
			// animates even though plan synthesis emits a single terminal
			// planResultMsg rather than a token stream. The loop self-terminates
			// once the planResultMsg handler clears the flags.
			return tea.Batch(
				m.flushPendingRecords(),
				m.smoothStreamTickCmd(),
				m.planSlowNoticeCmd(),
				m.runPlanEngineCmd(handoffSource, problem, m.cfg.ActiveModelName(), m.handoffCtx),
			)
		}

		// ── CONVERSATIONAL STREAMING PATH (Manual /plan usage) ──────────
		// Only reached when no investigation handoff exists (no handoffLedgerContent
		// and no ProposedFix). The structural engine path above always terminates
		// with either staged tasks or an explicit diagnostic — never falls through.
		cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
		assembly := cb.BuildPlanAssembly(content, m.attachedFiles)

		// SAFETY GUARD: Prevent empty prompt to LLM. If the ContextLedger has
		// diagnostics loaded but the generated prompt is empty, this indicates
		// a data flow regression that must be surfaced immediately.
		if assembly.RawContext == "" && m.sess.ContextLedger != nil && m.sess.ContextLedger.Diagnostics != "" {
			m.push(roleError, "[SYSTEM ERROR] Context ledger has diagnostics but generated prompt is empty. This indicates a data flow regression.")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		// ── EMPTY-HANDOFF GUARD (mirror of /build's zero-task guard) ────────
		// Reaching here means the structural engine path was skipped because
		// there was NO handoff ledger content and NO proposed fix. If there is
		// ALSO no diagnostics in the ledger and the conversational assembly is
		// empty and the user typed no objective, then there is genuinely
		// nothing to synthesize a plan from. Previously this fell through to
		// streamCmd("") which returns nil silently — the spinner never starts,
		// but the user is left at the prompt with zero feedback, which reads
		// exactly like the reported "hang". Surface a clean, actionable notice
		// and return control to the prompt instead of firing an empty request.
		//
		// NOTE: we intentionally do NOT gate on PendingTodos count. Zero
		// pending TODOs is the HEALTHY state for a /investigate → /plan handoff
		// (the forensic ledger, not pre-parsed TODOs, drives synthesis), so
		// blocking on that would break every valid handoff.
		if m.planHasNothingToSynthesize(assembly.RawContext, content) {
			m.push(roleSystem, infoStyle.Render("No context packets found in ledger. Run /investigate or $test first, then /plan to synthesize an execution plan."))
			m.reconcileSpinner()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m.flushPendingRecords()
		}

		modelName := m.cfg.ActiveModelName()
		if budgetErr := plan.CheckTokenBudget(modelName, assembly.EstimateTokens); budgetErr != nil {
			m.push(roleError, budgetErr.Error())
			m.push(roleSystem, infoStyle.Render(budgetErr.BudgetActionHint()))
			return nil
		}

		// ── MODE BOUNDARY LAW: /plan performs NO heavy semantic scanning ──
		// /plan is a pure, deterministic translator of structured diagnostic
		// data into atomic human tasks. It must NEVER trigger an automatic
		// `lx search` or any semantic text-retrieval mechanism — that duty
		// belongs exclusively to /investigate. The structural plan assembly
		// (graph symbols / attached files) is sufficient; any remote search
		// would both hang the viewport and leak /investigate's cognitive role.
		// Intentionally left as a no-op: do NOT re-add retrieval.SearchWithExtraction here.
		_ = content

		planTrace := &ctxpkg.CodebaseTrace{}
		for _, sf := range assembly.SymbolFiles {
			planTrace.MatchedFiles = append(planTrace.MatchedFiles, sf.Path)
			for _, sym := range sf.Symbols {
				planTrace.ResolvedSymbols = append(planTrace.ResolvedSymbols, sym.Name)
			}
		}
		return tea.Batch(
			func() tea.Msg { return traceUpdateMsg{trace: planTrace} },
			m.streamCmd(assembly.RawContext),
		)
	default:
		// ── /build mode boundary: strict structural-only execution ─────────
		// /build is a deterministic executor. It runs EXCLUSIVELY on the atomic
		// structural tasks staged by /plan (m.handoffCtx.PendingTodos and
		// m.sess.CurrentTasks). It must never process the stale conversational
		// log carried in raw input buffers or unstructured message history —
		// doing so re-injects past test failures / greetings into the build
		// engine (the zombie-data / stale-context bug). When no tasks are
		// staged, block immediately instead of contaminating the executor.
		if m.resolver.Current() == modes.ModeBuild {
			return m.runBuildCmd(content)
		}

		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)

		// ── ISOLATION BARRIER: Normal /ask chat vs $prompt handoff ────────
		// If the user is typing a normal chat message in /ask mode, clear any
		// residual action chip from a previous $prompt turn so it does not
		// render alongside the stream response. The lightweight streaming path
		// uses AskContract() — never AskPromptHandoffContract() — ensuring
		// zero system-prompt contamination between the two workflows.
		if m.resolver.Current() == modes.ModeAsk {
			m.currentResult = nil
		}

		if m.resolver.Current() == modes.ModeAsk && len(refFiles) == 0 {
			result := retrieval.RouteAsk(line, m.gitEng)
			if len(result.Targets) > 0 && m.graph != nil {
				cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
				ctx := cb.Build(ctxpkg.BuildRequest{
					Files:      result.Targets,
					MaxFiles:   len(result.Targets),
					MaxSymbols: 20,
				})
				if ctx != nil && len(ctx.Files) > 0 {
					header := fmt.Sprintf("### LOCALIZED CONTEXT (%s)\n\n", result.Label)
					content = header + ctxpkg.DefaultRenderer().Render(ctx) + "\n" + content
					if ctx.Trace != nil {
						trace = ctx.Trace
					}
				}
			}
		}

		if trace != nil {
			return tea.Batch(
				func() tea.Msg { return traceUpdateMsg{trace: trace} },
				m.streamCmd(content),
			)
		}
		return m.streamCmd(content)
	}
}

// planFirstTokenTimeout bounds how long the LLM provider may take to return its
// FIRST chunk of plan synthesis. A local model that is OOM/stalling will hang
// the connection indefinitely; this guard aborts fast so the UI never freezes
// for the full 120s hard budget waiting on a dead provider socket.
const planFirstTokenTimeout = 8 * time.Second

// planLocalMaxLatency bounds how long a LOCAL (non-streaming) model may take to
// return a full completion. Unlike cloud providers, Ollama's /chat/completions
// is non-streaming: the "first token" is the entire prefill+generation latency,
// which a 7B model commonly exceeds. We therefore allow a realistic local budget
// while still keeping the 120s hard cap as the overall ceiling.
const planLocalMaxLatency = 90 * time.Second

// runPlanEngineCmd executes the (potentially slow) PlanEngine ledger synthesis
// in a background goroutine so the synchronous LLM call never blocks the Bubble
// Tea event loop. The result is delivered asynchronously as a planResultMsg,
// which the Update() loop handles to stage tasks and clear streaming state.
//
// HARDENING: two layered deadlines protect the live terminal.
//  1. firstTokenCtx (8s) — the provider MUST return its first response byte
//     within this window. If the local model is stuck/OOM or the socket stalls,
//     we abort immediately instead of freezing the prompt for the full budget.
//  2. ctx (120s) — overall synthesis budget for a slow-but-alive model.

// debugLogPlan writes plan-synthesis trace lines to .izen/debug/plan.log
// instead of os.Stderr. Bubble Tea owns the terminal exclusively while
// tea.WithAltScreen() is active — any direct stdout/stderr write from a
// background goroutine races the renderer's own ANSI redraw sequences on the
// same TTY and corrupts the visible frame (cursor jumps, dropped redraws,
// an apparently "frozen" screen even though Update() is still running fine
// underneath). This mirrors debugLogPayload in stream.go so plan-synthesis
// tracing stays diagnostic without ever touching the live terminal.
func debugLogPlan(line string) {
	dir := filepath.Join(".izen", "debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	entry := time.Now().Format(time.RFC3339Nano) + " " + line + "\n"
	f, err := os.OpenFile(filepath.Join(dir, "plan.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(entry)
}

func (m *model) runPlanEngineCmd(handoffSource, problem, modelName string, handoff HandoffContext) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	// Register cancel so it can be invoked on mode transition/Ctrl+C
	m.registerBackgroundCancel(cancel)

	return func() tea.Msg {
		debugLogPlan("runPlanEngineCmd entered; model=" + modelName)

		// ── STRICT LEDGER TRUNCATION (every /investigate → /plan handoff) ──
		// The handoff ledger carries sanitized trace blocks only — verbose
		// compilation stack traces and dependency logs over-inflate the prompt
		// and overload the model (local 7B especially). IZEN therefore always
		// compresses the ledger to a hard ceiling at this boundary; local SLMs
		// get the tight ~4k-char ceiling (budget.ModelTokenBudget), cloud models
		// a more generous one. Only the core error line + confirmed hypothesis
		// status survive, preventing local-model overload and token bloat.
		ledgerToSend := handoffSource
		useFastTrack := false
		localModel := plan.IsLocalModel(modelName)
		truncateCeiling := plan.MaxLedgerChars
		if !localModel {
			// Cloud models can absorb more, but we still cap the handoff hard.
			truncateCeiling = plan.MaxLedgerChars * 4
		}
		if len(handoffSource) > truncateCeiling {
			truncated := plan.TruncateLedger(handoffSource, truncateCeiling)
			debugLogPlan("LEDGER TRUNCATION: ledger " +
				fmt.Sprint(len(handoffSource)) + "→" + fmt.Sprint(len(truncated)) +
				" chars (model=" + modelName + ")")
			ledgerToSend = truncated
		}

		// ── DETERMINISTIC STDLIB TYPO INTERCEPTOR ────────────────────────────
		// Before dispatching to the LLM for planning, check if the ledger
		// contains a simple stdlib case typo (e.g. undefined: Log, Fmt, Os).
		// These are handled deterministically without calling the LLM, bypassing
		// both the fast-track and the full plan synthesis paths. This prevents
		// the LLM from generating over-engineered plans (e.g. creating
		// pkg/util/logs/log.go) for a trivial stdlib case fix.
		//
		// HARD REQUIREMENT: on ANY undefined: Symbol match, emit exactly 1
		// FILE_MUTATE task — NEVER fall through to LLM synthesis (which would
		// hallucinate SHELL_EXEC go mod tidy for what is a simple stdlib typo).
		// SHELL_EXEC tasks are banned for undefined symbol errors unless a
		// go.mod/go.sum missing file error is explicitly present.
		debugLogPlan("STDLIB INTERCEPTOR: scanning ledger for undefined symbols")
		if undef := retrieval.ParseUndefinedSymbol(ledgerToSend); undef != nil && undef.Symbol != "" {
			debugLogPlan("STDLIB INTERCEPTOR: matched undefined: " + undef.Symbol +
				" at " + undef.File + ":" + fmt.Sprint(undef.Line))
			if pkgName, importPath, matched := retrieval.CheckStdlibCaseCorrection(undef.Symbol); matched {
				debugLogPlan("STDLIB INTERCEPTOR: stdlib case-correction fired: " +
					undef.Symbol + " → " + pkgName)
				cancel()
				sanitizedTarget, pathErr := retrieval.SanitizeTargetPath(undef.File)
				if pathErr != nil {
					debugLogPlan("STDLIB INTERCEPTOR: path not found — " + pathErr.Error())
					return planResultMsg{
						Tasks: []plan.Task{
							{
								StepNum:     1,
								IsDone:      false,
								Status:      "idle",
								Type:        "SHELL_EXEC",
								Target:      "go test ./...",
								Description: "Stdlib case-correction blocked: target file not found. Re-run build for diagnostics.",
								Rationale:   "File referenced by compiler does not exist on disk; may need a fresh build.",
								Solution:    "Run go test ./... to regenerate compiler diagnostics.",
								IsHardcoded: true,
							},
						},
						Handoff: handoff,
					}
				}
				desc := fmt.Sprintf("Fix %q at %s:%d: replace %q with %q and add import %q",
					undef.Symbol, sanitizedTarget, undef.Line, undef.Symbol, pkgName, importPath)
				return planResultMsg{
					Tasks: []plan.Task{
						{
							StepNum:     1,
							IsDone:      false,
							Status:      "idle",
							Type:        "FILE_MUTATE",
							Target:      sanitizedTarget,
							Description: desc,
							Rationale:   fmt.Sprintf("Fix standard library package casing/import (change %q to %q).", undef.Symbol, pkgName),
							Solution:    fmt.Sprintf("STDLIB:%s:%s:%s", undef.Symbol, pkgName, importPath),
							IsHardcoded: true,
						},
					},
					Handoff: handoff,
				}
			}
			debugLogPlan("STDLIB INTERCEPTOR: undefined symbol " + undef.Symbol +
				" not a stdlib typo; falling through to LLM synthesis")
		} else {
			debugLogPlan("STDLIB INTERCEPTOR: no undefined symbol match")
		}

		if localModel {
			// ── "0 TODO" FAST-TRACK SHORT-CIRCUIT ──────────────────────────
			// When there are no explicit code TODOs AND the ledger only contains
			// compilation/dependency blockers (resolvable via environment setup,
			// not a deep architectural plan), skip the heavy full-plan loop and
			// dispatch a minimal 3-line shell resolution prompt instead.
			//
			// CRITICAL: Extract the investigation conclusion BEFORE discarding the
			// full ledger context. The conclusion carries the resolved diagnosis
			// (e.g. "use github.com/moby/moby/client") which must be injected into
			// the fast-track prompt so the model does NOT re-derive a stale or
			// incorrect fix from raw error text alone.
			if len(handoff.PendingTodos) == 0 && plan.IsCompilationOrDependencyError(ledgerToSend) {
				coreErr := plan.CoreErrorLine(ledgerToSend)
				conclusion := plan.ExtractConclusionFromLedger(handoffSource)
				ledgerToSend = plan.FastTrackPrompt(coreErr, conclusion)
				problem = coreErr
				useFastTrack = true
				debugLogPlan("FAST-TRACK SHORT-CIRCUIT: 0 TODOs + compile/dep blocker → minimal prompt")
			}
		}

		// ── CLOUD PROVIDER FAST-TRACK (dependency/compilation blocker) ─────────
		// For cloud providers, we also use the fast-track path when there are
		// no explicit TODOs AND the ledger contains compilation/dependency errors.
		// This ensures SHELL_EXEC tasks are generated with high confidence for
		// dependency fixes, regardless of model type.
		if !useFastTrack && len(handoff.PendingTodos) == 0 && plan.IsCompilationOrDependencyError(ledgerToSend) {
			coreErr := plan.CoreErrorLine(ledgerToSend)
			conclusion := plan.ExtractConclusionFromLedger(handoffSource)
			ledgerToSend = plan.FastTrackPrompt(coreErr, conclusion)
			problem = coreErr
			useFastTrack = true
			debugLogPlan("CLOUD FAST-TRACK: 0 TODOs + compile/dep blocker → shell resolution prompt")
		}

		if m.planEngine == nil {
			cancel()
			debugLogPlan("plan engine not configured — aborting")
			return planResultMsg{Err: fmt.Errorf("plan engine not configured"), Handoff: handoff}
		}

		type outcome struct {
			tasks []plan.Task
			err   error
		}
		outCh := make(chan outcome, 1)

		// ── FIRST-TOKEN / COMPLETION GUARD ───────────────────────────────
		// The provider call (a single NON-STREAMING HTTP round-trip inside
		// ProcessFromLedger) inherits this deadline. For cloud providers the
		// round-trip returns the first token quickly, so the tight 8s guard is
		// appropriate. Local Ollama calls are non-streaming: "first token" == the
		// entire prefill+generation latency, which a 7B model easily exceeds. For
		// local models we therefore use a realistic budget (the 120s hard cap
		// still applies as the overall ctx), and only fall back to the cloud
		// prompt if the model is genuinely unresponsive.
		ftBudget := planFirstTokenTimeout
		if localModel {
			ftBudget = planLocalMaxLatency
		}
		ftCtx, ftCancel := context.WithTimeout(ctx, ftBudget)
		defer ftCancel()

		go func() {
			debugLogPlan("Preparing LLM payload (ledger bytes=" + fmt.Sprint(len(ledgerToSend)) +
				"; fastTrack=" + fmt.Sprint(useFastTrack) + ")")
			var tasks []plan.Task
			var err error
			if useFastTrack {
				tasks, err = m.planEngine.ProcessFromLedgerFastTrack(ftCtx, ledgerToSend, modelName)
			} else {
				tasks, err = m.planEngine.ProcessFromLedger(ftCtx, ledgerToSend, problem, modelName)
			}
			debugLogPlan("Provider returned; err=" + fmt.Sprint(err))
			outCh <- outcome{tasks: tasks, err: err}
		}()

		select {
		case o := <-outCh:
			cancel()
			return planResultMsg{Tasks: o.tasks, Err: o.err, Handoff: handoff}
		case <-ftCtx.Done():
			// First-token deadline missed: the provider is unresponsive.
			cancel()
			debugLogPlan("FIRST-TOKEN TIMEOUT after " + planFirstTokenTimeout.String() + " — provider unresponsive")
			// For local models, degrade gracefully: instead of a hard failure
			// that strands the user, surface a fallback action they can take
			// directly from the interactive prompt.
			if localModel {
				return planResultMsg{
					Err:     fmt.Errorf("[error] Local model (%s) produced no response within %s. The forensic ledger was already minimized and a fast-track shell plan was attempted — this points to an unloaded/OOM model. Ensure Ollama has the model loaded, or run `/provider <cloud>` to offload planning to a cloud model", modelName, planLocalMaxLatency),
					Handoff: handoff,
				}
			}
			return planResultMsg{
				Err:     fmt.Errorf("[error] LLM Provider timeout: no response within %s. Check if your local model is stuck/OOM, or that Ollama is running and the model (%s) is loaded", planFirstTokenTimeout, modelName),
				Handoff: handoff,
			}
		case <-ctx.Done():
			debugLogPlan("hard 120s timeout — aborting")
			return planResultMsg{Err: fmt.Errorf("plan synthesis timed out after 120s: %w", ctx.Err()), Handoff: handoff}
		}
	}
}

// planHasNothingToSynthesize reports whether a /plan invocation has genuinely
// no material to work from: no handoff ledger content, no proposed fix, no
// ledger diagnostics or analytical packets, an empty conversational assembly,
// AND no user-typed objective. In that state the previous code fell through to
// streamCmd("") which returns nil silently, leaving the user at the prompt with
// no feedback (indistinguishable from a hang). The caller uses this to surface
// an actionable notice instead.
//
// It deliberately ignores PendingTodos count: zero pending TODOs is the healthy
// state for a /investigate → /plan handoff (the forensic ledger drives
// synthesis, not pre-parsed TODOs), so gating on it would break valid handoffs.
func (m *model) planHasNothingToSynthesize(rawContext, content string) bool {
	if strings.TrimSpace(rawContext) != "" || strings.TrimSpace(content) != "" {
		return false
	}
	if strings.TrimSpace(m.handoffLedgerContent) != "" ||
		strings.TrimSpace(m.handoffCtx.ProposedFix) != "" {
		return false
	}
	if m.sess != nil && m.sess.ContextLedger != nil {
		l := m.sess.ContextLedger
		if strings.TrimSpace(l.Diagnostics) != "" || len(l.Packets) > 0 {
			return false
		}
	}
	return true
}

func parseModeShorthand(line string) (modes.Mode, string, bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, mode := range []modes.Mode{
		modes.ModeAsk,
		modes.ModePlan,
		modes.ModeBuild,
		modes.ModeInvestigate,
		modes.ModeReview,
	} {
		prefix := "/" + mode.String()
		if lower == prefix {
			return mode, "", true
		}
		if strings.HasPrefix(lower, prefix+" ") {
			return mode, strings.TrimSpace(line[len(prefix):]), true
		}
	}
	return modes.ModeAsk, "", false
}

func (m *model) setMode(mode modes.Mode) tea.Cmd {
	// ── RULE A: STRICT MODE TRANSITION GATEKEEPER ──────────────────────
	// Auto-transitions to /build from non-build modes are blocked unless
	// the user explicitly authorized the switch by typing a mode command
	// OR the plan has already been approved in this execution cycle.
	if !m.modeChangeAuthorized && !m.planApproved && mode == modes.ModeBuild && m.resolver.Current() != modes.ModeBuild {
		m.push(roleError, "State Transition Blocked: File modifications are only allowed inside /build mode after /plan approval. Please run /plan first, then use /build.")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	m.modeChangeAuthorized = false

	m.investigateInvocationCount = 0 // Unconditional state clearance to avoid hard lockout bugs during testing
	m.buildRecoveryCount = 0         // Reset auto-recovery counter on every mode transition

	// ── Plan-Approved Lifecycle ────────────────────────────────────────
	// Entering /plan or /investigate starts a new cycle — reset approval.
	if mode == modes.ModePlan || mode == modes.ModeInvestigate {
		m.planApproved = false
	}
	// HUMAN-IN-THE-LOOP: plan approval is now managed explicitly via
	// planApprovalActions (Approve/Reject chips). The m.planApproved flag
	// is set only when the user explicitly approves the plan through the
	// action chip handler. The old auto-approve-on-transition behavior is
	// removed — every /plan → /build transition now requires human sign-off.

	// ── HANDOFF SANITIZER (BUG 3): clear ALL transient raw-string state on
	// every mode transition so the target mode can never inherit stale
	// conversational context (past test failures, user greetings, abandoned
	// ledger text) from a previous phase. Structured typed payloads
	// (handoffCtx.PendingTodos / sess.CurrentTasks) survive this purge by
	// design — they are the authoritative /plan → /build contract.
	m.CleanContextTransitions(mode)

	// ── ABSOLUTE STALE GOROUTINE RELEASE ON MODE ENTRY ────────────────
	// Before any mode transition, cancel all in-flight background contexts,
	// drain stream buffers, and reset spinner state. This prevents stale
	// tickMsg loops and structural goroutines from a previous mode (e.g.,
	// $test from /review) from corrupting the single-source model state
	// of the new mode — the root cause of spinner frame mutation bugs.
	m.cancelStaleAgentOps()
	m.buildVerifyPending = false

	if mode == m.resolver.Current() {
		return nil
	}
	m.startModeTransition(mode)
	// ── Reset view-scoped workflow result on mode entry ────────────────
	// Entering a new mode starts a fresh workflow: the previous result's
	// capabilities (failure to investigate, build-verify commit/rollback) are
	// no longer relevant to the current view. handoffCtx is intentionally left
	// intact for genuine cross-mode handoffs.
	m.currentResult = nil
	m.sess.SetMode(mode)
	_ = m.sess.Save()

	// ── VIRTUAL SNAPSHOT STAGING ───────────────────────────────────────
	// On every mode switch that may involve file mutations, begin a fresh
	// virtual transaction. This snapshots the current workspace state so that
	// if the user rejects a proposal or a build fails, all disk mutations can
	// be instantly rolled back to this point. The transaction is committed
	// only on explicit user approval (Alt+A / Alt+L).
	if m.execEng != nil && (mode == modes.ModeBuild || mode == modes.ModeInvestigate || mode == modes.ModePlan || mode == modes.ModeReview) {
		m.execEng.BeginTransaction()
	}

	modeColor := modeAccentColor(mode)
	modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
		fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
	m.push(roleSystem, modeLabel)
	m.push(roleSystem, fmt.Sprintf("Switched to /%s", mode))

	// ── SYNCHRONOUS LEDGER RELOAD (1-TURN LATENCY FIX) ────────────────
	// CleanContextTransitions above purged transient in-memory handoff buffers
	// (m.handoffLedgerContent, etc.). Before dispatching the target mode's LLM
	// call we MUST synchronously reload the freshly written .izen/context_ledger.json
	// into memory so the new mode reads from the authoritative structured SSOT,
	// not the now-cleared transient state. This is the load-and-inject step of
	// the blocking handoff: write → clean → reload → inject → dispatch.
	m.reloadContextLedger()

	// ── PRIME TRANSIENT HANDOFF FROM RELOADED LEDGER ──────────────────
	// Re-populate the transient in-memory handoff (handoffLedgerContent /
	// handoffCtx) from the freshly reloaded authoritative ledger. This is what
	// the structural /plan and /build engines actually consume; without it the
	// handoff would be empty after CleanContextTransitions cleared it, and the
	// target mode would boot with a generic greeting.
	m.primeHandoffFromLedger(mode)

	// Handoff context injection primes the target mode with state from the
	// previous mode's terminal event.
	m.injectHandoffContext(mode)

	// ── AUTO-TRIGGER ENFORCEMENT (FULLY ASYNC) ──────────────────────────
	// If handoff context was injected for /plan or /build, immediately
	// trigger the mode's execution engine instead of waiting for user input.
	// This prevents mode stagnation where the LLM receives handoff data as
	// passive chat history but produces open-ended chatbot responses.
	//
	// CRITICAL: the dispatch MUST NOT run synchronously inside Update(). The
	// previous implementation called m.handleMessageContent(...) directly here,
	// which performed heavy, blocking work (ledger payload assembly, prompt
	// construction, engine call) on the Bubble Tea event-loop thread. That
	// froze the very first frame and made the UI unresponsive to Ctrl+C until
	// the work finished. Now the ENTIRE handoff→engine pipeline is wrapped in
	// the returned tea.Cmd's background goroutine closure, so Update() returns
	// instantly and the spinner is free to animate from millisecond zero.
	if !m.streaming && !m.agentRunning && !m.pipelineRunning {
		if m.buildHandoffTriggerContent(mode) != "" {
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return tea.Batch(
				m.smoothStreamTickCmd(),
				func() tea.Msg {
					// Everything below runs in the cmd-runner goroutine,
					// never on the Bubble Tea event loop.
					content := m.buildHandoffTriggerContent(mode)
					if content == "" {
						return nil
					}
					cmd := m.handleMessageContent(content)
					if cmd == nil {
						return nil
					}
					return cmd()
				},
			)
		}
	}

	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// buildHandoffTriggerContent returns a non-empty string when handoff data exists
// for the given mode, triggering immediate structural execution. For /plan mode
// the handoff is handled internally by the structural engine — the return value
// is the raw handoff text that feeds into the engine path. For /build mode the
// pending todos are formatted as a structured execution prompt.
func (m *model) buildHandoffTriggerContent(mode modes.Mode) string {
	switch mode {
	case modes.ModePlan:
		// m.handoffLedgerContent is primed from the reloaded authoritative
		// session.ContextLedger by primeHandoffFromLedger (called in setMode
		// after CleanContextTransitions). It carries the /investigate forensic
		// diagnostics/targets, so /plan boots directly into structured task
		// synthesis instead of a generic greeting.
		if m.handoffLedgerContent != "" {
			return m.handoffLedgerContent
		}
		if m.handoffCtx.ProposedFix != "" {
			return m.handoffCtx.ProposedFix
		}
		if m.handoffCtx.LastFailurePayload != "" {
			return m.handoffCtx.LastFailurePayload
		}
	case modes.ModeBuild:
		// /build STRICTLY consumes the atomic structural tasks produced by the
		// /plan phase (m.handoffCtx.PendingTodos and m.sess.CurrentTasks). It
		// must NEVER fall back to the raw conversational ProposedFix blob —
		// that would re-inject stale $test / chat text into the build
		// workspace. If no atomic tasks exist, return "" so setMode enters a
		// clean idle state instead of contaminating the buffer.
		hasStagedTasks := len(m.sess.CurrentTasks) > 0
		if len(m.handoffCtx.PendingTodos) == 0 && !hasStagedTasks {
			return ""
		}
		var b strings.Builder
		b.WriteString("## HANDOFF BUILD EXECUTION\n\n")
		b.WriteString("Execute the following planned tasks and output code patches directly.\n")
		b.WriteString("Do NOT restate the plan or ask for approval — produce the mutations now.\n\n")
		if len(m.handoffCtx.PendingTodos) > 0 {
			for i, todo := range m.handoffCtx.PendingTodos {
				fmt.Fprintf(&b, "Task %d: %s\n", i+1, todo)
			}
		} else if hasStagedTasks {
			for i, t := range m.sess.CurrentTasks {
				fmt.Fprintf(&b, "Task %d: %s — %s — %s\n", i+1, t.Type, t.Target, t.Description)
			}
		}
		return b.String()
	}
	return ""
}

// buildStrictHandoffPayload creates a minimal, focused context for the /build
// task execution. It contains ONLY:
// 1. The exact target file path(s) for the current task
// 2. The exact staged task description
// 3. The raw relevant symbol definition/context from the codebase
// This prevents cognitive drift by stripping all conversational history,
// raw chat logs, and unrelated codebase files.
// retryBuildWithStrictDirective re-executes the current build task with a
// maximally strict instruction that prohibits any conversational output.
// The LLM is told to output ONLY SEARCH/REPLACE or FILE_CREATE blocks with
// zero preamble, zero explanation, zero greeting.
func (m *model) retryBuildWithStrictDirective() tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		return nil
	}
	// Find the current processing/failed task.
	var targetTask *plan.Task
	for i, t := range tasks {
		if t.Status == "processing" || t.Status == "failed" || t.Status == "idle" {
			targetTask = &tasks[i]
			break
		}
	}
	if targetTask == nil {
		return nil
	}
	strictContent := fmt.Sprintf(
		"## STRICT BUILD DIRECTIVE — ZERO CONVERSATIONAL TEXT\n\n"+
			"YOU ARE A CODE GENERATION TOOL. DO NOT OUTPUT ANY TEXT THAT IS NOT A CODE PATCH.\n\n"+
			"REQUIRED OUTPUT FORMAT (FIRST TOKEN MUST MATCH):\n"+
			"- For existing files: ```go:path/to/file.go\n  <<<<<<< SEARCH\n  ...\n  =======\n  ...\n  >>>>>>>\n  ```\n"+
			"- For new files: ```\n  <<<<<<< FILE_CREATE: path/to/newfile.go\n  ...\n  >>>>>>> END_FILE\n  ```\n\n"+
			"FORBIDDEN OUTPUT:\n"+
			"- Greetings, acknowledgments, summaries, explanations\n"+
			"- Questions, clarifications, suggestions\n"+
			"- Markdown that is not SEARCH/REPLACE or FILE_CREATE\n"+
			"- JSON, YAML, or any structured data format\n\n"+
			"TASK:\n"+
			"Step %d: %s\nTarget: %s\nDescription: %s\n\n"+
			"OUTPUT YOUR PATCH NOW:",
		targetTask.StepNum, targetTask.Type, targetTask.Target, targetTask.Description)
	m.push(roleSystem, "Conversational output detected. Re-triggering build with strict directive...")
	m.sess.ClearHistory()
	_ = m.sess.Save()
	m.responseBuffer.Reset()
	m.streamBuffer = ""
	m.currentStreamContent = ""
	return m.streamCmd(strictContent)
}

// buildStrictHandoffPayload creates a minimal, focused context for the /build
// task execution. It contains ONLY:
// 1. The exact target file path(s) for the current task
// 2. The exact staged task description
// 3. The raw relevant symbol definition/context from the codebase
// This prevents cognitive drift by stripping all conversational history,
// raw chat logs, and unrelated codebase files.
func (m *model) buildStrictHandoffPayload() string {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 && len(m.handoffCtx.PendingTodos) == 0 {
		return ""
	}

	var targetTask *plan.Task
	if len(tasks) > 0 {
		for i, t := range tasks {
			if t.Status == "idle" || t.Status == "processing" {
				targetTask = &tasks[i]
				break
			}
		}
	}

	var b strings.Builder
	b.WriteString("## BUILD TASK EXECUTION\n\n")

	if targetTask != nil {
		b.WriteString("### TARGET\n")
		b.WriteString(targetTask.Target + "\n\n")
		b.WriteString("### TASK\n")
		b.WriteString(targetTask.Description + "\n\n")
	}

	// Include only the relevant symbol context for the target file
	if targetTask != nil && m.graph != nil {
		fn := m.graph.LookupFile(targetTask.Target)
		if fn != nil {
			b.WriteString("### SYMBOL CONTEXT\n")
			b.WriteString("```go\n")
			// Include just the symbol signatures, not full source
			for _, sym := range fn.Symbols {
				if sym.Exported || strings.Contains(strings.ToLower(sym.Name), strings.ToLower(targetTask.Target)) {
					b.WriteString(sym.Signature)
					b.WriteString("\n")
				}
			}
			b.WriteString("```\n\n")
		}
	}

	b.WriteString("### INSTRUCTION\n")
	b.WriteString("Implement ONLY this task. Output unified diff or FILE: block directly.\n")
	b.WriteString("Do NOT restate the plan, do NOT list other tasks, do NOT output JSON.\n")

	return b.String()
}

func (m *model) handleCommand(cmd string) tea.Cmd {
	name := strings.Fields(cmd)
	if len(name) == 0 {
		return nil
	}

	// ── Composite fast-query: /review $test ─────────────────────────────
	// Intercept the composite shortcut before any other routing. It runs the
	// dynamic test suite, injects the telemetry into the forensic ledger, then
	// triggers the risk analysis engine with both git diff AND test reports.
	if command.IsReviewTestComposite(cmd) {
		m.push(roleSystem, accentStyle.Render("⚡ [IZEN Shortcut] Running dynamic test suite before auditing commit risks..."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.runReviewTestComposite()
	}

	if _, ok := validSystemCommands[name[0]]; !ok {
		m.push(roleError, "unknown command: "+cmd)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	switch {
	case cmd == "/help" || cmd == "/?":
		m.push(roleSystem, labelBoldStyle.Render("modes"))
		m.push(roleSystem, infoStyle.Render("  /ask         explain, inspect, understand (read-only)"))
		m.push(roleSystem, infoStyle.Render("  /plan        architecture, migrations, refactors"))
		m.push(roleSystem, infoStyle.Render("  /build       implement, refactor, write tests"))
		m.push(roleSystem, infoStyle.Render("  /investigate debug bugs, failures, regressions"))
		m.push(roleSystem, infoStyle.Render("  /review      audit changes, detect risks"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("commands"))
		m.push(roleSystem, infoStyle.Render("  /help  /usage  /model  /objective  /drop  /clear  /quit"))
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint  /arch"))
		m.push(roleSystem, infoStyle.Render("  /objective approve  approve budget-guarded objective"))
		m.push(roleSystem, infoStyle.Render("  /usage           inspect token usage and provider status"))
		m.push(roleSystem, infoStyle.Render("  /model           interactive model picker (fuzzy search)"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>  run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("ask sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $prompt <idea>  refine architectural idea via Senior Architect analysis"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("review sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $test [path]  run tests (safety-gated for large repos)"))
		m.push(roleSystem, infoStyle.Render("  $run  [path]  run go build (safety-gated for large repos)"))
		m.push(roleSystem, infoStyle.Render("  $fix          auto-fix from last test/run failure output"))
		m.push(roleSystem, infoStyle.Render("  $log          evaluate shell trace & run implicit pipeline"))
		m.push(roleSystem, infoStyle.Render(""))
		m.push(roleSystem, labelBoldStyle.Render("investigate sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $env            capture environment diagnostics"))
		m.push(roleSystem, infoStyle.Render("  $trace [fn]     live execution trace with -race (auto from context log)"))
		m.push(roleSystem, infoStyle.Render("  $diagnose       root cause analysis from forensic data"))
		m.push(roleSystem, infoStyle.Render("  $log            evaluate shell trace & run implicit pipeline"))
		m.push(roleSystem, "")
		m.push(roleSystem, infoStyle.Render("  @<path>  reference a file in your message"))
		return nil

	case cmd == "/quit":
		m.push(roleSystem, "goodbye.")
		return m.cleanShutdownCmd()

	case cmd == "/usage":
		return m.runUsageCmd()

	case strings.HasPrefix(cmd, "/provider"):
		parts := strings.Fields(cmd)
		if len(parts) >= 2 {
			// Still allow provider switching via /provider for backwards
			// compatibility, but show a deprecation hint.
			m.push(roleSystem, mutedStyle.Render("💡 Tip: Use /model to pick models across any provider. Provider switching happens automatically."))
			return m.switchProvider(parts[1])
		}
		// Bare /provider: redirect to /usage
		m.push(roleSystem, mutedStyle.Render("💡 Tip: Provider switching is automatic! Use /model to pick any model, or /usage to inspect provider API keys."))
		return m.runUsageCmd()

	case cmd == "/model":
		m.showModelPicker = true
		m.modelPicker = NewModelPickerModal()
		m.modelPicker.SetSize(m.width, m.height)

		providers := make(map[string]string)
		for name, prov := range m.cfg.AI.Providers {
			providers[name] = prov.APIKey
		}
		if len(providers) == 0 {
			providers["ollama"] = ""
		}

		return m.modelPicker.LoadModels(providers)

	case strings.HasPrefix(cmd, "/objective"):
		objArg := strings.TrimSpace(strings.TrimPrefix(cmd, "/objective"))
		if strings.EqualFold(objArg, "approve") {
			if m.sess.ObjectiveState == nil {
				m.uiNotice = "No active objective to approve."
				return nil
			}
			m.sess.ObjectiveState.HumanConfirmed = true
			if m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveAnalyzing || m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveIdle {
				m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			}
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
			m.uiNotice = "Objective approved for outbound pipelines."
			return nil
		}
		if objArg != "" {
			m.resetObjectiveContextStacks()
			obj := domain.NewObjective(objArg)
			obj.CurrentStatus = domain.ObjectiveAnalyzing
			m.sess.SetObjectiveState(obj)
			_ = m.sess.Save()
			m.uiNotice = "Objective analysis started."
			return m.analyzeObjectiveCmd(obj)
		} else {
			m.uiNotice = "Usage: /objective <description>"
		}
		return nil

	case cmd == "/clear":
		m.records = nil
		m.PreRenderedHistory = ""
		m.showBanner = true
		m.currentResult = nil
		m.currentPrompt = ""
		m.responseBuffer.Reset()
		m.streamBuffer = ""
		m.currentStreamContent = ""
		m.streaming = false

		// Purge ContextLedger (ask_handoff_payload, investigation findings,
		// pending execution tasks, and all analytical packets).
		if m.sess != nil {
			m.sess.ContextLedger = nil
			m.sess.InvestigationID = ""
			m.sess.ReviewID = ""
			m.sess.ClearHistory()
			m.sess.ClearTasks()
			_ = m.sess.Save()
		}

		// Clear handoff pipeline state.
		m.handoffCtx = HandoffContext{}
		m.handoffLedgerContent = ""
		m.lastInvestigateLedger = nil

		// Clear forensic / test telemetry caches.
		m.lastTestOutput = ""
		m.lastTestFailed = false
		m.lastTestTarget = ""
		m.pendingFileRefs = nil

		// Reset build and proposal gates.
		m.buildRecoveryCount = 0
		m.buildVerifyPending = false
		m.pendingBuildApproval = false
		m.pendingBuildTask = nil
		m.pendingBuildAllowAlways = false
		m.pendingProposals = nil
		m.acceptedProposals = nil
		m.awaitingConfirmation = false
		m.acceptAll = false
		m.pendingHotfixTask = nil
		m.currentBuildTaskID = 0
		m.pendingTestConfirm = false
		m.pendingTestTarget = ""
		m.investigateInvocationCount = 0

		// Zero out cumulative token counters.
		m.InputTokens = 0
		m.OutputTokens = 0
		m.TotalTokens = 0
		m.ContextLimit = 0
		m.AccumulatedCost = 0

		m.refreshViewportContent()
		return tea.Sequence(
			tea.ClearScreen,
			tea.Println("✕ [IZEN Memory] Context ledger and pending tasks successfully purged. Workspace reset."),
		)

	case cmd == "/drop" || cmd == "/drop all":
		m.attachedFiles = nil
		m.pendingFileRefs = nil
		m.push(roleSystem, infoStyle.Render("all context files detached"))
		return nil

	case strings.HasPrefix(cmd, "/drop "):
		raw := strings.TrimSpace(strings.TrimPrefix(cmd, "/drop"))
		// Strip optional @ prefix for @file syntax
		raw = strings.TrimPrefix(raw, "@")
		target := filepath.Clean(raw)
		if target == "" || target == "." {
			m.push(roleSystem, infoStyle.Render("usage: /drop [@file|all]"))
			return nil
		}
		filtered := make([]string, 0, len(m.attachedFiles))
		for _, f := range m.attachedFiles {
			if filepath.Clean(f) != target {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == len(m.attachedFiles) {
			m.push(roleSystem, infoStyle.Render("not attached: "+raw))
			return nil
		}
		m.attachedFiles = filtered
		if len(m.attachedFiles) == 0 {
			m.push(roleSystem, infoStyle.Render("all context files detached"))
		} else {
			m.push(roleSystem, infoStyle.Render("detached: "+raw))
		}
		return nil

	case strings.HasPrefix(cmd, "/undo"):
		return m.runUndoCmd(cmd)

	case cmd == "/commit", strings.HasPrefix(cmd, "/commit "):
		if m.resolver.Current() != modes.ModeBuild {
			m.push(roleError, "commit error: /commit is only available in /build mode")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		msg := strings.TrimSpace(strings.TrimPrefix(cmd, "/commit"))
		return m.runCommitCmdAgent(msg)

	case cmd == "/checkpoint":
		m.push(roleSystem, infoStyle.Render("/checkpoint not yet implemented"))
		return nil

	case cmd == "/arch":
		m.showBanner = false
		m.push(roleSystem, "Mapping codebase...")
		m.refreshViewportContent()
		return func() tea.Msg {
			graphText := m.renderArch()
			return archDoneMsg{Content: graphText}
		}

	}

	m.push(roleError, "unknown command: "+cmd)
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

func (m *model) startModeTransition(target modes.Mode) {
	m.lineAnimTargetMode = target
	m.lineAnimProgress = 0.0
	m.lineAnimating = true
	m.resolver.Set(target)
}

// CleanContextTransitions is the single handoff sanitizer invoked on every mode
// transition. It explicitly clears all transient raw-string state so a new mode
// never inherits stale conversational context from a previous phase:
//   - handoffLedgerContent: raw Context-Ledger output (superseded by structured tasks)
//   - rawInputBuffer / input builder: unstructured message history
//   - transient raw string variables (lastTestOutput, currentPrompt, responseBuffer)
//
// Structured, typed payloads (handoffCtx.PendingTodos, sess.CurrentTasks) are
// intentionally preserved — they are the authoritative inter-mode contract that
// /plan → /build relies on, and clearing them would break the pipeline.
func (m *model) CleanContextTransitions(targetMode modes.Mode) {
	// REFORM C: Aggressively zero out ALL unstructured raw text buffers
	// during mode transitions. Only structured, verified task slices traverse
	// the boundaries.

	// ── SERIALIZE STRUCTURED LEDGER TO DISK (SINGLE SOURCE OF TRUTH) ──
	// Compile a fresh ContextLedger for the incoming mode and persist it to
	// .izen/context_ledger.json. This is an absolute overwrite: the previous
	// ledger is replaced, so no stale prompts, build logs, or chat history can
	// leak across the boundary. The same ledger is mirrored into the session
	// record and persisted to .izen/session.json for full durability.
	//
	// CRITICAL: Preserve Diagnostics AND Packets from investigation when
	// transitioning to /plan. The investigation findings must survive the mode
	// transition so the plan engine receives the forensic context needed for
	// structured analysis. The Packets carry the ID-addressed analytical units
	// (targets, evidence, root cause) that the plan engine's pre-processors
	// (canonical mismatch, undefined symbol) scan deterministically.
	prevDiagnostics := ""
	var prevPackets []session.LedgerPacket
	if m.sess != nil && m.sess.ContextLedger != nil {
		prevDiagnostics = m.sess.ContextLedger.Diagnostics
		if len(m.sess.ContextLedger.Packets) > 0 {
			prevPackets = make([]session.LedgerPacket, len(m.sess.ContextLedger.Packets))
			copy(prevPackets, m.sess.ContextLedger.Packets)
		}
	}

	ledger := session.NewContextLedger(targetMode)
	if m.sess != nil {
		ledger.TargetFile = m.sess.ContextLabel()
		// Preserve investigation diagnostics and ask handoff payloads for
		// /plan and /investigate modes so the forensic engine can extract
		// its baseline context without manual copy-pasting.
		if prevDiagnostics != "" && (targetMode == modes.ModePlan || targetMode == modes.ModeInvestigate) {
			ledger.Diagnostics = prevDiagnostics
		}
		// Re-inject the sequential, ID-addressed analytical packets from the
		// previous ledger. These carry the forensic findings (targets, evidence,
		// root cause, conclusion) that the downstream mode reads via
		// FormatPacketsForPlan. InjectPacket assigns monotonic IDs starting from
		// the new ledger's existing (empty) packet index, ensuring every packet
		// survives the transition with its full payload intact.
		for _, p := range prevPackets {
			ledger.InjectPacket(p)
		}
		ledger.Tasks = nil
		for _, t := range m.sess.CurrentTasks {
			ledger.Tasks = append(ledger.Tasks, plan.AtomicTask{
				TaskID:      t.StepNum,
				File:        t.Target,
				Strategy:    t.Type,
				Description: t.Description,
			})
		}
		if err := ledger.Save(); err == nil {
			m.sess.SetContextLedger(ledger)
		}
	}

	// ── INVALIDATE MEMORY CACHE: zero out every raw response buffer,
	// streaming string array, and historical message slice so the target mode
	// can never inherit ghost output or stale topic references.
	m.handoffLedgerContent = ""
	m.input.Reset()
	m.ti.SetValue("")
	m.ti.Reset()
	m.syncInputFromTI()
	m.currentPrompt = ""
	m.responseBuffer.Reset()
	m.streamBuffer = ""
	m.currentStreamContent = ""
	m.lastTestOutput = ""
	m.lastTestFailed = false
	m.lastTestTarget = ""
	m.handoffCtx.ProposedFix = ""
	m.handoffCtx.LastFailurePayload = ""
	m.handoffCtx.TargetScope = ""

	// ── PROMPT BUFFER BLEEDING FIX ─────────────────────────────────────
	// Clear the LLM dialog history on every mode transition so no stale
	// conversational context (previous greetings, abandoned analyses, failed
	// task history) leaks into the new mode's context window. Each mode starts
	// with a clean prompt buffer — the ContextLedger is the SINGLE source of
	// truth for cross-mode handoff.
	if m.sess != nil {
		m.sess.ClearHistory()
		_ = m.sess.Save()
	}
}

// runBuildCmd is the /build mode execution entry. It strictly blocks when no
// atomic structural tasks are staged (the zombie-data guard) and otherwise
// executes EXCLUSIVELY on the structured items, ignoring any unstructured
// message history or stale conversational buffers.
func (m *model) runBuildCmd(content string) tea.Cmd {
	hasStagedTasks := len(m.sess.CurrentTasks) > 0
	hasPendingTodos := len(m.handoffCtx.PendingTodos) > 0
	hasLedgerTasks := m.sess != nil && m.sess.ContextLedger != nil && len(m.sess.ContextLedger.Tasks) > 0

	// ── ZERO-TASK VALIDATION (build-freeze fix, TASK 3.1) ──────────────
	// Deterministic guard: if there is nothing to execute, halt immediately,
	// set state to idle, and print a clean notification. Never enter any
	// execution loop — this prevents the empty-queue deadlock / spinner freeze
	// that occurred when /plan produced no tasks.
	if !hasStagedTasks && !hasPendingTodos && !hasLedgerTasks {
		m.push(roleError, "[BUILD HALTED] No active tasks found. Please formulate a plan in /plan first.")
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.streaming = false
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── VIRTUAL SNAPSHOT STAGING ───────────────────────────────────────
	// Begin a fresh transaction for this build execution to snapshot the
	// workspace. If the build fails or the user rejects proposals, all
	// mutations can be rolled back instantly.
	if m.execEng != nil {
		m.execEng.BeginTransaction()
	}

	// ── PROMPT BUFFER BLEEDING FIX ─────────────────────────────────────
	// Clear the LLM dialog buffer at the start of every build invocation so
	// no stale context from previous build runs or failed tasks can leak into
	// the new execution window. Each build starts with a clean prompt scope.
	if m.sess != nil {
		m.sess.ClearHistory()
		_ = m.sess.Save()
	}

	// Sanitize any leftover unstructured content — /build operates purely on
	// the structural task ledger, never on free-form conversational input.
	_ = content
	m.responseBuffer.Reset()
	if m.execEng != nil {
		m.execEng.SetStreamContextFiles(m.attachedFiles)
	}

	if m.buildLedger == nil {
		m.buildLedger = ctxpkg.NewTaskLedger()
	}

	// Materialize PendingTodos into typed tasks if no staged tasks exist yet.
	// Parse the formatted string "[TYPE] target — description" back into
	// structured Task fields so the build dispatcher routes to the correct
	// execution path (FILE_MUTATE/SHELL_EXEC) instead of the generic streaming
	// path that produces conversational prose.
	if !hasStagedTasks && hasPendingTodos {
		var tasks []plan.Task
		for i, t := range m.handoffCtx.PendingTodos {
			taskType, taskTarget, taskDesc := parsePendingTodo(t)
			if taskType == "" {
				taskType = "task"
			}
			if taskTarget == "" {
				taskTarget = "workspace"
			}
			tasks = append(tasks, plan.Task{
				StepNum:     i + 1,
				Type:        taskType,
				Target:      taskTarget,
				Description: taskDesc,
				Status:      "idle",
			})
		}
		if len(tasks) > 0 {
			m.sess.StageTaskList(&tasks)
			_ = m.sess.Save()
		}
	}

	// Execute the first idle staged task.
	return m.handleBuildRun(0)
}

// parsePendingTodo extracts the task type, target, and description from a
// PendingTodos string formatted as:
//
//	<icon> [<TYPE>] <target> — <description>
//
// The icon prefix is stripped; the type is extracted from the first bracket
// pair; the target is the text between the closing bracket and the em-dash;
// the description is everything after the em-dash. Returns empty strings for
// any component that cannot be parsed, so the caller can apply defaults.
func parsePendingTodo(todo string) (taskType, taskTarget, taskDesc string) {
	// Strip leading icon (non-space characters before the first space)
	trimmed := strings.TrimSpace(todo)
	if idx := strings.Index(trimmed, " "); idx > 0 {
		trimmed = strings.TrimSpace(trimmed[idx+1:])
	}

	// Extract [TYPE]
	if open := strings.Index(trimmed, "["); open >= 0 {
		if close := strings.Index(trimmed[open:], "]"); close > 0 {
			taskType = strings.TrimSpace(trimmed[open+1 : open+close])
			trimmed = strings.TrimSpace(trimmed[open+close+1:])
		}
	}

	// Split on " — " to separate target from description
	if idx := strings.Index(trimmed, " — "); idx >= 0 {
		taskTarget = strings.TrimSpace(trimmed[:idx])
		taskDesc = strings.TrimSpace(trimmed[idx+3:])
	} else {
		taskTarget = trimmed
	}

	return
}

// handleHotfixCmd implements the $hot urgent hotfix workflow in /build mode.
//
// Flow:
//  1. Stash the current build task queue to .izen/stashed_plan.json (if non-empty).
//  2. Clear the active queue.
//  3. Synthesize a single ad-hoc FILE_MUTATE task with the user's prompt.
//  4. Execute it immediately via handleBuildRun.
//
// After the hotfix task completes (success or failure), the buildResultMsg
// handler in update.go restores the stashed plan deterministically in Go —
// the LLM never sees the original plan state, preventing 7B context drift.
func (m *model) handleHotfixCmd(prompt string) tea.Cmd {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		m.push(roleError, "usage: $hot <hotfix prompt> — e.g. $hot add a MIT LICENSE file")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// Guard: must be in /build mode.
	if m.resolver.Current() != modes.ModeBuild {
		m.push(roleError, "$hot is only available in /build mode")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// Stage 1: Stash the current plan if tasks exist.
	hasTasks := len(m.sess.CurrentTasks) > 0
	if hasTasks {
		if err := m.stashPlan(); err != nil {
			m.push(roleError, fmt.Sprintf("[HOTFIX] Failed to stash current plan: %v", err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
	}

	// Stage 2: Clear the active execution queue.
	m.sess.ClearTasks()

	// Stage 3: Set the hotfix flag so buildResultMsg knows to restore.
	m.hotfixActive = true

	// Stage 4: Create a single ad-hoc FILE_MUTATE task.
	m.push(roleStatus, fmt.Sprintf("[HOTFIX] Urgent hotfix: %s", prompt))

	// ── DYNAMIC TARGET RESOLUTION ─────────────────────────────────────
	// Extract the real target file path from the developer's request.
	// If no file can be resolved, error out early rather than targeting a
	// metadata file inside .izen/ (which would trigger self-patching).
	target := resolveHotfixTarget(prompt)
	if target == "" {
		m.push(roleError, "Could not determine target file. Use @filename — e.g. $hot change year 2023 to 2026 @LICENSE")
		m.hotfixActive = false
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	hotfixTask := plan.Task{
		StepNum:     0,
		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      target,
		Description: prompt,
	}
	tasks := []plan.Task{hotfixTask}
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()

	// Stage 5: Generate the patch but DO NOT apply it. The engine must render
	// a code diff proposal and obtain explicit developer authorization before
	// any byte is written to disk (Bug Fix 2). After approval (y) the patch is
	// applied and the stashed plan restored; on rejection (n) the hotfix aborts
	// cleanly and returns the pipeline to PAUSED without touching any file.
	//
	// ── ACTIVE LOADING INDICATOR (Feature) ────────────────────────────
	// Mount the spinner + emit the first lifecycle log IMMEDIATELY so the
	// developer never sees a 30s frozen pane while the local LLM silently
	// generates the patch. The spinner keeps animating until the proposal
	// message arrives and swaps the pane into the diff view.
	m.push(roleStatus, "[HOTFIX] Generating patch via local LLM... (This may take up to 30s)")
	m.push(roleSystem, fmt.Sprintf("  ⚙ Thinking... (Invoking %s)", m.cfg.ActiveModelName()))

	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "hotfix"
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.lastAgentActivity = time.Now()

	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "hotfix"} },
		m.proposeHotfixPatch(&hotfixTask),
		m.spinnerTickCmd(),
		m.hotfixProgressCmd(),
	)
}

// hotfixProgressCmd emits the $hot generation lifecycle log lines on a timer so
// the developer sees active progress (and the spinner keeps animating) while
// the local LLM silently generates the patch — eliminating the 30s "deadlock"
// freeze. The lines are delivered as hotfixProgressMsg through the event loop,
// never from the background goroutine, so there is no data race on the record
// buffer.
func (m *model) hotfixProgressCmd() tea.Cmd {
	lines := []string{
		"  ↺ Intercepted structural breakdown. Refining context (Attempt 1/2)...",
		"  ⚙ Compiling unified diff schema...",
	}
	var cmds = make([]tea.Cmd, 0, len(lines))
	for i, line := range lines {
		delay := time.Duration(i+1) * 900 * time.Millisecond
		l := line
		cmds = append(cmds, tea.Tick(delay, func(time.Time) tea.Msg {
			return hotfixProgressMsg{Line: l}
		}))
	}
	return tea.Batch(cmds...)
}

// sanitizeFileOutput cleans generated file content produced by the local model
// before it is written to disk. It trims leading/trailing whitespace and strips
// a single wrapping code block: an opening fence ("```", "```go", "```mit",
// etc.) and a closing "```". Without this, literal triple backticks are written
// into the file, corrupting its syntax.
func sanitizeFileOutput(content string) string {
	content = strings.TrimSpace(content)
	// Check for markdown block prefix.
	if strings.HasPrefix(content, "```") {
		// Find the end of the opening fence line.
		if idx := strings.Index(content, "\n"); idx != -1 {
			content = content[idx+1:]
		}
	}
	// Check for markdown block suffix.
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

// resolveHotfixTarget extracts the concrete destination file path for a $hot
// request from the developer's natural-language prompt. It scans for explicit
// file path tokens (e.g. cmd/api/main.go, ./LICENSE, @internal/foo/bar.go) and
// returns the first plausible one. The bare token "workspace" is explicitly
// rejected — it denotes the project-root scope, not a file name. When no file
// is named, a sensible default is derived from the prompt keywords for
// well-known files. Returns "" when no target can be resolved (caller must
// handle the error).
//
// Guardrails:
//   - Paths inside .izen/ are blocked (metadata directory, not a patch target).
//   - .patch files are blocked (cannot self-patch hotfix artifacts).
func resolveHotfixTarget(prompt string) string {
	// Strip @ prefix so @LICENSE, @cmd/api/main.go resolve correctly.
	prompt = strings.ReplaceAll(prompt, "@", "")

	// Candidate path tokens: sequences of word/path chars including slashes,
	// dots and an extension, or bare "LICENSE"/"Makefile"-style names.
	pathRe := regexp.MustCompile(`(?:[./]?[\w-]+(?:/[\w.-]+)+|\.\/?[\w.-]+|[\w.-]+\.[\w]+|(?:LICENSE|Makefile|Dockerfile|README|go\.mod|go\.sum|CHANGELOG|NOTICE))`)
	for _, m := range pathRe.FindAllString(prompt, -1) {
		m = strings.TrimSpace(m)
		if m == "" || strings.EqualFold(m, "workspace") {
			continue
		}
		// Normalize a leading "./" to a repo-relative path.
		m = strings.TrimPrefix(m, "./")
		m = strings.TrimPrefix(m, "/")
		if m == "" {
			continue
		}
		// Block self-patching: reject .izen/ paths and .patch files.
		if strings.HasPrefix(m, ".izen/") || strings.Contains(m, "/.izen/") ||
			strings.HasSuffix(m, ".patch") {
			continue
		}
		// Sanity: must contain a path separator or an extension, and must not
		// be a single bare word that merely looks like an extension.
		if strings.Contains(m, "/") || strings.Contains(m, ".") {
			return m
		}
	}

	// ── No explicit file named: synthesize a target from keywords.
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "license"):
		return "LICENSE"
	case strings.Contains(lower, "readme"):
		return "README.md"
	case strings.Contains(lower, "docker"):
		return "Dockerfile"
	case strings.Contains(lower, "makefile") || strings.Contains(lower, "make file"):
		return "Makefile"
	case strings.Contains(lower, "changelog"):
		return "CHANGELOG.md"
	case strings.Contains(lower, "notice"):
		return "NOTICE"
	case strings.Contains(lower, "gitignore"):
		return ".gitignore"
	}
	// No target could be resolved — caller must handle the error.
	return ""
}

// proposeHotfixPatch generates the patch for a $hot FILE_MUTATE task via the
// LLM (one non-streaming call) WITHOUT applying it. Instead, it renders a code
// diff proposal and freezes the pipeline in StateAwaitingApproval so the
// developer can authorize (y) or reject (n) the change before any disk write.
func (m *model) proposeHotfixPatch(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		if m.provider == nil {
			return hotfixProposalMsg{Err: fmt.Errorf("build execution error: no provider configured")}
		}

		// ── CRITICAL: Read existing file content BEFORE calling the LLM ──
		// Without the original content in the prompt, local LLMs hallucinate
		// a full-file rewrite that silently deletes all existing content.
		// The original is read here (pre-LLM) for prompt context AND below
		// (post-LLM) for diff computation — single read, dual use.
		var orig string
		if data, rerr := os.ReadFile(task.Target); rerr == nil {
			orig = string(data)
		}

		// Build a focused, non-chat patch-generation prompt with full file
		// context so the LLM produces precise SEARCH/REPLACE blocks or
		// unified diffs rather than a destructive full-file replacement.
		handoff := ctxpkg.SanitizeBuildHandoff(task, "")
		if orig != "" {
			handoff += "\n\n### TARGET_FILE_CONTENT\n```\n" + orig + "\n```\n"
			handoff += "\nModify the above file content to fulfill the task. "
			handoff += "Output a SEARCH/REPLACE block (`<<<<<<< SEARCH`) or a unified diff. "
			handoff += "Do NOT output a full FILE: block — the file already exists."
		}
		system := prompt.BuildContract()
		req := ai.Request{
			Model:    m.cfg.ActiveModelName(),
			System:   system,
			Stream:   false,
			Messages: []ai.Message{{Role: "user", Content: handoff}},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		resp, err := m.provider.Execute(ctx, req)
		if err != nil {
			return hotfixProposalMsg{Err: fmt.Errorf("patch generation failed: %w", err)}
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			return hotfixProposalMsg{Err: fmt.Errorf("patch generation returned empty output")}
		}

		// ── STEP 1/2: Content Cleanse — strip markdown code fences the local
		// model wraps around the generated file (e.g. "```mit ... ```"). Writing
		// the raw text verbatim injects literal triple backticks into the
		// document and corrupts its syntax, so we sanitize BEFORE the diff is
		// computed and the patch is staged for disk write.
		cleaned := sanitizeFileOutput(resp.Content)

		// Compute a unified diff for display (green additions / red removals).
		diff := computeUnifiedDiff(task.Target, orig, cleaned)

		patch := &execution.Patch{
			ID:        fmt.Sprintf("hotfix-%d", task.StepNum),
			File:      task.Target,
			Original:  orig,
			Modified:  cleaned,
			TaskID:    task.StepNum,
			ContextID: m.sess.ContextID,
		}

		return hotfixProposalMsg{
			Task:  task,
			Patch: patch,
			Diff:  diff,
		}
	}
}

// proposeStdlibBuildPatch generates a patch for a hardcoded stdlib case-correction
// FILE_MUTATE task WITHOUT calling the LLM. It reads the actual file from disk,
// applies the deterministic symbol case + import fix, computes the unified diff,
// and returns a buildProposalReadyMsg for human approval — identical UX to the
// LLM-based patch flow but with zero model cost and no placeholder-code risk.
func (m *model) proposeStdlibBuildPatch(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		// Extract fix parameters from Solution: "STDLIB:symbol:pkgName:importPath"
		parts := strings.SplitN(task.Solution, ":", 4)
		if len(parts) != 4 || parts[0] != "STDLIB" {
			return buildProposalReadyMsg{Err: fmt.Errorf("invalid stdlib fix solution format: %q", task.Solution)}
		}
		symbol, pkgName, importPath := parts[1], parts[2], parts[3]

		// Read actual file and compute deterministic fix.
		orig, modified, err := retrieval.ApplyStdlibCaseFix(task.Target, symbol, pkgName, importPath)
		if err != nil {
			return buildProposalReadyMsg{Err: fmt.Errorf("stdlib fix failed for %s: %w", task.Target, err)}
		}

		diff := computeUnifiedDiff(task.Target, orig, modified)

		patch := &execution.Patch{
			ID:        fmt.Sprintf("stdlib-%d", task.StepNum),
			File:      task.Target,
			Original:  orig,
			Modified:  modified,
			TaskID:    task.StepNum,
			ContextID: m.sess.ContextID,
		}

		return buildProposalReadyMsg{
			Task:   task,
			Patch:  patch,
			Diff:   diff,
			Output: fmt.Sprintf("Applied stdlib case-correction: %q -> %q + import %q in %s", symbol, pkgName, importPath, task.Target),
		}
	}
}

// proposeBuildPatch generates a patch for a regular FILE_MUTATE / GIT_ACTION
// build task via the LLM (one non-streaming call) WITHOUT applying it. Instead
// it returns a buildProposalReadyMsg so the update loop can extract proposals
// and freeze the pipeline in StateAwaitingProposal for human approval before
// any disk write occurs.
//
// Auto-retry: if the LLM returns an ambiguous snippet (no SEARCH/REPLACE markers,
// no diff headers) for an existing file, the function re-prompts the LLM with the
// rejection error up to 2 times before giving up. This prevents the human from
// seeing a bad patch and avoids a pointless fail cycle.
func (m *model) proposeBuildPatch(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		if m.provider == nil {
			return buildProposalReadyMsg{Err: fmt.Errorf("build execution error: no provider configured")}
		}

		// ── CRITICAL: Read the target file BEFORE the LLM call ──────────
		// Without the actual file content in the prompt, the LLM hallucinates
		// the original content (e.g. "Copyright (c) 2023 Jay") and generates
		// a unified diff that can never match the file on disk, producing:
		//   "patch hunk does not match file content".
		//
		// The original is read once here and included in both the initial
		// handoff and any retry handoff so the LLM always sees real content.
		var orig string
		if data, rerr := os.ReadFile(task.Target); rerr == nil {
			orig = string(data)
		}

		baseHandoff := ctxpkg.SanitizeBuildHandoff(task, "")
		if orig != "" {
			baseHandoff += "\n\n### TARGET_FILE_CONTENT\n```\n" + orig + "\n```\n"
			baseHandoff += "\nModify the above file content to fulfill the task. "
			baseHandoff += "Output a SEARCH/REPLACE block (`<<<<<<< SEARCH`) or a unified diff. "
			baseHandoff += "Do NOT output a full FILE: block — the file already exists."
		}
		system := prompt.BuildContract()
		maxRetries := 2
		handoff := baseHandoff

		for attempt := 0; attempt <= maxRetries; attempt++ {
			req := ai.Request{
				Model:    m.cfg.ActiveModelName(),
				System:   system,
				Stream:   false,
				Messages: []ai.Message{{Role: "user", Content: handoff}},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			resp, err := m.provider.Execute(ctx, req)
			cancel()
			if err != nil {
				return buildProposalReadyMsg{Err: fmt.Errorf("patch generation failed: %w", err)}
			}
			if resp == nil || strings.TrimSpace(resp.Content) == "" {
				return buildProposalReadyMsg{Err: fmt.Errorf("patch generation returned empty output")}
			}

			cleaned := sanitizeFileOutput(resp.Content)

			// Validate patch format BEFORE presenting to human.
			// If the snippet is ambiguous (no SEARCH/REPLACE markers, no diff
			// headers, significantly smaller than the original), reject early
			// and retry with explicit SEARCH/REPLACE formatting instructions.
			// The retry prompt also includes the actual file content so the
			// LLM can generate a correct patch.
			if execution.IsAmbiguousSnippet(orig, cleaned) {
				if attempt < maxRetries {
					handoff = fmt.Sprintf(
						"Your proposed patch for %s was rejected due to invalid format: Ambiguous snippet without SEARCH/REPLACE markers. Re-send the modification using strict <<<<<<< SEARCH ... ======= ... >>>>>>> blocks.\n\nOriginal task:\n%s",
						task.Target, baseHandoff)
					continue
				}
				return buildProposalReadyMsg{Err: fmt.Errorf("%w: ambiguous snippet without SEARCH/REPLACE markers for existing file %s — retry with SEARCH/REPLACE block or unified diff", execution.ErrInvalidPatchFormat, task.Target)}
			}

			diff := computeUnifiedDiff(task.Target, orig, cleaned)

			patch := &execution.Patch{
				ID:        fmt.Sprintf("build-%d", task.StepNum),
				File:      task.Target,
				Original:  orig,
				Modified:  cleaned,
				TaskID:    task.StepNum,
				ContextID: m.sess.ContextID,
			}

			return buildProposalReadyMsg{
				Task:   task,
				Patch:  patch,
				Diff:   diff,
				Output: resp.Content,
			}
		}

		return buildProposalReadyMsg{Err: fmt.Errorf("patch generation failed after %d retries: ambiguous snippet format", maxRetries)}
	}
}

// applyHotfixPatch applies a pre-generated $hot patch through the execution
// engine's PatchManager — never via the conversational stream. It returns a
// buildResultMsg so the standard update.go handler restores the stashed plan
// and freezes the pipeline to PAUSED afterwards. On failure the task is marked
// failed and the buildResultMsg handler rolls back any partial mutation.
func (m *model) applyHotfixPatch(task *plan.Task, patch *execution.Patch) tea.Cmd {
	return func() tea.Msg {
		if applyErr := m.execEng.Patches.Apply(patch); applyErr != nil {
			tasks := m.sess.CurrentTasks
			for i := range tasks {
				if tasks[i].StepNum == task.StepNum {
					tasks[i].Status = "failed"
					break
				}
			}
			m.sess.StageTaskList(&tasks)
			_ = m.sess.Save()
			return buildResultMsg{
				output:   patch.Modified,
				exitCode: 1,
				err:      fmt.Errorf("hotfix patch apply failed: %w", applyErr),
			}
		}

		// Mark the task terminal in the live session ledger.
		tasks := m.sess.CurrentTasks
		for i := range tasks {
			if tasks[i].StepNum == task.StepNum {
				tasks[i].Status = "completed"
				break
			}
		}
		m.sess.StageTaskList(&tasks)
		_ = m.sess.Save()
		return buildResultMsg{
			output:   fmt.Sprintf("Applied hotfix patch to %s", task.Target),
			exitCode: 0,
		}
	}
}

// computeUnifiedDiff produces a line-oriented unified diff (a la `diff -u`)
// between the original and modified file contents. Lines present only in
// original are prefixed "-" (red) and lines only in modified are prefixed "+"
// (green) — matching the visual contract required by the hotfix approval gate.
// The header uses the conventional `--- a/<file>` / `+++ b/<file>` markers so
// the MutationRenderer's new-file detection and gutter rendering behave
// correctly for both edits and new-file creations.
func computeUnifiedDiff(path, original, modified string) string {
	origLines := strings.Split(original, "\n")
	modLines := strings.Split(modified, "\n")
	// Trailing empty element produced by a final "\n" — drop for clean diffs.
	trim := func(s []string) []string {
		if len(s) > 0 && s[len(s)-1] == "" {
			return s[:len(s)-1]
		}
		return s
	}
	origLines = trim(origLines)
	modLines = trim(modLines)

	var b strings.Builder
	if original == "" {
		// New file: only additions.
		b.WriteString("--- a/" + path + "\n")
		b.WriteString("+++ b/" + path + "\n")
		for _, line := range modLines {
			b.WriteString("+" + line + "\n")
		}
		return b.String()
	}

	b.WriteString("--- a/" + path + "\n")
	b.WriteString("+++ b/" + path + "\n")

	// Classic longest-common-subsequence alignment keeps the diff minimal.
	n, m := len(origLines), len(modLines)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case origLines[i] == modLines[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case origLines[i] == modLines[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			b.WriteString("-" + origLines[i] + "\n")
			i++
		default:
			b.WriteString("+" + modLines[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		b.WriteString("-" + origLines[i] + "\n")
	}
	for ; j < m; j++ {
		b.WriteString("+" + modLines[j] + "\n")
	}
	return b.String()
}

// findFailedBuildTask returns the step number of the first task in the build
// queue whose status is "failed" or "stalled". Returns 0 if no such task exists.
func (m *model) findFailedBuildTask() int {
	for _, t := range m.sess.CurrentTasks {
		if t.Status == "failed" || t.Status == "stalled" {
			return t.StepNum
		}
	}
	return 0
}

// amendBuildTask resets a failed/stalled task to "idle", appends the user's
// feedback to its description, saves the updated task list, and re-executes
// the task with the amendment as additional context. This replaces the old
// behavior of stubbornly re-running the exact same failed command with no
// opportunity for the user to provide corrective input.
func (m *model) amendBuildTask(stepNum int, feedback string) tea.Cmd {
	tasks := m.sess.CurrentTasks
	for i := range tasks {
		if tasks[i].StepNum == stepNum {
			tasks[i].Status = "idle"
			tasks[i].Description = tasks[i].Description + " | AMENDMENT: " + feedback
			break
		}
	}
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
	return m.handleBuildRun(stepNum)
}

// runBuildShellExec executes a SHELL_EXEC build task directly via the OS shell
// and reports the result — it never dispatches the command to the LLM. After a
// run the task is marked terminal and the next idle task is advanced, preserving
// /build's execute-only contract.
//
// HARD GATE: commands containing "sudo" or other OS-escalation keywords are
// intercepted immediately and returned as a blocked buildResultMsg instead of
// being executed. The user must copy the command and run it manually outside
// IZEN. This is the absolute last line of defense against silent root escalation.
func (m *model) runBuildShellExec(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		// ── SUDO / PRIVILEGE ESCALATION INTERCEPT ──────────────────────
		lower := strings.ToLower(strings.TrimSpace(task.Target))
		if strings.Contains(lower, "sudo") {
			return buildResultMsg{
				output:   "",
				exitCode: -1,
				err: fmt.Errorf(
					"[SUDO BLOCKED] SHELL_EXEC task requires sudo: %s; "+
						"IZEN never runs sudo automatically. Copy the command above and "+
						"run it manually in your terminal outside IZEN, then re-run /build",
					task.Target),
			}
		}
		// ── OS-FENCE on Darwin: block Linux-only package manager commands ──
		if runtime.GOOS == "darwin" {
			linuxPatterns := []string{"apt-get", "apt ", "dpkg", "yum ", "dnf "}
			for _, pat := range linuxPatterns {
				if strings.Contains(lower, pat) {
					return buildResultMsg{
						output:   "",
						exitCode: -1,
						err: fmt.Errorf(
							"[OS MISMATCH] SHELL_EXEC task uses %q which is a Linux package manager; "+
								"this host is macOS; use Homebrew (`brew`) or `go install` instead",
							strings.TrimSpace(pat)),
					}
				}
			}
		}
		// ── Sandbox check before execution ──────────────────────────────
		runner := execExecutionRunner(".")
		if blocked, reason := m.shellFirewall(task.Target); blocked {
			return buildResultMsg{
				output:   "",
				exitCode: -1,
				err:      fmt.Errorf("[BLOCKED BY FIREWALL] %s", reason),
			}
		}

		result, err := runner.Run(task.Target)
		output := ""
		exitCode := 0
		if result != nil {
			output = result.Stdout
			if result.Stderr != "" {
				if output != "" {
					output += "\n"
				}
				output += result.Stderr
			}
			exitCode = result.ExitCode
		}
		if err != nil && output == "" {
			output = err.Error()
			if exitCode == 0 {
				exitCode = 1
			}
		}

		// Mark the task terminal in the live session ledger so the queue
		// advances and the developer sees progress.
		tasks := m.sess.CurrentTasks
		for i := range tasks {
			if tasks[i].StepNum == task.StepNum {
				if exitCode == 0 {
					tasks[i].Status = "completed"
				} else {
					tasks[i].Status = "failed"
				}
				break
			}
		}
		m.sess.StageTaskList(&tasks)
		_ = m.sess.Save()
		return buildResultMsg{output: output, exitCode: exitCode, err: err}
	}
}

func (m *model) handleBuildRun(stepNum int) tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		m.push(roleStatus, "no tasks staged — use /plan first")
		return nil
	}
	var targetTask *plan.Task
	if stepNum > 0 {
		for i, t := range tasks {
			if t.StepNum == stepNum {
				targetTask = &tasks[i]
				break
			}
		}
		if targetTask == nil {
			m.push(roleStatus, fmt.Sprintf("task %d not found", stepNum))
			return nil
		}
		if targetTask.Status == "stalled" || targetTask.Status == "failed" {
			m.push(roleError, fmt.Sprintf("[BUILD HALTED] Task %d is %s. Use /investigate or /plan to re-generate a valid ledger.", stepNum, targetTask.Status))
			return nil
		}
	} else {
		for i, t := range tasks {
			if t.Status == "idle" {
				targetTask = &tasks[i]
				break
			}
		}
	}
	if targetTask == nil {
		// Check if any tasks are stalled (failed build), if so give a
		// better diagnostic than the generic "all tasks already completed".
		for _, t := range tasks {
			if t.Status == "stalled" {
				m.push(roleError, "[BUILD HALTED] A previous step failed. Remaining tasks are stalled. Use /investigate or /plan to re-generate a valid ledger.")
				return nil
			}
		}
		m.push(roleStatus, "all tasks already completed")
		return nil
	}
	targetTask.Status = "processing"
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
	m.push(roleStatus, fmt.Sprintf("executing step %d: %s — %s", targetTask.StepNum, targetTask.Type, targetTask.Target))

	content := fmt.Sprintf(
		"EXECUTION MODE — implement ONLY this task. "+
			"ZERO conversational text, ZERO explanations, ZERO greetings, ZERO summaries.\n"+
			"YOUR FIRST OUTPUT TOKEN MUST BE A SEARCH/REPLACE BLOCK (for existing files) "+
			"OR A FILE_CREATE BLOCK (for new files).\n"+
			"Do NOT output JSON, do NOT restate the plan, do NOT list other tasks.\n"+
			"Do NOT ask questions, do NOT ask for clarification, do NOT acknowledge.\n\n"+
			"Step %d: %s\nTarget: %s\nDescription: %s",
		targetTask.StepNum, targetTask.Type, targetTask.Target, targetTask.Description)

	if m.graph != nil {
		compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.ObjectiveIntent())
		compressed := compressor.CompressLines(content)
		if compressed != "" && compressed != content {
			content = retrieval.FormatCompressedFrame(compressed) + "\n\n" + content
		}
		g := m.graph
		go retrieval.BuildGlobalCompressor(g, m.sess.ObjectiveIntent())
	}

	m.responseBuffer.Reset()
	m.execEng.SetStreamContextFiles(m.attachedFiles)

	// Bridge the live /plan task ledger into the execution engine: the patch
	// manager marks task Completed and renders the build summary on commit.
	if m.buildLedger == nil {
		m.buildLedger = ctxpkg.NewTaskLedger()
	}
	m.currentBuildTaskID = targetTask.StepNum
	m.execEng.Patches.SetLedger(m.buildLedger)
	m.execEng.Patches.SetContextID(m.sess.ContextID)

	// ── SHELL_EXEC: INTERACTIVE APPROVAL GATE ──────────────────────────
	// CRITICAL SECURITY CONSTRAINT: Every SHELL_EXEC command requires
	// explicit human approval before it reaches the OS shell. A dedicated
	// visual "Permission Required" box is rendered in the proposal dock,
	// with single-character key bindings:
	//   [y] Allow Once    [a] Allow Always    [n] Reject
	// If the user previously selected "Allow Always" (m.pendingBuildAllowAlways),
	// the gate is bypassed for the remainder of the session.
	if targetTask.Type == "SHELL_EXEC" {
		// ── Allow Always bypass ────────────────────────────────────────
		if m.pendingBuildAllowAlways {
			return tea.Batch(
				func() tea.Msg { return agentStartMsg{label: "shell exec"} },
				m.runBuildShellExec(targetTask),
				m.spinnerTickCmd(),
			)
		}

		// Render the visual permission box via the proposal dock (view layer).
		m.pendingBuildApproval = true
		m.pendingBuildTask = targetTask
		m.state = StateAwaitingApproval
		m.ti.Blur()
		m.recalcViewportHeight()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── FILE_MUTATE / GIT_ACTION: generate a patch for human approval ────
	// These tasks mutate the workspace. /build MUST NOT apply any mutation
	// without explicit human sign-off. The patch is generated by the LLM in a
	// non-streaming call and returned as buildProposalReadyMsg, which freezes
	// the pipeline in StateAwaitingApproval and renders a unified diff for
	// explicit authorization (Alt+A / Alt+L / Alt+R).
	if targetTask.Type == "FILE_MUTATE" || targetTask.Type == "GIT_ACTION" {
		// ── DETERMINISTIC STDLIB FIX (no LLM) ──────────────────────────
		// Hardcoded stdlib case-correction tasks carry fix parameters in
		// the Solution field ("STDLIB:symbol:pkgName:importPath"). Apply
		// the fix directly by reading the actual file and computing the
		// targeted replacement — bypassing the LLM entirely. This prevents
		// the model from generating placeholder code ("// existing code")
		// and ensures the file is mutated in-place at the correct location.
		if targetTask.IsHardcoded && strings.HasPrefix(targetTask.Solution, "STDLIB:") {
			return tea.Batch(
				func() tea.Msg { return agentStartMsg{label: "stdlib patch"} },
				m.proposeStdlibBuildPatch(targetTask),
				m.spinnerTickCmd(),
			)
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "patching"} },
			m.proposeBuildPatch(targetTask),
			m.spinnerTickCmd(),
		)
	}

	buildTrace := &ctxpkg.CodebaseTrace{
		MatchedFiles:    []string{targetTask.Target},
		ResolvedSymbols: []string{targetTask.Target},
	}
	return tea.Batch(
		func() tea.Msg { return traceUpdateMsg{trace: buildTrace} },
		m.streamCmd(content),
	)
}

func (m *model) handleReviewTestConfirm(line string) tea.Cmd {
	m.pendingTestConfirm = false
	target := strings.TrimSpace(line)
	if target == "" || target == "y" || target == "yes" {
		return m.runTestEngine("./...")
	}
	return m.runTestEngine(target)
}

// countGoFiles walks the repository root and counts .go source files,
// excluding vendor/, .izen/, node_modules/, and other generated directories.
func countGoFiles(root string) int {
	count := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".izen" || base == "node_modules" ||
				base == ".git" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".go") {
			count++
		}
		return nil
	})
	return count
}

func (m *model) runTestCmd(target string) tea.Cmd {
	if target == "" {
		goFileCount := countGoFiles(".")
		if goFileCount >= 50 {
			warning := fmt.Sprintf(
				"[!] WARNING: Repository contains %d Go source files.\n"+
					"    Running global ./... will scan the entire project.\n"+
					"    Estimated token weight: ~%dk tokens.\n\n"+
					"    Press Enter to confirm global execution, or type a specific\n"+
					"    target path (e.g. ./pkg/foo, ./internal/bar/...).",
				goFileCount, goFileCount*8,
			)
			m.push(roleSystem, warningStyle.Render(warning))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			m.pendingTestConfirm = true
			m.pendingTestTarget = "./..."
			return nil
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "testing"} },
			m.runTestEngine("./..."),
			m.spinnerTickCmd(),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "testing"} },
		m.runTestEngine(target),
		m.spinnerTickCmd(),
	)
}

func (m *model) runRunCmd(target string) tea.Cmd {
	if target == "" {
		goFileCount := countGoFiles(".")
		if goFileCount >= 50 {
			warning := fmt.Sprintf(
				"[!] WARNING: Repository contains %d Go source files.\n"+
					"    Running global ./... will scan the entire project.\n"+
					"    Estimated token weight: ~%dk tokens.\n\n"+
					"    Press Enter to confirm global execution, or type a specific\n"+
					"    target path (e.g. ./pkg/foo, ./internal/bar/...).",
				goFileCount, goFileCount*8,
			)
			m.push(roleSystem, warningStyle.Render(warning))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			m.pendingTestConfirm = true
			m.pendingTestTarget = "./..."
			return nil
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "building"} },
			m.runBuildEngine("./..."),
			m.spinnerTickCmd(),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "building"} },
		m.runBuildEngine(target),
		m.spinnerTickCmd(),
	)
}

func (m *model) runTestEngine(target string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = TaskFinishedMsg{}
			}
		}()
		runner := execExecutionRunner(".")
		cmd := "go test -v " + target
		result, err := runner.Run(cmd)
		output := ""
		passed := true
		failedCount := 0
		totalCount := 0

		if result != nil {
			output = result.Stdout
			if result.Stderr != "" {
				if output != "" {
					output += "\n"
				}
				output += result.Stderr
			}
			// Count pass/fail lines
			for _, line := range strings.Split(output, "\n") {
				if strings.Contains(line, "--- FAIL:") {
					failedCount++
				}
				if strings.Contains(line, "--- PASS:") {
					totalCount++
				}
			}
			totalCount += failedCount
			if result.ExitCode != 0 || failedCount > 0 {
				passed = false
			}
		}
		if err != nil && output == "" {
			output = err.Error()
			passed = false
		}

		// ── Compile/Build failure detection ───────────────────────────────
		// When `go test` encounters a build error (syntax, missing import, etc.)
		// it exits non-zero with 0 tests run. Treat this as an active diagnostic
		// event: generate a Context ID, persist the session, and write the log
		// so $trace can find it.
		isCompileFailure := result != nil && result.ExitCode != 0 && totalCount == 0 && failedCount == 0
		if isCompileFailure && m.sess != nil {
			ctxID := ctxpkg.GenerateContextID("go")
			m.sess.ContextID = ctxID
			m.sess.RunNumber++
			_ = m.sess.Save()
		}

		// Persist test output to context log file for auto-trace ($trace without args)
		if m.sess != nil && m.sess.ContextID != "" {
			logPath := m.sess.TestRunLogPath()
			if logDir := filepath.Dir(logPath); logDir != "" {
				if mkErr := os.MkdirAll(logDir, 0755); mkErr == nil {
					_ = os.WriteFile(logPath, []byte(output), 0644)
				}
			}
		}

		return testResultMsg{
			output: output,
			passed: passed,
			failed: failedCount,
			total:  totalCount,
			err:    err,
		}
	}
}

func (m *model) runBuildEngine(target string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = TaskFinishedMsg{}
			}
		}()
		runner := execExecutionRunner(".")
		cmd := "go build " + target
		result, err := runner.Run(cmd)
		output := ""
		exitCode := 0

		if result != nil {
			output = result.Stdout
			if result.Stderr != "" {
				if output != "" {
					output += "\n"
				}
				output += result.Stderr
			}
			exitCode = result.ExitCode
		}
		if err != nil && output == "" {
			output = err.Error()
			if exitCode == 0 {
				exitCode = 1
			}
		}

		return buildResultMsg{
			output:   output,
			exitCode: exitCode,
			err:      err,
		}
	}
}

func execExecutionRunner(root string) *executionRunner {
	return &executionRunner{root: root}
}

type executionRunner struct {
	root string
}

func (r *executionRunner) Run(command string) (*executionRunResult, error) {
	c := exec.CommandContext(context.Background(), "bash", "-c", command)
	c.Dir = r.root
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	execution.TrackProcess(c)
	defer execution.UntrackProcess(c)
	err := c.Run()
	result := &executionRunResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result, err
}

type executionRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func (m *model) runFixCmd(target string) tea.Cmd {
	// ── FAIL-SAFE: Belt-and-suspenders write-capability guard ────────────
	if !m.resolver.Current().CanWrite() && !m.resolver.Current().CanPatch() {
		m.cancelStaleAgentOps()
		m.push(roleSystem, mutedStyle.Render("Write access required. Switch to /build."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	if m.lastTestOutput == "" {
		m.push(roleError, "no previous test/run output available — run $test or $run first")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "fixing"}
		},
		m.spinnerTickCmd(),
		func() tea.Msg {
			output := m.lastTestOutput
			frames := investigate.ParseStackFrames(output)

			var fixCtx strings.Builder
			fixCtx.WriteString("## FAILURE LOG\n\n```\n")
			fixCtx.WriteString(output)
			fixCtx.WriteString("\n```\n\n")

			if len(frames) > 0 {
				fixCtx.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&fixCtx, "### %s:%d\n\n", slice.File, slice.Line)
						fixCtx.WriteString("```go\n")
						for _, cline := range slice.Context {
							fixCtx.WriteString(cline)
							fixCtx.WriteString("\n")
						}
						fixCtx.WriteString("```\n\n")
					}
				}
			}

			if m.lastTestTarget != "" {
				fmt.Fprintf(&fixCtx, "**Target:** `%s`\n\n", m.lastTestTarget)
			}

			fixCtx.WriteString("## INSTRUCTION — AUTO-RECOVERY MODE\n")
			fixCtx.WriteString("MODE: AUTO-RECOVERY — execute a targeted fix.\n\n")
			fixCtx.WriteString("PURPOSE:\n")
			fixCtx.WriteString("- Apply the minimal code change to fix the compilation error below.\n")
			fixCtx.WriteString("- Output ONLY compilable code. No analysis, no explanations.\n\n")
			fixCtx.WriteString("FORBIDDEN:\n")
			fixCtx.WriteString("- Do NOT output conversational text of any kind.\n")
			fixCtx.WriteString("- Do NOT greet, summarize, or restate the problem.\n")
			fixCtx.WriteString("- The first output token MUST be ```diff or FILE:. ZERO exceptions.\n\n")
			fixCtx.WriteString("OUTPUT FORMAT:\n")
			fixCtx.WriteString("- Unified diff (```diff ... ```) for existing files.\n")
			fixCtx.WriteString("- FILE: block for new files or full rewrites.\n")
			fixCtx.WriteString("- No markdown outside code blocks.\n")
			fixCtx.WriteString("- No conversational setup, no sign-off.\n")

			return fixResultMsg{content: fixCtx.String()}
		},
	)
}

// ── $log (view mode) — Filtered mutation log display ──────────────────────────
// runLogViewCmd reads .izen/audit/mutations.log and renders entries as a
// rigidly-bounded, non-breaking box. Uses utf8.RuneCountInString for Unicode
// width checks and lipgloss.Width for ANSI-styled segments. Every row is
// truncated or padded to an exact contentWidth rune count so the border
// frame can never warp.
func (m *model) runLogViewCmd(showAll bool) tea.Cmd {
	ctxID := ""
	if !showAll && m.sess != nil {
		ctxID = m.sess.ContextID
	}
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = TaskFinishedMsg{}
			}
		}()
		logPath := filepath.Join(".izen", "audit", "mutations.log")
		data, err := os.ReadFile(logPath)
		if err != nil {
			m.push(roleStatus, "No mutations found.")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return agentDoneMsg{}
		}

		rawLines := strings.Split(string(data), "\n")
		type logEntry struct {
			Timestamp string `json:"timestamp"`
			Role      string `json:"role"`
			Mode      string `json:"mode"`
			Preview   string `json:"preview"`
		}

		// ── Fixed box geometry ────────────────────────────────────────────
		// Total visual width of the box, derived from main viewport width.
		boxWidth := m.width - 4
		if boxWidth < 40 {
			boxWidth = 40
		}
		if boxWidth > 100 {
			boxWidth = 100
		}
		// Border markers: "│ " (2) + " │" (2) = 4 chars eaten by frame.
		// contentWidth is the exact space available for the inner text line.
		contentWidth := boxWidth - 4

		// ── Static styled components (used for late styling only) ─────────
		bullet := accentStyle.Render("›")

		var formatted []string
		for _, line := range rawLines {
			if line == "" {
				continue
			}
			var entry logEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				// Fallback: pure text geometry
				rawFallback := "› " + line
				fbWidth := runewidth.StringWidth(rawFallback)
				if fbWidth > contentWidth {
					var trimmed strings.Builder
					w := 0
					for _, r := range rawFallback {
						rw := runewidth.RuneWidth(r)
						if w+rw > contentWidth-3 {
							break
						}
						trimmed.WriteRune(r)
						w += rw
					}
					trimmed.WriteString("...")
					rawFallback = trimmed.String()
				} else {
					rawFallback += strings.Repeat(" ", contentWidth-fbWidth)
				}
				styledFallback := strings.Replace(rawFallback, "›", bullet, 1)
				formatted = append(formatted, styledFallback)
				continue
			}
			if ctxID != "" && !strings.Contains(line, "context="+ctxID) {
				continue
			}

			modeLabel := entry.Mode
			if modeLabel == "" {
				modeLabel = "Unknown"
			}

			// ── Sanitize preview ──────────────────────────────────────────
			preview := entry.Preview
			preview = strings.ReplaceAll(preview, "\n", " ")
			preview = strings.ReplaceAll(preview, "```", "`")
			preview = strings.TrimSpace(preview)

			// ── Pre-filtering: detect metadata tokens and rewrite ────────
			hasCtx := strings.Contains(preview, "context=")
			hasPatch := strings.Contains(preview, "patch=")

			switch {
			case hasCtx && hasPatch:
				preview = "Applied structural patch update to repository"
			case hasPatch:
				if idx := strings.Index(preview, "patch="); idx >= 0 {
					rest := preview[idx+6:]
					if spaceIdx := strings.Index(rest, " "); spaceIdx >= 0 {
						rest = rest[:spaceIdx]
					}
					if rest != "" {
						preview = fmt.Sprintf("Synchronized baseline patch for %s", rest)
					} else {
						preview = "Applied structural patch update to repository"
					}
				}
			default:
				preview = stripLogTokens(preview)
			}
			preview = strings.TrimSpace(preview)
			if preview == "" {
				preview = "No details"
			}

			// ── PURE TEXT GEOMETRY (NO LIVE CELL MEASUREMENT) ─────────
			// 1. Build 100% raw plain text line.
			rawLine := "› [" + modeLabel + " Mode] " + preview

			// 2. Rigid truncation & padding using visual cell width.
			lineWidth := runewidth.StringWidth(rawLine)
			if lineWidth > contentWidth {
				if contentWidth > 3 {
					var trimmed strings.Builder
					w := 0
					for _, r := range rawLine {
						rw := runewidth.RuneWidth(r)
						if w+rw > contentWidth-3 {
							break
						}
						trimmed.WriteRune(r)
						w += rw
					}
					trimmed.WriteString("...")
					rawLine = trimmed.String()
				} else {
					var trimmed strings.Builder
					w := 0
					for _, r := range rawLine {
						rw := runewidth.RuneWidth(r)
						if w+rw > contentWidth {
							break
						}
						trimmed.WriteRune(r)
						w += rw
					}
					rawLine = trimmed.String()
				}
			} else {
				rawLine += strings.Repeat(" ", contentWidth-lineWidth)
			}

			// 3. Late styling — rawLine now occupies exactly contentWidth columns.
			modeTag := "[" + modeLabel + " Mode]"
			parsedMode, _ := modes.Parse(strings.ToLower(modeLabel))
			styledModeTag := lipgloss.NewStyle().Foreground(modeAccentColor(parsedMode)).Render(modeTag)
			styledLine := strings.Replace(rawLine, "›", bullet, 1)
			if idx := strings.Index(styledLine, modeTag); idx >= 0 {
				styledLine = styledLine[:idx] + styledModeTag + styledLine[idx+len(modeTag):]
			}
			formatted = append(formatted, styledLine)
		}

		if len(formatted) == 0 {
			msg := "No log entries."
			if ctxID != "" {
				msg += " Context: " + ctxID
			}
			m.push(roleStatus, msg)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return agentDoneMsg{}
		}

		// ── Render the rigid box ──────────────────────────────────────────
		var b strings.Builder

		// Top border: ┌─ $log: Mutation History ────────────────┐
		topPrefix := "┌─ $log: Mutation History "
		b.WriteString(topPrefix)
		fillTop := boxWidth - runewidth.StringWidth(topPrefix) - 1
		if fillTop > 0 {
			b.WriteString(strings.Repeat("─", fillTop))
		}
		b.WriteString("┐\n")

		// Content rows: │ › [Build Mode] text{padded} │
		for _, line := range formatted {
			b.WriteString("│ ")
			b.WriteString(line)
			b.WriteString(" │\n")
		}

		// Bottom border: └──────────────────────────────────────┘
		b.WriteString("└" + strings.Repeat("─", boxWidth-2) + "┘")

		m.push(roleStatus, b.String())

		// ── Append review provenance box if an active ledger exists ──
		if m.currentReviewLedger != nil {
			pr := riview.NewProvenanceRenderer(m.currentReviewLedger, boxWidth)
			m.push(roleStatus, pr.Render())
		}

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return agentDoneMsg{}
	}
}

// ── $log — Under-the-hood pipeline trigger ─────────────────────────────────────
// runLogCmd receives a shell execution trace, evaluates crash signatures via the
// ContextLedger, and triggers the implicit silent analysis pipeline
// (investigate → plan → build) without visible mode bouncing.
func (m *model) runLogCmd(traceData string) tea.Cmd {
	m.cancelStaleAgentOps()
	m.pipelineRunning = true
	m.pipelineStep = "analyzing trace"

	// Capture raw shell output from the execution runner
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "$log trace analysis"} },
		func() (msg tea.Msg) {
			defer func() {
				if r := recover(); r != nil {
					msg = TaskFinishedMsg{}
				}
			}()
			runner := execExecutionRunner(".")
			var output string
			if traceData != "" {
				out, err := runner.Run(traceData)
				if err != nil {
					return logInputMsg{err: err}
				}
				if out != nil {
					output = out.Stdout
					if out.Stderr != "" {
						if output != "" {
							output += "\n"
						}
						output += out.Stderr
					}
				}
			}

			m.push(roleSystem, "Tracing execution...")

			// Extract stack frames for ledger registration
			frames := investigate.ParseStackFrames(output)
			var files []string
			for _, f := range frames {
				files = append(files, f.File)
			}
			if len(files) > 50 {
				files = files[:50]
			}

			// Register with ContextLedger
			if m.ledger == nil {
				m.ledger = NewContextLedger()
			}
			ledgerID := m.ledger.Record(files, output)

			// Build analysis payload for Step 1 (silent investigation)
			var analysis strings.Builder
			analysis.WriteString("## [$log] UNDER-THE-HOOD TRACE ANALYSIS\n\n")
			fmt.Fprintf(&analysis, "**Ledger ID:** `%s`\n\n", ledgerID)
			analysis.WriteString("## RAW TRACE OUTPUT\n\n```\n")
			analysis.WriteString(output)
			analysis.WriteString("\n```\n\n")

			if len(frames) > 0 {
				analysis.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&analysis, "### %s:%d\n\n```go\n", slice.File, slice.Line)
						for _, cline := range slice.Context {
							analysis.WriteString(cline)
							analysis.WriteString("\n")
						}
						analysis.WriteString("```\n\n")
					}
				}
			}

			analysis.WriteString("## INSTRUCTION\n")
			analysis.WriteString("Analyze the trace above. Identify the root cause. ")
			analysis.WriteString("Output a structured diagnosis with the root cause, evidence, and proposed resolution.\n")

			m.reviewRunning = true
			m.lastActionTime = time.Now()
			return logInputMsg{output: analysis.String()}
		},
	)
}

// ── $log → silent investigate step ──────────────────────────────────────────────
// handleLogInput processes the capture trace and fires the silent investigation
// step through streamCmd (read-only LLM analysis). No mode transition occurs.
func (m *model) handleLogInput(msg logInputMsg) tea.Cmd {
	m.pipelineStep = "analyzing failure"
	if msg.err != nil {
		m.pipelineRunning = false
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "$log: execution error: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, "Step 1/3: Analyzing failure...")
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	flush := m.flushPendingRecords()
	return tea.Batch(flush, m.streamCmd(msg.output))
}

// handleInvestigateComplete receives the silent analysis and pipes it into plan.
// Step 2: silent blueprinting. No UI mode transition occurs.
func (m *model) handleInvestigateComplete(msg investigateCompleteMsg) tea.Cmd {
	m.pipelineStep = "blueprinting"
	if msg.err != nil {
		m.pipelineRunning = false
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: analysis failed: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, infoStyle.Render("Step 2/3: Generating blueprint..."))
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	m.handoffCtx.ProposedFix = msg.analysis
	flush := m.flushPendingRecords()
	return tea.Batch(flush, m.streamCmd(msg.analysis))
}

// handleBlueprintReady receives the plan output and jumps to /build execution.
// Step 3: Explicit execution jump to /build with the fully realized blueprint.
func (m *model) handleBlueprintReady(msg blueprintReadyMsg) tea.Cmd {
	m.pipelineRunning = false
	m.pipelineStep = ""

	if msg.err != nil {
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: blueprint error: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	// ── CLEAN CONTEXT FLUSH BEFORE AUTOPILOT RESUME ─────────────────────
	// Explicitly flush stale diff layers and malformed state from prior
	// failed runs so the fresh blueprint enters a pristine buffer.
	m.lastTestOutput = ""
	m.lastTestFailed = false
	m.lastTestTarget = ""
	m.handoffCtx.LastFailurePayload = ""
	m.acceptedProposals = nil
	m.pendingProposals = nil

	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Blueprint ready [%s].", msg.ledgerID)))

	// ── RULE A: BLOCKED AUTO-TRANSITION TO /build ──────────────────────
	// The pipeline blueprint is ready, but auto-transitioning to /build
	// is blocked. The user must explicitly switch to /build.
	m.push(roleError, "State Transition Blocked: File modifications are only allowed inside /build mode after /plan approval. Please run /plan first, then use /build.")
	m.handoffCtx.ProposedFix = msg.blueprint
	m.pipelineRunning = false
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	flush := m.flushPendingRecords()
	return flush
}

// ── Workflow lifecycle: context cancellation for stale goroutine release ──
// cancelStaleAgentOps cancels any in-flight background context and resets
// all agent/spinner state to prevent stale tickMsg loops, goroutine leaks,
// and spinner frame corruption across mode transitions and $sub-command
// re-entry. MUST be called before spawning any new execution loop.
//
// ContextLedger immunity: the ledger data block is preserved during child
// suffix transitions, allowing a sibling sub-scope (#101-sub) to inherit
// the parent state. On new root allocations (completely decoupled crashes),
// the ledger is re-initialized via ResetForNewRoot.
func (m *model) cancelStaleAgentOps() {
	// Stash ContextLedger before clearing everything else
	if m.ledger != nil {
		m.ledgerStash = m.ledger.stashLedgerData()
	}

	// Cancel ALL registered background contexts (ghost loop prevention)
	m.cancelAllBackgroundContexts()

	m.reviewRunning = false
	m.agentRunning = false
	m.agentDone = false
	m.agentLabel = ""
	m.lastActionTime = time.Time{}
	m.spinnerFrame = 0

	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamCh = nil
	m.streaming = false
	m.streamTickActive = false
	m.streamBuffer = ""
	m.currentStreamContent = ""
	m.interruptRequested = false

	// Preserve pipeline state if active (implicit pipeline continues)
	if m.pipelineRunning {
		return
	}

	// Re-hydrate ledger from stash for new root allocations
	if m.ledgerStash != nil {
		if m.ledger == nil {
			m.ledger = NewContextLedger()
		}
		m.ledger.restoreLedgerData(m.ledgerStash)
		m.ledgerStash = nil
	}
}

// registerBackgroundCancel registers a cancel function for a background
// context so it can be cancelled on mode transitions or Ctrl+C.
func (m *model) registerBackgroundCancel(cancel context.CancelFunc) {
	if cancel != nil {
		m.backgroundCancels = append(m.backgroundCancels, cancel)
	}
}

// cancelAllBackgroundContexts cancels all registered background contexts
// and clears the registry. Used to prevent ghost loops on mode transitions.
func (m *model) cancelAllBackgroundContexts() {
	for _, cancel := range m.backgroundCancels {
		cancel()
	}
	m.backgroundCancels = nil
}

// handleReviewDollar routes $ sub-commands.
// ModeReview: $test, $run, $fix, $log
// ModeInvestigate: $env, $trace, $diagnose, $log
// Sets reviewRunning synchronously so the view can render an immediate
// spinner before the async agentStartMsg is processed.
func (m *model) handleReviewDollar(line string) tea.Cmd {
	action := strings.TrimSpace(line[1:])
	mode := m.resolver.Current()

	// ── $log — UNDER-THE-HOOD IMPLICIT PIPELINE ──────────────────────────
	// $log evaluates a shell failure trace, fires the silent analysis pipeline
	// (investigate → plan → build) without bouncing the UI between modes.
	// The ContextLedger tracks issues silently via #number scoping.
	//
	// By default $log renders only telemetry and mutation logs matching the
	// active #number context. Pass --all to show the full unfiltered history.
	if action == "log" || strings.HasPrefix(action, "log ") {
		rest := strings.TrimSpace(strings.TrimPrefix(action, "log"))
		if rest == "" || rest == "--all" {
			showAll := rest == "--all"
			return m.runLogViewCmd(showAll)
		}
		return m.runLogCmd(rest)
	}

	// ── $fix BLOCKED IN /review (Post-Fix Verification — Read-Only) ─────
	// $fix requires write access which /review mode explicitly denies.
	if mode == modes.ModeReview && (action == "fix" || strings.HasPrefix(action, "fix ")) {
		m.cancelStaleAgentOps()
		m.push(roleSystem, mutedStyle.Render("Write access required. Switch to /build."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── $fix BLOCKED IN /investigate (Read-Only Diagnostics) ────────────
	if mode == modes.ModeInvestigate && (action == "fix" || strings.HasPrefix(action, "fix ")) {
		m.cancelStaleAgentOps()
		m.push(roleError, "unknown investigate action: $fix")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── $diagnose in /investigate — runs analysis; auto-transition to /build
	// happens from streamDoneMsg when mutation is detected in the output.
	if mode == modes.ModeInvestigate && (action == "diagnose" || strings.HasPrefix(action, "diagnose ")) {
		m.cancelStaleAgentOps()
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		return m.runDiagnoseCmd()
	}

	// ── $test in /investigate — full Test=Yes privilege. Let run read-only.
	if mode == modes.ModeInvestigate && (action == "test" || strings.HasPrefix(action, "test ")) {
		m.cancelStaleAgentOps()
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "test"))
		return m.runTestCmd(rest)
	}

	// ── ABSOLUTE STALE GOROUTINE RELEASE (ANTI-CORRUPTION) ───────────────
	// Before spawning ANY new execution, kill/drain/cancel all previous
	// background agents. This prevents stale tickMsg loops and structural
	// goroutines from the previous $test/$run from corrupting the single-source
	// model state — which causes the custom star spinner to mutate into defaults.
	m.cancelStaleAgentOps()

	var cmd tea.Cmd

	switch {
	case mode == modes.ModeReview && (action == "test" || strings.HasPrefix(action, "test ")):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "test"))
		cmd = m.runTestCmd(rest)

	case mode == modes.ModeReview && (action == "run" || strings.HasPrefix(action, "run")):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "run"))
		cmd = m.runRunCmd(rest)

	case mode == modes.ModeInvestigate && action == "env":
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		cmd = m.runEnvCmd()

	case mode == modes.ModeInvestigate && (strings.HasPrefix(action, "trace ") || action == "trace"):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "trace"))
		isAutoTrace := rest == "" || strings.TrimSpace(rest) == ""
		if isAutoTrace {
			// Force disk reload: the ContextID may have been written by a
			// previous engine run (e.g. $test) that the in-memory session
			// hasn't picked up yet.
			_ = m.sess.Reload()
			if m.sess.ContextID == "" {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, "[System Error] No active Context ID found. Please run $test first to execute diagnostic verification and generate a context session.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return nil
			}
			logPath := m.sess.TestRunLogPath()
			data, err := os.ReadFile(logPath)
			if err != nil {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, fmt.Sprintf("[System Error] Failed to read log at %s: %v", logPath, err))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return nil
			}
			if len(data) == 0 {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, "[System Error] Log file located but 0 stack trace frames parsed. Raw log size: 0 bytes.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return nil
			}
			logStr := string(data)
			frames := investigate.ParseStackFrames(logStr)
			if len(frames) == 0 {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, fmt.Sprintf("[System Error] Log file located but 0 stack trace frames parsed. Raw log size: %d bytes.", len(data)))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return nil
			}
			logStr = ansiRe.ReplaceAllString(logStr, "")
			cmd = m.runAutoTraceCmd(logStr)
			break
		}
		cmd = m.runTraceCmd(rest)

	case mode == modes.ModeBuild && (action == "fix" || strings.HasPrefix(action, "fix ")):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "fix"))
		cmd = m.runFixCmd(rest)

	case mode == modes.ModeBuild && (strings.HasPrefix(action, "hot ") || action == "hot"):
		rest := strings.TrimSpace(strings.TrimPrefix(action, "hot"))
		// handleHotfixCmd handles its own state and returns an appropriate cmd.
		cmd = m.handleHotfixCmd(rest)

	default:
		switch mode {
		case modes.ModeReview:
			m.push(roleError, fmt.Sprintf("unknown review action: $%s (use $test, $run, or $log)", action))
		case modes.ModeInvestigate:
			m.push(roleError, fmt.Sprintf("unknown investigate action: $%s (use $env, $trace, $test, $diagnose, or $log)", action))
		case modes.ModeBuild:
			m.push(roleError, fmt.Sprintf("unknown build action: $%s (use $fix or $hot <prompt>)", action))
		default:
			m.push(roleError, fmt.Sprintf("$ sub-commands not available in /%s mode", mode))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	if cmd == nil {
		m.reviewRunning = false
		m.lastActionTime = time.Time{}
	}
	return cmd
}

// runEnvCmd captures Go version, git status, and key environment variables
// into a structured [SYSTEM ENVIRONMENT DIAGNOSTICS] block.
func (m *model) runEnvCmd() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "env diagnostics"}
		},
		func() (msg tea.Msg) {
			defer func() {
				if r := recover(); r != nil {
					msg = TaskFinishedMsg{}
				}
			}()
			var b strings.Builder
			b.WriteString("\n═══════════════════════════════════════════\n")
			b.WriteString("  [SYSTEM ENVIRONMENT DIAGNOSTICS]\n")
			b.WriteString("═══════════════════════════════════════════\n")

			goVer, _ := execShell("go version")
			goVer = strings.TrimSpace(goVer)
			fmt.Fprintf(&b, "  Go Version : %s\n", goVer)

			branch, branchErr := m.gitEng.Branch()
			hash, hashErr := m.gitEng.CurrentHash()
			if branchErr == nil {
				fmt.Fprintf(&b, "  Git Branch : %s\n", branch)
			}
			if hashErr == nil {
				fmt.Fprintf(&b, "  Git Commit : %s\n", hash)
			}

			statusOut, _ := execShell("git status --short")
			if strings.TrimSpace(statusOut) != "" {
				b.WriteString("  Git Dirt   :\n")
				for _, line := range strings.Split(strings.TrimRight(statusOut, "\n"), "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						fmt.Fprintf(&b, "    %s\n", line)
					}
				}
			}

			b.WriteString("  Environment :\n")
			relevantVars := []string{"GOPATH", "GO111MODULE", "GOFLAGS", "GOROOT", "PATH", "SHELL", "TERM", "HOME"}
			for _, name := range relevantVars {
				if val, ok := os.LookupEnv(name); ok {
					fmt.Fprintf(&b, "    %s=%s\n", name, val)
				}
			}

			b.WriteString("═══════════════════════════════════════════\n")

			return envResultMsg{content: b.String()}
		},
	)
}

// runTraceCmd dispatches a live go test -run=[target] -v -race execution
// and captures full stdout/stderr including panic frames and data races.
func (m *model) runTraceCmd(target string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "tracing: " + target}
		},
		func() (msg tea.Msg) {
			defer func() {
				if r := recover(); r != nil {
					msg = TaskFinishedMsg{}
				}
			}()
			runner := execExecutionRunner(".")
			cmd := "go test -run=" + target + " -v -race 2>&1"
			result, err := runner.Run(cmd)

			output := ""
			passed := true
			failedCount := 0
			totalCount := 0

			if result != nil {
				output = result.Stdout
				if result.Stderr != "" {
					if output != "" {
						output += "\n"
					}
					output += result.Stderr
				}
				for _, line := range strings.Split(output, "\n") {
					if strings.Contains(line, "--- FAIL:") {
						failedCount++
					}
					if strings.Contains(line, "--- PASS:") {
						totalCount++
					}
				}
				totalCount += failedCount
				if result.ExitCode != 0 || failedCount > 0 {
					passed = false
				}
			}
			if err != nil && output == "" {
				output = err.Error()
				passed = false
			}

			return traceResultMsg{
				output: output,
				target: target,
				passed: passed,
				failed: failedCount,
				total:  totalCount,
				err:    err,
			}
		},
	)
}

// runAutoTraceCmd parses a saved test log and renders the local Call Stack
// trace using the graph AST proximity slicer, without re-running the test.
func (m *model) runAutoTraceCmd(logData string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "auto-trace from context log"}
		},
		func() (msg tea.Msg) {
			defer func() {
				if r := recover(); r != nil {
					msg = TaskFinishedMsg{}
				}
			}()

			frames := investigate.ParseStackFrames(logData)
			failedCount := 0
			totalCount := 0
			for _, line := range strings.Split(logData, "\n") {
				if strings.Contains(line, "--- FAIL:") {
					failedCount++
				}
				if strings.Contains(line, "--- PASS:") {
					totalCount++
				}
			}
			totalCount += failedCount
			passed := failedCount == 0

			output := logData
			callStackRendered := false
			if len(frames) > 0 && m.graph != nil {
				var b strings.Builder
				b.WriteString("## CALL STACK TRACE (from saved context log)\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						callStackRendered = true
						fmt.Fprintf(&b, "### %s:%d\n\n```go\n", slice.File, slice.Line)
						for _, cline := range slice.Context {
							b.WriteString(cline)
							b.WriteString("\n")
						}
						b.WriteString("```\n\n")
					}
				}
				if callStackRendered {
					output = b.String() + "---\n" + output
				}
			}
			if !callStackRendered {
				output = fmt.Sprintf("[System Error] Log file located but 0 stack trace frames parsed. Raw log size: %d bytes.\n---\n%s", len(logData), logData)
			}

			return traceResultMsg{
				output: output,
				target: "(auto-trace from context log)",
				passed: passed,
				failed: failedCount,
				total:  totalCount,
				err:    nil,
			}
		},
	)
}

// runDiagnoseCmd reads the active context error log and runs it through the
// local SLM bridge (Ollama /api/generate) for a distilled one-sentence root
// cause diagnosis. The result is stored in the session and rendered on the TUI.
func (m *model) runDiagnoseCmd() tea.Cmd {
	m.reviewRunning = true
	m.lastActionTime = time.Now()

	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "local slm diagnosis"}
		},
		func() (msg tea.Msg) {
			defer func() {
				if r := recover(); r != nil {
					msg = TaskFinishedMsg{}
				}
			}()

			// Sync session from disk, then check for an active context.
			_ = m.sess.Reload()
			if m.sess.ContextID == "" {
				m.push(roleError, "[System Error] No active diagnostic context found. Run $test or $trace first.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}

			// Read the error log for the active context.
			logPath := m.sess.TestRunLogPath()
			logData, err := os.ReadFile(logPath)
			if err != nil {
				m.push(roleError, fmt.Sprintf("[System Error] Failed to read error log at %s: %v", logPath, err))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}
			if len(logData) == 0 {
				m.push(roleError, "[System Error] Error log is empty — no diagnostic data to analyze.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}

			// Use the SAME unified provider interface that /ask relies on
			// (m.provider.Execute / ExecuteStream). Do NOT type-assert to a
			// concrete *OllamaProvider — that assertion is what produced the
			// false-positive "provider unreachable" error. Reusing the shared
			// interface guarantees the exact provider configuration, model tag
			// binding (m.cfg.ActiveModelName()), and base URL context that lets
			// /ask execute successfully.
			if m.provider == nil {
				m.push(roleError, "[System Error] No AI provider is configured. Run /model to select one.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}

			// Run the diagnosis through the unified client router.
			resp, err := m.provider.Execute(context.Background(), ai.Request{
				Model: m.cfg.ActiveModelName(),
				Messages: []ai.Message{
					{Role: "user", Content: string(logData)},
				},
				Stream: false,
				System: providers.DiagnoseSystemPrompt,
			})
			if err != nil {
				m.push(roleError, fmt.Sprintf("[System Error] Diagnosis failed: %v", err))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}
			diagnosis := ""
			if resp != nil {
				diagnosis = resp.Content
			}

			// Store in session and persist.
			m.sess.DiagnosticsSummary = diagnosis
			_ = m.sess.Save()

			// Render the diagnosis on the TUI.
			m.push(roleSystem, fmt.Sprintf("[Local SLM Diagnosis] %s", diagnosis))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()

			// Also store in handoff context for downstream mode pipelines.
			m.handoffCtx.LastFailurePayload = diagnosis
			// The diagnosis is a failure produced by the current workflow:
			// expose a capability to investigate its root cause via the
			// current result (cleared on mode entry, so it never persists
			// as a stale chip).
			m.currentResult = failureResult(diagnosis)

			return agentDoneMsg{}
		},
	)
}

// runAskPromptHandoffCmd passes the user's raw architectural idea directly to
// the Strict Senior Architect persona for refinement, pruning, and tradeoff
// analysis. No session history aggregation — the raw input IS the payload.
//
// ISOLATION CONTRACT — This function is called STRICTLY from handleInput when
// the user types "$prompt <raw_idea>" in /ask mode. It uses its own system
// prompt (AskPromptHandoffSystemPrompt) and a non-streaming provider call that
// NEVER touches the normal chat session history (no AddMessage, no sess.Save).
// Normal chat continues to use AskContract() via the streamCmd path with zero
// contamination.
func (m *model) runAskPromptHandoffCmd(rawInput string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "refining architectural idea"}
		},
		m.spinnerTickCmd(),
		func() tea.Msg {
			if m.provider == nil {
				m.push(roleError, "[System Error] No AI provider is configured. Run /model to select one.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}

			uname := m.cfg.Username
			if uname == "" {
				uname = m.userName
			}
			systemPrompt := prompt.AskPromptHandoffSystemPrompt(uname)

			req := ai.Request{
				Model: m.cfg.ActiveModelName(),
				Messages: []ai.Message{
					{Role: "user", Content: rawInput},
				},
				Stream: false,
				System: systemPrompt,
			}

			resp, err := m.provider.Execute(context.Background(), req)
			if err != nil {
				return promptHandoffMsg{err: fmt.Errorf("prompt synthesis failed: %w", err)}
			}

			var content string
			if resp != nil {
				content = strings.TrimSpace(resp.Content)
			}

			if content == "" {
				return promptHandoffMsg{err: fmt.Errorf("prompt synthesis returned empty response")}
			}

			// The FollowUp action chip is delivered via the promptHandoffMsg.actions
			// field and rendered as an interactive terminal component by the
			// promptHandoffMsg handler in update.go — never embedded in the
			// markdown body.
			followUpAction := []Action{
				{
					ID:       "ask-prompt-handoff-investigate",
					Label:    "Forward to /investigate for deep-dive forensic analysis",
					Shortcut: "alt+f",
					Command:  "/mode investigate",
					Query:    content,
					Enabled:  true,
					Priority: 100,
				},
			}

			return promptHandoffMsg{content: content, actions: followUpAction}
		},
	)
}

// shellFirewall checks a shell command against the security guard rail.
// Returns (blocked, violationMessage).
// Global blacklist applies in all modes; /mode investigate has an additional
// read-only allowlist that rejects anything outside inspection binaries.
func (m *model) shellFirewall(cmd string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	if lower == "" {
		return false, ""
	}

	// ── Mode-specific allowlist: /mode investigate — read-only only ──
	if m.resolver.Current() == modes.ModeInvestigate {
		allowed := false
		for _, prefix := range []string{"go test", "go version", "git status", "git diff", "dlv"} {
			if strings.HasPrefix(lower, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return true, fmt.Sprintf(
				"Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.",
				cmd)
		}
	}

	// ── Global blacklist (SECURITY CRITICAL) ──
	// Every blacklisted token is a hard block: the command cannot be executed
	// through any code path — not via !cmd, not via proposedShellCmd, not via
	// SHELL_EXEC, not via any AI-generated script. This is the last line of
	// defense against silent privilege escalation.
	blacklist := []string{
		"rm ", "sudo", "chmod", "chown", "mkfs", "dd ",
		"mv /*", "> /dev/gpi",
		"apt-get", "apt ", "dpkg", "yum ", "dnf ",
	}
	for _, b := range blacklist {
		if strings.Contains(lower, b) {
			violation := b
			if violation == "sudo" {
				return true, fmt.Sprintf(
					"[SUDO BLOCKED] '%s' requires root privileges. IZEN never runs sudo automatically. "+
						"To execute this command, copy it and run it manually in your terminal outside IZEN.", cmd)
			}
			return true, fmt.Sprintf(
				"Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.",
				cmd)
		}
	}

	return false, ""
}

func execShell(cmd string) (string, error) {
	c := exec.CommandContext(context.Background(), "bash", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	out := stdout.String()
	if stderr.Len() > 0 {
		if out != "" {
			out += "\n"
		}
		out += stderr.String()
	}
	return out, err
}

func (m *model) analyzeObjectiveCmd(obj *domain.Objective) tea.Cmd {
	return func() tea.Msg {
		resultCh := make(chan objectiveAnalyzedMsg, 1)
		go func() {
			if obj == nil {
				resultCh <- objectiveAnalyzedMsg{err: fmt.Errorf("objective is nil")}
				return
			}
			res := objengine.BuildObjectiveContext(obj.RawIntent, m.cfg.ActiveModelName(), m.graph)
			obj.Scope = res.Scope
			obj.TokenBudget = res.Budget
			obj.Telemetry = append(obj.Telemetry[:0], res.Telemetry...)
			obj.CurrentStatus = domain.ObjectivePlanned
			obj.HumanConfirmed = !res.Budget.RequiresApproval
			resultCh <- objectiveAnalyzedMsg{objective: obj}
		}()
		return <-resultCh
	}
}

func (m *model) resetObjectiveContextStacks() {
	m.pendingFileRefs = nil
	m.attachedFiles = nil
	m.investigateInvocationCount = 0
	m.pendingTestConfirm = false
	m.pendingTestTarget = ""
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil
	m.pendingBuildAllowAlways = false
	m.lastTestOutput = ""
	m.lastTestFailed = false
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.acceptAll = false
	m.state = StateChat
	m.recalcViewportHeight()
	m.acceptedProposals = nil
	m.proposedShellCmd = ""
	m.sess.InvestigationID = ""
	m.sess.ReviewID = ""
	m.sess.ClearHistory()
	m.sess.ClearTasks()
	_ = m.sess.Save()
}

// ── Handoff Pipeline ───────────────────────────────────────────────────────────

// ── Greeting-Detection Guards ──────────────────────────────────────────────────

var genericGreetingPatterns = []string{
	"I am IZEN",
	"How can I assist you",
	"What are things like for you today",
	"Hello!",
	"Hi there",
}

// IsGenericGreeting detects whether a string is a generic fallback greeting
// rather than substantive engineering analysis output.
func IsGenericGreeting(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 20 {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range genericGreetingPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// CleanHandoffPayload strips generic greeting content from handoff payloads,
// retaining only substantive engineering content (error logs, diagnostics, etc.).
// If the entire payload is a greeting with no engineering content, returns "".
func CleanHandoffPayload(payload string) string {
	if !IsGenericGreeting(payload) {
		return payload
	}
	var builder strings.Builder
	for _, line := range strings.Split(payload, "\n") {
		trimmed := strings.TrimSpace(line)
		isGreeting := false
		for _, p := range genericGreetingPatterns {
			if strings.Contains(strings.ToLower(trimmed), strings.ToLower(p)) {
				isGreeting = true
				break
			}
		}
		if !isGreeting {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(line)
		}
	}
	return strings.TrimSpace(builder.String())
}

// ExtractSearchTerms (from the retrieval package) is the authoritative query
// sanitizer for code search. It replaces the previous inline sanitizeSearchQuery
// because it performs structural term extraction (symbols/paths/error
// constants) rather than only stripping control characters — raw log strings
// are now safely skipped instead of fired verbatim at the search engine.

// injectHandoffContext primes the target mode with contextual state from the
// previous mode. Called during setMode when a handoff context is available.
func (m *model) injectHandoffContext(mode modes.Mode) {
	switch mode {
	case modes.ModeInvestigate:
		if m.handoffCtx.LastFailurePayload != "" {
			sanitized := config.SanitizeForSession(m.handoffCtx.LastFailurePayload)
			m.handoffCtx.LastFailurePayload = sanitized
			m.push(roleSystem, "Handoff context injected.")
		}

	case modes.ModePlan:
		if m.handoffCtx.ProposedFix != "" {
			cleaned := CleanHandoffPayload(m.handoffCtx.ProposedFix)
			sanitized := config.SanitizeForSession(cleaned)
			m.handoffCtx.ProposedFix = sanitized
			if len(m.handoffCtx.PendingTodos) == 0 {
				m.handoffCtx.PendingTodos = parseProposedFixIntoTodos(m.handoffCtx.ProposedFix)
			}
			m.push(roleSystem, fmt.Sprintf(
				"Handoff context injected: %d pending TODO(s).",
				len(m.handoffCtx.PendingTodos)))
		}

	case modes.ModeBuild:
		// /build consumes ONLY the atomic structural tasks (PendingTodos /
		// staged tasks) produced by /plan. The raw ProposedFix chat blob from
		// an earlier phase is purged here so it can never re-inject stale
		// conversational text into the build workspace. We keep a checkpoint
		// for reversibility regardless.
		if len(m.handoffCtx.PendingTodos) > 0 || len(m.sess.CurrentTasks) > 0 {
			m.createBuildCheckpoint(0)
			m.push(roleSystem, "Handoff context injected. Checkpoint created.")
		}
		// Purge stale conversational handoff so the build buffer stays clean.
		m.handoffCtx.ProposedFix = ""

		// REFORM A: Build strict minimal context for the active task.
		// This is injected as the initial prompt for the build execution.
		m.handoffCtx.LastFailurePayload = m.buildStrictHandoffPayload()
	}
}

// handleChipActivation routes a hotkey press to the matching capability and
// executes it. The action is a pure capability produced by the workflow layer
// (see BuildViewContext); the renderer never decides activation. The consumed
// capability's result is cleared because the action has been taken.
func (m *model) handleChipActivation(action Action) tea.Cmd {
	if !action.Enabled {
		return nil
	}
	m.push(roleUser, action.Command)
	m.push(roleSystem, fmt.Sprintf("Activated: %s", action.Label))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()

	// Consuming a result capability ends the current result's relevance.
	m.currentResult = nil

	// Mode transition capabilities: /mode <name>
	parts := strings.Fields(action.Command)
	if len(parts) >= 2 && parts[0] == "/mode" {
		mode, ok := modes.Parse(parts[1])
		if ok {
			m.modeChangeAuthorized = true
			if action.Query != "" {
				// Suppress setMode auto-trigger — the explicit Query takes precedence.
				// Clear handoff sources so setMode does not start a redundant stream.
				// Handoff data is already captured in action.Query from workspace
				// build time — the Query is the canonical payload.
				m.handoffCtx.ProposedFix = ""
				m.handoffLedgerContent = ""
				m.setMode(mode)
				return m.handleMessageContent(action.Query)
			}
			return m.setMode(mode)
		}
		return nil
	}

	// Mode-switch command chips: /investigate, /plan, /build, /ask, /review
	// These are NOT in validSystemCommands — they must be routed as mode
	// transitions instead of falling through to handleCommand.
	if mode, content, ok := parseModeShorthand(action.Command); ok {
		m.modeChangeAuthorized = true

		// ── PLAN APPROVAL GATE ─────────────────────────────────────────
		// When the user explicitly approves the plan via the action chip,
		// set planApproved = true so the /build handoff engine fires.
		// Without this explicit approval, /build remains blocked.
		if action.ID == "approve-plan" {
			m.planApproved = true
			m.push(roleSystem, infoStyle.Render("✓ Plan approved. Transitioning to /build for execution..."))
		}

		// ── PLAN REJECTION ─────────────────────────────────────────────
		// When the user rejects the plan, clear all handoff context including
		// staged tasks so no stale plan data leaks into the next cycle.
		if action.ID == "reject-plan" {
			m.push(roleSystem, infoStyle.Render("✗ Plan rejected. Clearing staged tasks..."))
			m.handoffCtx = HandoffContext{}
			m.handoffLedgerContent = ""
			if m.sess != nil {
				m.sess.ClearTasks()
				m.sess.ContextLedger = nil
				_ = m.sess.Save()
			}
		}

		m.handoffCtx.ProposedFix = ""
		m.handoffLedgerContent = ""
		m.currentResult = nil
		cmd := m.setMode(mode)
		if action.Query != "" {
			return m.handleMessageContent(action.Query)
		}
		if content != "" {
			return m.handleMessageContent(content)
		}
		return cmd
	}

	// Direct command capabilities: /commit, /undo, etc.
	return m.handleCommand(action.Command)
}

// parseProposedFixIntoTodos converts a proposed fix (markdown/diff) into a
// checklist of concrete TODO strings for the plan mode dashboard.
// maxPendingTodos caps how many pending TODO items a handoff payload may yield.
// A well-formed investigation produces a handful of targeted items; anything
// beyond this is a symptom of noise leaking through, so we clamp hard.
const maxPendingTodos = 5

// parseProposedFixIntoTodos extracts genuine, actionable task items from a
// handoff payload and returns them deduplicated and clamped to maxPendingTodos.
//
// The payload is NOT a task list — it is the structured investigation forensics
// blob (FormatForPlan output + [PKT-N] analytical packets: raw diagnostics,
// code-fence blocks, section headers, compiler output). The previous
// implementation had a catch-all fallback that promoted EVERY non-empty line to
// a "TODO" when no checkbox markers were present, so a single handoff spawned
// ~18 junk TODOs made of ``` fences, "### RAW DIAGNOSTICS" headers, "[PKT-N]"
// lines, and raw shell prints. Those flooded the /plan prompt and stalled
// synthesis.
//
// This version enforces a strict data boundary: it only accepts lines that are
// explicitly marked as tasks (checkbox / bullet-status glyphs), and even then
// rejects anything that is recognizably log or layout noise. If the payload
// carries no explicit task markers it yields ZERO todos — the forensics still
// travel to /plan via handoffLedgerContent, and /plan owns task synthesis.
func parseProposedFixIntoTodos(fix string) []string {
	lines := strings.Split(fix, "\n")
	var todos []string
	seen := make(map[string]bool)

	add := func(item string) {
		item = strings.TrimSpace(item)
		if item == "" || isHandoffNoiseLine(item) {
			return
		}
		key := strings.ToLower(item)
		if seen[key] {
			return
		}
		seen[key] = true
		todos = append(todos, item)
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "- [ ]"), strings.HasPrefix(trimmed, "- [x]"):
			add(trimmed[5:])
		case strings.HasPrefix(trimmed, "✓ "), strings.HasPrefix(trimmed, "○ "), strings.HasPrefix(trimmed, "● "):
			add(trimmed[len("✓ "):])
		}
	}

	if len(todos) > maxPendingTodos {
		todos = todos[:maxPendingTodos]
	}
	return todos
}

// isHandoffNoiseLine reports whether a line is investigation log/layout noise
// rather than an actionable task. It rejects markdown section headers, code
// fences, analytical-packet framing ([PKT-N], "Total packets:"), verbatim
// compiler/shell coordinates and download chatter, and other raw diagnostic
// residue that must never become a pending TODO.
func isHandoffNoiseLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	// Markdown headers and code fences are pure layout.
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") || t == "`" {
		return true
	}
	// Analytical-packet framing emitted by FormatPacketsForPlan.
	if strings.HasPrefix(t, "[PKT-") ||
		strings.HasPrefix(t, "Total packets:") ||
		strings.HasPrefix(t, "kind=") ||
		strings.HasPrefix(t, "node=") ||
		strings.HasPrefix(t, "snippet:") {
		return true
	}
	// FormatForPlan structural labels / boundary markers.
	lower := strings.ToLower(t)
	for _, p := range []string{
		"source:", "problem:", "target file:", "diagnostics error log:",
		"raw diagnostics", "boundary enforcement", "affected symbols",
		"investigation ledger", "investigation handoff",
	} {
		if strings.HasPrefix(lower, p) || lower == p {
			return true
		}
	}
	// Raw compiler/shell residue: "go: downloading ...", "no required module ...",
	// and file:line:col coordinates carrying no imperative verb.
	if strings.HasPrefix(lower, "go: ") ||
		strings.HasPrefix(lower, "no required module") ||
		compilerCoordRe.MatchString(t) {
		return true
	}
	return false
}

// compilerCoordRe matches a bare "path/file.ext:line:col" compiler coordinate at
// the start of a line — raw diagnostic residue, never an actionable task.
var compilerCoordRe = regexp.MustCompile(`^[^\s:]+\.\w+:\d+:\d+`)

// extractTodosFromPlan extracts TODO items from a plan-mode LLM response.
func extractTodosFromPlan(content string) []string {
	tasks := plan.ParseMarkdownToTasks(content)
	if len(tasks) > 0 {
		todos := make([]string, 0, len(tasks))
		for _, t := range tasks {
			label := t.Type + ": " + t.Target
			if t.Description != "" {
				label += " — " + t.Description
			}
			todos = append(todos, label)
		}
		return todos
	}
	return parseProposedFixIntoTodos(content)
}
