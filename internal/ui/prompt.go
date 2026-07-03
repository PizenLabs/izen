package ui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

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
	switch msg := msg.(type) {
	case tea.KeyMsg:
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
