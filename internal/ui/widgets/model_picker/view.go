package model_picker

import (
	"fmt"
	"strconv"
	"strings"

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
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderDivider())
	b.WriteString("\n")
	b.WriteString(m.renderSearchLine())
	b.WriteString("\n")

	models := m.snapshotModels()
	if len(models) == 0 {
		b.WriteString(m.renderZeroStatePanel())
		b.WriteString("\n")
		return m.padFooter(b.String())
	}
	if m.err != nil {
		b.WriteString(errStyle.Render(" cache load failed: " + m.err.Error()))
		b.WriteString("\n")
	}

	if len(m.filtered) == 0 {
		if len(models) > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf(" No models matching query: '%s' ", m.query)))
		} else {
			b.WriteString(mutedStyle.Render(" no models loaded "))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(m.renderList())
		b.WriteString("\n")
	}

	b.WriteString(m.renderReasoningSection())
	b.WriteString("\n")
	b.WriteString(m.renderBindingsLine())
	b.WriteString("\n")

	if m.status != "" {
		b.WriteString(mutedStyle.Render(" " + m.status))
		b.WriteString("\n")
	}
	return m.padFooter(b.String())
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
// the keybindings footer lands on the last line of the modal card.
func (m Model) padFooter(body string) string {
	footer := m.footerText()
	if m.height <= 0 {
		return body + footer
	}
	// Body lines used + 1 footer line; filler bridges the remainder.
	used := strings.Count(body, "\n") + 1
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
			cells = append(cells, mutedStyle.Render("All"))
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
				cells = append(cells, mutedStyle.Render(cell))
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
// height (header+search+reasoning+bindings+footer) with cursor-following
// scroll offset. Zero height = show all (unbounded, for tests).
func (m Model) visibleWindow() (start, end int) {
	total := len(m.filtered)
	if total == 0 {
		return 0, 0
	}
	budget := total
	if m.height > 0 {
		chrome := 7 // header, search, reasoning, bindings, footer + spacing
		if m.status != "" {
			chrome++
		}
		if m.err != nil {
			chrome++
		}
		budget = m.height - chrome
		if budget < 1 {
			budget = 1
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

func (m Model) renderList() string {
	start, end := m.visibleWindow()
	if start >= end {
		return ""
	}
	window := m.filtered[start:end]
	// Column widths for alignment (ID, context, price, caps).
	idW, ctxW, priceW, capsW := 0, 0, 0, 0
	ids := make([]string, len(window))
	ctxs := make([]string, len(window))
	prices := make([]string, len(window))
	caps := make([]string, len(window))
	for i, d := range window {
		ids[i] = d.ID
		ctxs[i] = formatContext(d.ContextWindow)
		prices[i] = formatPricePair(d.InputCostPerM, d.OutputCostPerM)
		caps[i] = formatCaps(d)
		if len(ids[i]) > idW {
			idW = len(ids[i])
		}
		if len(ctxs[i]) > ctxW {
			ctxW = len(ctxs[i])
		}
		if len(prices[i]) > priceW {
			priceW = len(prices[i])
		}
		if len(caps[i]) > capsW {
			capsW = len(caps[i])
		}
	}
	var b strings.Builder
	for i, d := range window {
		idx := start + i
		badges := renderBadges(BadgesFor(d, m.roles))
		prov := providerTag(d.Provider)
		tail := strings.TrimSpace(caps[i] + " " + prov + " " + badges)
		// Inline role badge for the highlighted model is already in badges;
		// ensure the current cursor row surfaces it even when roles map is
		// mid-transition (pending saving shows the target separately).
		cursor := " "
		idCell := fmt.Sprintf("%-*s", idW, ids[i])
		row := fmt.Sprintf("%s %s  %-*s  %-*s  %-*s %s", cursor, idCell, ctxW, ctxs[i], priceW, prices[i], capsW, caps[i], tail)
		if idx == m.cursor {
			row = fmt.Sprintf("%s %s  %-*s  %-*s  %-*s %s", ">", idCell, ctxW, ctxs[i], priceW, prices[i], capsW, caps[i], tail)
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
		// Prepend scroll context without shifting the cursor math.
		return mutedStyle.Render(fmt.Sprintf(" … %d above", start)) + "\n" + strings.TrimSuffix(b.String(), "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
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

// formatPricePair renders "$in/$out" compactly ("$0.55/$2.19", "—").
func formatPricePair(in, out float64) string {
	if in == 0 && out == 0 {
		return "—"
	}
	return fmt.Sprintf("$%g/$%g", in, out)
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
