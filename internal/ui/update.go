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
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// Init initializes background loop clock ticks for state rendering animations.
func (m *model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), animTickCmd())
}

// ── Mouse leak interception ──────────────────────────────────────────────────

var sgrMousePattern = regexp.MustCompile(`(?:\x1b)?\[<(\d+);(\d+);(\d+)([Mm])`)
var mouseFragmentRegex = regexp.MustCompile(`\[<\d*;?\d*;?\d*[Mm]?`)

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

func isSGRMouseLeak(s string) bool {
	if strings.Contains(s, "[<") || strings.Contains(s, "\x1b[<") {
		return true
	}
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

// dispatchMouseLeak handles raw escape data stream from input buffer.
func (m *model) dispatchMouseLeak(s string) {
	for _, seq := range sgrMouseLeaks(s) {
		button, col, row, press, ok := parseSGRMouse(seq)
		if !ok {
			continue
		}

		// Normalize Ghostty + Shift protocol bindings (buttons 0, 4, 32, 36)
		switch button {
		case 64: // Wheel up
			if press {
				m.vp.LineUp(3)
			}
		case 65: // Wheel down
			if press {
				m.vp.LineDown(3)
			}
		case 0, 4, 32, 36:
			point, inside := m.viewportPoint(col-1, row-1)

			// Handle mouse down press
			if press && (button == 0 || button == 4) {
				if inside {
					m.mouseSelecting = true
					m.startMouseRow = point.row
					m.startMouseCol = point.col
					m.currentMouseRow = point.row
					m.currentMouseCol = point.col
				}
				continue
			}

			// Handle active motion updates
			if press && (button == 32 || button == 36) {
				if m.mouseSelecting && inside {
					m.currentMouseRow = point.row
					m.currentMouseCol = point.col
				}
				continue
			}

			// Absolute release safety guarantee trigger
			if !press {
				if m.mouseSelecting {
					m.mouseSelecting = false
					end := mouseSelectionPoint{row: m.currentMouseRow, col: m.currentMouseCol}
					if inside {
						end = point
					}
					m.copyMouseSelection(end)
				}
			}
		}
	}
}

// mouseSelectionPoint represents a point in the viewport.
type mouseSelectionPoint struct {
	row int
	col int
}

// viewportPoint converts viewport-relative coordinates to buffer coordinates.
func (m *model) viewportPoint(x, y int) (mouseSelectionPoint, bool) {
	if !m.vpReady {
		return mouseSelectionPoint{}, false
	}

	adjustedY := y - focusLineHeight
	if adjustedY < 0 || adjustedY >= m.vp.Height {
		return mouseSelectionPoint{}, false
	}

	if x < 0 {
		x = 0
	}
	maxWidth := m.width
	if maxWidth <= 0 {
		maxWidth = m.vp.Width
	}
	if maxWidth > 0 && x >= maxWidth {
		x = maxWidth - 1
	}

	return mouseSelectionPoint{row: m.vp.YOffset + adjustedY, col: x}, true
}

// selectedViewportText returns the selected text in the viewport.
func selectedViewportText(lines []string, start, end mouseSelectionPoint) string {
	if len(lines) == 0 {
		return ""
	}

	sRow, sCol := start.row, start.col
	eRow, eCol := end.row, end.col

	if sRow < 0 {
		sRow = 0
	}
	if eRow < 0 {
		eRow = 0
	}
	if sRow >= len(lines) {
		sRow = len(lines) - 1
	}
	if eRow >= len(lines) {
		eRow = len(lines) - 1
	}

	if sRow > eRow || (sRow == eRow && sCol > eCol) {
		sRow, eRow = eRow, sRow
		sCol, eCol = eCol, sCol
	}

	var selected strings.Builder
	for row := sRow; row <= eRow; row++ {
		rawLine := ansi.Strip(lines[row])
		line := []rune(rawLine)
		lineLen := len(line)

		if lineLen == 0 {
			if row < eRow {
				selected.WriteByte('\n')
			}
			continue
		}

		startCol := 0
		if row == sRow {
			startCol = sCol
		}

		endCol := lineLen
		if row == eRow {
			endCol = eCol + 1
		}

		if startCol < 0 {
			startCol = 0
		}
		if startCol > lineLen {
			startCol = lineLen
		}
		if endCol < 0 {
			endCol = 0
		}
		if endCol > lineLen {
			endCol = lineLen
		}
		if endCol < startCol {
			endCol = startCol
		}

		chunk := string(line[startCol:endCol])
		selected.WriteString(strings.TrimRight(chunk, " \t"))

		if row < eRow {
			selected.WriteByte('\n')
		}
	}

	return strings.TrimSpace(selected.String())
}

// copyMouseSelection copies the selected text to the clipboard.
func (m *model) copyMouseSelection(end mouseSelectionPoint) {
	content := m.vp.View()
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return
	}

	localStart := mouseSelectionPoint{
		row: m.startMouseRow - m.vp.YOffset,
		col: m.startMouseCol,
	}
	localEnd := mouseSelectionPoint{
		row: end.row - m.vp.YOffset,
		col: end.col,
	}

	text := selectedViewportText(lines, localStart, localEnd)
	if text != "" {
		_ = clipboard.WriteAll(text)
	}
}

// Update maps layout engines and routes state machines.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		vpCmd tea.Cmd
		tiCmd tea.Cmd
	)

	// ── STAGE 0: Pre-switch raw mouse tracking intercept ──────────────────────
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		if isSGRMouseLeak(kmsg.String()) {
			if m.vpReady {
				m.dispatchMouseLeak(kmsg.String())
			}
			return m, nil
		}
	}

	// ── STAGE 1: Standard unified system tea.MouseMsg handling ────────────────
	if _, isMouse := msg.(tea.MouseMsg); isMouse {
		if !m.vpReady {
			return m, nil
		}
		mouseMsg := msg.(tea.MouseMsg)

		// Unified scroll handling
		if mouseMsg.Button == tea.MouseButtonWheelUp || mouseMsg.Type == tea.MouseWheelUp {
			m.vp.LineUp(3)
			return m, nil
		}
		if mouseMsg.Button == tea.MouseButtonWheelDown || mouseMsg.Type == tea.MouseWheelDown {
			m.vp.LineDown(3)
			return m, nil
		}

		// Handle bubbletea standard mouse structure action events
		switch mouseMsg.Action {
		case tea.MouseActionPress:
			point, ok := m.viewportPoint(mouseMsg.X, mouseMsg.Y)
			if !ok {
				m.mouseSelecting = false
				return m, nil
			}
			m.mouseSelecting = true
			m.startMouseRow = point.row
			m.startMouseCol = point.col
			m.currentMouseRow = point.row
			m.currentMouseCol = point.col
			return m, nil

		case tea.MouseActionMotion:
			if m.mouseSelecting {
				if point, ok := m.viewportPoint(mouseMsg.X, mouseMsg.Y); ok {
					m.currentMouseRow = point.row
					m.currentMouseCol = point.col
				}
			}
			return m, nil

		case tea.MouseActionRelease:
			if m.mouseSelecting {
				m.mouseSelecting = false
				end := mouseSelectionPoint{row: m.currentMouseRow, col: m.currentMouseCol}
				if point, ok := m.viewportPoint(mouseMsg.X, mouseMsg.Y); ok {
					end = point
				}
				m.copyMouseSelection(end)
			}
			return m, nil
		}

		return m, nil
	}

	switch msg := msg.(type) {

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

	case tickMsg:
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			if m.vpReady {
				m.rebuildViewport()
			}
		}
		return m, tickCmd()

	case animTickMsg:
		if m.lineAnimating {
			m.lineAnimProgress += 25.0 / 150.0
			if m.lineAnimProgress >= 1.0 {
				m.lineAnimProgress = 1.0
				m.lineAnimating = false
			}
		}
		return m, animTickCmd()

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

		// Show delta tokens for this turn in the status message
		delta := msg.tokenInput + msg.tokenOutput
		deltaStr := fmt.Sprintf("%d", delta)
		if delta >= 1000 {
			deltaStr = fmt.Sprintf("%.1fk", float64(delta)/1000)
		}
		costStr := "$0.00"
		if m.cfg.ActiveProviderName() != "ollama" {
			c := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
			costStr = fmt.Sprintf("$%.4f", c)
		}
		m.push(roleStatus, fmt.Sprintf("done - +%s tokens (this turn)  •  %s", deltaStr, costStr))

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
						m.ti.CursorEnd()
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

	if !m.mouseSelecting {
		m.vp, vpCmd = m.vp.Update(msg)
	}

	m.ti, tiCmd = m.ti.Update(msg)

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
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func animTickCmd() tea.Cmd {
	return tea.Tick(25*time.Millisecond, func(t time.Time) tea.Msg { return animTickMsg(t) })
}
