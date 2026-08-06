package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// LogEntryKind is the type of execution log action.
type LogEntryKind int

const (
	LogCreate LogEntryKind = iota
	LogEdit
	LogBash
	LogSearch
	LogDelete
	LogOther
)

func (k LogEntryKind) String() string {
	switch k {
	case LogCreate:
		return "Create"
	case LogEdit:
		return "Edit"
	case LogBash:
		return "Bash"
	case LogSearch:
		return "Search"
	case LogDelete:
		return "Delete"
	default:
		return "Action"
	}
}

// LogEntry represents a single foldable execution log entry.
// Thinking stores full LLM reasoning tokens from the <thought> block
// and SystemLog stores the raw system execution output. Both are
// persisted even during Per-Task Fallback mode so they are always
// available for expanded display via Ctrl+O.
type LogEntry struct {
	ID        int
	Kind      LogEntryKind
	Target    string // file path or command
	Success   bool
	Content   string // full diff/patch/stdout (shown when expanded)
	Thinking  string // full LLM reasoning content (shown when expanded)
	SystemLog string // raw system execution log (shown when expanded)
	Expanded  bool
}

// LogStore holds a collection of foldable log entries.
type LogStore struct {
	entries []LogEntry
	nextID  int
}

func NewLogStore() *LogStore {
	return &LogStore{
		entries: make([]LogEntry, 0),
		nextID:  1,
	}
}

// Add appends a new log entry and returns its ID.
func (ls *LogStore) Add(kind LogEntryKind, target string, success bool, content string) int {
	return ls.AddFull(kind, target, success, content, "", "")
}

// AddFull appends a new log entry with full thinking and system log content,
// then returns its ID. The thinking and systemLog fields are retained even
// during Per-Task Fallback mode so expandable thought logs remain available.
func (ls *LogStore) AddFull(kind LogEntryKind, target string, success bool, content string, thinking string, systemLog string) int {
	id := ls.nextID
	ls.nextID++
	ls.entries = append(ls.entries, LogEntry{
		ID:        id,
		Kind:      kind,
		Target:    target,
		Success:   success,
		Content:   content,
		Thinking:  thinking,
		SystemLog: systemLog,
		Expanded:  false,
	})
	return id
}

// Toggle toggles the expanded state of the entry with the given ID.
// Returns true if the entry was found.
func (ls *LogStore) Toggle(id int) bool {
	for i := range ls.entries {
		if ls.entries[i].ID == id {
			ls.entries[i].Expanded = !ls.entries[i].Expanded
			return true
		}
	}
	return false
}

// ToggleLast toggles the expanded state of the last log entry.
// Returns true if an entry was found and toggled.
func (ls *LogStore) ToggleLast() bool {
	entries := ls.entries
	if len(entries) == 0 {
		return false
	}
	last := entries[len(entries)-1]
	return ls.Toggle(last.ID)
}

// ToggleCycle rotates through all entries, collapsing any expanded
// entry and expanding the next one. Returns the ID of the newly
// expanded entry, or -1 if there are no entries.
func (ls *LogStore) ToggleCycle() int {
	entries := ls.entries
	if len(entries) == 0 {
		return -1
	}
	// If any entry is already expanded, collapse it and expand the next.
	for i, e := range entries {
		if e.Expanded {
			ls.entries[i].Expanded = false
			next := (i + 1) % len(entries)
			ls.entries[next].Expanded = true
			return entries[next].ID
		}
	}
	// No entry is expanded — expand the first one.
	ls.entries[0].Expanded = true
	return entries[0].ID
}

// Clear removes all log entries.
func (ls *LogStore) Clear() {
	ls.entries = nil
	ls.nextID = 1
}

// Entries returns all log entries.
func (ls *LogStore) Entries() []LogEntry {
	return ls.entries
}

// RenderEntry renders a single log entry.
// Collapsed: ✓ Edit(path/to/file.go) (ctrl+o to expand) [animated dots if in-progress]
// Expanded:  shows full content, thinking, and system log below the summary.
func RenderEntry(entry LogEntry, width int, dotFrame int) string {
	icon := greenStyle.Render(Icon.Success)
	if !entry.Success {
		icon = redStyle.Render(Icon.Error)
	}

	label := entry.Kind.String()
	target := entry.Target
	if len(target) > 40 {
		target = "..." + target[len(target)-37:]
	}

	// Sanitize tool-output/log text before rendering so literal \n / \t / \"
	// escapes expand to real control characters and tabs align deterministically
	// — raw backslash noise never reaches the viewport.
	thinking := sanitizeText(entry.Thinking)
	systemLog := sanitizeText(entry.SystemLog)
	content := sanitizeText(entry.Content)

	summary := fmt.Sprintf("%s %s(%s)", icon, label, target)
	if !entry.Expanded {
		// Show animated truncation dots for in-progress entries.
		// When expanded (via Ctrl+O), dots are removed and full content is shown.
		dots := animatedDots(dotFrame)
		hint := dimmedStyle.Render(fmt.Sprintf(" ctrl+o to expand %s", dots))
		return lipgloss.NewStyle().Width(width).Render(summary + hint)
	}

	var b strings.Builder
	b.WriteString(summary)
	b.WriteString("\n")

	contentWidth := width - 6
	if contentWidth < 10 {
		contentWidth = 10
	}

	// Render thinking content (LLM reasoning) when expanded
	if thinking != "" {
		b.WriteString(thoughtLogBoxStyle.Render(thoughtLogTitleStyle.Render("  Reasoning")))
		b.WriteString("\n")
		for _, line := range strings.Split(thinking, "\n") {
			wrapped := wrapLine(line, contentWidth)
			for _, wl := range wrapped {
				b.WriteString(thoughtLogBoxStyle.Render("  " + mutedStyle.Render(wl)))
				b.WriteString("\n")
			}
		}
	}

	// Render system execution log when expanded
	if systemLog != "" {
		b.WriteString(systemLogBoxStyle.Render(systemLogTitleStyle.Render("  System Log")))
		b.WriteString("\n")
		for _, line := range strings.Split(systemLog, "\n") {
			wrapped := wrapLine(line, contentWidth)
			for _, wl := range wrapped {
				b.WriteString(systemLogBoxStyle.Render("  " + mutedStyle.Render(wl)))
				b.WriteString("\n")
			}
		}
	}

	if content != "" {
		b.WriteString(buildSummaryBoxStyle.Render(buildSummaryTitleStyle.Render("  Details")))
		b.WriteString("\n")
		for _, line := range strings.Split(content, "\n") {
			wrapped := wrapLine(line, contentWidth)
			for _, wl := range wrapped {
				b.WriteString(buildSummaryBoxStyle.Render("  " + mutedStyle.Render(wl)))
				b.WriteString("\n")
			}
		}
	}

	b.WriteString(dimmedStyle.Render(" (ctrl+o to collapse)"))
	return b.String()
}

// animatedDots returns a cycling sequence of truncation dots
// for in-progress execution log entries: ".", "..", "..."
// The dotFrame parameter is a 0-based counter that advances each
// viewport refresh, producing a smooth animated progression.
func animatedDots(dotFrame int) string {
	switch dotFrame % 3 {
	case 0:
		return "."
	case 1:
		return ".."
	default:
		return "..."
	}
}

// wrapLine wraps a single line of text to the specified width, splitting at
// word boundaries. Falls back to hard rune-wrap for words that exceed the width.
func wrapLine(text string, width int) []string {
	if len(text) == 0 || width < 1 {
		return []string{text}
	}
	var result []string
	words := strings.Fields(text)
	if len(words) == 0 {
		runes := []rune(text)
		for i := 0; i < len(runes); i += width {
			end := i + width
			if end > len(runes) {
				end = len(runes)
			}
			result = append(result, string(runes[i:end]))
		}
		return result
	}
	var line strings.Builder
	for _, word := range words {
		wordW := lipgloss.Width(word)
		if line.Len() > 0 && line.Len()+1+wordW > width {
			result = append(result, line.String())
			line.Reset()
			line.WriteString(word)
		} else {
			if line.Len() > 0 {
				line.WriteString(" ")
			}
			line.WriteString(word)
		}
	}
	if line.Len() > 0 {
		result = append(result, line.String())
	}
	return result
}

// RenderLogSummary renders all collapsed log entries as compact bullet list.
func RenderLogSummary(entries []LogEntry, width int, dotFrame int) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(RenderEntry(entry, width, dotFrame))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
