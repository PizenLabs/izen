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
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/providers"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui/status"
	verification "github.com/PizenLabs/izen/internal/verification"
	"github.com/PizenLabs/izen/pkg/control"
)

// stripModePrefix removes a leading mode command (e.g. "/plan", "/build",
// "/investigate") from an input so the raw user intent is extracted for
// context decisions. Inputs without a mode prefix are returned trimmed.
func stripModePrefix(s string) string {
	s = strings.TrimSpace(s)
	for _, cmd := range []string{"/plan", "/build", "/investigate", "/objective"} {
		if strings.HasPrefix(strings.ToLower(s), cmd) {
			s = strings.TrimSpace(s[len(cmd):])
		}
	}
	return strings.TrimSpace(s)
}

// Init initializes the spinner tick, pro tip rotation, and text input blink.
func (m *model) Init() tea.Cmd {
	m.currentTip = allTips[0]
	m.lastTipRotation = time.Now()
	m.proTipIndex = 0
	if m.initStage != initNone && m.initStage != initComplete {
		return tea.Batch(m.smoothStreamTickCmd(), m.proTipTickCmd(), m.configLoadedCmd())
	}
	cmds := []tea.Cmd{
		m.smoothStreamTickCmd(),
		m.proTipTickCmd(),
		m.ti.Focus(),
		m.initSessionStartCheckpoint,
		m.configLoadedCmd(),
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

	// ── DEFENSIVE WORKSPACE GUARD ──────────────────────────────────────────
	// Reconcile the in-memory initStage with the on-disk workspace state on
	// every update. A completed initStage that is no longer backed by disk
	// state (e.g. the user deleted .izen/ mid-session) must either self-heal
	// or route back to the onboarding wizard — it can never be left rendering
	// a frozen welcome header with no interactive input bar. This runs before
	// any key routing so the deadlock state is dissolved on the very first
	// event after the workspace disappears.
	if m.initStage == initComplete && !m.isProjectInitialized() {
		if !m.selfHealWorkspace() {
			m.initStage = initNone
			m.ti.Blur()
		}
	}

	// ── QUIT-CONFIRM MODAL INTERCEPT ─────────────────────────────────────
	// While the exit-safety dialog is open, every key is routed to the modal
	// handler: input is frozen and only [ No ]/[ Yes ] navigation, cancel, and
	// confirm keys are honored. This runs before any other key routing so a
	// stray keystroke can never dismiss or bypass the modal.
	if m.pendingQuitConfirm {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			return m, m.handleQuitConfirmKey(keyMsg)
		}
	}

	// ── MOUSE SELECTION ESC CANCEL (presentation-only) ─────────────────
	// Esc clears an active mouse drag selection without affecting execution.
	// It is handled before the emergency hatch so a selection is dismissed
	// without aborting a concurrent streaming/tool run.
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyEsc && m.mouseSel.Active {
		m.mouseSel = mouseSelection{}
		m.frozenFullHitRows = nil
		m.frozenViewportStr = ""
		m.frozenRecords = nil
		m.refreshViewportContent()
		return m, nil
	}

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
			// Unified Ctrl+C protocol: first press cancels the active
			// operation (or dismisses the ambiguity card) gracefully; a
			// second press while a cancellation is in progress hard-exits
			// with status 130. Falls back to the legacy interrupt path when
			// no cancellation applies.
			if handled, cmd := m.handleCtrlC(); handled {
				return m, cmd
			}
			if m.state == StateProcessing || m.state == StateAwaitingApproval ||
				m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.planPending {
				return m.handleEmergencyInterrupt("ctrl-c")
			}
		case tea.KeyEsc:
			// Esc aborts any active review OR investigate pipeline (manual
			// /review, /investigate, or a $test/$run/$log sub-command that
			// holds reviewRunning) by cancelling the registered background
			// context and returning focus to the input bar — never killing
			// the app.
			if m.state == StateProcessing || m.planPending || m.reviewRunning || m.investigateRunning {
				return m.handleEmergencyInterrupt("escape")
			}
		case tea.KeyCtrlD:
			if m.state == StateProcessing {
				return m.handleEmergencyInterrupt("ctrl-d")
			}
		}
	}

	// ── OS-SIGNAL INTERRUPT ──────────────────────────────────────────────
	// Bubble Tea forwards an OS SIGINT (non-TTY input) as tea.InterruptMsg;
	// the root signal bridge forwards SIGINT/SIGTERM as interruptSignalMsg.
	// Both route through the same graceful Ctrl+C cancellation protocol.
	if interruptMsg, ok := msg.(tea.InterruptMsg); ok {
		if handled, cmd := m.handleCtrlC(); handled {
			return m, cmd
		}
		_ = interruptMsg
		return m, nil
	}
	if sigMsg, ok := msg.(interruptSignalMsg); ok {
		if handled, cmd := m.handleCtrlC(); handled {
			return m, cmd
		}
		_ = sigMsg
		return m, nil
	}

	// ── OPERATION WATCHDOG TICK ──────────────────────────────────────────
	// The stuck-detection loop runs on the UI goroutine only: it reports —
	// never kills — operations that have made no meaningful progress for a
	// long window, and self-terminates when the runtime is idle.
	if w, ok := msg.(watchdogMsg); ok {
		return m, m.handleWatchdog(w)
	}

	// ── HARD KEYBOARD INTERCEPT: Approval/Processing states bypass all sub-components ──
	// Viewport scroll keys are exempt during StateProcessing so the user can
	// inspect history while streaming/tool execution - presentation-only.
	// AwaitingApproval keeps its dedicated proposal-diff scrolling.
	switch m.state { //nolint:staticcheck
	case StateProcessing:
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.Type {
			case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
				// Allow viewport navigation even while processing.
			default:
				return m.handleKey(keyMsg)
			}
		}
	case StateAwaitingApproval, StateHotfixAmbiguous:
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
				m.gotoBottomIfAllowed()
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
				cmd := m.enterViMode()
				return m, cmd
			}
		}
	} else if _, ok := msg.(tea.KeyMsg); ok {
		m.escCount = 0
	}

	// ── VI-MODE INTERCEPT: route all key events to the vi-mode handler ──
	if keyMsg, ok := msg.(tea.KeyMsg); ok && m.inViMode {
		return m.handleViModeKey(keyMsg)
	}

	// ── SESSION PICKER ROUTING: intercept key events during session selection ──
	if m.showSessionPicker && m.sessionPicker != nil {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.sessionPicker, cmd = m.sessionPicker.Update(msg)
			return m, cmd
		}
		switch msg.(type) {
		case sessionPickerResumeMsg, sessionPickerNewMsg, sessionPickerRenameMsg, sessionPickerArchiveMsg, sessionPickerDeleteMsg, sessionPickerCompactMsg, sessionPickerCloseMsg:
			// fall through to main switch
		case tea.WindowSizeMsg:
			// fall through to main switch (resize adapts modal via render)
		default:
			return m, nil
		}
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

	case configLoadedMsg:
		// Defensive workspace loader result (dispatched once per startup from
		// Init). Reconciles initStage with the on-disk workspace state so the
		// UI always reaches the interactive input bar or the onboarding
		// wizard — never a frozen, header-only screen.
		m.handleConfigLoaded(msg)
		return m, nil

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

	case sessionPickerResumeMsg:
		return m, m.handleSessionPickerResume(msg.slot)
	case sessionPickerNewMsg:
		return m, m.handleSessionPickerNew()
	case sessionPickerRenameMsg:
		return m, m.handleSessionPickerRename(msg.slot, msg.title)
	case sessionPickerArchiveMsg:
		return m, m.handleSessionPickerArchive(msg.slot)
	case sessionPickerDeleteMsg:
		return m, m.handleSessionPickerDelete(msg.slot)
	case sessionPickerCompactMsg:
		return m, m.handleSessionPickerCompact(msg.slot)
	case sessionPickerCloseMsg:
		return m, m.closeSessionPicker()

	case runtimeResultMsg:
		// Outcome of a RuntimeCommand executed through the facade. Only
		// errors are surfaced; successful commands rendered their own
		// presentation events.
		if msg.err != nil {
			m.push(roleError, fmt.Sprintf("command %s failed: %v", msg.typ, msg.err))
			m.refreshViewportContent()
			if m.Ready && !m.userIsScrollingUp {
				m.gotoBottomIfAllowed()
			}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		padding := 4
		w := msg.Width - padding
		if w < 20 {
			w = 20
		}
		m.wrapWidth = w
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

		// Full layout re-hydration on resize: clear and rebuild document layout
		// using the updated wrapWidth, then re-anchor scroll offset.
		m.rebuildDocumentLayout()

		// Flush and rebuild hitmap immediately on resize; clear any frozen
		// drag snapshot so new dimensions are reflected.
		m.frozenFullHitRows = nil
		m.frozenViewportStr = ""
		m.frozenRecords = nil
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
			m.gotoBottomIfAllowed()
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
			m.shellRunning || m.state == StateProcessing || m.state == StateAwaitingApproval ||
			m.shimmerActive || m.autonomousActive
		if hasActiveWork {
			// Keep the activity heartbeat fresh while any execution indicator
			// is live. The idle-gate in the reconcile block above relies on
			// this to avoid prematurely force-clearing a healthy spinner.
			m.lastAgentActivity = time.Now()
			// 1. Physically advance the spinner frame.
			m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
			// 2. Repaint the viewport from the live stream/agent buffers.
			// Layout freezing: while dragging, background ticks must not
			// trigger re-layout that would shift rows under the cursor.
			if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.state == StateProcessing || m.shellRunning {
				if !m.mouseSel.Dragging {
					m.refreshViewportContent()
				}
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

	case agentDoneMsg:
		m.clearBusyFlags()
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, flush

	case investigateResultMsg:
		m.lastAgentActivity = time.Now()
		// GUARANTEED LIFECYCLE PATTERN: universally reset every transient
		// processing flag (including investigateRunning) so the spinner can
		// never be orphaned on a failed, timed-out, or aborted investigation —
		// then re-derive the presentation state so a stale StateProcessing
		// derived during the run is released and the viewport returns to
		// interactive chat. Pending-approval overrides.
		m.clearBusyFlags()
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		m.syncUIState()
		if msg.err != nil {
			m.push(roleError, "investigation error: "+providers.SanitizeAPIError(msg.err))
			// PERSISTENT NAVIGATION CHIPS (BUG 1): even on failure the user
			// must never be left on a dead viewport. Surface Re-investigate
			// so the diagnostic loop can be retried.
			m.currentResult = investigateResultActions()
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		cmds = append(cmds, m.flushPendingRecords())
		return m, tea.Batch(cmds...)

	case planResultMsg:
		// Terminal handler for the asynchronous PlanEngine synthesis. Only here
		// do we stage tasks and clear streaming state — never while the LLM call
		// is in flight (that would re-block the event loop).
		m.planPending = false
		m.planStartedAt = time.Time{}

		// GUARANTEED LIFECYCLE PATTERN: universally reset every transient
		// processing flag first so the spinner can never freeze, regardless of
		// which branch below we take, then re-derive the presentation state
		// from the cleared flags: a stale StateProcessing derived during
		// synthesis (e.g. via a phase-change event while agentRunning was
		// true) must be released here so the viewport returns to interactive
		// chat and Alt+P / Alt+R respond immediately. Pending-approval always
		// overrides if a gate is set.
		m.clearBusyFlags()
		m.reconcileSpinner()
		m.syncUIState()

		if msg.Err != nil {
			m.push(roleError, fmt.Sprintf("Failed to synthesize plan from ledger: %v", msg.Err))
			// Deterministic pipeline rejections (PolicyEngine escalation /
			// lowering failure) must surface their explicit reason in the
			// status-bar footer.
			if msg.Microkernel || msg.IntentCompiler {
				m.setToast(msg.Err.Error())
			}
			// Retain a baseline Action Chip so the user is never left with a
			// dead viewport and no buttons — they can re-investigate the failure.
			m.currentResult = failureResult(m.handoffLedgerContent)
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
			flush := m.flushPendingRecords()
			return m, flush
		}

		// ── TOKEN ACCOUNTING ────────────────────────────────────────────
		// The provider-reported usage of plan synthesis is dispatched as a
		// TokenUsageMsg (see the final return of this case) so the TokenUsageMsg
		// handler accumulates it into the session counters and refreshes the
		// footer. The plan engine records usage even when the response was
		// truncated by the completion ceiling (finish_reason: "length"), so the
		// token figures never silently vanish on a truncated plan.

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
					m.gotoBottomIfAllowed()
					flush := m.flushPendingRecords()
					return m, flush
				}
			}
		}

		// ── PLAN INTENT CAPTURE (TaskContext hygiene) ──────────────────
		// Record the raw user intent that produced this plan so /build can
		// reconstruct the rewrite context WITHOUT reading obsolete workspace
		// file contents. The current prompt (mode prefix stripped) is
		// preferred; the persisted objective is the fallback.
		if intent := stripModePrefix(m.currentPrompt); intent != "" {
			m.lastPlanIntent = intent
		} else if m.sess != nil {
			m.lastPlanIntent = m.sess.ObjectiveIntent()
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
			m.setToast(fmt.Sprintf("Microkernel plan: %d deterministic task(s), no model call", len(msg.Tasks)))
		}
		if msg.IntentCompiler {
			// IR-driven intent compiler plans consumed no model tokens.
			m.setToast(fmt.Sprintf("Intent compiler plan: %d task(s) from IR lowerer — no model call", len(msg.Tasks)))
		}
		// Render the staged task list into the viewport so the developer can
		// see exactly what /build will execute — Principal Engineer format.
		// Use [ ] checkbox markers for each pending task to create an
		// interactive todo checklist look in the TUI.
		// Also expose the plan approval action chips — the user must explicitly
		// approve the plan before /build execution begins.
		// Fast-track plans are auto-approved — execution continues IMMEDIATELY
		// (continuous execution: no artificial chip gate for an already
		// authorized, deterministic plan). The plan is still rendered first so
		// the user sees what /build is executing, then runBuildCmd picks up the
		// staged tasks in the same turn.
		// Non-fast-track plans show the explicit approval gate.
		if msg.IsFastTrack {
			m.currentResult = nil
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
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		base := []tea.Cmd{flush, m.tokenUsageCmd(msg.TokenInput, msg.TokenOutput)}

		// ── CONTINUOUS EXECUTION: auto-approved fast-track continues to build ──
		// A fast-track plan is pre-approved and deterministic (the compressor /
		// intent compiler already resolved the target). The build executor
		// starts in the same turn — no second /build, no chip press, no
		// artificial pause between plan and execution. The mutation approval
		// gate (patch proposal) still protects every byte that reaches disk.
		//
		// SINGLE-DISPATCH GUARD: setMode's own auto-trigger already dispatches
		// build execution when a handoff payload exists (buildHandoffTriggerContent
		// → handleMessageContent → runBuildCmd), so we NEVER pair setMode with an
		// explicit runBuildCmd in the same batch. runBuildCmd is dispatched
		// directly only when already in /build (no transition) or when the
		// handoff auto-trigger cannot produce a payload.
		if msg.IsFastTrack {
			if m.resolver.Current() != modes.ModeBuild {
				m.modeChangeAuthorized = true
				if m.buildHandoffTriggerContent(modes.ModeBuild) != "" {
					base = append(base, m.setMode(modes.ModeBuild))
				} else {
					base = append(base, m.setMode(modes.ModeBuild), m.runStagedBuildViaRuntime())
				}
			} else {
				base = append(base, m.runStagedBuildViaRuntime())
			}
			base = append(base, m.smoothStreamTickCmd(), m.shimmerTickCmd())
		}
		return m, tea.Batch(base...)

	case graphBuiltMsg:
		m.clearBusyFlags()
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "graph indexing failed: "+msg.err.Error())
			m.indexingStatus = "error"
			m.pendingArchArgs = ""
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
		} else {
			m.pendingArchArgs = ""
		}
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
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
		// GUARANTEED LIFECYCLE PATTERN: universally reset every transient
		// processing flag so the spinner can never be orphaned on a failed or
		// aborted review, then re-derive the presentation state so a stale
		// StateProcessing derived during the run (e.g. via a phase-change
		// event arriving while reviewRunning was true) is released here and
		// the "Processing file mutations..." spinner can never stay up.
		// Pending-approval always overrides if a gate is set.
		m.clearBusyFlags()
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.stopShimmer()
		m.syncUIState()
		if msg.err != nil {
			m.push(roleError, "review error: "+providers.SanitizeAPIError(msg.err))
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, flush

	case testResultMsg:
		m.clearBusyFlags()
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
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, flush

	case executionResultMsg:
		// ── RUNTIME EXECUTOR RESULT (authority migration, Steps 2-4) ──
		// The RuntimeExecutor returned the terminal outcome of an execution
		// request. The runtime owned every provider invocation, patch creation,
		// mutation and verification; the UI projects the result and renders the
		// canonical events (MutationCompleted / VerificationCompleted /
		// ExecutionFinished) it already receives on the bus. No provider or
		// PatchManager call lives on this path anymore.
		return m.executionResultUpdate(msg)

	case gatedExecutionMsg:
		// ── UNIFIED INTENT GATEWAY RESULT ─────────────────────────────
		// Every execution-bearing user action (bare text, $prompt, $hot)
		// produces a gated execution; the update loop projects the runtime
		// result through the same executionResultUpdate projection.
		return m.handleGatedExecution(msg)

	case autonomousRunMsg:
		// ── PRODUCTION AUTONOMOUS DRIVER OUTCOME (Phase 6) ───────────
		// The bounded driver Run/Resume/Abort returned: a terminal outcome
		// (completed/aborted) is projected; a nil term means the run parked at
		// a human boundary (approve/clarify/inform) and the boundary card
		// renders for the operator's decision.
		return m, m.handleAutonomousRun(msg)

	case TokenUsageMsg:
		// TokenUsageMsg is dispatched on EVERY async execution exit path —
		// success, parse error, truncation, or abort — so the status bar
		// footer never reports 0 tokens after a provider has consumed tokens.
		// Accumulate into the session counters and force an immediate footer
		// refresh via syncUIState.
		if msg.PromptTokens > 0 || msg.CompletionTokens > 0 {
			m.commitTokenUsage(msg.PromptTokens, msg.CompletionTokens)
		}
		if msg.Known {
			// The provider reported usage this turn (even zero): the footer
			// may now render a real "0 tok" instead of "usage unknown".
			m.markUsageKnown()
		}
		m.syncUIState()
		return m, nil

	case ThoughtBufferUpdatedMsg:
		// Full stream transparency: one raw LLM chunk (reasoning or content)
		// appended to the active ThinkingBuffer in real time so the Ctrl+O
		// thought drawer renders the model's raw stream live — 100% of the
		// output is retained, never discarded. Done collapses the block to its
		// summary once the stream/hotfix completes.
		if msg.Content != "" {
			if m.thinkingBuffer == nil {
				m.thinkingBuffer = NewThinkingBuffer()
			}
			m.thinkingBuffer.Append(msg.Content)
		}
		if msg.Done {
			if m.thinkingBuffer != nil {
				m.thinkingBuffer.MarkComplete()
			}
		}
		m.refreshViewportContent()
		if m.Ready && !m.userIsScrollingUp {
			m.gotoBottomIfAllowed()
		}
		return m, nil

	case ReasoningChunkMsg:
		// Sub-task reasoning trace: one thinking chunk from an async executor
		// stream (autonomous DAG_EXECUTING, gated $prompt/$hot) appended to the
		// active ThinkingBuffer so Ctrl+O expands a live thought drawer while
		// the run executes — never an empty overlay.
		if msg.Chunk == "" {
			return m, nil
		}
		if m.thinkingBuffer == nil {
			m.thinkingBuffer = NewThinkingBuffer()
		}
		m.thinkingBuffer.Append(msg.Chunk)
		m.refreshViewportContent()
		if m.Ready && !m.userIsScrollingUp {
			m.gotoBottomIfAllowed()
		}
		return m, nil

	case buildResultMsg:
		// GUARANTEED LIFECYCLE PATTERN: universally reset every transient
		// processing flag so the spinner can never be orphaned on a failed or
		// aborted build, then re-derive the presentation state from the cleared
		// flags so a stale StateProcessing derived during the build is
		// released. Pending-approval overrides (e.g. a queued proposal
		// awaiting authorization).
		//
		// HOTFIX ORDERING: for a $hot apply result the flags are NOT cleared
		// here — the hotfix branch below assembles the full terminal result
		// records FIRST and releases the processing flags only AFTER the
		// "Applied hotfix patch to <file>" / "Pipeline PAUSED" message is
		// rendered, so the mutation spinner persists until the result frame.
		if !m.hotfixActive {
			m.clearBusyFlags()
		}
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = msg.exitCode != 0
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

		// ── FIX 2: Freeze state machine on task failure ───────────────
		// If a step fails, the overall plan status must be STALLED. It is
		// strictly forbidden to advance the internal task index pointer.
		// All remaining idle tasks are marked "stalled" so subsequent
		// /build invocations see them blocked rather than silently
		// advancing into corrupted state.
		if msg.exitCode != 0 {
			// ── ROLLBACK AUTHORITY ──────────────────────────────────
			// Rollback authority is owned by the RuntimeExecutor boundary:
			// its MutationSet restores shadow backups inside Approve/Reject.
			// The UI records the halt state only.
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
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
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, flush

	case objectiveAnalyzedMsg:
		if msg.err != nil {
			m.setToast("Objective analysis failed: " + msg.err.Error())
			if m.sess.ObjectiveState != nil {
				m.sess.ObjectiveState.CurrentStatus = domain.ObjectiveIdle
				m.sess.SetObjectiveState(m.sess.ObjectiveState)
				_ = m.sess.Save()
			}
			return m, nil
		}
		if msg.objective == nil {
			m.setToast("Objective analysis failed: empty objective result.")
			return m, nil
		}
		m.sess.SetObjectiveState(msg.objective)
		_ = m.sess.Save()
		if msg.objective.TokenBudget.RequiresApproval {
			m.setToast("Objective needs manual approval. Run /objective approve.")
		} else {
			m.setToast("Objective planned and active.")
		}
		return m, nil

	case archDoneMsg:
		for _, line := range strings.Split(msg.Content, "\n") {
			m.push(roleSystem, infoStyle.Render(line))
		}
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return m, nil

	case mutationResultMsg:
		// OPERATION LIFECYCLE: the zero-patch short-circuit returns
		// mutationResultMsg directly from proposeBuildPatch (skipping
		// buildProposalReadyMsg), so the build-patch operation begun in
		// handleBuildRun must be finalized here too. On the normal apply path
		// the operation was already finalized at proposal-ready — this is an
		// idempotent no-op.
		m.finalizeBuildOperation(msg.err)
		// ── AUTHORITATIVE PROVIDER USAGE ─────────────────────────────
		// The zero-patch short-circuit and the apply paths carry the provider
		// usage of the call that produced the result. Commit it so the footer
		// reflects the real tokens the provider consumed even when the task
		// ended in "nochange"/"skipped" — never a silent drop to 0.
		if msg.usageKnown {
			m.commitTokenUsage(msg.TokenInput, msg.TokenOutput)
			m.markUsageKnown()
		}
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
			// ── RELEASE AGENT/SPINNER STATE ───────────────────────────
			// A terminal mutation result must always unwind the patching
			// spinner and restore input focus. This covers the zero-patch
			// short-circuit (proposeBuildPatch returning mutationResultMsg
			// directly) which never passes through buildProposalReadyMsg, so
			// the "Generating patch..." shimmer would otherwise hang forever.
			m.agentRunning = false
			m.agentDone = true
			m.agentLabel = ""
			m.stopShimmer()

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
				outcomeLine := fmt.Sprintf("%s %s • %s", successBannerStyle.Render("[✓]"), msg.file, msg.status)
				// ── REAL DIFF EVIDENCE (Phase 4) ──────────────────────────
				// When the runtime captured the compiled diff metrics, surface them
				// in the outcome line so the UI proves the actual patch ("+3 -1"),
				// never a vague "Edit(file)" success event.
				evidenceDetail := msg.status
				if msg.evidence != nil && msg.evidence.DiffPresent {
					outcomeLine = fmt.Sprintf("%s %s • +%d -%d",
						successBannerStyle.Render("[✓]"), msg.file, msg.evidence.DiffAdds, msg.evidence.DiffRemoves)
					evidenceDetail = fmt.Sprintf("+%d -%d", msg.evidence.DiffAdds, msg.evidence.DiffRemoves)
				}
				m.push(roleSystem, outcomeLine)
				m.createBuildCheckpoint(1)

				// Log foldable entry for the file mutation.
				// Capture the current thinking-panel content so it persists
				// in the entry even during Per-Task Fallback mode.
				thinkingContent := ""
				if m.thinkingPanel != nil {
					thinkingContent = m.thinkingPanel.String()
				}
				// ── TRUTHFUL RESULT ENTRY (Phase 4) ───────────────────────
				// The terminal result is logged with its semantic outcome. Only a
				// real filesystem mutation (changed/created) is a successful Edit;
				// nochange and skipped render neutrally, never as a green ✓ Edit.
				outcome := msg.outcome()
				mutated := outcome.MutationSucceeded()
				if !m.activitySurfaceSealed {
					m.logStore.AddFullSemantic(LogResult, msg.file, mutated, evidenceDetail, thinkingContent, "", execution.StageResult, outcome)
				}
				// ── FAST-TRACK EARLY COMPLETION ─────────────────────────
				// When a fast-track batch covered every plan target and the
				// last proposal has been applied, per-task execution is
				// redundant work on already-applied files. Complete the build
				// loop immediately instead of advancing to "executing step N"
				// (Rule "Explicit Over Implicit").
				if m.fastTrackCoversAllPlanTargets() {
					return m.completeFastTrackBuild()
				}
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
				if !m.activitySurfaceSealed {
					m.logStore.AddFullSemantic(LogResult, msg.file, false, msg.err.Error(), thinkingContent, "", execution.StageResult, msg.outcome())
				}
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
		// OPERATION LIFECYCLE: the apply-all batch operation (begun in
		// applyAllProposals) reaches its terminal outcome here. finalizeBuildOperation
		// releases ownership and stops the "Processing file mutations..."
		// spinner; idempotent when the operation was already finalized.
		m.finalizeBuildOperation(nil)
		applied := 0
		failed := 0
		for _, r := range msg.results {
			if r.err != nil {
				m.setApplyError("apply failed: " + r.err.Error())
				// Late failure from a cleared execution (sealed surface): keep
				// the error state but do not resurrect the cleared log.
				if !m.activitySurfaceSealed {
					thinkingContent := ""
					if m.thinkingPanel != nil {
						thinkingContent = m.thinkingPanel.String()
					}
					m.logStore.AddFullSemantic(LogResult, r.file, false, r.err.Error(), thinkingContent, "", execution.StageResult, r.outcome())
				}
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
			// ── TRUTHFUL RESULT ENTRY (Phase 4) ───────────────────────
			// Only a real filesystem mutation renders as a successful Edit;
			// nochange/skipped render neutrally with their outcome label.
			outcome := r.outcome()
			if !m.activitySurfaceSealed {
				m.logStore.AddFullSemantic(LogResult, r.file, outcome.MutationSucceeded(), r.status, thinkingContent, "", execution.StageResult, outcome)
			}
			applied++
		}
		m.pendingProposals = nil
		m.awaitingConfirmation = false
		m.acceptAll = false
		m.ti.Focus()
		m.resolveApprovalState()
		m.recalcViewportHeight()
		// ── FAST-TRACK FULL COVERAGE ──────────────────────────────────
		// When the apply-all batch covered every plan target, drain the
		// queue deterministically so a subsequent /build never re-executes
		// tasks the fast-track batch already completed.
		if m.fastTrackCoversAllPlanTargets() {
			m.markAllPlanTasksCompleted()
			m.fastTrackTargets = nil
		}
		var testCmd tea.Cmd
		switch {
		case applied > 0 && failed == 0:
			// Transaction commit authority is owned by the RuntimeExecutor
			// approval boundary — no UI-owned commit runs here.
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
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, flush

	case shellChunkMsg:
		// ── LIVE SHELL OUTPUT ───────────────────────────────────────
		// Stream each stdout/stderr chunk into the running exec entry of the
		// activity tree so the output grows in real-time (visible via Ctrl+O
		// expansion). The heartbeat keeps the idle-gate hang detector from
		// force-clearing the shell spinner.
		if !m.activitySurfaceSealed && m.activityTree != nil {
			m.activityTree.AppendExecOutput(msg.text)
		}
		m.lastAgentActivity = time.Now()
		if m.Ready {
			m.refreshViewportContent()
		}
		if !m.userIsScrollingUp {
			m.gotoBottomIfAllowed()
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
		if !m.activitySurfaceSealed && m.activityTree != nil {
			m.activityTree.CompleteLastExec(msg.exitCode, msg.elapsed)
		}
		if !m.activitySurfaceSealed && msg.err != nil && msg.exitCode != 0 {
			m.push(roleSystem, dimmedStyle.Render(fmt.Sprintf(
				"shell exited %d (%s)", msg.exitCode, formatElapsed(msg.elapsed))))
		}
		m.refreshViewportContent()
		flush := m.flushPendingRecords()
		return m, flush

	case repaintTickMsg:
		// ── SINGLE-FLIGHT 30FPS REPAINT GATE ──────────────────────────
		// Incoming tokens were appended to docLayout in memory instantly; this
		// tick renders exactly one visible frame and resets the gate. It is
		// NEVER chained recursively — a fresh repaint is only scheduled when
		// new tokens actually arrive.
		m.refreshScheduled = false
		m.refreshViewportContent()
		// ── NO-TOKEN-LEFT-BEHIND GUARANTEE ──────────────────────────
		// If new streaming tokens were spliced into docLayout while this
		// repaint was armed (repaintSeq advanced past the captured value), the
		// frame just rendered is one emission stale. Re-arm exactly ONE final
		// repaint so no token buffer is ever left unrendered in memory; the
		// sequence stops advancing once the stream ends, so the gate cannot
		// cascade indefinitely.
		if m.repaintSeq != m.repaintScheduledAt {
			if repaint := m.scheduleRepaint(); repaint != nil {
				return m, repaint
			}
		}
		return m, nil

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
		//
		// frameAdvanced tracks whether any visual state changed this tick
		// (new token OR spinner-frame advance); it drives the single-flight
		// 30FPS repaint gate so an idle loop never schedules repaints.
		frameAdvanced := false
		backgroundActive := m.streaming || m.agentRunning || m.reviewRunning ||
			m.pipelineRunning || m.planPending || m.shellRunning || m.autonomousActive
		if backgroundActive {
			m.lastAgentActivity = time.Now()
			// GUARD: when the shimmer is active, the shimmerTickCmd already
			// advances m.spinnerFrame at 50ms cadence. Skip the smooth-stream
			// advance to prevent double-incrementing the snowflake frames.
			if !m.shimmerActive && time.Since(m.lastSpinnerAdvance) >= 100*time.Millisecond {
				m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
				m.lastSpinnerAdvance = time.Now()
				frameAdvanced = true
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
			// Tokens are appended to docLayout in memory INSTANTLY (the
			// streaming tail is spliced inside emitVisibleContent); the
			// viewport repaint is throttled separately to 30FPS.
			m.emitVisibleContent(emitContent)
			frameAdvanced = true
		}

		// ── SINGLE-FLIGHT 30FPS REPAINT ───────────────────────────────
		// Do NOT force a refresh here: refreshViewportContent is gated behind
		// scheduleRepaint (one repaintTickMsg at a time, never chained). Only
		// visual changes (a new token or a spinner-frame advance) request a
		// repaint; an idle tick loop re-schedules itself but never repaints.
		if m.Ready && frameAdvanced {
			if repaint := m.scheduleRepaint(); repaint != nil {
				m.streamTickActive = true
				return m, tea.Batch(m.smoothStreamTickCmd(), repaint)
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
		// stream flags. m.autonomousActive keeps the loop armed through the
		// whole driver run/DAG_EXECUTING even when a mid-run stream event
		// cleared the shimmer.
		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.planPending || m.shellRunning || m.shimmerActive || m.autonomousActive {
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
		// autonomousActive is included so the safety-net never fires while the
		// autonomous driver Run/Resume command is in flight: autonomous runs are
		// the ONLY operation that uses shimmerActive without setting any of the
		// legacy background-producer flags (agentRunning, streaming, etc.).
		// Without this guard the shimmer self-terminates on the first frame
		// after startShimmer("", "autonomy") is called, freezing the spinner
		// for the entire provider invocation.
		if !m.streaming && !m.agentRunning && !m.reviewRunning && !m.pipelineRunning && !m.planPending && !m.shellRunning && !m.autonomousActive {
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
				m.gotoBottomIfAllowed()
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
		// ── AUTHORITATIVE STAGE: provider bytes are arriving ────────
		// Reasoning tokens are real provider output — the indicator becomes
		// "streaming" (never "thinking"), without exposing the reasoning text.
		// NO token count is asserted here: only the producer's authoritative
		// streamUsageMsg (provider-reported usage) may populate the count.
		m.setStage("model", m.getActiveModelName(), stageStreaming)
		m.ensureStreamBlocks().Append(KindThinking, string(msg))
		// Full stream transparency: the reasoning chunk is also retained in the
		// active ThinkingBuffer via the ThoughtBufferUpdatedMsg protocol so the
		// Ctrl+O thought drawer renders it live. The repaint is throttled to
		// the single-flight 30FPS gate — never a per-token refresh.
		var cmds []tea.Cmd
		cmds = append(cmds, m.readStream(), m.thoughtUpdateCmd(string(msg), false))
		if repaint := m.scheduleRepaint(); repaint != nil {
			cmds = append(cmds, repaint)
		}
		return m, tea.Batch(cmds...)

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
		// ── AUTHORITATIVE STAGE: real provider tokens are arriving ──
		// Only content bytes received from the provider mark the stage as
		// streaming. The token count is NEVER derived from the response
		// buffer length — it is populated only by the producer's authoritative
		// streamUsageMsg (provider-reported usage).
		if raw != "" {
			m.setStage("model", m.getActiveModelName(), stageStreaming)
		}
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
		if m.execStreaming {
			cmds = append(cmds, m.readExecStream())
		} else {
			cmds = append(cmds, m.readStream())
		}
		// Full stream transparency: every content chunk is retained in the
		// active ThinkingBuffer via the ThoughtBufferUpdatedMsg protocol so the
		// Ctrl+O thought drawer renders the raw stream live.
		cmds = append(cmds, m.thoughtUpdateCmd(raw, false))
		if !m.streamTickActive {
			m.streamTickActive = true
			cmds = append(cmds, m.smoothStreamTickCmd())
		}
		// Keep cursor blink alive during streaming
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		cmds = append(cmds, tiCmd)
		return m, tea.Batch(cmds...)

	case streamUsageMsg:
		// ── AUTHORITATIVE LIVE TOKEN USAGE ───────────────────────────
		// The provider reported a usage update while the stream is live. Feed
		// ONLY that authoritative count into the streaming indicator — never a
		// character-count estimate. A zero/unknown usage leaves the count
		// empty so the renderer shows plain "streaming". The reasoning split
		// also backs the compact thought summary so its "N tokens" is
		// provider-reported, not estimated.
		m.setStageMetrics(0, 0, msg.output)
		if m.thinkingBuffer != nil && msg.reasoning > 0 {
			m.thinkingBuffer.SetReasoningTokens(msg.reasoning)
		}
		// The usage message was pulled off the stream channel — chain the next
		// read so the token/done messages behind it keep flowing.
		if m.execStreaming {
			return m, m.readExecStream()
		}
		return m, m.readStream()

	case streamDoneMsg:
		// ── AUTHORITATIVE STAGE: provider stream completed ─────────
		// A terminal stream is done; the stage can never linger as "streaming".
		m.setStage("model", m.getActiveModelName(), stageDone)
		// Freeze Thought duration timer upon stream completion.
		if m.thoughtEndTime.IsZero() {
			m.thoughtEndTime = time.Now()
		}
		if m.thinkingPanel != nil {
			m.thinkingPanel.Freeze()
		}
		if m.thinkingBuffer != nil && !m.thinkingBuffer.Complete() {
			m.thinkingBuffer.MarkComplete()
		}

		// Handle executor streaming (gated path) separately from /ask streaming.
		// The executor stream is for provider output during patch generation;
		// the final result arrives via gatedExecutionMsg.
		if m.execStreaming {
			m.execStreamCh = nil
			m.execStreaming = false
			m.stopShimmer()
			// The final result will arrive via gatedExecutionMsg -> executionResultUpdate
			return m, nil
		}

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
		m.TurnInputTokens = msg.tokenInput
		m.TurnOutputTokens = msg.tokenOutput
		SetTraceTurnTokens(msg.tokenInput, msg.tokenOutput)
		m.InputTokens += msg.tokenInput
		m.OutputTokens += msg.tokenOutput
		m.TotalTokens = m.InputTokens + m.OutputTokens
		// Provider-reported usage (authoritative or an explicit local-model
		// estimate) transitions the footer out of "usage unknown".
		if msg.tokenInput > 0 || msg.tokenOutput > 0 || msg.usageEstimated {
			m.markUsageKnown()
		}

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
			// A late stream completion after /clear (sealed surface) must not
			// resurrect the cleared thinking buffers.
			if !m.activitySurfaceSealed {
				if m.thinkingPanel != nil {
					m.thinkingPanel.Append(finalReasoning)
				}
				if m.thinkingBuffer != nil {
					m.thinkingBuffer.Append(finalReasoning)
					m.thinkingBuffer.MarkComplete()
				}
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
					m.refreshViewportContentImmediate()
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
				m.refreshViewportContentImmediate()
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

		// ── MANDATORY SYNCHRONOUS FLUSH (STREAM COMPLETION) ─────────
		// The final frame must render NOW, on this turn — never deferred to a
		// pending repaintTickMsg that could be dropped, starved, or processed
		// after the viewport went idle. refreshViewportContentImmediate resets
		// the single-flight repaint gate, strips the stream cursor (▋) from the
		// tail, and renders the completed response directly into m.Viewport so
		// it is visible instantly without requiring an external UI event (the
		// "prompt response invisible until the second prompt" regression).
		m.refreshViewportContentImmediate()
		// Full stream transparency: mark the live thought block complete so the
		// Ctrl+O drawer collapses to its "▸ Thought for Xs (N tokens)" summary.
		return m, m.thoughtUpdateCmd("", true)

	case streamErrMsg:
		// Handle executor streaming error separately.
		if m.execStreaming {
			m.execStreamCh = nil
			m.execStreaming = false
			m.setStage("model", m.getActiveModelName(), stageFailed)
			m.stopShimmer()
			// The error will be surfaced via gatedExecutionMsg -> executionResultUpdate or autonomousRunMsg
			return m, nil
		}

		// OPERATION LIFECYCLE: a stream error must release any in-flight
		// build-patch operation (defensive; streams normally run without one).
		m.finalizeBuildOperation(msg.err)
		// ── AUTHORITATIVE STAGE: provider stream failed ─────────────
		// A terminal stream failure marks the stage failed so no "waiting" /
		// "streaming" indicator can survive the error.
		m.setStage("model", m.getActiveModelName(), stageFailed)
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
		// Explicit Over Implicit: whatever usage the provider reported (or the
		// character estimate the stream reader produced) is dispatched as a
		// TokenUsageMsg — even before the stream died — so tokens consumed on a
		// timeout/error are not silently zeroed in the footer. Publish the
		// typed StreamUsage event so telemetry projections observe the failed
		// attempt too.
		if msg.tokenInput > 0 || msg.tokenOutput > 0 {
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
		return m, tea.Batch(flush, m.tokenUsageCmd(msg.tokenInput, msg.tokenOutput))

	case thinkingStreamMsg:
		// Real-time reasoning token dispatch to the TUI Thinking Panel.
		// Updates from token #1 — no waiting for the full response.
		// A late chunk after /clear (sealed surface) must not resurrect the
		// cleared thinking panel.
		if !m.activitySurfaceSealed && m.thinkingPanel != nil {
			m.thinkingPanel.Append(msg.Content)
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		return m, m.readStream()

	case buildFailedMsg:
		// OPERATION LIFECYCLE: a failed fast-track stream must release any
		// in-flight build-patch operation (defensive; the fast-track path is
		// streaming-based and normally holds no operation).
		m.finalizeBuildOperation(msg.Err)
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
		// attempt so consumed tokens are never silently zeroed (dispatched as a
		// TokenUsageMsg so the footer refreshes immediately).
		m.push(roleError, "fast-track build failed: "+providers.SanitizeAPIError(msg.Err))
		// "Human-Centered / Reversible": a failed build stream must never trap
		// the workflow in the build phase. Unwind the state machine back to
		// StateChat/interactive so the next prompt routes normally instead of
		// failing with "transition from build to ask".
		m.unwindBuildFailure()
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.tokenUsageCmd(msg.TokenInput, msg.TokenOutput))

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
		m.gotoBottomIfAllowed()
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

	case selectionScrollTickMsg:
		return m, m.handleSelectionAutoScroll(msg)

	case tea.MouseMsg:
		// ── Viewport + Selection invariant: execution state != viewport state.
		// Scrolling/selection are presentation-only and remain available while
		// streaming/processing unless a genuinely modal interaction owns input.
		if m.isModalForMouse() {
			return m, nil
		}
		// Wheel scroll: always available outside modal states, even while
		// streaming/tool execution/processing. It mutates the single app-owned
		// scroll offset (the bubbles viewport is a pure pre-sliced render
		// surface, so wheel input can never double-scroll).
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			if m.Ready {
				if msg.Button == tea.MouseButtonWheelUp {
					m.scrollBy(-3)
				} else {
					m.scrollBy(3)
				}
				return m, nil
			}
			return m, nil
		}
		// Left-button selection lifecycle: Down → drag → Up → auto-copy.
		// Works in any non-modal state, including during streaming.
		switch msg.Action {
		case tea.MouseActionPress:
			if msg.Button == tea.MouseButtonLeft {
				pos := m.mousePosToGlobal(msg)
				m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: pos, Cursor: pos, lastY: msg.Y, lastX: msg.X, TickActive: false}
				// Freeze layout snapshot at drag start so background ticks
				// cannot shift rows under the cursor.
				canonical := m.canonicalRecordsContent()
				m.frozenRecords = append([]record(nil), m.records...)
				m.frozenViewportStr = canonical
				if len(m.fullHitRows) > 0 {
					m.frozenFullHitRows = append([]RowLayout(nil), m.fullHitRows...)
				} else {
					m.frozenFullHitRows = buildFullHitMap(m)
				}
				// Freeze streaming auto-follow while inspecting via selection.
				m.setScrollLocked(true)
				m.refreshViewportContent()
				return m, nil
			}
		case tea.MouseActionMotion:
			if m.mouseSel.Dragging {
				m.mouseSel.lastY = msg.Y
				m.mouseSel.lastX = msg.X
				m.mouseSel.Cursor = m.mousePosToGlobal(msg)
				m.refreshViewportContent()
				// Auto-scroll engine: continuous background ticking when mouse held past viewport bounds.
				// Spec: when mouseSel.Active==true and screen Y outside viewport range
				// (msg.Y >= topMargin+viewportHeight or msg.Y < topMargin), trigger continuous
				// AutoScrollTickMsg until mouse release.
				geo := m.viewportGeometry()
				relY := msg.Y - geo.Top
				isOutside := msg.Y >= geo.Top+geo.Height || msg.Y < geo.Top
				inEdge := relY < selectionEdgeRows || relY >= geo.Height-selectionEdgeRows
				shouldTick := isOutside || inEdge
				if shouldTick && !m.mouseSel.TickActive {
					m.mouseSel.TickActive = true
					return m, AutoScrollTickCmd(msg.Y, msg.X)
				}
				if !shouldTick {
					m.mouseSel.TickActive = false
				}
				return m, nil
			}
		case tea.MouseActionRelease:
			if msg.Button == tea.MouseButtonLeft && m.mouseSel.Dragging {
				m.mouseSel.Cursor = m.mousePosToGlobal(msg)
				m.mouseSel.Dragging = false
				m.mouseSel.TickActive = false
				// Clear frozen layout snapshot; next refresh will rebuild
				// with current content including any background updates.
				m.frozenFullHitRows = nil
				m.frozenViewportStr = ""
				m.frozenRecords = nil
				cmd := m.copyMouseSelection()
				return m, cmd
			}
			// Release with ButtonNone (some terminals) while dragging
			if m.mouseSel.Dragging {
				m.mouseSel.Cursor = m.mousePosToGlobal(msg)
				m.mouseSel.Dragging = false
				m.mouseSel.TickActive = false
				m.frozenFullHitRows = nil
				m.frozenViewportStr = ""
				m.frozenRecords = nil
				cmd := m.copyMouseSelection()
				return m, cmd
			}
		}
		return m, nil

	case tea.KeyMsg:

		// ── PRIORITY 1: ACTIVE TEXT INPUT ────────────────────────────
		// A printable character typed into the focused input is ALWAYS text.
		// It can never be hijacked by a capability hotkey or any other
		// single-character keybinding. Explicit keybinding mechanisms (Enter,
		// Esc, arrows, alt+…, ctrl+…) fall through to the handlers below.
		//
		// EXCEPTION: a '?' typed into a COMPLETELY EMPTY input buffer opens the
		// command palette / help modal instead of inserting '?' — the char is
		// consumed and never reaches the text input.
		if m.ti.Focused() && isPrintableRunes(msg) {
			if msg.String() == "?" && strings.TrimSpace(m.ti.Value()) == "" {
				m.showHelpOverlay = !m.showHelpOverlay
				return m, nil
			}
			return m, m.forwardToInput(msg)
		}

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
				m.completeAutocomplete(false)
				return m, nil
			case tea.KeyEnter:
				// A unique whole-line suggestion completes and executes
				// immediately ("/q" → "/quit"); anything else just completes
				// the token under the caret and stays in the input.
				if m.completeAutocomplete(true) {
					return m.submitEnter()
				}
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
				step := m.Viewport.Height / 2
				if step < 1 {
					step = 1
				}
				m.scrollBy(-step)
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				step := m.Viewport.Height / 2
				if step < 1 {
					step = 1
				}
				m.scrollBy(step)
				return m, nil
			}
		}

		// ── SPACE snap-to-bottom (resets user scroll-lock) ─────────────────
		if msg.Type == tea.KeySpace && !m.autocompleteActive {
			m.setScrollLocked(false)
			// Re-anchor the expanded output-trace window to the tail so the
			// user "catches up" to the latest streamed content.
			m.traceWindowAnchored = false
			if m.Ready {
				m.gotoBottomIfAllowed()
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
		m.gotoBottomIfAllowed()
		return m, tea.Batch(cmds...)
	}

	// ── Viewport scroll keys (any state) ─────────────────────────────────────
	if m.Ready {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			step := m.Viewport.Height / 2
			if step < 1 {
				step = 1
			}
			switch keyMsg.Type {
			case tea.KeyPgUp, tea.KeyHome:
				m.scrollBy(-step)
				return m, nil
			case tea.KeyPgDown, tea.KeyEnd:
				m.scrollBy(step)
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

// enterViMode transitions the UI into navigation/inspection mode: blurs the
// text input, initializes cursor at the last record, resets selection state,
// and refreshes the viewport with cursor highlighting. Mouse reporting is now
// globally enabled, so this returns nil (keeps the shared mouse mode).
func (m *model) enterViMode() tea.Cmd {
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
	m.setToast("Copy mode: j/k scroll, v select, y yank, / search, Esc or :q to exit")
	return tea.EnableMouseCellMotion
}

// exitViMode returns the UI to normal interactive mode: clears selection,
// refocuses the text input, and resets all vi-mode state. It returns
// DisableMouse so wheel reporting is scoped to inspection mode only.
func (m *model) exitViMode() tea.Cmd {
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
	m.gotoBottomIfAllowed()
	m.clearToast()
	return nil
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
	case "i", "esc":
		return m, m.exitViMode()

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
			m.setScrollLocked(true)
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
				return m, m.exitViMode()
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
	var werr error
	if m.clipboard != nil {
		werr = m.clipboard.WriteAll(text)
	} else {
		werr = clipboardWriteAll(text)
	}
	if werr != nil {
		m.push(roleSystem, mutedStyle.Render("clipboard error: "+werr.Error()))
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

	// Sync the single app-owned scroll offset using cumulative physical line
	// counts (vi-mode keeps its own viewport scroll via Viewport.YOffset).
	if m.Ready {
		m.Viewport.YOffset = yOffset
		m.docScrollOffset = yOffset + m.renderChromePrefixHeight()
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
