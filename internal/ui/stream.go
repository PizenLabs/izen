package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/workspace"
)

// askCodingMaxTokens is the explicit max_tokens output budget for technical /
// coding prompts issued from the interactive stream. 4096 keeps long
// code-generation answers clear of the completion ceiling (finish_reason
// "length"); casual chat keeps its own smaller budget via
// gateway.CasualChatMaxTokens.
const askCodingMaxTokens = 4096

// Stream context lifecycle (decoupled TTFT vs active-stream deadlines).
//
//	pre-TTFT:  the dynamic TTFT deadline (m.ttftDuration: 15s fast models,
//	          up to 90s reasoning/free-tier) bounds the wait for the first
//	          response byte; the transport ResponseHeaderTimeout (10s cloud
//	          / 15s local) remains the socket-level backstop. The footer
//	          countdown renders the same dynamic deadline.
//	post-TTFT: once the first byte arrives the stream is alive. Liveness is
//	          governed by the inter-token idle timeout (reset on every chunk)
//	          under a generous absolute stream-max ceiling — never by the old
//	          fixed 15s total request deadline that expired mid-generation.
const (
	// streamTTFTBudget is the standard-tier pre-TTFT bound. It mirrors the
	// dynamic resolver's standard tier (llm.TTFTStandardTimeout) and remains
	// the documented default; per-turn streams resolve their own deadline
	// via m.ttftDuration().
	streamTTFTBudget = 15 * time.Second
	// streamInterTokenIdle is the post-TTFT liveness bound: any chunk
	// within this window proves the stream alive and resets the deadline.
	streamInterTokenIdle = 30 * time.Second
	// streamMaxDuration is the generous absolute ceiling for one stream.
	streamMaxDuration = 10 * time.Minute
)

// debugLogPayload writes the exact outgoing LLM payload to
// .izen/debug/payload.log so we can prove what the model actually receives on
// each /ask turn. This is purely diagnostic — it appends one JSON line per
// streamCmd invocation and never affects the runtime path.
func debugLogPayload(content string, msgs []ai.Message) {
	dir := filepath.Join(".izen", "debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	// Capture only the final user message and the last 4 history turns to
	// keep the log compact and focused on ordering/duplication evidence.
	last := msgs
	if len(last) > 4 {
		last = last[len(last)-4:]
	}
	entry := struct {
		Time      string       `json:"time"`
		FinalUser string       `json:"final_user_content"`
		Window    []ai.Message `json:"last_messages"`
	}{
		Time:      time.Now().Format(time.RFC3339Nano),
		FinalUser: content,
		Window:    last,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	f, err := os.OpenFile(filepath.Join(dir, "payload.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(data)
}

// injectObjectiveContext prefixes the active human-confirmed objective frame
// onto stream content. Migrated from internal/agents.InjectObjectiveContext so
// the ui package no longer depends on the legacy agents layer; behavior is
// identical.
func injectObjectiveContext(content string, objective *domain.Objective) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return trimmed
	}
	if objective == nil || strings.TrimSpace(objective.RawIntent) == "" {
		return trimmed
	}
	if !objective.HumanConfirmed || objective.CurrentStatus == domain.ObjectiveAnalyzing {
		return trimmed
	}

	budgetStatus := "OK"
	if objective.TokenBudget.RequiresApproval {
		budgetStatus = "WARNING"
	}

	frame := fmt.Sprintf(
		"### ACTIVE OBJECTIVE\nID: %s\nIntent: %s\nStatus: %s\nScope Files: %d\nScope Symbols: %d\nBudget: %s (%d/%d)\n",
		objective.ID,
		objective.RawIntent,
		objective.CurrentStatus,
		len(objective.Scope.Files),
		len(objective.Scope.Symbols),
		budgetStatus,
		objective.TokenBudget.CurrentWeight,
		objective.TokenBudget.Threshold,
	)

	if strings.HasPrefix(trimmed, "### ACTIVE OBJECTIVE\n") {
		return trimmed
	}
	return frame + "\n" + trimmed
}

func (m *model) streamCmd(content string) tea.Cmd {
	// Guard against empty content or unintended/stray submissions
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// ── PER-TURN CONTEXT GOVERNANCE ──────────────────────────────────
	// The /ask async prep (prepareAskStreamCmd) sets askContextGoverned when it
	// already assembled context. Capture it here and clear it IMMEDIATELY so a
	// stale marker can never survive an early return (nil provider, local
	// intent intercept) and leak into the next turn.
	plannerGoverned := m.askContextGoverned && m.resolver.Current() == modes.ModeAsk
	m.askContextGoverned = false

	content = injectObjectiveContext(content, m.sess.ObjectiveState)
	if m.streamCh != nil {
		m.push(roleSystem, "Stream blocked: task active.")
		return nil
	}
	if m.provider == nil {
		m.push(roleSystem, "Stream blocked: no provider.")
		return nil
	}

	// ── LOCAL INTENT INTERCEPTOR ─────────────────────────────────────
	// Intercept common identity/greeting queries locally without calling
	// the LLM API, saving tokens and providing instant responses.
	if response := m.interceptLocalIntent(content); response != "" {
		m.push(roleAI, response)
		return nil
	}

	// Execution heartbeat: mark when the execution lifecycle starts.
	m.executionStartedAt = time.Now()
	// A fresh turn resets the live tok/s estimate (content + reasoning) and
	// the t=0 cost baseline (pricing + prompt tokens). The full request
	// length refines the baseline below once msgs are assembled.
	m.streamLiveTokens = 0
	m.streamBaseInputTokens = estimatePromptTokens(content)
	m.streamInputPricePerM, m.streamOutputPricePerM = m.lookupStreamPricing(m.getActiveModelName())
	m.streamCh = make(chan tea.Msg, 1024)
	m.streaming = true
	m.spinnerFrame = 0
	// A fresh stream starts a new assistant record: the streaming tail is
	// re-established (with a fresh block-boundary separator) on the first token.
	m.streamingDocStart = -1
	m.resetStreamingRenderer()
	// TRUTHFUL PROVIDER STATUS: the loading dock derives its indicator from
	// the authoritative stage — a provider round-trip before the first byte
	// renders as "Model ● waiting", never as "Thinking...".
	m.startShimmer("Waiting for model...", "analyze")
	m.setStage("model", m.getActiveModelName(), stageWaiting)
	m.responseBuffer.Reset()
	m.reasoningBuffer.Reset()
	m.traceBuffer.Reset()
	m.traceExpanded = false
	m.traceWindowStart = 0
	m.traceWindowAnchored = false
	m.pendingReasoningFragment = ""
	if m.thinkingPanel != nil {
		m.thinkingPanel.Reset()
	}
	if m.liveCodePreview != nil {
		m.liveCodePreview.Reset()
	}
	m.sentinelReasoningFlushed = 0
	if m.thinkingBuffer == nil {
		m.thinkingBuffer = NewThinkingBuffer()
	} else {
		m.thinkingBuffer.Reset()
	}
	// Thought duration timer: reset start, clear end on new prompt.
	m.thoughtStartTime = time.Now()
	m.thoughtEndTime = time.Time{}
	if m.activityTree == nil {
		m.activityTree = NewActivityTree()
	} else {
		m.activityTree.Reset()
	}
	if m.streamThrottle == nil {
		m.streamThrottle = NewStreamThrottle()
	} else {
		m.streamThrottle.Reset()
	}
	if m.utf8StreamBuf == nil {
		m.utf8StreamBuf = &stream.StreamBuffer{}
	} else {
		m.utf8StreamBuf.Reset()
	}
	// ── TRANSIENT BUFFER RESET (1-TURN LATENCY FIX) ───────────────────
	// Explicitly clear all accumulated raw-string buffers before launching the
	// stream so the rendering pipeline cannot leak or re-send leftover bytes
	// from the previous turn (the ghost-output / stale-context bug).
	m.streamBuffer = ""
	m.currentStreamContent = ""
	m.resetStreamBlocks()
	m.streamParser = NewIncrementalStreamParser(m.width - 2)
	m.streamParser.Reset()
	if m.sess.ObjectiveState != nil && m.sess.ObjectiveState.HumanConfirmed {
		m.sess.ObjectiveState.CurrentStatus = domain.ObjectiveExecuting
		m.sess.SetObjectiveState(m.sess.ObjectiveState)
		_ = m.sess.Save()
	}

	// Context isolation: ASK single-shot prompts must not carry stale failed history.
	// If the previous turn failed with TTFT timeout (history ends with two consecutive
	// user messages: stale failed prompt + new prompt), prune the stale one before
	// building the LLM context window. This prevents a failed/timeout prompt from
	// polluting the next turn's context unless explicitly in a multi-turn thread.
	if m.sess != nil && len(m.sess.History) >= 2 && m.resolver.Current() == modes.ModeAsk {
		if m.sess.History[len(m.sess.History)-1].Role == "user" && m.sess.History[len(m.sess.History)-2].Role == "user" {
			stale := m.sess.History[len(m.sess.History)-2]
			if stale.Content != content {
				// Remove stale failed user message (second last), keep newest.
				newHist := make([]session.Message, 0, len(m.sess.History)-1)
				newHist = append(newHist, m.sess.History[:len(m.sess.History)-2]...)
				newHist = append(newHist, m.sess.History[len(m.sess.History)-1])
				m.sess.History = newHist
				_ = m.sess.Save()
			}
		}
	}

	var msgs []ai.Message
	// Context isolation for /build: never replay a prior /plan JSON ledger back
	// to the model. When it sees its own plan contract in history, weaker models
	// re-print the plan instead of executing the active task. The staged task
	// list (passed as the current user turn) is the single source of truth.
	buildMode := m.resolver.Current() == modes.ModeBuild
	if history := m.sess.History; len(history) > 0 {
		for _, msg := range history {
			raw := msg.Content
			if buildMode && msg.Role == "assistant" {
				if r := plan.ParseJSONPlan(raw); r != nil && r.Valid && r.Plan != nil {
					continue
				}
			}
			// READS: Never pass viewport-rendered content — only session-persisted raw text.
			msgs = append(msgs, ai.Message{
				Role:    msg.Role,
				Content: raw,
			})
		}
	}

	// ── SLIDING WINDOW TRUNCATION ──────────────────────────────────
	// Keep at most the last 20 history entries (≈10 exchanges) to
	// prevent unbounded token growth across long sessions.
	const maxHistoryMessages = 20
	if len(msgs) > maxHistoryMessages {
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}

	// ABSOLUTE GUARD: content MUST be raw input text, NOT m.Viewport.View() or any
	// concatenation of rendered history + status bar + prompt prefix.
	msgs = append(msgs, ai.Message{Role: "user", Content: content})

	// ── AUTOMATIC FILE CONTEXT INJECTION ──────────────────────
	// Skip injection for casual greetings / small talk — they don't
	// need codebase context and pulling random snippets (config files,
	// release notes, etc.) into the LLM window is both wasteful and
	// the source of hallucinated RAG context on short inputs.
	if m.workspaceRoot != "" && !gateway.IsCasualChat(content) {
		// CONTEXT GOVERNANCE (P3): When the Context Planner already governed
		// the /ask turn (prepareAskStreamCmd assembled budget-fitted context and
		// routed @file references through the FileSource adapter), the
		// ungoverned file-read fallback is skipped entirely. Otherwise —
		// non-/ask paths and graph-less /ask setups — @file reads go through
		// the planner's governed ResolveFileContext when a planner is wired,
		// degrading to an isolated read only when no planner exists.
		if !plannerGoverned {
			augmented := m.injectFileContext(m.workspaceRoot, content, msgs[len(msgs)-1].Content)
			if augmented != "" {
				msgs[len(msgs)-1].Content = augmented
			}
		}
	}

	var systemPrompt string
	// Technical / coding prompts carry an explicit 4096-token output budget
	// so long answers complete without hitting the provider's completion
	// ceiling (finish_reason "length") — never rely on provider defaults
	// (often ~1500-2048 tokens) for code generation.
	maxTokens := askCodingMaxTokens

	if gateway.IsCasualChat(content) {
		systemPrompt = gateway.CasualChatSystemPrompt()
		maxTokens = gateway.CasualChatMaxTokens()
	} else {
		systemPrompt = prompt.ForModeWithUser(m.resolver.Current().String(), m.userName)
		if len(msgs) > 0 && msgs[0].Role == "system" {
			msgs[0].Content = systemPrompt
		} else {
			msgs = append([]ai.Message{{Role: "system", Content: systemPrompt}}, msgs...)
		}
	}

	debugLogPayload(content, msgs)

	// Refine the t=0 prompt baseline with the full assembled request length
	// (system + history + current turn) so C_in covers the billed prompt.
	totalChars := len(systemPrompt)
	for _, msg := range msgs {
		totalChars += len(msg.Content)
	}
	m.initStreamCostTelemetry(totalChars)

	// Capture the channel reference locally so the goroutine (and the
	// ReasoningHandler below, which runs on the producer goroutine during
	// ExecuteStream reads) never reads m.streamCh after Update() clears it to
	// nil. Without this, the deferred close(m.streamCh) would panic with
	// "close of nil channel".
	streamCh := m.streamCh

	req := ai.Request{
		Model:     m.getActiveModelName(),
		Messages:  msgs,
		Stream:    true,
		System:    systemPrompt,
		MaxTokens: maxTokens,
		ReasoningHandler: func(chunk string) error {
			if m.bus != nil {
				m.bus.Publish(events.NewReasoningStream(chunk, false))
			}
			// Reasoning tokens also flow into the typed stream channel so the
			// UI renders them inline in the dimmed thinking style, in arrival
			// order relative to content tokens.
			if chunk != "" {
				streamCh <- thinkingTokenMsg(chunk)
			}
			return nil
		},
	}

	// The request context is derived from the active operation (when one is
	// registered, e.g. a build-context stream) so Ctrl+C cancels the provider
	// stream; otherwise it falls back to a plain background parent.
	// m.streamCancel is the handle handleEmergencyInterrupt and
	// cancelStaleAgentOps already invoke to tear the stream down.
	//
	// DECOUPLED LIFECYCLE (no fixed total deadline):
	//   pre-TTFT  — the dynamic TTFT deadline (ttftTimeout below: 15s fast
	//               models, up to 90s reasoning/free-tier) bounds the wait
	//               for the first response byte; the transport
	//               ResponseHeaderTimeout (10s cloud / 15s local) remains
	//               the socket-level backstop and fails fast with a
	//               phase-identifiable error.
	//   post-TTFT — once bytes flow, liveness is governed by the inter-token
	//               idle watchdog (reset on every chunk) wrapped around the
	//               SSE body below. The context here carries only the generous
	//               stream-max ceiling (10m) so a slow-but-continuous
	//               generation (e.g. 45s of steady tokens) completes instead
	//               of dying to a fixed 15s "context deadline exceeded".
	// Each attempt still defers cancel() so OS sockets force-close on exit.
	// The dynamic TTFT is captured here (synchronously, before the producer
	// goroutine starts) so the first-byte window, the footer countdown and
	// the stall diagnosis all share one deadline for the turn.
	ttftTimeout := m.ttftDuration()
	ctx, cancel := context.WithTimeout(m.operationContext(), streamMaxDuration)
	m.streamCancel = cancel

	// STREAM CONSUMER CONTRACT (deadlock-free):
	// This producer goroutine is the ONLY place that reads from the LLM stream.
	// It MUST NOT acquire any ContextLedger / TaskLedger mutex while waiting for
	// the next token: it merely reads a chunk, appends to a local `full`
	// builder, and dispatches an immutable tokenMsg to the UI channel. All
	// ledger state is committed ONCE, at io.EOF, by the streamDoneMsg handler
	// on the main Bubble Tea goroutine — never per-token. Holding a ledger lock
	// here would serialize the token loop against the TUI renderer and freeze
	// the stream (the historical 108-token stall). The producer only touches
	// the channel, the local buffer, and the captured `streamCh`/`cancel`.
	go func() {
		// ── WORKER LIFETIME (Phase 3) ────────────────────────────────
		// The producer goroutine is a real worker of the current operation:
		// register it so the terminal-lifecycle tests can prove it is released
		// before the operation finalizes. A no-op for plain /ask streams that
		// hold no operation.
		m.spawnOpWorker("stream")
		defer m.releaseOpWorker("stream")

		defer func() {
			if r := recover(); r != nil {
				select {
				case streamCh <- streamErrMsg{err: fmt.Errorf("stream panic: %v", r)}:
				default:
				}
			}
		}()
		defer close(streamCh)
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			streamCh <- streamErrMsg{err: err}
			return
		}
		defer func() { _ = rawStream.Close() }()
		// INTER-TOKEN IDLE WATCHDOG (two-phase liveness): the reader opens
		// with the dynamic TTFT deadline as its first-byte window, then
		// relaxes to the steady inter-token window the instant the first
		// chunk proves the stream alive (see relaxToSteady below). A
		// slow-but-continuous generation never trips it; a stalled socket
		// is force-closed with an identifiable ErrStreamIdleTimeout instead
		// of hanging to the stream-max ceiling. Usage/finish-reason
		// assertions below keep reading from rawStream (the wrapper only
		// carries the byte path).
		idleBody := stream.NewIdleTimeoutReader(rawStream, ttftTimeout)
		defer func() { _ = idleBody.Close() }()
		// Task 2: strict context cancellation — force-close SSE body on interrupt.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				_ = idleBody.Close()
			case <-done:
			}
		}()

		// ── AUTHORITATIVE LIVE USAGE ───────────────────────────────────
		// The streaming indicator must never display a character-count
		// estimate. usageUp exposes the provider's real cumulative usage as
		// usage chunks arrive (Claude reports input at message_start and
		// cumulative output per message_delta; OpenAI-compatible providers
		// report once at the end). emitUsage forwards ONLY authoritative
		// updates (Known && !Estimated) so a partial or final provider count
		// reaches the stage while an unknown/estimated count renders as plain
		// "streaming" with no number.
		var usageUp ai.UsageProvider
		if up, ok := rawStream.(ai.UsageProvider); ok {
			usageUp = up
		}
		var lastUsage = ai.ProviderUsage{}
		emitUsage := func() {
			if usageUp == nil {
				return
			}
			u := usageUp.Usage()
			if !u.Known || u.Estimated {
				return
			}
			if u.PromptTokens == lastUsage.PromptTokens && u.CompletionTokens == lastUsage.CompletionTokens &&
				u.ReasoningTokens == lastUsage.ReasoningTokens {
				return
			}
			lastUsage = u
			streamCh <- streamUsageMsg{input: u.PromptTokens, output: u.CompletionTokens, reasoning: u.ReasoningTokens}
		}

		// Two-phase TTFT: the first chunk (content or thinking) proves the
		// stream alive, so the dynamic first-byte window relaxes to the
		// steady inter-token window from here on.
		relaxedToSteady := false
		relaxToSteady := func() {
			if !relaxedToSteady {
				relaxedToSteady = true
				idleBody.SetIdle(streamInterTokenIdle)
			}
		}
		full, ingestErr := ingestLLMStream(idleBody, m.bus, func(text string) {
			relaxToSteady()
			streamCh <- tokenMsg(text)
			emitUsage()
		}, func(text string) {
			relaxToSteady()
			streamCh <- thinkingTokenMsg(text)
			emitUsage()
		})

		// ── AUTHORITATIVE PROVIDER USAGE ──────────────────────────────
		// The provider's final usage is the single source of truth for the
		// footer token count. Streaming and non-streaming executions converge
		// on the same ProviderUsage contract; the provider's authoritative
		// counts are preserved verbatim and NEVER replaced by a local
		// token-event counter. When the provider reports no usage (Known==false)
		// the values stay 0 so the footer can render "usage unknown" instead of
		// inventing a number.
		tokIn, tokOut := 0, 0
		usageKnown := false
		usageEstimated := false
		if up, ok := rawStream.(ai.UsageProvider); ok {
			u := up.Usage()
			usageKnown = u.Known
			usageEstimated = u.Estimated
			tokIn = u.PromptTokens
			tokOut = u.CompletionTokens
		}
		// LOCAL-ONLY ESTIMATE FALLBACK: reserved strictly for local models
		// (ollama) that genuinely do not report usage metadata. Cloud
		// providers never get a fabricated count — their reported usage is
		// authoritative and 0 stays 0 when the provider billed nothing.
		if !usageKnown && !m.IsCloudModel {
			tokIn = len(content) / 4
			tokOut = len(full) / 4
			usageEstimated = true
		}

		// TRUNCATION DETECTION: when the provider reports finish_reason ==
		// "length", the response was cut off by the API completion ceiling, not
		// finished naturally. Flag it so the streamDoneMsg handler can surface a
		// visible notice instead of silently presenting an incomplete answer.
		truncated := false
		if frp, ok := rawStream.(ai.FinishReasonProvider); ok && frp.FinishReason() == "length" {
			truncated = true
		}

		if ingestErr != nil {
			// "Explicit Over Implicit": the stream reader accumulates the
			// provider-reported usage (or a character estimate) even when it
			// was interrupted — carry it on the error message so the footer
			// reports consumed tokens instead of a silent 0.
			streamCh <- streamErrMsg{err: ingestErr, content: full, tokenInput: tokIn, tokenOutput: tokOut, usageEstimated: usageEstimated}
			return
		}
		streamCh <- streamDoneMsg{
			content:        full,
			tokenInput:     tokIn,
			tokenOutput:    tokOut,
			usageEstimated: usageEstimated,
			truncated:      truncated,
		}
	}()

	return tea.Batch(m.streamTraceCmd(), m.readStream(), m.smoothStreamTickCmd(), m.shimmerTickCmd())
}

// streamTraceCmd emits the most recent /ask planner trace (thought-route panel)
// as a traceUpdateMsg, if one was produced by the async ask prep
// (prepareAskStreamCmd). tea.Batch
// drops nil cmds, so returning nil when there is no trace is safe.
func (m *model) streamTraceCmd() tea.Cmd {
	if m.lastAskTrace == nil {
		return nil
	}
	tr := m.lastAskTrace
	m.lastAskTrace = nil
	return func() tea.Msg { return traceUpdateMsg{trace: tr} }
}

// ingestLLMStream reads raw bytes from r, applies UTF-8 rune-safe buffering
// and thought/content separation, and returns the assembled response content.
//
// Raw LLM chunks are NOT aligned to UTF-8 rune boundaries. Slicing them
// directly (string(buf[:n])) can split a multi-byte rune across two reads and
// corrupt the markdown answer with replacement chars. RuneBuffer holds
// incomplete runes until they complete. The Classifier then classifies each
// frame: reasoning (<thought>…</thought> / reasoning sentinels) is published
// to the event bus as EventReasoningStream AND routed to emitThinking (so the
// UI can render it inline in a dimmed style) — it NEVER enters the response
// pipeline; only content frames reach emitContent. Escapes are preserved
// verbatim through both layers. A terminal ReasoningStream event (empty chunk,
// IsComplete) is always published so the UI can collapse the thinking box.
func ingestLLMStream(r io.Reader, bus *events.Bus, emitContent func(string), emitThinking func(string)) (string, error) {
	var full strings.Builder
	runeBuf := stream.NewRuneBuffer()
	classifier := stream.NewClassifier()

	publishReasoning := func(text string) {
		if bus != nil {
			bus.Publish(events.NewReasoningStream(text, false))
		}
	}

	emitFrame := func(tok stream.Token) {
		if tok.Kind == stream.TokenKindThinking {
			if tok.Text != "" {
				publishReasoning(tok.Text)
				if emitThinking != nil {
					emitThinking(tok.Text)
				}
			}
			return
		}
		if tok.Text == "" {
			return
		}
		full.WriteString(tok.Text)
		if emitContent != nil {
			emitContent(tok.Text)
		}
	}

	flushBuffered := func() {
		if rem := runeBuf.Flush(); rem != "" {
			classifier.Write(rem, emitFrame)
		}
		classifier.Flush(emitFrame)
	}

	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if text := runeBuf.Write(buf[:n]); text != "" {
				classifier.Write(text, emitFrame)
			}
		}
		if err == io.EOF {
			// Release any trailing incomplete rune and final partial markers.
			flushBuffered()
			// Terminal reasoning event: closes the reasoning block so the UI
			// can collapse the thinking box into compact mode.
			if bus != nil {
				bus.Publish(events.NewReasoningStream("", true))
			}
			return full.String(), nil
		}
		if err != nil {
			// Flush buffered runes/partials before reporting the error so no
			// already-received bytes are lost.
			flushBuffered()
			return full.String(), err
		}
	}
}

// injectFileContext resolves explicit file references and injects their content
// into the LLM user turn. CONTEXT GOVERNANCE (P3): when a Context Planner is
// wired, the read is routed through Planner.ResolveFileContext, which delegates
// the actual disk read to the FileSource adapter and applies the planner's
// budget/ranking/compression policy — Context Governance never bypasses the I/O
// layer. The isolated os.ReadFile fallback is reserved for setups with no
// planner (headless / graph-less) and carries the same @file resolution.
func (m *model) injectFileContext(workspaceRoot, prompt, userContent string) string {
	if p := m.contextPlanner(); p != nil {
		if governed, err := p.ResolveFileContext(context.Background(), prompt); err == nil && governed != "" {
			return userContent + "\n\n## GOVERNED FILE CONTEXT\n" + governed
		}
	}
	resolver := workspace.NewTargetFileResolver(workspaceRoot)
	target := resolver.Resolve(prompt)
	if target == "" {
		return userContent
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, target))
	if err != nil {
		return userContent
	}
	return userContent + "\n\n## Workspace File: " + target + "\n```\n" + string(data) + "\n```"
}

func (m *model) readStream() tea.Cmd {
	return func() tea.Msg {
		// Defensive: if the channel is nil (already cleaned up), return
		// immediately instead of blocking forever.
		if m.streamCh == nil {
			return nil
		}
		msg, ok := <-m.streamCh
		if !ok {
			return nil
		}
		return msg
	}
}

// readExecStream reads from the executor streaming channel (used by $prompt/$hot).
func (m *model) readExecStream() tea.Cmd {
	return func() tea.Msg {
		if m.execStreamCh == nil {
			return nil
		}
		msg, ok := <-m.execStreamCh
		if !ok {
			return nil
		}
		return msg
	}
}

// greetingResponses provides variety when responding to a first-turn greeting.
var greetingResponses = []string{
	"Hello %s! How can I assist you today?",
	"Hey %s! What are we building today?",
	"Yo %s! Ready to crush some code?",
	"Ciao %s! How can I help you right now?",
	"What's up %s! What can I do for you today?",
}

// greetingPhrases is the set of normalized greeting inputs we answer locally
// on the very first turn of a session, instead of spending a model call on
// small talk. Keep entries lowercase and space-collapsed — see
// normalizeIntent for how raw input is normalized before lookup.
var greetingPhrases = map[string]struct{}{
	"hi":             {},
	"hii":            {},
	"hiii":           {},
	"hello":          {},
	"helo":           {},
	"hey":            {},
	"heya":           {},
	"hey there":      {},
	"hi there":       {},
	"yo":             {},
	"yow":            {},
	"sup":            {},
	"wassup":         {},
	"whats up":       {},
	"what's up":      {},
	"howdy":          {},
	"greetings":      {},
	"good morning":   {},
	"good afternoon": {},
	"good evening":   {},
	"morning":        {},
}

// identityQuestions maps fixed identity/self-referential questions to
// canned answers. These are answered regardless of session history length,
// since a user may reasonably ask "who are you" mid-session.
var identityQuestions = map[string]string{
	"what is your name": "I am IZEN, a fast CLI coding companion.",
	"whats your name":   "I am IZEN, a fast CLI coding companion.",
	"what's your name":  "I am IZEN, a fast CLI coding companion.",
	"who are you":       "I am IZEN, a fast CLI coding companion.",
}

// normalizeIntent lowercases, trims leading/trailing punctuation and
// whitespace, and collapses internal whitespace so that inputs like
// "  Hii!! " and "hi" normalize to the same lookup key.
func normalizeIntent(s string) string {
	s = strings.ToLower(s)
	s = strings.Trim(s, " \t\n.,!?:;~-_")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// interceptLocalIntent checks whether the user input matches common identity
// or greeting patterns that can be answered locally without calling the LLM.
// Returns a non-empty response string if intercepted, empty string otherwise.
func (m *model) interceptLocalIntent(content string) string {
	normalized := normalizeIntent(content)

	if answer, ok := identityQuestions[normalized]; ok {
		return answer
	}

	switch normalized {
	case "what is my name", "whats my name", "what's my name", "who am i", "my name":
		return "Your name is " + m.userName + "."
	}

	if _, ok := greetingPhrases[normalized]; ok && len(m.sess.History) == 0 {
		tpl := greetingResponses[rand.Intn(len(greetingResponses))]
		return fmt.Sprintf(tpl, m.userName)
	}

	return ""
}
