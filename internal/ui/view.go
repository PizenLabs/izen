package ui

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

type blockType int

const (
	blockText blockType = iota
	blockPlan
	blockDiff
	blockTable
	blockEvidence
	blockRisk
	blockCommand
)

type contentBlock struct {
	kind blockType
	raw  string
}

// ── Responsive layout thresholds ────────────────────────────────────────
// IZEN is frequently run inside a split terminal pane (tmux/Ghostty splits
// side by side with other panes), so these thresholds let the startup
// banner, status line, and autocomplete dropdown rearrange their content
// instead of overflowing or wrapping mid-render when width is limited.
const (
	wideBannerThreshold     = 76 // >= : side-by-side robot art + text banner
	compactStatusThreshold  = 64 // <  : drop checkpoint id / git branch from status line & banner meta
	minimalStatusThreshold  = 46 // <  : drop model name from status line, keep spinner + tokens only
	dropdownTwoColThreshold = 56 // <  : collapse file autocomplete to a single column
)

// renderContextHeader renders a collapsible #number context header at the top
// of the viewport when an active context ID is set.
func (m *model) renderContextHeader() string {
	if m.sess == nil || m.sess.ContextID == "" {
		return ""
	}
	label := accentStyle.Render("▸ " + m.sess.ContextLabel())
	return label + "\n"
}

// View is the renderer entry point. It is a pure projection: it obtains the
// current Workspace from the workflow layer and renders it. The renderer knows
// nothing about modes, banners, prompts, footers, or action logic — only how
// to project a Workspace onto the terminal.
func (m *model) View() string {
	return renderWorkspace(m.BuildWorkspace())
}

// renderWorkspace is the ONLY rendering primitive. It projects a Workspace
// onto the terminal with no awareness of mode, workflow, or UI logic.
func renderWorkspace(ws Workspace) string {
	if ws.Overlay != "" {
		return ws.Overlay
	}
	var parts []string
	parts = append(parts, ws.Viewport)
	if ws.ProposalDock != "" {
		parts = append(parts, ws.ProposalDock)
	}
	if ws.Input != "" {
		parts = append(parts, ws.Input)
	}
	if ws.Footer != "" {
		parts = append(parts, ws.Footer)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// assembleScreen builds the Workspace's screen regions from the supplied
// capabilities. This is workflow/screen-assembly logic that belongs to the
// model layer, NOT the renderer: it computes region heights, sizes the
// viewport, and precomposes the input and footer regions. The renderer later
// projects the resulting Workspace without re-deriving any of this.
func (m *model) assembleScreen(actions []Action) Workspace {
	width := m.width
	if width < 40 {
		width = 40
	}

	mode := m.resolver.Current()
	modeColor := m.modeStyle(mode)

	// ── Input region: autocomplete + separators + prompt ──
	var inputView strings.Builder
	if m.autocompleteActive && len(m.autocompleteItems) > 0 {
		inputView.WriteString(m.renderAutocompleteDropdown(width))
	}
	inputView.WriteString(rule(width, modeColor) + "\n")
	promptLabel := modeColor.Render(mode.String() + " " + Icon.Command)
	inputView.WriteString(promptLabel + " " + m.ti.View() + "\n")
	inputView.WriteString(rule(width, modeColor))

	// ── Footer: status bar (telemetry with capabilities inlined) ──
	footerView := m.renderStatusBar(width, actions)

	// ── Proposal dock (conditional) ──
	var proposalDockView string
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		proposalDockView = m.renderProposalBlock()
	}

	// ── Size the viewport to fill the remaining space (no manual constants) ──
	inputHeight := lipgloss.Height(inputView.String())
	statusHeight := lipgloss.Height(footerView)
	proposalHeight := lipgloss.Height(proposalDockView)

	m.Viewport.Height = m.height - inputHeight - statusHeight - proposalHeight
	if m.Viewport.Height < 1 {
		m.Viewport.Height = 1
	}

	return Workspace{
		Viewport:     m.Viewport.View(),
		ProposalDock: proposalDockView,
		Input:        inputView.String(),
		Footer:       footerView,
		Actions:      actions,
	}
}

// renderProposalBlock renders the interactive proposal/processing dock
// between the viewport and the input line, framed for clear isolation.
func (m *model) renderProposalBlock() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	var b strings.Builder

	switch m.state {
	case StateAwaitingApproval:
		if len(m.pendingProposals) == 0 {
			return ""
		}
		p := m.pendingProposals[0]
		if p.Diff == "" {
			b.WriteString("  " + infoStyle.Render("Waiting for proposal payload...\n"))
			break
		}
		vm := ToMutationCardViewModelFromProposal(p)
		mr := &MutationRenderer{Width: width, ScrollOffset: m.proposalDiffOffset}
		b.WriteString(mr.Render(vm))

	case StateProcessing:
		frame := ProposalSpinnerFrames[m.spinnerFrame%len(ProposalSpinnerFrames)]
		sp := SpinnerStyle.Render(frame)
		b.WriteString("  " + sp + " " + infoStyle.Render("Processing file mutations... Please wait."))
		if len(m.pendingProposals) > 0 {
			b.WriteString(" " + tracerStyle.Render(m.pendingProposals[0].Target.QualifiedName))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// modeStyle returns the appropriate lipgloss style for a mode.
// Core engineering modes (ask, build, investigate, review) get their
// unique thematic color. Secondary/utils modes get unified subtle styling.
func (m *model) modeStyle(mode modes.Mode) lipgloss.Style {
	if isCoreEngineeringMode(mode) {
		return modeBoldFgStyles[mode]
	}
	return secondaryModeStyle
}

// ── Autocomplete Dropdown ──────────────────────────────────────────────────

// ── Command section categories for autocomplete ───────────────────────

type cmdCategory int

const (
	catPrimaryMode cmdCategory = iota
	catSystemCommand
	catModeContextual
)

type cmdSection struct {
	title string
	style lipgloss.Style
	items []string
}

func (m *model) cmdCategoryFor(item string) cmdCategory {
	for _, c := range coreModes {
		if c == item {
			return catPrimaryMode
		}
	}
	for _, c := range globalCommands {
		if c == item {
			return catSystemCommand
		}
	}
	return catModeContextual
}

func (m *model) buildCmdSections(items []string) []cmdSection {
	var primary, sys, ctx []string
	for _, it := range items {
		switch m.cmdCategoryFor(it) {
		case catPrimaryMode:
			primary = append(primary, it)
		case catSystemCommand:
			sys = append(sys, it)
		case catModeContextual:
			ctx = append(ctx, it)
		}
	}
	var sections []cmdSection
	if len(primary) > 0 {
		sections = append(sections, cmdSection{title: "PRIMARY MODES", style: accentStyle, items: primary})
	}
	if len(sys) > 0 {
		sections = append(sections, cmdSection{title: "SYSTEM COMMANDS", style: subtleStyle, items: sys})
	}
	if len(ctx) > 0 {
		sections = append(sections, cmdSection{title: "MODE CONTEXTUAL", style: mutedStyle, items: ctx})
	}
	return sections
}

// renderAutocompleteDropdown renders a compact border-box suggestion list
// positioned directly above the top parallel line. For file selections (@),
// it uses a two-column layout with filename on the left and directory on the
// right. Command selections (/) are displayed in categorized section blocks.
func (m *model) renderAutocompleteDropdown(width int) string {
	if len(m.autocompleteItems) == 0 || !m.autocompleteActive {
		return ""
	}
	var b strings.Builder

	maxShow := 8
	list := m.autocompleteItems
	if len(list) > maxShow {
		list = list[:maxShow]
	}

	// Pre-compiled styles for the dropdown
	highlightedBgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(colorOverlay))

	// Top border with title
	title := "Context Selection"
	titleSection := "── " + title + " ──"
	topFiller := width - lipgloss.Width(titleSection) - 2
	if topFiller < 0 {
		topFiller = 0
	}
	b.WriteString(subtleStyle.Render("╭"+titleSection+strings.Repeat("─", topFiller)+"╮") + "\n")

	if m.autocompleteType == "file" {
		if width < dropdownTwoColThreshold {
			// Narrow/split-pane fallback: the directory column has no room
			// to breathe next to the filename, so collapse to a single
			// column showing just the (possibly truncated) path.
			for i, item := range list {
				icon := "↪ "
				if i == m.autocompleteIdx {
					icon = "▶ "
				}
				display := icon + item
				maxContent := width - 4
				if maxContent < 6 {
					maxContent = 6
				}
				if lipgloss.Width(display) > maxContent {
					display = display[:maxContent-1] + "…"
				}
				pad := width - lipgloss.Width(display) - 4
				if pad < 0 {
					pad = 0
				}
				rowString := display + strings.Repeat(" ", pad)
				if i == m.autocompleteIdx {
					b.WriteString("│ " + highlightedBgStyle.Render(rowString) + " │\n")
				} else {
					b.WriteString("│ " + textStyle.Render(rowString) + " │\n")
				}
			}
		} else {
			for i, item := range list {
				name := filepath.Base(item)
				dir := filepath.Dir(item)
				if dir == "." {
					dir = "./"
				}

				icon := "↪ "
				if i == m.autocompleteIdx {
					icon = "▶ "
				}

				// Left column: file name (high contrast)
				leftSide := textStyle.Render(icon + name)
				// Right column: parent directory (low contrast #6c7086)
				rightSide := mutedStyle.Render(dir + " ")

				paddingCount := width - lipgloss.Width(icon+name) - lipgloss.Width(dir+" ") - 4
				if paddingCount < 0 {
					paddingCount = 0
				}

				if i == m.autocompleteIdx {
					rowString := leftSide + strings.Repeat(" ", paddingCount) + rightSide
					b.WriteString("│ " + highlightedBgStyle.Render(rowString) + " │\n")
				} else {
					b.WriteString("│ " + leftSide + strings.Repeat(" ", paddingCount) + rightSide + " │\n")
				}
			}
		}
	} else {
		sections := m.buildCmdSections(list)
		itemIdx := 0
		for _, sec := range sections {
			// Section header
			headerStr := "  " + sec.style.Render(sec.title)
			hPad := width - lipgloss.Width(headerStr) - 4
			if hPad < 0 {
				hPad = 0
			}
			b.WriteString("│ " + headerStr + strings.Repeat(" ", hPad) + " │\n")

			for _, item := range sec.items {
				display := item
				lw := lipgloss.Width(display)
				maxContent := width - 8
				if maxContent < 10 {
					maxContent = 10
				}
				if lw > maxContent {
					display = display[:maxContent-1] + "…"
					lw = lipgloss.Width(display)
				}
				pad := strings.Repeat(" ", width-lw-6)

				rowString := display + pad
				if itemIdx == m.autocompleteIdx {
					b.WriteString("│ " + highlightedBgStyle.Render("▶ "+rowString) + " │\n")
				} else {
					b.WriteString("│ " + dimmedStyle.Render("↪ "+rowString) + " │\n")
				}
				itemIdx++
			}
		}
	}

	// Bottom border
	b.WriteString(subtleStyle.Render("╰"+strings.Repeat("─", width-2)+"╯") + "\n")

	return b.String()
}

// ── Help Overlay ───────────────────────────────────────────────────────────

// renderHelpOverlay displays IZEN's philosophy, operational rules, and
// keyboard shortcuts as a full-height overlay panel.
func (m *model) renderHelpOverlay() string {
	lines := []string{
		"",
		boldAccentStyle.Render("  " + Icon.Spark + " IZEN  "),
		textStyle.Render("  engineering intelligence · human in control"),
		"",
		subtleStyle.Render("  ─── Modes ───"),
		"  " + accentStyle.Render("/ask") + "         " + dimmedStyle.Render("explain, inspect, understand"),
		"  " + orangeStyle.Render("/plan") + "        " + dimmedStyle.Render("break down, structure, design"),
		"  " + blueStyle.Render("/build") + "       " + dimmedStyle.Render("implement, refactor, elevate"),
		"  " + greenStyle.Render("/investigate") + "  " + dimmedStyle.Render("debug, trace, root-cause"),
		"  " + yellowStyle.Render("/review") + "      " + dimmedStyle.Render("analyze, critique, improve"),
		"",
		subtleStyle.Render("  ─── Commands ───"),
		"  " + dimmedStyle.Render("/help  /?  /mode  /objective  /clear  /drop  /undo"),
		"  " + dimmedStyle.Render("/commit  /checkpoint  /arch  /quit"),
		"  " + dimmedStyle.Render("!<cmd>          run a shell command"),
		"  " + dimmedStyle.Render("@<path>         attach a file"),
		"",
		subtleStyle.Render("  ─── Shortcuts ───"),
		"  " + dimmedStyle.Render("Esc (×3)        quit IZEN"),
		"  " + dimmedStyle.Render("↑/↓             history navigation"),
		"  " + dimmedStyle.Render("Tab/Enter       complete autocomplete"),
		"  " + dimmedStyle.Render("?               toggle this help overlay"),
		"",
		mutedStyle.Render("  press " + boldTextStyle.Render("Esc") + " or " + boldTextStyle.Render("?") + " to close"),
		"",
	}

	return strings.Join(lines, "\n")
}

// ── Runtime Status ────────────────────────────────────────────────────

// renderRuntimeStatus renders a single telemetry line with zero duplication.
// Format: ● <model> │ <tokens> tkn
//
// DUAL-SPINNER ARCHITECTURE:
//
//	A. Loading Spinner (rect/braille dots) — shown strictly in the footer
//	   status bar, immediately preceding the active LLM model name, during
//	   ANY active background execution (/commit, $test, $fix, $run, $log,
//	   streaming, or mutation processing). This spinner type is the
//	   rectangular braille matrix (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏).
//
//	B. Streaming Spinner (star/flowing glyph) — rendered ONLY inside the
//	   viewport content (chat history) during active token streaming, and
//	   NEVER in the status bar or input prompt line.
//
// The two spinners occupy separate rendering layers and are triggered by
// orthogonal state flags: m.streaming (view) vs. m.agentRunning/reviewRunning
// (status bar). They cannot be collapsed or swapped.
// renderRuntimeStatus renders the runtime metadata line — the lowest visual
// priority element on screen. Format:
//
//	[spinner] model · context · tokens · cost · checkpoint
//
// Every segment uses a muted/dimmed style so the line never competes with the
// primary document content. Segments drop in priority order as the pane narrows.
func (m *model) renderRuntimeStatus(width int) string {
	var b strings.Builder

	// ── Loading Spinner (rect/braille): background execution indicator ──
	// Active during streaming, background tasks, or mutation processing.
	if m.streaming || m.agentRunning || m.reviewRunning || m.state == StateProcessing {
		b.WriteString(m.renderRectSpinner())
	} else {
		b.WriteString(dimmedStyle.Render("●"))
	}
	b.WriteByte(' ')

	// AI INTERRUPT ENGINE: high-visibility indicator when streaming
	if m.streaming {
		b.WriteString(interruptLabelStyle.Render("⏹ Ctrl+C interrupt "))
	}

	// Agent label — shown immediately after the spinner, before model name
	if m.agentRunning || m.reviewRunning {
		b.WriteString(infoStyle.Render(m.agentLabel))
		b.WriteByte(' ')
	}

	// Metadata segments: model · context · tokens · cost · checkpoint
	var meta []string

	// Model name — dropped first when the pane is too narrow to fit it.
	if width >= minimalStatusThreshold {
		meta = append(meta, dimmedStyle.Render(m.cfg.ActiveModelName()))
	}

	// Active context ID — conveys workspace continuity without shouting.
	if m.sess != nil && m.sess.ContextID != "" {
		ctx := m.sess.ContextID
		if len(ctx) > 9 {
			ctx = ctx[:9]
		}
		meta = append(meta, mutedStyle.Render(Icon.Context+" "+ctx))
	}

	// Tokens — always shown; this is the minimum viable status line.
	if m.TotalTokens > 0 {
		meta = append(meta, dimmedStyle.Render(strconv.Itoa(m.TotalTokens)+" tok"))
	} else {
		meta = append(meta, dimmedStyle.Render("0 tok"))
	}

	// Accumulated cost — dropped before checkpoint as panes narrow.
	if m.AccumulatedCost > 0 {
		meta = append(meta, dimmedStyle.Render(fmt.Sprintf("$%.3f", m.AccumulatedCost)))
	}

	// Checkpoint (truncated) — the least essential glance-able telemetry.
	if width >= compactStatusThreshold {
		if cp := m.latestCheckpointID(); cp != "" {
			cp = strings.TrimPrefix(cp, "cp-")
			if len(cp) > 7 {
				cp = cp[:7]
			}
			meta = append(meta, dimmedStyle.Render("cp-"+cp))
		}
	}

	sep := dimmedStyle.Render(" · ")
	b.WriteString(strings.Join(meta, sep))

	return b.String()
}

var devTips = []string{
	"Pro Tip: Press [Esc] three times quickly anywhere to cleanly safely quit IZEN.",
	"Pro Tip: Use '@path' to attach files/folders. Multi-column layout automatically isolates parent package names.",
	"Pro Tip: IZEN locks execution boundaries. /ask is strictly Read-Only, use /build to run shell mutations.",
	"Pro Tip: Run !<command> to escape the prompt and execute short native shell actions synchronously.",
	"Pro Tip: Toggle the global help dashboard overlay instantly by pressing [?] during idle input states.",
}

// renderStatusBar renders the runtime telemetry line — the lowest visual
// priority element, pinned to the bottom. When capabilities are exposed by the
// current workflow context, they are rendered INLINE on the same line,
// right-aligned, so the bar never grows an extra row and the prompt above
// stays perfectly still. The renderer never decides which capabilities exist;
// it only projects the slice handed to it by the workflow layer.
//
// Layout (capability active):
//
//	qwen2.5-coder · 9 tok · cp-1784040                     ⌥A Investigate Root Cause
//
// Layout (no capability):
//
//	qwen2.5-coder · 9 tok · cp-1784040
func (m *model) renderStatusBar(width int, actions []Action) string {
	chip := renderActions(actions)
	if chip == "" {
		return m.renderRuntimeStatus(width)
	}
	// Reserve the chip's width on the right and let the status line shrink its
	// own segments to fit the remaining budget, so the two never collide.
	gap := 2
	chipW := lipgloss.Width(chip)
	statusBudget := width - chipW - gap
	if statusBudget < 0 {
		statusBudget = 0
	}
	status := m.renderRuntimeStatus(statusBudget)
	// Right-align the chip with at least `gap` spaces of breathing room.
	pad := width - lipgloss.Width(status) - chipW
	if pad < gap {
		pad = gap
	}
	return status + strings.Repeat(" ", pad) + chip
}

// renderActions renders the currently available capabilities as inline,
// right-aligned tokens: a hotkey + label pair. It is a pure projection of the
// Action slice — it inspects no mode, no handoff state, and no engine flag.
// Returns "" when no capability is available.
// NOTE: Capability hotkeys use alt+ modifier — single-letter hotkeys are banned
// to prevent key collisions with normal prompt input.
func renderActions(actions []Action) string {
	displayKey := func(key string) string {
		// Render the alt/option modifier as the ⌥ glyph (e.g. "alt+c" → "⌥C").
		if len(key) > 4 && key[:4] == "alt+" {
			return "⌥" + strings.ToUpper(key[4:])
		}
		return strings.ToUpper(key)
	}
	var b strings.Builder
	for _, act := range actions {
		if !act.Enabled {
			continue
		}
		hotkey := hotkeyStyle.Render(displayKey(act.Shortcut))
		label := textStyle.Render(act.Label)
		if b.Len() > 0 {
			b.WriteString("  ")
		}
		// Inline chip shows the hotkey + label only; the command itself is
		// executed on activation and need not be displayed.
		b.WriteString(hotkey + " " + label)
	}
	return b.String()
}

// ── Startup banner ────────────────────────────────────────────────────

var bannerModes = []struct{ name, desc string }{
	{"/ask", "explain, inspect, understand"},
	{"/plan", "break down, structure, design"},
	{"/build", "implement, refactor, elevate"},
	{"/investigate", "debug, trace, root-cause"},
	{"/review", "analyze, critique, improve"},
}

func (m *model) getGreeting() string {
	userName := m.userName
	if userName == "" {
		userName = "developer"
	}
	hour := time.Now().Hour()
	switch {
	case hour >= 5 && hour < 12:
		return fmt.Sprintf("Hi %s, Good morning!", userName)
	case hour >= 12 && hour < 17:
		return fmt.Sprintf("Hi %s, Good afternoon!", userName)
	case hour >= 17 && hour < 21:
		return fmt.Sprintf("Hi %s, Good evening!", userName)
	default:
		return fmt.Sprintf("Hi %s, night owl!", userName)
	}
}

func (m *model) renderStartupBanner(termWidth int) string {
	if termWidth < wideBannerThreshold {
		return m.renderStartupBannerCompact(termWidth)
	}

	innerW := termWidth - 6
	if innerW < 60 {
		innerW = 60
	}

	const robotW = 6
	const sep = "  "

	cleanRobotArt := []string{
		"  ██  ",
		" █  █ ",
		" ████ ",
		" █ ██ ",
		" █  █ ",
	}

	rightCol := make([]string, 0, 4+len(bannerModes))
	rightCol = append(rightCol,
		boldAccentStyle.Render(m.getGreeting()),
		textStyle.Render("engineering intelligence."),
		textStyle.Render("human in control."),
		"",
	)
	for _, mode := range bannerModes {
		nameS := boldTextStyle.Render(mode.name)
		descS := mutedStyle.Render(mode.desc)
		padLen := max(1, 15-lipgloss.Width(nameS))
		rightCol = append(rightCol, nameS+strings.Repeat(" ", padLen)+descS)
	}

	var rows []string
	totalRows := len(cleanRobotArt)
	if len(rightCol) > totalRows {
		totalRows = len(rightCol)
	}
	for i := 0; i < totalRows; i++ {
		var robotPart string
		if i < len(cleanRobotArt) {
			robotPart = boldAccentStyle.Render(padRight(cleanRobotArt[i], robotW))
		} else {
			robotPart = strings.Repeat(" ", robotW)
		}
		var rightPart string
		if i < len(rightCol) {
			rightPart = rightCol[i]
		}
		rows = append(rows, robotPart+sep+rightPart)
	}

	divider := subtleStyle.Render(strings.Repeat("─", innerW-2))
	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	metaParts := []string{
		mutedStyle.Render(projectPathDisplay()),
		mutedStyle.Render(provider + " " + modelName),
	}
	if branch, err := m.gitEng.Branch(); err == nil && branch != "" {
		metaParts = append(metaParts, mutedStyle.Render("git ("+branch+")"))
	}
	metaSep := subtleStyle.Render(" · ")
	meta := strings.Join(metaParts, metaSep)

	tip := mutedStyle.Render(devTips[rand.Intn(len(devTips))])
	rows = append(rows, divider, meta, "", tip)
	body := strings.Join(rows, "\n")

	box := bannerBorderStyle.BorderTop(false).Width(termWidth - 2).Render(body)
	boxLines := strings.Split(box, "\n")
	boxWidth := 0
	if len(boxLines) > 0 {
		boxWidth = lipgloss.Width(boxLines[0])
	}
	title := boldAccentStyle.Render("izen") + mutedStyle.Render(" v"+version)
	titleBar := renderTitledTopBorder(boxWidth, title)

	return titleBar + "\n" + box
}

// renderStartupBannerCompact renders a single-column banner for narrow or
// split terminal panes. The two-column robot-art layout used by
// renderStartupBanner assumes a minimum width to stay readable; below
// wideBannerThreshold it would either overflow the pane or force a wider
// box than the pane actually has. This stacks the same content vertically
// instead, scaled to the real (possibly very narrow) termWidth.
func (m *model) renderStartupBannerCompact(termWidth int) string {
	innerW := termWidth - 4
	if innerW < 20 {
		innerW = 20
	}

	initialCap := 4 + 2*len(bannerModes) + 2
	rows := make([]string, 0, initialCap)
	rows = append(rows,
		boldAccentStyle.Render(m.getGreeting()),
		textStyle.Render("engineering intelligence."),
		textStyle.Render("human in control."),
		"",
	)
	for _, mode := range bannerModes {
		rows = append(rows, boldTextStyle.Render(mode.name))
		rows = append(rows, "  "+mutedStyle.Render(mode.desc))
	}

	divider := subtleStyle.Render(strings.Repeat("─", innerW))

	// Meta line: project path always shown; provider/model and git branch
	// are dropped as the pane narrows further, same priority order as the
	// runtime status line.
	metaParts := []string{mutedStyle.Render(projectPathDisplay())}
	if termWidth >= compactStatusThreshold {
		provider := m.cfg.ActiveProviderName()
		modelName := m.cfg.ActiveModelName()
		metaParts = append(metaParts, mutedStyle.Render(provider+" "+modelName))
		if branch, err := m.gitEng.Branch(); err == nil && branch != "" {
			metaParts = append(metaParts, mutedStyle.Render("git ("+branch+")"))
		}
	}
	meta := strings.Join(metaParts, subtleStyle.Render(" · "))

	rows = append(rows, divider, meta)

	body := strings.Join(rows, "\n")
	box := bannerBorderStyle.BorderTop(false).Width(termWidth - 2).Render(body)
	boxLines := strings.Split(box, "\n")
	boxWidth := 0
	if len(boxLines) > 0 {
		boxWidth = lipgloss.Width(boxLines[0])
	}
	title := boldAccentStyle.Render("izen") + mutedStyle.Render(" v"+version)
	titleBar := renderTitledTopBorder(boxWidth, title)

	return titleBar + "\n" + box
}

// projectPathDisplay returns the current working directory formatted for
// the startup banner, abbreviating the user's home directory to "~" (e.g.
// "~/notes") the same way the shell/prompt convention does. Falls back to
// "." if the working directory can't be resolved.
func projectPathDisplay() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if wd == home {
			return "~"
		}
		if rel, err := filepath.Rel(home, wd); err == nil && !strings.HasPrefix(rel, "..") {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return wd
}

func padRight(s string, n int) string {
	sw := len(s)
	if sw >= n {
		return s
	}
	return s + strings.Repeat(" ", n-sw)
}

// renderTitledTopBorder builds a top-border line with a left-aligned label
// embedded in it, safely adapting to extremely narrow screen splits without breaking.
func renderTitledTopBorder(totalWidth int, label string) string {
	border := bannerBorderStyle.GetBorderStyle()

	fill := border.Top
	if fill == "" {
		fill = "─"
	}
	left := border.TopLeft
	if left == "" {
		left = fill
	}
	right := border.TopRight
	if right == "" {
		right = fill
	}

	borderColor := lipgloss.NewStyle().Foreground(bannerBorderStyle.GetBorderTopForeground())

	padded := " " + label + " "
	labelW := lipgloss.Width(padded)
	innerW := totalWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if innerW < 0 {
		innerW = 0
	}
	if labelW >= innerW {
		return borderColor.Render(left + strings.Repeat(fill, innerW) + right)
	}

	// Dynamic safe left-alignment scaling
	leftFillN := 4
	if leftFillN+labelW > innerW {
		leftFillN = innerW - labelW
	}
	if leftFillN < 0 {
		leftFillN = 0
	}
	rightFillN := innerW - labelW - leftFillN

	return borderColor.Render(left+strings.Repeat(fill, leftFillN)) +
		padded +
		borderColor.Render(strings.Repeat(fill, rightFillN)+right)
}

// ── Record renderer (for viewport content) ────────────────────

func (m *model) printRecord(rec record) string {
	gutter := gutterFor(rec.role)
	content := rec.text

	if rec.role == roleAI {
		return m.renderAIResponseBlocks(content, m.width)
	}

	availableWidth := m.width - 2
	if availableWidth < 20 {
		availableWidth = 20
	}

	wrapStringToWidth := func(text string, maxW int) []string {
		if len(text) == 0 {
			return []string{""}
		}
		var chunks []string
		words := strings.Fields(text)
		if len(words) == 0 {
			runes := []rune(text)
			for i := 0; i < len(runes); i += maxW {
				end := i + maxW
				if end > len(runes) {
					end = len(runes)
				}
				chunks = append(chunks, string(runes[i:end]))
			}
			return chunks
		}

		var currentLine strings.Builder
		for _, word := range words {
			if currentLine.Len()+1+len(word) > maxW {
				chunks = append(chunks, currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			} else {
				if currentLine.Len() > 0 {
					currentLine.WriteString(" ")
				}
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			chunks = append(chunks, currentLine.String())
		}
		return chunks
	}

	wrappedLines := wrapStringToWidth(content, availableWidth)

	switch rec.role {
	case roleUser:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = userBgStyle.Render(" " + line)
		}
		return strings.Join(styledLines, "\n")
	case roleError:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + errorStyle.Render(line)
		}
		return strings.Join(styledLines, "\n")
	case roleStatus:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + dimmedStyle.Render(line)
		}
		return strings.Join(styledLines, "\n")
	default:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = gutter + outputStyle.Render(line)
		}
		return strings.Join(styledLines, "\n")
	}
}

// ── AST Trace Renderer ────────────────────────────────────────────

// ── Widget Box & Semantic Renderers ───────────────────────────────────

// widgetIcon returns a monochrome glyph for semantic widget headings.
// Icons are chosen to improve scanability — one glyph per domain concept.
func widgetIcon(title string) string {
	switch strings.ToLower(title) {
	case "plan":
		return Icon.Plan + " "
	case "edit", "diff":
		return Icon.Edit + " "
	case "command":
		return Icon.Command + " "
	case "evidence":
		return Icon.Evidence + " "
	case "risk analysis", "risk":
		return Icon.Risk + " "
	case "table":
		return Icon.Table + " "
	case "summary":
		return Icon.Summary + " "
	default:
		return Icon.Summary + " "
	}
}

func renderWidget(title string, content string, width int, accentHex string) string {
	var b strings.Builder

	// Title as LEVEL 2 heading with semantic icon
	icon := widgetIcon(title)
	var titleColor lipgloss.Style
	switch accentHex {
	case colorModeAsk:
		titleColor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeAsk))
	case colorModePlan:
		titleColor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModePlan))
	case colorModeBuild:
		titleColor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeBuild))
	case colorModeInvestigate:
		titleColor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeInvestigate))
	case colorModeReview:
		titleColor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorModeReview))
	default:
		titleColor = boldTextStyle
	}
	b.WriteString(titleColor.Render(icon + title))
	b.WriteByte('\n')

	// Content lines with left-side anchors
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for _, line := range lines {
		lineTrimmed := strings.TrimRight(line, " \r")
		b.WriteString(subtleStyle.Render("│ "))
		b.WriteString(lineTrimmed)
		b.WriteByte('\n')
	}

	return b.String()
}

func (m *model) renderAIResponseBlocks(content string, width int) string {
	return m.renderStreamingContent(content, width)
}

func parseAIContent(content string) []contentBlock {
	var blocks []contentBlock
	lines := strings.Split(content, "\n")

	var currentBlock []string
	var currentKind = blockText

	inCodeBlock := false
	codeBlockLang := ""

	flush := func() {
		if len(currentBlock) == 0 {
			return
		}
		raw := strings.Join(currentBlock, "\n")
		raw = strings.TrimSpace(raw)
		if raw != "" {
			blocks = append(blocks, contentBlock{kind: currentKind, raw: raw})
		}
		currentBlock = nil
		currentKind = blockText
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				currentBlock = append(currentBlock, line)
				inCodeBlock = false
				flush()
			} else {
				flush()
				inCodeBlock = true
				codeBlockLang = strings.TrimPrefix(trimmed, "```")
				switch {
				case strings.HasPrefix(codeBlockLang, "diff"):
					currentKind = blockDiff
				case codeBlockLang == "bash" || codeBlockLang == "sh":
					currentKind = blockCommand
				default:
					currentKind = blockText
				}
				currentBlock = append(currentBlock, line)
			}
			continue
		}

		if inCodeBlock {
			currentBlock = append(currentBlock, line)
			continue
		}

		// Outside code block
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			if currentKind != blockTable {
				flush()
				currentKind = blockTable
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		isPlanLine := strings.HasPrefix(trimmed, "✓") || strings.HasPrefix(trimmed, "●") ||
			strings.HasPrefix(trimmed, "○") || strings.HasPrefix(trimmed, "✗") ||
			strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") ||
			strings.HasPrefix(trimmed, "- [/]")

		if isPlanLine || (strings.HasPrefix(strings.ToLower(trimmed), "plan") && i < len(lines)-1 && (strings.HasPrefix(strings.TrimSpace(lines[i+1]), "-") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "1.") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "✓") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "●"))) {
			if currentKind != blockPlan {
				flush()
				currentKind = blockPlan
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "evidence") || strings.HasPrefix(strings.ToLower(trimmed), "source:") || strings.HasPrefix(strings.ToLower(trimmed), "confidence:") {
			if currentKind != blockEvidence {
				flush()
				currentKind = blockEvidence
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "risk") || strings.HasPrefix(strings.ToLower(trimmed), "score:") || strings.HasPrefix(strings.ToLower(trimmed), "breaking api:") {
			if currentKind != blockRisk {
				flush()
				currentKind = blockRisk
			}
			currentBlock = append(currentBlock, line)
			continue
		}

		if currentKind != blockText {
			flush()
		}

		currentBlock = append(currentBlock, line)
	}
	flush()
	return blocks
}

func renderTable(rawTable string, width int) string {
	lines := strings.Split(rawTable, "\n")
	var grid [][]string
	var colWidths []int

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "---") && !strings.Contains(trimmed, "[a-zA-Z]") {
			clean := strings.ReplaceAll(trimmed, "|", "")
			clean = strings.ReplaceAll(clean, "-", "")
			clean = strings.ReplaceAll(clean, " ", "")
			if clean == "" {
				continue
			}
		}
		parts := strings.Split(trimmed, "|")
		var row []string
		for _, p := range parts {
			row = append(row, strings.TrimSpace(p))
		}
		if len(row) > 0 && row[0] == "" {
			row = row[1:]
		}
		if len(row) > 0 && row[len(row)-1] == "" {
			row = row[:len(row)-1]
		}
		if len(row) > 0 {
			grid = append(grid, row)
			for len(colWidths) < len(row) {
				colWidths = append(colWidths, 0)
			}
			for idx, val := range row {
				valLen := lipgloss.Width(val)
				if valLen > colWidths[idx] {
					colWidths[idx] = valLen
				}
			}
		}
	}

	if len(grid) == 0 {
		return rawTable
	}

	// Calculate sum of column widths including padding and grid lines
	totalTableW := 0
	for _, w := range colWidths {
		totalTableW += w + 3
	}
	totalTableW += 1

	// Fallback to compact key-value listing if split terminal screen is too small
	if totalTableW > width || width < 60 {
		var b strings.Builder
		headers := grid[0]
		for rowIdx := 1; rowIdx < len(grid); rowIdx++ {
			row := grid[rowIdx]
			if rowIdx > 1 {
				b.WriteString("\n" + strings.Repeat("─", width) + "\n")
			}
			for colIdx, val := range row {
				header := fmt.Sprintf("Col %d", colIdx+1)
				if colIdx < len(headers) {
					header = headers[colIdx]
				}
				line := fmt.Sprintf("• %s: %s", header, val)
				wrapped := wrapStreamText(line, width)
				b.WriteString(strings.Join(wrapped, "\n") + "\n")
			}
		}
		return strings.TrimSuffix(b.String(), "\n")
	}

	var b strings.Builder
	b.WriteString("┌")
	for idx, w := range colWidths {
		if idx > 0 {
			b.WriteString("┬")
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString("┐\n")

	for rowIdx, row := range grid {
		if rowIdx > 0 && rowIdx == 1 {
			b.WriteString("├")
			for idx, w := range colWidths {
				if idx > 0 {
					b.WriteString("┼")
				}
				b.WriteString(strings.Repeat("─", w+2))
			}
			b.WriteString("┤\n")
		}

		b.WriteString("│")
		for idx, w := range colWidths {
			val := ""
			if idx < len(row) {
				val = row[idx]
			}
			padded := " " + val + " "
			extra := w + 2 - lipgloss.Width(padded)
			if extra > 0 {
				padded += strings.Repeat(" ", extra)
			}
			b.WriteString(padded)
			b.WriteString("│")
		}
		b.WriteString("\n")
	}

	b.WriteString("└")
	for idx, w := range colWidths {
		if idx > 0 {
			b.WriteString("┴")
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString("┘")

	return b.String()
}

func parseDiffMetadata(diffBody string) (file, symbol, linesRange, cleanDiff string) {
	lines := strings.Split(diffBody, "\n")
	var diffLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		}
		if strings.HasPrefix(line, "@@") {
			parts := strings.Split(line, "@@")
			if len(parts) >= 3 {
				header := strings.TrimSpace(parts[1])
				symbol = strings.TrimSpace(parts[2])

				subparts := strings.Fields(header)
				if len(subparts) >= 2 {
					newRange := strings.TrimPrefix(subparts[1], "+")
					rangeParts := strings.Split(newRange, ",")
					if len(rangeParts) >= 2 {
						start, _ := strconv.Atoi(rangeParts[0])
						count, _ := strconv.Atoi(rangeParts[1])
						linesRange = fmt.Sprintf("Lines %d-%d", start, start+count-1)
					}
				}
			}
		}
		diffLines = append(diffLines, line)
	}
	cleanDiff = strings.Join(diffLines, "\n")
	return
}
