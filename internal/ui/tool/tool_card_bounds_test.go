package tool

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// widths under test: a normal pane, a split pane, and the narrow end of the
// range a terminal can actually be resized to.
var boundWidths = []int{0, 20, 40, 60, 80, 100, 120, 200}

// widest reports the largest cell count across the rows of a rendered card.
func widest(rendered string) int {
	max := 0
	for _, line := range strings.Split(rendered, "\n") {
		if w := ansi.StringWidth(line); w > max {
			max = w
		}
	}
	return max
}

// longCommand is the payload that makes a tool card overrun in practice: a
// one-line curl invocation with a full URL, which is exactly what a user pastes
// into a tool call. The command is rendered UNTRUNCATED in the card header, so
// it is the natural trigger for a frame wider than the terminal.
const longCommand = "curl -sS -X POST https://api.openrouter.ai/api/v1/chat/completions " +
	"-H 'Authorization: Bearer sk-or-v1-0000000000000000000000000000000000000000000000000000000000' " +
	"-H 'Content-Type: application/json' --data '{\"model\":\"anthropic/claude-opus-4\",\"messages\":[{\"role\":\"user\",\"content\":\"summarise this repository\"}]}'"

func newWideCard() *ToolCard {
	c := New("id-1", "shell", longCommand)
	c.OutputBuf.WriteString(strings.Repeat("x", 400) + "\n")
	for i := 0; i < 30; i++ {
		c.OutputBuf.WriteString("a-fairly-long-line-of-tool-output-number-" + strings.Repeat("y", 120) + "\n")
	}
	return c
}

// TestToolCardNeverExceedsItsWidth is the bounding regression. The guard used to
// re-render at `Width(width)`, which — because lipgloss adds the border AFTER
// the content box — still rendered `width + 2` cells. The right border landed
// two cells past the viewport edge and the terminal wrapped, corrupting every
// line below the card rather than just this one.
func TestToolCardNeverExceedsItsWidth(t *testing.T) {
	c := newWideCard()
	for _, w := range boundWidths {
		box := c.Render(0, w, 12)
		if w <= 0 {
			// Non-positive width is the unconstrained pre-bootstrap case.
			continue
		}
		if got := widest(box); got > w {
			t.Errorf("width %d: card is %d cells wide\n%s", w, got, box)
		}
	}
}

// TestToolCardBorderIsIntact is the paired visual assertion: a card that fits is
// useless if the fit came from losing the border. The left and right edges must
// survive on every content row (the top and bottom rules carry ┌┐/└┘ corners
// instead, so they are checked separately).
func TestToolCardBorderIsIntact(t *testing.T) {
	c := newWideCard()
	for _, w := range []int{40, 80, 120} {
		box := c.Render(0, w, 12)
		plain := ansi.Strip(box)
		if !strings.HasPrefix(plain, "╭") {
			t.Errorf("width %d: missing top-left corner\n%s", w, plain)
		}
		if !strings.HasSuffix(plain, "╯") {
			t.Errorf("width %d: missing bottom-right corner\n%s", w, plain)
		}
		rows := strings.Split(plain, "\n")
		for i, line := range rows {
			if i == 0 || i == len(rows)-1 {
				continue // the rules, already checked via the corners
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
				t.Errorf("width %d: content row is missing a vertical border edge: %q", w, line)
			}
		}
	}
}

// TestBatchCardNeverExceedsItsWidth covers the frame that had NO width bound at
// all. The batch card nests a framed child card when expanded, so the outer
// frame was sized to its widest content — which included the child's own border
// — and ran `width + 6` cells wide.
func TestBatchCardNeverExceedsItsWidth(t *testing.T) {
	batch := NewBatch("batch-1", []struct{ ID, Name, Command string }{
		{ID: "a", Name: "shell", Command: longCommand},
		{ID: "b", Name: "read", Command: longCommand},
	})
	child := batch.Tools[0]
	child.OutputBuf.WriteString(strings.Repeat("z", 500) + "\n")
	batch.ToggleSelected() // expand the selected child

	for _, w := range boundWidths {
		if w <= 0 {
			continue
		}
		box := batch.Render(0, w, 12)
		if got := widest(box); got > w {
			t.Errorf("width %d: batch card is %d cells wide\n%s", w, got, box)
		}
	}
}

// TestCollapsedCardRendersNoFrame pins the shape that keeps the common case
// cheap: a collapsed terminal card is a one-line summary, not a frame. Framing
// a single summary row costs four cells of chrome, and a card that renders a
// frame is a card whose width has to be bounded on every path.
func TestCollapsedCardRendersNoFrame(t *testing.T) {
	c := New("id-1", "shell", "ls")
	c.Status = StatusSuccess
	c.ExitCode = 0
	c.IsCollapsed = true
	summary := ansi.Strip(c.Render(0, 40, 12))
	if strings.Contains(summary, "╭") {
		t.Errorf("a collapsed terminal card drew a frame: %q", summary)
	}
	if !strings.Contains(summary, "[tab to expand]") {
		t.Errorf("the collapsed card lost its expand affordance: %q", summary)
	}
}
