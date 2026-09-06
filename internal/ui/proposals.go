package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/infrastructure/capabilities"
)

func (m *model) createBuildCheckpoint(fileCount int) {
	if m.execEng == nil {
		return
	}
	cp, err := m.execEng.Checkpoints.Create(fmt.Sprintf("izen build: %d file(s)", fileCount))
	if err != nil {
		m.push(roleSystem, infoStyle.Render("checkpoint: "+err.Error()))
	} else if cp != nil {
		shortHash := cp.Hash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		m.push(roleSystem, infoStyle.Render(
			fmt.Sprintf("checkpoint: %s (%d files)", shortHash, fileCount)))
	}
}

// shellExecRegex matches bash/sh code blocks in AI responses.
var shellExecRegex = regexp.MustCompile("(?s)```(?:bash|sh)\\n(.*?)```")

// extractShellCommands scans a response for bash/sh code blocks and returns
// the command strings for explicit human-in-the-loop confirmation.
func extractShellCommands(response string) []string {
	matches := shellExecRegex.FindAllStringSubmatch(response, -1)
	var cmds []string
	for _, m := range matches {
		cmd := strings.TrimSpace(m[1])
		if cmd == "" {
			continue
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

// sanitizeShellCmd guards the TUI input bar against auto-loading commands
// that are dangerously long or contain diff formatting (e.g., unified diff
// paste). Returns (cleaned, rejected, reason).
var diffHeaderRegex = regexp.MustCompile(`(?m)^(?:---\s+\S+|\+\+\+\s+\S+|@@\s+-\d+(?:,\d+)?\s+\+\d+(?:,\d+)?\s*@@)`)

func sanitizeShellCmd(cmd string) (string, bool, string) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return cmd, true, "empty command"
	}

	const maxLen = 500
	if len(trimmed) > maxLen {
		return cmd, true, fmt.Sprintf(
			"command exceeds %d character limit (%d chars)", maxLen, len(trimmed))
	}

	if diffHeaderRegex.MatchString(trimmed) {
		return cmd, true, "command contains unified diff headers (---/+++/@@)"
	}

	return cmd, false, ""
}

// streamShellCmd launches a shell command via the control-plane shell port
// and streams its output as shellChunkMsg values. No direct os/exec in the
// presentation layer – delegated to the substrate shell port.
func (m *model) streamShellCmd(cmd string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.shellCancel = cancel
	if m.activityTree != nil {
		m.activityTree.AppendOrUpdateExec(cmd, -1, 0, "")
	}
	shellCh := make(chan tea.Msg, 512)
	m.shellCh = shellCh
	m.shellRunning = true
	go func() {
		m.spawnOpWorker("shell")
		defer m.releaseOpWorker("shell")
		defer cancel()
		defer close(shellCh)
		start := time.Now()
		shell := capabilities.NewExecShell(0)
		res, err := shell.Execute(ctx, cmd)
		if res.Stdout != "" {
			shellCh <- shellChunkMsg{text: res.Stdout}
		}
		if res.Stderr != "" {
			shellCh <- shellChunkMsg{text: res.Stderr}
		}
		exitCode := res.ExitCode
		if err != nil && exitCode == 0 {
			exitCode = -1
		}
		shellCh <- shellExitMsg{cmd: cmd, exitCode: exitCode, elapsed: time.Since(start), err: err}
	}()
	return m.readShellCh()
}

// readShellCh reads one message from the streaming shell channel and returns
// it to the event loop. It returns nil (no-op) when the channel has been torn
// down, mirroring the readStream pattern so the loop never blocks forever.
func (m *model) readShellCh() tea.Cmd {
	return func() tea.Msg {
		if m.shellCh == nil {
			return nil
		}
		msg, ok := <-m.shellCh
		if !ok {
			return nil
		}
		return msg
	}
}
