package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AskComponent is the interactive clarification (Ask) card rendered inline in
// the active chat stream while the engine wall-timer is paused.
//
//	Keybindings: j/k or Up/Down navigate, Space toggles (multi-select only),
//	Enter submits, Esc cancels. Recommended options carry a (Recommended)
//	badge. The component is keyboard-driven, zero padding, high density, and
//	never panics on empty options or narrow widths.
type AskComponent struct {
	Question    string
	MultiSelect bool
	Options     []AskOption
	Cursor      int
	Selected    map[int]bool
	Done        bool
	Cancelled   bool
	Submitted   []string
}

// AskOption is one selectable row of the Ask card.
type AskOption struct {
	ID          string
	Title       string
	Description string
	Recommended bool
}

// MsgClarificationResponse is the human resolution that resumes the paused
// session wall-timer. IDs carries the submitted option ids (one entry for
// single-select); Cancelled reports an Esc dismissal.
type MsgClarificationResponse struct {
	IDs       []string
	Cancelled bool
}

// EventClarificationRequired is the engine event name emitted when the card
// opens (the wall-timer pauses on this event).
const EventClarificationRequired = "clarification.required"

// NewAskComponent builds a card over the given options. The cursor starts at
// 0 (or stays 0 when empty — renders an empty-safe card, never panics).
func NewAskComponent(question string, multiSelect bool, options []AskOption) AskComponent {
	return AskComponent{
		Question:    question,
		MultiSelect: multiSelect,
		Options:     options,
		Selected:    make(map[int]bool),
	}
}

var (
	askCardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#74c7ec"))
	askTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	askCursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	askDescStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	askRecStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f9e2af"))
	askHintStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("#585b70"))
)

// HandleKey routes one logical key ("j", "k", "up", "down", "space",
// "enter", "esc") and reports whether the card resolved (submit/cancel).
// It is the unit-testable core; Update adapts tea.KeyMsg onto it.
func (a *AskComponent) HandleKey(key string) (resolved bool) {
	if a.Done {
		return true
	}
	switch strings.ToLower(key) {
	case "j", "down":
		if len(a.Options) > 0 {
			a.Cursor = (a.Cursor + 1) % len(a.Options)
		}
		return false
	case "k", "up":
		if len(a.Options) > 0 {
			a.Cursor = (a.Cursor - 1 + len(a.Options)) % len(a.Options)
		}
		return false
	case "space", " ":
		if a.MultiSelect && len(a.Options) > 0 {
			if a.Selected[a.Cursor] {
				delete(a.Selected, a.Cursor)
			} else {
				a.Selected[a.Cursor] = true
			}
		}
		return false
	case "enter":
		a.Submit()
		return true
	case "esc", "escape":
		a.Cancel()
		return true
	default:
		return false
	}
}

// Submit resolves the card: multi-select submits all toggled ids (or the
// cursor when nothing is toggled); single-select submits the cursor.
func (a *AskComponent) Submit() {
	if a.Done {
		return
	}
	if len(a.Options) == 0 {
		a.Submitted = nil
		a.Done = true
		return
	}
	if a.MultiSelect {
		var ids []string
		for i := range a.Options {
			if a.Selected[i] {
				ids = append(ids, a.Options[i].ID)
			}
		}
		if len(ids) == 0 {
			ids = []string{a.Options[a.Cursor].ID}
		}
		a.Submitted = ids
	} else {
		a.Submitted = []string{a.Options[a.Cursor].ID}
	}
	a.Done = true
	a.Cancelled = false
}

// Cancel dismisses the card without a selection.
func (a *AskComponent) Cancel() {
	a.Done = true
	a.Cancelled = true
	a.Submitted = nil
}

// Response converts the resolution into the bus message that resumes the
// engine wall-timer.
func (a AskComponent) Response() MsgClarificationResponse {
	return MsgClarificationResponse{IDs: a.Submitted, Cancelled: a.Cancelled}
}

// Update adapts Bubble Tea key messages onto HandleKey. It returns a
// MsgClarificationResponse command exactly once, on resolution.
func (a AskComponent) Update(msg tea.Msg) (AskComponent, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}
	var key string
	switch km.Type {
	case tea.KeyUp:
		key = "up"
	case tea.KeyDown:
		key = "down"
	case tea.KeyEnter:
		key = "enter"
	case tea.KeyEscape:
		key = "esc"
	case tea.KeySpace:
		key = "space"
	default:
		key = km.String()
	}
	if resolved := a.HandleKey(key); resolved {
		resp := a.Response()
		return a, func() tea.Msg { return resp }
	}
	return a, nil
}

// Render renders the modal card at the given width. Empty options render a
// safe placeholder; width < 20 degrades gracefully (never panics).
func (a AskComponent) Render(width int) string {
	if width < 20 {
		width = 20
	}
	if width > 80 {
		width = 80
	}
	var b strings.Builder
	q := a.Question
	if q == "" {
		q = "Clarification needed"
	}
	b.WriteString(askTitleStyle.Render("? "+q) + "\n")
	for i, opt := range a.Options {
		cursor := "  "
		if i == a.Cursor {
			cursor = askCursorStyle.Render("▸ ")
		}
		check := "○"
		if a.MultiSelect {
			if a.Selected[i] {
				check = "●"
			}
			cursor += check + " "
		}
		title := opt.Title
		if opt.Recommended {
			title += " " + askRecStyle.Render("(Recommended)")
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, title))
		if opt.Description != "" {
			b.WriteString("    " + askDescStyle.Render(opt.Description) + "\n")
		}
	}
	if len(a.Options) == 0 {
		b.WriteString(askDescStyle.Render("  (no options — Enter to dismiss)") + "\n")
	}
	if a.MultiSelect {
		b.WriteString(askHintStyle.Render("j/k navigate · space toggle · enter submit · esc cancel") + "\n")
	} else {
		b.WriteString(askHintStyle.Render("j/k navigate · enter submit · esc cancel") + "\n")
	}
	return askCardStyle.Width(width).Render(strings.TrimSuffix(b.String(), "\n"))
}
