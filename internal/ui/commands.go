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
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain"
	objengine "github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/orchestrator"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
	verification "github.com/PizenLabs/izen/internal/verification"
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
	"/new":              {},
	"/undo":             {},
	"/commit":           {},
	"/checkpoint":       {},
	"/arch":             {},
	"/explain-decision": {},
	"/decide":           {},
	"/copy":             {},
	"/copy-mode":        {},
	"/copy_mode":        {},
	"/inspect":          {},
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

	// ── STALE ACTION-CHIP INVALIDATION ───────────────────────────────
	// Any new user input ends the previous result's relevance: a chip
	// referencing a completed, cancelled, or superseded operation must not
	// linger and offer to re-run an obsolete action. A fresh interaction
	// always renders with a clean chip surface.
	m.currentResult = nil

	// Clear any stale error bar on new user input
	m.lastApplyError = ""

	// Rigid active guards to block spamming inputs during background processes
	if m.streaming || m.agentRunning {
		// $inspect is a read-only observational directive: it renders the
		// telemetry of the most recently finalized operation without starting
		// any work. It is exempt from the busy-input guard so the developer can
		// always inspect the authoritative execution record.
		if !strings.HasPrefix(line, "$inspect") {
			m.push(roleSystem, "Input blocked: task active.")
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}
	}

	// Safety gate confirmation: pending test/run confirmation for large repos
	if m.pendingTestConfirm {
		return m.handleReviewTestConfirm(line)
	}

	// ── $decide AUTONOMY TRACE (intercepted BEFORE the parser pipeline) ──
	// $decide <prompt> runs the human-authorized decision runtime: intent
	// classification (independent of mode), capability-based workspace
	// selection, and the autonomy verdict. It is purely observational — it
	// changes no routing and executes nothing.
	if strings.HasPrefix(line, "$decide") {
		decideContent := strings.TrimSpace(strings.TrimPrefix(line, "$decide"))
		if decideContent == "" {
			m.push(roleSystem, infoStyle.Render("[Usage] $decide <prompt> — run the intent → workspace → autonomy decision trace"))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}
		return m.runAutonomyDecideCmd(decideContent)
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}
		currentMode := m.resolver.Current()
		if !currentMode.CanShell() {
			m.push(roleError, fmt.Sprintf("shell execution blocked in /%s mode (no CapShell)", currentMode))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}

		// ── Shell Guard Rail: Security-aware command firewall ──
		if blocked, _ := m.shellFirewall(shellCmd); blocked {
			m.reviewRunning = false
			m.agentRunning = false
			m.agentLabel = ""
			m.push(roleError, fmt.Sprintf("[SECURITY ALERT] Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.", shellCmd))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		return nil
	}

	// Directive- and global-bearing intents (including the /review $test
	// composite and the $prompt /ask router) are dispatched structurally from
	// the AST: explicit workspace transition first, then commands, then
	// directives. Bare workspace switches and free-form goals fall through to
	// the legacy string routing below, which already handles them.
	if len(ast.Directives) > 0 || len(ast.GlobalCommands) > 0 {
		return m.dispatchASTIntent(ast, line)
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
			// ── /ask SINGLE DECISION AUTHORITY (§4) ────────────────────
			// /ask is the explicit read-only chat boundary, so its content
			// flows through the SAME autonomy decision authority as every other
			// objective. A read-only /ask request (question, inspection,
			// explanation) answers in the ask workspace as before; a mutation
			// request typed into /ask ("/ask remove redundant content from
			// @index.html") must NEVER silently execute or be answered as chat —
			// the autonomy runtime classifies it as a mutation and returns a
			// capability escalation proposal. The user authorizes the boundary;
			// the runtime never re-asks for a mode command.
			if mode == modes.ModeAsk && m.autonomy != nil {
				return tea.Batch(m.runAutonomyRoutedCmd(content), switchCmd)
			}
			return tea.Batch(m.handleMessageContent(content), switchCmd)
		}
		if mode == modes.ModeReview {
			// ── FAST-PATH EARLY EXIT: CLEAN WORKING TREE ────────────────────
			// Probe BEFORE any mode-switch side effects: setMode persists the
			// session to .izen/session.json inside the workspace, which would
			// itself mark an otherwise pristine tree dirty (untracked .izen/)
			// and defeat this exact check on machines without a global git
			// exclude for .izen/. A full-diff /review on a clean tree has
			// nothing to audit — report it immediately and reset every
			// processing flag WITHOUT starting the async pipeline or its
			// spinner; the "Processing file mutations..." animation must never
			// appear for a run that will perform zero mutations.
			if rev := review.NewEngine(".", nil, nil); rev.IsCleanWorkingTree() {
				m.setMode(mode)
				m.push(roleSystem, infoStyle.Render("no changes to review — working tree is clean"))
				m.reviewRunning = false
				m.agentRunning = false
				m.lastActionTime = time.Time{}
				m.syncUIState()
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
				// No command is returned on this path — not even the runtime
				// SwitchModeCmd. A clean-tree review performs zero work, so the
				// fast-path must be fully synchronous: the mode switch already
				// happened via setMode above, and any dispatched command (e.g. a
				// wired runtime switch) would read as "a pipeline/spinner was
				// started" to the caller.
				return nil
			}
			m.setMode(mode)
			m.push(roleSystem, infoStyle.Render("Running review pipeline..."))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return tea.Batch(m.runReviewCmd(""), switchCmd)
		}
		// ── AUTO-TRIGGER /build EXECUTION ──────────────────────
		// When /build is invoked while already in /build mode and
		// a Fast-Track plan or pending TODO checklist exists,
		// immediately trigger execution instead of returning nil
		// (which leaves the UI frozen in an idle state).
		if mode == modes.ModeBuild {
			if m.hasStagedBuildWork() {
				return tea.Batch(m.runStagedBuildViaRuntime(), switchCmd)
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
			m.gotoBottomIfAllowed()
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
	//
	// When the autonomy decision runtime is wired, free-form input flows
	// through it in ANY workspace (including /ask): the runtime classifies
	// conversation directly (no workspace switch, no execution) and routes
	// meaningful requests to their capability-driven workspace. This replaces
	// the mode-first router as the decision layer.
	if m.autonomy != nil {
		return m.routeFreeInput(line)
	}
	return m.runGatedLine(line)
}

// routeFreeInput dispatches a free-form prompt through the autonomy runtime
// when it is wired: the runtime classifies intent, resolves capabilities,
// evaluates risk and selects the workspace — conversation answers directly,
// meaningful requests execute in the decided workspace. When the decision
// runtime is not wired (headless/test harnesses), the input falls through to
// the unified IntentGateway (RuntimeExecutor), which selects the execution
// path deterministically; the UI never decides the path.
func (m *model) routeFreeInput(line string) tea.Cmd {
	if m.autonomy != nil {
		return m.runAutonomyRoutedCmd(line)
	}
	return m.runGatedLine(line)
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

	// ── $hot routes EXCLUSIVELY through the unified IntentGateway ────
	// $hot is an execution directive: the AST parser classifies it and
	// dispatchDirectives routes it through runHotExecution → runGatedLine
	// (IntentGateway.Gate → RuntimeExecutor.Execute). There is NO legacy
	// provider-path branch here — the runtime owns every $hot execution and the
	// UI only submits the request and projects the events.

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
			// An investigate-decided mutation routes through the RuntimeExecutor,
			// never the legacy build engine.
			return m.runRuntimePrompt(content)
		}
	}

	switch currentMode {
	case modes.ModeInvestigate:
		if m.investigateInvocationCount >= maxInvestigateInvocations {
			m.push(roleError, fmt.Sprintf("max investigate invocations (%d) reached", maxInvestigateInvocations))
			m.push(roleSystem, infoStyle.Render("start a new session with /objective <desc> or restart"))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()

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
					m.gotoBottomIfAllowed()
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
					m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			return m.runRuntimePrompt(content)
		}

		m.responseBuffer.Reset()

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
			// ── INSTANT VISUAL FEEDBACK (TUI non-blocking invariant) ─────
			// The Model · waiting stage MUST be visible in <10ms from Enter,
			// BEFORE the async Context Planner (Lea + retrieval) runs. The
			// planner's gatherFileHits / SearchContext can stall for ~5s on a
			// generic query ("what") via the Lynx subprocess, which previously
			// gated the waiting state until after the scan. Publish the stage
			// synchronously here on the UI goroutine so the next View() flush
			// already contains "Model · waiting" while the background goroutine
			// assembles context.
			m.setStage("model", m.getActiveModelName(), stageWaiting)
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
		var cbTrace *ctxpkg.CodebaseTrace
		if p != nil && !isGenericAskContent(content) {
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			plan, err := p.Plan(ctx, content)
			cancel()
			if err == nil && plan != nil && len(plan.Chunks) > 0 {
				header := fmt.Sprintf("### PLANNED CONTEXT (%s intent, %d tokens)\n\n",
					plan.Intent, plan.TokenTotal)
				prepared = header + plan.Assemble() + "\n\n" + content
				governed = true
				cbTrace = planToTrace(plan)
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
		return askStreamPreparedMsg{content: prepared, governed: governed, trace: cbTrace}
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

	return func() (msg tea.Msg) {
		debugLogPlan("runPlanEngineCmd entered; model=" + modelName)

		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// The terminal planResultMsg MUST reach the TUI event loop on ANY
		// exit path — success, model error, fallback failure, timeout, or
		// runtime panic. A panic inside the closure (e.g. a nil engine during
		// scope-guard validation) is converted into an error-carrying
		// planResultMsg so the plan spinner can never be orphaned.
		defer func() {
			if r := recover(); r != nil {
				msg = planResultMsg{
					Err:     fmt.Errorf("plan pipeline panic: %v", r),
					Handoff: handoff,
				}
			}
		}()

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
			// ── WORKER LIFETIME (Phase 3) ────────────────────────────────
			// The plan-synthesis worker is registered against the active
			// operation (when one exists) so no orphan worker can survive the
			// operation's terminalization. A no-op for plan synthesis that
			// holds no operation.
			m.spawnOpWorker("plan")
			defer m.releaseOpWorker("plan")

			// ── PANIC GUARD ─────────────────────────────────────────────
			// A panic inside the LLM plan synthesis (or the scope-guard
			// retry) must still deliver an error outcome to outCh so the
			// select below resolves immediately instead of freezing the
			// spinner for the full deadline waiting on the channel.
			defer func() {
				if r := recover(); r != nil {
					select {
					case outCh <- outcome{tasks: nil, err: fmt.Errorf("plan engine panic: %v", r)}:
					default:
					}
				}
			}()
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
		// ── FAST-PATH EPHEMERAL PLAN INJECTION ($hot / "/build" shortcuts) ──
		// These shortcuts switch the phase ask → build dynamically WITHOUT an
		// initial LLM plan. Inject an EphemeralPlan wrapper into the
		// orchestrator state so the workflow guard sees authorized plan
		// evidence — otherwise Phase lands on building while the guard still
		// evaluates an uninitialized planning context and rejects the very
		// execution the user explicitly requested.
		if mode == modes.ModeBuild && !m.hasStagedBuildWork() && !m.orch.HasAuthorizedPlan() {
			_ = m.orch.InjectEphemeralPlan("fast-path:" + mode.String())
		}
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
		m.gotoBottomIfAllowed()
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
	m.clearAutonomyProposal()

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
			m.gotoBottomIfAllowed()
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
	m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
		m.push(roleSystem, labelBoldStyle.Render("autonomy"))
		m.push(roleSystem, infoStyle.Render("  $prompt <objective>  enter the autonomous runtime (intent → capability → workspace → decision → execution)"))
		m.push(roleSystem, infoStyle.Render("  $decide <prompt>     run the intent → workspace → decision trace"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("commands"))
		m.push(roleSystem, infoStyle.Render("  /help  /usage  /model  /objective  /drop  /clear  /quit  /copy"))
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint  /arch <layer|pkg>  /copy-mode"))
		m.push(roleSystem, infoStyle.Render("  /copy          copy full canonical transcript to clipboard"))
		m.push(roleSystem, infoStyle.Render("  /copy-mode     scrollable inspection mode (j/k, / search, v/y yank, wheel)"))
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

	case cmd == "/grant":
		// DEPRECATED: authorization is now an internal operation of the
		// autonomy proposal (ask_user → Execute). The handler remains only as
		// an internal compatibility seam; the /grant token is not a registry
		// command and is not reachable through the parser pipeline.
		return m.handleAutonomyGrant("")

	case strings.HasPrefix(cmd, "/decide"):
		content := strings.TrimSpace(strings.TrimPrefix(cmd, "/decide"))
		if content == "" {
			m.push(roleSystem, infoStyle.Render("usage: /decide <prompt>  — run the intent → workspace → decision trace"))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}
		return m.runAutonomyDecideCmd(content)

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
		// ── /CLEAR = "Clear what I SEE" ────────────────────────────
		// Clears the visible output, resets the viewport, and hides old
		// execution presentation (activity tree, execution log, control tree,
		// thinking/loading, chips) plus the visible approval presentation.
		//
		// It keeps the session, workspace, mode, git, context (context
		// ledger, attached files, staged plan tasks, persistent history) and
		// token telemetry. It executes nothing and creates nothing. It seals
		// the activity surface so a late event from the cleared execution can
		// never resurrect stale activity. See lifecycle.go.
		m.resetTransientInteraction()
		return tea.Sequence(
			tea.ClearScreen,
			tea.Println("✕ [IZEN Memory] View cleared. Session, context, workspace and history preserved."),
		)

	case cmd == "/drop" || cmd == "/drop all":
		// ── /DROP = "Discard what I am ABOUT TO DO" ────────────────
		// Cancels the active transient execution (if any) and discards every
		// pending proposal / pending action / unresolved mutation / staged
		// plan task list. It keeps the conversation (records), the session,
		// the workspace and the mode. Bare /drop also detaches all attached
		// context files (the historical file-pruning role). It is deliberately
		// NOT a visual clear — nothing visible is erased. See lifecycle.go.
		m.discardPendingAction()
		m.attachedFiles = nil
		m.pendingFileRefs = nil
		m.push(roleSystem, infoStyle.Render("pending action discarded · context files detached"))
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

	case cmd == "/new":
		// ── /NEW = FUTURE session boundary ─────────────────────────
		// Reserved semantic: "Start somewhere NEW" — a new session, new
		// conversation context, reset transient state, fresh presentation
		// (workspace may remain the same; the old session becomes
		// recoverable/history). Deliberately NOT implemented in this phase;
		// /clear is NOT /new (it keeps the session, context and history).
		// See lifecycle.go for the full command contract.
		m.push(roleSystem, infoStyle.Render("/new is the future session boundary — not yet implemented. Use /clear to clear the view (keeps session & context) or /drop to discard a pending action."))
		return nil

	case strings.HasPrefix(cmd, "/undo"):
		return m.runUndoCmd(cmd)

	case cmd == "/commit", strings.HasPrefix(cmd, "/commit "):
		if m.resolver.Current() != modes.ModeBuild {
			m.push(roleError, "commit error: /commit is only available in /build mode")
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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

	case cmd == "/copy", strings.HasPrefix(cmd, "/copy "):
		m.handleCopy()
		return nil

	case cmd == "/copy-mode", cmd == "/copy_mode", cmd == "/inspect":
		if m.inViMode {
			m.uiNotice = "Already in copy mode"
			return nil
		}
		if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
			m.uiNotice = "Cannot enter copy mode while a task is running"
			return nil
		}
		return m.enterViMode()

	}

	m.push(roleError, "unknown command: "+cmd)
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
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
				m.gotoBottomIfAllowed()
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
	m.gotoBottomIfAllowed()
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
	tctx := workflow.TransitionContext{
		HasPlan:         m.sess != nil && len(m.sess.CurrentTasks) > 0,
		HasCapabilities: m.caps != nil,
	}
	// ── ORCHESTRATOR-DRIVEN TRANSITION ──────────────────────────────
	// The orchestrator owns the workflow SM. It drives the canonical
	// Idle -> Plan -> Build path (and any required reset fallback) while
	// sharing the persistent RuntimeContext, so conversation history and
	// workspace artifacts survive the transition.
	if m.orch != nil {
		err := m.orch.Transition(orchestrator.PhaseBuild, tctx)
		if err == nil {
			return nil
		}
		// ── ILLEGAL PHASE-HOP FALLBACK (e.g. $hot from Idle) ─────────
		// A $hot urgent fix skips the plan phase: the orchestrator may sit at
		// PhaseIdle (or Ask/Review), where the strict phase table forbids a
		// direct Idle -> Build hop. Falling back to Force resets the shared SM
		// to idle and drives it forward along the canonical path so the
		// workspace lands in StateBuilding cleanly — never surfacing
		// "invalid transition idle -> build" after a valid patch apply and
		// never triggering automated rollback of an applied hotfix.
		var te *orchestrator.TransitionError
		if errors.As(err, &te) {
			return m.orch.Force(orchestrator.PhaseBuild, tctx)
		}
		return err
	}
	// Legacy raw-SM fallback (headless/test harnesses without an orchestrator).
	if state == workflow.StateIdle {
		if err := m.workflowSM.SendEvent(workflow.EventPlan, workflow.TransitionContext{}); err != nil {
			return err
		}
	}
	if m.workflowSM.State() == workflow.StatePlanning {
		return m.workflowSM.SendEvent(workflow.EventBuild, tctx)
	}
	return fmt.Errorf("workflow: cannot transition to building from %s", state)
}

// hasStagedBuildWork reports whether the session carries any executable build
// work (staged typed tasks, pending TODO checklist, or ledger tasks). It is
// the single source of truth for "a build can start right now" used by the
// continuous-execution seams (chip activation, mode auto-entry).
func (m *model) hasStagedBuildWork() bool {
	return (m.sess != nil && len(m.sess.CurrentTasks) > 0) ||
		len(m.handoffCtx.PendingTodos) > 0 ||
		(m.sess != nil && m.sess.ContextLedger != nil && len(m.sess.ContextLedger.Tasks) > 0)
}

// formatRedundancyLedger renders the deterministic redundant-content findings
// as a compact Context Evidence Ledger the model reasons over — it never
// re-discovers structural facts (requirement §8: context evidence precedes
// model reasoning).
func formatRedundancyLedger(target string, redundant []hotfix.RedundantTarget) string {
	if len(redundant) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context Evidence Ledger\nTarget: %s\nRedundant content findings:\n", target)
	for i, r := range redundant {
		if i >= 6 {
			b.WriteString("* ... more findings omitted\n")
			break
		}
		fmt.Fprintf(&b, "* %s — %s\n", r.Kind, r.Describe())
	}
	return strings.TrimSpace(b.String())
}

// isHTMLTarget reports whether the target file is an HTML document eligible for
// the deterministic target-resolution stage.
func isHTMLTarget(target string) bool {
	ext := strings.ToLower(filepath.Ext(target))
	return ext == ".html" || ext == ".htm" || ext == ".xhtml"
}

// a single, unambiguous full-creation contract.

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
// the task with the amendment as additional context. The amended task is an
// intent only: re-execution crosses the single admission boundary via
// handleBuildRun → dispatchStagedTask (RuntimeExecutor / shell gate) — never
// a caller-side mutation path.
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
	return func() (msg tea.Msg) {
		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// A panic inside the shell execution wrapper must still deliver a
		// terminal buildResultMsg so the spinner can never be orphaned.
		defer func() {
			if r := recover(); r != nil {
				msg = buildResultMsg{
					output:   "",
					exitCode: -1,
					err:      fmt.Errorf("shell exec pipeline panic: %v", r),
				}
			}
		}()

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

		// ── CANCELLATION-COMPLETE SHELL EXECUTION ─────────────────────
		// The subprocess runs under the active operation context so Ctrl+C /
		// Esc cancel it through the context AND the global orphan-kill list.
		// The shell stage is recorded as a real execution stage on the
		// operation telemetry so its latency is attributed truthfully.
		m.setStage("shell", task.Target, stageRunning)
		result, err := runner.RunContext(m.operationContext(), task.Target)
		m.setStage("shell", task.Target, stageDone)
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

// handleBuildRun is the /build queue driver and a pure intent producer. It
// selects the next staged task, performs session-ledger bookkeeping, and
// submits the task across the single execution admission boundary:
//
//	FILE_MUTATE / GIT_ACTION → runRuntimeTaskRequest → RuntimeExecutor.Execute
//	SHELL_EXEC               → runStagedShellGate    → interactive approval
//
// It owns no provider invocation, no patch creation or application, no
// workspace writes, and no execution-engine transactions. Every workspace
// mutation is executed by the RuntimeExecutor; every OS command crosses the
// interactive shell gate.
func (m *model) handleBuildRun(stepNum int) tea.Cmd {
	// Transition workflow state to Building before any execution
	// begins. If the transition fails (e.g. missing plan guards
	// when in StateIdle), handle gracefully and do not attempt
	// authorization in an invalid state.
	if err := m.transitionToBuilding(); err != nil {
		m.push(roleError, fmt.Sprintf("[BUILD HALTED] Workflow state transition failed: %v", err))
		return nil
	}
	targetTask := m.beginStagedTask(stepNum)
	if targetTask == nil {
		return nil
	}
	return m.dispatchStagedTask(targetTask)
}

// beginStagedTask selects the staged /build task to execute — stepNum > 0
// pins an explicit step, otherwise the first idle task is taken — marks it
// processing in the live session ledger and records the per-task
// bookkeeping. It returns nil (after surfacing the reason) when nothing is
// executable right now.
func (m *model) beginStagedTask(stepNum int) *plan.Task {
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
	// ── AUTHORITATIVE STAGE: target resolution ──────────────────────
	// The concrete mutation target was resolved and selected — a real stage.
	m.setStage("target", targetTask.Target, stageDone)

	// Bridge the live /plan task ledger into the session bookkeeping so the
	// terminal-result projections mark task completion on authoritative
	// execution evidence emitted by the RuntimeExecutor.
	if m.buildLedger == nil {
		m.buildLedger = ctxpkg.NewTaskLedger()
	}
	m.currentBuildTaskID = targetTask.StepNum
	// Per-task execution invalidates any prior fast-track coverage state so a
	// mixed fast-track/per-task session never mis-detects full coverage.
	m.fastTrackTargets = nil
	return targetTask
}

// dispatchStagedTask crosses ONE staged task over the single execution
// admission boundary. FILE_MUTATE/GIT_ACTION work is submitted to the
// RuntimeExecutor — which owns provider invocation, context compilation,
// patch creation, the approval gate, apply and verification — and SHELL_EXEC
// work crosses the interactive shell gate. Unknown task types FAIL CLOSED:
// there is deliberately no caller-side fallback that could execute a
// mutation outside the runtime boundary.
func (m *model) dispatchStagedTask(task *plan.Task) tea.Cmd {
	switch task.Type {
	case "SHELL_EXEC":
		return m.runStagedShellGate(task)
	case "FILE_MUTATE", "GIT_ACTION":
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "patching"} },
			m.runRuntimeTaskRequest(task),
			m.smoothStreamTickCmd(),
		)
	default:
		tasks := m.sess.CurrentTasks
		for i := range tasks {
			if tasks[i].StepNum == task.StepNum {
				tasks[i].Status = "stalled"
				break
			}
		}
		m.sess.StageTaskList(&tasks)
		_ = m.sess.Save()
		m.push(roleError, fmt.Sprintf("[BUILD HALTED] Task %d has unsupported type %q — no admitted execution path exists.", task.StepNum, task.Type))
		return nil
	}
}

// runStagedShellGate presents the interactive approval gate for a SHELL_EXEC
// task. The human decision is part of the admission boundary: until it is
// granted (or a prior Allow-Always grant exists), nothing reaches the OS
// shell. The visual "Permission Required" box renders in the proposal dock:
//
//	[y] Allow Once    [a] Allow Always    [n] Reject
func (m *model) runStagedShellGate(task *plan.Task) tea.Cmd {
	if m.pendingBuildAllowAlways {
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "shell exec"} },
			m.runBuildShellExec(task),
			m.smoothStreamTickCmd(),
		)
	}
	m.pendingBuildApproval = true
	m.pendingBuildTask = task
	m.enterApprovalState()
	m.ti.Blur()
	m.recalcViewportHeight()
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		result, err := runner.RunContext(m.operationContext(), cmd)
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
		result, err := runner.RunContext(m.operationContext(), cmd)
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

// Run executes a shell command under a plain background context (legacy). The
// command is still registered for the global orphan-kill list so a Ctrl+C
// hard interrupt can terminate it. Prefer RunContext when a caller holds a
// cancellable operation context.
func (r *executionRunner) Run(command string) (*executionRunResult, error) {
	return r.RunContext(context.Background(), command)
}

// RunContext executes a shell command under ctx so a cancelled operation
// context (Ctrl+C / Esc / mode transition) cancels the subprocess promptly via
// the context in addition to the global orphan-kill list. It is the
// cancellation-complete variant of Run.
//
//nolint:contextcheck // ctx is the caller-supplied cancellation scope, never a fresh one
func (r *executionRunner) RunContext(ctx context.Context, command string) (*executionRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c := exec.CommandContext(ctx, "bash", "-c", command)
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
		m.gotoBottomIfAllowed()
		return nil
	}

	if m.lastTestOutput == "" {
		m.push(roleError, "no previous test/run output available — run $test or $run first")
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
				out, err := runner.RunContext(m.operationContext(), traceData)
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
		m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
	m.executionResolving = false
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

	// ── $inspect — DETAILED EXECUTION TELEMETRY (Phase 3) ───────────────
	// Renders the authoritative execution timeline of the most recently
	// finalized foreground operation: every real stage (target, read, model,
	// patch, validate, apply) with started/completed/elapsed, provider
	// request→waiting→first-token→streaming→terminal attribution, invocation
	// and retry counters, and live-worker tracking. This is execution
	// telemetry, NEVER chain-of-thought. The normal UI stays compact; the
	// detailed timeline lives behind this interaction only.
	if action == "inspect" || strings.HasPrefix(action, "inspect ") {
		return m.runInspectCmd(strings.TrimSpace(strings.TrimPrefix(action, "inspect")))
	}

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
		m.gotoBottomIfAllowed()
		return nil
	}

	// ── $fix BLOCKED IN /investigate (Read-Only Diagnostics) ────────────
	if mode == modes.ModeInvestigate && (action == "fix" || strings.HasPrefix(action, "fix ")) {
		m.cancelStaleAgentOps()
		m.push(roleError, "unknown investigate action: $fix")
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
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
				m.gotoBottomIfAllowed()
				return nil
			}
			logPath := m.sess.TestRunLogPath()
			data, err := os.ReadFile(logPath)
			if err != nil {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, fmt.Sprintf("[System Error] Failed to read log at %s: %v", logPath, err))
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
				return nil
			}
			if len(data) == 0 {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, "[System Error] Log file located but 0 stack trace frames parsed. Raw log size: 0 bytes.")
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
				return nil
			}
			logStr := string(data)
			frames := investigate.ParseStackFrames(logStr)
			if len(frames) == 0 {
				m.reviewRunning = false
				m.lastActionTime = time.Time{}
				m.push(roleError, fmt.Sprintf("[System Error] Log file located but 0 stack trace frames parsed. Raw log size: %d bytes.", len(data)))
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
				m.gotoBottomIfAllowed()
				return agentDoneMsg{}
			}

			// Read the error log for the active context.
			logPath := m.sess.TestRunLogPath()
			logData, err := os.ReadFile(logPath)
			if err != nil {
				m.push(roleError, fmt.Sprintf("[System Error] Failed to read error log at %s: %v", logPath, err))
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
				return agentDoneMsg{}
			}
			if len(logData) == 0 {
				m.push(roleError, "[System Error] Error log is empty — no diagnostic data to analyze.")
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
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
				m.gotoBottomIfAllowed()
				return agentDoneMsg{}
			}

			// Run the diagnosis through the unified client router. Bounded by
			// the operation context + generation deadline so a hung provider can
			// never freeze the /investigate $diagnose spinner.
			ctx, cancel := context.WithTimeout(m.operationContext(), buildGenerationTimeout)
			resp, err := m.provider.Execute(ctx, ai.Request{
				Model: m.routeModel("investigate"),
				Messages: []ai.Message{
					{Role: "user", Content: string(logData)},
				},
				Stream: false,
				System: providers.DiagnoseSystemPrompt,
			})
			cancel()
			if err != nil {
				m.push(roleError, fmt.Sprintf("[System Error] Diagnosis failed: %v", err))
				m.refreshViewportContent()
				m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()

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
	m.gotoBottomIfAllowed()

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

		// ── CONTINUOUS EXECUTION: an approved /build chip executes directly ──
		// The "Approve Plan" and "Execute Build" chips represent real
		// executable-now actions, not decorative mode switches. setMode's
		// auto-trigger dispatches the build executor when staged tasks exist
		// (buildHandoffTriggerContent → runBuildCmd) — so the user never
		// repeats `/build` to start the pipeline they just approved. When the
		// auto-trigger cannot fire (e.g. no staged handoff payload), run the
		// executor directly against the staged task queue.
		if mode == modes.ModeBuild && m.hasStagedBuildWork() && m.buildHandoffTriggerContent(mode) == "" {
			return tea.Batch(m.runStagedBuildViaRuntime(), cmd)
		}

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
// isGenericAskContent reports whether the ask content is too generic to
// benefit from workspace context. Generic single-word queries stall the
// planner's Lynx subprocess for ~4s while yielding zero signal.
func isGenericAskContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		lower := strings.ToLower(strings.Trim(fields[0], ".,!?:;~_-\"'"))
		if len(lower) < 4 {
			return true
		}
		switch lower {
		case "what", "why", "how", "when", "where", "who", "which", "hi", "hello", "hey", "yo", "sup":
			return true
		}
		switch lower {
		case "the", "and", "for", "with", "why", "how", "what", "when", "does", "is", "are", "this", "that", "from", "into", "about", "will", "can", "explain", "describe", "find", "show", "fix", "debug", "log", "panic", "error", "crash", "test", "tests", "failure", "failing", "refactor", "architecture", "overview", "route", "routes", "function", "func":
			return true
		}
	}
	return false
}

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
