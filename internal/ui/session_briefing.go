package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/session"
)

// resumeBriefingData is the session-scoped orientation payload rendered at the
// top of the fresh viewport when a dormant session is resumed. It is data, not
// a pre-rendered string, so a terminal resize re-wraps it automatically.
type resumeBriefingData struct {
	Slot          session.SlotID
	Title         string
	Tokens        int
	Turns         int
	Model         string
	ContextWindow int
}

// setResumeBriefing captures the orientation snapshot for a resumed session and
// repaints the viewport. It never injects legacy chat history: the resumed
// viewport shows only this briefing.
func (m *model) setResumeBriefing(slot session.SlotID, sess *session.Session) {
	if m == nil || sess == nil {
		return
	}
	title := strings.TrimSpace(sess.EffectiveTitle())
	if title == "" {
		title = "(untitled session)"
	}
	model := strings.TrimSpace(sess.Model)
	if model == "" {
		model = strings.TrimSpace(m.getActiveModelName())
	}
	m.resumeBriefing = &resumeBriefingData{
		Slot:          slot,
		Title:         title,
		Tokens:        session.EstimatedHistoryTokens(sess.History),
		Turns:         session.UserTurns(sess.History),
		Model:         model,
		ContextWindow: m.contextWindowForModel(model),
	}
	// The briefing replaces the fresh-launch banner for a resumed session.
	m.showBanner = false
	if m.Ready {
		m.refreshViewportContent()
	}
}

// clearResumeBriefing drops the briefing so a new conversation starts from a
// clean viewport.
func (m *model) clearResumeBriefing() {
	if m == nil {
		return
	}
	m.resumeBriefing = nil
}

// contextWindowForModel resolves the context-window denominator for a model,
// preferring the live session limit, then the model catalog, then the 128k
// default.
func (m *model) contextWindowForModel(model string) int {
	if model != "" {
		if w := llm.ContextWindowFor(model); w > 0 {
			return w
		}
	}
	if m != nil && m.ContextLimit > 0 {
		return m.ContextLimit
	}
	return 128000
}

// renderResumeBriefing renders the lightweight LipGloss orientation banner for
// the top of the fresh viewport. It returns "" when no briefing is staged. It
// displays the slot id, the full goal/title, token & turn metrics, and the
// active model assignment.
func (m *model) renderResumeBriefing() string {
	if m == nil || m.resumeBriefing == nil {
		return ""
	}
	b := m.resumeBriefing

	termWidth := m.PaneWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	// Reserve room for the banner border and horizontal padding.
	contentWidth := termWidth - 8
	if contentWidth < 16 {
		contentWidth = 16
	}

	pct := 0
	if b.ContextWindow > 0 && b.Tokens > 0 {
		pct = b.Tokens * 100 / b.ContextWindow
		if pct > 100 {
			pct = 100
		}
	}
	metrics := fmt.Sprintf("%s / %s tokens (%d%%) · %d turn(s)",
		formatTokenCount(b.Tokens), formatTokenCount(b.ContextWindow), pct, b.Turns)

	modelLabel := b.Model
	if modelLabel == "" {
		modelLabel = "(unassigned)"
	}

	goalLines := wrapText(b.Title, contentWidth)
	// Bound the goal to at most two lines so the banner stays lightweight.
	const maxGoalLines = 2
	if len(goalLines) > maxGoalLines {
		goalLines = goalLines[:maxGoalLines]
		last := goalLines[maxGoalLines-1]
		goalLines[maxGoalLines-1] = ansi.Truncate(last, contentWidth, "…")
	}

	rows := make([]string, 0, 2+len(goalLines))
	rows = append(rows, boldTextStyle.Render("Goal ")+textStyle.Render(goalLines[0]))
	for _, gl := range goalLines[1:] {
		rows = append(rows, strings.Repeat(" ", 5)+textStyle.Render(gl))
	}
	rows = append(rows,
		mutedStyle.Render("Metrics ")+accentStyle.Render(metrics),
		mutedStyle.Render("Model   ")+accentStyle.Render(modelLabel),
	)

	body := strings.Join(rows, "\n")
	box := bannerBorderStyle.BorderTop(false).Width(termWidth - 2).Render(body)

	boxLines := strings.Split(box, "\n")
	boxWidth := 0
	if len(boxLines) > 0 {
		boxWidth = lipgloss.Width(boxLines[0])
	}
	title := boldAccentStyle.Render("resumed") + mutedStyle.Render(fmt.Sprintf(" · slot %s", b.Slot))
	titleBar := renderTitledTopBorder(boxWidth, title)
	return titleBar + "\n" + box
}
