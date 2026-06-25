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
type agentDoneMsg struct{ label string }

// ── Output record ─────────────────────────────────────────────────────────────
// Every line in the scroll buffer carries a role so the renderer can apply
// the correct gutter color without re-parsing content.

type role uint8

const (
	roleSystem role = iota // dim — info, status, separators
	roleUser               // green gutter — the human's words
	roleAI                 // blue gutter  — the model's response
	roleError              // red gutter   — errors
	roleCode               // no gutter    — inside a fenced block (internal)
	roleStatus             // cyan gutter  — done / agent complete
)

type record struct {
	role role
	text string
}

// ── Wordmark ──────────────────────────────────────────────────────────────────
// Thin typographic construction — single-width strokes, geometric balance.
// Reads as "izen" without relying on block-fill mass.
const izenAscii = `╻ ╻▀█ ┏━╸┏┓╻
┃ ┃ ┃ ┣╸ ┃┗┫
┗━┛ ╹ ┗━╸╹ ╹`

// ── Colour palette ────────────────────────────────────────────────────────────
// Catppuccin Mocha base — keeps perceptual balance across token roles.
const (
	colorText    = "#cdd6f4" // lavender — main body
	colorAccent  = "#a6e3a1" // green    — user / interactive
	colorGreen   = "#a6e3a1"
	colorGreenBr = "#b9f0b4" // bright green — logo, done
	colorRed     = "#f38ba8" // maroon   — errors
	colorOrange  = "#fab387" // peach    — warnings
	colorYellow  = "#f9e2af" // yellow   — investigations
	colorCyan    = "#89dceb" // sky      — AI / streaming
	colorTeal    = "#94e2d5" // teal     — hypothesis
	colorPink    = "#f5c2e7" // pink     — review
	colorBlue    = "#89b4fa" // blue     — AI gutter / info
	colorMauve   = "#cba6f7" // mauve    — score / plan

	colorSurface = "#1e1e2e"
	colorOverlay = "#313244"
	colorSubtle  = "#45475a"
	colorMuted   = "#6c7086"
	colorDimmed  = "#585b70"

	colorModeAsk         = "#a6e3a1"
	colorModePlan        = "#cba6f7"
	colorModeBuild       = "#b9f0b4"
	colorModeInvestigate = "#f9e2af"
	colorModeReview      = "#f5c2e7"

	// Gutter bars — the parallel lines that distinguish speakers
	colorGutterUser   = "#a6e3a1" // green  — user turn
	colorGutterAI     = "#89b4fa" // blue   — AI turn
	colorGutterError  = "#f38ba8" // red    — error
	colorGutterStatus = "#89dceb" // cyan   — status / done
	colorGutterSystem = "#45475a" // subtle — system info
)

// ── Snowflake-in-bloom spinner ────────────────────────────────────────────────
// Crystal grows from a seed, blooms, breathes back. 100 ms per frame.
var spinnerFrames = []string{
	"·",  // seed
	"✦",  // spark
	"✧",  // open
	"❄",  // crystal
	"❅",  // bloom
	"❆",  // full
	"❅",  // recede
	"❄",  // settle
	"✧",  // dim
	"✦",  // ember
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	outputStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorText))
	labelBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))

	// Prompt — the single brightest element in the input zone
	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Blink(true)

	// Chrome
	sepStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSubtle))
	hairlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	infoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	spinnerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))

	// Semantic
	investigationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	evidenceStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))
	hypothesisStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(colorTeal))
	reviewStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	scoreStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))

	// Risk
	riskCriticalStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorRed))
	riskHighStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange))
	riskMediumStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	riskLowStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen))
	riskInfoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBlue))

	// Palette (command / file picker)
	paletteBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(colorSubtle)).
			Padding(0, 1)
	paletteSectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMuted))
	paletteSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	paletteItemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	paletteCoreItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	paletteHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))

	// Header / nav
	logoStyle           = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreenBr))
	contextStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	modeLabelStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	modeTabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed)).Padding(0, 1)
	utilitiesLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	utilitiesStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	footerStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed))
	footerActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
)

// ── Gutter helpers ────────────────────────────────────────────────────────────
// A gutter is the two-character left accent that identifies a speaker turn.
// Format: "▌ " in the role colour. System lines use a hairline "╎ ".

func gutterFor(r role) string {
	switch r {
	case roleUser:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser)).Render("▌") + " "
	case roleAI:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI)).Render("▌") + " "
	case roleError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterError)).Render("▌") + " "
	case roleStatus:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render("▌") + " "
	case roleSystem:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterSystem)).Render("╎") + " "
	default:
		return "  "
	}
}

// labelFor returns the small role tag shown on the first line of each turn.
func labelFor(r role) string {
	switch r {
	case roleUser:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser)).Render("you")
	case roleAI:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterAI)).Render("izen")
	case roleError:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterError)).Render("error")
	case roleStatus:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterStatus)).Render("done")
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

	input  strings.Builder
	// records is the scroll buffer — each entry carries role metadata for
	// the gutter renderer. A single "message" may span multiple records
	// (one per logical line).
	records []record
	width   int
	height  int

	// streaming
	streamCh       chan tea.Msg
	responseBuffer strings.Builder
	streaming      bool
	spinnerFrame   int
	tokenInput     int
	tokenOutput    int

	// agent animation
	agentRunning bool
	agentLabel   string
	agentDone    bool

	// suggestions
	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int
}

// push appends a styled record. Multi-line text is split so each line
// gets the same gutter applied independently in renderBody.
func (m *model) push(r role, text string) {
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
}

// pushLines appends pre-split lines all with the same role.
func (m *model) pushLines(r role, lines []string) {
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
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

	// ── Splash screen ────────────────────────────────────────────────────────
	// Art beside welcome text, zipped line-by-line.
	artStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGreenBr))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))

	artLines := strings.Split(izenAscii, "\n")
	sideLines := []string{
		"",
		dimStyle.Render("human-centered coding intelligence"),
		"",
		dimStyle.Render("type /help to get started"),
	}
	maxL := len(artLines)
	if len(sideLines) > maxL {
		maxL = len(sideLines)
	}
	for i := 0; i < maxL; i++ {
		art := ""
		if i < len(artLines) {
			art = artStyle.Render(fmt.Sprintf("%-14s", artLines[i]))
		} else {
			art = strings.Repeat(" ", 14)
		}
		side := ""
		if i < len(sideLines) {
			side = sideLines[i]
		}
		m.records = append(m.records, record{role: roleSystem, text: art + "  " + side})
	}
	m.records = append(m.records, record{role: roleSystem, text: ""})
	m.records = append(m.records, record{
		role: roleSystem,
		text: dimStyle.Render(fmt.Sprintf("mode: /%s — %s", sess.Mode, sess.Mode.Description())),
	})
	m.records = append(m.records, record{role: roleSystem, text: ""})

	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m *model) Init() tea.Cmd { return nil }

// ── Update ────────────────────────────────────────────────────────────────────

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tickMsg:
		any := false
		if m.streaming || m.agentRunning {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			any = true
		}
		if any {
			return m, tickCmd()
		}
		return m, nil

	case agentDoneMsg:
		m.agentRunning = false
		m.agentDone = true
		m.agentLabel = msg.label
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
				// Let user continue editing after file insertion
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
		// updateSuggestions handles: if @ mode and prefix contains a space,
		// it dismisses — so the user can keep typing after inserting a file ref.
		m.updateSuggestions()
		return m, nil

	case tea.KeyRunes:
		s := string(msg.Runes)
		m.input.WriteString(s)
		current := m.input.String()

		switch {
		case current == "/":
			m.showSuggestions = true
			m.suggestionType = "/"
			m.suggestions = m.filterCommands("")
			m.suggestionIdx = 0

		case current == "@":
			m.showSuggestions = true
			m.suggestionType = "@"
			m.suggestions = filterFilesRecursive("")
			m.suggestionIdx = 0

		case m.showSuggestions && m.suggestionType == "/" && strings.HasPrefix(current, "/"):
			m.suggestions = m.filterCommands(current[1:])
			m.suggestionIdx = 0
			if len(m.suggestions) == 1 && m.suggestions[0] == current {
				m.showSuggestions = false
			}

		case m.showSuggestions && m.suggestionType == "@":
			atIdx := strings.LastIndex(current, "@")
			if atIdx >= 0 {
				prefix := current[atIdx+1:]
				m.suggestions = filterFilesRecursive(prefix)
				m.suggestionIdx = 0
				if len(m.suggestions) == 1 && m.suggestions[0] == prefix {
					m.showSuggestions = false
				}
			} else {
				m.dismissSuggestions()
			}

		default:
			if m.showSuggestions {
				m.dismissSuggestions()
			}
		}
		return m, nil
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m *model) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	header      := m.renderHeader(width)
	modeBar     := m.renderModeBar(width)
	body        := m.renderBody(width)
	utilitiesBar := m.renderUtilitiesBar(width)
	sep         := sepStyle.Render(strings.Repeat("─", width))
	footer      := m.renderFooter(width)

	return lipgloss.JoinVertical(lipgloss.Top,
		header,
		modeBar,
		body,
		utilitiesBar,
		sep,
		footer,
	)
}

// ── Code highlighting ──────────────────────────────────────────────────────────
// Lightweight syntax colouring for fenced blocks. No external deps.
// Returns a new slice of display lines — may contain ANSI escapes.
// Each output line should be written verbatim (no additional style wrapping).

func highlightCode(lines []string) []string {
	kw      := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	strS    := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
	cmt     := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	num     := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMauve))
	typeS   := lipgloss.NewStyle().Foreground(lipgloss.Color(colorPink))
	codeBg  := lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Background(lipgloss.Color(colorOverlay))
	langTag := lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan)).Bold(true)

	goKeywords := map[string]bool{
		"func": true, "var": true, "const": true, "type": true, "struct": true,
		"interface": true, "map": true, "chan": true, "go": true, "defer": true,
		"return": true, "if": true, "else": true, "for": true, "range": true,
		"switch": true, "case": true, "default": true, "break": true, "continue": true,
		"package": true, "import": true, "select": true, "nil": true, "true": true,
		"false": true, "error": true, "string": true, "int": true, "bool": true,
		"make": true, "new": true, "append": true, "len": true, "cap": true,
		"delete": true, "close": true, "goroutine": true, "fallthrough": true,
	}
	shKeywords := map[string]bool{
		"echo": true, "cd": true, "ls": true, "mkdir": true, "rm": true,
		"cat": true, "grep": true, "sed": true, "awk": true, "curl": true,
		"export": true, "source": true, "sudo": true, "chmod": true,
		"git": true, "go": true, "make": true, "docker": true,
	}
	goTypes := map[string]bool{
		"string": true, "int": true, "int8": true, "int16": true, "int32": true,
		"int64": true, "uint": true, "float32": true, "float64": true, "byte": true,
		"rune": true, "bool": true, "error": true, "any": true,
	}

	colorTokens := func(line string) string {
		// Comment
		if idx := strings.Index(line, "//"); idx >= 0 {
			return line[:idx] + cmt.Render(line[idx:])
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			return cmt.Render(line)
		}
		// String literals
		if strings.ContainsAny(line, "\"'`") {
			return strS.Render(line)
		}
		words := strings.Fields(line)
		out := make([]string, len(words))
		for i, w := range words {
			clean := strings.Trim(w, "(),;:{}&*[]")
			switch {
			case goTypes[clean]:
				out[i] = strings.Replace(w, clean, typeS.Render(clean), 1)
			case goKeywords[clean] || shKeywords[clean]:
				out[i] = strings.Replace(w, clean, kw.Render(clean), 1)
			case len(clean) > 0 && clean[0] >= '0' && clean[0] <= '9':
				out[i] = strings.Replace(w, clean, num.Render(clean), 1)
			default:
				out[i] = w
			}
		}
		return strings.Join(out, " ")
	}

	result  := make([]string, 0, len(lines))
	inBlock := false
	lang    := ""

	for _, line := range lines {
		if !inBlock {
			if strings.HasPrefix(line, "```") {
				inBlock = true
				lang = strings.TrimPrefix(line, "```")
				tag := ""
				if lang != "" {
					tag = "  " + langTag.Render(lang)
				}
				// Opening bar — thin horizontal rule with language label
				result = append(result, codeBg.Render("  ╾──"+tag))
				continue
			}
			result = append(result, line)
			continue
		}
		if strings.HasPrefix(line, "```") {
			inBlock = false
			lang = ""
			result = append(result, codeBg.Render("  ╼──"))
			continue
		}
		_ = lang
		result = append(result, codeBg.Render("  │ ")+colorTokens(line))
	}
	if inBlock {
		result = append(result, codeBg.Render("  ╼──"))
	}
	return result
}

// ── Body renderer ─────────────────────────────────────────────────────────────
// Layout (top → bottom):
//
//	[scroll zone]  — visible records with gutter bars
//	[live stream]  — spinner + incremental response
//	[suggestions]  — command / file picker when active
//	[blank]
//	[prompt line]  — "▌ you  ❯ <input>▋"
//
// The prompt is NOT boxed. Two parallel vertical bars (one for the user side,
// one that's the gutter of the output above) create the visual rhythm.

func (m *model) renderBody(width int) string {
	// Chrome budget: header(2) + modeBar(2) + utilitiesBar(1) + sep(1) + footer(1) = 7
	// Prompt zone: 2 lines (blank + prompt)
	fixedLines  := 7
	promptLines := 2
	bodyHeight  := m.height - fixedLines
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	var body strings.Builder

	maxOutput := bodyHeight - promptLines
	if maxOutput < 1 {
		maxOutput = 1
	}
	if m.showSuggestions {
		suggLines := min(len(m.suggestions), 10) + 4
		maxOutput -= suggLines
		if maxOutput < 1 {
			maxOutput = 1
		}
	}
	// Reserve lines for streaming / agent status
	if m.streaming || m.agentRunning || m.agentDone {
		maxOutput -= 2
		if maxOutput < 1 {
			maxOutput = 1
		}
	}

	// ── Scroll output ─────────────────────────────────────────────────────────
	start := 0
	if len(m.records) > maxOutput {
		start = len(m.records) - maxOutput
	}
	visible := m.records[start:]

	// Run code highlighting pass on the text content
	texts := make([]string, len(visible))
	for i, rec := range visible {
		texts[i] = rec.text
	}
	highlighted := highlightCode(texts)

	for i, rec := range visible {
		gutter := gutterFor(rec.role)
		content := highlighted[i]
		// For system/plain lines, use the existing outputStyle
		switch rec.role {
		case roleUser:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleAI:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleError:
			body.WriteString(gutter + errorStyle.Render(content))
		case roleStatus:
			doneStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus))
			body.WriteString(gutter + doneStyle.Render(content))
		default:
			body.WriteString(gutter + outputStyle.Render(content))
		}
		body.WriteString("\n")
	}

	// ── Agent / streaming status ───────────────────────────────────────────────
	if m.agentRunning {
		spinner      := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		agentLStyle  := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow))
		aiGutter     := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI)).Render("▌") + " "
		body.WriteString(aiGutter + spinner + "  " + agentLStyle.Render(m.agentLabel+"…") + "\n")
	} else if m.agentDone {
		doneGutter := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render("▌") + " "
		doneLabel  := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render(m.agentLabel+" complete")
		body.WriteString(doneGutter + doneLabel + "\n")
	}

	if m.streaming {
		spinner   := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		aiGutter  := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterAI)).Render("▌") + " "
		if m.responseBuffer.Len() > 0 {
			body.WriteString(aiGutter + outputStyle.Render(m.responseBuffer.String()) + "\n")
		} else {
			body.WriteString(aiGutter + spinner + infoStyle.Render("  thinking…") + "\n")
		}
	}

	// ── Suggestions ───────────────────────────────────────────────────────────
	if m.showSuggestions && len(m.suggestions) > 0 {
		body.WriteString("\n")
		m.renderSuggestions(&body, width)
		body.WriteString("\n")
	}

	// ── Prompt line ───────────────────────────────────────────────────────────
	// Two parallel columns mirroring the output gutter:
	//   ▌ you  ❯ <typed text>▋
	// The user gutter bar and the "you" label create visual pairing with
	// the AI's "▌ izen" gutter above it in the scroll zone.
	userGutter := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterUser)).Render("▌") + " "
	userLabel  := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorGutterUser)).Render("you")
	chevron    := promptStyle.Render("  ❯ ")
	inputText  := outputStyle.Render(m.input.String())
	cursor     := cursorStyle.Render("▋")

	body.WriteString("\n")
	body.WriteString(userGutter + userLabel + chevron + inputText + cursor)

	return body.String()
}

// ── Header ────────────────────────────────────────────────────────────────────

func (m *model) renderHeader(width int) string {
	var h strings.Builder

	logo := logoStyle.Render("izen")
	h.WriteString(logo)
	h.WriteString("  ")

	wd, _     := os.Getwd()
	shortWd   := shortenPath(wd)
	ctxText   := shortWd
	if m.sess.Objective != "" {
		obj   := m.sess.Objective
		avail := width - lipgloss.Width(logo) - 6
		if avail > 10 && len(obj) > avail {
			obj = obj[:avail-3] + "…"
		}
		ctxText = shortWd + "  ·  " + obj
	}
	h.WriteString(contextStyle.Render(ctxText))
	h.WriteString("\n")

	mode      := m.resolver.Current()
	modeColor := modeAccentColor(mode)
	modeInd   := lipgloss.NewStyle().Bold(true).Foreground(modeColor).Render("/" + mode.String())
	h.WriteString(modeLabelStyle.Render("mode  "))
	h.WriteString(modeInd)
	h.WriteString(contextStyle.Render("  — " + mode.Description()))

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
			mode, _     := modes.Parse(mname[1:])
			activeStyle := lipgloss.NewStyle().Bold(true).Foreground(modeAccentColor(mode)).Padding(0, 0)
			b.WriteString(activeStyle.Render(mname))
		} else {
			b.WriteString(modeTabInactiveStyle.Render(mname))
		}
	}

	b.WriteString("\n")
	b.WriteString(hairlineStyle.Render(strings.Repeat("─", width)))

	return b.String()
}

// ── Utilities bar ─────────────────────────────────────────────────────────────

func (m *model) renderUtilitiesBar(width int) string {
	var b strings.Builder

	cmds := utilityCommands[m.resolver.Current()]
	if len(cmds) > 0 {
		b.WriteString(utilitiesLabelStyle.Render("›"))
		b.WriteString(" ")
		for i, cmd := range cmds {
			if i > 0 {
				b.WriteString(hairlineStyle.Render("  "))
			}
			b.WriteString(utilitiesStyle.Render(cmd))
		}
	}

	hint       := "/ palette  @ file  ! shell"
	hintStyled := paletteHintStyle.Render(hint)
	hintW      := lipgloss.Width(hintStyled)
	utilW      := lipgloss.Width(b.String())
	gap        := width - utilW - hintW
	if gap < 2 {
		gap = 2
	}
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(hintStyled)

	return b.String()
}

// ── Footer ────────────────────────────────────────────────────────────────────

func (m *model) renderFooter(width int) string {
	wd, _     := os.Getwd()
	shortWd   := shortenPath(wd)
	branch, _ := m.gitEng.Branch()
	left      := shortWd
	if branch != "" {
		left = left + " (" + branch + ")"
	}

	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()

	totalTokens := m.tokenInput + m.tokenOutput
	maxContext  := 32768
	pct         := float64(totalTokens) / float64(maxContext) * 100
	var tokStr string
	if totalTokens >= 1000 {
		tokStr = fmt.Sprintf("%.1fk/%dk", float64(totalTokens)/1000, maxContext/1000)
	} else {
		tokStr = fmt.Sprintf("%d/%dk", totalTokens, maxContext/1000)
	}

	var costStr string
	if provider == "ollama" {
		costStr = "$0.00 (local)"
	} else {
		cost    := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
		costStr  = fmt.Sprintf("$%.4f", cost)
	}

	leftStyled  := footerStyle.Render(left)
	right        := fmt.Sprintf("%s %s · %s (%.0f%%) · %s", provider, modelName, tokStr, pct, costStr)
	rightStyled := footerActiveStyle.Render(right)

	leftW  := lipgloss.Width(leftStyled)
	rightW := lipgloss.Width(rightStyled)
	gap    := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	return leftStyled + strings.Repeat(" ", gap) + rightStyled
}

// ── Suggestions ───────────────────────────────────────────────────────────────

func (m *model) renderSuggestions(b *strings.Builder, width int) {
	maxVisible := 12
	items       := m.suggestions
	if len(items) > maxVisible {
		items = items[:maxVisible]
	}

	var inner strings.Builder

	if m.suggestionType == "@" {
		inner.WriteString(paletteSectionStyle.Render("  files"))
		inner.WriteString("\n")
		for i, s := range items {
			if i == m.suggestionIdx {
				inner.WriteString(paletteSelectedStyle.Render("  ❯ " + s))
			} else {
				inner.WriteString(paletteItemStyle.Render("    " + s))
			}
			inner.WriteString("\n")
		}
	} else {
		inner.WriteString(paletteSectionStyle.Render("  command palette"))
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
					label = "utilities · " + m.resolver.Current().String()
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
				inner.WriteString(paletteSelectedStyle.Render("  ❯ " + s))
			} else {
				inner.WriteString(baseStyle.Render("    " + s))
			}
			inner.WriteString("\n")
		}
	}

	inner.WriteString(paletteHintStyle.Render("  tab/shift-tab navigate · enter confirm · esc dismiss"))

	boxWidth := 44
	if width < boxWidth+4 {
		boxWidth = width - 4
	}
	rendered := paletteBoxStyle.Width(boxWidth).Render(inner.String())
	b.WriteString(rendered)
}

func (m *model) dismissSuggestions() {
	m.showSuggestions = false
	m.suggestionType  = ""
	m.suggestions     = nil
	m.suggestionIdx   = 0
}

func (m *model) updateSuggestions() {
	current := m.input.String()
	if current == "" {
		m.dismissSuggestions()
		return
	}
	if strings.HasPrefix(current, "/") {
		m.showSuggestions = true
		m.suggestionType  = "/"
		m.suggestions     = m.filterCommands(current[1:])
		m.suggestionIdx   = 0
		if len(m.suggestions) == 1 && m.suggestions[0] == current {
			m.showSuggestions = false
		}
		return
	}
	// @ can appear anywhere mid-sentence
	atIdx := strings.LastIndex(current, "@")
	if atIdx >= 0 {
		prefix := current[atIdx+1:]
		// A space after the @ prefix means the user has moved on
		if !strings.Contains(prefix, " ") {
			m.showSuggestions = true
			m.suggestionType  = "@"
			m.suggestions     = filterFilesRecursive(prefix)
			m.suggestionIdx   = 0
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

// filterFilesRecursive walks the cwd recursively, returning paths that have
// the given prefix. Supports subdirectory navigation (e.g. "internal/ai").
func filterFilesRecursive(prefix string) []string {
	const limit = 20

	prefix    = strings.TrimPrefix(prefix, "./")
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
		base := d.Name()
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch base {
			case "vendor", "node_modules", "dist", "build", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		rel := path
		if strings.HasPrefix(rel, "./") {
			rel = rel[2:]
		}
		if strings.HasPrefix(rel, prefix) {
			results = append(results, rel)
		}
		return nil
	})

	sort.Strings(results)
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

	m.streamCh     = make(chan tea.Msg, 100)
	m.streaming    = true
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
					tokIn  = len(content) / 4
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

	// Shell passthrough
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

	// Expand @file references before routing
	line = m.expandFileRefs(line)

	if strings.HasPrefix(line, "/") {
		return m.handleCommand(line)
	}

	// Echo user turn with role label on first line only
	m.push(roleUser, line)

	// Possibly auto-switch mode
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
		m.handleInvestigateInput(content)
		return nil
	case modes.ModeReview:
		m.handleReviewInput(content)
		return nil
	default:
		m.responseBuffer.Reset()
		return m.streamCmd(content)
	}
}

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

	// Core mode switches with optional inline content
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

func (m *model) expandFileRefs(line string) string {
	fields  := strings.Fields(line)
	changed := false
	for i, field := range fields {
		if strings.HasPrefix(field, "@") {
			ref := field[1:]
			if ref == "" {
				continue
			}
			if _, err := os.Stat(ref); err == nil {
				fields[i] = ref
				changed    = true
				continue
			}
			matches, err := filepath.Glob(ref)
			if err == nil && len(matches) > 0 {
				fields[i] = matches[0]
				changed    = true
			}
		}
	}
	if changed {
		return strings.Join(fields, " ")
	}
	return line
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Agent handlers ────────────────────────────────────────────────────────────

func (m *model) handleInvestigateInput(line string) {
	m.agentRunning = true
	m.agentDone    = false
	m.agentLabel   = "investigating"
	m.spinnerFrame = 0

	eng    := investigate.NewEngine(".", line, nil, nil)
	result, err := eng.Run()
	if err != nil {
		m.agentRunning = false
		m.push(roleError, "investigation error: "+err.Error())
		return
	}

	m.agentRunning = false
	m.agentDone    = true

	m.push(roleAI, investigationStyle.Render(fmt.Sprintf("problem: %s", result.Problem)))
	m.push(roleAI, investigationStyle.Render(fmt.Sprintf(
		"duration: %s · loops: %d · hypotheses: %d · evidence: %d",
		result.Duration, result.Loops, len(result.Hypotheses), len(result.Evidence))))

	if result.Resolved {
		m.push(roleStatus, hypothesisStyle.Render("✓ "+result.Conclusion))
	} else {
		m.push(roleSystem, infoStyle.Render("investigation inconclusive"))
	}

	for _, h := range result.Hypotheses {
		sym := "○"
		switch h.Status {
		case investigate.HypothesisConfirmed:
			sym = "✓"
		case investigate.HypothesisRejected:
			sym = "✗"
		}
		m.push(roleAI, hypothesisStyle.Render(
			fmt.Sprintf("  %s %s [%s] (%.0f%%)", sym, h.Theory, h.Status, h.Confidence*100)))
	}

	for _, ev := range result.Evidence {
		content := ev.Content
		if len(content) > 60 {
			content = content[:60] + "…"
		}
		m.push(roleAI, evidenceStyle.Render(fmt.Sprintf("  [%s] %s", ev.Source, content)))
	}

	if !result.Resolved && result.Error != "" {
		m.push(roleError, "error: "+result.Error)
	}

	m.sess.SetInvestigationID(result.Problem)
	os.MkdirAll(filepath.Join(".izen", "investigations"), 0755)
}

func (m *model) handleReviewInput(_ string) {
	m.agentRunning = true
	m.agentDone    = false
	m.agentLabel   = "reviewing"
	m.spinnerFrame = 0

	eng    := review.NewEngine(".", nil, nil)
	result, err := eng.Run()
	if err != nil {
		m.agentRunning = false
		m.push(roleError, "review error: "+err.Error())
		return
	}
	if result.Error != "" {
		m.agentRunning = false
		m.push(roleSystem, infoStyle.Render(result.Error))
		return
	}

	m.agentRunning = false
	m.agentDone    = true

	m.push(roleAI, reviewStyle.Render(fmt.Sprintf("review: %s → %s", result.BaseBranch, result.Branch)))
	m.push(roleAI, reviewStyle.Render(fmt.Sprintf(
		"commit: %s · files: %d · duration: %s",
		result.CommitHash, len(result.FilesChanged), result.Duration)))

	scoreColor := scoreStyle
	if result.Score < 50 {
		scoreColor = errorStyle
	} else if result.Score < 75 {
		scoreColor = riskHighStyle
	}
	m.push(roleAI, scoreColor.Render(fmt.Sprintf("score: %d/100  risk: %d/100", result.Score, result.ImpactRadius.RiskScore)))

	if len(result.FilesChanged) > 0 {
		m.push(roleAI, infoStyle.Render("changed files:"))
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
			m.push(roleAI, infoStyle.Render(fmt.Sprintf("  %s %s (+%d/-%d)", sym, f.Path, f.Additions, f.Deletions)))
		}
	}

	if len(result.ImpactRadius.IndirectFiles) > 0 {
		m.push(roleAI, riskMediumStyle.Render(fmt.Sprintf(
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
		m.push(roleAI, style.Render(fmt.Sprintf("  [%s] %d findings", strings.ToUpper(string(sev)), len(findings))))
		for _, f := range findings {
			m.push(roleAI, style.Render(fmt.Sprintf("    %s:%d — %s", f.File, f.Line, f.Description)))
		}
	}

	if len(result.Recommendations) > 0 {
		m.push(roleAI, reviewStyle.Render("recommendations:"))
		for i, rec := range result.Recommendations {
			m.push(roleAI, infoStyle.Render(fmt.Sprintf("  %d. %s", i+1, rec)))
		}
	}

	m.sess.SetReviewID(result.Branch + "@" + result.CommitHash)
	review.SaveReport(result, ".")
}
