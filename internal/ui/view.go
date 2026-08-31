package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/ui/status"
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
	base := renderWorkspace(m.BuildWorkspace())
	if m.pendingQuitConfirm {
		return m.renderQuitConfirmOverlay(base)
	}
	return base
}

// renderWorkspace is the ONLY rendering primitive. It projects a Workspace
// onto the terminal with no awareness of mode, workflow, or UI logic.
// The screen is partitioned into three vertical regions:
//   - Fixed top region: Header (WorkflowState + toast overlay)
//   - Scrollable middle region: Viewport + ProposalDock + Input
//   - Fixed bottom region: Footer (single-line lifecycle bar)
func renderWorkspace(ws Workspace) string {
	if ws.Overlay != "" {
		return ws.Overlay
	}

	// Build the scrollable content body (viewport + proposal + input).
	var bodyParts []string
	if ws.Viewport != "" {
		bodyParts = append(bodyParts, ws.Viewport)
	}
	if ws.ProposalDock != "" {
		bodyParts = append(bodyParts, ws.ProposalDock)
	}
	if ws.Input != "" {
		bodyParts = append(bodyParts, ws.Input)
	}
	body := lipgloss.JoinVertical(lipgloss.Left, bodyParts...)

	// Partition into fixed header / scrollable body / fixed footer.
	// If either fixed region is empty, the layout falls back to a simple
	// vertical join so the caller sees no structural change.
	return Partition(body, ws.Header, ws.Footer)
}

// assembleScreen builds the Workspace's screen regions from the supplied
// capabilities. This is workflow/screen-assembly logic that belongs to the
// model layer, NOT the renderer: it computes region heights, sizes the
// viewport, and precomposes the input and footer regions. The renderer later
// projects the resulting Workspace without re-deriving any of this.
//
// Fixed Header and Fixed Footer are derived from RuntimeContext and
// WorkflowStateMachine — the model never caches these values independently.
func (m *model) assembleScreen(actions []Action) Workspace {
	width := m.width
	if width < 40 {
		width = 40
	}

	mode := m.resolver.Current()
	modeColor := m.modeStyle(mode)

	// ── Vi-mode override: use mauve border and status bar ──
	borderColor := modeColor
	if m.inViMode {
		borderColor = viBorderStyle
	}

	// ── Fixed Header / Footer (authoritative geometry source) ──
	headerView := m.renderTopBar(width)
	footerView := m.renderFixedFooter(width, actions)

	// ── Input region: autocomplete + separators + prompt ──
	var inputView strings.Builder
	if m.autocompleteActive && len(m.autocompleteItems) > 0 {
		inputView.WriteString(m.renderAutocompleteDropdown(width))
	}
	inputView.WriteString(rule(width, borderColor) + "\n")

	switch {
	case m.inViMode && m.viCmdMode:
		promptLabel := viCmdStyle.Render(m.viCmdBuf)
		inputView.WriteString(promptLabel + "\n")
	case m.inViMode:
		inputView.WriteString(viStatusStyle.Render("-- "+m.viModeLabel()+" --") + "\n")
	default:
		promptLabel := modeColor.Render(mode.String() + " " + Icon.Command)
		inputView.WriteString(promptLabel + " " + m.renderPromptView() + "\n")
	}
	inputView.WriteString(rule(width, borderColor))

	// ── Proposal dock (conditional) — floats above Input ──
	// NOTE: shimmerActive no longer triggers the proposalDock — the loading
	// indicator is rendered inside the viewport body (refreshViewportContent)
	// so it scrolls with the text content.
	var proposalDockView string
	if m.state == StateAwaitingApproval || m.state == StateProcessing {
		proposalDockView = m.renderProposalBlock()
	}

	// ── Size the viewport via single authoritative geometry ──
	geo := m.viewportGeometry()
	m.Viewport.Height = geo.Height

	return Workspace{
		Header:       headerView,
		Viewport:     m.Viewport.View(),
		ProposalDock: proposalDockView,
		Input:        inputView.String(),
		Footer:       footerView,
		Actions:      actions,
	}
}

// viModeLabel returns the current Vi-mode label for the status bar.
func (m *model) viModeLabel() string {
	switch {
	case m.viCmdMode && strings.HasPrefix(m.viCmdBuf, "/"):
		return "SEARCH"
	case m.viCmdMode && strings.HasPrefix(m.viCmdBuf, ":"):
		return "COMMAND"
	case m.viModeState == ViVisual:
		return "VI VISUAL"
	default:
		return "VI NORMAL"
	}
}

// renderReasoningBlock renders the collapsible reasoning block during streaming.
// Uses the ThinkingPanel for expanded/collapsed rendering.
// NOTE: during active streaming, live reasoning tokens are already shown via
// the ThinkingBuffer (event-driven) in renderStreamingContent. This block is
// the expanded/collapsible version for reviewing after streaming ends.
func (m *model) renderReasoningBlock(width int) string {
	if m.thinkingPanel == nil {
		return ""
	}
	if m.thinkingPanel.Len() == 0 {
		return ""
	}

	sp := m.renderFlowingSpinner()
	return m.thinkingPanel.Render(width, sp)
}

// wrapString wraps text to a given width, splitting at word boundaries.
// It delegates to the shared ANSI-aware, cell-accurate wrapper.
func wrapString(text string, width int) []string {
	return wrapText(text, width)
}

// renderProposalBlock renders the interactive proposal/processing dock
// between the viewport and the input line, framed for clear isolation.
func (m *model) renderProposalBlock() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	var b strings.Builder

	// ── AUTONOMY TARGET SELECTOR (§8) ──────────────────────────────
	// An ambiguous mutation target pauses with a small candidate selector. It
	// renders whenever a selector is outstanding, ahead of every other widget.
	if len(m.pendingAutonomyTargets) > 0 {
		b.WriteString(m.renderAutonomyTargetSelectorBlock(width))
		return b.String()
	}

	// ── AUTONOMY PROPOSAL (ask_user decision surface) ──────────────
	// The proposal is the ONLY authorization gate. It renders whenever a
	// proposal is outstanding, independent of the derived workflow state.
	if m.pendingAutonomyProposal != nil {
		b.WriteString(m.renderAutonomyProposalBlock(width))
		return b.String()
	}

	// NOTE: The shimmer loading dock has been moved into the viewport body
	// (refreshViewportContent) so it scrolls with the text content instead
	// of remaining fixed at the bottom above the prompt bar.

	switch m.state {
	case StateAwaitingApproval:
		// ── Production Autonomous Driver Boundary (Phase 6) ──────────
		// A parked driver run holds one human decision (approve / clarify /
		// inform). The boundary card is the ONLY decision surface for a parked
		// run; it renders ahead of every other approval widget.
		if m.autonomousParked() {
			b.WriteString(m.renderAutonomousBoundaryBlock(width))
			break
		}

		// ── Build Approval Permission Box (SHELL_EXEC gate) ─────────────
		if m.pendingBuildApproval && m.pendingBuildTask != nil {
			task := m.pendingBuildTask
			boxWidth := width - 4
			if boxWidth < 40 {
				boxWidth = 40
			}
			var content strings.Builder
			title := permissionTitleStyle.Render("▲ PERMISSION REQUIRED")
			action := permissionDescStyle.Render("Action:") + " " + boldTextStyle.Render("SHELL_EXEC")
			target := permissionTargetStyle.Render(task.Target)
			desc := permissionDescStyle.Render(fmt.Sprintf("Reason: %s", task.Description))
			sep := strings.Repeat("─", boxWidth-4)
			keys := fmt.Sprintf("%s  %s  %s",
				permissionKeyStyle.Render("Alt+A / Enter  Allow Once"),
				permissionKeyStyle.Render("Alt+L  Allow Always"),
				permissionKeyStyle.Render("Alt+R / Esc  Reject"),
			)
			content.WriteString(title + "\n")
			content.WriteString(action + "\n\n")
			content.WriteString(target + "\n")
			content.WriteString(desc + "\n")
			content.WriteString(" " + sep + "\n")
			content.WriteString(keys + "\n")
			b.WriteString(permissionBoxStyle.Render(content.String()))
			break
		}

		// ── Effort Selector ─────────────────────────────────────────────
		b.WriteString(m.renderEffortSelector(width))

		// ── Tool File Mutation Approval ─────────────────────────────────
		if m.toolCallBuffer != nil && m.toolCallBuffer.HasPending() {
			b.WriteString(m.renderToolCallApprovalBlock(width))
			break
		}

		if len(m.pendingProposals) == 0 {
			return b.String()
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
		// ── Truthful in-flight mutation dock ───────────────────────
		// Derived from the authoritative execution stage: the dock shows what
		// the runtime is ACTUALLY doing (apply/patch/model), never the generic
		// "Processing file mutations..." claim. When no authoritative stage
		// exists, nothing is rendered — empty is better than fake.
		frame := ProposalSpinnerFrames[m.spinnerFrame%len(ProposalSpinnerFrames)]
		sp := SpinnerStyle.Render(frame)
		stageLine := m.renderStageLine()
		if stageLine == "" {
			return b.String()
		}
		b.WriteString("  " + sp + " " + infoStyle.Render(stageLine))
		if len(m.pendingProposals) > 0 {
			b.WriteString(" " + tracerStyle.Render(m.pendingProposals[0].Target.QualifiedName))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderEffortSelector renders the interactive effort level selector widget.
func (m *model) renderEffortSelector(width int) string {
	var b strings.Builder

	effort := m.currentEffort
	levelStyle := effort.Style()
	desc := levelStyle.Render(effort.Description())

	b.WriteString("\n")
	b.WriteString("  " + boldTextStyle.Render(EffortSelectorLabel) + " ")
	b.WriteString(desc)
	b.WriteString("\n")

	labels := []string{"AUTO", "LOW", "MEDIUM", "HIGH"}
	effortValues := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh}

	b.WriteString("  ")
	for i, label := range labels {
		if i > 0 {
			b.WriteString(" ")
			b.WriteString(dimmedStyle.Render("│"))
			b.WriteString(" ")
		}
		if effortValues[i] == effort {
			b.WriteString(levelStyle.Bold(true).Render("[" + label + "]"))
		} else {
			b.WriteString(dimmedStyle.Render(label))
		}
	}
	b.WriteString("\n")
	b.WriteString("  " + mutedStyle.Render(EffortSelectorHint) + "\n")

	return b.String()
}

// renderToolCallApprovalBlock renders the approval controls for buffered tool calls.
func (m *model) renderToolCallApprovalBlock(width int) string {
	pending := m.toolCallBuffer.Pending()
	if len(pending) == 0 {
		return ""
	}

	var b strings.Builder
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	title := permissionTitleStyle.Render(Icon.Warning + " CODE MUTATION REQUIRES APPROVAL")
	b.WriteString(title + "\n")

	// List each pending tool call
	for i, tc := range pending {
		icon := Icon.Edit
		if tc.IsNew {
			icon = Icon.Spark
		}
		fmt.Fprintf(&b, "  %s %s\n", icon, tc.Path)
		if tc.Diff != "" {
			diffLines := strings.Split(tc.Diff, "\n")
			displayLines := diffLines
			if len(displayLines) > 8 {
				displayLines = displayLines[:8]
			}
			for _, dl := range displayLines {
				switch {
				case strings.HasPrefix(dl, "+") && !strings.HasPrefix(dl, "+++"):
					b.WriteString(greenStyle.Render("  "+dl) + "\n")
				case strings.HasPrefix(dl, "-") && !strings.HasPrefix(dl, "---"):
					b.WriteString(redStyle.Render("  "+dl) + "\n")
				default:
					b.WriteString(dimmedStyle.Render("  "+dl) + "\n")
				}
			}
			if len(diffLines) > 8 {
				b.WriteString(mutedStyle.Render(fmt.Sprintf("  ... %d more lines\n", len(diffLines)-8)))
			}
		}
		if i < len(pending)-1 {
			b.WriteString("\n")
		}
	}

	// Key bindings
	sep := strings.Repeat("─", boxWidth-4)
	b.WriteString(" " + sep + "\n")
	keys := fmt.Sprintf("%s  %s  %s  %s",
		permissionKeyStyle.Render("[A] "+ApprovalAccept),
		permissionKeyStyle.Render("[L] "+ApprovalAllowAll),
		permissionKeyStyle.Render("[R] "+ApprovalReject),
		permissionKeyStyle.Render("[E] "+ApprovalEditEffort),
	)
	b.WriteString(keys + "\n")

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

// maxSuggestionsVisible caps the dropdown rows; the highlighted row stays
// centered via autocompleteWindow.
const maxSuggestionsVisible = 8

// suggestHeader styles give each Context Selection section a bold, distinct
// header so the grouping reads at a glance.
var (
	suggestHeaderAccent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorAccent))
	suggestHeaderSubtle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorSubtle))
	suggestHeaderMuted  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorMuted))
)

// sectionStyleFor assigns the bold header style to a suggestion section title.
func sectionStyleFor(title string) lipgloss.Style {
	switch title {
	case "WORKSPACE CONTEXTS":
		return suggestHeaderAccent
	case "GLOBAL COMMANDS":
		return suggestHeaderSubtle
	default:
		return suggestHeaderMuted
	}
}

// autocompleteWindow returns the visible slice of suggestions and the absolute
// index of the first returned item, centering the highlighted row so it never
// scrolls out of the dropdown.
func (m *model) autocompleteWindow() ([]Suggestion, int) {
	all := m.autocompleteItems
	if len(all) <= maxSuggestionsVisible {
		return all, 0
	}
	start := m.autocompleteIdx - maxSuggestionsVisible/2
	if start < 0 {
		start = 0
	}
	if start+maxSuggestionsVisible > len(all) {
		start = len(all) - maxSuggestionsVisible
	}
	return all[start : start+maxSuggestionsVisible], start
}

// renderAutocompleteDropdown renders a compact border-box suggestion list
// positioned directly above the top parallel line. For scope selections (@)
// it uses a two-column layout with filename on the left and directory on the
// right. Command selections (/) and directives ($) are displayed in registry
// -driven categorized sections.
func (m *model) renderAutocompleteDropdown(width int) string {
	if len(m.autocompleteItems) == 0 || !m.autocompleteActive {
		return ""
	}
	list, baseIdx := m.autocompleteWindow()
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder

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

	if m.autocompleteType == "scope" {
		m.renderScopeRows(&b, list, m.autocompleteIdx, baseIdx, width, highlightedBgStyle)
	} else {
		m.renderSuggestSections(&b, list, m.autocompleteIdx, baseIdx, width, highlightedBgStyle)
	}

	// Bottom border
	b.WriteString(subtleStyle.Render("╰"+strings.Repeat("─", width-2)+"╯") + "\n")

	return b.String()
}

// renderScopeRows renders the two-column @ target rows: filename on the left,
// parent directory dimmed on the right. On narrow terminals it collapses to a
// single truncated path column.
func (m *model) renderScopeRows(b *strings.Builder, list []Suggestion, activeIdx, baseIdx, width int, highlightedBgStyle lipgloss.Style) {
	if width < dropdownTwoColThreshold {
		for i, item := range list {
			icon := "↪ "
			if i+baseIdx == activeIdx {
				icon = "▶ "
			}
			display := icon + item.Label
			maxContent := width - 4
			if maxContent < 6 {
				maxContent = 6
			}
			if lipgloss.Width(display) > maxContent {
				runes := []rune(display)
				if len(runes) > maxContent-1 {
					display = string(runes[:maxContent-1]) + "…"
				} else {
					display = string(runes) + "…"
				}
			}
			pad := width - lipgloss.Width(display) - 4
			if pad < 0 {
				pad = 0
			}
			rowString := display + strings.Repeat(" ", pad)
			if i+baseIdx == activeIdx {
				b.WriteString("│ " + highlightedBgStyle.Render(rowString) + " │\n")
			} else {
				b.WriteString("│ " + textStyle.Render(rowString) + " │\n")
			}
		}
		return
	}
	for i, item := range list {
		name := filepath.Base(item.Label)
		dir := filepath.Dir(item.Label)
		if dir == "." {
			dir = "./"
		}

		icon := "↪ "
		if i+baseIdx == activeIdx {
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

		if i+baseIdx == activeIdx {
			rowString := leftSide + strings.Repeat(" ", paddingCount) + rightSide
			b.WriteString("│ " + highlightedBgStyle.Render(rowString) + " │\n")
		} else {
			b.WriteString("│ " + leftSide + strings.Repeat(" ", paddingCount) + rightSide + " │\n")
		}
	}
}

// renderSuggestSections renders grouped command/directive rows with bold,
// color-distinct section headers and 2-space-indented items, highlighting the
// active row with a background fill and ▶ cursor.
func (m *model) renderSuggestSections(b *strings.Builder, list []Suggestion, activeIdx, baseIdx, width int, highlightedBgStyle lipgloss.Style) {
	activeRowStyle := highlightedBgStyle.Bold(true)
	sections := buildSuggestionSections(list)
	itemIdx := 0
	for _, sec := range sections {
		// Section header (bold, distinct color, flush under the border).
		headerStr := sectionStyleFor(sec.Title).Render(sec.Title)
		hPad := width - lipgloss.Width(headerStr) - 4
		if hPad < 0 {
			hPad = 0
		}
		b.WriteString("│ " + headerStr + strings.Repeat(" ", hPad) + " │\n")

		for _, item := range sec.Items {
			display := item.Label
			if item.Detail != "" {
				detail := "  " + dimmedStyle.Render(item.Detail)
				if lipgloss.Width(display)+lipgloss.Width(detail) <= width-10 {
					display += detail
				}
			}
			lw := lipgloss.Width(display)
			maxContent := width - 10
			if maxContent < 10 {
				maxContent = 10
			}
			if lw > maxContent {
				runes := []rune(display)
				if len(runes) > maxContent-1 {
					display = string(runes[:maxContent-1]) + "…"
				} else {
					display = string(runes) + "…"
				}
				lw = lipgloss.Width(display)
			}
			// Items sit 2 spaces under their section header.
			pad := strings.Repeat(" ", width-lw-8)

			rowString := "  " + display + pad
			if itemIdx+baseIdx == activeIdx {
				b.WriteString("│ " + activeRowStyle.Render("▶ "+rowString) + " │\n")
			} else {
				b.WriteString("│ " + dimmedStyle.Render("↳ "+rowString) + " │\n")
			}
			itemIdx++
		}
	}
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
		"  " + dimmedStyle.Render("/help  /?  /objective  /clear  /drop  /undo  /copy"),
		"  " + dimmedStyle.Render("/commit  /checkpoint  /arch  /copy-mode  /quit"),
		"  " + dimmedStyle.Render("!<cmd>          run a shell command"),
		"  " + dimmedStyle.Render("@<path>         attach a file"),
		"",
		subtleStyle.Render("  ─── Shortcuts ───"),
		"  " + dimmedStyle.Render("Esc (×3)        toggle vi-navigation mode (/copy-mode)"),
		"  " + dimmedStyle.Render("Esc (×3)        quit IZEN (normal mode)"),
		"  " + dimmedStyle.Render("↑/↓             history navigation"),
		"  " + dimmedStyle.Render("Tab/Enter       complete autocomplete"),
		"  " + dimmedStyle.Render("?               toggle this help overlay"),
		"",
		subtleStyle.Render("  ─── Vi Navigation Mode (/copy-mode) ───"),
		"  " + dimmedStyle.Render("j/k             cursor down/up (line-wise)"),
		"  " + dimmedStyle.Render("h/l             cursor left/right (character-wise)"),
		"  " + dimmedStyle.Render("0/$             jump to line start/end"),
		"  " + dimmedStyle.Render("Ctrl+D/U        page down/up (½ window)"),
		"  " + dimmedStyle.Render("gg              go to first message"),
		"  " + dimmedStyle.Render("G               go to last message"),
		"  " + dimmedStyle.Render("/<query> Enter  search forward"),
		"  " + dimmedStyle.Render("n/N             next/previous search result"),
		"  " + dimmedStyle.Render("v               toggle visual selection (char-level)"),
		"  " + dimmedStyle.Render("y               yank (copy) selected text"),
		"  " + dimmedStyle.Render("i / Esc         exit vi mode (return to input)"),
		"  " + dimmedStyle.Render(":q Enter        exit vi mode"),
		"  " + dimmedStyle.Render("mouse wheel     scroll viewport (in copy mode)"),
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
	// Active during streaming, background tasks, live shell execution, or
	// mutation processing.
	if m.streaming || m.agentRunning || m.reviewRunning || m.shellRunning || m.state == StateProcessing {
		b.WriteString(m.renderRectSpinner())
	} else {
		b.WriteString(dimmedStyle.Render(Icon.Check))
	}
	b.WriteByte(' ')

	// AI INTERRUPT ENGINE: high-visibility indicator that Ctrl+C is available
	// while ANY execution operation is in flight (streaming, provider wait,
	// agent run, patch generation, shell). Cancellation must be discoverable,
	// not implied.
	if m.streaming || m.shellRunning || m.agentRunning || m.reviewRunning ||
		m.pipelineRunning || m.planPending || m.activeOp != nil {
		b.WriteString(interruptLabelStyle.Render(Icon.Interrupt + " Ctrl+C interrupt "))
	}

	// Agent label — shown immediately after the spinner, before model name
	if m.agentRunning || m.reviewRunning {
		b.WriteString(infoStyle.Render(m.agentLabel))
		b.WriteByte(' ')
	}

	// Metadata segments: lang · model · context · tokens · cost · checkpoint
	var meta []string

	// Detected project language badge — dropped first when the pane is too narrow.
	if width >= minimalStatusThreshold && m.projectContext != nil {
		meta = append(meta, langBadgeStyle.Render(m.projectContext.Name))
	}

	// Model name — dropped after language when the pane is too narrow.
	if width >= minimalStatusThreshold {
		modelName := m.getActiveModelName()
		if m.sessionModel != "" {
			modelName = accentStyle.Render("✓") + " " + modelName
		}
		meta = append(meta, dimmedStyle.Render(modelName))
	}

	// Active context ID — conveys workspace continuity without shouting.
	if m.sess != nil && m.sess.ContextID != "" {
		ctx := m.sess.ContextID
		runes := []rune(ctx)
		if len(runes) > 9 {
			ctx = string(runes[:9])
		}
		meta = append(meta, mutedStyle.Render(Icon.Context+" "+ctx))
	}

	// Tokens — always shown; this is the minimum viable status line.
	// Cloud providers render the exact input/output split the provider
	// reported (2.3k + 1.5k tok); local models fall back to the session
	// total. The percentage reflects how much of the active model's context
	// window is consumed, so the bar stays aligned with the provider dashboard
	// instead of a static "/128000" ceiling.
	//
	// USAGE TRUTH (Phase 4): "usage unknown" is rendered until the provider
	// reports usage this session — never a fabricated "0 tok". Once usage is
	// known, "0 tok" genuinely means the provider reported zero tokens.
	tokDisplay := renderTokenUsage(m.usageKnown, m.InputTokens, m.OutputTokens, m.TotalTokens, m.activeContextLimit())
	meta = append(meta, dimmedStyle.Render(tokDisplay))

	// Accumulated cost — dropped before checkpoint as panes narrow.
	costDisplay := llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), m.AccumulatedCost)
	meta = append(meta, dimmedStyle.Render(llm.FormatCost(costDisplay)))

	// Checkpoint (truncated) — the least essential glance-able telemetry.
	if width >= compactStatusThreshold {
		if cp := m.latestCheckpointID(); cp != "" {
			cp = strings.TrimPrefix(cp, "cp-")
			runes := []rune(cp)
			if len(runes) > 7 {
				cp = string(runes[:7])
			}
			meta = append(meta, dimmedStyle.Render("cp-"+cp))
		}
	}

	sep := dimmedStyle.Render(" · ")
	b.WriteString(strings.Join(meta, sep))

	return b.String()
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
	modelName := m.getActiveModelName()
	metaParts := []string{
		mutedStyle.Render(projectPathDisplay()),
	}
	if m.projectContext != nil {
		metaParts = append(metaParts, langBadgeStyle.Render(m.projectContext.Name))
	}
	modelLabel := provider + " " + modelName
	if m.sessionModel != "" {
		modelLabel = accentStyle.Render("✓") + " " + modelLabel
	}
	metaParts = append(metaParts, mutedStyle.Render(modelLabel))
	if branch, err := m.gitEng.Branch(); err == nil && branch != "" {
		metaParts = append(metaParts, mutedStyle.Render("git ("+branch+")"))
	}
	metaSep := subtleStyle.Render(" · ")
	meta := strings.Join(metaParts, metaSep)

	tip := mutedStyle.Render(m.currentTip)
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
	if m.projectContext != nil {
		metaParts = append(metaParts, langBadgeStyle.Render(m.projectContext.Name))
	}
	if termWidth >= compactStatusThreshold {
		provider := m.cfg.ActiveProviderName()
		modelName := m.getActiveModelName()
		modelLabel := provider + " " + modelName
		if m.sessionModel != "" {
			modelLabel = accentStyle.Render("✓") + " " + modelLabel
		}
		metaParts = append(metaParts, mutedStyle.Render(modelLabel))
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

// selfHealRetryRe matches a self-healing retry activity line emitted by the
// event projection: "[RETRY 2] [TYPE_MISMATCH] worker.go". The optional
// trailing text is the mutated file path.
var selfHealRetryRe = regexp.MustCompile(`^\[RETRY (\d+)\] \[([A-Z_]+)\](?: (.*))?$`)

// selfHealExhaustedRe matches a self-healing exhaustion activity line emitted
// by the event projection: "[EXHAUSTED] self-healing stopped after N attempt(s)..."
var selfHealExhaustedRe = regexp.MustCompile(`^\[EXHAUSTED\](.*)$`)

// failureCategoryStyle maps a failure category label (e.g. "SYNTAX_ERROR") to
// a deterministic color so each self-healing retry reads distinctly.
func failureCategoryStyle(category string) lipgloss.Style {
	switch strings.ToUpper(strings.TrimSpace(category)) {
	case "SYNTAX_ERROR":
		return redStyle
	case "TYPE_MISMATCH":
		return yellowStyle
	case "MISSING_IMPORT":
		return blueStyle
	case "TEST_FAILURE":
		return orangeStyle
	case "SYSTEM_PERMISSION":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colorMaroon))
	default:
		return mutedStyle
	}
}

// styleActivityLine renders a system activity log line with mixed
// styling. Status badges ([ OK ], [FAIL]) are colorized, and any
// remaining Markdown syntax (bold **...**, bullets - ...) in the
// text body is stripped and re-rendered through the deterministic
// TUI markdown pipeline so the viewport never shows raw asterisks
// or dashes from the LLM's system summary.
func (m *model) styleActivityLine(line string) string {
	// ── QUIET TRACE SUPPRESSION (per-line activity path) ──────────
	// In quiet mode a raw engine line collapses to the single per-turn
	// "▸ Trace:" summary; any subsequent engine line for the same turn is
	// dropped entirely so the summary is never repeated sequentially.
	if !TraceVerbose && isQuietTraceText(line) {
		if m.traceSummaryShown {
			return ""
		}
		m.traceSummaryShown = true
		return dimmedStyle.Render(buildQuietTraceLine(line))
	}

	// ── SELF-HEALING BADGES ────────────────────────────────────────
	// Distinct retry / exhausted indicators with the attempt count and
	// failure category colorized deterministically.
	if mh := selfHealRetryRe.FindStringSubmatch(line); mh != nil {
		retry := badgeRetryStyle.Render("[RETRY " + mh[1] + "]")
		cat := failureCategoryStyle(mh[2]).Render("[" + mh[2] + "]")
		out := retry + " " + cat
		if mh[3] != "" {
			out += " " + systemActivityStyle.Render(mh[3])
		}
		return out
	}
	if mh := selfHealExhaustedRe.FindStringSubmatch(line); mh != nil {
		return badgeExhaustedStyle.Render("[EXHAUSTED]") + systemActivityStyle.Render(mh[1])
	}

	okTag := "[ OK ]"
	failTag := "[FAIL]"
	if idx := strings.Index(line, okTag); idx >= 0 {
		pre := systemActivityStyle.Render(line[:idx])
		tag := badgeOKStyle.Render(okTag)
		suf := systemActivityStyle.Render(line[idx+len(okTag):])
		return pre + tag + suf
	}
	if idx := strings.Index(line, failTag); idx >= 0 {
		pre := systemActivityStyle.Render(line[:idx])
		tag := badgeFailStyle.Render(failTag)
		suf := systemActivityStyle.Render(line[idx+len(failTag):])
		return pre + tag + suf
	}
	// Pass the line through the deterministic TUI markdown renderer
	// so that raw asterisks, bullet dashes, and other Markdown
	// syntax are converted to styled TUI output instead of leaking
	// as plain text.
	rendered := RenderDeterministicPipeline(line, m.width, false)
	if rendered != "" && rendered != line {
		return rendered
	}
	return systemActivityStyle.Render(line)
}

// ── Record renderer (for viewport content) ────────────────────

func (m *model) printRecord(rec record) string {
	gutter := gutterFor(rec.role)
	content := sanitizeText(rec.text)

	if rec.role == roleAI {
		return m.renderAIResponseBlocks(content, m.width)
	}

	wrapWidth := m.width - 4
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	// Per-line wrapping preserves explicit \n line breaks (TODO checklist
	// items, error log lines) and keeps leading indentation on continuation
	// lines so nested structures never collapse or overlap.
	var wrappedLines []string
	for _, srcLine := range strings.Split(content, "\n") {
		wrappedLines = append(wrappedLines, wrapIndentedLine(srcLine, wrapWidth)...)
	}

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
	case roleActivity:
		styledLines := make([]string, len(wrappedLines))
		for i, line := range wrappedLines {
			styledLines[i] = m.styleActivityLine(line)
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

		if currentKind == blockRisk || currentKind == blockEvidence {
			if trimmed == "" || strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				currentBlock = append(currentBlock, line)
				continue
			}
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

// renderTokenUsage renders the footer token segment with USAGE TRUTH
// semantics (Phase 4): "usage unknown" until the provider reports usage —
// never a fabricated "0 tok". Once known, "0 tok" genuinely means the provider
// reported zero tokens.
func renderTokenUsage(known bool, input, output, total, contextLimit int) string {
	if !known && input == 0 && output == 0 && total == 0 {
		return "usage unknown"
	}
	return status.FormatUsageContext(input, output, total, contextLimit)
}
