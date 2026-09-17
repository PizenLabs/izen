// Package components — spinner.go implements the thread-safe decoupled TUI
// ticker for Bounded Execution (Phase 6.2).
//
// The spinner animates on an independent tea.Tick(100ms) ticker (10Hz) and
// reads execution state ONLY through an immutable
// atomic.Pointer[ExecutionStateSnapshot]. Event Bus congestion or
// synchronous disk I/O MUST NOT freeze frame rendering: Tick/View never
// block on channels, mutexes, or I/O.
package components

import (
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// SpinnerTickInterval is the decoupled frame interval: 100ms (10Hz).
const SpinnerTickInterval = 100 * time.Millisecond

// SpinnerTickMsg is the independent ticker event. It carries only the tick
// timestamp — frame state is read from the atomic snapshot, never from the
// message bus payload.
type SpinnerTickMsg time.Time

// SpinnerTickCmd returns the 10Hz ticker command. Each tick must be re-armed
// by the host Update loop (standard Bubble Tea tick discipline).
func SpinnerTickCmd() tea.Cmd {
	return tea.Tick(SpinnerTickInterval, func(t time.Time) tea.Msg {
		return SpinnerTickMsg(t)
	})
}

// ExecutionPhase is the coarse backend phase projected onto the spinner.
type ExecutionPhase string

const (
	PhaseIdle      ExecutionPhase = "idle"
	PhaseExecuting ExecutionPhase = "executing"
	PhaseWaiting   ExecutionPhase = "waiting"
	PhaseDone      ExecutionPhase = "done"
)

// ExecutionStateSnapshot is the IMMUTABLE execution-state projection the
// spinner reads. Writers publish a wholly-new snapshot via Store; the
// spinner only Loads — no shared-mutable aliasing, no locks on the read
// path, hence race-free under -race.
type ExecutionStateSnapshot struct {
	Phase   ExecutionPhase
	StepID  string
	Message string
	Frame   uint64
	Done    bool
}

// Spinner is the decoupled TUI spinner. The animation frame counter is owned
// by the TUI event loop (Tick/View); backend progress arrives only as atomic
// snapshot Stores from any goroutine (Event Bus subscribers, I/O workers).
type Spinner struct {
	state *atomic.Pointer[ExecutionStateSnapshot]
	frame uint64
}

// NewSpinner builds a spinner bound to state. A nil pointer allocates a
// fresh one holding an idle snapshot so View never nil-dereferences.
func NewSpinner(state *atomic.Pointer[ExecutionStateSnapshot]) *Spinner {
	if state == nil {
		state = &atomic.Pointer[ExecutionStateSnapshot]{}
	}
	if state.Load() == nil {
		state.Store(&ExecutionStateSnapshot{Phase: PhaseIdle})
	}
	return &Spinner{state: state}
}

// Publish stores a wholly-new immutable snapshot. It never mutates the
// previously stored snapshot in place (callers must not mutate after Store).
func (s *Spinner) Publish(snap *ExecutionStateSnapshot) {
	if s == nil || s.state == nil || snap == nil {
		return
	}
	s.state.Store(snap)
}

// Snapshot Loads the latest immutable snapshot (nil-safe).
func (s *Spinner) Snapshot() *ExecutionStateSnapshot {
	if s == nil || s.state == nil {
		return &ExecutionStateSnapshot{Phase: PhaseIdle}
	}
	snap := s.state.Load()
	if snap == nil {
		return &ExecutionStateSnapshot{Phase: PhaseIdle}
	}
	return snap
}

// Update advances the animation frame on every SpinnerTickMsg and returns
// the re-armed tick command. Non-tick messages are ignored (nil cmd) so
// backend event bursts can never stall the 10Hz cadence.
func (s *Spinner) Update(msg tea.Msg) tea.Cmd {
	if s == nil {
		return nil
	}
	if _, ok := msg.(SpinnerTickMsg); !ok {
		return nil
	}
	s.frame++
	return SpinnerTickCmd()
}

// Frame returns the TUI-owned animation frame counter.
func (s *Spinner) Frame() uint64 {
	if s == nil {
		return 0
	}
	return s.frame
}

// frames is the Braille spinner glyph set.
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// View renders the current frame glyph plus the latest snapshot label. It
// performs zero I/O, takes no locks, and never blocks: a frozen backend
// still yields a fresh glyph per tick.
func (s *Spinner) View() string {
	if s == nil {
		return frames[0]
	}
	glyph := frames[s.frame%uint64(len(frames))]
	snap := s.Snapshot()
	if snap.Message != "" {
		return glyph + " " + snap.Message
	}
	if snap.StepID != "" {
		return glyph + " " + string(snap.Phase) + " " + snap.StepID
	}
	return glyph + " " + string(snap.Phase)
}
