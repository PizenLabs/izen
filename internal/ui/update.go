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

// parseSGRMouse parses an SGR mouse sequence of the form
// "ESC[<BUTTON;COL;ROW M/m" or "[<BUTTON;COL;ROW M/m" and returns (button, col, row, press, ok).
// button 64/65 = wheel up/down, 0 = left click.
func parseSGRMouse(s string) (button, col, row int, press, ok bool) {
	s = strings.TrimPrefix(s, "\x1b")
	if !strings.HasPrefix(s, "[<") {
		return
	}
	if !strings.HasSuffix(s, "M") && !strings.HasSuffix(s, "m") {
		return
	}
	press = strings.HasSuffix(s, "M")
	payload := s[2 : len(s)-1] // strip "[<" prefix and M/m suffix
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

// isSGRMouseLeak returns true if s is a complete or partial SGR mouse sequence
// that leaked through from tmux. Used as a fast pre-check before full parse.
func isSGRMouseLeak(s string) bool {
	if strings.Contains(s, "[<") || strings.Contains(s, "\x1b[<") {
		return true
	}
	// X10 normal encoding fallback
	if strings.HasPrefix(s, "\x1b[M") || strings.HasPrefix(s, "\x1b[m") {
		return true
	}
	return false
}

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

// dispatchMouseLeak parses an SGR mouse leak string and executes the matching
// viewport action. A single KeyMsg value may contain multiple concatenated
// sequences (e.g. "[<64;78;16M[<64;78;16M") from fast scrolling — we split and
// process each one.
func (m *model) dispatchMouseLeak(s string) {
	for _, seq := range sgrMouseLeaks(s) {
		button, _, row, press, ok := parseSGRMouse(seq)
		if !ok {
			continue
		}
		if !press {
			continue // ignore release events
		}
		switch button {
		case 64: // wheel up
			m.vp.LineUp(3)
		case 65: // wheel down
			m.vp.LineDown(3)
		case 0, 1, 2: // mouse click — copy line under cursor
			content := m.vp.View()
			lines := strings.Split(content, "\n")
			if row > 0 && row <= len(lines) {
				line := strings.TrimRight(lines[row-1], " ")
				_ = clipboard.WriteAll(line)
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
	// Check for KeyMsg containing SGR mouse leaks BEFORE the type switch.
	// This is the earliest possible interception point and catches all variants
	// including the concatenated multi-sequence strings seen in the screenshot.
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		if isSGRMouseLeak(kmsg.String()) {
			if m.vpReady {
				m.dispatchMouseLeak(kmsg.String())
			}
			return m, nil // swallow entirely — do not fall through
		}
	}

	// ── STAGE 1: Native tea.MouseMsg (when WithMouseCellMotion is active) ────
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
			if mouseMsg.Y < len(lines) {
				line := lines[mouseMsg.Y]
				line = strings.TrimRight(line, " ")
				if err := clipboard.WriteAll(line); err != nil {
					// Safely ignore clipboard engine warnings
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
		// Stage 0 already filtered all mouse leaks above — any KeyMsg reaching
		// here is a genuine key event.

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

	// Only forward genuine key events to the text input.
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		// Stage 0 should have already caught these, but belt-and-suspenders.
		if isSGRMouseLeak(kmsg.String()) {
			m.ti, tiCmd = m.ti, nil
		} else {
			m.ti, tiCmd = m.ti.Update(msg)
		}
	} else {
		m.ti, tiCmd = m.ti, nil
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
