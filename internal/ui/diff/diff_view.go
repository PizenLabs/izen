package diff

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DefaultCollapseThreshold folds interior unchanged runs longer than this.
const DefaultCollapseThreshold = 6

var styleDiffTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))

// collapsedRun is a folded block of unchanged lines.
type collapsedRun struct {
	Count int
}

// visibleItem is one rendered row: either a DiffLine or a collapsedRun,
// tagged with its file/hunk origin for focus tracking.
type visibleItem struct {
	FileIdx int
	HunkIdx int
	Line    *DiffLine
	Run     *collapsedRun
}

// Model is a self-contained Bubbletea viewport over parsed FileDiffs.
type Model struct {
	Files     []FileDiff
	Title     string
	Threshold int
	Width     int
	Height    int

	viewport viewport.Model
	items    []visibleItem
	ready    bool
}

// NewModel builds a diff viewer. Width/height size the inner viewport;
// threshold <= 0 selects DefaultCollapseThreshold. Hunks containing long
// interior unchanged runs start collapsed.
func NewModel(files []FileDiff, title string, width, height, threshold int) Model {
	if threshold <= 0 {
		threshold = DefaultCollapseThreshold
	}
	m := Model{Files: files, Title: title, Threshold: threshold, Width: width, Height: height}
	if m.Width < 20 {
		m.Width = 20
	}
	if m.Height < 5 {
		m.Height = 5
	}
	for fi := range m.Files {
		for hi := range m.Files[fi].Hunks {
			if hasCollapsibleRun(m.Files[fi].Hunks[hi].Lines, threshold) {
				m.Files[fi].Hunks[hi].Collapsed = true
			}
		}
	}
	m.rebuild()
	return m
}

// SetSize resizes the viewport and re-renders with width truncation.
func (m *Model) SetSize(width, height int) {
	if width < 20 {
		width = 20
	}
	if height < 5 {
		height = 5
	}
	m.Width, m.Height = width, height
	m.rebuild()
}

// ToggleHunk flips the collapse state of one hunk. Out-of-range indices are no-ops.
func (m *Model) ToggleHunk(fileIdx, hunkIdx int) {
	if fileIdx < 0 || fileIdx >= len(m.Files) {
		return
	}
	if hunkIdx < 0 || hunkIdx >= len(m.Files[fileIdx].Hunks) {
		return
	}
	h := &m.Files[fileIdx].Hunks[hunkIdx]
	h.Collapsed = !h.Collapsed
	m.rebuild()
}

// ToggleAll collapses everything if any hunk is expanded, else expands all.
func (m *Model) ToggleAll() {
	anyExpanded := false
	for fi := range m.Files {
		for hi := range m.Files[fi].Hunks {
			if hasCollapsibleRun(m.Files[fi].Hunks[hi].Lines, m.Threshold) && !m.Files[fi].Hunks[hi].Collapsed {
				anyExpanded = true
			}
		}
	}
	for fi := range m.Files {
		for hi := range m.Files[fi].Hunks {
			if hasCollapsibleRun(m.Files[fi].Hunks[hi].Lines, m.Threshold) {
				m.Files[fi].Hunks[hi].Collapsed = anyExpanded
			}
		}
	}
	m.rebuild()
}

// ExpandAll / CollapseAll set every collapsible hunk explicitly.
func (m *Model) ExpandAll() {
	for fi := range m.Files {
		for hi := range m.Files[fi].Hunks {
			m.Files[fi].Hunks[hi].Collapsed = false
		}
	}
	m.rebuild()
}

// CollapseAll folds every hunk with a collapsible run.
func (m *Model) CollapseAll() {
	for fi := range m.Files {
		for hi := range m.Files[fi].Hunks {
			if hasCollapsibleRun(m.Files[fi].Hunks[hi].Lines, m.Threshold) {
				m.Files[fi].Hunks[hi].Collapsed = true
			}
		}
	}
	m.rebuild()
}

// ToggleFocused toggles the hunk rendered at the top of the viewport.
// Returns the (file, hunk) toggled, or (-1, -1) when empty.
func (m *Model) ToggleFocused() (int, int) {
	if len(m.items) == 0 {
		return -1, -1
	}
	off := m.viewport.YOffset
	if off < 0 {
		off = 0
	}
	if off >= len(m.items) {
		off = len(m.items) - 1
	}
	// items includes file/hunk header rows (Line == nil, Run == nil);
	// walk forward to the first row bound to a hunk.
	for i := off; i < len(m.items); i++ {
		if m.items[i].HunkIdx >= 0 {
			m.ToggleHunk(m.items[i].FileIdx, m.items[i].HunkIdx)
			return m.items[i].FileIdx, m.items[i].HunkIdx
		}
	}
	for i := off; i >= 0; i-- {
		if m.items[i].HunkIdx >= 0 {
			m.ToggleHunk(m.items[i].FileIdx, m.items[i].HunkIdx)
			return m.items[i].FileIdx, m.items[i].HunkIdx
		}
	}
	return -1, -1
}

// Update forwards scroll/input messages to the inner viewport.
func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return *m, cmd
}

// GotoTop / GotoBottom jump the viewport.
func (m *Model) GotoTop()    { m.viewport.GotoTop() }
func (m *Model) GotoBottom() { m.viewport.GotoBottom() }

// View renders the title bar plus the scrollable diff viewport.
func (m *Model) View() string {
	var sb strings.Builder
	if m.Title != "" {
		sb.WriteString(styleDiffTitle.Render(m.Title))
		sb.WriteString("\n")
	}
	sb.WriteString(m.viewport.View())
	sb.WriteString("\n")
	sb.WriteString(styleCollapseBar.Render("j/k scroll · c toggle collapse · q/Esc close"))
	return sb.String()
}

// VisibleLineCount reports rendered rows (headers + lines + summary bars).
func (m *Model) VisibleLineCount() int { return len(m.items) }

// CollapsedCount counts summary bars currently rendered.
func (m *Model) CollapsedCount() int {
	n := 0
	for _, it := range m.items {
		if it.Run != nil {
			n++
		}
	}
	return n
}

// RenderedContent exposes the current viewport content (for tests/embedders).
func (m *Model) RenderedContent() string { return m.viewport.View() }

// rebuild flattens files/hunks into items and resets the viewport content.
func (m *Model) rebuild() {
	m.items = m.items[:0]
	var rows []string
	// Reserve 2 rows for the title bar + help footer.
	vpH := m.Height - 2
	if vpH < 3 {
		vpH = 3
	}
	for fi := range m.Files {
		f := &m.Files[fi]
		rows = append(rows, RenderFileHeader(*f, m.Width))
		m.items = append(m.items, visibleItem{FileIdx: fi, HunkIdx: -1})
		for hi := range f.Hunks {
			h := &f.Hunks[hi]
			rows = append(rows, RenderHunkHeader(h.Header, m.Width))
			m.items = append(m.items, visibleItem{FileIdx: fi, HunkIdx: hi})
			for _, it := range foldHunk(*h, m.Threshold) {
				switch {
				case it.Run != nil:
					rows = append(rows, RenderCollapseBar(it.Run.Count, m.Width))
					m.items = append(m.items, visibleItem{FileIdx: fi, HunkIdx: hi, Run: it.Run})
				default:
					cp := it.Line
					rows = append(rows, RenderLine(*cp, m.Width))
					m.items = append(m.items, visibleItem{FileIdx: fi, HunkIdx: hi, Line: cp})
				}
			}
		}
	}
	content := strings.Join(rows, "\n")
	if !m.ready {
		m.viewport = viewport.New(m.Width, vpH)
		m.ready = true
	} else {
		m.viewport.Width = m.Width
		m.viewport.Height = vpH
	}
	m.viewport.SetContent(content)
}

// foldHunk applies context folding to one hunk's lines.
func foldHunk(h DiffHunk, threshold int) []visibleItem {
	if !h.Collapsed || threshold <= 0 {
		out := make([]visibleItem, 0, len(h.Lines))
		for i := range h.Lines {
			cp := h.Lines[i]
			out = append(out, visibleItem{Line: &cp})
		}
		return out
	}
	var out []visibleItem
	i := 0
	for i < len(h.Lines) {
		if h.Lines[i].Type != LineUnchanged {
			cp := h.Lines[i]
			out = append(out, visibleItem{Line: &cp})
			i++
			continue
		}
		j := i
		for j < len(h.Lines) && h.Lines[j].Type == LineUnchanged {
			j++
		}
		runLen := j - i
		interior := i > 0 && j < len(h.Lines)
		switch {
		case runLen > threshold && interior:
			out = append(out, visibleItem{Run: &collapsedRun{Count: runLen}})
		case runLen > threshold:
			// Edge runs (leading/trailing context): keep the edge-adjacent
			// threshold lines, fold the rest.
			keep := threshold
			if i == 0 {
				// Leading run: drop the far head, keep the tail near the change.
				for k := j - keep; k < j; k++ {
					cp := h.Lines[k]
					out = append(out, visibleItem{Line: &cp})
				}
				if j-keep > i {
					// Prepend summary before kept lines for stable ordering.
					summary := visibleItem{Run: &collapsedRun{Count: runLen - keep}}
					out = append(out[:len(out)-keep], append([]visibleItem{summary}, out[len(out)-keep:]...)...)
				}
			} else {
				for k := i; k < i+keep && k < j; k++ {
					cp := h.Lines[k]
					out = append(out, visibleItem{Line: &cp})
				}
				if runLen > keep {
					out = append(out, visibleItem{Run: &collapsedRun{Count: runLen - keep}})
				}
			}
		default:
			for k := i; k < j; k++ {
				cp := h.Lines[k]
				out = append(out, visibleItem{Line: &cp})
			}
		}
		i = j
	}
	return out
}

func hasCollapsibleRun(lines []DiffLine, threshold int) bool {
	if threshold <= 0 {
		return false
	}
	run := 0
	for _, l := range lines {
		if l.Type == LineUnchanged {
			run++
			if run > threshold {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
