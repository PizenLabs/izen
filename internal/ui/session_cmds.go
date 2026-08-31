package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/session/compaction"
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

// runSessionCmd implements the `/session` control surface (SESSION.md §10):
//
//	/session                 list sessions
//	/session list            list sessions
//	/session resume <A|B>    switch to a dormant/archived session
//	/session inspect <A|B>   render structured metadata of a session
//	/session rename <A|B>    atomically retitle a session
//	/session archive <A|B>   transition a session to ARCHIVED
//	/session delete <A|B>    purge session-owned state (INV-SESSION-12)
//	/session compact <A|B>   manually trigger the Generational Compactor
func (m *model) runSessionCmd(cmd string) tea.Cmd {
	if m.sessionManager == nil {
		m.push(roleError, "/session unavailable: no session manager wired")
		return nil
	}

	parts := strings.Fields(cmd)
	switch {
	case len(parts) == 1:
		return m.toggleSessionPicker()
	case len(parts) == 2 && parts[1] == "list":
		return m.runSessionListCmd()

	case len(parts) == 3 && parts[1] == "resume":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionResumeCmd(target)

	case len(parts) == 3 && parts[1] == "inspect":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionInspectCmd(target)

	case len(parts) >= 3 && parts[1] == "rename":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionRenameCmd(target, strings.Join(parts[3:], " "))

	case len(parts) == 3 && parts[1] == "archive":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionArchiveCmd(target)

	case len(parts) == 3 && parts[1] == "delete":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionDeleteCmd(target)

	case len(parts) == 3 && parts[1] == "compact":
		target, ok := parseSlotArg(parts[2])
		if !ok {
			return m.sessionUsageError(parts[1], parts[2])
		}
		return m.runSessionCompactCmd(target)

	default:
		m.push(roleError, "usage:\n"+
			"  /session                  list sessions\n"+
			"  /session resume <A|B>     switch to a session\n"+
			"  /session inspect <A|B>    show session metadata\n"+
			"  /session rename <A|B> <t> retitle a session\n"+
			"  /session archive <A|B>    archive a session\n"+
			"  /session delete <A|B>     purge session-owned state\n"+
			"  /session compact <A|B>    run the Generational Compactor")
		return nil
	}
}

func (m *model) sessionUsageError(sub, arg string) tea.Cmd {
	m.push(roleError, fmt.Sprintf("/session %s: invalid session %q — expected A or B", sub, arg))
	return nil
}

func parseSlotArg(s string) (session.SlotID, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "A":
		return session.SlotA, true
	case "B":
		return session.SlotB, true
	}
	return "", false
}

// runSessionListCmd renders both slots with their lifecycle, title, objective,
// dirty-file guard state and recovery flags.
func (m *model) runSessionListCmd() tea.Cmd {
	var b strings.Builder
	for _, info := range m.sessionManager.List(context.Background()) {
		state := "dormant"
		if info.Active {
			state = "ACTIVE"
		}
		if info.Lifecycle == "archived" {
			state = "ARCHIVED"
		}
		label := fmt.Sprintf("  [%s] slot %s  %s", state, info.Slot, info.SessionID)
		if info.Objective != "" {
			label += "  " + info.Objective
		}
		if info.DirtyCount > 0 {
			label += fmt.Sprintf("  (⚠ %d uncommitted file(s))", info.DirtyCount)
		}
		if info.Error != "" {
			label += "  (" + info.Error + ")"
		}
		b.WriteString(label + "\n")
	}
	m.push(roleSystem, infoStyle.Render("sessions:\n"+strings.TrimRight(b.String(), "\n")))
	return nil
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
	msg := fmt.Sprintf("/session resume: now active on slot %s · %s", target, sess.SessionID)
	if len(sess.WorkspaceDirtyFiles) > 0 {
		msg += fmt.Sprintf(" · ⚠ uncommitted changes carried over: %d file(s)", len(sess.WorkspaceDirtyFiles))
	}
	m.push(roleSystem, infoStyle.Render(msg))
	return nil
}

// runSessionInspectCmd renders structured metadata of the target session: its
// identity, goal, decisions, artifact references and compact state (SESSION.md
// §10 `/session inspect`).
func (m *model) runSessionInspectCmd(target session.SlotID) tea.Cmd {
	sess, err := m.sessionManager.Inspect(target)
	if err != nil {
		m.push(roleError, fmt.Sprintf("/session inspect %s failed: %v", target, err))
		return nil
	}
	cc, _ := m.sessionManager.CompactContext(target)

	type compactView struct {
		Generation int    `json:"generation"`
		EventCount int    `json:"event_count"`
		Summary    string `json:"summary,omitempty"`
	}
	type inspectView struct {
		Slot       session.SlotID `json:"slot"`
		SessionID  string         `json:"session_id"`
		Title      string         `json:"title"`
		Goal       string         `json:"goal,omitempty"`
		Lifecycle  string         `json:"lifecycle,omitempty"`
		Mode       string         `json:"mode,omitempty"`
		CreatedAt  string         `json:"created_at,omitempty"`
		UpdatedAt  string         `json:"updated_at,omitempty"`
		TurnCount  int            `json:"turn_count"`
		Decisions  []string       `json:"decisions,omitempty"`
		Artifacts  []string       `json:"artifacts,omitempty"`
		DirtyFiles []string       `json:"uncommitted_files,omitempty"`
		Compact    *compactView   `json:"compact,omitempty"`
	}
	iv := inspectView{
		Slot:       target,
		SessionID:  sess.SessionID,
		Title:      sess.EffectiveTitle(),
		Goal:       sess.ObjectiveIntent(),
		Lifecycle:  string(sess.Lifecycle),
		Mode:       sess.Mode.String(),
		TurnCount:  len(sess.History),
		Artifacts:  append([]string(nil), sess.Checkpoints...),
		DirtyFiles: append([]string(nil), sess.WorkspaceDirtyFiles...),
	}
	if !sess.CreatedAt.IsZero() {
		iv.CreatedAt = sess.CreatedAt.Format("2006-01-02 15:04:05")
	}
	if !sess.UpdatedAt.IsZero() {
		iv.UpdatedAt = sess.UpdatedAt.Format("2006-01-02 15:04:05")
	}
	if cc != nil {
		iv.Decisions = append([]string(nil), cc.Decisions...)
		if len(iv.Artifacts) == 0 {
			iv.Artifacts = append([]string(nil), cc.Artifacts...)
		}
		iv.Compact = &compactView{Generation: cc.Generation, EventCount: cc.EventCount, Summary: cc.Summary}
	}

	data, err := json.MarshalIndent(iv, "", "  ")
	if err != nil {
		m.push(roleError, fmt.Sprintf("/session inspect %s failed: %v", target, err))
		return nil
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("session %s:\n%s", target, string(data))))
	return nil
}

// runSessionRenameCmd atomically retitles the target session in its
// session.json (SESSION.md §7: title is mutable, ID is not).
func (m *model) runSessionRenameCmd(target session.SlotID, title string) tea.Cmd {
	if err := m.sessionManager.Rename(context.Background(), target, title); err != nil {
		m.push(roleError, fmt.Sprintf("/session rename %s failed: %v", target, err))
		return nil
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session rename: slot %s now titled %q", target, title)))
	return nil
}

// runSessionArchiveCmd transitions the target session's lifecycle to ARCHIVED.
// An archived session remains inspectable and resumable unless explicitly
// deleted (SESSION.md §25).
func (m *model) runSessionArchiveCmd(target session.SlotID) tea.Cmd {
	if err := m.sessionManager.Archive(context.Background(), target); err != nil {
		m.push(roleError, fmt.Sprintf("/session archive %s failed: %v", target, err))
		return nil
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session archive: slot %s archived (still resumable via /session resume %s)", target, target)))
	return nil
}

// runSessionDeleteCmd explicitly purges the session-owned state of the slot
// (INV-SESSION-12). Project configuration, project knowledge and the global
// audit log are NEVER touched. Deleting the active slot atomically moves the
// pointer to the sibling before the removal, so the committed pointer never
// dangles.
func (m *model) runSessionDeleteCmd(target session.SlotID) tea.Cmd {
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		m.push(roleError, "/session delete cannot run while an execution is in flight. Wait for it to finish or /drop it first.")
		return nil
	}
	if err := m.sessionManager.Delete(context.Background(), target); err != nil {
		m.push(roleError, fmt.Sprintf("/session delete %s failed: %v", target, err))
		return nil
	}
	// Deleting the active slot switches the live session to the sibling;
	// re-mirror the canonical record so the UI never holds a purged session.
	m.sess = m.sessionManager.Session()
	m.resolver.Set(m.sess.Mode)
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session delete: slot %s purged · project state, knowledge and audit evidence preserved", target)))
	return nil
}

// runSessionCompactCmd manually triggers the Generational Compactor over the
// target session's raw history and sinks the produced generation through the
// SessionManager's atomic seam. Compaction is derived state — raw history is
// never touched (SESSION.md §14).
func (m *model) runSessionCompactCmd(target session.SlotID) tea.Cmd {
	if m.compactionRunner == nil {
		m.push(roleError, "/session compact unavailable: no compaction runner wired")
		return nil
	}
	sess, err := m.sessionManager.Inspect(target)
	if err != nil {
		m.push(roleError, fmt.Sprintf("/session compact %s failed: %v", target, err))
		return nil
	}
	base, _ := m.sessionManager.CompactContext(target)
	lastCP := ""
	if len(sess.Checkpoints) > 0 {
		lastCP = sess.Checkpoints[len(sess.Checkpoints)-1]
	}
	job := compaction.Job{
		Slot:       target,
		SessionID:  sess.SessionID,
		Objective:  sess.ObjectiveIntent(),
		Mode:       sess.Mode.String(),
		Checkpoint: lastCP,
		RunNumber:  sess.RunNumber,
		CreatedAt:  sess.CreatedAt,
		History:    append([]session.Message(nil), sess.History...),
		Base:       base,
	}
	cc, err := m.compactionRunner.Compact(context.Background(), job)
	if err != nil {
		m.push(roleError, fmt.Sprintf("/session compact %s failed: %v", target, err))
		return nil
	}
	if err := m.sessionManager.SetCompactContext(context.Background(), target, cc); err != nil {
		m.push(roleError, fmt.Sprintf("/session compact %s: sink failed: %v", target, err))
		return nil
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session compact %s: generation %d sealed · %d events folded", target, cc.Generation, cc.EventCount)))
	return nil
}

// ── Session Picker modal integration ─────────────────────────────────────────

// toggleSessionPicker opens the interactive picker when closed and closes it
// when open. Bare `/session` in the TUI is the single entry point; every
// parameterized subcommand bypasses this and executes deterministically.
func (m *model) toggleSessionPicker() tea.Cmd {
	if m.showSessionPicker {
		return m.closeSessionPicker()
	}
	return m.openSessionPicker()
}

// openSessionPicker populates the modal from SessionManager.List and shows it.
// It does NOT emit any chat history line — the modal itself is the presentation.
func (m *model) openSessionPicker() tea.Cmd {
	if m.sessionManager == nil {
		m.push(roleError, "/session unavailable: no session manager wired")
		return nil
	}
	infos := m.sessionManager.List(context.Background())
	sp := NewSessionPickerModal(infos)
	if m.width > 0 && m.height > 0 {
		w, h := m.sessionPickerDialogSize()
		sp.SetSize(w, h)
	}
	m.sessionPicker = sp
	m.showSessionPicker = true
	return nil
}

// closeSessionPicker dismisses the modal and returns focus to the prompt.
func (m *model) closeSessionPicker() tea.Cmd {
	m.showSessionPicker = false
	if m.sessionPicker != nil {
		m.sessionPicker = nil
	}
	m.ti.Focus()
	return nil
}

// refreshSessionPicker reloads the modal list after a lifecycle mutation.
func (m *model) refreshSessionPicker() {
	if m.sessionPicker == nil || m.sessionManager == nil {
		return
	}
	infos := m.sessionManager.List(context.Background())
	m.sessionPicker.SetSessions(infos)
}

// handleSessionPickerResume executes the atomic switch for the selected slot
// and closes the modal. It is the `Enter` handler of the picker.
func (m *model) handleSessionPickerResume(target session.SlotID) tea.Cmd {
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus("cannot switch while execution is in flight", true)
		}
		return nil
	}
	sess, err := m.sessionManager.ResumeSession(context.Background(), target)
	if err != nil {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus(fmt.Sprintf("resume %s failed: %v", target, err), true)
		} else {
			m.push(roleError, fmt.Sprintf("/session resume %s failed: %v", target, err))
		}
		return nil
	}
	m.sess = sess
	m.resolver.Set(sess.Mode)
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	_ = m.closeSessionPicker()
	msg := fmt.Sprintf("/session resume: now active on slot %s · %s", target, sess.SessionID)
	if len(sess.WorkspaceDirtyFiles) > 0 {
		msg += fmt.Sprintf(" · ⚠ %d uncommitted file(s)", len(sess.WorkspaceDirtyFiles))
	}
	m.push(roleSystem, infoStyle.Render(msg))
	return nil
}

// handleSessionPickerNew creates a fresh session and refreshes the modal.
func (m *model) handleSessionPickerNew() tea.Cmd {
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus("cannot start session while execution is in flight", true)
		}
		return nil
	}
	sess, err := m.sessionManager.NewSession(context.Background())
	if err != nil {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus(fmt.Sprintf("/new failed: %v", err), true)
		} else {
			m.push(roleError, fmt.Sprintf("/new failed: %v", err))
		}
		return nil
	}
	m.sess = sess
	m.resolver.Set(sess.Mode)
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	m.push(roleSystem, infoStyle.Render("/new: started a fresh session · previous session preserved, resumable via /session resume A|B"))
	m.refreshSessionPicker()
	if m.sessionPicker != nil {
		m.sessionPicker.SetStatus(fmt.Sprintf("new session on slot %s", m.sessionManager.Active()), false)
	}
	return nil
}

// handleSessionPickerRename retitles the selected session and refreshes.
func (m *model) handleSessionPickerRename(target session.SlotID, title string) tea.Cmd {
	if err := m.sessionManager.Rename(context.Background(), target, title); err != nil {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus(fmt.Sprintf("rename %s failed: %v", target, err), true)
		} else {
			m.push(roleError, fmt.Sprintf("/session rename %s failed: %v", target, err))
		}
		return nil
	}
	m.refreshSessionPicker()
	if m.sessionPicker != nil {
		m.sessionPicker.SetStatus(fmt.Sprintf("slot %s now titled %q", target, title), false)
	} else {
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session rename: slot %s now titled %q", target, title)))
	}
	return nil
}

// handleSessionPickerArchive toggles archive status via Archive.
func (m *model) handleSessionPickerArchive(target session.SlotID) tea.Cmd {
	if err := m.sessionManager.Archive(context.Background(), target); err != nil {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus(fmt.Sprintf("archive %s failed: %v", target, err), true)
		} else {
			m.push(roleError, fmt.Sprintf("/session archive %s failed: %v", target, err))
		}
		return nil
	}
	m.refreshSessionPicker()
	if m.sessionPicker != nil {
		m.sessionPicker.SetStatus(fmt.Sprintf("slot %s archived", target), false)
	} else {
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session archive: slot %s archived", target)))
	}
	return nil
}

// handleSessionPickerDelete deletes the selected slot and re-mirrors.
func (m *model) handleSessionPickerDelete(target session.SlotID) tea.Cmd {
	if m.state == StateProcessing || m.state == StateAwaitingApproval || m.streaming || m.agentRunning {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus("cannot delete while execution is in flight", true)
		}
		return nil
	}
	if err := m.sessionManager.Delete(context.Background(), target); err != nil {
		if m.sessionPicker != nil {
			m.sessionPicker.SetStatus(fmt.Sprintf("delete %s failed: %v", target, err), true)
		} else {
			m.push(roleError, fmt.Sprintf("/session delete %s failed: %v", target, err))
		}
		return nil
	}
	m.sess = m.sessionManager.Session()
	if m.sess != nil {
		m.resolver.Set(m.sess.Mode)
	}
	m.resetTransientInteraction()
	m.unsealActivitySurface()
	m.refreshSessionPicker()
	if m.sessionPicker != nil {
		m.sessionPicker.SetStatus(fmt.Sprintf("slot %s purged", target), false)
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("/session delete: slot %s purged · project state, knowledge and audit evidence preserved", target)))
	return nil
}

// handleSessionPickerCompact triggers compaction via the existing compact flow.
func (m *model) handleSessionPickerCompact(target session.SlotID) tea.Cmd {
	cmd := m.runSessionCompactCmd(target)
	m.refreshSessionPicker()
	if m.sessionPicker != nil && cmd == nil {
		// runSessionCompactCmd already pushed status; surface a transient modal status as well.
		// If no error was pushed, show success; errors already have a status via pushed records.
		// Check last error text to avoid double success on failure.
		_ = cmd
		m.sessionPicker.SetStatus(fmt.Sprintf("compact %s requested", target), false)
	}
	return cmd
}
