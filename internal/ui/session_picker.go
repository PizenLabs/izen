package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/session"
)

// ── Session picker layout constants ──────────────────────────────────────────

const (
	sessionPickerPreferredWidth  = 78
	sessionPickerPreferredHeight = 18
	sessionPickerMinWidth        = 58
	sessionPickerMinHeight       = 12
	sessionPickerListMinRows     = 3
	sessionPickerChromeLines     = 10
)

const sessionPickerDefaultBudget = 7

// SessionPickerModal is the centered interactive overlay for session lifecycle
// management. It is the TUI analogue of `/session` (bare) — a focused surface
// that surfaces both slots with status, dirty guard and recency, and exposes
// single-key lifecycle operations without leaving the modal.
type SessionPickerModal struct {
	sessions     []session.SlotInfo
	cursor       int
	scrollOffset int
	width        int
	height       int

	// inline rename state
	renaming     bool
	renameInput  textinput.Model
	renameTarget session.SlotID

	// delete confirmation state
	confirmDelete bool
	confirmTarget session.SlotID

	// transient status line
	statusMsg     string
	statusIsError bool
}

// sessionPickerResumeMsg is emitted when Enter is pressed on a row.
type sessionPickerResumeMsg struct{ slot session.SlotID }

// sessionPickerNewMsg is emitted when n is pressed.
type sessionPickerNewMsg struct{}

// sessionPickerRenameMsg is emitted when a rename is confirmed.
type sessionPickerRenameMsg struct {
	slot  session.SlotID
	title string
}

// sessionPickerArchiveMsg is emitted when a is pressed.
type sessionPickerArchiveMsg struct{ slot session.SlotID }

// sessionPickerDeleteMsg is emitted when a delete is confirmed.
type sessionPickerDeleteMsg struct{ slot session.SlotID }

// sessionPickerCompactMsg is emitted when c is pressed.
type sessionPickerCompactMsg struct{ slot session.SlotID }

// sessionPickerCloseMsg is emitted when Esc/q is pressed.
type sessionPickerCloseMsg struct{}

// NewSessionPickerModal creates a modal populated from the current manager list.
// sessions is copied; cursor starts at the active slot.
func NewSessionPickerModal(sessions []session.SlotInfo) *SessionPickerModal {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Placeholder = "new title"
	ti.CharLimit = 64
	ti.Width = 30

	activeIdx := 0
	for i, s := range sessions {
		if s.Active {
			activeIdx = i
			break
		}
	}
	return &SessionPickerModal{
		sessions:     append([]session.SlotInfo(nil), sessions...),
		cursor:       activeIdx,
		renameInput:  ti,
		scrollOffset: 0,
	}
}

// SetSessions refreshes the modal list after an external mutation (new, rename,
// archive, delete, compact). Cursor and scroll are clamped to the new length.
func (sp *SessionPickerModal) SetSessions(sessions []session.SlotInfo) {
	sp.sessions = append([]session.SlotInfo(nil), sessions...)
	sp.statusMsg = ""
	sp.statusIsError = false
	// Keep cursor on the same slot if possible, otherwise clamp.
	if sp.cursor >= len(sp.sessions) {
		sp.cursor = len(sp.sessions) - 1
	}
	if sp.cursor < 0 {
		sp.cursor = 0
	}
	sp.clampScrollOffset()
}

// Selected returns the currently highlighted slot, or nil if the list is empty.
func (sp *SessionPickerModal) Selected() *session.SlotInfo {
	if sp.cursor >= 0 && sp.cursor < len(sp.sessions) {
		return &sp.sessions[sp.cursor]
	}
	return nil
}

// SetSize adapts the modal to the terminal dimensions. It mirrors
// ModelPickerModal.SetSize: hard floors prevent zero-size, scroll is re-clamped.
func (sp *SessionPickerModal) SetSize(w, h int) {
	if w < sessionPickerMinWidth {
		w = sessionPickerMinWidth
	}
	if h < sessionPickerMinHeight {
		h = sessionPickerMinHeight
	}
	sp.width = w
	sp.height = h
	tiWidth := w - 20
	if tiWidth < 10 {
		tiWidth = 10
	}
	sp.renameInput.Width = tiWidth
	sp.clampScrollOffset()
}

func (sp *SessionPickerModal) listRowBudget() int {
	if sp.height <= 0 {
		return sessionPickerDefaultBudget
	}
	budget := sp.height - sessionPickerChromeLines
	if budget < sessionPickerListMinRows {
		budget = sessionPickerListMinRows
	}
	return budget
}

func (sp *SessionPickerModal) clampScrollOffset() {
	if len(sp.sessions) == 0 {
		sp.scrollOffset = 0
		return
	}
	if sp.cursor < 0 {
		sp.cursor = 0
	}
	if sp.cursor >= len(sp.sessions) {
		sp.cursor = len(sp.sessions) - 1
	}
	budget := sp.listRowBudget()
	if sp.cursor < sp.scrollOffset {
		sp.scrollOffset = sp.cursor
	} else if sp.cursor >= sp.scrollOffset+budget {
		sp.scrollOffset = sp.cursor - budget + 1
	}
	maxOffset := len(sp.sessions) - budget
	if maxOffset < 0 {
		maxOffset = 0
	}
	if sp.scrollOffset > maxOffset {
		sp.scrollOffset = maxOffset
	}
	if sp.scrollOffset < 0 {
		sp.scrollOffset = 0
	}
}

// SetStatus sets a transient status line shown inside the modal footer area.
func (sp *SessionPickerModal) SetStatus(msg string, isError bool) {
	sp.statusMsg = msg
	sp.statusIsError = isError
}

// Update handles key events when the modal is active. It implements a focus
// trap: every key is intercepted exclusively by the modal and never falls
// through to the main prompt while active. It emits typed messages for the
// parent model to execute sessionManager operations.
func (sp *SessionPickerModal) Update(msg tea.Msg) (*SessionPickerModal, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		msg := keyMsg
		// ── Inline rename mode takes exclusive priority ──
		if sp.renaming {
			switch msg.Type {
			case tea.KeyEnter:
				title := strings.TrimSpace(sp.renameInput.Value())
				if title == "" {
					sp.renaming = false
					sp.renameInput.Blur()
					sp.renameInput.SetValue("")
					sp.statusMsg = "rename cancelled: empty title"
					sp.statusIsError = true
					return sp, nil
				}
				target := sp.renameTarget
				sp.renaming = false
				sp.renameInput.Blur()
				sp.renameInput.SetValue("")
				return sp, func() tea.Msg {
					return sessionPickerRenameMsg{slot: target, title: title}
				}
			case tea.KeyEscape:
				sp.renaming = false
				sp.renameInput.Blur()
				sp.renameInput.SetValue("")
				return sp, nil
			default:
				var cmd tea.Cmd
				sp.renameInput, cmd = sp.renameInput.Update(msg)
				return sp, cmd
			}
		}

		// ── Delete confirmation mode ──
		if sp.confirmDelete {
			switch msg.String() {
			case "y", "Y":
				target := sp.confirmTarget
				sp.confirmDelete = false
				return sp, func() tea.Msg {
					return sessionPickerDeleteMsg{slot: target}
				}
			case "n", "N", "escape", "esc":
				sp.confirmDelete = false
				return sp, nil
			default:
				if msg.Type == tea.KeyEscape {
					sp.confirmDelete = false
					return sp, nil
				}
				return sp, nil
			}
		}

		// ── Normal navigation & actions ──
		switch msg.String() {
		case "q":
			return sp, func() tea.Msg { return sessionPickerCloseMsg{} }
		case "j":
			if sp.cursor < len(sp.sessions)-1 {
				sp.cursor++
			}
			sp.clampScrollOffset()
			return sp, nil
		case "k":
			if sp.cursor > 0 {
				sp.cursor--
			}
			sp.clampScrollOffset()
			return sp, nil
		case "n":
			return sp, func() tea.Msg { return sessionPickerNewMsg{} }
		case "r":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			sp.renaming = true
			sp.renameTarget = sel.Slot
			sp.renameInput.SetValue(sel.Objective)
			if sel.Objective == "" {
				sp.renameInput.SetValue(sel.SessionID)
			}
			sp.renameInput.Focus()
			sp.renameInput.CursorEnd()
			return sp, textinput.Blink
		case "a":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			slot := sel.Slot
			return sp, func() tea.Msg { return sessionPickerArchiveMsg{slot: slot} }
		case "d":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			sp.confirmDelete = true
			sp.confirmTarget = sel.Slot
			return sp, nil
		case "c":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			slot := sel.Slot
			return sp, func() tea.Msg { return sessionPickerCompactMsg{slot: slot} }
		}

		switch msg.Type {
		case tea.KeyUp:
			if sp.cursor > 0 {
				sp.cursor--
			}
			sp.clampScrollOffset()
			return sp, nil
		case tea.KeyDown:
			if sp.cursor < len(sp.sessions)-1 {
				sp.cursor++
			}
			sp.clampScrollOffset()
			return sp, nil
		case tea.KeyEnter:
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			slot := sel.Slot
			return sp, func() tea.Msg { return sessionPickerResumeMsg{slot: slot} }
		case tea.KeyEscape:
			return sp, func() tea.Msg { return sessionPickerCloseMsg{} }
		}
	}
	return sp, nil
}

// View renders the modal content. Outer border and centering are handled by
// renderSessionPickerModal in workspace.go; this method renders only the inner
// content box (title, table, footer) sized to sp.width/height.
func (sp *SessionPickerModal) View() string {
	if sp.renaming {
		return sp.renderRenameView()
	}
	if sp.confirmDelete {
		return sp.renderConfirmDeleteView()
	}
	return sp.renderListView()
}

func (sp *SessionPickerModal) renderListView() string {
	var b strings.Builder

	// ── Header ──────────────────────────────────────────────────────────
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colorMauve)).
		Render(" Session Manager ")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d session(s) · %d total · j/k navigate", len(sp.sessions), len(sp.sessions))))
	b.WriteString("\n\n")

	// ── Column header ───────────────────────────────────────────────────
	header := subtleStyle.Render(fmt.Sprintf("  %-10s %-6s %-24s %-10s %s",
		"STATUS", "SLOT", "TITLE / GOAL", "DIRTY", "LAST ACTIVE"))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(dimmedStyle.Render("  ──────────────────────────────────────────────────────────────────────"))
	b.WriteString("\n")

	// ── Scrollable list ─────────────────────────────────────────────────
	budget := sp.listRowBudget()
	total := len(sp.sessions)
	if sp.scrollOffset > total {
		sp.scrollOffset = total
	}
	if sp.scrollOffset < 0 {
		sp.scrollOffset = 0
	}
	end := sp.scrollOffset + budget
	if end > total {
		end = total
	}
	window := sp.sessions[sp.scrollOffset:end]

	for idx, info := range window {
		globalIdx := sp.scrollOffset + idx
		isCursor := globalIdx == sp.cursor
		b.WriteString(sp.renderRow(info, isCursor))
		b.WriteString("\n")
	}
	// Pad blank lines for constant height.
	for i := len(window); i < budget; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// ── Status line ─────────────────────────────────────────────────────
	if sp.statusMsg != "" {
		if sp.statusIsError {
			b.WriteString(redStyle.Render("  " + sp.statusMsg))
		} else {
			b.WriteString(greenStyle.Render("  " + sp.statusMsg))
		}
		b.WriteString("\n")
	}

	// ── Footer ──────────────────────────────────────────────────────────
	footer := mutedStyle.Render("↵ resume  n new  r rename  a archive  d delete  c compact  Esc/q close  ↑↓/j/k nav")
	b.WriteString(footer)

	content := b.String()
	return lipgloss.NewStyle().
		Width(sp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(1, 3).
		Render(content)
}

func (sp *SessionPickerModal) renderRow(info session.SlotInfo, isCursor bool) string {
	cursor := "  "
	rowStyle := dimmedStyle
	if isCursor {
		cursor = Icon.Chevron + " "
		rowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	}

	// Status badge
	var badge string
	switch {
	case info.Active:
		badge = greenStyle.Bold(true).Render("ACTIVE")
	case info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived):
		badge = yellowStyle.Bold(true).Render("archived")
	default:
		badge = mutedStyle.Render("dormant")
	}
	// Normalize badge width visually.
	badgeVisible := "dormant"
	if info.Active {
		badgeVisible = "ACTIVE"
	} else if info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived) {
		badgeVisible = "archived"
	}
	_ = badgeVisible

	slotStr := string(info.Slot)

	title := info.Objective
	if title == "" {
		title = info.SessionID
		if len(title) > 20 {
			title = title[:20]
		}
	}
	maxTitleWidth := sp.width - 38
	if maxTitleWidth < 10 {
		maxTitleWidth = 10
	}
	title = truncateWithEllipsis(title, maxTitleWidth)

	ts := "—"
	if !info.UpdatedAt.IsZero() {
		ts = info.UpdatedAt.Format("01-02 15:04")
	}

	// Use plain row for non-cursor to avoid bright styling bleeding; cursor
	// row uses accent style for the whole line except badge which is already colored.
	line := fmt.Sprintf("%s%-10s %-6s %-24s %-10s %s",
		cursor,
		// badge is already styled; we embed it but need to pad manually
		// Use non-styled badge text for width calculation then replace with styled version
		"", slotStr, truncateWithEllipsis(title, 24), "", ts)

	// Reconstruct with styled badge and dirty status.
	// We render badge separately to keep its color.
	badgePlain := "dormant"
	if info.Active {
		badgePlain = "ACTIVE"
	} else if info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived) {
		badgePlain = "archived"
	}
	// Build styled line piecewise to preserve badge color when cursor is active.
	_ = line
	_ = rowStyle

	// For correct lipgloss width while preserving badge color, render badge via its style
	// and the rest of the columns via rowStyle.
	leftBadge := badge
	// Pad badge to 10 visible chars.
	badgeWidth := lipgloss.Width(badgePlain)
	if badgeWidth < 10 {
		leftBadge += strings.Repeat(" ", 10-badgeWidth)
	}
	// Remaining columns
	slotCol := slotStr
	if len(slotCol) < 6 {
		slotCol += strings.Repeat(" ", 6-len(slotCol))
	}
	titleCol := truncateWithEllipsis(title, 24)
	if w := lipgloss.Width(titleCol); w < 24 {
		titleCol += strings.Repeat(" ", 24-w)
	}
	dirtyVisible := "—"
	if info.DirtyCount > 0 {
		dirtyVisible = fmt.Sprintf("⚠ %d", info.DirtyCount)
	}
	dirtyCol := dirtyVisible
	if w := len([]rune(dirtyVisible)); w < 10 {
		dirtyCol += strings.Repeat(" ", 10-w)
	}
	// Apply rowStyle to non-badge columns when cursor.
	if isCursor {
		slotCol = rowStyle.Render(slotCol)
		titleCol = rowStyle.Render(titleCol)
		dirtyCol = rowStyle.Render(dirtyCol)
		ts = rowStyle.Render(ts)
	} else {
		slotCol = dimmedStyle.Render(slotCol)
		titleCol = dimmedStyle.Render(titleCol)
		// dirty already has its own style when DirtyCount>0; keep as is for cursor case
		if info.DirtyCount > 0 {
			// dirtyCol already orange, but dim when not cursor handled above
			dirtyCol = orangeStyle.Render(dirtyVisible)
			if len([]rune(dirtyVisible)) < 10 {
				dirtyCol += strings.Repeat(" ", 10-len([]rune(dirtyVisible)))
			}
		} else {
			dirtyCol = dimmedStyle.Render(dirtyCol)
		}
		ts = mutedStyle.Render(ts)
	}

	return cursor + leftBadge + " " + slotCol + " " + titleCol + " " + dirtyCol + " " + ts
}

func (sp *SessionPickerModal) renderRenameView() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve)).Render(" Rename Session ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render(fmt.Sprintf(" Slot %s — enter new title:", sp.renameTarget)))
	b.WriteString("\n")
	b.WriteString(sp.renameInput.View())
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("↵ confirm  Esc cancel"))
	content := b.String()
	return lipgloss.NewStyle().
		Width(sp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(1, 3).
		Render(content)
}

func (sp *SessionPickerModal) renderConfirmDeleteView() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed)).Render(" Confirm Delete ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(redStyle.Render(fmt.Sprintf(" Delete session slot %s ?", sp.confirmTarget)))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render(" This will purge session-owned state. Project config and audit log are preserved."))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render(" Press "))
	b.WriteString(greenStyle.Bold(true).Render("y"))
	b.WriteString(mutedStyle.Render(" to confirm, "))
	b.WriteString(redStyle.Bold(true).Render("n"))
	b.WriteString(mutedStyle.Render(" or Esc to cancel"))
	content := b.String()
	return lipgloss.NewStyle().
		Width(sp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorRed)).
		Padding(1, 3).
		Render(content)
}

// FormatSessionTable renders a clean text table for non-interactive / headless
// consumption (bare /session without a TTY). It is the non-modal fallback and
// matches the style of runSessionListCmd.
func FormatSessionTable(infos []session.SlotInfo) string {
	var b strings.Builder
	b.WriteString("sessions:\n")
	for _, info := range infos {
		state := "dormant"
		if info.Active {
			state = "ACTIVE"
		}
		if info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived) {
			state = "ARCHIVED"
		}
		label := fmt.Sprintf("  [%s] slot %s  %s", state, info.Slot, info.SessionID)
		if info.Objective != "" {
			label += "  " + truncateWithEllipsis(info.Objective, 40)
		}
		if info.DirtyCount > 0 {
			label += fmt.Sprintf("  (⚠ %d uncommitted file(s))", info.DirtyCount)
		}
		if !info.UpdatedAt.IsZero() {
			label += fmt.Sprintf("  %s", info.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		if info.Error != "" {
			label += "  (" + info.Error + ")"
		}
		b.WriteString(label + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// lastActiveString formats UpdatedAt as a short timestamp.
func lastActiveString(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("01-02 15:04")
}

var _ = lastActiveString
