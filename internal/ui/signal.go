package ui

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Root OS-signal cancellation bridge.
//
// In a TTY Bubble Tea runs in raw mode, so Ctrl+C is delivered as a key message
// (tea.KeyCtrlC) and never reaches the OS signal path. When input is NOT a TTY,
// or when the process receives SIGINT/SIGTERM from the OS (kill, terminal
// teardown, session manager), Bubble Tea's own handler forwards one
// InterruptMsg and then stops listening. This bridge keeps a persistent root
// handler so that:
//
//   - the first SIGINT/SIGTERM is forwarded into the event loop as an
//     interruptSignalMsg, which the model treats exactly like Ctrl+C
//     (graceful cancellation of the active operation), and
//   - a second signal while a cancellation is in progress forces a hard exit
//     with status 130 — the application must never require tmux kill-pane.
//
// The terminal is restored (best effort) before the hard exit so the shell
// does not inherit raw mode.

// interruptSignalMsg is delivered by the root signal bridge for a SIGINT or
// SIGTERM that reached the OS-level handler (non-TTY input or external kill).
type interruptSignalMsg struct{ signal os.Signal }

// installRootSignalBridge wires the persistent SIGINT/SIGTERM handler onto the
// running program. It returns a stop function that detaches the handler.
func installRootSignalBridge(p *tea.Program) func() {
	if p == nil {
		return func() {}
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		count := 0
		last := time.Time{}
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				now := time.Now()
				// A signal far outside the cancellation grace window is a NEW
				// cancellation request, not the second press of an in-progress
				// one — reset the force-exit counter.
				if now.Sub(last) > cancelGraceWindow {
					count = 0
				}
				last = now
				count++
				if count >= 2 {
					// Second signal while a cancellation may still be in
					// progress: force termination with status 130.
					_ = p.RestoreTerminal()
					os.Exit(130)
				}
				p.Send(interruptSignalMsg{signal: sig})
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// hardExitFn is the process-exit hook for the force-exit path. Tests override
// it to capture the status instead of killing the test binary.
var hardExitFn = os.Exit

// cancelGraceWindow is how long a graceful cancellation stays armed so a second
// Ctrl+C within the window is treated as "cancellation still in progress" and
// forces a hard exit with status 130.
const cancelGraceWindow = 5 * time.Second
