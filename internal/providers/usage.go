package providers

// streamUsageTracker accumulates cumulative token accounting for an SSE stream
// reader. It records the authoritative provider-reported usage whenever a usage
// chunk arrives; when the stream is interrupted (context deadline / user cancel)
// before that chunk, it falls back to a character-count estimate of the output
// tokens so tokens already billed by the provider are never silently zeroed in
// local telemetry ("Explicit Over Implicit").
//
// The tracker is only ever read by the single consumer goroutine that drains
// the stream (after Close), so no synchronization is required.
type streamUsageTracker struct {
	promptTokens     int
	completionTokens int
	hasAuthoritative bool
	outputChars      int
	interrupted      bool
}

// recordUsage stores the authoritative provider-reported usage. Call it on
// every chunk that carries a complete usage object; the last one wins.
func (t *streamUsageTracker) recordUsage(prompt, completion int) {
	t.promptTokens = prompt
	t.completionTokens = completion
	t.hasAuthoritative = true
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

// recordOutput accumulates streamed output characters (content + reasoning)
// that were emitted to the consumer.
func (t *streamUsageTracker) recordOutput(n int) {
	if n > 0 {
		t.outputChars += n
	}
}

// markInterrupted flags a non-EOF stream termination (e.g. context deadline).
func (t *streamUsageTracker) markInterrupted() {
	t.interrupted = true
}

// Usage returns the authoritative token counts when a usage chunk arrived;
// otherwise it returns a character-count estimate of the output tokens
// (chars/4). Input tokens are unknown without a usage chunk and report 0 — the
// consumer's prompt-length estimate covers them.
func (t *streamUsageTracker) Usage() (input, output int) {
	if t.hasAuthoritative {
		return t.promptTokens, t.completionTokens
	}
	if t.outputChars > 0 {
		return 0, t.outputChars / 4
	}
	return 0, 0
}

// Interrupted reports whether the stream ended before a natural EOF.
func (t *streamUsageTracker) Interrupted() bool { return t.interrupted }

// Estimated reports whether Usage returned a character-count estimate rather
// than the authoritative provider-reported counts.
func (t *streamUsageTracker) Estimated() bool { return !t.hasAuthoritative }
