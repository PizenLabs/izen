package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// beginQuitConfirm opens the exit-safety modal. It is the ONLY path to clean
// shutdown: /quit, /q, and Ctrl+D all funnel through here instead of invoking
// cleanShutdownCmd directly. The dialog defaults to [ No ] so a stray Enter
// can never exit the application accidentally.
func (m *model) beginQuitConfirm() {
	m.pendingQuitConfirm = true
	m.quitConfirmYes = false
	m.dismissAutocomplete()
}

// cancelQuitConfirm dismisses the modal and safely returns focus to the
// interactive StateChat.
func (m *model) cancelQuitConfirm() {
	m.pendingQuitConfirm = false
	m.quitConfirmYes = false
	m.state = StateChat
	m.ti.Focus()
}

// handleQuitConfirmKey routes every key while the modal is open. Input is fully
// frozen: only navigation (←/→/Tab/h/l), cancel (Esc/Ctrl+C/n/N), and confirm
// (Enter on [ Yes ], or y/Y) are honored.
func (m *model) handleQuitConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
		m.quitConfirmYes = !m.quitConfirmYes
	case tea.KeyEscape, tea.KeyCtrlC:
		m.cancelQuitConfirm()
	case tea.KeyEnter:
		if m.quitConfirmYes {
			m.pendingQuitConfirm = false
			return m.cleanShutdownCmd()
		}
		m.cancelQuitConfirm()
	default:
		if msg.Type == tea.KeyRunes {
			switch msg.String() {
			case "h", "l":
				m.quitConfirmYes = !m.quitConfirmYes
			case "n", "N":
				m.cancelQuitConfirm()
			case "y", "Y":
				m.pendingQuitConfirm = false
				return m.cleanShutdownCmd()
			}
		}
	}
	return nil
}

// ── Quit-confirm modal rendering ─────────────────────────────────────────

// renderQuitConfirmModal renders the centered confirmation dialog. contentW is
// the width of the inner content area; the border and padding add to the final
// box width.
func (m *model) renderQuitConfirmModal(contentW int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	bodyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	activeOptStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	idleOptStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))

	title := titleStyle.Render("QUIT IZEN")
	question := bodyStyle.Render("Are you sure you want to exit the current session?")

	var noOpt, yesOpt string
	if m.quitConfirmYes {
		yesOpt = activeOptStyle.Render("► [ Yes ]")
		noOpt = idleOptStyle.Render("  [ No ]")
	} else {
		noOpt = activeOptStyle.Render("► [ No ]")
		yesOpt = idleOptStyle.Render("  [ Yes ]")
	}
	options := noOpt + "     " + yesOpt

	content := lipgloss.JoinVertical(lipgloss.Center,
		title,
		"",
		question,
		"",
		options,
	)

	return lipgloss.NewStyle().
		Width(contentW).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorAccent)).
		Padding(1, 2).
		Render(content)
}

// renderQuitConfirmOverlay dims the underlying workspace view and centers the
// confirmation dialog on top of it, so the session stays faintly visible
// (blurred) behind the modal.
func (m *model) renderQuitConfirmOverlay(base string) string {
	width := m.width
	height := m.height

	contentW := width - 12
	if contentW > 60 {
		contentW = 60
	}
	if contentW < 20 {
		contentW = 20
	}

	modal := m.renderQuitConfirmModal(contentW)
	modalLines := strings.Split(strings.TrimRight(modal, "\n"), "\n")
	modalW := 0
	for _, ln := range modalLines {
		if w := lipgloss.Width(ln); w > modalW {
			modalW = w
		}
	}
	modalH := len(modalLines)

	// Dim/blur the background: strip ANSI and ghost the workspace text so the
	// dialog reads as the focus while the session remains faintly visible.
	plain := ansi.Strip(base)
	dimmed := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorDimmed)).Render(plain)

	baseLines := strings.Split(dimmed, "\n")
	if len(baseLines) > height {
		baseLines = baseLines[:height]
	}
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}
	for i := range baseLines {
		if w := lipgloss.Width(baseLines[i]); w < width {
			baseLines[i] += strings.Repeat(" ", width-w)
		}
	}

	top := (height - modalH) / 2
	if top < 0 {
		top = 0
	}
	left := (width - modalW) / 2
	if left < 0 {
		left = 0
	}
	for i := 0; i < modalH && top+i < len(baseLines); i++ {
		ml := modalLines[i]
		tail := width - left - lipgloss.Width(ml)
		if tail < 0 {
			tail = 0
		}
		baseLines[top+i] = strings.Repeat(" ", left) + ml + strings.Repeat(" ", tail)
	}
	return strings.Join(baseLines, "\n")
}
