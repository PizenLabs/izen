package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ── EffortLabel ─────────────────────────────────────────────────────────────

// EffortLabel returns a human-readable label for the effort level.
// Uses Description() for the full text.
func EffortLabel(e EffortLevel) string {
	switch e {
	case EffortAuto:
		return "AUTO: Let model decide"
	case EffortLow:
		return "LOW: Fast-Track / Direct Mutation"
	case EffortMedium:
		return "MEDIUM: Hybrid Mutation + Local Templates"
	case EffortHigh:
		return "HIGH: Full Senior Architect Mode"
	default:
		return "AUTO: Let model decide"
	}
}

// ── ThinkingPanel ───────────────────────────────────────────────────────────

// ThinkingPanel tracks and renders live reasoning tokens.
type ThinkingPanel struct {
	mu        sync.Mutex
	buffer    strings.Builder
	startTime time.Time
	expanded  bool
	maxHeight int
}

// NewThinkingPanel creates a new thinking panel.
func NewThinkingPanel() *ThinkingPanel {
	return &ThinkingPanel{
		startTime: time.Now(),
		maxHeight: 10,
	}
}

// Append adds a reasoning chunk to the panel.
func (tp *ThinkingPanel) Append(chunk string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.buffer.WriteString(chunk)
}

// Len returns the current reasoning content length.
func (tp *ThinkingPanel) Len() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.buffer.Len()
}

// String returns the full reasoning text.
func (tp *ThinkingPanel) String() string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.buffer.String()
}

// Reset clears the panel.
func (tp *ThinkingPanel) Reset() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.buffer.Reset()
	tp.startTime = time.Now()
}

// Toggle switches between expanded and collapsed.
func (tp *ThinkingPanel) Toggle() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.expanded = !tp.expanded
}

// Expanded returns whether the panel is expanded.
func (tp *ThinkingPanel) Expanded() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.expanded
}

// SetExpanded sets the expanded state.
func (tp *ThinkingPanel) SetExpanded(v bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.expanded = v
}

// Render produces the visual thinking panel for the TUI.
// When collapsed, shows a single-line spinner with elapsed time.
// When expanded, shows the full reasoning content in a bordered box.
func (tp *ThinkingPanel) Render(width int, spinnerText string) string {
	tp.mu.Lock()
	content := tp.buffer.String()
	expanded := tp.expanded
	elapsed := time.Since(tp.startTime)
	tp.mu.Unlock()

	if content == "" {
		return ""
	}

	if !expanded {
		elapsedStr := fmt.Sprintf("%.0fs", elapsed.Seconds())
		status := fmt.Sprintf("%s Thinking... %s  %s", spinnerText, elapsedStr, mutedStyle.Render("[Ctrl+O to expand]"))
		return dimmedStyle.Render(status)
	}

	if width < 40 {
		width = 40
	}

	var b strings.Builder
	title := "Reasoning"
	elapsedStr := fmt.Sprintf("%.0fs", elapsed.Seconds())
	titleLine := fmt.Sprintf("%s %s", title, strings.Repeat("·", max(1, width-lipgloss.Width(title)-lipgloss.Width(elapsedStr)-8)))

	topFiller := width - lipgloss.Width(titleLine) - 6
	if topFiller < 0 {
		topFiller = 0
	}
	b.WriteString(dimmedStyle.Render("┌─ "+titleLine+" "+elapsedStr+" "+strings.Repeat("─", topFiller)+"┐") + "\n")

	lines := strings.Split(content, "\n")
	displayed := 0
	for _, line := range lines {
		if tp.maxHeight > 0 && displayed >= tp.maxHeight*2 {
			b.WriteString(dimmedStyle.Render("│ ") + mutedStyle.Render(fmt.Sprintf("... %d more lines", len(lines)-displayed)) + dimmedStyle.Render(" │") + "\n")
			break
		}
		line = strings.TrimRight(line, " \r")
		if line == "" {
			b.WriteString(dimmedStyle.Render("│ ") + " " + dimmedStyle.Render("  │") + "\n")
			displayed++
			continue
		}
		avail := width - 4
		if avail < 10 {
			avail = 10
		}
		wrapped := wrapString(line, avail)
		for _, wl := range wrapped {
			padded := wl + strings.Repeat(" ", avail-lipgloss.Width(wl))
			b.WriteString(dimmedStyle.Render("│ ") + mutedStyle.Render(padded) + dimmedStyle.Render(" │") + "\n")
			displayed++
		}
	}

	bottomFiller := width - 4
	if bottomFiller < 0 {
		bottomFiller = 0
	}
	b.WriteString(dimmedStyle.Render("└" + strings.Repeat("─", bottomFiller) + "┘"))

	return b.String()
}

// ── Live Code Preview ────────────────────────────────────────────────────────

// NewLiveCodePreview creates a new live code preview tracker.
func NewLiveCodePreview() *LiveCodePreview {
	return &LiveCodePreview{}
}

// LiveCodePreview streams code changes into the TUI as tool call arguments arrive.
type LiveCodePreview struct {
	mu       sync.Mutex
	previews []FilePreview
}

// FilePreview represents a live code preview for a single file.
type FilePreview struct {
	Path    string
	Content string
	IsNew   bool
}

// AddOrUpdate adds or updates a file preview.
func (lcp *LiveCodePreview) AddOrUpdate(path, content string, isNew bool) {
	lcp.mu.Lock()
	defer lcp.mu.Unlock()
	for i, p := range lcp.previews {
		if p.Path == path {
			lcp.previews[i].Content = content
			lcp.previews[i].IsNew = isNew
			return
		}
	}
	lcp.previews = append(lcp.previews, FilePreview{Path: path, Content: content, IsNew: isNew})
}

// Previews returns all active previews.
func (lcp *LiveCodePreview) Previews() []FilePreview {
	lcp.mu.Lock()
	defer lcp.mu.Unlock()
	result := make([]FilePreview, len(lcp.previews))
	copy(result, lcp.previews)
	return result
}

// Reset clears all previews.
func (lcp *LiveCodePreview) Reset() {
	lcp.mu.Lock()
	defer lcp.mu.Unlock()
	lcp.previews = nil
}

// HasContent returns true if there are active previews.
func (lcp *LiveCodePreview) HasContent() bool {
	lcp.mu.Lock()
	defer lcp.mu.Unlock()
	return len(lcp.previews) > 0
}

// RenderPreview produces a compact text representation of active file previews.
func (lcp *LiveCodePreview) RenderPreview(width int) string {
	lcp.mu.Lock()
	previews := make([]FilePreview, len(lcp.previews))
	copy(previews, lcp.previews)
	lcp.mu.Unlock()

	if len(previews) == 0 {
		return ""
	}

	var b strings.Builder
	for _, p := range previews {
		icon := Icon.Edit
		if p.IsNew {
			icon = Icon.Spark
		}
		lines := strings.Count(p.Content, "\n") + 1
		fmt.Fprintf(&b, "  %s %s (%d lines)\n", icon, p.Path, lines)
	}
	return dimmedStyle.Render(strings.TrimRight(b.String(), "\n"))
}

// ── Interaction Constants ────────────────────────────────────────────────────

const (
	EffortSelectorLabel = "Effort:"
	EffortSelectorHint  = "←/→ arrows to change  |  Tab: switch model"
	ApprovalAccept      = "Accept & Apply"
	ApprovalAllowAll    = "Allow All Steps"
	ApprovalReject      = "Reject"
	ApprovalEditEffort  = "Adjust Effort"
)
