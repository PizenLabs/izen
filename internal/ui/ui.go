package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
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
	gitEng   *git.Engine

	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int
}

const (
	mintGreen    = "#a6e3a1"
	dimmedGray   = "#a6adc8"
	darkGreen    = "#1b4d3e"
	textColor    = "#cdd6f4"
	surfaceColor = "#313244"
	subtleGray   = "#6c7086"
	errorRed     = "#f38ba8"
	warningColor = "#fab387"
	infoBlue     = "#89b4fa"
	goldColor    = "#f9e2af"
	pinkColor    = "#f5c2e7"
	cyanColor    = "#89dceb"
	tealColor    = "#94e2d5"
	peachColor   = "#fab387"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(mintGreen)).
			Padding(0, 1)

	modeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(dimmedGray)).
			Padding(0, 1)

	outputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(textColor)).
			Padding(0, 1)

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(mintGreen)).
			Bold(true)

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(darkGreen))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(dimmedGray))

	investigationStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(goldColor)).
				Padding(0, 1)

	evidenceStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(cyanColor))

	hypothesisStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(tealColor))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(errorRed))

	reviewStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(pinkColor)).
			Padding(0, 1)

	riskCriticalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(errorRed)).
				Bold(true)

	riskHighStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(peachColor))

	riskMediumStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(warningColor))

	riskLowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(goldColor))

	riskInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(infoBlue))

	scoreStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(mintGreen)).
			Padding(0, 1)

	suggestionBoxStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Margin(0, 0)

	suggestionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(mintGreen)).
				Padding(0, 0)

	suggestionSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(mintGreen)).
				Bold(true)

	suggestionItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(dimmedGray))

	footerLeftStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(dimmedGray))

	footerRightStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(dimmedGray))

	footerModelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(mintGreen))

	logoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(mintGreen))

	contextStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(dimmedGray))

	hintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(subtleGray))

	menuTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(textColor))

	headerLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(darkGreen))
)

var allCommands = []string{
	"/ask",
	"/plan",
	"/build",
	"/investigate",
	"/review",
	"/help",
	"/?",
	"/mode",
	"/objective",
	"/exit",
	"/quit",
}

func NewProgram(cfg *config.Config, sess *session.Session) *tea.Program {
	eng := git.NewEngine(".")

	m := &model{
		cfg:    cfg,
		sess:   sess,
		gitEng: eng,
		resolver: modes.NewResolver(),
		output: []string{
			"Welcome to Izen \u2014 human-centered coding intelligence",
		},
	}
	m.resolver.Set(sess.Mode)

	m.output = append(m.output,
		fmt.Sprintf("Mode: /%s \u2014 %s", sess.Mode, sess.Mode.Description()),
		separatorStyle.Render(strings.Repeat("\u2500", 40)),
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
		case tea.KeyCtrlC, tea.KeyEscape:
			if m.showSuggestions {
				m.dismissSuggestions()
				return m, nil
			}
			m.sess.SetMode(m.resolver.Current())
			m.sess.Save()
			return m, tea.Quit

		case tea.KeyEnter:
			line := m.input.String()
			m.input.Reset()

			if m.showSuggestions && len(m.suggestions) > 0 {
				line = m.suggestions[m.suggestionIdx]
			}
			m.dismissSuggestions()

			if line != "" {
				cmd := m.handleInput(line)
				return m, cmd
			}
			return m, nil

		case tea.KeyTab:
			if m.showSuggestions && len(m.suggestions) > 0 {
				m.suggestionIdx = (m.suggestionIdx + 1) % len(m.suggestions)
			}
			return m, nil

		case tea.KeyShiftTab:
			if m.showSuggestions && len(m.suggestions) > 0 {
				m.suggestionIdx--
				if m.suggestionIdx < 0 {
					m.suggestionIdx = len(m.suggestions) - 1
				}
			}
			return m, nil

		case tea.KeyBackspace:
			s := m.input.String()
			if len(s) > 0 {
				m.input.Reset()
				m.input.WriteString(s[:len(s)-1])
				m.updateSuggestions()
			} else {
				m.dismissSuggestions()
			}
			return m, nil

		case tea.KeyRunes:
			s := string(msg.Runes)
			m.input.WriteString(s)
			current := m.input.String()

			switch {
			case current == "/":
				m.showSuggestions = true
				m.suggestionType = "/"
				m.suggestions = filterCommands("")
				m.suggestionIdx = 0

			case current == "@":
				m.showSuggestions = true
				m.suggestionType = "@"
				m.suggestions = filterFiles("")
				m.suggestionIdx = 0

			case m.showSuggestions && m.suggestionType == "/" && strings.HasPrefix(current, "/"):
				m.suggestions = filterCommands(current[1:])
				m.suggestionIdx = 0
				if len(m.suggestions) == 1 && m.suggestions[0] == current {
					m.showSuggestions = false
				}

			case m.showSuggestions && m.suggestionType == "@" && strings.HasPrefix(current, "@"):
				m.suggestions = filterFiles(current[1:])
				m.suggestionIdx = 0
				if len(m.suggestions) == 1 && m.suggestions[0] == current[1:] {
					m.showSuggestions = false
				}

			default:
				if m.showSuggestions {
					m.dismissSuggestions()
				}
			}
			return m, nil
		}
	}

	return m, nil
}

func (m *model) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	header := m.renderHeader(width)
	footer := m.renderFooter(width)

	footerHeight := 3
	headerHeight := 3

	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	var body strings.Builder
	maxOutput := bodyHeight - 2
	if maxOutput < 1 {
		maxOutput = 1
	}

	start := 0
	if len(m.output) > maxOutput && m.showSuggestions == false {
		start = len(m.output) - maxOutput
	} else if len(m.output) > maxOutput && m.showSuggestions {
		suggLines := len(m.suggestions) + 3
		avail := maxOutput - suggLines
		if avail < 1 {
			avail = 1
		}
		if len(m.output) > avail {
			start = len(m.output) - avail
		}
	}
	for _, line := range m.output[start:] {
		body.WriteString(outputStyle.Render(line))
		body.WriteString("\n")
	}

	if m.showSuggestions && len(m.suggestions) > 0 {
		body.WriteString("\n")
		m.renderSuggestions(&body)
		body.WriteString("\n")
	}

	body.WriteString("\n")
	body.WriteString(promptStyle.Render("> "))
	body.WriteString(m.input.String())

	return lipgloss.JoinVertical(lipgloss.Top,
		header,
		body.String(),
		footer,
	)
}

func (m *model) renderHeader(width int) string {
	var h strings.Builder

	logo := logoStyle.Render("Z")
	h.WriteString(logo)

	h.WriteString("   ")

	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	ctxText := shortWd
	if m.sess.Objective != "" {
		obj := m.sess.Objective
		if len(obj) > 40 {
			obj = obj[:40] + "..."
		}
		ctxText = ctxText + "  \u2502  " + obj
	}
	h.WriteString(contextStyle.Render(ctxText))
	h.WriteString("\n")

	sep := strings.Repeat("\u2500", width)
	h.WriteString(separatorStyle.Render(sep))

	return h.String()
}

func (m *model) renderFooter(width int) string {
	var f strings.Builder

	sep := strings.Repeat("\u2500", width)
	f.WriteString(separatorStyle.Render(sep))
	f.WriteString("\n")

	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)

	branch, _ := m.gitEng.Branch()
	left := shortWd
	if branch != "" {
		left = left + " (" + branch + ")"
	}

	provider := m.cfg.Models.Provider
	modelName := m.cfg.Models.Default
	if provider == "" {
		provider = "unknown"
	}
	right := "(" + provider + ") " + modelName + " \u2022 active"
	rightStyled := footerModelStyle.Render(right)

	leftStyled := footerLeftStyle.Render(left)
	leftW := lipgloss.Width(leftStyled)
	rightW := lipgloss.Width(rightStyled)
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	line := leftStyled + strings.Repeat(" ", gap) + rightStyled
	f.WriteString(line)

	return f.String()
}

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func (m *model) renderSuggestions(b *strings.Builder) {
	header := "commands"
	if m.suggestionType == "@" {
		header = "files"
	}
	b.WriteString(menuTitleStyle.Render("select a " + header))
	b.WriteString("\n")
	for i, s := range m.suggestions {
		if i == m.suggestionIdx {
			b.WriteString(suggestionSelectedStyle.Render("-> " + s))
		} else {
			b.WriteString(suggestionItemStyle.Render("   " + s))
		}
		b.WriteString("\n")
	}
	b.WriteString(hintStyle.Render("press enter to confirm or esc to go back"))
}

func (m *model) dismissSuggestions() {
	m.showSuggestions = false
	m.suggestionType = ""
	m.suggestions = nil
	m.suggestionIdx = 0
}

func (m *model) updateSuggestions() {
	current := m.input.String()
	if current == "" {
		m.dismissSuggestions()
		return
	}
	if strings.HasPrefix(current, "/") {
		m.showSuggestions = true
		m.suggestionType = "/"
		m.suggestions = filterCommands(current[1:])
		m.suggestionIdx = 0
		if len(m.suggestions) == 1 && m.suggestions[0] == current {
			m.showSuggestions = false
		}
		return
	}
	if strings.HasPrefix(current, "@") {
		m.showSuggestions = true
		m.suggestionType = "@"
		m.suggestions = filterFiles(current[1:])
		m.suggestionIdx = 0
		if len(m.suggestions) == 1 && m.suggestions[0] == current[1:] {
			m.showSuggestions = false
		}
		return
	}
	m.dismissSuggestions()
}

func filterCommands(prefix string) []string {
	if prefix == "" {
		result := make([]string, len(allCommands))
		copy(result, allCommands)
		return result
	}
	var result []string
	for _, cmd := range allCommands {
		if strings.HasPrefix(cmd, "/"+prefix) {
			result = append(result, cmd)
		}
	}
	return result
}

func filterFiles(prefix string) []string {
	pattern := prefix + "*"
	if prefix == "" {
		pattern = "*"
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	limit := 15
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	line = m.expandFileRefs(line)

	switch {
	case line == "/help" || line == "/?":
		m.output = append(m.output, "Available modes:")
		m.output = append(m.output, "  /ask          - explain, inspect, understand (read-only)")
		m.output = append(m.output, "  /plan         - architecture, migrations, refactors (no exec)")
		m.output = append(m.output, "  /build        - implement, refactor, write tests (controlled exec)")
		m.output = append(m.output, "  /investigate  - debug bugs, failures, regressions")
		m.output = append(m.output, "  /review       - audit changes, detect risks")
		m.output = append(m.output, "")
		m.output = append(m.output, "Commands:")
		m.output = append(m.output, "  /help         - show this help")
		m.output = append(m.output, "  /mode <name>  - switch mode")
		m.output = append(m.output, "  /exit         - exit Izen")
		m.output = append(m.output, "  Ctrl+C / Esc  - exit Izen")
		m.output = append(m.output, "")
		m.output = append(m.output, "File References:")
		m.output = append(m.output, "  @<pattern>    - reference a file (e.g. @main.go)")
		return nil

	case line == "/exit" || line == "/quit":
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
		m.output = append(m.output, "Goodbye.")
		return tea.Quit

	case strings.HasPrefix(line, "/mode"):
		parts := strings.Fields(line)
		if len(parts) == 2 {
			mode, ok := modes.Parse(parts[1])
			if ok {
				m.resolver.Set(mode)
				m.sess.SetMode(mode)
				m.sess.Save()
				m.output = append(m.output,
					fmt.Sprintf("Mode: /%s \u2014 %s", mode, mode.Description()),
				)
				return nil
			}
		}
		m.output = append(m.output, "Usage: /mode <ask|plan|build|investigate|review>")
		return nil

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
		return nil
	}

	newMode := m.resolver.Resolve(line)
	modeChanged := newMode != m.resolver.Current()
	if modeChanged {
		m.resolver.Set(newMode)
		m.sess.SetMode(newMode)
		m.sess.Save()
		m.output = append(m.output,
			fmt.Sprintf("Switched to /%s \u2014 %s", newMode, newMode.Description()),
		)
	}

	content := stripModePrefix(line)
	if content == "" {
		return nil
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		m.handleInvestigateInput(content)
	case modes.ModeReview:
		m.handleReviewInput(content)
	default:
		m.output = append(m.output,
			fmt.Sprintf("[%s] %s", strings.ToUpper(m.resolver.Current().String()), content),
		)
	}
	return nil
}

func stripModePrefix(line string) string {
	for _, mode := range []modes.Mode{modes.ModeAsk, modes.ModePlan, modes.ModeBuild, modes.ModeInvestigate, modes.ModeReview} {
		prefix := "/" + mode.String()
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			rest := strings.TrimSpace(line[len(prefix):])
			return rest
		}
	}
	return line
}

func (m *model) expandFileRefs(line string) string {
	fields := strings.Fields(line)
	changed := false
	for i, field := range fields {
		if strings.HasPrefix(field, "@") {
			ref := field[1:]
			if ref == "" {
				continue
			}
			matches, err := filepath.Glob(ref)
			if err == nil && len(matches) > 0 {
				fields[i] = matches[0]
				changed = true
			}
		}
	}
	if changed {
		return strings.Join(fields, " ")
	}
	return line
}

func (m *model) handleInvestigateInput(line string) {
	sess := m.sess
	investDir := filepath.Join(".izen", "investigations")

	m.output = append(m.output, separatorStyle.Render("\u2501 Running investigation \u2501"))

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
		statusSym := "\u25cb"
		switch h.Status {
		case investigate.HypothesisConfirmed:
			statusSym = "\u2713"
		case investigate.HypothesisRejected:
			statusSym = "\u2717"
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

	m.output = append(m.output, separatorStyle.Render("\u2501 Investigation complete \u2501"))
}

func (m *model) handleReviewInput(line string) {
	sess := m.sess

	m.output = append(m.output, separatorStyle.Render("\u2501 Running review \u2501"))

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
		fmt.Sprintf("Review: %s \u2192 %s", result.BaseBranch, result.Branch)))
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
				statusSym = "\u2192"
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
					fmt.Sprintf("    %s:%d \u2014 %s", f.File, f.Line, f.Description)))
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

	m.output = append(m.output, separatorStyle.Render("\u2501 Review complete \u2501"))
}
