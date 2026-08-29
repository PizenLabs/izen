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
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain"
	domainworkflow "github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/orchestrator"
	"github.com/PizenLabs/izen/internal/patch"
	"github.com/PizenLabs/izen/internal/planner"
	"github.com/PizenLabs/izen/internal/presentation"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
	riview "github.com/PizenLabs/izen/internal/review"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/state"
	"github.com/PizenLabs/izen/internal/ui/status"
	proposaltui "github.com/PizenLabs/izen/internal/ui/tui"
	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
	"github.com/PizenLabs/izen/pkg/tui/tips"
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

// configLoadedMsg is dispatched after the defensive workspace loader runs
// (guaranteed once per startup via Init). It is the single seam that
// reconciles the in-memory initStage with the on-disk workspace state: a
// completed initStage backed by a missing .izen/ is self-healed (recreated
// with default project settings) or routed back to the onboarding wizard —
// never left rendering a frozen welcome header with no interactive input.
type configLoadedMsg struct {
	localCfg *config.LocalConfig
	err      error
}

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

// UIState is the derived modal presentation state. The canonical type lives in
// the presentation layer (presentation.UIState); these aliases keep the view
// vocabulary unified with the projection so AwaitingApproval/Processing are
// always computed from the canonical workflow state, never independent local
// flags.
type UIState = presentation.UIState

const (
	StateChat             = presentation.StateChat
	StateAwaitingApproval = presentation.StateAwaitingApproval
	StateProcessing       = presentation.StateProcessing
	StateHotfixAmbiguous  = presentation.StateHotfixAmbiguous
)

type record struct {
	role role
	text string
}

type tokenMsg string

// thinkingTokenMsg carries a thinking/reasoning token emitted by the stream
// classifier (delta reasoning_content, <thought>…</thought> blocks, or
// reasoning sentinels). It is deliberately separate from tokenMsg (content) so
// the renderer can apply the dimmed/faint style exclusively to reasoning while
// content streams in bright.
type thinkingTokenMsg string

// streamUsageMsg carries the provider's AUTHORITATIVE cumulative token usage
// observed mid-stream. It is emitted by the stream producer only when the
// provider reports a usage update (Known && !Estimated — never a character-count
// estimate), so the live streaming indicator reflects billed tokens and never a
// fabricated count. Reasoning carries the provider-reported reasoning-token
// split when available, which backs the compact thought summary.
type streamUsageMsg struct {
	input     int
	output    int
	reasoning int
}

type streamDoneMsg struct {
	content     string
	tokenInput  int
	tokenOutput int
	// usageEstimated is true when the token counts came from a character-count
	// estimate (local models that report no usage, or an interrupted stream)
	// rather than the provider's authoritative usage chunk.
	usageEstimated bool
	// truncated is true when the provider signalled finish_reason == "length":
	// the response hit the API completion ceiling and was cut off mid-generation
	// rather than finishing naturally ("stop").
	truncated bool
}

type traceUpdateMsg struct {
	trace *ctxpkg.CodebaseTrace
}

// askStreamPreparedMsg carries the outcome of the async /ask context
// assembly (Context Planner query + fallback file reads). It is produced by a
// background tea.Cmd so the synchronous graph/file I/O never blocks the Bubble
// Tea event loop on prompt submit — the Enter handler returns immediately with
// the loading shimmer animating, and this message hands the fully assembled
// content to streamCmd once the planner finishes.
type askStreamPreparedMsg struct {
	content string
	// governed is true when the prep already assembled context (planned chunks
	// OR the fallback @file reads), so streamCmd must not inject a second time.
	governed bool
	trace    *ctxpkg.CodebaseTrace
}

type streamErrMsg struct {
	err     error
	content string
	// tokenInput/tokenOutput carry the partial provider usage captured before
	// the stream error so consumed tokens are reported even on a failed
	// attempt ("Explicit Over Implicit").
	tokenInput  int
	tokenOutput int
	// usageEstimated is true when the token counts are a character-count
	// estimate rather than the provider's authoritative usage.
	usageEstimated bool
}

type PlanStreamingFinishedMsg struct {
	Success bool
}

type gitInitResultMsg struct{ err error }

type tickMsg time.Time

type smoothStreamTickMsg time.Time

type spinnerTickMsg time.Time

type proTipTickMsg time.Time

// planSlowNoticeMsg fires once, planSlowNoticeDelay after /plan synthesis
// starts. If synthesis is still pending when it arrives, a viewport-safe
// warning is surfaced (never a raw terminal print) so the user learns the
// local model may be unresponsive before the 120s hard timeout.
type planSlowNoticeMsg struct{ startedAt time.Time }

// planSlowNoticeDelay is how long /plan synthesis may run before the soft
// "provider may be unresponsive" notice is shown.
const planSlowNoticeDelay = 10 * time.Second

// proTipRotationInterval is the interval at which Pro Tips rotate
// in the welcome banner. Fast enough for visual engagement, slow
// enough to remain gentle and readable.
const proTipRotationInterval = 5 * time.Second

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
	ledger       *riview.ReviewLedger
	err          error
}

// planResultMsg carries the outcome of the asynchronous PlanEngine ledger
// synthesis. It is dispatched from a background tea.Cmd (runPlanEngineCmd) so
// the synchronous LLM call never blocks the Bubble Tea event loop.
type planResultMsg struct {
	Tasks       []plan.Task
	Err         error
	Handoff     HandoffContext // echoed back so the handler can populate PendingTodos
	IsFastTrack bool           // if true, auto-approve plan — bypass approval gate
	// Microkernel marks plans produced by the deterministic microkernel
	// pipeline (pkg/engine) rather than LLM synthesis. The handler uses it to
	// label the staged plan and to render rejection reasons in the footer.
	Microkernel bool
	// IntentCompiler marks plans produced by the IR-driven intent compiler
	// (inference → policy → IR plan → lowerer). The handler labels the staged
	// plan and notes that zero model tokens were consumed.
	IntentCompiler bool
	// EngineFirst marks plans staged by the engine-first strategy layer
	// (Phase 10): deterministic tasks the engine resolved without LLM
	// reasoning. The handler labels the staged plan truthfully.
	EngineFirst bool
	// TokenInput/TokenOutput are the provider-reported usage of the synthesis
	// call, committed even when the response was truncated (finish_reason:
	// "length"). The handler mirrors them into the session counters and the
	// global status.Tracker so token metrics are never lost to truncation.
	TokenInput  int
	TokenOutput int
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
	// evidence is the single source of truth for the mutation attempt's facts
	// (artifact presence, diff presence, apply execution, filesystem result,
	// verification). The renderer projects it — it never infers a mutation
	// from a planned "Edit" event. May be nil on pre-apply failures.
	evidence *execution.MutationEvidence
	// TokenInput/TokenOutput carry the provider-reported usage of the call
	// that produced this mutation result (the zero-patch short-circuit and
	// the apply paths). Without them the provider's real token consumption
	// would be silently dropped whenever a task ends in "nochange" or "skipped"
	// — the exact "OpenRouter billed 2048 completion tokens while the footer
	// shows 0 tok" bug. usageKnown is false when the provider reported no usage.
	TokenInput  int
	TokenOutput int
	usageKnown  bool
}

// outcome returns the semantic outcome of the mutation result, normalized onto
// the execution.MutationOutcome vocabulary. When the runtime filled a
// MutationEvidence record it is the single source of truth; otherwise the
// error/status is normalized so a vague status can never claim a mutation.
func (r mutationResultMsg) outcome() execution.MutationOutcome {
	if r.evidence != nil && r.evidence.Outcome != "" {
		return r.evidence.Outcome
	}
	if r.err != nil {
		if isContextCancelled(r.err) || isContextDeadline(r.err) {
			return execution.OutcomeCancelled
		}
		return execution.OutcomeApplyFailed
	}
	return execution.ParseMutationOutcome(r.status)
}

type applyAllResultMsg struct {
	results []mutationResultMsg
}

type shellOutputMsg struct {
	lines []string
}

var _ tea.Msg = shellOutputMsg{}

// shellChunkMsg carries one live stdout/stderr chunk from the streaming shell
// pipeline. The UI appends it to the ActivityTree's running exec entry so the
// output grows in the viewport in real-time.
type shellChunkMsg struct {
	text string
}

var _ tea.Msg = shellChunkMsg{}

// shellExitMsg is the terminal event of the streaming shell pipeline. It
// carries the process exit code and elapsed time so the running exec entry
// flips to a completed "(exit N · Xs)" line and the shimmer dock clears.
type shellExitMsg struct {
	cmd      string
	exitCode int
	elapsed  time.Duration
	err      error
}

var _ tea.Msg = shellExitMsg{}

type graphBuiltMsg struct {
	graph *lea.FileGraph
	err   error
}

type graphIndexingMsg struct {
	indexing bool
}

func buildGraphCmd(eng *lea.Engine) tea.Cmd {
	return func() tea.Msg {
		if eng == nil {
			return graphBuiltMsg{err: fmt.Errorf("graph engine not available")}
		}
		if _, err := eng.Index(context.Background()); err != nil {
			return graphBuiltMsg{err: err}
		}
		return graphBuiltMsg{graph: eng.FileGraph()}
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

// thinkingStreamMsg carries a reasoning token chunk from the SSE stream
// to the TUI Thinking Panel for real-time display during fast-track builds.
type thinkingStreamMsg struct {
	Content string
}

// livePreviewChunkMsg carries a code content or tool call chunk from the
// SSE stream to the LiveCodePreview for real-time display during fast-track
// builds.
type livePreviewChunkMsg struct {
	Content      string // raw content or tool call JSON
	IsTool       bool   // true if this chunk is a tool call delta
	IsDone       bool   // true if this is the final content chunk before stream end
	FinishReason string // "stop", "length", "tool_calls", etc.
}

// buildFailedMsg signals that the fast-track build stream failed
// (e.g. stream error, truncation, or provider failure). It guarantees
// the spinner is cleaned up and the pipeline is reset.
type buildFailedMsg struct {
	Err error
	// TokenInput/TokenOutput carry the partial provider usage captured before
	// the failure (see streamErrMsg).
	TokenInput  int
	TokenOutput int
}

// hotfixProposalMsg carries the LLM-generated patch for a $hot hotfix back to
// the Update loop. The engine does NOT apply it — it freezes the pipeline in
// StateAwaitingApproval and renders a diff proposal for explicit authorization.
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

// flowingSpinnerFrames is the space-padded snowflake sequence used by the
// inline loading spinner. The glyphs themselves are the canonical
// tokens.SpinnerSnowflakeFrames; only the padding is added here for the
// inline status line's breathing room.
var flowingSpinnerFrames = func() []string {
	out := make([]string, len(SpinnerSnowflakeFrames))
	for i, f := range SpinnerSnowflakeFrames {
		out[i] = " " + f + " "
	}
	return out
}()

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
	graph    *lea.FileGraph
	// leaEng is the Phase 3 Lea structural engine (canonical index for
	// architecture, call chains, routes and symbol lookups). It backs the
	// context planner's graph source and the /arch analysis. Nil only in
	// headless/test harnesses that never construct a model.
	leaEng            *lea.Engine
	extractorRegistry *symbol.ExtractorRegistry
	// contextPlanner classifies the user's question and injects budget-fitted
	// structural context (Lea graph symbols, tool logs, architecture overview)
	// ahead of the LLM call. Lazily constructed from the native graph and the
	// workspace root; nil when no graph is ready.
	planner   *planner.Planner
	plannerMu *sync.Mutex

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
	streamCh        chan tea.Msg
	execStreamCh    chan tea.Msg
	responseBuffer  strings.Builder
	reasoningBuffer strings.Builder
	streaming       bool
	execStreaming   bool
	// traceBuffer accumulates the raw streamed response of the current/last
	// completion so Ctrl+O can expand/collapse a full output-trace viewport
	// even for models that emit no formal reasoning channel (e.g. Gemma family
	// SLMs). It is reset at the start of every stream and survives completion
	// so the trace stays inspectable after the response ends.
	traceBuffer strings.Builder
	// traceExpanded is the Ctrl+O expansion state of the output-trace viewport.
	traceExpanded bool
	// traceWindowStart anchors the output-trace window while the trace is
	// expanded and streaming: it is frozen once anchored so new chunks never
	// slide the inspected lines out from under the user (the Ctrl+O viewport
	// flicker). traceWindowAnchored reports whether the anchor is live; it
	// resets when the stream starts, the trace is re-expanded, or the user
	// jumps back to the tail (Space).
	traceWindowStart    int
	traceWindowAnchored bool
	// pendingReasoningFragment holds an opened-but-not-yet-closed
	// \x00RSNG\x00 reasoning block carried over between extraction passes.
	// Without this, a sentinel pair split across two ticks (or a truncated
	// SSE chunk) leaked its still-streaming reasoning text straight into
	// the visible answer instead of staying buffered until the closing
	// marker arrives. See extractSentinelReasoning.
	pendingReasoningFragment string
	spinnerFrame             int
	// dotFrame advances each viewport refresh to drive the animated
	// truncation-dots counter in execution log entries (1 → 2 → 3 → 1…).
	dotFrame int
	// lastSpinnerAdvance throttles spinner-frame advancement inside the 20ms
	// smoothStreamTickMsg loop to a ~100ms cadence, so the braille animation
	// stays visually consistent with the 100ms tickMsg loop while token
	// rendering keeps its 20ms pacing. Zero value means "advance immediately".
	lastSpinnerAdvance   time.Time
	currentStreamContent string // accumulated raw text during active LLM stream
	// streamBlocks stores the active stream as typed blocks (content vs
	// thinking) so the renderer can apply differential styling — bright content
	// immediately, dimmed/faint reasoning while it streams.
	streamBlocks *StreamBuffer

	// Expanded metrics for status bar
	IsCloudModel    bool
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ContextLimit    int
	AccumulatedCost float64
	CheckpointID    string

	// usageKnown reports whether the provider has ever reported authoritative
	// (or explicit-estimate) usage this session. The footer distinguishes
	// "usage unknown" (never reported) from a genuine "0 tok" (provider
	// reported zero). "0 tok" must mean an actual zero, not an unknown.
	usageKnown bool

	streamParser     *IncrementalStreamParser
	streamBuffer     string // buffered tokens for smooth tick emission
	streamTickActive bool   // whether smooth-stream tick is active
	userName         string // dynamic system username (set at init)

	// Agent state
	agentRunning bool
	agentLabel   string
	agentDone    bool

	// executionResolving is the single in-flight marker of the gated
	// RuntimeExecutor loading phase. It is set at dispatch, cleared when a new
	// operation supersedes it, and cleared by the terminal execution events
	// (execution.finished / execution.failed / cancelled) so every terminal
	// event releases the loading state and the pending operation. It gates
	// clearExecutionLoading so a terminal runtime event can never clobber an
	// unrelated operation.
	executionResolving bool

	// execView is the single execution-view projection of the gated
	// RuntimeExecutor path. It REDUCES the canonical runtime lifecycle events
	// into one ExecutionViewState (Idle/Running(step)/WaitingApproval/
	// Completed/Failed) plus the human + debug narratives. The renderer for the
	// gated path depends ONLY on this state — it never invents execution truth.
	// It is reset at each gated dispatch and driven exclusively by
	// handleDomainEvent. Nil until the first gated execution.
	execView *presentation.ExecutionProjection

	// execVisibility is the active human presentation layer of the gated
	// execution. It is NORMAL by default (human narrative only); Ctrl+O cycles
	// Normal → Expanded → Debug. The renderer formats the projection's
	// ExecutionFrame for this layer and never decides what belongs in it.
	execVisibility presentation.Visibility

	// Quit-confirmation modal (exit safety guard). pendingQuitConfirm gates
	// clean shutdown behind an explicit [ No ] / [ Yes ] dialog; the dialog
	// defaults to [ No ] so a stray Enter can never exit accidentally.
	pendingQuitConfirm bool
	quitConfirmYes     bool

	// Suggestions
	showSuggestions bool
	suggestionType  string
	suggestions     []Suggestion
	suggestionIdx   int

	// Autocomplete (Prompt Sandwich dropdown)
	autocompleteActive bool
	autocompleteType   string       // "scope", "command", or "directive"
	autocompleteItems  []Suggestion // filtered matching items
	autocompleteIdx    int          // currently highlighted index

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

	// ── Live shell execution state (streaming pipeline) ───────────────
	// shellCh carries shellChunkMsg / shellExitMsg from the background process
	// to the Bubble Tea event loop; shellRunning gates the spinner/shimmer
	// tick loops while a command is in flight; shellCancel lets Ctrl+C abort
	// the running process.
	shellCh      chan tea.Msg
	shellRunning bool
	shellCancel  context.CancelFunc

	state UIState

	execEng    *execution.Engine
	planStore  *plan.PlanStore
	planEngine *plan.Engine // structural plan engine wired for ledger-driven execution

	// executor is the RuntimeExecutor authority boundary (composition root).
	// It owns provider invocation, patch creation, the mutation lifecycle and
	// verification on the migrated paths ($prompt targeted mutation). The UI
	// submits ExecuteRequests and approves/rejects via this boundary — it never
	// calls a provider or a PatchManager directly on those paths. Nil only in
	// headless/test harnesses that do not wire one.
	executor *execution.RuntimeExecutor
	// gateway is the unified IntentGateway: every user action (bare text,
	// $prompt, $hot) crosses it to produce an ExecuteRequest with an
	// unconditional Strategy.Select profile. The UI never routes by mode or
	// triggers hidden executions on the migrated paths.
	gateway *execution.IntentGateway
	// executorPendingPatchID is the approval-held patch ID of the executor
	// execution currently staged in the proposal dock. Non-empty routes the
	// approval keys through RuntimeExecutor.Approve/Reject.
	executorPendingPatchID string
	// executorPendingTargets is the execution target set of the approval-held
	// patch. It is captured when the proposal is staged so the approval key
	// can issue a MutationAuthorization over exactly these files before
	// RuntimeExecutor.Approve applies them.
	executorPendingTargets []string

	// ── PRODUCTION AUTONOMOUS DRIVER BRIDGE (Phase 6) ────────────────
	// autonomousDriver is the composition-bound runtime autonomy Driver, driven
	// through a structural interface so this package never imports
	// internal/runtime/autonomy (architecture invariant). The driver owns the
	// bounded loop; the UI initiates (Run), resumes (ResumeApprove/Reject/
	// Clarify) and aborts (Abort) parked runs, and projects its state. Nil in
	// harnesses without the driver wired — those fall back to the legacy
	// single-shot executor path.
	autonomousDriver autonomousDriver
	// autonomousActive is true while a driver Run command is in flight
	// (executing or parked). It gates duplicate-start protection in the UI.
	autonomousActive bool
	// autonomousBoundary is the snapshot of the driver's parked boundary while
	// awaiting a human response (approve/clarify/inform).
	autonomousBoundary *autonomy.HumanBoundary
	// autonomousSelect is the highlighted candidate index of a parked
	// clarify boundary.
	autonomousSelect int
	// autonomousObjective is the objective of the active/parked driver run.
	autonomousObjective string
	// proposalTUI is the interactive Zero-Token DecisionSurface selection model
	// rendered while the driver parks at HumanBoundaryProposal. It is pure
	// presentation (navigation + selection over the typed recovery options the
	// runtime boundary carries); selecting an option routes a pure intent to
	// Driver.ResumeWithProposal. Nil while no proposal boundary is active.
	proposalTUI *proposaltui.ProposalModel

	// microkernel is the immutable microkernel pipeline adapter. It primes
	// plan/investigate command handling for greenfield generation prompts so
	// the TUI renders explicit file targets instead of the legacy heuristic
	// fallback. It is constructed at bootstrap and immutable afterwards.
	microkernel *plan.MicrokernelPlanner

	// intentCompiler is the IR-driven intent compiler planner. It is the
	// deterministic PRIME path of the /plan handlers: it runs the full
	// inference → policy → IR plan → lowerer pipeline and stages concrete
	// FileArtifact targets (index.html, styles.css, script.js) without any LLM
	// call or heuristic fallback. Constructed at bootstrap and immutable.
	intentCompiler *plan.IntentCompilerPlanner

	// buildLedger is the live /plan task state bridge shared with the execution
	// engine. It is created lazily and survives across builds within a session.
	buildLedger *ctxpkg.TaskLedger
	// currentBuildTaskID is the plan task id being executed by the active
	// /build run; it is threaded into every committed patch so the ledger can
	// be marked Completed.
	currentBuildTaskID int
	// fastTrackTargets records the plan task targets covered by the active
	// fast-track patch batch (proposal targets extracted from native tool
	// calls). When every plan FILE_MUTATE/GIT_ACTION target is covered and the
	// batch has been applied, per-task execution is redundant and the build
	// completes immediately (Explicit Over Implicit).
	fastTrackTargets map[string]bool

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

	// lastPlanIntent is the raw user intent captured when /plan staged its task
	// list. It survives mode transitions (the current prompt is overwritten by
	// the later "/build" invocation) so /build can reconstruct the rewrite
	// context WITHOUT reading obsolete workspace file contents.
	lastPlanIntent string

	// Authorization engine for build/patch execution
	authEngine     *authorization.AuthorizationEngine
	mutationBudget *budget.MutationBudget
	microBudget    *budget.MicroBudget
	caps           *capability.CapabilitySet

	// Focus objective UI notifications (non-chat)
	uiNotice string

	// Proposal widget diff scroll offset
	proposalDiffOffset int

	// Project type detection
	detection project.Detection

	// Project context — never nil after model creation.
	// Falls back to generic/unknown when project detection finds
	// no recognized files (e.g. empty or unrecognized directories).
	projectContext *project.ProjectContext

	// Repository config — never nil after model creation.
	// Holds minimal metadata (root path, git status, default branch)
	// used by the status bar and rendering paths.
	repoConfig *project.RepoConfig

	// AST/Code Graph trace for rendering the AI's thought route
	currentTrace *ctxpkg.CodebaseTrace

	// askContextGoverned is set by the async /ask context prep when it
	// successfully assembled budget-fitted context for the current turn
	// (planned chunks, or the fallback @file reads). streamCmd reads and
	// clears it so the fallback file-read path never injects the same context
	// a second time.
	askContextGoverned bool

	// lastAskTrace carries the most recent planner trace from /ask context
	// assembly so streamCmd can update the UI thought-route panel.
	lastAskTrace *ctxpkg.CodebaseTrace

	// Tip of the Day
	currentTip      string
	proTipIndex     int
	lastTipRotation time.Time

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
	// terminal buildResultMsg handler can apply it (hotfix apply).
	pendingHotfixPatch *execution.Patch
	// hotfixCandidatesMode toggles the read-only candidate-inspection sub-view
	// of the ambiguous card. Inspecting candidates never mutates the file.
	hotfixCandidatesMode bool
	// appliedHotfixFile records the target file of an APPROVED hotfix so the
	// terminal buildResultMsg handler can dispatch the runtime approve_patch
	// projection ONLY after the authoritative apply (budget/authorization
	// gated) actually succeeded. Cleared on the terminal result.
	appliedHotfixFile string

	// ── Multi-file hotfix (Phase 9B): deterministic execution graph ──
	// activeGraph is the single ExecutionGraph owned by the active multi-file
	// $hot operation. Exactly one graph is active at a time (beginOperation
	// supersedes a previous one). It carries the one MutationSet the whole
	// graph executes under.
	activeGraph *execution.ExecutionGraph
	// lastExecutionGraph retains the most recently finalized multi-file graph
	// so $inspect can expose the aggregate execution evidence.
	lastExecutionGraph *execution.ExecutionGraph

	// ── Foreground operation lifecycle (single authoritative operation) ──
	// activeOp is the one foreground operation currently owned by the runtime,
	// or nil when idle. Every execution path (hotfix generation, build apply,
	// provider calls, subprocesses) registers here; AMBIGUOUS/CANCELLED/... are
	// terminal outcomes that release the ownership. See operation.go.
	activeOp *operation
	// opIDCounter issues monotonically increasing operation IDs.
	opIDCounter uint64
	// activitySurfaceSealed is set by /clear (resetTransientInteraction) and
	// cleared by the next foreground operation (beginOperation) or user
	// submission (submitEnter). While sealed, engine-derived activity
	// projections (domain events, engine telemetry, shell output, terminal
	// result records, reasoning streams, control facts) are dropped so a late
	// event from the cleared execution can never resurrect stale activity in
	// the viewport. See lifecycle.go.
	activitySurfaceSealed bool
	// cancelGraceDeadline is the double-Ctrl+C force-exit window armed when a
	// graceful cancellation is initiated. A second Ctrl+C before this deadline
	// hard-exits with status 130.
	cancelGraceDeadline time.Time
	// program is the owning Bubble Tea program, used to restore the terminal
	// before a hard force-exit. Nil in harnesses/tests.
	program *tea.Program

	// Active review ledger from the last /review pipeline run. Carries the
	// C-R-H-V-E evidence chain for /review provenance and $log display. Stored
	// here so $test and $log can attach evidence and render provenance traces.
	currentReviewLedger *riview.ReviewLedger

	// Review action spinner: set synchronously on $run/$test/$fix dispatch
	// so the view can immediately render a spinner without waiting for the
	// async agentStartMsg to be processed.
	reviewRunning bool

	// Investigate action spinner: set synchronously on /investigate dispatch
	// (runInvestigateCmd) so the view immediately renders a spinner and Esc /
	// Ctrl+C can cancel the in-flight run via the central Emergency Interrupt
	// Registry before the async engine even starts.
	investigateRunning bool

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

	// pendingArchArgs stores /arch arguments while indexing is in
	// progress. When indexing completes, the arch view renders
	// automatically using these args.
	pendingArchArgs string

	// indexingStatus tracks background graph indexing state.
	indexingStatus string // "" | "indexing" | "indexed" | "error"

	// viewRegistry resolves the current mode to its ViewMode builder. It is
	// injected at bootstrap (explicit, deterministic) and never mutated by
	// the renderer — the UI stays mode-agnostic.
	viewRegistry *Registry

	// Control Plane references: injected at bootstrap, never mutated by the UI.
	// The UI reads these directly to derive WorkflowState, CapabilitySet flags,
	// MutationBudget counters, and Artifact lifecycle states — it MUST NOT
	// store or cache its own copies of workflow states or capability flags.
	runtimeCtx *runtime.RuntimeContext
	workflowSM *workflow.WorkflowStateMachine
	// workflowRT is the Application-layer domain WorkflowRuntime (PhaseAsk /
	// PhaseBuild / ...) that submit_prompt / switch_mode handlers drive. On a
	// failed build the phase must be unwound back to Ask, or every later user
	// prompt is rejected with "transition from build to ask: moving to a
	// previous phase is not permitted" ("Human-Centered / Reversible").
	workflowRT domainworkflow.WorkflowRuntime
	// viewState is the derived presentation projection of the canonical
	// workflow event stream (EventPhaseChanged / EventApprovalRequested). The
	// UI derives StateAwaitingApproval/StateProcessing from it via
	// syncUIState instead of hand-setting independent flags.
	viewState *presentation.WorkflowViewState

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

	// Model Picker Modal
	showModelPicker bool
	modelPicker     *ModelPickerModal
	sessionModel    string // user-selected model override via /model

	// Foldable execution logs
	logStore *LogStore

	// Native Tool Call Buffer for in-memory tool call interception
	toolCallBuffer *execution.ToolCallBuffer

	// Realtime thinking/reasoning panel
	thinkingPanel *ThinkingPanel

	// Event-driven reasoning buffer. Fed exclusively by EventReasoningStream
	// domain events; renders as a distinct thinking box that never mixes with
	// the response pipeline.
	thinkingBuffer *ThinkingBuffer

	// Number of reasoningBuffer bytes already flushed into thinkingBuffer from
	// the sentinel path. Tracks the delta so the unified thinking box never
	// double-appends the same reasoning on successive ticks.
	sentinelReasoningFlushed int

	// Activity tree — structured tool call logging
	activityTree *ActivityTree

	// Authoritative execution-stage record — the single source of truth for
	// "what is the runtime doing right now". Every progress indicator derives
	// from it; it is updated ONLY at real execution boundaries (see stage.go).
	// Never written from the renderer.
	stage *execStage

	// lastExecutionSnapshot is the retained telemetry snapshot of the most
	// recently finalized foreground operation. It backs the debug/inspect
	// execution-timeline view (see execution_telemetry.go). Written on the UI
	// goroutine at finalization; read on the UI goroutine only.
	lastExecutionSnapshot execution.TelemetrySnapshot
	// lastExecutionTelemetry retains the finalized record itself so the
	// inspect view can render live counters (invocations/retries) without
	// re-folding the marker log. Nil until an operation completes.
	lastExecutionTelemetry *execution.Telemetry
	// lastPromptEnvelope is the deterministic context-ownership account of the
	// most recent directive execution (Phase 8). It proves what context crossed
	// to the provider and is exposed through $inspect. Written and read on the
	// UI goroutine only.
	lastPromptEnvelope PromptEnvelope
	// lastExecutionProof is the execution-evidence account of the most recent
	// hotfix/build mutation (Phase 8): provider invocations, usage, artifact/
	// diff/apply/filesystem/verify facts derived only from real runtime
	// evidence. Written and read on the UI goroutine only.
	lastExecutionProof ExecutionProof
	// lastRuntimeGraph is the runtime-owned execution graph evidence of the most
	// recent RuntimeExecutor execution (RequestID, per-stage kind/state/evidence/
	// timestamps). It is the authoritative execution timeline produced by the
	// runtime; $inspect renders it. Written and read on the UI goroutine only.
	lastRuntimeGraph []runtimegraph.StageSnapshot

	// lastExecutionStrategy is the engine-first execution-strategy decision
	// record of the most recent $prompt (Phase 10). It captures the strategy,
	// the target-resolution outcomes, the execution-factor complexity, the
	// context channels, the artifact contract and the budgets the engine
	// selected BEFORE any model invocation — exposed through $inspect so a
	// user can answer "why did Izen call the model / read this file / need
	// /plan?" without seeing model reasoning. Written on the UI goroutine only.
	lastExecutionStrategy strategy.ExecutionStrategyProfile
	// lastContextEnvelope is the minimum-sufficient context envelope the
	// engine-first compiler assembled for the most recent $prompt (Phase 10).
	// Every item names its owner, source and reason for inclusion; $inspect
	// renders it so context ownership is observable and auditable.
	lastContextEnvelope strategy.ContextEnvelope
	// activeStrategyBudget is the adaptive output budget the engine-first
	// router selected for the currently dispatched targeted mutation. Zero
	// means "use the default bounded budget". It is cleared when the mutation
	// operation reaches a terminal message.
	activeStrategyBudget int
	// hotfixBranding labels the bounded mutation executor's status lines.
	// "HOTFIX" for $hot, "PROMPT" when a $prompt routed to the same executor
	// via the engine-first strategy layer, so telemetry never mislabels the
	// operation source.
	hotfixBranding string

	// Stream throttle — frame-bounded token emission
	streamThrottle *StreamThrottle

	// Live code preview for streaming tool call arguments
	liveCodePreview *LiveCodePreview

	// ── Shimmer loading animation ────────────────────────────────────
	// The loading shimmer + contextual tip drive a status line in the
	// proposal dock during active execution states (streaming before the
	// first token, /plan synthesis, /build, /investigate, /review). The
	// shimmer component owns a ~50ms tick loop that self-terminates once
	// shimmerActive clears — the smooth-clearing seam on first token or
	// task completion.
	shimmerAnim   shimmer.Model
	shimmerActive bool
	shimmerText   string
	loadingTip    string
	tipProvider   *tips.Provider

	// Event bus — the headless engines publish domain events here and the UI
	// subscribes as a pure projection. Never nil after bootstrap.
	bus *events.Bus

	// Application-layer Runtime facade (RFC v1.0 section 1). The UI expresses
	// every canonical user interaction as a RuntimeCommand executed through
	// this facade and receives state changes as translated PresentationEvents.
	// Nil only in headless/test harnesses that never construct a model.
	rt *appruntime.Runtime
	// pres is the presentation-layer command/event gateway bound to rt.
	pres *presentation.Bridge
	// presSink forwards runtime.PresentationEvents into the Bubble Tea event
	// loop as presentationEventMsg. It must be closed on shutdown.
	presSink *presentation.EventSink

	// Execution orchestrator: maps logical phases (Idle/Ask/Investigate/Plan/
	// Build/Review) onto the single WorkflowStateMachine, sharing the
	// persistent RuntimeContext. Mode switches update the active phase without
	// resetting conversation history or workspace artifacts. Nil only in
	// headless/test harnesses that never construct a model.
	orch *orchestrator.Orchestrator

	// Autonomy decision runtime: classifies intent independently from workspace
	// selection, evaluates the autonomy decision model (auto_continue /
	// ask_user / block / direct_response), records session capability grants,
	// and drives the autonomous loop. The UI is a read-only observer: it
	// projects the autonomy events onto the activity log and never mutates the
	// engine. Nil only in headless/test harnesses.
	autonomy *autonomy.Engine
	// pendingAutonomyProposal holds the ask_user decision surface awaiting a
	// human decision. It is the ONLY user-facing authorization gate — Execute
	// issues the session capability grant internally, re-runs the decision and
	// continues execution. Nil when no proposal is outstanding.
	pendingAutonomyProposal *autonomy.Proposal
	// autonomyProposalSelect is the highlighted action index in the proposal
	// menu (Execute / Inspect / Cancel), navigated with ↑/↓.
	autonomyProposalSelect int
	// autonomyProposalInspect toggles the read-only decision-detail view of the
	// pending proposal.
	autonomyProposalInspect bool
	// pendingAutonomyTargets holds the deterministic candidate files when a
	// mutation target is ambiguous (§8). The user selects explicitly; no
	// candidate is ever auto-picked. Nil when no selector is outstanding.
	pendingAutonomyTargets []string
	// autonomyTargetSelect is the highlighted candidate index in the target
	// selector.
	autonomyTargetSelect int
	// pendingAutonomyTargetTrace is the decision trace resumed after the human
	// selects a candidate. The grant already covers the boundary, so selection
	// continues execution without re-authorization.
	pendingAutonomyTargetTrace autonomy.Trace
	// autonomyHotfix marks the current objective as a BUILD/hotfix execution
	// request (e.g. "/build$hot check @index.html and remove redundant
	// content"). While set, the decided BUILD workspace executes with hotfix
	// semantics (deterministic target resolution → targeted patch) instead of
	// the generic plan→build flow. It is cleared once the hotfix pipeline
	// starts.
	autonomyHotfix bool
	// pendingHotfixObjective carries the hotfix tail while the autonomy
	// authorization proposal is outstanding, so Execute can resume the hotfix
	// pipeline on the SAME objective without re-parsing a command.
	pendingHotfixObjective string

	// Patch engine: 4-tier pipeline (Tier 1 structured diff -> Tier 2
	// SEARCH/REPLACE -> Tier 3 whole-file -> Tier 4 human approval) replacing
	// the legacy build patch application. Emits PatchParsed/PatchValidated/
	// PatchRejected/ApprovalRequested on the bus. Nil only in headless/tests.
	patchEngine *patch.Engine

	// Layered pipeline engine (Layers 0-5): knowledge resolution, capability
	// detection, governed context, intent-based model routing and validation.
	// It is attached to the orchestrator; the UI reads the routed model for
	// each mode command. Nil only in headless/test harnesses.
	pipelineEngine *pipeline.Engine

	// ── Fact-only control projection ────────────────────────────────
	// controlSnapshot is the projected Dynamic IR snapshot reconstructed from
	// the fact-only control telemetry stream (control.iteration +
	// control.node_observed). The UI renders the execution tree from these
	// facts and never writes them back: no business logic, retries, or engine
	// state mutations run here.
	controlRunID    string
	controlSnapshot *ir.ExecutionSnapshot
	// controlFactSend bridges fact-only control events into the Bubble Tea
	// event loop. Set to p.Send at bootstrap; nil in headless/test harnesses.
	controlFactSend func(tea.Msg)

	// Current effort level for generation
	currentEffort EffortLevel

	// Clipboard abstraction for /copy and yank. Nil uses the default
	// system clipboard. Tests may inject a fake implementation.
	clipboard Clipboard

	// ── Mouse selection (orthogonal presentation state) ──────────────────
	// Execution State, Viewport State, Selection State and Copy State remain
	// independent concerns. Selection operates on logical records, not terminal
	// byte offsets.
	mouseSel mouseSelection
	// viewportHitMap is the single source of truth for mouse hit-testing.
	// Generated atomically alongside Viewport.SetContent; cached only for
	// the visible window (YOffset .. YOffset+Height) for bounded memory.
	viewportHitMap ViewportHitMap
	fullHitRows    []RowLayout // transient full physical rows before windowing
	// viewportPaneLeft/Top are split-pane offsets for multi-pane geometry.
	// When running inside tmux/Ghostty splits, the viewport does not occupy
	// absolute (0,0) of the terminal; mouse coordinates must be translated
	// relative to these offsets. Tests inject non-zero values to verify
	// relative hit-testing. Zero value means full-screen (no pane offset).
	viewportPaneLeft int
	viewportPaneTop  int
	// frozenHitRows preserves the hitmap while dragging to prevent background
	// ticks from shifting layout. Nil when not dragging.
	frozenFullHitRows []RowLayout
	frozenViewportStr string
	frozenRecords     []record
}

// isProjectInitialized checks whether .izen/ exists AND contains a valid
// config.json on disk. This is the AUTHORITATIVE first-run gate used by
// BuildWorkspace to decide whether to render the onboarding overlay or the
// normal mode workspace. It supersedes any in-memory initStage value.
// activeRouteModel resolves the intent-routed model for the currently active
// mode (via the layered Pipeline Engine router), falling back to the
// configured active model when the pipeline is unavailable. Mode commands use
// this so /plan and /investigate hit reasoning models while /build hits fast
// coding models.
func (m *model) activeRouteModel() string {
	if m == nil || m.resolver == nil {
		return ""
	}
	return m.routeModel(m.resolver.Current().String())
}

// routeModel resolves the intent-routed model for an explicit mode name. It is
// the single seam the UI commands use for intent-based model routing.
func (m *model) routeModel(mode string) string {
	if m == nil {
		return ""
	}
	if m.pipelineEngine != nil {
		return m.pipelineEngine.RouteForMode(mode).Model
	}
	if m.orch != nil && m.orch.Pipeline() != nil {
		return m.orch.Pipeline().RouteForMode(mode).Model
	}
	if m.cfg != nil {
		return m.cfg.ActiveModelName()
	}
	return ""
}

// syncPipelineTiers re-pins the layered pipeline router's per-intent models to
// the current configuration. It MUST be called whenever the active provider or
// model tier changes at runtime (provider switch, /model selection, config
// reload) so intent routing never serves a model that was pinned to a provider
// which is no longer active — an Ollama model leaking into an OpenRouter
// request fails with HTTP 400 "not a valid model ID".
func (m *model) syncPipelineTiers() {
	if m == nil || m.cfg == nil {
		return
	}
	var eng *pipeline.Engine
	switch {
	case m.pipelineEngine != nil:
		eng = m.pipelineEngine
	case m.orch != nil:
		eng = m.orch.Pipeline()
	}
	if eng == nil {
		return
	}
	eng.Router().SyncTiers(func(i pipeline.Intent) (string, string) {
		tier := i.String()
		return m.cfg.ResolveTierModel(tier), m.cfg.ResolveTierProvider(tier)
	})
}

// pipelineFacade returns the layered Pipeline Engine as its narrow
// pipeline.Facade boundary, resolving it from the UI's direct engine or the
// orchestrator. It is the seam Mode UseCases (investigate/review) consume for
// Layer 4 validation and heavy context generation. Nil in headless/test
// harnesses.
func (m *model) pipelineFacade() pipeline.Facade {
	if m == nil {
		return nil
	}
	if m.pipelineEngine != nil {
		return m.pipelineEngine
	}
	if m.orch != nil {
		return m.orch.Pipeline()
	}
	return nil
}

// isProjectInitialized checks whether .izen/ exists AND contains a valid
// config.json on disk. This is the AUTHORITATIVE first-run gate used by
// BuildWorkspace to decide whether to render the onboarding overlay or the
// normal mode workspace. It supersedes any in-memory initStage value. Any
// stat failure (missing, unreadable, ENOTDIR, or a non-directory .izen/)
// reports "not initialized" so the UI can never enter a workspace the disk
// does not back.
func (m *model) isProjectInitialized() bool {
	if m.workspaceRoot == "" {
		return false
	}
	izenDir := filepath.Join(m.workspaceRoot, ".izen")
	fi, err := os.Stat(izenDir)
	if err != nil || !fi.IsDir() {
		return false
	}
	cfgPath := filepath.Join(m.workspaceRoot, ".izen", "config.json")
	fi, err = os.Stat(cfgPath)
	if err != nil || fi.IsDir() {
		return false
	}
	return true
}

// contextPlanner returns the intent-aware Context Planner, constructing it
// lazily from the Phase 3 Lea structural engine, the workspace `.logs/` tee
// directory, and the retrieval search engine (for governed file reads). It is
// nil when no graph source is ready, so every consumer must guard.
// Construction is cheap and idempotent; the planner is retained for the
// session.
func (m *model) contextPlanner() *planner.Planner {
	if m == nil || (m.leaEng == nil && m.graph == nil) {
		return nil
	}
	if m.plannerMu == nil {
		m.plannerMu = &sync.Mutex{}
	}
	m.plannerMu.Lock()
	defer m.plannerMu.Unlock()
	if m.planner != nil {
		return m.planner
	}
	maxTokens := planner.DefaultMaxContextTokens
	if m.cfg != nil && m.cfg.Models.MaxTokens > 0 {
		maxTokens = m.cfg.Models.MaxTokens
	}

	opts := []planner.Option{planner.WithMaxTokens(maxTokens)}

	// Graph source: the Lea structural engine is canonical (symbols, call
	// chains, architecture, routes). The native graph remains a fallback for
	// headless/test harnesses that never attach a Lea engine.
	if m.leaEng != nil {
		opts = append(opts, planner.WithGraphSource(planner.NewLeaAdapter(m.leaEng)))
	} else if m.graph != nil {
		opts = append(opts, planner.WithGraphSource(planner.NewGraphAdapter(m.graph)))
	}

	// File source: the retrieval search engine (Lynx or native) governs file
	// reads so the planner can pull budget-fitted snippets instead of raw
	// whole-file dumps.
	if router := retrieval.GetGlobalRouter(); router != nil {
		opts = append(opts, planner.WithFileSource(planner.NewRetrievalFileAdapter(router)))
	}

	opts = append(opts, planner.WithLogSource(planner.NewTeeLogAdapter(m.workspaceRoot)))

	m.planner = planner.New(opts...)
	return m.planner
}

// planContextForAsk runs the Context Planner for a question and injects the
// budget-fitted context into the content that reaches the LLM. It degrades
// silently (returns the input unchanged) when the planner is unavailable or
// planning yields no chunks. When the planner successfully assembles context,
// askContextGoverned is set so streamCmd skips the ungoverned file-read
// fallback, and lastAskTrace is populated for the UI thought-route panel.
func (m *model) planContextForAsk(line string) string {
	p := m.contextPlanner()
	if p == nil {
		return line
	}
	plan, err := p.Plan(context.Background(), line)
	if err != nil || plan == nil || len(plan.Chunks) == 0 {
		return line
	}
	header := fmt.Sprintf("### PLANNED CONTEXT (%s intent, %d tokens)\n\n",
		plan.Intent, plan.TokenTotal)
	m.askContextGoverned = true
	m.lastAskTrace = planToTrace(plan)
	return header + plan.Assemble() + "\n\n" + line
}

// planToTrace projects a planner ContextPlan onto the UI's CodebaseTrace so the
// thought-route panel shows which files and symbols the planner actually
// surfaced, alongside the budget telemetry. Returns nil when the plan is empty.
func planToTrace(plan *planner.ContextPlan) *ctxpkg.CodebaseTrace {
	if plan == nil || len(plan.Chunks) == 0 {
		return nil
	}
	tr := &ctxpkg.CodebaseTrace{}
	seen := make(map[string]bool)
	for _, c := range plan.Chunks {
		switch c.Source {
		case planner.SourceFile:
			// File chunk content begins with the repository-relative path
			// ("path:line" from FocusedContext or the search hit projection).
			line := strings.TrimSpace(strings.SplitN(c.Content, "\n", 2)[0])
			if path, _, ok := strings.Cut(line, ":"); ok && path != "" && !seen[path] {
				seen[path] = true
				tr.MatchedFiles = append(tr.MatchedFiles, path)
			}
		case planner.SourceGraph:
			if sym, _, ok := strings.Cut(strings.TrimSpace(c.Content), " "); ok && sym != "" && !seen[sym] {
				seen[sym] = true
				tr.ResolvedSymbols = append(tr.ResolvedSymbols, sym)
			}
		}
	}
	if plan.Budget.Total > 0 {
		tr.CompressionRatio = float64(plan.TokenTotal) / float64(plan.Budget.Total)
	}
	return tr
}

// applyToolCallBuffer applies approved tool calls to disk and flushes the buffer.
func (m *model) applyToolCallBuffer() tea.Cmd {
	return func() (msg tea.Msg) {
		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// A panic inside the tool-call disk write must still produce a
		// terminal mutationResultMsg so the spinner can never be orphaned.
		defer func() {
			if r := recover(); r != nil {
				msg = mutationResultMsg{err: fmt.Errorf("tool call apply panic: %v", r)}
			}
		}()

		if m.toolCallBuffer == nil {
			return mutationResultMsg{err: fmt.Errorf("no tool call buffer")}
		}
		results, err := m.toolCallBuffer.ApplyApproved()
		if err != nil {
			return mutationResultMsg{err: err}
		}
		m.toolCallBuffer.Reset()
		return applyAllResultMsg{
			results: func() []mutationResultMsg {
				msgs := make([]mutationResultMsg, 0, len(results.Results))
				for _, r := range results.Results {
					status := "modified"
					if r.IsNew {
						status = "created"
					}
					msgs = append(msgs, mutationResultMsg{file: r.File, status: status})
				}
				return msgs
			}(),
		}
	}
}

// cycleEffort cycles the effort level through Auto → Low → Medium → High.
func (m *model) cycleEffort() {
	switch m.currentEffort {
	case EffortAuto:
		m.currentEffort = EffortLow
	case EffortLow:
		m.currentEffort = EffortMedium
	case EffortMedium:
		m.currentEffort = EffortHigh
	case EffortHigh:
		m.currentEffort = EffortAuto
	}
}

// ── Rendering helpers ─────────────────────────────────────────────────────────

// wrapStreamText wraps raw text lines dynamically during an active live stream.
// It delegates to the shared ANSI-aware, cell-accurate wrapper so the stream
// and the final history render identically.
func wrapStreamText(text string, maxW int) []string {
	return wrapText(text, maxW)
}

// sanitizeInputPrompt forces the text input prompt back to a clean baseline,
// ensuring no orphaned spinner sequences or dynamic artifacts remain embedded
// after any background task termination ($fix, $run, $log, $test, /commit).
// Called defensively by every task-termination message handler in update.go.
func (m *model) sanitizeInputPrompt() {
	m.ti.Prompt = ""
}

// commitTokenUsage folds provider-reported token usage into the session
// counters and the global status.Tracker. It is the single accounting entry
// point for BOTH successful and failed LLM attempts: on failure the provider's
// partial usage (or a character estimate) is still committed so consumed
// tokens are never silently zeroed ("Explicit Over Implicit"). known marks
// whether the provider reported usage at all; unknown usage leaves the
// "usage unknown" state intact so the footer never fabricates a zero.
func (m *model) commitTokenUsage(input, output int) {
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	m.InputTokens += input
	m.OutputTokens += output
	m.TotalTokens = m.InputTokens + m.OutputTokens
	if m.IsCloudModel {
		status.Default.Record(m.InputTokens, m.OutputTokens)
	} else {
		status.Default.Record(input, output)
	}
}

// markUsageKnown records that the provider reported authoritative usage this
// session, transitioning the footer from "usage unknown" to a real count.
func (m *model) markUsageKnown() {
	m.usageKnown = true
}

// tokenUsageCmd returns a command that dispatches the provider-reported token
// usage of an execution path to the Bubble Tea event loop as a TokenUsageMsg.
// The TokenUsageMsg handler in update.go accumulates the counts into the
// session counters and forces syncUIState so the status bar footer refreshes
// the token counters immediately — even when the underlying execution failed,
// was aborted, or was truncated mid-stream. Zero usage produces a nil command
// (nothing was consumed, nothing to report).
func (m *model) tokenUsageCmd(input, output int) tea.Cmd {
	return m.tokenUsageCmdKnown(input, output, input > 0 || output > 0)
}

// tokenUsageCmdKnown is tokenUsageCmd with an explicit known flag: known=true
// even with zero counts means the provider genuinely reported zero usage, so
// the footer renders "0 tok" instead of "usage unknown".
func (m *model) tokenUsageCmdKnown(input, output int, known bool) tea.Cmd {
	if input <= 0 && output <= 0 && !known {
		return nil
	}
	model := ""
	if m.cfg != nil {
		model = m.cfg.ActiveModelName()
	}
	return func() tea.Msg {
		return TokenUsageMsg{
			PromptTokens:     input,
			CompletionTokens: output,
			Model:            model,
			Known:            known,
		}
	}
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
// streaming freeze). ActivityTree entries are NOT populated here —
// they are fed directly from the engine via handleEngineEvent for
// typed events with real I/O metrics.
func (m *model) logActivity(format string, args ...interface{}) {
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the surface is sealed until the next operation or user
	// submission: a late engine activity line from the cleared execution must
	// never repopulate the cleared records.
	if m.activitySurfaceSealed {
		return
	}
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

// logRuntimeDetail writes a runtime lifecycle detail line (strategy, provider,
// token usage, event names) ONLY when the gated execution is in the DEBUG
// layer. In NORMAL and EXPANDED the human narrative panel is the only execution
// surface — internal runtime states are never rendered directly by default.
func (m *model) logRuntimeDetail(format string, args ...interface{}) {
	if m.execVisibility != presentation.VisibilityDebug {
		return
	}
	m.logActivity(format, args...)
}

// handleEngineEvent receives typed event payloads from the execution
// and retrieval packages and appends them to the ActivityTree. This is the
// ONLY path that populates ActivityTree — no string-parsing, no hardcoded
// entries. The event interface{} is type-asserted to known struct types
// from each engine package and converted to the canonical EngineEvent.
func (m *model) handleEngineEvent(ev interface{}) {
	if m.activityTree == nil {
		return
	}
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the structured activity tree is cleared and sealed; a late
	// typed engine I/O event (file read/mutate/search/resolve) from the cleared
	// execution must not re-append to it.
	if m.activitySurfaceSealed {
		return
	}
	// Type-assert to known event structs from engine packages.
	switch e := ev.(type) {
	case retrieval.FileReadEvent:
		m.activityTree.Append(EngineEvent{
			Kind: EventFileRead,
			Time: time.Now(),
			FileRead: &FileReadEvent{
				File:    e.File,
				Bytes:   e.Bytes,
				Elapsed: e.Elapsed,
			},
		})
	case retrieval.SearchEvent:
		m.activityTree.Append(EngineEvent{
			Kind: EventSearch,
			Time: time.Now(),
			Search: &SearchEvent{
				Query: e.Query,
				Hits:  e.Hits,
			},
		})
	case retrieval.ResolveEvent:
		m.activityTree.Append(EngineEvent{
			Kind: EventResolve,
			Time: time.Now(),
			Resolve: &ResolveEvent{
				Symbol: e.Symbol,
				Hits:   e.Hits,
			},
		})
	case execution.FileMutateEvent:
		m.activityTree.Append(EngineEvent{
			Kind: EventFileMutate,
			Time: time.Now(),
			FileMutate: &FileMutateEvent{
				File:     e.File,
				LinesAdd: e.LinesAdd,
				LinesDel: e.LinesDel,
				Elapsed:  e.Elapsed,
			},
		})
	case execution.CommandExecEvent:
		// Shell commands arrive as a running event (exitCode < 0) followed by
		// a terminal event (real exit code + output). AppendOrUpdateExec keeps
		// them as ONE tree line: the running entry flips to done with the exit
		// status and the accumulated output for Ctrl+O expansion.
		m.activityTree.AppendOrUpdateExec(e.Command, e.ExitCode, e.Elapsed, e.Output)
	}
}

// handleDomainEvent is the event bus projection. Every DomainEvent published
// by the headless mode engines is rendered here as a styled activity line (and
// enriched ActivityTree entry where metrics exist). This is the ONLY path that
// surfaces engine events in the viewport — engines never call the UI directly.
func (m *model) handleDomainEvent(ev events.DomainEvent) {
	if ev == nil {
		return
	}
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the surface is sealed until the next operation or user
	// submission: a late domain event from the cleared execution (activity
	// line, engine telemetry, reasoning stream) must never resurrect stale
	// activity. Engine-side workflow state (the WorkflowStateMachine) is
	// untouched — only the UI projection is dropped.
	if m.activitySurfaceSealed {
		return
	}
	// ── SINGLE EXECUTION-VIEW PROJECTION (Phase 4) ────────────────
	// Every canonical runtime lifecycle event advances the execution-view
	// projection. The renderer for the gated path reads ONLY this projection's
	// state — it never invents execution truth, and a terminal event ALWAYS
	// transitions it into a terminal phase (no stale spinner after success,
	// failure, or cancellation).
	if m.execView != nil {
		m.execView.Project(ev)
	}
	switch p := ev.Payload().(type) {
	case events.CommandReceivedPayload:
		m.logActivity("[%s] received command: %s", p.Mode, truncateForActivity(p.Command))
	case events.IntentParsedPayload:
		m.logActivity("[intent] parsed: %s (%.0f%% confidence)", p.Intent, p.Confidence*100)
	case events.PlanStagedPayload:
		m.logActivity("[plan] staged %d tasks", p.TaskCount)
	case events.PatchAttemptedPayload:
		m.logActivity("[build] patch attempt %d: %s (%s)", p.Attempt, p.File, p.Strategy)
	case events.PatchAppliedPayload:
		m.logActivity("[build] applied patch to %s (+%d/-%d lines)", p.File, p.LinesAdd, p.LinesDel)
		if m.activityTree != nil {
			m.activityTree.Append(EngineEvent{
				Kind: EventFileMutate,
				Time: ev.Timestamp(),
				FileMutate: &FileMutateEvent{
					File:     p.File,
					LinesAdd: p.LinesAdd,
					LinesDel: p.LinesDel,
					Elapsed:  p.Elapsed,
				},
			})
		}
	case events.ExecutionFailedPayload:
		m.logActivity("[error] %s", p.Error)
		// A terminal failure event is authoritative execution truth: it must
		// release the loading state, spinner, and pending operation.
		m.clearExecutionLoading(OpOutcomeFailure)
	case events.SelfHealingAttemptPayload:
		// Distinct retry badge + attempt count + failure category so the
		// self-healing loop reads as one clean, scannable line.
		m.logActivity("[RETRY %d] [%s] %s", p.Retry, p.Category, p.File)
	case events.SelfHealingExhaustedPayload:
		// Distinct exhausted badge + total attempt count; the raw verification
		// output is collapsed to its first line to keep the activity line tight.
		m.logActivity("[EXHAUSTED] self-healing stopped after %d attempt(s); workspace rolled back clean%s",
			p.Attempts, selfHealOutputSuffix(p.Output))
	case events.StageCompletedPayload:
		m.logActivity("[stage] %s completed (%s)", p.Stage, p.Summary)
	case events.ExecutionStartedPayload:
		m.logRuntimeDetail("[runtime] execution started: %s (mode %s)", truncateForActivity(p.Prompt), p.Mode)
	case events.StrategySelectedPayload:
		m.logRuntimeDetail("[runtime] strategy selected: %s", p.Strategy)
	case events.TargetResolvedPayload:
		// The authoritative stage is driven from the real runtime boundary: the
		// resolved target the runtime actually touches. Never a fabricated label.
		m.setStage("target", p.Target, stageRunning)
		m.logRuntimeDetail("[runtime] target resolved: %s (exists=%t, %s)", p.Target, p.Exists, p.Source)
	case events.ContextPreparedPayload:
		m.setStage("context", "", stageRunning)
		m.logRuntimeDetail("[runtime] context prepared: %d channel(s), ~%d tokens", len(p.Channels), p.Tokens)
	case events.ModelInvokedPayload:
		m.setStage("model", p.Model, stageWaiting)
		m.logRuntimeDetail("[runtime] model invoked: %s", p.Model)
	case events.ProviderWaitingPayload:
		// provider.waiting: the round-trip is in flight before the first byte —
		// the truthful provider state, never a fabricated "thinking" claim.
		m.setStage("model", p.Model, stageWaiting)
		m.logRuntimeDetail("[runtime] provider waiting: %s", p.Model)
	case events.ProviderFirstTokenPayload:
		// provider.first_token: real provider bytes are arriving.
		m.setStage("model", p.Model, stageStreaming)
		m.logRuntimeDetail("[runtime] provider first token: %s (%s)", p.Model, p.Latency.Round(time.Millisecond))
	case events.ProviderStreamDeltaPayload:
		// Evidence transport only: the provider is actively streaming. No
		// per-delta activity line (noise); the state is kept truthful.
		m.setStage("model", m.getActiveModelName(), stageStreaming)
	case events.ProviderUsageUpdatePayload:
		// Authoritative provider-reported usage during the live stream — only
		// real counts reach the indicator, never a character-count estimate.
		m.setStage("model", p.Model, stageStreaming)
		m.setStageMetrics(0, 0, p.OutputTokens)
		m.logRuntimeDetail("[runtime] provider usage: %d in / %d out", p.InputTokens, p.OutputTokens)
	case events.ReasoningTelemetryPayload:
		// Reasoning TELEMETRY only (duration + token count). Raw chain-of-
		// thought never crosses this boundary.
		m.logRuntimeDetail("[runtime] reasoning: %s (%d tok)", p.Duration.Round(time.Millisecond), p.Tokens)
	case events.ProviderResponsePayload:
		m.setStage("model", p.Model, stageDone)
		m.logRuntimeDetail("[runtime] provider response: %s (%d tok in / %d tok out)", p.Model, p.TokenInput, p.TokenOutput)
	case events.ArtifactProducedPayload:
		m.setStage("patch", p.Target, stageRunning)
		m.logRuntimeDetail("[runtime] artifact produced: %s (%s)", p.Kind, p.Target)
	case events.MutationStartedPayload:
		target := ""
		if len(p.Targets) > 0 {
			target = p.Targets[0]
		}
		m.setStage("apply", target, stageRunning)
		m.logRuntimeDetail("[runtime] mutation started: %d target(s)", len(p.Targets))
	case events.MutationCompletedPayload:
		m.setStage("apply", p.Target, stageDone)
		m.logRuntimeDetail("[runtime] mutation completed: %s (%s)", p.Target, p.Outcome)
	case events.VerificationCompletedPayload:
		m.setStage("validate", "", stageDone)
		m.logRuntimeDetail("[runtime] verification %s: %d step(s)", verificationTick(p.Passed), len(p.Steps))
	case events.ExecutionFinishedPayload:
		m.logRuntimeDetail("[runtime] execution finished: success=%t (%s)", p.Success, p.Outcome)
		// A terminal finished event is authoritative execution truth: it must
		// release the loading state, spinner, and pending operation regardless
		// of whether the result message has arrived yet.
		outcome := OpOutcomeSuccess
		if !p.Success {
			outcome = OpOutcomeFailure
		}
		if p.Outcome == string(execution.OutcomeCancelled) || p.Outcome == string(execution.OutcomeRejected) {
			outcome = OpOutcomeCancelled
		}
		m.clearExecutionLoading(outcome)
	case events.ApprovalRequiredPayload:
		m.logRuntimeDetail("[runtime] approval required: %s", p.Target)
	case events.ApprovalRejectedPayload:
		// The human explicitly rejected the held proposal — a real lifecycle
		// transition, distinct from a cancellation.
		m.logRuntimeDetail("[runtime] approval rejected: %s", p.Target)
	case events.IntentClassifiedPayload:
		// Hybrid Intent Gateway classification outcome projected onto the
		// activity log. Ambiguity is surfaced so the operator sees WHY the UI
		// is asking for disambiguation instead of acting.
		if p.ConfirmationRequired {
			m.logActivity("[intent] ambiguous: /%s (%.0f%%, %s) — asking user", p.Intent, p.Confidence*100, p.Explanation)
		} else {
			m.logActivity("[intent] classified: /%s (%.0f%%, %s)", p.Intent, p.Confidence*100, p.Explanation)
		}
	case events.PhaseChangedPayload:
		m.logActivity("[phase] %s → %s", p.From, p.To)
		// The presentation state is a pure projection of the canonical
		// workflow phase: derive, never hand-set.
		if m.viewState != nil {
			m.viewState.Project(ev)
		}
		m.syncUIState()
	case events.PatchParsedPayload:
		m.logActivity("[patch] parsed %s (strategy=%s, tier=%d)", p.File, p.Strategy, p.Tier)
	case events.PatchValidatedPayload:
		m.logActivity("[patch] validated %s (strategy=%s, tier=%d)", p.File, p.Strategy, p.Tier)
	case events.PatchRejectedPayload:
		m.logActivity("[patch] rejected %s (tier %d): %s", p.File, p.Tier, truncateForActivity(p.Reason))
	case events.ApprovalRequestedPayload:
		// Tier 4 Human-in-the-Loop fallback or gateway disambiguation surfaced
		// as a distinct approval activity line.
		target := p.Target
		if target == "" {
			target = "intent disambiguation"
		}
		m.logActivity("[approval] requested for %s: %s", target, truncateForActivity(p.Reason))
		// A Tier 4 approval request is projected onto the derived presentation
		// state: the WorkflowStateMachine holds the pending-approval truth and
		// the UI derives StateAwaitingApproval from it.
		if m.viewState != nil {
			m.viewState.Project(ev)
		}
		m.enterApprovalState()
	case events.ActivityPayload:
		// Engine activity telemetry (retrieval/execution/investigate sinks)
		// projected onto the UI goroutine through the bus.
		m.logActivity("%s", p.Line)
	case events.StreamUsagePayload:
		// "Explicit Over Implicit": an interrupted LLM stream reports its
		// partial token usage so consumed tokens never vanish from telemetry.
		statusWord := "interrupted"
		if !p.Interrupted {
			statusWord = "finished"
		}
		m.logActivity("[stream] %s: %s tok input + %s tok output (%s)", statusWord,
			status.FormatTokens(p.InputTokens), status.FormatTokens(p.OutputTokens), truncateForActivity(p.Reason))
	case events.EngineTelemetryPayload:
		// Typed engine I/O event wrapped for bus transport — projected into
		// the structured ActivityTree.
		m.handleEngineEvent(p.Event)
	case events.ReasoningPayload:
		// LLM reasoning stream: chunks are appended to the dedicated thinking
		// buffer (never the response pipeline); the terminal event collapses
		// the box into compact mode.
		m.handleReasoningStream(p.Chunk, p.IsComplete)
	case events.AutonomyDecisionPayload:
		// Autonomy decision runtime verdict: the canonical record of when the
		// runtime continues, asks, blocks or answers directly. Distinct visual
		// markers per verdict keep the autonomy surface scannable.
		marker := "◆"
		switch p.Decision {
		case "auto_continue":
			marker = "▶"
		case "ask_user":
			marker = "◈"
		case "block":
			marker = "■"
		case "direct_response":
			marker = "◇"
		}
		ws := p.Workspace
		if ws == "" {
			ws = "none"
		}
		if p.Decision == "direct_response" {
			m.logActivity("[autonomy] %s direct response — no workspace", marker)
		} else {
			m.logActivity("[autonomy] %s %s → workspace %s (risk %s, %.0f%%): %s",
				marker, p.Decision, ws, p.Risk, p.Confidence*100, truncateForActivity(p.Reason))
		}
		if len(p.MissingCapabilities) > 0 {
			m.logActivity("[autonomy] requesting capability: %s",
				strings.Join(p.MissingCapabilities, "+"))
		}
	case events.CapabilityGrantedPayload:
		// A session capability grant: one grant authorizes every operation in
		// its scope — the "no repeated approvals" guarantee.
		m.logActivity("[grant] %s: %s granted for %s (expires %s)",
			p.GrantID, strings.Join(p.Capabilities, "+"), p.Scope, orNever(p.ExpiresAt))
	case events.LoopTransitionPayload:
		// One step of the autonomous loop. Failure transitions are shown like
		// any other: the loop produces diagnosis, never termination.
		m.logActivity("[loop] %s → %s (%s): %s",
			p.From, p.To, p.Event, truncateForActivity(p.Reason))
	case events.ContextCompiledPayload:
		// The context intelligence layer compiled a structural understanding
		// of an artifact: findings, not raw bytes.
		m.logActivity("[context] compiled %s (%s): %d finding(s)",
			p.Path, p.Kind, p.FindingCount)
	case events.DecisionSurfacePayload:
		// The TYPED proposal payload of a Zero-Token DecisionSurface. The
		// runtime published it BEFORE parking, so the UI can project it as a
		// structured line (never inferred from a log string). The interactive
		// recovery surface itself renders from the authoritative boundary the
		// driver parks with — this projection is observability + the guarantee
		// that awaiting_human always has a published decision surface.
		m.logActivity("[preflight] decision surface: %s — %s (%d option(s))",
			p.Target, truncateForActivity(p.Reason), len(p.Options))
	case events.DecisionSurfaceLifecyclePayload:
		m.logActivity("[preflight] decision_surface.%s: %s", p.State, truncateForActivity(p.Reason))
	case events.PreflightEventPayload:
		m.logActivity("[preflight] %s: %s (target=%s est=%d max=%d)",
			p.State, truncateForActivity(p.Reason), p.Target, p.EstimatedTokens, p.MaxOutputTokens)
	case events.AutonomousLifecyclePayload:
		m.logActivity("[autonomy] %s: %s", strings.TrimPrefix(ev.Type(), "autonomous."), truncateForActivity(p.Reason))
	}
}

// clearExecutionLoading terminates the gated-execution loading projection
// (in-flight marker + busy flags + pending operation) when a terminal execution
// event arrives from the runtime. It is the projection-layer counterpart of
// finalizeOperation: the runtime emits execution.finished / execution.failed /
// cancelled as authoritative truth and the UI clears its loading state from
// those events — the gated execution's loading state can never outlive a
// terminal execution event.
//
// It is gated on the execution in-flight marker so a terminal runtime event can
// never clobber an unrelated operation's spinner: the marker is cleared the
// moment a new operation supersedes the gated execution or any terminal cleanup
// runs, making this a no-op everywhere except the genuine resolving phase.
func (m *model) clearExecutionLoading(outcome OperationOutcome) {
	if !m.executionResolving {
		return
	}
	m.finalizeOperation(outcome, nil)
}

// ── Derived UI state projection ──────────────────────────────────────────────
// StateAwaitingApproval and StateProcessing are DERIVED presentation states:
// they are computed through presentation.DeriveUIState from the canonical
// WorkflowStateMachine (phase + pending-approval gate) and the workflow event
// stream (EventPhaseChanged / EventApprovalRequested). The UI never hand-sets
// the approval state; it records the approval gate on the canonical source
// (MarkApprovalPending/Resolved) and re-derives.

// enterApprovalState freezes the workflow pending-approval gate and derives the
// UI state from it. The WorkflowStateMachine is the single source of truth for
// proposals awaiting human approval.
func (m *model) enterApprovalState() {
	if m.workflowSM != nil {
		m.workflowSM.MarkApprovalPending()
	}
	m.syncUIState()
}

// resolveApprovalState clears the workflow pending-approval gate and re-derives
// the presentation state. Approve/reject/cancel all funnel here so no path can
// leave the canonical gate and the UI state out of sync.
func (m *model) resolveApprovalState() {
	if m.workflowSM != nil {
		m.workflowSM.MarkApprovalResolved()
	}
	if m.viewState != nil {
		m.viewState.ResolveApproval()
	}
	m.syncUIState()
}

// unwindBuildFailure releases a failed build execution back to interactive
// state. It is the "Human-Centered / Reversible" guarantee for failed commands:
// a stream/engine failure (HTTP 400/500, network error) must never trap the
// user in the build phase — every later prompt would then be rejected with
// "workflow: transition from build to ask: moving to a previous phase is not
// permitted" and the session would be unrecoverable.
//
// It performs a deterministic recovery:
//  1. Release any outstanding approval gate (build failures can arrive while a
//     proposal freeze is pending).
//  2. Reset the core WorkflowStateMachine to StateIdle so /ask, /plan and
//     /build are all reachable again.
//  3. Reset the Application-layer domain WorkflowRuntime to PhaseAsk so
//     submit_prompt / switch_mode handlers never reject a recovery prompt.
//  4. Re-derive the presentation state to interactive StateChat and restore
//     keyboard focus.
func (m *model) unwindBuildFailure() {
	m.resolveApprovalState()
	// Drop any in-flight approval/patch state so the viewport returns to chat.
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil
	m.pendingHotfixTask = nil
	m.pendingHotfixPatch = nil
	m.hotfixCandidatesMode = false
	m.acceptAll = false

	m.clearAutonomyProposal()
	if m.workflowSM != nil {
		// From StateBuilding/StateFailed/StateRepairing the canonical exit is
		// a reset back to StateIdle, from which every forward phase is reachable.
		_ = m.workflowSM.SendEvent(workflow.EventReset, workflow.TransitionContext{})
	}
	if m.workflowRT != nil {
		m.workflowRT.Reset()
	}
	m.syncUIState()
	m.ti.Focus()
}

// handleEmergencyInterrupt is the unblockable emergency escape hatch. It is
// invoked from the very top of Update, BEFORE any state gating, so a stuck
// processing/approval state can NEVER swallow the keyboard (Philosophy Rule 1:
// Human-Centered / Reversible, Rule 3: Explicit Over Implicit).
//
// It performs a full deterministic reset:
//  1. Cancels every in-flight background context (ghost-loop prevention).
//  2. Clears every transient processing flag via reconcileSpinner so the
//     spinner can never stay up on a phantom producer.
//  3. Releases any outstanding approval gate on the canonical workflow source.
//  4. Drops in-flight approval/patch state so the viewport returns to chat.
//  5. Re-derives the presentation state to interactive StateChat.
func (m *model) handleEmergencyInterrupt(reason string) (tea.Model, tea.Cmd) {
	// 0. Cancel the authoritative operation context FIRST so provider calls
	// and subprocesses spawned under the active operation observe the
	// cancellation immediately (Section 6: context propagation).
	if m.activeOp != nil {
		m.activeOp.Cancel()
	}
	// 1. Cancel every in-flight background context (ghost-loop prevention).
	m.cancelAllBackgroundContexts()
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	if m.shellCancel != nil {
		m.shellCancel()
		m.shellCancel = nil
	}
	execution.KillAllOrphans()

	// For active autonomous runs, the canonical terminal path is the
	// autonomousRunMsg returned by the driver when its context is cancelled.
	// We must NOT finalize the operation here — that would race with the
	// driver's terminal message and leave the presentation in an inconsistent
	// state (spinner stopped, but autonomousActive not released).
	// The autonomous run's terminal message will call finalizeOperation.
	if !m.autonomousActive {
		// 1b. Release the active-operation ownership and clear the transient
		// busy flags + spinner through the single authoritative finalization
		// path. This is ONLY for non-autonomous operations.
		m.finalizeOperation(OpOutcomeCancelled, nil)

		// 2. Clear every transient processing flag so the spinner can never
		// stay up and the view can never block on a phantom producer.
		m.reconcileSpinner()

		// 2b. Re-derive the presentation state from the cleared flags so the
		// tick spinner loop halts and the viewport unwinds to interactive
		// chat. Any residual approval gate is overridden below.
		m.syncUIState()
	}

	// 3. Release any outstanding approval gate on the canonical source.
	m.resolveApprovalState()

	// 4. Drop in-flight approval/patch state so the viewport returns to chat.
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.pendingBuildApproval = false
	m.pendingBuildTask = nil
	m.pendingHotfixTask = nil
	m.pendingHotfixPatch = nil
	m.hotfixCandidatesMode = false
	m.acceptAll = false

	m.clearAutonomyProposal()
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reject()
	}

	// 4b. Abort any in-flight hotfix: restore the stashed plan and clear the
	// hotfixActive flag so a subsequent buildResultMsg can NEVER wrongly
	// trigger the plan-restore branch for a hotfix that was interrupted before
	// completion. Mirrors the Alt+R rejection path in keys.go.
	if m.hotfixActive {
		if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
			m.sess.StageTaskList(&stashedTasks)
			_ = m.sess.Save()
		}
		m.hotfixActive = false
	}

	// 5. Restore interactive input and force the presentation back to chat.
	// For autonomous runs, this will be done by handleAutonomousRun when the
	// terminal message arrives. For non-autonomous, do it now.
	if !m.autonomousActive {
		m.ti.Focus()
		m.recalcViewportHeight()
		m.state = StateChat

		m.push(roleSystem, infoStyle.Render("Interrupted."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
	}

	// 5b. Abort any parked autonomous run. The driver holds its own loop
	// state (no worker is blocked); Abort terminates it as a permanent human
	// cancellation and the terminal message projects through the normal
	// autonomousRunMsg path.
	var extra []tea.Cmd
	if m.autonomousDriver != nil && m.autonomousBoundary != nil {
		driver := m.autonomousDriver
		m.clearAutonomousRun()
		extra = append(extra, func() tea.Msg {
			term, err := driver.Abort(reason + " interrupt")
			return autonomousRunMsg{term: term, err: err}
		})
	}

	return m, tea.Batch(append(extra,
		func() tea.Msg { return TaskFinishedMsg{} },
		m.runtimeCancelCmd(reason+" interrupt"),
	)...)
}

// syncUIState projects the canonical workflow state onto the presentation
// state. It is the single place the approval presentation state is derived.
//
// StateAwaitingApproval is strictly derived from the canonical pending-approval
// gate. StateProcessing is derived from the active workflow phase only while a
// transient operation is genuinely in flight — a mode phase persists across
// operations, so it cannot gate the input line by itself. Otherwise the view
// rests in StateChat.
func (m *model) syncUIState() {
	if m == nil {
		return
	}
	// ── AUTONOMY PROPOSAL GATE ───────────────────────────────────────
	// The ask_user proposal is a pending human decision: it freezes the
	// interaction into StateAwaitingApproval so the keyboard routes to the
	// proposal (↑/↓ + Enter, Esc), independent of the workflow state machine.
	if m.pendingAutonomyProposal != nil {
		m.state = presentation.StateAwaitingApproval
		return
	}
	// ── AUTONOMY TARGET SELECTOR GATE (§8) ─────────────────────────
	// An ambiguous mutation target is a pending human decision: it freezes the
	// interaction into StateAwaitingApproval so the keyboard routes to the
	// candidate selector (↑/↓ + Enter, Esc).
	if len(m.pendingAutonomyTargets) > 0 {
		m.state = presentation.StateAwaitingApproval
		return
	}
	phase := ""
	approval := false
	if m.workflowSM != nil {
		phase = m.workflowSM.State().String()
		approval = m.workflowSM.PendingApproval()
	}
	if m.viewState != nil {
		m.viewState.Sync(phase, approval)
	}
	busy := m.isWorkflowBusy()
	if approval {
		// Approval ALWAYS overrides any residual processing signal: a plan
		// awaiting approval must hand control to the user even if a
		// background process forgot to clear its transient flags.
		m.state = presentation.DeriveUIState(phase, true, busy)
		return
	}
	if busy {
		m.state = presentation.DeriveUIState(phase, false, true)
		return
	}
	// Resting in a mode phase must never gate the input line by itself —
	// a persistent phase is NOT an in-flight operation.
	m.state = StateChat
}

// isWorkflowBusy reports whether a transient workflow operation (stream, agent,
// review, pipeline or shell command) is in flight. It feeds the derived
// UIState so Processing is only derived while an operation is genuinely
// running.
func (m *model) isWorkflowBusy() bool {
	return m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.shellRunning
}

// fastTrackCoversAllPlanTargets reports whether the active fast-track patch
// batch covered every FILE_MUTATE / GIT_ACTION plan task target. When true,
// per-task execution is redundant work on already-applied files and the build
// MUST complete immediately (Rule "Explicit Over Implicit"): the loop must
// never fall through to "executing step N" after a full fast-track batch.
func (m *model) fastTrackCoversAllPlanTargets() bool {
	if m.sess == nil || len(m.fastTrackTargets) == 0 {
		return false
	}
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Type != "FILE_MUTATE" && t.Type != "GIT_ACTION" {
			return false
		}
		if !m.fastTrackTargets[t.Target] {
			return false
		}
	}
	return true
}

// markAllPlanTasksCompleted flips every plan task status to "completed" so the
// build queue is fully drained and verification/handoff can run. It is used
// after a fast-track batch covers all plan targets, where per-task execution
// is skipped entirely.
func (m *model) markAllPlanTasksCompleted() {
	if m.sess == nil {
		return
	}
	tasks := m.sess.CurrentTasks
	changed := false
	for i := range tasks {
		if tasks[i].Status != "completed" {
			tasks[i].Status = "completed"
			changed = true
		}
	}
	if changed {
		m.sess.StageTaskList(&tasks)
		_ = m.sess.Save()
	}
}

// completeFastTrackBuild is the build completion sequence invoked when a
// fast-track batch covered every plan target: it drains the queue, clears
// the patching spinner and restores interactive input focus. Transaction
// commit authority is owned by the RuntimeExecutor approval boundary — the
// UI performs no execution-engine commit here.
// It returns the verification command so the build transitions to complete.
func (m *model) completeFastTrackBuild() (tea.Model, tea.Cmd) {
	m.markAllPlanTasksCompleted()
	m.fastTrackTargets = nil
	// Release any residual patching/agent flags so the derived presentation
	// state unwinds to interactive StateChat instead of a stuck spinner.
	m.agentRunning = false
	m.agentDone = true
	m.agentLabel = ""
	m.stopShimmer()
	m.awaitingConfirmation = false
	m.acceptAll = false
	m.pendingProposals = nil
	m.resolveApprovalState()
	m.ti.Focus()
	m.recalcViewportHeight()
	m.buildVerifyPending = true
	m.push(roleSystem, "Verifying build...")
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	flush := m.flushPendingRecords()
	return m, tea.Batch(flush, m.runTestEngine("./..."))
}

// handlePresentationEvent projects one Application-layer PresentationEvent
// onto the view. This is the ONLY path the UI renders translated workflow
// state: it consumes the decoupled payload (severity + summary + target) and
// never inspects the original domain event. Only ever runs on the UI
// goroutine, so all model mutation here is race-free.
func (m *model) handlePresentationEvent(ev appruntime.PresentationEvent) {
	// ── DEDUPLICATION (Rule 4: one execution path) ─────────────────────
	// IntentClassified / PhaseChanged / ApprovalRequested are ALSO subscribed
	// as raw domain events (program.go), which carry the structured payloads
	// the UI projects (viewState, approval gate). Rendering both the raw line
	// and the translated line would duplicate every one of them. The translated
	// presentation event is therefore dropped for these three types — the raw
	// projection is the single render.
	switch ev.Type {
	case appruntime.PresentationIntentClassified,
		appruntime.PresentationPhaseChanged,
		appruntime.PresentationApprovalRequested:
		return
	}
	switch ev.Severity {
	case appruntime.SeveritySuccess:
		m.logActivity("%s", successBannerStyle.Render(ev.Summary))
	case appruntime.SeverityWarning:
		m.logActivity("%s", warningBannerStyle.Render(ev.Summary))
	case appruntime.SeverityError:
		m.logActivity("%s", errorStyle.Render(ev.Summary))
	default:
		m.logActivity("%s", infoStyle.Render(ev.Summary))
	}
	if ev.Target != "" && ev.Target != ev.Summary {
		m.logActivity("  %s", dimmedStyle.Render(ev.Target))
	}
}

// handleControlFact is the fact-only control telemetry projection. It is a
// pure view-model fold: control.iteration and control.node_observed facts
// update the reconstructed Dynamic IR snapshot that the execution tree renders
// from. No business logic, retry, or engine state mutation happens here — the
// facts are read-only and the projected snapshot is never written back. Only
// ever runs on the UI goroutine, so all model mutation here is race-free.
func (m *model) handleControlFact(ev telemetry.Event) {
	if ev == nil {
		return
	}
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the fact-only execution tree is cleared and sealed; a late
	// control fact from the cleared execution must not rebuild it.
	if m.activitySurfaceSealed {
		return
	}
	switch ev.Type() {
	case telemetry.EventControlIteration:
		p, ok := ev.Payload().(*telemetry.ControlIterationPayload)
		if !ok {
			return
		}
		m.applyControlIteration(p)
	case telemetry.EventControlNodeObserved:
		p, ok := ev.Payload().(*telemetry.ControlNodeObservedPayload)
		if !ok {
			return
		}
		m.applyControlNodeObserved(p)
	}
	m.refreshViewportContent()
	if m.Ready && !m.userIsScrollingUp {
		m.Viewport.GotoBottom()
	}
}

// applyControlIteration folds one control.iteration fact (per-node states +
// attempt counts) into the projected Dynamic IR snapshot.
func (m *model) applyControlIteration(p *telemetry.ControlIterationPayload) {
	m.ensureControlSnapshot(p.RunID)
	for id, state := range p.NodeStates {
		m.controlSnapshot.NodeStates[id] = ir.NodeState(state)
	}
	for id, attempts := range p.Attempts {
		m.controlSnapshot.AttemptCounts[id] = attempts
	}
}

// applyControlNodeObserved folds one control.node_observed fact (a single node
// outcome) into the projected Dynamic IR snapshot. The observation is the
// definitive record of the executed attempt, so the projected glyph reflects it
// immediately rather than waiting for the next iteration.
func (m *model) applyControlNodeObserved(p *telemetry.ControlNodeObservedPayload) {
	m.ensureControlSnapshot(p.RunID)
	m.controlSnapshot.LastObservation[p.NodeID] = ir.ObservationPayload{
		NodeID:     p.NodeID,
		OK:         p.OK,
		Err:        p.Err,
		SkipReason: p.SkipReason,
		Output:     p.Output,
		Timestamp:  p.Timestamp,
	}
	switch {
	case p.OK || p.SkipReason != "":
		m.controlSnapshot.NodeStates[p.NodeID] = ir.StateSuccess
	default:
		m.controlSnapshot.NodeStates[p.NodeID] = ir.StateFailed
	}
}

// ensureControlSnapshot lazily allocates the projected Dynamic IR snapshot and
// pins its run id to the latest control fact.
func (m *model) ensureControlSnapshot(runID string) {
	if m.controlSnapshot == nil {
		m.controlSnapshot = &ir.ExecutionSnapshot{
			ID:              runID,
			NodeStates:      make(map[string]ir.NodeState),
			LastObservation: make(map[string]ir.ObservationPayload),
			AttemptCounts:   make(map[string]int),
			Variables:       ir.Variables{},
		}
		m.controlRunID = runID
		return
	}
	if runID != "" {
		m.controlRunID = runID
		m.controlSnapshot.ID = runID
	}
	if m.controlSnapshot.NodeStates == nil {
		m.controlSnapshot.NodeStates = make(map[string]ir.NodeState)
	}
	if m.controlSnapshot.LastObservation == nil {
		m.controlSnapshot.LastObservation = make(map[string]ir.ObservationPayload)
	}
	if m.controlSnapshot.AttemptCounts == nil {
		m.controlSnapshot.AttemptCounts = make(map[string]int)
	}
}

// handleReasoningStream projects one EventReasoningStream event onto the
// thinking buffer. Chunks are appended verbatim; the terminal event (empty
// chunk + IsComplete) collapses the box. Only ever runs on the UI goroutine.
func (m *model) handleReasoningStream(chunk string, isComplete bool) {
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the thinking buffer is cleared and sealed; a late reasoning
	// chunk from the cleared execution must not resurrect the thinking block.
	if m.activitySurfaceSealed {
		return
	}
	if m.thinkingBuffer == nil {
		m.thinkingBuffer = NewThinkingBuffer()
	}
	if chunk != "" {
		m.thinkingBuffer.Append(chunk)
	}
	if isComplete {
		m.thinkingBuffer.MarkComplete()
	}
	m.refreshViewportContent()
	if m.Ready && !m.userIsScrollingUp {
		m.Viewport.GotoBottom()
	}
}

// thoughtUpdateCmd returns a command that dispatches one raw LLM chunk to the
// Bubble Tea event loop as a ThoughtBufferUpdatedMsg. The handler appends the
// chunk to the active ThinkingBuffer (the Ctrl+O thought drawer) in real time
// so NO model output is discarded; Done marks the thought block complete.
func (m *model) thoughtUpdateCmd(content string, done bool) tea.Cmd {
	if content == "" && !done {
		return nil
	}
	return func() tea.Msg {
		return ThoughtBufferUpdatedMsg{Content: content, Done: done}
	}
}

// selfHealOutputSuffix collapses a self-healing verification output to a short
// trailing hint. Empty or whitespace-only output yields no suffix; otherwise
// the first meaningful line is appended, truncated to a single line width.
func selfHealOutputSuffix(output string) string {
	output = strings.ReplaceAll(output, "\\n", "\n")
	output = strings.ReplaceAll(output, "\\t", "\t")
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// ANSI-safe truncation: never byte-slice, or a style sequence (or a
		// multi-byte rune) can be cut mid-way and drop visible text.
		return " — " + truncateANSI(line, 64)
	}
	return ""
}

// truncateForActivity bounds free-form event text for single-line rendering.
func truncateForActivity(s string) string {
	s = strings.TrimSpace(s)
	return truncateANSI(s, 90)
}

// verificationTick renders the verifier's real verdict as a truthful glyph.
// "passed"/"failed" only ever appear from a real VerificationCompleted event.
func verificationTick(passed bool) string {
	if passed {
		return "passed"
	}
	return "failed"
}

// orNever renders an RFC3339 expiry as "never" when empty.
func orNever(ts string) string {
	if ts == "" {
		return "never"
	}
	return ts
}

// push appends a record. Records are flushed to the terminal's native
// scrollback at explicit sync points (user submit, stream done, etc.).
func (m *model) push(r role, text string) {
	// ── ACTIVITY-SURFACE SEAL ─────────────────────────────────────
	// After /clear the surface is sealed until the next operation or user
	// submission: a terminal result record pushed by the cleared execution
	// must never repopulate the cleared records. submitEnter reopens the
	// surface before any user-initiated interaction, so interactive messages
	// always render.
	if m.activitySurfaceSealed {
		return
	}
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
// AI records go through renderAIResponseBlocks, and everything else is wrapped
// per-line to a strict width bound to prevent right-border overflow.
//
// Per-line wrapping (not a single pass over the entire text) is critical:
// multi-line content like the TODO CHECKLIST must preserve its line structure
// (each checklist item on its own line) rather than being reflowed as one blob.
func (m *model) renderRecordForViewport(rec record) string {
	width := m.width
	if width < 40 {
		width = 40
	}

	wrapWidth := width - 4
	if wrapWidth <= 0 {
		wrapWidth = 80
	}
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	// Sanitize: normalize mixed line endings, convert literal \n and \t escape
	// sequences in error/log text to real control characters, and expand tabs
	// to spaces so multi-line messages display correctly instead of leaking raw
	// backslash sequences or tab misalignment into the viewport.
	text := sanitizeText(rec.text)

	// Strict per-line width bound is enforced by the wrapper below
	// (wrapIndentedLine / wrapText): every wrapped line lands at most
	// wrapWidth cells wide. We NEVER re-wrap already-wrapped content here —
	// applying lipgloss Width() to pre-wrapped, pre-styled text is the
	// double-wrapping corruption source that drops words and leaves isolated
	// punctuation. Trailing padding is trimmed so downstream
	// ANSI-strip/cursor-injection logic sees the exact printable text.
	renderBounded := func(s string) string {
		return strings.TrimRight(s, " ")
	}

	switch rec.role {
	case roleUser:
		displayName := config.SanitizeUsername(m.userName)
		userHeader := dimmedStyle.Render("@" + displayName + "  ")
		paddedText := " " + text
		padNeeded := width - lipgloss.Width(userHeader) - lipgloss.Width(paddedText) - 1
		if padNeeded > 0 {
			paddedText += strings.Repeat(" ", padNeeded)
		}
		return userHeader + userBgStyle.Render(paddedText)
	case roleAI:
		return m.renderAIResponseBlocks(text, width)
	case roleActivity:
		var b strings.Builder
		for _, srcLine := range strings.Split(text, "\n") {
			wrapped := wrapIndentedLine(srcLine, wrapWidth)
			for _, wl := range wrapped {
				b.WriteString(renderBounded(m.styleActivityLine(wl)))
				b.WriteByte('\n')
			}
		}
		return strings.TrimSuffix(b.String(), "\n")
	default:
		var b strings.Builder
		for _, srcLine := range strings.Split(text, "\n") {
			// Explicit \n line breaks between checklist items / log lines are
			// preserved (per-line wrap, never a single reflow pass); indented
			// continuation lines keep their hierarchy via wrapIndentedLine.
			wrapped := wrapIndentedLine(srcLine, wrapWidth)
			for _, wl := range wrapped {
				b.WriteString(renderBounded(wl))
				b.WriteByte('\n')
			}
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
}

// canonicalRecordsContent builds the single canonical rendered string for all
// records at the current width. It is the sole source of truth for both Idle
// and Selection rendering — enabling selection must not alter row count,
// line height, metadata visibility, or line merging. Physical row N in Idle
// MUST remain physical row N in Selection.
func (m *model) canonicalRecordsContent() string {
	if len(m.records) == 0 {
		return ""
	}
	var b strings.Builder
	for i, rec := range m.records {
		rendered := m.renderRecordForViewport(rec)
		if rendered == "" {
			continue
		}
		b.WriteString(rendered)
		if i < len(m.records)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderRecordsWithMouseSelectionFromCanonical renders the canonical records
// string with mouse selection highlight applied as a pure ANSI overlay.
// It reuses the same per-record render path as canonicalRecordsContent so row
// counts remain identical; only ANSI styling bytes are added.
func (m *model) renderRecordsWithMouseSelectionFromCanonical(canonical string) string {
	if len(m.records) == 0 {
		return ""
	}
	// Use per-record injection to keep 1:1 mapping with canonical's row
	// splitting. This delegates to the existing selection renderer which is
	// now guaranteed to use the same width/prefix as canonical.
	_ = canonical // canonical is the joined rendered output; we regenerate per-record to inject styles
	return m.renderRecordsWithMouseSelection()
}

// highlightFrozenCanonical re-applies the current mouse selection highlight
// onto the frozen canonical snapshot taken at drag start. This preserves row
// count while dragging so background timers cannot shift layout.
func (m *model) highlightFrozenCanonical(frozen string) string {
	if frozen == "" {
		return m.renderRecordsWithMouseSelection()
	}
	// If frozen records snapshot exists, use it to generate highlighted
	// output with the same row structure as at drag start. We temporarily
	// swap records for rendering then restore.
	if len(m.frozenRecords) > 0 {
		saved := m.records
		m.records = m.frozenRecords
		highlighted := m.renderRecordsWithMouseSelection()
		m.records = saved
		return highlighted
	}
	// Fallback: frozen is already the rendered canonical with no highlight;
	// split and inject highlight manually is complex, so regenerate from
	// current records which should be identical while frozen.
	return m.renderRecordsWithMouseSelection()
}

// sanitizeEscapes converts literal backslash escape sequences that reach the
// record text from external processes or engine payloads into real control
// characters: \n → newline, \t → tab, \" → quote. Expanding them here means
// Glamour/Lipgloss never sees raw backslash noise and never drops words next
// to an escaped punctuation mark.
func sanitizeEscapes(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\t", "\t")
	s = strings.ReplaceAll(s, "\\\"", "\"")
	return s
}

// leadingWhitespace returns the leading space/tab prefix of a line. It anchors
// the hanging indent used by wrapIndentedLine so indented structures (TODO
// checklist descriptions, error details) stay aligned across wrapped lines.
func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

// wrapIndentedLine wraps a single line to maxWidth using visual cell widths,
// preserving the leading indentation on every continuation line. This keeps
// nested structures aligned without overflowing the viewport's right border
// (word widths are measured with lipgloss.Width, not raw byte length).
func wrapIndentedLine(text string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	prefix := leadingWhitespace(text)
	body := strings.TrimLeft(text, " \t")
	if body == "" {
		return []string{prefix}
	}

	indent := lipgloss.Width(prefix)
	avail := maxWidth - indent
	if avail < 1 {
		avail = 1
	}

	var result []string
	line := prefix
	flush := func() {
		result = append(result, line)
		line = prefix
	}

	for _, word := range strings.Fields(body) {
		wordW := lipgloss.Width(word)
		if wordW > avail {
			// Unbreakable token wider than the available space — hard-chunk it
			// at cell boundaries (ansi.Cut), each piece re-indented. Chunking
			// by cell width (not rune index) keeps wide glyphs inside the bound.
			flush()
			for _, piece := range chunkWord(word, avail) {
				result = append(result, prefix+piece)
			}
			line = prefix
			continue
		}
		if line != prefix && lipgloss.Width(line)+1+wordW > maxWidth {
			flush()
		}
		if line != prefix {
			line += " "
		}
		line += word
	}
	if line != prefix {
		result = append(result, line)
	}
	if len(result) == 0 {
		result = []string{prefix}
	}
	return result
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
	m.shellRunning = false
	m.shellCh = nil
	if m.shellCancel != nil {
		m.shellCancel()
		m.shellCancel = nil
	}
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
	m.reasoningBuffer.Reset()
	m.sentinelReasoningFlushed = 0
	m.pendingReasoningFragment = ""
	m.resetStreamBlocks()
	if m.streamParser != nil {
		m.streamParser = nil
	}
	m.stopShimmer()
}

// clearBusyFlags is the UNIVERSAL TUI state reset for the async pipeline
// lifecycle. It resets every transient processing flag that drives the tick
// spinner loop (m.isProcessing is derived from these via syncUIState), so a
// pipeline that errors or aborts early can NEVER leave an orphaned spinner.
//
// GUARANTEED LIFECYCLE PATTERN: every async terminal message handler
// (investigateResultMsg, hotfixProposalMsg, reviewResultMsg, buildResultMsg,
// planResultMsg, ...) MUST execute clearBusyFlags() + syncUIState() so the
// spinner loop halts regardless of the exit path the producer took.
//
// IMPORTANT: this method ONLY touches transient processing flags. It must
// NEVER reset stream buffers, reasoning state, parser references, or persistent
// view state (m.state, m.currentResult, m.handoffCtx, m.pendingProposals) —
// reconcileSpinner() layers that additional cleanup on top.
func (m *model) clearBusyFlags() {
	m.streaming = false
	m.streamTickActive = false
	m.agentRunning = false
	m.agentLabel = ""
	m.agentDone = true
	m.executionResolving = false
	m.reviewRunning = false
	m.investigateRunning = false
	m.pipelineRunning = false
	m.planPending = false
	m.shellRunning = false
	m.spinnerFrame = 0
	m.lastSpinnerAdvance = time.Time{}
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
	m.clearBusyFlags()
	m.streamCh = nil
	m.streamCancel = nil
	m.shellCh = nil
	if m.shellCancel != nil {
		m.shellCancel()
		m.shellCancel = nil
	}
	m.reasoningBuffer.Reset()
	m.sentinelReasoningFlushed = 0
	m.pendingReasoningFragment = ""
	m.resetStreamBlocks()
	if m.streamParser != nil {
		m.streamParser = nil
	}
	m.stopShimmer()
}

// ensureStreamBlocks lazily constructs the typed stream block buffer.
func (m *model) ensureStreamBlocks() *StreamBuffer {
	if m.streamBlocks == nil {
		m.streamBlocks = NewStreamBuffer()
	}
	return m.streamBlocks
}

// resetStreamBlocks clears the typed stream block buffer (nil-safe).
func (m *model) resetStreamBlocks() {
	if m.streamBlocks != nil {
		m.streamBlocks.Reset()
	}
}

// emitVisibleContent routes one emitted content window into the response
// pipeline. It runs the reasoning extractor over the window, then appends only
// the sentinel-cleaned text to currentStreamContent and the typed stream
// buffer, and projects any newly extracted reasoning onto the unified
// ThinkingBuffer (deduplicated by the flushed-byte offset). Any pre-existing
// streamBuffer content (the legacy non-throttle path's un-emitted remainder) is
// preserved around the extraction so nothing already received is ever dropped.
func (m *model) emitVisibleContent(raw string) {
	if raw == "" {
		return
	}
	leftover := m.streamBuffer
	m.streamBuffer = raw
	m.extractReasoningContent()
	visible := m.streamBuffer
	m.streamBuffer = leftover

	// ── LIVE THINKING BOX ─────────────────────────────────────
	// Sentinel-extracted reasoning (reasoningBuffer, grown by
	// extractReasoningContent above) is flushed into the unified
	// ThinkingBuffer — the same box fed by EventReasoningStream on
	// the /ask path — deduplicated by the flushed-byte offset so the
	// box never re-appends already-consumed reasoning each tick.
	if m.thinkingBuffer != nil && m.reasoningBuffer.Len() > m.sentinelReasoningFlushed {
		reasoning := m.reasoningBuffer.String()[m.sentinelReasoningFlushed:]
		m.thinkingBuffer.Append(reasoning)
		m.sentinelReasoningFlushed = m.reasoningBuffer.Len()
	}

	if visible != "" {
		m.currentStreamContent += visible
		m.ensureStreamBlocks().Append(KindContent, visible)
	}
}

// extractReasoningContent scans raw stream text for reasoning sentinels
// (inserted by providers that surface delta.reasoning_content via SSE) and
// for inline  thinking... response tags (emitted by models that output reasoning
// directly in the message content). It separates reasoning into the dedicated
// reasoningBuffer and thinkingPanel, stripping it from the visible content buffer.
//
// Reasoning sentinels use zero-width markers: \x00RSNG\x00...reasoning...\x00RSNG\x00
//
// NOTE: this used to run two independent scans over the same buffer — one
// (extractSentinelReasoningContent) to read reasoning text for the
// thinkingPanel, and another (extractSentinelReasoning) to strip it and feed
// reasoningBuffer. Because they scanned separately, an opening sentinel with
// no closer yet (the common case: the closer just hasn't streamed in yet)
// caused the stripping pass to give up and write the raw, still-open
// reasoning fragment straight into the visible answer, while the read-only
// pass correctly found nothing to show yet — so reasoning text would appear
// in the response instead of the Thinking Panel. Both concerns are now
// handled by a single pass (extractSentinelReasoning) that never emits an
// unmatched fragment as visible text; it holds it in
// m.pendingReasoningFragment until the closer arrives.
func (m *model) extractReasoningContent() {
	// 1. Extract provider-level reasoning sentinels from streamBuffer.
	clean, extracted := m.extractSentinelReasoning(m.streamBuffer)
	m.streamBuffer = clean
	if extracted != "" {
		m.reasoningBuffer.WriteString(extracted)
		if m.thinkingPanel != nil {
			m.thinkingPanel.Append(extracted)
		}
		m.ensureStreamBlocks().Append(KindThinking, extracted)
	}
	// 2. Extract inline  thinking tags from the already-rendered content.
	thinkContent := m.extractThinkTagsContent(m.currentStreamContent)
	if thinkContent != "" && m.thinkingPanel != nil {
		m.thinkingPanel.Append(thinkContent)
		m.ensureStreamBlocks().Append(KindThinking, thinkContent)
	}
	m.currentStreamContent = m.extractThinkTags(m.currentStreamContent)
}

// extractSentinelReasoning scans raw for reasoning sentinel markers
// (\x00RSNG\x00...\x00RSNG\x00). It returns the cleaned visible text (with
// completed reasoning blocks removed) and the reasoning text pulled from
// those completed blocks.
//
// Any pending fragment left over from a previous call (an opening sentinel
// whose closer hadn't arrived yet) is prepended before scanning. If, after
// scanning, an opening sentinel still has no matching closer, it is NOT
// flushed into the visible text — it's stashed back into
// m.pendingReasoningFragment for the next call. This is what prevents
// partially-streamed reasoning (or a reasoning chunk split across ticks by
// the earlier SSE-reader truncation bug) from leaking into the answer.
func (m *model) extractSentinelReasoning(raw string) (clean string, reasoning string) {
	const sentinel = "\x00RSNG\x00"
	if m.pendingReasoningFragment != "" {
		raw = m.pendingReasoningFragment + raw
		m.pendingReasoningFragment = ""
	}
	if !strings.Contains(raw, sentinel) {
		return raw, ""
	}
	var cleanBuf, reasonBuf strings.Builder
	remaining := raw
	for {
		start := strings.Index(remaining, sentinel)
		if start < 0 {
			cleanBuf.WriteString(remaining)
			break
		}
		cleanBuf.WriteString(remaining[:start])
		rest := remaining[start+len(sentinel):]
		end := strings.Index(rest, sentinel)
		if end < 0 {
			// Incomplete pair — hold it back instead of dumping the
			// in-flight reasoning text into visible content.
			m.pendingReasoningFragment = sentinel + rest
			break
		}
		reasonBuf.WriteString(rest[:end])
		remaining = rest[end+len(sentinel):]
	}
	return cleanBuf.String(), reasonBuf.String()
}

// flushPendingReasoningFragment is called once, at stream end, when nothing
// more is coming. If a reasoning block was left open (its closing sentinel
// never arrived — e.g. the provider truncated the stream), the fragment
// would otherwise sit in m.pendingReasoningFragment forever. Rather than
// silently dropping it or leaking the raw sentinel bytes into the answer,
// surface the leftover text as reasoning content (stripped of the marker).
func (m *model) flushPendingReasoningFragment() {
	if m.pendingReasoningFragment == "" {
		return
	}
	const sentinel = "\x00RSNG\x00"
	leftover := strings.TrimPrefix(m.pendingReasoningFragment, sentinel)
	m.pendingReasoningFragment = ""
	if leftover == "" {
		return
	}
	m.reasoningBuffer.WriteString(leftover)
	// A late stream-completion reasoning flush after /clear (sealed surface)
	// must not resurrect the cleared thinking panel / stream blocks.
	if m.activitySurfaceSealed {
		return
	}
	if m.thinkingPanel != nil {
		m.thinkingPanel.Append(leftover)
	}
	m.ensureStreamBlocks().Append(KindThinking, leftover)
}

// extractThinkTagsContent extracts reasoning content from think tags
// and returns it without modifying the original text.
func (m *model) extractThinkTagsContent(text string) string {
	const thinkOpen = "\x3cthink\x3e"
	const thinkClose = "\x3c/think\x3e"
	if !strings.Contains(text, thinkOpen) {
		return ""
	}
	var reasoning strings.Builder
	remaining := text
	for {
		start := strings.Index(remaining, thinkOpen)
		if start < 0 {
			break
		}
		rest := remaining[start+len(thinkOpen):]
		end := strings.Index(rest, thinkClose)
		if end < 0 {
			break
		}
		reasoning.WriteString(rest[:end])
		remaining = rest[end+len(thinkClose):]
	}
	return reasoning.String()
}

// extractThinkTags scans text for <think>...</think> tags and moves the
// enclosed content into the reasoningBuffer. Returns the cleaned text
// with <think> tags stripped.
func (m *model) extractThinkTags(text string) string {
	if !strings.Contains(text, "<think>") {
		return text
	}
	var clean strings.Builder
	remaining := text
	for {
		start := strings.Index(remaining, "<think>")
		if start < 0 {
			clean.WriteString(remaining)
			break
		}
		clean.WriteString(remaining[:start])
		rest := remaining[start+len("<think>"):]
		end := strings.Index(rest, "</think>")
		if end < 0 {
			// No closing tag yet — keep remaining in content for now
			clean.WriteString(remaining[start:])
			break
		}
		m.reasoningBuffer.WriteString(rest[:end])
		remaining = rest[end+len("</think>"):]
	}
	return clean.String()
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
		// Stop the presentation event projection so no more messages are
		// forwarded into the (terminating) event loop.
		if m.presSink != nil {
			m.presSink.Close()
			m.presSink = nil
		}
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
	m.dotFrame = (m.dotFrame + 1) % 3

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

	// ── Idempotent canonical pipeline: both Idle and Selection use the same
	// canonical records string so physical row N remains identical. Selection
	// highlight is an ANSI overlay that does not alter row counts.
	canonical := m.canonicalRecordsContent()
	// Keep PreRenderedHistory in sync with canonical when not streaming and not
	// dragging so WindowSize-induced stale cache cannot cause layout shift.
	if !m.streaming && !m.mouseSel.Dragging && canonical != "" {
		m.PreRenderedHistory = canonical
	}
	switch {
	case m.inViMode:
		content.WriteString(m.renderRecordsWithCursor())
	case m.mouseSel.Active:
		// If dragging and we have a frozen snapshot, preserve row count by
		// using the frozen canonical layout and only updating highlight.
		// This prevents background Thought for timers or streaming tokens from
		// shifting rows under the cursor.
		if m.mouseSel.Dragging && m.frozenViewportStr != "" {
			// frozenViewportStr is the canonical at drag start; re-highlight
			// it with the current selection range. Row count stays frozen.
			content.WriteString(m.highlightFrozenCanonical(m.frozenViewportStr))
		} else {
			content.WriteString(m.renderRecordsWithMouseSelectionFromCanonical(canonical))
		}
	case canonical != "":
		content.WriteString(canonical)
	case m.PreRenderedHistory != "":
		// Fallback for legacy cached content when canonical is empty
		content.WriteString(m.PreRenderedHistory)
	}

	// ── Foldable execution log entries ─────────────────────────────
	if m.logStore != nil {
		entries := m.logStore.Entries()
		if len(entries) > 0 {
			content.WriteString("\n")
			content.WriteString(dimmedStyle.Render("── Execution Log ──"))
			content.WriteString("\n")
			for _, entry := range entries {
				rendered := RenderEntry(entry, m.width, m.dotFrame)
				content.WriteString(rendered)
				content.WriteString("\n")
			}
		}
	}

	// ── Inline Loading Dock (scrolls with content) ─────────────────
	// The shimmer loading indicator is rendered INSIDE the viewport body,
	// placed immediately below the latest streamed output or submitted
	// prompt. It scrolls dynamically with the text content during
	// streaming, rather than remaining fixed at the bottom above the
	// prompt bar. Clears smoothly when the first primary output token
	// arrives (tokenMsg handler calls stopShimmer).
	//
	// BUG FIX: the original condition was `shimmerActive && !m.streaming`,
	// but m.streaming is set true immediately in streamCmd(), so the dock
	// never rendered. Now we render whenever shimmerActive is true — the
	// shimmer lifecycle (startShimmer/stopShimmer) handles visibility.
	if m.shimmerActive {
		if dock := m.renderLoadingDock(); dock != "" {
			content.WriteString(dock)
		}
	}

	// ── Execution narrative panel (Phase 5/6) ─────────────────────
	// The gated RuntimeExecutor path renders its human narrative EXCLUSIVELY
	// from the execution-view projection (ExecutionNarrative) — never from raw
	// machine events and never from UI-authored progress text. The active
	// visibility layer (Normal/Expanded/Debug) selects what the frame carries.
	if panel := m.renderExecutionLayered(); panel != "" {
		content.WriteString(panel)
	}

	if m.streaming {
		// ── Differential typed stream rendering ─────────────────────
		// The structured buffer renders KindThinking blocks dimmed (faint +
		// italic) and KindContent blocks bright, in arrival order. When no
		// typed blocks exist (legacy non-throttle paths) it falls back to the
		// flat content string through the deterministic pipeline.
		streamed := m.renderStreamBlocks(m.width)
		if streamed == "" && m.currentStreamContent != "" {
			// SANITIZE BEFORE VIEWPORT: raw streamed text may still carry
			// literal \n / \t / \" escapes (preserved verbatim through the
			// rune-safe ingestion). They are expanded to real control
			// characters here so the deterministic pipeline never renders
			// backslash noise or drops words next to escaped punctuation.
			streamed = m.renderStreamingContent(sanitizeText(m.currentStreamContent), m.width)
		}
		if streamed != "" {
			content.WriteString(streamed)
			content.WriteString("\n")
		}

		// ── Inline thinking block (faint, collapsible) ───────────────
		// Live reasoning tokens are rendered inside the viewport body so
		// the user sees thinking in real-time. Ctrl+O / Alt+O toggles
		// expansion during active streaming without waiting for completion.
		//
		// SINGLE-SOURCE-OF-TRUTH DEDUP: while the bottom loading dock is
		// active (shimmerActive), the dock itself already carries the live
		// thinking status ("✻ Thinking... (Xs)"). Rendering the collapsed
		// one-liner here as well would ghost two "Thinking…" lines, so the
		// inline block is suppressed while the dock is live — it only appears
		// once the dock has handed off to the first content token, or when
		// the user expands it via Ctrl+O mid-stream (inspection overrides).
		//
		// When the typed buffer already renders thinking inline (streamThinking
		// blocks), that IS the live thinking display — the separate collapsible
		// box is suppressed to avoid duplicating the same reasoning text.
		dockActive := m.shimmerActive
		inlineThinking := m.streamBlocks != nil && m.streamBlocks.HasThinking()
		if m.thinkingBuffer != nil && m.thinkingBuffer.Len() > 0 {
			if !inlineThinking && (m.thinkingBuffer.Expanded() || !dockActive) {
				thoughts := m.renderLiveThinking(m.width)
				if thoughts != "" {
					content.WriteString(thoughts)
					content.WriteString("\n")
				}
			}
		} else if !dockActive {
			// Fallback: legacy ThinkingPanel for agent-style runs
			reasoningBlock := m.renderReasoningBlock(m.width)
			if reasoningBlock != "" {
				content.WriteString(reasoningBlock)
				content.WriteString("\n")
			}
		}
	}

	// ── Persisted collapsible thought block ────────────────────────────
	// After streaming ends the reasoning block is no longer rendered inline by
	// renderStreamingContent, so it is re-rendered here as a collapsed
	// single-line summary ("▸ Thought for Xs (N tokens)"). The user can expand
	// the full dimmed reasoning text with Ctrl+O / Alt+O at any time.
	if !m.streaming && m.thinkingBuffer != nil && m.thinkingBuffer.Len() > 0 {
		if thoughts := m.renderLiveThinking(m.width); thoughts != "" {
			content.WriteString(thoughts)
			content.WriteString("\n")
		}
	}

	// ── Unified output trace viewport (Ctrl+O) ─────────────────────────
	// Models without a formal reasoning channel never feed the ThinkingBuffer,
	// so Ctrl+O had nothing to expand. The raw streamed response is captured in
	// traceBuffer instead; when the user expands it, the full output trace
	// renders in a dimmed collapsible box for inspection.
	if m.traceExpanded && m.traceBuffer.Len() > 0 {
		if trace := m.renderOutputTrace(m.width); trace != "" {
			content.WriteString(trace)
			content.WriteString("\n")
		}
	}

	// ── Fact-only Execution Tree ────────────────────────────────────
	// The Dynamic IR projection rendered from control.iteration /
	// control.node_observed facts. Placed above the bottom dock so the live
	// execution tree reads as the pipeline's current state while tool steps
	// stream beneath it. It is a pure projection: the facts are read-only
	// and the tree never performs retries or state mutations.
	if m.controlSnapshot != nil && len(m.controlSnapshot.NodeStates) > 0 {
		if treeView := ProjectSnapshotToView(m.controlSnapshot, nil); treeView != "" {
			content.WriteString(treeView)
			content.WriteString("\n")
		}
	}

	// ── Activity Tree: structured tool call view ──────────────────────
	// Rendered outside the streaming block so it appears during /build
	// execution (non-streaming patch proposal) and persists through
	// approval states. Only renders when the tree has active entries.
	// The last entry carries a braille spinner status while any background
	// stage is still in flight, so the execution tree reads as a live
	// pipeline rather than a static log dump.
	if m.activityTree != nil {
		treeActive := m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning || m.shellRunning || m.state == StateProcessing
		// Pass the live spinner frame so the running exec snowflake cycles the
		// full 4-frame sequence (✻ ❅ ❆ ✦) and the status column cycles the
		// single-width braille spinner.
		treeView := m.activityTree.RenderActive(m.width, treeActive, m.spinnerFrame)
		if treeView != "" {
			content.WriteString(treeView)
			content.WriteString("\n")
		}
	}

	// ── Build ViewportHitMap atomically alongside content ──────────────
	// Single source of truth: wrap/prefix budgets are computed once in
	// buildFullHitMap (hitmap.go) and consumed by mouse hit-testing.
	// Memory bounded: only the visible window (YOffset..YOffset+Height) is cached.
	contentStr := content.String()
	var fullRows []RowLayout
	if m.mouseSel.Dragging && m.frozenFullHitRows != nil && len(m.frozenFullHitRows) > 0 {
		// Layout freezing: preserve the hitmap snapshot taken at drag start
		// so background Thought for timers cannot shift rows.
		fullRows = m.frozenFullHitRows
		// Still account for tail chrome that may have grown beyond frozen
		// length, but ensure the frozen prefix+records rows remain stable.
		totalRows := countPhysicalRows(contentStr)
		if totalRows > len(fullRows) {
			for i := len(fullRows); i < totalRows; i++ {
				fullRows = append(fullRows, RowLayout{RecordIdx: -1, LogicalLine: -1, PrefixCells: 0})
			}
		}
		m.fullHitRows = fullRows
	} else {
		fullRows = buildFullHitMap(m)
		// Account for tail chrome (execution log, shimmer, thinking, trace, trees)
		// that are part of viewport content but not records.
		totalRows := countPhysicalRows(contentStr)
		if totalRows > len(fullRows) {
			for i := len(fullRows); i < totalRows; i++ {
				fullRows = append(fullRows, RowLayout{RecordIdx: -1, LogicalLine: -1, PrefixCells: 0})
			}
		}
		m.fullHitRows = fullRows
		// Snapshot for future drag freeze when not already dragging.
		if m.mouseSel.Dragging && m.frozenFullHitRows == nil {
			m.frozenFullHitRows = append([]RowLayout(nil), fullRows...)
			m.frozenViewportStr = contentStr
		}
	}
	geoH := m.viewportGeometry().Height
	yOff := 0
	if m.Ready {
		yOff = m.Viewport.YOffset
	}
	if yOff < 0 {
		yOff = 0
	}
	if yOff > len(fullRows) {
		yOff = len(fullRows)
	}
	end := yOff + geoH
	if end > len(fullRows) {
		end = len(fullRows)
	}
	if yOff < end {
		m.viewportHitMap = ViewportHitMap{YOffset: yOff, Rows: append([]RowLayout(nil), fullRows[yOff:end]...)}
	} else {
		m.viewportHitMap = ViewportHitMap{YOffset: yOff, Rows: nil}
	}

	// ── VIEWPORT SCROLL LOCK (Ctrl+O output-trace) ────────────────────
	// While the expanded output-trace viewport is active, preserve the exact
	// YOffset across SetContent: a transient content shrink would otherwise
	// make the bubbles viewport clamp to the bottom and yank the inspected
	// lines (the Ctrl+O flicker during active generation).
	if m.traceExpanded {
		saved := m.Viewport.YOffset
		m.Viewport.SetContent(contentStr)
		m.Viewport.SetYOffset(saved)
		return
	}
	m.Viewport.SetContent(contentStr)
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
	list, _ := m.autocompleteWindow()
	if len(list) == 0 {
		return 0
	}
	if m.autocompleteType == "scope" {
		return len(list) + 2 // rows + top border + bottom border
	}
	h := 2 // borders
	for _, sec := range buildSuggestionSections(list) {
		h += 1 + len(sec.Items) // header + rows
	}
	return h
}

// computeVpHeight returns the number of terminal rows available for the
// scrollable viewport. Delegates to the single authoritative
// viewportGeometry so rendering and mouse mapping cannot drift.
func (m *model) computeVpHeight() int {
	return m.viewportGeometry().Height
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

// gotoBottomIfAllowed moves the viewport to the bottom only when no active
// user inspection owns the viewport. While a mouse drag selection is active
// the selection controller owns the viewport and streaming must not fight it.
func (m *model) gotoBottomIfAllowed() {
	if !m.Ready {
		return
	}
	if m.userIsScrollingUp || m.mouseSel.Dragging {
		return
	}
	m.Viewport.GotoBottom()
}

// renderFlowingSpinner renders a snowflake character (✻ / ❆) with a subtle
// icy color pulse: the color oscillates between dim and bright cyan using a
// sine wave, creating a cold shimmer effect that distinguishes the inline
// status line from the rectangular block spinner in the bottom loading dock.
func (m *model) renderFlowingSpinner() string {
	n := len(flowingSpinnerFrames)
	idx := m.spinnerFrame % n
	frameStr := flowingSpinnerFrames[idx]

	phase := float64(m.spinnerFrame) * (2 * math.Pi / float64(n))
	t := (math.Sin(phase) + 1) / 2
	t = t * t * (3 - 2*t)

	from := lipgloss.Color(colorSubtle)
	to := lipgloss.Color(colorSapphire)
	color := interpolateColor(from, to, t)

	return spinnerBaseStyle.Foreground(color).Render(frameStr)
}

// renderRectSpinner renders a clean braille/rectangular spinner frame.
// Used exclusively in the BOTTOM LOADING DOCK (above the prompt input bar)
// and the status bar to maintain layout symmetry — snowflake glyphs (✻/❆)
// are reserved for the inline status line in the viewport body.
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
	b.WriteString(modeNameStyle.Render(Icon.Check + " " + modeName))
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

// authorizeBuildExecution creates a MutationAuthorization token through the
// AuthorizationEngine and sets it on the execution engine so that subsequent
// PatchManager.Apply() and Runner.Run() calls succeed.
//
// For fast-track micro-plans (IsWithinMicroBudget), authorization is granted
// automatically without requiring human approval.
func (m *model) authorizeBuildExecution(targetFiles []string, humanApproved bool) error {
	if m.authEngine == nil {
		return nil
	}
	isMicroPlan := false
	if m.microBudget != nil {
		delta := budget.BudgetDelta{Files: len(targetFiles), DiffLines: 100, Tokens: 2000, Attempts: 1}
		isMicroPlan = m.microBudget.IsWithinMicroBudget(delta, false)
	}
	auth, err := m.authEngine.AuthorizeBuild(
		targetFiles,
		m.caps,
		m.mutationBudget,
		m.microBudget,
		isMicroPlan,
		humanApproved,
	)
	if err != nil {
		return fmt.Errorf("build authorization: %w", err)
	}
	m.execEng.SetAuthorization(auth)
	return nil
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
