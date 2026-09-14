package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestPromptCursorSuppression pins the dual-mode prompt contract: while a
// scroll burst is active (lastScrollTime watermark) every frame renders the
// cursor-FROZEN static view (cursor pinned ON as a reverse-video SGR block,
// byte-stable across frames, input state untouched), and the watermark expiry
// restores the active blink-mode view on the next natural render — with zero
// timers. The blink phase is folded into the active view's memo key, so a
// scroll frame can never be rewritten by a mid-burst phase flip.
func TestPromptCursorSuppression(t *testing.T) {
	// Cursor styling is a no-op under the test env's ASCII color profile;
	// force TrueColor so cursor ANSI is observable in the assertions.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := readyChatModel(newTestModel())
	m.applyVirtualCursorMode()
	m.ti.SetValue("ask hello")
	m.ti.Focus()
	// Focused with a memoryful idle blink parked at the HIDDEN half-cycle,
	// the active view renders the cursor plain (invisible); the static
	// scroll frame FREEZES it ON as a reverse-video block. Pinning the
	// phase true makes the two modes observably distinct — the dual-mode
	// contract: one cursor, one of two deterministic presentations.
	m.cursorHiddenPhase = true

	active := m.renderPromptView()
	static := m.renderPromptViewStatic()
	if active == static {
		t.Fatalf("dual-mode prompt requires distinct active/static frames, both %q", active)
	}
	if !strings.Contains(static, "\x1b[7m") {
		t.Fatalf("static frame must freeze the cursor ON as a reverse-video block: %q", static)
	}
	if strings.Contains(active, "\x1b[7m") {
		t.Fatalf("hidden-phase active frame must render the cursor plain (invisible): %q", active)
	}
	val, pos, focused := m.ti.Value(), m.ti.Position(), m.ti.Focused()

	// Burst: five wheel frames must all carry the static view, frozen.
	var firstInput string
	for i := 0; i < 5; i++ {
		_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		if !m.isScrollActive() {
			t.Fatalf("wheel %d must mark the scroll burst", i)
		}
		ws := m.assembleScreen(nil)
		if !strings.Contains(ws.Input, static) {
			t.Fatalf("scroll frame %d missing static prompt view", i)
		}
		if strings.Contains(ws.Input, active) {
			t.Fatalf("scroll frame %d leaked active cursor ANSI", i)
		}
		if i == 0 {
			firstInput = ws.Input
		} else if ws.Input != firstInput {
			t.Fatalf("scroll frames must freeze the prompt region (frame 0 != frame %d)", i)
		}
	}

	// Scroll frames must never mutate input state.
	if m.ti.Value() != val || m.ti.Position() != pos || m.ti.Focused() != focused {
		t.Errorf("scroll burst mutated input state: value %q pos %d focused %t",
			m.ti.Value(), m.ti.Position(), m.ti.Focused())
	}

	// Watermark expiry lifts suppression: a stale watermark (older than the
	// scroll-active window) must be treated as inactive, and the next render
	// restores the active cursor view.
	m.lastScrollTime = time.Now().Add(-scrollActiveWindow - time.Millisecond)
	if m.isScrollActive() {
		t.Fatal("expired watermark must not be treated as an active scroll burst")
	}
	ws := m.assembleScreen(nil)
	if !strings.Contains(ws.Input, active) {
		t.Error("post-expiry frame must restore the active cursor view")
	}

	// Instant edit recovery: any KeyMsg clears the watermark immediately so
	// the active cursor returns on the very next frame (no waiting).
	m.markScrollBurst()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.isScrollActive() {
		t.Error("KeyMsg must lift scroll suppression instantly (watermark cleared)")
	}
}

// TestMouseScrollIsolation pins strict mouse event isolation: wheel,
// press, motion, and release never reach the prompt input component —
// value, cursor position, focus, and the memoized prompt frame are
// untouched — while the wheel still marks the scroll burst watermark.
func TestMouseScrollIsolation(t *testing.T) {
	m := buildScrollableModel()
	m.ti.SetValue("ask input")
	m.ti.Focus()
	val, pos, focused := m.ti.Value(), m.ti.Position(), m.ti.Focused()
	cached := m.renderPromptView()

	_, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if cmd != nil {
		t.Errorf("wheel must return a zero command on the hot path, got %T", cmd)
	}
	if !m.isScrollActive() {
		t.Error("wheel must mark the scroll burst watermark")
	}
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 2})
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 2, Y: 4})
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 2, Y: 4})

	if m.ti.Value() != val || m.ti.Position() != pos || m.ti.Focused() != focused {
		t.Errorf("mouse leaked into input state: value %q pos %d focused %t",
			m.ti.Value(), m.ti.Position(), m.ti.Focused())
	}
	if got := m.renderPromptView(); got != cached {
		t.Error("mouse events must reuse the memoized prompt frame")
	}
}

// TestZeroTimerMouseHandling pins the TTY decoupling §2 invariant: the wheel
// and scroll-key hot path arms ZERO timers and ZERO goroutines. Every scroll
// frame returns a nil command, and burst liveness is derived purely from the
// lastScrollTime watermark.
func TestZeroTimerMouseHandling(t *testing.T) {
	m := buildScrollableModel()
	before := m.lastScrollTime

	for i := 0; i < 25; i++ {
		btn := tea.MouseButtonWheelUp
		if i%2 == 1 {
			btn = tea.MouseButtonWheelDown
		}
		_, cmd := m.Update(tea.MouseMsg{Button: btn})
		if cmd != nil {
			t.Fatalf("wheel frame %d armed a command (%T) — zero-timer contract violated", i, cmd)
		}
	}
	if m.lastScrollTime.IsZero() || !m.lastScrollTime.After(before) {
		t.Fatal("wheel frames must advance the scroll-burst watermark")
	}

	// Scroll keys (StateProcessing vi-nav) are equally timer-free. Use an
	// initialized workspace so keys route through the real scroll-key handler.
	keyM := initializedChatModel(t)
	keyM.state = StateProcessing
	keyM.refreshViewportContent()
	_, cmd := keyM.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if cmd != nil {
		t.Fatalf("scroll key armed a command (%T) — zero-timer contract violated", cmd)
	}
	if !keyM.isScrollActive() {
		t.Error("scroll key must mark the scroll burst watermark")
	}
}

// TestViewportFastPath pins the zero-overhead scroll assembly: consecutive
// scroll-only frames reuse cached header/footer chrome (hit counter
// advances, strings identical), while any state-changing message, width
// change, or live toast forces a full recompute.
func TestViewportFastPath(t *testing.T) {
	m := buildScrollableModel()

	first := m.assembleScreen(nil)
	if !m.chromeCacheValid {
		t.Fatal("full compose must populate the chrome cache")
	}
	if m.chromeCacheHits != 0 {
		t.Fatalf("full compose must not count as a cache hit, got %d", m.chromeCacheHits)
	}

	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	second := m.assembleScreen(nil)
	if m.chromeCacheHits != 1 {
		t.Fatalf("scroll frame must hit the chrome cache, hits=%d", m.chromeCacheHits)
	}
	if second.Header != first.Header || second.Footer != first.Footer {
		t.Error("scroll fast path must reuse header/footer verbatim")
	}

	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	_ = m.assembleScreen(nil)
	if m.chromeCacheHits != 2 {
		t.Fatalf("consecutive scroll frames must keep hitting, hits=%d", m.chromeCacheHits)
	}

	// A state-changing message (stream token) dirties the cache.
	m.streaming = true
	_, _ = m.Update(tokenMsg("live chunk"))
	hits := m.chromeCacheHits
	_ = m.assembleScreen(nil)
	if m.chromeCacheHits != hits {
		t.Error("token arrival must force a full recompute, not a cache hit")
	}

	// Width change and live toasts bypass the fast path.
	m.width += 10
	hits = m.chromeCacheHits
	_ = m.assembleScreen(nil)
	if m.chromeCacheHits != hits {
		t.Error("width change must force a full recompute")
	}
	m.setToast("transient")
	hits = m.chromeCacheHits
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	_ = m.assembleScreen(nil)
	if m.chromeCacheHits != hits {
		t.Error("live toast must force a full recompute")
	}
}

// TestFastPathViewportAssembly pins the TTY decoupling §4 contract: the
// scroll fast-path body is a raw string slice+join over the cached line pool
// whose bytes are IDENTICAL to the bubbles viewport rendering surface. It
// also proves the pool is rebuilt only on refresh (never per scroll event).
func TestFastPathViewportAssembly(t *testing.T) {
	m := buildScrollableModel()
	// Reconcile the viewport surface height with the authoritative geometry
	// (as assembleScreen does) so the refresh offsets and the parity slice
	// both span the real viewport rows.
	geo := m.viewportGeometry()
	m.Viewport.Height = geo.Height
	m.refreshViewportContent()
	if len(m.scrollDocLines) != m.lastScrollTotal {
		t.Fatalf("pool size %d != lastScrollTotal %d", len(m.scrollDocLines), m.lastScrollTotal)
	}
	if m.scrollSpaceLine == "" || m.scrollSpaceWidth != m.width {
		t.Fatalf("space row cache not initialized (width %d, space %d)", m.width, m.scrollSpaceWidth)
	}

	// Byte-parity of the pool-slice against the bubbles viewport padding:
	// feed the same window into the viewport surface and compare bytes.
	top := m.docScrollOffset
	visible := m.scrollDocLines[top : top+geo.Height]
	m.Viewport.SetContent(strings.Join(visible, "\n"))
	viewportBody := m.Viewport.View()
	poolBody := m.composeViewportWindow(top, m.width, geo.Height)
	if poolBody != viewportBody {
		t.Fatalf("composeViewportWindow != viewport surface\npool: %q\nvp:   %q", poolBody, viewportBody)
	}

	// The pool reference is stable across scroll events (no rebuild).
	poolRef := m.scrollDocLines
	_, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if cmd != nil {
		t.Fatalf("wheel armed a command (%T)", cmd)
	}
	if got := len(m.scrollDocLines); got != len(poolRef) {
		t.Fatalf("scroll must not rebuild the line pool (%d -> %d)", len(poolRef), got)
	}

	// The wheel frame's fast-path body equals the pool slice at the new offset.
	ws := m.assembleScreen(nil)
	newTop := m.docScrollOffset
	want := m.composeViewportWindow(newTop, m.width, geo.Height)
	if ws.Viewport != want {
		t.Fatalf("fast-path body mismatch at offset %d\ngot:  %q\nwant: %q", newTop, ws.Viewport, want)
	}
}

// TestVirtualSoftwareCursor pins the TTY decoupling §1 contract: the prompt
// cursor is a pure in-band SGR cell — reverse-video block in the visible
// phase, plain character in the hidden phase, no hardware cursor sequences
// (\x1b[?25h/\x1b[?25l) and no CSI positioning — with the phase toggled ONLY
// by the model-level cursorBlinkTickMsg. bubbles' own cursor.BlinkMsg must
// not disturb the presentation (the active view renders from a clone whose
// Blink flag is driven by cursorHiddenPhase), and the static scroll frame
// freezes the cursor in the ON position independently of the blink phase.
func TestVirtualSoftwareCursor(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := readyChatModel(newTestModel())
	m.applyVirtualCursorMode()
	if !m.virtualCursorEnabled() {
		t.Fatal("cursor must run in CursorStatic software mode")
	}
	// Focusing a static-mode cursor arms NO blink timer.
	if cmd := m.ti.Focus(); cmd != nil {
		t.Fatalf("software cursor Focus() must return no command (no blink timer), got %T", cmd)
	}

	m.ti.SetValue("abc")
	m.ti.SetCursor(1) // cursor on 'b'

	out := m.renderPromptView()
	if strings.Contains(out, "\x1b[?25h") || strings.Contains(out, "\x1b[?25l") {
		t.Fatal("prompt must never emit hardware cursor show/hide sequences")
	}
	if strings.Contains(out, "\x1b[6n") {
		t.Fatal("prompt must never emit device-status-report CSI")
	}
	if !strings.Contains(out, "\x1b[7m") {
		t.Errorf("active prompt must render the cursor as a reverse-video SGR block: %q", out)
	}

	// bubbles' own blink message must not sway the presentation: the active
	// frame is cloned with Blink sourced from cursorHiddenPhase, so the
	// underlying ti flag stays irrelevant to what renders.
	first := m.renderPromptView()
	for i := 0; i < 3; i++ {
		_, _ = m.Update(cursor.BlinkMsg{})
	}
	if got := m.renderPromptView(); got != first {
		t.Errorf("software cursor presentation must be stable across bubbles blink messages\nbefore: %q\nafter: %q", first, got)
	}

	// The model-level idle blink toggles the phase while focused and idle.
	if !m.cursorHiddenPhase {
		_, _ = m.Update(cursorBlinkTickMsg(time.Now()))
		if !m.cursorHiddenPhase {
			t.Error("cursorBlinkTickMsg must flip the hidden phase while focused")
		}
	}
	hidden := m.renderPromptView()
	if strings.Contains(hidden, "\x1b[7m") {
		t.Errorf("hidden-phase active frame must render the cursor plain: %q", hidden)
	}
	if hidden == first {
		t.Errorf("hidden-phase frame must differ from the visible-phase frame: %q", hidden)
	}

	// Static (scroll-suppressed) clone FREEZES the cursor ON as a
	// reverse-video block regardless of the phase — byte-stable, frozen.
	static := m.renderPromptViewStatic()
	if !strings.Contains(static, "\x1b[7m") {
		t.Fatal("static prompt view must freeze the cursor ON as a reverse-video SGR block")
	}
	for i := 0; i < 3; i++ {
		if got := m.renderPromptViewStatic(); got != static {
			t.Fatalf("static prompt view not byte-stable: %q vs %q", got, static)
		}
	}
}
