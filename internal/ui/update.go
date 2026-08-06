package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/core/classifier"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/build"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/providers"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui/status"
	verification "github.com/PizenLabs/izen/internal/verification"
	"github.com/PizenLabs/izen/pkg/control"
)

// Init initializes the spinner tick, pro tip rotation, and text input blink.
func (m *model) Init() tea.Cmd {
	m.currentTip = allTips[0]
	m.lastTipRotation = time.Now()
	m.proTipIndex = 0
	if m.initStage != initNone && m.initStage != initComplete {
		return tea.Batch(m.smoothStreamTickCmd(), m.proTipTickCmd())
	}
	cmds := []tea.Cmd{
		m.smoothStreamTickCmd(),
		m.proTipTickCmd(),
		m.ti.Focus(),
		m.initSessionStartCheckpoint,
	}
	// Arm the fact-only control telemetry bridge so control.iteration /
	// control.node_observed facts stream into the loop as controlFactMsg.
	if cmd := m.listenControlEventsCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// Update routes state machines and events.
func (m *model) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	// ── GLOBAL PANIC RECOVERY ──────────────────────────────────
	// Any panic inside the update loop is caught here, the full stack trace
	// is written to stderr for debugging, and the model is preserved so
	// the UI remains responsive instead of cascading into a second crash
	// when bubbletea calls View() on the nil model returned by the broken
	// update dispatch.
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			fmt.Fprintf(os.Stderr, "\nIZEN PANIC: %v\nStack:\n%s\n", r, buf[:n])
			model = m
		}
	}()

	// ── UNBLOCKABLE EMERGENCY ESCAPE HATCH ────────────────────────────────
	// Ctrl+C, Esc, and Ctrl+D are ALWAYS processed here, at the very top of
	// the update loop, BEFORE any state gating or sub-component intercept. A
	// stuck StateProcessing / StateAwaitingApproval must NEVER be able to
	// swallow the keyboard — the user can always interrupt back to
	// interactive chat (Philosophy Rule 1: Human-Centered / Reversible).
	//
	//   - Ctrl+C: hard interrupt whenever any workflow operation is in flight
	//     or the view is in a locked state.
	//   - Esc:    emergency abort while the view is frozen in StateProcessing
	//     (or a plan synthesis is pending); in all other states Esc keeps its
	//     normal contextual role (reject approval, dismiss overlay, ...).
	//   - Ctrl+D: emergency abort while frozen in StateProcessing only;
	//     otherwise it keeps its clean-shutdown role in chat.
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.Type {
		case tea.KeyCtrlC:
			if m.state == StateProcessing || m.state == StateAwaitingApproval ||
				m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.planPending {
				return m.handleEmergencyInterrupt("ctrl-c")
			}
		case tea.KeyEsc:
			if m.state == StateProcessing || m.planPending {
				return m.handleEmergencyInterrupt("escape")
			}
		case tea.KeyCtrlD:
			if m.state == StateProcessing {
				return m.handleEmergencyInterrupt("ctrl-d")
			}
		}
	}

	// ── HARD KEYBOARD INTERCEPT: Approval/Processing states bypass all sub-components ──
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			return m.handleKey(keyMsg)
		}
	}

	// ── GLOBAL INTERCEPT: [Alt+P] Toggle Proposal, [Alt+O] Toggle Reasoning ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "alt+p":
			if m.state == StateAwaitingApproval && len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, nil
			}
		case "alt+o":
			// Delegate to the unified reasoning toggle so Alt+O behaves exactly
			// like Ctrl+O: it expands/collapses the live ThinkingBuffer box in
			// the viewport body (immediate re-render, even mid-stream) instead
			// of flipping the vestigial showReasoning flag nothing renders from.
			m.toggleThoughtBlock()
			return m, nil
		}
	}

	// ── Triple-Escape detection: 3 consecutive esc presses enter vi-mode ─
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyEsc {
		now := time.Now()
		if now.Sub(m.lastEscTime) > viTripleEscMax {
			m.escCount = 1
		} else {
			m.escCount++
		}
		m.lastEscTime = now
		if m.escCount >= 3 {
			m.escCount = 0
			m.lastEscTime = time.Time{}
			if !m.inViMode && m.state == StateChat && !m.streaming && !m.agentRunning {
				m.enterViMode()
				return m, nil
			}
		}
	} else if _, ok := msg.(tea.KeyMsg); ok {
		m.escCount = 0
	}

	// ── VI-MODE INTERCEPT: route all key events to the vi-mode handler ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok && m.inViMode {
		return m.handleViModeKey(keyMsg)
	}

	// ── MODEL PICKER ROUTING: intercept key events during model selection ──
	if m.showModelPicker && m.modelPicker != nil {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			if keyMsg.Type == tea.KeyEscape {
				m.showModelPicker = false
				m.modelPicker = nil
				m.ti.Focus()
				return m, nil
			}
			var cmd tea.Cmd
			m.modelPicker, cmd = m.modelPicker.Update(msg)
			return m, cmd
		}
		switch msg := msg.(type) {
		case modelPickerLoadedMsg, modelPickerRefreshMsg:
			var cmd tea.Cmd
			m.modelPicker, cmd = m.modelPicker.Update(msg)
			return m, cmd
		case modelSelectedMsg, tea.WindowSizeMsg:
			// fall through to main switch
		default:
			return m, nil
		}
	}

	// ── INIT STAGE ROUTING: intercept all key messages during setup ─────
	initActive := m.initStage != initNone && m.initStage != initComplete
	if !initActive && !m.isProjectInitialized() {
		// Belt-and-suspenders: project is not initialized on disk. This can
		// happen when initStage is initNone (zero value) — intercept keys
		// to prevent the workspace from processing them before onboarding.
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			m.ti.Blur()
			return m.handleInitKeyMsg(keyMsg)
		}
		// Non-key messages always pass through to the main switch.
	}
	if initActive {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			// Defensive blur: no focused textinput should consume keys
			// during any init stage. Each sub-handler re-focuses as needed.
			m.ti.Blur()
			return m.handleInitKeyMsg(keyMsg)
		}
		// Allow tickMsg to pass through so the spinner continues animating
		// during init stages.
		if _, ok := msg.(tickMsg); ok {
			return m, m.smoothStreamTickCmd()
		}
		// Allow async result messages to reach their handlers in the main
		// type switch below. Without this, gitInitResultMsg gets swallowed
		// and the init stage never advances after pressing 'Y'.
		switch msg.(type) {
		case tea.WindowSizeMsg, gitInitResultMsg, providerSwitchMsg, graphBuiltMsg, graphIndexingMsg, domainEventMsg, controlFactMsg:
			// fall through to main type switch
		default:
			return m, nil
		}
	}

	switch msg := msg.(type) {

	case domainEventMsg:
		// Event bus projection: engines publish domain events headlessly and
		// the UI renders them as activity lines. Runs on the UI goroutine, so
		// all model mutation here is safe.
		m.handleDomainEvent(msg.ev)
		return m, nil

	case controlFactMsg:
		// Fact-only control telemetry projection (control.iteration /
		// control.node_observed). Pure view-model fold on the UI goroutine:
		// the facts update the projected execution tree and nothing else.
		m.handleControlFact(msg.ev)
		return m, nil

	case presentationEventMsg:
		// Application-layer translation projection: the view updates strictly
		// from the decoupled PresentationEvent payload. Runs on the UI
		// goroutine, so all model mutation here is race-free.
		m.handlePresentationEvent(msg.ev)
		return m, nil

	case runtimeResultMsg:
		// Outcome of a RuntimeCommand executed through the facade. Only
		// errors are surfaced; successful commands rendered their own
		// presentation events.
		if msg.err != nil {
			m.push(roleError, fmt.Sprintf("command %s failed: %v", msg.typ, msg.err))
			m.refreshViewportContent()
			if m.Ready && !m.userIsScrollingUp {
				m.Viewport.GotoBottom()
			}
		}
		return m, nil

	case routerResultMsg:
		return m.handleRouterResult(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ti.Width = msg.Width - 8

		// NOTE: the model picker's own size is NOT set here. It's derived
		// from m.width/m.height (just updated above) by
		// modelPickerDialogSize() and applied in renderModelPickerModal()
		// on every render — that's what lets it track resizes precisely
		// and shrink to fit a narrow tmux/terminal split instead of
		// overflowing it. A SetSize call here used to pass the *full*
		// terminal size (not the dialog's actual on-screen size) and was
		// immediately superseded by renderModelPickerModal's own call on
		// the next View() anyway, so it did nothing but mislead.

		vpHeight := m.computeVpHeight()

		if !m.Ready {
			m.Viewport = viewport.New(msg.Width, vpHeight)
			m.Ready = true
		} else {
			m.Viewport.Width = msg.Width
			m.Viewport.Height = vpHeight
		}

		if m.streamParser != nil {
			m.streamParser.SetWidth(msg.Width - 2)
		}

		m.syncShimmerWidth()

		m.refreshViewportContent()
		return m, nil

	case tickMsg:
		// IZEN SAFETY VALVE: force-clear stale review lock after 30s
		// Uses absolute wall-clock comparison (time.Now().Sub) to ensure
		// the timeout cannot be starved or deferred by sequential message
		// stream timing anomalies.
		if m.reviewRunning && !m.lastActionTime.IsZero() && time.Since(m.lastActionTime) > 30*time.Second {
			m.reviewRunning = false
			m.agentRunning = false
			m.agentLabel = ""
			m.agentDone = true
			m.lastActionTime = time.Time{}
			m.sanitizeInputPrompt()
			m.push(roleSystem, mutedStyle.Render("[safety] review action timed out — spinner force-cleared"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}

		// SPINNER SANITY: if pipeline crashes mid-stream or a boundary
		// refusal was triggered, drop spinnerFrame to 0 immediately so
		// the braille spinner in the status bar shows no residual animation.
		if m.spinnerFrame > 0 && !m.streaming && !m.agentRunning && !m.reviewRunning &&
			m.state != StateProcessing && m.state != StateAwaitingApproval && !m.pipelineRunning &&
			m.indexingStatus != "indexing" {
			m.spinnerFrame = 0
		}

		// DETERMINISTIC RECONCILE: a frozen spinner (frame still advancing,
		// animation requested below) with no owning producer is a leaked
		// state. Force-clear it so the UI can never lock on "✦ streaming…".
		//
		// IDLE-GATE FIX: the leak detector must NOT wipe m.streaming /
		// m.agentRunning on the first ticks before a background worker has had
		// a chance to write a process update. We only reconcile when the agent
		// flag is set but there is NO live stream channel driving it AND no
		// deferred orchestration result is expected (planPending) AND the last
		// recorded agent activity has been idle for at least 15 seconds — this
		// safely catches a genuine long-term hang (e.g. a deadlocked /build
		// handoff) while never freezing the spinner of a legitimate /plan or
		// /investigate worker that owns the flags until its terminal result
		// message arrives.
		const agentHangTimeout = 15 * time.Second
		if m.agentRunning && m.streamCh == nil && !m.planPending && m.state == StateChat &&
			!m.reviewRunning && !m.pipelineRunning &&
			m.state != StateProcessing && m.state != StateAwaitingApproval &&
			!m.lastAgentActivity.IsZero() && time.Since(m.lastAgentActivity) > agentHangTimeout {
			m.reconcileSpinner()
		}

		// ── UNIFIED TICK PATTERN ───────────────────────────────────────────
		// The render loop is driven purely by lightweight boolean flags.
		// While any background operation is in flight we advance the spinner
		// frame, repaint the viewport from its live buffers, and re-dispatch
		// the next tick. When idle we return nil and the loop stops — no
		// custom tick-source ownership, no locks, no deadlock.
		hasActiveWork := m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning ||
			m.shellRunning || m.state == StateProcessing || m.state == StateAwaitingApproval || m.shimmerActive
		if hasActiveWork {
			// Keep the activity heartbeat fresh while any execution indicator
			// is live. The idle-gate in the reconcile block above relies on
			// this to avoid prematurely force-clearing a healthy spinner.
			m.lastAgentActivity = time.Now()
			// 1. Physically advance the spinner frame.
			m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
			// 2. Repaint the viewport from the live stream/agent buffers.
			if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.state == StateProcessing || m.shellRunning {
				m.refreshViewportContent()
			}
			// 3. Re-dispatch the smooth tick to keep the render loop alive.
			return m, m.smoothStreamTickCmd()
		}

		return m, nil

	case spinnerTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
		m.refreshViewportContent()
		if m.indexingStatus == "indexing" || m.pendingArchArgs != "" {
			return m, m.spinnerTickCmd()
		}
		return m, nil

	case proTipTickMsg:
		now := time.Now()
		if now.Sub(m.lastTipRotation) >= proTipRotationInterval {
			n := len(allTips)
			if n > 1 {
				idx := rand.Intn(n - 1)
				if idx >= m.proTipIndex {
					idx++
				}
				m.proTipIndex = idx
			}
			m.currentTip = allTips[m.proTipIndex]
			m.lastTipRotation = now
		}
		m.refreshViewportContent()
		return m, m.proTipTickCmd()

	case agentStartMsg:
		m.agentRunning = true
		m.agentDone = false
		m.agentLabel = msg.label
		m.spinnerFrame = 0
		m.lastAgentActivity = time.Now()
		// Surface the loading shimmer + contextual tip for the agent's
		// execution phase (hotfix, build, review, investigate, ...).
		m.startShimmer(agentShimmerText(msg.label), shimmerPhaseForAgentLabel(msg.label))
		// Ensure the shimmer tick loop is alive for agent operations whose
		// dispatch batch did not include it (e.g. $log trace analysis).
		return m, m.shimmerTickCmd()

	case hotfixProgressMsg:
		// Stream a $hot lifecycle log line to the terminal so the developer
		// sees active progress while the LLM generates the patch. Only accept
		// lines while the hotfix is still generating (the proposal/error
		// message clears these flags), preventing stale trailing logs from
		// polluting the approval view.
		if m.agentRunning && m.agentLabel == "hotfix" {
			m.push(roleActivity, msg.Line)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return m, nil

	case agentDoneMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case investigateResultMsg:
		m.lastAgentActivity = time.Now()
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		// Re-derive the presentation state from the cleared flags so a stale
		// StateProcessing derived during the investigation is released and the
		// viewport returns to interactive chat. Pending-approval overrides.
		m.syncUIState()
		if msg.err != nil {
			m.push(roleError, "investigation error: "+providers.SanitizeAPIError(msg.err))
			// PERSISTENT NAVIGATION CHIPS (BUG 1): even on failure the user
			// must never be left on a dead viewport. Surface Re-investigate
			// so the diagnostic loop can be retried.
			m.currentResult = investigateResultActions()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		if msg.sessionKey != "" {
			m.sess.SetInvestigationID(msg.sessionKey)
		}
		// Force-reset streaming middleware flags to guarantee streamCmd can run
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.pushRecords(msg.records)
		// Store the raw Context-Ledger data before anything else — this is
		// the authoritative source for handoff, not the LLM's transient output.
		m.handoffLedgerContent = ctxpkg.SanitizeLedger(msg.ledgerContent)

		// Capture the structured forensic ledger so bridgeInvestigationToLedger
		// can inject its findings as sequential, ID-addressed packets into the
		// canonical session.ContextLedger.
		m.lastInvestigateLedger = msg.investigateLedger

		// BRIDGE: project read-only forensic findings into the canonical
		// session.ContextLedger (handoff SSOT) for downstream /plan consumption.
		m.bridgeInvestigationToLedger(m.handoffLedgerContent, msg.err)

		// Populate the handoff context so the /investigate workspace renders
		// its interactive Action Chip ("Formulate Execution Plan" → /plan).
		// Without this the terminal shows the completion notice but no buttons,
		// stranding the user into manually typing /plan.
		if m.handoffLedgerContent != "" {
			m.handoffCtx.ProposedFix = m.handoffLedgerContent
		}

		// SYSTEM BOUNDARY: when the engine already produced a resolved ledger,
		// its structured data IS the output. Re-streaming the escalation as
		// free-form chat leaks conversational fluff to the viewport, so we
		// suppress it and surface only the bounded "ready for /plan" notice
		// plus the Action Chip.
		if m.handoffLedgerContent == "" && msg.escalationContent != "" {
			m.push(roleSystem, "Diagnostics collected. Analyzing...")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, tea.Batch(flush, m.streamCmd(msg.escalationContent))
		}

		if len(msg.records) == 0 {
			m.push(roleSystem, "Investigation complete — no structured findings to report.")
		}

		// ── AUTO-HANDOFF: /investigate -> /plan or /build ─────────────────
		// REFORM RULES:
		//   - FRONTEND_UI intent ("hand off to plan") → route to /plan (enforces
		//     Layer 3 Hybrid Search — AST + CSS/DOM inspection — before edits).
		//   - Code mutation intent ("hand off to build") → route directly to /build
		//     (short-circuits plan for known mutation patterns).
		//   - Bug diagnostics → route to /plan as normal (full forensic → plan synthesis).
		var cmds []tea.Cmd
		switch {
		case strings.Contains(m.handoffLedgerContent, "hand off to plan"):
			m.modeChangeAuthorized = true
			m.handoffCtx.ProposedFix = m.handoffLedgerContent
			cmds = append(cmds, m.setMode(modes.ModePlan))
		case strings.Contains(m.handoffLedgerContent, "code mutation intent detected"):
			m.modeChangeAuthorized = true
			// Synthesize pending todos from the investigation content so the
			// build auto-trigger in setMode finds work to do immediately.
			m.handoffCtx.PendingTodos = synthesizeBuildTodosFromMutation(msg.sessionKey)
			cmds = append(cmds, m.setMode(modes.ModeBuild))
		default:
			m.handoffCtx.ProposedFix = m.handoffLedgerContent
			if m.handoffLedgerContent != "" {
				m.modeChangeAuthorized = true
				cmds = append(cmds, m.setMode(modes.ModePlan))
			}
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		cmds = append(cmds, m.flushPendingRecords())
		return m, tea.Batch(cmds...)

	case planResultMsg:
		// Terminal handler for the asynchronous PlanEngine synthesis. Only here
		// do we stage tasks and clear streaming state — never while the LLM call
		// is in flight (that would re-block the event loop).
		m.planPending = false
		m.planStartedAt = time.Time{}

		// ALWAYS clear the transient loading flags first so the spinner can
		// never freeze, regardless of which branch below we take.
		m.reconcileSpinner()
		// Re-derive the presentation state from the cleared flags: a stale
		// StateProcessing derived during synthesis (e.g. via a phase-change
		// event while agentRunning was true) must be released here so the
		// viewport returns to interactive chat and Alt+P / Alt+R respond
		// immediately. Pending-approval always overrides if a gate is set.
		m.syncUIState()

		if msg.Err != nil {
			m.push(roleError, fmt.Sprintf("Failed to synthesize plan from ledger: %v", msg.Err))
			// Deterministic pipeline rejections (PolicyEngine escalation /
			// lowering failure) must surface their explicit reason in the
			// status-bar footer.
			if msg.Microkernel || msg.IntentCompiler {
				m.uiNotice = msg.Err.Error()
			}
			// Retain a baseline Action Chip so the user is never left with a
			// dead viewport and no buttons — they can re-investigate the failure.
			m.currentResult = failureResult(m.handoffLedgerContent)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		if len(msg.Tasks) == 0 {
			// Deterministic fallback: a handoff that yields zero constructive
			// tasks must immediately clear the view-model flags rather than
			// leave the UI frozen on the spinner. We still surface a baseline
			// Action Chip (Investigate Root Cause) so the terminal stays alive.
			m.push(roleError, "plan synthesis produced zero tasks — investigation data may be insufficient")
			m.currentResult = failureResult(m.handoffLedgerContent)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// ── TOKEN ACCOUNTING ────────────────────────────────────────────
		// Commit the provider-reported usage of plan synthesis into the
		// session counters and the global status.Tracker. The plan engine
		// records this usage even when the response was truncated by the
		// completion ceiling (finish_reason: "length"), so the token figures
		// never silently vanish on a truncated plan.
		m.InputTokens += msg.TokenInput
		m.OutputTokens += msg.TokenOutput
		m.TotalTokens = m.InputTokens + m.OutputTokens
		if m.IsCloudModel {
			status.Default.Record(m.InputTokens, m.OutputTokens)
		} else {
			status.Default.Record(msg.TokenInput, msg.TokenOutput)
		}

		// ── SCOPE GUARD FINAL VALIDATION ───────────────────────────────────
		// Hard validation interceptor before staging: if the plan engine has a
		// grounded allowed file tree, verify every FILE_MUTATE/ATOMIC_REPLACE/
		// DIFF_PATCH task targets an allowed file. Rejected plans are surfaced
		// as a system activity (dimmed) log so the user sees the scope boundary
		// enforcement in real-time.
		if m.planEngine != nil && len(m.planEngine.AllowedFiles) > 0 {
			scopeTasks := make([]control.TaskTarget, len(msg.Tasks))
			for i, t := range msg.Tasks {
				scopeTasks[i] = control.TaskTarget{Target: t.Target, Type: string(t.Type)}
			}
			if scopeErr := control.ValidateStagedPlan(scopeTasks, m.planEngine.AllowedFiles); scopeErr != nil {
				var sv *control.ScopeViolationError
				if errors.As(scopeErr, &sv) {
					m.logActivity("[ScopeGuard] Rejected target %s - Not in workspace tree", sv.TargetString())
					m.logActivity("[ScopeGuard] Allowed files: %s", sv.AllowedString())
					m.push(roleError, fmt.Sprintf("Plan rejected: target %q is not in the workspace file tree", sv.TargetString()))
					m.refreshViewportContent()
					m.Viewport.GotoBottom()
					flush := m.flushPendingRecords()
					return m, flush
				}
			}
		}

		m.sess.StageTaskList(&msg.Tasks)
		// BRIDGE: mirror the structured /plan queue into the canonical
		// session.ContextLedger as []AtomicTask — the SSOT /build consumes.
		m.bridgePlanToLedger(msg.Tasks)
		m.handoffCtx.PendingTodos = make([]string, len(msg.Tasks))
		for i, t := range msg.Tasks {
			icon := Icon.ShellExec
			if t.Type == "FILE_MUTATE" || t.Type == "DIFF_PATCH" || t.Type == "ATOMIC_REPLACE" {
				icon = Icon.SrcPatch
			}
			m.handoffCtx.PendingTodos[i] = icon + " [" + string(t.Type) + "] " + t.Target + " — " + t.Description
		}
		if msg.IsFastTrack {
			// Auto-create a build checkpoint BEFORE presenting the plan so that
			// the Checkpoint Verification Guardrail allows patch execution without
			// throwing "no valid checkpoint exists".
			if m.execEng != nil {
				_, _ = m.execEng.Checkpoints.Create(fmt.Sprintf("izen fast-track: %d task(s)", len(msg.Tasks)))
			}
			m.planApproved = true
			m.push(roleStatus, accentStyle.Render(fmt.Sprintf("[Fast-Track] Plan auto-approved: %d task(s). Type /build to execute.", len(msg.Tasks))))
		} else {
			m.push(roleStatus, fmt.Sprintf("Plan staged: %d task(s). Approve (Alt+P) or Reject (Alt+R).", len(msg.Tasks)))
		}
		if msg.Microkernel {
			// Deterministic microkernel plans consumed no model tokens.
			m.uiNotice = fmt.Sprintf("Microkernel plan: %d deterministic task(s), no model call", len(msg.Tasks))
		}
		if msg.IntentCompiler {
			// IR-driven intent compiler plans consumed no model tokens.
			m.uiNotice = fmt.Sprintf("Intent compiler plan: %d task(s) from IR lowerer — no model call", len(msg.Tasks))
		}
		// Render the staged task list into the viewport so the developer can
		// see exactly what /build will execute — Principal Engineer format.
		// Use [ ] checkbox markers for each pending task to create an
		// interactive todo checklist look in the TUI.
		// Also expose the plan approval action chips — the user must explicitly
		// approve the plan before /build execution begins.
		// Fast-track plans are auto-approved — show execute-build + reset actions
		// so action chips are visible from ANY mode (including /ask).
		// Non-fast-track plans show the explicit approval gate.
		if msg.IsFastTrack {
			m.currentResult = fastTrackPlanActions()
		} else {
			m.currentResult = planApprovalActions()
		}
		var tb strings.Builder
		tb.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" STRATEGIC ARCHITECTURAL BLUEPRINT") + "\n")
		tb.WriteString("  ▸ Impact Domain      : Execution Layer — Dependency Resolution\n")
		tb.WriteString("  ▸ Risk Evaluation    : Low — Scoped dependency resolution\n")
		tb.WriteString("  ▸ Verification Vector: Build + Test pipeline\n")
		tb.WriteString("\n")
		tb.WriteString(boldMauveStyle.Render(Icon.Timeline+" TODO CHECKLIST") + "\n")
		totalTasks := len(msg.Tasks)
		for _, t := range msg.Tasks {
			icon, track := planTrackIcon(t)
			fmt.Fprintf(&tb, "[ ] %s [%s] #%d %s  [Target %d/%d]\n", icon, track, t.StepNum, t.Target, t.StepNum, totalTasks)
			if t.Description != "" {
				fmt.Fprintf(&tb, "      %s\n", t.Description)
			}
			if t.Rationale != "" && t.Rationale != t.Description {
				fmt.Fprintf(&tb, "      %s\n", t.Rationale)
			}
			if t.Solution != "" {
				fmt.Fprintf(&tb, "      %s\n", t.Solution)
			}
		}
		m.push(roleStatus, tb.String())
		if m.buildLedger == nil {
			m.buildLedger = ctxpkg.NewTaskLedger()
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case graphBuiltMsg:
		m.agentRunning = false
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "graph indexing failed: "+msg.err.Error())
			m.indexingStatus = "error"
			m.pendingArchArgs = ""
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		if msg.graph != nil {
			m.graph = msg.graph
		}
		m.indexingStatus = "indexed"
		// Auto-render pending /arch view if user invoked it
		// while indexing was still in progress.
		if m.pendingArchArgs != "" && m.graph != nil {
			args := m.pendingArchArgs
			m.pendingArchArgs = ""
			graphText := m.renderArch(args)
			m.push(roleSystem, infoStyle.Render(args))
			m.refreshViewportContent()
			m.push(roleSystem, graphText)
			m.Viewport.GotoBottom()
		} else {
			m.pendingArchArgs = ""
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case graphIndexingMsg:
		if msg.indexing {
			m.indexingStatus = "indexing"
		} else if m.indexingStatus != "indexed" {
			m.indexingStatus = "indexed"
		}
		m.refreshViewportContent()
		return m, nil

	case reviewResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		if msg.err != nil {
			m.push(roleError, "review error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetReviewID(msg.sessionKey)
		}
		if msg.saveReportFn != nil {
			msg.saveReportFn()
		}
		m.currentReviewLedger = msg.ledger
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case testResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = !msg.passed
		m.lastTestTarget = ""
		if msg.err != nil {
			m.push(roleError, "test execution error: "+providers.SanitizeAPIError(msg.err))
		}
		if msg.output != "" {
			for _, line := range strings.Split(msg.output, "\n") {
				if line == "" {
					continue
				}
				role := roleSystem
				if strings.Contains(line, "FAIL") || strings.Contains(line, "error") {
					role = roleError
				} else if strings.Contains(line, "PASS") || strings.Contains(line, "ok") {
					role = roleStatus
				}
				m.push(role, line)
			}
		}
		statusLine := fmt.Sprintf("tests: %d total, %d failed", msg.total, msg.failed)
		if msg.passed {
			statusLine = greenStyle.Render("✓ all tests passed (" + strconv.Itoa(msg.total) + ")")
		} else {
			statusLine = redStyle.Render("✗ " + statusLine)
		}
		m.push(roleSystem, infoStyle.Render(statusLine))

		// ── Attach test evidence to active review ledger ──────────────
		if m.currentReviewLedger != nil && m.resolver.Current() == modes.ModeReview {
			evStatus := riview.EvStatusPassed
			evConfidence := riview.ConfVerified
			if !msg.passed || msg.failed > 0 {
				evStatus = riview.EvStatusFailed
				evConfidence = riview.ConfMedium
			}
			m.currentReviewLedger.AddEvidence(
				"V-001",
				riview.EvTypeExistingTest,
				evStatus,
				evConfidence,
				"",
				msg.output,
			)
			if m.currentReviewLedger != nil {
				hasFailed := false
				for _, e := range m.currentReviewLedger.Evidences {
					if e.Status == riview.EvStatusFailed || e.Status == riview.EvStatusPanicked {
						hasFailed = true
						break
					}
				}
				if !hasFailed && msg.passed {
					m.currentReviewLedger.SetStatus(riview.StatusVerified)
				} else if !msg.passed {
					m.currentReviewLedger.SetStatus(riview.StatusConditional)
				}
			}
		}

		// ── Handoff: Capture failure context for mode pipeline ────────────
		if !msg.passed && msg.output != "" {
			m.handoffCtx.LastFailurePayload = msg.output
			m.handoffCtx.TargetScope = m.lastTestTarget
			// Expose the failure as a workflow result: the capability to
			// investigate its root cause is now available for the current
			// view. Cleared on mode entry, so it never persists as a stale
			// chip. A passing run clears any prior failure result.
			m.currentResult = failureResult(msg.output)
		} else if msg.passed {
			m.currentResult = nil
		}

		// ── Build verification: post-mutation test auto-result ───────────
		if m.buildVerifyPending {
			m.buildVerifyPending = false

			// ── Automated error recovery loop ───────────────────────────
			// When verification fails after a build patch, silently trigger
			// a recovery cycle: re-read the file, re-generate a corrected
			// AST block, and re-apply — without producing any UI chatter.
			//
			// RULE B: If the failure is caused by missing Go modules (e.g.,
			// "no required module provides package" or "to add it: go get"),
			// halt the recovery loop immediately. Do NOT waste recovery
			// attempts on import hallucinations — the .go imports are valid,
			// the dependency just needs to be fetched.
			//
			// RULE C: If the failure is an environment/setup error (e.g.,
			// missing go.mod, "pattern ./... does not contain main module"),
			// halt the recovery loop immediately. These are not code defects
			// and cannot be fixed by auto-recovery — they require manual
			// project setup before verification can succeed.
			if !msg.passed && m.resolver.Current() == modes.ModeBuild &&
				m.buildRecoveryCount < maxBuildRecoveryAttempts {
				if hasMissingModuleError(msg.output) || verification.IsEnvironmentSetupError(msg.output) {
					m.acceptAll = false
					m.push(roleError, "[BUILD HALTED] Build failed due to a Go environment setup error. Auto-recovery cannot fix missing go.mod or non-Go project verification. Run 'go mod init' or configure a Go module first, then retry.")
				} else {
					m.buildRecoveryCount++
					m.acceptAll = true
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"⚙ [recovery %d/%d] auto-correcting compilation errors...",
						m.buildRecoveryCount, maxBuildRecoveryAttempts)))
					flush := m.flushPendingRecords()
					return m, tea.Batch(flush, m.runFixCmd(""))
				}
			}

			// If recovery exhausted or verification passed, expose the
			// corresponding workflow result (commit / rollback).
			if m.buildRecoveryCount >= maxBuildRecoveryAttempts {
				m.acceptAll = false
			}
			m.currentResult = buildVerifyResult(msg.passed)

			// ── FAIL-FAST MACHINE: mirror outcome into the canonical
			// session.ContextLedger. On a hard failure (recovery exhausted,
			// missing-module halt, or environment setup error) the active
			// task is marked Failed and the queue is frozen — no subsequent
			// task is advanced, leaving the workspace in its broken state
			// for developer inspection.
			m.bridgeBuildResultToLedger(m.currentBuildTaskID, msg.passed, msg.output)
			if !msg.passed && m.resolver.Current() == modes.ModeBuild {
				if m.buildRecoveryCount >= maxBuildRecoveryAttempts || hasMissingModuleError(msg.output) || verification.IsEnvironmentSetupError(msg.output) {
					m.push(roleError, fmt.Sprintf(
						"[BUILD HALTED] Step %d failed verification. Queue frozen — %d/%d task(s) complete. Inspect and fix, then re-run /build.",
						m.currentBuildTaskID, m.countCompletedLedgerTasks(), len(m.sess.CurrentTasks)))
				} else if m.buildRecoveryCount < maxBuildRecoveryAttempts {
					// Soft failure within recovery budget: ledger still marks
					// the attempt, but the auto-recovery cycle continues.
					m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
						"Step %d verification failed — entering auto-recovery (attempt %d/%d).",
						m.currentBuildTaskID, m.buildRecoveryCount, maxBuildRecoveryAttempts)))
				}
			}
			if m.resolver.Current() == modes.ModeBuild {
				m.push(roleSystem, "Build verification complete.")

				// ── AUTO-HANDOFF: /build → /review ──────────────────────────
				// All build tasks completed successfully and verification tests
				// passed. Transition to /review for a full architectural review
				// of the changes. This enforces the automated handoff chain:
				// /investigate → /plan → approval → /build → /review.
				if msg.passed {
					m.modeChangeAuthorized = true
					m.setMode(modes.ModeReview)
				}
			}
		}

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case buildProposalReadyMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.pipelineRunning = false
		m.streaming = false
		m.sanitizeInputPrompt()
		m.stopShimmer()

		// ── BUILD PROPOSAL FAILURE ───────────────────────────────────
		if msg.Err != nil {
			// Commit any partial provider usage captured before the failure so
			// consumed tokens are not silently zeroed on a failed build attempt.
			if msg.TokenInput > 0 || msg.TokenOutput > 0 {
				m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
			}
			m.push(roleError, "patch generation failed: "+msg.Err.Error())
			tasks := m.sess.CurrentTasks
			for i := range tasks {
				if msg.Task != nil && tasks[i].StepNum == msg.Task.StepNum {
					tasks[i].Status = "failed"
					break
				}
			}
			m.sess.StageTaskList(&tasks)
			_ = m.sess.Save()
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, m.flushPendingRecords()
		}

		// ── TOKEN ACCOUNTING ─────────────────────────────────────────
		// Commit the provider-reported usage of the build proposal call so the
		// footer reflects the tokens consumed to produce the proposed patch.
		if msg.TokenInput > 0 || msg.TokenOutput > 0 {
			m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
		}

		// ── Extract proposals from LLM output ───────────────────────
		props := extractBuildProposals(msg.Output)
		if len(props) == 0 && len(msg.Patches) > 0 {
			// Fast-track path: create proposals from pre-buffered
			// native tool call patches (write_file / apply_patch).
			allCalls := m.toolCallBuffer.All()
			callByPath := make(map[string]execution.BufferedToolCall)
			for _, c := range allCalls {
				callByPath[c.Path] = c
			}
			for _, p := range msg.Patches {
				call, ok := callByPath[p.File]
				if !ok {
					continue
				}
				proposal := SemanticProposal{
					ID:   p.ID,
					Diff: call.Diff,
					Target: SemanticTarget{
						QualifiedName: p.File,
						Module:        filepath.Dir(p.File),
						Language:      langFromPath(p.File),
					},
					Expanded: true,
					Patch:    p,
				}
				props = append(props, proposal)
			}
		}
		if len(props) == 0 {
			// NOTE: msg.Task and msg.Patch are *pointers* that are only
			// populated by the single-task producers of buildProposalReadyMsg
			// (see the constructions in commands.go around runBuildTask /
			// hybrid template patching). The fast-track streaming producer
			// (the goroutine started from cmdFastTrackBuild) only ever sets
			// Patches/Output — it has no single driving Task, because it can
			// touch many files via native tool calls in one turn.
			//
			// This branch used to dereference msg.Task.Target and
			// msg.Patch.ID unconditionally. Whenever the fast-track path
			// produced neither native tool calls (so msg.Patches stayed
			// empty above) nor a plain-text proposal that extractBuildProposals
			// could parse, both msg.Task and msg.Patch were nil here and this
			// panicked with a nil pointer dereference — freezing the whole
			// TUI, since Update()'s recover() preserves the model but drops
			// the in-flight command, so nothing ever re-reads the stream
			// channel again. Fail soft instead: tell the user nothing could
			// be extracted, and let them retry or fall back to /plan.
			if msg.Task == nil || msg.Patch == nil {
				// Fast-track builds (msg.Task==nil && msg.Patch==nil) that
				// produced zero patches (e.g. provider without TCLL/RSNG
				// sentinels — no OpenRouter) get no usable output. Fall back
				// to per-task execution so the user can still build.
				if msg.Task == nil && msg.Patch == nil && len(msg.Patches) == 0 && len(m.sess.CurrentTasks) > 0 {
					m.push(roleStatus, "Fast-track produced no patches — switching to per-task execution.")
					m.refreshViewportContent()
					m.Viewport.GotoBottom()
					return m, m.handleBuildRun(0)
				}
				m.push(roleError, "No patch proposal could be extracted from the model's response — nothing to apply. Try rephrasing the request, or use /plan for a structured retry.")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, m.flushPendingRecords()
			}
			// Fallback: use the pre-computed diff and patch directly.
			// Store the full Patch so applyProposalCmd can use the exact
			// Modified content instead of the display Diff (which may lack
			// @@ hunk headers and trigger ErrInvalidPatchFormat).
			target := msg.Task.Target
			proposal := SemanticProposal{
				ID:   msg.Patch.ID,
				Diff: msg.Diff,
				Target: SemanticTarget{
					QualifiedName: target,
					Module:        filepath.Dir(target),
					Language:      langFromPath(target),
				},
				Expanded: true,
				Patch:    msg.Patch,
			}
			props = []SemanticProposal{proposal}
		}
		m.pendingProposals = props

		// ── FREEZE FOR HUMAN APPROVAL ───────────────────────────────
		m.enterApprovalState()
		m.awaitingConfirmation = true
		m.ti.Blur()
		m.recalcViewportHeight()
		m.Viewport.Height = m.computeVpHeight()

		// Human-readable status line. msg.Task is nil for the fast-track
		// multi-file path, so don't assume it's populated — fall back to
		// the single extracted proposal's target, or a file count.
		statusTarget := "the proposed change"
		switch {
		case msg.Task != nil:
			statusTarget = msg.Task.Target
		case len(props) == 1:
			statusTarget = props[0].Target.QualifiedName
		case len(props) > 1:
			statusTarget = fmt.Sprintf("%d files", len(props))
		}
		m.push(roleStatus, fmt.Sprintf("Proposed patch to %s", statusTarget))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

	case hotfixProposalMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()

		// ── HOTFIX PROPOSAL FAILURE ───────────────────────────────────
		// Patch generation failed: surface the error, abort the hotfix, and
		// restore the stashed plan so the pipeline returns to PAUSED cleanly.
		if msg.Err != nil {
			if msg.TokenInput > 0 || msg.TokenOutput > 0 {
				m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
			}
			m.push(roleError, "[HOTFIX] Patch generation failed: "+msg.Err.Error())
			m.hotfixActive = false
			if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
				m.sess.StageTaskList(&stashedTasks)
				_ = m.sess.Save()
			}
			m.push(roleSystem, infoStyle.Render("[HOTFIX] Pipeline PAUSED. No files were modified."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return m, m.flushPendingRecords()
		}

		// ── TOKEN ACCOUNTING ─────────────────────────────────────────
		// Commit the provider-reported usage of the hotfix patch call.
		if msg.TokenInput > 0 || msg.TokenOutput > 0 {
			m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
		}

		// ── FREEZE AND REQUEST AUTHORIZATION ─────────────────────────
		// Store the synthesized patch + rendered diff proposal. Enter the
		// StateAwaitingApproval approval gate so the developer can inspect the
		// code diff and explicitly approve (Alt+A) or reject (Alt+R) BEFORE
		// any change is written to disk.
		m.pendingHotfixTask = msg.Task
		m.pendingHotfixPatch = msg.Patch

		// Render the diff through the standard proposal dock (MutationRenderer),
		// exactly like a normal /build file-mutation proposal.
		target := msg.Task.Target
		proposal := SemanticProposal{
			ID:   msg.Patch.ID,
			Diff: msg.Diff,
			Target: SemanticTarget{
				QualifiedName: target,
				Module:        filepath.Dir(target),
				Language:      langFromPath(target),
			},
			Expanded: true,
		}
		m.pendingProposals = []SemanticProposal{proposal}

		// ── CLEAN TRANSITION TO PROPOSAL VIEW ────────────────────────
		m.push(roleActivity, "  ⚙ Compiling unified diff schema...")

		m.enterApprovalState()
		m.ti.Blur()
		m.recalcViewportHeight()

		m.push(roleStatus, fmt.Sprintf(
			"[HOTFIX APPROVAL] Proposed patch to %s", target))
		m.push(roleSystem, infoStyle.Render(
			"Review the code diff below. Use Alt+A to accept, Alt+R to reject."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

	case buildResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = msg.exitCode != 0
		// Re-derive the presentation state from the cleared flags so a stale
		// StateProcessing derived during the build is released. Pending-
		// approval overrides (e.g. a queued proposal awaiting authorization).
		m.syncUIState()

		// ── FIX 1: Flush prompt buffer on task failure ────────────────
		// Wipe the volatile user input cache so the next keystroke or
		// command is parsed as a brand-new clean request, rather than
		// appending to the failed context. This prevents the prompt buffer
		// from getting stuck on historical commands.
		if msg.exitCode != 0 {
			m.ti.SetValue("")
			m.syncInputFromTI()
			m.input.Reset()
			m.currentPrompt = ""
			m.responseBuffer.Reset()
			m.currentStreamContent = ""
			m.streamBuffer = ""
			m.resetStreamBlocks()
			m.historyIndex = -1
		}

		if msg.err != nil {
			m.push(roleError, "build execution error: "+providers.SanitizeAPIError(msg.err))
			if m.orch != nil {
				_ = m.orch.Fail(classifier.FailureUnknownClass)
			} else if m.workflowSM != nil {
				_ = m.workflowSM.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{
					FailureClass: classifier.FailureUnknownClass,
				})
			}
			// "Human-Centered / Reversible": an execution failure must never
			// trap the user in the build phase. Unwind back to interactive
			// StateChat so the next prompt routes normally.
			m.unwindBuildFailure()
		}
		if msg.output != "" {
			for _, line := range strings.Split(msg.output, "\n") {
				if line == "" {
					continue
				}
				m.push(roleSystem, line)
			}
		}
		if msg.exitCode == 0 {
			m.push(roleSystem, infoStyle.Render("Execution successful."))
		} else {
			m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Execution failed (exit %d).", msg.exitCode)))
		}

		// ── $hot HOTFIX: restore stashed plan AFTER hotfix ────────────
		// The hotfix lifecycle is fully contained here:
		//   1. Rollback any hotfix mutations on failure.
		//   2. Restore the stashed plan deterministically (Go-level, no LLM).
		//   3. Mark the pipeline PAUSED — no auto-advance, no stalled-marking.
		//   4. Return early, cutting off all fall-through execution paths.
		//
		// This prevents the restored plan's pending tasks from being mistaken
		// as the active pipeline's next steps (which would trigger automatic
		// re-execution of previously rejected SHELL_EXEC tasks).
		if m.hotfixActive {
			// Rollback on failure (no-op if no transaction was started).
			if msg.exitCode != 0 && m.execEng != nil {
				if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
					for _, err := range errs {
						m.push(roleError, fmt.Sprintf("build rollback error: %v", err))
					}
				}
			}

			// Restore the stashed plan deterministically.
			if stashedTasks, err := m.restorePlan(); err == nil {
				if len(stashedTasks) > 0 {
					m.sess.StageTaskList(&stashedTasks)
					_ = m.sess.Save()
				}
			} else {
				m.push(roleError, fmt.Sprintf("[HOTFIX] Failed to restore stashed plan: %v", err))
			}
			m.hotfixActive = false

			// Pipeline is PAUSED — the restored plan is frozen until the
			// user explicitly types "run" or provides feedback.
			m.push(roleSystem, infoStyle.Render("[HOTFIX] Stashed plan restored successfully. Pipeline PAUSED."))

			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// ── FIX 2: Freeze state machine on task failure ───────────────
		// If a step fails, the overall plan status must be STALLED. It is
		// strictly forbidden to advance the internal task index pointer.
		// All remaining idle tasks are marked "stalled" so subsequent
		// /build invocations see them blocked rather than silently
		// advancing into corrupted state.
		if msg.exitCode != 0 {
			// ── ROLLBACK ON FAILURE ─────────────────────────────────
			// Any disk mutations performed during this build execution
			// are rolled back so the workspace is never left in a broken
			// state. The transaction is then reset for the next attempt.
			if m.execEng != nil {
				if errs := m.execEng.RollbackTransaction(); len(errs) > 0 {
					for _, err := range errs {
						m.push(roleError, fmt.Sprintf("build rollback error: %v", err))
					}
				}
			}

			// ── CLEAR DIALOG BUFFER ON TASK FAILURE ────────────────
			// Wipe the LLM conversation history so the next diagnostic
			// or restart prompt starts with a clean context scope, never
			// appending to stale failed-task history.
			if m.sess != nil {
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}

			tasks := m.sess.CurrentTasks
			changed := false
			for i := range tasks {
				if tasks[i].Status == "idle" {
					tasks[i].Status = "stalled"
					changed = true
				}
			}
			if changed {
				m.sess.StageTaskList(&tasks)
				_ = m.sess.Save()
			}
			m.push(roleError, fmt.Sprintf(
				"[BUILD HALTED] Step %d failed. Queue frozen — remaining tasks marked stalled. Use /investigate or /plan to re-generate a valid ledger.",
				m.currentBuildTaskID))
			if m.orch != nil {
				_ = m.orch.Fail(classifier.FailureCodeClass)
			} else if m.workflowSM != nil {
				_ = m.workflowSM.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{
					FailureClass: classifier.FailureCodeClass,
				})
			}
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// After a SHELL_EXEC step finishes successfully, advance to the
		// next idle task so the build queue makes progress automatically.
		// When a $hot hotfix succeeds, the RESTORED plan is checked for
		// remaining work — the original execution flow resumes seamlessly.
		hasNext := false
		for _, t := range m.sess.CurrentTasks {
			if t.Status == "idle" || t.Status == "processing" {
				hasNext = true
				break
			}
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		if hasNext && m.resolver.Current() == modes.ModeBuild {
			return m, tea.Batch(flush, m.handleBuildRun(0))
		}
		// ── AUTO-HANDOFF: /build → /review ──────────────────────
		// All SHELL_EXEC steps completed successfully and no more tasks
		// remain. Transition to /review for a full architectural review.
		// setMode drives the orchestrator, which maps /review onto the shared
		// WorkflowStateMachine (Build -> Review) without touching history.
		if !hasNext && m.resolver.Current() == modes.ModeBuild {
			m.modeChangeAuthorized = true
			m.setMode(modes.ModeReview)
		}
		return m, flush

	case logInputMsg:
		m.agentRunning = false
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.reviewRunning = false
			m.agentDone = true
			m.agentLabel = ""
			m.lastActionTime = time.Time{}
			m.pipelineRunning = false
			m.push(roleError, "$log: error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		return m, m.handleLogInput(msg)

	case investigateCompleteMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.pipelineRunning = false
			m.push(roleError, "silent analysis error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Analysis complete [%s].", msg.ledgerID)))
		return m, m.handleInvestigateComplete(msg)

	case blueprintReadyMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.pipelineRunning = false
			m.push(roleError, "blueprint error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		return m, m.handleBlueprintReady(msg)

	case promptHandoffMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "prompt handoff error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleAI, msg.content)
		// Expose the FollowUp action chip as an interactive component at the
		// terminal footer, not as raw text in the markdown body.
		if len(msg.actions) > 0 {
			m.currentResult = &Result{Actions: msg.actions}
		}
		// Persist the handoff pack into the session.ContextLedger so it
		// survives CleanContextTransitions and is available to /investigate
		// (and downstream modes) as structured diagnostic context.
		if msg.content != "" {
			m.bridgeAskHandoffToLedger(msg.content)
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case fixResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "fix error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleSystem, "Analyzing failure...")
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.content))

	case envResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "env diagnostics error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		// Prepend env diagnostics to LastFailurePayload for cumulative forensic data
		if m.handoffCtx.LastFailurePayload != "" {
			m.handoffCtx.LastFailurePayload = msg.content + "\n" + m.handoffCtx.LastFailurePayload
		} else {
			m.handoffCtx.LastFailurePayload = msg.content
		}
		// env diagnostics carry a failure into the current view; expose it as
		// a workflow result so the investigate capability is available now.
		m.currentResult = failureResult(msg.content)
		m.push(roleSystem, msg.content)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case traceResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "trace execution error: "+providers.SanitizeAPIError(msg.err))
		}

		// Token optimization: truncate middle if output exceeds 4000 chars
		output := msg.output
		runes := []rune(output)
		if len(runes) > 4000 {
			top := string(runes[:2000])
			bottom := string(runes[len(runes)-2000:])
			output = top + "\n... [TRUNCATED " + strconv.Itoa(len(runes)-4000) + " runes] ...\n" + bottom
		}

		if output != "" {
			for _, line := range strings.Split(output, "\n") {
				if line == "" {
					continue
				}
				role := roleSystem
				if strings.Contains(line, "FAIL") || strings.Contains(line, "error") || strings.Contains(line, "panic") || strings.Contains(line, "WARNING: DATA RACE") {
					role = roleError
				} else if strings.Contains(line, "PASS") || strings.Contains(line, "ok") {
					role = roleStatus
				}
				m.push(role, line)
			}
		}

		// Pipe execution log into handoff context for $diagnose
		m.handoffCtx.LastFailurePayload = msg.output
		m.handoffCtx.TargetScope = msg.target
		// A trace that produced output exposes a failure result whose
		// investigate capability is available for the current view.
		m.currentResult = failureResult(msg.output)

		statusLine := fmt.Sprintf("trace: %d total, %d failed — target %q", msg.total, msg.failed, msg.target)
		if msg.passed {
			statusLine = greenStyle.Render("✓ trace passed (" + strconv.Itoa(msg.total) + ") — " + msg.target)
		} else {
			statusLine = redStyle.Render("✗ " + statusLine)
		}
		m.push(roleSystem, infoStyle.Render(statusLine))

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case diagnoseResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "diagnosis error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		// ── FAIL-SAFE: Investigate mode diagnostic is read-only stream ──
		// The diagnostic content is piped through the LLM stream for analysis
		// output. No patches or mutations are ever applied here.
		m.push(roleSystem, "[System] Running deep root cause analysis on qwen2.5-coder with forensic evidence...")
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.content))

	case commitGeneratedMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.pipelineRunning = false
		m.sanitizeInputPrompt()

		if msg.err != nil {
			m.push(roleError, "commit error: "+providers.SanitizeAPIError(msg.err))
		} else {
			result := fmt.Sprintf("Commit: %s · %s", msg.hash, msg.subject)
			m.push(roleSystem, successBannerStyle.Render("[✓] "+result))
		}

		_ = m.sess.Save()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case objectiveAnalyzedMsg:
		if msg.err != nil {
			m.uiNotice = "Objective analysis failed: " + msg.err.Error()
			if m.sess.ObjectiveState != nil {
				m.sess.ObjectiveState.CurrentStatus = domain.ObjectiveIdle
				m.sess.SetObjectiveState(m.sess.ObjectiveState)
				_ = m.sess.Save()
			}
			return m, nil
		}
		if msg.objective == nil {
			m.uiNotice = "Objective analysis failed: empty objective result."
			return m, nil
		}
		m.sess.SetObjectiveState(msg.objective)
		_ = m.sess.Save()
		if msg.objective.TokenBudget.RequiresApproval {
			m.uiNotice = "Objective needs manual approval. Run /objective approve."
		} else {
			m.uiNotice = "Objective planned and active."
		}
		return m, nil

	case archDoneMsg:
		for _, line := range strings.Split(msg.Content, "\n") {
			m.push(roleSystem, infoStyle.Render(line))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, nil

	case mutationResultMsg:
		if msg.err != nil {
			m.setApplyError("apply failed: " + msg.err.Error())
		} else {
			m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
				Target: msg.file,
				Status: msg.status,
			})
		}

		if len(m.pendingProposals) > 0 {
			m.pendingProposals = m.pendingProposals[1:]
		}
		m.proposalDiffOffset = 0

		if len(m.pendingProposals) == 0 {
			// ── MARK BUILD TASK COMPLETED / FAILED ─────────────────────
			// When the proposal flow originates from handleBuildRun
			// (non-streaming FILE_MUTATE/GIT_ACTION), update the task
			// status so the queue advances to the next idle task.
			if m.currentBuildTaskID > 0 && m.sess != nil {
				tasks := m.sess.CurrentTasks
				for i := range tasks {
					if tasks[i].StepNum == m.currentBuildTaskID {
						if msg.err != nil {
							tasks[i].Status = "failed"
						} else {
							tasks[i].Status = "completed"
						}
						break
					}
				}
				m.sess.StageTaskList(&tasks)
				_ = m.sess.Save()
			}

			m.ti.Focus()
			m.resolveApprovalState()
			m.recalcViewportHeight()
			m.awaitingConfirmation = false
			m.acceptAll = false
			if msg.err == nil {
				// ── COMMIT TRANSACTION ─────────────────────────────────
				// All mutations approved and applied — clear the snapshot
				// so the workspace is no longer pinned to the rollback point.
				if m.execEng != nil {
					m.execEng.CommitTransaction()
				}

				outcomeLine := fmt.Sprintf("%s %s • %s", successBannerStyle.Render("[✓]"), msg.file, msg.status)
				m.push(roleSystem, outcomeLine)
				m.createBuildCheckpoint(1)

				// Log foldable entry for the file mutation.
				// Capture the current thinking-panel content so it persists
				// in the entry even during Per-Task Fallback mode.
				thinkingContent := ""
				if m.thinkingPanel != nil {
					thinkingContent = m.thinkingPanel.String()
				}
				m.logStore.AddFull(LogEdit, msg.file, true, msg.status, thinkingContent, "")
				// ── ADVANCE BUILD QUEUE ────────────────────────────────
				// After a FILE_MUTATE/GIT_ACTION task completes, check for
				// the next idle task and execute it.
				if m.resolver.Current() == modes.ModeBuild {
					hasNext := false
					for _, t := range m.sess.CurrentTasks {
						if t.Status == "idle" || t.Status == "processing" {
							hasNext = true
							break
						}
					}
					if hasNext {
						m.refreshViewportContent()
						flush := m.flushPendingRecords()
						return m, tea.Batch(flush, m.handleBuildRun(0))
					}
					// All tasks done — run verification test.
					m.buildVerifyPending = true
					m.refreshViewportContent()
					m.push(roleSystem, "Verifying build...")
					flush := m.flushPendingRecords()
					return m, tea.Batch(flush, m.runTestEngine("./..."))
				}
			} else {
				m.push(roleSystem, failureBannerStyle.Render("[✗] "+msg.file+" — "+msg.err.Error()))
				thinkingContent := ""
				if m.thinkingPanel != nil {
					thinkingContent = m.thinkingPanel.String()
				}
				m.logStore.AddFull(LogEdit, msg.file, false, msg.err.Error(), thinkingContent, "")
			}
		} else {
			m.enterApprovalState()
			m.recalcViewportHeight()
			m.Viewport.Height = m.computeVpHeight()
			m.refreshViewportContent()
		}

		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case applyAllResultMsg:
		applied := 0
		failed := 0
		for _, r := range msg.results {
			if r.err != nil {
				m.setApplyError("apply failed: " + r.err.Error())
				thinkingContent := ""
				if m.thinkingPanel != nil {
					thinkingContent = m.thinkingPanel.String()
				}
				m.logStore.AddFull(LogEdit, r.file, false, r.err.Error(), thinkingContent, "")
				failed++
				continue
			}
			m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
				Target: r.file,
				Status: r.status,
			})
			thinkingContent := ""
			if m.thinkingPanel != nil {
				thinkingContent = m.thinkingPanel.String()
			}
			m.logStore.AddFull(LogEdit, r.file, true, r.status, thinkingContent, "")
			applied++
		}
		m.pendingProposals = nil
		m.awaitingConfirmation = false
		m.acceptAll = false
		m.ti.Focus()
		m.resolveApprovalState()
		m.recalcViewportHeight()
		var testCmd tea.Cmd
		switch {
		case applied > 0 && failed == 0:
			// ── COMMIT TRANSACTION ─────────────────────────────────
			// All mutations approved and applied — clear the snapshot.
			if m.execEng != nil {
				m.execEng.CommitTransaction()
			}

			summary := fmt.Sprintf("%s %d file(s) mutated. Checkpoint created.", successBannerStyle.Render("[✓]"), applied)
			m.push(roleSystem, summary)
			m.createBuildCheckpoint(applied)
			if m.resolver.Current() == modes.ModeBuild {
				m.buildVerifyPending = true
				m.push(roleSystem, "Verifying build...")
				testCmd = m.runTestEngine("./...")
			}
		case applied > 0:
			summary := fmt.Sprintf("%s %d mutated, %d failed.", warningBannerStyle.Render("[!]"), applied, failed)
			m.push(roleSystem, summary)
			m.createBuildCheckpoint(applied)
			if m.resolver.Current() == modes.ModeBuild {
				m.buildVerifyPending = true
				m.push(roleSystem, "Verifying build...")
				testCmd = m.runTestEngine("./...")
			}
		default:
			m.push(roleSystem, failureBannerStyle.Render(fmt.Sprintf("[✗] %d mutation(s) failed.", failed)))
		}
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		if testCmd != nil {
			return m, tea.Batch(flush, testCmd)
		}
		return m, flush

	case shellOutputMsg:
		for _, line := range msg.lines {
			m.push(roleSystem, line)
		}
		m.stopShimmer()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case shellChunkMsg:
		// ── LIVE SHELL OUTPUT ───────────────────────────────────────
		// Stream each stdout/stderr chunk into the running exec entry of the
		// activity tree so the output grows in real-time (visible via Ctrl+O
		// expansion). The heartbeat keeps the idle-gate hang detector from
		// force-clearing the shell spinner.
		if m.activityTree != nil {
			m.activityTree.AppendExecOutput(msg.text)
		}
		m.lastAgentActivity = time.Now()
		if m.Ready {
			m.refreshViewportContent()
		}
		if !m.userIsScrollingUp {
			m.Viewport.GotoBottom()
		}
		return m, m.readShellCh()

	case shellExitMsg:
		// ── SHELL TERMINAL EVENT: clean teardown ────────────────────
		// The running exec entry flips to a completed "(exit N · Xs)" line,
		// the shell channel is torn down, and the shimmer dock is stopped so
		// no static loading line lingers in the viewport history.
		m.shellCh = nil
		m.shellRunning = false
		if m.shellCancel != nil {
			m.shellCancel()
			m.shellCancel = nil
		}
		m.stopShimmer()
		if m.activityTree != nil {
			m.activityTree.CompleteLastExec(msg.exitCode, msg.elapsed)
		}
		if msg.err != nil && msg.exitCode != 0 {
			m.push(roleSystem, dimmedStyle.Render(fmt.Sprintf(
				"shell exited %d (%s)", msg.exitCode, formatElapsed(msg.elapsed))))
		}
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case smoothStreamTickMsg:
		// ── PHYSICALLY ADVANCE THE SPINNER FRAME ───────────────────────────
		// This 20ms smooth-tick loop drives token-stream rendering, but it is
		// ALSO the only tick loop dispatched for background ops that produce no
		// token stream — notably /plan synthesis (runPlanEngineCmd returns one
		// terminal planResultMsg, never tokens). Previously this handler shifted
		// the (empty) stream buffer but never touched m.spinnerFrame, so the
		// braille spinner (rendered as ProposalSpinnerFrames[spinnerFrame]) was
		// physically frozen on frame 0 (⠋) for the entire synthesis — the
		// classic "the spinner is stuck, the UI looks dead" report. Only the
		// 100ms tickMsg handler advanced the frame, and that loop is never
		// started for /plan. Advance the frame here too whenever a background
		// producer owns the flags, so the indicator animates regardless of
		// which tick loop is live. Throttled to ~100ms cadence so the animation
		// speed matches the tickMsg loop and 20ms token pacing stays smooth.
		// Keep the tick loop ALIVE for the entire duration of any background
		// op — including /plan synthesis, where m.streaming stays false and the
		// only other thing driving the event loop is the pending planResultMsg
		// from the background goroutine. If that goroutine hangs (unresponsive
		// local model), a dead tick loop starves the whole event loop: no
		// re-renders, no Ctrl+C responsiveness, no slow-notice — the UI appears
		// frozen. So the loop must keep self-scheduling whenever a background
		// producer still owns the flags.
		backgroundActive := m.streaming || m.agentRunning || m.reviewRunning ||
			m.pipelineRunning || m.planPending || m.shellRunning
		if backgroundActive {
			m.lastAgentActivity = time.Now()
			// GUARD: when the shimmer is active, the shimmerTickCmd already
			// advances m.spinnerFrame at 50ms cadence. Skip the smooth-stream
			// advance to prevent double-incrementing the snowflake frames.
			if !m.shimmerActive && time.Since(m.lastSpinnerAdvance) >= 100*time.Millisecond {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
				m.lastSpinnerAdvance = time.Now()
			}
		}

		// ── FRAME-THROTTLED FLUSH ──────────────────────────────────────
		// Prefer flushing from the StreamThrottle (16ms frame interval / 60FPS).
		// Falls back to direct streamBuffer for non-throttle paths. The throttle
		// retains whatever it does not release this frame, so content is never
		// dropped — only paced.
		emitContent := ""
		if m.streamThrottle != nil {
			emitContent, _ = m.streamThrottle.Flush()
		}
		if emitContent == "" && len(m.streamBuffer) > 0 {
			// Legacy fallback: emit directly from streamBuffer, up to the
			// first word boundary (capped), keeping the remainder buffered for
			// the next tick.
			emit := 0
			minChars := 3
			for i, c := range m.streamBuffer {
				if i >= minChars && (c == ' ' || c == '\n') {
					emit = i + 1
					break
				}
			}
			if emit == 0 {
				emit = len(m.streamBuffer)
			}
			if emit > 80 {
				emit = 80
				for emit > 0 && !utf8.RuneStart(m.streamBuffer[emit]) {
					emit--
				}
			}
			emitContent = m.streamBuffer[:emit]
			m.streamBuffer = m.streamBuffer[emit:]
		}
		if emitContent != "" {
			m.emitVisibleContent(emitContent)
		}

		// Refresh viewport with streaming content.
		if m.Ready {
			m.refreshViewportContent()
			// Only auto-scroll to bottom if the user hasn't explicitly
			// scrolled up — respects user-inspect position during streaming.
			// The expanded output-trace viewport (Ctrl+O) also disables
			// auto-scroll so the inspected lines never jump out from under
			// the user while chunks stream in.
			if m.streaming && !m.userIsScrollingUp && !m.traceExpanded {
				m.Viewport.GotoBottom()
			}
		}

		// Re-schedule the tick loop as long as ANY background producer owns
		// the flags. During /plan synthesis m.streaming is false and the stream
		// buffer is empty, so gating only on those would let the loop die and
		// starve the event loop (frozen UI). m.planPending / m.agentRunning /
		// m.shellRunning are the authoritative "a background op is still in
		// flight" signals. m.shimmerActive is included so the loop survives
		// the async /ask context-prep window (shimmer live, stream not yet
		// started) and any cloud-provider burst that momentarily empties the
		// stream flags.
		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.planPending || m.shellRunning || m.shimmerActive {
			m.streamTickActive = true
			return m, m.smoothStreamTickCmd()
		}
		// Streaming complete
		m.streamTickActive = false
		return m, nil

	case shimmerFrameMsg:
		// ── SHIMMER ANIMATION TICK (~100ms) ─────────────────────────
		// Forwards the frame to the shimmer component, which advances its
		// sweep; the next frame is then re-scheduled on the unified ~100ms
		// shimmer tick (m.shimmerTickCmd) so every animation loop in the UI
		// runs on the same cadence. When shimmerActive has been cleared (first
		// stream token, task completion, or a reconcile pass) the loop drops
		// out here without re-scheduling — the graceful stop that replaces the
		// animated loading line with the streaming output.
		//
		// CLOUD-SPINNER GUARD: the loop MUST keep re-scheduling while either
		// the shimmer is active OR a stream is live, regardless of provider
		// type. Cloud providers (OpenRouter/Cohere) stream in bursts that can
		// momentarily starve lower-priority tick messages; by re-arming here on
		// every frame while streaming is true, the spinner can never die
		// mid-answer. The inline braille spinner takes over from the snowflake
		// once the first content token hands off the dock.
		if !m.shimmerActive && !m.streaming {
			return m, nil
		}
		// SAFETY NET: if every background producer has released its flags but
		// stopShimmer was never invoked by the terminal handler, the next
		// frame self-stops. This guarantees the shimmer can never linger on a
		// dead loading line regardless of which handler resolved the task.
		if !m.streaming && !m.agentRunning && !m.reviewRunning && !m.pipelineRunning && !m.planPending && !m.shellRunning {
			m.stopShimmer()
			return m, nil
		}
		m.shimmerAnim, _ = m.shimmerAnim.Update(msg)
		if m.Ready {
			m.refreshViewportContent()
		}
		// Re-arm the NEXT frame on the unified 100ms shimmer tick (the
		// component's own Tick() cadence would otherwise drift from the rest
		// of the animation layer).
		return m, m.shimmerTickCmd()

	case planSlowNoticeMsg:
		// One-shot soft-timeout probe for /plan synthesis. Only act if THIS
		// synthesis is still pending (guard against a stale probe from a prior
		// run and against a synthesis that already resolved). This is purely
		// informational — it never cancels the 120s hard-timeout work, and it
		// is surfaced through the viewport (m.push), never a raw terminal print
		// that would corrupt the alt-screen frame.
		if m.planPending && msg.startedAt.Equal(m.planStartedAt) {
			m.push(roleSystem, mutedStyle.Render(fmt.Sprintf(
				"[timeout] LLM provider still synthesizing after %s — the local model may be unresponsive; check your model status. Ctrl+C to cancel.",
				planSlowNoticeDelay)))
			m.refreshViewportContent()
			if !m.userIsScrollingUp {
				m.Viewport.GotoBottom()
			}
			return m, m.flushPendingRecords()
		}
		return m, nil

	case askStreamPreparedMsg:
		// T=0MS ASYNC PREP RESOLUTION: the background /ask context assembly
		// (planner query + fallback file reads) has completed. Apply its
		// governance + trace flags, then run streamCmd synchronously on the
		// event loop so ALL stream state mutations stay on the UI goroutine —
		// no data race with the goroutine that produced this message.
		m.askContextGoverned = msg.governed
		if msg.trace != nil {
			m.lastAskTrace = msg.trace
		}
		return m, m.streamCmd(msg.content)

	case thinkingTokenMsg:
		// Thinking/reasoning token emitted by the stream classifier. It is
		// appended to the typed stream buffer as a KindThinking block so the
		// renderer can apply the dimmed/faint style — it never enters the
		// content pipeline. The loading shimmer keeps running (still
		// thinking); the first CONTENT token (tokenMsg) stops it.
		if msg == "" {
			return m, m.readStream()
		}
		m.ensureStreamBlocks().Append(KindThinking, string(msg))
		m.refreshViewportContent()
		if m.Ready && !m.userIsScrollingUp && !m.traceExpanded {
			m.Viewport.GotoBottom()
		}
		return m, m.readStream()

	case tokenMsg:
		// LOCK-FREE CONSUMER: this per-token handler MUST NOT acquire any
		// ContextLedger / TaskLedger mutex. It only appends to local buffers
		// and schedules the next read. The ledger is committed once, at EOF,
		// by the streamDoneMsg handler below. Holding a ledger lock here would
		// serialize the stream against the renderer and reproduce the
		// 108-token freeze.
		//
		// FRAME-THROTTLED EMISSION: raw token chunks are written through the
		// StreamThrottle which enforces a 16ms (≈60FPS) minimum frame interval.
		// The smoothStreamTick handler then flushes word-aligned content from
		// the throttle buffer instead of draining streamBuffer directly. This
		// eliminates layout snapping caused by dumping raw buffer chunks.
		raw := string(msg)
		// SMOOTH CLEARING: the first content token replaces the shimmer
		// loading line with the streaming output. The shimmer tick loop stops
		// itself on the next frame, so no animation frame ever bleeds into
		// the rendered answer.
		if raw != "" && m.shimmerActive {
			m.stopShimmer()
		}
		m.responseBuffer.WriteString(raw)
		m.traceBuffer.WriteString(raw)
		if m.streamThrottle != nil {
			m.streamThrottle.Write(raw)
		} else {
			m.streamBuffer += raw
		}
		if m.streamParser != nil {
			m.streamParser.ProcessChunk(raw)
		}
		var cmds []tea.Cmd
		cmds = append(cmds, m.readStream())
		if !m.streamTickActive {
			m.streamTickActive = true
			cmds = append(cmds, m.smoothStreamTickCmd())
		}
		// Keep cursor blink alive during streaming
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		cmds = append(cmds, tiCmd)
		return m, tea.Batch(cmds...)

	case streamDoneMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamCancel = nil
		m.stopShimmer()

		if m.streamParser != nil {
			m.streamParser.Flush()
			m.streamParser = nil
		}

		// Flush any remaining buffered stream content. The frame throttle may
		// still hold un-emitted tokens — the stream can end before a final tick
		// drains it — so it is drained unconditionally here. Without this, the
		// tail of every response is silently dropped.
		if m.streamThrottle != nil {
			m.streamBuffer += m.streamThrottle.Drain()
		}
		if m.streamTickActive || len(m.streamBuffer) > 0 {
			m.emitVisibleContent(m.streamBuffer)
			m.streamBuffer = ""
			m.streamTickActive = false
		}
		// Final reasoning extraction from any remaining content
		m.extractReasoningContent()

		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		m.InputTokens += msg.tokenInput
		m.OutputTokens += msg.tokenOutput
		m.TotalTokens = m.InputTokens + m.OutputTokens

		// Sync the provider-reported usage (or the local estimate fallback
		// computed above) to the global status tracker so the footer strictly
		// reflects what the provider billed. The model's own counters are
		// session-cumulative; the tracker mirrors them for the renderer.
		if m.IsCloudModel {
			status.Default.Record(m.InputTokens, m.OutputTokens)
		} else {
			status.Default.Record(msg.tokenInput, msg.tokenOutput)
		}

		// Use accumulated stream content as the canonical final text.
		// This avoids any race between async printing and the View cycle.
		final := m.currentStreamContent
		if final == "" {
			final = msg.content
		}
		if final == "" {
			final = m.responseBuffer.String()
		}
		m.responseBuffer.Reset()
		m.currentStreamContent = ""
		// The stream is over — clear the typed block buffer so the next turn
		// starts with a clean, empty renderer (the final answer above was
		// derived from currentStreamContent, which is content-only).
		m.resetStreamBlocks()

		// Sanitize: strip tool execution artifacts so they don't pollute
		// the downstream JSON parser or render pipeline. Lines matching
		// telemetry/error markers are removed from leading/trailing context.
		final = sanitizeFinalContent(final)
		// Strip any remaining reasoning sentinels from final content.
		var finalReasoning string
		final, finalReasoning = m.extractSentinelReasoning(final)
		if finalReasoning != "" {
			m.reasoningBuffer.WriteString(finalReasoning)
			m.sentinelReasoningFlushed = m.reasoningBuffer.Len()
			if m.thinkingPanel != nil {
				m.thinkingPanel.Append(finalReasoning)
			}
			if m.thinkingBuffer != nil {
				m.thinkingBuffer.Append(finalReasoning)
				m.thinkingBuffer.MarkComplete()
			}
		}
		// The stream is genuinely done now — nothing more will ever close
		// an opening sentinel that's still pending. Surface it rather than
		// leaving it stranded (or, as before the fix, leaking it into the
		// next turn's content).
		m.flushPendingReasoningFragment()

		// Prevent blank lines when the response was truncated with zero
		// content (finish_reason: length) or the provider returned nothing.
		if final == "" {
			final = "(response was empty)"
		}

		// Append the completed turn to PreRenderedHistory and freeze state.
		m.push(roleAI, final)

		// TRUNCATION NOTICE: finish_reason == "length" means the response was
		// cut off by the API completion ceiling, not finished naturally. Signal
		// it so the user knows the answer is incomplete rather than assuming a
		// full response (the ~78-token OpenRouter truncation wall).
		if msg.truncated {
			log.Printf("[TRUNCATION] response hit max_tokens ceiling (finish_reason: length) — %d output tokens", msg.tokenOutput)
			m.push(roleSystem, warningStyle.Render(
				"[TRUNCATED] The response hit the provider's max_tokens limit and was cut off mid-generation (finish_reason: \"length\"). Increase max_tokens in the provider config to allow longer responses."))
		}

		// ── IMPLICIT PIPELINE INTERCEPT: pipe stream output to next step ──
		if m.pipelineRunning {
			if m.pipelineStep == "analyzing failure" || m.pipelineStep == "analyzing trace" {
				// Step 1 complete → silently pipe analysis into plan blueprinting
				m.pipelineStep = "blueprinting"
				m.push(roleSystem, infoStyle.Render("Step 2/3: Generating blueprint..."))
				m.handoffCtx.ProposedFix = final

				var planCtx strings.Builder
				planCtx.WriteString("## ANALYSIS OUTPUT\n\n")
				planCtx.WriteString(final)
				planCtx.WriteString("\n\n## INSTRUCTION\n")
				planCtx.WriteString("Based on the analysis above, produce a precise execution plan with Markdown code ")
				planCtx.WriteString("diff blocks or complete file replacements for each fix. Output the plan as a structured ")
				planCtx.WriteString("task list with file targets and descriptions.\n")

				flush := m.flushPendingRecords()
				m.streamCh = nil
				m.streaming = false
				m.streamParser = nil
				return m, tea.Batch(flush, m.streamCmd(planCtx.String()))
			}

			if m.pipelineStep == "blueprinting" && final != "" {
				// Step 2 complete → blueprint is ready, but auto-build is blocked
				pipelineID := ""
				if m.ledger != nil {
					pipelineID = fmt.Sprintf("#%d", m.ledger.ActiveID)
				}
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Pipeline complete [%s].", pipelineID)))
				flush := m.flushPendingRecords()
				return m, tea.Batch(flush, func() tea.Msg {
					return blueprintReadyMsg{blueprint: final, ledgerID: pipelineID}
				})
			}
		}

		// ── Handoff: Capture ProposedFix from investigate mode ──────────
		// The "Formulate Execution Plan" capability is derived from
		// handoffCtx.ProposedFix in BuildViewContext; no UI cache to refresh.
		//
		// DATA HIERARCHY (Context-Ledger > Transaction Cache):
		// 1. handoffLedgerContent (structured Context-Ledger from the
		//    investigate engine's FormatLedgerForPlan) — authoritative SSOT.
		// 2. LastFailurePayload (raw compilation errors / test output).
		// 3. LLM output (final) — transient Transaction Cache, used only
		//    as a last resort when all structured sources are empty.
		// This prevents context poisoning where /plan receives a generic
		// greeting instead of actual engineering diagnostics.
		if m.resolver.Current() == modes.ModeInvestigate && final != "" {
			switch {
			case m.handoffLedgerContent != "":
				m.handoffCtx.ProposedFix = m.handoffLedgerContent
			case m.handoffCtx.LastFailurePayload != "" && IsGenericGreeting(final):
				m.handoffCtx.ProposedFix = m.handoffCtx.LastFailurePayload
			default:
				m.handoffCtx.ProposedFix = final
			}
		}

		// ── Auto-transition: investigate → build on mutation detection ──
		// When a read-only analysis ($diagnose, $test) in investigate mode
		// concludes with a concrete mutation proposal (code blocks with
		// language annotations), automatically transition to /build and
		// initiate the fix pipeline. This eliminates the manual handoff step.
		//
		// STRICT GUARD: If the last compilation state contains [build failed]
		// or any AST/syntax errors, the agent is strictly prohibited from
		// jumping directly to /build. Instead it must route to /plan for
		// structured recovery (Sanitization → Package Isolation → Atomic Fixes).
		if m.resolver.Current() == modes.ModeInvestigate && m.handoffCtx.ProposedFix != "" {
			if containsMutationIntention(m.handoffCtx.ProposedFix) {
				// ── Compile failure guard ────────────────────────────────
				compileFailure := detectCompileFailure(m.handoffCtx.LastFailurePayload)
				if !compileFailure && m.lastTestOutput != "" {
					compileFailure = detectCompileFailure(m.lastTestOutput)
				}

				if compileFailure {
					// ── FORCED ROUTING TO /plan ──────────────────────────
					// Structural errors require a deliberate design phase
					// before any patching is attempted.
					// Save handoff data and clear auto-trigger sources so
					// setMode does NOT double-fire the plan engine.
					savedLastFailure := m.handoffCtx.LastFailurePayload
					savedProposedFix := m.handoffCtx.ProposedFix
					m.handoffCtx.ProposedFix = ""
					m.handoffCtx.LastFailurePayload = ""
					m.handoffLedgerContent = ""

					m.push(roleSystem, warningBannerStyle.Render(
						"[!] Compile failure detected — routing to /plan for structured recovery."))
					m.push(roleSystem, infoStyle.Render(
						"Recovery checklist: Sanitization → Package Isolation → Atomic Fixes."))
					m.setMode(modes.ModePlan)

					recoveryPrompt := "## STRUCTURED RECOVERY CHECKLIST\n\n" +
						"The codebase has compilation errors. Create a structured recovery plan with:\n\n" +
						"### 1. Sanitization\n" +
						"- Identify and document all syntax/type errors in the compilation output\n" +
						"- Do NOT propose blind patches — first understand the full scope of breakage\n\n" +
						"### 2. Package Isolation\n" +
						"- Group related errors by package/file\n" +
						"- Determine dependency order for fixes\n\n" +
						"### 3. Atomic Fixes\n" +
						"- For each error, specify the minimal corrective change\n" +
						"- Output each fix as a separate task with file target and description\n\n" +
						"### Compilation Errors\n```\n" +
						savedLastFailure +
						"\n```\n" +
						"### Root Cause Analysis\n" +
						CleanHandoffPayload(savedProposedFix)

					m.currentPrompt = recoveryPrompt
					m.streamCh = nil
					m.streaming = false
					m.streamParser = nil
					flush := m.flushPendingRecords()
					m.refreshViewportContent()
					return m, tea.Batch(flush, m.streamCmd(recoveryPrompt))
				}

				// ── HANDOFF DATA PRESERVATION ───────────────────────────
				// ProposedFix is intentionally kept intact so the Action Chip
				// remains available for the user to manually trigger the
				// investigate → plan transition via the workspace. The user
				// has full agency to click the chip or type /plan manually;
				// the auto-trigger in setMode will handle the execution.
				m.push(roleSystem, infoStyle.Render(
					"Mutation proposals detected. Use the capability chip or /plan to formulate an execution plan."))
				flush := m.flushPendingRecords()
				m.refreshViewportContent()
				return m, flush
			}
		}

		// ── Handoff: Capture PendingTodos from plan mode ────────────────
		// The "Execute & Verify Patch" capability is derived from
		// handoffCtx.PendingTodos in BuildViewContext; no UI cache to refresh.
		if m.resolver.Current() == modes.ModePlan && final != "" {
			m.handoffCtx.PendingTodos = extractTodosFromPlan(final)
		}

		// SECTION 1: INTERCEPTING STREAM COMPLETION
		promptText := m.currentPrompt
		if promptText != "" {
			// Memory Context Update: Store the assistant reply in the sliding
			// window. The user message was already committed synchronously at
			// submit time (handleInput) to guarantee the model's context window
			// leads the API dispatch — so we append ONLY the assistant turn here
			// to avoid duplicating the user turn.
			m.sess.AddMessage("assistant", final, 5)

			// Securely commit session.json to disk
			if err := m.sess.Save(); err != nil {
				m.push(roleError, fmt.Sprintf("failed to save session: %v", err))
			}

			// History Stream (mutable, resettable on rollback): Write to history/input.log
			if err := session.WriteToHistoryLog(".", "user", promptText); err != nil {
				m.push(roleError, fmt.Sprintf("History Log Failure: %v", err))
			}
			if err := session.WriteToHistoryLog(".", "assistant", final); err != nil {
				m.push(roleError, fmt.Sprintf("History Log Failure: %v", err))
			}

			// Audit Trail (immutable): Log mutations if build mode
			if m.resolver.Current() == modes.ModeBuild || m.resolver.Current() == modes.ModeInvestigate {
				auditEntry := struct {
					Timestamp string `json:"timestamp"`
					Role      string `json:"role"`
					Mode      string `json:"mode"`
					Preview   string `json:"preview"`
				}{
					Role:    "assistant",
					Mode:    m.resolver.Current().String(),
					Preview: truncateString(final, 200),
				}
				data, _ := json.Marshal(auditEntry)
				data = append(data, '\n')
				auditPath := filepath.Join(".izen", "audit", "mutations.log")
				if f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
					_, _ = f.Write(data)
					_ = f.Close()
				}
			}

			// Clear cached prompt after use
			m.currentPrompt = ""
		}

		delta := msg.tokenInput + msg.tokenOutput
		m.IsCloudModel = m.cfg.ActiveProviderName() != "ollama"
		turnCost := 0.0
		if m.IsCloudModel {
			turnCost = float64(msg.tokenInput)*(3.0/1_000_000) + float64(msg.tokenOutput)*(15.0/1_000_000)
		}
		turnCost = llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), turnCost)
		if turnCost > 0 {
			m.AccumulatedCost += turnCost
		}
		costStr := llm.FormatCost(turnCost)
		latencySec := 0.0
		if !m.streamStartTime.IsZero() {
			latencySec = time.Since(m.streamStartTime).Seconds()
			m.streamStartTime = time.Time{}
		}
		m.push(roleStatus, dimmedStyle.Render(
			fmt.Sprintf("✔ done · +%d tok · %s · %.1fs", delta, costStr, latencySec)))

		if m.resolver.Current() == modes.ModePlan {
			// ── STRICT JSON SCHEMA ENFORCEMENT ───────────────────────
			// The /plan mode MUST consume ONLY the verified JSON structure
			// mapped by the schema (prompt/plan.go). If the handoff payload
			// is unparsed or corrupted, do NOT let the local LLM hallucinate
			// ambient tasks via markdown fallback. Force the controller to
			// surface the structural error and reject the output.
			// NOTE: raw final was already pushed as roleAI at line 771 and
			// rendered through cacheRecordToHistory → renderStreamingContent,
			// which handles JSON widget rendering. Do NOT push rendered content
			// again here — that creates a double-rendering loop.
			if jsonResult := plan.ParseJSONPlan(final); jsonResult != nil && jsonResult.Valid && jsonResult.Plan != nil {
				if len(jsonResult.Tasks) > 0 {
					tasks := jsonResult.Tasks
					m.sess.StageTaskList(&tasks)
					// Populate PendingTodos from JSON tasks so the plan view
					// renders the interactive TODO checklist and the /build
					// auto-trigger picks up the task payload.
					if len(m.handoffCtx.PendingTodos) == 0 {
						m.handoffCtx.PendingTodos = make([]string, len(tasks))
						for i, t := range tasks {
							icon := Icon.ShellExec
							if t.Type == "FILE_MUTATE" || t.Type == "DIFF_PATCH" || t.Type == "ATOMIC_REPLACE" {
								icon = Icon.SrcPatch
							}
							m.handoffCtx.PendingTodos[i] = icon + " [" + string(t.Type) + "] " + t.Target + " — " + t.Description
						}
					}
					m.currentResult = planApprovalActions()
					var tb strings.Builder
					tb.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" STRATEGIC ARCHITECTURAL BLUEPRINT") + "\n")
					tb.WriteString("  \u25b8 Impact Domain      : Execution Layer — Dependency Resolution\n")
					tb.WriteString("  \u25b8 Risk Evaluation    : Low — Scoped dependency resolution\n")
					tb.WriteString("  \u25b8 Verification Vector: Build + Test pipeline\n")
					tb.WriteString("\n")
					tb.WriteString(boldMauveStyle.Render(Icon.Timeline+" TODO CHECKLIST") + "\n")
					for _, t := range jsonResult.Tasks {
						icon, track := planTrackIcon(t)
						fmt.Fprintf(&tb, "[ ] %s [%s] %s\n", icon, track, t.Target)
						if t.Description != "" {
							fmt.Fprintf(&tb, "      %s\n", t.Description)
						}
						if t.Rationale != "" && t.Rationale != t.Description {
							fmt.Fprintf(&tb, "      %s\n", t.Rationale)
						}
						if t.Solution != "" {
							fmt.Fprintf(&tb, "      %s\n", t.Solution)
						}
					}
					m.push(roleStatus, tb.String())
					m.push(roleStatus, "Plan staged. Approve or reject with the action bar below.")
				}
			} else {
				errMsg := "plan rejected: output does not conform to JSON schema"
				if jsonResult != nil && jsonResult.Error != "" {
					errMsg = "plan rejected: " + jsonResult.Error
				}
				m.push(roleError, errMsg)
				m.push(roleSystem, infoStyle.Render("regenerate with more precise intent or use /plan again"))
				m.sess.ClearTasks()

				// ── PROMPT BUFFER BLEEDING FIX ────────────────────────
				// Clear the dialog buffer on plan rejection so the next
				// /plan attempt receives zero stale context from the
				// failed previous attempt. Each plan generation is an
				// independent lifecycle event.
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}
			// Ensure the plan view shows approval actions even when the
			// PlanEngine path (planResultMsg) was bypassed.
			if m.currentResult == nil && len(m.handoffCtx.PendingTodos) > 0 {
				m.currentResult = planApprovalActions()
			}
		}

		if m.resolver.Current() == modes.ModeBuild && m.state != StateAwaitingApproval {
			// Strip conversational preamble before patch extraction so that
			// system boilerplate like "Understood. I will follow..." never blocks
			// patch application during auto-recovery.
			clean := build.StripNonPatchProse(final)
			props := extractBuildProposals(clean)
			diffProps := extractDiffPatches(clean)
			if len(diffProps) > 0 {
				existing := make(map[string]bool)
				for _, p := range props {
					existing[p.Target.QualifiedName] = true
				}
				for _, d := range diffProps {
					if !existing[d.Target.QualifiedName] {
						props = append(props, d)
					}
				}
			}
			if len(props) > 0 {
				if m.acceptAll {
					m.pendingProposals = props
					m.state = StateProcessing
					m.recalcViewportHeight()
					m.ti.Blur()
					return m, m.applyAllProposalsCmd()
				} else {
					m.pendingProposals = props
					m.enterApprovalState()
					m.recalcViewportHeight()
					m.Viewport.Height = m.computeVpHeight()
					m.awaitingConfirmation = true
					m.ti.Blur()
					m.refreshViewportContent()
				}
			} else {
				// ── ZERO-TOLERANCE CONVERSATIONAL GUARD ─────────────
				// If the build LLM produced zero SEARCH/REPLACE or FILE_CREATE
				// blocks, it output conversational prose instead of code patches.
				// This is a hard violation of the build contract. Surface the error
				// and request a re-generation with strict tool-only directive.
				conversational := build.IsConversationalOutput(final)
				if conversational {
					regen := m.retryBuildWithStrictDirective()
					if regen != nil {
						return m, regen
					}
					m.push(roleError, "[BUILD GUARD] LLM output contained only conversational text — no SEARCH/REPLACE or FILE_CREATE blocks. Re-run /build with a stricter task description.")
				}
			}
			m.sess.ClearTasks()
		}

		// EXTRACT SHELL COMMANDS → INJECT INTO INPUT BAR (Human-In-The-Loop)
		// Under no circumstances does the TUI execute a shell command automatically.
		// The agent-proposed command is injected into the text input bar, where the
		// user must explicitly review and press Enter to execute.
		if m.state == StateChat && !m.awaitingConfirmation {
			shellCmds := extractShellCommands(final)
			if len(shellCmds) > 0 {
				mode := m.resolver.Current()
				if !mode.CanShell() {
					msg := fmt.Sprintf("Tool 'shell' rejected in /%s.", mode)
					// ── POLICY REJECTION BADGE ─────────────────────────────
					// Render the forbidden-tool notice as a clean muted status
					// badge ("☢ [POLICY] Tool 'shell' rejected in /ask.") instead
					// of raw unformatted text, so the scrollback reads as a
					// styled system notice with zero layout jitter. The session
					// copy fed to the model stays plain text.
					m.push(roleSystem, toolRejectBadgeStyle.Render("☢ [POLICY]")+" "+
						mutedStyle.Render(fmt.Sprintf("Tool 'shell' rejected in /%s.", mode)))
					m.sess.AddMessage("system", msg+" You are in a Read-Only execution environment and must stop requesting system mutations.", 3)
				} else {
					cmd := shellCmds[0]
					if sanitized, rejected, reason := sanitizeShellCmd(cmd); rejected {
						m.push(roleError, "[AUTO-FILL BLOCKED] Shell command not loaded: "+reason)
					} else if blocked, _ := m.shellFirewall(sanitized); blocked {
						m.push(roleError, "[SECURITY] Proposed shell command blocked by firewall.")
					} else {
						m.ti.SetValue(sanitized)
						m.ti.CursorEnd()
						m.syncInputFromTI()
						m.proposedShellCmd = sanitized
						m.push(roleSystem, infoStyle.Render(
							"Command injected into input bar. Review and press Enter to execute, Esc to cancel."))
					}
				}
			}
		}

		// AI response and telemetry rendered exclusively through View().
		// No tea.Println scrollback flush — prevents double-rendering in
		// terminal scrollback vs Bubble Tea viewport.

		// Clear planPending flag to prevent spinner lock on plan mode completion.
		m.planPending = false

		m.refreshViewportContent()
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.streamCancel = nil
		m.planPending = false
		m.stopShimmer()

		// User-initiated interrupt — suppress error noise, just clean up.
		if m.interruptRequested {
			m.interruptRequested = false
			m.responseBuffer.Reset()
			m.currentStreamContent = ""
			m.streamBuffer = ""
			m.resetStreamBlocks()
			m.streamTickActive = false
			m.streamCancel = nil
			m.refreshViewportContent()
			return m, nil
		}

		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		if errors.Is(msg.err, providers.ErrOpenRouterAuth) {
			m.push(roleError, errorStyle.Render("✗ OpenRouter Authorization Failed"))
			m.push(roleSystem, infoStyle.Render("Invalid or missing OPENROUTER_API_KEY. Please check your environment variables or run:"))
			m.push(roleSystem, infoStyle.Render("  export OPENROUTER_API_KEY=<your_key>"))
		} else {
			sanitized := providers.SanitizeAPIError(msg.err)
			m.push(roleError, "stream error: "+sanitized)
		}

		// ── TOKEN ACCOUNTING ON FAILURE ────────────────────────────────
		// Explicit Over Implicit: commit whatever usage the provider reported
		// (or the character estimate the stream reader produced) before the
		// stream died, so tokens consumed on a timeout/error are not silently
		// zeroed in the footer. Publish the typed StreamUsage event so
		// telemetry projections observe the failed attempt too.
		if msg.tokenInput > 0 || msg.tokenOutput > 0 {
			m.commitTokenUsage(msg.tokenInput, msg.tokenOutput)
			if m.bus != nil {
				modelName := ""
				if m.cfg != nil {
					modelName = m.cfg.ActiveModelName()
				}
				m.bus.Publish(events.NewStreamUsage(modelName, msg.tokenInput, msg.tokenOutput, true, msg.err.Error()))
			}
		}

		// ALWAYS flush partial stream tokens to the TUI so that tokens
		// already received on the wire are never discarded when a
		// mid-stream connection error or unexpected termination occurs.
		if msg.content != "" {
			if m.streamTickActive {
				m.currentStreamContent += m.streamBuffer
				m.streamBuffer = ""
				m.streamTickActive = false
			}
			m.streamBuffer = msg.content
			m.currentStreamContent = msg.content
			m.ensureStreamBlocks().Append(KindContent, msg.content)
			m.extractReasoningContent()
		}
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case thinkingStreamMsg:
		// Real-time reasoning token dispatch to the TUI Thinking Panel.
		// Updates from token #1 — no waiting for the full response.
		if m.thinkingPanel != nil {
			m.thinkingPanel.Append(msg.Content)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
		}
		return m, m.readStream()

	case livePreviewChunkMsg:
		// Stream content or tool call arguments directly into the
		// LiveCodePreview for real-time code preview during fast-track builds.
		if msg.Content != "" {
			m.traceBuffer.WriteString(msg.Content)
		}
		if m.liveCodePreview != nil && msg.Content != "" {
			label := "live_stream"
			if msg.IsTool {
				label = "live_tool"
			}
			m.liveCodePreview.AddOrUpdate(label, msg.Content, msg.IsTool)
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, m.readStream()

	case buildFailedMsg:
		// Guaranteed cleanup from stream defer: spinner stops, pipeline resets.
		m.streamCh = nil
		m.streaming = false
		m.streamCancel = nil
		m.pipelineRunning = false
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		// "Explicit Over Implicit": report partial usage on the failed fast-track
		// attempt so consumed tokens are never silently zeroed.
		if msg.TokenInput > 0 || msg.TokenOutput > 0 {
			m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
		}
		m.push(roleError, "fast-track build failed: "+providers.SanitizeAPIError(msg.Err))
		// "Human-Centered / Reversible": a failed build stream must never trap
		// the workflow in the build phase. Unwind the state machine back to
		// StateChat/interactive so the next prompt routes normally instead of
		// failing with "transition from build to ask".
		m.unwindBuildFailure()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case TaskFinishedMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.pipelineRunning = false
		m.pipelineStep = ""
		m.streaming = false
		m.streamCh = nil
		m.planPending = false
		if m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
		}
		m.shellRunning = false
		m.shellCh = nil
		if m.shellCancel != nil {
			m.shellCancel()
			m.shellCancel = nil
		}
		m.streamBuffer = ""
		m.currentStreamContent = ""
		m.resetStreamBlocks()
		m.streamTickActive = false
		m.interruptRequested = false
		m.spinnerFrame = 0
		m.stopShimmer()
		m.ti.Focus()
		m.sanitizeInputPrompt()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case traceUpdateMsg:
		m.currentTrace = msg.trace
		return m, nil

	case config.ConfigChangeMsg:
		newCfg, err := config.Load()
		if err == nil {
			m.cfg = newCfg
		}
		// A config file change may have altered the active provider or the
		// intent-tier models; re-pin the pipeline router so mode commands
		// never route a stale local model into a cloud provider.
		m.syncPipelineTiers()
		return m, nil

	case gitInitResultMsg:
		switch {
		case msg.err != nil:
			m.initGitInitErr = msg.err.Error()
		case m.initStage == initGitCheck:
			m.initGitInitDone = true
			m.advancePastGitCheck()
		case m.initStage == initNone:
			// Git was initialized from the first-run welcome screen (initNone).
			// Advance directly to identity setup, skipping the confirm step.
			m.advancePastGitCheck()
		}
		return m, m.smoothStreamTickCmd()

	case tea.MouseMsg:
		// HARD GUARD: In destructive states (approval/exec), mouse events are
		// completely ignored — no viewport scrolling, no coordinate mapping.
		// This eliminates any possibility of accidental mutation via click.
		// During processing, wheel events are allowed for scroll inspection.
		if m.state == StateAwaitingApproval {
			return m, nil
		}
		if m.state == StateProcessing && msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
			return m, nil
		}
		// Track scroll-up (wheel up) to suppress auto-scroll during
		// user-inspection. Scroll-down does NOT reset the flag — only
		// SPACE or a new submission resets it.
		if msg.Button == tea.MouseButtonWheelUp {
			m.userIsScrollingUp = true
		}
		// Pure O(1) viewport YOffset shift. No refreshViewportContent, no
		// re-rendering, no string mutation — the viewport internal buffer is
		// already set and only its scroll origin moves.
		if m.Ready {
			var vpCmd tea.Cmd
			m.Viewport, vpCmd = m.Viewport.Update(msg)
			return m, vpCmd
		}
		return m, nil

	case tea.KeyMsg:

		// ── Capability Hotkeys (alt+ modifier only) ────────────────────
		// Single-character hotkeys are strictly banned to prevent key
		// collisions with normal prompt input (e.g., typing in /plan).
		// The active capabilities come from the workflow layer's render
		// context; the renderer/update loop never decides which exist.
		if !m.streaming && !m.agentRunning && m.state == StateChat {
			key := msg.String()
			for _, act := range m.BuildWorkspace().Actions {
				if act.Enabled && strings.EqualFold(act.Shortcut, key) {
					return m, m.handleChipActivation(act)
				}
			}
		}

		// In special states, route directly to handleKey.
		if m.state == StateAwaitingApproval || m.state == StateProcessing {
			resModel, cmd := m.handleKey(msg)
			return resModel, cmd
		}

		if strings.TrimSpace(m.ti.Value()) == "/clear" && msg.String() == "enter" {
			m.showBanner = true
		} else if msg.String() == "enter" && strings.TrimSpace(m.ti.Value()) != "" {
			m.showBanner = false
		}

		// ── '?' help toggle (only when input buffer is empty) ────────────
		if msg.String() == "?" && strings.TrimSpace(m.ti.Value()) == "" {
			m.showHelpOverlay = !m.showHelpOverlay
			return m, nil
		}
		if m.showHelpOverlay {
			if msg.String() == "?" || msg.Type == tea.KeyEscape {
				m.showHelpOverlay = false
				return m, nil
			}
			// Block all other input while help is showing
			return m, nil
		}

		// ── Autocomplete active: intercept navigation / dismissal ──────
		if m.autocompleteActive && len(m.autocompleteItems) > 0 {
			switch msg.Type {
			case tea.KeyEscape:
				m.dismissAutocomplete()
				return m, nil
			case tea.KeyUp:
				m.navigateAutocomplete(-1)
				return m, nil
			case tea.KeyDown:
				m.navigateAutocomplete(1)
				return m, nil
			case tea.KeyTab:
				m.completeAutocomplete()
				return m, nil
			case tea.KeyEnter:
				m.completeAutocomplete()
				return m, nil
			case tea.KeySpace:
				m.dismissAutocomplete()
				// fall through so space inserts into textinput
			}
		}

		if !m.autocompleteActive && !m.streaming && !m.agentRunning {
			switch msg.Type {
			case tea.KeyUp:
				if len(m.history) > 0 {
					if m.historyIndex == -1 {
						m.historyIndex = len(m.history) - 1
					} else if m.historyIndex > 0 {
						m.historyIndex--
					}
					m.ti.SetValue(m.history[m.historyIndex])
					m.ti.CursorEnd()
				}
				return m, nil

			case tea.KeyDown:
				if m.historyIndex != -1 {
					if m.historyIndex < len(m.history)-1 {
						m.historyIndex++
						m.ti.SetValue(m.history[m.historyIndex])
						m.ti.CursorEnd()
					} else {
						m.historyIndex = -1
						m.ti.SetValue("")
						m.ti.CursorEnd()
					}
				}
				return m, nil
			}
		}

		// ── Viewport scroll keys with scroll-lock tracking ──────────────────
		if m.Ready {
			switch msg.Type {
			case tea.KeyPgUp, tea.KeyHome:
				m.Viewport, _ = m.Viewport.Update(msg)
				m.userIsScrollingUp = true
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				m.Viewport, _ = m.Viewport.Update(msg)
				return m, nil
			}
		}

		// ── SPACE snap-to-bottom (resets user scroll-lock) ─────────────────
		if msg.Type == tea.KeySpace && !m.autocompleteActive {
			m.userIsScrollingUp = false
			// Re-anchor the expanded output-trace window to the tail so the
			// user "catches up" to the latest streamed content.
			m.traceWindowAnchored = false
			if m.Ready {
				m.Viewport.GotoBottom()
			}
		}

		resModel, cmd := m.handleKey(msg)
		return resModel, cmd

	case modelSelectedMsg:
		m.showModelPicker = false
		m.modelPicker = nil
		m.sessionModel = msg.model.ID
		m.cfg.Models.SessionModel = msg.model.ID
		m.IsCloudModel = msg.model.Provider != "ollama"

		// Apply effort/intent level to the config tiers.
		effort := msg.effort
		tierKey := effort.ConfigTier()
		m.cfg.SetTierOverride(tierKey, msg.model.ID)
		m.syncPipelineTiers()

		modelProvider := msg.model.Provider
		currentProvider := ""
		if m.provider != nil {
			currentProvider = m.provider.Name()
		}

		var cmds []tea.Cmd
		if modelProvider != "" && modelProvider != currentProvider {
			envVar, known := validProviders[modelProvider]
			switch {
			case known && m.isProviderAvailable(modelProvider, envVar):
				cmds = append(cmds, m.switchProvider(modelProvider))
			case modelProvider == "ollama":
				cmds = append(cmds, m.switchProvider(modelProvider))
			default:
				m.push(roleError, fmt.Sprintf("[✗] Provider %q not configured — model set but provider unchanged", modelProvider))
			}
		}

		m.ti.Focus()
		effortLabel := msg.effort.Description()
		m.push(roleSystem, accentStyle.Render(fmt.Sprintf("✓ Model set to %s [%s]", msg.model.Name, msg.model.Provider)))
		m.push(roleSystem, mutedStyle.Render(fmt.Sprintf("  Effort: %s (%s)", msg.effort, effortLabel)))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m, tea.Batch(cmds...)
	}

	// ── Viewport scroll keys (any state) ─────────────────────────────────────
	if m.Ready {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.Type {
			case tea.KeyPgUp, tea.KeyHome:
				m.Viewport, _ = m.Viewport.Update(keyMsg)
				m.userIsScrollingUp = true
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				m.Viewport, _ = m.Viewport.Update(keyMsg)
				return m, nil
			}
		}
	}

	// ── Text Input Pass-Through ──────────────────────────────────────────────
	var tiCmd tea.Cmd
	m.ti, tiCmd = m.ti.Update(msg)
	return m, tiCmd
}

func (m *model) spinnerTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

func (m *model) proTipTickCmd() tea.Cmd {
	return tea.Tick(proTipRotationInterval, func(t time.Time) tea.Msg {
		return proTipTickMsg(t)
	})
}

func (m *model) smoothStreamTickCmd() tea.Cmd {
	return tea.Tick(20*time.Millisecond, func(t time.Time) tea.Msg {
		return smoothStreamTickMsg(t)
	})
}

// planSlowNoticeCmd schedules a one-shot soft-timeout probe for /plan synthesis.
// It captures the synthesis start time so the handler can verify the notice
// still applies to the CURRENT synthesis (a stale probe from a prior run is
// ignored). It never cancels or shortens the real work — it only surfaces a
// viewport-safe warning if the local model is slow to respond.
func (m *model) planSlowNoticeCmd() tea.Cmd {
	started := m.planStartedAt
	return tea.Tick(planSlowNoticeDelay, func(time.Time) tea.Msg {
		return planSlowNoticeMsg{startedAt: started}
	})
}

// containsMutationIntention detects whether an LLM analysis output from
// investigate mode proposes concrete file mutations. Uses language-annotated
// code blocks as the heuristic — when the agent outputs code blocks with known
// language identifiers (go, diff, python, etc.), it indicates a patch proposal.
// detectCompileFailure scans the given output for build/compile failure
// signals. Returns true when the codebase is in a non-compilable state that
// requires structural recovery before any patch can be applied.
//
// Detection is delegated to the canonical signal classifier: routing between
// investigate, plan and build evaluates the detected SignalKind values instead
// of re-scanning free text for compile-failure substrings.
func detectCompileFailure(output string) bool {
	if output == "" {
		return false
	}
	return signal.HasCompileFailure(signal.Detect(output, "ui.compile"))
}

// hasMissingModuleError detects Go missing-dependency signals in build/test
// output. When a build fails because a module is not in go.sum/go.mod, the
// compiler prints "no required module provides package" or hints "to add it:
// go get". These errors cannot be fixed by editing .go files — they require
// running go get <package>. Returns true when a SignalDepMissing is present.
func hasMissingModuleError(output string) bool {
	if output == "" {
		return false
	}
	return signal.HasKind(signal.Detect(output, "ui.build"), signal.SignalDepMissing)
}

func containsMutationIntention(content string) bool {
	lower := strings.ToLower(content)
	mutationLanguages := []string{
		"```go", "```diff", "```patch", "```python", "```typescript",
		"```javascript", "```java", "```rust", "```c", "```cpp", "```c++",
		"```rs", "```ts", "```js", "```py",
	}
	for _, lang := range mutationLanguages {
		if strings.Contains(lower, lang) {
			return true
		}
	}
	return false
}

// ── Vi-mode lifecycle ─────────────────────────────────────────────────────────

// enterViMode transitions the UI into navigation mode: blurs the text input,
// initializes cursor at the last record, resets selection state, and refreshes
// the viewport with cursor highlighting.
func (m *model) enterViMode() {
	m.inViMode = true
	m.viModeState = ViNormal
	m.cursorLine = max(0, len(m.records)-1)
	m.cursorCol = 0
	m.visualStartLine = 0
	m.visualStartCol = 0
	vpHeight := m.computeVpHeight()
	m.viTopLine = max(0, len(m.records)-vpHeight)
	if m.viTopLine > m.cursorLine {
		m.viTopLine = m.cursorLine
	}
	m.viSearchResults = nil
	m.viSearchIdx = -1
	m.viPendingPrefix = ""
	m.viCmdMode = false
	m.viCmdBuf = ""
	m.ti.Blur()
	m.refreshViewportContent()
}

// exitViMode returns the UI to normal interactive mode: clears selection,
// refocuses the text input, and resets all vi-mode state.
func (m *model) exitViMode() {
	m.inViMode = false
	m.viModeState = ViNormal
	m.cursorLine = 0
	m.cursorCol = 0
	m.visualStartLine = 0
	m.visualStartCol = 0
	m.viTopLine = 0
	m.viSearchResults = nil
	m.viSearchIdx = -1
	m.viPendingPrefix = ""
	m.viCmdMode = false
	m.viCmdBuf = ""
	m.searchActive = false
	m.searchQuery = ""
	m.ti.Focus()
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// ── Vi-mode key handler ───────────────────────────────────────────────────────

// handleViModeKey routes all keyboard events during vi-mode. It implements a
// state machine that handles motion (j/k/gg/G/Ctrl+d/Ctrl+u), search (/),
// visual selection (v), yank (y), command-line entry (:), and exit (i).
func (m *model) handleViModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── Command-line mode (:q, /search) ──────────────────────────────────
	if m.viCmdMode {
		return m.handleViCmdInput(msg)
	}

	// ── Pending prefix: handle multi-key sequences like gg ─────────────
	if m.viPendingPrefix != "" {
		prefix := m.viPendingPrefix
		m.viPendingPrefix = ""
		if prefix == "g" && (msg.String() == "g" || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'g')) {
			// Jump to absolute top: reset logical coords and instantly snap
			// the viewport offset to the very first physical line.
			m.cursorLine = 0
			m.cursorCol = 0
			m.viTopLine = 0
			m.viForceTop = true
			m.viSearchIdx = -1
			m.syncViewportToCursor()
			return m, nil
		}
	}

	// ── Single-key vi-mode actions ──────────────────────────────────────
	switch msg.String() {
	// ── Exit / return to normal input ──
	case "i":
		m.exitViMode()
		return m, nil

	// ── 2D Motions ──
	case "h":
		if m.cursorCol > 0 {
			m.cursorCol--
		}
		m.syncViewportToCursor()
		return m, nil

	case "l":
		lineLen := m.lineRuneLen(m.cursorLine)
		if m.cursorCol < lineLen-1 {
			m.cursorCol++
		}
		m.syncViewportToCursor()
		return m, nil

	case "j":
		if m.cursorLine < len(m.records)-1 {
			m.cursorLine++
			// Horizontal safe-guard: clamp cursorCol to new line length
			if m.cursorCol > m.lineRuneLen(m.cursorLine) {
				m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
			}
			m.viSearchIdx = -1
			m.syncViewportToCursor()
		}
		return m, nil

	case "k":
		if m.cursorLine > 0 {
			m.cursorLine--
			// Horizontal safe-guard: clamp cursorCol to new line length
			if m.cursorCol > m.lineRuneLen(m.cursorLine) {
				m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
			}
			m.viSearchIdx = -1
			m.syncViewportToCursor()
		}
		return m, nil

	// ── Line-boundary motions ──
	case "0":
		m.cursorCol = 0
		m.syncViewportToCursor()
		return m, nil

	case "$":
		m.cursorCol = max(0, m.lineRuneLen(m.cursorLine)-1)
		m.syncViewportToCursor()
		return m, nil

	// ── Page motions ──
	case "ctrl+d":
		pageSize := m.computeVpHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		m.cursorLine = min(m.cursorLine+pageSize, max(0, len(m.records)-1))
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	case "ctrl+u":
		pageSize := m.computeVpHeight() / 2
		if pageSize < 1 {
			pageSize = 1
		}
		m.cursorLine = max(m.cursorLine-pageSize, 0)
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	// ── Jump to bottom ──
	case "G":
		totalLines := len(m.records)
		if totalLines == 0 {
			return m, nil
		}
		// Move logical cursor to the last line.
		m.cursorLine = totalLines - 1
		// Clamp the column to the printable length of the last line (ANSI-safe).
		m.cursorCol = min(m.cursorCol, m.lineRuneLen(m.cursorLine))
		// Anchor the viewport so the last physical line sits at the very
		// bottom of the visible screen (handled physically in syncViewportToCursor).
		m.viTopLine = m.cursorLine
		m.viForceBottom = true
		m.viSearchIdx = -1
		m.syncViewportToCursor()
		return m, nil

	// ── Prefix for multi-key sequences ──
	case "g":
		if len(m.records) > 0 {
			m.viPendingPrefix = "g"
		}
		return m, nil

	// ── Search ──
	case "/":
		m.viCmdMode = true
		m.viCmdBuf = "/"
		m.searchActive = true
		m.searchQuery = ""
		return m, nil

	case "n":
		if len(m.viSearchResults) > 0 && m.viSearchIdx >= 0 {
			m.viSearchIdx = (m.viSearchIdx + 1) % len(m.viSearchResults)
			m.cursorLine = m.viSearchResults[m.viSearchIdx]
			m.cursorCol = 0
			m.syncViewportToCursor()
		}
		return m, nil

	case "N":
		if len(m.viSearchResults) > 0 && m.viSearchIdx >= 0 {
			m.viSearchIdx--
			if m.viSearchIdx < 0 {
				m.viSearchIdx = len(m.viSearchResults) - 1
			}
			m.cursorLine = m.viSearchResults[m.viSearchIdx]
			m.cursorCol = 0
			m.syncViewportToCursor()
		}
		return m, nil

	// ── Visual selection (character-level) ──
	case "v":
		if m.viModeState == ViVisual {
			m.viModeState = ViNormal
			m.visualStartLine = 0
			m.visualStartCol = 0
		} else {
			m.viModeState = ViVisual
			m.visualStartLine = m.cursorLine
			m.visualStartCol = m.cursorCol
		}
		m.refreshViewportContent()
		return m, nil

	// ── Yank (copy selected text to clipboard) ──
	case "y":
		if m.viModeState == ViVisual {
			m.yankSelection()
			m.viModeState = ViNormal
			m.visualStartLine = 0
			m.visualStartCol = 0
		}
		m.refreshViewportContent()
		return m, nil

	// ── Command-line entry ──
	case ":":
		m.viCmdMode = true
		m.viCmdBuf = ":"
		return m, nil

	// ── Scrolling with arrow keys / pgup/pgdn in viewport ──
	case "up", "ctrl+y":
		var vpCmd tea.Cmd
		if m.Ready {
			m.Viewport, vpCmd = m.Viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
			m.userIsScrollingUp = true
		}
		return m, vpCmd

	case "down", "ctrl+e":
		var vpCmd tea.Cmd
		if m.Ready {
			m.Viewport, vpCmd = m.Viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
		}
		return m, vpCmd

	// ── Space: snap viewport to cursor ──
	case " ":
		m.syncViewportToCursor()
		return m, nil
	}

	// ── Handle key type-based fallthrough ──
	return m, nil
}

// handleViCmdInput processes input within vi command-line mode (: or /).
// The first character of viCmdBuf determines the mode:
//   - ":"  → vim command (q to exit, etc.)
//   - "/"  → forward search
func (m *model) handleViCmdInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		cmd := strings.TrimSpace(m.viCmdBuf)
		m.viCmdMode = false

		if strings.HasPrefix(cmd, ":") {
			sub := strings.TrimSpace(cmd[1:])
			switch sub {
			case "q", "q!", "quit", "wq", "x":
				m.exitViMode()
			}
			return m, nil
		}

		if strings.HasPrefix(cmd, "/") {
			query := cmd[1:]
			m.searchActive = false
			m.searchQuery = query
			m.performSearch(query, false)
			return m, nil
		}

		return m, nil

	case tea.KeyEscape:
		m.viCmdMode = false
		m.searchActive = false
		m.searchQuery = ""
		m.viCmdBuf = ""
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.viCmdBuf) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.viCmdBuf)
			m.viCmdBuf = m.viCmdBuf[:len(m.viCmdBuf)-size]
		}
		return m, nil

	case tea.KeyRunes:
		m.viCmdBuf += string(msg.Runes)
		return m, nil

	default:
		return m, nil
	}
}

// performSearch scans m.records forward from cursorLine looking for a
// case-insensitive substring match. If reverse is true, scans backward.
func (m *model) performSearch(query string, reverse bool) {
	m.viSearchResults = nil
	m.viSearchIdx = -1

	if query == "" || len(m.records) == 0 {
		return
	}

	lowerQuery := strings.ToLower(query)
	start := m.cursorLine
	n := len(m.records)

	for i := 0; i < n; i++ {
		idx := (start + i) % n
		text := m.records[idx].text
		if strings.Contains(strings.ToLower(text), lowerQuery) {
			m.viSearchResults = append(m.viSearchResults, idx)
		}
	}

	if len(m.viSearchResults) > 0 {
		m.viSearchIdx = 0
		m.cursorLine = m.viSearchResults[0]
		m.syncViewportToCursor()
	}
}

// yankSelection copies the character-level visual selection to the system
// clipboard. It extracts rune-precise slices from the underlying text records
// (not from rendered view strings) to avoid any buffer truncation or multi-byte
// UTF-8 splitting.
func (m *model) yankSelection() {
	sLine, sCol := m.visualStartLine, m.visualStartCol
	eLine, eCol := m.cursorLine, m.cursorCol

	// Normalize: ensure start ≤ end in (line, col) tuple space
	if sLine > eLine || (sLine == eLine && sCol > eCol) {
		sLine, eLine = eLine, sLine
		sCol, eCol = eCol, sCol
	}

	var buf strings.Builder
	if sLine == eLine {
		// Single shared line: slice between sCol and eCol (inclusive of eCol)
		runes := []rune(m.records[sLine].text)
		endCol := eCol + 1
		if endCol > len(runes) {
			endCol = len(runes)
		}
		if sCol < endCol {
			buf.WriteString(string(runes[sCol:endCol]))
		}
	} else {
		for i := sLine; i <= eLine && i < len(m.records); i++ {
			runes := []rune(m.records[i].text)
			switch i {
			case sLine:
				// First line of multi-line: from sCol to end (inclusive)
				if sCol < len(runes) {
					buf.WriteString(string(runes[sCol:]))
				}
			case eLine:
				// Last line of multi-line: from start to eCol (inclusive)
				endCol := eCol + 1
				if endCol > len(runes) {
					endCol = len(runes)
				}
				if endCol > 0 {
					buf.WriteString(string(runes[:endCol]))
				}
			default:
				// Fully enclosed line: entire text
				buf.WriteString(m.records[i].text)
			}

			if i < eLine {
				buf.WriteString("\n")
			}
		}
	}

	text := buf.String()
	if text == "" {
		return
	}
	if err := clipboard.WriteAll(text); err != nil {
		m.push(roleSystem, mutedStyle.Render("clipboard error: "+err.Error()))
		m.refreshViewportContent()
	}
}

// syncViewportToCursor scrolls the viewport to bring the cursor line into
// view using viTopLine as the logical scroll anchor. Four constraints:
//  1. Vertical: if cursorLine < viTopLine, scroll viTopLine up to cursorLine.
//  2. Height: if cursorLine >= viTopLine+vpHeight, scroll viTopLine down.
//  3. Horizontal: cursorCol is clamped to the destination line length.
//
// syncViewportToCursor scrolls the viewport to bring the cursor line into
// view using viTopLine as the logical scroll anchor. Because chat records wrap
// into multiple physical terminal lines, all offset math is performed in
// PHYSICAL line space (cumulative rendered line counts) rather than raw record
// indexes, so wrapped lines never desync the viewport. Four constraints:
//  1. Vertical: if the cursor's physical row is above the viewport, scroll up.
//  2. Height: if the cursor's physical row is below the viewport, scroll down.
//  3. Horizontal: cursorCol is clamped to the printable length of the line.
//  4. TUI Sync: YOffset is computed from viTopLine via cumulative physical line
//     counts, and explicit gg/G anchors override with a definitive offset.
func (m *model) syncViewportToCursor() {
	if len(m.records) == 0 {
		return
	}

	vpHeight := m.computeVpHeight()
	if vpHeight < 1 {
		vpHeight = 1
	}

	// Horizontal safe-guard: ensure cursorCol is within the printable length
	// of the cursor line (ANSI-safe — operates on stripped text).
	lineLen := m.lineRuneLen(m.cursorLine)
	if m.cursorCol > lineLen {
		m.cursorCol = max(0, lineLen-1)
	}

	// Build cumulative physical (wrapped) line offsets across all records.
	n := len(m.records)
	phys := make([]int, n+1)
	for i := 0; i < n; i++ {
		phys[i+1] = phys[i] + m.renderedLineCount(m.records[i])
	}
	totalPhys := phys[n]

	// Convert the logical viTopLine anchor into a physical YOffset baseline.
	if m.viTopLine < 0 {
		m.viTopLine = 0
	}
	if m.viTopLine >= n {
		m.viTopLine = n - 1
	}
	yOffset := phys[m.viTopLine]

	// Physical row range occupied by the cursor line (it may wrap).
	cursorStart := phys[m.cursorLine]
	cursorEnd := phys[m.cursorLine+1] - 1

	// Keep the cursor visible: scroll within the physical coordinate space.
	if cursorEnd >= yOffset+vpHeight {
		yOffset = cursorEnd - vpHeight + 1
		m.viTopLine = m.logicalLineAtPhysical(phys, yOffset)
	}
	if cursorStart < yOffset {
		yOffset = cursorStart
		m.viTopLine = m.cursorLine
	}

	// Explicit anchoring requested by gg / G — overrides the window logic with
	// a definitive physical offset.
	if m.viForceTop {
		yOffset = 0
		m.viTopLine = 0
		m.viForceTop = false
	}
	if m.viForceBottom {
		yOffset = max(0, totalPhys-vpHeight)
		m.viTopLine = m.logicalLineAtPhysical(phys, yOffset)
		m.viForceBottom = false
	}

	// Clamp YOffset so the viewport never overscrolls.
	maxOffset := max(0, totalPhys-vpHeight)
	if yOffset < 0 {
		yOffset = 0
	}
	if yOffset > maxOffset {
		yOffset = maxOffset
	}

	m.refreshViewportContent()

	// Sync Bubble Tea viewport YOffset using cumulative physical line counts.
	if m.Ready {
		m.Viewport.YOffset = yOffset
	}
}

// logicalLineAtPhysical returns the logical record index whose physical line
// range contains the given physical row offset.
func (m *model) logicalLineAtPhysical(phys []int, yOffset int) int {
	for i := 0; i < len(phys)-1; i++ {
		if yOffset < phys[i+1] {
			return i
		}
	}
	return max(0, len(phys)-2)
}

// sanitizeFinalContent strips tool execution artifacts and telemetry markers
// from the LLM output before it reaches the JSON parser or render pipeline.
// Lines matching known error/telemetry patterns are removed when they appear
// as leading or trailing noise around the actual LLM response payload.
func sanitizeFinalContent(content string) string {
	lines := strings.Split(content, "\n")
	var clean []string
	inPayload := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inPayload {
			if trimmed == "" || strings.HasPrefix(trimmed, "[FAIL]") ||
				strings.HasPrefix(trimmed, "[ OK ]") ||
				strings.HasPrefix(trimmed, "INFO:") ||
				strings.HasPrefix(trimmed, "WARN:") ||
				strings.HasPrefix(trimmed, "ERROR:") {
				continue
			}
			inPayload = true
		}

		clean = append(clean, line)
	}

	result := strings.Join(clean, "\n")
	return strings.TrimSpace(result)
}

// synthesizeBuildTodosFromMutation creates pending todo strings from the
// original investigation content when the deadlock-guard short-circuits
// from /investigate to /build. Each todo is a FILE_MUTATE task that
// the build engine can execute immediately without a separate /plan step.
// Returns nil when the content is empty or does not contain mutation intent.
func synthesizeBuildTodosFromMutation(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	lower := strings.ToLower(content)
	var todos []string

	// Detect static website creation requests and create the standard
	// trio of files (HTML, CSS, JS).
	if strings.Contains(lower, "static website") ||
		strings.Contains(lower, "html") && strings.Contains(lower, "css") && strings.Contains(lower, "js") {
		todos = append(todos, "\uf05c [FILE_MUTATE] index.html — Create main HTML page with semantic structure, meta tags, and linked CSS/JS")
		todos = append(todos, "\uf05c [FILE_MUTATE] styles.css — Create responsive stylesheet with modern CSS layout and styling")
		todos = append(todos, "\uf05c [FILE_MUTATE] script.js — Create JavaScript file for interactive functionality")
		return todos
	}

	// Generic fallback: single FILE_MUTATE task captures the full mutation intent.
	// NOTE: target is intentionally a descriptive label, NOT a file path — the
	// build engine will resolve the actual file path via LLM synthesis. Using
	// placeholder strings like "workspace" as the target is FORBIDDEN because
	// the build parser would interpret it as a literal file path.
	todos = append(todos, "\uf05c [FILE_MUTATE] [resolve] — Create or modify files as described: "+strings.TrimSpace(content))
	return todos
}
