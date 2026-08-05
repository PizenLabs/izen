// Package shimmer provides a Bubble Tea component that animates a soft
// highlight wave sweeping left-to-right across a short status line (e.g.
// "Thinking...", "Evaluating policy...", "Executing strategy...") during
// active execution states.
//
// The component owns its own ~50ms tick loop (FrameMsg + Tick). It keeps
// re-scheduling only while Active is set, so the parent can gracefully stop
// the animation — and with it the tick loop — the moment streaming output
// begins or a task completes, with no leaked goroutine.
package shimmer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// TickInterval is the frame cadence of the sweep animation (~50ms).
const TickInterval = 50 * time.Millisecond

// Catppuccin Mocha palette used by the default pulse highlight.
const (
	// ColorBase is the resting colour of the status text (Mocha muted grey).
	ColorBase = "#6c7086"
	// ColorHighlight is the leading edge of the wave (Mocha peach).
	ColorHighlight = "#fab387"
	// ColorPulse is the trailing tone that widens the pulse (Mocha yellow).
	ColorPulse = "#f9e2af"
)

const (
	defaultGlowWidth = 7
	defaultStep      = 1.0
)

// FrameMsg is the animation tick message. It is exported so the parent model
// can forward it into the component and gate the loop on its own lifecycle.
type FrameMsg struct{}

// Tick returns a command that schedules the next animation frame after
// TickInterval. The parent re-dispatches it while the shimmer stays active.
func Tick() tea.Cmd {
	return tea.Tick(TickInterval, func(time.Time) tea.Msg { return FrameMsg{} })
}

// Model is the shimmer animation component. It is a value type designed to be
// embedded in a larger Bubble Tea model.
type Model struct {
	// Text is the status line to animate.
	Text string
	// Frame is the current animation frame, advanced once per tick.
	Frame int
	// Active toggles the animation. When false, View renders the text in the
	// base colour with no sweep, and Update drops the tick loop.
	Active bool
	// Width is the sweep span in terminal cells. 0 (or narrower than the
	// text) falls back to the text's own display width.
	Width int
	// Base is the resting rune colour (hex, e.g. "#6c7086").
	Base string
	// Highlight is the wave leading-edge colour (hex).
	Highlight string
	// Pulse is the softer trailing colour blended in behind the wave (hex).
	Pulse string
	// GlowWidth is the half-width, in cells, of the highlight wave.
	GlowWidth int
	// Step is how many cells the wave travels per frame.
	Step float64
}

// New returns a Model configured with the Catppuccin Mocha defaults and the
// given status text. The animation starts disabled; call SetActive(true) (or
// set Active directly) when the owning state begins. GlowWidth 0 selects an
// adaptive band proportional to the text length.
func New(text string) Model {
	return Model{
		Text:      text,
		Base:      ColorBase,
		Highlight: ColorHighlight,
		Pulse:     ColorPulse,
		GlowWidth: 0,
		Step:      defaultStep,
	}
}

// SetText replaces the animated text without resetting the frame counter.
func (m *Model) SetText(text string) {
	m.Text = text
}

// SetActive enables or disables the animation.
func (m *Model) SetActive(active bool) {
	m.Active = active
}

// Update implements tea.Model. It advances the frame on each FrameMsg while
// Active and re-schedules the next tick; any other message is passed through
// unchanged.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg.(type) {
	case FrameMsg:
		if !m.Active {
			return m, nil
		}
		m.Frame++
		return m, Tick()
	default:
		return m, nil
	}
}

// View implements tea.Model. It renders the animated status text, or the
// static base-colour text when inactive.
func (m Model) View() string {
	if !m.Active {
		return render(m.Text, 0, 0, m.Base, m.Highlight, m.Pulse, m.GlowWidth, 0)
	}
	return render(m.Text, m.Frame, m.Width, m.Base, m.Highlight, m.Pulse, m.GlowWidth, m.Step)
}

// Render animates text with the default Catppuccin Mocha palette. width is
// the sweep span in cells (0 = use the text's own width). It is a pure
// function so it can be tested and used without the full component lifecycle.
func Render(text string, frame int, width int) string {
	return render(text, frame, width, ColorBase, ColorHighlight, ColorPulse, 0, defaultStep)
}

// render is the pure sweep implementation shared by Render and Model.View.
func render(text string, frame int, width int, base, highlight, pulse string, glowWidth int, step float64) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)

	if glowWidth < 2 {
		glowWidth = 2
	}
	if step <= 0 {
		step = defaultStep
	}

	// Resolve the per-rune cell offsets once so the wave position is computed
	// in display cells, not code points (wide CJK glyphs occupy two cells).
	positions := make([]int, len(runes))
	textWidth := 0
	for i, r := range runes {
		positions[i] = textWidth
		textWidth += runewidth.RuneWidth(r)
	}
	if textWidth == 0 {
		return plain(text, base)
	}

	// The sweep travels across the status text itself. A caller may pass a
	// narrower span, but the span never exceeds the text so the wave never
	// lingers over empty cells to the right of the line.
	span := textWidth
	if width > 0 && width < span {
		span = width
	}

	// Adaptive glow: keep the highlight band proportional to the line length
	// so short status text still reads as a sweeping wave, not a full-line
	// pulse.
	if glowWidth <= 0 {
		glowWidth = adaptiveGlow(textWidth)
	}

	// The wave centre travels from off-screen left to off-screen right and
	// wraps, so each sweep has a full entry and exit phase.
	period := span + 2*glowWidth
	pos := math.Mod(float64(frame)*step, float64(period)) - float64(glowWidth)

	baseR, baseG, baseB := hexToRGB(base)
	hlR, hlG, hlB := hexToRGB(highlight)
	puR, puG, puB := hexToRGB(pulse)

	var b strings.Builder
	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if r == '\n' {
			b.WriteByte('\n')
			continue
		}
		if rw == 0 {
			// Combining marks and other zero-width runes are emitted without a
			// colour change, so the terminal inherits the preceding base
			// rune's wave colour instead of resetting the gradient.
			if i > 0 {
				b.WriteRune(r)
			}
			continue
		}
		d := math.Abs(float64(positions[i]) - pos)
		intensity := waveIntensity(d, float64(glowWidth))
		if intensity < 0.01 {
			b.WriteString(ansiFg(uint8(baseR), uint8(baseG), uint8(baseB)))
			b.WriteRune(r)
			continue
		}
		// Blend the pulse tone in behind the leading highlight so the wave
		// has a trailing gradient instead of a hard edge.
		leading := intensity
		trailing := math.Max(0, intensity-0.55) * 0.6
		ri := lerp(baseR, hlR, leading)
		gi := lerp(baseG, hlG, leading)
		bi := lerp(baseB, hlB, leading)
		ri = lerp(ri, puR, trailing)
		gi = lerp(gi, puG, trailing)
		bi = lerp(bi, puB, trailing)
		b.WriteString(ansiFg(uint8(ri), uint8(gi), uint8(bi)))
		b.WriteRune(r)
	}
	b.WriteString(ansiReset)
	return b.String()
}

// waveIntensity returns a cosine-bell intensity in [0,1] for a cell at
// distance d from the wave centre. It is 0 outside the glow window.
func waveIntensity(d, glow float64) float64 {
	if d > glow {
		return 0
	}
	return 0.5 * (1 + math.Cos(math.Pi*d/glow))
}

// adaptiveGlow picks a highlight-band half-width proportional to the text
// length, capped at defaultGlowWidth, so short status lines still animate as a
// wave rather than a full-line pulse.
func adaptiveGlow(textWidth int) int {
	if textWidth <= 0 {
		return defaultGlowWidth
	}
	g := textWidth / 4
	if g < 2 {
		g = 2
	}
	if g > defaultGlowWidth {
		g = defaultGlowWidth
	}
	return g
}

// plain renders text entirely in a single 24-bit foreground colour.
func plain(text, color string) string {
	r, g, b := hexToRGB(color)
	return ansiFg(uint8(r), uint8(g), uint8(b)) + text + ansiReset
}

// ansiFg emits a 24-bit truecolor foreground SGR sequence.
func ansiFg(r, g, b uint8) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

const ansiReset = "\x1b[0m"

// hexToRGB parses a "#rrggbb" hex colour into 0..255 floats.
func hexToRGB(hex string) (r, g, b float64) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0
	}
	rv, _ := strconv.ParseUint(hex[1:3], 16, 8)
	gv, _ := strconv.ParseUint(hex[3:5], 16, 8)
	bv, _ := strconv.ParseUint(hex[5:7], 16, 8)
	return float64(rv), float64(gv), float64(bv)
}

// lerp linearly interpolates a towards b by t in [0,1].
func lerp(a, b, t float64) float64 {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return a + (b-a)*t
}
