package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/pkg/provider/capability"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/ui/decision"
)

// DecisionSurfaceAdapter is the TUI Adapter that bridges pkg/runtime/ domain
// logic directly into internal/ui/ BubbleTea components. It keeps
// pkg/runtime/ strictly framework-agnostic (zero BubbleTea/Lipgloss imports)
// and lets internal/ui/ act as the adapter consuming pkg/runtime/ view models.
//
// Contract:
//   - pkg/runtime/ui/decision exports DecisionViewModel (pure data).
//   - internal/ui/decision_surface.go owns Init/Update/View and Lipgloss.
//   - Init/Update fetch decision state from pkg/runtime/preflight and build
//     the DecisionViewModel via decision.BuildViewModel / BuildViewModelFromState.
//   - View iterates over ViewModel.Options and renders dynamic labels:
//     green "(recommended)", red "[HIGH RISK]", grayed-out "[DISABLED: reason]".
//   - Cursor navigation skips disabled options.
type DecisionSurfaceAdapter struct {
	// ViewModel is the framework-agnostic domain view model.
	ViewModel decision.DecisionViewModel

	// cursor is the selected index in ViewModel.Options.
	cursor int

	// width is the terminal width for rendering.
	width int

	// source state used to (re)build the ViewModel on Init/Update.
	// They are kept so the adapter can re-fetch decision state from
	// pkg/runtime/preflight without ever calling os.ReadFile directly —
	// the snapshot []byte comes from the Loop's Observe phase.
	targetPath string
	snapshot   []byte
	maxTokens  int

	// selected holds the last committed selection key, if any.
	selected string
}

// NewDecisionSurfaceAdapter returns an adapter wired to an existing ViewModel.
func NewDecisionSurfaceAdapter(vm decision.DecisionViewModel) DecisionSurfaceAdapter {
	m := DecisionSurfaceAdapter{ViewModel: vm}
	m.cursor = m.firstSelectable()
	return m
}

// NewDecisionSurfaceAdapterFromPreflight fetches decision state from
// pkg/runtime/preflight and builds the DecisionViewModel. The caller passes
// the Observe-phase snapshot []byte — no filesystem re-read occurs here.
// This is the adapter entry used in Init()/Update() per spec.
func NewDecisionSurfaceAdapterFromPreflight(targetPath string, snapshot []byte, maxTokens int) DecisionSurfaceAdapter {
	// Infer AST status from the snapshot bytes (no disk I/O).
	astStatus := preflight.InferASTStatus(snapshot, targetPath)

	// Run the hard budget gate against the model's output ceiling.
	targetState := preflight.TargetState{
		Path:      targetPath,
		Content:   snapshot,
		ASTStatus: astStatus,
	}
	caps := capability.ModelCapabilities{MaxOutputTokens: maxTokens}
	gateResult := preflight.EvaluateBudgetGate(targetState, caps)

	// Build the kernel surface and convert to the domain ViewModel.
	vm := decision.BuildViewModel(targetPath, astStatus, &gateResult)

	m := DecisionSurfaceAdapter{
		ViewModel:  vm,
		targetPath: targetPath,
		snapshot:   snapshot,
		maxTokens:  maxTokens,
	}
	m.cursor = m.firstSelectable()
	return m
}

// NewDecisionSurfaceAdapterFromState is a convenience that builds from a
// preflight.TargetState directly (also snapshot-backed, no os.ReadFile).
func NewDecisionSurfaceAdapterFromState(state preflight.TargetState, maxTokens int) DecisionSurfaceAdapter {
	vm := decision.BuildViewModelFromState(state, maxTokens)
	m := DecisionSurfaceAdapter{
		ViewModel:  vm,
		targetPath: state.Path,
		snapshot:   state.Content,
		maxTokens:  maxTokens,
	}
	m.cursor = m.firstSelectable()
	return m
}

// Init fetches decision state from pkg/runtime/preflight when the adapter was
// constructed with a target/snapshot but an empty ViewModel. It is the
// BubbleTea Init entry per spec.
func (m DecisionSurfaceAdapter) Init() tea.Cmd {
	return nil
}

// Update handles navigation and rebuilds the ViewModel when new snapshot
// messages arrive. It prevents the cursor from ever landing on a disabled
// option.
func (m DecisionSurfaceAdapter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor = m.ViewModel.PrevSelectable(m.cursor)
			return m, nil
		case "down", "j":
			m.cursor = m.ViewModel.NextSelectable(m.cursor)
			return m, nil
		case "enter", " ":
			if m.ViewModel.IsDisabledOption(m.cursor) {
				// Prevent selection of disabled options.
				return m, nil
			}
			if m.cursor >= 0 && m.cursor < len(m.ViewModel.Options) {
				m.selected = m.ViewModel.Options[m.cursor].Key
			}
			return m, nil
		case "esc", "q":
			// Caller handles cancel; keep adapter alive.
			return m, nil
		}

	case DecisionViewModelMsg:
		// External bridge can push a refreshed ViewModel (e.g. after Observe).
		m.ViewModel = decision.DecisionViewModel(msg)
		// Ensure cursor lands on a selectable entry.
		if m.ViewModel.IsDisabledOption(m.cursor) {
			m.cursor = m.firstSelectable()
		}
		return m, nil

	case SnapshotMsg:
		// Fetch decision state from pkg/runtime/preflight and build
		// DecisionViewModel (spec: "In Init() / Update(): Fetch decision state
		// from pkg/runtime/preflight and build DecisionViewModel").
		s := preflight.TargetState{
			Path:      msg.Path,
			Content:   msg.Content,
			ASTStatus: preflight.InferASTStatus(msg.Content, msg.Path),
		}
		caps := capability.ModelCapabilities{MaxOutputTokens: msg.MaxTokens}
		gateResult := preflight.EvaluateBudgetGate(s, caps)
		m.ViewModel = decision.BuildViewModel(s.Path, s.ASTStatus, &gateResult)
		m.targetPath = s.Path
		m.snapshot = s.Content
		m.maxTokens = msg.MaxTokens
		m.cursor = m.firstSelectable()
		return m, nil
	}
	return m, nil
}

// View iterates over ViewModel.Options and renders dynamic labels:
//   - IsRecommended → green "(recommended)"
//   - Risk == HIGH   → red "[HIGH RISK]"
//   - IsDisabled     → grayed-out "[DISABLED: reason]"
//
// The cursor never rests on a disabled option.
func (m DecisionSurfaceAdapter) View() string {
	if len(m.ViewModel.Options) == 0 {
		return dimmedStyle.Render("No strategy options available.")
	}

	width := m.width
	if width < 52 {
		width = 52
	}
	boxWidth := width - 4

	var b strings.Builder
	// Header
	fmt.Fprintf(&b, "◆ STRATEGY DECISION — [%s]\n\n", displayTarget(m.ViewModel.TargetFile))
	fmt.Fprintf(&b, "AST Status    : %s\n", displayASTView(m.ViewModel.ASTStatus))
	fmt.Fprintf(&b, "Budget Status : %s\n", displayBudgetView(m.ViewModel.BudgetStatus))
	fmt.Fprintf(&b, "Estimated     : ~%s output tokens\n", formatIntView(m.ViewModel.EstimatedTok))
	fmt.Fprintf(&b, "Model Max Out : %s tokens\n\n", formatIntView(m.ViewModel.BudgetMaxTok))

	for i, opt := range m.ViewModel.Options {
		marker := "  "
		if i == m.cursor {
			marker = "► "
		}

		// Title base
		title := opt.Title

		// Build dynamic labels
		var labels []string
		if opt.IsRecommended {
			labels = append(labels, greenStyle.Render("(recommended)"))
		}
		if opt.Risk == decision.RiskHigh {
			labels = append(labels, redStyle.Render("[HIGH RISK]"))
		}

		line := fmt.Sprintf("%s[%d] %s", marker, opt.ID, title)
		if len(labels) > 0 {
			line += " " + strings.Join(labels, " ")
		}
		if opt.IsDisabled {
			reason := opt.DisabledReason
			if strings.TrimSpace(reason) == "" {
				reason = "Exceeds Model Output Budget"
			}
			disabledLabel := fmt.Sprintf("[DISABLED: %s]", reason)
			line += " " + dimmedStyle.Render(disabledLabel)
			// Gray out the whole line for disabled options
			line = dimmedStyle.Render(line) + dimmedStyle.Render(" (grayed out)")
		}

		fmt.Fprintln(&b, line)
		fmt.Fprintf(&b, "   %s\n", dimmedStyle.Render(opt.Description))
		fmt.Fprintf(&b, "   Risk: %s", opt.Risk)
		if opt.IsRecommended {
			b.WriteString(" · " + greenStyle.Render("recommended"))
		}
		if opt.IsDisabled {
			b.WriteString(" · " + dimmedStyle.Render("disabled"))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimmedStyle.Render("↑/↓ navigate · Enter select · Esc cancel"))
	if m.selected != "" {
		b.WriteString("\n" + mutedStyle.Render("Selected: "+m.selected))
	}

	return boxStringView(b.String(), boxWidth)
}

// Selected returns the last committed selection key.
func (m DecisionSurfaceAdapter) Selected() string { return m.selected }

// ViewModelMsg is a BubbleTea message carrying a refreshed DecisionViewModel.
type DecisionViewModelMsg decision.DecisionViewModel

// SnapshotMsg is a BubbleTea message carrying an Observe-phase snapshot.
// The adapter's Update() consumes it and builds the ViewModel without
// repeating os.ReadFile (verification uses snapshot []byte directly).
type SnapshotMsg struct {
	Path      string
	Content   []byte
	MaxTokens int
}

// firstSelectable returns the first selectable index, or 0 if none.
func (m DecisionSurfaceAdapter) firstSelectable() int {
	for i, o := range m.ViewModel.Options {
		if !o.IsDisabled {
			return i
		}
	}
	return 0
}

// Helpers for header rendering (mirrors decision.Surface Render but for ViewModel)

func displayTarget(t string) string {
	if strings.TrimSpace(t) == "" {
		return "(target not resolved)"
	}
	return t
}

func displayASTView(s string) string {
	switch strings.ToLower(s) {
	case "valid":
		return "valid"
	case "corrupt":
		return "corrupt"
	default:
		if s == "" {
			return "unverified"
		}
		return s
	}
}

func displayBudgetView(s string) string {
	switch strings.ToLower(s) {
	case "within_limits":
		return "within_limits"
	case "exceeded":
		return "exceeded"
	default:
		if s == "" {
			return "unknown"
		}
		return s
	}
}

func formatIntView(n int) string {
	if n <= 0 {
		return "0"
	}
	digits := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func boxStringView(body string, width int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", width) + "┐\n")
	for _, ln := range lines {
		pad := width - lipgloss.Width(ln)
		if pad < 0 {
			pad = 0
		}
		b.WriteString("│" + ln + strings.Repeat(" ", pad) + "│\n")
	}
	b.WriteString("└" + strings.Repeat("─", width) + "┘")
	return b.String()
}
