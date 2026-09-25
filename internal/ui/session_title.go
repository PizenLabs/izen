package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/session"
)

// titleRefineTimeout bounds the background Commit/Fast summarization pass so a
// hung provider can never leak a goroutine indefinitely.
const titleRefineTimeout = 20 * time.Second

// titleRefinerFunc is the injectable one-shot title summarizer. The model
// parameter is the resolved Commit/Fast role model id. A nil implementation
// falls back to the wired ai.Provider.
type titleRefinerFunc func(ctx context.Context, model, prompt string) (string, error)

// sessionTitleRefinedMsg is the asynchronous result of the Stage 2 title
// refinement. It carries the originating identity so a late completion from a
// superseded session can never retitle the active one.
type sessionTitleRefinedMsg struct {
	sessionID string
	slot      session.SlotID
	title     string
	err       error
}

// TitleRefinePrompt builds the deterministic Stage 2 instruction. It is
// exported for tests and for hosts that want to reuse the exact wording.
func TitleRefinePrompt(prompt string) string {
	return "Summarize task intent in 3-5 words: " + prompt
}

// applyFirstTurnTitle is Stage 1 of the two-stage titling pipeline. When the
// first accepted user turn lands in a session that has no explicit title, it
// derives a deterministic title from the prompt and arms the Stage 2
// refinement. The caller is responsible for persisting the session record
// afterwards (the normal turn commit does this).
func (m *model) applyFirstTurnTitle(prompt string) {
	if m == nil || m.sess == nil {
		return
	}
	if strings.TrimSpace(m.sess.Title) != "" {
		return
	}
	// Only the first accepted human turn seeds the heuristic title.
	if session.UserTurns(m.sess.History) > 1 {
		return
	}
	title := session.SanitizeTitle(prompt)
	if title == "" {
		return
	}
	m.sess.Title = title
	m.titleHeuristic = title
	m.titlePrompt = prompt
	m.titleRefineArmed = true
	if m.showSessionPicker {
		m.refreshSessionPicker()
	}
}

// refineTitleCmd is Stage 2: it returns a non-blocking tea.Cmd that runs the
// Commit/Fast summarization off the UI goroutine and emits the refined title.
func (m *model) refineTitleCmd() tea.Cmd {
	if m == nil || m.sess == nil {
		return nil
	}
	if m.provider == nil && m.titleRefiner == nil {
		return nil
	}
	prompt := strings.TrimSpace(m.titlePrompt)
	if prompt == "" {
		return nil
	}
	sessionID := m.sess.SessionID
	slot := session.SlotID("")
	if m.sessionManager != nil {
		slot = m.sessionManager.Active()
	}
	modelID := m.routeModel("commit")
	provider := m.provider
	refiner := m.titleRefiner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), titleRefineTimeout)
		defer cancel()
		var raw string
		var err error
		if refiner != nil {
			raw, err = refiner(ctx, modelID, prompt)
		} else {
			resp, execErr := provider.Execute(ctx, ai.Request{
				Model: modelID,
				Messages: []ai.Message{
					{Role: "user", Content: TitleRefinePrompt(prompt)},
				},
				Stream:        false,
				ContextPolicy: "none",
			})
			if execErr != nil {
				err = execErr
			} else if resp != nil {
				raw = resp.Content
			}
		}
		if err != nil {
			return sessionTitleRefinedMsg{sessionID: sessionID, slot: slot, err: err}
		}
		return sessionTitleRefinedMsg{
			sessionID: sessionID,
			slot:      slot,
			title:     cleanRefinedTitle(raw),
		}
	}
}

// handleSessionTitleRefined applies the Stage 2 result on the UI goroutine. It
// is a no-op when the result is stale (the active session changed) or when the
// user has since renamed the session manually.
func (m *model) handleSessionTitleRefined(msg sessionTitleRefinedMsg) {
	if m == nil || m.sess == nil || msg.err != nil || msg.title == "" {
		return
	}
	if msg.sessionID == "" || msg.sessionID != m.sess.SessionID {
		return
	}
	// Only replace the Stage 1 heuristic; never clobber an explicit rename.
	if m.titleHeuristic == "" || m.sess.Title != m.titleHeuristic {
		return
	}
	m.sess.Title = msg.title
	m.titleHeuristic = msg.title
	m.persistSession("session-title")
	if m.showSessionPicker {
		m.refreshSessionPicker()
	}
}

// cleanRefinedTitle normalizes a model-produced title into a single-line,
// layout-safe label. It strips list markers, quotes, and code fences, then
// applies the same deterministic truncation as Stage 1.
func cleanRefinedTitle(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return ""
	}
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	line = strings.Trim(line, "\"'` ")
	line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	line = strings.Trim(line, "\"'` ")
	return session.SanitizeTitle(line)
}

// resetTitlePipeline clears the transient Stage 2 state on a session boundary
// (new / resume) so a refinement armed for the previous session can never be
// dispatched against the newly active one.
func (m *model) resetTitlePipeline() {
	if m == nil {
		return
	}
	m.titleRefineArmed = false
	m.titlePrompt = ""
	m.titleHeuristic = ""
}
