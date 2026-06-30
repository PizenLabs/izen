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

// ── Mouse Leak Interception & Buffering ──────────────────────────────────────

var sgrMousePattern = regexp.MustCompile(`(?:\x1b)?\[<(\d+);(\d+);(\d+)([Mm])`)
var mouseFragmentRegex = regexp.MustCompile(`\[<.*|\[+.*|<+.*|;+.*`)

// Global timestamp tracker to trap split microsecond terminal driver leaks
var lastAnyMouseActivity time.Time

// parseSGRMouse decodes raw SGR mouse sequences into structured data components.
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

// isSGRMouseLeak detects if a raw key sequence contains mouse tracking signatures.
func isSGRMouseLeak(s string) bool {
	if strings.Contains(s, "[<") || strings.Contains(s, "\x1b[<") {
		return true
	}
	if strings.HasPrefix(s, "\x1b[M") || strings.HasPrefix(s, "\x1b[m") {
		return true
	}
	return false
}

// sgrMouseLeaks extracts valid mouse tracking sequence matches from text streams.
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

// dispatchMouseLeak processes raw control sequences bypassed by terminal protocol overrides.
func (m *model) dispatchMouseLeak(s string) {
	for _, seq := range sgrMouseLeaks(s) {
		button, col, row, press, ok := parseSGRMouse(seq)
		if !ok {
			continue
		}

		switch button {
		case 64, 96: // Fallback Wheel up sequence
			m.mouseSelecting = false
			m.vp.LineUp(3)
			m.rebuildViewport()
		case 65, 97: // Fallback Wheel down sequence
			m.mouseSelecting = false
			m.vp.LineDown(3)
			m.rebuildViewport()
		case 0, 4, 32, 36:
			point, inside := m.viewportPoint(col-1, row-1)

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

			if press && (button == 32 || button == 36) {
				if m.mouseSelecting && inside {
					m.currentMouseRow = point.row
					m.currentMouseCol = point.col
				}
				continue
			}

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

// mouseSelectionPoint represents a specific cell position in the buffer.
type mouseSelectionPoint struct {
	row int
	col int
}

// viewportPoint translates viewport-relative screen positions into global buffer indices.
func (m *model) viewportPoint(x, y int) (mouseSelectionPoint, bool) {
	if !m.vpReady {
		return mouseSelectionPoint{}, false
	}

	if y < 0 || y >= m.vp.Height {
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

	return mouseSelectionPoint{row: m.vp.YOffset + y, col: x}, true
}

// selectedViewportText extracts and strips ANSI sequences from a bound grid coordinate region.
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
		if row >= len(lines) {
			break
		}
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

// copyMouseSelection coordinates viewport context maps and writes matching text to clipboard.
func (m *model) copyMouseSelection(end mouseSelectionPoint) {
	lines := m.viewLines
	if len(lines) == 0 {
		content := m.vp.View()
		lines = strings.Split(content, "\n")
	}
	if len(lines) == 0 {
		return
	}

	startPoint := mouseSelectionPoint{row: m.startMouseRow, col: m.startMouseCol}
	text := selectedViewportText(lines, startPoint, end)
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

	now := time.Now()

	// ── STAGE 0: Absolute Time Shield & Broken Sequence Interception ──────────
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		rawStr := kmsg.String()

		// 1. Check if the keypress event falls within the active mouse tracking window
		if now.Sub(lastAnyMouseActivity) < 150*time.Millisecond {
			// Catch single or repeating control artifacts leaked at scrolling boundaries
			if rawStr == "[" || rawStr == "<" || rawStr == ";" || rawStr == "\x1b" ||
				strings.HasPrefix(rawStr, "m") || strings.HasPrefix(rawStr, "M") ||
				strings.Contains(rawStr, "[") || strings.Contains(rawStr, "<") ||
				mouseFragmentRegex.MatchString(rawStr) {
				return m, nil // Swallow the ghost character completely
			}
		}

		// 2. Instantly capture explicit inline bypass SGR mouse wheel sequences
		if strings.Contains(rawStr, "[<64;") || strings.Contains(rawStr, "<64;") ||
			strings.Contains(rawStr, "[<96;") || strings.Contains(rawStr, "<96;") {
			lastAnyMouseActivity = now
			m.mouseSelecting = false
			m.vp.LineUp(3)
			m.rebuildViewport()
			return m, nil
		}
		if strings.Contains(rawStr, "[<65;") || strings.Contains(rawStr, "<65;") ||
			strings.Contains(rawStr, "[<97;") || strings.Contains(rawStr, "<97;") {
			lastAnyMouseActivity = now
			m.mouseSelecting = false
			m.vp.LineDown(3)
			m.rebuildViewport()
			return m, nil
		}

		// 3. Consume structural raw SGR mouse leaks natively
		if isSGRMouseLeak(rawStr) {
			lastAnyMouseActivity = now
			if m.vpReady {
				m.dispatchMouseLeak(rawStr)
			}
			return m, nil
		}
	}

	// ── STAGE 1: Standard structured system tea.MouseMsg handling ─────────────
	if _, isMouse := msg.(tea.MouseMsg); isMouse {
		lastAnyMouseActivity = now
		if !m.vpReady {
			return m, nil
		}
		mouseMsg := msg.(tea.MouseMsg)

		switch mouseMsg.Type {
		case tea.MouseWheelUp:
			m.mouseSelecting = false
			m.vp.LineUp(3)
			m.rebuildViewport()
			return m, nil

		case tea.MouseWheelDown:
			m.mouseSelecting = false
			m.vp.LineDown(3)
			m.rebuildViewport()
			return m, nil

		case tea.MouseLeft:
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

		case tea.MouseMotion:
			if m.mouseSelecting {
				if point, ok := m.viewportPoint(mouseMsg.X, mouseMsg.Y); ok {
					m.currentMouseRow = point.row
					m.currentMouseCol = point.col
				}
			}
			return m, nil

		case tea.MouseRelease:
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

		m.records = append(m.records, record{role: roleAI, text: final})

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

	// ── STAGE 2: Viewport Update Deflection ───────────────────────────────────
	// Block standard bubbletea viewport internal mapping from routing if mouse selection is in progress.
	if !m.mouseSelecting {
		m.vp, vpCmd = m.vp.Update(msg)
	}

	m.ti, tiCmd = m.ti.Update(msg)
	return m, tea.Batch(vpCmd, tiCmd)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func animTickCmd() tea.Cmd {
	return tea.Tick(25*time.Millisecond, func(t time.Time) tea.Msg { return animTickMsg(t) })
}
