package provider

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stream Terminal Invariant (Phase 6.4.1): provider SSE readers MUST
// explicitly close output channels immediately upon parsing a valid
// finish_reason ("stop", "length", or any non-empty reason) to prevent UI
// time-drift at 0.0 tok/s while waiting for a terminal frame ([DONE]) that
// may never arrive.
//
// Zero-Delta Recovery Limit Invariant is enforced in the scheduler
// (internal/runtime/scheduler/continuation.go); this file owns the stream
// side of the hardening: terminal detection + zero-token idle deadline.

// StreamIdleTimeout bounds how long a stream may wait for its terminal
// frame while emitting zero tokens. If no tokens are emitted within this
// window, the stream context is force-cancelled so the UI timer stops
// instead of drifting at 0.0 tok/s.
const StreamIdleTimeout = 5 * time.Second

// UsageDrainTimeout bounds how long an SSE reader may wait for a trailing
// usage-only chunk after observing a terminal finish_reason (Phase 6.4.2
// Telemetry Accuracy Invariant). OpenRouter/OpenAI-compatible gateways
// frequently deliver the final usage object as a separate SSE event with
// choices: [] AFTER the finish_reason chunk (or in the same chunk). Closing
// the channel instantly on finish_reason drops those billed tokens from
// TaskState.TokenUsage and the UI footer (↑X ↓Y). The drain is deliberately
// tiny (50ms): a trailing usage chunk is already in flight on the same
// connection, so waiting longer only delays the UI timer for no gain.
const UsageDrainTimeout = 50 * time.Millisecond

// IsUsageOnlyChunk reports whether an SSE chunk carries provider-reported
// token usage but no choices (len(choices) == 0 && usage != nil). Such
// chunks MUST be recorded into the stream usage tracker before the channel
// is torn down — they are the authoritative telemetry, not padding.
func IsUsageOnlyChunk(choicesLen int, hasUsage bool) bool {
	return choicesLen == 0 && hasUsage
}

// IsTerminalFinishReason reports whether a parsed finish_reason value must
// trigger immediate channel closure. Any non-empty reason (after trimming
// whitespace) is terminal: "stop" and "length" are the common cases, but
// "tool_calls", "content_filter", provider-normalized variants ("STOP",
// "MAX_TOKENS", "end_turn", "max_tokens", ...) are terminal as well. Empty
// means the stream is still open.
func IsTerminalFinishReason(reason string) bool {
	return strings.TrimSpace(reason) != ""
}

// ShouldCloseOnFinishReason is the channel-closure predicate SSE readers
// apply immediately after parsing a chunk. It is intentionally identical to
// IsTerminalFinishReason: finish_reason != "" closes the output channel
// without waiting for [DONE].
func ShouldCloseOnFinishReason(reason string) bool {
	return IsTerminalFinishReason(reason)
}

// IsCleanTermination reports whether the terminal reason is a natural
// completion ("stop" and its common normalizations) as opposed to a
// truncation ("length" / "MAX_TOKENS" / "max_tokens").
func IsCleanTermination(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "stop", "end_turn", "stop_sequence":
		return true
	default:
		return false
	}
}

// IsTruncationTermination reports whether the terminal reason signals an
// output-ceiling truncation.
func IsTruncationTermination(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "output_limit":
		return true
	default:
		return false
	}
}

// StreamLifecycle tracks the terminal/idle state of one SSE stream. It is
// safe for concurrent use: token emission is recorded on the Read path
// while the idle watchdog fires on its own timer goroutine.
type StreamLifecycle struct {
	startedAt time.Time
	mu        sync.Mutex
	lastToken time.Time
	tokens    int
	closed    bool
}

// NewStreamLifecycle opens lifecycle tracking for one stream.
func NewStreamLifecycle() *StreamLifecycle {
	now := time.Now()
	return &StreamLifecycle{startedAt: now, lastToken: now}
}

// NoteTokens records n newly emitted tokens (bytes/chars proxy counts as
// well: any positive n marks the stream alive and resets the idle clock).
func (s *StreamLifecycle) NoteTokens(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.mu.Lock()
	s.tokens += n
	s.lastToken = time.Now()
	s.mu.Unlock()
}

// TokensEmitted reports the total tokens recorded so far.
func (s *StreamLifecycle) TokensEmitted() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

// MarkClosed records explicit channel closure (terminal frame or Close).
func (s *StreamLifecycle) MarkClosed() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// IsClosed reports whether the channel was explicitly closed.
func (s *StreamLifecycle) IsClosed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// IdleExpired reports whether the stream has emitted zero tokens for
// longer than StreamIdleTimeout while still waiting on its terminal frame.
// A stream that emitted any tokens is never idle-expired by this predicate
// (it made progress); a closed stream is never expired.
func (s *StreamLifecycle) IdleExpired(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.tokens > 0 {
		return false
	}
	return now.Sub(s.startedAt) > StreamIdleTimeout
}

// IdleSinceStart reports whether zero tokens were emitted in the first
// StreamIdleTimeout window. It is a convenience wrapper over IdleExpired
// anchored at time.Now.
func (s *StreamLifecycle) IdleSinceStart() bool {
	return s.IdleExpired(time.Now())
}

// idleGuard is the timer-based watchdog backing ArmIdleDeadline. It owns
// no stream state itself; the emitted/closed probes let readers expose
// their (possibly atomic) counters without sharing locks with the timer
// goroutine.
type idleGuard struct {
	timer   *time.Timer
	stopped atomic.Bool
} // ArmIdleDeadline starts a one-shot watchdog: if emitted() still reports
// zero when StreamIdleTimeout elapses and closed() is false, cancel() is
// invoked to force-tear-down the stream context (and, by reader contract,
// the reader then drains and closes its output channel). The returned stop
// function disarms the watchdog; readers should call it on first token or
// on terminal closure. It is idempotent and safe to call twice.
func ArmIdleDeadline(cancel context.CancelFunc, emitted func() int, closed func() bool) (stop func()) {
	g := &idleGuard{}
	g.timer = time.AfterFunc(StreamIdleTimeout, func() {
		if g.stopped.Load() {
			return
		}
		if closed != nil && closed() {
			return
		}
		if emitted != nil && emitted() > 0 {
			return
		}
		if cancel != nil {
			cancel()
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			g.stopped.Store(true)
			if g.timer != nil {
				g.timer.Stop()
			}
		})
	}
}

// ── Phase 6.4.4 Always-Flush Telemetry & Live Token Accounting ──────────
//
// StreamAccumulator is the transport-agnostic live token accumulator every
// SSE reader feeds in real time. It owns three invariants:
//
//   - Optimistic Prompt Token Invariant: SetPromptEstimate MUST be called
//     as soon as the HTTP request is dispatched (before the SSE chunk read
//     loop), so prompt cost is never lost on early stream cancellation.
//   - Real-Time Live Streaming Usage Invariant: AddContent/AddReasoning
//     increment completion counters per emitted chunk, so a timed-out stream
//     retains all partial tokens received prior to cancellation.
//   - Always-Flush Telemetry Invariant: Snapshot is total — it reports
//     whatever was consumed (authoritative or estimated) even when the
//     stream never delivered a terminal usage frame. Callers flush it via
//     defer on every exit path, including ctx.Err() != nil.
//
// Authoritative usage (a terminal usage chunk) always wins over estimates:
// SetAuthoritative replaces the character fallback and latches Known=true
// with Estimated=false. Without it, Snapshot derives completion tokens
// from streamed characters (chars/4) and prompt tokens from the optimistic
// estimate, marked Estimated=true but still Known=true so telemetry never
// renders a silent zero for billed work.
type StreamAccumulator struct {
	mu sync.Mutex

	promptEstimate int
	promptTokens   int
	hasPromptAuth  bool

	contentChars   int
	reasoningChars int

	completionTokens int
	hasCompAuth      bool

	hasAuthoritative bool
	interrupted      bool
}

// EstimatePromptTokens converts prompt characters to a token baseline
// (~4 chars per token, minimum 1 per non-empty prompt). It mirrors the
// UI-side estimatePromptTokens and the executor snapshot fallback so every
// layer converges on the same optimistic number.
func EstimatePromptTokens(promptChars int) int {
	if promptChars <= 0 {
		return 0
	}
	n := promptChars / 4
	if n < 1 {
		n = 1
	}
	return n
}

// EstimateCompletionTokens converts streamed content characters to a token
// fallback (chars/4). Reasoning characters are never mixed in — the caller
// passes content chars only.
func EstimateCompletionTokens(contentChars int) int {
	if contentChars <= 0 {
		return 0
	}
	return contentChars / 4
}

// SetPromptEstimate registers the optimistic prompt count immediately after
// dispatch, before any chunk is read. It never overwrites an authoritative
// prompt count. Safe to call twice (first wins unless authoritative).
func (a *StreamAccumulator) SetPromptEstimate(n int) {
	if a == nil || n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasPromptAuth {
		return
	}
	if a.promptEstimate == 0 {
		a.promptEstimate = n
	}
}

// SetPromptAuthoritative records the provider-reported prompt count,
// replacing any optimistic estimate.
func (a *StreamAccumulator) SetPromptAuthoritative(n int) {
	if a == nil || n < 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.promptTokens = n
	a.hasPromptAuth = true
	a.hasAuthoritative = a.hasAuthoritative || true
}

// AddContent records one emitted content chunk in real time. n is the byte
// length of the visible content emitted to the consumer.
func (a *StreamAccumulator) AddContent(n int) {
	if a == nil || n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contentChars += n
}

// AddReasoning records one emitted reasoning chunk in real time. Reasoning
// chars are tracked separately and never inflate the completion estimate.
func (a *StreamAccumulator) AddReasoning(n int) {
	if a == nil || n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningChars += n
}

// SetCompletionAuthoritative records the provider-reported completion count,
// discarding the character fallback.
func (a *StreamAccumulator) SetCompletionAuthoritative(n int) {
	if a == nil || n < 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completionTokens = n
	a.hasCompAuth = true
	a.hasAuthoritative = true
}

// SetAuthoritative records a full authoritative usage pair at once.
func (a *StreamAccumulator) SetAuthoritative(prompt, completion int) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.promptTokens = prompt
	a.completionTokens = completion
	a.hasPromptAuth = true
	a.hasCompAuth = true
	a.hasAuthoritative = true
}

// MarkInterrupted flags a non-EOF termination (deadline/cancel). The
// snapshot stays available — interruption never zeroes consumed tokens.
func (a *StreamAccumulator) MarkInterrupted() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interrupted = true
}

// Interrupted reports whether the stream ended before a natural EOF.
func (a *StreamAccumulator) Interrupted() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interrupted
}

// PromptTokens reports the live prompt count: authoritative when known,
// otherwise the optimistic estimate. Zero means truly unknown.
func (a *StreamAccumulator) PromptTokens() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasPromptAuth {
		return a.promptTokens
	}
	return a.promptEstimate
}

// CompletionTokens reports the live completion count in real time:
// authoritative when a usage chunk arrived, otherwise the character
// fallback over content actually emitted.
func (a *StreamAccumulator) CompletionTokens() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasCompAuth {
		return a.completionTokens
	}
	return a.contentChars / 4
}

// Snapshot returns (prompt, completion, known, estimated). Known is true
// whenever any tokens were consumed or authoritatively reported; estimated
// is true only when no authoritative usage chunk arrived. It never depends
// on a terminal usage frame — partial streams snapshot partial tokens.
func (a *StreamAccumulator) Snapshot() (prompt, completion int, known, estimated bool) {
	if a == nil {
		return 0, 0, false, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasAuthoritative || a.hasPromptAuth || a.hasCompAuth {
		p := a.promptTokens
		if !a.hasPromptAuth {
			p = a.promptEstimate
		}
		c := a.completionTokens
		if !a.hasCompAuth {
			c = a.contentChars / 4
		}
		return p, c, true, !a.hasAuthoritative && (a.contentChars > 0 || a.promptEstimate > 0)
	}
	if a.promptEstimate > 0 || a.contentChars > 0 {
		return a.promptEstimate, a.contentChars / 4, true, true
	}
	return 0, 0, false, false
}

// Reset clears per-turn live state so a cancelled request never leaks stale
// counts into the next command. Authoritative terminal state is discarded;
// the next turn re-seeds via SetPromptEstimate.
func (a *StreamAccumulator) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.promptEstimate = 0
	a.promptTokens = 0
	a.hasPromptAuth = false
	a.contentChars = 0
	a.reasoningChars = 0
	a.completionTokens = 0
	a.hasCompAuth = false
	a.hasAuthoritative = false
	a.interrupted = false
}
