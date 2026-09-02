// Package decision implements the pure-presentation strategy option surface
// rendered when preflight closes the hard gate. It annotates every option with
// an explicit risk hierarchy: the safest path is marked "(recommended)", escape
// hatches are marked "[HIGH RISK]", and strategies that are provably infeasible
// under the model's output budget are disabled with the
// "[DISABLED: Exceeds Model Output Budget]" label. It is presentation-only: it
// never reads the workspace, never invokes a provider, and never mutates state.
package decision

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/preflight"
)

// RiskLevel classifies how risky a strategy option is.
type RiskLevel int

const (
	// RiskLow marks the safest path: structural repair with validation.
	RiskLow RiskLevel = iota
	// RiskHigh marks an escape hatch that bypasses a safety gate.
	RiskHigh
)

// String returns a stable lowercase label for the risk level.
func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// StrategyOption is one selectable entry on the annotated surface. It carries
// only presentation + semantic-ID data — no callbacks.
type StrategyOption struct {
	// ID is the machine-readable option identifier.
	ID string
	// Label is the human-readable option title, including the dynamic
	// "(recommended)" / "[HIGH RISK]" / "[DISABLED: ...]" annotations.
	Label string
	// Description elaborates the option's consequence.
	Description string
	// RiskLevel is the explicit risk classification of the option.
	RiskLevel RiskLevel
	// Recommended marks the advisor-recommended (safest) option.
	Recommended bool
	// Disabled marks an option that must not be selectable (grayed out).
	Disabled bool
}

// Strategy option identifiers.
const (
	// OptionRepairAST is the recommended structural repair path.
	OptionRepairAST = "repair_ast_first"
	// OptionBoundedSearchReplace is the high-risk textual escape hatch.
	OptionBoundedSearchReplace = "bounded_search_replace"
	// OptionChunkedRepair is the budget-gate fallback strategy.
	OptionChunkedRepair = "chunked_repair"
	// OptionFullRewrite is the whole-file strategy (disabled when over budget).
	OptionFullRewrite = "full_rewrite"
	// OptionModelSwitch is required when no strategy fits the current model.
	OptionModelSwitch = "model_switch"
	// OptionCancel abandons the objective with zero spend.
	OptionCancel = "cancel"
)

// Surface is the annotated strategy option surface. It is plain data for the
// TUI to render and collapse into one strategy decision.
type Surface struct {
	// Target is the resolved canonical target path.
	Target string
	// ASTStatus is the deterministic structural verdict of the target.
	ASTStatus preflight.ASTStatus
	// BudgetStatus classifies feasibility of the requested strategy.
	BudgetStatus preflight.BudgetStatus
	// EstimatedTokens is the estimated output of the requested strategy.
	EstimatedTokens int
	// MaxOutputTokens is the selected model's maximum output budget.
	MaxOutputTokens int
	// Options are the annotated, dynamically risk-ordered selectable entries.
	Options []StrategyOption
}

// Option returns the option with the given ID, or nil.
func (s Surface) Option(id string) *StrategyOption {
	for i := range s.Options {
		if s.Options[i].ID == id {
			return &s.Options[i]
		}
	}
	return nil
}

// Has reports whether the surface offers the option with the given ID.
func (s Surface) Has(id string) bool { return s.Option(id) != nil }

// NewSurface is the DI-friendly constructor for the DecisionSurface.
// It delegates to Build and ensures AnnotateStrategies is applied.
func NewSurface(target string, ast preflight.ASTStatus, gate *preflight.BudgetGateResult) Surface {
	s := Build(target, ast, gate)
	AnnotateStrategies(&s)
	return s
}

// AnnotateStrategies applies the explicit risk hierarchy annotations to every
// option on the surface. It is idempotent and may be called after manual
// surface construction.
func AnnotateStrategies(s *Surface) {
	if s == nil {
		return
	}
	for i := range s.Options {
		opt := &s.Options[i]
		// Ensure risk annotations are consistent with the hierarchy invariant:
		// safest path (repair) is Low, escape hatches are High.
		switch opt.ID {
		case OptionRepairAST:
			if !opt.Recommended {
				opt.Label = ensureRecommended(opt.Label)
				opt.Recommended = true
			}
			opt.RiskLevel = RiskLow
		case OptionBoundedSearchReplace:
			if !strings.Contains(opt.Label, "[HIGH RISK]") {
				opt.Label += " [HIGH RISK]"
			}
			if opt.Description == "" {
				opt.Description = "Bypasses AST validation. May introduce syntax errors."
			}
			opt.RiskLevel = RiskHigh
		case OptionFullRewrite:
			if opt.Disabled && !strings.Contains(opt.Label, "[DISABLED: Exceeds Model Output Budget]") {
				opt.Label = "FULL_REWRITE [DISABLED: Exceeds Model Output Budget]"
			}
			if opt.Disabled {
				opt.RiskLevel = RiskHigh
			}
		}
	}
}

func ensureRecommended(label string) string {
	if strings.Contains(label, "(recommended)") {
		return label
	}
	return label + " (recommended)"
}

// Build assembles the annotated strategy surface from the preflight verdict.
// The risk hierarchy is explicit and dynamic:
//
//   - When ASTStatus == ASTCorrupt, "Repair AST first" is marked
//     "(recommended)" at RiskLow, and "Bounded textual SEARCH/REPLACE" is
//     marked "[HIGH RISK]" at RiskHigh with the syntax-corruption warning.
//   - When BudgetStatus == BudgetExceeded, FULL_REWRITE is disabled and labeled
//     "[DISABLED: Exceeds Model Output Budget]"; CHUNKED_REPAIR is proposed
//     (recommended when it fits the budget), and a model switch is surfaced as
//     the required path when no strategy fits.
//
// gate may be nil when only the AST hierarchy is relevant; budget annotations
// are then omitted.
func Build(target string, ast preflight.ASTStatus, gate *preflight.BudgetGateResult) Surface {
	s := Surface{Target: target, ASTStatus: ast}
	if gate != nil {
		s.BudgetStatus = gate.BudgetStatus
		s.EstimatedTokens = gate.EstimatedTokens
		s.MaxOutputTokens = gate.MaxOutputTokens
	}

	if ast == preflight.ASTCorrupt {
		s.Options = append(s.Options,
			StrategyOption{
				ID:          OptionRepairAST,
				Label:       "Repair AST first (recommended)",
				Description: "Repair the structurally corrupt target before mutating.",
				RiskLevel:   RiskLow,
				Recommended: true,
			},
			StrategyOption{
				ID:          OptionBoundedSearchReplace,
				Label:       "Bounded textual SEARCH/REPLACE [HIGH RISK]",
				Description: "Bypasses AST validation. May introduce syntax errors.",
				RiskLevel:   RiskHigh,
			},
		)
	}

	if gate != nil && gate.BudgetStatus == preflight.BudgetExceeded {
		chunked := StrategyOption{
			ID:          OptionChunkedRepair,
			Label:       "ChunkedRepair",
			Description: "Mutate the target in bounded sequential chunks to fit the model output budget.",
			RiskLevel:   RiskLow,
			Recommended: true,
			Disabled:    !gate.ChunkedRepairAvailable,
		}
		if gate.ChunkedRepairAvailable {
			chunked.Label += " (recommended)"
			chunked.Description = fmt.Sprintf(
				"Mutate in bounded chunks — estimated ~%d output tokens, fits the %d-token model budget.",
				gate.ChunkedRepairEstimatedTokens, gate.MaxOutputTokens)
		}
		s.Options = append(s.Options,
			chunked,
			StrategyOption{
				ID:          OptionFullRewrite,
				Label:       "FULL_REWRITE [DISABLED: Exceeds Model Output Budget]",
				Description: "Whole-file rewrite requires more output tokens than the model permits.",
				RiskLevel:   RiskHigh,
				Disabled:    true,
			},
		)
		if gate.RequiresModelSwitch {
			s.Options = append(s.Options, StrategyOption{
				ID:          OptionModelSwitch,
				Label:       "Switch model [REQUIRED]",
				Description: "No strategy fits the current model's output budget. Select a model with a larger maximum output.",
				RiskLevel:   RiskLow,
				Recommended: false,
			})
		}
	}

	s.Options = append(s.Options, StrategyOption{
		ID:          OptionCancel,
		Label:       "Cancel",
		Description: "Abandon the objective with zero mutation and zero spend.",
		RiskLevel:   RiskLow,
	})
	return s
}

// Render draws the framed annotated surface. It is pure presentation: it never
// reads the workspace and never mutates state.
func (s Surface) Render(width int) string {
	if width < 52 {
		width = 52
	}
	boxWidth := width - 4

	target := s.Target
	if target == "" {
		target = "(target not resolved)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "◆ STRATEGY DECISION — [%s]", target)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "AST Status    : %s", displayAST(s.ASTStatus))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Budget Status : %s", displayBudget(s.BudgetStatus))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Estimated     : ~%s output tokens", formatInt(s.EstimatedTokens))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Model Max Out : %s tokens", formatInt(s.MaxOutputTokens))
	b.WriteString("\n\n")

	for i, opt := range s.Options {
		marker := "  "
		if i == 0 {
			marker = "► "
		}
		state := ""
		if opt.Disabled {
			state = " (grayed out)"
		}
		fmt.Fprintf(&b, "%s[%d] %s%s\n", marker, i+1, opt.Label, state)
		fmt.Fprintf(&b, "   %s\n", opt.Description)
		fmt.Fprintf(&b, "   Risk: %s", opt.RiskLevel)
		if opt.Recommended {
			b.WriteString(" · recommended")
		}
		b.WriteString("\n")
	}

	b.WriteString("↑/↓ navigate · Enter select · Esc cancel")

	return boxString(b.String(), boxWidth)
}

// displayAST renders the AST status, falling back to "unverified".
func displayAST(s preflight.ASTStatus) string {
	switch s {
	case preflight.ASTValid:
		return "valid"
	case preflight.ASTCorrupt:
		return "corrupt"
	default:
		return "unverified"
	}
}

// displayBudget renders the budget status, falling back to "unknown".
func displayBudget(s preflight.BudgetStatus) string {
	switch s {
	case preflight.BudgetWithinLimits:
		return "within_limits"
	case preflight.BudgetExceeded:
		return "exceeded"
	default:
		return "unknown"
	}
}

// formatInt renders a non-negative integer with thousands separators.
func formatInt(n int) string {
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

// boxString wraps body in a lightweight ┌─┐ frame with the given inner width.
func boxString(body string, width int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", width) + "┐\n")
	for _, ln := range lines {
		pad := width - runeLen(ln)
		if pad < 0 {
			pad = 0
		}
		b.WriteString("│" + ln + strings.Repeat(" ", pad) + "│\n")
	}
	b.WriteString("└" + strings.Repeat("─", width) + "┘")
	return b.String()
}

// runeLen counts runes for box padding.
func runeLen(s string) int { return len([]rune(s)) }
