package executor

import (
	"context"
	"errors"
	"sync/atomic"
	"unicode/utf8"
)

// ErrProactiveBudgetExceeded is the cancellation cause used by StreamTokenGuard
// when a worker's real-time stream token estimate crosses TriggerCeiling. It is
// deliberately a partial-turn signal: the worker step context is cancelled, the
// staging buffer is discarded, and the scheduler CONTINUES with a fresh step.
// It never aborts the task (use context.Canceled for that).
var ErrProactiveBudgetExceeded = errors.New("executor: proactive budget exceeded — stream truncated at trigger ceiling")

// ProactivelyTruncatedInfo carries the guard state when proactive token
// ceiling enforcement truncates a step. A non-nil emit hook registered via
// OnProactivelyTruncated receives it so the bus wiring can publish
// events.EventStepProactivelyTruncated.
type ProactivelyTruncatedInfo struct {
	StepID          string
	EstimatedTokens int
	TriggerCeiling  int
	Budget          int
}

// StreamTokenGuard enforces a PROACTIVE output-token ceiling on a live worker
// byte stream, in real time, at the staging buffer boundary.
//
// It is speculative and conservative: EstimatedTokens is a byte/word-boundary
// HEURISTIC, not the provider's billed token count. It exists to halt an
// over-running stream BEFORE the provider signals finish_reason=length, using
// context.CancelCauseFunc so the downstream reader can distinguish the budget
// cut (ErrProactiveBudgetExceeded) from a user cancellation (context.Canceled).
type StreamTokenGuard struct {
	// Budget is the EffectiveStepBudget bound for this step.
	Budget int
	// TriggerCeiling is the token estimate that fires proactive cancellation:
	// EffectiveStepBudget - max(16, 5% of Budget). It is never negative.
	TriggerCeiling int
	// EstimatedTokens is the live stream token estimate, incremented per Write.
	EstimatedTokens atomic.Int64
	// cancelFn cancels the worker step context with the given cause. It is the
	// ONLY mutation the guard performs on the worker: the stream reader returns
	// ErrProactiveBudgetExceeded and halts.
	cancelFn context.CancelCauseFunc
}

// NewStreamTokenGuard builds a guard that cancels the worker step context
// (with cause ErrProactiveBudgetExceeded) once the heuristic stream token
// estimate reaches TriggerCeiling. A nil cancelFn keeps the guard purely
// observational (Write still returns ErrProactiveBudgetExceeded). A non-positive
// budget disables enforcement (TriggerCeiling=0).
func NewStreamTokenGuard(budget int, cancelFn context.CancelCauseFunc) *StreamTokenGuard {
	ceiling := 0
	if budget > 0 {
		ceiling = budget - maxCeilingReserve(budget)
		// Keep the guard live even for budgets smaller than the fixed floor of
		// the reserve: a positive ceiling of 1 fires on the very first token.
		if ceiling < 1 {
			ceiling = 1
		}
	}
	return &StreamTokenGuard{
		Budget:         budget,
		TriggerCeiling: ceiling,
		cancelFn:       cancelFn,
	}
}

// maxCeilingReserve derives the headroom the trigger ceiling keeps below the
// hard EffectiveStepBudget: max(16, 5% of budget). This absorbs estimation
// error so the guard fires BEFORE the provider's own length limit.
func maxCeilingReserve(budget int) int {
	const floor = 16
	reserve := budget / 20 // 5% of budget
	if reserve < floor {
		return floor
	}
	return reserve
}

// EstimateTokens approximates the token count of one UTF-8 chunk using
// byte-length / word-boundary heuristics:
//   - multibyte UTF-8 sequences count as one token each,
//   - ASCII runs collapse to ~4 bytes per token,
//   - any non-empty chunk counts at least one token.
//
// It is a streaming scan (not a naive division of the whole buffer): runs are
// tracked across the chunk so both a dense ASCII burst and a multibyte CJK
// burst estimate conservatively without over- or under-firing the ceiling.
func (g *StreamTokenGuard) EstimateTokens(p []byte) int {
	if g == nil || len(p) == 0 {
		return 0
	}
	return estimateChunkTokens(p)
}

// Ceiling returns the effective trigger ceiling. It is asserted to stay at or
// below Budget for the enforcement test.
func (g *StreamTokenGuard) Ceiling() int {
	if g == nil {
		return 0
	}
	return g.TriggerCeiling
}

// Write estimates one streamed chunk, adds it to EstimatedTokens, and —
// when the ceiling is crossed — cancels the worker step context with cause
// ErrProactiveBudgetExceeded. It returns ErrProactiveBudgetExceeded so the
// stream reader halts at the buffer boundary; the breaching bytes are NEVER
// staged (they are discarded with the buffer on Finalize).
func (g *StreamTokenGuard) Write(p []byte) (int, error) {
	if g == nil {
		return len(p), nil
	}
	increment := g.EstimateTokens(p)
	if increment > 0 {
		g.EstimatedTokens.Add(int64(increment))
	}
	if g.TriggerCeiling > 0 && g.EstimatedTokens.Load() >= int64(g.TriggerCeiling) {
		if g.cancelFn != nil {
			g.cancelFn(ErrProactiveBudgetExceeded)
		}
		return 0, ErrProactiveBudgetExceeded
	}
	return len(p), nil
}

// Cancelled reports whether the guard has already fired and cancelled the
// worker context (idempotent enforcement).
func (g *StreamTokenGuard) Cancelled() bool {
	if g == nil {
		return false
	}
	return g.EstimatedTokens.Load() >= int64(g.TriggerCeiling) && g.TriggerCeiling > 0
}

func estimateChunkTokens(p []byte) int {
	switch {
	case len(p) == 0:
		return 0
	case len(p) < 4:
		return 1
	}

	tokens := 0
	asciiRun := 0
	flushASCII := func() {
		// Collapse an ASCII run to ~4 bytes per token, rounding up.
		tokens += (asciiRun + 3) / 4
		asciiRun = 0
	}
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size <= 1 {
			// Invalid byte: count it and move on.
			asciiRun++
			i++
			continue
		}
		if size == 1 {
			asciiRun++
		} else {
			flushASCII()
			// Each multibyte rune ≈ one token.
			tokens++
		}
		i += size
	}
	flushASCII()
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// ProactiveBudgetReason is the deterministic StepOutcomeReason label attached
// to a StepOutcomePartial produced by proactive token ceiling enforcement.
const OutputCeilingReason StepOutcomeReason = "OUTPUT_CEILING"

const ProactiveBudgetReason = OutputCeilingReason

// IsProactiveBudgetCancel reports whether a finalize error represents the
// deterministic partial classification of the stream budget guard — and ONLY
// that: it must never swallow unrelated context cancellations.
func IsProactiveBudgetCancel(err error) bool {
	return errors.Is(err, ErrProactiveBudgetExceeded)
}
