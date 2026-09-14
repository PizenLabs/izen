package ui

import "time"

// ── Shared Stream Ingestion (tokenMsg / thinkingTokenMsg / streamUsageMsg) ──
//
// The bodies of the three high-frequency stream handlers are extracted here so
// the frame-pass ring drain can ingest overflow tokens through the SAME
// lock-free code path as the channel-delivered messages. Each helper is a pure
// memory append + counter advance: it MUST NOT acquire any ContextLedger /
// TaskLedger mutex, MUST NOT invoke markdown AST parsing or table layout, and
// MUST NOT issue an immediate repaint (rendering stays behind the 30FPS
// single-flight gate).

// ingestContentToken appends one content chunk to the local stream buffers and
// advances the live token estimate. It is the shared body of the tokenMsg
// handler and drainStreamRing.
func (m *model) ingestContentToken(raw string) {
	// SMOOTH CLEARING: the first content token replaces the shimmer loading
	// line with the streaming output.
	if raw != "" && m.shimmerActive {
		m.stopShimmer()
	}
	m.responseBuffer.WriteString(raw)
	// ── AUTHORITATIVE STAGE: real provider tokens are arriving ──
	// Only content bytes received from the provider mark the stage as
	// streaming; the live tok/s estimate advances per chunk (estimate only,
	// never the authoritative count — streamUsageMsg owns that).
	m.streamLiveTokens += estimateStreamTokens(raw)
	// Inter-token idle deadline: arm a rolling streamInterTokenIdle (30s)
	// deadline on the first chunk and reset on every subsequent chunk.
	if raw != "" && m.streamCancel != nil && m.streamInterTokenDeadline.IsZero() {
		m.streamInterTokenDeadline = time.Now().Add(streamInterTokenIdle)
	} else if raw != "" && !m.streamInterTokenDeadline.IsZero() {
		m.streamInterTokenDeadline = time.Now().Add(streamInterTokenIdle)
	}
	if raw != "" {
		m.setStage("model", m.getActiveModelName(), stageStreaming)
	}
	m.traceBuffer.WriteString(raw)
	// UTF-8 safe byte buffer (Option A cumulative source of truth): while
	// utf8StreamBuf is active it is the SOLE content emitter (drained by
	// FrameTickMsg). Raw tokens are appended ONLY here — never additionally
	// to the throttle/legacy buffers — so no byte can be emitted twice.
	switch {
	case m.utf8StreamBuf != nil:
		m.utf8StreamBuf.Append([]byte(raw))
	case m.streamThrottle != nil:
		m.streamThrottle.Write(raw)
	default:
		m.streamBuffer += raw
	}
	if m.streamParser != nil {
		m.streamParser.ProcessChunk(raw)
	}
}

// ingestThinkingToken appends one reasoning chunk to the typed stream buffer
// (dimmed thinking style) and advances the live token estimate. It never
// enters the content pipeline.
func (m *model) ingestThinkingToken(sanitized string) {
	m.streamLiveTokens += estimateStreamTokens(sanitized)
	m.setStage("model", m.getActiveModelName(), stageStreaming)
	m.ensureStreamBlocks().Append(KindThinking, sanitized)
}

// ingestStreamUsage feeds an authoritative provider-reported usage update into
// the live stage metrics without ever fabricating a count: the output count is
// set verbatim, the live estimate is floored by it, an authoritative prompt
// count replaces the t=0 chars/4 estimate, and the reasoning split backs the
// compact thought summary.
func (m *model) ingestStreamUsage(input, output, reasoning int) {
	m.setStageMetrics(0, 0, output)
	if total := output + reasoning; total > m.streamLiveTokens {
		m.streamLiveTokens = total
	}
	if input > 0 {
		m.streamBaseInputTokens = input
	}
	if m.thinkingBuffer != nil && reasoning > 0 {
		m.thinkingBuffer.SetReasoningTokens(reasoning)
	}
}

// ── Overflow Ring Drain (frame-flush pass) ────────────────────────────────
// drainStreamRing pops every currently-buffered message from the lock-free
// overflow ring and ingests it through the shared paths above. It is called
// from the FrameTickMsg flush pass (the UI event loop's 30FPS frame loop) and
// from the terminal stream handlers (streamDoneMsg / streamErrMsg / interrupt
// teardown) so no overflow token can ever be left unrendered when a stream
// ends. Content ingested here lands in the same utf8StreamBuf/throttle buffers
// the immediate flush of the frame drains, so the emission stays single-pass
// and the repaint gate stays single-flight.
func (m *model) drainStreamRing() {
	if m.streamRing == nil {
		return
	}
	for {
		msg, ok := m.streamRing.Pop()
		if !ok {
			return
		}
		switch t := msg.(type) {
		case tokenMsg:
			m.ingestContentToken(SanitizeForIngest(string(t)))
		case thinkingTokenMsg:
			m.ingestThinkingToken(SanitizeForIngest(string(t)))
		case streamUsageMsg:
			m.ingestStreamUsage(t.input, t.output, t.reasoning)
		}
	}
}
