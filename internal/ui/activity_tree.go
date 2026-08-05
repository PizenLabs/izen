package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// stagePrefixStyle colors the ":: stage" marker of each execution-tree entry.
var stagePrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCyan))

type EventKind int

const (
	EventFileRead EventKind = iota
	EventFileMutate
	EventCommandExec
	EventSearch
	EventResolve
)

type FileReadEvent struct {
	File    string
	Bytes   int64
	Elapsed time.Duration
}

type FileMutateEvent struct {
	File     string
	LinesAdd int
	LinesDel int
	Elapsed  time.Duration
}

type CommandExecEvent struct {
	Command  string
	ExitCode int
	Elapsed  time.Duration
	// Output accumulates the combined stdout/stderr of the command while it
	// runs. It is streamed live into the entry so Ctrl+O can expand/collapse
	// the output in the viewport without waiting for the process to exit.
	Output string
}

type SearchEvent struct {
	Query string
	Hits  int
}

type ResolveEvent struct {
	Symbol string
	Hits   int
}

type EngineEvent struct {
	Kind EventKind
	Time time.Time

	FileRead    *FileReadEvent
	FileMutate  *FileMutateEvent
	CommandExec *CommandExecEvent
	Search      *SearchEvent
	Resolve     *ResolveEvent
}

func NewFileReadEvent(file string, bytes int64, elapsed time.Duration) EngineEvent {
	return EngineEvent{
		Kind: EventFileRead,
		Time: time.Now(),
		FileRead: &FileReadEvent{
			File:    file,
			Bytes:   bytes,
			Elapsed: elapsed,
		},
	}
}

func NewFileMutateEvent(file string, added, removed int, elapsed time.Duration) EngineEvent {
	return EngineEvent{
		Kind: EventFileMutate,
		Time: time.Now(),
		FileMutate: &FileMutateEvent{
			File:     file,
			LinesAdd: added,
			LinesDel: removed,
			Elapsed:  elapsed,
		},
	}
}

func NewCommandExecEvent(command string, exitCode int, elapsed time.Duration) EngineEvent {
	return EngineEvent{
		Kind: EventCommandExec,
		Time: time.Now(),
		CommandExec: &CommandExecEvent{
			Command:  command,
			ExitCode: exitCode,
			Elapsed:  elapsed,
		},
	}
}

func NewSearchEvent(query string, hits int) EngineEvent {
	return EngineEvent{
		Kind:   EventSearch,
		Time:   time.Now(),
		Search: &SearchEvent{Query: query, Hits: hits},
	}
}

func NewResolveEvent(symbol string, hits int) EngineEvent {
	return EngineEvent{
		Kind:    EventResolve,
		Time:    time.Now(),
		Resolve: &ResolveEvent{Symbol: symbol, Hits: hits},
	}
}

type ActivityTree struct {
	mu      sync.Mutex
	entries []EngineEvent
	// expanded toggles the Ctrl+O expansion of the most recent shell-exec
	// entry so its accumulated stdout/stderr is rendered inline below the
	// "✻ exec │ ..." line in real-time.
	expanded bool
}

func NewActivityTree() *ActivityTree {
	return &ActivityTree{}
}

func (at *ActivityTree) Reset() {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.entries = nil
	at.expanded = false
}

// ToggleExpanded flips the shell-output expansion state and returns the new
// value. The model calls it from the Ctrl+O / Alt+O handler.
func (at *ActivityTree) ToggleExpanded() bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.expanded = !at.expanded
	return at.expanded
}

// Expanded reports the shell-output expansion state.
func (at *ActivityTree) Expanded() bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	return at.expanded
}

// HasCommandExec reports whether the tree's most recent entry is a shell-exec
// step — that entry is the Ctrl+O expansion target. An intervening read/grep
// entry breaks the chain, so only the tail entry counts.
func (at *ActivityTree) HasCommandExec() bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	if len(at.entries) == 0 {
		return false
	}
	return at.entries[len(at.entries)-1].Kind == EventCommandExec
}

// AppendOrUpdateExec records a shell-exec activity. When the most recent
// entry is a RUNNING exec (exitCode < 0) for the same command, the event
// updates it in place (exit code + elapsed on completion, or appended output)
// instead of stacking duplicate lines. Otherwise a fresh entry is appended —
// a second run of the same command after a completed one becomes its own step.
func (at *ActivityTree) AppendOrUpdateExec(cmd string, exitCode int, elapsed time.Duration, output string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if len(at.entries) > 0 {
		last := &at.entries[len(at.entries)-1]
		if last.Kind == EventCommandExec && last.CommandExec != nil &&
			last.CommandExec.ExitCode < 0 && last.CommandExec.Command == cmd {
			if exitCode >= 0 {
				last.CommandExec.ExitCode = exitCode
				last.CommandExec.Elapsed = elapsed
			}
			if output != "" {
				last.CommandExec.Output += output
			}
			return
		}
	}
	at.entries = append(at.entries, EngineEvent{
		Kind: EventCommandExec,
		Time: time.Now(),
		CommandExec: &CommandExecEvent{
			Command:  cmd,
			ExitCode: exitCode,
			Elapsed:  elapsed,
			Output:   output,
		},
	})
}

// AppendExecOutput streams a live stdout/stderr chunk into the most recent
// shell-exec entry (creating a running entry when none exists). Used by the
// streaming shell pipeline so output appears in the viewport as it arrives.
func (at *ActivityTree) AppendExecOutput(chunk string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if chunk == "" {
		return
	}
	if len(at.entries) > 0 {
		last := &at.entries[len(at.entries)-1]
		if last.Kind == EventCommandExec && last.CommandExec != nil {
			last.CommandExec.Output += chunk
			return
		}
	}
	at.entries = append(at.entries, EngineEvent{
		Kind: EventCommandExec,
		Time: time.Now(),
		CommandExec: &CommandExecEvent{
			ExitCode: -1, // running sentinel
			Output:   chunk,
		},
	})
}

// CompleteLastExec finalizes the most recent shell-exec entry with its exit
// code and elapsed time (the streaming shell's terminal event handler).
func (at *ActivityTree) CompleteLastExec(exitCode int, elapsed time.Duration) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if len(at.entries) == 0 {
		return
	}
	last := &at.entries[len(at.entries)-1]
	if last.Kind == EventCommandExec && last.CommandExec != nil {
		last.CommandExec.ExitCode = exitCode
		last.CommandExec.Elapsed = elapsed
	}
}

func (at *ActivityTree) Append(ev EngineEvent) {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.entries = append(at.entries, ev)
}

func (at *ActivityTree) Entries() []EngineEvent {
	at.mu.Lock()
	defer at.mu.Unlock()
	r := make([]EngineEvent, len(at.entries))
	copy(r, at.entries)
	return r
}

func (at *ActivityTree) Len() int {
	at.mu.Lock()
	defer at.mu.Unlock()
	return len(at.entries)
}

// Render produces the full execution tree view. Completed entries carry a
// "[done]" badge; when active=true the last entry is treated as the
// in-flight stage and gets a "[running]" badge with animated dots so the
// viewer can tell exactly where the pipeline stands.
func (at *ActivityTree) Render(width int) string {
	return at.RenderActive(width, false, 0)
}

// RenderActive renders the tree with a live "[running]" badge on the last
// entry while active is true. frame is the live spinner frame (advanced by the
// shimmer / smooth tick loops): its low bits drive the truncation dots and the
// 4-frame animated snowflake exec icon, so a running shell visibly cycles
// ✻ → ❅ → ❆ → ✦. When the tree is expanded (Ctrl+O) the most recent shell-exec
// entry's accumulated output is rendered inline below its command line.
func (at *ActivityTree) RenderActive(width int, active bool, frame int) string {
	at.mu.Lock()
	entries := make([]EngineEvent, len(at.entries))
	copy(entries, at.entries)
	expanded := at.expanded
	at.mu.Unlock()

	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	for i, ev := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		running := active && i == len(entries)-1
		b.WriteString(at.renderEvent(ev, width, running, frame, expanded))
	}
	return dimmedStyle.Render(b.String())
}

// execSpinnerFrames is the 4-frame animated snowflake cycle used as the live
// shell-exec icon. It mirrors flowingSpinnerFrames but without the surrounding
// spaces so the glyph sits flush against the " exec │ " stage label.
var execSpinnerFrames = []string{"✻", "❅", "❆", "✦"}

// stageLabel maps an event kind to its tool stage name, rendered as the text
// after the dedicated activity icon (Claude Code / OpenCode style).
func stageLabel(kind EventKind) string {
	switch kind {
	case EventFileRead:
		return "read"
	case EventFileMutate:
		return "diff"
	case EventCommandExec:
		return "exec"
	case EventSearch:
		return "grep"
	case EventResolve:
		return "resolve"
	default:
		return "step"
	}
}

// badge renders the trailing [running] / [done] status marker.
func stageBadge(running bool, dotFrame int) string {
	if running {
		return orangeStyle.Render(fmt.Sprintf("[running%s]", animatedDots(dotFrame)))
	}
	return greenStyle.Render("[done]")
}

func (at *ActivityTree) renderEvent(ev EngineEvent, width int, running bool, frame int, expanded bool) string {
	// The running badge dots cycle every 3 frames; the snowflake exec icon
	// cycles every 4 (✻ ❅ ❆ ✦) so a full rotation is visible.
	dotFrame := frame % 3
	execFrame := frame % len(execSpinnerFrames)

	// Reserve cell budget for the "<icon> <stage> │ " prefix and the trailing
	// status badge so long paths/commands truncate cleanly instead of
	// overflowing the viewport's right border.
	prefixW := 14
	badgeW := 9
	contentW := width - prefixW - badgeW
	if contentW < 20 {
		contentW = 20
	}

	prefix := func(icon, stage string, iconStyle lipgloss.Style) string {
		return iconStyle.Render(icon) + " " + stagePrefixStyle.Render(stage) + mutedStyle.Render(" │ ")
	}

	switch ev.Kind {
	case EventFileRead:
		e := ev.FileRead
		if e == nil {
			return ""
		}
		elapsed := formatElapsed(e.Elapsed)
		return fmt.Sprintf("%s%s (%d B · %s) %s",
			prefix(Icon.Read, stageLabel(ev.Kind), blueStyle),
			truncateMiddle(e.File, contentW), e.Bytes, mutedStyle.Render(elapsed), stageBadge(running, dotFrame))

	case EventFileMutate:
		e := ev.FileMutate
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%s%s (+%d / -%d lines) %s",
			prefix(Icon.Diff, stageLabel(ev.Kind), orangeStyle),
			truncateMiddle(e.File, contentW), e.LinesAdd, e.LinesDel, stageBadge(running, dotFrame))

	case EventCommandExec:
		e := ev.CommandExec
		if e == nil {
			return ""
		}
		// The exec icon is the animated snowflake while the command is still
		// running; a static snowflake marks the completed entry.
		icon := Icon.Exec
		iconStyle := mutedStyle
		if running {
			icon = execSpinnerFrames[execFrame]
			iconStyle = orangeStyle
		}

		var b strings.Builder
		b.WriteString(prefix(icon, stageLabel(ev.Kind), iconStyle))
		b.WriteString(yellowStyle.Render(truncateMiddle(e.Command, contentW)))
		if !running {
			exitStr := fmt.Sprintf("exit %d", e.ExitCode)
			if e.ExitCode == 0 {
				exitStr = greenStyle.Render("exit 0")
			} else {
				exitStr = redStyle.Render(exitStr)
			}
			fmt.Fprintf(&b, " (%s · %s) %s",
				exitStr, mutedStyle.Render(formatElapsed(e.Elapsed)), stageBadge(running, dotFrame))
		} else {
			b.WriteString(" " + stageBadge(running, dotFrame))
		}

		// Ctrl+O expanded shell output: render the accumulated stdout/stderr
		// inline, muted and indented, so streaming output is inspectable live.
		if expanded && strings.TrimSpace(e.Output) != "" {
			outW := width - 8
			if outW < 10 {
				outW = 10
			}
			for _, line := range strings.Split(strings.TrimRight(e.Output, "\r\n"), "\n") {
				if strings.TrimSpace(line) == "" {
					b.WriteString("\n  ")
					continue
				}
				b.WriteString("\n  " + mutedStyle.Render("│ ") + truncateMiddle(line, outW))
			}
		}
		return b.String()

	case EventSearch:
		e := ev.Search
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%s%s (%d hits) %s",
			prefix(Icon.Grep, stageLabel(ev.Kind), cyanStyle),
			truncateMiddle(e.Query, contentW), e.Hits, stageBadge(running, dotFrame))

	case EventResolve:
		e := ev.Resolve
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%s%s (%d hits) %s",
			prefix(Icon.Grep, stageLabel(ev.Kind), cyanStyle),
			truncateMiddle(e.Symbol, contentW), e.Hits, stageBadge(running, dotFrame))

	default:
		return ""
	}
}

// truncateMiddle shortens a long string to at most maxW visual cells, keeping
// the head and a short tail separated by an ellipsis so identity stays
// recognizable. Strings already within the budget are returned unchanged.
func truncateMiddle(s string, maxW int) string {
	if maxW < 8 {
		maxW = 8
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	runes := []rune(s)
	// Reserve room for the ellipsis and a short identity tail; the head and
	// tail together never exceed the cell budget.
	tail := 3
	head := maxW - 3 - tail
	if head < 2 {
		head = 2
	}
	if head+3+tail > maxW {
		tail = maxW - 3 - head
		if tail < 1 {
			tail = 1
		}
	}
	// Build head greedily up to the cell budget; the tail is the final cells.
	headS := ""
	headW := 0
	for _, r := range runes {
		w := runewidth.RuneWidth(r)
		if headW+w > head {
			break
		}
		headS += string(r)
		headW += w
	}
	tailS := ""
	tailW := 0
	for i := len(runes) - 1; i >= 0 && tailW+runewidth.RuneWidth(runes[i]) <= tail; i-- {
		tailS = string(runes[i]) + tailS
		tailW += runewidth.RuneWidth(runes[i])
	}
	return headS + "..." + tailS
}

func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		secs := d.Seconds()
		return fmt.Sprintf("%.1fs", secs)
	default:
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", mins, secs)
	}
}

type StreamThrottle struct {
	buffer    strings.Builder
	lastFlush time.Time
	minDelay  time.Duration
	maxChunk  int
}

func NewStreamThrottle() *StreamThrottle {
	return &StreamThrottle{
		minDelay: 16 * time.Millisecond,
		maxChunk: 80,
	}
}

func (st *StreamThrottle) Write(chunk string) {
	st.buffer.WriteString(chunk)
}

func (st *StreamThrottle) Flush() (string, bool) {
	if st.buffer.Len() == 0 {
		return "", false
	}

	elapsed := time.Since(st.lastFlush)
	if elapsed < st.minDelay && st.buffer.Len() < st.maxChunk {
		return "", false
	}

	content := st.buffer.String()
	st.lastFlush = time.Now()

	emit := st.maxChunk
	if len(content) < emit {
		emit = len(content)
	}

	for emit > 0 && content[emit-1] != ' ' && content[emit-1] != '\n' {
		emit--
	}
	if emit == 0 {
		emit = st.maxChunk
		if emit > len(content) {
			emit = len(content)
		}
	}

	// PRESERVE THE REMAINDER: only the emitted window leaves the buffer.
	// Anything beyond it stays buffered for the next Flush. Historically this
	// Reset() was called before slicing, so the tail of every flush window was
	// permanently dropped and long responses rendered truncated — the
	// "text missing mid-answer" bug. Content is never discarded now.
	st.buffer.Reset()
	if emit < len(content) {
		st.buffer.WriteString(content[emit:])
	}

	return content[:emit], true
}

// Drain returns the entire remaining buffer and empties the throttle,
// bypassing the frame-delay gate. It is called once at stream end so residual
// content that the tick loop never flushed is still delivered, never dropped.
func (st *StreamThrottle) Drain() string {
	content := st.buffer.String()
	st.buffer.Reset()
	st.lastFlush = time.Now()
	return content
}

func (st *StreamThrottle) Reset() {
	st.buffer.Reset()
}

func (st *StreamThrottle) Len() int {
	return st.buffer.Len()
}
