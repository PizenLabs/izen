package ui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// Init initializes background loop clock ticks for state rendering animations.
func (m *model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), animTickCmd())
}

// ── Mouse leak interception ───────────────────────────────────────────────────
//
// Under tmux with `set mouse = on`, SGR mouse byte sequences bypass Bubbletea's
// mouse decoder and arrive as raw tea.KeyMsg strings. The pattern is always:
//
//   [<BUTTON;COL;ROW M   (press)
//   [<BUTTON;COL;ROW m   (release)
//
// tmux consumes the leading ESC so we only see the bare bracket form.
// Each scroll tick emits multiple of these in rapid succession — that's why
// the input box fills with repeated "[<64;78;16M[<64;78;16M..." strings.
//
// Strategy: detect the pattern at the earliest possible point (before the type
// switch), parse the button byte, and dispatch as a real viewport action.

var sgrMousePattern = regexp.MustCompile(`(?:\x1b)?\[<(\d+);(\d+);(\d+)([Mm])`)

// mouseFragmentRegex targets broken or single-character escape remnants
// that leak sequentially when scrolling past the top/bottom viewport boundaries.
var mouseFragmentRegex = regexp.MustCompile(`\[<\d*;?\d*;?\d*[Mm]?`)

// parseSGRMouse decodes raw SGR protocol payload dimensions.
func parseSGRMouse(s string) (button, col, row int, press, ok bool) {
	s = strings.TrimPrefix(s, "\x1b")
	if !strings.HasPrefix(s, "[<") {
		return
	}
	if !strings.HasSuffix(s, "M") && !strings.HasSuffix(s, "m") {
		return
	}
	press = strings.HasSuffix(s, "M")
	payload := s[2 : len(s)-1]
	parts := strings.Split(payload, ";")
	if len(parts) != 3 {
		return
	}
	var e1, e2, e3 error
	button, e1 = strconv.Atoi(parts[0])
	col, e2 = strconv.Atoi(parts[1])
	row, e3 = strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return
	}
	ok = true
	return
}

// isSGRMouseLeak performs structural heuristic scans for terminal control markers.
func isSGRMouseLeak(s string) bool {
	if strings.Contains(s, "[<") || strings.Contains(s, "\x1b[<") {
		return true
	}
	if strings.HasPrefix(s, "\x1b[M") || strings.HasPrefix(s, "\x1b[m") {
		return true
	}
	return false
}

// sgrMouseLeaks extracts well-formed discrete sequence components.
func sgrMouseLeaks(s string) []string {
	matches := sgrMousePattern.FindAllString(s, -1)
	if len(matches) > 0 {
		return matches
	}
	if isSGRMouseLeak(s) {
		return []string{s}
	}
	return nil
}

// dispatchMouseLeak converts low-level control codes into viewport structural operations.
func (m *model) dispatchMouseLeak(s string) {
	for _, seq := range sgrMouseLeaks(s) {
		button, _, row, press, ok := parseSGRMouse(seq)
		if !ok || !press {
			continue
		}
		switch button {
		case 64: // Wheel up
			m.vp.LineUp(3)
		case 65: // Wheel down
			m.vp.LineDown(3)
		case 0, 1, 2: // Left/Middle/Right click from raw SGR streams
			if !m.vpReady {
				continue
			}
			content := m.vp.View()
			lines := strings.Split(content, "\n")
			// SGR rows are 1-indexed. Offset by focusLineHeight to map correctly into the viewport space.
			visualRow := row - 1 - focusLineHeight
			if visualRow >= 0 && visualRow < len(lines) {
				line := strings.TrimRight(lines[visualRow], " ")
				if line != "" {
					_ = clipboard.WriteAll(line)
				}
			}
		}
	}
}

// Update acts as the central state machine processor routing keyboard events and framework messages.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		vpCmd tea.Cmd
		tiCmd tea.Cmd
	)

	// ── STAGE 0: Pre-switch mouse intercept ──────────────────────────────────
	// Intercept incoming key messages if they match SGR escape signatures.
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		if isSGRMouseLeak(kmsg.String()) {
			if m.vpReady {
				m.dispatchMouseLeak(kmsg.String())
			}
			return m, nil
		}
	}

	// ── STAGE 1: Native tea.MouseMsg handling ───────────────────────────────
	// Handles mouse signals routed when WithMouseCellMotion context flags are enabled.
	if _, isMouse := msg.(tea.MouseMsg); isMouse {
		if !m.vpReady {
			return m, nil
		}
		mouseMsg := msg.(tea.MouseMsg)
		switch mouseMsg.Type {
		case tea.MouseWheelUp:
			m.vp.LineUp(3)
			return m, nil
		case tea.MouseWheelDown:
			m.vp.LineDown(3)
			return m, nil
		case tea.MouseLeft:
			content := m.vp.View()
			lines := strings.Split(content, "\n")
			// Normalize Y-axis to match the structural viewport position by subtracting layout offset height.
			visualRow := mouseMsg.Y - focusLineHeight
			if visualRow >= 0 && visualRow < len(lines) {
				line := lines[visualRow]
				line = strings.TrimRight(line, " ")
				if line != "" {
					_ = clipboard.WriteAll(line)
				}
			}
			return m, nil
		}
		return m, nil
	}

	switch msg := msg.(type) {

	// ── Window Sizing ─────────────────────────────────────────────────────────
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
			m.showBanner = true
			m.rebuildViewport()
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = vpH
			m.rebuildViewport()
		}
		return m, nil

	// ── Spinner Tick (100ms) ──────────────────────────────────────────────────
	case tickMsg:
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			if m.vpReady {
				m.rebuildViewport()
			}
		}
		return m, tickCmd()

	// ── Animation Tick (25ms) ─────────────────────────────────────────────────
	case animTickMsg:
		if m.lineAnimating {
			m.lineAnimProgress += 25.0 / 150.0
			if m.lineAnimProgress >= 1.0 {
				m.lineAnimProgress = 1.0
				m.lineAnimating = false
			}
		}
		return m, animTickCmd()

	// ── Agent pipelines ───────────────────────────────────────────────────────
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

	// ── Streaming ─────────────────────────────────────────────────────────────
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
		m.push(roleStatus, fmt.Sprintf("✓ done  •  %s  •  %s", tokStr, costStr))

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
					proposalMsg := "proposed changes:"
					for _, p := range props {
						proposalMsg += fmt.Sprintf("\n    • %s", p.File)
					}
					m.push(roleSystem, infoStyle.Render(proposalMsg))
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

	// ── Keyboard ──────────────────────────────────────────────────────────────
	case tea.KeyMsg:
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
					}
				}
				return m, nil

			case tea.KeyPgUp, tea.KeyPgDown:
				m.vp, vpCmd = m.vp.Update(msg)
				return m, vpCmd
			}
		}

		resModel, cmd := m.handleKey(msg)
		return resModel, cmd
	}

	// ── Fallback propagation ──────────────────────────────────────────────────
	m.vp, vpCmd = m.vp.Update(msg)
	m.ti, tiCmd = m.ti.Update(msg)

	// Fallback Guard: Scan and purge asynchronous sequence fragments or leaks
	// from accumulating inside the text input field value.
	val := m.ti.Value()
	if strings.Contains(val, "[<") || mouseFragmentRegex.MatchString(val) {
		cleanVal := mouseFragmentRegex.ReplaceAllString(val, "")
		if cleanVal != val {
			m.ti.SetValue(cleanVal)
			m.ti.CursorEnd()
		}
	}

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
