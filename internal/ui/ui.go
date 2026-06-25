package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/session"
)

type tokenMsg string
type streamDoneMsg struct{ content string }
type streamErrMsg struct{ err error }

type model struct {
	cfg      *config.Config
	sess     *session.Session
	provider ai.Provider
	resolver *modes.Resolver
	input    strings.Builder
	output   []string
	width    int
	height   int
	gitEng   *git.Engine

	streamCh chan tea.Msg

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

	modeTabActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(mintGreen)).
				Padding(0, 1)

	modeTabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(dimmedGray)).
				Padding(0, 1)

	utilitiesStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(subtleGray))

	utilitiesLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(dimmedGray)).
				Bold(true)

	paletteHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(subtleGray))
)

var coreModes = []string{
	"/ask",
	"/plan",
	"/build",
	"/investigate",
	"/review",
}

var utilityCommands = map[modes.Mode][]string{
	modes.ModeAsk:         {"/models", "/clear"},
	modes.ModePlan:        {"/clear"},
	modes.ModeBuild:       {"/undo", "/commit", "/checkpoint", "/clear"},
	modes.ModeInvestigate: {"/history", "/resume", "/clear", "/tokens"},
	modes.ModeReview:      {"/clear"},
}

var globalCommands = []string{
	"/help",
	"/mode",
	"/objective",
	"/q",
	"/exit",
}

var allCommands []string

func init() {
	allCommands = append(allCommands, coreModes...)
	seen := map[string]bool{}
	for _, cmds := range utilityCommands {
		for _, c := range cmds {
			if !seen[c] {
				allCommands = append(allCommands, c)
				seen[c] = true
			}
		}
	}
	for _, c := range globalCommands {
		if !seen[c] {
			allCommands = append(allCommands, c)
			seen[c] = true
		}
	}
}

func NewProgram(cfg *config.Config, sess *session.Session, mgr *ai.Manager) *tea.Program {
	eng := git.NewEngine(".")

	var provider ai.Provider
	defaultP, _ := mgr.Default()
	if defaultP != nil {
		provider = defaultP
	}

	m := &model{
		cfg:      cfg,
		sess:     sess,
		provider: provider,
		gitEng:   eng,
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
	case tokenMsg:
		m.output = append(m.output, string(msg))
		return m, m.readStream()

	case streamDoneMsg:
		m.streamCh = nil
		m.output = append(m.output, separatorStyle.Render("\u2501 Response complete \u2501"))
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.output = append(m.output, errorStyle.Render("Stream error: "+msg.err.Error()))
		return m, nil

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
	modeBar := m.renderModeBar(width)

	fixedLines := 3 + 2 + 1 + 1 + 1
	bodyHeight := m.height - fixedLines
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	var body strings.Builder
	maxOutput := bodyHeight - 2
	if maxOutput < 1 {
		maxOutput = 1
	}

	start := 0
	if len(m.output) > maxOutput && !m.showSuggestions {
		start = len(m.output) - maxOutput
	} else if len(m.output) > maxOutput {
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

	utilitiesBar := m.renderUtilitiesBar(width)
	separator := separatorStyle.Render(strings.Repeat("\u2500", width))
	footer := m.renderFooter(width)

	return lipgloss.JoinVertical(lipgloss.Top,
		header,
		modeBar,
		body.String(),
		utilitiesBar,
		separator,
		footer,
	)
}

func (m *model) renderHeader(width int) string {
	var h strings.Builder

	logo := logoStyle.Render("Z")
	h.WriteString(logo)
	h.WriteString("  ")

	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	ctxText := shortWd
	if m.sess.Objective != "" {
		obj := m.sess.Objective
		avail := width - lipgloss.Width(logo) - 4
		if avail < 10 {
			avail = 10
		}
		if len(obj) > avail-4 {
			obj = obj[:avail-4] + "..."
		}
		ctxText = ctxText + "  \u2502  " + obj
	}

	ctxLen := lipgloss.Width(contextStyle.Render(ctxText))
	maxCtx := width - lipgloss.Width(logo) - 4
	if ctxLen > maxCtx && maxCtx > 10 {
		short := shortWd
		if len(short) > maxCtx-4 {
			short = short[:maxCtx-4] + "..."
		}
		ctxText = short
	}
	h.WriteString(contextStyle.Render(ctxText))
	h.WriteString("\n")

	mode := m.resolver.Current()
	modeDesc := "/" + mode.String() + " \u2014 " + mode.Description()
	modeDescW := lipgloss.Width(contextStyle.Render(modeDesc))
	if modeDescW > width-8 {
		modeDesc = "/" + mode.String()
		if lipgloss.Width(contextStyle.Render(modeDesc)) > width-8 {
			modeDesc = "/" + mode.String()
		}
	}
	h.WriteString(modeStyle.Render("Mode:"))
	h.WriteString(" ")
	h.WriteString(contextStyle.Render(modeDesc))

	return h.String()
}

func (m *model) renderModeBar(width int) string {
	var b strings.Builder

	current := "/" + m.resolver.Current().String()
	for i, mname := range coreModes {
		if i > 0 {
			b.WriteString(" ")
		}
		if mname == current {
			b.WriteString(modeTabActiveStyle.Render(mname))
		} else {
			b.WriteString(modeTabInactiveStyle.Render(mname))
		}
	}

	b.WriteString("\n")
	sep := strings.Repeat("\u2500", width)
	b.WriteString(separatorStyle.Render(sep))

	return b.String()
}

func (m *model) renderUtilitiesBar(width int) string {
	var b strings.Builder

	cmds := utilityCommands[m.resolver.Current()]
	if len(cmds) > 0 {
		b.WriteString(utilitiesLabelStyle.Render("Utilities:"))
		b.WriteString(" ")
		for i, cmd := range cmds {
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(utilitiesStyle.Render(cmd))
		}
	}

	paletteHint := "  / for palette  ! for bash"
	paletteHintLen := lipgloss.Width(paletteHintStyle.Render(paletteHint))

	utilLine := b.String()
	utilW := lipgloss.Width(utilLine)
	gap := width - utilW - paletteHintLen
	if gap < 2 {
		gap = 2
	}
	if gap > 0 {
		b.WriteString(strings.Repeat(" ", gap))
	}
	b.WriteString(paletteHintStyle.Render(paletteHint))

	return b.String()
}

func (m *model) renderFooter(width int) string {
	var f strings.Builder

	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)

	branch, _ := m.gitEng.Branch()
	left := shortWd
	if branch != "" {
		left = left + " (" + branch + ")"
	}

	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	right := "(" + provider + ") " + modelName + " \u2022 active"

	leftStyled := footerLeftStyle.Render(left)
	rightStyled := footerModelStyle.Render(right)
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
	if m.suggestionType == "@" {
		b.WriteString(menuTitleStyle.Render("select a file"))
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
		return
	}

	b.WriteString(menuTitleStyle.Render("command palette"))
	b.WriteString("\n")

	for i, s := range m.suggestions {
		isCore := false
		for _, c := range coreModes {
			if c == s {
				isCore = true
				break
			}
		}
		if i == m.suggestionIdx {
			b.WriteString(suggestionSelectedStyle.Render("-> " + s))
		} else {
			style := suggestionItemStyle
			if isCore {
				style = style.Foreground(lipgloss.Color(textColor))
			}
			b.WriteString(style.Render("   " + s))
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

func (m *model) streamCmd(content string) tea.Cmd {
	if m.streamCh != nil {
		m.output = append(m.output, infoStyle.Render("Already streaming..."))
		return nil
	}
	if m.provider == nil {
		m.output = append(m.output, errorStyle.Render("No AI provider configured"))
		return nil
	}

	m.streamCh = make(chan tea.Msg, 100)

	modelName := m.cfg.ActiveModelName()
	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{Role: "user", Content: content},
		},
		Stream: true,
	}

	go func() {
		defer close(m.streamCh)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			m.streamCh <- streamErrMsg{err: err}
			return
		}
		defer stream.Close()

		m.streamCh <- tokenMsg("--- streaming response ---")

		var full strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := stream.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				full.WriteString(chunk)
				m.streamCh <- tokenMsg(chunk)
			}
			if err == io.EOF {
				m.streamCh <- streamDoneMsg{content: full.String()}
				return
			}
			if err != nil {
				m.streamCh <- streamErrMsg{err: err}
				return
			}
		}
	}()

	return m.readStream()
}

func (m *model) readStream() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.streamCh
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.output = append(m.output, infoStyle.Render("Usage: !<shell command>"))
			return nil
		}
		m.output = append(m.output, infoStyle.Render("$ "+shellCmd))
		out, err := execShell(shellCmd)
		if err != nil {
			m.output = append(m.output, errorStyle.Render(err.Error()))
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			m.output = append(m.output, outputStyle.Render(l))
		}
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
		m.output = append(m.output, "  /q            - exit Izen")
		m.output = append(m.output, "  !<cmd>        - run a shell command")
		m.output = append(m.output, "  Ctrl+C / Esc  - exit Izen")
		m.output = append(m.output, "")
		m.output = append(m.output, "File References:")
		m.output = append(m.output, "  @<pattern>    - reference a file (e.g. @main.go)")
		return nil

	case line == "/q" || line == "/quit" || line == "/exit":
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
		return nil
	case modes.ModeReview:
		m.handleReviewInput(content)
		return nil
	default:
		m.output = append(m.output,
			fmt.Sprintf("[%s] %s", strings.ToUpper(m.resolver.Current().String()), content),
		)
		return m.streamCmd(content)
	}
}

func execShell(cmd string) (string, error) {
	c := exec.Command("bash", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	out := stdout.String()
	if stderr.Len() > 0 {
		if out != "" {
			out += "\n"
		}
		out += stderr.String()
	}
	return out, err
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