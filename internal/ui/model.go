package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

// ── Message types ─────────────────────────────────────────────────────────────

type role uint8

const (
	roleSystem role = iota
	roleUser
	roleAI
	roleError
	roleCode
	roleStatus
)

type UIState uint8

const (
	StateChat UIState = iota
	StateAwaitingApproval
)

type record struct {
	role role
	text string
}

type patchProposal struct {
	File    string
	Content string
	ID      string
}

type tokenMsg string

type streamDoneMsg struct {
	content     string
	tokenInput  int
	tokenOutput int
}

type streamErrMsg struct{ err error }

type tickMsg time.Time
type animTickMsg time.Time

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

type commitGeneratedMsg struct {
	subject string
	body    string
	hash    string
	err     error
}

type buildProposalsReadyMsg struct {
	proposals []patchProposal
}

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	version                   = "0.1.0"
	maxInvestigateInvocations = 5

	// Fixed heights of chrome elements (lines)
	focusLineHeight = 1 // top colored rule
	promptBoxHeight = 3 // border top + content + border bottom
	statusBarHeight = 1
	viewportPadding = 1 // breathing room
)

var coreModes = []string{"/ask", "/plan", "/build", "/investigate", "/review"}

var utilityCommands = map[modes.Mode][]string{
	modes.ModeAsk:         {"/clear"},
	modes.ModePlan:        {"/clear"},
	modes.ModeBuild:       {"/undo", "/commit", "/checkpoint", "/clear"},
	modes.ModeInvestigate: {"/clear"},
	modes.ModeReview:      {"/clear"},
}

var globalCommands = []string{"/help", "/?", "/mode", "/objective", "/drop", "/quit"}

// ── Elegant spinner frames ────────────────────────────────────────────────────
var spinnerFrames = []string{" ⊹ ", " ⁕ ", " ⚙ ", " ❃ ", " ❄ ", " ❆ ", " ❃ ", " ⚙ ", " ⁕ ", " ⊹ "}

// ── Model ─────────────────────────────────────────────────────────────────────

type model struct {
	cfg      *config.Config
	sess     *session.Session
	provider ai.Provider
	resolver *modes.Resolver
	gitEng   *git.Engine
	graphEng *graph.Engine
	graph    *graph.Graph

	// Input
	ti    textinput.Model
	input strings.Builder // kept in sync with ti for suggestions.go

	// Viewport for scrollable conversation history
	vp        viewport.Model
	vpReady   bool
	viewLines []string // rendered lines fed into viewport

	// Banner visibility state
	showBanner bool

	// Window dimensions
	width  int
	height int

	// Streaming
	streamCh       chan tea.Msg
	responseBuffer strings.Builder
	streaming      bool
	spinnerFrame   int
	tokenInput     int
	tokenOutput    int

	// Agent state
	agentRunning bool
	agentLabel   string
	agentDone    bool

	// Suggestions
	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int

	// File context
	pendingFileRefs []string
	attachedFiles   []string

	// Proposals / approvals
	awaitingConfirmation bool
	pendingProposals     []patchProposal
	acceptAll            bool

	state UIState

	execEng     *execution.Engine
	buildOutput strings.Builder

	investigateInvocationCount int

	// Command history
	history      []string
	historyIndex int
	historyPath  string

	// Escape key press counter for quit
	escPressCount int

	// Mode-line animation
	lineAnimProgress   float64
	lineAnimTargetMode modes.Mode
	lineAnimating      bool

	// Records (source of truth; rendered into viewLines → viewport)
	records []record

	// Copy
	mouseSelecting  bool
	startMouseCol   int
	startMouseRow   int
	currentMouseCol int
	currentMouseRow int
}

// ── Viewport helpers ──────────────────────────────────────────────────────────

// viewportHeight calculates available lines for the conversation viewport.
func (m *model) viewportHeight() int {
	// Base heights: Focus line (1) + Prompt Box (3) + Runtime Status (1) + Footer (1)
	baseHeight := 1 + 3 + 1 + 1

	// Add dynamic heights
	widgetH := m.activeWidgetHeight()

	h := m.height - baseHeight - widgetH - viewportPadding
	if h < 3 {
		h = 3
	}
	return h
}

func (m *model) activeWidgetHeight() int {
	widget := m.renderActiveWidget(m.width)
	if widget == "" {
		return 0
	}
	return len(strings.Split(widget, "\n"))
}

// wrapStreamText wraps raw text lines dynamically during an active live stream.
func wrapStreamText(text string, maxW int) []string {
	if len(text) == 0 {
		return []string{""}
	}
	var chunks []string
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		words := strings.Fields(line)
		if len(words) == 0 {
			chunks = append(chunks, "")
			continue
		}

		var currentLine strings.Builder
		for _, word := range words {
			if currentLine.Len()+1+len(word) > maxW {
				chunks = append(chunks, currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			} else {
				if currentLine.Len() > 0 {
					currentLine.WriteString(" ")
				}
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			chunks = append(chunks, currentLine.String())
		}
	}
	return chunks
}

// rebuildViewport re-renders all records into the viewport content.
func (m *model) rebuildViewport() {
	if !m.vpReady {
		return
	}

	// Sync viewport height dynamically
	m.vp.Height = m.viewportHeight()

	followBottom := m.vp.AtBottom()
	var lines []string
	if m.showBanner {
		for _, l := range strings.Split(m.renderStartupBanner(m.width), "\n") {
			lines = append(lines, l)
		}
		lines = append(lines, "")
	}
	for _, rec := range m.records {
		lines = append(lines, m.printRecord(rec))
	}

	// Live streaming: wrap incoming chunk buffer on-the-fly to enforce terminal boundary safety
	if m.streaming && m.responseBuffer.Len() > 0 {
		gutter := gutterAIStyle.Render("▌") + " "
		availableWidth := m.width - 2
		if availableWidth < 20 {
			availableWidth = 20
		}

		wrappedStream := wrapStreamText(m.responseBuffer.String(), availableWidth)
		for _, l := range wrappedStream {
			lines = append(lines, gutter+l)
		}
	} else if m.streaming {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
		lines = append(lines, gutterAIStyle.Render("▌")+" "+sp+"  "+infoStyle.Render("thinking…"))
	}

	m.viewLines = lines
	m.vp.SetContent(strings.Join(lines, "\n"))

	if followBottom {
		m.vp.GotoBottom()
	}
}

// appendViewLine appends a rendered line and synchronizes viewport tracking.
func (m *model) appendViewLine(line string) {
	visualLines := strings.Split(line, "\n")
	m.viewLines = append(m.viewLines, visualLines...)
	m.vp.SetContent(strings.Join(m.viewLines, "\n"))
	m.vp.GotoBottom()
}

// ── Record helpers ─────────────────────────────────────────────────────────────

func (m *model) push(r role, text string) {
	// Preserve complete original string to let printRecord's advanced layout algorithms resolve wrapping
	rec := record{role: r, text: text}
	m.records = append(m.records, rec)

	if m.vpReady {
		m.rebuildViewport()
	}
}

func (m *model) pushLines(r role, lines []string) {
	m.push(r, strings.Join(lines, "\n"))
}

func (m *model) pushRecords(recs []record) {
	for _, rec := range recs {
		m.records = append(m.records, rec)
	}
	if m.vpReady {
		m.rebuildViewport()
	}
}

// ── History persistence ───────────────────────────────────────────────────────

func (m *model) historyFilePath() string {
	if m.historyPath != "" {
		return m.historyPath
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		cfgDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfgDir, "izen", "history")
}

func (m *model) loadHistory() {
	f, err := os.Open(m.historyFilePath())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			m.history = append(m.history, s)
		}
	}
}

func (m *model) saveHistory() {
	path := m.historyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(m.history) == 0 {
		return
	}
	last := m.history[len(m.history)-1]
	b, _ := json.Marshal(last)
	fmt.Fprintf(f, "%s\n", b)
}

func (m *model) inputString() string { return m.ti.Value() }
