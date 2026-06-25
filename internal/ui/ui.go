package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
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

	investigationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFD700")).
				Padding(0, 1)

	evidenceStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#87CEEB"))

	hypothesisStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98FB98"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B"))

	reviewStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF69B4")).
			Padding(0, 1)

	riskCriticalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF0000")).
				Bold(true)

	riskHighStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF4500"))

	riskMediumStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500"))

	riskLowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD700"))

	riskInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#87CEEB"))

	scoreStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00FF00")).
			Padding(0, 1)
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
		switch m.resolver.Current() {
		case modes.ModeInvestigate:
			m.handleInvestigateInput(line)
		case modes.ModeReview:
			m.handleReviewInput(line)
		default:
			m.output = append(m.output,
				fmt.Sprintf("[%s] %s", strings.ToUpper(m.resolver.Current().String()), line),
			)
		}
	}
}

func (m *model) handleInvestigateInput(line string) {
	sess := m.sess
	investDir := filepath.Join(".izen", "investigations")

	m.output = append(m.output, separatorStyle.Render("━ Running investigation ━"))

	eng := investigate.NewEngine(".", line, nil, nil)
	result, err := eng.Run()

	if err != nil {
		m.output = append(m.output, errorStyle.Render("Investigation error: "+err.Error()))
		return
	}

	m.output = append(m.output, investigationStyle.Render(
		fmt.Sprintf("Investigation: %s", result.Problem)))
	m.output = append(m.output, investigationStyle.Render(
		fmt.Sprintf("Duration: %s | Loops: %d | Hypotheses: %d | Evidence: %d",
			result.Duration, result.Loops, len(result.Hypotheses), len(result.Evidence))))

	if result.Resolved {
		m.output = append(m.output, hypothesisStyle.Render(
			fmt.Sprintf("Resolved: %s", result.Conclusion)))
	} else {
		m.output = append(m.output, infoStyle.Render("Investigation did not reach a conclusion"))
	}

	for _, h := range result.Hypotheses {
		statusSym := "○"
		switch h.Status {
		case investigate.HypothesisConfirmed:
			statusSym = "✓"
		case investigate.HypothesisRejected:
			statusSym = "✗"
		}
		m.output = append(m.output, hypothesisStyle.Render(
			fmt.Sprintf("  %s %s [%s] (%.0f%%)", statusSym, h.Theory, h.Status, h.Confidence*100)))
	}

	for _, ev := range result.Evidence {
		content := ev.Content
		if len(content) > 60 {
			content = content[:60] + "..."
		}
		m.output = append(m.output, evidenceStyle.Render(
			fmt.Sprintf("  [%s] %s", ev.Source, content)))
	}

	if !result.Resolved && result.Error != "" {
		m.output = append(m.output, errorStyle.Render("Error: "+result.Error))
	}

	sess.SetInvestigationID(result.Problem)
	os.MkdirAll(investDir, 0755)

	m.output = append(m.output, separatorStyle.Render("━ Investigation complete ━"))
}

func (m *model) handleReviewInput(line string) {
	sess := m.sess

	m.output = append(m.output, separatorStyle.Render("━ Running review ━"))

	eng := review.NewEngine(".", nil, nil)
	result, err := eng.Run()

	if err != nil {
		m.output = append(m.output, errorStyle.Render("Review error: "+err.Error()))
		return
	}

	if result.Error != "" {
		m.output = append(m.output, infoStyle.Render(result.Error))
		return
	}

	m.output = append(m.output, reviewStyle.Render(
		fmt.Sprintf("Review: %s → %s", result.BaseBranch, result.Branch)))
	m.output = append(m.output, reviewStyle.Render(
		fmt.Sprintf("Commit: %s | Files: %d | Duration: %s",
			result.CommitHash, len(result.FilesChanged), result.Duration)))

	scoreColor := scoreStyle
	if result.Score < 50 {
		scoreColor = errorStyle
	} else if result.Score < 75 {
		scoreColor = riskHighStyle
	}
	m.output = append(m.output, scoreColor.Render(
		fmt.Sprintf("Review Score: %d/100 | Risk Score: %d/100",
			result.Score, result.ImpactRadius.RiskScore)))

	if len(result.FilesChanged) > 0 {
		m.output = append(m.output, infoStyle.Render("Changed Files:"))
		for _, f := range result.FilesChanged {
			statusSym := "~"
			switch f.Status {
			case "added":
				statusSym = "+"
			case "deleted":
				statusSym = "-"
			case "renamed":
				statusSym = "→"
			}
			m.output = append(m.output, infoStyle.Render(
				fmt.Sprintf("  %s %s (+%d/-%d)", statusSym, f.Path, f.Additions, f.Deletions)))
		}
	}

	if len(result.ImpactRadius.IndirectFiles) > 0 {
		impactMsg := fmt.Sprintf("Impact Radius: %d direct, %d indirect files, %d packages",
			len(result.ImpactRadius.DirectFiles),
			len(result.ImpactRadius.IndirectFiles),
			len(result.ImpactRadius.AffectedPkgs))
		m.output = append(m.output, riskMediumStyle.Render(impactMsg))
	}

	severityOrder := []review.RiskSeverity{review.RiskCritical, review.RiskHigh, review.RiskMedium, review.RiskLow, review.RiskInfo}
	severityStyles := map[review.RiskSeverity]lipgloss.Style{
		review.RiskCritical: riskCriticalStyle,
		review.RiskHigh:     riskHighStyle,
		review.RiskMedium:   riskMediumStyle,
		review.RiskLow:      riskLowStyle,
		review.RiskInfo:     riskInfoStyle,
	}

	for _, sev := range severityOrder {
		var sevFindings []review.RiskFinding
		for _, f := range result.RiskFindings {
			if f.Severity == sev {
				sevFindings = append(sevFindings, f)
			}
		}
		if len(sevFindings) > 0 {
			style := severityStyles[sev]
			m.output = append(m.output, style.Render(
				fmt.Sprintf("  [%s] (%d findings)", strings.ToUpper(string(sev)), len(sevFindings))))
			for _, f := range sevFindings {
				code := f.Code
				if len(code) > 50 {
					code = code[:50] + "..."
				}
				m.output = append(m.output, style.Render(
					fmt.Sprintf("    %s:%d — %s", f.File, f.Line, f.Description)))
			}
		}
	}

	if len(result.Recommendations) > 0 {
		m.output = append(m.output, reviewStyle.Render("Recommendations:"))
		for i, rec := range result.Recommendations {
			m.output = append(m.output, infoStyle.Render(fmt.Sprintf("  %d. %s", i+1, rec)))
		}
	}

	sess.SetReviewID(result.Branch + "@" + result.CommitHash)
	review.SaveReport(result, ".")

	m.output = append(m.output, separatorStyle.Render("━ Review complete ━"))
}
