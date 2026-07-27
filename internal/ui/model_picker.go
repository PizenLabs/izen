package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/llm"
)

// EffortLevel represents the intent/effort slider value.
type EffortLevel int

const (
	EffortAuto EffortLevel = iota
	EffortLow
	EffortMedium
	EffortHigh
)

func (e EffortLevel) String() string {
	switch e {
	case EffortAuto:
		return "auto"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	default:
		return "auto"
	}
}

// Description returns a human-readable description of the effort level.
func (e EffortLevel) Description() string {
	switch e {
	case EffortAuto:
		return "Auto / Let model decide"
	case EffortLow:
		return "Fast-Track / Direct Mutation"
	case EffortMedium:
		return "Hybrid Mutation + Local Templates"
	case EffortHigh:
		return "Full Senior Architect Mode"
	default:
		return "Auto / Let model decide"
	}
}

// ConfigTier maps effort level to the config tier key.
func (e EffortLevel) ConfigTier() string {
	switch e {
	case EffortAuto:
		return "auto_intent"
	case EffortLow:
		return "low_intent"
	case EffortMedium:
		return "medium_intent"
	case EffortHigh:
		return "high_intent"
	default:
		return "auto_intent"
	}
}

// Style returns the pre-compiled render-path style for this effort level:
// low is green (fast/low-risk), medium is yellow (moderate), high is red
// (heaviest/highest-risk mode) — reusing the shared Catppuccin styles from
// styles.go rather than declaring new ones.
func (e EffortLevel) Style() lipgloss.Style {
	switch e {
	case EffortAuto:
		return infoStyle
	case EffortLow:
		return greenStyle
	case EffortMedium:
		return yellowStyle
	case EffortHigh:
		return redStyle
	default:
		return infoStyle
	}
}

type modelPickerState int

const (
	mpLoading modelPickerState = iota
	mpReady
	mpErr
)

// modelListLineBudget is the *default* number of lines the scrollable
// model list body occupies when no terminal size is known yet (i.e.
// before the first SetSize call). Once SetSize has run, the real budget
// is computed per-render by listRowBudget(), which shrinks or grows the
// list to fit the space actually available — this is what keeps the
// modal from being clipped when the user is running Izen in a narrow
// tmux/terminal split pane.
//
// Regardless of which value is in play, the list stays windowed/padded
// in terms of *rendered rows* rather than filtered items, so renderList()'s
// total output height is still perfectly constant for any given terminal
// size (cursor movement, filtering, refresh never change it) — only a
// resize event changes it. That's what lets the outer modal box in
// workspace.go (renderModelPickerModal) auto-size around it instead of
// hardcoding its own Height/MaxHeight.
const modelListLineBudget = 7

// modelListMinRows is the smallest the scrollable list body is ever
// allowed to shrink to, even in a very short split pane. Below this the
// picker stops being usable, so we'd rather show a cramped-but-functional
// list than one that's silently cut off.
const modelListMinRows = 3

// modelPickerChromeLines is the number of fixed, non-list-body lines
// renderList() always emits: title + blank, search input, count/refresh
// line + blank, effort description + effort track + blank, the gap before
// the footer, and the footer itself — plus the outer border (2) and
// Padding(1, 3) (2). Kept in sync with renderList(); see the comment
// there if either changes.
const modelPickerChromeLines = 8 + 2 + 2 + 2

// modelPickerMinWidth/Height are floors applied in SetSize so a very
// aggressively split pane doesn't hand us a zero or negative size.
const modelPickerMinWidth = 28
const modelPickerMinHeight = modelPickerChromeLines + modelListMinRows

type ModelPickerModal struct {
	ti       textinput.Model
	state    modelPickerState
	models   []llm.ModelInfo
	filtered []llm.ModelInfo
	cursor   int
	loading  bool
	errMsg   string
	width    int
	height   int
	registry *llm.ModelRegistry

	effortIdx    int // 0=auto, 1=low, 2=medium, 3=high
	scrollOffset int // row-based offset into buildRows(), NOT an item index
}

func (mp *ModelPickerModal) CurrentEffort() EffortLevel {
	switch mp.effortIdx {
	case 0:
		return EffortAuto
	case 1:
		return EffortLow
	case 2:
		return EffortMedium
	case 3:
		return EffortHigh
	default:
		return EffortAuto
	}
}

type modelPickerLoadedMsg struct {
	models []llm.ModelInfo
	err    error
}

type modelPickerRefreshMsg struct {
	models []llm.ModelInfo
	err    error
}

func NewModelPickerModal() *ModelPickerModal {
	ti := textinput.New()
	ti.Prompt = "▸ "
	ti.Placeholder = "type to filter models..."
	ti.CharLimit = 64
	ti.Width = 40
	ti.Focus()

	return &ModelPickerModal{
		ti:       ti,
		state:    mpLoading,
		registry: llm.NewModelRegistry(),
	}
}

func (mp *ModelPickerModal) LoadModels(providers map[string]string) tea.Cmd {
	mp.loading = true
	mp.state = mpLoading
	mp.models = nil
	mp.filtered = nil

	return func() tea.Msg {
		models, err := mp.registry.GetModels(providers)
		if err != nil && models == nil {
			return modelPickerLoadedMsg{err: err}
		}
		return modelPickerLoadedMsg{models: models}
	}
}

func (mp *ModelPickerModal) RefreshModels(providers map[string]string) tea.Cmd {
	return func() tea.Msg {
		mp.registry.InvalidateCache()
		models, err := mp.registry.Refresh(providers)
		if err != nil && models == nil {
			return modelPickerRefreshMsg{err: err}
		}
		return modelPickerRefreshMsg{models: models}
	}
}

type modelSelectedMsg struct {
	model  llm.ModelInfo
	effort EffortLevel
}

func (mp *ModelPickerModal) Update(msg tea.Msg) (*ModelPickerModal, tea.Cmd) {
	switch msg := msg.(type) {
	case modelPickerLoadedMsg:
		mp.loading = false
		if msg.err != nil {
			mp.state = mpErr
			mp.errMsg = msg.err.Error()
			return mp, nil
		}
		mp.state = mpReady
		mp.models = msg.models
		mp.applyFilter()
		return mp, nil

	case modelPickerRefreshMsg:
		if msg.err != nil {
			mp.errMsg = msg.err.Error()
			mp.state = mpErr
			return mp, nil
		}
		mp.state = mpReady
		mp.errMsg = ""
		mp.models = msg.models
		mp.applyFilter()
		return mp, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlR:
			return mp, mp.RefreshModels(providerConfigsFromModel(mp.models))

		case tea.KeyUp:
			if mp.cursor > 0 {
				mp.cursor--
			}
			mp.clampScrollOffset()
			return mp, nil

		case tea.KeyDown:
			if mp.cursor < len(mp.filtered)-1 {
				mp.cursor++
			}
			mp.clampScrollOffset()
			return mp, nil

		case tea.KeyLeft:
			if mp.effortIdx > 0 {
				mp.effortIdx--
			}
			return mp, nil

		case tea.KeyRight:
			if mp.effortIdx < 3 {
				mp.effortIdx++
			}
			return mp, nil

		case tea.KeyEnter:
			if mp.cursor >= 0 && mp.cursor < len(mp.filtered) {
				selected := mp.filtered[mp.cursor]
				effort := mp.CurrentEffort()
				return mp, func() tea.Msg {
					return modelSelectedMsg{model: selected, effort: effort}
				}
			}
			return mp, nil

		case tea.KeyEscape:
			return mp, nil

		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
				var cmd tea.Cmd
				mp.ti, cmd = mp.ti.Update(msg)
				mp.cursor = 0
				mp.applyFilter()
				return mp, cmd
			}
		}
	}

	return mp, nil
}

func (mp *ModelPickerModal) SetSize(w, h int) {
	if w < modelPickerMinWidth {
		w = modelPickerMinWidth
	}
	if h < modelPickerMinHeight {
		h = modelPickerMinHeight
	}
	mp.width = w
	mp.height = h

	tiWidth := w - 12
	if tiWidth < 10 {
		tiWidth = 10
	}
	mp.ti.Width = tiWidth

	// Re-clamp the current scroll position: shrinking the pane can put
	// the previous offset past the new (smaller) row budget.
	mp.clampScrollOffset()
}

// listRowBudget returns how many rows the scrollable list body should
// render this frame. It's derived from the last known terminal size
// (via SetSize) minus the fixed chrome around it, floored at
// modelListMinRows so the list never fully disappears in a tiny pane,
// and falls back to the static modelListLineBudget default before the
// first SetSize call has happened (e.g. mid-startup).
func (mp *ModelPickerModal) listRowBudget() int {
	if mp.height <= 0 {
		return modelListLineBudget
	}
	budget := mp.height - modelPickerChromeLines
	if budget < modelListMinRows {
		budget = modelListMinRows
	}
	return budget
}

// ── Row model ────────────────────────────────────────────────────────────
//
// The list body is windowed and padded in terms of *rendered lines*, not
// filtered items. A provider boundary produces a blank separator row plus
// a header row in addition to the item rows — all of those now count
// against the same fixed modelListLineBudget, so the body height (and
// therefore the modal's outer border) never changes as the cursor moves.

type mpRowKind int

const (
	mpRowHeader mpRowKind = iota
	mpRowBlank
	mpRowItem
)

type mpRow struct {
	kind      mpRowKind
	provider  string // valid for mpRowHeader
	itemIndex int    // valid for mpRowItem; index into mp.filtered
}

func (mp *ModelPickerModal) buildRows() []mpRow {
	rows := make([]mpRow, 0, len(mp.filtered)+4)
	var prevProvider string
	for i, m := range mp.filtered {
		if m.Provider != prevProvider {
			if prevProvider != "" {
				rows = append(rows, mpRow{kind: mpRowBlank})
			}
			rows = append(rows, mpRow{kind: mpRowHeader, provider: m.Provider})
			prevProvider = m.Provider
		}
		rows = append(rows, mpRow{kind: mpRowItem, itemIndex: i})
	}
	return rows
}

func rowIndexForItem(rows []mpRow, itemIndex int) int {
	for i, r := range rows {
		if r.kind == mpRowItem && r.itemIndex == itemIndex {
			return i
		}
	}
	return 0
}

func (mp *ModelPickerModal) clampScrollOffset() {
	if len(mp.filtered) == 0 {
		mp.scrollOffset = 0
		return
	}
	if mp.cursor >= len(mp.filtered) {
		mp.cursor = len(mp.filtered) - 1
	}
	if mp.cursor < 0 {
		mp.cursor = 0
	}

	rows := mp.buildRows()
	total := len(rows)
	if total == 0 {
		mp.scrollOffset = 0
		return
	}

	cursorRow := rowIndexForItem(rows, mp.cursor)
	budget := mp.listRowBudget()

	// Keep the cursor's row inside the visible window.
	if cursorRow < mp.scrollOffset {
		mp.scrollOffset = cursorRow
	} else if cursorRow >= mp.scrollOffset+budget {
		mp.scrollOffset = cursorRow - budget + 1
	}

	maxOffset := total - budget
	if maxOffset < 0 {
		maxOffset = 0
	}
	if mp.scrollOffset > maxOffset {
		mp.scrollOffset = maxOffset
	}
	if mp.scrollOffset < 0 {
		mp.scrollOffset = 0
	}
}

func (mp *ModelPickerModal) applyFilter() {
	mp.scrollOffset = 0
	mp.cursor = 0
	query := mp.ti.Value()
	if query == "" {
		mp.filtered = mp.models
		return
	}

	lower := strings.ToLower(query)
	var results []llm.ModelInfo
	for _, m := range mp.models {
		if strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) ||
			strings.Contains(strings.ToLower(m.Provider), lower) {
			results = append(results, m)
		}
	}

	if len(results) > 100 {
		results = results[:100]
	}
	mp.filtered = results
}

func (mp *ModelPickerModal) View() string {
	if mp.loading {
		return mp.renderLoading()
	}
	if mp.state == mpErr {
		return mp.renderError()
	}
	return mp.renderList()
}

func (mp *ModelPickerModal) renderLoading() string {
	return lipgloss.NewStyle().
		Width(mp.width-4).
		Height(5).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Align(lipgloss.Center, lipgloss.Center).
		Render("Fetching models...")
}

func (mp *ModelPickerModal) renderError() string {
	return lipgloss.NewStyle().
		Width(mp.width-4).
		Height(5).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorRed)).
		Align(lipgloss.Center, lipgloss.Center).
		Render(fmt.Sprintf("Error: %s", mp.errMsg))
}

func (mp *ModelPickerModal) renderList() string {
	var b strings.Builder

	// ── Header ─────────────────────────────────────────────────────────
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colorMauve)).
		Render(" Model Picker ")
	b.WriteString(title)
	b.WriteString("\n\n")

	// ── Search bar ──────────────────────────────────────────────────────
	b.WriteString(mp.ti.View())
	b.WriteString("\n")

	// Count + refresh hint on one line
	if mp.ti.Value() != "" {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d matches", len(mp.filtered))))
	} else {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d models", len(mp.models))))
	}
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Faint(true).Render("Ctrl+R refresh"))
	b.WriteString("\n\n")

	// ── Effort / Intent Slider ───────────────────────────────────────────
	effortRow := mp.renderEffortSlider()
	b.WriteString(effortRow)
	b.WriteString("\n\n")

	// ── Resizable, row-based scrolling list ─────────────────────────────
	// budget is recomputed from the last known terminal size (see
	// listRowBudget) rather than a hardcoded constant, so a narrow/short
	// tmux split pane shrinks the list instead of clipping the modal.
	budget := mp.listRowBudget()
	rows := mp.buildRows()
	total := len(rows)

	if mp.scrollOffset > total {
		mp.scrollOffset = total
	}
	if mp.scrollOffset < 0 {
		mp.scrollOffset = 0
	}
	end := mp.scrollOffset + budget
	if end > total {
		end = total
	}
	window := rows[mp.scrollOffset:end]

	for _, row := range window {
		switch row.kind {
		case mpRowBlank:
			b.WriteString("\n")

		case mpRowHeader:
			providerStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorSapphire))
			header := " " + strings.ToUpper(row.provider)

			authLabel := providerAuthStatus(row.provider)
			if authLabel != "" {
				if strings.Contains(authLabel, "✓") {
					header += "  " + greenStyle.Render(authLabel)
				} else {
					header += "  " + redStyle.Render(authLabel)
				}
			}
			b.WriteString(providerStyle.Render(header))
			b.WriteString("\n")

		case mpRowItem:
			m := mp.filtered[row.itemIndex]
			cursor := "  "
			itemStyle := dimmedStyle
			if row.itemIndex == mp.cursor {
				cursor = "▸ "
				itemStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color(colorAccent)).
					Bold(true)
			}
			// Truncate long model IDs in narrow panes instead of letting
			// them wrap and blow out the fixed row budget.
			maxIDWidth := mp.width - 12
			id := truncateWithEllipsis(m.ID, maxIDWidth)
			fmt.Fprintf(&b, "%s%s", cursor, itemStyle.Render(id))
			b.WriteString("\n")
		}
	}

	// Pad blank lines so the body — and therefore the whole modal — never
	// changes height for a given terminal size, no matter how many
	// header/blank rows were in view. (It *does* change across a resize,
	// since budget itself is resize-driven — that's the point.)
	for i := len(window); i < budget; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// ── Footer ──────────────────────────────────────────────────────────
	footer := mutedStyle.Render("↑↓ navigate  ↵ select  ←→ effort  Esc close")
	b.WriteString(footer)

	borderColor := lipgloss.Color(colorMauve)
	content := b.String()

	return lipgloss.NewStyle().
		Width(mp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 3).
		Render(content)
}

// renderEffortSlider renders the interactive effort/intent slider.
// Visual layout: auto ───○─── low ────── medium ────── high
func (mp *ModelPickerModal) renderEffortSlider() string {
	levels := []struct {
		label string
		desc  string
	}{
		{"auto", "Let model decide"},
		{"low", "Fast-Track"},
		{"medium", "Hybrid"},
		{"high", "Senior Arch"},
	}

	trackLen := 4
	var b strings.Builder

	// Description line — tinted to match the selected level, so the
	// color reads as "how heavy is this effort" at a glance.
	effort := mp.CurrentEffort()
	levelStyle := effort.Style()
	desc := levelStyle.Render(effort.Description())
	b.WriteString("  " + mutedStyle.Render("Effort:") + " " + desc + "\n")

	// Slider track
	b.WriteString("  ")
	for i, lvl := range levels {
		if i == mp.effortIdx {
			b.WriteString(levelStyle.Bold(true).Render(lvl.label))
		} else {
			b.WriteString(dimmedStyle.Render(lvl.label))
		}
		if i < len(levels)-1 {
			b.WriteString(" ")
			for j := 0; j < trackLen; j++ {
				switch {
				case i == mp.effortIdx && j == 0:
					b.WriteString(levelStyle.Render("●"))
				case i < mp.effortIdx && j == trackLen-1:
					b.WriteString(levelStyle.Render("●"))
				default:
					b.WriteString(dimmedStyle.Render("─"))
				}
			}
			b.WriteString(" ")
		}
	}

	return b.String()
}

// truncateWithEllipsis shortens s to fit within max runes, replacing the
// tail with "…" when it doesn't fit. Operates on the raw (unstyled)
// string, so callers should truncate before applying lipgloss styling.
// A non-positive max is treated as "no room" and returns "".
func truncateWithEllipsis(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

func providerAuthStatus(provider string) string {
	if provider == "ollama" {
		return ""
	}
	if config.HasCredentials(provider) {
		return "[Logged In]"
	}
	return "[Needs Auth]"
}

func providerConfigsFromModel(models []llm.ModelInfo) map[string]string {
	seen := make(map[string]string)
	for _, m := range models {
		switch m.Provider {
		case "openrouter":
			seen["openrouter"] = ""
		case "ollama":
			seen["ollama"] = ""
		case "anthropic":
			seen["anthropic"] = ""
		case "openai":
			seen["openai"] = ""
		}
	}
	return seen
}
