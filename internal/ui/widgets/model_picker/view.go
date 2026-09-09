package model_picker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/adapter"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// View implements tea.Model. Pure view, zero I/O: renders the registry
// header with sync state, the search + provider filter line, the primary
// model table (cursor, ID, context, price, capabilities, role badge), the
// contextual reasoning section (collapses for ReasoningModeNone), the
// contextual bindings line (saving... -> ok/fail), and the hotkey footer.
// The model list is the central element; chrome adapts around it with
// dynamic viewport budgeting. Empty snapshots render inline, never modal.
func (m Model) View() string {
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
			b.WriteString(m.clipLine(mutedStyle.Render(" no models loaded ")))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderList())
		b.WriteString("\n")
	}

	b.WriteString(m.clipLine(m.renderReasoningSection()))
	b.WriteString("\n")
	b.WriteString(m.clipLine(m.renderBindingsLine()))
	b.WriteString("\n")

	if m.status != "" {
		b.WriteString(m.clipLine(mutedStyle.Render(" " + m.status)))
		b.WriteString("\n")
	}
	return m.padFooter(b.String())
}

// clipLine enforces strict single-line width: truncates styled lines to the
// inner width when bounded, stripping any embedded newlines.
func (m Model) clipLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if m.width <= 0 {
		return s
	}
	return truncateStyled(s, m.width)
}

// renderDivider renders the subtle horizontal rule separating the header
// from the search line. Width follows the picker bounds when set (modal
// inner width), else a 64-column default. Zero I/O.
func (m Model) renderDivider() string {
	w := m.width
	if w <= 0 {
		w = 64
	}
	if w < 16 {
		w = 16
	}
	return dividerStyle.Render(strings.Repeat("─", w))
}

// footerText is the keybindings footer anchored at the bottom of the modal
// card. Kept as a helper so tests and the anchor logic share one string.
func (m Model) footerText() string {
	return mutedStyle.Render("↑↓ select   ←→ reasoning   d/p/s/v/a bind   Ctrl+R sync   Enter use")
}

// padFooter appends height-aware padding plus the anchored footer to body.
// body holds every line above the footer (each terminated by "\n"). When a
// height bound is set and the body is shorter, blank lines fill the gap so
// the keybindings footer lands on the last line of the modal card. When the
// body overflows, it is hard-clipped to height-1 lines so the modal border
// stays stationary (zero frame stretching).
func (m Model) padFooter(body string) string {
	footer := m.footerText()
	if m.width > 0 {
		footer = truncateStyled(strings.ReplaceAll(footer, "\n", " "), m.width)
	}
	if m.height <= 0 {
		return body + footer
	}
	// Hard clip: keep at most height-1 body lines + 1 footer line.
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	body = strings.Join(lines, "\n") + "\n"
	// Body lines used + 1 footer line; filler bridges the remainder.
	used := len(lines) + 1
	if pad := m.height - used; pad > 0 {
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
	b.WriteString("│" + pad("  No cached models found in ~/.izen/cache/models.json") + "│\n")
	b.WriteString("│" + pad("  (no models loaded)") + "│\n")
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
func (m Model) renderHeader() string {
	total := len(m.snapshotModels())
	status, style := m.syncIndicator()
	left := headerStyle.Render("IZEN MODEL REGISTRY")
	// Header metric is the TOTAL snapshot loaded in RAM — never the filtered
	// count. Keep grep-friendly ("2,140 models loaded" style counts render
	// raw; keep plain "%d models" substring).
	return fmt.Sprintf("%s  %d models loaded   %s", left, total, style.Render(status))
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

// renderSearchLine renders the search query and compact provider filter with
// inline per-provider sync states (Provider: [All] openrouter ⟳ groq ✓ ...).
func (m Model) renderSearchLine() string {
	prov := "All"
	if m.provider != "" {
		prov = m.provider
	}
	line := fmt.Sprintf("Search: %s  Provider: %s", m.query, prov)
	if m.snap != nil && len(m.snap.Providers) > 0 {
		cells := make([]string, 0, len(m.snap.Providers)+1)
		if m.provider == "" {
			cells = append(cells, accentStyle.Render("[All]"))
		} else {
			cells = append(cells, inactiveProviderStyle.Render("All"))
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
				cells = append(cells, accentStyle.Render("["+cell+"]"))
			} else {
				cells = append(cells, inactiveProviderStyle.Render(cell))
			}
		}
		line += "  " + strings.Join(cells, "  ")
	}
	// Focus/scope affordance stays grep-friendly for legacy tests.
	focus := "search"
	if !m.searchFocused {
		focus = "list"
	}
	scope := "local"
	if m.isGlobal {
		scope = "global"
	}
	// Match count is filter-scoped: len(filtered)/len(snapshot) matches.
	// The header above always carries the total; this line carries matches.
	line += mutedStyle.Render(fmt.Sprintf("  focus:%s scope:%s %d/%d matches", focus, scope, len(m.filtered), len(m.snapshotModels())))
	return line
}

// visibleWindow computes the dynamic viewport: chrome-aware budget from
// height with cursor-following scroll offset. Static chrome = 7 lines:
// header title (1) + search/status (2) + divider (1) + reasoning/bindings/
// footer (3). listRowBudget = max(3, modalH-7) in modal coordinates, which
// maps to inner height minus chrome here. Zero height = show all
// (unbounded, for tests).
func (m Model) visibleWindow() (start, end int) {
	total := len(m.filtered)
	if total == 0 {
		return 0, 0
	}
	budget := total
	if m.height > 0 {
		chrome := 7 // header, search, divider, reasoning, bindings, footer + spacing
		if m.status != "" {
			chrome++
		}
		if m.err != nil {
			chrome++
		}
		budget = m.height - chrome
		if budget < 3 {
			budget = 3
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
	colNameW     = 32
	colProviderW = 12
	colContextW  = 8
	colPriceW    = 14
	colCapsMinW  = 10
)

// renderList renders the strict single-line tabular grid. Every row is
// EXACTLY 1 physical line: each column is truncated/padded to its fixed
// width before concatenation, and the assembled line is hard-clipped to the
// inner width. Zero text wrapping allowed.
func (m Model) renderList() string {
	start, end := m.visibleWindow()
	if start >= end {
		return ""
	}
	window := m.filtered[start:end]
	innerW := m.width

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

// renderRow assembles one guaranteed-single-line row: format & truncate
// EVERY column to its fixed width before concatenation, then hard-clip the
// assembled line to width. Per-cell colors preserve visible widths.
func (m Model) renderRow(d registry.ModelDescriptor, selected bool, nameW, providerW, ctxW, priceW, capsW, innerW int) string {
	// 1. Format & truncate plain cells.
	nameStr := fitCell(d.ID, nameW)
	providerStr := fitCell(providerPlainTag(d.Provider), providerW)
	contextStr := fitCell(formatContext(d.ContextWindow), ctxW)
	priceStr := fitCell(formatPricePair(d.InputCostPerM, d.OutputCostPerM), priceW)
	// Capabilities share their budget with role badges so [PLAN]/[DEFAULT]
	// stay visible when space allows, always within capsW.
	badges := strings.Join(BadgesFor(d, m.roles), " ")
	capsPlain := formatCaps(d)
	capsAndBadges := capsPlain
	if badges != "" {
		if capsAndBadges == "" || capsAndBadges == "—" {
			capsAndBadges = badges
		} else {
			capsAndBadges = capsAndBadges + " " + badges
		}
	}
	capsStr := truncateWithEllipsis(capsAndBadges, capsW)

	// 2. Style cells (styling preserves visible width).
	var nameCell, provCell, ctxCell, priceCell, capsCell, cursor string
	if selected {
		nameCell = selectedRowStyle.Render(nameStr)
		ctxCell = selectedMetaStyle.Render(contextStr)
		priceCell = selectedMetaStyle.Render(priceStr)
		capsCell = selectedRowStyle.Render(padRight(capsStr, capsW))
		cursor = cursorStyle.Render(">")
		// Re-apply the provider hue on top of the selection background so
		// the pill stays distinguishable while selected.
		provCell = providerTagStyled(providerStr, d.Provider, true)
	} else {
		nameCell = normalRowStyle.Render(nameStr)
		provCell = providerTagStyled(providerStr, d.Provider, false)
		ctxCell = metaStyle.Render(contextStr)
		priceCell = metaStyle.Render(priceStr)
		capsCell = mutedStyle.Render(padRight(capsStr, capsW))
		cursor = " "
	}

	// 3. Assemble line (guaranteed <= innerW in visible cells).
	line := cursor + " " + lipgloss.JoinHorizontal(lipgloss.Left,
		nameCell,
		" ",
		provCell,
		" ",
		ctxCell,
		" ",
		priceCell,
		" ",
		capsCell,
	)
	// Belt-and-suspenders: hard-clip any ANSI edge case to innerW.
	line = truncateStyled(line, innerW)
	if selected {
		// Ensure the full-width selection background without rewrapping:
		// pad visible remainder with the selection background.
		if vw := lipgloss.Width(line); vw < innerW {
			line += selectedRowStyle.Render(strings.Repeat(" ", innerW-vw))
		}
		return lipgloss.NewStyle().Width(innerW).MaxWidth(innerW).MaxHeight(1).Render(line)
	}
	return lipgloss.NewStyle().Width(innerW).MaxWidth(innerW).MaxHeight(1).Render(line)
}

// providerPlainTag returns the grep-friendly uppercase pill text.
func providerPlainTag(provider string) string {
	if provider == "" {
		return ""
	}
	return "[" + strings.ToUpper(provider) + "]"
}

// renderReasoningSection renders the contextual reasoning block. It collapses
// to one minimal line for ReasoningModeNone ("REASONING   —") and formats
// every other mode natively (no universal low/medium/high forcing).
func (m Model) renderReasoningSection() string {
	hl := m.Highlighted()
	if hl == nil {
		return mutedStyle.Render("REASONING   —")
	}
	mode := ReasoningModeFor(*hl)
	switch mode {
	case adapter.ReasoningModeNone:
		return mutedStyle.Render("REASONING   —")
	case adapter.ReasoningModeFixed:
		// Spec: "Fixed · locked" / "Fixed · provider controlled".
		return mutedStyle.Render("REASONING   ") + mutedStyle.Render("Fixed · provider controlled ") + otherBadge.Render("[Locked]")
	default:
		return mutedStyle.Render("REASONING   ") + m.RenderReasoningBar()
	}
}

// renderBindingsLine renders active role mappings with focus highlight and
// persistence truth states (saving... -> ✓ / ✕).
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

// providerTagStyled colors a pre-fitted provider cell (exact visible width)
// with the per-provider hue. Selected rows keep Surface0 background.
func providerTagStyled(fitted, provider string, selected bool) string {
	bg := ""
	_ = bg
	switch strings.ToLower(provider) {
	case "openrouter":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fab387")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return openRouterBadge.Render(fitted)
	case "anthropic":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return anthropicBadge.Render(fitted)
	case "openai":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return openAIBadge.Render(fitted)
	case "ollama":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return ollamaBadge.Render(fitted)
	case "gemini", "google":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return geminiBadge.Render(fitted)
	case "deepseek":
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94e2d5")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return deepseekBadge.Render(fitted)
	case "":
		if selected {
			return selectedRowStyle.Render(fitted)
		}
		return fitted
	default:
		if selected {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6c7086")).Background(lipgloss.Color("#313244")).Render(fitted)
		}
		return providerBadge.Render(fitted)
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

// formatContext renders a context window compactly ("128k", "163k", "—").
func formatContext(ctx int) string {
	if ctx <= 0 {
		return "—"
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
	return strings.Join(parts, " ")
}

// shortenID truncates long model IDs for the bindings strip (keep tail).
func shortenID(id string) string {
	if id == "—" || len(id) <= 28 {
		return id
	}
	return "…" + id[len(id)-27:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
