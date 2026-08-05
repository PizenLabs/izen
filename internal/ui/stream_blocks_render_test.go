package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/PizenLabs/izen/internal/modes"
)

// TestStreamThinkingStyleIsDimmed guards the differential-styling contract: the
// thinking style used for KindThinking blocks MUST be Faint + Italic so
// reasoning is visually subordinate to the bright content style.
func TestStreamThinkingStyleIsDimmed(t *testing.T) {
	if !streamThinkingStyle.GetFaint() || !streamThinkingStyle.GetItalic() {
		t.Errorf("streamThinkingStyle = faint=%v italic=%v, want both true",
			streamThinkingStyle.GetFaint(), streamThinkingStyle.GetItalic())
	}
	if streamThinkingStyle.GetForeground() == brightStyle.GetForeground() {
		t.Error("thinking and content styles must use different foreground colors")
	}
	if brightStyle.GetFaint() || brightStyle.GetItalic() {
		t.Error("brightStyle must NOT be faint/italic — content streams crisp")
	}
}

// TestRenderStreamBlocksDifferentialStyling verifies that a mixed typed buffer
// renders thinking dimmed and content bright, and that both survive verbatim
// (ANSI-stripped) with no cross-contamination.
func TestRenderStreamBlocksDifferentialStyling(t *testing.T) {
	m := &model{width: 80, resolver: modes.NewResolver()}
	b := NewStreamBuffer()
	b.Append(KindThinking, "analyzing the call graph")
	b.Append(KindContent, "Here is the answer.")
	m.streamBlocks = b

	// Force a true-color profile so lipgloss emits real ANSI SGR attributes
	// (tests otherwise render plain text on a non-TTY). Restored after.
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)

	out := m.renderStreamBlocks(m.width)

	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "analyzing the call graph") {
		t.Errorf("thinking text missing from rendered stream:\n%s", stripped)
	}
	if !strings.Contains(stripped, "Here is the answer.") {
		t.Errorf("content text missing from rendered stream:\n%s", stripped)
	}

	// The thinking block must carry lipgloss's faint+italic SGR prefix
	// (\x1b[3;2;…) while the content block renders with the bright text color
	// (\x1b[38;2;205;214;243m) and no faint attribute.
	if !strings.Contains(out, "\x1b[3;2") {
		t.Errorf("thinking block did not render faint+italic:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;205;214;243m") {
		t.Errorf("content block did not render in the bright text color:\n%q", out)
	}
	// Sanity: the thinking text must stream before the content text — the
	// blocks render in arrival order, never merged.
	thinkIdx := strings.Index(out, "analyzing the call graph")
	contentIdx := strings.Index(out, "Here is the answer.")
	if thinkIdx < 0 || contentIdx < 0 || thinkIdx > contentIdx {
		t.Errorf("thinking/content blocks merged or reordered:\n%q", out)
	}
}

// TestRenderStreamBlocksEmpty returns "" for an empty buffer so the caller can
// fall back to the legacy content path.
func TestRenderStreamBlocksEmpty(t *testing.T) {
	m := &model{width: 80, resolver: modes.NewResolver()}
	if got := m.renderStreamBlocks(m.width); got != "" {
		t.Errorf("empty buffer rendered %q", got)
	}
}

// TestThinkingTokenMsgRoutesToStreamBlocks verifies the typed thinking message
// handler appends a KindThinking block (never content) and triggers a refresh.
func TestThinkingTokenMsgRoutesToStreamBlocks(t *testing.T) {
	m := newTestModel()
	m.streaming = true
	nm, _ := m.Update(thinkingTokenMsg("first pass"))
	m2 := nm.(*model)

	if m2.streamBlocks == nil || !m2.streamBlocks.HasThinking() {
		t.Fatal("thinking token did not create a thinking block")
	}
	if got := m2.streamBlocks.Thinking(); got != "first pass" {
		t.Errorf("Thinking() = %q", got)
	}
	if m2.streamBlocks.HasContent() {
		t.Error("thinking token leaked into the content pipeline")
	}
}

// TestStreamBufferEndToEndMixedStream drives a thinking-then-content-then-
// thinking sequence through the model's streaming handlers and verifies the
// blocks land in the correct order with correct kinds. Content arrives via the
// emission pipeline (the throttle-tick path), thinking via the typed message.
func TestStreamBufferEndToEndMixedStream(t *testing.T) {
	m := newTestModel()
	m.streaming = true

	nm, _ := m.Update(thinkingTokenMsg("step one"))
	m = nm.(*model)
	nm, _ = m.Update(thinkingTokenMsg(" step two"))
	m = nm.(*model)

	m.emitVisibleContent("Answer begins ")
	m.emitVisibleContent("and continues.")

	// A late reasoning block (some models re-think mid-stream) must start a
	// new KindThinking block rather than merge into the content.
	nm, _ = m.Update(thinkingTokenMsg(" final check"))
	m = nm.(*model)

	blocks := m.streamBlocks.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != KindThinking || blocks[0].Text != "step one step two" {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Kind != KindContent || blocks[1].Text != "Answer begins and continues." {
		t.Errorf("block 1 = %+v", blocks[1])
	}
	if blocks[2].Kind != KindThinking || blocks[2].Text != " final check" {
		t.Errorf("block 2 = %+v, want a trailing thinking block", blocks[2])
	}
	if m.currentStreamContent != "Answer begins and continues." {
		t.Errorf("currentStreamContent = %q, want content only", m.currentStreamContent)
	}
}
