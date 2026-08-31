package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/session"
)

// ── Session picker layout constants ──────────────────────────────────────────
// Responsive engine: supports Tmux panes down to 45x10 without border wrapping.
// Preferred 88 allows W>=85 full-column layout on 120-wide terminals; min 36
// fits ultra-narrow 45-width panes (modalWidth = max(36, min(parent-2,88))).
const (
	sessionPickerPreferredWidth  = 88
	sessionPickerPreferredHeight = 18
	sessionPickerMinWidth        = 36
	sessionPickerMinHeight       = 8
	sessionPickerListMinRows     = 3
	sessionPickerChromeLines     = 10
)

const sessionPickerCompactChromeLines = 7

const sessionPickerDefaultBudget = 7

// Fixed cell widths for strict truncation (guarantee zero wrapping).
const (
	sessionPickerStatusWidth  = 10
	sessionPickerSlotWidth    = 6
	sessionPickerDirtyWidth   = 10
	sessionPickerLastActWidth = 12
)

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

// SetSize adapts the modal to the terminal dimensions. It enforces safety
// bounds: modalWidth = max(minWidth, min(parentWidth-2, preferredWidth)) is
// computed by the caller (workspace.go); here we apply the floor and re-clamp
// scroll. Ultra-narrow panes (45) and short panes (10) are supported.
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

func (sp *SessionPickerModal) isCompact() bool { return sp.height < 18 }

func (sp *SessionPickerModal) chromeLines() int {
	if sp.isCompact() {
		return sessionPickerCompactChromeLines
	}
	return sessionPickerChromeLines
}

func (sp *SessionPickerModal) listRowBudget() int {
	if sp.height <= 0 {
		return sessionPickerDefaultBudget
	}
	budget := sp.height - sp.chromeLines()
	floor := sessionPickerListMinRows
	if sp.isCompact() {
		floor = 1
	}
	if budget < floor {
		budget = floor
	}
	if budget < 1 {
		budget = 1
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
	compact := sp.isCompact()

	// ── Header: title + count ──────────────────────────────────────────
	if compact {
		// Single collapsed line per spec.
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve)).Render(fmt.Sprintf(" Session Manager (%d) ", len(sp.sessions)))
		b.WriteString(title)
		b.WriteString("\n")
	} else {
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve)).Render(" Session Manager ")
		b.WriteString(title)
		b.WriteString("\n")
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d session(s) · %d total · j/k navigate", len(sp.sessions), len(sp.sessions))))
		b.WriteString("\n")
		b.WriteString("\n")
	}

	// ── Column header ───────────────────────────────────────────────────
	showSlot, showDirty, showLast := sp.visibleColumns()
	headerLine := sp.renderHeader(showSlot, showDirty, showLast)
	b.WriteString(subtleStyle.Render(headerLine))
	b.WriteString("\n")
	// Separator truncated to content width to avoid wrapping.
	sepWidth := sp.contentWidth()
	if sepWidth < 1 {
		sepWidth = 1
	}
	sep := strings.Repeat("─", sepWidth-2)
	// Ensure strictly within width via ANSI-safe Truncate.
	sep = ansi.Truncate(sep, sepWidth-2, "…")
	b.WriteString(dimmedStyle.Render("  " + sep))
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
	// Pad blank lines for constant height (deterministic across resizes).
	for i := len(window); i < budget; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// ── Status line ─────────────────────────────────────────────────────
	if sp.statusMsg != "" {
		// Truncate status to avoid wrapping (ANSI-safe).
		maxStatus := sepWidth
		statusText := "  " + sp.statusMsg
		statusText = ansi.Truncate(statusText, maxStatus, "…")
		if sp.statusIsError {
			b.WriteString(redStyle.Render(statusText))
		} else {
			b.WriteString(greenStyle.Render(statusText))
		}
		b.WriteString("\n")
	}

	// ── Footer ──────────────────────────────────────────────────────────
	if compact {
		footer := mutedStyle.Render("[Enter] switch · [n] new · [d] del · [Esc] close")
		footer = ansi.Truncate(footer, sepWidth, "…")
		b.WriteString(footer)
	} else {
		footer1 := mutedStyle.Render("↵ resume  n new  r rename  a archive")
		footer2 := mutedStyle.Render("d delete  c compact  Esc/q close  ↑↓/j/k nav")
		footer1 = ansi.Truncate(footer1, sepWidth, "…")
		footer2 = ansi.Truncate(footer2, sepWidth, "…")
		b.WriteString(footer1)
		b.WriteString("\n")
		b.WriteString(footer2)
	}

	content := b.String()
	return lipgloss.NewStyle().
		Width(sp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(1, 3).
		Render(content)
}

// visibleColumns returns dynamic visibility per W thresholds.
func (sp *SessionPickerModal) visibleColumns() (showSlot, showDirty, showLast bool) {
	w := sp.width
	showLast = w >= 85
	showDirty = w >= 65
	showSlot = w >= 50
	return
}

func (sp *SessionPickerModal) contentWidth() int {
	// Inner usable width after outer Width(width-4) + Padding(1,3) (6) + border(2).
	// Conservative estimate: width - 12 ensures zero wrapping even in ultra-narrow.
	w := sp.width - 12
	if w < 10 {
		w = 10
	}
	return w
}

func (sp *SessionPickerModal) renderHeader(showSlot, showDirty, showLast bool) string {
	// Build header cells with strict Truncate per cell.
	var cols []string
	cols = append(cols, cellWithWidth("STATUS", sessionPickerStatusWidth))
	if showSlot {
		cols = append(cols, cellWithWidth("SLOT", sessionPickerSlotWidth))
	}
	// TITLE is flexible; width computed after fixed cols.
	fixed := sessionPickerStatusWidth
	if showSlot {
		fixed += 1 + sessionPickerSlotWidth
	}
	if showDirty {
		fixed += 1 + sessionPickerDirtyWidth
	}
	if showLast {
		fixed += 1 + sessionPickerLastActWidth
	}
	available := sp.contentWidth() - fixed - 2 // 2 for cursor prefix
	if available < 8 {
		available = 8
	}
	cols = append(cols, cellWithWidth("TITLE / GOAL", available))
	if showDirty {
		cols = append(cols, cellWithWidth("DIRTY", sessionPickerDirtyWidth))
	}
	if showLast {
		cols = append(cols, cellWithWidth("LAST ACTIVE", sessionPickerLastActWidth))
	}
	return "  " + strings.Join(cols, " ")
}

func cellWithWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	trunc := ansi.Truncate(s, w, "…")
	// Pad to exact width for deterministic row width (ansi.Truncate does not pad).
	if cur := lipgloss.Width(trunc); cur < w {
		trunc += strings.Repeat(" ", w-cur)
	}
	return trunc
}

func (sp *SessionPickerModal) renderRow(info session.SlotInfo, isCursor bool) string {
	cursor := "  "
	rowStyle := dimmedStyle
	if isCursor {
		cursor = Icon.Chevron + " "
		rowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	}

	showSlot, showDirty, showLast := sp.visibleColumns()

	// Status badge (always visible, strict width).
	var badgePlain string
	switch {
	case info.Active:
		badgePlain = "ACTIVE"
	case info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived):
		badgePlain = "archived"
	default:
		badgePlain = "dormant"
	}
	leftBadge := cellWithWidth(badgePlain, sessionPickerStatusWidth)
	// Re-apply badge color after width calc (cellWithWidth is plain, now style).
	switch {
	case info.Active:
		leftBadge = greenStyle.Bold(true).Render(leftBadge)
	case info.Lifecycle == "archived" || info.Lifecycle == string(session.LifecycleArchived):
		leftBadge = yellowStyle.Bold(true).Render(leftBadge)
	default:
		leftBadge = mutedStyle.Render(leftBadge)
	}

	// Title / goal (flexible, fills remaining).
	title := info.Objective
	if title == "" {
		title = info.SessionID
	}
	// Compute available width for title after fixed columns.
	fixed := sessionPickerStatusWidth
	if showSlot {
		fixed += 1 + sessionPickerSlotWidth
	}
	if showDirty {
		fixed += 1 + sessionPickerDirtyWidth
	}
	if showLast {
		fixed += 1 + sessionPickerLastActWidth
	}
	available := sp.contentWidth() - fixed - 2 // 2 for cursor prefix
	if available < 8 {
		available = 8
	}
	titleCell := cellWithWidth(title, available)

	ts := "—"
	if !info.UpdatedAt.IsZero() {
		ts = info.UpdatedAt.Format("01-02 15:04")
	}

	// Build row piecewise to keep badge color and guarantee no wrapping.
	var parts []string
	parts = append(parts, leftBadge)
	if showSlot {
		slotStr := string(info.Slot)
		slotCell := cellWithWidth(slotStr, sessionPickerSlotWidth)
		if isCursor {
			slotCell = rowStyle.Render(slotCell)
		} else {
			slotCell = dimmedStyle.Render(slotCell)
		}
		parts = append(parts, slotCell)
	}
	// Title cell styling.
	if isCursor {
		titleCell = rowStyle.Render(titleCell)
	} else {
		titleCell = dimmedStyle.Render(titleCell)
	}
	parts = append(parts, titleCell)
	if showDirty {
		dirtyVisible := "—"
		if info.DirtyCount > 0 {
			dirtyVisible = fmt.Sprintf("⚠ %d", info.DirtyCount)
		}
		dirtyCell := cellWithWidth(dirtyVisible, sessionPickerDirtyWidth)
		switch {
		case isCursor:
			dirtyCell = rowStyle.Render(dirtyCell)
		case info.DirtyCount > 0:
			dirtyCell = orangeStyle.Render(dirtyCell)
		default:
			dirtyCell = dimmedStyle.Render(dirtyCell)
		}
		parts = append(parts, dirtyCell)
	}
	if showLast {
		tsCell := cellWithWidth(ts, sessionPickerLastActWidth)
		if isCursor {
			tsCell = rowStyle.Render(tsCell)
		} else {
			tsCell = mutedStyle.Render(tsCell)
		}
		parts = append(parts, tsCell)
	}

	row := cursor + strings.Join(parts, " ")
	// Final safety: truncate entire row to content width to prevent ANSI overflow.
	row = ansi.Truncate(row, sp.contentWidth()+2, "…")
	return row
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
	cw := sp.contentWidth()
	long := " This will purge session-owned state. Project config and audit log are preserved."
	long = ansi.Truncate(long, cw, "…")
	b.WriteString(mutedStyle.Render(long))
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
