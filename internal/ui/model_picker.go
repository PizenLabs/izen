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

// Description returns the provider-spec conformant label for the effort level.
// It strictly uses the official API values ("auto","low","medium","high") without
// fabricated marketing strings. Anthropic thinking budgets are mapped from these
// levels via the decision engine (low=1024, medium=4096, high=8192).
func (e EffortLevel) Description() string {
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
// model list body occupies when no terminal size is known yet.
const modelListLineBudget = 7

// modelListMinRows is the smallest the scrollable list body is ever
// allowed to shrink to, even in a very short split pane.
const modelListMinRows = 3

// modelPickerMaxListRows caps the viewport at 20 rows as per spec
// (min(70% height, 20 rows)).
const modelPickerMaxListRows = 20

// Strict height allocations per spec 2A.
const modelPickerHeaderAndSearchArea = 5
const modelPickerFooterArea = 3
const modelPickerEffortBlockArea = 4
const modelPickerBordersAndPadding = 2

// modelPickerChromeLines is the base chrome when Effort is VISIBLE.
const modelPickerChromeLines = modelPickerHeaderAndSearchArea + modelPickerFooterArea + modelPickerEffortBlockArea + modelPickerBordersAndPadding // 14
const modelPickerChromeLinesWithoutEffort = modelPickerHeaderAndSearchArea + modelPickerFooterArea + modelPickerBordersAndPadding                 // 10

// modelPickerMinWidth/Height are floors applied in SetSize. The height floor
// uses the hidden-effort chrome (smallest possible) so a pane that can fit
// the list without effort is not rejected as too small when effort is hidden.
const modelPickerMinWidth = 28
const modelPickerMinHeight = modelPickerChromeLinesWithoutEffort + modelListMinRows

type ModelPickerModal struct {
	ti       textinput.Model
	state    modelPickerState
	models   []llm.ModelInfo
	filtered []llm.ModelInfo
	cursor   int // row index into visibleRows (headers + items) — keyboard-first, headers selectable
	loading  bool
	errMsg   string
	width    int
	height   int
	registry *llm.ModelRegistry

	effortIdx    int // 0=auto, 1=low, 2=medium, 3=high
	scrollOffset int // row-based offset into visibleRows()

	// Collapsible provider groups: true = collapsed.
	collapsed map[string]bool

	// Interactive auth overlay (Ctrl+A).
	authOverlay  bool
	authProvider string
	authInput    textinput.Model
	authErr      string
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
	ti.Prompt = Icon.Chevron + " "
	ti.Placeholder = "type to filter models..."
	ti.CharLimit = 64
	ti.Width = 40
	ti.Focus()

	authTI := textinput.New()
	authTI.Prompt = "› "
	authTI.Placeholder = "API key..."
	authTI.CharLimit = 256
	authTI.EchoMode = textinput.EchoPassword
	authTI.Width = 40

	return &ModelPickerModal{
		ti:        ti,
		state:     mpLoading,
		registry:  llm.NewModelRegistry(),
		collapsed: make(map[string]bool),
		authInput: authTI,
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

// ── Helpers for selection / effort / auth ────────────────────────────────────

func (mp *ModelPickerModal) visibleRows() []mpRow {
	return mp.buildRows()
}

func (mp *ModelPickerModal) selectedRow() *mpRow {
	rows := mp.visibleRows()
	if len(rows) == 0 || mp.cursor < 0 || mp.cursor >= len(rows) {
		return nil
	}
	return &rows[mp.cursor]
}

func (mp *ModelPickerModal) selectedModel() *llm.ModelInfo {
	row := mp.selectedRow()
	if row == nil || row.kind != mpRowItem {
		return nil
	}
	if row.itemIndex < 0 || row.itemIndex >= len(mp.filtered) {
		return nil
	}
	return &mp.filtered[row.itemIndex]
}

func (mp *ModelPickerModal) selectedProvider() string {
	row := mp.selectedRow()
	if row == nil {
		return ""
	}
	if row.kind == mpRowHeader {
		return row.provider
	}
	if row.kind == mpRowItem {
		if m := mp.selectedModel(); m != nil {
			return m.Provider
		}
	}
	return ""
}

func (mp *ModelPickerModal) supportsEffortForSelected() bool {
	// Strict cursor context validation per spec: only RowTypeModel with
	// SupportsEffort == true qualifies. Provider headers are never effort-capable.
	row := mp.selectedRow()
	if row == nil || row.kind != mpRowItem {
		return false
	}
	if m := mp.selectedModel(); m != nil {
		// Prefer dynamic SupportsReasoning from registry discovery (API supported_parameters)
		if m.SupportsReasoning != nil {
			return *m.SupportsReasoning
		}
		return llm.ModelSupportsEffortWithProvider(m.Provider, m.ID)
	}
	return false
}

// shouldShowEffort reports whether the Effort UI should be allocated at all.
// When false the TUI hides the component with 0-row height and reclaims space
// for the model list viewport.
func (mp *ModelPickerModal) shouldShowEffort() bool {
	return mp.supportsEffortForSelected()
}

// chromeLines returns the current chrome budget based on Effort visibility.
func (mp *ModelPickerModal) chromeLines() int {
	if mp.shouldShowEffort() {
		return modelPickerChromeLines
	}
	return modelPickerChromeLinesWithoutEffort
}

func (mp *ModelPickerModal) needsAuthForSelected() bool {
	prov := mp.selectedProvider()
	if prov == "" {
		return false
	}
	if prov == "ollama" {
		return false
	}
	return !config.HasCredentials(prov)
}

func (mp *ModelPickerModal) openAuthOverlay() {
	prov := mp.selectedProvider()
	if prov == "" {
		// Fallback to first filtered model's provider if cursor on blank.
		if len(mp.filtered) > 0 {
			prov = mp.filtered[0].Provider
		}
	}
	if prov == "" || prov == "ollama" {
		return
	}
	mp.authOverlay = true
	mp.authProvider = prov
	mp.authInput.SetValue("")
	mp.authInput.Focus()
	mp.authErr = ""
}

func (mp *ModelPickerModal) closeAuthOverlay() {
	mp.authOverlay = false
	mp.authProvider = ""
	mp.authErr = ""
	mp.ti.Focus()
}

func (mp *ModelPickerModal) toggleCollapsed(provider string) {
	if provider == "" {
		return
	}
	mp.collapsed[provider] = !mp.collapsed[provider]
	// After toggling, clamp cursor to stay within bounds and keep scroll in view.
	rows := mp.visibleRows()
	if len(rows) == 0 {
		mp.cursor = 0
		mp.scrollOffset = 0
		return
	}
	if mp.cursor >= len(rows) {
		mp.cursor = len(rows) - 1
	}
	if mp.cursor < 0 {
		mp.cursor = 0
	}
	mp.clampScrollOffset()
}

// ── Update ───────────────────────────────────────────────────────────────────

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
		// Auth overlay has priority.
		if mp.authOverlay {
			switch msg.Type {
			case tea.KeyEscape:
				mp.closeAuthOverlay()
				return mp, nil
			case tea.KeyEnter:
				key := strings.TrimSpace(mp.authInput.Value())
				if key == "" {
					mp.authErr = "API key cannot be empty"
					return mp, nil
				}
				// Persist via config.SaveProviderToken; on success close overlay and refresh status.
				if err := config.SaveProviderToken(mp.authProvider, key); err != nil {
					mp.authErr = err.Error()
					return mp, nil
				}
				mp.closeAuthOverlay()
				return mp, nil
			default:
				var cmd tea.Cmd
				mp.authInput, cmd = mp.authInput.Update(msg)
				return mp, cmd
			}
		}

		switch msg.Type {
		case tea.KeyCtrlR:
			return mp, mp.RefreshModels(providerConfigsFromModel(mp.models))

		case tea.KeyCtrlA:
			if mp.needsAuthForSelected() {
				mp.openAuthOverlay()
			}
			return mp, nil

		case tea.KeyUp:
			if mp.cursor > 0 {
				mp.cursor--
			}
			mp.clampScrollOffset()
			return mp, nil

		case tea.KeyDown:
			rows := mp.visibleRows()
			if mp.cursor < len(rows)-1 {
				mp.cursor++
			}
			mp.clampScrollOffset()
			return mp, nil

		case tea.KeyLeft:
			if !mp.supportsEffortForSelected() {
				return mp, nil
			}
			if mp.effortIdx > 0 {
				mp.effortIdx--
			}
			return mp, nil

		case tea.KeyRight:
			if !mp.supportsEffortForSelected() {
				return mp, nil
			}
			if mp.effortIdx < 3 {
				mp.effortIdx++
			}
			return mp, nil

		case tea.KeyEnter:
			row := mp.selectedRow()
			if row == nil {
				return mp, nil
			}
			if row.kind == mpRowHeader {
				// Enter on header toggles collapse.
				mp.toggleCollapsed(row.provider)
				return mp, nil
			}
			if row.kind == mpRowItem {
				if m := mp.selectedModel(); m != nil {
					selected := *m
					effort := mp.CurrentEffort()
					return mp, func() tea.Msg {
						return modelSelectedMsg{model: selected, effort: effort}
					}
				}
			}
			return mp, nil

		case tea.KeyEscape:
			return mp, nil

		case tea.KeyTab:
			// Tab toggles collapse for selected provider (header or item's provider).
			prov := mp.selectedProvider()
			if prov != "" {
				mp.toggleCollapsed(prov)
			}
			return mp, nil

		default:
			// Space is reserved for the search filter input and MUST NOT toggle collapse.
			// Tab is the sole keybinding for collapsing provider groups (see case tea.KeyTab).
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
				var cmd tea.Cmd
				mp.ti, cmd = mp.ti.Update(msg)
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
	// Keep auth input in sync
	authW := w - 16
	if authW < 10 {
		authW = 10
	}
	mp.authInput.Width = authW

	mp.clampScrollOffset()
}

// listRowBudget returns the strict AvailableListRows per spec 2A.
// ModalHeight is fixed (min(0.75*termHeight,26)); AvailableListRows is
// ModalHeight - (HeaderAndSearchArea + FooterArea + BordersAndPadding + EffortBlockArea?).
// This ensures the list strictly terminates before the footer boundary and
// the outer modal never jitters when hovering between reasoning/non-reasoning.
func (mp *ModelPickerModal) listRowBudget() int {
	if mp.height <= 0 {
		return modelListLineBudget
	}
	available := mp.height - mp.chromeLines()
	if available < modelListMinRows {
		available = modelListMinRows
	}
	if available > modelPickerMaxListRows {
		available = modelPickerMaxListRows
	}
	// `visibleItems = items[scrollOffset : min(scrollOffset+AvailableListRows, len(items))]`
	// is enforced in renderList via budget truncation.
	return available
}

// AvailableListRows is the public strict budget per spec, alias for listRowBudget.
func (mp *ModelPickerModal) AvailableListRows() int {
	return mp.listRowBudget()
}

// ── Row model ────────────────────────────────────────────────────────────

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
				// Only insert blank separator if previous provider wasn't collapsed
				// (collapsed groups have no item rows, so blank would be spurious).
				if !mp.collapsed[prevProvider] {
					rows = append(rows, mpRow{kind: mpRowBlank})
				}
			}
			rows = append(rows, mpRow{kind: mpRowHeader, provider: m.Provider})
			prevProvider = m.Provider
		}
		if mp.collapsed[m.Provider] {
			continue
		}
		rows = append(rows, mpRow{kind: mpRowItem, itemIndex: i})
	}
	return rows
}

func (mp *ModelPickerModal) clampScrollOffset() {
	rows := mp.buildRows()
	total := len(rows)
	if total == 0 {
		mp.scrollOffset = 0
		if mp.cursor < 0 {
			mp.cursor = 0
		}
		return
	}
	if mp.cursor >= total {
		mp.cursor = total - 1
	}
	if mp.cursor < 0 {
		mp.cursor = 0
	}
	budget := mp.listRowBudget()

	if mp.cursor < mp.scrollOffset {
		mp.scrollOffset = mp.cursor
	} else if mp.cursor >= mp.scrollOffset+budget {
		mp.scrollOffset = mp.cursor - budget + 1
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
	query := mp.ti.Value()
	if query == "" {
		mp.filtered = mp.models
		// Reset cursor to first row, keep collapsed state as user left it.
		mp.cursor = 0
		mp.scrollOffset = 0
		mp.clampScrollOffset()
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

	// Auto-expand groups containing search matches.
	matchedProviders := make(map[string]bool)
	for _, m := range results {
		matchedProviders[m.Provider] = true
	}
	for prov := range matchedProviders {
		mp.collapsed[prov] = false
	}
	// Optionally collapse groups with no matches? They have no rows anyway, so no effect.

	mp.cursor = 0
	mp.scrollOffset = 0
	mp.clampScrollOffset()
}

func (mp *ModelPickerModal) View() string {
	if mp.loading {
		return mp.renderLoading()
	}
	if mp.state == mpErr {
		return mp.renderError()
	}
	if mp.authOverlay {
		return mp.renderAuthOverlay()
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

func (mp *ModelPickerModal) renderAuthOverlay() string {
	var b strings.Builder
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colorMauve)).
		Render(fmt.Sprintf(" Authenticate %s ", strings.ToUpper(mp.authProvider)))
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render(fmt.Sprintf("Provider %q needs credentials.", mp.authProvider)))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Enter API key (input hidden) — Enter to save, Esc to cancel"))
	b.WriteString("\n\n")
	b.WriteString(mp.authInput.View())
	b.WriteString("\n")
	if mp.authErr != "" {
		b.WriteString(redStyle.Render("  " + mp.authErr))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	footer := mutedStyle.Render("↵ save  Esc cancel")
	b.WriteString(footer)

	content := b.String()
	return lipgloss.NewStyle().
		Width(mp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorOrange)).
		Padding(1, 3).
		Render(content)
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

	// ── Effort / Intent Slider (hidden when not applicable — 0-row height) ───────
	if mp.shouldShowEffort() {
		effortRow := mp.renderEffortSlider()
		b.WriteString(effortRow)
		b.WriteString("\n\n")
	}

	// ── Resizable, row-based scrolling list ─────────────────────────────
	budget := mp.listRowBudget()
	rows := mp.visibleRows()
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

	query := mp.ti.Value()
	for idx, row := range window {
		absRow := mp.scrollOffset + idx
		isSelected := absRow == mp.cursor
		switch row.kind {
		case mpRowBlank:
			b.WriteString("\n")

		case mpRowHeader:
			collapsed := mp.collapsed[row.provider]
			arrow := "▼"
			if collapsed {
				arrow = "▶"
			}
			providerStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorSapphire))
			// Highlight header when selected (keyboard-first).
			if isSelected {
				providerStyle = providerStyle.Background(lipgloss.Color(colorSurface)).Foreground(lipgloss.Color(colorAccent))
			}
			prefix := " "
			if isSelected {
				prefix = Icon.Chevron + " "
			}
			header := prefix + arrow + " " + strings.ToUpper(row.provider)
			if collapsed {
				// Count hidden models for hint
				hidden := 0
				for _, m := range mp.filtered {
					if m.Provider == row.provider {
						hidden++
					}
				}
				if hidden > 0 {
					header += mutedStyle.Render(fmt.Sprintf(" (%d)", hidden))
				}
			}

			authLabel := providerAuthStatus(row.provider)
			if authLabel != "" {
				if strings.Contains(authLabel, "✓") || strings.Contains(authLabel, "Logged") {
					header += "  " + greenStyle.Render(authLabel)
				} else {
					header += "  " + redStyle.Render(authLabel)
				}
			}
			if collapsed {
				header += dimmedStyle.Render("  [collapsed]")
			}
			b.WriteString(providerStyle.Render(header))
			b.WriteString("\n")

		case mpRowItem:
			m := mp.filtered[row.itemIndex]
			cursor := "  "
			itemStyle := dimmedStyle
			if isSelected {
				cursor = Icon.Chevron + " "
				itemStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color(colorAccent)).
					Bold(true)
			}
			// Build line with badges: "model-id  •  200k  [Vision] [Tools]"
			// Use ModelInfo.ContextWindow when available (accurate API value: 128k for DeepSeek),
			// fallback to heuristic only if registry did not populate it.
			ctxWindow := m.ContextWindow
			if ctxWindow == 0 {
				ctxWindow = llm.ContextWindowFor(m.ID)
			}
			badgeCtx := llm.FormatContextWindow(ctxWindow)
			caps := llm.ModelCapabilities(m.ID)

			// Assemble badge suffix (un-styled for width calc)
			var badgeParts []string
			if badgeCtx != "" {
				badgeParts = append(badgeParts, badgeCtx)
			}
			for _, c := range caps {
				badgeParts = append(badgeParts, "["+c+"]")
			}
			badgeStr := ""
			if len(badgeParts) > 0 {
				badgeStr = "  •  " + strings.Join(badgeParts, " ")
			}

			// Truncation: ensure line fits within mp.width.
			// Reserve: cursor (2) + padding/border slack (approx 8) + badges
			maxIDWidth := mp.width - 14 - lipgloss.Width(badgeStr)
			if maxIDWidth < 8 {
				maxIDWidth = 8
			}
			rawID := m.ID
			truncatedRaw := truncateWithEllipsis(rawID, maxIDWidth)
			// Re-apply highlight on truncated raw
			styledID := truncatedRaw
			if query != "" {
				styledID = highlightMatch(truncatedRaw, query)
			}

			// Combine: styled ID + muted badges
			var line string
			if isSelected {
				accentID := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true).Render(styledID)
				line = accentID + mutedStyle.Render(badgeStr)
			} else {
				line = itemStyle.Render(styledID) + mutedStyle.Render(badgeStr)
			}

			fmt.Fprintf(&b, "%s%s", cursor, line)
			b.WriteString("\n")
		}
	}

	// Pad blank lines so the body never changes height for a given terminal size.
	for i := len(window); i < budget; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// ── Footer ──────────────────────────────────────────────────────────
	footerParts := []string{"↑↓ navigate", "↵ select", "Esc close"}
	if mp.supportsEffortForSelected() {
		footerParts = append([]string{"←→ effort"}, footerParts...)
	}
	// Tab hint - Space is reserved for search input
	footerParts = append(footerParts, "Tab collapse")
	if mp.needsAuthForSelected() {
		footerParts = append(footerParts, lipgloss.NewStyle().Foreground(lipgloss.Color(colorOrange)).Bold(true).Render("Ctrl+A auth"))
	} else {
		footerParts = append(footerParts, "Ctrl+R refresh")
	}
	footer := mutedStyle.Render(strings.Join(footerParts, "  •  "))
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

// highlightMatch wraps occurrences of query in the text with an underline accent style.
// It operates on plain strings (no ANSI) and returns a lipgloss-styled string.
func highlightMatch(text, query string) string {
	if query == "" || text == "" {
		return text
	}
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(query)
	idx := strings.Index(lowerText, lowerQuery)
	if idx < 0 {
		return text
	}
	// Highlight only first occurrence for simplicity; handles rune-safe slicing via rune indices.
	runes := []rune(text)
	lowerRunes := []rune(lowerText)
	queryRunes := []rune(lowerQuery)
	qLen := len(queryRunes)
	// Find rune index
	rIdx := -1
	for i := 0; i+qLen <= len(lowerRunes); i++ {
		match := true
		for j := 0; j < qLen; j++ {
			if lowerRunes[i+j] != queryRunes[j] {
				match = false
				break
			}
		}
		if match {
			rIdx = i
			break
		}
	}
	if rIdx < 0 {
		return text
	}
	before := string(runes[:rIdx])
	matched := string(runes[rIdx : rIdx+qLen])
	after := string(runes[rIdx+qLen:])
	hl := lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Bold(true).Underline(true).Render(matched)
	return before + hl + after
}

// renderEffortSlider renders the interactive effort/intent slider.
// Caller guarantees shouldShowEffort() == true; the component is hidden
// otherwise with 0-row height so vertical space is reclaimed for the list.
// It also displays real-time token info per spec: "Effort: medium (Thinking Budget: 16k | Total Max: 20k tokens)"
func (mp *ModelPickerModal) renderEffortSlider() string {
	levels := []struct {
		label string
		desc  string
	}{
		{"auto", "auto"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
	}

	trackLen := 4
	var b strings.Builder

	effort := mp.CurrentEffort()
	levelStyle := effort.Style()
	// Base effort label
	baseDesc := effort.Description()
	// Enrich with token info via TokenManager when model is known
	if m := mp.selectedModel(); m != nil {
		tm := llm.NewTokenManager()
		info := tm.InfoFor(m.Provider, m.ID, effort.String())
		thinkingStr := llm.FormatTokenCount(info.ThinkingBudget)
		totalStr := llm.FormatTokenCount(info.TotalMax)
		if effort == EffortAuto {
			// Auto has no thinking budget, show total only
			if info.TotalMax > 0 {
				baseDesc = fmt.Sprintf("%s (Total Max: %s tokens)", baseDesc, totalStr)
			}
		} else {
			if info.ThinkingBudget > 0 && info.TotalMax > 0 {
				baseDesc = fmt.Sprintf("%s (Thinking Budget: %s | Total Max: %s tokens)", baseDesc, thinkingStr, totalStr)
			} else if info.TotalMax > 0 {
				baseDesc = fmt.Sprintf("%s (Total Max: %s tokens)", baseDesc, totalStr)
			}
		}
	}
	desc := levelStyle.Render(baseDesc)
	b.WriteString("  " + mutedStyle.Render("Effort:") + " " + desc + "\n")

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
					b.WriteString(levelStyle.Render(Icon.Check))
				case i < mp.effortIdx && j == trackLen-1:
					b.WriteString(levelStyle.Render(Icon.Check))
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
// tail with "…" when it doesn't fit.
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
		case "gemini":
			seen["gemini"] = ""
		case "groq":
			seen["groq"] = ""
		}
	}
	return seen
}
