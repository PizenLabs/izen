package tool

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRingBufferEnforcesMaxLines streams 1,000 log lines and asserts the
// MaxLines window holds without unbounded growth.
func TestRingBufferEnforcesMaxLines(t *testing.T) {
	c := New("t1", "shell", "go test -v ./...")
	c.MaxLines = 500

	var chunk strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&chunk, "line %04d\n", i)
	}
	c.Append([]byte(chunk.String()))

	if got := c.LineCount(); got != 500 {
		t.Fatalf("LineCount = %d, want 500", got)
	}
	lines := c.Lines()
	if !strings.Contains(lines[0], "line 0500") {
		t.Fatalf("oldest retained line = %q, want line 0500 window", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "line 0999") {
		t.Fatalf("newest retained line = %q, want line 0999", lines[len(lines)-1])
	}
	if c.OutputBuf == nil || c.OutputBuf.Len() == 0 {
		t.Fatal("OutputBuf must retain the windowed output")
	}
	// Buffer must stay proportional to the window, not the full stream.
	if c.OutputBuf.Len() > 64*1024 {
		t.Fatalf("OutputBuf grew unbounded: %d bytes", c.OutputBuf.Len())
	}
}

// TestChunkedAppendsPreservePartialLines verifies chunks split mid-line are
// reassembled and the window still holds.
func TestChunkedAppendsPreservePartialLines(t *testing.T) {
	c := New("t1", "shell", "npm test")
	c.MaxLines = 10
	c.Append([]byte("hel"))
	c.Append([]byte("lo\nworld"))
	lines := c.Lines()
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world" {
		t.Fatalf("Lines = %q, want [hello world]", lines)
	}
}

// TestAutoCollapseOnFinish verifies completion collapses to the summary
// header and Tab toggles expansion back.
func TestAutoCollapseOnFinish(t *testing.T) {
	c := New("t1", "shell", "go test -v ./...")
	c.Append([]byte("=== RUN   TestX\n--- PASS: TestX (0.12s)\n"))

	if c.ShouldAutoCollapse() {
		t.Fatal("running card must not auto-collapse")
	}
	c.Finish(0, "", 1200*time.Millisecond)
	if c.Status != StatusSuccess {
		t.Fatalf("Status = %s, want SUCCESS", c.Status)
	}
	if !c.ShouldAutoCollapse() {
		t.Fatal("finished card must request auto-collapse")
	}

	// Simulate the delayed toolCollapseMsg.
	c.Collapse()
	if !c.IsCollapsed {
		t.Fatal("Collapse must set IsCollapsed")
	}
	rendered := c.Render(0, 120, 12)
	if !strings.Contains(rendered, "completed") || !strings.Contains(rendered, "go test -v ./...") {
		t.Fatalf("collapsed render must be the summary header, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "=== RUN") {
		t.Fatalf("collapsed render must hide output, got:\n%s", rendered)
	}

	// Tab toggles expansion of the historical log.
	c.Toggle()
	if c.IsCollapsed {
		t.Fatal("Toggle must expand the card")
	}
	rendered = c.Render(0, 120, 12)
	if !strings.Contains(rendered, "=== RUN") {
		t.Fatalf("expanded render must show output, got:\n%s", rendered)
	}
}

// TestFailedFinishRendersError verifies failure summaries carry the detail.
func TestFailedFinishRendersError(t *testing.T) {
	c := New("t1", "shell", "go build ./...")
	c.Finish(1, "exit code 1", 300*time.Millisecond)
	if c.Status != StatusFailed {
		t.Fatalf("Status = %s, want FAILED", c.Status)
	}
	if got := c.Summary(); !strings.Contains(got, "failed") {
		t.Fatalf("failure summary = %q, want failure detail", got)
	}
}

// TestActiveRenderAutoScrollTail verifies open cards show the tail window
// with an omission marker while streaming.
func TestActiveRenderAutoScrollTail(t *testing.T) {
	c := New("t1", "shell", "git status")
	for i := 0; i < 30; i++ {
		c.Append([]byte(fmt.Sprintf("out %d\n", i)))
	}
	rendered := c.Render(2, 120, 12)
	if !strings.Contains(rendered, "TOOL EXEC") || !strings.Contains(rendered, "Running") {
		t.Fatalf("active render must show terminal chrome, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "earlier lines") {
		t.Fatalf("active render must mark omitted head lines, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "out 29") {
		t.Fatalf("active render must pin the tail, got:\n%s", rendered)
	}
}
