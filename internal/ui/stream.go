package ui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/providers"
)

func (m *model) streamCmd(content string) tea.Cmd {
	if m.streamCh != nil {
		m.push(roleSystem, "already streaming…")
		return nil
	}
	if m.provider == nil {
		m.push(roleError, "no AI provider configured")
		return nil
	}

	m.streamCh = make(chan tea.Msg, 1024)
	m.streaming = true
	m.spinnerFrame = 0

	msgs := []ai.Message{{Role: "user", Content: content}}

	if m.resolver.Current() == modes.ModeBuild {
		sys := prompt.BuildSystemPrompt()
		msgs = append([]ai.Message{{Role: "system", Content: sys}}, msgs...)
	}

	req := ai.Request{
		Model:    m.cfg.ActiveModelName(),
		Messages: msgs,
		Stream:   true,
	}

	go func() {
		defer close(m.streamCh)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rawStream, err := m.provider.ExecuteStream(ctx, req)
		if err != nil {
			m.streamCh <- streamErrMsg{err: err}
			return
		}
		defer rawStream.Close()

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
				m.streamCh <- streamDoneMsg{content: full.String(), tokenInput: tokIn, tokenOutput: tokOut}
				return
			}
			if err != nil {
				m.streamCh <- streamErrMsg{err: err}
				return
			}
		}
	}()

	return tea.Batch(m.readStream(), tickCmd())
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
