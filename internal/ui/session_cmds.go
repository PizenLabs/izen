package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/session"
)

// runNewSessionCmd implements the `/new` session boundary: it persists the
// current session to its (now dormant) slot, creates a fresh session, and
// atomically switches the active pointer through the SessionManager. Execution
// state drains through the RuntimeExecutor boundary hook — the UI never touches
// a second execution engine.
func (m *model) runNewSessionCmd() tea.Cmd {
	if m.sessionManager == nil {
		m.push(roleError, "/new unavailable: no session manager wired")
		return nil
	}
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		m.push(roleError, "/new cannot start a session boundary while an execution is in flight. Wait for it to finish or /drop it first.")
		return nil
	}

	sess, err := m.sessionManager.NewSession(context.Background())
	if err != nil {
		m.push(roleError, fmt.Sprintf("/new failed: %v", err))
		return nil
	}

	m.sess = sess
	m.resolver.Set(sess.Mode)
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	m.push(roleSystem, infoStyle.Render("/new: started a fresh session · previous session preserved, resumable via /session resume A|B"))
	return nil
}

// runSessionCmd implements `/session` (list) and `/session resume <A|B>`.
// Resuming switches the active pointer to the target slot after persisting the
// current one; the target slot must hold a recoverable session.
func (m *model) runSessionCmd(cmd string) tea.Cmd {
	if m.sessionManager == nil {
		m.push(roleError, "/session unavailable: no session manager wired")
		return nil
	}

	parts := strings.Fields(cmd)
	switch {
	case len(parts) == 1:
		// /session — list both slots.
		var b strings.Builder
		for _, info := range m.sessionManager.List(context.Background()) {
			state := "dormant"
			if info.Active {
				state = "ACTIVE"
			}
			label := fmt.Sprintf("  [%s] slot %s  %s", state, info.Slot, info.SessionID)
			if info.Objective != "" {
				label += "  " + info.Objective
			}
			if info.Error != "" {
				label += "  (" + info.Error + ")"
			}
			b.WriteString(label + "\n")
		}
		m.push(roleSystem, infoStyle.Render("sessions:\n"+strings.TrimRight(b.String(), "\n")))
		return nil

	case len(parts) == 3 && parts[1] == "resume":
		target := session.SlotID(strings.ToUpper(parts[2]))
		return m.runSessionResumeCmd(target)

	default:
		m.push(roleError, "usage: /session            list sessions\n       /session resume A|B  resume a dormant session")
		return nil
	}
}

// runSessionResumeCmd performs the resume handshake: validate target -> persist
// active -> prepare target -> atomically commit pointer.
func (m *model) runSessionResumeCmd(target session.SlotID) tea.Cmd {
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		m.push(roleError, "/session resume cannot switch sessions while an execution is in flight. Wait for it to finish or /drop it first.")
		return nil
	}

	sess, err := m.sessionManager.ResumeSession(context.Background(), target)
	if err != nil {
		m.push(roleError, fmt.Sprintf("/session resume %s failed: %v", target, err))
		return nil
	}

	m.sess = sess
	m.resolver.Set(sess.Mode)
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session resume: now active on slot %s · %s", target, sess.SessionID)))
	return nil
}
