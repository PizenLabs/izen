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
}

func NewActivityTree() *ActivityTree {
	return &ActivityTree{}
}

func (at *ActivityTree) Reset() {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.entries = nil
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
// entry while active is true. dotFrame advances per viewport refresh to
// animate the truncation dots on the running entry.
func (at *ActivityTree) RenderActive(width int, active bool, dotFrame int) string {
	at.mu.Lock()
	entries := make([]EngineEvent, len(at.entries))
	copy(entries, at.entries)
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
		b.WriteString(at.renderEvent(ev, width, running, dotFrame))
	}
	return dimmedStyle.Render(b.String())
}

// stageLabel maps an event kind to its modern pipeline stage name, rendered as
// a ":: stage" prefix (Grok/OpenCode style) so the tool tree reads as a
// scannable stage list rather than a raw log dump.
func stageLabel(kind EventKind) string {
	switch kind {
	case EventFileRead:
		return "explore"
	case EventFileMutate:
		return "diff"
	case EventCommandExec:
		return "exec"
	case EventSearch:
		return "search"
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

func (at *ActivityTree) renderEvent(ev EngineEvent, width int, running bool, dotFrame int) string {
	// Reserve cell budget for the ":: stage │ " prefix and the trailing
	// status badge so long paths/commands truncate cleanly instead of
	// overflowing the viewport's right border.
	prefixW := 16
	badgeW := 9
	contentW := width - prefixW - badgeW
	if contentW < 20 {
		contentW = 20
	}

	prefix := func(stage string) string {
		return stagePrefixStyle.Render(":: "+stage) + mutedStyle.Render(" │ ")
	}

	switch ev.Kind {
	case EventFileRead:
		e := ev.FileRead
		if e == nil {
			return ""
		}
		elapsed := formatElapsed(e.Elapsed)
		return fmt.Sprintf("%sRead %s (%d B · %s) %s",
			prefix("explore"), truncateMiddle(e.File, contentW), e.Bytes, mutedStyle.Render(elapsed), stageBadge(running, dotFrame))

	case EventFileMutate:
		e := ev.FileMutate
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%sPatch %s (+%d / -%d lines) %s",
			prefix("diff"), truncateMiddle(e.File, contentW), e.LinesAdd, e.LinesDel, stageBadge(running, dotFrame))

	case EventCommandExec:
		e := ev.CommandExec
		if e == nil {
			return ""
		}
		exitStr := fmt.Sprintf("exit %d", e.ExitCode)
		if e.ExitCode == 0 {
			exitStr = greenStyle.Render("exit 0")
		} else {
			exitStr = redStyle.Render(exitStr)
		}
		elapsed := formatElapsed(e.Elapsed)
		return fmt.Sprintf("%s%s (%s · %s) %s",
			prefix("exec"), yellowStyle.Render(truncateMiddle(e.Command, contentW)), exitStr, mutedStyle.Render(elapsed), stageBadge(running, dotFrame))

	case EventSearch:
		e := ev.Search
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%sSearch %s (%d hits) %s",
			prefix("search"), truncateMiddle(e.Query, contentW), e.Hits, stageBadge(running, dotFrame))

	case EventResolve:
		e := ev.Resolve
		if e == nil {
			return ""
		}
		return fmt.Sprintf("%sResolve %s (%d hits) %s",
			prefix("resolve"), truncateMiddle(e.Symbol, contentW), e.Hits, stageBadge(running, dotFrame))

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
