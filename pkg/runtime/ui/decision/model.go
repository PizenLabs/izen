// Package decision — model.go defines the framework-agnostic domain ViewModel
// that bridges pkg/runtime/ into the BubbleTea TUI adapter. It carries only
// plain data — no BubbleTea, no Lipgloss, no filesystem access.
package decision

import (
	"strings"

	"github.com/PizenLabs/izen/pkg/provider/capability"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
)

// OptionRisk is the TUI-facing risk taxonomy exported to the adapter.
// Keeping it string-typed makes it wire-safe and JSON-friendly while still
// distinct from the kernel's RiskLevel int.
type OptionRisk string

const (
	RiskLow  OptionRisk = "LOW"
	RiskHigh OptionRisk = "HIGH"
)

// StrategyViewOption is one selectable entry as seen by the TUI.
// In the adapter View(): green "(recommended)", red "[HIGH RISK]", and
// grayed-out "[DISABLED: <reason>]" are driven entirely from these fields.
type StrategyViewOption struct {
	ID             int
	Key            string
	Title          string
	Description    string
	IsRecommended  bool
	IsDisabled     bool
	DisabledReason string
	Risk           OptionRisk
}

// DecisionViewModel is the pure data contract exported from pkg/runtime/ to
// internal/ui/. The adapter owns presentation (Lipgloss, BubbleTea cursor),
// the kernel owns this data (preflight verdicts, budget gate, AST status).
type DecisionViewModel struct {
	TargetFile   string
	ASTStatus    string
	BudgetStatus string
	EstimatedTok int
	BudgetMaxTok int
	Options      []StrategyViewOption
}

// IsDisabledOption reports whether the option with the given index is disabled.
func (m DecisionViewModel) IsDisabledOption(idx int) bool {
	if idx < 0 || idx >= len(m.Options) {
		return false
	}
	return m.Options[idx].IsDisabled
}

// SelectableIndices returns indices of options that are not disabled.
func (m DecisionViewModel) SelectableIndices() []int {
	out := make([]int, 0, len(m.Options))
	for i, o := range m.Options {
		if !o.IsDisabled {
			out = append(out, i)
		}
	}
	return out
}

// NextSelectable returns the next selectable index after cur, wrapping forward.
// It never lands on a disabled option; if no option is selectable it returns cur.
func (m DecisionViewModel) NextSelectable(cur int) int {
	if len(m.Options) == 0 {
		return cur
	}
	for step := 1; step <= len(m.Options); step++ {
		nxt := (cur + step) % len(m.Options)
		if !m.Options[nxt].IsDisabled {
			return nxt
		}
	}
	return cur
}

// PrevSelectable returns the previous selectable index before cur, wrapping backward.
func (m DecisionViewModel) PrevSelectable(cur int) int {
	if len(m.Options) == 0 {
		return cur
	}
	for step := 1; step <= len(m.Options); step++ {
		nxt := (cur - step + len(m.Options)) % len(m.Options)
		if !m.Options[nxt].IsDisabled {
			return nxt
		}
	}
	return cur
}

// FromSurface converts a kernel decision.Surface into the TUI-facing
// DecisionViewModel. It is the canonical adapter bridge: the kernel decides,
// the ViewModel transports, the TUI renders.
func FromSurface(s Surface) DecisionViewModel {
	vm := DecisionViewModel{
		TargetFile:   s.Target,
		ASTStatus:    string(s.ASTStatus),
		BudgetStatus: string(s.BudgetStatus),
		EstimatedTok: s.EstimatedTokens,
		BudgetMaxTok: s.MaxOutputTokens,
	}
	vm.Options = make([]StrategyViewOption, 0, len(s.Options))
	for i, opt := range s.Options {
		viewOpt := StrategyViewOption{
			ID:            i + 1,
			Key:           opt.ID,
			Title:         opt.Label,
			Description:   opt.Description,
			IsRecommended: opt.Recommended,
			IsDisabled:    opt.Disabled,
			Risk:          riskLevelToOptionRisk(opt.RiskLevel),
		}
		if opt.Disabled {
			viewOpt.DisabledReason = disabledReason(opt)
		}
		vm.Options = append(vm.Options, viewOpt)
	}
	return vm
}

// BuildViewModel is a DI-friendly helper: builds the kernel Surface from the
// preflight verdict and returns the ViewModel. Fetching decision state from
// pkg/runtime/preflight and building the DecisionViewModel is done here — the
// adapter's Init()/Update() call this, never a direct os.ReadFile.
func BuildViewModel(target string, ast preflight.ASTStatus, gate *preflight.BudgetGateResult) DecisionViewModel {
	s := Build(target, ast, gate)
	AnnotateStrategies(&s)
	return FromSurface(s)
}

// BuildViewModelFromState is the Observation-backed builder: given the
// preflight TargetState and model capability ceiling it runs the budget gate
// and returns the ready-to-render ViewModel. The caller passes the already-
// observed snapshot.Content []byte — no filesystem re-read occurs here.
func BuildViewModelFromState(target preflight.TargetState, maxOutputTokens int) DecisionViewModel {
	caps := capability.ModelCapabilities{MaxOutputTokens: maxOutputTokens}
	gateResult := preflight.EvaluateBudgetGate(target, caps)
	return BuildViewModel(target.Path, target.ASTStatus, &gateResult)
}

// disabledReason extracts the human-readable disabled reason from a StrategyOption.
// The kernel annotates FULL_REWRITE as "FULL_REWRITE [DISABLED: Exceeds Model Output Budget]".
func disabledReason(opt StrategyOption) string {
	if !opt.Disabled {
		return ""
	}
	// Prefer parsing the bracketed reason when present.
	if strings.Contains(opt.Label, "[DISABLED:") {
		start := strings.Index(opt.Label, "[DISABLED:")
		end := strings.Index(opt.Label[start:], "]")
		if end >= 0 {
			inner := opt.Label[start+len("[DISABLED:") : start+end]
			inner = strings.TrimSpace(inner)
			if inner != "" {
				return inner
			}
		}
	}
	if strings.Contains(opt.Description, "Model Output Budget") {
		return "Exceeds Model Output Budget"
	}
	return "Disabled"
}

func riskLevelToOptionRisk(r RiskLevel) OptionRisk {
	if r == RiskLevelHigh {
		return RiskHigh
	}
	return RiskLow
}
