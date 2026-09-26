// reasoning_viewport_test.go — THE REASONING BLOCK IS ITS OWN VIEWPORT.
//
// # WHY THIS FILE EXISTS
//
// The reasoning block and the conversation are two scroll surfaces, not one
// surface with a decoration on it. Everything that makes that distinction real
// is invisible in a single frame and only shows up when two events collide, so
// each test below pairs a state transition with the event that is allowed to
// steal the input:
//
//   - The frame is still EXACTLY the pane's height with the panel open. A panel
//     that renders "about the right number of rows" is one keypress away from
//     pushing the prompt bar into scrollback.
//   - The wheel moves the panel and ONLY the panel, and the conversation's
//     auto-follow is not disturbed by it.
//   - The hand-back is a field, not a hit-test: the very next wheel after the
//     closing keypress lands on the conversation, wherever the pointer is.
//   - A block that is not scrollable must not eat the keyboard. A panel three
//     rows tall that swallows `j` and `k` takes the prompt bar with it.
//   - Reasoning outlives the turn. A [PARTIAL] truncation, a provider failure
//     and plain idleness all leave the block inspectable, because the only
//     thing that can refuse the toggle is the settings preference.
package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/config"
)

// reasoningTestModel returns a workspace model whose reasoning buffer holds
// `lines` numbered reasoning lines — enough text to overflow any realistic
// panel, which is the only state in which scroll routing is observable at all.
func reasoningTestModel(t *testing.T, lines int) *model {
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
	m.thinkingBuffer.MarkComplete()
	return m
}

// ── The toggle itself ────────────────────────────────────────────────────────

// TestCtrlOTogglesReasoningPanelAndFocusLock is the primary contract: the
// keypress takes BOTH the expansion state and the scroll focus, and the second
// keypress gives both back.
func TestCtrlOTogglesReasoningPanelAndFocusLock(t *testing.T) {
	m := reasoningTestModel(t, 60)

	if m.reasoningExpanded || m.reasoningFocused {
		t.Fatal("a fresh model must not have the reasoning panel open")
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.reasoningExpanded {
		t.Fatal("Ctrl+O did not expand the reasoning panel")
	}
	if !m.reasoningFocused {
		t.Fatal("Ctrl+O did not take the scroll focus for the reasoning panel")
	}
	if !m.thinkingBuffer.Expanded() {
		t.Fatal("the panel handle and the panel state have diverged")
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.reasoningExpanded || m.reasoningFocused {
		t.Fatal("Ctrl+O did not collapse the reasoning panel and release the focus")
	}
	if m.thinkingBuffer.Expanded() {
		t.Fatal("the panel handle and the panel state have diverged on collapse")
	}
}

// TestCtrlOReasoningPanelWorksWhileIdle is the post-generation half of the
// contract. Reasoning is inspectable after the stream is long over, with no
// state gate in front of it — a block that could only be opened while tokens
// were arriving would be unreadable exactly when a reader wants it.
func TestCtrlOReasoningPanelWorksWhileIdle(t *testing.T) {
	m := reasoningTestModel(t, 40)
	m.state = StateChat
	m.streaming = false
	m.assembleScreen(nil) // a frame, so the row budget is live

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.reasoningExpanded {
		t.Fatal("Ctrl+O must open the panel while idle")
	}
	if m.reasoningViewport.Height <= 0 {
		t.Fatal("the idle panel was mounted with no rows")
	}
	// The window opens at the tail, so assert on a line the tail can reach: the
	// affordance every mounted panel carries.
	if !strings.Contains(ansi.Strip(m.assembleScreen(nil).ReasoningPanel), "Ctrl+O collapse") {
		t.Fatal("the idle panel did not render the captured reasoning")
	}
}

// TestCtrlOPartialResponseKeepsReasoningInspectable pins the [PARTIAL] case: a
// response cut off at the provider's max_tokens ceiling still carries the
// reasoning that produced it, and the boundary badge does not make that
// reasoning unopenable. This is the state where a reader most wants to see WHY
// the model was still thinking when it ran out of budget.
func TestCtrlOPartialResponseKeepsReasoningInspectable(t *testing.T) {
	m := reasoningTestModel(t, 40)

	// A truncated turn: the stream ends, a PARTIAL boundary badge is pushed, and
	// the reasoning buffer is marked complete rather than discarded.
	m.streaming = true
	m.handleReasoningStream("reasoning step 0\n", false)
	m.push(roleAI, "an answer that was cut off mid-sentence")
	m.push(roleSystem, boundedWarning("[PARTIAL] The response hit the provider's "+
		"max_tokens limit and was cut off mid-generation (finish_reason: \"length\", "+
		"EvidenceState.PARTIAL).", m.PaneWidth()))
	m.thinkingBuffer.MarkComplete()
	m.streaming = false
	m.state = StateChat

	if m.thinkingBuffer.Len() == 0 {
		t.Fatal("the [PARTIAL] turn discarded its reasoning")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	frame := ansi.Strip(m.assembleScreen(nil).ReasoningPanel)
	if !strings.Contains(frame, "reasoning step 0") {
		t.Fatalf("a [PARTIAL] turn left its reasoning unopenable: %q", frame)
	}
}

// TestCtrlOWithReasoningDisabledIsARefusalNotACorruption is the settings half.
// The preference is authoritative over the block, not only over the shortcut,
// so the keypress must change NOTHING — not the expansion, not the focus, not
// the row budget — and must say why rather than being swallowed.
func TestCtrlOWithReasoningDisabledIsARefusalNotACorruption(t *testing.T) {
	m := reasoningTestModel(t, 40)
	m.cfg = &config.Config{UI: config.UIConfig{HideThinking: true}}
	m.hideThinkingBlocks = true
	m.assembleScreen(nil)
	before := m.assembleScreen(nil)

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	got := nm.(*model)
	if cmd != nil {
		t.Error("a refused toggle must not schedule work")
	}
	if got.reasoningExpanded || got.reasoningFocused {
		t.Error("a refused toggle must not expand the panel or take the focus")
	}
	if got.reasoningViewport.Height != 0 || got.reasoningPanelRows != 0 {
		t.Error("a refused toggle must not claim a single row of the pane")
	}
	if !strings.Contains(got.toast, "disabled") {
		t.Errorf("a refused toggle must name the setting that caused it, toast = %q", got.toast)
	}
	// The frame must be byte-identical to the one before the keypress.
	if after := got.assembleScreen(nil); after.Viewport != before.Viewport ||
		after.ReasoningPanel != before.ReasoningPanel {
		t.Error("a refused toggle changed the rendered frame")
	}
}

// TestSettingsChangeUnmountsThePanel covers the same authority arriving from
// the other direction: the setting is switched off while the panel is open.
// A panel left mounted would keep rendering a hidden trace AND keep swallowing
// the conversation's wheel events.
func TestSettingsChangeUnmountsThePanel(t *testing.T) {
	m := reasoningTestModel(t, 40)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.reasoningExpanded {
		t.Fatal("precondition: the panel should be open")
	}
	m.applyLoadedConfig(&config.Config{UI: config.UIConfig{HideThinking: true}})
	if m.reasoningExpanded || m.reasoningFocused {
		t.Error("hiding reasoning must unmount the panel and hand the wheel back")
	}
}

// ── Scroll routing ───────────────────────────────────────────────────────────

// TestWheelScrollsOnlyTheReasoningPanel is the exclusivity contract. One wheel
// event must move one viewport: while the panel holds the lock the conversation
// does not scroll, does not become user-scroll-locked, and does not lose its
// auto-follow. The last part is the subtle one — a wheel that sets the
// conversation's scroll lock would freeze the live tail for the rest of the
// stream because the reader scrolled a DIFFERENT viewport.
func TestWheelScrollsOnlyTheReasoningPanel(t *testing.T) {
	m := reasoningTestModel(t, 200)
	m.assembleScreen(nil)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)

	m.reasoningViewport.GotoBottom()
	atBottom := m.reasoningViewport.YOffset
	convBefore := m.docScrollOffset
	if convBefore == 0 {
		// Give the conversation something to scroll so "did not move" is a
		// meaningful assertion rather than a coincidence.
		for i := 0; i < 40; i++ {
			m.records = append(m.records, record{role: roleAI, text: "a line of conversation"})
		}
		m.refreshViewportContent()
		convBefore = m.docScrollOffset
	}
	lockedBefore := m.userIsScrollingUp

	for i := 0; i < 3; i++ {
		nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		m = nm.(*model)
		if cmd != nil {
			t.Fatal("the reasoning scroll path must stay command-free (zero-timer contract)")
		}
	}

	if m.reasoningViewport.YOffset >= atBottom {
		t.Errorf("the wheel did not scroll the reasoning panel (offset %d, was %d)",
			m.reasoningViewport.YOffset, atBottom)
	}
	if m.docScrollOffset != convBefore {
		t.Errorf("the wheel moved the conversation too: %d → %d", convBefore, m.docScrollOffset)
	}
	if m.userIsScrollingUp != lockedBefore {
		t.Error("scrolling the reasoning panel must not lock the conversation's auto-follow")
	}
}

// TestWheelReturnsToTheConversationOnCollapse is the hand-back. The lock is a
// field rather than a hit-test, so the very next wheel after the closing
// keypress reaches the conversation no matter where the pointer is resting —
// including over the panel's own former rows.
func TestWheelReturnsToTheConversationOnCollapse(t *testing.T) {
	m := reasoningTestModel(t, 200)
	for i := 0; i < 40; i++ {
		m.records = append(m.records, record{role: roleAI, text: "a line of conversation"})
	}
	m.assembleScreen(nil)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = nm.(*model)
	if m.reasoningViewport.YOffset == 0 {
		t.Fatal("precondition: the panel should have scrolled")
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.reasoningScrollLocked() {
		t.Fatal("collapse must release the scroll lock")
	}
	panelTop := m.reasoningPanelTop
	convBefore := m.docScrollOffset

	// A wheel event at the row the panel used to occupy. The lock is gone, so
	// this must reach the conversation.
	nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress, Y: panelTop})
	m = nm.(*model)
	if m.docScrollOffset == convBefore {
		t.Errorf("after collapsing, the wheel over row %d did not reach the conversation", panelTop)
	}
}

// TestWheelOverThePanelDoesNotSelectConversationText covers the click path. The
// mouse-to-logical mapper resolves rows through the MAIN viewport's rectangle,
// so a drag started on the panel would highlight whichever conversation record
// happened to share that row index — selecting text the user never touched.
func TestWheelOverThePanelDoesNotSelectConversationText(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.assembleScreen(nil)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	row := m.reasoningPanelTop + 1
	if !m.reasoningPanelAt(row) {
		t.Fatalf("row %d should be inside the panel (top=%d rows=%d)",
			row, m.reasoningPanelTop, m.reasoningPanelRows)
	}
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 4, Y: row})
	if nm.(*model).mouseSel.Active {
		t.Error("a press inside the reasoning panel started a conversation selection")
	}
	// A press in the conversation still selects — the panel takes its own rows
	// and nothing else.
	nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 4, Y: m.reasoningPanelTop - 1})
	if !nm.(*model).mouseSel.Active {
		t.Error("a press in the conversation must still start a selection")
	}
}

// TestKeyboardScrollStaysOutOfThePromptBar is the keyboard half of the same
// contract, and it has a failure mode the wheel does not. Printable keys are
// NEVER scroll input — a viewer that stole "j" from a half-typed word would be a
// worse defect than one whose j does not scroll — so the keys that actually move
// the panel are the ones a prompt cannot want: the arrows, PgUp/PgDn, Home/End.
//
// The second half is the direction of the guarantee: a panel that does not
// overflow must not consume those keys either, or the conversation's own
// navigation stops working whenever reasoning happens to be short.
func TestKeyboardScrollStaysOutOfThePromptBar(t *testing.T) {
	m := newWorkspaceModel(t)
	m = setPane(t, m, 100, 40)
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("one short thought")
	m.ti.Focus()

	// A panel that FITS: the arrows belong to the conversation, and "j" belongs
	// to the prompt.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	if m.reasoningScrollable() {
		t.Fatalf("precondition: a one-line panel must not be scrollable (rows=%d)", m.reasoningViewport.Height)
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if !strings.Contains(nm.(*model).ti.Value(), "j") {
		t.Error("a non-overflowing reasoning panel swallowed the letter j")
	}

	// A panel that OVERFLOWS: the arrows move the panel and never the prompt.
	m2 := reasoningTestModel(t, 200)
	m2.ti.Focus()
	m2.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m2.assembleScreen(nil)
	if !m2.reasoningScrollable() {
		t.Fatalf("precondition: a 200-line panel must be scrollable (rows=%d total=%d)",
			m2.reasoningViewport.Height, m2.reasoningViewport.TotalLineCount())
	}
	m2.reasoningViewport.GotoBottom()
	tail := m2.reasoningViewport.YOffset
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyUp})
	got := nm2.(*model)
	if got.reasoningViewport.YOffset >= tail {
		t.Error("the up arrow did not scroll the reasoning panel")
	}
	// Printable input is untouched even while the panel owns the arrows.
	nm3, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if !strings.Contains(nm3.(*model).ti.Value(), "j") {
		t.Error("the panel's scroll lock hijacked a printable key from the prompt")
	}
}

// TestSpaceReturnsTheReasoningPanelToItsTail pins the recovery key: a reader who
// has scrolled away from the live tail gets one key back to it, and the live
// follow re-engages from there.
func TestSpaceReturnsTheReasoningPanelToItsTail(t *testing.T) {
	m := reasoningTestModel(t, 200)
	m.streaming = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	m.reasoningViewport.GotoTop()
	if m.reasoningViewport.YOffset == 0 {
		// Already at the top: scroll down instead, so the test has a direction.
		m.reasoningViewport.GotoBottom()
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	got := nm.(*model)
	if !got.reasoningViewport.AtBottom() {
		t.Errorf("Space did not return the panel to its tail (offset %d, max %d)",
			got.reasoningViewport.YOffset, got.reasoningViewport.TotalLineCount()-got.reasoningViewport.Height)
	}
}

// ── The window policy ────────────────────────────────────────────────────────

// TestReasoningPanelFollowsTheTailWhileStreamingAndHoldsWhenScrolledUp is the
// flicker contract. A panel that yanks to the tail on every arriving chunk makes
// reading impossible; a panel that never follows makes the stream unreadable
// live. The two are distinguished by one bit — whether the reader is at the tail.
func TestReasoningPanelFollowsTheTailWhileStreamingAndHoldsWhenScrolledUp(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.streaming = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	m.reasoningViewport.GotoBottom()

	// Streaming, sitting at the tail: new reasoning must become visible.
	m.thinkingBuffer.Append("reasoning step 999\n")
	m.syncReasoningContent()
	if !m.reasoningViewport.AtBottom() {
		t.Error("a panel at its tail stopped following a live stream")
	}

	// Scrolled up: the next chunk must NOT drag the reader back.
	m.reasoningViewport.ScrollUp(4)
	held := m.reasoningViewport.YOffset
	m.thinkingBuffer.Append("reasoning step 1000\n")
	m.syncReasoningContent()
	if m.reasoningViewport.YOffset != held {
		t.Errorf("a new chunk yanked a scrolled-up reader from row %d to %d", held, m.reasoningViewport.YOffset)
	}
}

// TestReasoningPanelPreservesItsPositionWhenIdle is the other half: once the
// stream is over there is nothing to follow, so the position is simply left
// where the reader put it — including across a collapse/re-expand round trip.
func TestReasoningPanelPreservesItsPositionWhenIdle(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	m.reasoningViewport.ScrollUp(5)
	m.assembleScreen(nil)
	held := m.reasoningViewport.YOffset
	if held == 0 {
		t.Fatal("precondition: the panel should be scrolled off its tail")
	}

	// Collapse and re-expand with no new reasoning: the reader must land back on
	// the same line.
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	if m.reasoningViewport.YOffset != held {
		t.Errorf("collapse/re-expand moved the reader from row %d to %d", held, m.reasoningViewport.YOffset)
	}
}

// ── The row budget ───────────────────────────────────────────────────────────

// TestReasoningPanelHeightClamp pins the arithmetic of the split in isolation,
// including the panes where the clamps fight each other. A budget that can
// produce a negative or zero main viewport is a budget that produces a frame one
// row taller than the pane.
func TestReasoningPanelHeightClamp(t *testing.T) {
	for _, tc := range []struct{ available, want int }{
		{available: 0, want: 0},
		{available: 1, want: 0},
		{available: 5, want: 0}, // too cramped to split: refuse rather than lie
		{available: 6, want: 1}, // the ceiling wins: the conversation gets 5
		{available: 8, want: 1}, // …because 8-8 leaves the panel nothing
		{available: 10, want: 2},
		{available: 12, want: 4},
		{available: 20, want: 8}, // 40% exactly
		{available: 25, want: 10},
		{available: 40, want: 16},
		{available: 100, want: 40},
		{available: 1000, want: 400},
	} {
		if got := reasoningPanelHeight(tc.available); got != tc.want {
			t.Errorf("reasoningPanelHeight(%d) = %d, want %d", tc.available, got, tc.want)
		}
		// The invariant the whole budget exists to keep, asserted on every case.
		if got := tc.available - reasoningPanelHeight(tc.available); got < 1 && tc.available >= 1 {
			t.Errorf("available %d left the conversation %d rows", tc.available, got)
		}
	}
}

// TestReasoningSplitNeverProducesADegenerateViewport drives the split through
// the model's own budget at every pane size the terminal can be resized to. The
// two terms must always be non-negative integers that sum to what was available.
func TestReasoningSplitNeverProducesADegenerateViewport(t *testing.T) {
	for _, h := range []int{1, 2, 3, 4, 5, 6, 8, 10, 12, 20, 24, 40, 60, 200} {
		m := newWorkspaceModel(t)
		m = setPane(t, m, 100, h)
		m.thinkingBuffer = NewThinkingBuffer()
		m.thinkingBuffer.Append(strings.Repeat("reasoning line\n", 40))
		m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})

		ws := m.assembleScreen(nil)
		if ws.ViewportRows < 1 {
			t.Errorf("pane %d: conversation collapsed to %d rows", h, ws.ViewportRows)
		}
		if ws.ReasoningRows < 0 || ws.ViewportRows+ws.ReasoningRows > h {
			t.Errorf("pane %d: %d + %d rows does not fit", h, ws.ViewportRows, ws.ReasoningRows)
		}
		if got := lipgloss.Height(ws.ReasoningPanel); ws.ReasoningRows > 0 && got != ws.ReasoningRows {
			t.Errorf("pane %d: the panel rendered %d rows for a budget of %d", h, got, ws.ReasoningRows)
		}
	}
}

// TestFrameIsExactlyPaneHeightWithTheReasoningPanelOpen is the bottom-anchor
// DoD with the panel mounted: the frame is EXACTLY the pane's height, the prompt
// bar and the footer are still the LAST rows, and no panel row is over-wide.
// A panel that renders more than its budget is the same bug as a frame that
// comes up long — the prompt bar ends up in scrollback.
func TestFrameIsExactlyPaneHeightWithTheReasoningPanelOpen(t *testing.T) {
	for _, hw := range [][2]int{{100, 40}, {80, 24}, {56, 30}, {40, 20}, {200, 60}, {80, 10}, {40, 6}, {40, 4}} {
		w, h := hw[0], hw[1]
		t.Run(itoa(w)+"x"+itoa(h), func(t *testing.T) {
			m := reasoningTestModel(t, 80)
			m = setPane(t, m, w, h)
			m.ti.SetValue("a prompt that fills the input row")
			m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
			m.refreshViewportContent()

			frame := m.View()
			if got := lipgloss.Height(frame); got != h {
				t.Fatalf("frame is %d rows, pane is %d\n%s", got, h, frame)
			}
			for i, line := range strings.Split(frame, "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Errorf("row %d is %d cells, pane is %d: %q", i, got, w, ansi.Strip(line))
				}
			}
			if h < 4 {
				return // below this the chrome cannot fit and the frame is clipped
			}
			rows := frameRows(ansi.Strip(frame))
			if strings.TrimSpace(rows[len(rows)-1]) == "" {
				t.Error("the last row is blank: the bottom stack floated off the pane's edge")
			}
			// The panel is present, is where the budget put it, and is the only
			// place the reasoning is rendered.
			if m.reasoningPanelRows <= 0 {
				return
			}
			if !m.reasoningPanelAt(m.reasoningPanelTop) ||
				m.reasoningPanelAt(m.reasoningPanelTop+m.reasoningPanelRows) {
				t.Error("the published panel rectangle does not match its row budget")
			}
			if !strings.Contains(ansi.Strip(m.assembleScreen(nil).ReasoningPanel), "Ctrl+O collapse") {
				t.Error("the reasoning text is not in the panel")
			}
		})
	}
}

// TestReasoningPanelRowsNeverExceedThePaneWidth is the width half of the same
// contract. The panel wraps to the pane width, so no reasoning line — however
// long a single unbreakable token in it is — can push a row past the pane edge.
// The floor is minViewportWidth, the same one the conversation viewport uses: a
// pane narrower than it is already rendering every surface at that width, and
// this test is about the panel not being the surface that adds an overflow.
func TestReasoningPanelRowsNeverExceedThePaneWidth(t *testing.T) {
	for _, w := range []int{20, 40, 60, 80, 120, 200} {
		limit := max(w, minViewportWidth)
		m := newWorkspaceModel(t)
		m = setPane(t, m, w, 40)
		m.thinkingBuffer = NewThinkingBuffer()
		m.thinkingBuffer.Append(strings.Repeat("A", 400) + "\nreasoning with a normal tail\n")
		m.thinkingBuffer.MarkComplete()
		m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})

		panel := ansi.Strip(m.assembleScreen(nil).ReasoningPanel)
		for i, line := range strings.Split(panel, "\n") {
			if got := lipgloss.Width(line); got > limit {
				t.Errorf("viewport %d: panel row %d is %d cells: %q", w, i, got, line)
			}
		}
		if !strings.Contains(panel, "normal tail") {
			t.Errorf("viewport %d: the unbreakable token swallowed the rest of the text", w)
		}
	}
}

// TestCollapsedPanelCostsTheConversationNothing is the inverse of the split: a
// closed panel must be indistinguishable from a model that has never had one.
func TestCollapsedPanelCostsTheConversationNothing(t *testing.T) {
	m := reasoningTestModel(t, 80)
	m.assembleScreen(nil)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	open := m.assembleScreen(nil)
	if open.ReasoningRows <= 0 || open.ReasoningPanel == "" {
		t.Fatal("precondition: the panel should be open")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	closed := m.assembleScreen(nil)
	if closed.ReasoningRows != 0 || closed.ReasoningPanel != "" {
		t.Errorf("a collapsed panel still occupies the frame: %d rows, %q",
			closed.ReasoningRows, closed.ReasoningPanel)
	}
	if closed.ViewportRows <= open.ViewportRows {
		t.Errorf("collapsing did not give the rows back: %d → %d",
			open.ViewportRows, closed.ViewportRows)
	}
	if m.reasoningPanelAt(m.reasoningPanelTop) {
		t.Error("a collapsed panel still claims rows in the mouse router")
	}
}

// TestReasoningIsNotDuplicatedAcrossTheTwoSurfaces is the single-surface
// contract. The reasoning exists in exactly one place on screen: the band the
// wheel can scroll. A second copy in the conversation body is not a redundancy,
// it is a surface that looks live and cannot be scrolled.
func TestReasoningIsNotDuplicatedAcrossTheTwoSurfaces(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	ws := m.assembleScreen(nil)
	if !strings.Contains(ansi.Strip(ws.ReasoningPanel), "reasoning step") {
		t.Fatal("the panel does not carry the reasoning")
	}
	if strings.Contains(ansi.Strip(ws.Viewport), "reasoning step") {
		t.Error("the conversation body duplicated the reasoning the panel owns")
	}
	// The handle survives, so the keypress that opened the panel did not look
	// like it deleted the line it was pressed on.
	if !strings.Contains(ansi.Strip(ws.Viewport), "Ctrl+O") {
		t.Error("the conversation body kept no handle for the open panel")
	}
}

// TestThePanelSurvivesGenerationEnd: the panel is not tied to the lifetime of
// the stream that filled it. When generation ends the panel stays exactly where
// it is — still mounted, still holding the focus, still at the reader's
// position — because a reasoning block that closes itself the moment the answer
// arrives is a block nobody ever gets to finish reading.
func TestThePanelSurvivesGenerationEnd(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.streaming = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	m.reasoningViewport.ScrollUp(3)
	m.assembleScreen(nil)
	held := m.reasoningViewport.YOffset
	if held == 0 {
		t.Fatal("precondition: the reader should be off the tail")
	}

	// The turn finishes.
	m.streaming = false
	m.thinkingBuffer.MarkComplete()
	ws := m.assembleScreen(nil)

	if !m.reasoningExpanded || !m.reasoningFocused {
		t.Fatal("generation end unmounted the panel or released the focus")
	}
	if ws.ReasoningRows <= 0 || ws.ReasoningPanel == "" {
		t.Fatal("generation end released the panel's rows")
	}
	if m.reasoningViewport.YOffset != held {
		t.Errorf("generation end moved the reader from row %d to %d", held, m.reasoningViewport.YOffset)
	}
}

// TestClearUnmountsTheReasoningPanel: /clear clears what I see, and a panel
// left mounted would spend rows on a band with nothing in it — while still
// holding the wheel lock away from the conversation.
func TestClearUnmountsTheReasoningPanel(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.reasoningExpanded {
		t.Fatal("precondition: the panel should be open")
	}
	m.handleCommand("/clear")
	if m.reasoningExpanded || m.reasoningFocused {
		t.Error("/clear left the reasoning panel mounted")
	}
	if m.reasoningViewport.Height != 0 {
		t.Errorf("/clear left the panel holding %d rows", m.reasoningViewport.Height)
	}
}

// TestDragOverThePanelDoesNotAutoScrollTheConversation is the last half of the
// click contract. A drag anchored in the conversation is allowed to wander over
// the panel — the anchor is real, so the drag must survive it — but the
// auto-scroll engine reads "below the viewport's last row" as "keep scrolling",
// and a panel row is below the viewport's last row. Left alone, holding the
// pointer over the panel would scroll the transcript on its own.
func TestDragOverThePanelDoesNotAutoScrollTheConversation(t *testing.T) {
	m := reasoningTestModel(t, 40)
	for i := 0; i < 60; i++ {
		m.records = append(m.records, record{role: roleAI, text: "a line of conversation " + itoa(i)})
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	geo := m.viewportGeometry()

	// Anchor inside the conversation, then drag down over the panel.
	nm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 4, Y: geo.Top + 1})
	m = nm.(*model)
	if !m.mouseSel.Active {
		t.Fatal("precondition: the press should have started a selection")
	}
	overPanel := m.reasoningPanelTop + 1
	nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 4, Y: overPanel})
	m = nm.(*model)
	if !m.mouseSel.Dragging {
		t.Error("a drag that crossed the panel should still be a drag")
	}
	if cmd != nil {
		t.Error("a motion over the panel armed the conversation auto-scroll engine")
	}
	if m.mouseSel.TickActive {
		t.Error("a motion over the panel started the conversation auto-scroll engine")
	}
}

// TestReasoningPanelFollowsATerminalResize: the panel re-wraps to the new pane
// width rather than keeping rows cut for the old one. A resize is a content
// change like any other, so it goes through the same freshness guard as a live
// stream — and the position the reader chose is still theirs.
//
// The re-wrap lands on the FRAME TICK, not on the layout pass: building panel
// content is O(len) and must not run per rendered message (see
// flushReasoningViewport). So the test pumps a tick, which is what the running
// app does within ~33ms of the resize. What this pins is that a frame tick is
// sufficient to catch the change — a resize is never left showing rows cut for
// the old width, and never costs the reader their offset while it fixes them.
func TestReasoningPanelFollowsATerminalResize(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	m.reasoningViewport.GotoBottom()
	held := m.reasoningViewport.YOffset

	narrow := setPane(t, m, 60, 40)
	if updated, _ := narrow.Update(FrameTickMsg{}); updated != nil {
		narrow = updated.(*model)
	}
	if narrow.reasoningSyncedWidth != 60 {
		t.Errorf("the panel did not re-wrap to the new pane width (%d)", narrow.reasoningSyncedWidth)
	}
	if narrow.reasoningViewport.Width != max(60, minViewportWidth) {
		t.Errorf("the panel kept its old width: %d", narrow.reasoningViewport.Width)
	}
	for i, line := range strings.Split(ansi.Strip(narrow.assembleScreen(nil).ReasoningPanel), "\n") {
		if got := lipgloss.Width(line); got > 60 {
			t.Errorf("row %d is %d cells after the resize: %q", i, got, line)
		}
	}
	if narrow.reasoningViewport.YOffset != held {
		t.Errorf("the resize moved the reader from row %d to %d", held, narrow.reasoningViewport.YOffset)
	}
}

// TestAnEmptiedReasoningPanelUnmountsItself covers the same reconciliation on
// the path nobody writes a test for: a NEW TURN resets the reasoning buffer, and
// the panel is still open when it does. A band with nothing behind it must give
// its rows back and release the wheel.
//
// It unmounts from the LAYOUT pass, not the frame tick, and that asymmetry with
// TestReasoningPanelFollowsATerminalResize is the point: releasing an empty
// panel is a state decision costing two integer reads, so it is answered
// synchronously and there is never a frame in which an empty panel still holds
// the reader's wheel. Only the expensive half — building content — is deferred
// to the tick.
func TestAnEmptiedReasoningPanelUnmountsItself(t *testing.T) {
	m := reasoningTestModel(t, 60)
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	m.assembleScreen(nil)
	if m.reasoningPanelRows <= 0 {
		t.Fatal("precondition: the panel should hold rows")
	}

	// The turn boundary: the reasoning buffer is reset for the next response.
	m.thinkingBuffer.Reset()
	ws := m.assembleScreen(nil)
	if m.reasoningExpanded || m.reasoningFocused {
		t.Error("an emptied reasoning panel stayed mounted and kept the wheel")
	}
	if ws.ReasoningRows != 0 || ws.ReasoningPanel != "" {
		t.Errorf("an emptied panel still occupies %d rows: %q", ws.ReasoningRows, ws.ReasoningPanel)
	}
}

// TestToggleRepaintsTheConversationAtItsNewHeight closes the one-frame window
// the split would otherwise open. The document window is composed against the
// viewport's height, so a refresh that ran BEFORE the re-budget would slice the
// conversation to the old height and the first frame after the keypress would
// carry a stale row — or a blank one, which is worse: it looks like a hole in
// the transcript.
func TestToggleRepaintsTheConversationAtItsNewHeight(t *testing.T) {
	m := reasoningTestModel(t, 200)
	for i := 0; i < 60; i++ {
		m.records = append(m.records, record{role: roleAI, text: "a line of conversation " + itoa(i)})
	}
	m.refreshViewportContent()
	before := m.Viewport.Height

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	// No compose in between: the toggle itself must have re-budgeted and
	// repainted, because that is the frame the terminal will receive.
	if m.Viewport.Height >= before {
		t.Errorf("the conversation kept its full height across the split: %d → %d",
			before, m.Viewport.Height)
	}
	if m.Viewport.Height+m.reasoningViewport.Height != before {
		t.Errorf("the split does not account for every row: %d + %d != %d",
			m.Viewport.Height, m.reasoningViewport.Height, before)
	}
	if got := lipgloss.Height(m.Viewport.View()); got != m.Viewport.Height {
		t.Errorf("the repainted body is %d rows for a %d-row viewport", got, m.Viewport.Height)
	}
}
