package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/policy"
)

// permissionRiskBorder maps a risk level onto the modal border color:
// LOW -> subtle cyan/blue, MEDIUM -> yellow/amber, HIGH -> red.
func permissionRiskBorder(level policy.PermissionRiskLevel) lipgloss.Color {
	switch level {
	case policy.RiskHigh:
		return lipgloss.Color(colorRed)
	case policy.RiskMedium:
		return lipgloss.Color(colorYellow)
	default:
		return lipgloss.Color(colorCyan)
	}
}

// permissionRiskTitle maps a risk level onto the modal header.
func permissionRiskTitle(req policy.PermissionRequest) string {
	if req.RiskLevel == policy.RiskHigh {
		return "[SECURITY INTERCEPT] HIGH RISK"
	}
	return fmt.Sprintf("[%s RISK]", string(req.RiskLevel))
}

// renderPermissionModal renders the centered floating permission dialog for
// the request. Width is the terminal width; editing switches the command box
// into inline-edit mode showing editValue instead of req.Command.
func renderPermissionModal(req policy.PermissionRequest, width int, editing bool, editValue string) string {
	if width < 50 {
		width = 50
	}
	boxWidth := width - 10
	if boxWidth > 72 {
		boxWidth = 72
	}
	if boxWidth < 40 {
		boxWidth = 40
	}
	border := permissionRiskBorder(req.RiskLevel)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(border)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorText))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	codeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Background(lipgloss.Color(colorSurface)).Padding(0, 1)
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMauve))

	var b strings.Builder
	b.WriteString(titleStyle.Render(permissionRiskTitle(req)))
	b.WriteString("\n")
	b.WriteString(headerStyle.Render(fmt.Sprintf("Tool Execution Request: %s", req.ToolName)))
	b.WriteString("\n\n")

	// Command / path detail code box.
	detail := req.Command
	if editing {
		detail = editValue + "█"
	}
	if detail == "" && len(req.TargetPaths) > 0 {
		detail = strings.Join(req.TargetPaths, "\n")
	}
	if detail == "" {
		detail = "(no command or target captured)"
	}
	// Clamp detail to the box width (cell-accurate, no wrapping library).
	maxDetailW := boxWidth - 6
	var detailLines []string
	for _, ln := range strings.Split(detail, "\n") {
		for len(ln) > 0 && lipgloss.Width(ln) > maxDetailW {
			// Hard-cut on bytes is safe here: command strings are re-rendered
			// verbatim on resolve, only the display line is clamped.
			cut := maxDetailW
			if cut > len(ln) {
				cut = len(ln)
			}
			detailLines = append(detailLines, ln[:cut])
			ln = ln[cut:]
		}
		detailLines = append(detailLines, ln)
	}
	if len(detailLines) > 6 {
		detailLines = append(detailLines[:6], fmt.Sprintf("… (%d more lines)", len(strings.Split(detail, "\n"))-6))
	}
	b.WriteString(labelStyle.Render("Command / Targets:"))
	b.WriteString("\n")
	for _, ln := range detailLines {
		b.WriteString("  " + codeStyle.Render(ln))
		b.WriteString("\n")
	}
	if len(req.TargetPaths) > 0 && req.Command != "" {
		b.WriteString(labelStyle.Render("Paths:"))
		b.WriteString(" " + strings.Join(req.TargetPaths, ", "))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Context / risk reason.
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Policy requires explicit authorization before this tool call executes."
	}
	if editing {
		reason = "EDIT MODE — modify the command, Enter to submit, Esc to cancel edit."
	}
	b.WriteString(labelStyle.Render("Why:"))
	b.WriteString(" " + reason)
	b.WriteString("\n\n")

	// Action footer: two lines so the box never wraps mid-keybinding.
	sep := strings.Repeat("─", boxWidth-6)
	b.WriteString(" " + sep + "\n")
	footer := fmt.Sprintf("%s  %s\n%s  %s",
		keyStyle.Render("[y] Allow Once"),
		keyStyle.Render("[a] Always Allow (Session)"),
		keyStyle.Render("[n] Deny"),
		keyStyle.Render("[e] Edit Command"),
	)
	b.WriteString(footer)

	return lipgloss.NewStyle().
		Width(boxWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(1, 2).
		Render(b.String())
}

// renderPermissionOverlay dims the workspace and centers the permission modal,
// so the blocked tool call reads as the focus while the session stays faintly
// visible behind it.
func (m *model) renderPermissionOverlay(base string) string {
	if m.pendingPermission == nil {
		return base
	}
	width := m.width
	if width < 40 {
		width = 40
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	modal := renderPermissionModal(*m.pendingPermission, width, m.permissionEditing, m.permissionEditValue)
	modalLines := strings.Split(strings.TrimRight(modal, "\n"), "\n")
	modalW := 0
	for _, ln := range modalLines {
		if w := lipgloss.Width(ln); w > modalW {
			modalW = w
		}
	}
	modalH := len(modalLines)

	plain := ansi.Strip(base)
	dimmed := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(colorDimmed)).Render(plain)
	baseLines := strings.Split(dimmed, "\n")
	if len(baseLines) > height {
		baseLines = baseLines[:height]
	}
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}
	for i := range baseLines {
		if w := lipgloss.Width(baseLines[i]); w < width {
			baseLines[i] += strings.Repeat(" ", width-w)
		}
	}
	top := (height - modalH) / 2
	if top < 0 {
		top = 0
	}
	left := (width - modalW) / 2
	if left < 0 {
		left = 0
	}
	for i := 0; i < modalH && top+i < len(baseLines); i++ {
		ml := modalLines[i]
		tail := width - left - lipgloss.Width(ml)
		if tail < 0 {
			tail = 0
		}
		baseLines[top+i] = strings.Repeat(" ", left) + ml + strings.Repeat(" ", tail)
	}
	return strings.Join(baseLines, "\n")
}

// openPermissionModal stages the request and freezes the approval gate. It is
// the ONLY path that makes a permission modal visible: the request is stored,
// edit mode is cleared, and the canonical workflow approval gate is marked so
// StateAwaitingApproval derives truthfully.
func (m *model) openPermissionModal(msg PermissionPromptMsg) {
	// A stale pending request must never swallow a new RespCh: deny-deliver
	// the orphan (non-blocking) before superseding it.
	if m.pendingPermission != nil && m.permissionRespCh != nil {
		orphan := policy.PermissionResponse{RequestID: m.pendingPermission.ID, Allowed: false}
		select {
		case m.permissionRespCh <- orphan:
		default:
		}
	}
	cp := msg.Req
	m.pendingPermission = &cp
	m.permissionRespCh = msg.RespCh
	m.permissionEditing = false
	m.permissionEditValue = ""
	m.enterApprovalState()
	m.refreshViewportContent()
	m.followTail()
}

// deliverPermissionResponse sends the response on the held channel
// (non-blocking — the agent may have timed out) and clears modal state. The
// "a" (Remember) grant is recorded on the session whitelist here, so every
// later identical tool call passes without prompting.
func (m *model) deliverPermissionResponse(resp policy.PermissionResponse) tea.Cmd {
	if m.pendingPermission == nil {
		return nil
	}
	if resp.RequestID == "" {
		resp.RequestID = m.pendingPermission.ID
	}
	if resp.Allowed && resp.Remember {
		m.permissionAllowlistAdd(*m.pendingPermission)
	}
	if ch := m.permissionRespCh; ch != nil {
		select {
		case ch <- resp:
		default:
		}
	}
	tool := m.pendingPermission.ToolName
	allowed := resp.Allowed
	remember := resp.Remember
	m.pendingPermission = nil
	m.permissionRespCh = nil
	m.permissionEditing = false
	m.permissionEditValue = ""
	m.resolveApprovalState()
	m.recalcViewportHeight()
	m.ti.Focus()
	if allowed {
		if remember {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Allowed (always for session) — "+tool))
		} else {
			m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Allowed once — "+tool))
		}
	} else {
		m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Denied — "+tool+". No files were modified."))
	}
	m.refreshViewportContent()
	m.followTail()
	return func() tea.Msg { return PermissionResolvedMsg{Resp: resp} }
}

// permissionAllowlistAdd records the session grant for the request pattern.
// The whitelist lives on the model so headless/test harnesses without one
// still resolve correctly (nil-safe via policy.SessionWhitelist methods).
func (m *model) permissionAllowlistAdd(req policy.PermissionRequest) {
	if m.permissionWhitelist == nil {
		m.permissionWhitelist = policy.NewSessionWhitelist()
	}
	m.permissionWhitelist.AddRequest(req)
}

// isPermissionAllowedBySession reports whether a session "[a]" grant already
// covers the request, letting repeat tool calls pass without prompting.
func (m *model) isPermissionAllowedBySession(req policy.PermissionRequest) bool {
	if m.permissionWhitelist == nil {
		return false
	}
	return m.permissionWhitelist.HasRequest(req)
}

// denyPendingPermission delivers a denial for the outstanding request, if any.
// It is the leak-proof seam shared by the emergency interrupt and shutdown
// paths: a blocked agent goroutine is always released, never orphaned.
func (m *model) denyPendingPermission(reason string) {
	if m.pendingPermission == nil {
		return
	}
	tool := m.pendingPermission.ToolName
	resp := policy.PermissionResponse{RequestID: m.pendingPermission.ID, Allowed: false}
	if ch := m.permissionRespCh; ch != nil {
		select {
		case ch <- resp:
		default:
		}
	}
	m.pendingPermission = nil
	m.permissionRespCh = nil
	m.permissionEditing = false
	m.permissionEditValue = ""
	m.resolveApprovalState()
	if reason != "" {
		m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Denied ("+reason+") — "+tool+"."))
	}
}

// handlePermissionModalKey routes every key while the security modal is open.
//   - y / Enter: Allow Once -> PermissionResolvedMsg{Allowed:true}
//   - a: Always Allow (Session) -> {Allowed:true, Remember:true}
//   - n / Esc: Deny -> {Allowed:false}
//   - e: inline command-editing mode; Enter submits the edit, Esc exits edit
//     mode (second Esc denies).
//
// While editing, printable runes append to the edit buffer, Backspace deletes,
// and Enter submits PermissionResolvedMsg{Allowed:true, Edited:true}.
func (m *model) handlePermissionModalKey(msg tea.KeyMsg) tea.Cmd {
	if m.pendingPermission == nil {
		return nil
	}
	req := *m.pendingPermission

	// ── EDIT MODE ────────────────────────────────────────────────
	if m.permissionEditing {
		switch msg.Type {
		case tea.KeyEnter:
			edited := m.permissionEditValue
			m.permissionEditing = false
			return m.deliverPermissionResponse(policy.PermissionResponse{
				RequestID: req.ID,
				Allowed:   true,
				EditedCmd: edited,
				Edited:    true,
			})
		case tea.KeyEscape:
			// First Esc exits edit mode; the request stays pending so a
			// second Esc denies. This keeps "Esc denies" truthful without
			// destroying an in-progress edit on a stray keypress.
			m.permissionEditing = false
			m.refreshViewportContent()
			return nil
		case tea.KeyBackspace, tea.KeyDelete, tea.KeyCtrlH:
			if len(m.permissionEditValue) > 0 {
				m.permissionEditValue = m.permissionEditValue[:len(m.permissionEditValue)-1]
				m.refreshViewportContent()
			}
			return nil
		case tea.KeyRunes:
			// Printable edit text only — control sequences never enter the
			// command buffer (SGR/mouse-escape guard parity with keys.go).
			if len(msg.Runes) == 0 || msg.Alt {
				return nil
			}
			for _, r := range msg.Runes {
				if r < 32 && r != '\t' {
					return nil
				}
			}
			m.permissionEditValue += string(msg.Runes)
			m.refreshViewportContent()
			return nil
		default:
			return nil
		}
	}

	// ── DECISION MODE ────────────────────────────────────────────
	switch msg.Type {
	case tea.KeyEnter:
		return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: true})
	case tea.KeyEscape:
		return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: false})
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !msg.Alt {
			switch msg.Runes[0] {
			case 'y', 'Y':
				return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: true})
			case 'a', 'A':
				return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: true, Remember: true})
			case 'n', 'N':
				return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: false})
			case 'e', 'E':
				m.permissionEditing = true
				m.permissionEditValue = req.Command
				m.refreshViewportContent()
				return nil
			}
		}
		// Explicit Ctrl+C denial keeps the blocked agent goroutine leak-proof
		// when the modal holds the only release for the executor.
		if msg.Type == tea.KeyCtrlC {
			return m.deliverPermissionResponse(policy.PermissionResponse{RequestID: req.ID, Allowed: false})
		}
		return nil
	}
}
