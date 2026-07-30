package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

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

func (at *ActivityTree) Render(width int) string {
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
		b.WriteString(at.renderEvent(ev, width))
	}
	return dimmedStyle.Render(b.String())
}

func (at *ActivityTree) renderEvent(ev EngineEvent, width int) string {
	switch ev.Kind {
	case EventFileRead:
		e := ev.FileRead
		if e == nil {
			return ""
		}
		prefix := blueStyle.Render("→")
		elapsed := formatElapsed(e.Elapsed)
		return fmt.Sprintf("%s Read %s (%d B · %s)", prefix, e.File, e.Bytes, mutedStyle.Render(elapsed))

	case EventFileMutate:
		e := ev.FileMutate
		if e == nil {
			return ""
		}
		prefix := blueStyle.Render("→")
		return fmt.Sprintf("%s Patch %s (+%d / -%d lines)", prefix, e.File, e.LinesAdd, e.LinesDel)

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
		return fmt.Sprintf("* Exec %s (%s · %s)", yellowStyle.Render(e.Command), exitStr, mutedStyle.Render(elapsed))

	case EventSearch:
		e := ev.Search
		if e == nil {
			return ""
		}
		prefix := orangeStyle.Render("*")
		return fmt.Sprintf("%s Search %s (%d hits)", prefix, e.Query, e.Hits)

	case EventResolve:
		e := ev.Resolve
		if e == nil {
			return ""
		}
		prefix := orangeStyle.Render("*")
		return fmt.Sprintf("%s Resolve %s (%d hits)", prefix, e.Symbol, e.Hits)

	default:
		return ""
	}
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

type ThoughtStream struct {
	mu        sync.Mutex
	buffer    strings.Builder
	visible   bool
	maxLines  int
	startTime time.Time
}

func NewThoughtStream() *ThoughtStream {
	return &ThoughtStream{
		visible:   true,
		maxLines:  8,
		startTime: time.Now(),
	}
}

func (ts *ThoughtStream) Append(chunk string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.buffer.WriteString(chunk)
}

func (ts *ThoughtStream) Len() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.buffer.Len()
}

func (ts *ThoughtStream) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.buffer.Reset()
	ts.startTime = time.Now()
}

func (ts *ThoughtStream) SetVisible(v bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.visible = v
}

func (ts *ThoughtStream) Render(width int, spinner string) string {
	ts.mu.Lock()
	content := ts.buffer.String()
	visible := ts.visible
	elapsed := time.Since(ts.startTime)
	ts.mu.Unlock()

	if content == "" {
		return ""
	}

	if !visible {
		elapsedStr := fmt.Sprintf("%.0fs", elapsed.Seconds())
		status := fmt.Sprintf("%s thinking... %s", spinner, elapsedStr)
		return dimmedStyle.Faint(true).Render(status)
	}

	if width < 40 {
		width = 40
	}

	availWidth := width - 6
	if availWidth < 10 {
		availWidth = 10
	}

	lines := strings.Split(content, "\n")
	var displayed []string
	for _, line := range lines {
		if len(displayed) >= ts.maxLines {
			remaining := len(lines) - len(displayed)
			if remaining > 0 {
				displayed = append(displayed, mutedStyle.Render(fmt.Sprintf("... %d more lines", remaining)))
			}
			break
		}
		line = strings.TrimRight(line, " \r")
		if line == "" {
			displayed = append(displayed, "")
			continue
		}
		wrapped := wrapString(line, availWidth)
		displayed = append(displayed, wrapped...)
	}

	elapsedStr := fmt.Sprintf("%.0fs", elapsed.Seconds())

	linesOut := make([]string, 0, len(displayed)+2)
	titleLine := fmt.Sprintf(" %s thoughts  %s", spinner, mutedStyle.Render(elapsedStr))
	linesOut = append(linesOut, dimmedStyle.Faint(true).Render(titleLine))

	for _, line := range displayed {
		if line == "" {
			linesOut = append(linesOut, dimmedStyle.Faint(true).Render(" \u2502"))
		} else {
			linesOut = append(linesOut, dimmedStyle.Faint(true).Render(" \u2502 "+line))
		}
	}

	return strings.Join(linesOut, "\n")
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
	st.buffer.Reset()
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

	return content[:emit], true
}

func (st *StreamThrottle) Reset() {
	st.buffer.Reset()
}

func (st *StreamThrottle) Len() int {
	return st.buffer.Len()
}
