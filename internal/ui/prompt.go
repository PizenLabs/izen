package ui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Virtual Software Cursor (TTY render decoupling §1) ─────────────────────

// applyVirtualCursorMode pins the input cursor to the pure software cursor
// contract: an always-reversed in-band SGR block cell that is memoryless
// across frames. CursorStatic disables the bubbletea cursor state machine —
// Focus() never arms a blink timer, the cell never swaps phases, and the
// prompt emits zero hardware cursor sequences (\x1b[?25h / \x1b[?25l) and
// zero CSI cursor positioning for the cursor itself.
func (m *model) applyVirtualCursorMode() {
	m.ti.Cursor.SetMode(cursor.CursorStatic)
}

// virtualCursorEnabled reports whether the input cursor runs in software
// cursor mode (testability).
func (m *model) virtualCursorEnabled() bool {
	return m.ti.Cursor.Mode() == cursor.CursorStatic
}

type confirmModel struct {
	question string
	result   bool
	done     bool
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) View() string {
	if m.done {
		return ""
	}
	return fmt.Sprintf("\n%s (y/n) ", m.question)
}

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			m.result = true
			m.done = true
			return m, tea.Quit
		case "n", "N", "ctrl+c":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func ConfirmInit(question string) bool {
	p := tea.NewProgram(confirmModel{question: question})
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "izen: prompt error: %v\n", err)
		return false
	}
	return finalModel.(confirmModel).result
}

// ── Multi-line Paste Folding ────────────────────────────────────────────────

// pasteLineThreshold is the minimum line count to trigger folding.
const pasteLineThreshold = 3

// pasteCharThreshold is the minimum character count (with line breaks) to
// trigger folding. A large single-line payload is NOT folded — it must
// contain at least one newline.
const pasteCharThreshold = 150

// pasteBadgeRe matches the plain textual representation of a paste pill:
// "[Paste #<id> - <N> lines]".
var pasteBadgeRe = regexp.MustCompile(`\[Paste #(\d+) - (\d+) lines\]`)

// ShouldFoldPaste reports whether the raw text should be collapsed into a
// paste token. Threshold: >=3 lines or >150 chars with at least one line break.
func ShouldFoldPaste(text string) bool {
	if text == "" {
		return false
	}
	lines := strings.Count(text, "\n") + 1
	if lines >= pasteLineThreshold {
		return true
	}
	if len(text) > pasteCharThreshold && strings.Contains(text, "\n") {
		return true
	}
	return false
}

// CountPasteLines returns the line count of a raw paste text.
func CountPasteLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// FormatPasteBadge returns the ANSI-styled pill badge for the given paste id
// and line count. Style: inverse/muted background 60;50;90 with foreground
// 205;214;244, as specified in the task.
func FormatPasteBadge(id, lines int) string {
	return fmt.Sprintf("\x1b[48;2;60;50;90m\x1b[38;2;205;214;244m [Paste #%d - %d lines] \x1b[0m", id, lines)
}

// PlainPasteBadge returns the plain textual badge without ANSI escapes, used
// as the atomic placeholder inside the prompt buffer.
func PlainPasteBadge(id, lines int) string {
	return fmt.Sprintf("[Paste #%d - %d lines]", id, lines)
}

// PasteToken is the atomic unit stored for an expanded paste.
type PasteToken struct {
	ID        int
	RawText   string
	LineCount int
}

// ensurePasteStore lazily initialises the per-session paste map.
func (m *model) ensurePasteStore() {
	if m.pasteTokens == nil {
		m.pasteTokens = make(map[int]string)
	}
}

// InsertPaste creates a new paste token from raw, increments the session
// counter, stores the raw text, and returns the plain badge to be inserted
// into the prompt buffer. The caller is responsible for inserting the badge
// at the current cursor position.
func (m *model) InsertPaste(raw string) string {
	m.ensurePasteStore()
	m.pasteCounter++
	lines := CountPasteLines(raw)
	m.pasteTokens[m.pasteCounter] = raw
	return PlainPasteBadge(m.pasteCounter, lines)
}

// HandlePasteInput is the high-level paste interceptor used by the key
// handler: if raw meets the folding threshold it is collapsed into a pill
// badge placeholder and inserted atomically at the cursor; otherwise it is
// inserted verbatim. Returns true if folded.
func (m *model) HandlePasteInput(raw string) bool {
	if !ShouldFoldPaste(raw) {
		return false
	}
	m.ensurePasteStore()
	badge := m.InsertPaste(raw)
	val := m.ti.Value()
	pos := m.ti.Position()
	if pos < 0 {
		pos = 0
	}
	if pos > len(val) {
		pos = len(val)
	}
	newVal := val[:pos] + badge + val[pos:]
	m.ti.SetValue(newVal)
	m.ti.SetCursor(pos + len(badge))
	m.syncInputFromTI()
	return true
}

// ExpandPasteTokens expands every plain paste badge inside text back into its
// exact RawText string before emitting to the backend worker. Expansion is
// performed by regex-matching plain badges and looking up the id in the
// session store; badges with no stored raw are left unchanged.
func (m *model) ExpandPasteTokens(text string) string {
	if len(m.pasteTokens) == 0 {
		return text
	}
	return pasteBadgeRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteBadgeRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		var id int
		_, _ = fmt.Sscanf(sub[1], "%d", &id)
		if raw, ok := m.pasteTokens[id]; ok {
			return raw
		}
		return match
	})
}

// ExpandPasteTokensStatic is a package-level helper for tests that operate
// without a full model: it expands badges using the provided store map.
func ExpandPasteTokensStatic(text string, store map[int]string) string {
	if len(store) == 0 {
		return text
	}
	return pasteBadgeRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteBadgeRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		var id int
		_, _ = fmt.Sscanf(sub[1], "%d", &id)
		if raw, ok := store[id]; ok {
			return raw
		}
		return match
	})
}

// tryDeletePasteBadgeAtomic attempts to delete an entire paste badge
// atomically when Backspace or Delete is pressed immediately adjacent to a
// badge. For Backspace (backward delete) the badge must END at the cursor
// position; for Delete (forward delete) the badge must START at the cursor.
// Returns true if a badge was deleted and the value/cursor were updated.
func (m *model) tryDeletePasteBadgeAtomic(isBackspace bool) bool {
	val := m.ti.Value()
	pos := m.ti.Position()
	indices := pasteBadgeRe.FindAllStringIndex(val, -1)
	if len(indices) == 0 {
		return false
	}
	for _, idx := range indices {
		start, end := idx[0], idx[1]
		if isBackspace {
			if end == pos {
				// Backspace immediately after badge → delete whole badge.
				newVal := val[:start] + val[end:]
				m.ti.SetValue(newVal)
				m.ti.SetCursor(start)
				m.syncInputFromTI()
				return true
			}
		} else {
			if start == pos {
				newVal := val[:start] + val[end:]
				m.ti.SetValue(newVal)
				m.ti.SetCursor(start)
				m.syncInputFromTI()
				return true
			}
		}
	}
	return false
}

// handlePasteBackspace is the public atomic deletion entry point for the key
// handler. It handles both Backspace (before badge) and adjacent Delete.
func (m *model) handlePasteBackspace(keyType tea.KeyType) bool {
	switch keyType {
	case tea.KeyBackspace, tea.KeyCtrlH:
		return m.tryDeletePasteBadgeAtomic(true)
	case tea.KeyDelete:
		return m.tryDeletePasteBadgeAtomic(false)
	default:
		return false
	}
}

// PasteBadgeAt checks whether a paste badge is at the given position and
// returns its bounds. Used for atomic cursor navigation.
func PasteBadgeAt(text string, pos int) (start, end int, found bool) {
	indices := pasteBadgeRe.FindAllStringIndex(text, -1)
	for _, idx := range indices {
		if idx[0] <= pos && pos < idx[1] {
			return idx[0], idx[1], true
		}
	}
	return 0, 0, false
}

// RenderPasteBadgesStyled replaces every plain paste badge inside text with
// its ANSI-styled pill badge equivalent for rendering in the prompt view.
func RenderPasteBadgesStyled(text string) string {
	return pasteBadgeRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteBadgeRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		var id, lines int
		_, _ = fmt.Sscanf(sub[1], "%d", &id)
		_, _ = fmt.Sscanf(sub[2], "%d", &lines)
		return FormatPasteBadge(id, lines)
	})
}

// invalidatePromptCache drops the memoized prompt frames (active and
// static) so the next render recomputes. Call on keyboard input, cursor
// moves, focus shifts, and cursor blink ticks — never on scroll or stream
// messages.
func (m *model) invalidatePromptCache() {
	m.cachedPromptKey = ""
	m.cachedPromptStaticKey = ""
}

// renderPromptView returns the textinput view string with paste badges
// rendered as styled pill badges. This is the ACTIVE input view: it carries
// the live cursor for editing.
//
// PROMPT RENDER ISOLATION: the rendered line is memoized and re-generated
// ONLY when prompt state actually changes (input text, cursor position, or
// focus). Viewport scrolling (tea.MouseMsg) and stream token arrivals
// (tokenMsg) reuse the cached frame directly, so the prompt never emits
// cursor hide/show ANSI during viewport-only frame updates.
func (m *model) renderPromptView() string {
	focus := "0"
	if m.ti.Focused() {
		focus = "1"
	}
	key := m.ti.Value() + "\x00" + strconv.Itoa(m.ti.Position()) + "\x00" + focus
	if m.cachedPromptKey == key && m.cachedPromptView != "" {
		return m.cachedPromptView
	}
	m.cachedPromptKey = key
	m.cachedPromptView = RenderPasteBadgesStyled(m.ti.View())
	return m.cachedPromptView
}

// renderPromptViewStatic returns the SCROLL-SUPPRESSED static prompt view:
// identical text and prompt prefix with hardware cursor codes stripped —
// the cursor renders as nothing at all, so scroll frames emit zero cursor
// positioning ANSI to stdout and the prompt stays visually frozen.
//
// It renders from a blurred CLONE of the textinput (textinput.Model is a
// lock-free value struct; Blur on the copy flips focus without touching
// m.ti), so the live input state — value, cursor position, blink timer —
// is never mutated by a scroll frame. Memoized separately from the active
// view; invalidated by the same prompt-state changes.
func (m *model) renderPromptViewStatic() string {
	key := m.ti.Value() + "\x00" + strconv.Itoa(m.ti.Position())
	if m.cachedPromptStaticKey == key && m.cachedPromptStaticView != "" {
		return m.cachedPromptStaticView
	}
	tiCopy := m.ti
	tiCopy.Blur()
	m.cachedPromptStaticKey = key
	m.cachedPromptStaticView = RenderPasteBadgesStyled(tiCopy.View())
	return m.cachedPromptStaticView
}

// renderPromptForFrame dispatches the dual-mode prompt view: while a scroll
// burst is active (lastScrollTime watermark) every frame uses the static,
// cursor-suppressed view; otherwise the live active view with cursor.
// Key input, focus changes, and the expiry of the 150ms scroll-activation
// window restore the active view — instantly on edit, on the first natural
// render pass after the window expires (no timer is armed).
func (m *model) renderPromptForFrame() string {
	if m.isScrollActive() {
		return m.renderPromptViewStatic()
	}
	return m.renderPromptView()
}

// expandPromptForSubmit expands all paste badges in the current prompt value
// and returns the resolved string to be submitted to the backend. It does NOT
// mutate the stored pasteTokens — the collapsed view is preserved (the input
// is cleared on submit anyway).
func (m *model) expandPromptForSubmit() string {
	val := m.ti.Value()
	return m.ExpandPasteTokens(val)
}
