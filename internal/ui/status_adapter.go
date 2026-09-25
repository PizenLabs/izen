package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
	statuscommand "github.com/PizenLabs/izen/internal/ui/commands"
	statuswidget "github.com/PizenLabs/izen/internal/ui/widgets"
	"github.com/PizenLabs/izen/internal/workspace"
)

// statusCommandInput takes a cheap, UI-thread snapshot of the runtime-owned
// facets. The expensive/bounded part (Git) is intentionally left to the
// commands.StatusCmd worker.
func (m *model) statusCommandInput() statuscommand.Input {
	if m == nil {
		return statuscommand.Input{}
	}

	input := statuscommand.Input{Root: m.workspaceRoot}

	// Use the already-materialized file graph's O(1) counters. This keeps the
	// status snapshot itself on the fast UI path; the background Lea/Lynx
	// indexer owns the expensive graph walk.
	indexer := workspace.IndexerStatus{Status: strings.ToLower(strings.TrimSpace(m.indexingStatus))}
	switch indexer.Status {
	case "indexed":
		indexer.Indexed = true
	case "indexing":
		indexer.ASTStatus = "pending"
	case "error":
		indexer.ASTStatus = "error"
	}
	if m.graph != nil {
		indexer.SymbolCount = m.graph.SymCount
		if m.graph.FileCount > 0 && indexer.Status == "" {
			indexer.Status = "indexed"
			indexer.Indexed = true
		}
	} else if m.leaEng != nil {
		// Headless/test compositions may not materialize the file projection.
		// Lea.Debug is the engine's explicit read-only diagnostic seam; it is
		// used only when that projection is absent, so the normal TUI path
		// remains O(1).
		debug := m.leaEng.Debug()
		indexer.SymbolCount = debug.Symbols
		if debug.Indexed() && indexer.Status == "" {
			indexer.Status = "indexed"
			indexer.Indexed = true
		}
	}
	input.Indexer = indexer

	if m.sess != nil {
		input.Session.Title = m.sess.EffectiveTitle()
		input.Session.Goal = m.sess.ObjectiveIntent()
		input.Session.TurnCount = session.UserTurns(m.sess.History)
		if input.Session.TurnCount == 0 && m.sess.Revision > 0 {
			input.Session.TurnCount = int(m.sess.Revision)
		}
	}
	// Token counters are UI-owned even in lightweight headless harnesses where
	// no durable Session object has been attached yet.
	statusInput := m.sessionDisplayInput()
	statusOutput := m.sessionDisplayOutput()
	statusTotal := m.TotalTokens
	if sum := statusInput + statusOutput; sum > statusTotal {
		statusTotal = sum
	}
	input.Session.TokenUsage = workspace.TokenUsage{
		InputTokens:  statusInput,
		OutputTokens: statusOutput,
		TotalTokens:  statusTotal,
	}
	if m.sessionManager != nil {
		if slot := m.sessionManager.Active(); slot != "" {
			input.Session.SlotID = slot.String()
		}
	}
	if input.Session.Tokens == (workspace.TokenUsage{}) {
		input.Session.Tokens = input.Session.TokenUsage
	}
	input.Session.TokenUsage = input.Session.Tokens
	if input.Session.Tokens.TotalTokens == 0 {
		input.Session.Tokens.TotalTokens = input.Session.Tokens.InputTokens + input.Session.Tokens.OutputTokens
	}
	input.Session.TokenUsage = input.Session.Tokens

	provider, model := m.statusProviderModel()
	mode := modes.ModeAsk
	if m.resolver != nil {
		mode = m.resolver.Current()
	} else if m.sess != nil {
		mode = m.sess.Mode
	}
	gate := statuscommand.PolicyGateForMode(mode.String())
	// A pending human decision is useful security context, but it remains an
	// observation and does not change the mode or grant any capability.
	if m.awaitingConfirmation || m.pendingTestConfirm || m.pendingBuildApproval || m.pendingHotfixTask != nil || m.pendingPermission != nil {
		gate = "Awaiting Approval"
	}
	input.Engine = workspace.EngineStatus{
		Provider:   provider,
		Model:      model,
		PolicyGate: gate,
		Mode:       mode.String(),
	}
	return input
}

func (m *model) statusProviderModel() (provider, model string) {
	if m == nil {
		return "", ""
	}
	if m.modelAuthority != nil {
		binding := m.modelAuthority.ActiveBinding()
		provider = string(binding.ProviderID)
		model = string(binding.ModelID)
	}
	if provider == "" && m.provider != nil {
		provider = m.provider.Name()
	}
	if provider == "" && m.cfg != nil {
		provider = m.cfg.ActiveProviderName()
	}
	if model == "" && m.sess != nil {
		model = m.sess.Model
	}
	if model == "" && m.cfg != nil {
		model = m.cfg.ActiveModelName()
	}
	return strings.TrimSpace(provider), strings.TrimSpace(model)
}

// toggleStatusModal opens or closes the standalone status popup. Collection
// remains asynchronous; the modal itself is rendered from the last snapshot
// while the bounded probe is in flight.
func (m *model) toggleStatusModal() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.showStatus {
		m.closeStatus()
		return nil
	}

	m.showStatus = true
	m.statusRequest++
	m.statusCommandBuffer = ""
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	modalWidth, modalHeight := StatusModalSize(width, height)
	m.statusView = m.statusView.SetSize(modalWidth, modalHeight).
		SetSnapshot(workspace.Status{}).
		SetLoading(true)
	m.ti.Blur()
	m.dismissSuggestions()

	input := m.statusCommandInput()
	input.Generation = m.statusRequest
	return statuscommand.StatusCmd(input)
}

// runStatusCmd is retained as the command-handler seam used by commands.go.
func (m *model) runStatusCmd() tea.Cmd {
	return m.toggleStatusModal()
}

func (m *model) closeStatus() {
	if m == nil {
		return
	}
	m.showStatus = false
	m.statusRequest++ // invalidate an in-flight collector result
	m.statusCommandBuffer = ""
	m.statusView = m.statusView.SetLoading(false)
	m.ti.Focus()
	m.dismissSuggestions()
}

func (m *model) handleStatusResult(msg statuscommand.ResultMsg) {
	if m == nil || !m.showStatus {
		return
	}
	if msg.Generation != 0 && msg.Generation != m.statusRequest {
		return
	}
	if msg.Err != nil {
		m.statusView = m.statusView.SetSnapshot(msg.Status).SetError(msg.Err)
		return
	}
	m.statusView = m.statusView.SetSnapshot(msg.Status)
}

// handleStatusKey keeps the modal isolated from the workspace while still
// allowing the exact slash command that toggled it to be re-entered without
// sending any of the intermediate characters to the background prompt.
func (m *model) handleStatusKey(msg tea.KeyMsg) {
	if msg.Type == tea.KeyEsc || strings.EqualFold(msg.String(), "q") {
		m.closeStatus()
		return
	}

	switch msg.Type {
	case tea.KeyRunes:
		if !isPrintableRunes(msg) || len(msg.Runes) == 0 {
			return
		}
		if msg.Runes[0] == 'q' {
			m.closeStatus()
			return
		}
		m.statusCommandBuffer += string(msg.Runes)
		// Only the canonical command can be actionable; discard other input
		// instead of retaining an unbounded modal-local buffer.
		if len([]rune(m.statusCommandBuffer)) > len([]rune(statuscommand.Name)) {
			m.statusCommandBuffer = ""
		}
	case tea.KeyBackspace:
		runes := []rune(m.statusCommandBuffer)
		if len(runes) > 0 {
			m.statusCommandBuffer = string(runes[:len(runes)-1])
		}
	case tea.KeyEnter:
		if strings.EqualFold(strings.TrimSpace(m.statusCommandBuffer), statuscommand.Name) {
			m.closeStatus()
			return
		}
		m.statusCommandBuffer = ""
	}
}

func (m *model) resizeStatusView(width, height int) {
	if m == nil {
		return
	}
	updated, _ := m.statusView.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if view, ok := updated.(statuswidget.StatusView); ok {
		m.statusView = view
	}
}

func isStatusCommandInput(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 1 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	if strings.EqualFold(fields[0], statuscommand.Name) {
		return true
	}
	return resolveCommandToken(fields[0]) == statuscommand.Name
}
