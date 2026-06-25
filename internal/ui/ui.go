package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

type model struct {
	cfg      *config.Config
	sess     *session.Session
	resolver *modes.Resolver
	input    strings.Builder
	output   []string
	width    int
	height   int
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00FFFF")).
			Padding(0, 1)

	modeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFA500")).
			Padding(0, 1)

	outputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0E0E0")).
			Padding(0, 1)

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00")).
			Bold(true)

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888"))
)

func NewProgram(cfg *config.Config, sess *session.Session) *tea.Program {
	m := &model{
		cfg:      cfg,
		sess:     sess,
		resolver: modes.NewResolver(),
		output: []string{
			"Welcome to Izen — human-centered coding intelligence",
		},
	}
	m.resolver.Set(sess.Mode)

	m.output = append(m.output,
		fmt.Sprintf("Mode: /%s — %s", sess.Mode, sess.Mode.Description()),
		separatorStyle.Render(strings.Repeat("─", 40)),
	)

	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.sess.SetMode(m.resolver.Current())
			m.sess.Save()
			return m, tea.Quit

		case tea.KeyEnter:
			line := m.input.String()
			m.input.Reset()
			if line != "" {
				m.handleInput(line)
			}
			return m, nil

		case tea.KeyBackspace:
			s := m.input.String()
			if len(s) > 0 {
				m.input.Reset()
				m.input.WriteString(s[:len(s)-1])
			}
			return m, nil

		case tea.KeyRunes:
			m.input.WriteString(string(msg.Runes))
			return m, nil
		}
	}

	return m, nil
}

func (m *model) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Izen"))
	b.WriteString(" ")
	b.WriteString(modeStyle.Render("/" + m.resolver.Current().String()))
	b.WriteString("\n\n")

	maxOutput := m.height - 5
	if maxOutput < 1 {
		maxOutput = 1
	}

	start := 0
	if len(m.output) > maxOutput {
		start = len(m.output) - maxOutput
	}
	for _, line := range m.output[start:] {
		b.WriteString(outputStyle.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(promptStyle.Render("> "))
	b.WriteString(m.input.String())

	return b.String()
}

func (m *model) handleInput(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	newMode := m.resolver.Resolve(line)
	if newMode != m.resolver.Current() {
		m.resolver.Set(newMode)
		m.sess.SetMode(newMode)
		m.sess.Save()
		m.output = append(m.output,
			fmt.Sprintf("Switched to /%s — %s", newMode, newMode.Description()),
		)
		return
	}

	switch {
	case line == "/help" || line == "/?":
		m.output = append(m.output, "Available modes:")
		m.output = append(m.output, "  /ask          — explain, inspect, understand (read-only)")
		m.output = append(m.output, "  /plan         — architecture, migrations, refactors (no exec)")
		m.output = append(m.output, "  /build        — implement, refactor, write tests (controlled exec)")
		m.output = append(m.output, "  /investigate  — debug bugs, failures, regressions")
		m.output = append(m.output, "  /review       — audit changes, detect risks")
		m.output = append(m.output, "")
		m.output = append(m.output, "Commands:")
		m.output = append(m.output, "  /help         — show this help")
		m.output = append(m.output, "  /mode <name>  — switch mode")
		m.output = append(m.output, "  /exit         — exit Izen")
		m.output = append(m.output, "  Ctrl+C / Esc  — exit Izen")

	case line == "/exit" || line == "/quit":
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
		m.output = append(m.output, "Goodbye.")
		m.input.Reset()

	case strings.HasPrefix(line, "/mode"):
		parts := strings.Fields(line)
		if len(parts) == 2 {
			mode, ok := modes.Parse(parts[1])
			if ok {
				m.resolver.Set(mode)
				m.sess.SetMode(mode)
				m.sess.Save()
				m.output = append(m.output,
					fmt.Sprintf("Mode: /%s — %s", mode, mode.Description()),
				)
				return
			}
		}
		m.output = append(m.output, "Usage: /mode <ask|plan|build|investigate|review>")

	case strings.HasPrefix(line, "/objective"):
		obj := strings.TrimPrefix(line, "/objective")
		obj = strings.TrimSpace(obj)
		if obj != "" {
			m.sess.SetObjective(obj)
			m.sess.Save()
			m.output = append(m.output, "Objective set: "+obj)
		} else {
			m.output = append(m.output, "Usage: /objective <description>")
		}

	default:
		m.output = append(m.output,
			fmt.Sprintf("[%s] %s", strings.ToUpper(m.resolver.Current().String()), line),
		)
	}
}
