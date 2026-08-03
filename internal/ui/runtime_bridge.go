package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
)

// runRuntimeCmd executes a RuntimeCommand through the presentation Bridge on a
// background goroutine and reports the outcome as a runtimeResultMsg. It is
// nil-safe: without a wired runtime it returns a no-op so the rich UI path is
// unaffected in harnesses. The model is NEVER mutated here — every side effect
// re-enters the Bubble Tea event loop as a presentationEventMsg, so there are
// zero races between command execution and rendering.
func (m *model) runRuntimeCmd(cmd appruntime.RuntimeCommand) tea.Cmd {
	if m.pres == nil || cmd == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return runtimeResultMsg{typ: cmd.Type(), err: m.pres.Execute(ctx, cmd)}
	}
}

// runtimeSubmitCmd translates a submitted prompt into a SubmitPromptCmd routed
// through the Application layer. It is the presentation input-to-command
// translation seam mandated by the RFC; the rich engine path still runs
// alongside for the full interactive experience.
func (m *model) runtimeSubmitCmd(line string) tea.Cmd {
	mode := ""
	if m.resolver != nil {
		mode = m.resolver.Current().String()
	}
	return m.runRuntimeCmd(appruntime.SubmitPromptCmd{Prompt: line, Mode: mode})
}

// runtimeSwitchCmd translates a user mode switch into a SwitchModeCmd.
func (m *model) runtimeSwitchCmd(mode modes.Mode) tea.Cmd {
	return m.runRuntimeCmd(appruntime.SwitchModeCmd{Mode: mode.String()})
}

// runtimeApproveCmd translates an approval confirmation into an
// ApprovePatchCmd.
func (m *model) runtimeApproveCmd(patchID string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.ApprovePatchCmd{PatchID: patchID})
}

// runtimeRejectCmd translates an approval rejection into a RejectPatchCmd.
func (m *model) runtimeRejectCmd(patchID, reason string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.RejectPatchCmd{PatchID: patchID, Reason: reason})
}

// runtimeCancelCmd translates a user cancellation into a CancelCmd.
func (m *model) runtimeCancelCmd(reason string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.CancelCmd{Reason: reason})
}
