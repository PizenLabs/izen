package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── AUTO-SCROLL DETACH LATCH & FRAME-THROTTLED VIEWPORT SYNC ────────────────
//
// Two guarantees are under test, and they are the two halves of the same
// complaint: "scrolling lags and jitters while the answer streams".
//
// The lag is a THROTTLE question — how much work does the UI goroutine do per
// incoming token. The jitter is a LATCH question — whose scroll position wins
// when a token arrives and the reader has deliberately moved.
//
// A test that only checked the latch would pass on the old code, because the
// conversation already had one. A test that only checked the throttle would miss
// a latch that re-wraps correctly but then hands the window to GotoBottom. So
// both are pinned here, and TestReasoningContentIsThrottledToTheFrameTick
// asserts the negative (the render path does NOT sync) because that is the only
// way to catch a regression back to per-token work.

// latchModel returns a chat model with a conversation long enough to scroll.
func latchModel() *model {
	return buildScrollableModel()
}

// TestUserDetachedIsTheSingleLatchAuthority pins the consolidation. One bit,
// written from one place, with the legacy mirrors following it — because three
// names for "the reader left the tail" is how the question came to have three
// different answers depending on which file asked it.
func TestUserDetachedIsTheSingleLatchAuthority(t *testing.T) {
	m := latchModel()
	if m.userDetached {
		t.Fatal("precondition: a fresh model must be armed to follow the tail")
	}

	for _, locked := range []bool{true, false, true, false} {
		m.setScrollLocked(locked)
		if m.userDetached != locked {
			t.Fatalf("setScrollLocked(%v): userDetached = %v", locked, m.userDetached)
		}
		// Every legacy name must agree, or a render gate reading a different
		// one than the tail-pin decision would disagree with it.
		if m.userScrolledAway != locked || m.userIsScrollingUp != locked || m.userScrollLocked != locked {
			t.Fatalf("setScrollLocked(%v): mirrors diverged (away=%v up=%v locked=%v)",
				locked, m.userScrolledAway, m.userIsScrollingUp, m.userScrollLocked)
		}
	}
}

// TestDetachedViewportIsPinnedWhileTokensStream is the primary DoD for the
// conversation: scroll up mid-answer, keep streaming, and the window must not
// move — while the document underneath keeps growing.
func TestDetachedViewportIsPinnedWhileTokensStream(t *testing.T) {
	m := latchModel()
	m.streaming = true

	// The reader scrolls up.
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("precondition: scrolling up must engage the auto-scroll detach latch")
	}
	held := m.docScrollOffset
	if held >= m.maxAppScroll() {
		t.Fatalf("precondition: the scroll must leave the tail (offset %d, max %d)", held, m.maxAppScroll())
	}

	// The answer keeps arriving, and frames keep being painted.
	for i := 0; i < 25; i++ {
		nm, _ = m.Update(tokenMsg("streaming chunk "))
		m = nm.(*model)
		nm, _ = m.Update(repaintTickMsg{})
		m = nm.(*model)
	}
	if m.docScrollOffset != held {
		t.Errorf("streaming moved the detached viewport %d -> %d", held, m.docScrollOffset)
	}
	if !m.userDetached {
		t.Error("streaming cleared the detach latch")
	}
	if m.maxAppScroll() <= held {
		t.Error("precondition: the document should have grown under the detached reader")
	}
}

// TestScrollingBackToTheBottomReArmsAutoScroll is the other half of the DoD:
// the latch releases only at the absolute bottom, so a reader who scrolls back
// down but is still catching up is not yanked by the next chunk.
func TestScrollingBackToTheBottomReArmsAutoScroll(t *testing.T) {
	m := latchModel()
	m.streaming = true
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("precondition: the wheel-up must detach")
	}

	// A wheel-down that lands short of the bottom keeps the latch. The offset
	// is placed several rows above the tail so the step cannot overshoot it.
	m.docScrollOffset = m.maxAppScroll() - 10
	nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("a downward scroll that stopped short of the bottom must stay detached")
	}

	// Reaching the absolute bottom re-arms.
	for i := 0; i < 200 && m.userDetached; i++ {
		nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = nm.(*model)
	}
	if m.userDetached {
		t.Fatal("reaching the absolute bottom must re-arm auto-scroll")
	}
	if m.docScrollOffset != m.maxAppScroll() {
		t.Errorf("re-armed offset %d != tail %d", m.docScrollOffset, m.maxAppScroll())
	}

	// And now the tail owns the viewport again: a new chunk moves it.
	before := m.docScrollOffset
	nm, _ = m.Update(tokenMsg("one more chunk that wraps onto extra rows of document"))
	m = nm.(*model)
	nm, _ = m.Update(repaintTickMsg{})
	m = nm.(*model)
	if m.docScrollOffset < before {
		t.Errorf("an armed viewport drifted off the tail: %d -> %d", before, m.docScrollOffset)
	}
}

// ── The reasoning panel's own latch ─────────────────────────────────────────

// liveReasoningPanel returns a model with a GROWING reasoning trace visible in
// the focused panel, scrolled to the tail and armed to follow it.
//
// It builds its own buffer rather than reusing reasoningTestModel because that
// helper ends with MarkComplete, and ThinkingBuffer.Append treats a completed
// buffer as the start of a NEW reasoning block: it resets the builder and keeps
// only the new chunk. Appending to it would shrink the trace rather than grow
// it. A live stream is an OPEN buffer, so that is what this one is.
func liveReasoningPanel(t *testing.T, lines int) *model {
	t.Helper()
	m := newWorkspaceModel(t)
	m = setPane(t, m, 100, 40)
	m.thinkingBuffer = NewThinkingBuffer()
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("reasoning step ")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	m.thinkingBuffer.Append(b.String())
	m.streaming = true

	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = nm.(*model)
	if !m.reasoningExpanded || !m.reasoningScrollLocked() {
		t.Fatal("precondition: Ctrl+O must mount and focus the reasoning panel")
	}
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)
	if !m.reasoningScrollable() {
		t.Fatal("precondition: the trace must overflow the panel for scrolling to be observable")
	}
	if m.reasoningDetached {
		t.Fatal("precondition: a freshly opened panel must be armed to follow the tail")
	}
	return m
}

// growReasoning appends more live reasoning to the panel's source and paints one
// frame, which is what a token burst looks like from the panel's point of view.
func growReasoning(t *testing.T, m *model, chunk string) *model {
	t.Helper()
	m.thinkingBuffer.Append(chunk)
	nm, _ := m.Update(FrameTickMsg{})
	return nm.(*model)
}

// TestReasoningDetachLatchSurvivesAGrowingStream: the reader scrolls up inside
// the panel, and the trace keeps growing underneath them. The window must hold.
func TestReasoningDetachLatchSurvivesAGrowingStream(t *testing.T) {
	m := liveReasoningPanel(t, 200)

	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.reasoningDetached {
		t.Fatal("scrolling up in the reasoning panel must engage its detach latch")
	}
	held := m.reasoningViewport.YOffset
	if m.reasoningViewport.AtBottom() {
		t.Fatal("precondition: the wheel-up must leave the tail")
	}

	// More reasoning arrives, and frames are painted.
	for i := 0; i < 20; i++ {
		m = growReasoning(t, m, "additional reasoning step that adds wrapped rows\n")
	}
	if m.reasoningViewport.YOffset != held {
		t.Errorf("a growing stream moved the detached panel %d -> %d", held, m.reasoningViewport.YOffset)
	}
	if !m.reasoningDetached {
		t.Error("a growing stream cleared the panel's detach latch")
	}
	if !m.reasoningScrollable() {
		t.Error("precondition: the trace should have grown past the panel")
	}
}

// TestReasoningDetachReArmsOnlyAtTheBottom mirrors the conversation's rule.
func TestReasoningDetachReArmsOnlyAtTheBottom(t *testing.T) {
	m := liveReasoningPanel(t, 200)
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.reasoningDetached {
		t.Fatal("precondition: the wheel-up must detach")
	}

	// Park several rows short of the bottom: still detached, because a reader
	// who is still catching up must not be yanked by the next chunk.
	m.reasoningViewport.SetYOffset(m.reasoningViewport.TotalLineCount() - m.reasoningViewport.Height - 10)
	nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = nm.(*model)
	if !m.reasoningDetached {
		t.Fatal("a downward scroll that stopped short of the bottom must stay detached")
	}

	// Reach the absolute bottom: re-armed, and the tail is followed again.
	for i := 0; i < 400 && m.reasoningDetached; i++ {
		nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = nm.(*model)
	}
	if m.reasoningDetached {
		t.Fatal("reaching the absolute bottom must re-arm the panel's follow")
	}
	if !m.reasoningViewport.AtBottom() {
		t.Fatalf("precondition: the re-armed panel is not at the tail (%d)",
			m.reasoningViewport.YOffset)
	}

	tail := m.reasoningViewport.YOffset
	m = growReasoning(t, m, "a further reasoning step that grows the trace past the window\n")
	if !m.reasoningViewport.AtBottom() {
		t.Errorf("a re-armed panel stopped following the tail (%d, bottom %d)",
			m.reasoningViewport.YOffset, m.reasoningViewport.TotalLineCount()-m.reasoningViewport.Height)
	}
	if m.reasoningViewport.YOffset <= tail {
		t.Error("the re-armed panel did not advance with the growing trace")
	}
}

// TestReasoningScrollKeysMaintainTheLatch: the keyboard is a second input
// surface for the same panel, and it must not leave the latch behind.
func TestReasoningScrollKeysMaintainTheLatch(t *testing.T) {
	t.Run("up detaches", func(t *testing.T) {
		m := liveReasoningPanel(t, 200)
		if handled, _ := m.handleReasoningScrollKey(tea.KeyMsg{Type: tea.KeyUp}); !handled {
			t.Fatal("Up must be handled inside the focused panel")
		}
		if !m.reasoningDetached {
			t.Error("Up must engage the panel's detach latch")
		}
	})

	t.Run("page up detaches", func(t *testing.T) {
		m := liveReasoningPanel(t, 200)
		m.handleReasoningScrollKey(tea.KeyMsg{Type: tea.KeyPgUp})
		if !m.reasoningDetached {
			t.Error("PgUp must engage the panel's detach latch")
		}
	})

	t.Run("space re-arms unconditionally", func(t *testing.T) {
		m := liveReasoningPanel(t, 200)
		m.reasoningDetached = true
		if handled, _ := m.handleReasoningScrollKey(tea.KeyMsg{Type: tea.KeySpace}); !handled {
			t.Fatal("Space must be handled inside the focused panel")
		}
		if m.reasoningDetached {
			t.Error("Space is an explicit 'take me to the tail' and must re-arm the follow")
		}
	})

	t.Run("down to the bottom re-arms", func(t *testing.T) {
		m := liveReasoningPanel(t, 200)
		m.handleReasoningScrollKey(tea.KeyMsg{Type: tea.KeyUp})
		for i := 0; i < 400 && m.reasoningDetached; i++ {
			m.handleReasoningScrollKey(tea.KeyMsg{Type: tea.KeyDown})
		}
		if m.reasoningDetached {
			t.Error("scrolling to the absolute bottom with the keyboard must re-arm the follow")
		}
	})
}

// TestPanelScrollDoesNotDisturbTheConversation is the reason the panel has its
// own latch rather than sharing the conversation's. The panel holds an
// EXCLUSIVE wheel lock, so a scroll that reached it must not be able to change
// whether the conversation is following its own stream — otherwise a reader who
// scrolled up inside the reasoning trace would come back to a conversation that
// had silently stopped following, scrolled by a wheel that was never theirs.
func TestPanelScrollDoesNotDisturbTheConversation(t *testing.T) {
	m := liveReasoningPanel(t, 200)
	if m.userDetached {
		t.Fatal("precondition: the conversation must start armed")
	}

	for i := 0; i < 5; i++ {
		nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		m = nm.(*model)
	}
	if !m.reasoningDetached {
		t.Fatal("precondition: the panel wheel-up must have detached the panel")
	}
	if m.userDetached {
		t.Error("a wheel event routed to the reasoning panel detached the conversation")
	}
	if m.userScrolledAway || m.userIsScrollingUp || m.userScrollLocked {
		t.Error("a wheel event routed to the reasoning panel moved a conversation latch")
	}
}

// ── The throttle ────────────────────────────────────────────────────────────

// TestReasoningContentIsThrottledToTheFrameTick is the performance contract.
//
// The bug it pins: panel content was built from the LAYOUT pass, and the layout
// pass runs once per Bubble Tea message — so every incoming token re-wrapped the
// entire reasoning trace on the UI goroutine, the goroutine that has to drain
// the mouse queue. At high token throughput that is the whole "the wheel
// doesn't respond while it streams" report.
//
// The assertion is NEGATIVE on purpose. Checking that a tick eventually syncs
// would also pass on the old code, because the layout synced it sooner. Only
// "the render path does not sync, and one tick syncs once" distinguishes a
// throttled sync from an unthrottled one.
func TestReasoningContentIsThrottledToTheFrameTick(t *testing.T) {
	m := liveReasoningPanel(t, 200)
	syncedAt := m.reasoningSyncedBytes

	m.thinkingBuffer.Append("fresh reasoning that the panel has not been told about yet\n")

	// The render path runs as many times as a token burst produces messages.
	// Not one of them may build panel content.
	for i := 0; i < 40; i++ {
		m.assembleScreen(nil)
	}
	if m.reasoningSyncedBytes != syncedAt {
		t.Fatalf("the render path re-synced panel content (%d -> %d); content must be frame-locked",
			syncedAt, m.reasoningSyncedBytes)
	}

	// One frame tick picks it up — once.
	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	if m.reasoningSyncedBytes == syncedAt {
		t.Fatal("a frame tick did not pick up the new reasoning content")
	}
	afterTick := m.reasoningSyncedBytes
	if want := m.thinkingBuffer.Len(); afterTick != want {
		t.Errorf("the frame tick left the panel behind its source (%d of %d bytes)", afterTick, want)
	}

	// A second tick with no new content must not rebuild anything: the cost of
	// an idle frame has to be the O(1) staleness check, not a re-wrap.
	for i := 0; i < 10; i++ {
		nm, _ = m.Update(FrameTickMsg{})
		m = nm.(*model)
	}
	if m.reasoningSyncedBytes != afterTick {
		t.Errorf("an unchanged trace was re-synced (%d -> %d)", afterTick, m.reasoningSyncedBytes)
	}
}

// TestFrameTickStaysAliveWhileThePanelIsMounted: the tick is the only thing that
// can notice a change to the reasoning source, and reasoning is appended from
// seams that are not tied to the answer stream (the sub-task chunk path, the
// event-driven buffer). If the loop were gated on m.streaming, a change arriving
// with no tick in flight would never reach the panel.
func TestFrameTickStaysAliveWhileThePanelIsMounted(t *testing.T) {
	m := reasoningTestModel(t, 200)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = nm.(*model)

	// Nothing is streaming, nothing is loading: only the panel is mounted.
	if m.streaming || m.shimmerActive || m.skeletonActive() {
		t.Skip("precondition: an unrelated tick owner is active")
	}
	nm, cmd := m.Update(FrameTickMsg{})
	m = nm.(*model)
	if cmd == nil {
		t.Fatal("the frame tick must stay alive while the reasoning panel is mounted")
	}
	if !m.frameTickActive {
		t.Error("frameTickActive must be set while the panel holds the loop")
	}

	// Collapsing the panel releases it, so an idle app has no leaked timer.
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = nm.(*model)
	_, cmd = m.Update(FrameTickMsg{})
	if cmd != nil {
		t.Error("collapsing the panel must release the frame tick loop")
	}
}

// TestPanelReWrapsOnAResizeWithoutLosingTheReader: a resize is a content
// change like any other, so it goes through the same throttle — a frame tick is
// enough to catch it, and it must not cost the reader their offset.
func TestPanelReWrapsOnAResizeWithoutLosingTheReader(t *testing.T) {
	m := liveReasoningPanel(t, 200)
	m.reasoningViewport.GotoBottom()
	held := m.reasoningViewport.YOffset

	narrow := setPane(t, m, 60, 40)
	nm, _ := narrow.Update(FrameTickMsg{})
	narrow = nm.(*model)
	if narrow.reasoningSyncedWidth != 60 {
		t.Errorf("a frame tick did not re-wrap to the new width (%d)", narrow.reasoningSyncedWidth)
	}
	if narrow.reasoningViewport.YOffset != held {
		t.Errorf("the resize moved the reader from %d to %d", held, narrow.reasoningViewport.YOffset)
	}
}

// ── Turn boundary ───────────────────────────────────────────────────────────

// TestAutoScrollLatchResetsAtStreamCompletion: the latch exists to hold a
// reader off a GROWING tail. At completion there is no tail left, and the
// document has just been re-laid out (the streaming tail became a committed
// record, a status line was appended), so a preserved offset no longer
// addresses the content it was chosen for. Leaving it engaged would pin the
// NEXT thing that arrives to a stale row.
func TestAutoScrollLatchResetsAtStreamCompletion(t *testing.T) {
	m := latchModel()
	m.streaming = true
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("precondition: the wheel-up must detach")
	}
	// Chat mode keeps the completion path off the build-mode audit/mutation
	// branches, which are about provenance rather than scroll state.
	m.resolver.Set(modes.ModeAsk)
	m.currentStreamContent = "the answer"
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.utf8StreamBuf.Append([]byte("the answer"))

	nm, _ = m.Update(streamDoneMsg{content: "the answer"})
	m = nm.(*model)

	if m.streaming {
		t.Fatal("precondition: the stream should be finished")
	}
	if m.userDetached {
		t.Error("stream completion must re-arm the auto-scroll latch")
	}
	if m.userScrolledAway || m.userIsScrollingUp || m.userScrollLocked {
		t.Error("stream completion left a conversation latch engaged")
	}
	if m.docScrollOffset != m.maxAppScroll() {
		t.Errorf("the completed turn is not anchored to the tail (offset %d, max %d)",
			m.docScrollOffset, m.maxAppScroll())
	}
	// The completion frame must carry the whole answer, not a torn tail.
	if !strings.Contains(m.currentStreamContent, "the answer") &&
		!strings.Contains(recordsText(m), "the answer") {
		t.Error("stream completion did not render the final content buffer")
	}
}

// TestAutoScrollLatchResetsOnClear: a cleared document has no tail, so an
// offset carried over from the conversation the user just dismissed describes a
// document that no longer exists.
func TestAutoScrollLatchResetsOnClear(t *testing.T) {
	m := latchModel()
	m.streaming = true
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = nm.(*model)
	if !m.userDetached {
		t.Fatal("precondition: the wheel-up must detach")
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/clear"), Alt: false})
	m = nm.(*model)
	_ = nm

	// Drive the command through the same entry point the router uses, since a
	// bare "/clear" only reaches it once the prompt has been submitted.
	m = readyChatModel(m)
	m.resetTransientInteraction()

	if m.userDetached {
		t.Error("/clear must re-arm the auto-scroll latch")
	}
	if m.userScrolledAway || m.userIsScrollingUp || m.userScrollLocked {
		t.Error("/clear left a conversation latch engaged")
	}
}

// TestPromptSubmissionReArmsTheLatch pins the other turn boundary: submitting a
// new prompt re-arms auto-follow, so a turn can never inherit the previous
// turn's detach state — including when the model is not yet ready and
// followTail's early return would otherwise skip the unlock.
func TestPromptSubmissionReArmsTheLatch(t *testing.T) {
	m := latchModel()
	m.setScrollLocked(true)
	m.Ready = false

	m.lockTailToNewPrompt()

	if m.userDetached || m.userScrolledAway || m.userIsScrollingUp || m.userScrollLocked {
		t.Error("a prompt submission must re-arm auto-follow, even before the model is ready")
	}
}
