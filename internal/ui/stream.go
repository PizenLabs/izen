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
		m.push(roleSystem, "[System] Stream blocked: an execution channel is currently active.")
		return nil
	}
	if m.provider == nil {
		m.push(roleSystem, "[System] Stream blocked: no AI provider is configured.")
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
	if history := m.sess.History; len(history) > 0 {
		for _, msg := range history {
			raw := msg.Content
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

	var systemPrompt string
	if m.resolver.Current() == modes.ModeAsk {
		uname := m.cfg.Username
		if uname == "" {
			uname = m.userName
		}
		if uname == "" {
			uname = "developer"
		}
		systemPrompt = prompt.AskSystemPrompt(uname)
	}
	if m.resolver.Current() == modes.ModeBuild {
		systemPrompt = prompt.BuildSystemPrompt()
	}
	if m.resolver.Current() == modes.ModePlan {
		systemPrompt = prompt.PlanSystemPrompt()
	}

	if systemPrompt == "" {
		uname := m.cfg.Username
		if uname == "" {
			uname = m.userName
		}
		if uname == "" {
			uname = "developer"
		}
		systemPrompt = strings.ReplaceAll(prompt.AskSystemPromptTemplate, "{{.Username}}", uname)
	}

	req := ai.Request{
		Model:    m.cfg.ActiveModelName(),
		Messages: msgs,
		Stream:   true,
		System:   systemPrompt,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel

	go func() {
		defer close(m.streamCh)
		defer func() { m.streamCancel = nil }()
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			m.streamCh <- streamErrMsg{err: err}
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
				m.streamCh <- tokenMsg(chunk)
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
				m.streamCh <- msg
				return
			}
			if err != nil {
				m.streamCh <- streamErrMsg{err: err}
				return
			}
		}
	}()

	return tea.Batch(m.readStream(), m.spinnerTickCmd())
}

func (m *model) readStream() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.streamCh
		if !ok {
			return nil
		}
		return msg
	}
}
