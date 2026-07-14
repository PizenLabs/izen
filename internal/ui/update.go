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

	// ── INIT STAGE ROUTING: intercept all key messages during setup ─────
	if m.initStage != initNone && m.initStage != initComplete {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			return m.handleInitKeyMsg(keyMsg)
		}
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			// Fall through to window resize below for init stage too
		} else {
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
		m.push(roleSystem, "[System] Engine diagnostics collected. Escalating to LLM for analysis...")
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
			m.updateActionChips()
		}

		// ── Build verification: post-mutation test auto-result ───────────
		if m.buildVerifyPending {
			m.buildVerifyPending = false
			if msg.passed {
				m.activeChips = []actionChip{
					{key: "alt+d", label: "Commit Safe Baseline", action: "/commit"},
				}
			} else {
				m.activeChips = []actionChip{
					{key: "alt+r", label: "Rollback Workspace", action: "/undo"},
				}
			}
			m.showChips = true
			if m.resolver.Current() == modes.ModeBuild {
				m.push(roleSystem, "[System] Build verification complete. Use action chips to commit or rollback.")
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
			m.push(roleSystem, infoStyle.Render("[System] Execution Successful (Exit Code 0)"))
		} else {
			m.push(roleSystem, infoStyle.Render(fmt.Sprintf("[System] Execution Failed (Exit Code %d)", msg.exitCode)))
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
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("[System] Silent analysis complete [%s]. Proceeding to blueprint...", msg.ledgerID)))
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
		m.push(roleSystem, "[System] Analyzing failure context and generating fix...")
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
					m.push(roleSystem, "[System] Running build verification test...")
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
				m.push(roleSystem, "[System] Running build verification test...")
				testCmd = m.runTestEngine("./...")
			}
		case applied > 0:
			summary := fmt.Sprintf("%s %d mutated, %d failed.", warningBannerStyle.Render("[!]"), applied, failed)
			m.push(roleSystem, summary)
			m.createBuildCheckpoint(applied)
			if m.resolver.Current() == modes.ModeBuild {
				m.buildVerifyPending = true
				m.push(roleSystem, "[System] Running build verification test...")
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

		// Append the completed turn to PreRenderedHistory and freeze state.
		m.push(roleAI, final)

		// ── IMPLICIT PIPELINE INTERCEPT: pipe stream output to next step ──
		if m.pipelineRunning {
			if m.pipelineStep == "analyzing failure" || m.pipelineStep == "analyzing trace" {
				// Step 1 complete → silently pipe analysis into plan blueprinting
				m.pipelineStep = "blueprinting"
				m.push(roleSystem, infoStyle.Render("[System] Pipeline Step 2/3 — Blueprint in progress..."))
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
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("[System] Pipeline complete [%s]. Delegating to /build for execution...", pipelineID)))
				flush := m.flushPendingRecords()
				return m, tea.Batch(flush, func() tea.Msg {
					return blueprintReadyMsg{blueprint: final, ledgerID: pipelineID}
				})
			}
		}

		// ── Handoff: Capture ProposedFix from investigate mode ──────────
		if m.resolver.Current() == modes.ModeInvestigate && final != "" {
			m.handoffCtx.ProposedFix = final
			m.updateActionChips()
		}

		// ── Auto-transition: investigate → build on mutation detection ──
		// When a read-only analysis ($diagnose, $test) in investigate mode
		// concludes with a concrete mutation proposal (code blocks with
		// language annotations), automatically transition to /build and
		// initiate the fix pipeline. This eliminates the manual handoff step.
		if m.resolver.Current() == modes.ModeInvestigate && m.handoffCtx.ProposedFix != "" {
			if containsMutationIntention(m.handoffCtx.ProposedFix) {
				m.push(roleSystem, infoStyle.Render("[System] Analysis complete — file mutation detected. Auto-transitioning to /build mode for execution..."))
				m.setMode(modes.ModeBuild)
				m.lastTestOutput = m.handoffCtx.ProposedFix
				flush := m.flushPendingRecords()
				m.refreshViewportContent()
				return m, tea.Batch(flush, m.runFixCmd(""))
			}
		}

		// ── Handoff: Capture PendingTodos from plan mode ────────────────
		if m.resolver.Current() == modes.ModePlan && final != "" {
			m.handoffCtx.PendingTodos = extractTodosFromPlan(final)
			m.updateActionChips()
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
		m.push(roleStatus, mutedStyle.Render(
			fmt.Sprintf("↳ done · +%d tok · %s · %.1fs", delta, costStr, latencySec)))

		if m.resolver.Current() == modes.ModePlan {
			validation := plan.ValidatePlanOutput(final)
			if !validation.Valid {
				errMsg := plan.FormatValidationError(validation)
				m.push(roleError, errMsg)
				m.push(roleSystem, infoStyle.Render("regenerate with more precise intent"))
			}

			// Collapse to valid blocks only; fall back to raw parse if empty.
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
				width := m.width - 2
				if width < 20 {
					width = 20
				}
				renderer := NewMarkdownRenderer(width)
				rendered := renderer.Render(compileTaskListMarkdown(&tasks))
				if rendered != "" {
					m.push(roleAI, rendered)
				}
				m.push(roleStatus, "System status: Plan staged. Use /build to execute changes.")
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

		// Extract shell commands from the response for explicit approval
		if m.state == StateChat && !m.awaitingConfirmation {
			shellBlocks := extractShellCommands(final)
			if len(shellBlocks) > 0 {
				mode := m.resolver.Current()
				if mode.CanShell() {
					m.pendingShellExec = shellBlocks
					m.shellAwaitingIdx = 0
					m.state = StateAwaitingShellExec
					m.push(roleSystem, shellWarningStyle.Render(
						fmt.Sprintf("Shell Execution: %d command(s) pending approval", len(shellBlocks))))
				} else {
					msg := fmt.Sprintf("[System] Tool 'shell' rejected. Reason: Explicit boundary violation for '%s' mode.", mode)
					m.push(roleSystem, msg)
					m.sess.AddMessage("system", msg+" You are in a Read-Only execution environment and must stop requesting system mutations.", 3)
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

	case traceUpdateMsg:
		m.currentTrace = msg.trace
		return m, nil

	case config.ConfigChangeMsg:
		newCfg, err := config.Load()
		if err == nil {
			m.cfg = newCfg
		}
		return m, nil

	case tea.MouseMsg:
		// HARD GUARD: In destructive states (approval/exec), mouse events are
		// completely ignored — no viewport scrolling, no coordinate mapping.
		// This eliminates any possibility of accidental mutation via click.
		// During processing, wheel events are allowed for scroll inspection.
		if m.state == StateAwaitingApproval || m.state == StateAwaitingShellExec {
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
		// AI INTERRUPT ENGINE: Ctrl+D cancels an active LLM stream.
		if m.streaming && msg.Type == tea.KeyCtrlD {
			if m.streamCancel != nil {
				m.streamCancel()
			}
			m.streamCancel = nil
			m.interruptRequested = true
			m.push(roleSystem, "[System] Generation interrupted by user.")
			return m, nil
		}

		// ── Action Chip Hotkeys (alt+ modifier only) ─────────────────────
		// Single-character hotkeys are strictly banned to prevent key
		// collisions with normal prompt input (e.g., typing in /plan).
		if m.showChips && !m.streaming && !m.agentRunning && m.state == StateChat {
			switch msg.String() {
			case "alt+a":
				return m, m.handleChipActivation("alt+a")
			case "alt+b":
				return m, m.handleChipActivation("alt+b")
			case "alt+c":
				return m, m.handleChipActivation("alt+c")
			case "alt+d":
				return m, m.handleChipActivation("alt+d")
			case "alt+r":
				return m, m.handleChipActivation("alt+r")
			}
		}

		// In special states, route directly to handleKey.
		if m.state == StateAwaitingApproval || m.state == StateAwaitingShellExec || m.state == StateProcessing {
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

func compileTaskListMarkdown(tasks *[]plan.Task) string {
	var b strings.Builder

	b.WriteString("# TASK LIST\n\n")
	for _, task := range *tasks {
		glyph := "○"
		if task.Status == "processing" {
			glyph = "●"
		} else if task.Status == "done" || task.IsDone {
			glyph = "✓"
		}
		fmt.Fprintf(&b, "%s **%s**: %s | %s\n\n", glyph, task.Type, task.Target, task.Description)
	}

	return b.String()
}
