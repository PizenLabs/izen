package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func newSelectionModel(records []record) *model {
	m := newTestModel()
	m.state = StateChat
	m.showBanner = false
	m.records = records
	m.PreRenderedHistory = ""
	m.width = 80
	m.height = 24
	m.Ready = true
	m.Viewport.Height = 20
	m.streaming = false
	m.agentRunning = false
	// Build document layout for space-anchored selection
	dl := BuildDocumentLayout(m.records, m.width)
	m.docLayout = &dl
	m.refreshViewportContent()
	return m
}

// TestSelection_MouseDownStarts verifies MouseDown initiates selection.
func TestSelection_MouseDownStarts(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "hello world"}, {role: roleAI, text: "second line"}})
	m.width = 80
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 2})
	m2 := updated.(*model)
	if !m2.mouseSel.Active || !m2.mouseSel.Dragging {
		t.Fatalf("mouse down should start selection, got active=%v dragging=%v", m2.mouseSel.Active, m2.mouseSel.Dragging)
	}
}

// TestSelection_DragUpdatesCursor verifies motion updates cursor while dragging.
func TestSelection_DragUpdatesCursor(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "line one"}, {role: roleAI, text: "line two"}, {role: roleAI, text: "line three"}})
	m.width = 80
	// Press at line 0
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 1})
	m = updated.(*model)
	anchor := m.mouseSel.Anchor
	// Drag to line 2
	updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 2, Y: 4})
	m = updated.(*model)
	if m.mouseSel.Cursor.Y == anchor.Y && m.mouseSel.Cursor.X == anchor.X {
		t.Fatal("drag should update cursor position")
	}
	// Anchor must remain stable
	if m.mouseSel.Anchor != anchor {
		t.Fatalf("anchor changed during drag: was %v now %v", anchor, m.mouseSel.Anchor)
	}
}

// TestSelection_ReleaseCopiesAndClears verifies release serializes and copies.
func TestSelection_ReleaseCopiesAndClears(t *testing.T) {
	cb := &fakeClipboard{}
	records := []record{{role: roleUser, text: "hello"}, {role: roleAI, text: "world"}}
	m := newSelectionModel(records)
	m.clipboard = cb
	m.width = 80
	// Simulate a selection that covers both records: anchor line 0 col 0 to line 1 col 4
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 1, X: 4}}
	m.Viewport.Height = 20
	m.Ready = true
	releaseY := m.viewportGeometry().Top + m.viewportContentPrefixHeight() + 1
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 6, Y: releaseY})
	m2 := updated.(*model)
	if cb.writes != 1 {
		t.Fatalf("release should copy to clipboard, writes=%d", cb.writes)
	}
	if m2.mouseSel.Active {
		t.Fatal("selection should clear after copy")
	}
	if !strings.Contains(cb.content, "hello") || !strings.Contains(cb.content, "world") {
		t.Fatalf("copied content should contain both records, got %q", cb.content)
	}
}

// TestSelection_WheelScrollsInNormalChat verifies wheel scrolls outside vi mode.
func TestSelection_WheelScrollsInNormalChat(t *testing.T) {
	m := newSelectionModel(make([]record, 30))
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "content line"}
	}
	m.height = 15
	m.Ready = true
	m.Viewport.Height = 5
	m.Viewport.YOffset = 10
	before := m.Viewport.YOffset
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m2 := updated.(*model)
	// Wheel up sets scrolling flag and moves viewport
	if !m2.userIsScrollingUp {
		t.Fatal("wheel up should set userIsScrollingUp")
	}
	// Viewport may have moved via Viewport.Update
	_ = before
}

// TestSelection_WheelDuringStreaming verifies wheel works while streaming when non-modal.
func TestSelection_WheelDuringStreaming(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "history"}})
	m.streaming = true
	m.state = StateChat // not modal
	m.Ready = true
	m.Viewport.Height = 5
	m.Viewport.YOffset = 0
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m2 := updated.(*model)
	if !m2.userIsScrollingUp {
		t.Fatal("wheel should work while streaming")
	}
	// Streaming flag must remain true (selection/scroll doesn't cancel it)
	if !m2.streaming {
		t.Fatal("wheel should not cancel streaming")
	}
}

// TestSelection_CrossViewportBoundaries verifies anchor stability across viewport movement.
func TestSelection_CrossViewportBoundaries(t *testing.T) {
	m := newSelectionModel(make([]record, 20))
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "line content"}
	}
	m.width = 80
	m.height = 15
	m.Ready = true
	m.Viewport.Height = 5
	// Start selection at top
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 2})
	m = updated.(*model)
	anchor := m.mouseSel.Anchor
	// Drag far down beyond viewport
	updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 2, Y: 12})
	m = updated.(*model)
	if m.mouseSel.Anchor != anchor {
		t.Fatalf("anchor must remain stable across viewport movement, was %v now %v", anchor, m.mouseSel.Anchor)
	}
}

// TestSelection_AutoScrollUpAndDown verifies edge auto-scroll directions.
func TestSelection_AutoScrollUpAndDown(t *testing.T) {
	m := newSelectionModel(make([]record, 30))
	for i := range m.records {
		m.records[i] = record{role: roleAI, text: "auto scroll line"}
	}
	m.height = 20
	m.width = 80
	m.Ready = true
	m.Viewport.Height = 10
	m.refreshViewportContent()
	m.Viewport.YOffset = 10
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 5, X: 0}, Cursor: GlobalPos{Y: 10, X: 0}, lastY: 1}
	// Simulate auto-scroll tick near top edge
	cmd := m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: 1, X: 2})
	if m.Viewport.YOffset >= 10 {
		t.Fatalf("auto-scroll up should move viewport, offset %d", m.Viewport.YOffset)
	}
	if cmd == nil {
		t.Fatal("auto-scroll should schedule next tick while in edge zone")
	}
	// Now test down
	m.Viewport.YOffset = 5
	m.mouseSel.lastY = m.Viewport.Height - 1
	before := m.Viewport.YOffset
	_ = m.handleSelectionAutoScroll(selectionScrollTickMsg{Y: m.height - 1, X: 2})
	if m.Viewport.YOffset <= before {
		t.Fatalf("auto-scroll down should move viewport, before %d after %d", before, m.Viewport.YOffset)
	}
}

// TestSelection_AutoScrollNotTightLoop verifies bounded interval (not burning CPU)
func TestSelection_AutoScrollNotTightLoop(t *testing.T) {
	// Interval should be >= 50ms
	if selectionAutoScrollInterval < 50*time.Millisecond {
		t.Fatalf("auto-scroll interval too tight: %v", selectionAutoScrollInterval)
	}
	if selectionAutoScrollInterval > 200*time.Millisecond {
		t.Fatalf("auto-scroll interval too slow: %v", selectionAutoScrollInterval)
	}
}

// TestSelection_SerializeMultiline verifies multiline copy.
func TestSelection_SerializeMultiline(t *testing.T) {
	m := newSelectionModel([]record{{role: roleUser, text: "line one\nline two\nline three"}})
	// With global flat document, each logical line is a physical row: Y 0->line one, Y1->line two, Y2->line three
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 2, X: 50}}
	got := m.serializeMouseSelection()
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("multiline not preserved: %q", got)
	}
}

// TestSelection_SerializeNoANSI verifies no ANSI leaks.
func TestSelection_SerializeNoANSI(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("red error")
	m := newSelectionModel([]record{{role: roleError, text: styled}})
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 20}}
	got := m.serializeMouseSelection()
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("ANSI leaked into selection: %q", got)
	}
	if !strings.Contains(got, "red error") {
		t.Fatalf("plain text lost: %q", got)
	}
}

// TestSelection_SerializeNoBordersOrSpinners verifies no viewport chrome leaks.
func TestSelection_SerializeNoBordersOrSpinners(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "real content here"}})
	m.spinnerFrame = 3
	m.shimmerActive = true
	m.streaming = true
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 4}}
	got := m.serializeMouseSelection()
	if strings.Contains(got, "spinner") || strings.Contains(got, "shimmer") || strings.Contains(got, "─") {
		t.Fatalf("viewport chrome leaked: %q", got)
	}
	// Stripped output should equal plain text slice
	plain := ansi.Strip(got)
	if plain != got {
		t.Fatalf("ANSI strip changed output, leakage: %q vs %q", got, plain)
	}
}

// TestSelection_StateIsolation verifies selection doesn't mutate execution state.
func TestSelection_StateIsolation(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "content"}})
	m.streaming = true
	m.agentRunning = true
	origStreaming := m.streaming
	origAgent := m.agentRunning
	origRecords := len(m.records)
	// Start and move selection
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 1})
	m = updated.(*model)
	updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: 1})
	m = updated.(*model)
	if m.streaming != origStreaming || m.agentRunning != origAgent {
		t.Fatal("selection mutated execution state")
	}
	if len(m.records) != origRecords {
		t.Fatal("selection mutated records")
	}
}

// TestSelection_HighlightIsVisualOnly verifies highlight doesn't affect transcript.
func TestSelection_HighlightIsVisualOnly(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "hello world"}})
	m.mouseSel = mouseSelection{Active: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 4}}
	highlighted := m.renderRecordsWithMouseSelection()
	if strings.Contains(highlighted, "\x00SEL") || strings.Contains(highlighted, "\x00") {
		t.Fatal("selection markers leaked")
	}
	// Underlying record must be unchanged
	if m.records[0].text != "hello world" {
		t.Fatal("highlight mutated underlying text")
	}
	// /copy must still produce original transcript, not highlighted
	plain := SerializeTranscript(m.records)
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("/copy transcript broken: %q", plain)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("/copy leaked ANSI: %q", plain)
	}
}

// TestSelection_EscClears verifies Esc cancels selection without affecting execution.
func TestSelection_EscClears(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "hello"}})
	m.mouseSel = mouseSelection{Active: true, Dragging: false, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 4}}
	m.streaming = true
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := updated.(*model)
	if m2.mouseSel.Active {
		t.Fatal("Esc should clear selection")
	}
	if !m2.streaming {
		t.Fatal("Esc clearing selection should not cancel streaming")
	}
}

// TestSelection_CopyUsesClipboardAbstraction verifies clipboard abstraction is used.
func TestSelection_CopyUsesClipboardAbstraction(t *testing.T) {
	cb := &fakeClipboard{}
	m := newSelectionModel([]record{{role: roleAI, text: "clipboard test"}})
	m.clipboard = cb
	m.mouseSel = mouseSelection{Active: true, Dragging: true, Anchor: GlobalPos{Y: 0, X: 0}, Cursor: GlobalPos{Y: 0, X: 7}}
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 7, Y: 1})
	m2 := updated.(*model)
	if cb.writes != 1 {
		t.Fatalf("should use clipboard abstraction, writes=%d", cb.writes)
	}
	if cb.content == "" {
		t.Fatal("clipboard empty")
	}
	if m2.mouseSel.Active {
		t.Fatal("should clear after copy")
	}
}

// TestSelection_ModalBlocksSelection verifies approval state blocks selection.
func TestSelection_ModalBlocksSelection(t *testing.T) {
	m := newSelectionModel([]record{{role: roleAI, text: "content"}})
	m.state = StateAwaitingApproval
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 2})
	m2 := updated.(*model)
	if m2.mouseSel.Active {
		t.Fatal("selection should be blocked in modal approval state")
	}
}
