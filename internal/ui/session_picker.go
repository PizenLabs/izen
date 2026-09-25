package ui

import (
	"fmt"
	"strings"
	"sync/atomic"
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
// fits ultra-narrow panes while the host reserves room for both modal borders.
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

var sessionPickerModalSeq atomic.Uint64

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

	// Inline input state. The picker has one textinput because rename and new
	// are mutually exclusive, but keeping the mode explicit makes the focus
	// trap unambiguous: while either mode is active, navigation is disabled and
	// every key (including j/k and arrows) is delegated to the textinput.
	pickerID        uint64
	inputSeq        uint64
	renaming        bool
	creating        bool
	renameInput     textinput.Model
	renameTarget    session.SlotID
	pendingRenameID uint64
	pendingNewID    uint64

	// delete confirmation state
	confirmDelete bool
	confirmTarget session.SlotID

	// transient status line
	statusMsg     string
	statusCompact string
	statusIsError bool
}

// sessionPickerResumeMsg is emitted when Enter is pressed on a row.
type sessionPickerResumeMsg struct {
	slot     session.SlotID
	pickerID uint64
}

// sessionPickerNewMsg is emitted in two phases. The initial n key starts the
// inline title editor and carries begin=true; Enter emits commit=true with the
// title. Keeping the two phases as messages preserves the modal's command
// boundary without allowing the initial n key to create a session prematurely.
type sessionPickerNewMsg struct {
	title    string
	begin    bool
	commit   bool
	inputID  uint64
	pickerID uint64
}

// sessionPickerRenameMsg is emitted when a rename is confirmed.
type sessionPickerRenameMsg struct {
	slot     session.SlotID
	title    string
	inputID  uint64
	pickerID uint64
}

// sessionPickerArchiveMsg is emitted when a is pressed.
type sessionPickerArchiveMsg struct {
	slot     session.SlotID
	pickerID uint64
}

// sessionPickerDeleteMsg is emitted when a delete is confirmed.
type sessionPickerDeleteMsg struct {
	slot     session.SlotID
	pickerID uint64
}

// sessionPickerCompactMsg is emitted when c is pressed.
type sessionPickerCompactMsg struct {
	slot     session.SlotID
	pickerID uint64
}

// sessionPickerCloseMsg is emitted when Esc/q is pressed.
type sessionPickerCloseMsg struct{ pickerID uint64 }

// sessionPickerEditorMsg wraps asynchronous messages produced by the bubbles
// textinput/cursor components. Keeping the wrapper private prevents unrelated
// background messages from being mistaken for editor events by the parent
// focus trap.
type sessionPickerEditorMsg struct{ msg tea.Msg }

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
	sp := &SessionPickerModal{
		pickerID:     sessionPickerModalSeq.Add(1),
		sessions:     append([]session.SlotInfo(nil), sessions...),
		cursor:       activeIdx,
		renameInput:  ti,
		scrollOffset: 0,
	}
	// A modal can be constructed and rendered directly in tests/headless code
	// before the first WindowSizeMsg. Seed a usable size so its layout never
	// starts with zero-width columns.
	sp.SetSize(sessionPickerPreferredWidth, sessionPickerPreferredHeight)
	return sp
}

// SetSessions refreshes the modal list after an external mutation (new, rename,
// archive, delete, compact). Cursor and scroll are clamped to the new length.
// The status is intentionally cleared: callers that need to retain a freshly
// computed result can use RefreshSessions instead.
func (sp *SessionPickerModal) SetSessions(sessions []session.SlotInfo) {
	sp.replaceSessions(sessions)
	sp.statusMsg = ""
	sp.statusCompact = ""
	sp.statusIsError = false
}

// RefreshSessions updates the rows without erasing a transient status banner.
// Mutation handlers use this after calculating a result so feedback survives
// the refresh that follows the write.
func (sp *SessionPickerModal) RefreshSessions(sessions []session.SlotInfo) {
	sp.replaceSessions(sessions)
}

func (sp *SessionPickerModal) replaceSessions(sessions []session.SlotInfo) {
	sp.sessions = append([]session.SlotInfo(nil), sessions...)
	// Keep cursor on the same slot if possible, otherwise clamp.
	if sp.cursor >= len(sp.sessions) {
		sp.cursor = len(sp.sessions) - 1
	}
	if sp.cursor < 0 {
		sp.cursor = 0
	}
	sp.clampScrollOffset()
}

func (sp *SessionPickerModal) beginInlineInput() uint64 {
	sp.inputSeq++
	if sp.inputSeq == 0 {
		sp.inputSeq = 1
	}
	// Starting a new editor invalidates any command that was emitted by the
	// previous editor, even if that command has not reached the parent yet.
	sp.pendingRenameID = 0
	sp.pendingNewID = 0
	return sp.inputSeq
}

func (sp *SessionPickerModal) ownsMessage(pickerID uint64) bool {
	// Zero is retained as a compatibility path for hand-built legacy messages.
	return pickerID == 0 || pickerID == sp.pickerID
}

// Selected returns the currently highlighted slot, or nil if the list is empty.
func (sp *SessionPickerModal) Selected() *session.SlotInfo {
	if sp.cursor >= 0 && sp.cursor < len(sp.sessions) {
		return &sp.sessions[sp.cursor]
	}
	return nil
}

// SetSize adapts the modal to the dialog dimensions. It enforces safety
// bounds and recalculates the flexible title column and the textinput width on
// every call. Ultra-narrow panes (45) and short panes (10) are supported.
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
	// bubbles/textinput caches its horizontal window; nudging the cursor
	// through SetCursor recomputes that window after a resize.
	sp.renameInput.SetCursor(sp.renameInput.Position())
	sp.clampScrollOffset()
}

// SetViewportSize adapts the modal from the parent terminal dimensions. Keeping
// this conversion next to the widget means a direct tea.WindowSizeMsg delivered
// to the modal and the parent model's resize path use identical constraints.
func (sp *SessionPickerModal) SetViewportSize(terminalWidth, terminalHeight int) {
	w, h := sessionPickerDialogSizeFor(terminalWidth, terminalHeight)
	sp.SetSize(w, h)
}

// sessionPickerDialogSizeFor computes the dialog bounds for a terminal or
// pane. The two-cell edge margin keeps the widget border off the viewport
// edge; the minimums still provide a usable dialog for small headless renders.
func sessionPickerDialogSizeFor(terminalWidth, terminalHeight int) (int, int) {
	w := sessionPickerPreferredWidth
	h := sessionPickerPreferredHeight
	const edgeMargin = 2
	if terminalWidth > 0 {
		if maxW := terminalWidth - edgeMargin; maxW < w {
			w = maxW
		}
	}
	if terminalHeight > 0 {
		if maxH := terminalHeight - edgeMargin; maxH < h {
			h = maxH
		}
	}
	if w < sessionPickerMinWidth {
		w = sessionPickerMinWidth
	}
	if h < sessionPickerMinHeight {
		h = sessionPickerMinHeight
	}
	return w, h
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
	// The DETAILS preview panel is a static, cursor-derived region that shares
	// the modal height with the row table. Reserving its lines here keeps the
	// list scroll budget deterministic without any timer/ticker loop.
	budget := sp.height - sp.chromeLines() - sp.detailsHeight()
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

// detailsHeight returns the number of lines the DETAILS preview panel occupies
// for the current modal height. It degrades gracefully in short panes so the
// list, status, and footer always remain reachable.
func (sp *SessionPickerModal) detailsHeight() int {
	if len(sp.sessions) == 0 {
		return 0
	}
	switch {
	case sp.height >= 16:
		return 6
	case sp.height >= 12:
		return 4
	case sp.height >= 9:
		return 2
	default:
		return 0
	}
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
	sp.statusCompact = ""
	sp.statusIsError = isError
}

// SetCompactionStatus stores both the full savings report and a compact form
// for short panes, where the full before/after line cannot fit without
// truncating the most useful result.
func (sp *SessionPickerModal) SetCompactionStatus(target session.SlotID, before, after int) {
	sp.statusMsg = compactionStatus(target, before, after)
	shortBefore := shortTokenCount(before)
	shortAfter := shortTokenCount(after)
	change := "="
	percent := 0
	if before == 0 && after > 0 {
		change = "new"
	} else if before > 0 {
		if after < before {
			change = "-"
			percent = (before - after) * 100 / before
		} else if after > before {
			change = "+"
			percent = (after - before) * 100 / before
		}
	}
	sp.statusCompact = fmt.Sprintf("[%s] %s->%s %s%d%%", target, shortBefore, shortAfter, change, percent)
	sp.statusIsError = false
}

func shortTokenCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func wrapSessionPickerEditorCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		return sessionPickerEditorMsg{msg: cmd()}
	}
}

func (sp *SessionPickerModal) updateTextInput(msg tea.Msg) (*SessionPickerModal, tea.Cmd) {
	var cmd tea.Cmd
	sp.renameInput, cmd = sp.renameInput.Update(msg)
	return sp, wrapSessionPickerEditorCmd(cmd)
}

// Update handles key events when the modal is active. It implements a focus
// trap: every key is intercepted exclusively by the modal and never falls
// through to the main prompt while active. It emits typed messages for the
// parent model to execute sessionManager operations.
func (sp *SessionPickerModal) Update(msg tea.Msg) (*SessionPickerModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sp.SetViewportSize(msg.Width, msg.Height)
		return sp, nil
	case tea.KeyMsg:
		// Inline rename and create modes own the keyboard completely. In
		// particular, j/k and the arrow keys are passed to textinput and can
		// never reach the row-navigation switch below. Treat the focus bit as
		// part of the trap too, so a focus transition can never leak one frame
		// of table navigation to the parent.
		if sp.inlineInputActive() {
			switch msg.Type {
			case tea.KeyEnter:
				title := strings.TrimSpace(sp.renameInput.Value())
				wasRenaming := sp.renaming
				target := sp.renameTarget
				inputID := sp.inputSeq
				if inputID == 0 {
					sp.inputSeq = 1
					inputID = 1
				}
				sp.clearInlineInput()
				if wasRenaming {
					if title == "" {
						sp.statusMsg = "rename cancelled: empty title"
						sp.statusIsError = true
						return sp, nil
					}
					sp.pendingRenameID = inputID
					return sp, func() tea.Msg {
						return sessionPickerRenameMsg{slot: target, title: title, inputID: inputID, pickerID: sp.pickerID}
					}
				}
				// Creation intentionally permits an empty title: the
				// session manager can then derive its title from the first
				// prompt later in the engine.
				sp.pendingNewID = inputID
				return sp, func() tea.Msg {
					return sessionPickerNewMsg{title: title, commit: true, inputID: inputID, pickerID: sp.pickerID}
				}
			case tea.KeyEscape:
				sp.clearInlineInput()
				return sp, nil
			default:
				return sp.updateTextInput(msg)
			}
		}

		// ── Delete confirmation mode ──
		if sp.confirmDelete {
			switch {
			case msg.Type == tea.KeyEscape || msg.String() == "n" || msg.String() == "N":
				sp.confirmDelete = false
				return sp, nil
			case msg.String() == "y" || msg.String() == "Y":
				target := sp.confirmTarget
				sp.confirmDelete = false
				return sp, func() tea.Msg {
					return sessionPickerDeleteMsg{slot: target, pickerID: sp.pickerID}
				}
			default:
				return sp, nil
			}
		}

		// ── Normal navigation & actions ──
		switch msg.String() {
		case "q":
			return sp, func() tea.Msg { return sessionPickerCloseMsg{pickerID: sp.pickerID} }
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
			// Start an inline title editor. The begin message is harmless to
			// the parent (it must not create a session until Enter is pressed).
			sp.renaming = false
			sp.creating = true
			sp.renameTarget = ""
			inputID := sp.beginInlineInput()
			sp.renameInput.CharLimit = 64
			sp.renameInput.SetValue("")
			sp.renameInput.Placeholder = "new title"
			sp.renameInput.Focus()
			sp.renameInput.CursorStart()
			return sp, func() tea.Msg { return sessionPickerNewMsg{begin: true, inputID: inputID, pickerID: sp.pickerID} }
		case "r":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			sp.renaming = true
			sp.creating = false
			sp.renameTarget = sel.Slot
			sp.beginInlineInput()
			initial := sel.Title
			if initial == "" {
				initial = sel.Objective
			}
			if initial == "" {
				initial = sel.SessionID
			}
			sp.renameInput.Placeholder = "new title"
			// Leave room to edit a full existing title; the manager remains
			// the source of truth for validation and persistence.
			sp.renameInput.CharLimit = max(64, len([]rune(initial))+64)
			sp.renameInput.SetValue(initial)
			sp.renameInput.Focus()
			sp.renameInput.CursorEnd()
			return sp, wrapSessionPickerEditorCmd(textinput.Blink)
		case "a":
			sel := sp.Selected()
			if sel == nil {
				return sp, nil
			}
			slot := sel.Slot
			return sp, func() tea.Msg { return sessionPickerArchiveMsg{slot: slot, pickerID: sp.pickerID} }
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
			return sp, func() tea.Msg { return sessionPickerCompactMsg{slot: slot, pickerID: sp.pickerID} }
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
			return sp, func() tea.Msg { return sessionPickerResumeMsg{slot: slot, pickerID: sp.pickerID} }
		case tea.KeyEscape:
			return sp, func() tea.Msg { return sessionPickerCloseMsg{pickerID: sp.pickerID} }
		}
	default:
		// Only messages explicitly emitted by our wrapped textinput command are
		// editor-owned. Unrelated application/background messages must continue
		// to the parent update loop.
		if editorMsg, ok := msg.(sessionPickerEditorMsg); ok && sp.inlineInputActive() {
			return sp.updateTextInput(editorMsg.msg)
		}
	}
	return sp, nil
}

func (sp *SessionPickerModal) inlineInputActive() bool {
	return sp.renaming || sp.creating || sp.renameInput.Focused()
}

func (sp *SessionPickerModal) clearInlineInput() {
	sp.renaming = false
	sp.creating = false
	sp.renameTarget = ""
	sp.pendingRenameID = 0
	sp.pendingNewID = 0
	sp.renameInput.Blur()
	sp.renameInput.SetValue("")
}

// View renders the bordered modal content (title, table, footer) sized to
// sp.width/sp.height. The host only centers this already-sized surface in the
// workspace, so a second wrapper cannot clip the border during a resize.
func (sp *SessionPickerModal) View() string {
	if sp.inlineInputActive() {
		return sp.renderInlineInputView()
	}
	if sp.confirmDelete {
		return sp.renderConfirmDeleteView()
	}
	return sp.renderListView()
}

func (sp *SessionPickerModal) renderListView() string {
	var b strings.Builder
	compact := sp.isCompact()
	ultraCompact := sp.height <= 8
	sepWidth := sp.contentWidth()

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
	// At the smallest supported height, retain the title, one row, status,
	// and footer rather than allowing chrome to push the border off-screen.
	if !ultraCompact {
		layout := sp.columnLayout()
		headerLine := sp.renderHeader(layout.showSlot, layout.showDirty, layout.showLast)
		b.WriteString(subtleStyle.Render(headerLine))
		b.WriteString("\n")
		// Separator truncated to content width to avoid wrapping.
		sepBodyWidth := sepWidth - 2
		if sepBodyWidth < 1 {
			sepBodyWidth = 1
		}
		sep := strings.Repeat("─", sepBodyWidth)
		// Ensure strictly within width via ANSI-safe truncation.
		sep = ansi.Truncate(sep, sepBodyWidth, "...")
		b.WriteString(dimmedStyle.Render("  " + sep))
		b.WriteString("\n")
	}

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
	// ── DETAILS preview panel ──────────────────────────────────────────
	// Static, cursor-derived: it re-renders from Selected() on every View()
	// call, so j/k and ↑/↓ update it instantly with no timer/ticker loop.
	if sp.detailsHeight() > 0 {
		b.WriteString(sp.renderDetailsPanel())
		b.WriteString("\n")
	} else if !ultraCompact && (!compact || sp.statusMsg == "") {
		// In a short pane the feedback line replaces the discretionary spacer;
		// this keeps the status and footer inside the available height.
		b.WriteString("\n")
	}

	// ── Status line ─────────────────────────────────────────────────────
	if sp.statusMsg != "" {
		// Truncate status to avoid wrapping (ANSI-safe). Prefer the compact
		// savings form in short panes so the arrow and percentage survive.
		maxStatus := sepWidth
		statusText := "  " + sp.statusMsg
		if sp.statusCompact != "" && lipgloss.Width(statusText) > maxStatus {
			statusText = "  " + sp.statusCompact
		}
		statusText = ansi.Truncate(statusText, maxStatus, "...")
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
		footer = ansi.Truncate(footer, sepWidth, "...")
		b.WriteString(footer)
	} else {
		footer1 := mutedStyle.Render("↵ resume  n new  r rename  a archive")
		footer2 := mutedStyle.Render("d delete  c compact  Esc/q close  ↑↓/j/k nav")
		footer1 = ansi.Truncate(footer1, sepWidth, "...")
		footer2 = ansi.Truncate(footer2, sepWidth, "...")
		b.WriteString(footer1)
		b.WriteString("\n")
		b.WriteString(footer2)
	}

	content := b.String()
	innerWidth := sp.width - 4
	if innerWidth < 1 {
		innerWidth = 1
	}
	return lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(1, 3).
		Render(content)
}

// renderDetailsPanel renders the static DETAILS box for the highlighted row.
// It is derived exclusively from Selected(), so it updates instantly on
// ↑/↓/j/k navigation without any timer, ticker, or background query. It
// returns exactly detailsHeight() lines (plain text + ANSI styling), each
// truncated to the modal content width so the bordered surface never wraps.
func (sp *SessionPickerModal) renderDetailsPanel() string {
	sel := sp.Selected()
	h := sp.detailsHeight()
	if sel == nil || h <= 0 {
		return ""
	}
	cw := sp.contentWidth()
	if cw < 8 {
		cw = 8
	}

	title := strings.TrimSpace(sel.Title)
	if title == "" {
		title = strings.TrimSpace(sel.Objective)
	}
	if title == "" {
		title = strings.TrimSpace(sel.SessionID)
	}
	if title == "" {
		title = "(untitled session)"
	}

	window := sel.ContextWindow
	if window <= 0 {
		window = 128000
	}
	used := sel.Tokens
	pct := 0
	if window > 0 && used > 0 {
		pct = used * 100 / window
		if pct > 100 {
			pct = 100
		}
	}
	metrics := fmt.Sprintf("%s / %s tokens (%d%%) | %d turns",
		formatTokenCount(used), formatTokenCount(window), pct, sel.Turns)

	modelName := strings.TrimSpace(sel.Model)
	if modelName == "" {
		modelName = "(unassigned)"
	}
	last := strings.TrimSpace(sel.LastPrompt)
	if last == "" {
		last = "—"
	}

	styleLine := func(label, value string) string {
		prefix := "  " + mutedStyle.Render(label)
		pad := 8 - lipgloss.Width(label)
		if pad > 0 {
			prefix += strings.Repeat(" ", pad)
		}
		return ansi.Truncate(prefix+value, cw, "…")
	}

	var lines []string
	switch {
	case h >= 6:
		// Standard: heading + wrapped title (2) + metrics + model + last.
		heading := " " + boldMauveStyle.Render("DETAILS")
		titleLines := wrapPlainLine(title, max(8, cw-9))
		titleTruncated := len(titleLines) > 2
		for len(titleLines) < 2 {
			titleLines = append(titleLines, "")
		}
		if titleTruncated {
			titleLines[1] = ansi.Truncate(titleLines[1], max(8, cw-9), "…")
		}
		titleLines = titleLines[:2]
		lines = []string{
			ansi.Truncate(heading, cw, ""),
			styleLine("Title", textStyle.Render(titleLines[0])),
			styleLine("", textStyle.Render(titleLines[1])),
			styleLine("Tokens", accentStyle.Render(metrics)),
			styleLine("Model", accentStyle.Render(modelName)),
			styleLine("Last", mutedStyle.Render(last)),
		}
	case h >= 4:
		lines = []string{
			ansi.Truncate(" "+boldMauveStyle.Render("DETAILS"), cw, ""),
			styleLine("Title", mutedStyle.Render(truncateWithEllipsis(title, max(8, cw-9)))),
			styleLine("Tokens", accentStyle.Render(metrics)),
			styleLine("Model", accentStyle.Render(modelName)),
		}
	default:
		lines = []string{
			styleLine("Tokens", accentStyle.Render(metrics)),
			styleLine("Last", mutedStyle.Render(last)),
		}
	}

	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// sessionPickerColumnLayout describes the widths used by both the header and
// rows. STATUS, SLOT, DIRTY, and LAST ACTIVE retain their fixed widths; only
// TITLE / GOAL yields space as the terminal changes width.
type sessionPickerColumnLayout struct {
	showSlot  bool
	showDirty bool
	showLast  bool
	title     int
}

func (sp *SessionPickerModal) columnLayout() sessionPickerColumnLayout {
	showLast := sp.width >= 85
	showDirty := sp.width >= 65
	showSlot := sp.width >= 50
	return sp.columnLayoutFor(showSlot, showDirty, showLast)
}

func (sp *SessionPickerModal) columnLayoutFor(showSlot, showDirty, showLast bool) sessionPickerColumnLayout {
	// The two-cell prefix is present on every row, and each visible column
	// contributes its fixed width plus one separator from the preceding one.
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
	// The title is preceded by one separator even when all optional columns
	// are hidden, hence the three-cell reservation (two-cell cursor prefix
	// plus that separator).
	title := sp.contentWidth() - fixed - 3
	if title < 8 {
		title = 8
	}
	return sessionPickerColumnLayout{
		showSlot:  showSlot,
		showDirty: showDirty,
		showLast:  showLast,
		title:     title,
	}
}

// visibleColumns returns dynamic visibility per W thresholds.
func (sp *SessionPickerModal) visibleColumns() (showSlot, showDirty, showLast bool) {
	layout := sp.columnLayout()
	return layout.showSlot, layout.showDirty, layout.showLast
}

func (sp *SessionPickerModal) contentWidth() int {
	// Inner usable width after the outer Width(width-4), padding, and border.
	// The conservative margin leaves room for the row cursor prefix while
	// guaranteeing that a long title cannot wrap into the modal border.
	if sp.width <= 0 {
		sp.width = sessionPickerPreferredWidth
	}
	w := sp.width - 12
	if w < 10 {
		w = 10
	}
	return w
}

func (sp *SessionPickerModal) renderHeader(showSlot, showDirty, showLast bool) string {
	layout := sp.columnLayoutFor(showSlot, showDirty, showLast)
	cols := []string{cellWithWidth("STATUS", sessionPickerStatusWidth)}
	if layout.showSlot {
		cols = append(cols, cellWithWidth("SLOT", sessionPickerSlotWidth))
	}
	// TITLE / GOAL is the only elastic column. Truncate it before the fixed
	// metadata columns so a long title can never push the border off-screen.
	cols = append(cols, cellWithWidth("TITLE / GOAL", layout.title))
	if layout.showDirty {
		cols = append(cols, cellWithWidth("DIRTY", sessionPickerDirtyWidth))
	}
	if layout.showLast {
		cols = append(cols, cellWithWidth("LAST ACTIVE", sessionPickerLastActWidth))
	}
	return "  " + strings.Join(cols, " ")
}

func cellWithWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	// Lip Gloss v1.1 delegates its cell-safe truncation to ansi.Truncate;
	// use a literal three-dot suffix so the elastic title has the same marker
	// regardless of terminal font/renderer.
	trunc := ansi.Truncate(s, w, "...")
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

	layout := sp.columnLayout()

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

	// Title / goal is the elastic column. Use the same layout calculation as
	// the header so resizing cannot desynchronize their widths.
	title := info.Title
	if title == "" {
		title = info.Objective
	}
	if title == "" {
		title = info.SessionID
	}
	titleCell := cellWithWidth(title, layout.title)

	ts := "—"
	if !info.UpdatedAt.IsZero() {
		ts = info.UpdatedAt.Format("01-02 15:04")
	}

	// Build row piecewise to keep badge color and guarantee no wrapping.
	var parts []string
	parts = append(parts, leftBadge)
	if layout.showSlot {
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
	if layout.showDirty {
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
	if layout.showLast {
		tsCell := cellWithWidth(ts, sessionPickerLastActWidth)
		if isCursor {
			tsCell = rowStyle.Render(tsCell)
		} else {
			tsCell = mutedStyle.Render(tsCell)
		}
		parts = append(parts, tsCell)
	}

	row := cursor + strings.Join(parts, " ")
	// Final safety: truncate the complete ANSI row to the available content
	// width so even malformed metadata cannot wrap the modal border.
	row = ansi.Truncate(row, sp.contentWidth(), "...")
	return row
}

func (sp *SessionPickerModal) renderInlineInputView() string {
	creating := sp.creating
	heading := " Rename Session "
	prompt := fmt.Sprintf("Slot %s — enter new title:", sp.renameTarget)
	if creating {
		heading = " New Session "
		prompt = "Enter a title for the new session:"
	}

	var b strings.Builder
	ultraCompact := sp.height <= 8
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve)).Render(heading)
	b.WriteString(title)
	if ultraCompact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}
	prompt = ansi.Truncate(prompt, sp.contentWidth(), "...")
	b.WriteString(mutedStyle.Render(prompt))
	b.WriteString("\n")
	b.WriteString(sp.renameInput.View())
	if ultraCompact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}
	b.WriteString(mutedStyle.Render("↵ confirm  Esc cancel"))
	content := b.String()
	innerWidth := sp.width - 4
	if innerWidth < 1 {
		innerWidth = 1
	}
	return lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(1, 3).
		Render(content)
}

func (sp *SessionPickerModal) renderConfirmDeleteView() string {
	var b strings.Builder
	ultraCompact := sp.height <= 8
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed)).Render(" Confirm Delete ")
	b.WriteString(title)
	if ultraCompact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}
	b.WriteString(redStyle.Render(fmt.Sprintf(" Delete session slot %s ?", sp.confirmTarget)))
	b.WriteString("\n")
	cw := sp.contentWidth()
	long := " This will purge session-owned state. Project config and audit log are preserved."
	long = ansi.Truncate(long, cw, "...")
	b.WriteString(mutedStyle.Render(long))
	if ultraCompact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}
	help := "Press y to confirm, n/Esc to cancel"
	help = ansi.Truncate(help, cw, "...")
	b.WriteString(mutedStyle.Render(help))
	content := b.String()
	innerWidth := sp.width - 4
	if innerWidth < 1 {
		innerWidth = 1
	}
	return lipgloss.NewStyle().
		Width(innerWidth).
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
		if info.Title != "" {
			label += "  " + truncateWithEllipsis(info.Title, 40)
		} else if info.Objective != "" {
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
