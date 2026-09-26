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
	"fmt"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerTickInterval is the decoupled frame interval: 100ms (10Hz).
//
// 100ms is the animation rate, not the render rate: a braille glyph cycle has
// 10 frames, so 10Hz gives a full 1.0s rotation. Faster cadences (the 30/60 FPS
// UI frame loop) re-render the SAME glyph 3–6 times per animation step, which is
// what removes the visible stutter: the glyph advances smoothly at 10Hz while
// the frame that draws it is redrawn on a steady, independent cadence.
const SpinnerTickInterval = 100 * time.Millisecond

// Dots is the canonical smooth braille dot set, sourced from bubbles' spinner
// definitions so the glyph cycle can never drift from the framework's. It is
// referenced (not copied) so a framework upgrade propagates here.
//
// The 10-glyph MiniDot set is the smoothest of the braille cycles: each frame
// advances exactly one dot, so the perceived motion is a continuous orbit with
// no direction reversal (the Jump and Dot sets visibly stutter on the turn).
var Dots = spinner.MiniDot

// DotsFrames is the resolved glyph cycle. Copied once at init so a caller
// mutating Dots.Frames can never corrupt the running animation.
var DotsFrames = append([]string(nil), Dots.Frames...)

// ── Emerald colour ramp ───────────────────────────────────────────────────────
//
// A static colour makes a spinner read as a stuck indicator: the glyph changes
// but the eye latches onto the fixed hue and the motion blurs into a flicker.
// Cycling the foreground along a narrow green→emerald ramp restores perceived
// motion at 10Hz — the eye tracks the brightening dot instead of the shape —
// while staying inside one hue family so the accent never reads as a state
// change (no red/amber leaks into a healthy "working" indicator).
var (
	// emeraldRamp walks dim sage → mint → emerald and back, one step per
	// animation frame. Adjacent steps are close enough that the transition
	// reads as a pulse, not a colour change.
	emeraldRamp = []lipgloss.Color{
		"#7fd6a2", "#8bdcac", "#97e2b6", "#a3e8c0",
		"#a6e3a1", "#9de09f", "#93dd9d", "#89da9b",
		"#7fd6a2", "#8bdcac", "#97e2b6", "#a3e8c0",
	}
	// emeraldStyle is the ramp pre-rendered. lipgloss styles are immutable and
	// safe for concurrent use, so the render path allocates nothing per frame.
	emeraldStyle = func() []lipgloss.Style {
		out := make([]lipgloss.Style, len(emeraldRamp))
		for i, c := range emeraldRamp {
			out[i] = lipgloss.NewStyle().Foreground(c)
		}
		return out
	}()

	// emeraldSGR is the same ramp as raw 24-bit SGR prefixes.
	//
	// lipgloss renders a style to PLAIN TEXT when the active terminal colour
	// profile is Ascii — which is exactly the case for a pipe, a test harness,
	// and TERM=dumb. Without a raw fallback the spinner would silently lose its
	// ramp in every non-TTY capture (and in every screenshot) and degrade to a
	// colourless flicker. The fallback fires only when lipgloss actually
	// dropped the colour, so a real terminal keeps its profile-resolved
	// (possibly downgraded) output.
	emeraldSGR = func() []string {
		out := make([]string, len(emeraldRamp))
		for i, c := range emeraldRamp {
			out[i] = sgrPrefixForHex(string(c))
		}
		return out
	}()
)

// sgrPrefixForHex converts a "#rrggbb" literal into a 24-bit foreground SGR
// prefix (no reset — the caller appends the glyph then the reset).
func sgrPrefixForHex(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	parse := func(i int) int {
		v := 0
		for _, c := range hex[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= int(c - '0')
			case c >= 'a' && c <= 'f':
				v |= int(c-'a') + 10
			}
		}
		return v
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", parse(1), parse(3), parse(5))
}

// SpinnerStyleFor returns the emerald ramp style for animation frame n. The
// index wraps, so it is total and allocation-free.
func SpinnerStyleFor(frame uint64) lipgloss.Style {
	if len(emeraldStyle) == 0 {
		return lipgloss.NewStyle()
	}
	return emeraldStyle[frame%uint64(len(emeraldStyle))]
}

// SpinnerGlyph returns the smooth braille glyph for animation frame n,
// pre-rendered in the emerald ramp so the caller can concatenate it into a
// status line without a second styling pass. It is never unstyled: when the
// active colour profile cannot carry the ramp (pipe, test, TERM=dumb) the raw
// SGR sequence is emitted instead.
func SpinnerGlyph(frame uint64) string {
	if len(DotsFrames) == 0 {
		return ""
	}
	glyph := DotsFrames[frame%uint64(len(DotsFrames))]
	if len(emeraldStyle) == 0 {
		return glyph
	}
	step := frame % uint64(len(emeraldStyle))
	if rendered := emeraldStyle[step].Render(glyph); rendered != glyph {
		return rendered
	}
	if sgr := emeraldSGR[step]; sgr != "" {
		return sgr + glyph + "\x1b[0m"
	}
	return glyph
}

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

// View renders the current frame glyph plus the latest snapshot label. It
// performs zero I/O, takes no locks, and never blocks: a frozen backend
// still yields a fresh glyph per tick.
//
// The glyph comes from the canonical smooth braille dot set and is coloured by
// the emerald ramp, so a 10Hz animation reads as continuous motion rather than
// a flickering static shape.
func (s *Spinner) View() string {
	if s == nil {
		return SpinnerGlyph(0)
	}
	glyph := SpinnerGlyph(s.frame)
	snap := s.Snapshot()
	if snap.Message != "" {
		return glyph + " " + snap.Message
	}
	if snap.StepID != "" {
		return glyph + " " + string(snap.Phase) + " " + snap.StepID
	}
	return glyph + " " + string(snap.Phase)
}
