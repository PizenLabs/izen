package execution

// ── Stream Token Synchronization (Phase 15) ──────────────────────────────────
//
// Provider billing is a fact about BYTES THAT CROSSED THE WIRE, and it is
// settled the moment the stream terminates. Whether those bytes then parsed into
// a valid artifact is a fact about the artifact boundary, and it is settled
// later, somewhere else. Coupling the two is how an aggregate drifts from the
// provider's invoice: a run that ends at the artifact boundary — a contract
// error, a prose rejection, a cancelled stream — can easily return through a
// path that never consults the usage the provider already reported.
//
// The asymmetry this type removes is structural rather than a matter of
// discipline. Before, finalizing the account required the stream to reach its
// happy path, so every TERMINATION had to be remembered individually, and any
// termination someone forgot was simply a run that under-reported what it
// spent. Now the aggregator owns the stream's usage from the first live reading
// to the final flush, and `Finalize` is invoked from ONE place — a deferred
// teardown — so it cannot be reached on one path and skipped on another.
//
// Three properties are load-bearing:
//
//  1. ONCE. Finalize is idempotent: a second call returns the first verdict
//     unchanged. A terminator may therefore call it eagerly on the path it
//     believes is terminal without needing to prove no other path will also
//     fire.
//
//  2. MONOTONIC. Readings are merged by maximum, never by sum and never by last
//     write. A provider that emits an estimated prefix and then the
//     authoritative total must not have the authoritative total overwritten by a
//     late, smaller estimate; and a repeated reading of the same cumulative
//     counter must not double-count.
//
//  3. HONEST ABOUT THE UNKNOWN. A stream that reported nothing stays unknown.
//     The aggregator never manufactures a count from a character estimate: the
//     caller decides whether an estimate is admissible (it is, for a cancelled
//     stream, and not for a clean one), and the aggregator records which
//     accounting it produced so the difference stays visible downstream.

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// StreamTokenAggregator owns the authoritative token account of ONE provider
// stream. It is safe for concurrent use: a provider may report usage from its
// own reader goroutine while the executor's teardown finalizes.
type StreamTokenAggregator struct {
	mu sync.Mutex

	model    string
	started  time.Time
	finished time.Time

	// live is the highest cumulative reading observed on the stream.
	live ai.ProviderUsage
	// observed records that at least one reading arrived, so "the provider
	// reported nothing" stays distinguishable from "the provider reported zero".
	observed bool

	finalized    bool
	final        ai.ProviderUsage
	finalizedAt  time.Time
	flushRequest func(prompt, completion, reasoning int)
}

// NewStreamTokenAggregator starts the account for one stream invocation.
// `model` names the model for diagnostics; `started` anchors the latency
// measurement and may be zero when the caller has no clock reading yet.
func NewStreamTokenAggregator(model string, started time.Time) *StreamTokenAggregator {
	return &StreamTokenAggregator{model: model, started: started}
}

// OnReading merges one live usage reading from the provider.
//
// The merge is by MAXIMUM rather than by assignment or addition. A cumulative
// counter that is read twice reports the same total twice, so summing would
// double the bill; a provider that seeds an estimated prompt baseline and later
// reports the authoritative total would have its total clobbered by a late
// smaller reading, so last-write would under-report. Taking the maximum of each
// dimension independently is the only merge that is correct for both shapes.
//
// An unknown reading carries no information and is discarded: recording it
// would let a "usage unknown" frame erase a real count observed a moment earlier.
func (a *StreamTokenAggregator) OnReading(u ai.ProviderUsage) {
	if a == nil || !u.Known {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finalized {
		// A reading that arrives after the terminator finalized is a late frame
		// from the same stream. It cannot change a settled account, and letting
		// it do so would make the reported total depend on goroutine scheduling.
		return
	}
	a.observed = true
	if u.PromptTokens > a.live.PromptTokens {
		a.live.PromptTokens = u.PromptTokens
	}
	if u.CompletionTokens > a.live.CompletionTokens {
		a.live.CompletionTokens = u.CompletionTokens
	}
	if u.CachedTokens > a.live.CachedTokens {
		a.live.CachedTokens = u.CachedTokens
	}
	if u.ReasoningTokens > a.live.ReasoningTokens {
		a.live.ReasoningTokens = u.ReasoningTokens
	}
	if u.HTTPAttempts > a.live.HTTPAttempts {
		a.live.HTTPAttempts = u.HTTPAttempts
	}
	if u.RateLimitedRetries > a.live.RateLimitedRetries {
		a.live.RateLimitedRetries = u.RateLimitedRetries
	}
	if u.TotalTokens > a.live.TotalTokens {
		a.live.TotalTokens = u.TotalTokens
	}
	// Authority and estimation are latched, never re-derived: once a stream has
	// reported an authoritative count, a later estimated frame must not demote
	// the account to "estimated", because a demotion changes what the number
	// means to every consumer downstream.
	if u.FinishReason != "" {
		a.live.FinishReason = u.FinishReason
	}
	if u.Known && !u.Estimated {
		a.live.Known = true
		a.live.Estimated = false
	} else if !a.live.Known {
		a.live.Known = true
		a.live.Estimated = true
	}
	if !u.RequestStartedAt.IsZero() {
		a.live.RequestStartedAt = u.RequestStartedAt
	}
	if !u.FirstTokenAt.IsZero() {
		a.live.FirstTokenAt = u.FirstTokenAt
	}
	if !u.CompletedAt.IsZero() {
		a.live.CompletedAt = u.CompletedAt
	}
}

// Live returns the highest reading observed so far. It is the value to publish
// on every mid-stream update, and it is never a fabricated count: with no
// reading at all it reports Known=false.
func (a *StreamTokenAggregator) Live() ai.ProviderUsage {
	if a == nil {
		return ai.ProviderUsage{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.observed {
		return ai.ProviderUsage{}
	}
	return a.live
}

// SetFlushRequest installs the sink that receives the final counts. It is the
// graph/telemetry commit the always-flush invariant requires on EVERY exit path,
// including the ones that return an error. Installing it as a callback rather
// than having Finalize return a value makes it structurally impossible to
// finalize without also committing: there is no path that settles the account
// and forgets to publish it.
func (a *StreamTokenAggregator) SetFlushRequest(fn func(prompt, completion, reasoning int)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushRequest = fn
}

// Finalize settles the account on stream termination and returns it.
//
// It is IDEMPOTENT: the first call wins, and every later call — including one
// from a deferred teardown that runs after an eager terminator already
// finalized — returns the same verdict without flushing twice. That is what
// lets the executor call it on the paths that believe they are terminal and
// still keep it in the deferred teardown.
//
// `fallback` is consulted only when the provider reported nothing at all. Passing
// a zero value keeps the account UNKNOWN rather than reporting a genuine zero,
// which is the honest rendering of "this provider does not report usage".
func (a *StreamTokenAggregator) Finalize(fallback ai.ProviderUsage) ai.ProviderUsage {
	if a == nil {
		return ai.ProviderUsage{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finalized {
		return a.final
	}
	a.finalized = true
	a.finalizedAt = time.Now()

	final := a.live
	if !a.observed {
		final = ai.ProviderUsage{}
		if fallback.Known {
			final = fallback
		}
	}
	if final.Known && final.TotalTokens == 0 {
		// A provider that reports the two halves but leaves the total unset is
		// reporting real counts; the total is derived, never invented.
		final.TotalTokens = final.PromptTokens + final.CompletionTokens
	}
	if final.CompletedAt.IsZero() {
		if !a.finished.IsZero() {
			final.CompletedAt = a.finished
		} else {
			final.CompletedAt = a.finalizedAt
		}
	}
	if final.RequestStartedAt.IsZero() {
		final.RequestStartedAt = a.started
	}
	a.final = final

	if a.flushRequest != nil && final.Known &&
		(final.PromptTokens != 0 || final.CompletionTokens != 0) {
		a.flushRequest(final.PromptTokens, final.CompletionTokens, final.ReasoningTokens)
	}
	return final
}

// Finalized reports whether the account has been settled, and when. It exists
// for tests and telemetry: an aggregate that was never finalized is a run whose
// provider billing is still unaccounted, and that fact should be assertable
// rather than inferred from a zero.
func (a *StreamTokenAggregator) Finalized() (bool, ai.ProviderUsage) {
	if a == nil {
		return false, ai.ProviderUsage{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finalized, a.final
}
