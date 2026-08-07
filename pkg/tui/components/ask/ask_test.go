package ask

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/pkg/ir"
)

// sampleQuestions builds a two-question fixture with a preset default and a
// free-form branch.
func sampleQuestions() []ir.ClarificationQuestion {
	return []ir.ClarificationQuestion{
		{
			ID:           "workspace-conflict",
			Header:       "Workspace Conflict Detected",
			QuestionText: "Your request targets a portfolio, but this workspace is a todo_app workspace.",
			Options: []ir.QuestionOption{
				{ID: ir.OptionReplaceWorkspace, Label: "Completely replace workspace with portfolio", Description: "Discards the existing todo_app files"},
				{ID: ir.OptionBuildAlongside, Label: "Build alongside", Description: "Keeps the current files"},
				{ID: ir.OptionMergeSelective, Label: "Merge selectively", Description: "Keeps the relevant parts", IsDefault: true},
				ir.NewCustomAnswerOption(),
			},
		},
		{
			ID:           "stack-choice",
			Header:       "Stack Choice",
			QuestionText: "Which stack should the portfolio use?",
			Options: []ir.QuestionOption{
				{ID: "vanilla", Label: "Vanilla HTML/CSS/JS", Description: "No framework"},
				{ID: "react", Label: "React", Description: "Component-based"},
			},
		},
	}
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// enterCustomMode moves the cursor onto the free-form branch and enters
// typing mode.
func enterCustomMode(m Model) Model {
	m, _ = m.UpdateModel(key(tea.KeyDown)) // default(2) -> 3 (custom)
	m, _ = m.UpdateModel(key(tea.KeyEnter))
	return m
}

func TestNewPositionsCursorOnDefault(t *testing.T) {
	m := New(sampleQuestions())
	if m.Active != 0 {
		t.Errorf("Active = %d, want 0", m.Active)
	}
	if m.Cursor != 2 {
		t.Errorf("Cursor = %d, want 2 (merge_selective default)", m.Cursor)
	}
	if m.Done() || m.Dismissed() {
		t.Error("fresh model must not be done")
	}
	if m.ActiveQuestion().ID != "workspace-conflict" {
		t.Errorf("ActiveQuestion = %+v", m.ActiveQuestion())
	}
}

func TestTabSwitchesQuestion(t *testing.T) {
	m := New(sampleQuestions())
	next, _ := m.UpdateModel(key(tea.KeyTab))
	if next.Active != 1 {
		t.Errorf("Active after Tab = %d, want 1", next.Active)
	}
	// Cursor re-anchors on the second question's default (first option).
	if next.Cursor != 0 {
		t.Errorf("Cursor after Tab = %d, want 0", next.Cursor)
	}
	// Wraps back to the first question.
	wrapped, _ := next.UpdateModel(key(tea.KeyTab))
	if wrapped.Active != 0 || wrapped.Cursor != 2 {
		t.Errorf("wrapped = Active %d Cursor %d, want 0/2", wrapped.Active, wrapped.Cursor)
	}
}

func TestShiftTabSwitchesBack(t *testing.T) {
	m := New(sampleQuestions())
	back, _ := m.UpdateModel(key(tea.KeyShiftTab))
	if back.Active != 1 {
		t.Errorf("Active after Shift+Tab = %d, want 1 (wrapped back)", back.Active)
	}
}

func TestUpDownNavigatesOptions(t *testing.T) {
	m := New(sampleQuestions())
	down, _ := m.UpdateModel(key(tea.KeyDown))
	if down.Cursor != 3 {
		t.Errorf("Cursor after Down = %d, want 3", down.Cursor)
	}
	downAgain, _ := down.UpdateModel(key(tea.KeyDown))
	if downAgain.Cursor != 0 {
		t.Errorf("Cursor wrapped = %d, want 0", downAgain.Cursor)
	}
	up, _ := downAgain.UpdateModel(key(tea.KeyUp))
	if up.Cursor != 3 {
		t.Errorf("Cursor after Up = %d, want 3", up.Cursor)
	}
}

func TestEnterConfirmsAndAdvances(t *testing.T) {
	m := New(sampleQuestions())
	// Move to the replace branch (index 0) and confirm q1.
	m, _ = m.UpdateModel(key(tea.KeyUp)) // cursor 1 (build alongside)
	m, _ = m.UpdateModel(key(tea.KeyUp)) // cursor 0 (replace)
	m, cmd := m.UpdateModel(key(tea.KeyEnter))

	if m.Done() {
		t.Fatal("confirming the first of two questions must auto-advance, not finish")
	}
	if cmd != nil {
		t.Errorf("advance command = %v, want nil", cmd)
	}
	if m.Active != 1 {
		t.Errorf("Active after q1 confirm = %d, want 1", m.Active)
	}
	resp := m.Result()
	if len(resp.Answers) != 1 {
		t.Fatalf("answers = %d, want 1 (q1 filled, q2 pending)", len(resp.Answers))
	}
	if resp.Answers[0].QuestionID != "workspace-conflict" || resp.Answers[0].OptionID != ir.OptionReplaceWorkspace {
		t.Errorf("answer[0] = %+v, want replace_workspace", resp.Answers[0])
	}
}

func TestConfirmAllQuestionsFinishes(t *testing.T) {
	m := New(sampleQuestions())
	// Answer q1 (default merge_selective) then q2 (default vanilla).
	m, _ = m.UpdateModel(key(tea.KeyEnter))
	m, cmd := m.UpdateModel(key(tea.KeyEnter))

	if !m.Done() {
		t.Fatal("model must finish once every question is answered")
	}
	if cmd == nil {
		t.Fatal("finishing must emit tea.Quit")
	}
	resp := m.Result()
	if len(resp.Answers) != 2 {
		t.Fatalf("answers = %d, want 2", len(resp.Answers))
	}
	if resp.Answers[0].OptionID != ir.OptionMergeSelective {
		t.Errorf("answer[0] = %+v, want merge_selective", resp.Answers[0])
	}
	if resp.Answers[1].OptionID != "vanilla" {
		t.Errorf("answer[1] = %+v, want vanilla", resp.Answers[1])
	}
}

func TestCustomOptionSwitchesToTyping(t *testing.T) {
	m := New(sampleQuestions())
	m = enterCustomMode(m)
	if m.Done() {
		t.Error("custom confirm must not finish the model")
	}
	if m.Mode != ModeTyping {
		t.Errorf("Mode = %d, want ModeTyping", m.Mode)
	}
	if m.Cursor != 3 {
		t.Errorf("Cursor = %d, want 3 (custom)", m.Cursor)
	}
}

func TestTypingCustomAnswer(t *testing.T) {
	m := New(sampleQuestions())
	m = enterCustomMode(m)

	// Type the custom answer and submit: this answers q1 and auto-advances.
	m, _ = m.UpdateModel(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("SvelteKit")})
	m, cmd := m.UpdateModel(key(tea.KeyEnter))
	if m.Done() {
		t.Fatal("custom answer must auto-advance to the remaining question, not finish")
	}
	if cmd != nil {
		t.Errorf("advance command = %v, want nil", cmd)
	}
	if m.Active != 1 {
		t.Errorf("Active = %d, want 1", m.Active)
	}

	// Answer the remaining question; the model then finishes.
	m, cmd = m.UpdateModel(key(tea.KeyEnter))
	if !m.Done() {
		t.Fatal("model must be done once every question is answered")
	}
	if cmd == nil {
		t.Fatal("finishing must emit tea.Quit")
	}
	resp := m.Result()
	if len(resp.Answers) != 2 {
		t.Fatalf("answers = %d, want 2", len(resp.Answers))
	}
	if resp.Answers[0].OptionID != ir.OptionTypeYourOwn {
		t.Errorf("OptionID = %q, want type_your_own", resp.Answers[0].OptionID)
	}
	if resp.Answers[0].CustomAnswer != "SvelteKit" {
		t.Errorf("CustomAnswer = %q, want SvelteKit", resp.Answers[0].CustomAnswer)
	}
	if resp.Answers[1].OptionID != "vanilla" {
		t.Errorf("answer[1] = %+v, want vanilla", resp.Answers[1])
	}
}

func TestEscInTypingReturnsToBrowse(t *testing.T) {
	m := New(sampleQuestions())
	m = enterCustomMode(m)
	if m.Mode != ModeTyping {
		t.Fatalf("setup: Mode = %d, want typing", m.Mode)
	}
	m, _ = m.UpdateModel(key(tea.KeyEscape))
	if m.Mode != ModeBrowse {
		t.Errorf("Mode after Esc = %d, want browse", m.Mode)
	}
	if m.Done() {
		t.Error("Esc in typing must not dismiss the whole prompt")
	}
}

func TestEscDismisses(t *testing.T) {
	m := New(sampleQuestions())
	m, cmd := m.UpdateModel(key(tea.KeyEscape))
	if !m.Done() || !m.Dismissed() {
		t.Fatal("Esc must dismiss the prompt")
	}
	if cmd == nil {
		t.Fatal("dismiss must emit tea.Quit")
	}
	resp := m.Result()
	if len(resp.Answers) != 2 {
		t.Fatalf("dismissed answers = %d, want 2 (defaults)", len(resp.Answers))
	}
	if resp.Answers[0].OptionID != ir.OptionMergeSelective {
		t.Errorf("dismissed answer[0] = %+v, want merge_selective default", resp.Answers[0])
	}
	if resp.Answers[1].OptionID != "vanilla" {
		t.Errorf("dismissed answer[1] = %+v, want vanilla default", resp.Answers[1])
	}
}

func TestViewRendersLayout(t *testing.T) {
	m := New(sampleQuestions())
	view := m.View()
	for _, want := range []string{
		"Questions Before Implementation",
		"Workspace Conflict",
		"Stack Choice",
		"Your request targets a portfolio",
		"⇆ tab",
		"↑↓ select",
		"enter confirm",
		"esc dismiss",
		"Type your own answer",
		"Completely replace workspace with portfolio",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q\nview:\n%s", want, view)
		}
	}
}

func TestViewTypingModeRendersInput(t *testing.T) {
	m := New(sampleQuestions())
	m = enterCustomMode(m)
	view := m.View()
	for _, want := range []string{"Your answer:", "enter submit", "esc back to options"} {
		if !strings.Contains(view, want) {
			t.Errorf("typing View missing %q\nview:\n%s", want, view)
		}
	}
}

func TestNewEmptyQuestions(t *testing.T) {
	m := New(nil)
	if m.Done() {
		t.Error("empty model must not be done")
	}
	if view := m.View(); view != "" {
		t.Errorf("empty View = %q, want empty", view)
	}
	updated, cmd := m.UpdateModel(key(tea.KeyEnter))
	if updated.Done() || cmd != nil {
		t.Error("confirm on empty model must no-op without panicking")
	}
}

func TestWindowSizePropagates(t *testing.T) {
	m := New(sampleQuestions())
	m, _ = m.UpdateModel(tea.WindowSizeMsg{Width: 120, Height: 30})
	if m.Width != 120 || m.Height != 30 {
		t.Errorf("WindowSize not stored: %dx%d", m.Width, m.Height)
	}
}

func TestNoOptionsQuestion(t *testing.T) {
	q := []ir.ClarificationQuestion{{ID: "q1", Header: "H", QuestionText: "Free form only"}}
	m := New(q)
	m, cmd := m.UpdateModel(key(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("enter with no options returned a command: %v", cmd)
	}
	if m.Done() {
		t.Error("enter with no options must not finish (no confirmable branch)")
	}
	if view := m.View(); !strings.Contains(view, "no preset options") {
		t.Errorf("View should explain the empty option list:\n%s", view)
	}
}
