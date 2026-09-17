// Package executor — staging.go implements Transactional Proposal Staging
// for Bounded Execution (Phase 6.2).
//
// Invariant 4 (Partial Output Isolation): finish_reason=length yields
// StepOutcomePartial. Incomplete model proposals MUST NEVER cross the
// authorization boundary. Partial stream buffers in ProposalStagingBuffer
// are discarded cleanly without corrupting TaskState or workspace memory.
//
// Invariant 5 (Execution/Evidence Truth): model completion assertions are
// untrusted until verified by ExecutionResult + EvidenceState. TaskState
// updates strictly require runtime evidence (Commit requires evidence).
package executor

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
	providercap "github.com/PizenLabs/izen/internal/provider"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// StepOutcome is the bounded-step outcome registered by the staging layer.
type StepOutcome string

const (
	// StepOutcomeComplete marks a fully-streamed proposal eligible for the
	// authorization boundary.
	StepOutcomeComplete StepOutcome = "complete"
	// StepOutcomePartial marks finish_reason=length truncation: the staging
	// buffer was discarded and the scheduler must issue a continuation.
	StepOutcomePartial StepOutcome = "partial"
	// StepOutcomeFailed marks a stream error / cancellation.
	StepOutcomeFailed StepOutcome = "failed"
)

// ProposalStagingBuffer isolates raw worker output before authorization. It
// is append-only while streaming; Finalize either releases the staged bytes
// for authorization (complete) or discards them (partial/failed). Discarded
// bytes are unrecoverable by design.
//
// The optional StreamTokenGuard upgrades the buffer with PROACTIVE token
// ceiling enforcement: Write tracks a real-time heuristic token estimate and
// cancels the worker step context (ErrProactiveBudgetExceeded) when the
// trigger ceiling is crossed — BEFORE the provider signals finish_reason=
// length. The subsequent FinalizeErr(ErrProactiveBudgetExceeded) classifies
// the step as StepOutcomePartial (deterministic continuation), discards the
// buffer, and emits EventStepProactivelyTruncated via the optional hook.
type ProposalStagingBuffer struct {
	mu        sync.Mutex
	stepID    string
	buf       []byte
	finalized bool
	discarded bool
	outcome   StepOutcome
	// guard optionally enforces proactive stream token ceiling cancellation.
	guard *StreamTokenGuard
	// emitTruncation, when set, is called inside FinalizeErr when a proactive
	// budget cancellation is classified as a partial step. Wiring it to the
	// events bus publishes EventStepProactivelyTruncated.
	emitTruncation func(ProactivelyTruncatedInfo)
	state          *durable.TaskState
	provider       *dprovider.ProviderMetadata
	recovery       durable.RecoveryContext
	observedTokens int
	payloadLimit   int
	reason         StepOutcomeReason
	symbolBaseline *SymbolBaseline
	symbolTarget string
}

// NewProposalStagingBuffer opens an isolated staging buffer for one step.
func NewProposalStagingBuffer(stepID string) *ProposalStagingBuffer {
	return &ProposalStagingBuffer{stepID: stepID}
}

func (b *ProposalStagingBuffer) WithRecoveryContext(state *durable.TaskState, meta *dprovider.ProviderMetadata, recovery durable.RecoveryContext) *ProposalStagingBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state, b.provider, b.recovery = state, meta, recovery
	return b
}

// WithSymbolBaseline binds a runtime-captured baseline before worker output.
func (b *ProposalStagingBuffer) WithSymbolBaseline(target string, baseline *SymbolBaseline) *ProposalStagingBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.symbolTarget, b.symbolBaseline = target, baseline
	return b
}

func (b *ProposalStagingBuffer) WithObservedTokens(tokens int) *ProposalStagingBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tokens > 0 {
		b.observedTokens = tokens
	}
	return b
}

func (b *ProposalStagingBuffer) WithPayloadLimit(tokens int) *ProposalStagingBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.payloadLimit = tokens
	return b
}

func (b *ProposalStagingBuffer) partialLocked() StagingDisposition {
	b.finalized, b.discarded = true, true
	b.outcome, b.reason = StepOutcomePartial, OutputCeilingReason
	r := b.recovery
	r.Phase, r.StepID, r.Reason = durable.RecoveryRequired, b.stepID, string(OutputCeilingReason)
	r.StreamBytes = len(b.buf)
	r.LastCleanByteOffset, r.LastCleanLine, r.LastCleanTokenOffset = 0, 0, 0
	for i, value := range b.buf {
		if value == '\n' {
			r.LastCleanByteOffset = i + 1
			r.LastCleanLine++
		}
	}
	r.LastCleanTokenOffset = estimateChunkTokens(b.buf[:r.LastCleanByteOffset])
	r.ObservedTokens = b.observedTokens
	if r.ObservedTokens <= 0 && b.guard != nil {
		r.ObservedTokens = int(b.guard.EstimatedTokens.Load())
	}
	if r.ObservedTokens <= 0 {
		r.ObservedTokens = estimateChunkTokens(b.buf)
	}
	b.buf = nil
	if b.state != nil {
		b.state.RecoveryContext = r
	}
	if b.provider != nil {
		b.provider.RecordOutputLimit(dprovider.OutputLimit{Value: r.ObservedTokens, Source: dprovider.LimitObserved})
	}
	return b.dispositionLocked()
}

func (b *ProposalStagingBuffer) dispositionLocked() StagingDisposition {
	return StagingDisposition{Outcome: b.outcome, Discarded: b.discarded, NeedsContinuation: b.outcome == StepOutcomePartial, Reason: b.reason}
}

// WithTokenGuard binds a proactive stream token ceiling guard to the buffer.
// Once bound, Write tracks the stream and cancels the worker step context when
// the estimate crosses the guard's trigger ceiling.
func (b *ProposalStagingBuffer) WithTokenGuard(guard *StreamTokenGuard) *ProposalStagingBuffer {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.guard = guard
	return b
}

// OnProactivelyTruncated registers a callback invoked (once, inside FinalizeErr)
// when a proactive budget cancellation classifies the step as partial. The
// callback receives the guard state so the wiring can publish
// events.NewStepProactivelyTruncated.
func (b *ProposalStagingBuffer) OnProactivelyTruncated(fn func(info ProactivelyTruncatedInfo)) *ProposalStagingBuffer {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emitTruncation = fn
	return b
}

// StepID returns the owning step.
func (b *ProposalStagingBuffer) StepID() string {
	if b == nil {
		return ""
	}
	return b.stepID
}

// Append streams one raw chunk into isolation. Writes after Finalize or
// Discard are dropped.
func (b *ProposalStagingBuffer) Append(chunk []byte) {
	if b == nil || len(chunk) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized || b.discarded {
		return
	}
	b.buf = append(b.buf, chunk...)
}

// AppendString streams one raw string chunk into isolation.
func (b *ProposalStagingBuffer) AppendString(chunk string) {
	b.Append([]byte(chunk))
}

// Write streams one raw chunk into isolation through the optional StreamTokenGuard.
// It satisfies io.Writer so the worker stream reader can pump directly into the
// buffer. When the guard's real-time token estimate crosses the trigger ceiling
// the buffer cancels the worker step context with cause ErrProactiveBudgetExceeded
// and returns that error so the stream reader halts AT the buffer boundary.
// The breaching chunk is never staged; FinalizeErr discards everything staged.
//
// Without a guard Write degrades to Append semantics and returns len(p), nil.
func (b *ProposalStagingBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return 0, nil
	}
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized || b.discarded {
		// Writes after finalize/discard are dropped (io convention: the bytes
		// are counted as consumed, never staged).
		return len(p), nil
	}
	if b.guard == nil {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	if _, err := b.guard.Write(p); err != nil {
		// Proactive ceiling crossed: halt the reader before the provider
		// signals length. The worker step context has been cancelled with
		// cause ErrProactiveBudgetExceeded; nothing more is staged.
		return 0, err
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// EstimatedTokensSnapshot reports the current guard token estimate (0 without a
// guard). It is for telemetry/assertion; the enforcing read lives in the guard.
func (b *ProposalStagingBuffer) EstimatedTokensSnapshot() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.guard == nil {
		return 0
	}
	return b.guard.EstimatedTokens.Load()
}

// Len returns the staged byte count.
func (b *ProposalStagingBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// Discard drops the staged bytes without exposing them. It is idempotent
// and leaves no residue: after Discard the buffer reads empty and can never
// be committed.
func (b *ProposalStagingBuffer) Discard() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = nil
	b.discarded = true
	b.finalized = true
	b.outcome = StepOutcomePartial
}

// StagingDisposition is the Finalize verdict for one streamed step.
type StagingDisposition struct {
	// Outcome is the registered StepOutcome.
	Outcome StepOutcome
	// Proposal carries the staged bytes ONLY when Outcome == Complete.
	// It is always empty on Partial/Failed (isolation).
	Proposal string
	// NeedsContinuation requests a StepScheduler continuation turn.
	NeedsContinuation bool
	// Discarded reports the staging buffer was dropped.
	Discarded bool
	// Reason is the deterministic classification reason for Partial outcomes
	// (e.g. ProactiveBudgetReason when proactive token ceiling enforcement cut
	// the stream). It is empty for every other outcome.
	Reason StepOutcomeReason
}
// RedundantSymbolReason is the deterministic StepOutcomeReason label
// attached to a StepOutcomePartial produced by the redundancy gate: a new
// private helper with >70% signature/structure similarity to an available
// baseline symbol. The proposal never reaches the workspace sink.
const RedundantSymbolReason StepOutcomeReason = "REDUNDANT_SYMBOL"

// StepOutcomeReason is a deterministic, closed reason vocabulary for Partial
// step outcomes so scheduler continuation policy never parses free-form logs.
type StepOutcomeReason string

// Finalize closes the stream for finishReason ("stop", "length", ...) with
// an explicit truncated flag (transport-level cut). Length/truncation yields
// StepOutcomePartial with the buffer discarded; anything else complete maps
// to StepOutcomeComplete, failures to StepOutcomeFailed.
func (b *ProposalStagingBuffer) Finalize(finishReason string, truncated bool) StagingDisposition {
	if b == nil {
		return StagingDisposition{Outcome: StepOutcomeFailed, Discarded: true}
	}
	outcome := providercap.MapFinishReason(finishReason)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return b.dispositionLocked()
	}
	if truncated || (b.guard != nil && b.guard.Cancelled()) ||
		(b.payloadLimit > 0 && estimateChunkTokens(b.buf) > b.payloadLimit) {
		return b.partialLocked()
	}
	b.finalized = true
	switch {
	case outcome.IsPartial() || isLengthReason(finishReason):
		return b.partialLocked()
	case outcome == providercap.StreamComplete:
		proposal := string(b.buf)
		if redundant := b.symbolBaseline.Check(b.symbolTarget, proposal); redundant != nil {
			b.buf = nil
			b.discarded = true
			b.outcome, b.reason = StepOutcomePartial, RedundantSymbolReason
			r := b.recovery
			r.Phase, r.Reason = durable.RecoveryRequired, string(RedundantSymbolReason)
			r.ReuseTarget, r.ReuseSymbols = redundant.ExistingFile, []string{redundant.ExistingSymbol}
			r.BaselineScope = b.symbolBaseline.Names()
			if b.state != nil { b.state.RecoveryContext = r }
			return b.dispositionLocked()
		}
		b.outcome = StepOutcomeComplete
		return StagingDisposition{Outcome: StepOutcomeComplete, Proposal: proposal}
	default:
		b.buf = nil
		b.discarded = true
		b.outcome = StepOutcomeFailed
		return StagingDisposition{Outcome: StepOutcomeFailed, Discarded: true}
	}
}

func isLengthReason(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "length")
}

// FinalizeErr closes the stream from a WORKER STEP ERROR, classifying it by
// deterministic cause:
//
//   - errors.Is(err, ErrProactiveBudgetExceeded) → StepOutcomePartial with
//     ProactiveBudgetReason: the buffer is flushed/discarded, the hook emits
//     EventStepProactivelyTruncated, and the scheduler must continue (NOT an
//     abort). This is the invariant Deterministic Partial Classification.
//   - errors.Is(err, context.Canceled) → StepOutcomeFailed: a user cancellation
//     aborts the step; the task must NOT auto-continue.
//   - other non-nil errors → StepOutcomeFailed (stream/integration failure).
//   - nil → the stream completed normally and is treated identically to a clean
//     stop Finalize("stop", false).
func (b *ProposalStagingBuffer) FinalizeErr(err error) StagingDisposition {
	if b == nil {
		return StagingDisposition{Outcome: StepOutcomeFailed, Discarded: true}
	}
	if err == nil {
		return b.Finalize("stop", false)
	}
	if errors.Is(err, ErrProactiveBudgetExceeded) {
		return b.finalizeProactive()
	}
	if errors.Is(err, context.Canceled) {
		return b.finalizeFailed()
	}
	return b.finalizeFailed()
}

// finalizeProactive marks the step deterministic partial: the staging buffer is
// flushed from memory and the optional hook emits EventStepProactivelyTruncated.
func (b *ProposalStagingBuffer) finalizeProactive() StagingDisposition {
	b.mu.Lock()
	proactive := !b.finalized
	if proactive {
		b.partialLocked()
	}
	disposition := b.dispositionLocked()
	stepID := b.stepID
	emit := b.emitTruncation
	var estimated int64
	budget, ceiling := 0, 0
	if b.guard != nil {
		estimated = b.guard.EstimatedTokens.Load()
		budget = b.guard.Budget
		ceiling = b.guard.TriggerCeiling
	}
	b.mu.Unlock()

	if proactive && emit != nil {
		emit(ProactivelyTruncatedInfo{
			StepID:          stepID,
			EstimatedTokens: int(estimated),
			TriggerCeiling:  ceiling,
			Budget:          budget,
		})
	}
	return disposition
}

func (b *ProposalStagingBuffer) finalizeFailed() StagingDisposition {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return b.dispositionLocked()
	}
	b.finalized = true
	b.discarded = true
	b.buf = nil
	b.outcome = StepOutcomeFailed
	return StagingDisposition{Outcome: StepOutcomeFailed, Discarded: true}
}

// WorkspaceSink is the workspace mutation boundary the staging layer guards.
// Apply runs only for Complete dispositions with verified evidence; the
// executor never calls it for Partial/Failed.
type WorkspaceSink interface {
	Apply(proposal string) (patches int, err error)
}

// CommitGate enforces Invariant 5: a staged proposal crosses the
// authorization/execution boundary ONLY on (Complete disposition + passing
// evidence). Partial dispositions and unverified evidence never reach the
// sink: zero patches hit the workspace.
func CommitGate(disp StagingDisposition, state evidence.EvidenceState, sink WorkspaceSink) (patches int, outcome StepOutcome, err error) {
	if disp.Outcome != StepOutcomeComplete {
		return 0, disp.Outcome, nil
	}
	if disp.Proposal == "" {
		return 0, StepOutcomeComplete, nil
	}
	if state == evidence.VerdictFailed {
		return 0, StepOutcomeFailed, nil
	}
	if state != evidence.VerdictPassed {
		// Inconclusive/PARTIAL evidence is not execution truth: hold the
		// proposal without mutating the workspace.
		return 0, StepOutcomeFailed, nil
	}
	if sink == nil {
		return 0, StepOutcomeComplete, nil
	}
	n, err := sink.Apply(disp.Proposal)
	if err != nil {
		return 0, StepOutcomeFailed, err
	}
	return n, StepOutcomeComplete, nil
}
