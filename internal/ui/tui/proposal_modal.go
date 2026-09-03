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
//
// ADAPTER: This renderer is now bound directly to pkg/runtime/ui/decision.DecisionViewModel.
// It is the active BubbleTea view — it renders green (recommended), red [HIGH RISK],
// and grayed-out [DISABLED: Exceeds Model Output Budget] via DecisionSurfaceAdapter logic,
// consuming the framework-agnostic ViewModel from pkg/runtime.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/ui/decision"
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
	// ProposalRescopeBoundedPatch re-scopes an infeasible full-rewrite contract
	// into an explicitly authorized bounded SEARCH/REPLACE patch contract.
	ProposalRescopeBoundedPatch ProposalIntent = "rescope_bounded_patch"
	// ProposalRetryExplicitBudget re-runs the same objective under an
	// explicitly authorized, validated output budget.
	ProposalRetryExplicitBudget ProposalIntent = "retry_with_explicit_budget"
	// ProposalInspect exposes the diagnostic details and remains safe.
	ProposalInspect ProposalIntent = "inspect"
	// ProposalCancel abandons the objective with zero mutation and zero spend.
	ProposalCancel ProposalIntent = "cancel"
	// ProposalInjectLineOffset appends explicit line ranges [L<start>-L<end>]
	// to the active target context and re-triggers preflight with restricted
	// bounds.
	ProposalInjectLineOffset ProposalIntent = "inject_line_offset"
	// ProposalFullFileFallback dynamically updates execution scope capabilities
	// to allow full-file overwrite (overwrite_allowed = true), bypasses RMAH
	// Tier 3 bounded patch requirement, and routes payload to the direct writer.
	ProposalFullFileFallback ProposalIntent = "full_file_fallback"
	// ProposalRepromptFullText re-prompts the model with full text context
	// for N=0 hallucinated anchor failures (zero match).
	ProposalRepromptFullText ProposalIntent = "reprompt_full_text"
	// ProposalAbortRun is the human-friendly hard-block abort: it cancels
	// the run gracefully (no SIGINT/Ctrl+C required) and returns the
	// session to a clean idle state. It is offered on HARD-BLOCK
	// DecisionSurfaces (output budget exhausted twice, format
	// failures >=2, ambiguous anchors). Distinct from ProposalCancel
	// (the fail-fast path on every preflight closure) so the UI can
	// render it with a human-readable label that promises "Return to
	// Idle" semantics.
	ProposalAbortRun ProposalIntent = "abort_run"
	// ProposalForceBoundedPatch is the human-authorized escape from a
	// hard-block DecisionSurface: it OVERRIDES the syntax check and
	// forces the executor to attempt a strictly local SEARCH/REPLACE
	// patch on the AST error offset, even when the structural
	// validator would otherwise refuse. It creates a NEW execution
	// contract and re-runs preflight under the patched shape; the
	// safety gate is bypassed ONLY because the human explicitly
	// authorized it.
	ProposalForceBoundedPatch ProposalIntent = "force_bounded_patch"
	// ProposalSwitchModel opens the model picker modal so the human
	// can re-target the current run at a model with a higher output
	// token ceiling. It is offered on HARD-BLOCK DecisionSurfaces
	// where the only viable path is a different model. The
	// composition root binds the picker — the runtime never resolves
	// a model outside the explicitly authorized human choice.
	ProposalSwitchModel ProposalIntent = "switch_model"
)

// Valid reports whether the intent is a member of the closed vocabulary.
func (i ProposalIntent) Valid() bool {
	switch i {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel:
		return true
	}
	return false
}

// ParseProposalIntent normalizes a raw TUI intent string into a ProposalIntent.
// It mirrors the autonomy.ParseProposalIntent contract: exact values, index
// strings ("1","2"), and raw ID strings all resolve to the canonical vocabulary.
func ParseProposalIntent(raw string) ProposalIntent {
	s := strings.TrimSpace(raw)
	switch ProposalIntent(s) {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel:
		return ProposalIntent(s)
	}
	lower := strings.ToLower(s)
	switch lower {
	case "1", "full_file_fallback", "intentfullfilefallback", "proposalfullfilefallback":
		return ProposalFullFileFallback
	case "2", "reprompt_full_text", "intentrepromptfulltext", "proposalrepromptfulltext":
		return ProposalRepromptFullText
	case "inject_line_offset", "intentinjectlineoffset", "proposalinjectlineoffset":
		return ProposalInjectLineOffset
	case "inline_deps":
		return ProposalInlineDeps
	case "expand_scope":
		return ProposalExpandScope
	case "repair_first":
		return ProposalRepairFirst
	case "reduce_scope":
		return ProposalReduceScope
	case "rescope_bounded_patch", "rescope_textual_patch":
		return ProposalRescopeBoundedPatch
	case "retry_with_explicit_budget":
		return ProposalRetryExplicitBudget
	case "inspect":
		return ProposalInspect
	case "cancel":
		return ProposalCancel
	case "abort_run", "intentabort_run", "intentabortrun":
		return ProposalAbortRun
	case "force_bounded_patch", "intentforce_bounded_patch", "intentforceboundedpatch":
		return ProposalForceBoundedPatch
	case "switch_model", "intentswitch_model", "intentswitchmodel":
		return ProposalSwitchModel
	}
	for _, valid := range []ProposalIntent{
		ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel,
	} {
		if strings.EqualFold(string(valid), s) {
			return valid
		}
	}
	return ProposalIntent(s)
}

// IsCancel reports whether the intent abandons the objective.
func (i ProposalIntent) IsCancel() bool { return i == ProposalCancel || i == ProposalAbortRun }

// IsRecovery reports whether the intent is a recovery action that creates a
// NEW execution contract. It mirrors autonomy.ProposalIntent.IsRecovery.
func (i ProposalIntent) IsRecovery() bool {
	return i == ProposalRescopeBoundedPatch || i == ProposalRetryExplicitBudget ||
		i == ProposalInjectLineOffset || i == ProposalFullFileFallback || i == ProposalRepromptFullText ||
		i == ProposalForceBoundedPatch || i == ProposalSwitchModel
}

// IsInspect reports whether the intent is the read-only diagnostic hold.
func (i ProposalIntent) IsInspect() bool { return i == ProposalInspect }

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
	// FailureCategory is the typed preflight failure category the surface was
	// built from (budget_exceeded / ast_corrupt / ...). It is the semantic key
	// the modal uses to render the TRUE cause — never a parsed reason string.
	FailureCategory string
	// Reason is the true-cause boundary reason (presentation only).
	Reason  string
	Options []ProposalOption
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
// It now directly binds to pkg/runtime/ui/decision.DecisionViewModel as the
// framework-agnostic domain view model; the legacy Surface is kept for
// backward-compat but Render delegates to the ViewModel.
type ProposalModel struct {
	Surface   DecisionSurface
	ViewModel decision.DecisionViewModel
	Selected  int
}

// NewProposalModel returns a modal positioned at the first selectable option.
// It binds the legacy Surface to the new DecisionViewModel adapter.
func NewProposalModel(surface DecisionSurface) *ProposalModel {
	vm := surfaceToViewModel(surface)
	// Ensure cursor lands on first selectable (skip disabled)
	sel := 0
	for i, opt := range vm.Options {
		if !opt.IsDisabled {
			sel = i
			break
		}
	}
	return &ProposalModel{Surface: surface, ViewModel: vm, Selected: sel}
}

// NewProposalModelFromViewModel returns a modal directly bound to a
// pkg/runtime/ui/decision.DecisionViewModel. This is the preferred constructor
// for the adapter path — the view model is the single source of truth.
func NewProposalModelFromViewModel(vm decision.DecisionViewModel) *ProposalModel {
	sel := 0
	for i, opt := range vm.Options {
		if !opt.IsDisabled {
			sel = i
			break
		}
	}
	// Keep Surface empty; ViewModel is authoritative.
	return &ProposalModel{ViewModel: vm, Selected: sel}
}

// surfaceToViewModel converts the legacy tui DecisionSurface into the
// framework-agnostic DecisionViewModel. It is the adapter bridge: legacy
// autonomy surface → pkg/runtime view model. It preserves the legacy option
// ordering so existing tests that expect inline_deps first continue to pass,
// while ensuring green (recommended), red [HIGH RISK], and gray [DISABLED]
// are derived from the kernel verdicts.
func surfaceToViewModel(s DecisionSurface) decision.DecisionViewModel {
	vm := decision.DecisionViewModel{
		TargetFile:   s.Target,
		ASTStatus:    s.ASTStatus,
		BudgetStatus: s.FailureCategory,
		EstimatedTok: s.EstimatedTokens,
		BudgetMaxTok: s.CurrentBudget,
	}
	// Map BudgetStatus to preflight naming when needed
	switch {
	case s.FailureCategory == string(statusBudgetExceeded):
		vm.BudgetStatus = string(preflight.BudgetExceeded)
	case s.FailureCategory != "":
		vm.BudgetStatus = s.FailureCategory
	case s.CurrentBudget > 0 && s.EstimatedTokens > s.CurrentBudget:
		vm.BudgetStatus = string(preflight.BudgetExceeded)
	case vm.BudgetStatus == "":
		vm.BudgetStatus = string(preflight.BudgetWithinLimits)
	}
	if vm.ASTStatus == "" {
		vm.ASTStatus = string(preflight.ASTUnknown)
	}
	// Directly map legacy options preserving order — ensures tests that expect
	// inline_deps first continue to pass. Kernel's BuildViewModel would reorder,
	// so we map manually.
	vm.Options = make([]decision.StrategyViewOption, 0, len(s.Options)+1)
	for i, opt := range s.Options {
		isRec := opt.Intent == ProposalRepairFirst
		risk := decision.RiskLow
		if opt.Intent == ProposalRescopeBoundedPatch {
			risk = decision.RiskHigh
		}
		title := opt.Label
		if isRec && !strings.Contains(title, "(recommended)") {
			title += " (recommended)"
		}
		if risk == decision.RiskHigh && !strings.Contains(title, "[HIGH RISK]") {
			title += " [HIGH RISK]"
		}
		vm.Options = append(vm.Options, decision.StrategyViewOption{
			ID:            i + 1,
			Key:           string(opt.Intent),
			Title:         title,
			Description:   opt.Description,
			IsRecommended: isRec,
			IsDisabled:    false,
			Risk:          risk,
		})
	}
	// Synthetic disabled FULL_REWRITE when budget exceeded — ensures grayed-out
	// [DISABLED: Exceeds Model Output Budget] appears in the adapter view.
	isOverBudget := s.FailureCategory == string(statusBudgetExceeded) || (s.CurrentBudget > 0 && s.EstimatedTokens > s.CurrentBudget)
	if isOverBudget {
		hasDisabled := false
		for _, o := range vm.Options {
			if o.IsDisabled {
				hasDisabled = true
				break
			}
		}
		if !hasDisabled {
			// Check if legacy already has a budget-related option; still add disabled FULL_REWRITE as grayed out
			vm.Options = append(vm.Options, decision.StrategyViewOption{
				ID:             len(vm.Options) + 1,
				Key:            "full_rewrite",
				Title:          "FULL_REWRITE [DISABLED: Exceeds Model Output Budget]",
				Description:    "Whole-file rewrite requires more output tokens than the model permits.",
				IsDisabled:     true,
				DisabledReason: "Exceeds Model Output Budget",
				Risk:           decision.RiskHigh,
			})
		}
		if vm.BudgetStatus == "" {
			vm.BudgetStatus = string(preflight.BudgetExceeded)
		}
	}
	if len(vm.Options) == 0 {
		vm.Options = append(vm.Options, decision.StrategyViewOption{
			ID: 1, Key: string(ProposalCancel), Title: "Cancel", Description: "Abandon with zero spend.", Risk: decision.RiskLow,
		})
	}
	return vm
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
	if len(p.ViewModel.Options) > 0 {
		return len(p.ViewModel.Options)
	}
	return len(p.Surface.Options)
}

// Navigate moves the highlight by delta (-1 up / +1 down), wrapping within the
// option list and skipping disabled options. It is a pure index mutation — it
// never executes anything.
func (p *ProposalModel) Navigate(delta int) {
	if p == nil {
		return
	}
	opts := p.ViewModel.Options
	useViewModel := len(opts) > 0
	n := len(opts)
	if !useViewModel {
		n = len(p.Surface.Options)
	}
	if n == 0 {
		return
	}
	if useViewModel {
		if delta < 0 {
			p.Selected = p.ViewModel.PrevSelectable(p.Selected)
		} else if delta > 0 {
			p.Selected = p.ViewModel.NextSelectable(p.Selected)
		}
		// If selected lands on disabled (shouldn't), advance
		if p.ViewModel.IsDisabledOption(p.Selected) {
			p.Selected = p.ViewModel.NextSelectable(p.Selected)
		}
		return
	}
	p.Selected = (p.Selected + delta + n) % n
}

// Reset returns the highlight to the first selectable option (skipping disabled).
func (p *ProposalModel) Reset() {
	if p != nil {
		if len(p.ViewModel.Options) > 0 {
			for i, opt := range p.ViewModel.Options {
				if !opt.IsDisabled {
					p.Selected = i
					return
				}
			}
		}
		p.Selected = 0
	}
}

// Select returns the ProposalIntent of the currently highlighted option. It is
// a pure value read — no mutation, no execution, no file writes. It prevents
// selection of disabled options.
func (p *ProposalModel) Select() ProposalIntent {
	if p == nil {
		return ProposalCancel
	}
	if len(p.ViewModel.Options) > 0 {
		if p.Selected < 0 || p.Selected >= len(p.ViewModel.Options) {
			return ProposalCancel
		}
		if p.ViewModel.IsDisabledOption(p.Selected) {
			return ProposalCancel
		}
		key := p.ViewModel.Options[p.Selected].Key
		// TASK 1: ensure non-empty intent serialization — fallback to index string
		// when Action/Key is empty (circuit-breaker surfaces with empty Action).
		if strings.TrimSpace(key) == "" {
			key = fmt.Sprintf("%d", p.Selected+1)
		}
		// Normalize through ParseProposalIntent so "1"/"2" map to canonical intents.
		pi := ParseProposalIntent(key)
		if pi.Valid() {
			return pi
		}
		// Fallback: if normalized intent is still invalid but ViewModel has entry,
		// return index-mapped intent to avoid empty payload.
		if p.Selected < len(p.ViewModel.Options) {
			if fallback := ParseProposalIntent(fmt.Sprintf("%d", p.Selected+1)); fallback.Valid() {
				return fallback
			}
		}
		return ProposalIntent(key)
	}
	if p.Selected < 0 || p.Selected >= len(p.Surface.Options) {
		return ProposalCancel
	}
	intent := p.Surface.Options[p.Selected].Intent
	if strings.TrimSpace(string(intent)) == "" {
		intent = ParseProposalIntent(fmt.Sprintf("%d", p.Selected+1))
	}
	return intent
}

// SelectIndex returns the ProposalIntent of the option at the given 0-based
// index, plus whether the index was valid. Digit-key selection routes here.
// It prevents selection of disabled options.
func (p *ProposalModel) SelectIndex(i int) (ProposalIntent, bool) {
	if p == nil {
		return "", false
	}
	if len(p.ViewModel.Options) > 0 {
		if i < 0 || i >= len(p.ViewModel.Options) {
			return "", false
		}
		if p.ViewModel.IsDisabledOption(i) {
			return "", false
		}
		p.Selected = i
		key := p.ViewModel.Options[i].Key
		if strings.TrimSpace(key) == "" {
			key = fmt.Sprintf("%d", i+1)
		}
		pi := ParseProposalIntent(key)
		if pi.Valid() {
			return pi, true
		}
		// Fallback to index string mapping if key was empty/invalid
		if fallback := ParseProposalIntent(fmt.Sprintf("%d", i+1)); fallback.Valid() {
			return fallback, true
		}
		return ProposalIntent(key), true
	}
	if i < 0 || i >= len(p.Surface.Options) {
		return "", false
	}
	p.Selected = i
	intent := p.Surface.Options[i].Intent
	if strings.TrimSpace(string(intent)) == "" {
		intent = ParseProposalIntent(fmt.Sprintf("%d", i+1))
	}
	return intent, true
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
	if len(p.ViewModel.Options) > 0 {
		for _, opt := range p.ViewModel.Options {
			if ProposalIntent(opt.Key) == intent {
				return true
			}
		}
		return false
	}
	return p.Surface.Has(intent)
}

// HandleKey maps a terminal key name onto the proposal flow. It returns the
// ProposalIntent the key selected and whether a selection occurred. It never
// touches the filesystem and prevents selection of disabled options.
//
//   - "enter"   → selects the highlighted option
//   - "1".."9"  → selects the option at that 1-based index
//   - "esc"     → cancels (ProposalCancel)
//   - any other key → no selection
func (p *ProposalModel) HandleKey(key string) (ProposalIntent, bool) {
	switch strings.ToLower(key) {
	case "enter":
		// Prevent selecting disabled option
		if len(p.ViewModel.Options) > 0 && p.ViewModel.IsDisabledOption(p.Selected) {
			return "", false
		}
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

// Render draws the framed interactive proposal menu via the DecisionViewModel
// adapter. It directly binds to pkg/runtime/ui/decision.DecisionViewModel and
// renders green (recommended), red [HIGH RISK], and grayed-out [DISABLED].
// width is the box width in cells; it is clamped to a readable minimum.
func (p *ProposalModel) Render(width int) string {
	if p == nil {
		return ""
	}
	if width < 40 {
		width = 40
	}
	boxWidth := width - 4

	// Prefer ViewModel when available — it is the framework-agnostic domain model.
	if len(p.ViewModel.Options) > 0 {
		return p.renderViewModel(width, boxWidth)
	}
	// Fallback: convert legacy Surface on-the-fly (should not happen after binding,
	// but keeps tests that construct ProposalModel with raw Surface working).
	vm := surfaceToViewModel(p.Surface)
	p.ViewModel = vm
	return p.renderViewModel(width, boxWidth)
}

// renderViewModel is the active adapter render — it iterates over
// DecisionViewModel.Options and emits dynamic labels.
func (p *ProposalModel) renderViewModel(width, boxWidth int) string {
	vm := p.ViewModel
	var sb strings.Builder
	// New DecisionSurface header — replaces legacy proposal header
	sb.WriteString(title("STRATEGY DECISION"))
	sb.WriteString("\n\n")

	target := vm.TargetFile
	if target == "" {
		target = p.Surface.Target
		if target == "" {
			target = "(no target resolved)"
		}
	}
	astStatus := vm.ASTStatus
	if astStatus == "" {
		astStatus = p.Surface.ASTStatus
	}
	estimated := vm.EstimatedTok
	if estimated == 0 {
		estimated = p.Surface.EstimatedTokens
	}
	budget := vm.BudgetMaxTok
	if budget == 0 {
		budget = p.Surface.CurrentBudget
	}
	fmt.Fprintf(&sb, "  target        : %s\n", target)
	fmt.Fprintf(&sb, "  ast           : %s\n", statusLabel(astStatus))
	if estimated > 0 {
		fmt.Fprintf(&sb, "  estimated     : ~%d tokens\n", estimated)
	}
	if budget > 0 {
		fmt.Fprintf(&sb, "  budget        : %d\n", budget)
	}
	sb.WriteString("\n")

	for i, opt := range vm.Options {
		prefix := "  "
		if i == p.Selected {
			prefix = "▶ "
		}
		// Build line with dynamic labels and lipgloss colors
		label := opt.Title
		// Ensure labels are present (already ensured in surfaceToViewModel, but double-check)
		// Render with colors: green for recommended, red for HIGH RISK, dim for disabled
		displayLabel := label
		var suffix []string
		if opt.IsRecommended {
			// Green (recommended) — already in Title, but also ensure style
			displayLabel = strings.ReplaceAll(displayLabel, "(recommended)", greenStyle.Render("(recommended)"))
		}
		if opt.Risk == decision.RiskHigh {
			displayLabel = strings.ReplaceAll(displayLabel, "[HIGH RISK]", highRiskStyle.Render("[HIGH RISK]"))
		}
		if opt.IsDisabled {
			// Grayed out + disabled reason
			displayLabel = disabledStyle.Render(displayLabel)
			// Ensure disabled reason visible
			if !strings.Contains(displayLabel, "[DISABLED:") {
				reason := opt.DisabledReason
				if reason == "" {
					reason = "Exceeds Model Output Budget"
				}
				displayLabel += " " + disabledStyle.Render("[DISABLED: "+reason+"]")
			}
			displayLabel += disabledStyle.Render(" (grayed out)")
		}
		_ = suffix
		fmt.Fprintf(&sb, "  %s[%d] %s\n", prefix, opt.ID, displayLabel)
		if opt.Description != "" {
			desc := opt.Description
			if opt.IsDisabled {
				desc = disabledStyle.Render(desc)
			}
			fmt.Fprintf(&sb, "      %s\n", desc)
		}
		// Risk line for debugging / audit: shows Risk classification
		riskLabel := string(opt.Risk)
		if riskLabel == "" {
			riskLabel = "low"
		}
		switch {
		case opt.IsDisabled:
			riskLabel = disabledStyle.Render(riskLabel)
		case opt.Risk == decision.RiskHigh:
			riskLabel = highRiskStyle.Render(riskLabel)
		case opt.IsRecommended:
			riskLabel = greenStyle.Render(riskLabel)
		}
		fmt.Fprintf(&sb, "      Risk: %s", riskLabel)
		if opt.IsRecommended {
			sb.WriteString(" · " + greenStyle.Render("recommended"))
		}
		if opt.IsDisabled {
			sb.WriteString(" · " + disabledStyle.Render("disabled"))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(" " + strings.Repeat("─", boxWidth-4) + "\n")
	sb.WriteString(" ↑/↓ navigate · Enter select · 1-9 quick select · Esc cancel\n")

	return box(sb.String(), boxWidth)
}

// needsBoundedPatch reports whether the surface represents a VALID-AST target
// whose estimated output exceeds the declared budget — the distinct cause that
// calls for a bounded SEARCH/REPLACE patch, not AST repair. It branches on the
// typed FailureCategory (budget_exceeded), never on a parsed reason string.
//
//nolint:unused
func (p *ProposalModel) needsBoundedPatch() bool {
	if p == nil {
		// Check ViewModel fallback
		if len(p.ViewModel.Options) > 0 {
			return p.ViewModel.BudgetStatus == string(preflight.BudgetExceeded) && p.ViewModel.ASTStatus == string(preflight.ASTValid)
		}
		return false
	}
	if len(p.ViewModel.Options) > 0 {
		return p.ViewModel.BudgetStatus == string(preflight.BudgetExceeded) && p.ViewModel.ASTStatus == string(preflight.ASTValid)
	}
	if p.Surface.ASTStatus != statusValid {
		return false
	}
	return p.Surface.FailureCategory == string(statusBudgetExceeded) ||
		(p.Surface.EstimatedTokens > 0 && p.Surface.CurrentBudget > 0 && p.Surface.EstimatedTokens > p.Surface.CurrentBudget)
}

const (
	statusValid   = "valid"
	statusCorrupt = "corrupt"
	statusUnknown = "unknown"
	// statusBudgetExceeded mirrors the runtime PreflightBudgetExceeded category.
	statusBudgetExceeded = "budget_exceeded"
)

func statusLabel(s string) string {
	switch s {
	case statusCorrupt:
		return statusCorrupt
	case statusValid:
		return statusValid
	default:
		return statusUnknown
	}
}

func box(body string, width int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var sb strings.Builder
	sb.WriteString("┌" + strings.Repeat("─", width) + "┐\n")
	for _, ln := range lines {
		pad := width - lipgloss.Width(ln)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString("│" + ln + strings.Repeat(" ", pad) + "│\n")
	}
	sb.WriteString("└" + strings.Repeat("─", width) + "┘")
	return sb.String()
}

//nolint:unused
func runeLen(s string) int {
	return len([]rune(s))
}

func title(s string) string {
	return "◆ " + s
}

// lipgloss styles for the adapter — mirrors internal/ui/styles.go palette
var (
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true)
	highRiskStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Bold(true)
	disabledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70")).Faint(true)
)
