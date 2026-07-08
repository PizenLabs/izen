package ui

import (
	"bufio"
	"context"
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
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// ── Init stage types ──────────────────────────────────────────────────────────

type initStage int

const (
	initNone initStage = iota
	initConfirm
	initIdentity
	initProviderSelect
	initComplete
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
	StateProcessing
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

type traceUpdateMsg struct {
	trace *ctxpkg.CodebaseTrace
}

type streamErrMsg struct{ err error }

type tickMsg time.Time

type smoothStreamTickMsg time.Time

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
type agentDoneMsg struct{}

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

type archDoneMsg struct {
	Content string
}

type mutationResultMsg struct {
	err    error
	file   string
	status string
}

type applyAllResultMsg struct {
	results []mutationResultMsg
}

type shellOutputMsg struct {
	lines []string
}

var _ tea.Msg = shellOutputMsg{}

type graphBuiltMsg struct {
	graph *graph.Graph
	err   error
}

func buildGraphCmd(eng *graph.Engine) tea.Cmd {
	return func() tea.Msg {
		g, _, err := eng.BuildOrLoad()
		if err != nil {
			return graphBuiltMsg{err: err}
		}
		return graphBuiltMsg{graph: g}
	}
}

type testResultMsg struct {
	output string
	passed bool
	failed int
	total  int
	err    error
}

type fixResultMsg struct {
	content string
	err     error
}

type envResultMsg struct {
	content string
	err     error
}

type traceResultMsg struct {
	output string
	target string
	passed bool
	failed int
	total  int
	err    error
}

type diagnoseResultMsg struct {
	content string
	err     error
}

// ── Handoff Context ───────────────────────────────────────────────────────────

// HandoffContext carries state across mode boundaries for the smart handoff
// pipeline. Every terminal state primes the context for the next mode.
type HandoffContext struct {
	LastFailureLog string   // Compile errors, test stack traces, or panic traces
	ProposedFix    string   // Populated by investigate/plan (markdown/diff format)
	TargetScope    string   // Target directory or file currently in focus
	PendingTodos   []string // TODO strings passed down to /mode plan
}

// actionChip represents a selectable action rendered at the bottom boundary
// of the TUI. Hotkey activation executes the associated command.
type actionChip struct {
	key    string // Hotkey letter (A, B, C, etc.)
	label  string // Display label (e.g. "Investigate Root Cause")
	action string // Command to execute (e.g. "/mode investigate")
	query  string // Optional seed content passed with the command
}

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	version                   = "0.1.0"
	maxInvestigateInvocations = 20

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
var flowingSpinnerFrames = []string{" ⊹ ", " ⁕ ", " ⚙ ", " ❃ ", " ❄ ", " ❆ ", " ❃ ", " ⚙ ", " ⁕ ", " ⊹ "}

// providerSwitchMsg signals a successful provider switch.
type providerSwitchMsg struct {
	name string
}

// ── Model ─────────────────────────────────────────────────────────────────────

type model struct {
	cfg      *config.Config
	sess     *session.Session
	provider ai.Provider
	mgr      *ai.Manager
	resolver *modes.Resolver
	gitEng   *git.Engine
	graphEng *graph.Engine
	graph    *graph.Graph

	// Input
	ti    textinput.Model
	input strings.Builder // kept in sync with ti for suggestions.go

	// Banner visibility state
	showBanner bool

	// Window dimensions
	width  int
	height int

	// Viewport for scrollable chat history
	Viewport           viewport.Model
	Ready              bool
	PreRenderedHistory string

	// Streaming
	streamCh             chan tea.Msg
	responseBuffer       strings.Builder
	streaming            bool
	spinnerFrame         int
	currentStreamContent string // accumulated raw text during active LLM stream

	// Expanded metrics for status bar
	IsCloudModel    bool
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ContextLimit    int
	AccumulatedCost float64
	CheckpointID    string

	streamParser     *IncrementalStreamParser
	streamBuffer     string // buffered tokens for smooth tick emission
	streamTickActive bool   // whether smooth-stream tick is active
	userName         string // dynamic system username (set at init)

	// Agent state
	agentRunning bool
	agentLabel   string
	agentDone    bool

	// Suggestions
	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int

	// Autocomplete (Prompt Sandwich dropdown)
	autocompleteActive bool
	autocompleteType   string   // "file" or "command"
	autocompleteItems  []string // filtered matching items
	autocompleteIdx    int      // currently highlighted index

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

	// Focus objective UI notifications (non-chat)
	uiNotice string

	// Proposal widget diff scroll offset
	proposalDiffOffset int

	// AST/Code Graph trace for rendering the AI's thought route
	currentTrace *ctxpkg.CodebaseTrace

	// Tip of the Day
	currentTip string

	// Help overlay toggle
	showHelpOverlay bool

	// Last apply error for the red error bar
	lastApplyError string
	applyErrorTime time.Time

	// Latency telemetry: marked when a turn is submitted, read back when the
	// stream completes to compute this-turn latency for the status line.
	streamStartTime time.Time

	// AI Interrupt Engine: cancel function for active stream, set by streamCmd.
	streamCancel       context.CancelFunc
	interruptRequested bool

	// Viewport scroll tracking: when the user scrolls up to inspect code,
	// auto-scroll to bottom is suppressed until SPACE or a new message.
	userIsScrollingUp bool

	// Test/run output storage for /fix consumption
	lastTestOutput string
	lastTestFailed bool
	lastTestTarget string

	// Safety gate confirmation state
	pendingTestConfirm bool
	pendingTestTarget  string

	// Review action spinner: set synchronously on $run/$test/$fix dispatch
	// so the view can immediately render a spinner without waiting for the
	// async agentStartMsg to be processed.
	reviewRunning bool

	// Safety valve: timestamp of the last review action dispatch. If
	// reviewRunning stays true longer than the timeout threshold, the
	// tick loop force-clears it to prevent ghost spinner lock.
	lastActionTime time.Time

	// Handoff pipeline: inter-mode state transfer
	handoffCtx  HandoffContext
	activeChips []actionChip
	showChips   bool

	// Build verification flag: set after build mutation auto-test
	buildVerifyPending bool

	// Workspace root path for config/session persistence
	workspaceRoot string

	// Init/setup state machine
	initStage          initStage
	initConfirmDone    bool
	initIdentityInput  textinput.Model
	initProviderIdx    int
	initProviderFilter string
	initProviderItems  []string

	// Read-only prefill defaults sourced from the global ~/.izen/config.yml.
	// These seed the interactive onboarding form values so the user can
	// simply press Enter to confirm; they never bypass onboarding.
	initPrefillUsername string
	initPrefillProvider string
}

// ── Rendering helpers ─────────────────────────────────────────────────────────

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

// setApplyError captures an apply error.
func (m *model) setApplyError(text string) {
	m.lastApplyError = text
	m.applyErrorTime = time.Now()
	m.push(roleError, text)
}

// ── Record helpers ─────────────────────────────────────────────────────────────

// push appends a record. Records are flushed to the terminal's native
// scrollback at explicit sync points (user submit, stream done, etc.).
func (m *model) push(r role, text string) {
	m.records = append(m.records, record{role: r, text: text})
	m.cacheRecordToHistory(record{role: r, text: text})
}

// cacheRecordToHistory renders a single record and appends it to PreRenderedHistory.
// During active streaming, the cache is frozen to avoid re-highlighting old history.
// Uses the same rendering logic as the original View() inline loop to guarantee
// identical output (user header, AI block rendering, raw styled text).
func (m *model) cacheRecordToHistory(rec record) {
	if m.streaming {
		return
	}
	if m.width == 0 {
		return
	}
	rendered := m.renderRecordForViewport(rec)
	if rendered != "" {
		m.PreRenderedHistory += rendered + "\n"
	}
}

// renderRecordForViewport renders a single record exactly as the original View()
// inline loop did — user records get the @username header with right-padding,
// AI records go through renderAIResponseBlocks, and everything else is raw text.
func (m *model) renderRecordForViewport(rec record) string {
	width := m.width
	if width < 40 {
		width = 40
	}

	switch rec.role {
	case roleUser:
		userHeader := dimmedStyle.Render("@" + m.userName + "  ")
		paddedText := " " + rec.text
		padNeeded := width - lipgloss.Width(userHeader) - lipgloss.Width(paddedText) - 1
		if padNeeded > 0 {
			paddedText += strings.Repeat(" ", padNeeded)
		}
		return userHeader + userBgStyle.Render(paddedText)
	case roleAI:
		return m.renderAIResponseBlocks(rec.text, width)
	default:
		return rec.text
	}
}

// pushRecords appends multiple records.
func (m *model) pushRecords(recs []record) {
	m.records = append(m.records, recs...)
	for _, rec := range recs {
		m.cacheRecordToHistory(rec)
	}
}

// flushRecord returns a tea.Cmd that renders and flushes a record
// via tea.Println into the terminal's native scrollback history.
func (m *model) flushRecord(rec record) tea.Cmd {
	rendered := strings.TrimRight(m.printRecord(rec), "\n")
	if rendered == "" {
		return nil
	}
	return tea.Println(rendered)
}

// flushPendingRecords returns a batch cmd that flushes all records.
func (m *model) flushPendingRecords() tea.Cmd {
	if len(m.records) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, rec := range m.records {
		cmds = append(cmds, m.flushRecord(rec))
	}
	return tea.Batch(cmds...)
}

var spinnerBaseStyle = lipgloss.NewStyle()

// latestCheckpointID returns the most recent checkpoint ID from the session.
func (m *model) latestCheckpointID() string {
	if len(m.sess.Checkpoints) == 0 {
		return ""
	}
	return m.sess.Checkpoints[len(m.sess.Checkpoints)-1]
}

// refreshViewportContent rebuilds the viewport's internal content from
// PreRenderedHistory (cached) plus any active streaming content.
// During streaming the PreRenderedHistory cache is never rebuilt,
// which avoids re-highlighting or re-wrapping old history on every tick.
func (m *model) refreshViewportContent() {
	if !m.Ready {
		return
	}

	var content strings.Builder

	if m.showBanner && len(m.records) == 0 {
		content.WriteString(m.renderStartupBanner(m.width))
		content.WriteString("\n")
	}

	if m.PreRenderedHistory != "" {
		content.WriteString(m.PreRenderedHistory)
	}

	if m.streaming {
		if m.currentStreamContent != "" {
			rendered := m.renderStreamingContent(m.currentStreamContent, m.width)
			if rendered != "" {
				content.WriteString(rendered)
				content.WriteString("\n")
			}
		}
		sp := m.renderFlowingSpinner()
		status := "streaming…"
		if m.agentRunning {
			status = m.agentLabel
		}
		content.WriteString(sp + " " + infoStyle.Render(status) + "\n")
	} else if m.reviewRunning || m.agentRunning {
		frame := ProposalSpinnerFrames[m.spinnerFrame%len(ProposalSpinnerFrames)]
		content.WriteString(SpinnerStyle.Render(frame) + " " + infoStyle.Render(m.agentLabel) + "\n")
	}

	m.Viewport.SetContent(content.String())
}

// countRenderedDiffLines returns how many lines DiffRenderer would output
// for the given raw diff string, excluding pure metadata (---/+++).
func countRenderedDiffLines(diff string) int {
	if diff == "" {
		return 0
	}
	lines := strings.Split(diff, "\n")
	n := 0
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+++") {
			continue
		}
		n++
	}
	return n
}

// getProposalDockCurrentHeight returns the exact line count of the rendered
// proposal dock block (renderProposalBlock), computed dynamically from the
// actual diff content so the viewport can reclaim every spare line.
//
//	StateProcessing:        3 lines (top divider + spinner + bottom divider)
//	StateAwaitingApproval:
//	  Collapsed:            9 lines (top divider + 7 card lines + bottom divider)
//	  Expanded:             1 + card(4 + cappedDiff + blank + scrollHint + action + blank + border) + 1
func (m *model) getProposalDockCurrentHeight() int {
	switch m.state {
	case StateProcessing:
		return 3
	case StateAwaitingApproval:
		if len(m.pendingProposals) == 0 {
			return 0
		}
		p := m.pendingProposals[0]
		if !p.Expanded {
			return 9 // 1 (top divider) + 7 (collapsed MutationRenderer) + 1 (bottom divider)
		}
		n := countRenderedDiffLines(p.Diff)
		capped := n
		if capped > maxProposalDiffHeight {
			capped = maxProposalDiffHeight
		}
		scrollHint := 0
		if n > maxProposalDiffHeight || m.proposalDiffOffset > 0 {
			scrollHint = 1
		}
		// Expanded MutationRenderer: border + header + metadata + blank = 4
		//                          + capped diff lines
		//                          + blank after diff = 1
		//                          + scroll hint (if needed) = 0/1
		//                          + action line = 1
		//                          + blank = 1
		//                          + border = 1
		cardLines := 4 + capped + 1 + scrollHint + 1 + 1 + 1
		return 1 + cardLines + 1
	}
	return 0
}

// getAutocompleteHeight returns the exact number of terminal lines the
// autocomplete dropdown occupies when rendered. This must be subtracted from
// the viewport height to prevent the input line from being pushed upward.
func (m *model) getAutocompleteHeight() int {
	if !m.autocompleteActive || len(m.autocompleteItems) == 0 {
		return 0
	}
	maxShow := 8
	n := len(m.autocompleteItems)
	if n > maxShow {
		n = maxShow
	}
	return n + 2 // items + top border + bottom border
}

// computeVpHeight returns the number of terminal rows available for the
// scrollable viewport. Matches the View() JoinVertical layout — zero gaps:
//
//	Status line (renderRuntimeStatus)             → 1 line
//	Separator below input + input + top separator → 3 lines
//	Autocomplete dropdown (inputView)              → dynamic (getAutocompleteHeight)
//	Proposal dock (renderProposalBlock)            → dynamic (getProposalDockCurrentHeight)
//	m.Viewport.View()                             → remaining height (vpHeight)
//
// The viewport always sits at the top, consuming 100% of remaining space.
// The input line and status bar are rigidly pinned to the terminal bottom edge
// with zero floating margin between them.
func (m *model) computeVpHeight() int {
	const inputHeight = 3 // separator above + input line + separator below
	const statusLineHeight = 1

	vpHeight := m.height - inputHeight - statusLineHeight
	vpHeight -= m.getAutocompleteHeight()
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		vpHeight -= m.getProposalDockCurrentHeight()
	}
	if m.showChips && len(m.activeChips) > 0 {
		vpHeight -= len(m.activeChips)
	}
	if vpHeight < 1 {
		return 1
	}
	return vpHeight
}

// recalcViewportHeight recomputes and applies the viewport height when the
// proposal dock visibility changes (state transitions) so the layout always
// fits the terminal without overflow.
func (m *model) recalcViewportHeight() {
	if !m.Ready {
		return
	}
	m.Viewport.Height = m.computeVpHeight()
}

// renderFlowingSpinner renders a single animated character with a smooth flowing
// light effect: the color oscillates between dim and bright using a sine wave,
// creating the feeling of seamless movement.
func (m *model) renderFlowingSpinner() string {
	n := len(flowingSpinnerFrames)
	idx := m.spinnerFrame % n
	frameStr := flowingSpinnerFrames[idx]

	phase := float64(m.spinnerFrame) * (2 * math.Pi / float64(n))
	t := (math.Sin(phase) + 1) / 2
	t = t * t * (3 - 2*t)

	from := lipgloss.Color(colorSubtle)
	to := lipgloss.Color(colorText)
	color := interpolateColor(from, to, t)

	return spinnerBaseStyle.Foreground(color).Render(frameStr)
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
