package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/context/compactor"
	cmdreg "github.com/PizenLabs/izen/internal/domain/command"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui/widgets"
)

// compactMaxTokens resolves the context window size for compaction runs.
func (m *model) compactMaxTokens() int {
	if m.cfg != nil && m.cfg.Models.MaxTokens > 0 {
		return m.cfg.Models.MaxTokens
	}
	if m.cfg != nil && m.cfg.AI.MaxTokens > 0 {
		return m.cfg.AI.MaxTokens
	}
	return compactor.DefaultMaxTokens
}

// compactEngine builds a compactor bound to the runtime threshold and UI bus.
func (m *model) compactEngine() *compactor.Engine {
	ratio, _ := cmdreg.CompactThreshold()
	return compactor.New(
		compactor.WithMaxTokens(m.compactMaxTokens()),
		compactor.WithThresholdRatio(ratio),
		compactor.WithEventBus(m.bus),
	)
}

// compactConversation projects the session history into a compactable
// conversation. Prior summary system nodes are stripped so repeated runs fold
// forward instead of stacking digests.
func (m *model) compactConversation() *compactor.Conversation {
	conv := &compactor.Conversation{}
	if m.sess != nil {
		conv.SystemPrompt = m.sess.ObjectiveIntent()
		for _, h := range m.sess.History {
			if h.Role == "system" && strings.HasPrefix(h.Content, "[Compacted summary]") {
				continue
			}
			conv.Turns = append(conv.Turns, compactor.Turn{
				Role:         h.Role,
				Content:      h.Content,
				IsToolOutput: h.Role == "tool",
			})
		}
		// Restore the folded digest separately so Budget accounts it.
		for _, h := range m.sess.History {
			if h.Role == "system" && strings.HasPrefix(h.Content, "[Compacted summary]") {
				conv.Summary = h.Content
				break
			}
		}
	}
	return conv
}

// applyCompactedHistory writes the compacted window back into the session in
// place, preserving the summary as the leading system node.
func (m *model) applyCompactedHistory(conv *compactor.Conversation) {
	if m.sess == nil || conv == nil {
		return
	}
	kept := make([]struct{ role, content string }, 0, len(conv.Turns)+1)
	if strings.TrimSpace(conv.Summary) != "" {
		kept = append(kept, struct{ role, content string }{"system", conv.Summary})
	}
	for _, t := range conv.Turns {
		kept = append(kept, struct{ role, content string }{t.Role, t.Content})
	}
	m.sess.ClearHistory()
	for _, k := range kept {
		m.sess.History = append(m.sess.History, session.Message{Role: k.role, Content: k.content, Timestamp: time.Now()})
	}
	_ = m.sess.Save()
}

// runCompactCmd implements /compact [now|stats|info|auto <ratio|off>].
func (m *model) runCompactCmd(cmd string) tea.Cmd {
	req, err := cmdreg.ParseCompactCommand(cmd)
	if err != nil {
		m.push(roleError, err.Error())
		m.push(roleSystem, infoStyle.Render("usage: /compact [now|stats|auto <0.10-0.95|off>]"))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	switch req.Action {
	case cmdreg.CompactStats:
		return m.runCompactStatsCmd()
	case cmdreg.CompactAuto:
		if req.ThresholdOff {
			cmdreg.DisableCompactAuto()
			m.push(roleSystem, infoStyle.Render("/compact auto off — threshold-triggered auto compaction disabled"))
		} else {
			if err := cmdreg.SetCompactThreshold(req.Threshold); err != nil {
				m.push(roleError, err.Error())
			} else {
				m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/compact auto threshold → %.2f", req.Threshold)))
			}
		}
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	default:
		return m.runCompactNowCmd()
	}
}

func (m *model) runCompactStatsCmd() tea.Cmd {
	conv := m.compactConversation()
	b := m.compactEngine().Budget(conv)
	view := widgets.BudgetView{
		MaxTokens:          b.MaxTokens,
		CurrentTokens:      b.CurrentTokens,
		SystemPromptTokens: b.SystemPromptTokens,
		RecentTurnTokens:   b.RecentTurnTokens,
		SummaryTokens:      b.SummaryTokens,
		ToolOutputTokens:   b.ToolOutputTokens,
	}
	for _, line := range strings.Split(widgets.RenderCompactionCard(view), "\n") {
		m.push(roleSystem, line)
	}
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

func (m *model) runCompactNowCmd() tea.Cmd {
	conv := m.compactConversation()
	engine := m.compactEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := engine.Compact(ctx, conv, compactor.ForceOptions{Force: true})
	if err != nil {
		m.push(roleError, fmt.Sprintf("/compact failed: %v", err))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	m.applyCompactedHistory(conv)
	m.push(roleSystem, infoStyle.Render(widgets.RenderCompactionResult(widgets.ResultView{
		Strategy:     string(res.StrategyUsed),
		TokensBefore: res.TokensBefore,
		TokensAfter:  res.TokensAfter,
		FreedTokens:  res.FreedTokens,
		Uncompacted:  res.UncompactedTurns,
	})))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}
