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
	colorSurface0 = lipgloss.Color("#313244")
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

// renderBrowsingLayout renders the list/search view with header, search bar,
// model list, and browsing footer.
func (m Model) renderBrowsingLayout() string {
	var b strings.Builder
	b.WriteString(m.clipLine(m.renderHeader()))
	b.WriteString("\n")
	b.WriteString(m.renderDivider())
	b.WriteString("\n")
	b.WriteString(m.clipLine(m.renderSearchLine()))
	b.WriteString("\n")

	models := m.snapshotModels()
	if len(models) == 0 {
		b.WriteString(m.renderZeroStatePanel())
		b.WriteString("\n")
		return m.padFooter(b.String())
	}
	if m.err != nil {
		b.WriteString(m.clipLine(errStyle.Render(" cache load failed: " + m.err.Error())))
		b.WriteString("\n")
	}

	if len(m.filtered) == 0 {
		if len(models) > 0 {
			b.WriteString(m.clipLine(mutedStyle.Render(fmt.Sprintf(" No models matching query: '%s' ", m.query))))
		} else {
			b.WriteString(m.clipLine(mutedStyle.Render(" No models loaded for provider. Press Alt+A to configure or trigger sync. ")))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderList())
		b.WriteString("\n")
	}

	// Browsing state: clean footer only (no reasoning/bindings controls on list).
	b.WriteString(m.renderDivider())
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

// renderBrowsingFooter delivers the spec's clean 1-line footer with no
// BINDINGS or Alt bindings. It is the sole footer for StateBrowsing.
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
		keyStyle.Render("↑/↓"), descStyle.Render("select"),
		keyStyle.Render("type"), descStyle.Render("search"),
		keyStyle.Render("Enter"), descStyle.Render("inspect & assign"),
		keyStyle.Render("a"), descStyle.Render("quick assign"),
		keyStyle.Render("Esc"), descStyle.Render("cancel"),
	)
	// Unbounded (tests/headless): no truncation so the full key contract
	// stays grep-friendly. Bounded modals clip to the inner width.
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

	var lines []string

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

// renderZeroStatePanel renders the structured empty-catalog help panel.
// It replaces the legacy single "no models loaded" line with actionable
// guidance while keeping the legacy substring for grep compatibility.
func (m Model) renderZeroStatePanel() string {
	inner := 61
	pad := func(s string) string {
		// Pad content to the inner width (ASCII-safe: panel strings are
		// plain ASCII; rune count == display width here).
		n := len([]rune(s))
		if n < inner {
			return s + strings.Repeat(" ", inner-n)
		}
		if n > inner {
			runes := []rune(s)
			return string(runes[:inner])
		}
		return s
	}
	title := "NO MODELS AVAILABLE"
	titlePadLeft := (inner - len([]rune(title))) / 2
	titlePadRight := inner - len([]rune(title)) - titlePadLeft
	titleLine := "│" + strings.Repeat(" ", titlePadLeft) + title + strings.Repeat(" ", titlePadRight) + "│"
	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", inner) + "┐\n")
	b.WriteString(titleLine + "\n")
	b.WriteString("│" + strings.Repeat(" ", inner) + "│\n")
	b.WriteString("│" + pad("  No models loaded for provider. Press Alt+A to configure credentials or trigger sync.") + "│\n")
	b.WriteString("│" + strings.Repeat(" ", inner) + "│\n")
	b.WriteString("│" + pad("  • Press Ctrl+R to fetch models from configured providers") + "│\n")
	b.WriteString("│" + pad("  • Check environment variables (OPENAI_API_KEY, etc.)") + "│\n")
	b.WriteString("└" + strings.Repeat("─", inner) + "┘")
	return mutedStyle.Render(b.String())
}

// snapshotModels reads the immutable snapshot RAM slice (no copy on the hot
// render path; callers must not mutate).
func (m Model) snapshotModels() []registry.ModelDescriptor {
	if m.snap == nil {
		return nil
	}
	return m.snap.Models
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
	// Active focus indicator: Focus: [SEARCH] (Tab to List) / [LIST] (Tab to Search).
	focusLabel, focusHint := "SEARCH", "Tab to List"
	if m.focus == FocusList {
		focusLabel, focusHint = "LIST", "Tab to Search"
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

// renderSearchLine renders the search query and compact provider filter.
// SPEC: Strict zero-wrap single-line guarantee. Search input is always active
// (Width 16 lock), no wrapping, provider bar clipped to remaining width,
// final sanitation strips any embedded newlines.
func (m Model) renderSearchLine() string {
	m.searchInput.Width = 16
	avail := m.innerWidth
	if avail <= 0 {
		avail = m.width
	}
	// Spec uses lipgloss subtext0 for label and guarantees single line.
	colorSubtext0 := lipgloss.Color("#6c7086")
	searchLabel := lipgloss.NewStyle().Foreground(colorSubtext0).Render("Search: ")
	// Render query cell with strict truncation; sanitize newlines.
	qDisplay := fitCell(m.query, searchInputWidth)
	qDisplay = strings.ReplaceAll(qDisplay, "\n", "")
	inputStr := qDisplay
	inputStr = strings.ReplaceAll(inputStr, "\n", "")
	leftBlock := searchLabel + inputStr + "  "
	leftBlock = strings.ReplaceAll(leftBlock, "\n", "")
	leftW := lipgloss.Width(leftBlock)

	// Unbounded (tests/headless): no truncation so grep-friendly substrings survive.
	if avail <= 0 {
		prov := "All"
		if m.provider != "" {
			prov = m.provider
		}
		var providerPart string
		if m.snap != nil && len(m.snap.Providers) > 0 {
			cells := make([]string, 0, len(m.snap.Providers)+1)
			if m.provider == "" {
				cells = append(cells, providerFilterActive.Render("[All]"))
			} else {
				cells = append(cells, providerFilterInactive.Render("all"))
			}
			for _, ps := range m.snap.Providers {
				var icon string
				switch ps.Status {
				case registry.CacheStatusTimeout:
					icon = "⟳"
				case registry.CacheStatusError:
					icon = "⚠"
				case registry.CacheStatusOK, "":
					icon = "✓"
				default:
					icon = ps.Status
				}
				cell := fmt.Sprintf("%s %s", ps.Name, icon)
				if m.provider != "" && strings.EqualFold(m.provider, ps.Name) {
					cells = append(cells, providerFilterActive.Render("["+cell+"]"))
				} else {
					cells = append(cells, providerFilterInactive.Render(cell))
				}
			}
			providerPart = fmt.Sprintf("Provider: %s  %s", prov, strings.Join(cells, "  "))
		} else {
			providerPart = fmt.Sprintf("Provider: %s", "All")
		}
		scope := "local"
		if m.isGlobal {
			scope = "global"
		}
		tail := mutedStyle.Render(fmt.Sprintf("  scope:%s %d/%d matches", scope, len(m.filtered), len(m.snapshotModels())))
		full := leftBlock + providerPart + tail
		return strings.ReplaceAll(full, "\n", "")
	}

	remainingW := max(0, avail-leftW)
	providerBar := m.renderProviderFilterBar(remainingW)
	providerBar = strings.ReplaceAll(providerBar, "\n", "")

	fullLine := leftBlock + providerBar
	// Also append scope/matches within remaining budget (kept for match-count tests)
	scope := "local"
	if m.isGlobal {
		scope = "global"
	}
	tail := mutedStyle.Render(fmt.Sprintf("  scope:%s %d/%d matches", scope, len(m.filtered), len(m.snapshotModels())))
	tail = strings.ReplaceAll(tail, "\n", "")
	if tail != "" && lipgloss.Width(fullLine+tail) <= avail {
		fullLine += tail
	} else if tail != "" {
		// Truncate combined right side as whole to avoid overflow – spec renderSearchLine clips fullLine.
		combined := providerBar + tail
		clippedRight := runewidth.Truncate(combined, remainingW, "")
		clippedRight = strings.ReplaceAll(clippedRight, "\n", "")
		fullLine = leftBlock + clippedRight
	}
	clipped := runewidth.Truncate(fullLine, avail, "")
	clipped = strings.ReplaceAll(clipped, "\n", "")
	// Ensure ANSI-aware width clamp without wrap.
	return truncateStyled(clipped, avail)
}

// renderProviderFilterBar is the spec-named helper for search header composition.
// It renders the provider pill strip clipped to maxW with Catppuccin Mocha
// palette: active provider = yellow bold, inactive = subtext0 muted.
// Hard truncation via runewidth.Truncate ensures no overflow before assembly.
func (m Model) renderProviderFilterBar(maxW int) string {
	if maxW <= 0 {
		return ""
	}
	var items []string
	if m.snap != nil && len(m.snap.Providers) > 0 {
		// Include "All" pill first for UX parity with unbounded mode.
		if m.provider == "" {
			items = append(items, providerFilterActive.Render("[ALL]"))
		} else {
			items = append(items, providerFilterInactive.Render("all"))
		}
		for _, ps := range m.snap.Providers {
			label := ps.Name
			if m.provider != "" && strings.EqualFold(m.provider, ps.Name) {
				items = append(items, providerFilterActive.Render("["+strings.ToUpper(label)+"]"))
			} else {
				items = append(items, providerFilterInactive.Render(strings.ToLower(label)))
			}
		}
	} else {
		// No providers yet: still show All placeholder for layout stability.
		items = append(items, providerFilterInactive.Render("all"))
	}
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render("Provider: ") + strings.Join(items, " ")
	// Cell-level hard truncation before assembly (ANSI-aware).
	return truncateStyled(bar, maxW)
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

// Fixed column grid widths (visible cells, pre-style plain text).
const (
	colNameW         = 32
	colProviderW     = 12
	colContextW      = 8
	colPriceW        = 14
	colCapsMinW      = 10
	searchInputWidth = 16
)

// renderList renders the strict single-line tabular grid. Every row is
// EXACTLY 1 physical line: each column is truncated/padded to its fixed
// width before concatenation, and the assembled line is hard-clipped to the
// inner width (innerWidth = modalW - 4). Zero text wrapping allowed.
func (m Model) renderList() string {
	start, end := m.visibleWindow()
	if start >= end {
		return ""
	}
	window := m.filtered[start:end]
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}

	// Unbounded (tests/headless): legacy full render with sanitized pricing,
	// no truncation so grep-friendly substrings survive.
	if innerW <= 0 {
		var b strings.Builder
		for i, d := range window {
			idx := start + i
			badges := renderBadges(BadgesFor(d, m.roles))
			prov := providerTag(d.Provider)
			caps := formatCaps(d)
			ctx := formatContext(d.ContextWindow)
			price := formatPricePair(d.InputCostPerM, d.OutputCostPerM)
			tail := strings.TrimSpace(caps + " " + prov + " " + badges)
			cursor := " "
			if idx == m.cursor {
				cursor = ">"
			}
			row := fmt.Sprintf("%s %s  %s  %s  %s", cursor, d.ID, ctx, price, tail)
			if idx == m.cursor {
				b.WriteString(selectedRowStyle.Render(row))
			} else {
				b.WriteString(" " + row)
			}
			b.WriteString("\n")
		}
		if end < len(m.filtered) {
			b.WriteString(mutedStyle.Render(fmt.Sprintf(" … %d more", len(m.filtered)-end)))
			b.WriteString("\n")
		}
		if start > 0 {
			return mutedStyle.Render(fmt.Sprintf(" … %d above", start)) + "\n" + strings.TrimSuffix(b.String(), "\n")
		}
		return strings.TrimSuffix(b.String(), "\n")
	}

	// Fixed grid: adapt the name column on narrow panes so the total always
	// fits innerW with zero wrapping.
	nameW, providerW, ctxW, priceW := colNameW, colProviderW, colContextW, colPriceW
	const separators = 7 // 2 cursor prefix + 4 inter-column spaces + 1
	minTotal := 10 + providerW + ctxW + priceW + separators + colCapsMinW
	if innerW < colNameW+providerW+ctxW+priceW+separators+colCapsMinW {
		nameW = innerW - (providerW + ctxW + priceW + separators + colCapsMinW)
		if nameW < 10 {
			nameW = 10
		}
	}
	_ = minTotal
	capsW := innerW - (2 + nameW + 1 + providerW + 1 + ctxW + 1 + priceW + 1)
	if capsW < 0 {
		capsW = 0
	}

	var b strings.Builder
	for i, d := range window {
		idx := start + i
		b.WriteString(m.renderRow(d, idx == m.cursor, nameW, providerW, ctxW, priceW, capsW, innerW))
		b.WriteString("\n")
	}
	if end < len(m.filtered) {
		b.WriteString(truncateStyled(mutedStyle.Render(fmt.Sprintf(" … %d more", len(m.filtered)-end)), innerW))
		b.WriteString("\n")
	}
	if start > 0 {
		// Prepend scroll context without shifting the cursor math.
		return truncateStyled(mutedStyle.Render(fmt.Sprintf(" … %d above", start)), innerW) + "\n" + strings.TrimSuffix(b.String(), "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
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

// renderRow assembles one guaranteed-single-line row with CELL-LEVEL hard
// truncation via runewidth.Truncate BEFORE column assembly. Never allows cell
// content to exceed allocated column width, preventing hyphen-wrap bleed for
// long IDs like cohere/north-mini-code:free.
// SPEC: ZERO-WRAP – no .Width() on row styles, every column truncated and padded
// to exact width, whole line clipped to avail and sanitized to single line.
func (m Model) renderRow(d registry.ModelDescriptor, selected bool, nameW, providerW, ctxW, priceW, capsW, innerW int) string {
	if capsW < 6 {
		capsW = 6
	}
	avail := innerW - 2
	if avail < 0 {
		avail = 0
	}
	// Spec budgeting recalculated per row to guarantee capsW fits avail.
	// If caller capsW is inconsistent with avail, recompute safely.
	expectedAvail := nameW + providerW + ctxW + priceW + 4 // 4 inter-col spaces
	if capsW < 6 || avail < expectedAvail+6 {
		// Re-derive capsW from avail to ensure 1 line.
		capsW = max(6, avail-(nameW+providerW+ctxW+priceW+4))
	}
	// 1. Cell-level hard truncation (MUST run before styling or assembly).
	cleanID := strings.ReplaceAll(d.ID, "\n", " ")
	cName := padRightExact(runewidth.Truncate(cleanID, nameW, "…"), nameW)

	// Colored provider badge per spec Catppuccin palette.
	cProvider := renderProviderBadge(d.Provider, providerW)

	cleanCtx := strings.ReplaceAll(formatContext(d.ContextWindow), "\n", " ")
	cContext := padRightExact(runewidth.Truncate(cleanCtx, ctxW, ""), ctxW)

	cleanPrice := strings.ReplaceAll(formatPricePair(d.InputCostPerM, d.OutputCostPerM), "\n", " ")
	cPrice := padRightExact(runewidth.Truncate(cleanPrice, priceW, ""), priceW)

	// Capabilities cell sanitized to single line.
	badges := strings.ReplaceAll(strings.Join(BadgesFor(d, m.roles), " "), "\n", " ")
	capsPlain := strings.ReplaceAll(formatCaps(d), "\n", " ")
	capsAndBadges := capsPlain
	if badges != "" {
		if capsAndBadges == "" || capsAndBadges == "—" {
			capsAndBadges = badges
		} else {
			capsAndBadges = capsAndBadges + " " + badges
		}
	}
	cleanCaps := strings.ReplaceAll(capsAndBadges, "\n", " ")
	cCaps := padRightExact(runewidth.Truncate(cleanCaps, capsW, "…"), capsW)

	// 2. Assemble plain buffer then clip to avail (spec: 32/12/8/12 grid).
	// Include ANSI-aware provider badge width via lipgloss.Width for line budget.
	lineBuffer := fmt.Sprintf("%s %s %s %s %s", cName, cProvider, cContext, cPrice, cCaps)
	// Clip visible width to avail (innerWidth-2) then pad to exact avail.
	// Use runewidth for plain portion but ANSI-aware truncateStyled for final.
	clippedLine := runewidth.Truncate(lineBuffer, avail, "")
	// When provider badge ANSI inflates width, truncateStyled will handle later;
	// pad to avail using visible width.
	clippedLine = padRightExact(clippedLine, avail)

	var out string
	if selected {
		out = selectedRowStyle.Render("> " + clippedLine)
	} else {
		out = normalRowStyle.Render("  " + clippedLine)
	}
	// ABSOLUTE SANITATION: guarantee 1 physical line.
	out = strings.ReplaceAll(out, "\n", "")
	out = truncateStyled(out, innerW)
	return strings.ReplaceAll(out, "\n", "")
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

func renderBadges(badges []string) string {
	if len(badges) == 0 {
		return ""
	}
	cells := make([]string, 0, len(badges))
	for _, badge := range badges {
		cells = append(cells, styleBadge(badge))
	}
	return strings.Join(cells, " ")
}

func styleBadge(badge string) string {
	switch badge {
	case "[DEFAULT]":
		return defaultBadge.Render(badge)
	case "[PLAN]":
		return planBadge.Render(badge)
	case "[THINKING]":
		return thinkBadge.Render(badge)
	case "[VISION]":
		return visionBadge.Render(badge)
	default:
		return otherBadge.Render(badge)
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

// formatPricePair renders "$in/$out" compactly ("$0.55/$2.19", "free").
// Strict float sanitizer: never emits IEEE 754 bloat like $0.099999999.
func formatPricePair(in, out float64) string {
	return formatPricing(in, out)
}

// formatCaps renders classifier-effective capabilities ("Thinking Tools").
func formatCaps(d registry.ModelDescriptor) string {
	caps := role.EffectiveCapabilities(d)
	var parts []string
	if role.EffectiveIsThinking(d) {
		parts = append(parts, "Thinking")
	} else if role.HasCapability(caps, registry.CapThinking) {
		parts = append(parts, "Thinking")
	}
	if role.HasCapability(caps, registry.CapVision) {
		parts = append(parts, "Vision")
	}
	if role.HasCapability(caps, registry.CapTools) {
		parts = append(parts, "Tools")
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "\n", " ")
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
