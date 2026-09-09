package tool

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TaskStatus mirrors the plan package lifecycle so tool cards and plan steps
// share one status vocabulary without an import cycle.
type TaskStatus string

const (
	StatusPending TaskStatus = "PENDING"
	StatusRunning TaskStatus = "RUNNING"
	StatusSuccess TaskStatus = "SUCCESS"
	StatusFailed  TaskStatus = "FAILED"
)

// DefaultMaxLines caps the in-memory output buffer.
const DefaultMaxLines = 500

// SpinnerFrames animates RUNNING tool cards, driven by TUI frame ticks.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	toolBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#45475a")).
			Padding(0, 1)

	toolTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	toolDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))
	toolOutStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
	toolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb"))
	toolSuccessStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	toolFailedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f38ba8"))
)

// ToolCard is a collapsible streaming terminal card for one background
// process execution. OutputBuf is append-only; Lines enforces the MaxLines
// ring-buffer window.
type ToolCard struct {
	ID          string
	ToolName    string
	Command     string
	Status      TaskStatus
	OutputBuf   *bytes.Buffer
	lines       []string
	partial     string
	MaxLines    int
	AutoScroll  bool
	IsCollapsed bool
	StartTime   time.Time
	Duration    time.Duration
	ExitCode    int
	ErrMsg      string
}

// BatchCard groups parallel tool cards. Each child owns its own ring buffer,
// so interleaved tool_chunk events can never mix output in the TUI.
type BatchCard struct {
	ID       string
	Tools    []*ToolCard
	Selected int
	Expanded bool
}

func NewBatch(id string, calls []struct{ ID, Name, Command string }) *BatchCard {
	b := &BatchCard{ID: id}
	for _, call := range calls {
		b.Tools = append(b.Tools, New(call.ID, call.Name, call.Command))
	}
	return b
}

func (b *BatchCard) Select(delta int) {
	if b == nil || len(b.Tools) == 0 {
		return
	}
	b.Selected = (b.Selected + delta) % len(b.Tools)
	if b.Selected < 0 {
		b.Selected += len(b.Tools)
	}
}
func (b *BatchCard) SelectedTool() *ToolCard {
	if b == nil || b.Selected < 0 || b.Selected >= len(b.Tools) {
		return nil
	}
	return b.Tools[b.Selected]
}
func (b *BatchCard) ToggleSelected() {
	if b != nil {
		b.Expanded = !b.Expanded
		if t := b.SelectedTool(); t != nil {
			t.Toggle()
		}
	}
}

func (b *BatchCard) Render(frame, width, tailLines int) string {
	if b == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString(toolTitleStyle.Render(fmt.Sprintf("[TOOL BATCH] %d tools", len(b.Tools))))
	for i, t := range b.Tools {
		mark := "  "
		if i == b.Selected {
			mark = "> "
		}
		status := toolDimStyle.Render("[⏳ Running]")
		if t.Status == StatusSuccess {
			status = toolSuccessStyle.Render("[✓ Done]")
		}
		if t.Status == StatusFailed {
			status = toolFailedStyle.Render("[✗ Failed]")
		}
		out.WriteString("\n" + mark + status + " " + t.ToolName + " (" + t.ID + ")")
		if b.Expanded && i == b.Selected {
			out.WriteString("\n" + t.Render(frame, width, tailLines))
		}
	}
	return toolBoxStyle.Render(out.String())
}

// New creates a RUNNING tool card with a fresh buffer.
func New(id, toolName, command string) *ToolCard {
	return &ToolCard{
		ID:         id,
		ToolName:   toolName,
		Command:    command,
		Status:     StatusRunning,
		OutputBuf:  &bytes.Buffer{},
		MaxLines:   DefaultMaxLines,
		AutoScroll: true,
		StartTime:  time.Now(),
	}
}

// Append adds a stdout/stderr chunk, splitting on newlines and enforcing the
// MaxLines ring-buffer window on both lines and OutputBuf.
func (c *ToolCard) Append(chunk []byte) {
	if c == nil || len(chunk) == 0 {
		return
	}
	if c.OutputBuf == nil {
		c.OutputBuf = &bytes.Buffer{}
	}
	if c.MaxLines <= 0 {
		c.MaxLines = DefaultMaxLines
	}
	c.OutputBuf.Write(chunk)
	text := c.partial + string(chunk)
	parts := strings.Split(text, "\n")
	c.partial = parts[len(parts)-1]
	c.lines = append(c.lines, parts[:len(parts)-1]...)
	if overflow := len(c.lines) - c.MaxLines; overflow > 0 {
		c.lines = append([]string(nil), c.lines[overflow:]...)
		c.rebuildBuffer()
	}
}

// Lines returns the buffered output lines (plus a trailing partial line when
// present), capped at MaxLines.
func (c *ToolCard) Lines() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.lines)+1)
	out = append(out, c.lines...)
	if c.partial != "" {
		out = append(out, c.partial)
	}
	if c.MaxLines > 0 && len(out) > c.MaxLines {
		out = out[len(out)-c.MaxLines:]
	}
	return out
}

// LineCount reports the number of buffered lines including a partial tail.
func (c *ToolCard) LineCount() int { return len(c.Lines()) }

// Finish marks process completion, records duration/exit code, and enables
// the auto-collapse behavior for successful runs.
func (c *ToolCard) Finish(exitCode int, errMsg string, duration time.Duration) {
	if c == nil {
		return
	}
	c.ExitCode = exitCode
	c.Duration = duration
	c.ErrMsg = errMsg
	if errMsg != "" || exitCode != 0 {
		c.Status = StatusFailed
	} else {
		c.Status = StatusSuccess
	}
}

// Collapse / Expand / Toggle control the summary-vs-terminal presentation.
func (c *ToolCard) Collapse() { c.IsCollapsed = true }
func (c *ToolCard) Expand()   { c.IsCollapsed = false }
func (c *ToolCard) Toggle()   { c.IsCollapsed = !c.IsCollapsed }

// ShouldAutoCollapse reports whether a finished card should collapse to its
// single-line summary header.
func (c *ToolCard) ShouldAutoCollapse() bool {
	return c != nil && c.Status.IsTerminal()
}

// IsTerminal reports whether the status is terminal.
func (s TaskStatus) IsTerminal() bool {
	return s == StatusSuccess || s == StatusFailed
}

func (c *ToolCard) rebuildBuffer() {
	buf := &bytes.Buffer{}
	for _, ln := range c.lines {
		buf.WriteString(ln)
		buf.WriteByte('\n')
	}
	if c.partial != "" {
		buf.WriteString(c.partial)
	}
	c.OutputBuf = buf
}

// Elapsed reports the live or final duration of the execution.
func (c *ToolCard) Elapsed() time.Duration {
	if c == nil {
		return 0
	}
	if c.Status.IsTerminal() {
		return c.Duration
	}
	if c.StartTime.IsZero() {
		return 0
	}
	return time.Since(c.StartTime)
}

// Summary renders the single-line collapsed header, e.g.
// "✓ Shell command 'go test ./...' completed (1.2s)".
func (c *ToolCard) Summary() string {
	if c == nil {
		return ""
	}
	dur := formatElapsed(c.Elapsed())
	name := c.ToolName
	if name == "" {
		name = "shell"
	}
	cmd := c.Command
	switch c.Status {
	case StatusSuccess:
		return toolSuccessStyle.Render("✓") + " " + name + " command '" + cmd + "' completed (" + dur + ")"
	case StatusFailed:
		detail := fmt.Sprintf("exit %d", c.ExitCode)
		if c.ErrMsg != "" {
			detail = c.ErrMsg
		}
		return toolFailedStyle.Render("✗") + " " + name + " command '" + cmd + "' failed (" + detail + " · " + dur + ")"
	case StatusRunning:
		return toolRunningStyle.Render("⠋") + " " + name + ": " + cmd + " (" + dur + ")"
	default:
		return toolDimStyle.Render("○") + " " + name + ": " + cmd
	}
}

// Render returns the card view. Collapsed cards render the summary line;
// open cards render the terminal box with tail output. frame drives the
// RUNNING spinner; width caps the box; tailLines caps visible output lines
// (<=0 defaults to 12).
func (c *ToolCard) Render(frame, width, tailLines int) string {
	if c == nil {
		return ""
	}
	if c.IsCollapsed && c.Status.IsTerminal() {
		return c.Summary() + " " + toolDimStyle.Render("[tab to expand]")
	}
	if tailLines <= 0 {
		tailLines = 12
	}
	spinner := SpinnerFrames[frame%len(SpinnerFrames)]
	if frame < 0 {
		spinner = SpinnerFrames[0]
	}
	title := c.ToolName
	if title == "" {
		title = "tool"
	}
	head := toolTitleStyle.Render("[TOOL EXEC] " + title + ": " + c.Command)
	var b strings.Builder
	b.WriteString(head)
	lines := c.Lines()
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
		omitted := c.LineCount() - tailLines
		b.WriteString("\n" + toolDimStyle.Render(fmt.Sprintf("… %d earlier lines", omitted)))
	}
	if len(lines) == 0 {
		b.WriteString("\n" + toolDimStyle.Render("(waiting for output…)"))
	} else {
		for _, ln := range lines {
			b.WriteString("\n" + toolOutStyle.Render(truncateLine(ln, 200)))
		}
	}
	footer := ""
	if c.Status.IsTerminal() {
		footer = c.Summary()
	} else {
		footer = toolRunningStyle.Render(spinner + " Running " + formatElapsed(c.Elapsed()))
	}
	b.WriteString("\n" + footer)
	box := toolBoxStyle.Render(b.String())
	if width > 0 && lipgloss.Width(box) > width {
		box = toolBoxStyle.Width(width).Render(b.String())
	}
	return box
}

func truncateLine(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
