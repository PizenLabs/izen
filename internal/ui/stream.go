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

	"github.com/PizenLabs/izen/internal/agents"
	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/workspace"
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

	content = agents.InjectObjectiveContext(content, m.sess.ObjectiveState)
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

	m.streamCh = make(chan tea.Msg, 1024)
	m.streaming = true
	m.spinnerFrame = 0
	// TRUTHFUL PROVIDER STATUS: the loading dock derives its indicator from
	// the authoritative stage — a provider round-trip before the first byte
	// renders as "Model ● waiting", never as "Thinking...".
	m.startShimmer("Waiting for model...", "analyze")
	m.setStage("model", m.cfg.ActiveModelName(), stageWaiting)
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
	var maxTokens int

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

	// Capture the channel reference locally so the goroutine (and the
	// ReasoningHandler below, which runs on the producer goroutine during
	// ExecuteStream reads) never reads m.streamCh after Update() clears it to
	// nil. Without this, the deferred close(m.streamCh) would panic with
	// "close of nil channel".
	streamCh := m.streamCh

	req := ai.Request{
		Model:     m.cfg.ActiveModelName(),
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
	// stream; otherwise it falls back to a plain background parent and only the
	// 5-minute ceiling applies. m.streamCancel is the handle
	// handleEmergencyInterrupt and cancelStaleAgentOps already invoke to tear
	// the stream down.
	ctx, cancel := context.WithTimeout(m.operationContext(), 5*time.Minute)
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

		full, ingestErr := ingestLLMStream(rawStream, m.bus, func(text string) {
			streamCh <- tokenMsg(text)
		}, func(text string) {
			streamCh <- thinkingTokenMsg(text)
		})

		type usageProvider interface {
			Usage() (input, output int)
		}
		tokIn, tokOut := 0, 0
		if up, ok := rawStream.(usageProvider); ok {
			tokIn, tokOut = up.Usage()
		}
		// LOCAL-ONLY ESTIMATE FALLBACK: the character-count estimate (/4) is a
		// stand-in reserved strictly for local models (ollama) that genuinely
		// do not report usage metadata. For cloud providers the provider's
		// final-chunk usage is authoritative: if it reports 0/0 the values are
		// left as 0 so the footer shows only what the provider actually billed
		// — never an invented number that diverges from the dashboard.
		if tokIn == 0 && tokOut == 0 && !m.IsCloudModel {
			tokIn = len(content) / 4
			tokOut = len(full) / 4
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
			streamCh <- streamErrMsg{err: ingestErr, content: full, tokenInput: tokIn, tokenOutput: tokOut}
			return
		}
		streamCh <- streamDoneMsg{
			content:     full,
			tokenInput:  tokIn,
			tokenOutput: tokOut,
			truncated:   truncated,
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
