package ui

import (
	"strings"
	"sync"
)

// ── THREAD-SAFE TOKEN ACCUMULATOR (engine→UI decoupling, layer 1) ─────────────
//
// # WHAT THIS IS
//
// The place raw provider deltas land. The LLM streaming goroutine appends every
// content and reasoning chunk here and returns; the Bubble Tea event loop drains
// the whole batch on its next frame tick. Between those two points there is no
// tea.Msg, no channel send, no command, and no allocation on the consumer's
// behalf.
//
// # WHY THE PER-TOKEN MESSAGE HAD TO GO
//
// A tea.Msg per token is not one message — it is a message PLUS the read
// command that fetches it PLUS the work Update() does to answer it, and all
// three land on the same goroutine that has to answer a wheel event. At 100
// tok/s that is 100 round trips through the event loop per second, each one
// queued in front of the user's next scroll notch, and the queue is the
// latency. Removing the message does not slow the stream: the provider goroutine
// already returns immediately after appending, and the bytes are on screen at
// the next frame either way.
//
// The batch is also what makes the drain cheap. Draining 60 chunks costs ONE
// buffer copy; draining them one at a time costs 60 Update() entries, 60
// command allocations and 60 separate re-entrancies into the render pipeline.
//
// # WHY A MUTEX AND NOT AN ATOMIC
//
// The accumulator is a growable string. There is no atomic form of "append N
// bytes to a shared string" — an atomic swap would need the string to already
// exist at the right capacity, which is precisely the allocation this type
// exists to move off the consumer. One uncontended mutex per chunk is a few
// nanoseconds and, unlike a lock-free ring, it cannot be full: a burst of
// 10,000 tokens parks as bytes, not as 10,000 queued messages. Backpressure on
// the producer becomes memory instead of latency, which is the correct trade for
// a text stream whose bytes are small and whose arrival rate is the thing that
// must never gate.
//
// # ORDERING
//
// Content and reasoning are drained in arrival order within a frame, and the
// per-stream token estimate is summed at APPEND time (not drain time) so batching
// cannot change the number the footer reports. estimateStreamTokens is a
// per-chunk function; summing it over a batch is what keeps the batched drain
// byte-for-byte equivalent to the per-token drain it replaced.

// streamAccumulator is a mutex-protected pair of delta buffers plus the
// counters that must be attributed at append time.
//
// It is safe for concurrent use by exactly one producer goroutine (the
// provider stream reader) and one consumer (the UI event loop). It is NOT a
// general-purpose concurrent queue: it has no wake-up, no capacity bound and no
// ordering across the two buffers beyond the frame in which they were drained,
// because the frame tick is the only consumer and it drains both every time.
type streamAccumulator struct {
	mu sync.Mutex

	content  strings.Builder
	thinking strings.Builder

	// contentTokens and thinkingTokens are the sums of estimateStreamTokens
	// over every chunk appended but not yet drained, kept per buffer. They are
	// accumulated per chunk (never per batch) so the live tok/s meter is
	// invariant under batching.
	contentTokens  int
	thinkingTokens int
	// dirty records whether anything is pending at all. A frame that finds
	// dirty == false does no copy, no allocation and no buffer swap — it pays
	// one mutex acquisition, which is the entire cost of an idle frame.
	dirty bool
}

// NewStreamAccumulator returns an empty accumulator ready for one stream turn.
func NewStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{}
}

// AppendContent records one raw content delta from the provider goroutine.
//
// The token estimate is taken HERE, on the producer side, and the reason is
// exactness under batching: estimateStreamTokens is a function of a single
// chunk's length, so summing it per chunk and reporting the sum is identical to
// the per-token path. Estimating the joined batch instead would round
// differently and the footer's live rate would depend on the frame cadence.
func (a *streamAccumulator) AppendContent(delta string) {
	if a == nil || delta == "" {
		return
	}
	a.mu.Lock()
	a.content.WriteString(delta)
	a.contentTokens += estimateStreamTokens(delta)
	a.dirty = true
	a.mu.Unlock()
}

// AppendThinking records one raw reasoning delta from the provider goroutine.
//
// Reasoning is kept in its own buffer, not appended to the content buffer,
// because it must never enter the content pipeline: it renders in the dimmed
// thinking style and is stripped from the answer. Merging the two would make
// the separation a downstream guess instead of a structural fact.
func (a *streamAccumulator) AppendThinking(delta string) {
	if a == nil || delta == "" {
		return
	}
	a.mu.Lock()
	a.thinking.WriteString(delta)
	a.thinkingTokens += estimateStreamTokens(delta)
	a.dirty = true
	a.mu.Unlock()
}

// Drain hands the consumer everything appended since the last drain, per buffer,
// with the per-chunk token estimate for each. It returns empty strings and
// zeros when nothing is pending, which is the idle-frame case.
//
// The buffers are SWAPPED, not truncated: the next frame's appends land in
// fresh builders, so a drain never invalidates a string the consumer is still
// holding, and the consumer's copy is a single allocation of exactly the
// drained size.
func (a *streamAccumulator) Drain() (content, thinking string, contentTokens, thinkingTokens int) {
	if a == nil {
		return "", "", 0, 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.dirty {
		return "", "", 0, 0
	}
	content = a.content.String()
	thinking = a.thinking.String()
	contentTokens = a.contentTokens
	thinkingTokens = a.thinkingTokens
	a.content = strings.Builder{}
	a.thinking = strings.Builder{}
	a.contentTokens = 0
	a.thinkingTokens = 0
	a.dirty = false
	return content, thinking, contentTokens, thinkingTokens
}

// Pending reports how many bytes are waiting for the next frame tick. It is the
// O(1) staleness probe: a caller that only wants to know "did anything arrive?"
// pays one mutex acquisition and no copy, which is what makes an idle frame and
// a frame with nothing new cost the same thing.
func (a *streamAccumulator) Pending() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.content.Len() + a.thinking.Len()
}

// Reset drops every pending delta. It is the turn-boundary teardown: a stream
// that was cancelled must not hand its un-drained tail to the next turn, whose
// accumulator is a different conversation.
func (a *streamAccumulator) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.content = strings.Builder{}
	a.thinking = strings.Builder{}
	a.contentTokens = 0
	a.thinkingTokens = 0
	a.dirty = false
	a.mu.Unlock()
}
