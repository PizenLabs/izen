package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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
//
// Phase 6.4.5 Global UI Telemetry Binding: usage events bind directly to the
// global footer view model (live turn mirrors + usage-known flag) so token
// metrics render continuously across ALL system states (plan, investigate,
// ask, execute) — never gated on a specific execution mode.
func (m *model) ingestStreamUsage(input, output, reasoning int) {
	m.setStageMetrics(0, 0, output)
	if total := output + reasoning; total > m.streamLiveTokens {
		m.streamLiveTokens = total
	}
	if input > 0 {
		m.streamBaseInputTokens = input
	}
	if input > 0 || output > 0 || reasoning > 0 {
		m.markUsageKnown()
	}
	if m.thinkingBuffer != nil && reasoning > 0 {
		m.thinkingBuffer.SetReasoningTokens(reasoning)
	}
	// ── PHASE 15: TELEMETRY IS AN IN-PLACE REPLACE, NOT A RECORD ─────
	// The provider-reported prompt count is the context the model actually
	// received, measured by the authority that knows best. It belongs on the fixed
	// HUD, where the previous value is still on screen to be compared against.
	// Appending it to the conversation would push the value it supersedes out of
	// view, which is the opposite of what a live counter is for.
	if input > 0 {
		m.routeContextTokens(input)
	}
}

// ── Overflow Ring Drain (frame-flush pass) ────────────────────────────────
// drainStreamRing releases ONE FRAME'S WORTH of tokens parked in the lock-free
// overflow ring and ingests them through the shared paths above.
//
// It is called from the FrameTickMsg flush pass (the UI event loop's 30FPS frame
// loop) and — in its DrainAll form, drainStreamRingAll — from the terminal
// stream handlers (streamDoneMsg / streamErrMsg / interrupt teardown) so no
// overflow token can ever be left unrendered when a stream ends. Content
// ingested here lands in the same utf8StreamBuf/throttle buffers the immediate
// flush of the frame drains, so the emission stays single-pass and the repaint
// gate stays single-flight.
//
// BATCHING (see model_stream.go): a steady state releases TokensPerFrame tokens
// per tick; a backlogged queue releases proportionally more so it converges to
// empty instead of displaying a persistent delay. The batch is a VISUAL LATENCY
// bound only — the terminal drain is exhaustive, so a paced batch can never
// drop a token.
func (m *model) drainStreamRing() {
	if m.streamRing == nil && m.tokenPacer == nil {
		return
	}
	m.ingestPacedTokens(false)
}

// drainStreamRingAll is the terminal drain: it releases every queued token
// before the stream is sealed, so no received byte is left behind in the ring
// when the stream ends, errors, or is interrupted.
func (m *model) drainStreamRingAll() {
	if m.streamAccum != nil {
		m.drainStreamAccumulatorAll()
	}
	if m.streamRing == nil && m.tokenPacer == nil {
		return
	}
	m.ingestPacedTokens(true)
}

// ── Accumulator drain (frame pass) ──────────────────────────────────────────
//
// # WHERE THE DELTAS CROSS
//
// The provider goroutine appends to m.streamAccum and returns. That is the last
// thing it does with a token. The bytes become model state HERE, on the frame
// tick, on the UI goroutine — which is what makes it safe for the accumulator to
// be a plain mutex-protected buffer rather than a concurrent structure the rest
// of the model would have to defend against everywhere else.
//
// # WHY THE BATCH IS ONE INGEST, NOT N
//
// ingestContentToken is a pure append plus a few counter advances, so ingesting
// a 60-chunk batch through it once is the same state as ingesting 60 chunks
// through it 60 times — with two differences that both matter. The per-chunk
// token estimate is summed by the accumulator at append time, so the footer rate
// is unchanged. And the first-byte transition happens on the frame that actually
// revealed the first byte, which is the frame the reader will see anyway; a
// per-token path would have flipped the shimmer 60ms earlier for a reader who
// was never going to see the intermediate frame.

// ── Producer-side lane selection ────────────────────────────────────────────

// accumulateStreamMsg routes one producer message and reports whether it was a
// token delta — i.e. whether it belongs in the accumulator and must NEVER
// become a tea.Msg.
//
// It is a named function rather than an inline type switch because the whole
// point of the change is that there is now exactly ONE place where a token can
// be diverted away from the event loop, and a lane decision that only exists
// inside a closure inside a 700-line function is a lane decision no test can
// reach. Everything the provider produces falls into exactly one of two
// buckets here, and a third bucket is a bug: a token delta that reaches the
// channel queue is the latency problem this file exists to remove, and a control
// message that lands in the accumulator would be silently reordered against the
// content around it.
func accumulateStreamMsg(accum *streamAccumulator, msg tea.Msg) bool {
	switch v := msg.(type) {
	case tokenMsg:
		accum.AppendContent(string(v))
		return true
	case thinkingTokenMsg:
		accum.AppendThinking(string(v))
		return true
	}
	return false
}

// drainStreamAccumulator promotes every delta accumulated since the last frame
// into model state, in arrival order, exactly once.
//
// It is the ONLY reader of m.streamAccum. Both the steady-state frame tick and
// the terminal drains (stream completion, stream error, interrupt teardown) go
// through here, because a drain path that exists in two places is a drain path
// that eventually skips one of them and drops a token at the end of a stream —
// which is the one place a dropped token is always visible.
func (m *model) drainStreamAccumulator() {
	if m.streamAccum == nil {
		return
	}
	content, thinking, contentTokens, thinkingTokens := m.streamAccum.Drain()
	if content != "" {
		m.ingestStreamBatch(content, contentTokens, false)
	}
	if thinking != "" {
		m.ingestStreamBatch(thinking, thinkingTokens, true)
	}
}

// drainStreamAccumulatorAll is the terminal form of the drain: it is identical,
// because the frame drain is already exhaustive over what has been appended. The
// separate name exists so every teardown path says out loud that it is the last
// chance a delta can reach the viewport, which is the invariant a reader
// notices immediately when it is violated.
func (m *model) drainStreamAccumulatorAll() {
	m.drainStreamAccumulator()
}

// ingestStreamBatch is the batched body of the two per-token ingest paths.
//
// It deliberately mirrors ingestContentToken/ingestThinkingToken field for field
// rather than calling them per chunk: a loop over chunks would restore exactly
// the O(chunks) Update-loop-shaped work this batching exists to remove, and the
// fields those helpers touch do not depend on chunk boundaries at all — the only
// chunk-sensitive quantity is the live token estimate, and the accumulator has
// already summed it per chunk so the total is invariant under batching.
func (m *model) ingestStreamBatch(batch string, tokens int, reasoning bool) {
	if batch == "" {
		return
	}
	// ── AUTHORITATIVE STAGE: real provider bytes are arriving ──
	// The live tok/s estimate advances by the batch's per-chunk sum; the
	// authoritative count is untouched (streamUsageMsg owns it).
	//
	// The sum was taken on the provider's RAW bytes, at append time, per chunk.
	// That is a deliberate difference from the per-token path, which estimated
	// on the sanitized chunk: the meter is a measure of what the provider sent,
	// and estimating before sanitization keeps the sum invariant under batching
	// (sanitizing a joined batch is not the same operation as sanitizing each
	// chunk). It is an ESTIMATE and is explicitly never the authoritative
	// count, so the only thing it can move is a "~N tok/s" label.
	m.streamLiveTokens += tokens
	m.setStage("model", m.getActiveModelName(), stageStreaming)

	// ONE sanitization for the whole batch, applied at the point every
	// consumer reads from. The per-token path sanitized each chunk and wrote
	// the result to every buffer; the batched path must do the same, because
	// the buffers that matter here are the ones that can outlive the frame —
	// responseBuffer is the fallback source of the FINAL ANSWER, and traceBuffer
	// is what Ctrl+E shows. Writing the provider's raw bytes into either would
	// put terminal control sequences into text the user reads and copies.
	clean := SanitizeForIngest(batch)
	if reasoning {
		// Reasoning never enters the content pipeline: it renders in the dimmed
		// thinking style and is stripped from the answer.
		m.ensureStreamBlocks().Append(KindThinking, clean)
	} else {
		// SMOOTH CLEARING: the first real content delta replaces the shimmer
		// loading line with the streaming output. This is the frame that
		// reveals the first byte, so this is the frame the shimmer must yield
		// on — the per-token path flipped it up to one frame earlier for a
		// reader who was never going to see the intermediate frame.
		if m.shimmerActive {
			m.stopShimmer()
		}
		// Inter-token idle deadline: arm a rolling streamInterTokenIdle (30s)
		// deadline on the first batch and reset on every subsequent one.
		if m.streamCancel != nil && m.streamInterTokenDeadline.IsZero() {
			m.streamInterTokenDeadline = time.Now().Add(streamInterTokenIdle)
		} else if !m.streamInterTokenDeadline.IsZero() {
			m.streamInterTokenDeadline = time.Now().Add(streamInterTokenIdle)
		}
		m.responseBuffer.WriteString(clean)
		m.traceBuffer.WriteString(clean)
		// The UTF-8 safe byte buffer is the SOLE content emitter: it is the
		// only place a content delta is written, so no byte can be emitted
		// twice by the legacy throttle/streamBuffer fallbacks below.
		switch {
		case m.utf8StreamBuf != nil:
			m.utf8StreamBuf.Append([]byte(clean))
		case m.streamThrottle != nil:
			m.streamThrottle.Write(clean)
		default:
			m.streamBuffer += clean
		}
		if m.streamParser != nil {
			m.streamParser.ProcessChunk(clean)
		}
	}

	// Full stream transparency: the stream is retained in the active
	// ThinkingBuffer (the Ctrl+O panel's source) exactly as the per-token
	// ThoughtBufferUpdatedMsg protocol retained it — 100% of the output is kept.
	// Batched, because a per-chunk append is what used to schedule a
	// refreshViewportContent per token. A /clear seals the surface, and a
	// sealed surface must not be resurrected by bytes that were still in flight.
	if !m.activitySurfaceSealed {
		if m.thinkingBuffer == nil {
			m.thinkingBuffer = NewThinkingBuffer()
		}
		m.thinkingBuffer.Append(clean)
	}
}
