package ui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/agents"
	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/providers"
)

func (m *model) streamCmd(content string) tea.Cmd {
	// Guard against empty content or unintended/stray submissions
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	content = agents.InjectObjectiveContext(content, m.sess.ObjectiveState)
	if m.streamCh != nil {
		m.push(roleSystem, "Stream blocked: task active.")
		return nil
	}
	if m.provider == nil {
		m.push(roleSystem, "Stream blocked: no provider.")
		return nil
	}

	m.streamCh = make(chan tea.Msg, 1024)
	m.streaming = true
	m.spinnerFrame = 0
	m.responseBuffer.Reset()
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

	// ABSOLUTE GUARD: content MUST be raw input text, NOT m.Viewport.View() or any
	// concatenation of rendered history + status bar + prompt prefix.
	msgs = append(msgs, ai.Message{Role: "user", Content: content})

	uname := m.cfg.Username
	if uname == "" {
		uname = m.userName
	}
	systemPrompt := prompt.ForModeWithUser(m.resolver.Current().String(), uname)

	// Inject identity context directly into the messages array so it lands
	// near the user's current turn in the model's context window. This is
	// critical for smaller models (e.g. Qwen 2.5 7B) that poorly attend to
	// the system prompt but follow instructions embedded in the chat flow.
	if identityLine := prompt.IdentityStatement(uname); identityLine != "" {
		identityMsg := ai.Message{Role: "system", Content: identityLine}
		// Insert right before the current user message
		beforeUser := msgs[:len(msgs)-1]
		rest := msgs[len(msgs)-1:]
		msgs = append(append(beforeUser, identityMsg), rest...)
	}

	req := ai.Request{
		Model:    m.cfg.ActiveModelName(),
		Messages: msgs,
		Stream:   true,
		System:   systemPrompt,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel

	// Capture the channel reference locally so the goroutine never reads
	// m.streamCh after Update() clears it to nil. Without this, the
	// deferred close(m.streamCh) would panic with "close of nil channel".
	streamCh := m.streamCh

	go func() {
		defer close(streamCh)
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			streamCh <- streamErrMsg{err: err}
			return
		}
		defer func() { _ = rawStream.Close() }()

		var full strings.Builder
		tokIn, tokOut := 0, 0
		buf := make([]byte, 4096)

		for {
			n, err := rawStream.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				full.WriteString(chunk)
				streamCh <- tokenMsg(chunk)
			}
			if err == io.EOF {
				if sr, ok := rawStream.(*providers.StreamResult); ok {
					tokIn, tokOut = sr.Usage()
				}
				if tokIn == 0 && tokOut == 0 {
					tokIn = len(content) / 4
					tokOut = full.Len() / 4
				}
				msg := streamDoneMsg{
					content:     full.String(),
					tokenInput:  tokIn,
					tokenOutput: tokOut,
				}
				streamCh <- msg
				return
			}
			if err != nil {
				streamCh <- streamErrMsg{err: err}
				return
			}
		}
	}()

	return tea.Batch(m.readStream())
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
