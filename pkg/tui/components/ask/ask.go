// Package ask provides the interactive "Questions Before Implementation"
// component: a Bubble Tea model that pauses the pipeline to ask the user
// which execution branch an ambiguous intent should take.
//
// Layout, from top to bottom:
//
//	┌ Questions Before Implementation ────────────────────────┐
//	│ [1/2]  Workspace Conflict Detected                      │
//	│ Your request targets a portfolio, but this workspace is │
//	│ currently a todo_app workspace. How should I proceed?   │
//	│                                                         │
//	│   ▸ Completely replace workspace with portfolio         │
//	│     Discards the existing todo_app files...             │
//	│   ○ Build portfolio alongside the existing workspace    │
//	│   ○ Type your own answer                                │
//	│                                                         │
//	│ ⇆ tab   ↑↓ select   enter confirm   esc dismiss         │
//	└─────────────────────────────────────────────────────────┘
//
// The model is a value type designed to be embedded in a larger Bubble Tea
// model; Run wraps it in a standalone program for pipeline use. Confirming an
// option resolves the model with an ir.ClarificationResponse; Esc dismisses
// it, falling back to the default answers so a pipeline never hangs.
package ask

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/pkg/ir"
)

// InputMode discriminates whether the model is browsing options or collecting
// a free-form answer.
type InputMode int

const (
	// ModeBrowse highlights and confirms preset options.
	ModeBrowse InputMode = iota
	// ModeTyping collects a custom answer through the embedded text input.
	ModeTyping
)

// Model is the interactive clarification component.
type Model struct {
	// Questions is the set of questions to ask, in tab order.
	Questions []ir.ClarificationQuestion
	// Active is the index of the currently displayed question.
	Active int
	// Cursor is the index of the highlighted option within the active
	// question.
	Cursor int
	// Mode is the current input mode.
	Mode InputMode
	// Input is the free-form answer text input.
	Input textinput.Model

	// Width and Height are the terminal bounds the View lays out within.
	Width  int
	Height int

	response  ir.ClarificationResponse
	done      bool
	dismissed bool
}

// New builds a browsing-mode model over the given questions. The cursor is
// pre-positioned on each question's default option.
func New(questions []ir.ClarificationQuestion) Model {
	ti := textinput.New()
	ti.Placeholder = "type your answer…"
	ti.CharLimit = 256

	m := Model{
		Questions: questions,
		Input:     ti,
	}
	if len(questions) > 0 {
		m.Cursor = indexOfOption(questions[0], questions[0].DefaultOptionID())
	}
	return m
}

// Result returns the accumulated response. When the model was dismissed the
// response holds the default answers for every question.
func (m Model) Result() ir.ClarificationResponse {
	if m.dismissed {
		return ir.ClarificationResponse{Answers: ir.DefaultAnswers(m.Questions)}
	}
	out := m.response
	out.Answers = append([]ir.ClarificationAnswer(nil), m.response.Answers...)
	return out
}

// Done reports whether the model resolved and should quit.
func (m Model) Done() bool { return m.done }

// Dismissed reports whether the user escaped out instead of confirming.
func (m Model) Dismissed() bool { return m.dismissed }

// ActiveQuestion returns the question currently displayed. It returns the
// zero value when no questions exist.
func (m Model) ActiveQuestion() ir.ClarificationQuestion {
	if len(m.Questions) == 0 {
		return ir.ClarificationQuestion{}
	}
	return m.Questions[m.Active]
}

// Init implements tea.Model. The component starts with no commands.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model. Use UpdateModel to keep a concrete Model when
// embedding the component in a parent Bubble Tea model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	return updated, cmd
}

// UpdateModel is the typed variant of Update for hosts that embed the
// component and need a concrete Model back.
func (m Model) UpdateModel(msg tea.Msg) (Model, tea.Cmd) {
	return m.update(msg)
}

// update is the shared dispatch used by both Update and UpdateModel.
func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}

// handleKey routes a keystroke by input mode.
func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.Mode == ModeTyping {
		return m.handleTypingKey(msg)
	}
	return m.handleBrowseKey(msg)
}

// handleBrowseKey implements the option-list keybindings.
func (m Model) handleBrowseKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyTab:
		m.nextQuestion()
		return m, nil
	case tea.KeyShiftTab:
		m.prevQuestion()
		return m, nil
	case tea.KeyUp, tea.KeyShiftUp:
		if msg.Type == tea.KeyShiftUp {
			m.prevQuestion()
			return m, nil
		}
		m.moveCursor(-1)
		return m, nil
	case tea.KeyDown, tea.KeyShiftDown:
		if msg.Type == tea.KeyShiftDown {
			m.nextQuestion()
			return m, nil
		}
		m.moveCursor(1)
		return m, nil
	case tea.KeyEnter:
		return m.confirm()
	case tea.KeyEscape:
		return m.dismiss()
	default:
		return m, nil
	}
}

// handleTypingKey implements the free-form input keybindings: Enter submits
// the typed answer, Esc returns to the option list.
func (m Model) handleTypingKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		answer := ir.ClarificationAnswer{
			QuestionID:   m.ActiveQuestion().ID,
			OptionID:     ir.OptionTypeYourOwn,
			CustomAnswer: strings.TrimSpace(m.Input.Value()),
		}
		return m.confirmWith(answer)
	case tea.KeyEscape:
		m.Mode = ModeBrowse
		m.Input.Blur()
		return m, nil
	default:
		var cmd tea.Cmd
		m.Input, cmd = m.Input.Update(msg)
		return m, cmd
	}
}

// nextQuestion switches to the following question, wrapping around, and
// re-anchors the cursor on its default option.
func (m *Model) nextQuestion() {
	if len(m.Questions) == 0 {
		return
	}
	m.Active = (m.Active + 1) % len(m.Questions)
	m.Cursor = indexOfOption(m.ActiveQuestion(), m.ActiveQuestion().DefaultOptionID())
}

// prevQuestion switches to the preceding question, wrapping around.
func (m *Model) prevQuestion() {
	if len(m.Questions) == 0 {
		return
	}
	m.Active = (m.Active - 1 + len(m.Questions)) % len(m.Questions)
	m.Cursor = indexOfOption(m.ActiveQuestion(), m.ActiveQuestion().DefaultOptionID())
}

// moveCursor shifts the highlighted option within the active question.
func (m *Model) moveCursor(delta int) {
	q := m.ActiveQuestion()
	if len(q.Options) == 0 {
		return
	}
	m.Cursor = (m.Cursor + delta + len(q.Options)) % len(q.Options)
}

// confirm resolves the active question with the highlighted option. When that
// option is the free-form branch, the model switches to typing mode instead.
func (m Model) confirm() (Model, tea.Cmd) {
	q := m.ActiveQuestion()
	if len(q.Options) == 0 {
		return m, nil
	}
	idx := clampCursor(m.Cursor, len(q.Options))
	opt := q.Options[idx]
	if ir.IsCustomAnswerOption(opt) {
		m.Mode = ModeTyping
		m.Input.Focus()
		return m, nil
	}
	return m.confirmWith(ir.ClarificationAnswer{QuestionID: q.ID, OptionID: opt.ID})
}

// confirmWith records an answer for the active question and advances to the
// next unanswered question. When every question carries a selection the model
// finishes and the host reads Result() from the final model.
func (m Model) confirmWith(answer ir.ClarificationAnswer) (Model, tea.Cmd) {
	active := m.Active
	m.response.Answers = upsertAnswer(m.response.Answers, active, answer)

	if next := m.nextUnanswered(); next >= 0 {
		m.Mode = ModeBrowse
		m.Input.Blur()
		m.Active = next
		m.Cursor = indexOfOption(m.ActiveQuestion(), m.ActiveQuestion().DefaultOptionID())
		return m, nil
	}
	m.done = true
	return m, tea.Quit
}

// nextUnanswered returns the index of the first question without a recorded
// answer, or -1 when every question is answered.
func (m Model) nextUnanswered() int {
	for i := range m.Questions {
		if i >= len(m.response.Answers) {
			return i
		}
		a := m.response.Answers[i]
		if a.OptionID == "" && a.CustomAnswer == "" {
			return i
		}
	}
	return -1
}

// dismiss resolves every question to its default and finishes.
func (m Model) dismiss() (Model, tea.Cmd) {
	m.dismissed = true
	m.done = true
	return m, tea.Quit
}

// upsertAnswer replaces the answer at position idx of the response, growing
// the slice as needed.
func upsertAnswer(answers []ir.ClarificationAnswer, idx int, answer ir.ClarificationAnswer) []ir.ClarificationAnswer {
	for len(answers) <= idx {
		answers = append(answers, ir.ClarificationAnswer{})
	}
	answers[idx] = answer
	return answers
}

// indexOfOption returns the index of the option with id within q, falling
// back to the first option (or -1 when there are none).
func indexOfOption(q ir.ClarificationQuestion, id string) int {
	for i, o := range q.Options {
		if o.ID == id {
			return i
		}
	}
	if len(q.Options) > 0 {
		return 0
	}
	return -1
}

// clampCursor keeps cursor within [0, n-1], guarding render and confirm
// against a stale cursor from a question switch.
func clampCursor(cursor, n int) int {
	if n <= 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= n {
		return n - 1
	}
	return cursor
}

// View implements tea.Model. It renders the full "Questions Before
// Implementation" frame.
func (m Model) View() string {
	if len(m.Questions) == 0 {
		return ""
	}
	width := m.Width
	if width <= 0 {
		width = 80
	}

	q := m.ActiveQuestion()
	var b strings.Builder
	b.WriteString(renderHeader(m.Questions, m.Active, width))
	b.WriteString("\n\n")
	b.WriteString(renderQuestionText(q.QuestionText, width))
	b.WriteString("\n\n")
	if m.Mode == ModeTyping {
		b.WriteString(renderTypingInput(m, width))
	} else {
		b.WriteString(renderOptions(q.Options, m.Cursor, width))
	}
	b.WriteString("\n\n")
	b.WriteString(renderFooter(width))
	return b.String()
}

// renderHeader draws the highlighted badge and the horizontal question tabs.
func renderHeader(questions []ir.ClarificationQuestion, active, width int) string {
	badge := headerStyle.Render("Questions Before Implementation")
	tabsWidth := width - lipgloss.Width(badge) - 2
	if tabsWidth < 12 {
		tabsWidth = 12
	}
	tabs := renderTabs(questions, active, tabsWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, badge, "  ", tabs)
}

// renderTabs renders the horizontal tab bar: one tab per question, with the
// active tab highlighted. Each tab carries its own question header.
func renderTabs(questions []ir.ClarificationQuestion, active, width int) string {
	if len(questions) == 0 {
		return ""
	}
	if len(questions) == 1 {
		return tabInactiveStyle.Render("◈ " + truncate(questions[0].Header, width))
	}
	const sep = 2
	per := (width - sep*(len(questions)-1)) / len(questions)
	if per < 8 {
		per = 8
	}
	var tabs []string
	for i, q := range questions {
		label := truncate(q.Header, per-2)
		if i == active {
			tabs = append(tabs, tabActiveStyle.Render("◈ "+label))
		} else {
			tabs = append(tabs, tabInactiveStyle.Render("◇ "+label))
		}
	}
	return strings.Join(tabs, strings.Repeat(" ", sep))
}

// renderQuestionText renders the detailed prompt of the active question.
func renderQuestionText(text string, _ int) string {
	return questionStyle.Render(text)
}

// renderOptions renders the focusable option list with description cards.
func renderOptions(options []ir.QuestionOption, cursor int, _ int) string {
	if len(options) == 0 {
		return mutedStyle.Render("(no preset options — press enter to type your own)")
	}
	cur := clampCursor(cursor, len(options))
	var lines []string
	for i, opt := range options {
		lines = append(lines, renderOption(opt, i == cur))
	}
	return strings.Join(lines, "\n")
}

// renderOption renders one option row: a cursor marker, the label and the
// description card on the following indented line.
func renderOption(opt ir.QuestionOption, focused bool) string {
	marker := "  "
	label := optionStyle.Render(opt.Label)
	desc := descStyle.Render(opt.Description)
	if focused {
		marker = "▸ "
		label = optionFocusedStyle.Render(opt.Label)
		desc = descFocusedStyle.Render(opt.Description)
	} else if opt.IsDefault {
		label = optionStyle.Render(opt.Label + "  (default)")
	}
	descLine := ""
	if desc != "" {
		descLine = "\n    " + desc
	}
	return marker + label + descLine
}

// renderTypingInput renders the free-form answer text field with a prompt.
func renderTypingInput(m Model, _ int) string {
	label := inputLabelStyle.Render("Your answer:")
	value := m.Input.Value()
	if value == "" {
		value = inputPlaceholderStyle.Render(m.Input.Placeholder)
	}
	return label + "\n  " + value + "\n" + mutedStyle.Render("  enter submit · esc back to options")
}

// renderFooter draws the keybinding legend.
func renderFooter(width int) string {
	legend := "⇆ tab  ↑↓ select  enter confirm  esc dismiss"
	return strings.Repeat("─", width) + "\n" + footerStyle.Render(legend)
}

// truncate caps s to max runes, preserving a trailing marker.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
