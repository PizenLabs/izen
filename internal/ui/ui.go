package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/session"
)

// ── Message types ────────────────────────────────────────────────────────────

type tokenMsg string
type streamDoneMsg struct {
	content     string
	tokenInput  int
	tokenOutput int
}
type streamErrMsg struct{ err error }
type tickMsg time.Time

type investigateResultMsg struct {
	records    []record
	sessionKey string
	err        error
}
type reviewResultMsg struct {
	records      []record
	sessionKey   string
	saveReportFn func()
	err          error
}

type agentDoneMsg struct{ label string }

// ── Output record ─────────────────────────────────────────────────────────────

type role uint8

const (
	roleSystem role = iota
	roleUser
	roleAI
	roleError
	roleCode
	roleStatus
)

type record struct {
	role role
	text string
}

// ── Version ───────────────────────────────────────────────────────────────────

const version = "0.1.0"

// ── Colour palette (Catppuccin Mocha — dim, cohesive) ───────────────────────

const (
	colorText    = "#cdd6f4"
	colorAccent  = "#a6e3a1"
	colorGreen   = "#a6e3a1"
	colorGreenBr = "#b9f0b4"
	colorRed     = "#f38ba8"
	colorOrange  = "#fab387"
	colorYellow  = "#f9e2af"
	colorCyan    = "#89dceb"
	colorTeal    = "#94e2d5"
	colorPink    = "#f5c2e7"
	colorBlue    = "#89b4fa"
	colorMauve   = "#cba6f7"

	colorSurface = "#1e1e2e"
	colorOverlay = "#313244"
	colorSubtle  = "#45475a"
	colorMuted   = "#6c7086"
	colorDimmed  = "#585b70"
	colorBase    = "#181825"
	colorCrust   = "#11111b"

	colorModeAsk         = "#a6e3a1"
	colorModePlan        = "#cba6f7"
	colorModeBuild       = "#b9f0b4"
	colorModeInvestigate = "#f9e2af"
	colorModeReview      = "#f5c2e7"

	colorGutterUser   = "#a6e3a1"
	colorGutterAI     = "#89b4fa"
	colorGutterError  = "#f38ba8"
	colorGutterStatus = "#89dceb"
	colorGutterSystem = "#45475a"
)

// ── Spinner (Braille — smooth, compact) ─────────────────────────────────────

var spinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	outputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	labelBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))

	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent))

	sepStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	hairlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	infoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	spinnerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	subtleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))

	investigationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	evidenceStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))
	hypothesisStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorTeal))
	reviewStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	scoreStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))

	riskCriticalStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	riskHighStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange))
	riskMediumStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	riskLowStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	riskInfoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))

	// Palette / completion box
	paletteBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorSubtle)).
			Padding(0, 1)
	paletteSectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMuted))
	paletteSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	paletteItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	paletteCoreItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	paletteHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	// Header
	logoStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreenBr))
	versionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	dotStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	bulletStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).SetString(" • ")

	// Mode tabs
	modeTabActiveStyle   = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	modeTabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed)).Padding(0, 1)
	modeLabelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))

	// Status bar
	statusLeftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	statusRightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	statusSepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle)).SetString(" │ ")

	// Gutter
	gutterUserStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser))
	gutterAIStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI))
	gutterErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError))
	gutterStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
	gutterSysStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem))

	labelUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser))
	labelAIStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterAI))
	labelErrorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterError))
	labelStatusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterStatus))

	// Code highlighting
	hlKeyword = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	hlString  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	hlComment = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	hlNumber  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	hlType    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	hlCodeBg  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))
	hlLang    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)
)

// ── Gutter helpers ────────────────────────────────────────────────────────────

func gutterFor(r role) string {
	switch r {
	case roleUser:
		return gutterUserStyle.Render("▌") + " "
	case roleAI:
		return gutterAIStyle.Render("▌") + " "
	case roleError:
		return gutterErrorStyle.Render("▌") + " "
	case roleStatus:
		return gutterStatusStyle.Render("▌") + " "
	case roleSystem:
		return gutterSysStyle.Render("╎") + " "
	default:
		return "  "
	}
}

func labelFor(r role) string {
	switch r {
	case roleUser:
		return labelUserStyle.Render("you")
	case roleAI:
		return labelAIStyle.Render("izen")
	case roleError:
		return labelErrorStyle.Render("error")
	case roleStatus:
		return labelStatusStyle.Render("done")
	default:
		return ""
	}
}

// ── Commands ──────────────────────────────────────────────────────────────────

var coreModes = []string{"/ask", "/plan", "/build", "/investigate", "/review"}

var utilityCommands = map[modes.Mode][]string{
	modes.ModeAsk:         {"/models", "/clear"},
	modes.ModePlan:        {"/clear"},
	modes.ModeBuild:       {"/undo", "/commit", "/checkpoint", "/clear"},
	modes.ModeInvestigate: {"/history", "/resume", "/clear", "/tokens"},
	modes.ModeReview:      {"/clear"},
}

var globalCommands = []string{"/help", "/mode", "/objective", "/quit"}

func cmdCategory(cmd string) string {
	for _, c := range coreModes {
		if c == cmd {
			return "core"
		}
	}
	for _, c := range globalCommands {
		if c == cmd {
			return "global"
		}
	}
	return "utility"
}

func modeAccentColor(m modes.Mode) lipgloss.Color {
	switch m {
	case modes.ModeAsk:
		return lipgloss.Color(colorModeAsk)
	case modes.ModePlan:
		return lipgloss.Color(colorModePlan)
	case modes.ModeBuild:
		return lipgloss.Color(colorModeBuild)
	case modes.ModeInvestigate:
		return lipgloss.Color(colorModeInvestigate)
	case modes.ModeReview:
		return lipgloss.Color(colorModeReview)
	default:
		return lipgloss.Color(colorAccent)
	}
}

// ── Model ─────────────────────────────────────────────────────────────────────

type model struct {
	cfg      *config.Config
	sess     *session.Session
	provider ai.Provider
	resolver *modes.Resolver
	gitEng   *git.Engine

	input   strings.Builder
	records []record
	width   int
	height  int

	streamCh       chan tea.Msg
	responseBuffer strings.Builder
	streaming      bool
	spinnerFrame   int
	tokenInput     int
	tokenOutput    int

	agentRunning bool
	agentLabel   string
	agentDone    bool

	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int
}

func (m *model) push(r role, text string) {
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
}

func (m *model) pushLines(r role, lines []string) {
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
}

func (m *model) pushRecords(recs []record) {
	m.records = append(m.records, recs...)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func NewProgram(cfg *config.Config, sess *session.Session, mgr *ai.Manager) *tea.Program {
	eng := git.NewEngine(".")

	var provider ai.Provider
	if defaultP, _ := mgr.Default(); defaultP != nil {
		provider = defaultP
	}

	m := &model{
		cfg:      cfg,
		sess:     sess,
		provider: provider,
		gitEng:   eng,
		resolver: modes.NewResolver(),
	}
	m.resolver.Set(sess.Mode)

	m.records = append(m.records, record{role: roleSystem, text: ""})

	return tea.NewProgram(m, tea.WithAltScreen())
}

// FIX #2: tick chain starts unconditionally — never dies between idle periods.
func (m *model) Init() tea.Cmd {
	return tickCmd()
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tickMsg:
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		}
		return m, tickCmd()

	case agentDoneMsg:
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = msg.label
		return m, nil

	case investigateResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "investigation error: "+msg.err.Error())
			return m, nil
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetInvestigationID(msg.sessionKey)
		}
		os.MkdirAll(filepath.Join(".izen", "investigations"), 0755)
		return m, nil

	case reviewResultMsg:
		m.agentRunning = false
		m.agentDone = true
		if msg.err != nil {
			m.push(roleError, "review error: "+msg.err.Error())
			return m, nil
		}
		m.pushRecords(msg.records)
		if msg.sessionKey != "" {
			m.sess.SetReviewID(msg.sessionKey)
		}
		if msg.saveReportFn != nil {
			msg.saveReportFn()
		}
		return m, nil

	case tokenMsg:
		m.responseBuffer.WriteString(string(msg))
		return m, m.readStream()

	case streamDoneMsg:
		m.streamCh = nil
		m.streaming = false
		m.tokenInput += msg.tokenInput
		m.tokenOutput += msg.tokenOutput
		final := msg.content
		if final == "" {
			final = m.responseBuffer.String()
		}
		m.push(roleAI, final)
		m.push(roleStatus, "response complete")
		m.responseBuffer.Reset()
		return m, nil

	case streamErrMsg:
		m.streamCh = nil
		m.streaming = false
		m.push(roleError, "stream error: "+msg.err.Error())
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// ── Key handling ──────────────────────────────────────────────────────────────

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			sel := m.suggestions[m.suggestionIdx]
			if m.suggestionType == "@" {
				raw := line
				atIdx := strings.LastIndex(raw, "@")
				if atIdx >= 0 {
					line = raw[:atIdx] + sel
				} else {
					line = sel
				}
				m.dismissSuggestions()
				m.input.WriteString(line)
				return m, nil
			}
			line = sel
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

	case tea.KeySpace:
		m.input.WriteString(" ")
		m.updateSuggestions()
		return m, nil

	case tea.KeyRunes:
		m.input.WriteString(string(msg.Runes))
		m.updateSuggestions()
		return m, nil
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

const (
	// Fixed chrome lines consumed by non-body elements.
	chromeHeader  = 1 // header line
	chromeModeBar = 1 // mode tab line
	chromeTopDiv  = 1 // separator after mode bar
	chromeBotDiv  = 1 // separator before status
	chromeStatus  = 1 // status bar at very bottom
	chromePrompt  = 2 // blank + gutter+label+chevron+input+cursor
	chromeFixed   = chromeHeader + chromeModeBar + chromeTopDiv + chromeBotDiv + chromeStatus + chromePrompt
)

func (m *model) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	header := m.renderHeader(width)
	modeBar := m.renderModeBar(width)
	topDiv := hairlineStyle.Render(strings.Repeat("─", width))
	body := m.renderBody(width)
	botDiv := topDiv
	status := m.renderStatusBar(width)

	parts := []string{header, modeBar, topDiv, body}
	if m.showSuggestions && len(m.suggestions) > 0 {
		parts = append(parts, "\n"+m.renderSuggestions(width))
	}
	parts = append(parts, botDiv, status)

	return lipgloss.JoinVertical(lipgloss.Top, parts...)
}

// ── Code highlighting ──────────────────────────────────────────────────────────

var goKeywords = map[string]bool{
	"func": true, "var": true, "const": true, "type": true, "struct": true,
	"interface": true, "map": true, "chan": true, "go": true, "defer": true,
	"return": true, "if": true, "else": true, "for": true, "range": true,
	"switch": true, "case": true, "default": true, "break": true, "continue": true,
	"package": true, "import": true, "select": true, "nil": true, "true": true,
	"false": true, "error": true, "string": true, "int": true, "bool": true,
	"make": true, "new": true, "append": true, "len": true, "cap": true,
	"delete": true, "close": true, "goroutine": true, "fallthrough": true,
}

var shKeywords = map[string]bool{
	"echo": true, "cd": true, "ls": true, "mkdir": true, "rm": true,
	"cat": true, "grep": true, "sed": true, "awk": true, "curl": true,
	"export": true, "source": true, "sudo": true, "chmod": true,
	"git": true, "go": true, "make": true, "docker": true,
}

var goTypes = map[string]bool{
	"string": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "uint": true, "float32": true, "float64": true, "byte": true,
	"rune": true, "bool": true, "error": true, "any": true,
}

func colorTokens(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[:idx] + hlComment.Render(line[idx:])
	}
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return hlComment.Render(line)
	}
	if strings.ContainsAny(line, "\"'`") {
		return hlString.Render(line)
	}
	words := strings.Fields(line)
	out := make([]string, len(words))
	for i, w := range words {
		clean := strings.Trim(w, "(),;:{}&*[]")
		switch {
		case goTypes[clean]:
			out[i] = strings.Replace(w, clean, hlType.Render(clean), 1)
		case goKeywords[clean] || shKeywords[clean]:
			out[i] = strings.Replace(w, clean, hlKeyword.Render(clean), 1)
		case len(clean) > 0 && clean[0] >= '0' && clean[0] <= '9':
			out[i] = strings.Replace(w, clean, hlNumber.Render(clean), 1)
		default:
			out[i] = w
		}
	}
	return strings.Join(out, " ")
}

func highlightCode(lines []string) []string {
	result := make([]string, 0, len(lines))
	inBlock := false
	lang := ""

	for _, line := range lines {
		if !inBlock {
			if strings.HasPrefix(line, "```") {
				inBlock = true
				lang = strings.TrimPrefix(line, "```")
				tag := ""
				if lang != "" {
					tag = "  " + hlLang.Render(lang)
				}
				result = append(result, hlCodeBg.Render("  ╾──"+tag))
				continue
			}
			result = append(result, line)
			continue
		}
		if strings.HasPrefix(line, "```") {
			inBlock = false
			lang = ""
			result = append(result, hlCodeBg.Render("  ╼──"))
			continue
		}
		_ = lang
		result = append(result, hlCodeBg.Render("  │ ")+colorTokens(line))
	}
	if inBlock {
		result = append(result, hlCodeBg.Render("  ╼──"))
	}
	return result
}

// ── Body renderer ─────────────────────────────────────────────────────────────

func (m *model) renderBody(width int) string {
	var body strings.Builder

	// Calculate available lines for records.
	// Always reserve chromeMaxSugg lines for suggestions (even when hidden)
	// to prevent viewport jumping when the palette appears/disappears.
	statusLines := 0
	if m.agentRunning || m.agentDone || m.streaming {
		statusLines = 1
	}

	available := m.height - chromeFixed - statusLines
	if available < 1 {
		available = 1
	}

	// Slice records from the end.
	start := 0
	if len(m.records) > available {
		start = len(m.records) - available
	}
	visible := m.records[start:]

	// Highlight code blocks.
	texts := make([]string, len(visible))
	for i, rec := range visible {
		texts[i] = rec.text
	}
	highlighted := highlightCode(texts)

	// Render records.
	for i, rec := range visible {
		gutter := gutterFor(rec.role)
		content := highlighted[i]
		switch rec.role {
		case roleUser:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleAI:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleError:
			body.WriteString(gutter + errorStyle.Render(content))
		case roleStatus:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render(content))
		default:
			body.WriteString(gutter + outputStyle.Render(content))
		}
		body.WriteString("\n")
	}

	// Dynamic status lines (agent / streaming).
	if m.agentRunning {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		aiGutter := gutterAIStyle.Render("▌") + " "
		body.WriteString(aiGutter + sp + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render(m.agentLabel+"…") + "\n")
	} else if m.agentDone {
		doneGutter := gutterStatusStyle.Render("▌") + " "
		body.WriteString(doneGutter + labelStatusStyle.Render(m.agentLabel+" complete") + "\n")
	} else if m.streaming {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		aiGutter := gutterAIStyle.Render("▌") + " "
		if m.responseBuffer.Len() > 0 {
			streamStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorText)).
				Width(width - 4)
			body.WriteString(aiGutter + streamStyle.Render(m.responseBuffer.String()) + "\n")
		} else {
			body.WriteString(aiGutter + sp + "  " + infoStyle.Render("thinking…") + "\n")
		}
	}

	// Prompt line.
	promptLine := m.renderPrompt(width)
	body.WriteString(promptLine)

	return body.String()
}

// ── Prompt ────────────────────────────────────────────────────────────────────

func (m *model) renderPrompt(width int) string {
	gutter := gutterUserStyle.Render("▌") + " "
	label := labelUserStyle.Render("you")
	chevron := promptStyle.Render(" ❯ ")
	inputText := outputStyle.Render(m.input.String())
	cursor := cursorStyle.Render("▋")
	return "\n" + gutter + label + chevron + inputText + cursor
}

// ── Header ────────────────────────────────────────────────────────────────────

func (m *model) renderHeader(width int) string {
	var h strings.Builder

	// "izen v0.1.0" — compact logo + version
	h.WriteString(logoStyle.Render("izen"))
	h.WriteString(" ")
	h.WriteString(versionStyle.Render("v" + version))
	h.WriteString(bulletStyle.String())

	// Model info
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	h.WriteString(dimStyle.Render(provider + " " + modelName))
	h.WriteString(bulletStyle.String())

	// Working directory
	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	h.WriteString(dimStyle.Render(shortWd))

	// Objective (if set)
	if m.sess.Objective != "" {
		obj := m.sess.Objective
		avail := width - lipgloss.Width(h.String()) - 6
		if avail > 10 && len(obj) > avail {
			obj = obj[:avail-3] + "…"
		}
		h.WriteString(dotStyle.Render(" · "))
		h.WriteString(infoStyle.Render(obj))
	}

	return h.String()
}

// ── Mode bar ──────────────────────────────────────────────────────────────────

func (m *model) renderModeBar(width int) string {
	var b strings.Builder

	current := "/" + m.resolver.Current().String()
	for i, mname := range coreModes {
		if i > 0 {
			b.WriteString(hairlineStyle.Render("  "))
		}
		if mname == current {
			mode, _ := modes.Parse(mname[1:])
			activeStyle := modeTabActiveStyle.Foreground(modeAccentColor(mode))
			b.WriteString(activeStyle.Render(mname))
		} else {
			b.WriteString(modeTabInactiveStyle.Render(mname))
		}
	}

	// Mode description on the right.
	desc := m.resolver.Current().Description()
	descStyled := dimStyle.Render("— " + desc)
	barW := lipgloss.Width(b.String())
	gap := width - barW - lipgloss.Width(descStyled) - 2
	if gap < 2 {
		gap = 2
	}
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(descStyled)

	return b.String()
}

// ── Status bar (tmux/airline-style) ──────────────────────────────────────────

func (m *model) renderStatusBar(width int) string {
	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	branch, _ := m.gitEng.Branch()

	// Left: path (branch)
	var left strings.Builder
	left.WriteString(statusLeftStyle.Render(shortWd))
	if branch != "" {
		left.WriteString(statusLeftStyle.Render(" (" + branch + ")"))
	}

	// Right: provider model · tokens · cost
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()

	totalTokens := m.tokenInput + m.tokenOutput
	maxContext := 32768
	pct := float64(totalTokens) / float64(maxContext) * 100
	var tokStr string
	if totalTokens >= 1000 {
		tokStr = fmt.Sprintf("%.1fk/%dk", float64(totalTokens)/1000, maxContext/1000)
	} else {
		tokStr = fmt.Sprintf("%d/%dk", totalTokens, maxContext/1000)
	}

	var costStr string
	if provider == "ollama" {
		costStr = "$0.00"
	} else {
		cost := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
		costStr = fmt.Sprintf("$%.4f", cost)
	}

	var right strings.Builder
	right.WriteString(statusRightStyle.Render(provider))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(modelName))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(tokStr + fmt.Sprintf(" (%.0f%%)", pct)))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(costStr))

	// Pad between left and right.
	leftW := lipgloss.Width(left.String())
	rightW := lipgloss.Width(right.String())
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	return left.String() + strings.Repeat(" ", gap) + right.String()
}

// ── Suggestions ───────────────────────────────────────────────────────────────

func (m *model) renderSuggestions(width int) string {
	maxVisible := 6
	items := m.suggestions
	if len(items) > maxVisible {
		items = items[:maxVisible]
	}

	var inner strings.Builder

	if m.suggestionType == "@" {
		inner.WriteString(paletteSectionStyle.Render("files"))
		inner.WriteString("\n")
		for i, s := range items {
			if i == m.suggestionIdx {
				inner.WriteString(paletteSelectedStyle.Render(" ❯ " + s))
			} else {
				inner.WriteString(paletteItemStyle.Render("   " + s))
			}
			inner.WriteString("\n")
		}
	} else {
		inner.WriteString(paletteSectionStyle.Render("commands"))
		inner.WriteString("\n")

		prevCat := ""
		for i, s := range items {
			cat := cmdCategory(s)
			if cat != prevCat {
				prevCat = cat
				var label string
				switch cat {
				case "core":
					label = "modes"
				case "utility":
					label = m.resolver.Current().String()
				case "global":
					label = "global"
				}
				inner.WriteString(paletteSectionStyle.Render("  " + label))
				inner.WriteString("\n")
			}

			baseStyle := paletteItemStyle
			if cat == "core" {
				baseStyle = paletteCoreItemStyle
			}
			if i == m.suggestionIdx {
				inner.WriteString(paletteSelectedStyle.Render(" ❯ " + s))
			} else {
				inner.WriteString(baseStyle.Render("   " + s))
			}
			inner.WriteString("\n")
		}
	}

	inner.WriteString(paletteHintStyle.Render("tab · enter · esc"))

	boxWidth := 40
	if width < boxWidth+4 {
		boxWidth = width - 4
	}
	return paletteBoxStyle.Width(boxWidth).Render(inner.String())
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
		m.suggestions = m.filterCommands(current[1:])
		m.suggestionIdx = 0
		if len(m.suggestions) == 1 && m.suggestions[0] == current {
			m.showSuggestions = false
		}
		return
	}
	atIdx := strings.LastIndex(current, "@")
	if atIdx >= 0 {
		prefix := current[atIdx+1:]
		if !strings.Contains(prefix, " ") {
			m.showSuggestions = true
			m.suggestionType = "@"
			m.suggestions = filterFilesRecursive(prefix)
			m.suggestionIdx = 0
			if len(m.suggestions) == 1 && m.suggestions[0] == prefix {
				m.showSuggestions = false
			}
			return
		}
	}
	m.dismissSuggestions()
}

func (m *model) filterCommands(prefix string) []string {
	var result []string
	matches := func(cmd string) bool {
		return prefix == "" || strings.HasPrefix(cmd, "/"+prefix)
	}
	currentMode := m.resolver.Current()
	for _, c := range coreModes {
		if matches(c) {
			result = append(result, c)
		}
	}
	for _, c := range utilityCommands[currentMode] {
		if matches(c) {
			result = append(result, c)
		}
	}
	for _, c := range globalCommands {
		if matches(c) {
			result = append(result, c)
		}
	}
	return result
}

// ── @ file auto-complete ─────────────────────────────────────────────────────

func filterFilesRecursive(prefix string) []string {
	const limit = 20

	prefix = strings.TrimPrefix(prefix, "./")

	// Determine search root: the directory prefix, if any.
	searchDir := "."
	if idx := strings.LastIndex(prefix, "/"); idx >= 0 {
		searchDir = prefix[:idx]
		if searchDir == "" {
			searchDir = "."
		}
	}

	var results []string
	_ = filepath.WalkDir(searchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(results) >= limit {
			return filepath.SkipAll
		}

		// Skip hidden files/dirs at the root level, but allow hidden subdir matches.
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				// Skip known noisy directories at any depth.
				switch name {
				case ".git", ".svn", ".DS_Store", ".izen":
					return filepath.SkipDir
				}
				// Allow hidden dirs deeper in the tree if they're being explicitly searched.
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch name {
			case "vendor", "node_modules", "dist", "build", "__pycache__", "target", ".next":
				return filepath.SkipDir
			}
			return nil
		}

		rel := path
		if strings.HasPrefix(rel, "./") {
			rel = rel[2:]
		}

		// Match prefix against the relative path from the project root.
		if prefix == "" || strings.HasPrefix(rel, prefix) || strings.Contains(strings.ToLower(rel), strings.ToLower(prefix)) {
			results = append(results, rel)
		}
		return nil
	})

	sort.Slice(results, func(i, j int) bool {
		// Prefer files with exact prefix match, then shorter paths.
		iExact := strings.HasPrefix(results[i], prefix)
		jExact := strings.HasPrefix(results[j], prefix)
		if iExact != jExact {
			return iExact
		}
		return len(results[i]) < len(results[j])
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// ── Streaming ─────────────────────────────────────────────────────────────────

func (m *model) streamCmd(content string) tea.Cmd {
	if m.streamCh != nil {
		m.push(roleSystem, "already streaming…")
		return nil
	}
	if m.provider == nil {
		m.push(roleError, "no AI provider configured")
		return nil
	}

	m.streamCh = make(chan tea.Msg, 1024)
	m.streaming = true
	m.spinnerFrame = 0

	req := ai.Request{
		Model: m.cfg.ActiveModelName(),
		Messages: []ai.Message{
			{Role: "user", Content: content},
		},
		Stream: true,
	}

	go func() {
		defer close(m.streamCh)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			m.streamCh <- streamErrMsg{err: err}
			return
		}
		defer rawStream.Close()

		var full strings.Builder
		tokIn, tokOut := 0, 0
		buf := make([]byte, 4096)

		for {
			n, err := rawStream.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				full.WriteString(chunk)
				m.streamCh <- tokenMsg(chunk)
			}
			if err == io.EOF {
				if sr, ok := rawStream.(*providers.StreamResult); ok {
					tokIn, tokOut = sr.Usage()
				}
				if tokIn == 0 && tokOut == 0 {
					tokIn = len(content) / 4
					tokOut = full.Len() / 4
				}
				m.streamCh <- streamDoneMsg{content: full.String(), tokenInput: tokIn, tokenOutput: tokOut}
				return
			}
			if err != nil {
				m.streamCh <- streamErrMsg{err: err}
				return
			}
		}
	}()

	return tea.Batch(m.readStream(), tickCmd())
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

// ── Input dispatch ────────────────────────────────────────────────────────────

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			return nil
		}
		m.push(roleSystem, "$ "+shellCmd)
		out, err := execShell(shellCmd)
		if err != nil {
			m.push(roleError, err.Error())
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			m.push(roleSystem, l)
		}
		return nil
	}

	line = m.expandFileRefs(line)

	if strings.HasPrefix(line, "/") {
		return m.handleCommand(line)
	}

	m.push(roleUser, line)

	newMode := m.resolver.Resolve(line)
	if newMode != m.resolver.Current() {
		m.resolver.Set(newMode)
		m.sess.SetMode(newMode)
		m.sess.Save()
		modeColor := modeAccentColor(newMode)
		modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(fmt.Sprintf("→ /%s — %s", newMode, newMode.Description()))
		m.push(roleSystem, modeLabel)
	}

	content := stripModePrefix(line)
	if content == "" {
		return nil
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		return m.runInvestigateCmd(content)
	case modes.ModeReview:
		return m.runReviewCmd()
	default:
		m.responseBuffer.Reset()
		return m.streamCmd(content)
	}
}

// ── Agent commands (non-blocking) ─────────────────────────────────────────────

func (m *model) runInvestigateCmd(content string) tea.Cmd {
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "investigating"
	m.spinnerFrame = 0

	return func() tea.Msg {
		eng := investigate.NewEngine(".", content, nil, nil)
		result, err := eng.Run()
		if err != nil {
			return investigateResultMsg{err: err}
		}

		var recs []record
		push := func(r role, text string) {
			for _, l := range strings.Split(text, "\n") {
				recs = append(recs, record{role: r, text: l})
			}
		}

		push(roleAI, investigationStyle.Render(fmt.Sprintf("problem: %s", result.Problem)))
		push(roleAI, investigationStyle.Render(fmt.Sprintf(
			"duration: %s · loops: %d · hypotheses: %d · evidence: %d",
			result.Duration, result.Loops, len(result.Hypotheses), len(result.Evidence))))

		if result.Resolved {
			push(roleStatus, hypothesisStyle.Render("✓ "+result.Conclusion))
		} else {
			push(roleSystem, infoStyle.Render("investigation inconclusive"))
		}

		for _, h := range result.Hypotheses {
			sym := "○"
			switch h.Status {
			case investigate.HypothesisConfirmed:
				sym = "✓"
			case investigate.HypothesisRejected:
				sym = "✗"
			}
			push(roleAI, hypothesisStyle.Render(
				fmt.Sprintf("  %s %s [%s] (%.0f%%)", sym, h.Theory, h.Status, h.Confidence*100)))
		}

		for _, ev := range result.Evidence {
			c := ev.Content
			if len(c) > 60 {
				c = c[:60] + "…"
			}
			push(roleAI, evidenceStyle.Render(fmt.Sprintf("  [%s] %s", ev.Source, c)))
		}

		if !result.Resolved && result.Error != "" {
			push(roleError, "error: "+result.Error)
		}

		return investigateResultMsg{records: recs, sessionKey: result.Problem}
	}
}

func (m *model) runReviewCmd() tea.Cmd {
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "reviewing"
	m.spinnerFrame = 0

	return func() tea.Msg {
		eng := review.NewEngine(".", nil, nil)
		result, err := eng.Run()
		if err != nil {
			return reviewResultMsg{err: err}
		}

		var recs []record
		push := func(r role, text string) {
			for _, l := range strings.Split(text, "\n") {
				recs = append(recs, record{role: r, text: l})
			}
		}

		if result.Error != "" {
			push(roleSystem, infoStyle.Render(result.Error))
			return reviewResultMsg{records: recs}
		}

		push(roleAI, reviewStyle.Render(fmt.Sprintf("review: %s → %s", result.BaseBranch, result.Branch)))
		push(roleAI, reviewStyle.Render(fmt.Sprintf(
			"commit: %s · files: %d · duration: %s",
			result.CommitHash, len(result.FilesChanged), result.Duration)))

		scoreColor := scoreStyle
		if result.Score < 50 {
			scoreColor = errorStyle
		} else if result.Score < 75 {
			scoreColor = riskHighStyle
		}
		push(roleAI, scoreColor.Render(fmt.Sprintf("score: %d/100  risk: %d/100", result.Score, result.ImpactRadius.RiskScore)))

		if len(result.FilesChanged) > 0 {
			push(roleAI, infoStyle.Render("changed files:"))
			for _, f := range result.FilesChanged {
				sym := "~"
				switch f.Status {
				case "added":
					sym = "+"
				case "deleted":
					sym = "-"
				case "renamed":
					sym = "→"
				}
				push(roleAI, infoStyle.Render(fmt.Sprintf("  %s %s (+%d/-%d)", sym, f.Path, f.Additions, f.Deletions)))
			}
		}

		if len(result.ImpactRadius.IndirectFiles) > 0 {
			push(roleAI, riskMediumStyle.Render(fmt.Sprintf(
				"impact: %d direct · %d indirect · %d packages",
				len(result.ImpactRadius.DirectFiles),
				len(result.ImpactRadius.IndirectFiles),
				len(result.ImpactRadius.AffectedPkgs))))
		}

		sevOrder := []review.RiskSeverity{
			review.RiskCritical, review.RiskHigh, review.RiskMedium, review.RiskLow, review.RiskInfo,
		}
		sevStyles := map[review.RiskSeverity]lipgloss.Style{
			review.RiskCritical: riskCriticalStyle,
			review.RiskHigh:     riskHighStyle,
			review.RiskMedium:   riskMediumStyle,
			review.RiskLow:      riskLowStyle,
			review.RiskInfo:     riskInfoStyle,
		}

		for _, sev := range sevOrder {
			var findings []review.RiskFinding
			for _, f := range result.RiskFindings {
				if f.Severity == sev {
					findings = append(findings, f)
				}
			}
			if len(findings) == 0 {
				continue
			}
			style := sevStyles[sev]
			push(roleAI, style.Render(fmt.Sprintf("  [%s] %d findings", strings.ToUpper(string(sev)), len(findings))))
			for _, f := range findings {
				push(roleAI, style.Render(fmt.Sprintf("    %s:%d — %s", f.File, f.Line, f.Description)))
			}
		}

		if len(result.Recommendations) > 0 {
			push(roleAI, reviewStyle.Render("recommendations:"))
			for i, rec := range result.Recommendations {
				push(roleAI, infoStyle.Render(fmt.Sprintf("  %d. %s", i+1, rec)))
			}
		}

		sessionKey := result.Branch + "@" + result.CommitHash
		savedResult := result
		return reviewResultMsg{
			records:      recs,
			sessionKey:   sessionKey,
			saveReportFn: func() { review.SaveReport(savedResult, ".") },
		}
	}
}

// ── Command handler ───────────────────────────────────────────────────────────

func (m *model) handleCommand(cmd string) tea.Cmd {
	switch {
	case cmd == "/help" || cmd == "/?":
		m.push(roleSystem, labelBoldStyle.Render("modes"))
		m.push(roleSystem, infoStyle.Render("  /ask         explain, inspect, understand (read-only)"))
		m.push(roleSystem, infoStyle.Render("  /plan        architecture, migrations, refactors"))
		m.push(roleSystem, infoStyle.Render("  /build       implement, refactor, write tests"))
		m.push(roleSystem, infoStyle.Render("  /investigate debug bugs, failures, regressions"))
		m.push(roleSystem, infoStyle.Render("  /review      audit changes, detect risks"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("commands"))
		m.push(roleSystem, infoStyle.Render("  /help         show this help"))
		m.push(roleSystem, infoStyle.Render("  /mode <name>  switch mode"))
		m.push(roleSystem, infoStyle.Render("  /objective    set session objective"))
		m.push(roleSystem, infoStyle.Render("  /quit         exit"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>        run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("file references"))
		m.push(roleSystem, infoStyle.Render("  @<path>  reference a file anywhere in your message"))
		m.push(roleSystem, infoStyle.Render("           supports subdirs, e.g. @internal/ai/client.go"))
		return nil

	case cmd == "/quit":
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
		m.push(roleSystem, "goodbye.")
		return tea.Quit

	case strings.HasPrefix(cmd, "/mode"):
		parts := strings.Fields(cmd)
		if len(parts) == 2 {
			mode, ok := modes.Parse(parts[1])
			if ok {
				m.resolver.Set(mode)
				m.sess.SetMode(mode)
				m.sess.Save()
				modeColor := modeAccentColor(mode)
				modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
					fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
				m.push(roleSystem, modeLabel)
				return nil
			}
		}
		m.push(roleSystem, infoStyle.Render("usage: /mode <ask|plan|build|investigate|review>"))
		return nil

	case strings.HasPrefix(cmd, "/objective"):
		obj := strings.TrimSpace(strings.TrimPrefix(cmd, "/objective"))
		if obj != "" {
			m.sess.SetObjective(obj)
			m.sess.Save()
			m.push(roleSystem, infoStyle.Render("objective: "+obj))
		} else {
			m.push(roleSystem, infoStyle.Render("usage: /objective <description>"))
		}
		return nil

	case cmd == "/clear":
		m.records = nil
		return nil

	case cmd == "/models":
		m.push(roleSystem, infoStyle.Render("active model: "+m.cfg.ActiveModelName()))
		return nil

	case cmd == "/tokens":
		m.push(roleSystem, infoStyle.Render(
			fmt.Sprintf("tokens: %d in / %d out", m.tokenInput, m.tokenOutput)))
		return nil

	case cmd == "/undo":
		m.push(roleSystem, infoStyle.Render("/undo not yet implemented"))
		return nil

	case cmd == "/commit":
		m.push(roleSystem, infoStyle.Render("/commit not yet implemented"))
		return nil

	case cmd == "/checkpoint":
		m.push(roleSystem, infoStyle.Render("/checkpoint not yet implemented"))
		return nil

	case cmd == "/history":
		m.push(roleSystem, infoStyle.Render("/history not yet implemented"))
		return nil

	case cmd == "/resume":
		m.push(roleSystem, infoStyle.Render("/resume not yet implemented"))
		return nil
	}

	for _, mode := range []modes.Mode{
		modes.ModeAsk, modes.ModePlan, modes.ModeBuild,
		modes.ModeInvestigate, modes.ModeReview,
	} {
		prefix := "/" + mode.String()
		if strings.HasPrefix(strings.ToLower(cmd), prefix) {
			m.resolver.Set(mode)
			m.sess.SetMode(mode)
			m.sess.Save()
			modeColor := modeAccentColor(mode)
			modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
				fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
			m.push(roleSystem, modeLabel)
			content := strings.TrimSpace(cmd[len(prefix):])
			if content == "" {
				return nil
			}
			m.push(roleUser, content)
			m.responseBuffer.Reset()
			return m.streamCmd(content)
		}
	}

	m.push(roleError, "unknown command: "+cmd)
	return nil
}

// ── Utilities ─────────────────────────────────────────────────────────────────

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

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func stripModePrefix(line string) string {
	for _, mode := range []modes.Mode{
		modes.ModeAsk, modes.ModePlan, modes.ModeBuild,
		modes.ModeInvestigate, modes.ModeReview,
	} {
		prefix := "/" + mode.String()
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return line
}

// expandFileRefs resolves @-prefixed file references in the input line.
func (m *model) expandFileRefs(line string) string {
	fields := strings.Fields(line)
	changed := false
	for i, field := range fields {
		if strings.HasPrefix(field, "@") {
			ref := filepath.Clean(field[1:])
			if ref == "" || ref == "." {
				continue
			}
			if _, err := os.Stat(ref); err == nil {
				fields[i] = ref
				changed = true
				continue
			}
			// Try glob match.
			matches, err := filepath.Glob(ref)
			if err == nil && len(matches) > 0 {
				fields[i] = matches[0]
				changed = true
				continue
			}
			// Try as-is without @ prefix (relative path from project root).
			if _, err := os.Stat(field[1:]); err == nil {
				fields[i] = field[1:]
				changed = true
				continue
			}
			// Unresolvable: warn but keep the reference.
			m.push(roleSystem, infoStyle.Render("warn: @"+field[1:]+" not found — sending as literal"))
			fields[i] = field[1:]
			changed = true
		}
	}
	if changed {
		return strings.Join(fields, " ")
	}
	return line
}
