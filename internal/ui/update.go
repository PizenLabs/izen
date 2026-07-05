package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// Init initializes the spinner tick and text input blink.
func (m *model) Init() tea.Cmd {
	m.currentTip = randomTip()
	return tea.Batch(m.spinnerTickCmd(), m.ti.Focus())
}

// Update routes state machines and events.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ── GLOBAL INTERCEPT: [P] Toggle Hotkey ──────────────────────────────────
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "p" || keyMsg.String() == "P" {
			if m.state == StateAwaitingApproval && len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				return m, nil
			}
		}
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ti.Width = msg.Width - 8

		if m.streamParser != nil {
			m.streamParser.SetWidth(msg.Width - 2)
		}

		return m, nil

	case tickMsg:
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		}
		return m, m.spinnerTickCmd()

	case agentStartMsg:
		m.agentRunning = true
		m.agentDone = false
		m.agentLabel = msg.label
		m.spinnerFrame = 0
		return m, m.spinnerTickCmd()

	case agentDoneMsg:
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = msg.label
		flush := m.flushPendingRecords()
		return m, flush

	case investigateResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "investigation error: "+msg.err.Error())
		}
		m.records = append(m.records, msg.records...)
		if msg.sessionKey != "" {
			m.sess.SetInvestigationID(msg.sessionKey)
		}
		// Force-reset streaming middleware flags to guarantee streamCmd can run
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		m.push(roleSystem, "[System] Engine diagnostics collected. Escalating to LLM for analysis...")
		flush := m.flushPendingRecords()
		return m, tea.Batch(flush, m.streamCmd(msg.escalationContent))

	case reviewResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "review error: "+msg.err.Error())
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
		flush := m.flushPendingRecords()
		return m, flush

	case commitGeneratedMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "commit error: "+msg.err.Error())
			flush := m.flushPendingRecords()
			return m, flush
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("commit: %s", msg.subject)))
		if msg.body != "" {
			for _, l := range strings.Split(msg.body, "\n") {
				m.push(roleSystem, infoStyle.Render(l))
			}
		}
		m.push(roleStatus, fmt.Sprintf("amended as %s", msg.hash))
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

	case tokenMsg:
		raw := string(msg)
		m.responseBuffer.WriteString(raw)
		m.currentStreamContent += raw
		if m.streamParser != nil {
			m.streamParser.ProcessChunk(raw)
		}
		// Keep cursor blink alive during streaming
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		return m, tea.Batch(m.readStream(), tiCmd)

	case streamDoneMsg:
		m.streamCh = nil
		m.streaming = false

		if m.streamParser != nil {
			m.streamParser.Flush()
			m.streamParser = nil
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

		// Flush the AI response to terminal scrollback via a SINGLE tea.Println.
		aiRecord := record{role: roleAI, text: final}
		m.records = append(m.records, aiRecord)

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
		deltaStr := fmt.Sprintf("%d", delta)
		if delta >= 1000 {
			deltaStr = fmt.Sprintf("%.1fk", float64(delta)/1000)
		}
		m.IsCloudModel = m.cfg.ActiveProviderName() != "ollama"
		costStr := "$0.0000"
		if m.IsCloudModel {
			turnCost := float64(msg.tokenInput)*(3.0/1_000_000) + float64(msg.tokenOutput)*(15.0/1_000_000)
			m.AccumulatedCost += turnCost
			costStr = fmt.Sprintf("$%.4f", turnCost)
		}
		m.push(roleStatus, fmt.Sprintf("done - +%s tokens (this turn)  •  %s", deltaStr, costStr))

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
					m.records = append(m.records, record{role: roleAI, text: rendered})
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
					applied := 0
					for _, p := range props {
						patch := &execution.Patch{
							ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
							File:     p.Target.QualifiedName,
							Modified: p.Diff,
						}
						orig, err := os.ReadFile(p.Target.QualifiedName)
						if err == nil {
							patch.Original = string(orig)
						}
						if err := m.execEng.Patches.Apply(patch); err != nil {
							m.setApplyError("apply failed: " + err.Error())
							continue
						}
						applied++
						status := "modified"
						if isNewFileCreation(p.Diff) {
							status = "created"
						}
						m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
							Target: p.Target.QualifiedName,
							Status: status,
						})
						acceptedLine := fmt.Sprintf("%s Accepted • %s • %s", acceptedDotStyle, p.Target.QualifiedName, status)
						m.push(roleSystem, acceptedLineStyle.Render(acceptedLine))
					}
					if applied > 0 {
						m.createBuildCheckpoint(applied)
					}
				} else {
					m.pendingProposals = props
					m.state = StateAwaitingApproval
					m.awaitingConfirmation = true
					proposalMsg := "proposed changes:"
					for _, p := range props {
						proposalMsg += fmt.Sprintf("\n    • %s", p.Target.QualifiedName)
					}
					m.push(roleSystem, infoStyle.Render(proposalMsg))
				}
			}
			m.sess.ClearTasks()
		}

		// Extract shell commands from the response for explicit approval
		if m.state == StateChat && !m.awaitingConfirmation {
			shellBlocks := extractShellCommands(final)
			if len(shellBlocks) > 0 {
				m.pendingShellExec = shellBlocks
				m.shellAwaitingIdx = 0
				m.state = StateAwaitingShellExec
				m.push(roleSystem, shellWarningStyle.Render(
					fmt.Sprintf("Shell Execution: %d command(s) pending approval", len(shellBlocks))))
			}
		}

		// Single atomic flush: AI response + all status records in one batch.
		return m, m.flushPendingRecords()

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.streamParser = nil
		if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveExecuting {
			m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
		}
		m.push(roleError, "stream error: "+msg.err.Error())
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

	case tea.KeyMsg:
		// In special states, route directly to handleKey.
		if m.state == StateAwaitingApproval || m.state == StateAwaitingShellExec {
			resModel, cmd := m.handleKey(msg)
			return resModel, cmd
		}

		if strings.TrimSpace(m.ti.Value()) == "/clear" && msg.String() == "enter" {
			m.showBanner = true
		} else if msg.String() == "enter" && strings.TrimSpace(m.ti.Value()) != "" {
			m.showBanner = false
		}

		if !m.showSuggestions && !m.streaming && !m.agentRunning {
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

		resModel, cmd := m.handleKey(msg)
		return resModel, cmd
	}

	// ── Text Input Pass-Through ──────────────────────────────────────────────
	var tiCmd tea.Cmd
	m.ti, tiCmd = m.ti.Update(msg)
	return m, tiCmd
}

func (m *model) spinnerTickCmd() tea.Cmd {
	frame := m.spinnerFrame % len(spinnerFrames)
	frameStr := spinnerFrames[frame]

	var delay time.Duration
	switch frameStr {
	case " ⊹ ":
		delay = 40 * time.Millisecond
	case " ⁕ ":
		delay = 70 * time.Millisecond
	case " ❃ ", " ❄ ", " ❆ ":
		delay = 250 * time.Millisecond
	default:
		delay = 100 * time.Millisecond
	}

	return tea.Tick(delay, func(t time.Time) tea.Msg { return tickMsg(t) })
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
