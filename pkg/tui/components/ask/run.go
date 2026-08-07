package ask

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/pkg/ir"
)

// Run executes the interactive clarification component as a standalone Bubble
// Tea program and returns the user's response.
//
// Esc dismisses the prompt, which resolves to the default answers so the
// pipeline always resumes. When the terminal cannot be initialised (headless
// CI, piped output) or the program is interrupted, Run also falls back to the
// default answers — a missing interactive user must never deadlock the
// pipeline. Only a cancelled context is surfaced as an error.
func Run(ctx context.Context, questions []ir.ClarificationQuestion) (ir.ClarificationResponse, error) {
	m := New(questions)
	if len(questions) == 0 {
		return m.Result(), nil
	}

	opts := []tea.ProgramOption{}
	if ctx != nil {
		opts = append(opts, tea.WithContext(ctx))
	}
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		if ctx != nil && ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
			return ir.ClarificationResponse{Answers: ir.DefaultAnswers(questions)}, ctx.Err()
		}
		return ir.ClarificationResponse{Answers: ir.DefaultAnswers(questions)}, nil
	}
	if fm, ok := final.(Model); ok {
		return fm.Result(), nil
	}
	return ir.ClarificationResponse{Answers: ir.DefaultAnswers(questions)}, nil
}
