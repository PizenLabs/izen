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

// ── Implicit Pipeline Messages ─────────────────────────────────────────────────

// logInputMsg is the payload for the $log sub-command. Carries a shell
// execution trace output for silent investigate→plan→build routing.
type logInputMsg struct {
	output string // raw shell/execution output
	err    error
}

// investigateCompleteMsg signals that the silent investigation step has
// completed and produced a structured analysis payload for the plan step.
type investigateCompleteMsg struct {
	analysis string // parsed stack trace and diagnostic context
	ledgerID string // the #number assigned by the ContextLedger
	err      error
}

// blueprintReadyMsg signals that the plan step has completed and the
// code patch blueprint is ready for explicit build execution.
type blueprintReadyMsg struct {
	blueprint string // assembled markdown diff/patch blueprint
	ledgerID  string // the #number assigned by the ContextLedger
	err       error
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

// buildResultMsg is the result from a $run build execution.
// Separated from testResultMsg so its feedback renders a clean
// system metric block instead of the test component's template.
type buildResultMsg struct {
	output   string
	exitCode int
	err      error
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

// ── Context Ledger ─────────────────────────────────────────────────────────────

// IssueScope tracks a single failure context with a numeric ID and optional
// child sub-scopes for overlapping crash signatures.
type IssueScope struct {
	ID         int      `json:"id"`
	Suffix     string   `json:"suffix,omitempty"` // e.g. "sub" for overlapping crashes
	Files      []string `json:"files,omitempty"`  // files referenced in the crash
	StackTrace string   `json:"stack_trace,omitempty"`
	Label      string   `json:"label,omitempty"` // human-readable label
}

// ActiveID returns the formatted ledger key (e.g. "#101" or "#101-sub").
func (s *IssueScope) ActiveID() string {
	if s.Suffix != "" {
		return fmt.Sprintf("#%d-%s", s.ID, s.Suffix)
	}
	return fmt.Sprintf("#%d", s.ID)
}

// ContextLedger maintains silent issue tracking across failure sessions
// without forcing UI view state mutations. It maps #number IDs to failure
// scopes and handles suffix-based sub-scoping.
type ContextLedger struct {
	ActiveID     int                    `json:"active_id"`
	Counter      int                    `json:"counter"`
	Entries      map[string]*IssueScope `json:"entries"`
	lastFiles    []string               // files from the most recent crash
	lastStackSig string                 // fingerprint of the last stack trace
}

// NewContextLedger creates a fresh ledger starting at #100.
func NewContextLedger() *ContextLedger {
	return &ContextLedger{
		ActiveID: 100,
		Counter:  100,
		Entries:  make(map[string]*IssueScope),
	}
}

// Record registers a crash signature. If the files/stack overlap with the
// previously recorded crash, a child sub-scope is used. Otherwise a new
// root issue is minted. Returns the assigned ledger key.
func (cl *ContextLedger) Record(files []string, stackTrace string) string {
	stackSig := stackTraceFingerprint(stackTrace)

	overlap := cl.filesOverlap(files)
	sameStack := cl.lastStackSig != "" && cl.lastStackSig == stackSig

	var scope *IssueScope
	if overlap || sameStack {
		if entry, ok := cl.Entries[fmt.Sprintf("#%d", cl.ActiveID)]; ok && entry != nil {
			scope = &IssueScope{
				ID:         cl.ActiveID,
				Suffix:     "sub",
				Files:      files,
				StackTrace: stackTrace,
			}
			cl.Entries[scope.ActiveID()] = scope
			cl.lastFiles = files
			cl.lastStackSig = stackSig
			return scope.ActiveID()
		}
	}
	cl.Counter++
	cl.ActiveID = cl.Counter
	scope = &IssueScope{
		ID:         cl.ActiveID,
		Files:      files,
		StackTrace: stackTrace,
	}
	cl.Entries[scope.ActiveID()] = scope

	cl.lastFiles = files
	cl.lastStackSig = stackSig
	return scope.ActiveID()
}

// filesOverlap checks whether the new crash touches files from the last one.
func (cl *ContextLedger) filesOverlap(files []string) bool {
	lastSet := make(map[string]struct{}, len(cl.lastFiles))
	for _, f := range cl.lastFiles {
		lastSet[f] = struct{}{}
	}
	for _, f := range files {
		if _, ok := lastSet[f]; ok {
			return true
		}
	}
	return false
}

// ResetForNewRoot clears sub-contexts and prepares for a new root scope.
func (cl *ContextLedger) ResetForNewRoot() {
	for key, scope := range cl.Entries {
		if scope.Suffix != "" {
			delete(cl.Entries, key)
		}
	}
	cl.lastFiles = nil
	cl.lastStackSig = ""
}

// stashLedgerData returns the current ledger entries for memo during
// stale agent op cancellation.
func (cl *ContextLedger) stashLedgerData() *ContextLedger {
	if cl == nil {
		return nil
	}
	cpy := &ContextLedger{
		ActiveID:     cl.ActiveID,
		Counter:      cl.Counter,
		Entries:      make(map[string]*IssueScope, len(cl.Entries)),
		lastFiles:    cl.lastFiles,
		lastStackSig: cl.lastStackSig,
	}
	for k, v := range cl.Entries {
		scope := *v
		cpy.Entries[k] = &scope
	}
	return cpy
}

// restoreLedgerData restores the ledger from a stashed copy.
func (cl *ContextLedger) restoreLedgerData(stashed *ContextLedger) {
	if stashed == nil {
		return
	}
	cl.ActiveID = stashed.ActiveID
	cl.Counter = stashed.Counter
	cl.Entries = stashed.Entries
	cl.lastFiles = stashed.lastFiles
	cl.lastStackSig = stashed.lastStackSig
}

// stackTraceFingerprint creates a simple hash of the stack trace for comparison.
func stackTraceFingerprint(trace string) string {
	lines := strings.SplitN(trace, "\n", 8)
	if len(lines) > 6 {
		lines = lines[:6]
	}
	var key strings.Builder
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			key.WriteString(l)
			key.WriteString("|")
		}
	}
	return key.String()
}

// ── Handoff Context ───────────────────────────────────────────────────────────

// HandoffContext carries state across mode boundaries for the smart handoff
// pipeline. Every terminal state primes the context for the next mode.
type HandoffContext struct {
	LastFailurePayload string   // Compile errors, test stack traces, or panic traces
	ProposedFix        string   // Populated by investigate/plan (markdown/diff format)
	TargetScope        string   // Target directory or file currently in focus
	PendingTodos       []string // TODO strings passed down to /mode plan
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

var flowingSpinnerFrames = []string{" ✦ ", " ★ ", " ⚙ ", " ❋ ", " ❄ ", " ❆ ", " ❋ ", " ⚙ ", " ★ ", " ✦ "}

// providerSwitchMsg signals a successful provider switch.
type providerSwitchMsg struct {
	name string
}

// TaskFinishedMsg is a forced-termination signal that systematically unblocks
// the input state by clearing all agent/stream/review flags. Dispatched via
// defer at the end of every blocking execution command ($trace, $env, $test,
// $run, $diagnose, $log) to guarantee cleanup even on panic or hang.
// Also dispatched by the Ctrl+C hard-override handler.
type TaskFinishedMsg struct{}

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

	// Handoff pipeline: inter-mode state transfer (WORKFLOW STATE).
	// This survives mode transitions and must never be cleared to hide UI.
	handoffCtx HandoffContext

	// currentResult is the most recent workflow RESULT and the capabilities it
	// exposes. It is DOMAIN state (the engine's current outcome) — NOT a UI
	// flag. The renderer never reads it directly; it flows through
	// BuildViewContext into ViewContext.Actions. It is cleared when a new
	// workflow begins (mode entry / clear / new task), which bounds capability
	// staleness to the current view without any presentation state mirroring
	// the engine.
	currentResult *Result

	// Build verification flag: set after build mutation auto-test (in-flight
	// workflow signal, not a render flag).
	buildVerifyPending bool

	// Context Ledger: silent issue tracking across failure sessions
	ledger *ContextLedger

	// Implicit pipeline state: prevents UI view bouncing during silent
	// investigate→plan→build flow.
	pipelineRunning bool

	// Stashed ledger data for preservation during cancelStaleAgentOps
	ledgerStash *ContextLedger

	// Label for the active pipeline step (used for spinner display only)
	pipelineStep string

	// Workspace root path for config/session persistence
	workspaceRoot string

	// viewRegistry resolves the current mode to its ViewMode builder. It is
	// injected at bootstrap (explicit, deterministic) and never mutated by
	// the renderer — the UI stays mode-agnostic.
	viewRegistry *Registry

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

// sanitizeInputPrompt forces the text input prompt back to a clean baseline,
// ensuring no orphaned spinner sequences or dynamic artifacts remain embedded
// after any background task termination ($fix, $run, $log, $test, /commit).
// Called defensively by every task-termination message handler in update.go.
func (m *model) sanitizeInputPrompt() {
	m.ti.Prompt = ""
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

	ctxHeader := m.renderContextHeader()
	if ctxHeader != "" {
		content.WriteString(ctxHeader)
	}

	if !m.showBanner || len(m.records) > 0 {
		content.WriteString(m.renderWorkspaceHeader())
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
		return 1
	case StateAwaitingApproval:
		if len(m.pendingProposals) == 0 {
			return 0
		}
		p := m.pendingProposals[0]
		if !p.Expanded || p.Diff == "" {
			return 6
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
		return 7 + capped + scrollHint
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
	// NOTE: capabilities render INLINE on the status bar line (see
	// renderStatusBar), so they occupy the single statusLineHeight row above
	// and never add extra rows.
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

// renderRectSpinner renders a clean braille/rectangular spinner frame.
// Used exclusively in the status bar to maintain layout symmetry — star
// glyphs are reserved for the chat prompt label.
func (m *model) renderRectSpinner() string {
	n := len(ProposalSpinnerFrames)
	idx := m.spinnerFrame % n
	return SpinnerStyle.Render(ProposalSpinnerFrames[idx])
}

func (m *model) renderWorkspaceHeader() string {
	mode := m.resolver.Current()
	modeName := strings.ToUpper(mode.String())

	// Semantic color per mode (Level 1 in visual hierarchy)
	var modeAccentStr string
	switch mode {
	case modes.ModeAsk:
		modeAccentStr = colorModeAsk
	case modes.ModePlan:
		modeAccentStr = colorModePlan
	case modes.ModeBuild:
		modeAccentStr = colorModeBuild
	case modes.ModeInvestigate:
		modeAccentStr = colorModeInvestigate
	case modes.ModeReview:
		modeAccentStr = colorModeReview
	default:
		modeAccentStr = colorMuted
	}
	modeNameStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(modeAccentStr))

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  ")
	b.WriteString(modeNameStyle.Render("● " + modeName))
	b.WriteString("  " + dimmedStyle.Render(mode.Description()))
	b.WriteString("\n\n")
	return b.String()
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
