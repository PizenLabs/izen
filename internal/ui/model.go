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
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/state"
)

// ── Init stage types ──────────────────────────────────────────────────────────

type initStage int

const (
	initNone initStage = iota
	initGitCheck
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
	roleActivity
)

type UIState uint8

const (
	StateChat UIState = iota
	StateAwaitingApproval
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

type PlanStreamingFinishedMsg struct {
	Success bool
}

type gitInitResultMsg struct{ err error }

type tickMsg time.Time

type smoothStreamTickMsg time.Time

// planSlowNoticeMsg fires once, planSlowNoticeDelay after /plan synthesis
// starts. If synthesis is still pending when it arrives, a viewport-safe
// warning is surfaced (never a raw terminal print) so the user learns the
// local model may be unresponsive before the 120s hard timeout.
type planSlowNoticeMsg struct{ startedAt time.Time }

// planSlowNoticeDelay is how long /plan synthesis may run before the soft
// "provider may be unresponsive" notice is shown.
const planSlowNoticeDelay = 10 * time.Second

type investigateResultMsg struct {
	records           []record
	sessionKey        string
	err               error
	escalationContent string // when Resolved=false, pipe investigation data to LLM for analysis
	ledgerContent     string // FormatLedgerForPlan() — structured Context-Ledger data, the SSOT for handoff
	investigateLedger *investigate.ContextLedger
}

type reviewResultMsg struct {
	records      []record
	sessionKey   string
	saveReportFn func()
	err          error
}

// planResultMsg carries the outcome of the asynchronous PlanEngine ledger
// synthesis. It is dispatched from a background tea.Cmd (runPlanEngineCmd) so
// the synchronous LLM call never blocks the Bubble Tea event loop.
type planResultMsg struct {
	Tasks   []plan.Task
	Err     error
	Handoff HandoffContext // echoed back so the handler can populate PendingTodos
}

type agentStartMsg struct{ label string }
type agentDoneMsg struct{}

// promptHandoffMsg carries the result of a $prompt synthesis in /ask mode.
// The content field holds the full markdown of the IZEN INTELLIGENT PROMPT
// HANDOFF PACK. The actions slice carries the FollowUp navigation chip data
// to be rendered as an interactive Action component at the terminal footer.
type promptHandoffMsg struct {
	content string
	actions []Action
	err     error
}

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

// hotfixProposalMsg carries the LLM-generated patch for a $hot hotfix back to
// the Update loop. The engine does NOT apply it — it freezes the pipeline in
// StateAwaitingApproval and renders a diff proposal for explicit authorization.
type hotfixProposalMsg struct {
	Task  *plan.Task
	Patch *execution.Patch
	Diff  string
	Err   error
}

// hotfixProgressMsg streams a lifecycle log line to the terminal while the
// $hot patch is being generated in the background. It is delivered through the
// Bubble Tea event loop (never from the background goroutine) so the spinner
// stays alive and the developer sees active progress instead of a frozen pane.
type hotfixProgressMsg struct {
	Line string
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
	maxBuildRecoveryAttempts  = 3

	maxProposalDiffHeight = 15 // max visible diff lines in expanded proposal widget

	// Vi-mode states
	ViNormal = 0
	ViVisual = 1

	viGGTimeout    = 500 * time.Millisecond
	viTripleEscMax = 800 * time.Millisecond

	// Inline markers for cursor/selection injection in raw text.
	// These are zero-width sentinel sequences that we insert into raw
	// record text before the rendering pipeline, then detect and replace
	// with styled lipgloss output after rendering.
	cursorOpen  = "\x00CURSOR\x00"
	cursorClose = "\x00/ CURSOR\x00"
	selOpen     = "\x00SEL\x00"
	selClose    = "\x00/SEL\x00"
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

var flowingSpinnerFrames = []string{" ✦ ", " ✧ ", " ⚙ ", " ❋ ", " ❄ ", " ✱ ", " ❋ ", " ⚙ ", " ✧ ", " ✦ "}

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
	streamCh       chan tea.Msg
	responseBuffer strings.Builder
	streaming      bool
	spinnerFrame   int
	// lastSpinnerAdvance throttles spinner-frame advancement inside the 20ms
	// smoothStreamTickMsg loop to a ~100ms cadence, so the braille animation
	// stays visually consistent with the 100ms tickMsg loop while token
	// rendering keeps its 20ms pacing. Zero value means "advance immediately".
	lastSpinnerAdvance   time.Time
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

	// Shell command proposed by agent, injected into the input bar.
	// The command only executes when the user presses Enter.
	proposedShellCmd string

	state UIState

	execEng    *execution.Engine
	planStore  *plan.PlanStore
	planEngine *plan.Engine // structural plan engine wired for ledger-driven execution

	// buildLedger is the live /plan task state bridge shared with the execution
	// engine. It is created lazily and survives across builds within a session.
	buildLedger *ctxpkg.TaskLedger
	// currentBuildTaskID is the plan task id being executed by the active
	// /build run; it is threaded into every committed patch so the ledger can
	// be marked Completed.
	currentBuildTaskID int

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

	// Project type detection
	detection project.Detection

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

	// Background context registry: tracks all in-flight background contexts
	// so they can be cancelled on mode transitions or Ctrl+C.
	// Each entry is a cancel function returned by context.WithCancel.
	backgroundCancels []context.CancelFunc

	// Viewport scroll tracking: when the user scrolls up to inspect code,
	// auto-scroll to bottom is suppressed until SPACE or a new message.
	userIsScrollingUp bool

	// Vi-mode navigation state
	inViMode        bool      // viewport navigation mode active
	viModeState     int       // ViNormal (0) or ViVisual (1)
	cursorLine      int       // index into m.records for cursor (logical row)
	cursorCol       int       // rune offset within the active record's text (logical col)
	visualStartLine int       // anchor record index for visual block selection
	visualStartCol  int       // anchor rune offset for character-level visual selection
	viTopLine       int       // top visible record line index (viewport scroll anchor)
	viForceTop      bool      // gg: snap viewport YOffset to absolute top
	viForceBottom   bool      // G: snap viewport YOffset to absolute bottom
	searchQuery     string    // active search query buffer
	searchActive    bool      // user is typing a search query
	viSearchResults []int     // line numbers matching the search
	viSearchIdx     int       // current position in search results
	viPendingPrefix string    // for multi-key sequences (gg, etc.)
	escCount        int       // consecutive escape presses
	lastEscTime     time.Time // timestamp of last escape press
	viCmdMode       bool      // typing a : or / command in vi-mode
	viCmdBuf        string    // buffered vi command text

	// Test/run output storage for /fix consumption
	lastTestOutput string
	lastTestFailed bool
	lastTestTarget string

	// Safety gate confirmation state
	pendingTestConfirm bool
	pendingTestTarget  string

	// Build approval gate: when a SHELL_EXEC task is queued, the system
	// requires explicit user confirmation before any command reaches the OS
	// shell. pendingBuildTask holds the task awaiting y/n input.
	pendingBuildApproval bool
	pendingBuildTask     *plan.Task
	// pendingBuildAllowAlways, when set from the permission box "Allow Always"
	// option, skips the approval gate for subsequent SHELL_EXEC tasks for the
	// remainder of the session. Reset on mode transitions or /clear.
	pendingBuildAllowAlways bool

	// Hotfix approval gate: $hot MUST NOT apply structural patches to disk
	// silently. After the model synthesizes the patch, the engine freezes in
	// StateAwaitingApproval and renders the code diff proposal. The developer
	// authorizes (y) or rejects (n) before any byte touches the workspace.
	// pendingHotfixTask is the synthesized FILE_MUTATE task awaiting y/n.
	pendingHotfixTask *plan.Task
	// pendingHotfixPatch holds the generated patch awaiting approval so the
	// apply step does not need to re-invoke the LLM on confirmation.
	pendingHotfixPatch *execution.Patch

	// Review action spinner: set synchronously on $run/$test/$fix dispatch
	// so the view can immediately render a spinner without waiting for the
	// async agentStartMsg to be processed.
	reviewRunning bool

	// Safety valve: timestamp of the last review action dispatch. If
	// reviewRunning stays true longer than the timeout threshold, the
	// tick loop force-clears it to prevent ghost spinner lock.
	lastActionTime time.Time

	// lastAgentActivity is the wall-clock timestamp of the most recent
	// background-agent activity (agent start, progress tick, or result
	// receipt). The tickMsg leak detector uses it to distinguish a genuine
	// long-term hang from a legitimate in-flight worker: UI execution flags
	// (m.streaming / m.agentRunning) are only force-cleared once activity has
	// been idle for at least 15 seconds, preventing premature spinner freezes.
	lastAgentActivity time.Time

	// Handoff pipeline: inter-mode state transfer (WORKFLOW STATE).
	// This survives mode transitions and must never be cleared to hide UI.
	handoffCtx HandoffContext

	// handoffLedgerContent stores the raw Context-Ledger output from the
	// investigate engine (FormatLedgerForPlan). It is the authoritative
	// Single Source of Truth for mode-to-mode handoffs — preferred over
	// the transient LLM output text (Transaction Cache).
	handoffLedgerContent string

	// lastInvestigateLedger holds the structured forensic findings produced by
	// the most recent /investigate run. bridgeInvestigationToLedger projects it
	// into the canonical session.ContextLedger as sequential, ID-addressed
	// packets, preserving state across the mode transition.
	lastInvestigateLedger *investigate.ContextLedger

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

	// Build auto-recovery counter: tracks retry attempts after persistent
	// build failure during verification. Reset on mode entry and clear.
	buildRecoveryCount int

	// hotfixActive tracks whether we are executing a $hot urgent hotfix task.
	// When true, the build result handler will restore the stashed plan from
	// .izen/stashed_plan.json after the hotfix completes.
	hotfixActive bool

	// modeChangeAuthorized is set true ONLY when the user explicitly types a
	// mode-switch command (/build, /plan, /mode build). Auto-transitions from
	// the execution pipeline or investigate→build detection are blocked unless
	// this flag is true. Reset to false after every setMode call.
	modeChangeAuthorized bool

	// planApproved tracks whether the current plan has been generated and
	// approved by the user. Once true, the engine permits direct transition
	// to /build without re-entering /plan. Set true on successful plan→build
	// transition. Reset to false when entering /plan or /investigate.
	planApproved bool

	// planPending marks that an asynchronous PlanEngine ledger synthesis is
	// in flight (set when the /plan handoff spawns runPlanEngineCmd, cleared
	// when planResultMsg arrives). It is the definitive signal that the
	// spinner is legitimately owned by a live orchestration worker, so the
	// tickMsg leak-detector must NOT wipe the loading flags until the
	// terminal planResultMsg is delivered.
	planPending bool

	// planStartedAt records when the current /plan synthesis began. It backs
	// the soft-timeout notice (planSlowNoticeMsg): if synthesis is still in
	// flight after planSlowNoticeDelay, a single viewport-safe warning is
	// surfaced so the user knows the local model may be unresponsive — well
	// before the 120s hard context timeout fires.
	planStartedAt time.Time

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
	initGitInitDone    bool
	initGitInitErr     string
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

// logActivity appends a system activity record and forces an immediate
// viewport redraw so the user sees every internal tool invocation in
// real time — even during streaming (bypasses the PreRenderedHistory
// streaming freeze).
func (m *model) logActivity(format string, args ...interface{}) {
	msg := sanitizeIngressANSI(fmt.Sprintf(format, args...))
	r := record{role: roleActivity, text: msg}
	m.records = append(m.records, r)
	if m.width > 0 {
		rendered := m.renderRecordForViewport(r)
		if rendered != "" {
			m.PreRenderedHistory += rendered + "\n"
		}
	}
	m.refreshViewportContent()
	if m.Ready && !m.userIsScrollingUp {
		m.Viewport.GotoBottom()
	}
}

// push appends a record. Records are flushed to the terminal's native
// scrollback at explicit sync points (user submit, stream done, etc.).
func (m *model) push(r role, text string) {
	text = sanitizeIngressANSI(text)
	m.records = append(m.records, record{role: r, text: text})
	m.cacheRecordToHistory(record{role: r, text: text})
}

// sanitizeIngressANSI is the ingress filter for external stream ingestion.
// External processes (go test, build runners, shells) sometimes emit SGR
// bytes whose leading ESC (\x1b) was stripped before the line reached the
// buffer, e.g. "[38;2;108;112;134m". Rendered verbatim these print as raw
// garbage on the TUI viewport and corrupt vi-mode column alignment. We drop
// only the orphaned sequences — any SGR still prefixed with \x1b is valid
// ANSI and is preserved verbatim, so intentional styling (lipgloss output)
// survives untouched.
func sanitizeIngressANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	n := len(runes)
	i := 0
	for i < n {
		// Copy any valid escape sequence (\x1b[ ... <final byte>) verbatim.
		if runes[i] == '\x1b' {
			if i+1 < n && runes[i+1] == '[' {
				b.WriteRune('\x1b')
				i++
				b.WriteRune('[')
				i++
				// CSI/SGR parameter + intermediate bytes are all < 'A'; the
				// final byte (e.g. 'm', 'J', 'K') lies in 'A'..'~'.
				for i < n && (runes[i] < 'A' || runes[i] > '~') {
					b.WriteRune(runes[i])
					i++
				}
				if i < n {
					b.WriteRune(runes[i])
					i++
				}
			} else {
				// Non-CSI escape (e.g. OSC intro \x1b]); keep the ESC and let
				// the following bytes be re-scanned for orphaned SGR.
				b.WriteRune('\x1b')
				i++
			}
			continue
		}

		// Outside an escape: an orphaned SGR looks like "[\d+(;\d+)*m" with no
		// preceding ESC. Also catch orphaned DEC private-mode mouse tracking
		// sequences like "[<0;26;37M" that are left when the leading \x1b was
		// stripped in an earlier text-pass. Detect and skip both.
		if runes[i] == '[' {
			if idx := matchOrphanSGR(runes, i); idx >= 0 {
				i = idx + 1
				continue
			}
			if idx := matchOrphanMouse(runes, i); idx >= 0 {
				i = idx + 1
				continue
			}
		}

		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

// matchOrphanSGR returns the index of the closing 'm' of an orphaned SGR
// sequence beginning at runes[start]=='[', or -1 when it does not match the
// pattern \[\d+(?:;\d+)*m (i.e. it is part of ordinary text such as "[3m]").
func matchOrphanSGR(runes []rune, start int) int {
	j := start + 1
	if j >= len(runes) || runes[j] < '0' || runes[j] > '9' {
		return -1
	}
	j++ // first numeric block
	for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
		j++
	}
	for j < len(runes) && runes[j] == ';' {
		k := j + 1
		if k < len(runes) && runes[k] >= '0' && runes[k] <= '9' {
			j = k + 1
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
		} else {
			break
		}
	}
	if j < len(runes) && runes[j] == 'm' {
		return j
	}
	return -1
}

// matchOrphanMouse returns the index of the closing letter of an orphaned
// DEC private-mode mouse tracking sequence beginning at runes[start]=='['
// followed by '<' (e.g. "[<0;26;37M" / "[<0;26;37m"). These lack the leading
// ESC byte because a previous text-input handling pass stripped the \x1b but
// left the CSI payload behind, causing raw garbage like ";26;37M[<0;26;37m"
// to leak into the viewport. Returns -1 when no match.
func matchOrphanMouse(runes []rune, start int) int {
	j := start + 1
	if j >= len(runes) || runes[j] != '<' {
		return -1
	}
	j++
	if j >= len(runes) || runes[j] < '0' || runes[j] > '9' {
		return -1
	}
	j++
	for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
		j++
	}
	for j < len(runes) && runes[j] == ';' {
		k := j + 1
		if k < len(runes) && runes[k] >= '0' && runes[k] <= '9' {
			j = k + 1
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
		} else {
			break
		}
	}
	// Final byte: 'M' (DEC private mode press/release) or 'm' (SGR variant)
	if j < len(runes) && (runes[j] == 'M' || runes[j] == 'm') {
		return j
	}
	return -1
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
		displayName := config.SanitizeUsername(m.userName)
		userHeader := dimmedStyle.Render("@" + displayName + "  ")
		paddedText := " " + rec.text
		padNeeded := width - lipgloss.Width(userHeader) - lipgloss.Width(paddedText) - 1
		if padNeeded > 0 {
			paddedText += strings.Repeat(" ", padNeeded)
		}
		return userHeader + userBgStyle.Render(paddedText)
	case roleAI:
		return m.renderAIResponseBlocks(rec.text, width)
	case roleActivity:
		return m.styleActivityLine(rec.text)
	default:
		return rec.text
	}
}

// pushRecords appends multiple records.
func (m *model) pushRecords(recs []record) {
	for _, rec := range recs {
		rec.text = sanitizeIngressANSI(rec.text)
		m.records = append(m.records, rec)
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
// resetStreamingState forcibly clears every background-execution flag that
// drives the "⚙ streaming…" prompt indicator and the runtime-status spinner.
// Call this when a background engine path terminates (the async planResultMsg
// handler) so a prior stream/agent session can never leak its spinner into a
// subsequent idle view. It mirrors the teardown performed by streamDoneMsg.
func (m *model) resetStreamingState() {
	m.streaming = false
	m.streamCh = nil
	m.streamCancel = nil
	m.streamTickActive = false
	m.agentRunning = false
	m.agentLabel = ""
	m.planPending = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	if m.streamParser != nil {
		m.streamParser = nil
	}
}

// reconcileSpinner is the single deterministic reset point that ties the
// Bubble Tea spinner lifecycle to command resolution. It is called whenever an
// async producer (plan result, investigate result, ledger handoff) resolves or
// yields zero constructive tasks, guaranteeing the transient loading flags are
// cleared immediately so the UI can never freeze on "✦ streaming…".
//
// IMPORTANT: this method ONLY clears transient loading flags. It must NEVER
// touch persistent view state — m.state (UIState), m.currentResult (which
// drives Action Chip rendering), m.handoffCtx, m.pendingProposals, or component
// visibility — otherwise it would wipe the user's actionable buttons or corrupt
// the active layout when a background command resolves.
func (m *model) reconcileSpinner() {
	m.streaming = false
	m.streamCh = nil
	m.streamCancel = nil
	m.streamTickActive = false
	m.agentRunning = false
	m.agentLabel = ""
	m.agentDone = true
	m.reviewRunning = false
	m.pipelineRunning = false
	m.planPending = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	if m.streamParser != nil {
		m.streamParser = nil
	}
}

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

// cleanShutdownCmd performs a graceful session teardown: kills orphan
// processes, purges in-memory session state, and preserves the persistent
// .izen metadata directory for future sessions. The .izen directory is NEVER
// deleted — it is permanent and persists across application lifecycles.
// Only transient session files (session.json, context_ledger.json) are cleared
// to give a clean slate on next startup.
func (m *model) cleanShutdownCmd() tea.Cmd {
	return func() tea.Msg {
		execution.KillAllOrphans()
		if m.sess != nil {
			m.sess.SetMode(m.resolver.Current())
			m.sess.Purge()
		}
		if m.workspaceRoot != "" {
			_ = state.CleanupLocalState(m.workspaceRoot)
		}
		return tea.Quit()
	}
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
// When in Vi-mode, records are rendered directly with cursor/selection
// highlighting instead of using the cached PreRenderedHistory.
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

	if m.inViMode {
		content.WriteString(m.renderRecordsWithCursor())
	} else if m.PreRenderedHistory != "" {
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

// renderRecordsWithCursor renders all chat records with vi-mode cursor and
// visual selection highlighting applied inline. Highlighting is performed on
// the already-rendered ANSI output via injectStyleRange, which locates the
// target printable character(s) without slicing raw text — so ANSI escape
// sequences are never cut and style bytes never leak to the screen.
func (m *model) renderRecordsWithCursor() string {
	if len(m.records) == 0 {
		return ""
	}

	var b strings.Builder

	// Normalize visual selection coordinates (top-left → bottom-right)
	selStartLine, selEndLine := 0, -1
	selStartCol, selEndCol := 0, 0
	if m.viModeState == ViVisual {
		if m.visualStartLine < m.cursorLine ||
			(m.visualStartLine == m.cursorLine && m.visualStartCol <= m.cursorCol) {
			selStartLine, selEndLine = m.visualStartLine, m.cursorLine
			selStartCol, selEndCol = m.visualStartCol, m.cursorCol
		} else {
			selStartLine, selEndLine = m.cursorLine, m.visualStartLine
			selStartCol, selEndCol = m.cursorCol, m.visualStartCol
		}
	}

	for i, rec := range m.records {
		rendered := m.renderRecordForViewport(rec)
		if rendered == "" {
			continue
		}

		// Normal mode: highlight the single cursor character inline.
		if i == m.cursorLine && m.viModeState == ViNormal {
			rendered = injectStyleRange(rendered, m.cursorCol, m.cursorCol, viCursorStyle)
		}

		// Visual mode: highlight the character range on each selected line.
		if m.viModeState == ViVisual && i >= selStartLine && i <= selEndLine {
			lineLen := m.lineRuneLen(i)
			if lineLen > 0 {
				sCol, eCol := 0, lineLen-1
				if i == selStartLine {
					sCol = clampCol(selStartCol, lineLen)
				}
				if i == selEndLine {
					eCol = clampCol(selEndCol, lineLen)
				}
				if eCol < sCol {
					eCol = sCol
				}
				rendered = injectStyleRange(rendered, sCol, eCol, viSelectionBgStyle)
			}
		}

		b.WriteString(rendered)
		if i < len(m.records)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// clampCol constrains a 0-based printable column to [0, lineLen-1].
func clampCol(c, lineLen int) int {
	if c < 0 {
		return 0
	}
	if c > lineLen-1 {
		return lineLen - 1
	}
	return c
}

// tokenKind distinguishes printable text from atomic ANSI escape sequences.
type tokenKind int

const (
	tokenText tokenKind = iota // Raw, printable characters
	tokenANSI                  // Full, unbroken ANSI escape sequence (e.g. "\x1b[32m")
)

// lineToken is a single atom of a styled line: either a run of printable text
// or one complete, unbroken ANSI escape sequence. Keeping ANSI sequences as
// indivisible tokens makes it physically impossible to split one and drop its
// leading ESC, which is the root cause of raw SGR leaks during hjkl navigation.
type lineToken struct {
	Kind  tokenKind
	Value string
}

// tokenizeLine parses a styled line into alternating Text/ANSI tokens. Every
// complete escape sequence (from \x1b to its final byte) becomes one TokenANSI;
// everything else is grouped into TokenText runs. The byte content of each
// token is preserved verbatim and in original order.
func tokenizeLine(s string) []lineToken {
	var tokens []lineToken
	runes := []rune(s)
	n := len(runes)
	i := 0

	var cur strings.Builder
	flushText := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, lineToken{Kind: tokenText, Value: cur.String()})
			cur.Reset()
		}
	}

	for i < n {
		if runes[i] == '\x1b' {
			flushText()
			start := i
			i++
			if i < n && runes[i] == '[' {
				i++
				// CSI parameter/intermediate bytes are all < 'A'; the final
				// byte (e.g. 'm', 'J', 'K') lies in 'A'..'~'.
				for i < n && (runes[i] < 'A' || runes[i] > '~') {
					i++
				}
				if i < n {
					i++ // consume the final byte
				}
			}
			tokens = append(tokens, lineToken{Kind: tokenANSI, Value: string(runes[start:i])})
			continue
		}
		cur.WriteRune(runes[i])
		i++
	}
	flushText()

	return tokens
}

// injectStyleRange wraps the printable characters at columns [startCol, endCol]
// (both inclusive, 0-based) of an already-rendered ANSI string with the given
// lipgloss style. The line is first tokenized into atomic Text/ANSI tokens.
// Printable characters are counted only across TokenText tokens, and the style
// injection happens by splitting the single TokenText that contains the target
// column(s) — TokenANSI tokens are never sliced, so escape sequences stay
// whole and no \x1b is ever dropped. After the highlighted segment the active
// style (most recent TokenANSI) is re-emitted so the surrounding coloring
// continues seamlessly onto the remaining characters.
func injectStyleRange(s string, startCol, endCol int, style lipgloss.Style) string {
	if startCol < 0 || endCol < startCol {
		return s
	}

	tokens := tokenizeLine(s)

	var out strings.Builder
	var lastAnsi strings.Builder // most recent TokenANSI (active surrounding style)
	printable := 0

	for _, tok := range tokens {
		if tok.Kind == tokenANSI {
			out.WriteString(tok.Value)
			lastAnsi.Reset()
			lastAnsi.WriteString(tok.Value)
			continue
		}

		// TokenText: only this kind contributes printable characters.
		tRunes := []rune(tok.Value)
		tLen := len(tRunes)

		// No overlap with [startCol, endCol]: emit verbatim.
		if endCol < printable || startCol > printable+tLen-1 {
			out.WriteString(tok.Value)
			printable += tLen
			continue
		}

		// Overlap: split this text token at the relative offset(s).
		from := 0
		if startCol > printable {
			from = startCol - printable
		}
		to := tLen - 1
		if endCol < printable+tLen-1 {
			to = endCol - printable
		}

		out.WriteString(string(tRunes[:from]))
		out.WriteString(style.Render(string(tRunes[from : to+1])))
		// Restore the surrounding style so following text keeps its color.
		out.WriteString(lastAnsi.String())
		out.WriteString(string(tRunes[to+1:]))

		printable += tLen
	}

	return out.String()
}

// renderedLineCount returns the approximate number of terminal lines a record
// occupies when rendered through renderRecordForViewport.
func (m *model) renderedLineCount(rec record) int {
	rendered := m.renderRecordForViewport(rec)
	if rendered == "" {
		return 0
	}
	return strings.Count(rendered, "\n") + 1
}

// lineRuneLen returns the number of printable runes in a record's text.
// It strips ANSI escape sequences first so cursor positioning and column
// clamping operate on the visible (plain) characters, never on style bytes.
func (m *model) lineRuneLen(lineIdx int) int {
	if lineIdx < 0 || lineIdx >= len(m.records) {
		return 0
	}
	return len([]rune(ansi.Strip(m.records[lineIdx].text)))
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
