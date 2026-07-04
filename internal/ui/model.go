package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
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
	StateAwaitingShellExec
)

type record struct {
	role role
	text string
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
	records           []record
	sessionKey        string
	err               error
	escalationContent string // when Resolved=false, pipe investigation data to LLM for analysis
}

type reviewResultMsg struct {
	records      []record
	sessionKey   string
	saveReportFn func()
	err          error
}

type agentStartMsg struct{ label string }
type agentDoneMsg struct{ label string }

type commitGeneratedMsg struct {
	subject string
	body    string
	hash    string
	err     error
}

type objectiveAnalyzedMsg struct {
	objective *domain.Objective
	err       error
}

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	version                   = "0.1.0"
	maxInvestigateInvocations = 20

	// Fixed heights of chrome elements (lines)
	focusLineHeight = 1 // top colored rule
	promptBoxHeight = 3 // border top + content + border bottom
	statusBarHeight = 1
	viewportPadding = 1 // breathing room

	maxProposalDiffHeight = 15 // max visible diff lines in expanded proposal widget
)

var coreModes = []string{"/ask", "/plan", "/build", "/investigate", "/review"}

var utilityCommands = map[modes.Mode][]string{
	modes.ModeAsk:         {"/clear"},
	modes.ModePlan:        {"/clear"},
	modes.ModeBuild:       {"/undo", "/commit", "/checkpoint", "/clear"},
	modes.ModeInvestigate: {"/clear"},
	modes.ModeReview:      {"/clear"},
}

var globalCommands = []string{"/help", "/?", "/mode", "/objective", "/drop", "/quit", "/arch"}

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
	pendingProposals     []SemanticProposal
	acceptAll            bool

	// Accepted proposals (collapsed single-line summaries)
	acceptedProposals []acceptedProposal

	// Shell execution proposals awaiting approval
	pendingShellExec []shellExecBlock
	shellAwaitingIdx int

	state UIState

	execEng   *execution.Engine
	planStore *plan.PlanStore

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

	// Cached prompt text for logging (set on submit, cleared after stream completion)
	currentPrompt string

	// Copy
	mouseSelecting  bool
	startMouseCol   int
	startMouseRow   int
	currentMouseCol int
	currentMouseRow int

	// Focus objective UI notifications (non-chat)
	uiNotice string

	// Proposal widget diff scroll offset
	proposalDiffOffset int
}

// ── Viewport helpers ──────────────────────────────────────────────────────────

// viewportHeight calculates available lines for the conversation viewport.
func (m *model) viewportHeight() int {
	// Base heights: Focus line (1) + Prompt Box (3) + Runtime Status (1) + Footer (1)
	baseHeight := 1 + 3 + 1 + 1

	// Dynamic: top bar (0 or 1), widget, suggestions
	topBarH := 0
	if m.renderTopBar() != "" {
		topBarH = 1
	}
	widgetH := m.activeWidgetHeight()
	suggestionH := m.suggestionPaletteHeight()

	h := m.height - baseHeight - topBarH - widgetH - suggestionH - viewportPadding

	// Safety guard: viewport must never collapse below 5 lines.
	// When a proposal widget is active, this ensures the conversation
	// history remains scrollable even on small terminals.
	if h < 5 {
		h = 5
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

func (m *model) suggestionPaletteHeight() int {
	if !m.showSuggestions || len(m.suggestions) == 0 {
		return 0
	}
	palette := m.renderSuggestions(m.width)
	return len(strings.Split(palette, "\n"))
}

// widgetScreenStartY calculates the screen Y position where the active widget begins.
// Returns -1 if no widget is currently rendered.
func (m *model) widgetScreenStartY() int {
	if m.state != StateAwaitingApproval || len(m.pendingProposals) == 0 {
		return -1
	}

	y := 0
	// Top Bar
	if m.renderTopBar() != "" {
		y++
	}
	// Viewport — use actual rendered line count, not configured Height,
	// because bubbletea's viewport may return fewer lines when content
	// is shorter than the viewport window (it does not pad to Height).
	vpView := m.vp.View()
	y += len(strings.Split(vpView, "\n"))
	// Suggestions
	y += m.suggestionPaletteHeight()
	return y
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
		lines = append(lines, strings.Split(m.renderStartupBanner(m.width), "\n")...)
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
		sp := m.renderFlowingSpinner()
		lines = append(lines, gutterAIStyle.Render("▌")+" "+sp+"  "+infoStyle.Render("thinking…"))
	}

	m.viewLines = lines
	m.vp.SetContent(strings.Join(lines, "\n"))

	if followBottom {
		m.vp.GotoBottom()
	}
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

func (m *model) pushRecords(recs []record) {
	m.records = append(m.records, recs...)
	if m.vpReady {
		m.rebuildViewport()
	}
}

// renderFlowingSpinner renders a single animated character with a smooth flowing
// light effect: the color oscillates between dim and bright using a sine wave,
// creating the feeling of seamless movement.
func (m *model) renderFlowingSpinner() string {
	n := len(spinnerFrames)
	idx := m.spinnerFrame % n
	frameStr := spinnerFrames[idx]

	phase := float64(m.spinnerFrame) * (2 * math.Pi / float64(n))
	t := (math.Sin(phase) + 1) / 2
	t = t * t * (3 - 2*t) // smoothstep for butter-smooth oscillation

	from := lipgloss.Color(colorSubtle)
	to := lipgloss.Color(colorText)
	color := interpolateColor(from, to, t)

	return lipgloss.NewStyle().Foreground(color).Render(frameStr)
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
	defer func() { _ = f.Close() }()
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
	defer func() { _ = f.Close() }()
	if len(m.history) == 0 {
		return
	}
	last := m.history[len(m.history)-1]
	b, _ := json.Marshal(last)
	_, _ = fmt.Fprintf(f, "%s\n", b)
}
