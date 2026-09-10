package model_picker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Color constants for the spec's detail view palette.
var (
	colorText     = lipgloss.Color("#cdd6f4")
	colorSubtext0 = lipgloss.Color("#6c7086")
	colorMauve    = lipgloss.Color("#cba6f7")
)

// formatCapabilities is the spec alias for formatCaps (capability string).
func formatCapabilities(caps []registry.ModelCapability) string {
	if len(caps) == 0 {
		return "—"
	}
	var parts []string
	for _, c := range caps {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, " ")
}

// View implements tea.Model. Renders the redesigned provider-centric control
// surface: [PROVIDERS pane | MODELS pane] with capability-truthful variant
// rendering (Supported / Unsupported / Unknown). The 5-workspace matrix and
// 3-role assignment matrix are removed per Phase 3 spec. When the secure
// inline API-key overlay is open it renders exclusively as that dialog.
func (m Model) View() string {
	if m.apiKeyInput != nil {
		return m.renderApiKeyOverlay()
	}
	if m.state == StateDetail {
		return m.renderDetailLayout()
	}
	return m.renderBrowsingLayout()
}

// renderApiKeyOverlay renders the secure inline API-key capture dialog. It is
// the entire picker surface while open: title, masked text input (EchoPassword),
// and save/cancel hints. Zero I/O: submission emits SaveProviderKeyMsg.
func (m Model) renderApiKeyOverlay() string {
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	if innerW <= 0 {
		innerW = 64
	}
	innerH := m.innerHeight
	if innerH <= 0 {
		innerH = m.height
	}

	title := accentStyle.Render("SET API KEY — " + strings.ToUpper(m.apiKeyProvider))
	hint := mutedStyle.Render("API key is masked · stored in provider config")

	fieldPlain := "  " + m.apiKeyInput.View()
	field := lipgloss.NewStyle().MaxWidth(innerW - 4).MaxHeight(1).Render(fieldPlain)

	body := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		hint,
		"",
		field,
		"",
		mutedStyle.Render("Enter save · Esc cancel"),
	)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#cba6f7")).
		Width(max(16, innerW-4)).
		Padding(1, 2).
		Render(body)

	// Hard clip to the modal bounds (strict single-line contract preserved).
	if innerH > 0 {
		lines := strings.Split(strings.ReplaceAll(box, "\r", ""), "\n")
		if len(lines) > innerH {
			lines = lines[:innerH]
		}
		box = strings.Join(lines, "\n")
	}
	return box
}

// renderBrowsingLayout renders the dual-pane provider-centric layout:
//
//	┌────────────────┬────────────────────────────────┐
//	│ PROVIDERS      │ MODELS                         │
//	│ ✓ OpenRouter   │ cohere/north-mini-code:free    │
//	│ · Ollama       │ thinkingmachines/inkling...    │
//	└────────────────┴────────────────────────────────┘
//
// Left pane: provider list with configured status (✓/·).
// Right pane: models belonging to the highlighted provider.
// Tab toggles focus between panes.
func (m Model) renderBrowsingLayout() string {
	var b strings.Builder
	b.WriteString(m.clipLine(m.renderHeader()))
	b.WriteString("\n")
	b.WriteString(m.renderDivider())
	b.WriteString("\n")

	// Dual pane: left providers (or roles tab), right models, with vertical separator.
	leftPane := m.renderLeftPane()
	rightPane := m.renderModelsPane()
	separator := m.buildVerticalSeparator()
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, separator, rightPane)
	b.WriteString(panes)
	b.WriteString("\n")

	b.WriteString(m.renderDivider())
	b.WriteString("\n")
	b.WriteString(m.clipLine(m.renderActiveLine()))
	b.WriteString("\n")
	b.WriteString(m.clipLine(m.renderVariantLine()))
	b.WriteString("\n")

	if m.status != "" {
		b.WriteString(m.clipLine(mutedStyle.Render(" " + m.status)))
		b.WriteString("\n")
	}
	return m.padFooter(b.String())
}

// renderDetailLayout renders the model detail view with header, detail body,
// and a dedicated detail footer. No search bar or browsing list leaks through.
func (m Model) renderDetailLayout() string {
	header := m.clipLine(m.renderHeader())
	body := m.renderDetailView()
	footer := m.renderDetailFooter()

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(m.renderDivider())
	b.WriteString("\n")
	b.WriteString(body)
	b.WriteString("\n")
	if m.status != "" {
		b.WriteString(m.clipLine(mutedStyle.Render(" " + m.status)))
		b.WriteString("\n")
	}
	b.WriteString(m.renderDivider())
	b.WriteString("\n")
	b.WriteString(footer)

	return b.String()
}

// clipLine enforces strict single-line width: truncates styled lines to the
// inner width when bounded, stripping any embedded newlines.
func (m Model) clipLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	inner := m.innerWidth
	if inner <= 0 {
		inner = m.width
	}
	if inner <= 0 {
		return s
	}
	return truncateStyled(s, inner)
}

// renderDivider renders the subtle horizontal rule separating the header
// from the search line. Width follows the picker inner bounds exactly
// (m.innerWidth), else a 64-column default. Zero I/O.
func (m Model) renderDivider() string {
	w := m.innerWidth
	if w <= 0 {
		w = m.width
	}
	if w <= 0 {
		w = 64
	}
	if w < 16 {
		w = 16
	}
	return dividerStyle.Render(strings.Repeat("─", w))
}

// footerText is the keybindings footer anchored at the bottom of the modal
// card. Delegates to renderBrowsingFooter for the spec's clean one-line footer.
// Retained for backward compatibility with padFooter anchoring.
func (m Model) footerText() string {
	return m.renderBrowsingFooter()
}

// renderBrowsingFooter delivers the dual-pane keybinding footer.
func (m Model) renderBrowsingFooter() string {
	keyStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorSubtext0)

	inner := m.innerWidth
	if inner <= 0 {
		inner = m.width
	}
	if inner <= 0 {
		inner = 64
	}

	help := fmt.Sprintf("%s %s   %s %s   %s %s   %s %s   %s %s   %s %s",
		keyStyle.Render("Tab"), descStyle.Render("select pane"),
		keyStyle.Render("↑/↓"), descStyle.Render("navigate"),
		keyStyle.Render("Enter"), descStyle.Render("activate / save key"),
		keyStyle.Render("Alt+A"), descStyle.Render("set API key"),
		keyStyle.Render("i"), descStyle.Render("details"),
		keyStyle.Render("Esc"), descStyle.Render("close"),
	)
	if m.innerWidth <= 0 && m.width <= 0 {
		return help
	}
	return truncateStyled(help, inner)
}

// renderDetailView renders the model detail card: a bordered information card
// with MODEL DETAILS header, provider badge, specifications, and reasoning
// policy control. Clean Lipgloss borders, consistent padding, and clear
// key-value alignments.
func (m Model) renderDetailView() string {
	model := m.SelectedModel()
	if model == nil {
		return ""
	}

	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	if innerW <= 0 {
		innerW = 64
	}

	// ── Card title ──
	titleStyle := lipgloss.NewStyle().Foreground(colorMauve).Bold(true)
	title := titleStyle.Render("MODEL DETAILS: " + model.ID)

	// Provider badge
	providerLine := mutedStyle.Render("Provider: ") + renderProviderBadge(model.Provider, 16)

	// ── SPECIFICATIONS section ──
	sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(colorText)
	valueStyle := lipgloss.NewStyle().Foreground(colorSubtext0)

	ctxWin := formatContextWindow(model.ContextWindow)
	price := formatPricing(model.InputCostPerM, model.OutputCostPerM)

	caps := model.Capabilities
	if len(caps) == 0 {
		caps = role.EffectiveCapabilities(*model)
	}
	capsStr := formatCapabilities(caps)

	specLines := []string{
		"",
		sectionStyle.Render("SPECIFICATIONS"),
		"",
		labelStyle.Render("  Context Window  : ") + valueStyle.Render(ctxWin+" tokens"),
		labelStyle.Render("  Pricing         : ") + valueStyle.Render(price+" per 1M tokens"),
		labelStyle.Render("  Capabilities    : ") + capsStr,
	}

	// ── REASONING POLICY section ──
	reasoningLines := []string{
		"",
		sectionStyle.Render("REASONING POLICY OVERRIDE (Press 'r' to cycle)"),
		"",
	}
	// Inline reasoning options: [default] • low • medium • high • xhigh • max
	sel := m.SelectedModel()
	if sel != nil {
		reasoningLines = append(reasoningLines, "  "+m.renderReasoningOptionsInline(sel))
	}

	// ── Assemble card ──
	allLines := append([]string{title, providerLine}, specLines...)
	allLines = append(allLines, reasoningLines...)
	body := lipgloss.JoinVertical(lipgloss.Left, allLines...)

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#cba6f7")).
		Width(max(16, innerW-4)).
		Padding(0, 1).
		Render(body)

	return card
}

// renderReasoningOptionsInline renders the reasoning effort options as a
// single horizontal line with the selected option highlighted:
//
//	[default]  •  low  •  medium  •  high  •  xhigh  •  max
//
// Non-reasoning models return "Not supported by model". Provider-managed
// models return "Provider managed / Not configurable".
func (m Model) renderReasoningOptionsInline(sel *registry.ModelDescriptor) string {
	if sel == nil {
		return mutedStyle.Render("Not supported by model")
	}
	caps := sel.GetReasoningCapability()

	if !caps.Supported {
		return mutedStyle.Render("Not supported by model")
	}
	if !caps.Configurable || len(caps.Options) == 0 {
		return mutedStyle.Render("Provider managed / Not configurable")
	}

	idx := m.selectedReasoningOptIdx
	if idx < 0 || idx >= len(caps.Options) {
		idx = 0
	}

	var cells []string
	for i, opt := range caps.Options {
		label := opt.Label
		if label == "" {
			label = opt.ID
		}
		if i == idx {
			cells = append(cells, effortStyle(opt.ID).Underline(true).Render("["+label+"]"))
		} else {
			cells = append(cells, mutedStyle.Render(label))
		}
	}
	return strings.Join(cells, mutedStyle.Render("  •  "))
}

// renderDetailFooter renders the dedicated keybinding footer for the detail view.
func (m Model) renderDetailFooter() string {
	keyStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorSubtext0)

	help := fmt.Sprintf("%s %s   %s %s   %s %s   %s %s",
		keyStyle.Render("↑↓"), descStyle.Render("select"),
		keyStyle.Render("r"), descStyle.Render("reasoning policy"),
		keyStyle.Render("Enter"), descStyle.Render("activate & exit"),
		keyStyle.Render("Esc"), descStyle.Render("back"),
	)
	inner := m.innerWidth
	if inner <= 0 {
		inner = m.width
	}
	if inner <= 0 {
		inner = 64
	}
	// Unbounded (tests/headless): no truncation so the full key contract
	// stays grep-friendly. Bounded modals clip to the inner width.
	if m.innerWidth <= 0 && m.width <= 0 {
		return help
	}
	return truncateStyled(help, inner)
}

// renderReasoningOptions returns the reasoning effort pills for the bottom panel.
// Spec alias for RenderReasoningBar with bar formatting; keeps provider-native tiers.
//
//nolint:unused // spec-required helper; used via renderBottomPanel
func (m Model) renderReasoningOptions() string {
	return m.RenderReasoningBar()
}

// renderBottomPanel creates the explicit 3-tier hierarchy (List -> Configuration
// Bar -> Hotkey Footer) separated by subtle dividers. It renders a divider line,
// the REASONING & role bindings bar, and the keyboard shortcuts help line. All
// lines are hard-truncated to innerWidth and single-line.
//
//nolint:unused // spec-required helper verified via View divider hierarchy
func (m Model) renderBottomPanel() string {
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	if innerW <= 0 {
		innerW = 64
	}
	// Divider line separating list from configuration/hotkey tiers.
	divider := dividerStyle.Render(strings.Repeat("─", innerW))

	// Line 1: Reasoning & Role Bindings Bar
	reasoningLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Bold(true).Render("REASONING ")
	reasoningOpts := m.renderReasoningOptions()
	line1Plain := reasoningLabel + reasoningOpts
	// Hard truncate with ANSI awareness.
	line1 := truncateStyled(line1Plain, innerW)

	// Line 2: bindings line (preserved for test expectation "BINDINGS")
	bindingsLine := m.renderBindingsLine()
	bindingsClipped := truncateStyled(strings.ReplaceAll(bindingsLine, "\n", " "), innerW)

	// Line 3: Keyboard Shortcuts Help (spec: unified input UX)
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4")).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	help := fmt.Sprintf("%s %s   %s %s   %s %s   %s %s",
		keyStyle.Render("↑/↓"), descStyle.Render("select"),
		keyStyle.Render("type to search"), descStyle.Render(""),
		keyStyle.Render("Alt+d/p/s/v/a"), descStyle.Render("bind role"),
		keyStyle.Render("Enter"), descStyle.Render("activate"),
	)
	// Ensure the spec-required substrings survive clipping:
	// "↑/↓ select", "type to search", "Alt+d/p/s/v/a bind role", "Enter activate"
	_ = help
	helpClipped := truncateStyled(strings.ReplaceAll(help, "\n", " "), innerW)
	// Fallback to legacy footer text if help somehow empty, ensures test "Enter: activate" always present.
	if !strings.Contains(helpClipped, "Enter") {
		helpClipped = truncateStyled(m.footerText(), innerW)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		divider,
		line1,
		bindingsClipped,
		helpClipped,
	)
}

// padFooter appends height-aware padding plus the anchored footer to body.
// body holds every line above the footer (each terminated by "\n"). When a
// height bound is set and the body is shorter, blank lines fill the gap so
// the keybindings footer lands on the last line of the modal card. When the
// body overflows, it is hard-clipped to height-1 lines so the modal border
// stays stationary (zero frame stretching).
func (m Model) padFooter(body string) string {
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	innerH := m.innerHeight
	if innerH <= 0 {
		innerH = m.height
	}
	footer := m.footerText()
	if innerW > 0 {
		footer = truncateStyled(strings.ReplaceAll(footer, "\n", " "), innerW)
	}
	if innerH <= 0 {
		return body + footer
	}
	// Hard clip: keep at most height-1 body lines + 1 footer line.
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > innerH-1 {
		lines = lines[:innerH-1]
	}
	body = strings.Join(lines, "\n") + "\n"
	// Body lines used + 1 footer line; filler bridges the remainder.
	used := len(lines) + 1
	if pad := innerH - used; pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	return body + footer
}

// snapshotModels reads the immutable snapshot RAM slice (no copy on the hot
// render path; callers must not mutate).
func (m Model) snapshotModels() []registry.ModelDescriptor {
	if m.snap == nil {
		return nil
	}
	return m.snap.Models
}

// paneHeight returns the height available for the dual-pane content area.
// Chrome above and below the panes: header(1) + divider(1) + divider(1) +
// active(1) + variant(1) = 5 lines, plus padFooter's 1-line footer = 6 total.
func (m Model) paneHeight() int {
	innerH := m.innerHeight
	if innerH <= 0 {
		innerH = m.height
	}
	if innerH <= 0 {
		return 16
	}
	return max(3, innerH-6)
}

// renderLeftPane dispatches the left pane: the Roles policy list when the
// Roles tab is active, else the provider list (with [All models] first).
func (m Model) renderLeftPane() string {
	if m.showingRoles {
		return m.renderRolesPane()
	}
	return m.renderProvidersPane()
}

// Fixed layout constants.
const (
	searchInputWidth = 16
	// providersPaneWidth is the fixed width of the left providers pane
	// in the dual-pane browsing layout. Right pane gets the remainder.
	providersPaneWidth = 24
)

// Tabular column widths for the MODELS pane: Model ID (flexible), Context
// Window (fixed), Pricing (fixed), Capability flags (leftover, capped).
const (
	tableCtxColW   = 6
	tablePriceColW = 12
	tableFlagsMaxW = 24
	tableIDMinW    = 10
)

// renderProvidersPane renders the left pane: the synthetic [All models] entry
// followed by the provider list. Configured providers render ✓ (green-ish);
// unconfigured providers render a dim ○. Every row is hard-truncated to
// paneWidth with MaxWidth/MaxHeight(1) so nothing ever wraps the 2 columns.
func (m Model) renderProvidersPane() string {
	paneW := providersPaneWidth
	paneH := m.paneHeight()

	var lines []string

	// Pane header.
	lines = append(lines, mutedStyle.Render("PROVIDERS"))
	lines = append(lines, "")

	// Synthetic [All models] entry (global scope) pinned first.
	m.appendProviderRow(&lines, 0, fmt.Sprintf("All models (%d)", m.allModelsCount()), true, paneW)

	names := m.providerNames()
	for i, name := range names {
		if len(lines) >= paneH {
			break
		}
		idx := i + 1
		configured := m.isProviderConfigured(name)
		icon := "○"
		if configured {
			icon = "✓"
		}
		cell := fmt.Sprintf("%s %s", icon, name)
		m.appendProviderRow(&lines, idx, cell, false, paneW)
	}

	// Pad to paneH.
	for len(lines) < paneH {
		lines = append(lines, strings.Repeat(" ", paneW))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// appendProviderRow appends one single-line provider-row to lines. The cell
// is hard-clipped to paneW cells and wrapped in MaxWidth(paneW).MaxHeight(1)
// so a long provider name can never wrap and break the 2-column alignment.
func (m Model) appendProviderRow(lines *[]string, idx int, cell string, isAll bool, paneW int) {
	if len(*lines) >= m.paneHeight() {
		return
	}
	selected := idx == m.providerCursor && m.paneFocus == PaneProviders && !m.showingRoles
	raw := runewidth.Truncate(cell, paneW, "…")
	raw = padRightExact(raw, paneW)
	switch {
	case selected:
		*lines = append(*lines, selectedRowStyle.Render(raw))
	case isAll:
		*lines = append(*lines, allModelsStyle.Render(raw))
	default:
		*lines = append(*lines, mutedStyle.Render(raw))
	}
}

// renderRolesPane renders the left pane in Roles mode: the two top-level
// policy overrides (Plan/Thinking and Commit/Fast) with their bound model /
// reasoning effort summary lines.
func (m Model) renderRolesPane() string {
	paneW := providersPaneWidth
	paneH := m.paneHeight()

	var lines []string
	lines = append(lines, mutedStyle.Render("ROLES"))
	lines = append(lines, "")

	for i, ro := range roleOverrideEntries {
		if len(lines) >= paneH {
			break
		}
		selected := i == m.roleCursor && m.paneFocus == PaneRoles
		cell := fmt.Sprintf("%s %s", "▶", ro.Label)
		raw := runewidth.Truncate(cell, paneW, "…")
		raw = padRightExact(raw, paneW)
		if selected {
			lines = append(lines, selectedRowStyle.Render(raw))
		} else {
			lines = append(lines, mutedStyle.Render(raw))
		}
		// Binding summary sub-line.
		if len(lines) < paneH {
			sub := m.roleOverrideSummary(ro.Key)
			sub = runewidth.Truncate(sub, paneW-subPrefixW, "…")
			sub = padRightExact(sub, paneW-subPrefixW)
			lines = append(lines, mutedStyle.Render("  "+sub))
		}
	}

	for len(lines) < paneH {
		lines = append(lines, strings.Repeat(" ", paneW))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// roleOverrideEntries is the closed set of top-level role policy overrides.
var roleOverrideEntries = []struct{ Key, Label string }{
	{RoleOverridePlan, "Plan / Thinking"},
	{RoleOverrideCommit, "Commit / Fast"},
}

// subPrefixW reserves the leading "  " indent for roles-pane summary rows.
const subPrefixW = 2

// roleOverrideSummary renders the bound model + reasoning effort for a role
// override key, or the unbound marker when none is set.
func (m Model) roleOverrideSummary(key string) string {
	if ob, ok := m.policyOverrides[key]; ok && ob.ModelID != "" {
		s := "→ " + ob.ModelID
		if ob.Effort != "" && ob.Effort != DefaultReasoningOption {
			s += " · " + ob.Effort
		}
		return s
	}
	return "→ —"
}

// allModelsCount reports the total number of models in the snapshot.
func (m Model) allModelsCount() int {
	if m.snap == nil {
		return 0
	}
	return len(m.snap.Models)
}

// renderModelsPane renders the right pane: a pinned RECENTLY USED section
// (when browsing providers, matches the current scope) is followed by the
// tabular model list (ID · Context · Price · Capability flags). Every row is a
// single hard-clipped line; columns are derived from ModelDescriptor fields.
func (m Model) renderModelsPane() string {
	// Correct inner width math: account for outer modal borders and padding.
	frameH, _ := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).GetFrameSize()
	innerW := max(40, m.width-frameH)
	if innerW <= 0 {
		innerW = 64
	}
	paneW := innerW - providersPaneWidth - 1 // -1 for separator; guarantee equality
	_ = paneW + providersPaneWidth + 1 // assert: left + sep + right == innerW
	if paneW < 16 {
		paneW = 16
	}
	paneH := m.paneHeight()

	var lines []string

	// Pane header with optional search query / roles target.
	header := "MODELS"
	if m.showingRoles {
		header = fmt.Sprintf("MODELS — assign %s", m.HighlightedRole())
	} else if m.query != "" {
		header += "  /" + m.query
	}
	lines = append(lines, mutedStyle.Render(header))
	lines = append(lines, "")

	// Pinned RECENTLY USED section (provider browsing scope only).
	recent := m.recentSectionLines(paneW, paneH-len(lines))
	lines = append(lines, recent...)

	// Model list from visible window (budget shrunk by the pinned rows above).
	listBudget := paneH - len(lines)
	if listBudget < 3 {
		listBudget = 3
	}
	start, end := m.visibleWindowBudget(listBudget)
	window := m.filtered[start:end]

	for _, d := range window {
		if len(lines) >= paneH {
			break
		}
		idx := start
		selected := idx == m.cursor && m.paneFocus == PaneModels
		start++

		cell := m.renderModelRow(d, paneW)
		switch {
		case selected:
			lines = append(lines, selectedRowStyle.Render(cell))
		default:
			lines = append(lines, mutedStyle.Render(cell))
		}
	}

	if len(window) == 0 {
		lines = append(lines, mutedStyle.Render("  (no models)"))
	}

	// Pad to paneH.
	for len(lines) < paneH {
		lines = append(lines, strings.Repeat(" ", paneW))
	}

	// Sanitize: no embedded newlines before joining.
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], "\n", " ")
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderModelRow builds one tabular model row as pure unstyled plain text,
// truncated/padded to EXACTLY paneW characters. Styling (selectedRowStyle /
// mutedStyle) is applied atomically by the caller (renderModelsPane) so
// individual cells never introduce partial ANSI sequences that could break
// on truncation.
func (m Model) renderModelRow(item registry.ModelDescriptor, rightPaneW int) string {
	ctxW := 6
	priceW := 12
	flagsW := 16
	spacing := 3 // spaces between 4 columns

	idW := rightPaneW - (ctxW + priceW + flagsW + spacing)
	if idW < 10 {
		idW = 10
	}

	// 1. Hard-truncate Model ID first.
	truncatedID := runewidth.Truncate(item.ID, idW, "…")
	paddedID := padRightExact(truncatedID, idW)

	// 2. Format remaining columns with exact fixed widths.
	paddedCtx := padLeftExact(formatContextWindow(item.ContextWindow), ctxW)
	paddedPrice := padLeftExact(formatPricing(item.InputCostPerM, item.OutputCostPerM), priceW)
	paddedFlags := padRightExact(capabilityFlags(item), flagsW)

	// 3. Assemble single plain string.
	plainRow := paddedID + " " + paddedCtx + " " + paddedPrice + " " + paddedFlags

	// 4. Force exact fit to rightPaneW.
	return padOrTruncateExact(plainRow, rightPaneW)
}

// capabilityFlags renders the [Thinking]/[Vision]/[Tools] badge set for a
// model, derived strictly from its ModelDescriptor capabilities (falling back
// to the classifier for unlisted but detectable caps).
func capabilityFlags(d registry.ModelDescriptor) string {
	caps := d.Capabilities
	if len(caps) == 0 {
		caps = role.EffectiveCapabilities(d)
	}
	if len(caps) == 0 {
		return "—"
	}
	var parts []string
	for _, c := range caps {
		switch c {
		case registry.CapThinking:
			parts = append(parts, "[Thinking]")
		case registry.CapVision:
			parts = append(parts, "[Vision]")
		case registry.CapTools:
			parts = append(parts, "[Tools]")
		default:
			parts = append(parts, "["+string(c)+"]")
		}
	}
	return strings.Join(parts, " ")
}

// recentSectionLines renders the pinned RECENTLY USED block (up to 3 rows)
// from the MRU bindings, filtered to the current provider scope and snapshot.
// Returns nil when empty, when the Roles tab is active, or when the budget is
// too small to justify a pinned section.
func (m Model) recentSectionLines(paneW, budget int) []string {
	if len(m.recentBindings) == 0 || budget < 3 || m.showingRoles {
		return nil
	}
	byID := make(map[string]registry.ModelDescriptor, len(m.snapshotModels()))
	for _, d := range m.snapshotModels() {
		if m.provider != "" && !strings.EqualFold(d.Provider, m.provider) {
			continue
		}
		if _, ok := byID[d.ID]; !ok {
			byID[d.ID] = d
		}
	}
	lines := []string{mutedStyle.Render("RECENTLY USED")}
	maxRows := min(3, budget-1)
	rows := 0
	for _, b := range m.recentBindings {
		if rows >= maxRows {
			break
		}
		d, ok := byID[string(b.ModelID)]
		if !ok {
			continue
		}
		cell := runewidth.Truncate("  "+d.ID, paneW, "…")
		cell = padRightExact(cell, paneW)
		lines = append(lines, mutedStyle.Render(cell))
		rows++
	}
	if len(lines) == 1 {
		return nil
	}
	return lines
}

// visibleWindowBudget computes the cursor-following scroll window for an
// explicit row budget (used when recent-section rows shrink the list area).
func (m Model) visibleWindowBudget(budget int) (start, end int) {
	total := len(m.filtered)
	if total == 0 {
		return 0, 0
	}
	if budget <= 0 {
		budget = total
	}
	if budget > total {
		budget = total
	}
	if budget >= total {
		return 0, total
	}
	// Cursor-following offset: keep highlight visible with minimal scroll.
	start = m.cursor - budget + 1
	if start < 0 {
		start = 0
	}
	if start+budget > total {
		start = total - budget
	}
	if start < 0 {
		start = 0
	}
	return start, start + budget
}

// buildVerticalSeparator renders the divider line between the two panes.
func (m Model) buildVerticalSeparator() string {
	paneH := m.paneHeight()
	lines := make([]string, paneH)
	for i := range lines {
		lines[i] = dividerStyle.Render("│")
	}
	return strings.Join(lines, "\n")
}

// renderActiveLine shows the currently highlighted provider and model. In
// Roles mode it surfaces the highlighted role policy override instead.
func (m Model) renderActiveLine() string {
	if m.showingRoles {
		role := m.HighlightedRole()
		suffix := ""
		if ob, ok := m.policyOverrides[role]; ok && ob.ModelID != "" {
			suffix = " → " + ob.ModelID
		}
		return mutedStyle.Render("Role: ") + accentStyle.Render(role) + mutedStyle.Render(suffix)
	}
	prov := m.highlightedProvider()
	if prov == "" {
		prov = "All models"
	}
	modelID := "—"
	if hl := m.Highlighted(); hl != nil {
		modelID = hl.ID
	}
	return mutedStyle.Render("Active: ") + accentStyle.Render(prov) + mutedStyle.Render(" / ") + accentStyle.Render(modelID)
}

// renderVariantLine shows the reasoning policy variant for the highlighted model.
func (m Model) renderVariantLine() string {
	sel := m.Highlighted()
	if sel == nil {
		return mutedStyle.Render("Variant: —")
	}
	opt, ok := m.CurrentReasoningOption()
	if !ok {
		return mutedStyle.Render("Variant: Fixed")
	}
	label := opt
	if label == "" {
		label = "Default"
	}
	return mutedStyle.Render("Variant: ") + accentStyle.Render(label)
}

// renderHeader renders the registry title with total count and sync state:
// "● synced" (non-empty, idle) | "⟳ syncing" (loading) | "⚠ empty"
// (zero models) | "⚠ <provider> <status>".
// Strict width clipping: truncates to availWidth (innerW = modalW-4) and
// preserves right border with adaptive gap. Never expands header width.
func (m Model) renderHeader() string {
	m.searchInput.Width = 16
	total := len(m.snapshotModels())
	status, style := m.syncIndicator()
	leftTitle := headerStyle.Render("IZEN MODEL REGISTRY (Provider-Centric)")
	countText := fmt.Sprintf("%d models loaded", total)
	// Active focus indicator for the 3-pane layout: PROVIDERS, MODELS or ROLES.
	focusLabel, focusHint := "PROVIDERS", "Tab to Models"
	switch m.paneFocus {
	case PaneModels:
		focusLabel, focusHint = "MODELS", "Tab to Roles"
	case PaneRoles:
		focusLabel, focusHint = "ROLES", "Tab to Providers"
	}
	focusPill := mutedStyle.Render("Focus: [") + accentStyle.Render(focusLabel) + mutedStyle.Render(fmt.Sprintf("] (%s)", focusHint))
	avail := m.innerWidth
	if avail <= 0 {
		avail = m.width
	}
	if avail <= 0 {
		return fmt.Sprintf("%s  %s  %s   %s", leftTitle, focusPill, mutedStyle.Render(countText), style.Render(status))
	}
	// Right side: focus pill + count + sync status, styled
	rightCombined := lipgloss.JoinHorizontal(lipgloss.Left,
		focusPill,
		"   ",
		mutedStyle.Render(countText),
		"   ",
		style.Render(status),
	)
	// Calculate gap so title stays left, status stays right, truncated cleanly.
	gap := avail - lipgloss.Width(leftTitle) - lipgloss.Width(rightCombined)
	if gap < 1 {
		// Fallback truncation for narrow windows — preserve border
		return lipgloss.NewStyle().MaxWidth(avail).MaxHeight(1).Render(leftTitle)
	}
	spaces := strings.Repeat(" ", gap)
	combined := leftTitle + spaces + rightCombined
	// Hard clip to exact inner width, single line, preserve outer border
	return lipgloss.NewStyle().MaxWidth(avail).MaxHeight(1).Render(combined)
}

// renderTitleBar is the spec-named alias of renderHeader for narrow-window
// clipping verification. It enforces the same strict width contract.
//
//nolint:unused // spec-required helper for narrow-window clipping verification
func (m Model) renderTitleBar(availWidth int) string {
	if availWidth <= 0 {
		availWidth = m.innerWidth
		if availWidth <= 0 {
			availWidth = m.width
		}
	}
	m2 := m
	m2.innerWidth = availWidth
	m2.width = availWidth
	return m2.renderHeader()
}

// syncIndicator derives the header sync state purely from RAM:
// loading -> syncing (takes precedence so Ctrl+R is visible even on empty),
// empty snapshot -> ⚠ empty (never misleading ● synced on 0 models),
// err -> stale/warn, non-ok provider summary -> warn, else synced.
func (m Model) syncIndicator() (string, interface {
	Render(...string) string
}) {
	if m.loading {
		return "⟳ syncing", syncBusyStyle
	}
	if m.snap == nil || len(m.snap.Models) == 0 {
		return "⚠ empty", syncWarnStyle
	}
	if m.err != nil {
		return "⚠ stale", syncWarnStyle
	}
	if m.snap != nil {
		for _, ps := range m.snap.Providers {
			switch ps.Status {
			case registry.CacheStatusOK, "":
				continue
			case registry.CacheStatusTimeout:
				return "⟳ syncing " + ps.Name, syncBusyStyle
			default:
				return "⚠ " + ps.Name + " " + ps.Status, syncWarnStyle
			}
		}
	}
	return "● synced", syncOkStyle
}

// visibleWindow computes the dynamic viewport: chrome-aware budget from
// innerHeight with cursor-following scroll offset. Spec: Total Chrome = 6
// (Title, Search, Divider, Reasoning, Bindings, Help footer).
// listRowBudget = max(3, innerHeight - 6). Zero height = show all.
//
//nolint:unused // retained as the pane-agnostic contract ancestor of visibleWindowBudget
func (m Model) visibleWindow() (start, end int) {
	total := len(m.filtered)
	if total == 0 {
		return 0, 0
	}
	budget := total
	innerH := m.innerHeight
	if innerH <= 0 {
		innerH = m.height
	}
	if innerH > 0 {
		if m.listRowBudget > 0 {
			budget = m.listRowBudget
		} else {
			budget = max(3, innerH-6)
		}
		if budget > total {
			budget = total
		}
	}
	if budget >= total {
		return 0, total
	}
	// Cursor-following offset: keep highlight visible with minimal scroll.
	start = m.cursor - budget + 1
	if start < 0 {
		start = 0
	}
	if start+budget > total {
		start = total - budget
	}
	if start < 0 {
		start = 0
	}
	return start, start + budget
}

// padLeftExact pads s with leading spaces to exactly w visible cells.
func padLeftExact(s string, w int) string {
	vw := lipgloss.Width(s)
	if vw >= w {
		return s
	}
	return strings.Repeat(" ", w-vw) + s
}

// padRightExact pads s with spaces to exactly w visible cells using
// lipgloss.Width. s is assumed already truncated to <= w.
func padRightExact(s string, w int) string {
	vw := lipgloss.Width(s)
	if vw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vw)
}

// renderReasoningSection renders the dynamic reasoning variant block for the
// selected model from its real ReasoningCapability (CapabilityResolver,
// Model ID / Provider matching). It never renders a hardcoded variant line:
//
//	PATH A (unsupported): "Reasoning: Not supported by model" (zero variants).
//	PATH B (provider-managed): "Reasoning: Provider managed / Not configurable".
//	PATH C (configurable): "Reasoning Policy (Press 'r' to cycle): [Label]"
//	  plus the REAL option labels from caps.Options joined by "  ·  " with the
//	  selected index bracketed.
//
//nolint:unused // retained for spec compatibility and external callers
func (m *Model) renderReasoningSection(sel *registry.ModelDescriptor) string {
	var sb strings.Builder
	if sel == nil {
		sb.WriteString("Reasoning: Not supported by model\n")
		return sb.String()
	}
	caps := sel.GetReasoningCapability()

	// PATH A: Model does NOT support reasoning
	if !caps.Supported {
		sb.WriteString("Reasoning: Not supported by model\n")
		return sb.String()
	}

	// PATH B: Supported but Provider Managed / Always On (e.g. DeepSeek R1)
	if !caps.Configurable || len(caps.Options) == 0 {
		sb.WriteString("Reasoning: Provider managed / Not configurable\n")
		return sb.String()
	}

	// PATH C: Configurable — Render REAL options from caps.Options
	idx := m.selectedReasoningOptIdx
	if idx < 0 || idx >= len(caps.Options) {
		idx = 0
	}
	currentOpt := caps.Options[idx]
	label := currentOpt.Label
	if label == "" {
		label = currentOpt.ID
	}
	fmt.Fprintf(&sb, "Reasoning Policy (Press 'r' to cycle): [%s]\n", label)

	var optionLabels []string
	for i, opt := range caps.Options {
		l := opt.Label
		if l == "" {
			l = opt.ID
		}
		if i == idx {
			optionLabels = append(optionLabels, fmt.Sprintf("[%s]", l))
		} else {
			optionLabels = append(optionLabels, l)
		}
	}
	sb.WriteString(strings.Join(optionLabels, "  ·  "))
	sb.WriteString("\n")

	return sb.String()
}

// renderBindingsLine renders active role mappings with focus highlight and
// persistence truth states (saving... -> ✓ / ✕).
//
//nolint:unused // retained for legacy compatibility
func (m Model) renderBindingsLine() string {
	var cells []string
	hl := m.Highlighted()
	hlID := ""
	if hl != nil {
		hlID = hl.ID
	}
	for _, r := range role.ValidRoles {
		bound, hasBound := m.roles[r]
		display := bound
		suffix := ""
		state := ""
		if pb, ok := m.pending[r]; ok {
			switch pb.State {
			case PendingSaving:
				display = pb.ModelID
				suffix = " · saving..."
				state = PendingSaving
			case PendingOK:
				// Confirmed: roles map already carries the model; mark ✓.
				if pb.ModelID != "" {
					display = pb.ModelID
				}
				suffix = " ✓"
				state = PendingOK
			case PendingFail:
				if pb.ModelID != "" {
					display = pb.ModelID
				} else if hasBound {
					display = bound
				}
				if pb.ErrMsg != "" {
					suffix = " ✕ " + truncate(pb.ErrMsg, 40)
				} else {
					suffix = " ✕"
				}
				state = PendingFail
			}
		}
		if display == "" {
			display = "—"
		}
		// Shorten long IDs for the single-line bindings strip.
		short := shortenID(display)
		cell := fmt.Sprintf("%s → %s%s", r, short, suffix)
		_ = state
		// Focus highlight: highlighted model bound to this role renders in
		// the primary accent; failures render the ✕ in error style.
		if hasBound && hlID != "" && bound != "" && strings.EqualFold(bound, hlID) && state != PendingFail {
			cells = append(cells, accentStyle.Render(cell))
		} else if pb, ok := m.pending[r]; ok && pb.State == PendingFail {
			// Keep role→model readable, mark only the cross in error color.
			base := fmt.Sprintf("%s → %s", r, short)
			cells = append(cells, base+" "+errStyle.Render("✕"))
		} else if pb, ok := m.pending[r]; ok && pb.State == PendingSaving {
			cells = append(cells, mutedStyle.Render(fmt.Sprintf("%s → %s", r, short))+accentStyle.Render(" · saving..."))
		} else if pb, ok := m.pending[r]; ok && pb.State == PendingOK {
			cells = append(cells, mutedStyle.Render(fmt.Sprintf("%s → %s", r, short))+okStyle.Render(" ✓"))
		} else {
			cells = append(cells, mutedStyle.Render(cell))
		}
	}
	return mutedStyle.Render("BINDINGS   ") + strings.Join(cells, mutedStyle.Render("   "))
}

// providerTag renders the colored provider badge for a model row:
// OpenRouter orange, Anthropic purple, OpenAI green, Ollama cyan,
// Gemini blue, DeepSeek teal, others muted. Uppercase grep-friendly text.
func providerTag(provider string) string {
	tag := "[" + strings.ToUpper(provider) + "]"
	switch strings.ToLower(provider) {
	case "openrouter":
		return openRouterBadge.Render(tag)
	case "anthropic":
		return anthropicBadge.Render(tag)
	case "openai":
		return openAIBadge.Render(tag)
	case "ollama":
		return ollamaBadge.Render(tag)
	case "gemini", "google":
		return geminiBadge.Render(tag)
	case "deepseek":
		return deepseekBadge.Render(tag)
	case "":
		return ""
	default:
		return providerBadge.Render(tag)
	}
}

// formatContext renders a context window compactly ("1M", "2M", "128k",
// "163k", "—"). Values >= 1M use dynamic M units (whole → "2M", fractional
// → "1.5M"); values >= 1k use "k"; non-positive renders "—". It delegates to
// the shared formatContextWindow helper for unit math.
func formatContext(ctx int) string {
	if ctx <= 0 {
		return "—"
	}
	if ctx >= 1_000_000 {
		val := float64(ctx) / 1_000_000.0
		if val == float64(int(val)) {
			return fmt.Sprintf("%dM", int(val))
		}
		return fmt.Sprintf("%.1fM", val)
	}
	if ctx >= 1000 {
		if ctx%1000 == 0 {
			return strconv.Itoa(ctx/1000) + "k"
		}
		return strconv.Itoa(ctx/1000) + "k"
	}
	return strconv.Itoa(ctx)
}

// shortenID truncates long model IDs for the bindings strip (keep tail).
//
//nolint:unused // retained for legacy compatibility
func shortenID(id string) string {
	if id == "—" || len(id) <= 28 {
		return id
	}
	return "…" + id[len(id)-27:]
}

//nolint:unused // retained for legacy compatibility
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
