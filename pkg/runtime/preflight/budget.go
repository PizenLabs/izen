package preflight

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/provider/capability"
)

// ExecutionStrategy is the mutation strategy an execution cycle runs under. It
// drives both the LLM prompt contract and the token requirement estimate.
type ExecutionStrategy string

// Supported execution strategies.
const (
	// StrategyFullRewrite replaces the whole target file: the model emits the
	// complete post-mutation content. It needs the most output tokens.
	StrategyFullRewrite ExecutionStrategy = "FULL_REWRITE"
	// StrategyBoundedPatch emits a localized search/replace patch that
	// preserves the surrounding structure. It needs a fraction of the output
	// budget of a full rewrite.
	StrategyBoundedPatch ExecutionStrategy = "BOUNDED_PATCH"
)

// Valid reports whether s is a canonical execution strategy.
func (s ExecutionStrategy) Valid() bool {
	switch s {
	case StrategyFullRewrite, StrategyBoundedPatch:
		return true
	default:
		return false
	}
}

// String returns the machine-readable strategy label.
func (s ExecutionStrategy) String() string { return string(s) }

// DecisionChoiceID identifies one selectable choice on a DecisionSurface.
type DecisionChoiceID string

// Decision surface choices.
const (
	// ChoiceApplyRecommendation accepts the advisor's proposal: the strategy
	// is rescoped to BOUNDED_PATCH and effort/token limits are set optimally.
	ChoiceApplyRecommendation DecisionChoiceID = "apply_recommendation"
	// ChoiceForceCurrentSettings preserves the user's explicit intent and
	// proceeds with the original strategy and budget despite overflow risk.
	ChoiceForceCurrentSettings DecisionChoiceID = "force_current_settings"
)

// Token estimation constants.
const (
	// bytesPerToken is the heuristic compression ratio for file size → token
	// count (~4 bytes per token for code).
	bytesPerToken = 4
	// baseOutputTokens is the fixed prompt/wrapper headroom added to every
	// required-token estimate.
	baseOutputTokens = 256
	// fullRewriteFactor scales the file token count for a FULL_REWRITE (the
	// model re-emits the entire file plus its reasoning overhead).
	fullRewriteFactor = 1.25
	// boundedPatchFactor scales the file token count for a BOUNDED_PATCH (the
	// model emits only the changed regions).
	boundedPatchFactor = 0.35
)

// BudgetAdvisor is the deterministic adaptive preflight budget engine. It
// estimates the output tokens a given strategy needs for a target file and
// constructs a DecisionSurface proposal — it never fails execution. When the
// requested strategy overflows the model's max output budget the advisor
// recommends rescoping to BOUNDED_PATCH; the caller decides on the surface.
type BudgetAdvisor struct{}

// NewBudgetAdvisor returns a stateless budget advisor.
func NewBudgetAdvisor() *BudgetAdvisor { return &BudgetAdvisor{} }

// EstimateFileTokens converts a raw byte count into an approximate token count
// using the code heuristic of ~4 bytes per token. It always returns at least
// one token for a non-empty payload.
func (a *BudgetAdvisor) EstimateFileTokens(fileSizeBytes int) int {
	if fileSizeBytes <= 0 {
		return 0
	}
	tokens := fileSizeBytes / bytesPerToken
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// EstimateRequiredTokens computes the model output tokens required to mutate a
// file of fileSizeTokens tokens under the given strategy:
//
//	FULL_REWRITE:  FileSizeInTokens × 1.25 + 256
//	BOUNDED_PATCH: FileSizeInTokens × 0.35 + 256
func (a *BudgetAdvisor) EstimateRequiredTokens(strategy ExecutionStrategy, fileSizeTokens int) int {
	switch strategy {
	case StrategyBoundedPatch:
		return int(float64(fileSizeTokens)*boundedPatchFactor) + baseOutputTokens
	default: // StrategyFullRewrite and unknown strategies default to the rewrite contract.
		return int(float64(fileSizeTokens)*fullRewriteFactor) + baseOutputTokens
	}
}

// Advise evaluates a BudgetAdvisoryRequest against the model's max output
// budget. It always returns an advice value; when the requested strategy fits,
// Overflow is false and no surface is built. When it does not fit, a
// Recommendation and a DecisionSurface are populated so the caller can present
// the two choices without failing the cycle.
func (a *BudgetAdvisor) Advise(req BudgetAdvisoryRequest) BudgetAdvice {
	if req.Strategy == "" {
		req.Strategy = StrategyFullRewrite
	}
	if req.Effort == "" {
		req.Effort = capability.EffortHigh
	}
	fileTokens := a.EstimateFileTokens(req.FileSizeBytes)
	required := a.EstimateRequiredTokens(req.Strategy, fileTokens)

	advice := BudgetAdvice{
		Requested:       req.Strategy,
		RequiredTokens:  required,
		FileTokens:      fileTokens,
		MaxOutputTokens: req.MaxOutputTokens,
		OriginalEffort:  req.Effort,
	}

	if req.MaxOutputTokens > 0 && required > req.MaxOutputTokens {
		advice.Overflow = true
		bounded := a.EstimateRequiredTokens(StrategyBoundedPatch, fileTokens)
		rec := Recommendation{
			Strategy:        StrategyBoundedPatch,
			Effort:          capability.EffortHigh,
			EstimatedOutput: bounded,
			MaxTokens:       bounded,
		}
		rec.FitsBudget = bounded <= req.MaxOutputTokens
		advice.Recommendation = &rec
		advice.Surface = buildSurface(req, fileTokens, required, rec)
	}
	return advice
}

// BudgetAdvisoryRequest is the input to the budget advisor: the resolved
// target identity and size, the requested execution strategy, and the selected
// model's output capability.
type BudgetAdvisoryRequest struct {
	// TargetFile is the resolved canonical target path, used only for
	// presentation on the decision surface.
	TargetFile string
	// FileSizeBytes is the target file's size in bytes.
	FileSizeBytes int
	// Strategy is the user-requested execution strategy.
	Strategy ExecutionStrategy
	// MaxOutputTokens is the selected model's maximum output token budget.
	MaxOutputTokens int
	// ModelDisplayName is the human-readable model label for the surface.
	ModelDisplayName string
	// Effort is the user's current effort selection (kept when forcing).
	Effort capability.EffortLevel
}

// BudgetAdvice is the deterministic outcome of an advisory evaluation. When
// Overflow is true the caller must present the DecisionSurface; execution is
// never aborted by the advisor itself.
type BudgetAdvice struct {
	// Requested is the strategy the user asked for.
	Requested ExecutionStrategy
	// RequiredTokens is the estimated output tokens for the requested strategy.
	RequiredTokens int
	// FileTokens is the estimated token count of the target file.
	FileTokens int
	// MaxOutputTokens is the model's advertised output budget.
	MaxOutputTokens int
	// OriginalEffort is the user's current effort selection.
	OriginalEffort capability.EffortLevel
	// Overflow reports whether the requested strategy exceeds the model's max
	// output budget.
	Overflow bool
	// Recommendation is the proposed rescope (non-nil only when Overflow).
	Recommendation *Recommendation
	// Surface is the decision surface proposal (non-nil only when Overflow).
	Surface *DecisionSurface
}

// Recommendation is the advisor's proposed execution configuration. It rescopes
// the strategy to BOUNDED_PATCH and selects the optimal effort and token limits
// so the estimated output fits the model budget.
type Recommendation struct {
	// Strategy is always BOUNDED_PATCH.
	Strategy ExecutionStrategy
	// Effort is the optimal effort level for the rescaled strategy.
	Effort capability.EffortLevel
	// EstimatedOutput is the estimated output tokens under the rescope.
	EstimatedOutput int
	// MaxTokens is the output limit to enforce for the rescope.
	MaxTokens int
	// FitsBudget reports whether the estimated output fits the model budget.
	FitsBudget bool
}

// DecisionChoice is one selectable entry on the decision surface. It carries
// only presentation data plus its semantic ID — no callbacks.
type DecisionChoice struct {
	// ID is the machine-readable choice identifier.
	ID DecisionChoiceID
	// Label is the human-readable option title.
	Label string
	// Description elaborates the option's consequence.
	Description string
	// Recommended marks the advisor-recommended option.
	Recommended bool
}

// DecisionSurface is the pure data proposal rendered when a budget overflow is
// detected. It presents the advisory facts and exactly two choices: apply the
// recommendation or force the current settings.
type DecisionSurface struct {
	// Target is the resolved target file path.
	Target string
	// TargetSizeBytes is the target file size in bytes.
	TargetSizeBytes int
	// TargetSizeTokens is the target file size in tokens.
	TargetSizeTokens int
	// ModelMaxOutput is the model's maximum output token budget.
	ModelMaxOutput int
	// ModelDisplayName is the model's display label.
	ModelDisplayName string
	// RequestedStrategy is the user's original strategy.
	RequestedStrategy ExecutionStrategy
	// RequiredTokens is the estimated output for the requested strategy.
	RequiredTokens int
	// Recommended is the proposed rescope.
	Recommended Recommendation
	// Choices are the selectable options.
	Choices []DecisionChoice
}

// Choice returns the choice with the given ID, or nil.
func (s *DecisionSurface) Choice(id DecisionChoiceID) *DecisionChoice {
	if s == nil {
		return nil
	}
	for i := range s.Choices {
		if s.Choices[i].ID == id {
			return &s.Choices[i]
		}
	}
	return nil
}

// Has reports whether the surface offers the given choice.
func (s *DecisionSurface) Has(id DecisionChoiceID) bool { return s.Choice(id) != nil }

// ExecutionConfig is the effective execution parameters after a decision is
// made on the surface.
type ExecutionConfig struct {
	// Strategy is the strategy to execute under.
	Strategy ExecutionStrategy
	// Effort is the effort level to dispatch with.
	Effort capability.EffortLevel
	// MaxOutputTokens is the output token limit to enforce.
	MaxOutputTokens int
}

// Resolve applies the chosen decision to the original user configuration.
// Selecting ChoiceApplyRecommendation yields the recommended rescope; any other
// choice (including forcing current settings) preserves the original config
// verbatim, honoring the user's explicit intent despite overflow risk.
func (s *DecisionSurface) Resolve(choice DecisionChoiceID, original ExecutionConfig) ExecutionConfig {
	if s == nil {
		return original
	}
	switch choice {
	case ChoiceApplyRecommendation:
		return ExecutionConfig{
			Strategy:        s.Recommended.Strategy,
			Effort:          s.Recommended.Effort,
			MaxOutputTokens: s.Recommended.MaxTokens,
		}
	default:
		return original
	}
}

// buildSurface assembles the two-choice decision surface for an overflow.
func buildSurface(req BudgetAdvisoryRequest, fileTokens, required int, rec Recommendation) *DecisionSurface {
	fits := "Fits model budget"
	if !rec.FitsBudget {
		fits = "May still exceed model budget"
	}
	return &DecisionSurface{
		Target:            req.TargetFile,
		TargetSizeBytes:   req.FileSizeBytes,
		TargetSizeTokens:  fileTokens,
		ModelMaxOutput:    req.MaxOutputTokens,
		ModelDisplayName:  req.ModelDisplayName,
		RequestedStrategy: req.Strategy,
		RequiredTokens:    required,
		Recommended:       rec,
		Choices: []DecisionChoice{
			{
				ID:          ChoiceApplyRecommendation,
				Label:       "Apply Recommendation (Recommended)",
				Recommended: true,
				Description: fmt.Sprintf("Switch strategy to %s and set effort to %s. Estimated output: ~%d tokens (%s).",
					strings.ToUpper(string(rec.Strategy)), strings.ToUpper(string(rec.Effort)), rec.EstimatedOutput, fits),
			},
			{
				ID:    ChoiceForceCurrentSettings,
				Label: "Skip / Force Current Settings",
				Description: fmt.Sprintf("Proceed with %s using %s max tokens. High risk of truncation (OUTPUT_EXHAUSTED).",
					req.Strategy, formatInt(req.MaxOutputTokens)),
			},
		},
	}
}

// Render draws the framed budget & effort advisory. It mirrors the headless
// decision-surface presentation contract:
//
//	◆ BUDGET & EFFORT ADVISORY — [index.html] requires parameters adjustment
//	...
//	► [1] Apply Recommendation (Recommended)
//	[2] Skip / Force Current Settings
//	↑/↓ navigate · Enter select · Esc cancel
//
// Render is pure presentation: it never reads the workspace and never mutates
// state.
func (s *DecisionSurface) Render(width int) string {
	if s == nil {
		return ""
	}
	if width < 52 {
		width = 52
	}
	boxWidth := width - 4

	target := s.Target
	if target == "" {
		target = "(target not resolved)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "◆ BUDGET & EFFORT ADVISORY — [%s] requires parameters adjustment", target)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Current Target Size : %s bytes (~%s tokens)",
		formatInt(s.TargetSizeBytes), formatInt(s.TargetSizeTokens))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Model Max Output    : %s tokens (%s)",
		formatInt(s.ModelMaxOutput), displayName(s.ModelDisplayName))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Requested Strategy  : %s (Requires ~%s output tokens)",
		s.RequestedStrategy, formatInt(s.RequiredTokens))
	b.WriteString("\n\n")

	for i, choice := range s.Choices {
		marker := "  "
		if i == 0 {
			marker = "► "
		}
		fmt.Fprintf(&b, "%s[%d] %s\n", marker, i+1, choice.Label)
		fmt.Fprintf(&b, "   %s\n", choice.Description)
		if i == 0 {
			b.WriteString("\n")
		}
	}

	b.WriteString("↑/↓ navigate · Enter select · Esc cancel")

	return boxString(b.String(), boxWidth)
}

// displayName renders the model label, falling back to "(unknown model)".
func displayName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "unknown model"
	}
	return strings.TrimSpace(name)
}

// formatInt renders a non-negative integer with thousands separators, e.g.
// 7780 → "7,780". The zero value renders as "0".
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
