package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/domain/task"
	objengine "github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/orchestrator"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/templates"
	verification "github.com/PizenLabs/izen/internal/verification"
	"github.com/PizenLabs/izen/internal/workspace"
	"github.com/PizenLabs/izen/pkg/capability/policy"
	"github.com/PizenLabs/izen/pkg/control"
	cmdreg "github.com/PizenLabs/izen/pkg/domain/command"
)

// resolveCommandToken canonicalizes a typed '/token' through the command
// registry: registered aliases ("/q" → quit, "/?" → help) and unambiguous
// prefixes ("/qu" → quit) resolve to the canonical command when that command
// is a valid system command. Unknown or ambiguous tokens are returned
// unchanged so the caller reports "unknown command" as before.
func resolveCommandToken(token string) string {
	if _, ok := validSystemCommands[token]; ok {
		return token
	}
	d, ok := cmdreg.Default().LookupPrefix(cmdreg.MarkerSlash, strings.TrimPrefix(token, "/"))
	if !ok {
		return token
	}
	canonical := "/" + d.Name
	if _, known := validSystemCommands[canonical]; known {
		return canonical
	}
	return token
}

var validSystemCommands = map[string]struct{}{
	"/help":             {},
	"/?":                {},
	"/quit":             {},
	"/usage":            {},
	"/provider":         {},
	"/model":            {},
	"/objective":        {},
	"/clear":            {},
	"/drop":             {},
	"/undo":             {},
	"/commit":           {},
	"/checkpoint":       {},
	"/arch":             {},
	"/explain-decision": {},
}

// ansiRe strips terminal ANSI escape color codes (e.g. \x1b[31m) that can
// corrupt regex-based stack frame parsers in auto-trace.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// inputANSIRe strips all escape sequences from the interactive text-input
// buffer, including SGR mouse-tracking reports (\x1b[<0;26;37M / \x1b[<...m)
// and trailing coordinate remnants. Bubble Tea parses genuine mouse events
// into tea.MouseMsg before they reach the textinput, so under normal operation
// nothing leaks — but a defensive strip here guarantees no raw terminal escape
// can ever pollute the editable command buffer (e.g. during /build shell
// execution context switches where raw-mode state could briefly differ).
// It also catches orphaned sequences (ESC byte stripped upstream) like
// [<64;83;30M that would otherwise leak into the input buffer during
// mouse wheel scrolling.
var inputANSIRe = regexp.MustCompile(`\x1b\[[<?][0-9;]*[a-zA-Z]|\[<[0-9;]+M`)

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
			m.push(roleError, providers.SanitizeAPIError(err))
		}
		scanner := bufio.NewScanner(strings.NewReader(strings.TrimRight(out, "\r\n")))
		for scanner.Scan() {
			m.push(roleSystem, scanner.Text())
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── DETERMINISTIC PARSE PIPELINE ───────────────────────────────────
	// Every remaining input reaches the parser BEFORE any string-prefix
	// command lookup or intent dispatch. parser.ParseInWorkspace resolves
	// /workspace markers, $ directives, @ scopes, and the natural-language
	// goal into a structured IntentAST and enforces the permission policy
	// against the effective workspace (the active session workspace when the
	// line declares none). Parse errors are surfaced verbatim and execution
	// stops — the raw-input "unknown command: <line>" fallback is never
	// emitted.
	ast, err := m.intentFromInput(line)
	if err != nil {
		m.push(roleError, err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// Directive- and global-bearing intents (including the /review $test
	// composite and the $prompt /ask router) are dispatched structurally from
	// the AST: workspace transition first, then commands, then directives.
	// Bare workspace switches and free-form goals fall through to the legacy
	// string routing below, which already handles them.
	if len(ast.Directives) > 0 || len(ast.GlobalCommands) > 0 {
		return m.dispatchASTIntent(ast)
	}

	if mode, content, ok := parseModeShorthand(line); ok {
		m.modeChangeAuthorized = true
		// ── APPLICATION-LAYER COMMAND RECORD ──────────────────────────
		// Every explicit user mode switch is routed through the Runtime
		// facade as a SwitchModeCmd so the canonical command/event contract
		// observes it. Nil-safe when no runtime is wired.
		switchCmd := m.runtimeSwitchCmd(mode)
		if content != "" {
			m.setMode(mode)
			return tea.Batch(m.handleMessageContent(content), switchCmd)
		}
		if mode == modes.ModeReview {
			m.setMode(mode)
			m.push(roleSystem, infoStyle.Render("Running review pipeline..."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			// ── FAST-PATH EARLY EXIT: CLEAN WORKING TREE ────────────────────
			// A full-diff /review on a clean tree has nothing to audit. Report
			// it immediately and reset every processing flag WITHOUT starting
			// the async pipeline or its spinner — the "Processing file
			// mutations..." animation must never appear for a run that will
			// perform zero mutations.
			if rev := review.NewEngine(".", nil, nil); rev.IsCleanWorkingTree() {
				m.push(roleSystem, infoStyle.Render("no changes to review — working tree is clean"))
				m.reviewRunning = false
				m.agentRunning = false
				m.lastActionTime = time.Time{}
				m.syncUIState()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return tea.Batch(switchCmd)
			}
			return tea.Batch(m.runReviewCmd(""), switchCmd)
		}
		// ── AUTO-TRIGGER /build EXECUTION ──────────────────────
		// When /build is invoked while already in /build mode and
		// a Fast-Track plan or pending TODO checklist exists,
		// immediately trigger execution instead of returning nil
		// (which leaves the UI frozen in an idle state).
		if mode == modes.ModeBuild {
			hasStagedTasks := len(m.sess.CurrentTasks) > 0
			hasPendingTodos := len(m.handoffCtx.PendingTodos) > 0
			hasLedgerTasks := m.sess != nil && m.sess.ContextLedger != nil && len(m.sess.ContextLedger.Tasks) > 0
			if hasStagedTasks || hasPendingTodos || hasLedgerTasks {
				return tea.Batch(m.runBuildCmd(""), switchCmd)
			}
		}
		return tea.Batch(m.setMode(mode), switchCmd)
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

	// ── HYBRID INTENT GATEWAY ────────────────────────────────────────
	// Free-form input (no explicit mode shorthand, no command, no shell) goes
	// through the Hybrid Intent Gateway: the deterministic fast path first, then
	// the semantic IntentClassifier when no deterministic signal matches. The
	// classified intent is projected onto the current phase (auto mode switch),
	// or — at low confidence (ConfirmationRequirement) — surfaced as an
	// interactive mode-selection prompt instead of acting on a blind guess.
	// The route runs async because the semantic fallback may invoke the LLM.
	//
	// ── /ask MODE LOCK: Direct Read-Only Chat boundary ───────────────
	// /ask is a strict read-only chat boundary. The ONLY valid sub-prompt
	// is $prompt (handled above). Free-form input in /ask MUST NEVER route
	// through the intent classifier — the classifier can misclassify natural
	// questions as /plan, /investigate, or /build and auto-switch modes,
	// violating the Direct Chat contract. Bypass the router entirely.
	if m.intentRouter != nil && m.resolver.Current() != modes.ModeAsk {
		return m.routeFreeInput(line)
	}

	return m.handleMessageContent(line)
}

// routeFreeInput dispatches a free-form prompt through the Hybrid Intent
// Gateway off the Bubble Tea event loop. The router runs the deterministic
// fast path first and only falls back to the semantic classifier; both are
// cheap enough to run inside the returned command's goroutine. The result is
// delivered back as a routerResultMsg for the Update loop to project.
func (m *model) routeFreeInput(line string) tea.Cmd {
	r := m.intentRouter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := r.Route(ctx, line)
		return routerResultMsg{line: line, result: res, err: err}
	}
}

func (m *model) handleMessageContent(line string) tea.Cmd {
	var refFiles []string
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

	// CONTEXT GOVERNANCE (P3): /ask context assembly is routed EXCLUSIVELY
	// through the Context Planner (prepareAskStreamCmd → Planner.Plan()). Explicit
	// @file references are resolved by the planner's gatherFileRefs via the
	// governed FileSource adapter; the fallback file-read path lives in
	// streamCmd (injectFileContext) and is likewise planner-backed. No raw disk
	// reads are performed here.

	line = m.expandFileRefs(line)

	content := strings.TrimSpace(line)

	// ── $hot FAST-TRACK ─────────────────────────────────────────────────
	// Any message starting with $hot bypasses ALL plan generation and
	// diagnostic loops, routing directly to the /build engine for instant
	// execution. Also strip the $hot prefix before passing to build.
	if strings.HasPrefix(strings.TrimSpace(content), "$hot") {
		hotContent := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "$hot"))
		return m.runBuildCmd(hotContent)
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

	// ── INTENT-BASED MODE OVERRIDE ──────────────────────────────────────
	// When the current mode is investigate but the user intent involves code
	// creation/mutation (not bug diagnostics), override to /build directly.
	// This prevents the investigate deadlock where the engine loops over
	// forensic evidence for a task that requires writing code.
	currentMode := m.resolver.Current()
	if currentMode == modes.ModeInvestigate && len(refFiles) > 0 {
		if hasMutationIntent(content) {
			return m.runBuildCmd(content)
		}
	}

	switch currentMode {
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

		// ── INTENT-BASED /investigate BYPASS ──────────────────────────────
		// FRONTEND_UI and code-generation/rewrite prompts add ZERO diagnostic
		// value to /investigate: the engine short-circuits them in 0s with an
		// "Inconclusive" result while injecting forensic overhead into the TUI
		// and context ledger. Bypass the engine entirely and transition
		// directly to /plan (UI/layout tasks) or /build (code mutation),
		// mirroring the routing the engine would have performed.
		intent := investigate.ClassifyIntent(content)
		switch {
		case intent.IsFrontendUI():
			// Preserve the raw prompt alongside the routing marker so the
			// microkernel pipeline (and the legacy LLM synthesis, as a
			// fallback) can plan from the actual request instead of a
			// placeholder. The "hand off to plan" substring is the routing
			// marker consumed by the /investigate engine result handler.
			m.handoffLedgerContent = "frontend ui intent detected — hand off to plan\n" + content
			m.handoffCtx.ProposedFix = m.handoffLedgerContent
			m.persistUserIntentPacket(content)
			m.modeChangeAuthorized = true
			m.investigateInvocationCount++
			return m.setMode(modes.ModePlan)
		case hasMutationIntent(content) && hasExecutableBuildTarget(content, m):
			m.handoffCtx.PendingTodos = synthesizeBuildTodosFromMutation(content)
			m.modeChangeAuthorized = true
			m.investigateInvocationCount++
			return m.setMode(modes.ModeBuild)
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

			// PROBLEM for the synthesis prompt must be the SHORT user goal, not
			// the full forensic ledger. The ledger itself already carries the
			// problem line at its head, so passing the entire handoff payload as
			// `problem` would duplicate every byte into BuildPlanJSONPrompt
			// (PROBLEM: <ledger> + LEDGER: <ledger>) and bloat the token budget
			// for queued cloud models. Prefer the raw objective/intent; fall back
			// to the last failure payload only when no objective is recorded.
			problem := ""
			if m.sess != nil {
				problem = m.sess.ObjectiveIntent()
			}
			if problem == "" {
				problem = m.handoffCtx.LastFailurePayload
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
			m.startShimmer("Synthesizing plan...", "plan")
			m.push(roleSystem, infoStyle.Render("Synthesizing structured execution plan from investigation data..."))
			// FAST-TRACK NOTICE: when there are zero pre-parsed TODOs the
			// synthesis runs purely on the forensic ledger. Surface an implicit
			// hint so the user understands the engine is working (not hung) and
			// that a first-token guard will bail fast if the local model is
			// unresponsive.
			if len(m.handoffCtx.PendingTodos) == 0 {
				m.push(roleSystem, mutedStyle.Render(
					"0 pending TODOs — synthesizing from forensic ledger. If your local model is stuck, this aborts within ~150s instead of hanging."))
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
				m.shimmerTickCmd(),
				m.runPlanEngineCmd(handoffSource, problem, m.routeModel("plan"), m.handoffCtx),
			)
		}

		// ── CONVERSATIONAL STREAMING PATH (Manual /plan usage) ──────────
		// Only reached when no investigation handoff exists (no handoffLedgerContent
		// and no ProposedFix). The structural engine path above always terminates
		// with either staged tasks or an explicit diagnostic — never falls through.

		// ── INTENT COMPILER PRIME PATH (direct /plan prompts) ────────────
		// A greenfield generation prompt typed straight into /plan is planned
		// deterministically by the IR-driven intent compiler before any LLM
		// call or heuristic assembly. Rejections surface the explicit reason in
		// the footer; successes stage explicit CREATE/WRITE file tasks.
		if m.intentCompiler != nil {
			if tasks, handled, icErr := m.intentCompiler.TryPlan(context.Background(), content, m.currentPrompt); handled {
				if icErr != nil {
					m.reconcileSpinner()
					m.push(roleError, icErr.Error())
					m.uiNotice = icErr.Error()
					m.refreshViewportContent()
					m.Viewport.GotoBottom()
					return m.flushPendingRecords()
				}
				if len(tasks) > 0 {
					m.streaming = false
					m.agentRunning = false
					m.planPending = false
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"Intent compiler plan: %d task(s) staged from the IR lowerer — no model call.",
						len(tasks))))
					m.refreshViewportContent()
					return func() tea.Msg {
						return planResultMsg{Tasks: tasks, IntentCompiler: true}
					}
				}
			}
		}

		// ── MICROKERNEL PRIME PATH (immutable plan pipeline fallback) ────
		// The legacy immutable microkernel pipeline remains as a secondary
		// deterministic path for greenfield requests the intent compiler does
		// not own.
		if m.microkernel != nil {
			if tasks, handled, mkErr := m.microkernel.TryPlan(context.Background(), content); handled {
				if mkErr != nil {
					m.reconcileSpinner()
					m.push(roleError, mkErr.Error())
					m.uiNotice = mkErr.Error()
					m.refreshViewportContent()
					m.Viewport.GotoBottom()
					return m.flushPendingRecords()
				}
				if len(tasks) > 0 {
					m.streaming = false
					m.agentRunning = false
					m.planPending = false
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"Microkernel plan: %d deterministic task(s) staged — no model call.",
						len(tasks))))
					m.refreshViewportContent()
					return func() tea.Msg {
						return planResultMsg{Tasks: tasks, Microkernel: true}
					}
				}
			}
		}

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

		modelName := m.routeModel("plan")
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

		// CONTEXT GOVERNANCE (P3): ALL /ask context assembly routes strictly
		// through the Context Planner. The legacy RouteAsk + context Builder
		// path (raw file reads bypassing the I/O layer) has been removed; the
		// planner's Plan() classifies the question, computes a token budget
		// split per context source, queries only the intent-prioritized engines
		// (Lea graph symbols, tool logs, @file references, architecture
		// overview), ranks and dedupes the chunks, and enforces the budget
		// before the context reaches the LLM. The injection is a strict
		// additive enrichment: it degrades silently to the untouched input
		// when no graph is ready or the plan yields no chunks.
		//
		// T=0MS ASYNC DISPATCH: the planner query + fallback file reads run on
		// a background goroutine (prepareAskStreamCmd) so the Enter handler can
		// return immediately with the loading shimmer animating. The assembled
		// content is delivered as askStreamPreparedMsg, whose Update handler
		// runs streamCmd synchronously on the event loop (all model mutations
		// stay on the UI goroutine).
		if m.resolver.Current() == modes.ModeAsk {
			// Local intent interception is cheap and MUST run first — a greeting
			// ("hi") or identity question is answered locally with zero LLM or
			// planner involvement, so casual chat never triggers a workspace
			// scan. streamCmd runs the same check for non-/ask paths.
			if response := m.interceptLocalIntent(content); response != "" {
				m.push(roleAI, response)
				return nil
			}
			return m.prepareAskStreamCmd(content)
		}

		return m.streamCmd(content)
	}
}

// prepareAskStreamCmd assembles /ask context (Context Planner query + fallback
// @file reads) on a background goroutine. It is the t=0ms async seam for the
// prompt-submit hot path: the Enter handler dispatches it together with the
// shimmer tick so the loading dock animates INSTANTLY while the planner scans
// the workspace. The goroutine performs strictly read-only work — the planner
// is pre-warmed on the event loop and captured, and no model field is written
// — so there is no data race with the UI goroutine. All state mutations happen
// in the askStreamPreparedMsg handler when the message lands.
func (m *model) prepareAskStreamCmd(content string) tea.Cmd {
	// Pre-warm the planner on the event loop (cheap construction — it only
	// assembles adapters, it never queries) so the background goroutine reads
	// an already-cached m.planner and can never race its lazy construction.
	p := m.contextPlanner()
	workspaceRoot := m.workspaceRoot
	return func() tea.Msg {
		prepared := content
		governed := false
		var trace *ctxpkg.CodebaseTrace
		if p != nil {
			if plan, err := p.Plan(context.Background(), content); err == nil && plan != nil && len(plan.Chunks) > 0 {
				header := fmt.Sprintf("### PLANNED CONTEXT (%s intent, %d tokens)\n\n",
					plan.Intent, plan.TokenTotal)
				prepared = header + plan.Assemble() + "\n\n" + content
				governed = true
				trace = planToTrace(plan)
			}
		}
		if !governed {
			// Fallback: ungoverned @file resolution + disk read. Runs here (off
			// the event loop) so even the degraded path never blocks submit.
			if augmented := m.injectFileContext(workspaceRoot, content, content); augmented != content {
				prepared = augmented
			}
			// The fallback attempted resolution, so streamCmd must not re-run it.
			governed = true
		}
		return askStreamPreparedMsg{content: prepared, governed: governed, trace: trace}
	}
}

// planFirstTokenTimeout bounds how long the LLM provider may take to return its
// FIRST chunk of plan synthesis. Cloud/remote models (OpenRouter free-tier, etc.)
// are subject to queueing, cold starts, and rate limiting — so a 120s first-token
// guard prevents spurious "context deadline exceeded" aborts while the provider
// is merely queued/slow. Local models that are OOM/stalling will hang
// indefinitely; this guard aborts fast so the UI never freezes for the full
// hard budget waiting on a dead provider socket.
const planFirstTokenTimeout = 120 * time.Second

// planLocalMaxLatency bounds how long a LOCAL (non-streaming) model may take to
// return a full completion. Unlike cloud providers, Ollama's /chat/completions
// is non-streaming: the "first token" is the entire prefill+generation latency,
// which a 7B model commonly exceeds. We therefore allow a realistic local budget
// while still keeping the hard cap as the overall ceiling.
const planLocalMaxLatency = 150 * time.Second

// buildGenerationTimeout bounds a single build/patch generation LLM call
// (the streaming fast-track path or the non-streaming per-task patch path).
// It is deliberately 5 minutes (300s): SLM fast-track streams must have enough
// headroom to emit every file mutation as raw code fences without tripping the
// context deadline mid-output, and OpenRouter free-tier models are routinely
// queued before the first token. This is the "5-minute fast-track window" that
// lets a rate-limited small model finish the whole unified build in one turn.
const buildGenerationTimeout = 5 * time.Minute

// hotfixApplyTimeout is the strict deadline for applying an approved hotfix
// patch to disk (file IO, shadow backup, transaction recording). A wedged
// filesystem or git operation must never freeze the "Applying hotfix..."
// spinner indefinitely — on expiry the apply aborts cleanly and a terminal
// buildResultMsg is emitted.
const hotfixApplyTimeout = 30 * time.Second

// runPlanEngineCmd executes the (potentially slow) PlanEngine ledger synthesis
// in a background goroutine so the synchronous LLM call never blocks the Bubble
// Tea event loop. The result is delivered asynchronously as a planResultMsg,
// which the Update() loop handles to stage tasks and clear streaming state.
//
// HARDENING: two layered deadlines protect the live terminal.
//  1. firstTokenCtx (120s cloud / 150s local) — the provider MUST return its
//     first response byte within this window. OpenRouter free-tier models are
//     routinely queued for well beyond 45s, so the guard is deliberately
//     generous to avoid false "context deadline exceeded" failures. If the
//     local model is stuck/OOM or the socket stalls, we abort immediately
//     instead of freezing the prompt for the full budget.
//  2. ctx (180s) — overall synthesis budget for a slow-but-alive model.

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

// compressHandoffSource aggressively prunes and compresses the handoff
// payload before it reaches the LLM. It strips verbose stack-trace
// lines, removes duplicate content, and caps the total size so the
// prompt stays within a tight token budget. This prevents the 2.5k+
// token handoff bloat that causes OpenRouter cold-start queuing and
// completion timeouts.
func compressHandoffSource(source string, maxBytes int) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	lines := strings.Split(source, "\n")
	kept := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	total := 0

	for _, raw := range lines {
		// Skip verbose stack-trace frames (raw lines starting with tabs)
		if strings.HasPrefix(raw, "\t") {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			// Keep at most one blank line between sections
			if len(kept) > 0 && kept[len(kept)-1] != "" {
				kept = append(kept, "")
			}
			continue
		}
		// Skip verbose stack-trace frames (Go "at " stack marker)
		if strings.Contains(trimmed, "at ") {
			continue
		}
		// Skip extremely long lines (>400 chars = likely verbose trace/context block)
		if len(trimmed) > 400 {
			continue
		}
		// Deduplicate
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}

		if total+len(trimmed)+1 > maxBytes {
			break
		}
		kept = append(kept, trimmed)
		total += len(trimmed) + 1
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func (m *model) runPlanEngineCmd(handoffSource, problem, modelName string, handoff HandoffContext) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	// Register cancel so it can be invoked on mode transition/Ctrl+C
	m.registerBackgroundCancel(cancel)

	return func() tea.Msg {
		debugLogPlan("runPlanEngineCmd entered; model=" + modelName)

		// ── COMPRESS HANDOFF PAYLOAD ──────────────────────────────
		// Strip verbose stack-trace lines, deduplicate, and cap the
		// raw payload before truncation. This prevents the 2.5k+ token
		// handoff bloat that causes OpenRouter cold-start queuing and
		// completion timeouts. Cloud models get a 2× ceiling instead
		// of 4× to keep the prompt lean.
		handoffSource = compressHandoffSource(handoffSource, plan.MaxLedgerChars)

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
			truncateCeiling = plan.MaxLedgerChars * 2
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
								StepNum: 1,

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
							StepNum: 1,

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
			tasks  []plan.Task
			err    error
			tokIn  int
			tokOut int
		}
		outCh := make(chan outcome, 1)

		// ── FIRST-TOKEN / COMPLETION GUARD ───────────────────────────────
		// The provider call (a single streaming round-trip inside
		// ProcessFromLedger) inherits this deadline. OpenRouter free-tier
		// models are frequently queued/cold-started well past 45s, so the
		// cloud guard is set to 120s to prevent spurious context deadline
		// exceeded failures. Local Ollama calls are non-streaming: "first
		// token" == the entire prefill+generation latency, which a 7B model
		// easily exceeds. For local models we therefore use an even more
		// realistic budget (the hard cap still applies as the overall ctx),
		// and only fall back to the cloud prompt if the model is genuinely
		// unresponsive.
		ftBudget := planFirstTokenTimeout
		if localModel {
			ftBudget = planLocalMaxLatency
		}
		ftCtx, ftCancel := context.WithTimeout(ctx, ftBudget)
		defer ftCancel()

		// ── INTENT COMPILER PIPELINE PRIME PATH ─────────────────────────────
		// Generation requests are planned deterministically by the IR-driven
		// intent compiler (inspect → infer → policy → IR plan → lower → tasks),
		// bypassing LLM plan synthesis AND its heuristic prose fallback (now
		// hard-killed) entirely. The candidates are the short user goal, the
		// handoff payload (which re-embeds the raw user intent from the ledger)
		// and the last typed prompt, in that order. When it takes ownership it
		// stages explicit CREATE/WRITE file tasks from the lowered FileArtifacts
		// or surfaces the exact policy/lowering rejection reason.
		if m.intentCompiler != nil {
			if tasks, handled, icErr := m.intentCompiler.TryPlan(ftCtx, problem, handoffSource, m.currentPrompt); handled {
				cancel()
				if icErr != nil {
					debugLogPlan("INTENT COMPILER: rejected — " + icErr.Error())
					return planResultMsg{Err: icErr, Handoff: handoff, IntentCompiler: true}
				}
				if len(tasks) > 0 {
					debugLogPlan("INTENT COMPILER: staged " + fmt.Sprint(len(tasks)) + " artifact task(s)")
					return planResultMsg{Tasks: tasks, Handoff: handoff, IntentCompiler: true}
				}
				debugLogPlan("INTENT COMPILER: produced zero tasks — falling back")
			} else {
				debugLogPlan("INTENT COMPILER: not applicable — falling back")
			}
		}

		// ── MICROKERNEL PRIME PATH (immutable plan pipeline fallback) ─────
		// The legacy immutable microkernel pipeline remains as a secondary
		// deterministic path for greenfield requests the intent compiler does
		// not own (e.g. non-web generation). It runs before LLM synthesis.
		if m.microkernel != nil {
			if tasks, handled, mkErr := m.microkernel.TryPlan(ftCtx, handoffSource, problem); handled {
				cancel()
				if mkErr != nil {
					debugLogPlan("MICROKERNEL: rejected — " + mkErr.Error())
					return planResultMsg{Err: mkErr, Handoff: handoff, Microkernel: true}
				}
				if len(tasks) > 0 {
					debugLogPlan("MICROKERNEL: staged " + fmt.Sprint(len(tasks)) + " deterministic task(s)")
					return planResultMsg{Tasks: tasks, Handoff: handoff, Microkernel: true}
				}
				debugLogPlan("MICROKERNEL: produced zero tasks — falling back to legacy synthesis")
			} else {
				debugLogPlan("MICROKERNEL: not applicable — falling back to legacy plan synthesis")
			}
		}

		// ── WORKSPACE DISCOVERY ──────────────────────────────────────────────
		// If the plan engine has no allowed file tree yet, discover it now via
		// pkg/recon + pkg/grounding so the scope guard can validate targets.
		if m.planEngine != nil && m.workspaceRoot != "" {
			if len(m.planEngine.AllowedFiles) == 0 {
				m.planEngine.SetRootPath(m.workspaceRoot)
				allowed, discErr := m.planEngine.DiscoverAllowedFiles()
				if discErr != nil {
					debugLogPlan("WORKSPACE DISCOVERY: " + discErr.Error())
				} else {
					debugLogPlan("WORKSPACE DISCOVERY: " + fmt.Sprint(len(allowed)) + " files in tree")
				}
			}
		}

		go func() {
			debugLogPlan("Preparing LLM payload (ledger bytes=" + fmt.Sprint(len(ledgerToSend)) +
				"; fastTrack=" + fmt.Sprint(useFastTrack) + ")")
			var tasks []plan.Task
			var err error
			tokIn, tokOut := 0, 0
			if useFastTrack {
				tasks, err = m.planEngine.ProcessFromLedgerFastTrack(ftCtx, ledgerToSend, modelName)
			} else {
				tasks, err = m.planEngine.ProcessFromLedger(ftCtx, ledgerToSend, problem, modelName)
			}
			// Capture the provider-reported usage (committed by the plan engine
			// even when the response was truncated at finish_reason: "length")
			// so the token counters survive truncation.
			if m.planEngine != nil {
				tokIn, tokOut = m.planEngine.LastUsage()
			}
			debugLogPlan("Provider returned; err=" + fmt.Sprint(err))

			// ── SCOPE GUARD VALIDATION ──────────────────────────────────────
			// Validate generated tasks against the allowed file tree before
			// returning to the event loop. If a violation is detected, reject the
			// plan, log the rejection, and attempt ONE retry with the strict
			// scope instruction injected into the prompt.
			if err == nil && len(tasks) > 0 && len(m.planEngine.AllowedFiles) > 0 {
				scopeTasks := make([]control.TaskTarget, len(tasks))
				for i, t := range tasks {
					scopeTasks[i] = control.TaskTarget{Target: t.Target, Type: string(t.Type)}
				}
				if scopeErr := control.ValidateStagedPlan(scopeTasks, m.planEngine.AllowedFiles); scopeErr != nil {
					var sv *control.ScopeViolationError
					if errors.As(scopeErr, &sv) {
						debugLogPlan("SCOPE GUARD: rejected target " + sv.TargetString())
						// Build a retry prompt with the exact allowed file list.
						retryPrompt := ledgerToSend + control.FormatRepromptInstruction(m.planEngine.AllowedFiles)
						retryTasks, retryErr := m.planEngine.ProcessFromLedger(ftCtx, retryPrompt, problem, modelName)
						if retryErr == nil && len(retryTasks) > 0 {
							// Re-validate retry tasks.
							retryScopeTasks := make([]control.TaskTarget, len(retryTasks))
							for i, t := range retryTasks {
								retryScopeTasks[i] = control.TaskTarget{Target: t.Target, Type: string(t.Type)}
							}
							if retryScopeErr := control.ValidateStagedPlan(retryScopeTasks, m.planEngine.AllowedFiles); retryScopeErr == nil {
								debugLogPlan("SCOPE GUARD: retry succeeded with " + fmt.Sprint(len(retryTasks)) + " tasks")
								tasks = retryTasks
								err = nil
							} else {
								debugLogPlan("SCOPE GUARD: retry also rejected — " + fmt.Sprint(retryScopeErr))
								// Keep original tasks but annotate the error.
								err = fmt.Errorf("scope guard: %w (retry also rejected %w)", scopeErr, retryScopeErr)
							}
						} else {
							if retryErr != nil {
								debugLogPlan("SCOPE GUARD: retry failed — " + fmt.Sprint(retryErr))
								err = fmt.Errorf("scope guard: %w (retry failed %w)", scopeErr, retryErr)
							} else {
								debugLogPlan("SCOPE GUARD: retry produced no tasks")
								err = fmt.Errorf("scope guard: %w (retry produced no tasks)", scopeErr)
							}
						}
						// Refresh usage after the scope-guard retry (the retry
						// consumed another provider round-trip).
						if m.planEngine != nil {
							tokIn, tokOut = m.planEngine.LastUsage()
						}
					}
				}
			}

			outCh <- outcome{tasks: tasks, err: err, tokIn: tokIn, tokOut: tokOut}
		}()

		select {
		case o := <-outCh:
			cancel()
			return planResultMsg{Tasks: o.tasks, Err: o.err, Handoff: handoff, TokenInput: o.tokIn, TokenOutput: o.tokOut}
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
			// Determine the active provider for a provider-aware error message.
			activeProvider := m.cfg.ActiveProviderName()
			if activeProvider == "" {
				activeProvider = "unknown"
			}
			var providerErrMsg string
			switch activeProvider {
			case "openrouter":
				providerErrMsg = fmt.Sprintf("[error] OpenRouter request timed out (> %s). Free-tier models may be queued or rate-limited.", planFirstTokenTimeout)
			case "ollama":
				providerErrMsg = fmt.Sprintf("[error] LLM Provider timeout: no response within %s. Check if your local model is stuck/OOM, or that Ollama is running and the model (%s) is loaded", planFirstTokenTimeout, modelName)
			default:
				providerErrMsg = fmt.Sprintf("[error] LLM Provider timeout: no response within %s. The provider (%s) may be slow, overloaded, or unreachable.", planFirstTokenTimeout, activeProvider)
			}
			return planResultMsg{
				Err:     fmt.Errorf("%s", providerErrMsg),
				Handoff: handoff,
			}
		case <-ctx.Done():
			debugLogPlan("hard 180s timeout — aborting")
			return planResultMsg{Err: fmt.Errorf("plan synthesis timed out after 180s: %w", ctx.Err()), Handoff: handoff}
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

// phaseForMode maps a UI mode onto its canonical orchestrator phase. The
// orchestrator uses these to drive the shared WorkflowStateMachine.
func phaseForMode(mode modes.Mode) orchestrator.Phase {
	switch mode {
	case modes.ModeAsk:
		return orchestrator.PhaseAsk
	case modes.ModeInvestigate:
		return orchestrator.PhaseInvestigate
	case modes.ModePlan:
		return orchestrator.PhasePlan
	case modes.ModeBuild:
		return orchestrator.PhaseBuild
	case modes.ModeReview:
		return orchestrator.PhaseReview
	default:
		return orchestrator.PhaseIdle
	}
}

func (m *model) setMode(mode modes.Mode) tea.Cmd {
	// ── ORCHESTRATOR PHASE SYNC ─────────────────────────────────────
	// The orchestrator maps the logical mode onto the shared WorkflowStateMachine
	// while preserving the persistent RuntimeContext (conversation history and
	// workspace artifacts are never reset). Force is used because an explicit
	// user mode switch always wins over the logical phase graph (e.g. switching
	// directly from /review to /plan is not a valid edge).
	if m.orch != nil {
		_ = m.orch.Force(phaseForMode(mode), workflow.TransitionContext{
			HasPlan:         m.sess != nil && len(m.sess.CurrentTasks) > 0,
			HasCapabilities: m.caps != nil,
		})
	}

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

	// ── CLEAN STATE: flush tool call buffer, thinking panel, live preview ──
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reset()
	}
	if m.thinkingPanel != nil {
		m.thinkingPanel.Reset()
	}
	if m.liveCodePreview != nil {
		m.liveCodePreview.Reset()
	}
	// Clear stale error messages and execution log buffers
	m.lastApplyError = ""
	m.applyErrorTime = time.Time{}
	if m.logStore != nil {
		m.logStore.Clear()
	}

	// ── CLEAR STALE UI WIDGETS ON MODE TRANSITION ─────────────────────
	// Overlay/proposal widgets are scoped to the mode that created them:
	// the Effort selector, mutation approval dock, and pending proposal
	// cards (e.g. a stale "Edit: [Resolve]" frame) belong to the build
	// workflow and MUST NOT bleed into /plan or /investigate views. Reset
	// the interaction state and drop pending proposals so the incoming
	// mode renders its own clean dock.
	m.resolveApprovalState()
	m.pendingProposals = nil
	m.proposalDiffOffset = 0
	m.currentEffort = EffortAuto
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil

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

	// ── SILENT MODE TRANSITION ────────────────────────────────────────
	// Mode switches must not spam the conversation viewport. The active
	// prompt indicator ("plan )" / "build )") is updated by startModeTransition
	// above; only a transient footer notice confirms the switch.
	m.uiNotice = fmt.Sprintf("Switched to /%s", mode)

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

// buildMutationHandoffPayload creates an active mutation prompt from
// synthesized pending todos. This is used when the deadlock-guard or
// short-circuit logic routes from /investigate directly to /build,
// ensuring the Execution Engine receives a mutation prompt instead of
// a generic greeting. The payload contains only the task descriptions,
// stripped of any conversational framing.
func buildMutationHandoffPayload(todos []string) string {
	if len(todos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## MUTATION HANDOFF — AUTO-TRIGGERED FROM INVESTIGATION\n\n")
	b.WriteString("Execute the following mutation tasks immediately. Do NOT ask for approval or restate the plan.\n\n")
	for i, todo := range todos {
		// Strip the icon prefix for cleaner task display.
		clean := strings.TrimSpace(todo)
		if idx := strings.Index(clean, "] "); idx > 0 {
			clean = strings.TrimSpace(clean[idx+2:])
		}
		fmt.Fprintf(&b, "Task %d: %s\n", i+1, clean)
	}
	b.WriteString("\nBEGIN EXECUTION NOW.")
	return b.String()
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
		// REFORM: /build STRICTLY consumes:
		//   1. The user's raw intent (UserRawIntent from the ledger)
		//   2. The atomic structural tasks produced by /plan (PendingTodos / staged tasks)
		// It must NEVER fall back to the raw conversational ProposedFix blob or
		// AssistantDiscussionNotes — those may contain pre-baked hallucinated steps.
		// If no atomic tasks exist, return "" so setMode enters a clean idle state.
		hasStagedTasks := len(m.sess.CurrentTasks) > 0
		if len(m.handoffCtx.PendingTodos) == 0 && !hasStagedTasks {
			// DEADLOCK-GUARD FALLBACK: when the investigate engine
			// short-circuited with mutation intent and no structured
			// tasks were synthesized, create one from the handoff ledger.
			if strings.Contains(m.handoffLedgerContent, "code mutation intent detected") {
				m.handoffCtx.PendingTodos = synthesizeBuildTodosFromMutation(m.handoffLedgerContent)
			}
			if len(m.handoffCtx.PendingTodos) == 0 {
				return ""
			}
		}

		rawIntent := ""
		if m.sess != nil && m.sess.ContextLedger != nil {
			rawIntent = m.sess.ContextLedger.UserRawIntent
		}

		var b strings.Builder
		if rawIntent != "" {
			b.WriteString("## USER RAW INTENT\n")
			b.WriteString(rawIntent)
			b.WriteString("\n\n")
		}
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
	// Strategy-aware strict directive: the required output format follows the
	// target file's on-disk state. Forcing SEARCH/REPLACE on a new/0-byte file
	// makes small models loop on a missing "old content" anchor and time out.
	var outputFormat string
	if data, rerr := os.ReadFile(targetTask.Target); rerr == nil && len(data) > 0 {
		outputFormat = "- For existing files: ```go:main.go\n  <<<<<<< SEARCH\n  ...\n  =======\n  ...\n  >>>>>>>\n  ```\n"
	} else {
		outputFormat = "- For new files: ```<language>\n  <COMPLETE file content>\n  ```\n  (or a FILE: <path> block). Do NOT use SEARCH/REPLACE or unified diff — the file does not exist yet.\n"
	}
	strictContent := fmt.Sprintf(
		"## STRICT BUILD DIRECTIVE — ZERO CONVERSATIONAL TEXT\n\n"+
			"YOU ARE A CODE GENERATION TOOL. DO NOT OUTPUT ANY TEXT THAT IS NOT A CODE PATCH.\n\n"+
			"REQUIRED OUTPUT FORMAT (FIRST TOKEN MUST MATCH):\n"+
			outputFormat+
			"FORBIDDEN OUTPUT:\n"+
			"- Greetings, acknowledgments, summaries, explanations\n"+
			"- Questions, clarifications, suggestions\n"+
			"- Markdown that is not a valid patch or complete file content\n"+
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
	m.resetStreamBlocks()
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
		m.push(roleSystem, accentStyle.Render(Icon.Index+" [IZEN Shortcut] Running dynamic test suite before auditing commit risks..."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.runReviewTestComposite()
	}

	// ── Alias / prefix resolution ───────────────────────────────────────
	// Resolve registered aliases and unambiguous prefixes to their canonical
	// command so "/q" executes as "/quit" instead of dumping "unknown
	// command: /q" to the output. Args are preserved for prefixed commands
	// ("/m claude" → "/model claude"); the trailing separator is trimmed so
	// exact-match cases still fire for bare commands.
	if resolved := resolveCommandToken(name[0]); resolved != name[0] {
		cmd = resolved + " " + strings.Join(name[1:], " ")
		name[0] = resolved
		cmd = strings.TrimSpace(cmd)
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
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint  /arch <layer|pkg>"))
		m.push(roleSystem, infoStyle.Render("  /explain-decision  inspect why a tech stack was chosen"))
		m.push(roleSystem, infoStyle.Render("  /objective approve  approve budget-guarded objective"))
		m.push(roleSystem, infoStyle.Render("  /usage           inspect token usage and provider status"))
		m.push(roleSystem, infoStyle.Render("  /model        interactive model picker (fuzzy search)"))
		m.push(roleSystem, infoStyle.Render("  /model <name> switch active model directly (e.g. /model claude-3-5-sonnet)"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>  run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("ask sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $prompt <idea>  refine architectural idea into actionable prompt"))
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
		m.beginQuitConfirm()
		return nil

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

	case strings.HasPrefix(cmd, "/model "):
		modelArg := strings.TrimSpace(strings.TrimPrefix(cmd, "/model"))
		if modelArg == "" {
			m.push(roleSystem, infoStyle.Render("usage: /model <model_name>  — switch active model directly"))
			return nil
		}
		return m.switchModelDirect(modelArg)

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
		m.resetStreamBlocks()
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

	case strings.HasPrefix(cmd, "/arch"):
		m.showBanner = false
		args := strings.TrimSpace(strings.TrimPrefix(cmd, "/arch"))
		if m.indexingStatus == "indexing" {
			m.pendingArchArgs = args
			m.push(roleSystem, infoStyle.Render("[⠋] Mapping codebase structure... (indexing in progress)"))
			m.refreshViewportContent()
			return m.spinnerTickCmd()
		}
		m.push(roleSystem, "Mapping codebase...")
		m.refreshViewportContent()
		return func() tea.Msg {
			graphText := m.renderArch(args)
			return archDoneMsg{Content: graphText}
		}

	case cmd == "/explain-decision":
		return m.runExplainDecisionCmd()

	}

	m.push(roleError, "unknown command: "+cmd)
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// switchModelDirect handles /model <model_name> for direct, non-interactive
// model switching. It resolves the model name against the configured providers
// and sets it as the session-level override (m.sessionModel).
func (m *model) switchModelDirect(modelName string) tea.Cmd {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		m.push(roleSystem, infoStyle.Render("usage: /model <model_name>  — switch active model directly"))
		return nil
	}

	// Check model tier configuration for active_override or tier-default resolution.
	// If the model name matches an active_override in any tier, use that tier's
	// provider association for routing.
	resolvedProvider := ""
	if m.cfg.Models.Tiers != nil {
		for _, tc := range m.cfg.Models.Tiers {
			if tc.ActiveOverride == modelName || tc.Model == modelName {
				if tc.Provider != "" {
					resolvedProvider = tc.Provider
				}
				break
			}
		}
	}

	// Set the session model override immediately so the status bar reflects
	// the change before the provider switch completes.
	m.sessionModel = modelName
	m.cfg.Models.SessionModel = modelName
	// Re-pin the pipeline router intent tiers to the newly active model so
	// mode commands never route a stale local model into a cloud request.
	m.syncPipelineTiers()

	// Determine the provider for this model. If we couldn't resolve it from
	// tier config, try to infer from the model name format.
	if resolvedProvider == "" {
		resolvedProvider = m.inferProviderFromModel(modelName)
	}

	// If the provider changed, switch providers.
	if resolvedProvider != "" {
		currentProvider := ""
		if m.provider != nil {
			currentProvider = m.provider.Name()
		}
		if resolvedProvider != currentProvider {
			// Validate the provider exists in config.
			if _, ok := m.cfg.AI.Providers[resolvedProvider]; ok || resolvedProvider == "ollama" {
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("switching to provider %q for model %q...", resolvedProvider, modelName)))
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return tea.Batch(
					m.switchProvider(resolvedProvider),
					func() tea.Msg {
						return providerSwitchMsg{name: resolvedProvider}
					},
				)
			}
		}
	}

	m.ti.Focus()
	m.push(roleSystem, accentStyle.Render(fmt.Sprintf("✓ Model set to %s", modelName)))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// inferProviderFromModel tries to infer the provider from the model name format.
// Model names with "/" are treated as openrouter-style (provider/model).
// Ollama models (e.g. qwen2.5-coder:7b, llama3:8b) default to ollama.
func (m *model) inferProviderFromModel(modelName string) string {
	if strings.Contains(modelName, "/") {
		return "openrouter"
	}
	// Check if the model name looks like an Ollama model (contains ":" version tag)
	// or is a known local model pattern. Default to ollama for non-cloud models.
	if strings.Contains(modelName, ":") {
		return "ollama"
	}
	// For bare model names without version tags, check if it's a known cloud model.
	cloudModels := map[string]bool{
		"gpt-4o": true, "gpt-4": true, "gpt-4-turbo": true, "gpt-3.5-turbo": true,
		"claude-sonnet-4-20250514": true, "claude-3-5-sonnet": true, "claude-3-opus": true,
		"llama-3.3-70b-versatile": true, "llama3": true, "llama3.1": true, "llama3.2": true,
		"mistral": true, "mixtral": true,
	}
	if cloudModels[modelName] {
		return "openai"
	}
	return "ollama"
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
				Strategy:    string(t.Type),
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
	m.resetStreamBlocks()
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

// transitionToBuilding attempts to move the WorkflowStateMachine into
// StateBuilding before any build execution begins. It handles the
// idempotent case (already Building) and gracefully falls through
// when plan guards prevent the transition (e.g. missing plan from
// StateIdle). Callers must not invoke authorizeBuildExecution when
// this returns an error.
func (m *model) transitionToBuilding() error {
	if m.workflowSM == nil {
		return nil
	}
	state := m.workflowSM.State()
	if state == workflow.StateBuilding || state == workflow.StateRepairing {
		return nil
	}
	// ── ORCHESTRATOR-DRIVEN TRANSITION ──────────────────────────────
	// The orchestrator owns the workflow SM. It drives the canonical
	// Idle -> Plan -> Build path (and any required reset fallback) while
	// sharing the persistent RuntimeContext, so conversation history and
	// workspace artifacts survive the transition.
	if m.orch != nil {
		return m.orch.Transition(orchestrator.PhaseBuild, workflow.TransitionContext{
			HasPlan:         len(m.sess.CurrentTasks) > 0,
			HasCapabilities: m.caps != nil,
		})
	}
	// Legacy raw-SM fallback (headless/test harnesses without an orchestrator).
	if state == workflow.StateIdle {
		if err := m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
			return err
		}
	}
	if m.workflowSM.State() == workflow.StatePlanning {
		return m.workflowSM.SendEvent(workflow.EventBuild, workflow.TransitionContext{
			HasPlan:         len(m.sess.CurrentTasks) > 0,
			HasCapabilities: m.caps != nil,
		})
	}
	return fmt.Errorf("workflow: cannot transition to building from %s", state)
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
				taskTarget = m.resolvePendingTodoTarget(t)
			}
			if taskTarget == "" {
				continue
			}
			tasks = append(tasks, plan.Task{
				StepNum:     i + 1,
				Type:        task.TaskType(taskType),
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

	// ── EARLY BUDGET SCALING ────────────────────────────────────────────
	// Before executing the first step, ScaleBudget to the exact number of
	// staged plan tasks so MaxMutations > 0 and IsMultiStepPlan() returns true.
	// This converts each step's authorization from single-use (consuming the
	// budget per step) to multi-step (non-single-use, budget consumed only
	// once). Without this, Step 1 exhausts the mutation budget and Steps 2..N
	// all fail with "mutation budget already exhausted".
	if m.mutationBudget != nil {
		n := len(m.sess.CurrentTasks)
		if n > 0 {
			m.mutationBudget.ScaleBudget(n)
		}
	}

	// Execute the first idle staged task.
	// ── FAST-TRACK EXECUTION BYPASS ──────────────────────────────
	// When multiple FILE_MUTATE tasks target different files, bypass
	// the legacy per-task iteration loop (handleBuildRun) which
	// processes tasks one at a time with SEARCH/REPLACE diff parsing.
	// Instead, combine all plan goals into a SINGLE unified prompt
	// request with native write_file / apply_patch tools and dispatch
	// it directly to the Native Agentic Tool Loop (ToolCallBuffer).
	// This eliminates "executing step N: FILE_MUTATE" noise and
	// gives the LLM full context of all file mutations in one pass.
	if m.isFastTrackEligible() {
		return m.performFastTrackBuildCmd()
	}
	return m.handleBuildRun(0)
}

// isFastTrackEligible returns true when there are at least 2 staged
// tasks that are idle/processing and all of them are FILE_MUTATE or
// GIT_ACTION targets, indicating a multi-file generation prompt that
// benefits from unified native-tool execution rather than legacy
// per-task SEARCH/REPLACE iteration.
func (m *model) isFastTrackEligible() bool {
	tasks := m.sess.CurrentTasks
	eligible := 0
	for _, t := range tasks {
		if t.Status != "idle" && t.Status != "processing" {
			continue
		}
		if t.Type != "FILE_MUTATE" && t.Type != "GIT_ACTION" {
			return false
		}
		eligible++
	}
	return eligible >= 2
}

// performFastTrackBuildCmd executes all staged FILE_MUTATE/GIT_ACTION tasks
// in a single unified agentic session, bypassing the legacy per-task
// iteration loop. It dispatches native write_file / apply_patch tool calls
// to the LLM, buffers all responses into ToolCallBuffer, and triggers
// buildProposalReadyMsg to display the interactive Human Approval Screen.
// The API call is executed asynchronously without freezing the UI.
func (m *model) performFastTrackBuildCmd() tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		m.push(roleStatus, "no tasks staged — use /plan first")
		return nil
	}
	if m.toolCallBuffer == nil {
		return m.runBuildFastTrack()
	}
	m.streaming = true
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.agentRunning = true
	m.agentLabel = "building"
	m.agentDone = false
	m.pipelineRunning = true
	m.startShimmer("Executing strategy...", "execute")
	m.push(roleStatus, "BUILDING...")
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return tea.Batch(m.smoothStreamTickCmd(), m.shimmerTickCmd(), m.runBuildFastTrack())
}

// runBuildFastTrack executes all staged FILE_MUTATE tasks in a single
// unified agentic session, bypassing the legacy per-task iteration loop.
// It combines plan goals into one prompt, dispatches native write_file /
// apply_patch tool calls to the LLM, buffers all responses into
// ToolCallBuffer, and presents all diffs for human approval at once.
// Reasoning tokens (<thought>) and live tool call arguments stream into
// the TUI Thinking Panel and Live Code Preview during execution.
func (m *model) runBuildFastTrack() tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		m.push(roleStatus, "no tasks staged — use /plan first")
		return nil
	}

	// Transition workflow state to Building.
	if err := m.transitionToBuilding(); err != nil {
		m.push(roleError, fmt.Sprintf("[BUILD HALTED] Workflow state transition failed: %v", err))
		return nil
	}

	// Virtual snapshot staging.
	if m.execEng != nil {
		m.execEng.BeginTransaction()
	}

	// Clear the LLM dialog buffer at the start of every build invocation.
	if m.sess != nil {
		m.sess.ClearHistory()
		_ = m.sess.Save()
	}

	m.responseBuffer.Reset()
	m.traceBuffer.Reset()
	m.traceExpanded = false
	if m.execEng != nil {
		m.execEng.SetStreamContextFiles(m.attachedFiles)
	}

	// ── Clear previous state ──────────────────────────────────────
	if m.buildLedger == nil {
		m.buildLedger = ctxpkg.NewTaskLedger()
	}
	// A fresh build invocation invalidates any prior fast-track coverage
	// tracking (Explicit Over Implicit: coverage is per-batch).
	m.fastTrackTargets = nil
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reset()
	}
	if m.thinkingPanel != nil {
		m.thinkingPanel.Reset()
	}
	if m.liveCodePreview != nil {
		m.liveCodePreview.Reset()
	}

	// ── EARLY BUDGET SCALING ──────────────────────────────────────
	if m.mutationBudget != nil {
		n := len(tasks)
		if n > 0 {
			m.mutationBudget.ScaleBudget(n)
		}
	}

	// Bridge the live /plan task ledger into the execution engine.
	m.execEng.Patches.SetLedger(m.buildLedger)
	m.execEng.Patches.SetContextID(m.sess.ContextID)

	// ── Build unified prompt from all task goals ──────────────────
	// Combine plan goals into a SINGLE unified prompt. The prompt package owns
	// the tier adaptation: Mid/Frontier models are told to emit native
	// write_file / apply_patch tool calls; TierSLM models get a prompt with ALL
	// JSON tool definitions stripped that enforces plain markdown code blocks
	// (```lang:path) instead — a small model loops on tool-call JSON syntax, so
	// it must be steered straight to raw code fences.
	//
	// ── REWRITE CONTEXT SANITIZATION (TaskContext hygiene) ────────────
	// Under a full-rewrite intent (CLEAR ALL EXISTING CODE / redesign /
	// rewrite / brand-new-from-scratch) the current workspace file contents are
	// OBSOLETE. fastTrackGoals and fastTrackFileContext strip the old file
	// bytes and carry ONLY the explicit user intent, the target filename and
	// the create-from-scratch directive — never the old implementation — so
	// the model can never anchor on it (e.g. regenerating the To-Do App
	// instead of the requested Portfolio).
	m.currentBuildTaskID = 0 // unified session, no single task tracking
	intent := m.buildIntentContext()

	var targets []string
	seen := make(map[string]bool)
	for _, t := range tasks {
		if seen[t.Target] {
			continue
		}
		seen[t.Target] = true
		targets = append(targets, t.Target)
	}
	fileContext := fastTrackFileContext(intent, targets, os.ReadFile)
	if !isFullRewriteIntent(intent) && m.activityTree != nil {
		for _, target := range targets {
			if data, err := os.ReadFile(target); err == nil {
				m.activityTree.Append(NewFileReadEvent(target, int64(len(data)), 0))
			}
		}
	}

	fullPrompt := prompt.FastTrackPromptForTier(m.promptTier(), fileContext, fastTrackGoals(intent, tasks))

	// ── Build system and request ──────────────────────────────────
	// The system prompt is tier-adapted: SLM models receive the compact
	// positive-only contract, Mid/Frontier models the full-context contract.
	systemPrompt := m.tieredModePrompt(m.resolver.Current().String())

	// Construct messages without repeating the plan JSON ledger
	// to prevent 7B context drift from the model re-printing its own plan.
	var msgs []ai.Message
	if history := m.sess.History; len(history) > 0 {
		for _, msg := range history {
			raw := msg.Content
			if r := plan.ParseJSONPlan(raw); r != nil && r.Valid && r.Plan != nil {
				continue
			}
			msgs = append(msgs, ai.Message{Role: msg.Role, Content: raw})
		}
	}

	// ── SLIDING WINDOW TRUNCATION ──────────────────────────────────
	// Keep at most the last 20 history entries (≈10 exchanges) to
	// prevent unbounded token growth.
	const maxHistoryMessages = 20
	if len(msgs) > maxHistoryMessages {
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}

	msgs = append(msgs, ai.Message{Role: "user", Content: fullPrompt})

	if len(msgs) > 0 && msgs[0].Role == "system" {
		msgs[0].Content = systemPrompt
	} else {
		msgs = append([]ai.Message{{Role: "system", Content: systemPrompt}}, msgs...)
	}

	req := ai.Request{
		Model:     m.activeRouteModel(),
		Messages:  msgs,
		Stream:    true,
		System:    systemPrompt,
		MaxTokens: 8192,
		Tools:     m.fileMutationTools(),
		// Dynamically resolved reasoning directive (auto effort / tier-aware
		// SLM CoT caps). Nil when no reasoning control is warranted.
		Reasoning: m.effortFromTasks(),
	}

	// ── REAL-TIME SSE STREAMING ──────────────────────────────────────
	// Open a streaming connection and dispatch tokens to the TUI as they
	// arrive. The Thinking Panel updates from token #1 and the LiveCodePreview
	// reflects tool call arguments as they stream in. A deferred guarantee
	// ensures the spinner always stops and the pipeline resets, even on
	// truncation (finish_reason == "length") or provider error.
	m.streamCh = make(chan tea.Msg, 1024)
	// Capture locally so the goroutine never sends on m.streamCh after
	// Update() clears it to nil (prevents send-on-nil-channel panic).
	streamCh := m.streamCh

	go func() {
		// Recover from any panic in the stream goroutine so a bad token
		// chunk or streaming error never kills the Bubble Tea loop.
		defer func() {
			if r := recover(); r != nil {
				select {
				case streamCh <- buildFailedMsg{Err: fmt.Errorf("stream panic: %v", r)}:
				default:
				}
			}
		}()

		defer func() {
			m.pipelineRunning = false
			m.agentRunning = false
			m.streaming = false
		}()

		ctx, cancel := context.WithTimeout(context.Background(), buildGenerationTimeout)
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			select {
			case streamCh <- buildFailedMsg{Err: fmt.Errorf("fast-track stream failed: %w", err)}:
			default:
			}
			return
		}
		defer func() { _ = rawStream.Close() }()

		var fullContent strings.Builder
		var reasoningBuf strings.Builder
		var toolCalls []ai.ToolCall
		var buf strings.Builder
		readBuf := make([]byte, 4096)

		// usageProvider captures cumulative token usage from the stream reader
		// (authoritative when a usage chunk arrived, otherwise a character
		// estimate) so consumed tokens survive a timeout/error path.
		type usageProvider interface {
			Usage() (input, output int)
		}
		streamUsage := func() (int, int) {
			if up, ok := rawStream.(usageProvider); ok {
				return up.Usage()
			}
			return 0, 0
		}

		// ── REASONING TERMINAL EVENT GUARANTEE ───────────────────────────
		// Whatever way the stream ends (EOF, provider error, truncation), any
		// reasoning already forwarded to the bus must be closed with a terminal
		// IsComplete event so the UI never shows an orphaned open thinking
		// block. Reasoning tokens already published are never dropped.
		defer func() {
			if m.bus != nil && reasoningBuf.Len() > 0 {
				m.bus.Publish(events.NewReasoningStream("", true))
			}
		}()

		sentinelRSNG := "\x00RSNG\x00"
		sentinelTCLL := "\x00TCLL\x00"

		// flushSafeContent returns the portion of buf that is safe to treat
		// as final visible content — i.e. it cannot be, or be the start of,
		// a \x00RSNG\x00 or \x00TCLL\x00 sentinel that just hasn't fully
		// arrived yet — and leaves the remaining tail buffered for the next
		// read. Model output doesn't legitimately contain raw NUL bytes, so
		// holding back everything from the last NUL onward never withholds
		// real content; it only ever withholds an in-flight or malformed
		// marker. At true stream end (atEOF) nothing more will ever arrive
		// to complete a pending marker, so the whole buffer is released.
		flushSafeContent := func(buf *strings.Builder, atEOF bool) string {
			s := buf.String()
			if atEOF || s == "" {
				buf.Reset()
				return s
			}
			if lastNul := strings.LastIndexByte(s, 0x00); lastNul >= 0 {
				safe := s[:lastNul]
				tail := s[lastNul:]
				buf.Reset()
				buf.WriteString(tail)
				return safe
			}
			buf.Reset()
			return s
		}

		// runeBuf makes the fast-track ingestion UTF-8 safe: raw Read chunks
		// are not rune-aligned, so slicing string(readBuf[:n]) directly could
		// split a multi-byte rune across two reads. RuneBuffer holds incomplete
		// runes until they complete and only releases whole runes.
		runeBuf := stream.NewRuneBuffer()

		for {
			n, err := rawStream.Read(readBuf)
			if n > 0 {
				buf.WriteString(runeBuf.Write(readBuf[:n]))
			}
			// On EOF release any incomplete rune still held back so no
			// already-received bytes are dropped before final dispatch.
			if err == io.EOF {
				if rem := runeBuf.Flush(); rem != "" {
					buf.WriteString(rem)
				}
			}

			// ── Extract reasoning tokens (real-time) ──
			for {
				idx := strings.Index(buf.String(), sentinelRSNG)
				if idx < 0 {
					break
				}
				rest := buf.String()[idx+len(sentinelRSNG):]
				endIdx := strings.Index(rest, sentinelRSNG)
				if endIdx < 0 {
					break
				}
				reasoningChunk := rest[:endIdx]
				reasoningBuf.WriteString(reasoningChunk)
				// Forward to the event bus as well so the unified ThinkingBuffer
				// (fed by EventReasoningStream) renders the same reasoning the
				// legacy thinkingStreamMsg path shows. Never dropped even if the
				// request times out afterwards.
				if m.bus != nil {
					m.bus.Publish(events.NewReasoningStream(reasoningChunk, false))
				}
				select {
				case streamCh <- thinkingStreamMsg{Content: reasoningChunk}:
				default:
				}
				// Remove processed content from buf
				remaining := buf.String()[:idx] + rest[endIdx+len(sentinelRSNG):]
				buf.Reset()
				buf.WriteString(remaining)
			}

			// ── Extract tool call deltas (real-time) ──
			for {
				idx := strings.Index(buf.String(), sentinelTCLL)
				if idx < 0 {
					break
				}
				rest := buf.String()[idx+len(sentinelTCLL):]
				endIdx := strings.Index(rest, sentinelTCLL)
				if endIdx < 0 {
					break
				}
				tcJSON := rest[:endIdx]
				var tcDelta struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				}
				if jsonErr := json.Unmarshal([]byte(tcJSON), &tcDelta); jsonErr == nil && tcDelta.ID != "" {
					toolCalls = append(toolCalls, ai.ToolCall{
						ID:   tcDelta.ID,
						Type: tcDelta.Type,
						Function: ai.ToolCallFunction{
							Name:      tcDelta.Function.Name,
							Arguments: tcDelta.Function.Arguments,
						},
					})
					select {
					case streamCh <- livePreviewChunkMsg{Content: tcJSON, IsTool: true}:
					default:
					}
				}
				remaining := buf.String()[:idx] + rest[endIdx+len(sentinelTCLL):]
				buf.Reset()
				buf.WriteString(remaining)
			}

			// ── Content: send to live preview as it arrives ──
			//
			// This used to flush the entire remaining buf unconditionally,
			// every iteration. That's wrong whenever buf still holds an
			// opened-but-not-yet-closed \x00RSNG\x00/\x00TCLL\x00 marker —
			// e.g. the closing half of a sentinel pair hadn't streamed in
			// yet, or (before the openrouter.go Read() fix) a large chunk
			// got truncated mid-marker. In both cases the still-in-flight
			// reasoning/tool-call bytes, sentinel included, were dumped
			// straight into fullContent and shown as if they were the
			// model's answer — exactly the "thinking bleeding into the
			// response" symptom.
			//
			// flushSafeContent only releases the portion of buf that can't
			// possibly be the start of a marker still arriving, holding the
			// rest back until either its pair completes (handled by the two
			// loops above) or the stream truly ends (atEOF, below).
			if content := flushSafeContent(&buf, err == io.EOF); content != "" {
				fullContent.WriteString(content)
				select {
				case streamCh <- livePreviewChunkMsg{Content: content}:
				default:
				}
			}

			if err == io.EOF {
				break
			}
			if err != nil {
				tokIn, tokOut := streamUsage()
				select {
				case streamCh <- streamErrMsg{err: err, content: fullContent.String(), tokenInput: tokIn, tokenOutput: tokOut}:
				default:
				}
				return
			}
		}

		// ── FINAL DISPATCH (guaranteed by defer above) ──
		// Parse whatever was accumulated into patches. Handle truncation
		// (finish_reason == "length") gracefully by recovering partial
		// tool calls from the ToolCallBuffer.
		tokIn, tokOut := streamUsage()
		finishReason := ""
		if frp, ok := rawStream.(ai.FinishReasonProvider); ok {
			finishReason = frp.FinishReason()
		}
		var patches []*execution.Patch
		if len(toolCalls) > 0 {
			if m.toolCallBuffer != nil {
				if bufErr := m.toolCallBuffer.BufferAll(toolCalls); bufErr != nil {
					btokIn, btokOut := streamUsage()
					select {
					case streamCh <- buildFailedMsg{Err: fmt.Errorf("fast-track tool call buffer: %w", bufErr), TokenInput: btokIn, TokenOutput: btokOut}:
					default:
					}
					return
				}
				for _, tc := range m.toolCallBuffer.All() {
					if m.liveCodePreview != nil {
						m.liveCodePreview.AddOrUpdate(tc.Path, tc.Modified, tc.IsNew)
					}
				}
				for _, tc := range m.toolCallBuffer.All() {
					patches = append(patches, &execution.Patch{
						ID:            tc.ID,
						File:          tc.Path,
						Original:      tc.Original,
						Modified:      tc.Modified,
						TaskID:        m.currentBuildTaskID,
						IsFullRewrite: tc.IsNew,
					})
				}
			}
		}

		// ── FINAL OUTPUT ASSEMBLY (reasoning-token hygiene) ────────────
		// Reasoning/thinking text is STRIPPED from the output handed to
		// proposal extraction: prepending <thought> chain-of-thought text
		// previously leaked into extractBuildProposals (fake FILE:/fence
		// matches → garbage proposals) and inflated the reported completion
		// tokens while the visible code stayed tiny. VisibleCompletion strips
		// thinking blocks; reasoning is only used as the output when content
		// is entirely absent (models that emit their whole answer in
		// reasoning_content, e.g. Cohere North Mini).
		finalOutput := ai.VisibleCompletion(fullContent.String())
		if strings.TrimSpace(finalOutput) == "" && reasoningBuf.Len() > 0 {
			finalOutput = ai.VisibleCompletion(reasoningBuf.String())
		}
		// Dump the raw-vs-visible completion composition (content len vs
		// stripped reasoning len, provider tokens, truncation) for audit.
		debugLogCompletion(fullContent.String(), tokIn, tokOut, finishReason, "build.fasttrack")

		select {
		case streamCh <- buildProposalReadyMsg{
			Patches:     patches,
			Output:      finalOutput,
			TokenInput:  tokIn,
			TokenOutput: tokOut,
		}:
		default:
		}
	}()

	return tea.Batch(m.readStream(), m.smoothStreamTickCmd())
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
	m.push(roleStatus, "[HOTFIX] Generating patch (local short-circuit for simple modifications)...")
	m.push(roleSystem, fmt.Sprintf("  ⚙ Thinking... (Invoking %s)", m.activeRouteModel()))

	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "hotfix"
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.lastAgentActivity = time.Now()
	m.startShimmer("Applying hotfix...", "execute")

	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "hotfix"} },
		m.proposeHotfixPatch(&hotfixTask),
		m.smoothStreamTickCmd(),
		m.shimmerTickCmd(),
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
		"  ↺ Attempting local resolution for hotfix...",
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

// resolvePendingTodoTarget uses deterministic workspace file matching
// to resolve a pending TODO's target path. Never returns "workspace"
// or an empty string — callers must handle the empty case.
func (m *model) resolvePendingTodoTarget(todo string) string {
	if m.workspaceRoot == "" {
		return ""
	}
	resolver := workspace.NewTargetFileResolver(m.workspaceRoot)
	return resolver.Resolve(todo)
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
			return gateway.CanonicalizeFileName(m)
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
//
// Zero-token short-circuit (Path A): if the target file is explicitly referenced
// (e.g. @LICENSE) and the action is a simple text modification (rename/change/
// update/replace a string), execution.ApplyContextAwareFuzzyReplace is attempted locally
// first. On success the patch is returned immediately without any LLM call.
//
// Early abort (Path B): when the active provider is a cloud model, the raw
// LLM response is checked for diff markers (---, diff, <<<<<<<) before
// attempting expensive extraction. If the response lacks diff markers, the
// function aborts immediately and falls back to local fuzzy replacement or
// exits cleanly — ensuring a single $hot command never triggers multiple LLM
// API calls.
func (m *model) proposeHotfixPatch(task *plan.Task) tea.Cmd {
	return func() (msg tea.Msg) {
		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// The terminal hotfixProposalMsg MUST reach the TUI event loop on ANY
		// exit path — success, error, or panic — so the "Applying hotfix..."
		// spinner can never be orphaned while the patch is generated.
		defer func() {
			if r := recover(); r != nil {
				msg = hotfixProposalMsg{Err: fmt.Errorf("hotfix patch generation panic: %v", r)}
			}
		}()

		// ── Read existing file content ──────────────────────────
		// Without the original content, local fuzzy replacement and
		// LLM-based diff computation both produce incorrect results.
		var orig string
		if data, rerr := os.ReadFile(task.Target); rerr == nil {
			orig = string(data)
		}

		// ── PATH A: Deterministic Local Short-Circuit (0 Tokens) ──
		// If the target file is explicitly referenced with @ syntax
		// and the action is a simple text modification, attempt local
		// resolution first. On success, bypass the LLM entirely.
		if orig != "" && isHotfixLocalCandidate(task.Description, task.Target) {
			if modified, ok := execution.ApplyContextAwareFuzzyReplace(orig, task.Description, task.Target); ok {
				diffContent := computeUnifiedDiff(task.Target, orig, modified)
				patch := &execution.Patch{
					ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
					File:          task.Target,
					Original:      orig,
					Modified:      modified,
					TaskID:        task.StepNum,
					ContextID:     m.sess.ContextID,
					IsFullRewrite: true,
				}
				return hotfixProposalMsg{
					Task:  task,
					Patch: patch,
					Diff:  diffContent,
				}
			}
		}

		if m.provider == nil {
			return hotfixProposalMsg{Err: fmt.Errorf("build execution error: no provider configured")}
		}

		// Build a focused, non-chat patch-generation prompt with full file
		// context using the appropriate strategy for the file state.
		strategy := execution.StrategyForOriginal(orig)
		var handoff string
		switch strategy {
		case execution.STRATEGY_NEW_FILE:
			if orig == "" {
				handoff = buildNewFileHandoff(task)
			} else {
				handoff = buildStubOverwriteHandoff(task, orig)
			}
		default:
			handoff = ctxpkg.SanitizeBuildHandoff(task, "")
			handoff += "\n\n### TARGET_FILE_CONTENT\n```\n" + orig + "\n```\n"
			handoff += "\nModify the above file content to fulfill the task. "
			handoff += "Output a unified diff (--- a/ ... +++ b/ ...) or a SEARCH/REPLACE block (<<<<<<< SEARCH). "
			handoff += "Do NOT rewrite the entire file. "
			handoff += "Return ONLY the SEARCH/REPLACE block or unified diff."
		}
		system := m.tieredStrategyContract(strategy.SystemPromptKey())

		cloudCfg := gateway.ClassifyCloudProvider(m.cfg.ActiveProviderName())
		isCloud := cloudCfg.CloudProvider != ""

		stop := []string{">>>>>>>"}
		if strategy == execution.STRATEGY_NEW_FILE {
			stop = []string{"```\n\n"}
		}

		req := ai.Request{
			Model:     m.activeRouteModel(),
			System:    system,
			Stream:    false,
			MaxTokens: 2048,
			Stop:      stop,
			Messages:  []ai.Message{{Role: "user", Content: handoff}},
			Tools:     m.fileMutationTools(),
			Reasoning: m.effortFromTasks(),
		}

		ctx, cancel := context.WithTimeout(context.Background(), buildGenerationTimeout)
		resp, err := m.provider.Execute(ctx, req)
		cancel()
		if err != nil {
			if orig != "" {
				if modified, ok := execution.ApplyFuzzyStringReplace(orig, task.Description, task.Target); ok {
					diffContent := computeUnifiedDiff(task.Target, orig, modified)
					patch := &execution.Patch{
						ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
						File:          task.Target,
						Original:      orig,
						Modified:      modified,
						TaskID:        task.StepNum,
						ContextID:     m.sess.ContextID,
						IsFullRewrite: true,
					}
					return hotfixProposalMsg{
						Task:  task,
						Patch: patch,
						Diff:  diffContent,
					}
				}
			}
			return hotfixProposalMsg{Err: fmt.Errorf("patch generation failed: %w", err)}
		}

		// ── NATIVE TOOL CALLING PATH (BUFFERED, REQUIRES APPROVAL) ──
		// Tool calls are now intercepted in memory and presented for human
		// approval before any disk mutation occurs.
		if len(resp.ToolCalls) > 0 {
			if err := m.toolCallBuffer.BufferAll(resp.ToolCalls); err != nil {
				return hotfixProposalMsg{Err: fmt.Errorf("tool call buffer: %w", err)}
			}
			// Feed live preview for each buffered call
			for _, tc := range m.toolCallBuffer.All() {
				if m.liveCodePreview != nil {
					m.liveCodePreview.AddOrUpdate(tc.Path, tc.Modified, tc.IsNew)
				}
			}
			// Don't apply yet — stay in hotfix flow with pending buffer
			// The main handler will transition to StateAwaitingApproval
			// This is a hot fix path, so we trigger the approval gate
			return hotfixProposalMsg{
				Err: fmt.Errorf("tool calls buffered for approval"),
			}
		}

		// ── FALLBACK: No tool calls — use existing markdown extraction ──
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			if isCloud {
				m.push(roleSystem, infoStyle.Render("[HOTFIX] Cloud model returned empty output. Aborting LLM attempt, trying local fallback..."))
			}
			if orig != "" {
				if modified, ok := execution.ApplyFuzzyStringReplace(orig, task.Description, task.Target); ok {
					diffContent := computeUnifiedDiff(task.Target, orig, modified)
					patch := &execution.Patch{
						ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
						File:          task.Target,
						Original:      orig,
						Modified:      modified,
						TaskID:        task.StepNum,
						ContextID:     m.sess.ContextID,
						IsFullRewrite: true,
					}
					return hotfixProposalMsg{
						Task:  task,
						Patch: patch,
						Diff:  diffContent,
					}
				}
			}
			return hotfixProposalMsg{Err: fmt.Errorf("patch generation returned empty output")}
		}

		rawContent := resp.Content

		if isCloud && !hasDiffMarkerPrefix(rawContent) && orig != "" {
			m.push(roleSystem, infoStyle.Render("[HOTFIX] Cloud model output lacks diff markers. Aborting early, trying local fallback..."))
			if orig != "" {
				if modified, ok := execution.ApplyFuzzyStringReplace(orig, task.Description, task.Target); ok {
					diffContent := computeUnifiedDiff(task.Target, orig, modified)
					patch := &execution.Patch{
						ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
						File:          task.Target,
						Original:      orig,
						Modified:      modified,
						TaskID:        task.StepNum,
						ContextID:     m.sess.ContextID,
						IsFullRewrite: true,
					}
					return hotfixProposalMsg{
						Task:  task,
						Patch: patch,
						Diff:  diffContent,
					}
				}
			}
			return hotfixProposalMsg{Err: fmt.Errorf("patch generation: cloud model produced output without diff markers and local fallback also failed for %s", task.Target)}
		}

		var resolved string
		var diffContent string

		switch strategy {
		case execution.STRATEGY_NEW_FILE:
			if extracted, ok := execution.ExtractRawCodeBlock(rawContent); ok {
				resolved = execution.SanitizeRawCodeBlock(extracted)
			} else if extracted, ok := execution.ExtractCodeBlockContent(rawContent); ok {
				resolved = execution.SanitizeRawCodeBlock(extracted)
			} else {
				resolved = execution.SanitizeRawCodeBlock(rawContent)
			}
			diffContent = computeUnifiedDiff(task.Target, orig, resolved)

		default:
			resolved, diffFound := execution.ExtractDiffFromLLMOutput(rawContent, orig, task.Description)
			if diffFound {
				diffContent = computeUnifiedDiff(task.Target, orig, resolved)
			} else {
				resolved = execution.ResolveModifiedContent(orig, rawContent)
				if resolved == orig && orig != "" {
					if isCloud {
						m.push(roleSystem, infoStyle.Render("[HOTFIX] Cloud model structural breakdown. Trying local fallback..."))
						if modified, ok := execution.ApplyFuzzyStringReplace(orig, task.Description, task.Target); ok {
							diffContent = computeUnifiedDiff(task.Target, orig, modified)
							patch := &execution.Patch{
								ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
								File:          task.Target,
								Original:      orig,
								Modified:      modified,
								TaskID:        task.StepNum,
								ContextID:     m.sess.ContextID,
								IsFullRewrite: true,
							}
							return hotfixProposalMsg{
								Task:  task,
								Patch: patch,
								Diff:  diffContent,
							}
						}
						return hotfixProposalMsg{Err: fmt.Errorf("patch generation: cloud model returned unparseable output and local fallback also failed for %s", task.Target)}
					}
					return hotfixProposalMsg{Err: fmt.Errorf("patch generation: no valid diff or search/replace block found in LLM output for %s", task.Target)}
				}
				diffContent = computeUnifiedDiff(task.Target, orig, resolved)
			}
		}

		cleaned := sanitizeFileOutput(rawContent)

		patch := &execution.Patch{
			ID:            fmt.Sprintf("hotfix-%d", task.StepNum),
			File:          task.Target,
			Original:      orig,
			Modified:      cleaned,
			TaskID:        task.StepNum,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: strategy == execution.STRATEGY_NEW_FILE,
		}

		return hotfixProposalMsg{
			Task:        task,
			Patch:       patch,
			Diff:        diffContent,
			TokenInput:  resp.TokenInput,
			TokenOutput: resp.TokenOutput,
		}
	}
}

// isHotfixLocalCandidate checks whether a $hot task qualifies for
// zero-token local resolution: the target file must be explicitly
// referenced with @ syntax (e.g. @LICENSE) and the description
// must contain a simple-text-modification verb (rename, change,
// update, replace, fix, etc.).
func isHotfixLocalCandidate(description, target string) bool {
	if target == "" || description == "" {
		return false
	}
	// Require an explicit @file reference so we never apply a
	// local heuristic to an ambiguous prompt. Match @basename
	// (e.g. @LICENSE) and also @path/target for explicit paths.
	targetBase := filepath.Base(target)
	hasExplicitRef := strings.Contains(description, "@"+targetBase) ||
		strings.Contains(description, "@"+target)
	if !hasExplicitRef {
		return false
	}
	lower := strings.ToLower(description)
	simpleMutationSignals := []string{
		"rename", "change", "update", "replace", "with ", "to ",
		"fix typo", "fix spelling", "fix grammar",
		"capitalize", "lowercase", "uppercase",
		"bump version", "remove ", "delete ", "strip ",
	}
	for _, sig := range simpleMutationSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// hasDiffMarkerPrefix reports whether the raw LLM output starts
// with a recognizable diff marker (--- a/, diff, <<<<<<< SEARCH)
// after trimming leading whitespace. Used as an early-abort guard
// for cloud providers so that responses clearly lacking diff
// structure are not subjected to expensive extraction logic.
func hasDiffMarkerPrefix(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.HasPrefix(trimmed, "---") ||
		strings.HasPrefix(trimmed, "diff") ||
		strings.HasPrefix(trimmed, "<<<<<<<")
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

		// ── Activity Tree: log stdlib fix read ──────────────────────────
		if m.activityTree != nil {
			m.activityTree.Append(NewFileReadEvent(task.Target, 0, 0))
		}

		// Read actual file and compute deterministic fix.
		orig, modified, err := retrieval.ApplyStdlibCaseFix(task.Target, symbol, pkgName, importPath)
		if err != nil {
			if m.activityTree != nil {
				m.activityTree.Append(NewFileMutateEvent(task.Target, 0, 0, 0))
			}
			return buildProposalReadyMsg{Err: fmt.Errorf("stdlib fix failed for %s: %w", task.Target, err)}
		}

		diff := computeUnifiedDiff(task.Target, orig, modified)

		// ── Activity Tree: log stdlib patch with real diff metrics ─────
		if m.activityTree != nil {
			added, removed := countLinesDelta(diff)
			m.activityTree.Append(NewFileMutateEvent(task.Target, added, removed, 0))
		}

		patch := &execution.Patch{
			ID:            fmt.Sprintf("stdlib-%d", task.StepNum),
			File:          task.Target,
			Original:      orig,
			Modified:      modified,
			TaskID:        task.StepNum,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: true,
		}

		return buildProposalReadyMsg{
			Task:   task,
			Patch:  patch,
			Diff:   diff,
			Output: fmt.Sprintf("Applied stdlib case-correction: %q -> %q + import %q in %s", symbol, pkgName, importPath, task.Target),
		}
	}
}

// buildNewFileHandoff renders a clean handoff for creating a brand-new file.
// It deliberately does NOT include SanitizeBuildHandoff's unified-diff /
// SEARCH/REPLACE instructions: a weak model (e.g. gemma-4-26b-a4b) follows
// the first emphasized format directive and emits a diff against a file that
// does not exist, producing a (+0 / -0 lines) no-op patch. New-file tasks get
// a single, unambiguous full-creation contract.
func buildNewFileHandoff(task *plan.Task) string {
	if task == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## BUILD HANDOFF — NEW FILE CREATION\n\n")
	b.WriteString("Execute ONLY the following task. The target file does NOT exist yet — CREATE it.\n")
	b.WriteString("Do NOT restate the plan, do NOT explain, do NOT list other tasks.\n\n")
	b.WriteString("### TARGET FILE\n")
	b.WriteString(task.Target + "\n")
	if lang := ctxpkg.LanguageFromExtension(task.Target); lang != "" {
		b.WriteString("### EXPECTED LANGUAGE\n")
		b.WriteString(lang + "\n")
	}
	b.WriteString("### TASK\n")
	b.WriteString(string(task.Type) + ": " + task.Target)
	if task.Description != "" {
		b.WriteString(" — " + task.Description)
	}
	b.WriteString("\n\n")
	b.WriteString("### INSTRUCTION\n")
	b.WriteString("Output the COMPLETE file content inside a single markdown code block ")
	b.WriteString("with the appropriate language tag (e.g. ```css, ```javascript, ```html, ```python, ```go). ")
	b.WriteString("Do NOT use SEARCH/REPLACE blocks (<<<<<<< SEARCH) or unified diffs (--- a/ +++ b/) — the file does not exist yet. ")
	b.WriteString("Do NOT use FILE_CREATE markers. ")
	b.WriteString("Output ONLY the code block. No conversational text, no explanations, no greetings.\n\n")
	fmt.Fprintf(&b, "CURRENT_YEAR: %d\n", time.Now().Year())
	return strings.TrimSpace(b.String())
}

// buildStubOverwriteHandoff renders the full-file overwrite handoff for an
// EXISTING stub file (under SmallFileLineThreshold lines) whose content is
// passed in context. It demands a COMPLETE, fully-implemented rewrite inside a
// single markdown code block and forbids SEARCH/REPLACE / unified diff — a
// small model diffing a stub either fails with "ambiguous snippet without
// SEARCH/REPLACE markers" or echoes the skeleton back unchanged.
func buildStubOverwriteHandoff(task *plan.Task, orig string) string {
	if task == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## BUILD HANDOFF — FULL FILE OVERWRITE\n\n")
	b.WriteString("Execute ONLY the following task. The target file is an incomplete stub — output its COMPLETE, FULLY IMPLEMENTED content.\n")
	b.WriteString("Do NOT restate the plan, do NOT explain, do NOT list other tasks.\n\n")
	b.WriteString("### TARGET FILE\n")
	b.WriteString(task.Target + "\n")
	if lang := ctxpkg.LanguageFromExtension(task.Target); lang != "" {
		b.WriteString("### EXPECTED LANGUAGE\n")
		b.WriteString(lang + "\n")
	}
	b.WriteString("### TASK\n")
	b.WriteString(string(task.Type) + ": " + task.Target)
	if task.Description != "" {
		b.WriteString(" — " + task.Description)
	}
	b.WriteString("\n\n")
	b.WriteString("### CURRENT FILE CONTENT (incomplete skeleton)\n```\n" + orig + "\n```\n\n")
	b.WriteString("### INSTRUCTION\n")
	b.WriteString("The current file is an incomplete skeleton. Fully implement and expand all functions, styles, and markup. Do NOT repeat incomplete stubs. ")
	b.WriteString("Output the COMPLETE, FULLY IMPLEMENTED file content inside a single markdown code block ")
	b.WriteString("with the appropriate language tag (e.g. ```css, ```javascript, ```html, ```python, ```go). ")
	b.WriteString("Do NOT use SEARCH/REPLACE blocks (<<<<<<< SEARCH) or unified diffs (--- a/ +++ b/). ")
	b.WriteString("Do NOT use FILE_CREATE markers. ")
	b.WriteString("Output ONLY the code block. No conversational text, no explanations, no greetings.\n\n")
	fmt.Fprintf(&b, "CURRENT_YEAR: %d\n", time.Now().Year())
	return strings.TrimSpace(b.String())
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
//
// Cloud-model robustness: when the active provider is a cloud model
// (OpenRouter / Cohere / etc.), the function uses ExtractDiffFromLLMOutput
// to recover unified diff blocks from conversational markdown, and falls
// back to fuzzy string replacement when strict parsing fails. On structural
// breakdown (attempt 1), a warning is emitted and the prompt is corrected
// with an explicit diff format demonstration to prevent token burn on
// repetitive empty retries.
func (m *model) proposeBuildPatch(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		if m.provider == nil {
			return buildProposalReadyMsg{Err: fmt.Errorf("build execution error: no provider configured")}
		}

		// ── Read fresh file content from disk ──────────────────────────
		// NON-ZERO FILE READ ENFORCEMENT: if the file exists on disk with
		// a positive size but readFileContent returns empty, abort patch
		// generation immediately. An empty read for an existing non-empty
		// file indicates a stale or locked file — passing empty context to
		// the LLM would silently overwrite the real content with a patch
		// trained on nothing.
		readFileContent := func() (string, error) {
			fi, serr := os.Stat(task.Target)
			if serr == nil && fi.Size() > 0 {
				data, rerr := os.ReadFile(task.Target)
				if rerr != nil {
					return "", fmt.Errorf("read %s: %w", task.Target, rerr)
				}
				if len(data) == 0 {
					return "", fmt.Errorf("zero-content read on non-empty file %s (%d bytes on disk) — aborting to prevent silent data loss", task.Target, fi.Size())
				}
				return string(data), nil
			}
			if serr == nil {
				// File exists but is genuinely empty (new file creation).
				return "", nil
			}
			// File does not exist on disk — new file creation.
			return "", nil
		}

		orig, rerr := readFileContent()
		if rerr != nil {
			if m.activityTree != nil {
				m.activityTree.Append(NewFileReadEvent(task.Target, 0, 0))
			}
			return buildProposalReadyMsg{Err: rerr}
		}

		// ── Determine strategy based on file state ─────────────────────
		strategy := execution.StrategyForOriginal(orig)

		// ── Build the handoff with the current file content ────────────
		buildHandoff := func(content string) string {
			switch strategy {
			case execution.STRATEGY_NEW_FILE:
				if content == "" {
					// Clean full-creation contract: no diff instructions at all.
					return buildNewFileHandoff(task)
				}
				// Existing stub (< SmallFileLineThreshold lines): whole-file
				// overwrite with the skeleton in context so the model expands
				// it instead of echoing it back.
				return buildStubOverwriteHandoff(task, content)
			default:
				h := ctxpkg.SanitizeBuildHandoff(task, "")
				h += "\n\n### TARGET_FILE_CONTENT\n```\n" + content + "\n```\n"
				h += "\nModify the above file content to fulfill the task. "
				h += "Output a unified diff (--- a/ ... +++ b/ ...) or a SEARCH/REPLACE block (<<<<<<< SEARCH). "
				h += "Do NOT rewrite the entire file. "
				h += "Return ONLY the SEARCH/REPLACE block or unified diff. No explanatory text."
				return h
			}
		}

		handoff := buildHandoff(orig)

		// Select system prompt based on strategy (tier-adapted).
		system := m.tieredStrategyContract(strategy.SystemPromptKey())

		cloudCfg := gateway.ClassifyCloudProvider(m.cfg.ActiveProviderName())
		isCloud := cloudCfg.CloudProvider != ""

		maxRetries := 2

		// ── Activity Tree: log file read ──────────────────────────────
		if m.activityTree != nil {
			m.activityTree.Append(NewFileReadEvent(task.Target, 0, 0))
		}

		for attempt := 0; attempt <= maxRetries; attempt++ {
			// On retry: re-read the file from disk in case it changed
			// (e.g. a previous retry attempt wrote partial content or
			// another task modified it).
			if attempt > 0 {
				orig, rerr = readFileContent()
				if rerr != nil {
					return buildProposalReadyMsg{Err: rerr}
				}
				handoff = buildHandoff(orig)
			}

			// On retry for existing small files, switch to new-file strategy
			// (full content overwrite) to avoid repeated diff failures.
			currentStrategy := strategy
			if attempt > 0 && orig != "" && execution.IsSmallFile(orig) {
				currentStrategy = execution.STRATEGY_NEW_FILE
				handoff = buildStubOverwriteHandoff(task, orig) + "\n\nCORRECTION: Previous patch attempts failed. Output the COMPLETE new file content inside a single markdown code block. Do NOT use SEARCH/REPLACE or diff format."
				system = m.tieredStrategyContract("new_file")

				// ── Activity Tree: log retry ───────────────────────────
				if m.activityTree != nil {
					m.activityTree.Append(NewFileMutateEvent(task.Target, 0, 0, 0))
				}
			} else if attempt > 0 {
				// ── Activity Tree: log retry ───────────────────────────
				if m.activityTree != nil {
					m.activityTree.Append(NewFileReadEvent(task.Target, 0, 0))
				}
			}

			stop := []string{">>>>>>>", "```\n\n"}
			if currentStrategy == execution.STRATEGY_NEW_FILE {
				stop = []string{"```\n\n"}
			}
			req := ai.Request{
				Model:     m.activeRouteModel(),
				System:    system,
				Stream:    false,
				MaxTokens: 2048,
				Stop:      stop,
				Messages:  []ai.Message{{Role: "user", Content: handoff}},
				Tools:     m.fileMutationTools(),
				Reasoning: m.effortFromTasks(),
			}

			ctx, cancel := context.WithTimeout(context.Background(), buildGenerationTimeout)
			resp, err := m.provider.Execute(ctx, req)
			cancel()
			if err != nil {
				return buildProposalReadyMsg{Err: fmt.Errorf("patch generation failed: %w", err)}
			}

			// ── NATIVE TOOL CALLING PATH (BUFFERED, REQUIRES APPROVAL) ──
			// Tool calls are intercepted in memory and presented as Diffs
			// for human authorization. Disk mutation only occurs on approval.
			if len(resp.ToolCalls) > 0 {
				if err := m.toolCallBuffer.BufferAll(resp.ToolCalls); err != nil {
					return buildProposalReadyMsg{Err: fmt.Errorf("tool call buffer: %w", err)}
				}
				// Feed live preview for each buffered call
				for _, tc := range m.toolCallBuffer.All() {
					if m.liveCodePreview != nil {
						m.liveCodePreview.AddOrUpdate(tc.Path, tc.Modified, tc.IsNew)
					}
				}
				// Return a special message indicating we're awaiting approval
				return buildProposalReadyMsg{
					Err: fmt.Errorf("tool calls buffered for approval"),
				}
			}

			if resp == nil || strings.TrimSpace(resp.Content) == "" {
				if attempt == 0 && isCloud {
					m.push(roleSystem, fmt.Sprintf(
						warningStyle.Render("[BUILD WARNING] %s returned empty output on attempt %d. "+
							"The model may be wrapping the diff in markdown or conversational text. "+
							"Retrying with explicit format instructions."),
						m.activeRouteModel(), attempt+1))
				}
				if attempt < maxRetries {
					// Strategy-aware correction: a new-file task must be told to
					// emit complete content, never a diff against a non-existent
					// file (which makes SLMs emit (+0 / -0 lines) no-op patches).
					correction := ""
					if currentStrategy == execution.STRATEGY_NEW_FILE {
						correction = "CORRECTION: Your previous response was empty or unparseable. The target file does not exist yet. Output the COMPLETE file content inside a single markdown code block with the appropriate language tag. Do NOT use SEARCH/REPLACE or unified diff."
					} else {
						correction = fmt.Sprintf(
							"CORRECTION: Your previous response was empty or unparseable. The expected output is a raw unified diff block like:\n\n--- a/%s\n+++ b/%s\n@@ -1,3 +1,3 @@\n line1\n-line-old\n+line-new\n\nReturn ONLY that diff. No text before or after.",
							task.Target, task.Target)
					}
					handoff = buildHandoff(orig) + "\n\n" + correction
					continue
				}
				return buildProposalReadyMsg{Err: fmt.Errorf("patch generation returned empty output")}
			}

			rawContent := resp.Content
			var resolved string
			var diffContent string

			switch currentStrategy {
			case execution.STRATEGY_NEW_FILE:
				// New/0-byte file: NO diff markers are ever required. The
				// extractor accepts any path-tagged block (```lang:path,
				// ```lang path, ```file=path, === FILE:), any fenced code
				// block, or the raw text as the complete file content — a
				// new file has no old content to diff against.
				newContent, newOK := execution.ExtractNewFileContent(rawContent, task.Target)
				if !newOK {
					if attempt < maxRetries {
						continue
					}
					return buildProposalReadyMsg{Err: fmt.Errorf("patch generation: no file content extracted for new file %s after %d attempts", task.Target, attempt+1)}
				}
				resolved = newContent
				diffContent = computeUnifiedDiff(task.Target, orig, resolved)

			default:
				// Existing file: try robust diff extraction.
				resolved, diffFound := execution.ExtractDiffFromLLMOutput(rawContent, orig, task.Description)
				if diffFound {
					diffContent = computeUnifiedDiff(task.Target, orig, resolved)
				} else {
					resolved = execution.ResolveModifiedContent(orig, rawContent)
					if resolved == orig && orig != "" {
						// ── ZERO-PATCH SHORT-CIRCUIT ─────────────────────
						// The per-task mutation step produced 0 lines changed
						// (+0 / -0): the model's output resolves back to the
						// file's current content. This happens when fast-track
						// (or an earlier task) already applied the requested
						// modification. Complete the step gracefully instead of
						// re-looping the LLM, which would otherwise hang the
						// "Generating patch..." spinner for up to 3 ×
						// buildGenerationTimeout (Rule "Human-Centered /
						// Reversible": a step producing 0 patches MUST never
						// leave the UI spinner hanging).
						if m.activityTree != nil {
							m.activityTree.Append(NewFileMutateEvent(task.Target, 0, 0, 0))
						}
						return mutationResultMsg{file: task.Target, status: "nochange"}
					}
					diffContent = computeUnifiedDiff(task.Target, orig, resolved)
				}

				// Validate patch format for existing files.
				cleaned := sanitizeFileOutput(rawContent)
				if execution.IsAmbiguousSnippet(orig, cleaned) {
					if attempt < maxRetries {
						handoff = fmt.Sprintf(
							"Your proposed patch for %s was rejected due to invalid format: Ambiguous snippet without SEARCH/REPLACE markers. Re-send the modification using strict <<<<<<< SEARCH ... ======= ... >>>>>>> blocks.\n\nOriginal task:\n%s",
							task.Target, buildHandoff(orig))
						continue
					}
					return buildProposalReadyMsg{Err: fmt.Errorf("%w: ambiguous snippet without SEARCH/REPLACE markers for existing file %s — retry with SEARCH/REPLACE block or unified diff", execution.ErrInvalidPatchFormat, task.Target)}
				}
			}

			// ── DESTRUCTION GUARDRAIL ────────────────────────────────────
			// Only reject patches that delete content with ZERO additions
			// and leave the file empty or near-empty (≤ 5 remaining lines
			// and > 90% deleted). This allows legitimate deduplication edits
			// (e.g. removing 132/162 lines) and full-rewrite fallbacks while
			// still catching actual file wipes.
			// On the final attempt, falls through to full-rewrite retry.
			if diffContent != "" && orig != "" {
				origLineCount := len(strings.Split(orig, "\n"))
				if origLineCount > 0 {
					added, removed := countLinesDelta(diffContent)
					finalLineCount := origLineCount - removed + added

					if added == 0 {
						isEmpty := finalLineCount <= 0
						isNearWipe := origLineCount > 0 && float64(removed)/float64(origLineCount) > 0.9 && finalLineCount < 5

						if isEmpty || isNearWipe {
							if attempt < maxRetries {
								handoff = fmt.Sprintf(
									"ERROR: Proposed patch deletes entire file content without replacements. Preserve existing structure and remove ONLY duplicate blocks.\n\nOriginal task:\n%s",
									buildHandoff(orig))
								continue
							}
							// Last attempt: fall through to full-rewrite retry below.
							break
						}
					}
				}
			}

			// ── V3 ARTIFACT GATE (protocol-centric) ─────────────────────
			// Normalize + validate the resolved content inside the execution
			// engine before any proposal is surfaced for approval. A syntax
			// failure triggers the configured failure policy: reprompt with
			// the parser diagnostics while the retry budget allows, abort
			// once it is exhausted. The reasoning-leak observer consumes the
			// raw LLM output asynchronously and never blocks this path.
			if m.execEng != nil && m.execEng.Artifact != nil {
				m.execEng.Artifact.InspectReasoning(rawContent)
				gate := m.execEng.Artifact.ValidateContent(task.Target, []byte(resolved), attempt)
				if !gate.Passed {
					if gate.Decision == policy.DecisionRetry && attempt < maxRetries {
						handoff = buildHandoff(orig) + "\n\nCORRECTION: " + gate.Directive
						continue
					}
					return buildProposalReadyMsg{Err: fmt.Errorf("artifact validation rejected %s: %w", task.Target, gate.Error)}
				}
				if string(gate.Normalized) != resolved {
					// The canonical normalized bytes differ from the model
					// output (CRLF/BOM/trailing-newline). Propose the
					// canonical form so disk always receives protocol bytes.
					resolved = string(gate.Normalized)
					diffContent = computeUnifiedDiff(task.Target, orig, resolved)
				}
			}

			patch := &execution.Patch{
				ID:            fmt.Sprintf("build-%d", task.StepNum),
				File:          task.Target,
				Original:      orig,
				Modified:      resolved,
				TaskID:        task.StepNum,
				ContextID:     m.sess.ContextID,
				IsFullRewrite: currentStrategy == execution.STRATEGY_NEW_FILE,
			}

			// ── Activity Tree: mark read done, log patch ────────────────
			if m.activityTree != nil {
				added, removed := countLinesDelta(diffContent)
				m.activityTree.Append(NewFileMutateEvent(task.Target, added, removed, 0))
			}

			return buildProposalReadyMsg{
				Task:        task,
				Patch:       patch,
				Diff:        diffContent,
				Output:      rawContent,
				TokenInput:  resp.TokenInput,
				TokenOutput: resp.TokenOutput,
			}
		}

		// ── Activity Tree: mark failure on exhaustion ──────────────────
		if m.activityTree != nil {
			m.activityTree.Append(NewFileMutateEvent(task.Target, 0, 0, 0))
		}

		// ── FINAL FULL-FILE REWRITE RETRY ─────────────────────────────
		// All structured patch strategies (unified diff, SEARCH/REPLACE) have
		// failed after maxRetries attempts. Try one final time asking the LLM
		// to output the ENTIRE file content in a clean codeblock. This handles
		// small models (7B) that struggle with precise patch formatting.
		if orig != "" {
			fullRewriteHandoff := fmt.Sprintf(
				"%s\n\nALL PRIOR PATCH ATTEMPTS FAILED. Output the COMPLETE updated file content inside a SINGLE markdown code block. Do NOT use SEARCH/REPLACE, unified diff, or FILE_CREATE markers. Return the entire file — nothing less.",
				buildHandoff(orig))
			fullRewriteReq := ai.Request{
				Model:     m.activeRouteModel(),
				System:    m.tieredStrategyContract("small_fallback"),
				Stream:    false,
				MaxTokens: 4096,
				Stop:      []string{"```\n\n"},
				Messages:  []ai.Message{{Role: "user", Content: fullRewriteHandoff}},
				Reasoning: m.effortFromTasks(),
			}
			ctx, cancel := context.WithTimeout(context.Background(), buildGenerationTimeout)
			fullResp, fullErr := m.provider.Execute(ctx, fullRewriteReq)
			cancel()
			if fullErr == nil && fullResp != nil && strings.TrimSpace(fullResp.Content) != "" {
				var fullResolved string
				if extracted, ok := execution.ExtractRawCodeBlock(fullResp.Content); ok {
					fullResolved = execution.SanitizeRawCodeBlock(extracted)
				} else if extracted, ok := execution.ExtractCodeBlockContent(fullResp.Content); ok {
					fullResolved = execution.SanitizeRawCodeBlock(extracted)
				} else {
					fullResolved = execution.SanitizeRawCodeBlock(fullResp.Content)
				}
				if strings.TrimSpace(fullResolved) != "" {
					// V3 artifact gate on the last-resort rewrite: the
					// normalized canonical bytes are proposed so the disk
					// always receives protocol bytes.
					if m.execEng != nil && m.execEng.Artifact != nil {
						m.execEng.Artifact.InspectReasoning(fullResp.Content)
						gate := m.execEng.Artifact.ValidateContent(task.Target, []byte(fullResolved), maxRetries)
						if !gate.Passed {
							return buildProposalReadyMsg{Err: fmt.Errorf("artifact validation rejected %s: %w", task.Target, gate.Error)}
						}
						fullResolved = string(gate.Normalized)
					}
					fullDiff := computeUnifiedDiff(task.Target, orig, fullResolved)
					if m.activityTree != nil {
						added, removed := countLinesDelta(fullDiff)
						m.activityTree.Append(NewFileMutateEvent(task.Target, added, removed, 0))
					}
					return buildProposalReadyMsg{
						Task: task,
						Patch: &execution.Patch{
							ID:            fmt.Sprintf("build-%d", task.StepNum),
							File:          task.Target,
							Original:      orig,
							Modified:      fullResolved,
							TaskID:        task.StepNum,
							ContextID:     m.sess.ContextID,
							IsFullRewrite: true,
						},
						Diff:   fullDiff,
						Output: fullResp.Content,
					}
				}
			}
		}

		return buildProposalReadyMsg{Err: fmt.Errorf("patch generation failed after %d retries", maxRetries)}
	}
}

// applyTrivialTemplate bypasses the approval gate entirely for known
// static templates (LICENSE, .gitignore, .env). It generates the content
// deterministically from Go templates, applies it immediately, and returns
// a buildResultMsg — completing in < 50ms with zero LLM calls.
func (m *model) applyTrivialTemplate(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		canonicalTarget := gateway.CanonicalizeFileName(task.Target)
		var orig string
		if data, rerr := os.ReadFile(canonicalTarget); rerr == nil {
			orig = string(data)
		}

		description := task.Description
		content := generateTrivialContent(canonicalTarget, description)
		cleaned := execution.SanitizeLLMResponse(content)

		patch := &execution.Patch{
			ID:            fmt.Sprintf("template-%d", task.StepNum),
			File:          canonicalTarget,
			Original:      orig,
			Modified:      cleaned,
			TaskID:        task.StepNum,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: true,
		}

		if orig == cleaned {
			return buildResultMsg{
				output:   fmt.Sprintf("File %s is already up to date (trivial template).", canonicalTarget),
				exitCode: 0,
			}
		}

		if m.execEng != nil && m.execEng.Patches != nil {
			if err := m.execEng.Patches.Apply(patch); err != nil {
				return buildResultMsg{
					output:   "",
					exitCode: 1,
					err:      fmt.Errorf("trivial template apply failed: %w", err),
				}
			}
		}

		return buildResultMsg{
			output:   fmt.Sprintf("Created %s from template (%d lines).", canonicalTarget, len(strings.Split(cleaned, "\n"))),
			exitCode: 0,
		}
	}
}

// generateTrivialContent generates the file content for a trivial template
// file (LICENSE, .gitignore, .env) from the user's description string.
// Returns empty string if the target is not recognized.
func generateTrivialContent(target, description string) string {
	base := strings.ToLower(target)
	base = strings.TrimSuffix(base, ".md")

	switch base {
	case "license", "licence":
		licenseType := detectLicenseType(description)
		content, ok := templates.RenderLicense(licenseType, description)
		if !ok {
			return ""
		}
		return content
	case ".gitignore", "gitignore":
		return generateGitignore()
	case ".env", "env", ".env.example", "env.example":
		return generateEnv()
	default:
		return ""
	}
}

func detectLicenseType(description string) string {
	lower := strings.ToLower(description)
	switch {
	case strings.Contains(lower, "apache"):
		return "apache-2.0"
	case strings.Contains(lower, "gpl"):
		return "gpl-3.0"
	case strings.Contains(lower, "bsd"):
		return "bsd-3-clause"
	default:
		return "mit"
	}
}

// generateGitignore returns a basic Go .gitignore template.
func generateGitignore() string {
	return `# Dependencies
vendor/

# Build output
bin/
dist/
build/
*.exe
*.dll
*.so
*.dylib

# Go
*.test
*.out
*.prof
*.test.exe
go.work

# IDE
.idea/
.vscode/
*.swp
*.swo
*~

# OS
.DS_Store
Thumbs.db

# Environment
.env
.env.local
.env.*.local
`
}

// generateEnv returns a basic .env template.
func generateEnv() string {
	return `# Application
APP_ENV=development
APP_DEBUG=true
APP_PORT=8080

# Database
DB_HOST=localhost
DB_PORT=5432
DB_NAME=myapp
DB_USER=user
DB_PASSWORD=

# API Keys (fill in your own)
API_KEY=
SECRET_KEY=
`
}

func isYear(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n >= 1900 && n <= 2099
}

var customizationDirectivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:author|by|copyright|holder|organization)\b`),
	regexp.MustCompile(`\b(19[0-9][0-9]|20[0-9][0-9])\b`),
	regexp.MustCompile(`['"][^'"]+['"]`),
	regexp.MustCompile(`(?i)\brefactor|convert|replace|change|update|modify\b`),
}

func hasCustomizationDirectives(description string) bool {
	if description == "" {
		return false
	}
	lower := strings.ToLower(description)
	for _, pat := range customizationDirectivePatterns {
		if pat.MatchString(lower) {
			return true
		}
	}
	return false
}

// proposeHybridTemplatePatch generates a patch for a trivial template file
// (LICENSE, .gitignore, .env) by resolving the reference template text and
// injecting it into the LLM context as a reference, instructing the model to
// apply the user's specific modifications. This consumes LLM tokens but allows
// the model to handle author names, year overrides, and other customizations
// that the static Go template renderer cannot support.
//
// If the reference text cannot be resolved (unexpected target) the function
// falls through to generateTrivialContent — the static template renderer.
func (m *model) proposeHybridTemplatePatch(task *plan.Task) tea.Cmd {
	return func() tea.Msg {
		if m.provider == nil {
			return buildProposalReadyMsg{Err: fmt.Errorf("build execution error: no provider configured")}
		}

		canonicalTarget := gateway.CanonicalizeFileName(task.Target)
		task.Target = canonicalTarget

		var orig string
		if data, rerr := os.ReadFile(canonicalTarget); rerr == nil {
			orig = string(data)
		}

		referenceText := resolveReferenceTemplate(canonicalTarget, task.Description)
		if referenceText == "" {
			content := generateTrivialContent(canonicalTarget, task.Description)
			cleaned := execution.SanitizeLLMResponse(content)
			diff := computeUnifiedDiff(canonicalTarget, orig, cleaned)
			return buildProposalReadyMsg{
				Task: task,
				Patch: &execution.Patch{
					ID:            fmt.Sprintf("hybrid-%d", task.StepNum),
					File:          canonicalTarget,
					Original:      orig,
					Modified:      cleaned,
					TaskID:        task.StepNum,
					ContextID:     m.sess.ContextID,
					IsFullRewrite: true,
				},
				Diff:   diff,
				Output: cleaned,
			}
		}

		handoff := ctxpkg.SanitizeBuildHandoff(task, "")
		handoff += "\n\n### REFERENCE TEMPLATE\n"
		handoff += "Below is the base template text. Apply the user's specific modifications "
		handoff += "(e.g., author name, year, or content changes) to this text and output "
		handoff += "the final updated content as a FILE_CREATE block.\n\n"
		handoff += "```\n" + referenceText + "\n```\n"
		if orig != "" {
			handoff += "\n### EXISTING FILE CONTENT\n```\n" + orig + "\n```\n"
			handoff += "\nThe file already exists. Overwrite it with the modified template content."
		}
		handoff += "\n\n### USER REQUEST\n" + task.Description
		handoff += "\n\nOutput a <<<<<<< FILE_CREATE: " + canonicalTarget + " block with the final content."

		system := m.tieredStrategyContract("existing_file")

		req := ai.Request{
			Model:     m.activeRouteModel(),
			System:    system,
			Stream:    false,
			MaxTokens: 2048,
			Messages:  []ai.Message{{Role: "user", Content: handoff}},
			Reasoning: m.effortFromTasks(),
		}

		ctx, cancel := context.WithTimeout(context.Background(), buildGenerationTimeout)
		resp, err := m.provider.Execute(ctx, req)
		cancel()
		if err != nil {
			return buildProposalReadyMsg{Err: fmt.Errorf("hybrid template patch failed: %w", err)}
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			return buildProposalReadyMsg{Err: fmt.Errorf("hybrid template patch returned empty output")}
		}

		cleaned := sanitizeFileOutput(resp.Content)
		resolved := execution.ResolveModifiedContent(orig, resp.Content)
		if resolved == "" {
			resolved = cleaned
		}

		diff := computeUnifiedDiff(canonicalTarget, orig, resolved)
		patch := &execution.Patch{
			ID:            fmt.Sprintf("hybrid-%d", task.StepNum),
			File:          canonicalTarget,
			Original:      orig,
			Modified:      cleaned,
			TaskID:        task.StepNum,
			ContextID:     m.sess.ContextID,
			IsFullRewrite: true,
		}

		return buildProposalReadyMsg{
			Task:   task,
			Patch:  patch,
			Diff:   diff,
			Output: resp.Content,
		}
	}
}

// resolveReferenceTemplate resolves the reference template text for a given
// trivial template target. For LICENSE files it reads from the embedded license
// registry; for .gitignore / .env it returns the base generated content.
// Returns "" when the target is not a trivial template file.
func resolveReferenceTemplate(target, description string) string {
	base := strings.ToLower(target)
	base = strings.TrimSuffix(base, ".md")

	switch base {
	case "license", "licence":
		licenseType := detectLicenseType(description)
		if licenseType == "" {
			licenseType = "mit"
		}
		text, ok := templates.ReadLicenseTemplate(licenseType)
		if ok {
			return text
		}
		return ""
	case ".gitignore", "gitignore":
		return generateGitignore()
	case ".env", "env", ".env.example", "env.example":
		return generateEnv()
	default:
		return ""
	}
}

func (m *model) applyHotfixPatch(task *plan.Task, patch *execution.Patch) tea.Cmd {
	return func() (msg tea.Msg) {
		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// The terminal buildResultMsg MUST reach the TUI event loop on ANY
		// exit path — success, error, timeout, or panic — so the "Applying
		// hotfix..." spinner can never be orphaned. A panic inside the apply
		// pipeline (e.g. a nil execution engine) is converted into an
		// error-carrying buildResultMsg below.
		defer func() {
			if r := recover(); r != nil {
				msg = buildResultMsg{
					output:   "",
					exitCode: -1,
					err:      fmt.Errorf("hotfix apply panic: %v", r),
				}
			}
		}()

		if err := m.transitionToBuilding(); err != nil {
			return buildResultMsg{
				output:   "",
				exitCode: -1,
				err:      fmt.Errorf("hotfix workflow transition: %w", err),
			}
		}
		if err := m.authorizeBuildExecution([]string{task.Target}, true); err != nil {
			return buildResultMsg{
				output:   "",
				exitCode: -1,
				err:      fmt.Errorf("hotfix authorization failed: %w", err),
			}
		}

		// ── STRICT MUTATION TIMEOUT ─────────────────────────────────────
		// The patch application (file IO + shadow backup + transaction
		// recording) is bounded by a strict deadline so a deadlocked Apply can
		// never freeze the "Applying hotfix..." spinner indefinitely. On
		// expiry a terminal error message is emitted and the enclosing
		// transaction is rolled back by the buildResultMsg handler.
		if m.execEng == nil {
			return buildResultMsg{
				output:   "",
				exitCode: 1,
				err:      fmt.Errorf("hotfix patch apply aborted: execution engine not configured"),
			}
		}
		applyCtx, applyCancel := context.WithTimeout(context.Background(), hotfixApplyTimeout)
		defer applyCancel()
		applyErr := m.execEng.Patches.ApplyContext(applyCtx, patch)
		if applyErr != nil {
			// Graceful no-op skip: the destruction guardrail refused a >80%
			// file wipe without an explicit delete/clear instruction. The file
			// is unchanged — mark the task done (no changes needed) instead of
			// failing the /build run.
			if errors.Is(applyErr, execution.ErrDestructivePatchSkipped) {
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
					output:   fmt.Sprintf("Skipped destructive hotfix patch on %s (no changes needed — file left unchanged)", task.Target),
					exitCode: 0,
				}
			}
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

// countLinesDelta returns the number of added and removed lines in a unified diff.
// Used for accurate FileMutateEvent metrics and destruction guardrail checks.
func countLinesDelta(diff string) (added, removed int) {
	if diff == "" {
		return 0, 0
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
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
		if err := m.authorizeBuildExecution([]string{task.Target}, m.pendingBuildAllowAlways); err != nil {
			return buildResultMsg{
				output:   "",
				exitCode: -1,
				err:      fmt.Errorf("shell exec authorization failed: %w", err),
			}
		}
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

// buildFirstTokenDirective returns the format-forced first-token instruction
// for a build task, chosen by the target file's on-disk state:
//
//   - existing, non-empty file -> a SEARCH/REPLACE block or unified diff
//   - new or 0-byte file       -> a complete-file markdown code block
//
// Forcing a patch format onto a non-existent file makes small models loop on a
// missing "old content" anchor until the request times out; the new-file
// branch must always permit full content generation instead.
func buildFirstTokenDirective(target string) string {
	if data, err := os.ReadFile(target); err == nil && len(data) > 0 {
		return "A SEARCH/REPLACE BLOCK (<<<<<<< SEARCH) OR A UNIFIED DIFF (--- a/ ... +++ b/ ...) for the existing file."
	}
	return "THE OPENING ``` FENCE OF A COMPLETE-FILE MARKDOWN CODE BLOCK (or a FILE: <path> header) containing the FULL new file content. Do NOT use SEARCH/REPLACE or unified diff — the file does not exist yet."
}

func (m *model) handleBuildRun(stepNum int) tea.Cmd {
	// Transition workflow state to Building before any execution
	// begins. If the transition fails (e.g. missing plan guards
	// when in StateIdle), handle gracefully and do not attempt
	// authorization in an invalid state.
	if err := m.transitionToBuilding(); err != nil {
		m.push(roleError, fmt.Sprintf("[BUILD HALTED] Workflow state transition failed: %v", err))
		return nil
	}

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

	// Strategy-aware first-token instruction: never force a SEARCH/REPLACE
	// patch onto a file that does not exist yet. For new/0-byte targets the
	// model must emit the complete file content instead — forcing a patch
	// format on a non-existent file makes small models loop on a missing
	// "old content" anchor until the request times out.
	content := fmt.Sprintf(
		"EXECUTION MODE — implement ONLY this task. "+
			"ZERO conversational text, ZERO explanations, ZERO greetings, ZERO summaries.\n"+
			"YOUR FIRST OUTPUT TOKEN MUST BE %s\n"+
			"Do NOT output JSON, do NOT restate the plan, do NOT list other tasks.\n"+
			"Do NOT ask questions, do NOT ask for clarification, do NOT acknowledge.\n\n"+
			"Step %d: %s\nTarget: %s\nDescription: %s",
		buildFirstTokenDirective(targetTask.Target),
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
	// Per-task execution invalidates any prior fast-track coverage state so a
	// mixed fast-track/per-task session never mis-detects full coverage.
	m.fastTrackTargets = nil
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
				m.smoothStreamTickCmd(),
			)
		}

		// Render the visual permission box via the proposal dock (view layer).
		m.pendingBuildApproval = true
		m.pendingBuildTask = targetTask
		m.enterApprovalState()
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
		// ── TRIVIAL TEMPLATE CREATE (local generation, 0 cloud tokens) ─
		// If the task targets a trivial template file (LICENSE, .gitignore,
		// .env) AND the description contains no customization directives
		// (author name, year, refactor, etc.), generate it locally using
		// Go string templates. This bypasses the LLM entirely — zero HTTP
		// calls, zero cloud tokens consumed.
		if targetTask.IsHardcoded && gateway.IsTrivialCreateTarget(targetTask.Target) {
			if hasCustomizationDirectives(targetTask.Description) {
				return tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "hybrid template"} },
					m.proposeHybridTemplatePatch(targetTask),
					m.smoothStreamTickCmd(),
				)
			}
			return tea.Batch(
				func() tea.Msg { return agentStartMsg{label: "template"} },
				m.applyTrivialTemplate(targetTask),
			)
		}

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
				m.smoothStreamTickCmd(),
			)
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "patching"} },
			m.proposeBuildPatch(targetTask),
			m.smoothStreamTickCmd(),
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
			m.smoothStreamTickCmd(),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "testing"} },
		m.runTestEngine(target),
		m.smoothStreamTickCmd(),
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
			m.smoothStreamTickCmd(),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "building"} },
		m.runBuildEngine(target),
		m.smoothStreamTickCmd(),
	)
}

func (m *model) runTestEngine(target string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = TaskFinishedMsg{}
			}
		}()
		if !verification.IsGoProject(m.workspaceRoot) {
			return testResultMsg{
				output: verification.FormatSkipMessage("HTML/JS/CSS"),
				passed: true,
				failed: 0,
				total:  0,
				err:    nil,
			}
		}
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
		if !verification.IsGoProject(m.workspaceRoot) {
			return buildResultMsg{
				output:   verification.FormatSkipMessage("HTML/JS/CSS"),
				exitCode: 0,
				err:      nil,
			}
		}
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
		m.smoothStreamTickCmd(),
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
	m.startShimmer("Analyzing trace...", "analyze")

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
		m.push(roleError, "$log: execution error: "+providers.SanitizeAPIError(msg.err))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, "Step 1/3: Analyzing failure...")
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	flush := m.flushPendingRecords()
	cmd := m.streamCmd(msg.output)
	// Override the generic "Thinking..." label with the pipeline step so the
	// shimmer status text matches what the silent investigation is doing.
	m.startShimmer("Analyzing failure...", "analyze")
	return tea.Batch(flush, cmd)
}

// handleInvestigateComplete receives the silent analysis and pipes it into plan.
// Step 2: silent blueprinting. No UI mode transition occurs.
func (m *model) handleInvestigateComplete(msg investigateCompleteMsg) tea.Cmd {
	m.pipelineStep = "blueprinting"
	if msg.err != nil {
		m.pipelineRunning = false
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: analysis failed: "+providers.SanitizeAPIError(msg.err))
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
	cmd := m.streamCmd(msg.analysis)
	m.startShimmer("Blueprinting...", "plan")
	return tea.Batch(flush, cmd)
}

// handleBlueprintReady receives the plan output and jumps to /build execution.
// Step 3: Explicit execution jump to /build with the fully realized blueprint.
func (m *model) handleBlueprintReady(msg blueprintReadyMsg) tea.Cmd {
	m.pipelineRunning = false
	m.pipelineStep = ""

	if msg.err != nil {
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: blueprint error: "+providers.SanitizeAPIError(msg.err))
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
	m.investigateRunning = false
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
	m.resetStreamBlocks()
	m.interruptRequested = false

	// Preserve pipeline state if active (implicit pipeline continues)
	if m.pipelineRunning {
		return
	}

	m.stopShimmer()

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
			// binding (m.routeModel("investigate")), and base URL context that
			// lets /ask execute successfully.
			if m.provider == nil {
				m.push(roleError, "[System Error] No AI provider is configured. Run /model to select one.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			}

			// Run the diagnosis through the unified client router.
			resp, err := m.provider.Execute(context.Background(), ai.Request{
				Model: m.routeModel("investigate"),
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
// the ask handoff for refinement. No session history aggregation — the raw
// input IS the payload.
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
		m.smoothStreamTickCmd(),
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
				Model: m.routeModel("ask"),
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
			res := objengine.BuildObjectiveContext(obj.RawIntent, m.routeModel("plan"), m.graph)
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
	m.resolveApprovalState()
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
		}

	case modes.ModePlan:
		if m.handoffCtx.ProposedFix != "" {
			cleaned := CleanHandoffPayload(m.handoffCtx.ProposedFix)
			sanitized := config.SanitizeForSession(cleaned)
			m.handoffCtx.ProposedFix = sanitized
			if len(m.handoffCtx.PendingTodos) == 0 {
				m.handoffCtx.PendingTodos = parseProposedFixIntoTodos(m.handoffCtx.ProposedFix)
			}
		}

	case modes.ModeBuild:
		// REFORM: /build consumes ONLY:
		//   1. The user's raw intent (UserRawIntent from the ledger)
		//   2. The atomic structural tasks produced by /plan (PendingTodos / staged tasks)
		// The raw ProposedFix chat blob / AssistantDiscussionNotes are STRICTLY
		// PURGED here to prevent hallucinated procedural steps from contaminating
		// the build execution engine.
		// Purge stale pre-baked steps — build must not inherit them.
		m.handoffCtx.ProposedFix = ""

		// Build the execution payload from raw intent + plan artifacts only.
		rawIntent := ""
		if m.sess != nil && m.sess.ContextLedger != nil {
			rawIntent = m.sess.ContextLedger.UserRawIntent
		}

		if len(m.handoffCtx.PendingTodos) > 0 {
			payload := buildMutationHandoffPayload(m.handoffCtx.PendingTodos)
			if rawIntent != "" {
				payload = "## USER RAW INTENT\n" + rawIntent + "\n\n" + payload
			}
			m.handoffCtx.LastFailurePayload = payload
		} else {
			// Build strict minimal context for the active task.
			payload := m.buildStrictHandoffPayload()
			if rawIntent != "" {
				payload = "## USER RAW INTENT\n" + rawIntent + "\n\n" + payload
			}
			m.handoffCtx.LastFailurePayload = payload
		}
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

// hasMutationIntent reports whether the given content clearly describes a code
// creation or mutation task (as opposed to a bug diagnosis or investigation).
// Used to override /investigate mode routing and prevent the deadlock loop.
func hasMutationIntent(content string) bool {
	lower := strings.ToLower(content)

	// FRONTEND_UI / layout / rewrite intents are NEVER mutations: a request like
	// "rewrite my personal profile website" must route to /plan (UI creation),
	// never to /build. This guard runs BEFORE the substring checks below because
	// naive signals such as "write" match inside "rewrite" and would otherwise
	// misroute a UI task into the build engine.
	if investigate.ClassifyIntent(content).IsFrontendUI() {
		return false
	}

	mutationSignals := []string{
		"write", "create", "generate", "implement",
		"add ", "make ", "build ", "develop",
		"update ", "modify ", "change ", "refactor ",
		"fix ", "correct ", "edit ",
		"test", "spec", "stub", "mock",
	}
	diagnosticSignals := []string{
		"why is", "why does", "what caused", "investigate",
		"is broken", "is crashing", "is failing",
		"stack trace", "backtrace", "root cause",
		"crash", "panic", "bug",
	}
	// If diagnostic signals are present, this is NOT a mutation.
	for _, s := range diagnosticSignals {
		if strings.Contains(lower, s) {
			return false
		}
	}
	// Check for mutation signals.
	for _, s := range mutationSignals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// explicitFilePathPattern matches path-like tokens or bare filenames carrying a
// source-code extension — e.g. "internal/math/fib.go", "handler.ts", "styles.css".
// Used to confirm a mutation bypass has a concrete target file to edit. Note:
// gateway.ExtractDirectMutationTargets covers @refs and doc/config assets but
// deliberately excludes code extensions (.go/.py/...) that need compilation, so
// this backstops those cases.
var explicitFilePathPattern = regexp.MustCompile(`(?i)(?:^|[\s,:])[a-zA-Z0-9_./\\-]+\.(?:go|py|rs|java|js|jsx|ts|tsx|html|css|scss|less|md|json|yaml|yml|toml|c|cc|cpp|h|hpp|rb|php|sh|sql|proto|graphql|xml)`)

// hasExplicitFilePath reports whether content carries an explicit, targetable
// file reference: a token that looks like a path or bare filename with a
// source-code/document extension. This backstops gateway.ExtractDirectMutationTargets,
// which intentionally excludes code extensions (.go/.py/...) because those
// require compile verification and are not "direct" mutation targets.
func hasExplicitFilePath(content string) bool {
	return explicitFilePathPattern.MatchString(content)
}

// hasExecutableBuildTarget reports whether a mutation bypass has a concrete
// execution target ready for the build engine: explicit file path references
// in the content (either @refs or path/extension tokens) OR actionable pending
// TODOs already staged (e.g. from a /plan approval). A bare mutation intent
// with neither is NOT ready for /build — it must route through /plan for target
// resolution first.
func hasExecutableBuildTarget(content string, m *model) bool {
	if len(m.handoffCtx.PendingTodos) > 0 {
		return true
	}
	if len(gateway.ExtractDirectMutationTargets(content)) > 0 {
		return true
	}
	return hasExplicitFilePath(content)
}

// extractTodosFromPlan extracts TODO items from a plan-mode LLM response.
func extractTodosFromPlan(content string) []string {
	tasks := plan.ParseMarkdownToTasks(content)
	if len(tasks) > 0 {
		todos := make([]string, 0, len(tasks))
		for _, t := range tasks {
			label := string(t.Type) + ": " + t.Target
			if t.Description != "" {
				label += " — " + t.Description
			}
			todos = append(todos, label)
		}
		return todos
	}
	return parseProposedFixIntoTodos(content)
}
