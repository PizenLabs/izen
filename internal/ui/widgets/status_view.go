package widgets

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/workspace"
)

const (
	statusModalMaxWidth = 76
	statusWindowMargin  = 4
	statusBoxChromeX    = 6 // border (2) + horizontal padding (2+2)
	statusBoxChromeY    = 4 // border (2) + vertical padding (1+1)
	statusBorderAccent  = "#89b4fa"
)

var (
	statusTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#cdd6f4"))
	statusSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#89dceb"))
	statusLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#565f89"))
	statusBranchStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#89dceb"))
	statusModelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#cdd6f4"))
	statusValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#cdd6f4"))
	statusMutedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6c7086"))
	statusGoodStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#a6e3a1"))
	statusWarnStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#f9e2af"))
	statusErrorStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#f38ba8"))
)

// StatusModalSize returns the outer status modal bounds for a terminal window.
// Width is capped at 76 with a four-cell margin. Height is NOT capped at a
// fixed value: the second return is the maximum outer height available
// (window height minus margin). View shrink-wraps to the content height up to
// that maximum, so there is no dead vertical space.
func StatusModalSize(msg tea.WindowSizeMsg) (int, int) {
	return StatusModalSizeFor(msg.Width, msg.Height)
}

// StatusModalSizeFor is the integer form used by hosts that already store the
// terminal dimensions separately.
func StatusModalSizeFor(width, height int) (int, int) {
	return max(1, min(statusModalMaxWidth, width-statusWindowMargin)),
		max(1, height-statusWindowMargin)
}

// StatusView is the pure, value-type status modal. It owns presentation state
// only: a snapshot, loading/error state, and the responsive outer bounds. The
// host owns collection, focus, and modal visibility.
type StatusView struct {
	Snapshot workspace.Status
	Width    int
	Height   int
	Loading  bool
	Error    string
}

// NewStatusView creates a status view with a safe default width and automatic
// (shrink-wrapped) height. An optional snapshot is accepted for small
// headless/test compositions.
func NewStatusView(snapshot ...workspace.Status) StatusView {
	view := StatusView{Width: statusModalMaxWidth, Height: 0}
	if len(snapshot) > 0 {
		view.Snapshot = snapshot[0]
	}
	return view
}

// SetSize updates the outer modal bounds.
func (v StatusView) SetSize(width, height int) StatusView {
	v.Width = max(1, width)
	v.Height = max(1, height)
	return v
}

// SetSnapshot replaces the displayed runtime snapshot and clears loading state.
func (v StatusView) SetSnapshot(snapshot workspace.Status) StatusView {
	v.Snapshot = snapshot
	v.Loading = false
	v.Error = ""
	return v
}

// SetLoading controls the compact collection indicator.
func (v StatusView) SetLoading(loading bool) StatusView {
	v.Loading = loading
	return v
}

// SetError displays a bounded collection error in the modal title area.
func (v StatusView) SetError(err error) StatusView {
	v.Loading = false
	if err == nil {
		v.Error = ""
	} else {
		v.Error = err.Error()
	}
	return v
}

// Size returns the current outer modal bounds. A non-positive width falls
// back to the default; a non-positive height means automatic (shrink-wrapped)
// height and is returned as 0 so callers can distinguish it from a fixed
// height.
func (v StatusView) Size() (int, int) {
	width, height := v.Width, v.Height
	if width <= 0 {
		width = statusModalMaxWidth
	}
	if height < 0 {
		height = 0
	}
	return width, height
}

// InnerSize returns the strict content canvas after border and padding removal.
// A non-positive height means unconstrained (shrink-wrap) and yields 0.
func (v StatusView) InnerSize() (int, int) {
	width, height := v.Width, v.Height
	if width <= 0 {
		width = statusModalMaxWidth
	}
	if height <= 0 {
		return max(1, width-statusBoxChromeX), 0
	}
	return max(1, width-statusBoxChromeX), max(1, height-statusBoxChromeY)
}

// Init implements tea.Model for callers that want to treat the status view as
// a conventional Bubble Tea widget. It performs no work.
func (v StatusView) Init() tea.Cmd { return nil }

// Update consumes responsive resize messages. Key dismissal is deliberately
// host-owned because the status view does not own the workspace input focus.
func (v StatusView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if window, ok := msg.(tea.WindowSizeMsg); ok {
		width, height := StatusModalSize(window)
		return v.SetSize(width, height), nil
	}
	return v, nil
}

// View renders a shrink-wrapped bordered modal. Content rows are fitted to
// the inner cell budget, then LipGloss composes the box with 1-line
// top/bottom and 2-cell left/right padding inside a rounded accent border.
// No explicit Height is set, so the modal hugs the content: the divider and
// footer sit directly beneath the last section with zero empty gap. When the
// host supplies a maximum outer height (small terminals), overlong content is
// truncated to fit instead of breaching the viewport.
func (v StatusView) View() string {
	outerWidth := v.Width
	if outerWidth <= 0 {
		outerWidth = statusModalMaxWidth
	}
	contentWidth := max(1, outerWidth-statusBoxChromeX)
	contentMax := 0
	if v.Height > 0 {
		contentMax = max(1, v.Height-statusBoxChromeY)
	}

	lines := v.contentLines(contentWidth, contentMax)
	if contentMax > 0 && len(lines) > contentMax {
		lines = lines[:contentMax]
	}
	content := strings.Join(lines, "\n")

	// Width includes horizontal padding but not the border, so the rendered
	// outer box is exactly outerWidth wide. Height is intentionally unset
	// for shrink-wrap: Height = lipgloss.Height(renderedContent) + 2 borders.
	box := lipgloss.NewStyle().
		Width(contentWidth+4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(statusBorderAccent)).
		Padding(1, 2).
		Render(content)
	return box
}

const (
	statusLabelWidth = 8
	statusRowIndent  = "  "
)

func (v StatusView) contentLines(width, height int) []string {
	vcs := v.Snapshot.VCS
	modified := maxInt(vcs.ModifiedFiles, vcs.ModifiedCount)
	untracked := maxInt(vcs.UntrackedFiles, vcs.UntrackedCount)
	branch := compactValue(firstNonEmpty(vcs.Branch, "—"))
	commit := compactValue(firstNonEmpty(vcs.ShortSHA, vcs.Commit, vcs.CommitSHA, "—"))
	indexer := v.Snapshot.Indexer
	session := v.Snapshot.Session
	tokens := session.Tokens
	if tokens == (workspace.TokenUsage{}) {
		tokens = session.TokenUsage
	}
	if tokens.TotalTokens == 0 && (tokens.InputTokens > 0 || tokens.OutputTokens > 0) {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens
	}
	engine := v.Snapshot.Engine
	gateFull := fullStatusValue(firstNonEmpty(engine.PolicyGate, engine.ExecutionPolicy, engine.ExecutionPolicyGate, "Unknown"))
	rootFull := fullStatusValue(firstNonEmpty(v.Snapshot.WorkspaceRoot, v.Snapshot.Root, "—"))

	title := "WORKSPACE STATUS"
	if v.Loading {
		title += " · collecting"
	}
	if v.Error != "" {
		title += " · " + compactValue(v.Error)
	}

	titleLine := fitStatusLine(statusTitleStyle.Render(title), width)
	workspaceSection := fitStatusLine(statusSectionStyle.Render("WORKSPACE & VCS"), width)
	symbolSection := fitStatusLine(statusSectionStyle.Render("SYMBOL ENGINE"), width)
	sessionSection := fitStatusLine(statusSectionStyle.Render("SESSION CONTEXT"), width)
	engineSection := fitStatusLine(statusSectionStyle.Render("ENGINE & AUTHORITY"), width)
	blankLine := fitStatusLine("", width)

	pathRows := wrapStatusField("Path", rootFull, width, statusValueStyle)

	gitBranchPlain := fmt.Sprintf("%s (%s)", branch, commit)
	gitStatePlain := gitStateText(vcs, modified, untracked)
	gitSinglePlain := gitBranchPlain + " · " + gitStatePlain
	var gitRows []string
	if len([]rune(statusRowIndent))+statusLabelWidth+1+len([]rune(gitSinglePlain)) <= width {
		gitValue := statusBranchStyle.Render(gitBranchPlain) + " · " + renderGitState(vcs, modified, untracked)
		gitRows = []string{fitStatusLine(statusRow("Git", gitValue), width)}
	} else {
		// Wrap the single logical Git status string across two visual
		// lines so branch/SHA and dirty counts are never truncated away.
		first := fitStatusLine(statusRow("Git", statusBranchStyle.Render(gitBranchPlain)), width)
		contPad := strings.Repeat(" ", len(statusRowIndent)+statusLabelWidth+1)
		second := fitStatusLine(contPad+renderGitState(vcs, modified, untracked), width)
		gitRows = []string{first, second}
	}

	symbolCount := maxInt(indexer.SymbolCount, indexer.Symbols)
	lynxValue := renderIndexerState(indexer) +
		statusValueStyle.Render(fmt.Sprintf(" · %d symbols", symbolCount)) +
		" · " + statusValueStyle.Render("AST ") + renderASTState(indexer)
	lynxRow := fitStatusLine(statusRow("Lynx", lynxValue), width)

	slotID := fullStatusValue(firstNonEmpty(session.SlotID, session.ActiveSlot, "—"))
	titleGoal := fullStatusValue(firstNonEmpty(session.Title, session.Goal, "—"))
	slotDetail := titleGoal
	if goalFull := fullStatusValue(session.Goal); session.Title != "" && goalFull != "" && goalFull != "—" && goalFull != titleGoal {
		slotDetail = titleGoal + " · " + goalFull
	}
	slotRow := fitStatusLine(statusRow("Slot", statusValueStyle.Render(slotID+" · "+slotDetail)), width)

	tokensRow := fitStatusLine(statusRow("Tokens",
		statusValueStyle.Render(fmt.Sprintf("↑%d · ↓%d (%d total) · %d turns",
			tokens.InputTokens, tokens.OutputTokens, tokens.TotalTokens, maxInt(session.TurnCount, 0)))), width)

	provider := fullStatusValue(firstNonEmpty(engine.Provider, engine.ActiveProvider, "—"))
	modelName := fullStatusValue(firstNonEmpty(engine.Model, engine.ActiveModel, "—"))
	modelRow := fitStatusLine(statusRow("Model",
		statusModelStyle.Render(provider+" / "+modelName)), width)

	policyRows := wrapStatusFieldStyled("Policy", gateFull, width, policyStyleFor(gateFull))

	divider := fitStatusLine(statusMutedStyle.Render(strings.Repeat("─", max(1, width))), width)
	footer := fitStatusLine(statusMutedStyle.Render("Esc / q: Close status modal"), width)

	// Shrink-wrapped layout with one blank line before each section header
	// except the first. The divider and footer are pinned directly beneath
	// the last section with no intervening empty lines.
	lines := []string{titleLine, workspaceSection}
	lines = append(lines, pathRows...)
	lines = append(lines, gitRows...)
	lines = append(lines, blankLine, symbolSection, lynxRow, blankLine, sessionSection, slotRow, tokensRow, blankLine, engineSection, modelRow)
	lines = append(lines, policyRows...)
	lines = append(lines, divider, footer)

	if height > 0 && len(lines) > height {
		if height >= 10 {
			// Keep every section heading visible before optional facts.
			priority := []string{titleLine, workspaceSection}
			priority = append(priority, pathRows...)
			priority = append(priority, gitRows...)
			priority = append(priority, symbolSection, lynxRow, sessionSection, slotRow, engineSection, modelRow)
			if len(policyRows) > 0 {
				priority = append(priority, policyRows[0])
			}
			priority = append(priority, tokensRow)
			if len(policyRows) > 1 {
				priority = append(priority, policyRows[1:]...)
			}
			priority = append(priority, divider, footer)
			if len(priority) > height {
				// Always keep the footer visible; drop middle rows first.
				lines = priority[:max(0, height-1)]
				if len(lines) < height {
					lines = append(lines, footer)
				}
				if len(lines) > height {
					lines = lines[:height]
				}
			} else {
				lines = priority
			}
		} else {
			// Very short pane: one merged row per section plus footer.
			gitCompact := statusBranchStyle.Render(gitBranchPlain) + " · " + renderGitState(vcs, modified, untracked)
			compact := []string{
				titleLine,
				fitStatusLine(statusSectionStyle.Render("WORKSPACE & VCS")+" "+statusValueStyle.Render(rootFull), width),
				fitStatusLine(statusRow("Git", gitCompact), width),
				fitStatusLine(statusSectionStyle.Render("SYMBOL ENGINE")+" "+lynxValue, width),
				fitStatusLine(statusSectionStyle.Render("SESSION CONTEXT")+" "+statusValueStyle.Render(slotID+" · "+slotDetail), width),
				fitStatusLine(statusSectionStyle.Render("ENGINE & AUTHORITY")+" "+statusModelStyle.Render(provider+" / "+modelName), width),
			}
			optional := []string{tokensRow, divider, footer}
			for _, line := range optional {
				if len(compact) >= height {
					break
				}
				compact = append(compact, line)
			}
			if len(compact) > height {
				compact = compact[:height]
			}
			// Ensure the footer survives even the smallest heights.
			if height > 0 && len(compact) == height && height >= 2 {
				compact[height-1] = footer
			}
			lines = compact
		}
	}
	return lines
}

// RenderStatus renders a shrink-wrapped status modal. The variadic width keeps
// the old widget call shape source-compatible; an optional second value acts
// as a maximum outer-height constraint (0 = unconstrained).
func RenderStatus(snapshot workspace.Status, widths ...int) string {
	view := NewStatusView(snapshot)
	if len(widths) > 0 && widths[0] > 0 {
		height := 0
		if len(widths) > 1 && widths[1] > 0 {
			height = widths[1]
		}
		view = view.SetSize(widths[0], height)
	}
	return view.View()
}

// Render is the short widget API alias.
func Render(snapshot workspace.Status, widths ...int) string {
	return RenderStatus(snapshot, widths...)
}

// View is a Bubble Tea-style alias for callers that use widget naming
// conventions.
func View(snapshot workspace.Status, widths ...int) string {
	return RenderStatus(snapshot, widths...)
}

// RenderStatusView is an explicit view-oriented alias for integrations that
// keep widget constructors and renderers separate.
func RenderStatusView(snapshot workspace.Status, widths ...int) string {
	return RenderStatus(snapshot, widths...)
}

func statusRow(label, value string) string {
	padded := fmt.Sprintf("%-8s", label+":")
	return statusRowIndent + statusLabelStyle.Render(padded) + " " + value
}

func fullStatusValue(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "—"
	}
	return value
}

func wrapStatusField(label, value string, width int, style lipgloss.Style) []string {
	return wrapStatusFieldStyled(label, value, width, style)
}

func wrapStatusFieldStyled(label, value string, width int, style lipgloss.Style) []string {
	avail := width - len(statusRowIndent) - statusLabelWidth - 1
	if avail < 10 {
		avail = 10
	}
	chunks := wrapStatusText(value, avail)
	if len(chunks) == 0 {
		chunks = []string{"—"}
	}
	labelCell := statusLabelStyle.Render(fmt.Sprintf("%-8s", label+":"))
	contPad := strings.Repeat(" ", len(statusRowIndent)+statusLabelWidth+1)
	rows := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		var line string
		if i == 0 {
			line = statusRowIndent + labelCell + " " + style.Render(chunk)
		} else {
			// Continuation lines keep the 2-space left indent and align
			// under the value column for a clean wrapped block.
			line = contPad + style.Render(chunk)
		}
		rows = append(rows, fitStatusLine(line, width))
	}
	return rows
}

func wrapStatusText(text string, avail int) []string {
	if avail < 1 {
		avail = 1
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	words := strings.Fields(text)
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, word := range words {
		// Hard-break words longer than the available budget (e.g. a very
		// long path segment) so the right border is never breached.
		for len([]rune(word)) > avail {
			runes := []rune(word)
			chunk := string(runes[:avail])
			word = string(runes[avail:])
			if cur == "" {
				lines = append(lines, chunk)
			} else {
				flush()
				lines = append(lines, chunk)
			}
		}
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= avail:
			cur += " " + word
		default:
			flush()
			cur = word
		}
	}
	flush()
	if len(lines) == 0 {
		return []string{text}
	}
	return lines
}

func policyStyleFor(gate string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(gate)) {
	case "read-only", "readonly", "read only":
		return statusGoodStyle
	case "interactive", "controlled", "write", "execute":
		return statusWarnStyle
	case "awaiting approval", "approval", "blocked":
		return statusWarnStyle
	case "denied", "closed", "error":
		return statusErrorStyle
	default:
		return statusMutedStyle
	}
}

func fitStatusLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "…")
	if visible := lipgloss.Width(line); visible < width {
		line += strings.Repeat(" ", width-visible)
	}
	return line
}

func gitStateText(vcs workspace.VCSStatus, modified, untracked int) string {
	if !vcs.HasGit {
		return "not a git repository"
	}
	if !vcs.Available && vcs.Error != "" {
		return "unknown · status probe timed out"
	}
	if vcs.IsDirty || modified > 0 || untracked > 0 {
		return fmt.Sprintf("dirty · %d modified · %d untracked", modified, untracked)
	}
	return fmt.Sprintf("clean · %d modified · %d untracked", modified, untracked)
}

func renderGitState(vcs workspace.VCSStatus, modified, untracked int) string {
	if !vcs.HasGit {
		return statusMutedStyle.Render("not a git repository")
	}
	if !vcs.Available && vcs.Error != "" {
		return statusWarnStyle.Render("unknown · status probe timed out")
	}
	if !vcs.Available {
		// A manually assembled snapshot may carry counts/branch data without
		// the probe's transport flag; treat that explicit data as available.
		vcs.Available = true
	}
	if vcs.IsDirty || modified > 0 || untracked > 0 {
		return statusWarnStyle.Render(fmt.Sprintf("dirty · %d modified · %d untracked", modified, untracked))
	}
	return statusGoodStyle.Render(fmt.Sprintf("clean · %d modified · %d untracked", modified, untracked))
}

func renderIndexerState(indexer workspace.IndexerStatus) string {
	if strings.TrimSpace(indexer.Error) != "" {
		return statusErrorStyle.Render("error")
	}
	state := strings.ToLower(strings.TrimSpace(firstNonEmpty(indexer.Status, indexer.IndexingStatus)))
	if indexer.Indexed || state == "indexed" || state == "ready" {
		return statusGoodStyle.Render("✓ indexed")
	}
	switch state {
	case "indexing", "pending", "starting":
		return statusWarnStyle.Render("indexing")
	case "error", "failed":
		return statusErrorStyle.Render("error")
	default:
		return statusMutedStyle.Render("unavailable")
	}
}

func renderASTState(indexer workspace.IndexerStatus) string {
	state := strings.ToLower(strings.TrimSpace(indexer.ASTStatus))
	switch state {
	case "ready", "valid", "indexed", "ok":
		return statusGoodStyle.Render("✓ ready")
	case "pending", "indexing", "starting":
		return statusWarnStyle.Render("pending")
	case "error", "failed", "corrupt":
		return statusErrorStyle.Render("error")
	default:
		return statusMutedStyle.Render("unavailable")
	}
}

func compactValue(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "—"
	}
	const maxRunes = 72
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-1]) + "…"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
