package providers

import (
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// streamUsageTracker accumulates cumulative token accounting for an SSE stream
// reader. It records the authoritative provider-reported usage whenever a usage
// chunk arrives; when the stream is interrupted (context deadline / user cancel)
// before that chunk, it reports the usage as ESTIMATED from the character count
// that actually streamed — never a silent unknown, so tokens already billed by
// the provider are not dropped from local telemetry ("Explicit Over Implicit").
//
// The tracker is only ever read by the single consumer goroutine that drains
// the stream (after Close), so no synchronization is required.
type streamUsageTracker struct {
	promptTokens     int
	completionTokens int
	cachedTokens     int
	reasoningTokens  int
	totalTokens      int
	hasAuthoritative bool
	outputChars      int
	reasoningChars   int
	interrupted      bool
	// httpAttempts counts every transport round-trip a single logical
	// invocation performed (Phase 7 P5 retry forensics: 429 backoff and the
	// 400 reasoning-schema retry add attempts, never new invocations).
	httpAttempts       int
	rateLimitedRetries int

	// RequestStartedAt is set by the provider when the request is dispatched;
	// FirstTokenAt is latched on the first emitted output byte; CompletedAt is
	// latched at the natural stream end ([DONE] / EOF).
	requestStartedAt time.Time
	firstTokenAt     time.Time
	completedAt      time.Time
	finishReason     string
}

// markRequestStarted latches the request dispatch time. It is the provider's
// responsibility to call it before the stream begins.
func (t *streamUsageTracker) markRequestStarted(now time.Time) {
	if t.requestStartedAt.IsZero() {
		t.requestStartedAt = now
	}
}

// recordUsageFull stores the full provider-reported usage object including
// cached and reasoning token splits when the provider exposes them.
// TASK: Fix 2x double-counting — this is EXCLUSIVE assignment, not additive.
// When the final API response.Usage arrives, it REPLACES any intermediate
// character-count estimates (outputChars/4). The estimate is discarded; the
// authoritative provider counts are the ground truth (single-source event).
func (t *streamUsageTracker) recordUsageFull(u ai.ProviderUsage) {
	// FIX: Replace additive accumulator with explicit assignment (single-source).
	// Discard intermediate stream estimates — do NOT add final Usage on top.
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 && !u.Known {
		return
	}
	t.promptTokens = u.PromptTokens
	t.completionTokens = u.CompletionTokens
	t.cachedTokens = u.CachedTokens
	t.reasoningTokens = u.ReasoningTokens
	t.totalTokens = u.TotalTokens
	if t.totalTokens == 0 {
		t.totalTokens = u.PromptTokens + u.CompletionTokens + u.ReasoningTokens
	}
	t.finishReason = u.FinishReason
	t.hasAuthoritative = true
	// Discard character-count estimate so Usage() never double-counts.
	// The authoritative provider usage is the single source of truth.
	t.outputChars = 0
	t.reasoningChars = 0
}

// SetUsage is the explicit assignment API the telemetry layer must use.
// It replaces any prior estimate; it never accumulates.
func (t *streamUsageTracker) SetUsage(prompt, completion int) {
	t.promptTokens = prompt
	t.completionTokens = completion
	t.totalTokens = prompt + completion
	t.hasAuthoritative = true
	t.outputChars = 0
	t.reasoningChars = 0
}

// recordInputTokens stores only the authoritative input-token count (used by
// providers that report input and output usage in separate stream events, e.g.
// Anthropic's message_start / message_delta). It never clears the output count.
func (t *streamUsageTracker) recordInputTokens(n int) {
	t.promptTokens = n
	t.hasAuthoritative = true
}

// recordOutputTokens stores only the authoritative output-token count (see
// recordInputTokens). It never clears the input count.
func (t *streamUsageTracker) recordOutputTokens(n int) {
	t.completionTokens = n
	t.hasAuthoritative = true
}

// recordTransport surfaces retry forensics on the usage record (Phase 7 P5):
// attempts is the total HTTP round-trips the single logical invocation made,
// rateLimitedRetries how many of them were 429 rate-limit retries. This lets
// consumers distinguish "the provider recovered from transient throttling
// inside ONE invocation" from "the model was invoked N times".
func (t *streamUsageTracker) recordTransport(attempts, rateLimitedRetries int) {
	t.httpAttempts = attempts
	t.rateLimitedRetries = rateLimitedRetries
}

// recordOutput accumulates streamed OUTPUT content characters that were emitted
// to the consumer (Phase 7 P6: reasoning characters are accounted separately
// via recordReasoning and must never inflate the output-char estimate). It
// latches the first-token timestamp.
func (t *streamUsageTracker) recordOutput(n int) {
	if n <= 0 {
		return
	}
	if t.firstTokenAt.IsZero() {
		t.firstTokenAt = time.Now()
	}
	t.outputChars += n
}

// recordReasoning accumulates streamed REASONING characters separately from the
// visible output content (Phase 7 P6). Providers may include reasoning tokens
// inside completion/output usage, so they are part of billed provider usage
// when reported; the character estimate keeps them separate only to avoid
// inventing visible-output tokens from hidden reasoning text.
func (t *streamUsageTracker) recordReasoning(n int) {
	if n <= 0 {
		return
	}
	if t.firstTokenAt.IsZero() {
		t.firstTokenAt = time.Now()
	}
	t.reasoningChars += n
}

// markInterrupted flags a non-EOF stream termination (e.g. context deadline).
func (t *streamUsageTracker) markInterrupted() {
	t.interrupted = true
}

// markCompleted latches the natural stream-end timestamp and the terminal
// finish_reason. It is idempotent.
func (t *streamUsageTracker) markCompleted(now time.Time, finishReason string) {
	if t.completedAt.IsZero() {
		t.completedAt = now
	}
	if finishReason != "" {
		t.finishReason = finishReason
	}
}

// Usage returns the authoritative provider usage when a usage chunk arrived;
// otherwise it reports the output as an ESTIMATE derived from the character
// count that actually streamed (chars/4). Either way Known is true so the
// renderer can display a count; a genuinely unknown usage (no bytes, no chunk)
// returns Known=false.
func (t *streamUsageTracker) Usage() ai.ProviderUsage {
	u := ai.ProviderUsage{
		RequestStartedAt:   t.requestStartedAt,
		FirstTokenAt:       t.firstTokenAt,
		CompletedAt:        t.completedAt,
		FinishReason:       t.finishReason,
		HTTPAttempts:       t.httpAttempts,
		RateLimitedRetries: t.rateLimitedRetries,
	}
	if t.hasAuthoritative {
		u.Known = true
		u.PromptTokens = t.promptTokens
		u.CompletionTokens = t.completionTokens
		u.CachedTokens = t.cachedTokens
		u.ReasoningTokens = t.reasoningTokens
		u.TotalTokens = t.totalTokens
		if u.TotalTokens == 0 {
			u.TotalTokens = u.PromptTokens + u.CompletionTokens + u.ReasoningTokens
		}
		return u
	}
	if t.outputChars > 0 {
		// Interrupted before the usage chunk: the provider billed output that
		// never got a final usage event. Report the character estimate with
		// the Estimated flag so telemetry never zeroes billed work while
		// still being explicit that the count is an estimate, not provider
		// truth. Input tokens are unknown and stay 0 (Known input is only set
		// by an authoritative chunk).
		u.Known = true
		u.Estimated = true
		u.CompletionTokens = t.outputChars / 4
		u.TotalTokens = u.CompletionTokens
		return u
	}
	return u
}

// Interrupted reports whether the stream ended before a natural EOF.
func (t *streamUsageTracker) Interrupted() bool { return t.interrupted }

// openAICompatibleUsage converts the standard OpenAI-compatible usage object
// (prompt/completion/total) into the authoritative ai.ProviderUsage contract.
// The provider returns a usage object on every response, so Known is true.
func openAICompatibleUsage(prompt, completion, total int) ai.ProviderUsage {
	out := ai.ProviderUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		Known:            true,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = prompt + completion
	}
	return out
}

// Estimated reports whether Usage returned a character-count estimate rather
// than the authoritative provider-reported counts.
func (t *streamUsageTracker) Estimated() bool { return !t.hasAuthoritative && t.outputChars > 0 }
