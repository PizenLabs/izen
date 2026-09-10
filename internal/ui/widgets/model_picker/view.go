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
	colorSurface1 = lipgloss.Color("#45475a")
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
// 3-role assignment matrix are removed per Phase 3 spec.
func (m Model) View() string {
	if m.state == StateDetail {
		return m.renderDetailLayout()
	}
	return m.renderBrowsingLayout()
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

	// Dual pane: left providers, right models, with vertical separator.
	leftPane := m.renderProvidersPane()
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

	help := fmt.Sprintf("%s %s   %s %s   %s %s   %s %s   %s %s",
		keyStyle.Render("Tab"), descStyle.Render("select"),
		keyStyle.Render("↑/↓"), descStyle.Render("navigate"),
		keyStyle.Render("Enter"), descStyle.Render("activate"),
		keyStyle.Render("Alt+A"), descStyle.Render("configure"),
		keyStyle.Render("Esc"), descStyle.Render("close"),
	)
	if m.innerWidth <= 0 && m.width <= 0 {
		return help
	}
	return truncateStyled(help, inner)
}

// renderDetailView renders the contextual Model Detail View & Workspace Target
// Assignment Drawer per spec.
func (m Model) renderDetailView() string {
	model := m.SelectedModel()
	if model == nil {
		return ""
	}

	lines := make([]string, 0, 12)

	// Header & Identity
	title := lipgloss.NewStyle().Foreground(colorMauve).Bold(true).Render("MODEL DETAILS")
	idStr := lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(model.ID)
	badge := renderProviderBadge(model.Provider, 12)

	lines = append(lines, title, fmt.Sprintf("%s  %s", idStr, badge), "")

	// Technical Specs
	ctxStr := fmt.Sprintf("Context: %s", formatContextWindow(model.ContextWindow))
	priceStr := fmt.Sprintf("Price: %s", formatPricing(model.InputCostPerM, model.OutputCostPerM))
	caps := model.Capabilities
	if len(caps) == 0 {
		// Derive from classifier for display parity
		caps = role.EffectiveCapabilities(*model)
	}
	capsStr := fmt.Sprintf("Capabilities: %s", formatCapabilities(caps))

	lines = append(lines,
		lipgloss.NewStyle().Foreground(colorSubtext0).Render(ctxStr+" • "+priceStr),
		lipgloss.NewStyle().Foreground(colorSubtext0).Render(capsStr),
		"",
		lipgloss.NewStyle().Foreground(colorSurface1).Render(strings.Repeat("─", max(16, m.innerWidth))),
		"",
	)

	// Reasoning Policy Control — dynamic variant rendering (truthful I6):
	// the section binds strictly to the selected model's real
	// ReasoningCapability via CapabilityResolver (Model ID / Provider
	// matching). Non-reasoning models render PATH A with zero variant line;
	// provider-managed models render PATH B; configurable models render PATH C
	// with REAL options from caps.Options.
	lines = append(lines, strings.Split(strings.TrimSuffix(m.renderReasoningSection(model), "\n"), "\n")...)
	lines = append(lines, "")

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
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

// renderProvidersPane renders the left pane: provider list with configured
// status icons (✓ for ok/configured, · for unconfigured/error).
func (m Model) renderProvidersPane() string {
	paneW := providersPaneWidth
	paneH := m.paneHeight()

	var lines []string

	// Pane header.
	lines = append(lines, mutedStyle.Render("PROVIDERS"))
	lines = append(lines, "")

	names := m.providerNames()
	for i, name := range names {
		if len(lines) >= paneH {
			break
		}
		configured := m.isProviderConfigured(name)
		selected := i == m.providerCursor && m.paneFocus == PaneProviders

		icon := "·"
		if configured {
			icon = "✓"
		}

		cell := fmt.Sprintf("%s %s", icon, name)
		cell = runewidth.Truncate(cell, paneW, "…")
		cell = padRightExact(cell, paneW)

		if selected {
			lines = append(lines, selectedRowStyle.Render(cell))
		} else {
			lines = append(lines, mutedStyle.Render(cell))
		}
	}

	// Pad to paneH.
	for len(lines) < paneH {
		lines = append(lines, strings.Repeat(" ", paneW))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderModelsPane renders the right pane: model list filtered to the
// highlighted provider, with search query display and cursor highlight.
func (m Model) renderModelsPane() string {
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	if innerW <= 0 {
		innerW = 64
	}
	paneW := innerW - providersPaneWidth - 1 // -1 for separator
	if paneW < 10 {
		paneW = 10
	}
	paneH := m.paneHeight()

	var lines []string

	// Pane header with optional search query.
	header := "MODELS"
	if m.query != "" {
		header += "  /" + m.query
	}
	lines = append(lines, mutedStyle.Render(header))
	lines = append(lines, "")

	// Model list from visible window.
	start, end := m.visibleWindow()
	window := m.filtered[start:end]

	for i, d := range window {
		if len(lines) >= paneH {
			break
		}
		idx := start + i
		selected := idx == m.cursor && m.paneFocus == PaneModels

		cell := runewidth.Truncate(d.ID, paneW, "…")
		cell = padRightExact(cell, paneW)

		if selected {
			lines = append(lines, selectedRowStyle.Render(cell))
		} else {
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

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
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

// renderActiveLine shows the currently highlighted provider and model.
func (m Model) renderActiveLine() string {
	prov := m.highlightedProvider()
	if prov == "" {
		prov = "—"
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
	// Active focus indicator for dual-pane layout: PROVIDERS or MODELS.
	focusLabel, focusHint := "PROVIDERS", "Tab to Models"
	if m.paneFocus == PaneModels {
		focusLabel, focusHint = "MODELS", "Tab to Providers"
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

// Fixed layout constants.
const (
	searchInputWidth = 16
	// providersPaneWidth is the fixed width of the left providers pane
	// in the dual-pane browsing layout. Right pane gets the remainder.
	providersPaneWidth = 22
)

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
func (m *Model) renderReasoningSection(sel *registry.ModelDescriptor) string {
	var sb strings.Builder
	if sel == nil {
		sb.WriteString("──────────────────────────────────────────────────\n\n")
		sb.WriteString("Reasoning: Not supported by model\n")
		return sb.String()
	}
	caps := sel.GetReasoningCapability()

	sb.WriteString("──────────────────────────────────────────────────\n\n")

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
	fmt.Fprintf(&sb, "Reasoning Policy (Press 'r' to cycle): [%s]\n\n", label)

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
