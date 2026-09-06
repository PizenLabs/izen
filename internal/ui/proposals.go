package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// streamShellCmd launches a bash process and streams its stdout/stderr to the
// event loop as live shellChunkMsg values, followed by a terminal shellExitMsg.
// It is the real-time counterpart of execShellCmd: the running command shows an
// animated snowflake spinner, and its output is inspectable via Ctrl+O while it
// is still producing. The caller must also dispatch shimmerTickCmd and
// smoothStreamTickCmd so the spinner animates for the whole duration.
func (m *model) streamShellCmd(cmd string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	// The cancel is stored on the model (main goroutine — safe) so Ctrl+C can
	// abort the running process; it is cleared by the shellExitMsg handler.
	m.shellCancel = cancel

	// Seed the running exec entry NOW (main goroutine) so the tree shows the
	// animated snowflake for the whole duration — even for a command that
	// produces no output and exits before the first streamed chunk arrives.
	if m.activityTree != nil {
		m.activityTree.AppendOrUpdateExec(cmd, -1, 0, "")
	}

	shellCh := make(chan tea.Msg, 512)
	m.shellCh = shellCh
	m.shellRunning = true

	go func() {
		// ── WORKER LIFETIME (Phase 3) ────────────────────────────────
		// The streaming shell pump is a real worker; register it against the
		// active operation so terminal-lifecycle tests can prove it releases
		// before operation finalization. A no-op when no operation is attached.
		m.spawnOpWorker("shell")
		defer m.releaseOpWorker("shell")
		defer cancel()
		defer close(shellCh)

		start := time.Now()
		c := exec.CommandContext(ctx, "bash", "-c", cmd)
		stdout, err := c.StdoutPipe()
		if err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}
		stderr, err := c.StderrPipe()
		if err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}
		if err := c.Start(); err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}

		// ── CANCELLATION-SAFE PIPE DRAIN ─────────────────────────────
		// Killing the direct child does not necessarily release its pipes: a
		// grandchild that inherited the write ends (e.g. `bash -c "sleep 30"`
		// keeps `sleep` alive) holds them open, which would block the pump
		// goroutines below forever — leaking the worker even though the
		// operation was cancelled. Closing the read ends on ctx.Done makes the
		// pumps return immediately so the terminal shellExitMsg is ALWAYS
		// emitted after a cancellation.
		stopPipes := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = stdout.Close()
				_ = stderr.Close()
			case <-stopPipes:
			}
		}()
		defer close(stopPipes)

		// Drain both pipes concurrently so a chatty stream never deadlocks
		// the process (pipes block writes once their kernel buffers fill).
		var wg sync.WaitGroup

		// emit delivers a streamed chunk to the event loop. Delivery is
		// RELIABLE — a previous non-blocking `select { default: }` silently
		// dropped output whenever the channel was momentarily contended, which
		// produced "shell exited 0 but no output streamed" failures under CI
		// load. The consumer (readShellCh, always dispatched by the event loop)
		// continuously drains the buffered channel, so a blocking send cannot
		// deadlock; the context-cancellation branch unblocks the pump if the
		// shell is aborted mid-stream.
		emit := func(text string) bool {
			select {
			case shellCh <- shellChunkMsg{text: text}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		pump := func(r io.Reader) {
			defer wg.Done()
			br := bufio.NewReaderSize(r, 4096)
			var line strings.Builder
			for {
				chunk := make([]byte, 1024)
				n, readErr := br.Read(chunk)
				if n > 0 {
					line.Write(chunk[:n])
					// Emit whole lines as soon as a newline arrives so the
					// viewport updates incrementally, not in one burst.
					raw := line.String()
					for {
						idx := strings.IndexByte(raw, '\n')
						if idx < 0 {
							break
						}
						if !emit(raw[:idx+1]) {
							return
						}
						raw = raw[idx+1:]
					}
					line.Reset()
					line.WriteString(raw)
				}
				if readErr != nil {
					if line.Len() > 0 {
						emit(line.String())
					}
					return
				}
			}
		}
		wg.Add(2)
		go pump(stdout)
		go pump(stderr)

		runErr := c.Wait()
		wg.Wait()

		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		shellCh <- shellExitMsg{cmd: cmd, exitCode: exitCode, elapsed: time.Since(start), err: runErr}
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
