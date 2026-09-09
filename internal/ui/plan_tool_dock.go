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

func (m *model) ensureBatchCards() {
	if m.batchCards == nil {
		m.batchCards = make(map[string]*uitool.BatchCard)
	}
}

// handleToolBatchStarted spawns a grouped BatchCard. Each child owns its own
// ring buffer so interleaved tool_chunk streams never mix.
func (m *model) handleToolBatchStarted(msg ToolBatchStartedMsg) {
	m.ensureBatchCards()
	if _, exists := m.batchCards[msg.BatchID]; exists {
		return
	}
	batch := uitool.NewBatch(msg.BatchID, msg.Tools)
	m.batchCards[msg.BatchID] = batch
	m.batchOrder = append(m.batchOrder, msg.BatchID)
	if m.Ready {
		m.refreshViewportContent()
	}
}

// handleToolBatchChunk routes a chunk to the isolated buffer of the target
// tool inside the batch.
func (m *model) handleToolBatchChunk(msg ToolBatchChunkMsg) {
	batch := m.batchCards[msg.BatchID]
	if batch == nil {
		// Fallback: try legacy single-card routing for compatibility.
		if card := m.toolCards[msg.ToolID]; card != nil {
			card.Append(msg.Chunk)
		}
		return
	}
	for _, t := range batch.Tools {
		if t.ID == msg.ToolID {
			t.Append(msg.Chunk)
			break
		}
	}
	m.lastAgentActivity = time.Now()
	if m.Ready {
		m.refreshViewportContent()
	}
	if !m.userIsScrollingUp {
		m.gotoBottomIfAllowed()
	}
}

// handleToolBatchCompleted marks each child tool as done/failed.
func (m *model) handleToolBatchCompleted(msg ToolBatchCompletedMsg) {
	batch := m.batchCards[msg.BatchID]
	if batch == nil {
		return
	}
	for _, r := range msg.Results {
		for _, t := range batch.Tools {
			if t.ID == r.ID {
				errMsg := ""
				if r.Err != nil {
					errMsg = r.Err.Error()
				}
				t.Finish(r.ExitCode, errMsg, t.Elapsed())
				if errMsg != "" || r.ExitCode != 0 {
					// keep history for failures
					if !m.activitySurfaceSealed {
						m.push(roleSystem, t.Summary())
					}
				}
				break
			}
		}
	}
	if m.Ready {
		m.refreshViewportContent()
	}
}

// handleToolBatchSelect navigates Up/Down inside a grouped batch.
func (m *model) handleToolBatchSelect(msg ToolBatchSelectMsg) {
	batch := m.batchCards[msg.BatchID]
	if batch == nil {
		// No explicit batch: target the most recent batch.
		if len(m.batchOrder) > 0 {
			batch = m.batchCards[m.batchOrder[len(m.batchOrder)-1]]
		}
	}
	if batch != nil {
		batch.Select(msg.Delta)
		if m.Ready {
			m.refreshViewportContent()
		}
	}
}

// handleToolBatchToggle expands/collapses the selected tool overlay (Ctrl+O / Enter).
func (m *model) handleToolBatchToggle(msg ToolBatchToggleMsg) {
	batch := m.batchCards[msg.BatchID]
	if batch == nil && len(m.batchOrder) > 0 {
		batch = m.batchCards[m.batchOrder[len(m.batchOrder)-1]]
	}
	if batch != nil {
		batch.ToggleSelected()
		if m.Ready {
			m.refreshViewportContent()
		}
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
// If the ID belongs to a grouped batch child, it routes to the isolated
// ring buffer of that child so streams never interleave.
func (m *model) handleToolChunk(msg ToolChunkMsg) {
	if card := m.toolCards[msg.ID]; card != nil {
		card.Append(msg.Chunk)
		m.lastAgentActivity = time.Now()
		if m.Ready {
			m.refreshViewportContent()
		}
		if !m.userIsScrollingUp {
			m.gotoBottomIfAllowed()
		}
		return
	}
	for _, batch := range m.batchCards {
		for _, t := range batch.Tools {
			if t.ID == msg.ID {
				t.Append(msg.Chunk)
				m.lastAgentActivity = time.Now()
				if m.Ready {
					m.refreshViewportContent()
				}
				if !m.userIsScrollingUp {
					m.gotoBottomIfAllowed()
				}
				return
			}
		}
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
	parts := make([]string, 0, len(m.toolOrder)+len(m.batchOrder))
	for _, id := range m.batchOrder {
		batch := m.batchCards[id]
		if batch == nil {
			continue
		}
		parts = append(parts, batch.Render(m.spinnerFrame, width, 12))
	}
	for _, id := range m.toolOrder {
		card := m.toolCards[id]
		if card == nil {
			continue
		}
		parts = append(parts, card.Render(m.spinnerFrame, width, 12))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}
