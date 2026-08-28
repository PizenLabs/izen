// Package tui is the interactive proposal gateway renderer. It renders a
// DecisionSurface as a terminal selection menu and collapses a keypress into a
// single ProposalIntent.
//
// INVARIANT: the modal is PURE PRESENTATION. It never reads the workspace,
// never writes a file, and never invokes a provider. Selecting an option only
// returns the ProposalIntent value; the engine routes it across the
// RuntimeExecutor boundary via ResumeWithProposal.
//
// ARCHITECTURE: this package defines its OWN pure-data mirror of the autonomy
// proposal vocabulary (ProposalIntent / ProposalOption / DecisionSurface). It
// deliberately does NOT import internal/runtime/autonomy — the UI layer is a
// projection and must stay decoupled from the driver. The composition root
// (which may import both) or a test adapter converts between the two shapes.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ProposalIntent is the typed, closed vocabulary of a human-selected proposal
// strategy. It mirrors the autonomy package's vocabulary value-for-value; it is
// pure data — never a callback — and is the ONLY value a TUI option selection
// returns to the engine.
type ProposalIntent string

const (
	// ProposalInlineDeps converts an unresolved external reference into an
	// inline/local asset (e.g. bundle a referenced asset into the target).
	ProposalInlineDeps ProposalIntent = "inline_deps"
	// ProposalExpandScope widens the target boundary to cover additional files
	// (SYSTEMIC: $prompt ONLY. Never offered under $hot).
	ProposalExpandScope ProposalIntent = "expand_scope"
	// ProposalRepairFirst repairs a corrupt AST before any mutation.
	ProposalRepairFirst ProposalIntent = "repair_first"
	// ProposalReduceScope narrows the target boundary to stay within budget.
	ProposalReduceScope ProposalIntent = "reduce_scope"
	// ProposalCancel abandons the objective with zero mutation and zero spend.
	ProposalCancel ProposalIntent = "cancel"
)

// Valid reports whether the intent is a member of the closed vocabulary.
func (i ProposalIntent) Valid() bool {
	switch i {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalCancel:
		return true
	}
	return false
}

// IsCancel reports whether the intent abandons the objective.
func (i ProposalIntent) IsCancel() bool { return i == ProposalCancel }

// ProposalOption is one selectable entry on the decision surface. It carries
// only presentation + intent data — NO functional callback.
type ProposalOption struct {
	ID          string
	Label       string
	Description string
	Intent      ProposalIntent
}

// DecisionSurface is the pure data surface the modal renders. It never holds a
// callback and never mutates state.
type DecisionSurface struct {
	Target            string
	ASTStatus         string
	ExternalRefsCount int
	EstimatedTokens   int
	CurrentBudget     int
	Options           []ProposalOption
}

// Option returns the first option with the given intent, or nil.
func (s DecisionSurface) Option(intent ProposalIntent) *ProposalOption {
	for i := range s.Options {
		if s.Options[i].Intent == intent {
			return &s.Options[i]
		}
	}
	return nil
}

// Has reports whether an option with the given intent is present.
func (s DecisionSurface) Has(intent ProposalIntent) bool { return s.Option(intent) != nil }

// ProposalModel is the interactive selection state over one DecisionSurface.
// It is a plain value object: no callbacks, no I/O, no filesystem access.
type ProposalModel struct {
	Surface  DecisionSurface
	Selected int
}

// NewProposalModel returns a modal positioned at the first option.
func NewProposalModel(surface DecisionSurface) *ProposalModel {
	return &ProposalModel{Surface: surface}
}

// Init implements the Bubble Tea model lifecycle. The proposal modal is a pure
// value object — it schedules no background command, so Init returns nil. The
// dispatcher calls it when activating the modal so the view component enters
// the Bubble Tea loop through the same contract as any other model.
func (p *ProposalModel) Init() tea.Cmd { return nil }

// OptionCount returns the number of selectable options on the surface.
func (p *ProposalModel) OptionCount() int {
	if p == nil {
		return 0
	}
	return len(p.Surface.Options)
}

// Navigate moves the highlight by delta (-1 up / +1 down), wrapping within the
// option list. It is a pure index mutation — it never executes anything.
func (p *ProposalModel) Navigate(delta int) {
	if p == nil || len(p.Surface.Options) == 0 {
		return
	}
	n := len(p.Surface.Options)
	p.Selected = (p.Selected + delta + n) % n
}

// Reset returns the highlight to the first option.
func (p *ProposalModel) Reset() {
	if p != nil {
		p.Selected = 0
	}
}

// Select returns the ProposalIntent of the currently highlighted option. It is
// a pure value read — no mutation, no execution, no file writes.
func (p *ProposalModel) Select() ProposalIntent {
	if p == nil || p.Selected < 0 || p.Selected >= len(p.Surface.Options) {
		return ProposalCancel
	}
	return p.Surface.Options[p.Selected].Intent
}

// SelectIndex returns the ProposalIntent of the option at the given 0-based
// index, plus whether the index was valid. Digit-key selection routes here.
func (p *ProposalModel) SelectIndex(i int) (ProposalIntent, bool) {
	if p == nil || i < 0 || i >= len(p.Surface.Options) {
		return "", false
	}
	p.Selected = i
	return p.Surface.Options[i].Intent, true
}

// Cancel always returns the ProposalCancel intent. Esc and Ctrl+C route here.
func (p *ProposalModel) Cancel() ProposalIntent {
	return ProposalCancel
}

// HasOption reports whether the surface offers the given intent. It is the
// policy-isolation assertion surface tests use (e.g. $hot must never offer
// ProposalExpandScope).
func (p *ProposalModel) HasOption(intent ProposalIntent) bool {
	if p == nil {
		return false
	}
	return p.Surface.Has(intent)
}

// HandleKey maps a terminal key name onto the proposal flow. It returns the
// ProposalIntent the key selected and whether a selection occurred. It never
// touches the filesystem.
//
//   - "enter"   → selects the highlighted option
//   - "1".."9"  → selects the option at that 1-based index
//   - "esc"     → cancels (ProposalCancel)
//   - any other key → no selection
func (p *ProposalModel) HandleKey(key string) (ProposalIntent, bool) {
	switch strings.ToLower(key) {
	case "enter":
		return p.Select(), true
	case "esc":
		return p.Cancel(), true
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		idx := int(key[0] - '1')
		if intent, ok := p.SelectIndex(idx); ok {
			return intent, true
		}
	}
	return "", false
}

// ResumeProposalFunc applies a human-selected ProposalIntent across the
// RuntimeExecutor boundary. The composition root binds it to the runtime
// autonomy Driver's ResumeWithProposal. A nil func means routing is disabled.
type ResumeProposalFunc func(ctx context.Context, intent ProposalIntent) error

// Route passes a selected ProposalIntent back to the engine for execution
// (Engine.ResumeWithProposal). It is the ONLY mutation route from a proposal
// selection: the modal returns a pure intent, and this helper hands it to the
// engine — it NEVER writes a file or invokes a provider itself. ProposalCancel
// must route to the engine too, so the engine transitions to ABORTED with zero
// spend (the UI never hard-cancels on its own).
func Route(ctx context.Context, resume ResumeProposalFunc, intent ProposalIntent) error {
	if resume == nil {
		return errors.New("tui: proposal routing requires an engine resumer")
	}
	return resume(ctx, intent)
}

// Cancel is the convenience routing for the ProposalCancel intent.
func Cancel(ctx context.Context, resume ResumeProposalFunc) error {
	return Route(ctx, resume, ProposalCancel)
}

// Render draws the framed interactive proposal menu. width is the box width in
// cells; it is clamped to a readable minimum.
func (p *ProposalModel) Render(width int) string {
	if p == nil {
		return ""
	}
	if width < 40 {
		width = 40
	}
	boxWidth := width - 4

	var sb strings.Builder
	sb.WriteString(title("PROPOSAL STRATEGY"))
	sb.WriteString("\n\n")

	target := p.Surface.Target
	if target == "" {
		target = "(no target resolved)"
	}
	fmt.Fprintf(&sb, "  target        : %s\n", target)
	fmt.Fprintf(&sb, "  ast           : %s\n", statusLabel(p.Surface.ASTStatus))
	fmt.Fprintf(&sb, "  external refs : %d\n", p.Surface.ExternalRefsCount)
	if p.Surface.EstimatedTokens > 0 {
		fmt.Fprintf(&sb, "  estimated     : ~%d tokens\n", p.Surface.EstimatedTokens)
	}
	if p.Surface.CurrentBudget > 0 {
		fmt.Fprintf(&sb, "  budget        : %d\n", p.Surface.CurrentBudget)
	}
	sb.WriteString("\n")

	for i, opt := range p.Surface.Options {
		prefix := "  "
		if i == p.Selected {
			prefix = "▶ "
		}
		fmt.Fprintf(&sb, "  %s[%d] %s\n", prefix, i+1, opt.Label)
		if opt.Description != "" {
			fmt.Fprintf(&sb, "      %s\n", opt.Description)
		}
	}

	sb.WriteString(" " + strings.Repeat("─", boxWidth-4) + "\n")
	sb.WriteString(" ↑/↓ navigate · Enter select · 1-9 quick select · Esc cancel\n")

	return box(sb.String(), boxWidth)
}

func statusLabel(s string) string {
	switch s {
	case "corrupt":
		return "corrupt"
	case "valid":
		return "valid"
	default:
		return "unknown"
	}
}

func box(body string, width int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var sb strings.Builder
	sb.WriteString("┌" + strings.Repeat("─", width) + "┐\n")
	for _, ln := range lines {
		pad := width - runeLen(ln)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString("│" + ln + strings.Repeat(" ", pad) + "│\n")
	}
	sb.WriteString("└" + strings.Repeat("─", width) + "┘")
	return sb.String()
}

func runeLen(s string) int {
	return len([]rune(s))
}

func title(s string) string {
	return "◆ " + s
}
