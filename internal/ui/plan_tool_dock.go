package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	uitool "github.com/PizenLabs/izen/internal/ui/tool"
)

// toolAutoCollapseDelay is the delay after completion before a terminal tool
// card collapses to its single-line summary header.
const toolAutoCollapseDelay = 1500 * time.Millisecond

// ── Plan card handlers ─────────────────────────────────────────────────

// handlePlanUpdate replaces the current execution plan state.
func (m *model) handlePlanUpdate(msg PlanUpdateMsg) {
	cp := msg.Plan
	m.execPlan = &cp
	if m.Ready {
		m.refreshViewportContent()
	}
}

// handlePlanStep updates a single step in place.
func (m *model) handlePlanStep(msg PlanStepMsg) {
	if m.execPlan == nil {
		return
	}
	var elapsed time.Duration = -1
	if msg.Elapsed >= 0 {
		elapsed = time.Duration(msg.Elapsed)
	}
	m.execPlan.UpdateStep(msg.ID, msg.Status, elapsed, msg.Error)
	if m.Ready {
		m.refreshViewportContent()
	}
}

// renderPlanDock renders the execution plan card docked immediately below
// the prompt header. Empty when no plan is active.
func (m *model) renderPlanDock(width int) string {
	if m.execPlan == nil {
		return ""
	}
	return m.execPlan.Render(m.spinnerFrame, width)
}

// ── Tool card handlers ─────────────────────────────────────────────────

func (m *model) ensureToolCards() {
	if m.toolCards == nil {
		m.toolCards = make(map[string]*uitool.ToolCard)
	}
}

// handleToolStart spawns a new active Tool Output Card.
func (m *model) handleToolStart(msg ToolStartMsg) {
	m.ensureToolCards()
	id := strings.TrimSpace(msg.ID)
	if id == "" {
		id = fmt.Sprintf("tool-%d", len(m.toolOrder)+1)
	}
	name := msg.ToolName
	if name == "" {
		name = "shell"
	}
	card := uitool.New(id, name, msg.Command)
	m.toolCards[id] = card
	m.toolOrder = append(m.toolOrder, id)
	if m.Ready {
		m.refreshViewportContent()
	}
}

// handleToolChunk appends stdout/stderr stream data and auto-scrolls.
func (m *model) handleToolChunk(msg ToolChunkMsg) {
	card := m.toolCards[msg.ID]
	if card == nil {
		return
	}
	card.Append(msg.Chunk)
	m.lastAgentActivity = time.Now()
	if m.Ready {
		m.refreshViewportContent()
	}
	if !m.userIsScrollingUp {
		m.gotoBottomIfAllowed()
	}
}

// handleToolEnd marks process completion, records history continuity, and
// schedules auto-collapse after toolAutoCollapseDelay.
func (m *model) handleToolEnd(msg ToolEndMsg) tea.Cmd {
	card := m.toolCards[msg.ID]
	if card == nil {
		return nil
	}
	var dur time.Duration
	if msg.ElapsedNs > 0 {
		dur = time.Duration(msg.ElapsedNs)
	} else {
		dur = card.Elapsed()
	}
	errMsg := ""
	if msg.Err != nil {
		errMsg = msg.Err.Error()
	}
	card.Finish(msg.ExitCode, errMsg, dur)
	// History continuity: completed cards leave a summary record in the
	// chat stream thread so viewport history survives collapse.
	if !m.activitySurfaceSealed {
		m.push(roleSystem, card.Summary())
	}
	if m.Ready {
		m.refreshViewportContent()
	}
	id := msg.ID
	return tea.Tick(toolAutoCollapseDelay, func(time.Time) tea.Msg {
		return toolCollapseMsg{ID: id}
	})
}

// handleToolCollapse collapses a terminal card to its summary header.
func (m *model) handleToolCollapse(msg toolCollapseMsg) {
	card := m.toolCards[msg.ID]
	if card == nil || !card.ShouldAutoCollapse() {
		return
	}
	card.Collapse()
	if m.Ready {
		m.refreshViewportContent()
	}
}

// toggleToolCard toggles expansion of a historical output log. Empty ID
// targets the most recent card.
func (m *model) toggleToolCard(id string) {
	if id == "" && len(m.toolOrder) > 0 {
		id = m.toolOrder[len(m.toolOrder)-1]
	}
	if card := m.toolCards[id]; card != nil {
		card.Toggle()
		if m.Ready {
			m.refreshViewportContent()
		}
	}
}

// renderToolDock renders tool cards inline in viewport order. Active cards
// render open; completed cards render collapsed summaries (until toggled).
func (m *model) renderToolDock(width int) string {
	if len(m.toolOrder) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.toolOrder))
	for _, id := range m.toolOrder {
		card := m.toolCards[id]
		if card == nil {
			continue
		}
		parts = append(parts, card.Render(m.spinnerFrame, width, 12))
	}
	return strings.Join(parts, "\n")
}
