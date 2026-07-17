package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// Init initializes the spinner tick and text input blink.
func (m *model) Init() tea.Cmd {
	m.currentTip = randomTip()
	if m.initStage != initNone && m.initStage != initComplete {
		return m.spinnerTickCmd()
	}
	return tea.Batch(m.spinnerTickCmd(), m.ti.Focus())
}

// Update routes state machines and events.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ── GLOBAL PANIC RECOVERY ──────────────────────────────────────────
	// Any panic inside the update loop is caught here, the full stack trace
	// is written to stderr for debugging, and the program exits cleanly.
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			fmt.Fprintf(os.Stderr, "\nIZEN PANIC: %v\nStack:\n%s\n", r, buf[:n])
			os.Exit(1)
		}
	}()

	// ── HARD KEYBOARD INTERCEPT: Approval/Processing states bypass all sub-components ──
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			return m.handleKey(keyMsg)
		}
	}

	// ── GLOBAL INTERCEPT: [Alt+P] Toggle Hotkey ────────────────────────────
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "alt+p" {
			if m.state == StateAwaitingApproval && len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return m, nil
			}
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

	// ── INIT STAGE ROUTING: intercept all key messages during setup ─────
	if m.initStage != initNone && m.initStage != initComplete {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			// Defensive blur: no focused textinput should consume keys
			// during any init stage. Each sub-handler re-focuses as needed.
			m.ti.Blur()
			return m.handleInitKeyMsg(keyMsg)
		}
		// Allow tickMsg to pass through so the spinner continues animating
		// during init stages.
		if _, ok := msg.(tickMsg); ok {
			return m, m.spinnerTickCmd()
		}
		// Allow async result messages to reach their handlers in the main
		// type switch below. Without this, gitInitResultMsg gets swallowed
		// and the init stage never advances after pressing 'Y'.
		switch msg.(type) {
		case tea.WindowSizeMsg, gitInitResultMsg, providerSwitchMsg, graphBuiltMsg:
			// fall through to main type switch
		default:
			return m, nil
		}
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ti.Width = msg.Width - 8

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
			m.state != StateProcessing && m.state != StateAwaitingApproval && !m.pipelineRunning {
			m.spinnerFrame = 0
		}

		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning ||
			m.state == StateProcessing || m.state == StateAwaitingApproval {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(ProposalSpinnerFrames)
			if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.state == StateProcessing {
				m.refreshViewportContent()
			}
			return m, m.spinnerTickCmd()
		}
		return m, m.spinnerTickCmd()

	case agentStartMsg:
		m.agentRunning = true
		m.agentDone = false
		m.agentLabel = msg.label
		m.spinnerFrame = 0
		return m, nil

	case agentDoneMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case investigateResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "investigation error: "+msg.err.Error())
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
		m.push(roleSystem, "Diagnostics collected. Analyzing...")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.escalationContent))

	case graphBuiltMsg:
		m.agentRunning = false
		m.sanitizeInputPrompt()
		if msg.err == nil && msg.graph != nil {
			m.graph = msg.graph
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case reviewResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "review error: "+msg.err.Error())
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
			m.push(roleError, "test execution error: "+msg.err.Error())
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
			if !msg.passed && m.resolver.Current() == modes.ModeBuild &&
				m.buildRecoveryCount < maxBuildRecoveryAttempts {
				m.buildRecoveryCount++
				m.acceptAll = true
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
					"⚙ [recovery %d/%d] auto-correcting compilation errors...",
					m.buildRecoveryCount, maxBuildRecoveryAttempts)))
				flush := m.flushPendingRecords()
				return m, tea.Batch(flush, m.runFixCmd(""))
			}

			// If recovery exhausted or verification passed, expose the
			// corresponding workflow result (commit / rollback).
			if m.buildRecoveryCount >= maxBuildRecoveryAttempts {
				m.acceptAll = false
			}
			m.currentResult = buildVerifyResult(msg.passed)
			if m.resolver.Current() == modes.ModeBuild {
				m.push(roleSystem, "Build verification complete.")
			}
		}

		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case buildResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		m.lastTestOutput = msg.output
		m.lastTestFailed = msg.exitCode != 0
		if msg.err != nil {
			m.push(roleError, "build execution error: "+msg.err.Error())
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
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
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
			m.push(roleError, "$log: error: "+msg.err.Error())
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
			m.push(roleError, "silent analysis error: "+msg.err.Error())
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
			m.push(roleError, "blueprint error: "+msg.err.Error())
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			flush := m.flushPendingRecords()
			return m, flush
		}
		return m, m.handleBlueprintReady(msg)

	case fixResultMsg:
		m.agentRunning = false
		m.reviewRunning = false
		m.agentDone = true
		m.agentLabel = ""
		m.lastActionTime = time.Time{}
		m.sanitizeInputPrompt()
		if msg.err != nil {
			m.push(roleError, "fix error: "+msg.err.Error())
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
			m.push(roleError, "env diagnostics error: "+msg.err.Error())
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
			m.push(roleError, "trace execution error: "+msg.err.Error())
		}

		// Token optimization: truncate middle if output exceeds 4000 chars
		output := msg.output
		if len(output) > 4000 {
			top := output[:2000]
			bottom := output[len(output)-2000:]
			output = top + "\n... [TRUNCATED " + strconv.Itoa(len(msg.output)-4000) + " bytes] ...\n" + bottom
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
			m.push(roleError, "diagnosis error: "+msg.err.Error())
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
			m.push(roleError, "commit error: "+msg.err.Error())
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
			m.ti.Focus()
			m.state = StateChat
			m.recalcViewportHeight()
			m.awaitingConfirmation = false
			m.acceptAll = false
			if msg.err == nil {
				outcomeLine := fmt.Sprintf("%s %s • %s", successBannerStyle.Render("[✓]"), msg.file, msg.status)
				m.push(roleSystem, outcomeLine)
				m.createBuildCheckpoint(1)
				// Handoff: unbuffered build verification test
				if m.resolver.Current() == modes.ModeBuild {
					m.buildVerifyPending = true
					m.refreshViewportContent()
					m.push(roleSystem, "Verifying build...")
					flush := m.flushPendingRecords()
					return m, tea.Batch(flush, m.runTestEngine("./..."))
				}
			} else {
				m.push(roleSystem, failureBannerStyle.Render("[✗] "+msg.file+" — "+msg.err.Error()))
			}
		} else {
			m.state = StateAwaitingApproval
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
				failed++
				continue
			}
			m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
				Target: r.file,
				Status: r.status,
			})
			applied++
		}
		m.pendingProposals = nil
		m.awaitingConfirmation = false
		m.acceptAll = false
		m.ti.Focus()
		m.state = StateChat
		m.recalcViewportHeight()
		var testCmd tea.Cmd
		switch {
		case applied > 0 && failed == 0:
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
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		flush := m.flushPendingRecords()
		return m, flush

	case smoothStreamTickMsg:
		if len(m.streamBuffer) > 0 {
			// Emit word-aligned chunks for a natural reading rhythm.
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
			}
			m.currentStreamContent += m.streamBuffer[:emit]
			m.streamBuffer = m.streamBuffer[emit:]
		}

		// Refresh viewport with streaming content.
		if m.Ready {
			m.refreshViewportContent()
			// Only auto-scroll to bottom if the user hasn't explicitly
			// scrolled up — respects user-inspect position during streaming.
			if m.streaming && !m.userIsScrollingUp {
				m.Viewport.GotoBottom()
			}
		}

		if len(m.streamBuffer) > 0 || m.streaming {
			m.streamTickActive = true
			return m, m.smoothStreamTickCmd()
		}
		m.streamTickActive = false
		return m, nil

	case tokenMsg:
		raw := string(msg)
		m.responseBuffer.WriteString(raw)
		m.streamBuffer += raw
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

		if m.streamParser != nil {
			m.streamParser.Flush()
			m.streamParser = nil
		}

		// Flush any remaining buffered stream content
		if m.streamTickActive {
			m.currentStreamContent += m.streamBuffer
			m.streamBuffer = ""
			m.streamTickActive = false
		}

		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		m.InputTokens += msg.tokenInput
		m.OutputTokens += msg.tokenOutput
		m.TotalTokens = m.InputTokens + m.OutputTokens

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

		// Sanitize: strip tool execution artifacts so they don't pollute
		// the downstream JSON parser or render pipeline. Lines matching
		// telemetry/error markers are removed from leading/trailing context.
		final = sanitizeFinalContent(final)

		// Append the completed turn to PreRenderedHistory and freeze state.
		m.push(roleAI, final)

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
				// Step 2 complete → blueprint is ready, jump to build execution
				pipelineID := ""
				if m.ledger != nil {
					pipelineID = fmt.Sprintf("#%d", m.ledger.ActiveID)
				}
				m.pipelineRunning = false
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("Pipeline complete [%s]. Switched to /build.", pipelineID)))
				flush := m.flushPendingRecords()
				return m, tea.Batch(flush, func() tea.Msg {
					return blueprintReadyMsg{blueprint: final, ledgerID: pipelineID}
				})
			}
		}

		// ── Handoff: Capture ProposedFix from investigate mode ──────────
		// The "Formulate Execution Plan" capability is derived from
		// handoffCtx.ProposedFix in BuildViewContext; no UI cache to refresh.
		if m.resolver.Current() == modes.ModeInvestigate && final != "" {
			m.handoffCtx.ProposedFix = final
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
						m.handoffCtx.LastFailurePayload +
						"\n```\n" +
						"### Root Cause Analysis\n" +
						m.handoffCtx.ProposedFix

					m.currentPrompt = recoveryPrompt
					m.streamCh = nil
					m.streaming = false
					m.streamParser = nil
					flush := m.flushPendingRecords()
					m.refreshViewportContent()
					return m, tea.Batch(flush, m.streamCmd(recoveryPrompt))
				}

				// No compile failure — safe to auto-transition to /build
				m.push(roleSystem, infoStyle.Render("File mutation detected. Switched to /build."))
				m.setMode(modes.ModeBuild)
				m.lastTestOutput = m.handoffCtx.ProposedFix
				flush := m.flushPendingRecords()
				m.refreshViewportContent()
				return m, tea.Batch(flush, m.runFixCmd(""))
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
			// Memory Context Update: Store user and assistant messages in sliding window
			m.sess.AddMessage("user", promptText, 5)
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
		costStr := "$free"
		if m.IsCloudModel {
			turnCost := float64(msg.tokenInput)*(3.0/1_000_000) + float64(msg.tokenOutput)*(15.0/1_000_000)
			m.AccumulatedCost += turnCost
			costStr = fmt.Sprintf("$%.4f", turnCost)
		}
		latencySec := 0.0
		if !m.streamStartTime.IsZero() {
			latencySec = time.Since(m.streamStartTime).Seconds()
			m.streamStartTime = time.Time{}
		}
		m.push(roleStatus, dimmedStyle.Render(
			fmt.Sprintf("✔ done · +%d tok · %s · %.1fs", delta, costStr, latencySec)))

		if m.resolver.Current() == modes.ModePlan {
			// Try JSON plan parsing first (JSON output is the primary contract).
			// Falls back to legacy markdown format if JSON is invalid/absent.
			// NOTE: raw final was already pushed as roleAI at line 771 and
			// rendered through cacheRecordToHistory → renderStreamingContent,
			// which handles JSON widget rendering. Do NOT push rendered content
			// again here — that creates a double-rendering loop.
			if jsonResult := plan.ParseJSONPlan(final); jsonResult != nil && jsonResult.Valid && jsonResult.Plan != nil {
				if len(jsonResult.Tasks) > 0 {
					tasks := jsonResult.Tasks
					m.sess.StageTaskList(&tasks)
					m.push(roleStatus, "System status: Plan staged. Use /build to execute changes.")
				}
			} else {
				// Fall back to legacy markdown plan validation
				validation := plan.ValidatePlanOutput(final)
				if !validation.Valid {
					errMsg := plan.FormatValidationError(validation)
					m.push(roleError, errMsg)
					m.push(roleSystem, infoStyle.Render("regenerate with more precise intent"))
				}

				var blockContent string
				if len(validation.Blocks) > 0 {
					blockContent = plan.CollapsePlanSections(final)
				}

				tasks := plan.ParseMarkdownToTasks(blockContent)
				if len(tasks) == 0 {
					tasks = plan.ParseMarkdownToTasks(final)
				}

				if len(tasks) > 0 {
					m.sess.StageTaskList(&tasks)
					m.push(roleStatus, "System status: Plan staged. Use /build to execute changes.")
				}
			}
		}

		if m.resolver.Current() == modes.ModeBuild && m.state != StateAwaitingApproval {
			props := extractBuildProposals(final)
			diffProps := extractDiffPatches(final)
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
					m.state = StateAwaitingApproval
					m.recalcViewportHeight()
					m.Viewport.Height = m.computeVpHeight()
					m.awaitingConfirmation = true
					m.ti.Blur()
					m.refreshViewportContent()
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
					m.push(roleSystem, msg)
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

		m.refreshViewportContent()
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.streamCancel = nil

		// User-initiated interrupt — suppress error noise, just clean up.
		if m.interruptRequested {
			m.interruptRequested = false
			m.responseBuffer.Reset()
			m.currentStreamContent = ""
			m.streamBuffer = ""
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
		m.push(roleError, "stream error: "+msg.err.Error())
		m.refreshViewportContent()
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
		if m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
		}
		m.streamBuffer = ""
		m.currentStreamContent = ""
		m.streamTickActive = false
		m.interruptRequested = false
		m.spinnerFrame = 0
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
		return m, nil

	case gitInitResultMsg:
		if msg.err != nil {
			m.initGitInitErr = msg.err.Error()
		} else if m.initStage == initGitCheck {
			m.initGitInitDone = true
			m.advancePastGitCheck()
		}
		return m, m.spinnerTickCmd()

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
			if m.Ready {
				m.Viewport.GotoBottom()
			}
		}

		resModel, cmd := m.handleKey(msg)
		return resModel, cmd
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
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) smoothStreamTickCmd() tea.Cmd {
	return tea.Tick(20*time.Millisecond, func(t time.Time) tea.Msg {
		return smoothStreamTickMsg(t)
	})
}

// containsMutationIntention detects whether an LLM analysis output from
// investigate mode proposes concrete file mutations. Uses language-annotated
// code blocks as the heuristic — when the agent outputs code blocks with known
// language identifiers (go, diff, python, etc.), it indicates a patch proposal.
// detectCompileFailure scans the given output for build/compile failure
// signatures. Returns true when the codebase is in a non-compilable state
// that requires structural recovery before any patch can be applied.
func detectCompileFailure(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	indicators := []string{
		"[build failed]",
		"syntax error",
		"compilation error",
		"expected declaration",
		"non-declaration statement outside function body",
		"expected ';'",
		"cannot find package",
		"undefined:",
		"not enough arguments",
		"too many errors",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
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
			m.viCmdBuf = m.viCmdBuf[:len(m.viCmdBuf)-1]
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
