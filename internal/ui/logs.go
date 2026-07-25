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
type LogEntry struct {
	ID       int
	Kind     LogEntryKind
	Target   string // file path or command
	Success  bool
	Content  string // full diff/patch/stdout (shown when expanded)
	Expanded bool
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
	id := ls.nextID
	ls.nextID++
	ls.entries = append(ls.entries, LogEntry{
		ID:       id,
		Kind:     kind,
		Target:   target,
		Success:  success,
		Content:  content,
		Expanded: false,
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

// Entries returns all log entries.
func (ls *LogStore) Entries() []LogEntry {
	return ls.entries
}

// RenderEntry renders a single log entry.
// Collapsed: 🟢 ✓ Edit(path/to/file.go) (ctrl+o to expand)
// Expanded:  shows full content below the summary line.
func RenderEntry(entry LogEntry, width int) string {
	icon := greenStyle.Render("✓")
	if !entry.Success {
		icon = redStyle.Render("✗")
	}

	label := entry.Kind.String()
	target := entry.Target
	if len(target) > 40 {
		target = "..." + target[len(target)-37:]
	}

	summary := fmt.Sprintf("%s %s(%s)", icon, label, target)
	if !entry.Expanded {
		hint := dimmedStyle.Render(" (ctrl+o to expand)")
		return lipgloss.NewStyle().Width(width).Render(summary + hint)
	}

	var b strings.Builder
	b.WriteString(summary)
	b.WriteString("\n")
	if entry.Content != "" {
		lines := strings.Split(entry.Content, "\n")
		for _, line := range lines {
			b.WriteString("  ")
			b.WriteString(mutedStyle.Render(line))
			b.WriteString("\n")
		}
	}
	b.WriteString(dimmedStyle.Render(" (ctrl+o to collapse)"))
	return b.String()
}

// RenderLogSummary renders all collapsed log entries as compact bullet list.
func RenderLogSummary(entries []LogEntry, width int) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(RenderEntry(entry, width))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
