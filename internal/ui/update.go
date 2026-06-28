package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// Init initializes background loop clock ticks for state rendering animations.
func (m *model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), animTickCmd())
}

// Update acts as the central state machine processor routing keyboard events and framework messages.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		vpCmd tea.Cmd
		tiCmd tea.Cmd
	)

	switch msg := msg.(type) {

	// ── Window Sizing — dynamic dimensions & viewport calculation ─────────────
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ti.Width = msg.Width - 8

		vpH := m.viewportHeight()

		if !m.vpReady {
			m.vp = viewport.New(msg.Width, vpH)
			m.vp.YPosition = 0
			m.vp.HighPerformanceRendering = false
			m.vpReady = true

			// Initialize initial state structure: display welcome banner on fresh boot
			m.showBanner = true
			m.rebuildViewport()
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = vpH
			m.rebuildViewport()
		}
		return m, nil

	// ── Spinner Tick (100ms Clocks) ───────────────────────────────────────────
	case tickMsg:
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			if m.vpReady {
				m.rebuildViewport()
			}
		}
		return m, tickCmd()

	// ── Animation Tick (25ms Clocks) — mode line color fader ──────────────────
	case animTickMsg:
		if m.lineAnimating {
			m.lineAnimProgress += 25.0 / 150.0
			if m.lineAnimProgress >= 1.0 {
				m.lineAnimProgress = 1.0
				m.lineAnimating = false
			}
		}
		return m, animTickCmd()

	// ── Agent Async Core Pipelines ────────────────────────────────────────────
	case agentDoneMsg:
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = msg.label
		m.rebuildViewport()
		return m, nil

	case investigateResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "investigation error: "+msg.err.Error())
			return m, nil
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetInvestigationID(msg.sessionKey)
		}
		return m, nil

	case reviewResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "review error: "+msg.err.Error())
			return m, nil
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetReviewID(msg.sessionKey)
		}
		if msg.saveReportFn != nil {
			msg.saveReportFn()
		}
		return m, nil

	case commitGeneratedMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "commit error: "+msg.err.Error())
			return m, nil
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("commit: %s", msg.subject)))
		if msg.body != "" {
			for _, l := range strings.Split(msg.body, "\n") {
				m.push(roleSystem, infoStyle.Render(l))
			}
		}
		m.push(roleStatus, fmt.Sprintf("amended as %s", msg.hash))
		return m, nil

	// ── LLM Token Data Streaming Engines ──────────────────────────────────────
	case tokenMsg:
		m.responseBuffer.WriteString(string(msg))
		if m.vpReady {
			m.rebuildViewport()
		}
		return m, m.readStream()

	case streamDoneMsg:
		m.streamCh = nil
		m.streaming = false
		m.tokenInput += msg.tokenInput
		m.tokenOutput += msg.tokenOutput
		final := msg.content
		if final == "" {
			final = m.responseBuffer.String()
		}
		m.responseBuffer.Reset()

		// Run lines processing pipeline through high-fidelity code diff syntax scanner
		highlighted := highlightOutput(final)
		for _, l := range strings.Split(highlighted, "\n") {
			m.records = append(m.records, record{role: roleAI, text: l})
		}

		total := m.tokenInput + m.tokenOutput
		tokStr := fmt.Sprintf("%d/32k tokens", total)
		if total >= 1000 {
			tokStr = fmt.Sprintf("%.1fk/32k tokens", float64(total)/1000)
		}
		costStr := "$0.00"
		if m.cfg.ActiveProviderName() != "ollama" {
			c := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
			costStr = fmt.Sprintf("$%.4f", c)
		}
		doneStr := fmt.Sprintf("✓ done  •  %s  •  %s", tokStr, costStr)
		m.push(roleStatus, doneStr)

		if m.resolver.Current() == modes.ModeBuild && m.state != StateAwaitingApproval {
			props := extractBuildProposals(final)
			diffProps := extractDiffPatches(final)
			if len(diffProps) > 0 {
				existing := make(map[string]bool)
				for _, p := range props {
					existing[p.File] = true
				}
				for _, d := range diffProps {
					if !existing[d.File] {
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
							File:     p.File,
							Modified: p.Content,
						}
						orig, err := os.ReadFile(p.File)
						if err == nil {
							patch.Original = string(orig)
						}
						if err := m.execEng.Patches.Apply(patch); err != nil {
							m.push(roleError, "apply failed: "+err.Error())
						} else {
							applied++
							m.push(roleSystem, infoStyle.Render("applied: "+p.File))
						}
					}
					if applied > 0 {
						m.createBuildCheckpoint(applied)
					}
				} else {
					m.pendingProposals = props
					m.state = StateAwaitingApproval
					m.awaitingConfirmation = true
					msg := "proposed changes:"
					for _, p := range props {
						msg += fmt.Sprintf("\n  • %s", p.File)
					}
					m.push(roleSystem, infoStyle.Render(msg))
				}
			}
		}
		m.rebuildViewport()
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.push(roleError, "stream error: "+msg.err.Error())
		return m, nil

	// ── High-Fidelity Keyboard Navigation Event Interception ─────────────────
	case tea.KeyMsg:
		// Reset state trigger catch: intercept /clear sequence to pop banner back out
		if strings.TrimSpace(m.ti.Value()) == "/clear" && msg.String() == "enter" {
			m.showBanner = true
		} else if msg.String() == "enter" && strings.TrimSpace(m.ti.Value()) != "" {
			// On chat submission: hide banner immediately to maximize code layout real estate
			m.showBanner = false
		}

		// Handle standalone vertical scrolling if suggestion dropdowns are inactive
		if !m.showSuggestions && !m.streaming && !m.agentRunning {
			switch msg.Type {
			case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown:
				m.vp, vpCmd = m.vp.Update(msg)
				return m, vpCmd
			}
		}

		// Route all keystrokes into your custom autocomplete / interaction logic pipeline.
		// handleKey handles updating the text input fields internally and returns the updated model/commands.
		resModel, cmd := m.handleKey(msg)
		return resModel, cmd
	}

	// Dynamic fallback sequence propagation across active containers
	m.vp, vpCmd = m.vp.Update(msg)
	m.ti, tiCmd = m.ti.Update(msg)
	return m, tea.Batch(vpCmd, tiCmd)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func animTickCmd() tea.Cmd {
	return tea.Tick(25*time.Millisecond, func(t time.Time) tea.Msg {
		return animTickMsg(t)
	})
}
